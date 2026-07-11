package channelrouting

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
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

	snapshots := routingmetrics.DrainStableSnapshots()
	if len(snapshots) == 0 {
		return 0, nil
	}

	for start := 0; start < len(snapshots); start += model.RoutingMetricRollupMaxBatch {
		end := min(start+model.RoutingMetricRollupMaxBatch, len(snapshots))
		rollups := make([]model.RoutingMetricRollup, 0, end-start)
		for index := start; index < end; index++ {
			snapshot := snapshots[index]
			if snapshot.LastSnapshotRevision > math.MaxInt64 {
				routingmetrics.RequeueStableSnapshots(snapshots[start:])
				return start, errors.New("routing metric snapshot revision exceeds database range")
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
		if err := model.UpsertRoutingMetricRollupsContext(ctx, rollups); err != nil {
			routingmetrics.RequeueStableSnapshots(snapshots[start:])
			return start, err
		}
	}
	return len(snapshots), nil
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
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	rollupsDeleted, err := model.DeleteRoutingMetricRollupsBeforeContext(ctx, cutoff)
	if err != nil {
		return rollupsDeleted, err
	}
	auditsDeleted, err := model.DeleteRoutingDecisionAuditsBeforeContext(ctx, cutoff)
	return rollupsDeleted + auditsDeleted, err
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
