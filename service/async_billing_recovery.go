package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	asyncBillingRecoveryBatchSize    = 100
	asyncBillingReservedStaleAfter   = 2 * time.Minute
	asyncBillingAmbiguousReviewAfter = 15 * time.Minute
	asyncBillingProjectionLease      = time.Minute
)

type AsyncBillingRecoverySummary struct {
	ReleasedReservations                int   `json:"released_reservations"`
	TerminalDriftsRecovered             int   `json:"terminal_drifts_recovered"`
	ExpiredReceiptsDeleted              int64 `json:"expired_receipts_deleted"`
	ManualReviewMarked                  int   `json:"manual_review_marked"`
	CacheSyncCompleted                  int   `json:"cache_sync_completed"`
	IdentityCacheSyncCompleted          int   `json:"identity_cache_sync_completed"`
	ProjectionCompleted                 int   `json:"projection_completed"`
	StatsProjectionCompleted            int   `json:"stats_projection_completed"`
	StatsProjectionFailedPending        int64 `json:"stats_projection_failed_pending"`
	LogProjectionCompleted              int   `json:"log_projection_completed"`
	LogProjectionFailedPending          int64 `json:"log_projection_failed_pending"`
	Errors                              int   `json:"errors"`
	ManualReviewPending                 int64 `json:"manual_review_pending"`
	OldestManualAgeSec                  int64 `json:"oldest_manual_age_seconds"`
	TaskTerminalSnapshotsRepaired       int   `json:"task_terminal_snapshots_repaired"`
	TaskTerminalSnapshotFailures        int   `json:"task_terminal_snapshot_failures"`
	MidjourneyTerminalSnapshotsRepaired int   `json:"midjourney_terminal_snapshots_repaired"`
	MidjourneyTerminalSnapshotFailures  int   `json:"midjourney_terminal_snapshot_failures"`
	NextTaskTerminalAfterID             int64 `json:"next_task_terminal_after_id"`
	NextMidjourneyTerminalAfterID       int64 `json:"next_midjourney_terminal_after_id"`
	NextReceiptCleanupAfterID           int64 `json:"next_receipt_cleanup_after_id"`
}

type AsyncBillingRecoveryCursor struct {
	TaskTerminalAfterID       int64 `json:"task_terminal_after_id"`
	MidjourneyTerminalAfterID int64 `json:"midjourney_terminal_after_id"`
	ReceiptCleanupAfterID     int64 `json:"receipt_cleanup_after_id"`
}

func RunAsyncBillingRecoveryOnce(ctx context.Context) AsyncBillingRecoverySummary {
	owner := strings.TrimSpace(common.NodeName)
	if owner == "" {
		owner = "async-billing-recovery"
	}
	owner += "-" + common.GetRandomString(8)
	return RunAsyncBillingRecoveryOnceWithOwner(ctx, owner)
}

func RunAsyncBillingRecoveryOnceWithOwner(ctx context.Context, owner string) AsyncBillingRecoverySummary {
	return RunAsyncBillingRecoveryOnceWithCursor(ctx, owner, AsyncBillingRecoveryCursor{})
}

