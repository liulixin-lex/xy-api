package channelrouting

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

const (
	activeProbeOperationClaimLease  = time.Minute
	activeProbeOperationMaxAttempts = 8
	activeProbeOperationMaxBackoff  = 5 * time.Minute
)

type ActiveProbeOperationResult struct {
	Enabled bool             `json:"enabled"`
	Stats   ActiveProbeStats `json:"stats"`
}

func RunActiveProbeOperationCycleContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
) error {
	return runActiveProbeOperationCycleContext(
		ctx, setting, NewActiveProbeScheduler(), activeProbeOperationClaimLease,
	)
}

func runActiveProbeOperationCycleContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
	scheduler *ActiveProbeScheduler,
	claimLease time.Duration,
) error {
	return runActiveProbeOperationCycleWithHeartbeatContext(
		ctx, setting, scheduler, claimLease, nil, nil,
	)
}

func runActiveProbeOperationCycleWithHeartbeatContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
	scheduler *ActiveProbeScheduler,
	claimLease time.Duration,
	heartbeatTicks <-chan time.Time,
	heartbeatResults chan<- error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if scheduler == nil || claimLease <= 0 || claimLease > 5*time.Minute {
		return model.ErrRoutingOperationInvalid
	}
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return err
	}
	operation, err := model.ClaimRoutingOperationContext(
		ctx, model.RoutingOperationTypeActiveProbe, nowMs, claimLease.Milliseconds(),
	)
	if err != nil || operation == nil {
		return err
	}

	head, err := model.GetRoutingPolicyHeadContext(ctx)
	if err != nil {
		return errors.Join(err, transitionFailedActiveProbeOperationContext(ctx, *operation, err))
	}
	if operation.ExpectedRevision != head.CurrentRevision ||
		operation.ExpectedActivationID != head.CurrentActivationID {
		return model.SupersedeRoutingOperationContext(
			ctx, operation.ID, operation.ClaimToken, nowMs, "routing policy changed before active probe execution",
		)
	}
	if !activeProbeEnabled(setting) {
		return model.SupersedeRoutingOperationContext(
			ctx, operation.ID, operation.ClaimToken, nowMs, "active probe disabled before execution",
		)
	}

	operationCtx, cancelOperation := context.WithCancel(ctx)
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	heartbeatInterval := claimLease / 3
	if heartbeatInterval < 10*time.Millisecond {
		heartbeatInterval = 10 * time.Millisecond
	}
	var heartbeatTicker *time.Ticker
	if heartbeatTicks == nil {
		heartbeatTicker = time.NewTicker(heartbeatInterval)
		heartbeatTicks = heartbeatTicker.C
	}
	go func() {
		defer close(heartbeatDone)
		if heartbeatTicker != nil {
			defer heartbeatTicker.Stop()
		}
		for {
			select {
			case <-heartbeatStop:
				return
			case <-operationCtx.Done():
				return
			case _, open := <-heartbeatTicks:
				if !open {
					return
				}
				renewedAtMs, clockErr := model.RoutingEndpointDatabaseNowMsContext(operationCtx)
				if clockErr == nil {
					clockErr = model.RenewRoutingOperationClaimContext(
						operationCtx, operation.ID, operation.ClaimToken, renewedAtMs, claimLease.Milliseconds(),
					)
				}
				if heartbeatResults != nil {
					select {
					case heartbeatResults <- clockErr:
					default:
					}
				}
				if clockErr != nil {
					select {
					case heartbeatErr <- clockErr:
					default:
					}
					cancelOperation()
					return
				}
			}
		}
	}()

	probeErr := scheduler.RunCycle(operationCtx, setting)
	close(heartbeatStop)
	<-heartbeatDone
	cancelOperation()
	select {
	case err = <-heartbeatErr:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if probeErr != nil {
		return errors.Join(probeErr, transitionFailedActiveProbeOperationContext(ctx, *operation, probeErr))
	}

	finishedAtMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return err
	}
	result := ActiveProbeOperationResult{Enabled: activeProbeEnabled(setting), Stats: scheduler.Stats()}
	if err := model.SucceedRoutingOperationWithPayloadContext(
		ctx, operation.ID, operation.ClaimToken, finishedAtMs, result,
	); err != nil {
		return err
	}
	if _, err := PublishRoutingEvent(RoutingEventTypeProbeCompleted, uint64(operation.ExpectedRevision), map[string]any{
		"operation_id": operation.ID,
		"enabled":      result.Enabled,
		"executed":     result.Stats.Executed,
		"succeeded":    result.Stats.Succeeded,
		"failed":       result.Stats.Failed,
	}); err != nil {
		common.SysError("publish active probe completion event: " + common.SanitizeErrorMessage(err.Error()))
	}
	return nil
}

func transitionFailedActiveProbeOperationContext(
	ctx context.Context,
	operation model.RoutingOperation,
	operationErr error,
) error {
	if operationErr == nil || errors.Is(operationErr, context.Canceled) ||
		errors.Is(operationErr, context.DeadlineExceeded) ||
		errors.Is(operationErr, model.ErrRoutingOperationClaimLost) {
		return nil
	}
	message := common.SanitizeErrorMessage(operationErr.Error())
	if message == "" {
		message = "active probe execution failed"
	}
	safeErr := errors.New(message)
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return err
	}
	if operation.Attempts >= activeProbeOperationMaxAttempts {
		return model.FailRoutingOperationContext(
			ctx, operation.ID, operation.ClaimToken, nowMs, safeErr,
		)
	}
	delay := time.Second
	for attempt := 1; attempt < operation.Attempts && delay < activeProbeOperationMaxBackoff; attempt++ {
		if delay > activeProbeOperationMaxBackoff/2 {
			delay = activeProbeOperationMaxBackoff
			break
		}
		delay *= 2
	}
	if nowMs > math.MaxInt64-delay.Milliseconds() {
		return model.ErrRoutingOperationInvalid
	}
	return model.RetryRoutingOperationContext(
		ctx, operation.ID, operation.ClaimToken, nowMs, nowMs+delay.Milliseconds(), safeErr,
	)
}
