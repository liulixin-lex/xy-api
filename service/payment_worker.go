package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	paymentTaskPollInterval      = 2 * time.Second
	paymentTaskLeaseDuration     = 45 * time.Second
	paymentProviderCallTimeout   = 25 * time.Second
	paymentTaskClaimBatchSize    = 8
	paymentReconcileInitialDelay = 10 * time.Second
	paymentReconcileMaximumDelay = 60 * time.Second
	paymentMaintenanceInterval   = 30 * time.Second
	paymentMaintenanceBatchSize  = 200
	paymentCreateRecoveryMisses  = 2
)

var (
	paymentTaskRunnerOnce sync.Once
	paymentTaskWakeup     = make(chan struct{}, 1)
)

func notifyPaymentTaskRunner() {
	select {
	case paymentTaskWakeup <- struct{}{}:
	default:
	}
}

// StartPaymentTaskRunner starts a worker on every application node. Database
// leases and creation fencing, rather than a fixed master process, decide who
// may execute each provider operation.
func StartPaymentTaskRunner() {
	paymentTaskRunnerOnce.Do(func() {
		runnerID := fmt.Sprintf("%s-payment-%s", common.NodeName, common.GetRandomString(8))
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf("payment task runner started: runner=%s", runnerID))
			ticker := time.NewTicker(paymentTaskPollInterval)
			defer ticker.Stop()
			runPaymentTaskClaimPass(runnerID)
			for {
				select {
				case <-ticker.C:
				case <-paymentTaskWakeup:
				}
				runPaymentTaskClaimPass(runnerID)
			}
		})
	})
}

func runPaymentTaskClaimPass(runnerID string) {
	if err := EnsurePaymentClusterReady(); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf(
			"payment task claim skipped because cluster is not ready: code=%s",
			PaymentClusterReadinessCode(err),
		))
		return
	}
	if _, err := model.EnsurePaymentMaintenanceTask(); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("payment maintenance task initialization failed: %v", err))
		return
	}
	tasks, err := model.ClaimDuePaymentTasks(context.Background(), runnerID, common.GetTimestamp(), paymentTaskLeaseDuration, paymentTaskClaimBatchSize)
	if err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("payment task claim failed: %v", err))
		return
	}
	for _, task := range tasks {
		claimedTask := task
		gopool.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), paymentProviderCallTimeout)
			defer cancel()
			if err := runPaymentTask(ctx, claimedTask, runnerID); err != nil &&
				!errors.Is(err, model.ErrPaymentTaskLeaseLost) {
				logger.LogWarn(context.Background(), fmt.Sprintf(
					"payment task failed task_id=%s operation=%s order_id=%d error=%q",
					claimedTask.TaskID, claimedTask.Operation, claimedTask.PaymentOrderID, err.Error(),
				))
			}
		})
	}
}

func runPaymentTask(ctx context.Context, task *model.PaymentTask, runnerID string) error {
	if task == nil {
		return errors.New("payment task is required")
	}
	if err := EnsurePaymentClusterReady(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "cluster_not_ready", err)
	}
	switch task.Operation {
	case model.PaymentTaskOperationCreate:
		return runPaymentCreateTask(ctx, task, runnerID)
	case model.PaymentTaskOperationReconcile:
		return runPaymentReconcileTask(ctx, task, runnerID)
	case model.PaymentTaskOperationMaintenance:
		return runPaymentMaintenanceTask(ctx, task, runnerID)
	default:
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusFailed,
			"unsupported_operation", "unsupported payment task operation")
	}
}

