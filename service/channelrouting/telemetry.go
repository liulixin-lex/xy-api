package channelrouting

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
)

const (
	stableTelemetryDrainMaxBytes     = routingTelemetryEnvelopeMaxBytes * 3 / 4
	stableTelemetryFlushMaxEnvelopes = 8
)

var routingTelemetryMaintenance = func() chan struct{} {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return token
}()

func FlushStableTelemetryContext(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := lockRoutingTelemetry(ctx); err != nil {
		return 0, err
	}
	defer unlockRoutingTelemetry()

	flushed := 0
	for envelopeIndex := 0; envelopeIndex < stableTelemetryFlushMaxEnvelopes; envelopeIndex++ {
		now := time.Now()
		envelope, exists := routingTelemetryTransport.peek(now)
		if !exists {
			if !routingTelemetryTransport.hasCapacity(now) {
				return flushed, ErrRoutingTelemetryPendingFull
			}
			snapshots := routingmetrics.DrainStableSnapshotsLimited(model.RoutingTelemetryMaxItems, stableTelemetryDrainMaxBytes)
			if len(snapshots) == 0 {
				return flushed, nil
			}
			rollups, err := stableSnapshotsToRoutingRollups(snapshots)
			if err != nil {
				routingmetrics.RequeueStableSnapshots(snapshots)
				return flushed, err
			}
			envelope, err = newRoutingTelemetryEnvelope(rollups, now)
			if err != nil {
				routingmetrics.RequeueStableSnapshots(snapshots)
				return flushed, err
			}
			if err := routingTelemetryTransport.enqueue(envelope, now); err != nil {
				routingmetrics.RequeueStableSnapshots(snapshots)
				return flushed, err
			}
		}

		if err := deliverRoutingTelemetryEnvelopeContext(ctx, envelope); err != nil {
			return flushed, err
		}
		if !routingTelemetryTransport.remove(envelope.batch.Sequence) {
			return flushed, errors.New("routing telemetry pending queue changed during flush")
		}
		flushed += len(envelope.batch.Items)
	}
	return flushed, nil
}

func stableSnapshotsToRoutingRollups(snapshots []routingmetrics.StableSnapshot) ([]model.RoutingMetricRollup, error) {
	rollups := make([]model.RoutingMetricRollup, 0, len(snapshots))
	for index := range snapshots {
		snapshot := snapshots[index]
		if snapshot.LastSnapshotRevision > math.MaxInt64 {
			return nil, errors.New("routing metric snapshot revision exceeds database range")
		}
		rollups = append(rollups, model.RoutingMetricRollup{
			MemberID:                snapshot.PoolMemberID,
			CredentialID:            snapshot.CredentialID,
			ModelName:               snapshot.Model,
			BucketTs:                snapshot.BucketTs,
			ChannelID:               snapshot.ChannelID,
			PoolID:                  snapshot.PoolID,
			SchemaVersion:           model.RoutingMetricRollupSchemaVersion,
			LastSnapshotRevision:    int64(snapshot.LastSnapshotRevision),
			SketchCodecVersion:      snapshot.SketchCodecVersion,
			LatencySampleCount:      snapshot.LatencySampleCount,
			LatencySketch:           append([]byte(nil), snapshot.LatencySketch...),
			TtftSampleCount:         snapshot.TtftSampleCount,
			TtftSketch:              append([]byte(nil), snapshot.TtftSketch...),
			RequestCount:            snapshot.RequestCount,
			SuccessCount:            snapshot.SuccessCount,
			FailureCount:            snapshot.FailureCount,
			UnknownCount:            snapshot.UnknownClassificationCount,
			ReliabilityRequestCount: snapshot.ReliabilityRequestCount,
			ReliabilityFailureCount: snapshot.ReliabilityFailureCount,
			TotalLatencyMs:          snapshot.TotalLatencyMs,
			TtftSumMs:               snapshot.TtftSumMs,
			TtftCount:               snapshot.TtftCount,
			OutputTokens:            snapshot.OutputTokens,
			GenerationMs:            snapshot.GenerationMs,
			Err4xx:                  snapshot.Err4xx,
			Err5xx:                  snapshot.Err5xx,
			Err429:                  snapshot.Err429,
			Err529:                  snapshot.Err529,
			RetryAfterCount:         snapshot.RetryAfterCount,
			RetryAfterTotalMs:       snapshot.RetryAfterTotalMs,
		})
	}
	return rollups, nil
}

func DeleteExpiredRoutingHistoryContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := lockRoutingTelemetry(ctx); err != nil {
		return 0, err
	}
	defer unlockRoutingTelemetry()

	maxRetentionDays := int((time.Duration(1<<63 - 1)) / (24 * time.Hour))
	if retentionDays > maxRetentionDays {
		retentionDays = maxRetentionDays
	}
	databaseNowMs, err := model.RoutingDatabaseNowMsContext(ctx)
	if err != nil {
		return 0, err
	}
	now := time.UnixMilli(databaseNowMs)
	cutoffTime := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	cutoff := cutoffTime.Unix()
	rollupsDeleted, err := model.DeleteRoutingMetricRollupsBeforeContext(ctx, cutoff)
	if err != nil {
		return rollupsDeleted, err
	}
	auditsDeleted, err := model.DeleteRoutingDecisionAuditsBeforeContext(ctx, cutoff)
	if err != nil {
		return rollupsDeleted + auditsDeleted, err
	}
	costVersionsDeleted, err := model.DeleteRoutingCostSnapshotVersionsBeforeContext(ctx, cutoff)
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted, err
	}
	probeResultsDeleted, err := model.DeleteRoutingProbeResultsBeforeContext(ctx, cutoffTime.UnixMilli())
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted, err
	}
	probeLeasesDeleted, err := model.DeleteRoutingControlLeasesByPrefixBeforeContext(
		ctx, activeProbeLeasePrefix, cutoffTime.UnixMilli(), now.UnixMilli(), 500,
	)
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted + probeLeasesDeleted, err
	}
	receiptRetentionDays := retentionDays + 1
	if receiptRetentionDays < retentionDays || receiptRetentionDays > maxRetentionDays {
		receiptRetentionDays = maxRetentionDays
	}
	receiptCutoff := now.Add(-time.Duration(receiptRetentionDays) * 24 * time.Hour).UnixMilli()
	receiptsDeleted, err := model.DeleteRoutingTelemetryReceiptsBeforeContext(ctx, receiptCutoff)
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted + probeLeasesDeleted + receiptsDeleted, err
	}
	outboxDeleted, err := model.DeletePublishedRoutingConfigOutboxBeforeContext(ctx, cutoff)
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted + probeLeasesDeleted + receiptsDeleted + outboxDeleted, err
	}
	resetOutboxDeleted, err := model.DeletePublishedRoutingBreakerResetOutboxBeforeContext(ctx, cutoffTime.UnixMilli())
	if err != nil {
		return rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted + probeLeasesDeleted + receiptsDeleted + outboxDeleted + resetOutboxDeleted, err
	}
	checkpointsDeleted, err := model.DeleteExpiredRoutingRuntimeCheckpointsContext(ctx, now.Unix())
	deleted := rollupsDeleted + auditsDeleted + costVersionsDeleted + probeResultsDeleted + probeLeasesDeleted + receiptsDeleted + outboxDeleted + resetOutboxDeleted + checkpointsDeleted
	if err != nil {
		return deleted, err
	}
	endpointDeleted, err := DeleteExpiredRoutingEndpointHistoryContext(ctx, retentionDays)
	deleted += endpointDeleted
	if err != nil {
		return deleted, err
	}
	errorBudgetDeleted, err := model.DeleteRoutingErrorBudgetHistoryBeforeContext(ctx, cutoffTime.UnixMilli())
	deleted += errorBudgetDeleted
	if err != nil {
		return deleted, err
	}
	canaryEvaluationsDeleted, err := deleteExpiredRoutingCanaryEvaluationsContext(ctx, cutoffTime.UnixMilli())
	deleted += canaryEvaluationsDeleted
	if err != nil {
		return deleted, err
	}
	for batch := 0; batch < 20; batch++ {
		exportsDeleted, deleteErr := model.DeleteExpiredRoutingAuditExportsContext(ctx, now.UnixMilli(), 500)
		deleted += exportsDeleted
		if deleteErr != nil {
			return deleted, deleteErr
		}
		if exportsDeleted < 500 {
			break
		}
	}
	for batch := 0; batch < 20; batch++ {
		operationsDeleted, deleteErr := model.DeleteCompletedRoutingOperationsBeforeContext(
			ctx, cutoffTime.UnixMilli(), now.UnixMilli(), 500,
		)
		deleted += operationsDeleted
		if deleteErr != nil {
			return deleted, deleteErr
		}
		if operationsDeleted < 500 {
			break
		}
	}
	for batch := 0; batch < 20; batch++ {
		approvalsDeleted, deleteErr := model.DeleteStaleRoutingPolicyApprovalsContext(
			ctx, cutoffTime.UnixMilli(), 500,
		)
		deleted += approvalsDeleted
		if deleteErr != nil {
			return deleted, deleteErr
		}
		if approvalsDeleted < 500 {
			break
		}
	}
	for batch := 0; batch < 20; batch++ {
		approvalsDeleted, deleteErr := model.DeleteStaleRoutingPolicyRollbackApprovalsContext(
			ctx, cutoffTime.UnixMilli(), 500,
		)
		deleted += approvalsDeleted
		if deleteErr != nil {
			return deleted, deleteErr
		}
		if approvalsDeleted < 500 {
			break
		}
	}
	for batch := 0; batch < 20; batch++ {
		commandsDeleted, deleteErr := model.DeleteCompletedRoutingBreakerResetCommandsBeforeContext(
			ctx, cutoffTime.UnixMilli(), 500,
		)
		deleted += commandsDeleted
		if deleteErr != nil {
			return deleted, deleteErr
		}
		if commandsDeleted < 500 {
			break
		}
	}
	return deleted, nil
}

func deleteExpiredRoutingCanaryEvaluationsContext(ctx context.Context, cutoffMs int64) (int64, error) {
	return model.DeleteRoutingCanaryEvaluationsBeforeContext(ctx, cutoffMs)
}

func lockRoutingTelemetry(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-routingTelemetryMaintenance:
		return nil
	}
}

func unlockRoutingTelemetry() {
	routingTelemetryMaintenance <- struct{}{}
}