func RunAsyncBillingRecoveryOnceWithCursor(
	ctx context.Context,
	owner string,
	cursor AsyncBillingRecoveryCursor,
) AsyncBillingRecoverySummary {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	summary := AsyncBillingRecoverySummary{}
	if ctx.Err() != nil {
		return summary
	}

	taskSnapshotPage, err := model.RepairTaskTerminalSnapshotPage(
		ctx, cursor.TaskTerminalAfterID, asyncBillingRecoveryBatchSize, now,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.TaskTerminalSnapshotsRepaired = taskSnapshotPage.Repaired
		summary.TaskTerminalSnapshotFailures = taskSnapshotPage.Failed
		summary.Errors += taskSnapshotPage.Failed
		summary.NextTaskTerminalAfterID = taskSnapshotPage.NextID
	}
	if ctx.Err() != nil {
		return summary
	}
	midjourneySnapshotPage, err := model.RepairMidjourneyTerminalSnapshotPage(
		ctx, cursor.MidjourneyTerminalAfterID, asyncBillingRecoveryBatchSize, now,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.MidjourneyTerminalSnapshotsRepaired = midjourneySnapshotPage.Repaired
		summary.MidjourneyTerminalSnapshotFailures = midjourneySnapshotPage.Failed
		summary.Errors += midjourneySnapshotPage.Failed
		summary.NextMidjourneyTerminalAfterID = midjourneySnapshotPage.NextID
	}
	if ctx.Err() != nil {
		return summary
	}

	recoverableIDs, err := model.FindRecoverableAsyncBillingReservationIDsContext(
		ctx, now, asyncBillingReservedStaleAfter, asyncBillingRecoveryBatchSize,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for _, reservationID := range recoverableIDs {
			if ctx.Err() != nil {
				return summary
			}
			if _, releaseErr := model.ReleaseAsyncBillingReservation(ctx, reservationID, now); releaseErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				if !errors.Is(releaseErr, model.ErrAsyncBillingReservationAmbiguous) &&
					!errors.Is(releaseErr, model.ErrAsyncBillingReservationAccepted) {
					summary.Errors++
				}
				continue
			}
			summary.ReleasedReservations++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	drifts, err := model.FindAcceptedAsyncBillingTerminalDrifts(ctx, asyncBillingRecoveryBatchSize)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for index := range drifts {
			if ctx.Err() != nil {
				return summary
			}
			drift := &drifts[index]
			if drift.Task != nil {
				actualQuota, totalTokens, usageErr := recoverAcceptedTaskTerminalUsage(ctx, drift.Task)
				if usageErr != nil {
					if ctx.Err() != nil {
						return summary
					}
					reason := "terminal usage verification failed: " + common.SanitizeErrorMessage(usageErr.Error())
					if _, reviewErr := model.MarkAsyncBillingTerminalUsageManualReview(
						ctx, drift.Reservation.ID, reason, now,
					); reviewErr != nil {
						if ctx.Err() != nil {
							return summary
						}
						summary.Errors++
					} else {
						summary.ManualReviewMarked++
					}
					continue
				}
				_, recoverErr := finalizePolledTask(ctx, TaskFinalizationObservation{
					TaskID: drift.Task.ID, TerminalStatus: drift.Task.Status,
					Progress: drift.Task.Progress, SubmitTime: drift.Task.SubmitTime,
					StartTime: drift.Task.StartTime, FinishTime: drift.Task.FinishTime,
					FailReason: drift.Task.FailReason, UpstreamResultURL: drift.Task.GetUpstreamResultURL(),
					Data: drift.Task.Data, ActualQuota: actualQuota, TotalTokens: totalTokens,
				})
				if recoverErr != nil {
					if ctx.Err() != nil {
						return summary
					}
					summary.Errors++
					continue
				}
				summary.TerminalDriftsRecovered++
				continue
			}
			if drift.Midjourney == nil {
				summary.Errors++
				continue
			}
			var finalization *model.MidjourneyFailureFinalization
			var recoverErr error
			if drift.Midjourney.Status == "FAILURE" {
				finalization, recoverErr = FinalizeMidjourneyFailure(ctx, drift.Midjourney, drift.Midjourney.Status)
			} else {
				finalization, recoverErr = FinalizeMidjourneySuccess(ctx, drift.Midjourney, drift.Midjourney.Status)
			}
			if recoverErr == nil && finalization != nil && finalization.Operation.ID > 0 &&
				finalization.Operation.State != model.TaskBillingOperationStateCompleted {
				_, recoverErr = ProcessMidjourneyBillingOperation(
					ctx, finalization.Operation.ID, owner, asyncBillingProjectionLease,
				)
			}
			if recoverErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				summary.Errors++
				continue
			}
			summary.TerminalDriftsRecovered++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	for processed := 0; processed < asyncBillingRecoveryBatchSize; processed++ {
		if ctx.Err() != nil {
			return summary
		}
		projection, hadWork, projectionErr := ProcessNextBillingStatsProjection(ctx, owner, asyncBillingProjectionLease)
		if projectionErr != nil {
			if ctx.Err() != nil {
				return summary
			}
			summary.Errors++
		}
		if !hadWork {
			break
		}
		if projectionErr == nil && projection != nil && projection.State == model.BillingStatsProjectionStateCompleted {
			summary.StatsProjectionCompleted++
		}
	}

	for processed := 0; processed < asyncBillingRecoveryBatchSize; processed++ {
		if ctx.Err() != nil {
			return summary
		}
		projection, hadWork, projectionErr := ProcessNextBillingLogProjection(ctx, owner, asyncBillingProjectionLease)
		if projectionErr != nil {
			if ctx.Err() != nil {
				return summary
			}
			summary.Errors++
		}
		if !hadWork {
			break
		}
		if projectionErr == nil && projection != nil && projection.State == model.BillingLogProjectionStateCompleted {
			summary.LogProjectionCompleted++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	ambiguousIDs, err := model.FindStaleAmbiguousAsyncBillingReservationIDsContext(
		ctx, now, asyncBillingAmbiguousReviewAfter, asyncBillingRecoveryBatchSize,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for _, reservationID := range ambiguousIDs {
			if ctx.Err() != nil {
				return summary
			}
			if _, reviewErr := model.MarkAsyncBillingReservationManualReview(
				ctx, reservationID, "ambiguous upstream outcome exceeded review threshold", "", now,
			); reviewErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				summary.Errors++
				continue
			}
			summary.ManualReviewMarked++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	cacheIDs, err := model.FindPendingAsyncBillingCacheSyncIDsContext(ctx, now, asyncBillingRecoveryBatchSize)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for _, reservationID := range cacheIDs {
			if ctx.Err() != nil {
				return summary
			}
			if syncErr := model.SyncAsyncBillingReservationCaches(ctx, reservationID, now); syncErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				summary.Errors++
				continue
			}
			summary.CacheSyncCompleted++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	identityCacheSubjects, err := model.FindPendingIdentityCacheSyncSubjectsContext(
		ctx, now, asyncBillingRecoveryBatchSize,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for _, subject := range identityCacheSubjects {
			if ctx.Err() != nil {
				return summary
			}
			if syncErr := model.SyncIdentityCacheSubject(ctx, subject, now); syncErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				summary.Errors++
				continue
			}
			summary.IdentityCacheSyncCompleted++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	projectionIDs, err := model.FindPendingAsyncBillingAcceptedProjectionIDsContext(
		ctx, now, asyncBillingRecoveryBatchSize,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		for _, reservationID := range projectionIDs {
			if ctx.Err() != nil {
				return summary
			}
			if projectionErr := model.ProcessAsyncBillingAcceptedProjection(ctx, reservationID, now); projectionErr != nil {
				if ctx.Err() != nil {
					return summary
				}
				summary.Errors++
				continue
			}
			summary.ProjectionCompleted++
		}
	}
	if ctx.Err() != nil {
		return summary
	}

	manualCount, oldestMs, err := model.AsyncBillingManualReviewStatsContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.ManualReviewPending = manualCount
		if oldestMs > 0 && now.UnixMilli() > oldestMs {
			summary.OldestManualAgeSec = (now.UnixMilli() - oldestMs) / 1000
		}
	}
	if ctx.Err() != nil {
		return summary
	}
	failedStats, err := model.CountFailedBillingStatsProjections(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.StatsProjectionFailedPending = failedStats
	}
	if ctx.Err() != nil {
		return summary
	}
	failedLogs, err := model.CountFailedBillingLogProjections(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.LogProjectionFailedPending = failedLogs
	}
	if ctx.Err() != nil {
		return summary
	}
	cleanupPage, err := model.CleanupExpiredAsyncBillingReceiptsPage(
		ctx, model.AsyncBillingReceiptRetentionCutoff(now), cursor.ReceiptCleanupAfterID,
		asyncBillingRecoveryBatchSize,
	)
	if err != nil {
		if ctx.Err() != nil {
			return summary
		}
		summary.Errors++
	} else {
		summary.ExpiredReceiptsDeleted = cleanupPage.Deleted
		summary.NextReceiptCleanupAfterID = cleanupPage.NextID
	}
	return summary
}

func recoverAcceptedTaskTerminalUsage(ctx context.Context, task *model.Task) (*int, int, error) {
	if task == nil || task.Status == model.TaskStatusFailure {
		return nil, 0, nil
	}
	billingContext := task.EffectiveBillingContext()
	if billingContext == nil || billingContext.PerCallBilling {
		return nil, 0, nil
	}
	if GetTaskAdaptorFunc == nil {
		return nil, 0, errors.New("task adaptor registry is unavailable for terminal billing recovery")
	}
	adaptor := GetTaskAdaptorFunc(task.Platform)
	if adaptor == nil {
		return nil, 0, errors.New("task adaptor is unavailable for terminal billing recovery")
	}
	result, err := adaptor.ParseTaskResult(ctx, task.Data)
	if err != nil {
		return nil, 0, fmt.Errorf("recover terminal task usage: %w", err)
	}
	if result == nil {
		return nil, 0, errors.New("terminal task usage is missing")
	}
	if adjusted := adaptor.AdjustBillingOnComplete(task, result); adjusted > 0 {
		if adjusted > common.MaxQuota {
			return nil, 0, errors.New("terminal task adjusted quota exceeds the supported range")
		}
		return &adjusted, result.TotalTokens, nil
	}
	if result.TotalTokens <= 0 {
		return nil, 0, errors.New("terminal task usage is missing")
	}
	return nil, result.TotalTokens, nil
}