func runPaymentCreateTask(ctx context.Context, task *model.PaymentTask, runnerID string) error {
	order, err := model.GetPaymentOrderByID(task.PaymentOrderID)
	if err != nil {
		return finishPaymentTaskWithError(task, runnerID, model.PaymentTaskStatusFailed,
			"order_not_found", err)
	}
	if paymentOrderTerminal(order.Status) {
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
	}
	if order.ExpiresAt > 0 && order.ExpiresAt <= common.GetTimestamp() {
		if _, expireErr := model.ExpirePaymentOrderIfDueForTask(order.UserID, order.TradeNo, task, runnerID); expireErr != nil {
			return retryPaymentTask(task, runnerID, task.Phase, "order_expiry_failed", expireErr)
		}
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
	}
	if order.StartPayload != "" {
		if err := ensurePaymentReconcileTask(order); err != nil {
			return retryPaymentTask(task, runnerID, task.Phase, "reconcile_enqueue_failed", err)
		}
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
	}
	// Orders written by a pre-v0.2.1 node have no configuration-version
	// ownership marker. During a rolling deployment that older node may still
	// be inside its synchronous provider call, so a new worker must never race
	// it by creating a second upstream order.
	if order.ConfigurationVersion <= 0 {
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusManualReview,
			"legacy_creation_owner", "payment creation remains owned by a pre-worker application node")
	}
	if err := EnsurePaymentClusterReady(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "cluster_not_ready", err)
	}

	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "configuration_sync_failed", err)
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	ctx = withPaymentConfigurationReadLock(ctx)
	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	if err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "configuration_unavailable", err)
	}
	if order.ConfigurationVersion > 0 && order.ConfigurationVersion != configurationVersion {
		reason := "payment configuration changed before provider creation"
		if markErr := model.MarkPaymentOrderFailedFenced(order.TradeNo, reason, task.FenceToken); markErr != nil {
			return markErr
		}
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusFailed,
			"configuration_changed", reason)
	}

	provider, err := GetPaymentProvider(order.Provider)
	if err != nil {
		return failPaymentCreateTask(order, task, runnerID, "provider_unavailable", err)
	}
	// A quote is a pricing contract, not permission to bypass a later gateway
	// disable, compliance withdrawal, or credential-readiness failure. An order
	// created from an otherwise valid quote must therefore re-check the current
	// built-in provider configuration immediately before the background worker
	// crosses the external side-effect boundary.
	switch provider.(type) {
	case *epayPaymentProvider, *stripePaymentProvider, *xorPayProvider,
		*creemPaymentProvider, *waffoPaymentProvider, *waffoPancakePaymentProvider:
		if err := ValidatePaymentProviderForCreate(order.Provider, order.PaymentMethod); err != nil {
			return failPaymentCreateTask(order, task, runnerID, "provider_configuration_invalid", err)
		}
	}
	if credentialProvider, ok := provider.(PaymentCredentialGenerationProvider); ok {
		if order.ProviderCredentialGeneration == 0 {
			generation := credentialProvider.CredentialGeneration()
			if generation <= 0 {
				return failPaymentCreateTask(order, task, runnerID, "credential_unavailable",
					errors.New("payment provider credential generation is invalid"))
			}
			if err := model.BindPaymentOrderCredentialGeneration(order.TradeNo, generation, configurationVersion); err != nil {
				return failPaymentCreateTask(order, task, runnerID, "credential_binding_failed", err)
			}
			order.ProviderCredentialGeneration = generation
		} else {
			available, availabilityErr := model.PaymentCredentialGenerationAvailable(
				order.Provider, order.ProviderCredentialGeneration, order.CreatedAt,
			)
			if availabilityErr != nil {
				return retryPaymentTask(task, runnerID, task.Phase, "credential_check_failed", availabilityErr)
			}
			if !available {
				reason := "payment provider credential generation is no longer available"
				return finishPaymentTaskManualReview(order, task, runnerID,
					"credential_revoked", reason, reason)
			}
		}
	}

	if order.ProviderOrderKey != nil {
		return recoverPaymentStart(ctx, provider, order, task, runnerID)
	}
	if order.Provider == model.PaymentProviderXorPay && order.PaymentMethod == model.PaymentMethodXorPayJSAPI &&
		strings.TrimSpace(order.BrowserAuthorizationPayload) == "" {
		return retryPaymentTask(task, runnerID, model.PaymentTaskPhaseReady,
			"browser_authorization_required", model.ErrPaymentBrowserAuthorizationRequired)
	}
	if order.Provider == model.PaymentProviderXorPay && task.Phase == model.PaymentTaskPhaseProviderCallStarted {
		return recoverAmbiguousXorPayCreate(ctx, provider, order, task, runnerID)
	}
	if order.Provider == model.PaymentProviderWaffo && task.Phase == model.PaymentTaskPhaseProviderCallStarted {
		return recoverAmbiguousWaffoCreate(ctx, provider, order, task, runnerID)
	}
	if (order.Provider == model.PaymentProviderCreem || order.Provider == model.PaymentProviderWaffoPancake) &&
		task.Phase == model.PaymentTaskPhaseProviderCallStarted {
		return finishUnrecoverableRetainedCreate(order, task, runnerID,
			"provider_create_uncertain", "payment provider creation may have succeeded but no recoverable payment instructions were persisted")
	}

	if err := model.UpdatePaymentTaskPhase(task, runnerID, model.PaymentTaskPhaseProviderCallStarted); err != nil {
		return err
	}
	result, err := provider.Create(ctx, order)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			reason := "payment provider identity requires administrator review"
			return finishPaymentTaskManualReview(order, task, runnerID,
				"provider_identity_review", reason, err.Error())
		}
		if errors.Is(err, ErrPaymentStateUnknown) {
			if order.Provider == model.PaymentProviderCreem || order.Provider == model.PaymentProviderWaffoPancake {
				return finishUnrecoverableRetainedCreate(order, task, runnerID,
					"provider_create_uncertain", err.Error())
			}
			return retryPaymentTask(task, runnerID, model.PaymentTaskPhaseProviderCallStarted,
				"provider_create_uncertain", err)
		}
		return failPaymentCreateTask(order, task, runnerID, "provider_create_failed", err)
	}
	if result == nil {
		if order.Provider == model.PaymentProviderCreem || order.Provider == model.PaymentProviderWaffoPancake {
			return finishUnrecoverableRetainedCreate(order, task, runnerID,
				"provider_create_incomplete", "payment provider returned no durable start result")
		}
		return retryPaymentTask(task, runnerID, model.PaymentTaskPhaseProviderCallStarted,
			"provider_create_incomplete", errors.New("payment provider returned no start result"))
	}
	return commitPaymentStart(order, result, task, runnerID)
}

func recoverAmbiguousWaffoCreate(ctx context.Context, provider PaymentProvider, order *model.PaymentOrder, task *model.PaymentTask, runnerID string) error {
	if recoverer, ok := provider.(PaymentStartRecoverer); ok {
		recovered, err := recoverer.RecoverStart(ctx, order)
		if err == nil && recovered != nil {
			return commitPaymentStart(order, recovered, task, runnerID)
		}
		if err != nil && !errors.Is(err, ErrPaymentStateUnknown) {
			if errors.Is(err, model.ErrPaymentManualReview) {
				return finishPaymentTaskManualReview(order, task, runnerID,
					"provider_identity_review", "payment provider identity requires administrator review", err.Error())
			}
			return retryPaymentTask(task, runnerID, task.Phase, "provider_start_recovery_failed", err)
		}
	}
	event, err := provider.Query(ctx, order)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			return finishPaymentTaskManualReview(order, task, runnerID,
				"provider_identity_review", "payment provider identity requires administrator review", err.Error())
		}
		return retryPaymentTask(task, runnerID, task.Phase, "provider_query_failed", err)
	}
	if event == nil {
		return retryPaymentTask(task, runnerID, task.Phase, "provider_state_pending",
			errors.New("Waffo order state is unavailable"))
	}
	state := strings.TrimPrefix(event.EventType, "query:")
	if state == "not_exist" {
		misses := task.RecoveryMisses + 1
		phase := model.PaymentTaskPhaseProviderCallStarted
		if misses >= paymentCreateRecoveryMisses {
			// Waffo uses the same deterministic PaymentRequestID on every call.
			// Re-creation is allowed only after two independent not-found
			// inquiries have established that the original request did not land.
			phase = model.PaymentTaskPhaseReady
			misses = 0
		}
		return model.RetryPaymentTask(task, runnerID, common.GetTimestamp()+3, phase,
			"provider_order_not_found", "Waffo did not find the order during create recovery", misses)
	}
	if event.Paid || event.Failed || event.Expired || event.ManualReview {
		if _, processErr := processNormalizedPaymentEventForTask(event, task, runnerID); processErr != nil &&
			!errors.Is(processErr, model.ErrPaymentManualReview) {
			return retryPaymentTask(task, runnerID, task.Phase, "provider_event_failed", processErr)
		}
		status := model.PaymentTaskStatusSucceeded
		code := ""
		if event.ManualReview {
			status = model.PaymentTaskStatusManualReview
			code = "provider_manual_review"
		}
		return model.FinishPaymentTask(task, runnerID, status, code, "")
	}
	return finishUnrecoverableRetainedCreate(order, task, runnerID,
		"payment_instructions_unrecoverable", "Waffo order exists but its payment instructions cannot be recovered")
}

func finishUnrecoverableRetainedCreate(order *model.PaymentOrder, task *model.PaymentTask, runnerID, code, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "payment provider creation requires administrator review"
	}
	return finishPaymentTaskManualReview(order, task, runnerID, code, reason, reason)
}

func recoverPaymentStart(ctx context.Context, provider PaymentProvider, order *model.PaymentOrder, task *model.PaymentTask, runnerID string) error {
	if recoverer, ok := provider.(PaymentStartRecoverer); ok {
		recovered, err := recoverer.RecoverStart(ctx, order)
		if err == nil && recovered != nil {
			return commitPaymentStart(order, recovered, task, runnerID)
		}
		if err != nil && !errors.Is(err, ErrPaymentStateUnknown) {
			if errors.Is(err, model.ErrPaymentManualReview) {
				return finishPaymentTaskManualReview(order, task, runnerID,
					"provider_identity_review", "payment provider identity requires administrator review", err.Error())
			}
			return retryPaymentTask(task, runnerID, task.Phase, "provider_start_recovery_failed", err)
		}
	}
	event, err := provider.Query(ctx, order)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			return finishPaymentTaskManualReview(order, task, runnerID,
				"provider_identity_review", "payment provider identity requires administrator review", err.Error())
		}
		return retryPaymentTask(task, runnerID, task.Phase, "provider_query_failed", err)
	}
	if event == nil {
		return retryPaymentTask(task, runnerID, task.Phase, "provider_state_pending",
			errors.New("provider start state is not yet recoverable"))
	}
	if event.Paid || event.Failed || event.Expired || event.ManualReview {
		if _, processErr := processNormalizedPaymentEventForTask(event, task, runnerID); processErr != nil &&
			!errors.Is(processErr, model.ErrPaymentManualReview) {
			return retryPaymentTask(task, runnerID, task.Phase, "provider_event_failed", processErr)
		}
		status := model.PaymentTaskStatusSucceeded
		code := ""
		if event.ManualReview {
			status = model.PaymentTaskStatusManualReview
			code = "provider_manual_review"
		}
		return model.FinishPaymentTask(task, runnerID, status, code, "")
	}
	reason := "provider order exists but payment instructions cannot be recovered"
	return finishPaymentTaskManualReview(order, task, runnerID,
		"payment_instructions_unrecoverable", reason, reason)
}

func recoverAmbiguousXorPayCreate(ctx context.Context, provider PaymentProvider, order *model.PaymentOrder, task *model.PaymentTask, runnerID string) error {
	event, err := provider.Query(ctx, order)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			return finishPaymentTaskManualReview(order, task, runnerID,
				"provider_identity_review", "payment provider identity requires administrator review", err.Error())
		}
		return retryPaymentTask(task, runnerID, task.Phase, "provider_query_failed", err)
	}
	if event == nil {
		return retryPaymentTask(task, runnerID, task.Phase, "provider_state_pending",
			errors.New("xorpay order state is unavailable"))
	}
	state := strings.TrimPrefix(event.EventType, "query2:")
	switch state {
	case "not_exist":
		misses := task.RecoveryMisses + 1
		phase := model.PaymentTaskPhaseProviderCallStarted
		if misses >= paymentCreateRecoveryMisses {
			phase = model.PaymentTaskPhaseReady
			misses = 0
		}
		return model.RetryPaymentTask(task, runnerID, common.GetTimestamp()+3, phase,
			"provider_order_not_found", "xorpay did not find the order during create recovery", misses)
	case "new":
		reason := "provider order exists but its payment instructions cannot be recovered"
		return finishPaymentTaskManualReview(order, task, runnerID,
			"payment_instructions_unrecoverable", reason, reason)
	case "payed", "success", "expire", "fee_error":
		if _, processErr := processNormalizedPaymentEventForTask(event, task, runnerID); processErr != nil &&
			!errors.Is(processErr, model.ErrPaymentManualReview) {
			return retryPaymentTask(task, runnerID, task.Phase, "provider_event_failed", processErr)
		}
		status := model.PaymentTaskStatusSucceeded
		code := ""
		if state == "fee_error" {
			status = model.PaymentTaskStatusManualReview
			code = "provider_fee_error"
		}
		return model.FinishPaymentTask(task, runnerID, status, code, "")
	default:
		return retryPaymentTask(task, runnerID, task.Phase, "provider_state_unknown",
			fmt.Errorf("unexpected xorpay recovery state %q", state))
	}
}

func commitPaymentStart(order *model.PaymentOrder, result *PaymentStart, task *model.PaymentTask, runnerID string) error {
	result.TradeNo = order.TradeNo
	if result.ExpiresAt == 0 {
		result.ExpiresAt = order.ExpiresAt
	}
	payload, err := common.Marshal(result)
	if err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "start_snapshot_failed", err)
	}
	if err := model.SavePaymentOrderStartWithProviderIdentityFenced(
		order.TradeNo, result.Flow, string(payload), result.ExpiresAt,
		result.ProviderOrderKey, result.ProviderPaymentKey, task.FenceToken,
	); err != nil {
		return err
	}
	stored, err := model.GetPaymentOrderByTradeNo(order.TradeNo)
	if err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "order_reload_failed", err)
	}
	if err := ensurePaymentReconcileTask(stored); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "reconcile_enqueue_failed", err)
	}
	return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
}

func ensurePaymentReconcileTask(order *model.PaymentOrder) error {
	if order == nil || paymentOrderTerminal(order.Status) || order.Provider == model.PaymentProviderEpay {
		return nil
	}
	_, err := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationReconcile,
		common.GetTimestamp()+int64(paymentReconcileInitialDelay/time.Second))
	if err == nil {
		notifyPaymentTaskRunner()
	}
	return err
}

func runPaymentReconcileTask(ctx context.Context, task *model.PaymentTask, runnerID string) error {
	order, err := model.GetPaymentOrderByID(task.PaymentOrderID)
	if err != nil {
		return finishPaymentTaskWithError(task, runnerID, model.PaymentTaskStatusFailed, "order_not_found", err)
	}
	if paymentOrderTerminal(order.Status) {
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
	}
	if order.ExpiresAt > 0 && order.ExpiresAt <= common.GetTimestamp() {
		if _, err := model.ExpirePaymentOrderIfDueForTask(order.UserID, order.TradeNo, task, runnerID); err != nil {
			return retryPaymentTask(task, runnerID, task.Phase, "order_expiry_failed", err)
		}
		return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
	}
	if order.StartPayload == "" {
		return retryPaymentTask(task, runnerID, model.PaymentTaskPhaseWaiting,
			"payment_not_ready", errors.New("payment creation is still pending"))
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "configuration_sync_failed", err)
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	ctx = withPaymentConfigurationReadLock(ctx)
	provider, err := GetPaymentProvider(order.Provider)
	if err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "provider_unavailable", err)
	}
	if err := EnsurePaymentClusterReady(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "cluster_not_ready", err)
	}
	if err := model.AssertPaymentTaskLease(task, runnerID); err != nil {
		return err
	}
	event, err := provider.Query(ctx, order)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			reason := "payment provider identity requires administrator review"
			return finishPaymentTaskManualReview(order, task, runnerID,
				"provider_identity_review", reason, err.Error())
		}
		return retryPaymentTask(task, runnerID, task.Phase, "provider_query_failed", err)
	}
	if event != nil && (event.Paid || event.Failed || event.Expired || event.Refunded || event.Disputed || event.DisputeResolved || event.ManualReview) {
		if _, err := processNormalizedPaymentEventForTask(event, task, runnerID); err != nil && !errors.Is(err, model.ErrPaymentManualReview) {
			return retryPaymentTask(task, runnerID, task.Phase, "provider_event_failed", err)
		}
		stored, lookupErr := model.GetPaymentOrderByID(order.ID)
		if lookupErr == nil && paymentOrderTerminal(stored.Status) {
			return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusSucceeded, "", "")
		}
	}
	return retryPaymentTask(task, runnerID, model.PaymentTaskPhaseWaiting,
		"provider_state_pending", errors.New("payment is awaiting provider confirmation"))
}

func runPaymentMaintenanceTask(ctx context.Context, task *model.PaymentTask, runnerID string) error {
	now := common.GetTimestamp()
	if _, err := model.ExpireDuePaymentOrders(ctx, now, paymentMaintenanceBatchSize); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "order_expiry_failed", err)
	}
	if _, err := model.ReconcilePaymentLimitReservations(ctx, now, paymentMaintenanceBatchSize); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "limit_reconciliation_failed", err)
	}
	if _, err := model.CleanupPaymentQuotes(ctx, now, paymentMaintenanceBatchSize); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "quote_cleanup_failed", err)
	}
	if err := repairPaymentTasks(); err != nil {
		return retryPaymentTask(task, runnerID, task.Phase, "task_repair_failed", err)
	}
	return model.RetryPaymentTask(task, runnerID,
		now+int64(paymentMaintenanceInterval/time.Second), model.PaymentTaskPhaseWaiting, "", "", 0)
}

func repairPaymentTasks() error {
	var orders []model.PaymentOrder
	if err := model.DB.Model(&model.PaymentOrder{}).
		Where("payment_orders.status IN ?", []string{
			model.PaymentOrderStatusPending,
			model.PaymentOrderStatusProcessing,
		}).
		Where(`
			(payment_orders.start_payload = ? AND payment_orders.configuration_version > ? AND NOT EXISTS (
				SELECT 1 FROM payment_tasks
				WHERE payment_tasks.payment_order_id = payment_orders.id AND payment_tasks.operation = ?
			)) OR
			(payment_orders.start_payload <> ? AND payment_orders.provider <> ? AND NOT EXISTS (
				SELECT 1 FROM payment_tasks
				WHERE payment_tasks.payment_order_id = payment_orders.id AND payment_tasks.operation = ?
			))`,
			"", 0, model.PaymentTaskOperationCreate,
			"", model.PaymentProviderEpay, model.PaymentTaskOperationReconcile).
		Order("payment_orders.id ASC").Limit(paymentMaintenanceBatchSize).Find(&orders).Error; err != nil {
		return err
	}
	for i := range orders {
		order := &orders[i]
		if order.StartPayload == "" && order.ConfigurationVersion <= 0 {
			// See runPaymentCreateTask: an older node may still own this browser-
			// bound creation during a rolling release. Expiry/manual review is
			// safer than guessing that its upstream call did not succeed.
			continue
		}
		operation := model.PaymentTaskOperationCreate
		availableAt := common.GetTimestamp()
		if order.StartPayload != "" && order.Provider != model.PaymentProviderEpay {
			operation = model.PaymentTaskOperationReconcile
			availableAt += int64(paymentReconcileInitialDelay / time.Second)
		}
		if order.StartPayload != "" && order.Provider == model.PaymentProviderEpay {
			continue
		}
		if _, err := model.EnsurePaymentTask(order.ID, operation, availableAt); err != nil {
			return err
		}
	}
	return nil
}

func retryPaymentTask(task *model.PaymentTask, runnerID, phase, code string, err error) error {
	delay := paymentTaskRetryDelay(task.Attempts)
	message := ""
	if err != nil {
		message = err.Error()
	}
	return model.RetryPaymentTask(task, runnerID, common.GetTimestamp()+int64(delay/time.Second),
		phase, code, message, task.RecoveryMisses)
}

func paymentTaskRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := time.Duration(attempts*attempts) * 3 * time.Second
	if delay > paymentReconcileMaximumDelay {
		return paymentReconcileMaximumDelay
	}
	return delay
}

func failPaymentCreateTask(order *model.PaymentOrder, task *model.PaymentTask, runnerID, code string, err error) error {
	reason := "payment provider creation failed"
	if markErr := model.MarkPaymentOrderFailedFenced(order.TradeNo, reason, task.FenceToken); markErr != nil {
		return markErr
	}
	return finishPaymentTaskWithError(task, runnerID, model.PaymentTaskStatusFailed, code, err)
}

func finishPaymentTaskManualReview(order *model.PaymentOrder, task *model.PaymentTask, runnerID, code, reason, detail string) error {
	if order == nil || task == nil {
		return model.ErrPaymentTaskLeaseLost
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "payment requires administrator review"
	}
	if err := model.MarkPaymentOrderManualReviewForTask(order.TradeNo, reason, task, runnerID); err != nil {
		return err
	}
	return model.FinishPaymentTask(task, runnerID, model.PaymentTaskStatusManualReview, code, strings.TrimSpace(detail))
}

func finishPaymentTaskWithError(task *model.PaymentTask, runnerID, status, code string, err error) error {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return model.FinishPaymentTask(task, runnerID, status, code, message)
}

func paymentOrderTerminal(status string) bool {
	switch status {
	case model.PaymentOrderStatusFulfilled,
		model.PaymentOrderStatusFailed,
		model.PaymentOrderStatusExpired,
		model.PaymentOrderStatusManualReview,
		model.PaymentOrderStatusRefundPending,
		model.PaymentOrderStatusRefunded,
		model.PaymentOrderStatusDisputed,
		model.PaymentOrderStatusDebt:
		return true
	default:
		return false
	}
}
