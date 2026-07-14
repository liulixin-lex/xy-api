package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AsyncBillingManualResolutionRejected = "confirmed_rejected"
	AsyncBillingManualResolutionAccepted = "confirmed_accepted"

	AsyncBillingProviderStatusAccepted         = "confirmed_accepted"
	AsyncBillingProviderStatusRejected         = "confirmed_rejected"
	AsyncBillingProviderStatusNotFound         = "confirmed_not_found"
	AsyncBillingProviderStatusTerminalVerified = "terminal_usage_verified"

	asyncBillingReceiptRetentionEnv  = "ASYNC_BILLING_RECEIPT_RETENTION_DAYS"
	asyncBillingReceiptRetentionDays = 365
)

var (
	ErrAsyncBillingManualDecisionInvalid      = errors.New("async billing manual decision is invalid")
	ErrAsyncBillingManualDecisionConflict     = errors.New("async billing manual decision conflicts with the durable receipt")
	ErrAsyncBillingManualDecisionPrecondition = errors.New("async billing manual review precondition failed")
	ErrAsyncBillingManualDecisionBlocked      = errors.New("async billing manual decision is blocked by incomplete evidence")
)

type AsyncBillingReceiptCleanupPage struct {
	NextID  int64
	Scanned int
	Deleted int64
	Done    bool
}

type AsyncBillingManualResolution struct {
	ID                  int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	ReservationID       int64  `json:"reservation_id" gorm:"not null;uniqueIndex:uidx_async_billing_manual_reservation,priority:1"`
	Action              string `json:"action" gorm:"type:varchar(32);not null"`
	ReviewKind          string `json:"review_kind" gorm:"type:varchar(32);not null"`
	ActorUserID         int    `json:"actor_user_id" gorm:"not null;index"`
	ExpectedState       string `json:"expected_state" gorm:"type:varchar(24);not null"`
	ExpectedVersion     int64  `json:"expected_version" gorm:"not null;uniqueIndex:uidx_async_billing_manual_reservation,priority:2"`
	ExpectedETag        string `json:"expected_etag" gorm:"type:varchar(128);not null"`
	UpstreamTaskID      string `json:"upstream_task_id,omitempty" gorm:"type:varchar(191)"`
	ProviderStatus      string `json:"provider_status" gorm:"type:varchar(64);not null"`
	ProviderCheckedMs   int64  `json:"provider_checked_ms" gorm:"not null"`
	EvidenceReference   string `json:"evidence_reference" gorm:"type:varchar(512);not null"`
	Reason              string `json:"reason" gorm:"type:varchar(1024);not null"`
	BeforeState         string `json:"before_state" gorm:"type:varchar(24);not null"`
	AfterState          string `json:"after_state" gorm:"type:varchar(24);not null"`
	BeforeQuota         int    `json:"before_quota" gorm:"not null"`
	AfterQuota          int    `json:"after_quota" gorm:"not null"`
	QuotaDelta          int    `json:"quota_delta" gorm:"not null"`
	DecisionKeyHash     string `json:"-" gorm:"type:varchar(64);not null;uniqueIndex:uidx_async_billing_manual_decision"`
	DecisionPayloadHash string `json:"-" gorm:"type:varchar(64);not null"`
	CreatedTimeMs       int64  `json:"created_time_ms" gorm:"not null;index"`
	ResolvedTimeMs      int64  `json:"resolved_time_ms" gorm:"not null"`
}

type AsyncBillingManualDecisionSpec struct {
	ReservationID       int64
	Action              string
	ActorUserID         int
	ExpectedVersion     int64
	ExpectedETag        string
	UpstreamTaskID      string
	ProviderStatus      string
	ProviderCheckedMs   int64
	EvidenceReference   string
	Reason              string
	DecisionKeyHash     string
	DecisionPayloadHash string
}

type AsyncBillingManualDecisionResult struct {
	Reservation AsyncBillingReservation      `json:"reservation"`
	Resolution  AsyncBillingManualResolution `json:"resolution"`
}

type AsyncBillingManualReviewAttempt struct {
	AttemptIndex   int    `json:"attempt_index"`
	State          string `json:"state"`
	ChannelID      int    `json:"channel_id"`
	CredentialID   int    `json:"credential_id"`
	ChannelVersion string `json:"channel_version"`
	AuthorizedMs   int64  `json:"authorized_ms"`
	SendDeadlineMs int64  `json:"send_deadline_ms"`
	ResolvedMs     int64  `json:"resolved_ms,omitempty"`
}

type AsyncBillingManualReviewConsequences struct {
	CurrentCharge          int `json:"current_charge"`
	AcceptAdditionalCharge int `json:"accept_additional_charge"`
	AcceptFinalCharge      int `json:"accept_final_charge"`
	RejectRefund           int `json:"reject_refund"`
	RejectFinalCharge      int `json:"reject_final_charge"`
	RejectWriteOff         int `json:"reject_write_off"`
}

type AsyncBillingManualReviewItem struct {
	ReservationID       int64                                `json:"reservation_id"`
	Kind                string                               `json:"kind"`
	ReviewKind          string                               `json:"review_kind"`
	PublicTaskID        string                               `json:"public_task_id"`
	UpstreamTaskID      string                               `json:"upstream_task_id,omitempty"`
	UserID              int                                  `json:"user_id"`
	State               string                               `json:"state"`
	CurrentQuota        int                                  `json:"current_quota"`
	AcceptedQuota       int                                  `json:"accepted_quota"`
	ReviewVersion       int64                                `json:"review_version"`
	ETag                string                               `json:"etag"`
	ManualReviewSinceMs int64                                `json:"manual_review_since_ms"`
	Reason              string                               `json:"reason"`
	CanAccept           bool                                 `json:"can_accept"`
	CanReject           bool                                 `json:"can_reject"`
	Blockers            []string                             `json:"blockers"`
	Consequences        AsyncBillingManualReviewConsequences `json:"financial_consequences"`
	Attempts            []AsyncBillingManualReviewAttempt    `json:"attempts"`
}

type AsyncBillingManualReviewPage struct {
	Items      []AsyncBillingManualReviewItem `json:"items"`
	NextCursor int64                          `json:"next_cursor,omitempty"`
	HasMore    bool                           `json:"has_more"`
}

type AsyncBillingTerminalDrift struct {
	Reservation AsyncBillingReservation
	Task        *Task
	Midjourney  *Midjourney
}

// FindAcceptedAsyncBillingTerminalDrifts finds v2 rows that an old binary
// moved to terminal state without creating the v2 terminal operation.
func FindAcceptedAsyncBillingTerminalDrifts(ctx context.Context, limit int) ([]AsyncBillingTerminalDrift, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	drifts := make([]AsyncBillingTerminalDrift, 0, limit)
	cursor := int64(0)
	for len(drifts) < limit {
		var reservations []AsyncBillingReservation
		if err := DB.WithContext(ctx).Where("state = ? AND id > ?", AsyncBillingReservationStateAccepted, cursor).
			Where("task_id > 0 OR midjourney_id > 0").Order("id asc").Limit(limit).Find(&reservations).Error; err != nil {
			return nil, err
		}
		if len(reservations) == 0 {
			break
		}
		for index := range reservations {
			reservation := reservations[index]
			cursor = reservation.ID
			switch reservation.Kind {
			case AsyncBillingKindTask:
				var task Task
				if err := DB.WithContext(ctx).Where("id = ?", reservation.TaskID).First(&task).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						common.SysError(fmt.Sprintf("async billing terminal drift task is missing: reservation=%d", reservation.ID))
						continue
					}
					return nil, err
				}
				if task.PrivateData.AsyncBillingReservationID != reservation.ID || task.UserId != reservation.UserID {
					common.SysError(fmt.Sprintf("async billing terminal drift task invariant failed: reservation=%d", reservation.ID))
					continue
				}
				if task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure {
					drifts = append(drifts, AsyncBillingTerminalDrift{Reservation: reservation, Task: &task})
				}
			case AsyncBillingKindMidjourney:
				var task Midjourney
				if err := DB.WithContext(ctx).Where("id = ?", reservation.MidjourneyID).First(&task).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						common.SysError(fmt.Sprintf("async billing terminal drift midjourney task is missing: reservation=%d", reservation.ID))
						continue
					}
					return nil, err
				}
				if task.AsyncBillingReservationID != reservation.ID || task.UserId != reservation.UserID {
					common.SysError(fmt.Sprintf("async billing terminal drift midjourney invariant failed: reservation=%d", reservation.ID))
					continue
				}
				if task.Progress == "100%" && (task.Status == "SUCCESS" || task.Status == "FAILURE") {
					drifts = append(drifts, AsyncBillingTerminalDrift{Reservation: reservation, Midjourney: &task})
				}
			default:
				common.SysError(fmt.Sprintf("async billing terminal drift kind is invalid: reservation=%d", reservation.ID))
			}
			if len(drifts) == limit {
				break
			}
		}
		if len(reservations) < limit {
			break
		}
	}
	return drifts, nil
}

func completedBillingProjectionEvidenceTx(
	tx *gorm.DB,
	kind string,
	referenceID int64,
	operationKey string,
	statsRequired bool,
	logRequired bool,
	statsQuotaDelta int,
	statsRequestDelta int,
) (bool, error) {
	if tx == nil || referenceID <= 0 || strings.TrimSpace(operationKey) == "" {
		return false, ErrAsyncBillingReservationInvariant
	}
	var stats []BillingStatsProjection
	if err := lockForUpdate(tx).Where("kind = ? AND reference_id = ?", kind, referenceID).
		Limit(2).Find(&stats).Error; err != nil {
		return false, err
	}
	if len(stats) != 0 {
		if !statsRequired || len(stats) != 1 || stats[0].ProtocolVersion != BillingStatsProjectionProtocol ||
			stats[0].OperationKey != operationKey || stats[0].State != BillingStatsProjectionStateCompleted ||
			stats[0].QuotaDelta != statsQuotaDelta || stats[0].RequestDelta != statsRequestDelta {
			return false, nil
		}
	} else if statsRequired {
		return false, nil
	}
	var logs []BillingLogProjection
	if err := lockForUpdate(tx).Where("kind = ? AND reference_id = ?", kind, referenceID).
		Limit(2).Find(&logs).Error; err != nil {
		return false, err
	}
	if len(logs) != 0 {
		if !logRequired || len(logs) != 1 || logs[0].ProtocolVersion != BillingLogProjectionProtocol ||
			logs[0].OperationKey != operationKey || logs[0].State != BillingLogProjectionStateCompleted {
			return false, nil
		}
	} else if logRequired {
		return false, nil
	}
	return true, nil
}

// CleanupExpiredAsyncBillingReceipts removes old source receipts only after
// every financial, cache, stats, and log stage is durably complete.
func CleanupExpiredAsyncBillingReceiptsPage(
	ctx context.Context,
	cutoff time.Time,
	afterID int64,
	limit int,
) (AsyncBillingReceiptCleanupPage, error) {
	page := AsyncBillingReceiptCleanupPage{NextID: afterID}
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoff.IsZero() || afterID < 0 {
		return page, ErrAsyncBillingReservationInvariant
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var ids []int64
	query := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("state IN ? AND terminal_time_ms > 0 AND terminal_time_ms <= ?", []string{
			AsyncBillingReservationStateReleased, AsyncBillingReservationStateTerminal,
		}, cutoff.UnixMilli()).Where("id > ?", afterID).Order("id asc").Limit(limit)
	if err := query.Pluck("id", &ids).Error; err != nil {
		return page, err
	}
	page.Scanned = len(ids)
	page.Done = len(ids) < limit
	if len(ids) > 0 {
		page.NextID = ids[len(ids)-1]
	}
	for _, reservationID := range ids {
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		removed := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var reservation AsyncBillingReservation
			if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if reservation.TerminalTimeMs <= 0 || reservation.TerminalTimeMs > cutoff.UnixMilli() ||
				(reservation.State != AsyncBillingReservationStateReleased &&
					reservation.State != AsyncBillingReservationStateTerminal) || reservation.CacheSyncPending {
				return nil
			}
			if reservation.State == AsyncBillingReservationStateTerminal &&
				reservation.AcceptedProjectionState != AsyncBillingAcceptedProjectionCompleted {
				return nil
			}

			var taskOperations []TaskBillingOperation
			if err := lockForUpdate(tx).Where("reservation_id = ?", reservation.ID).
				Limit(2).Find(&taskOperations).Error; err != nil {
				return err
			}
			var midjourneyOperations []MidjourneyBillingOperation
			if err := lockForUpdate(tx).Where("reservation_id = ?", reservation.ID).
				Limit(2).Find(&midjourneyOperations).Error; err != nil {
				return err
			}
			if reservation.State == AsyncBillingReservationStateReleased {
				if len(taskOperations) != 0 || len(midjourneyOperations) != 0 {
					return nil
				}
				var acceptedProjectionCount int64
				if err := tx.Model(&BillingStatsProjection{}).
					Where("kind = ? AND reference_id = ?", BillingStatsProjectionKindAccepted, reservation.ID).
					Count(&acceptedProjectionCount).Error; err != nil {
					return err
				}
				if acceptedProjectionCount != 0 {
					return nil
				}
				if err := tx.Model(&BillingLogProjection{}).
					Where("kind = ? AND reference_id = ?", BillingLogProjectionKindAccepted, reservation.ID).
					Count(&acceptedProjectionCount).Error; err != nil {
					return err
				}
				if acceptedProjectionCount != 0 {
					return nil
				}
			} else {
				if reservation.AcceptedProjectionKey == nil {
					return nil
				}
				acceptedEvidence, err := completedBillingProjectionEvidenceTx(
					tx, BillingStatsProjectionKindAccepted, reservation.ID,
					strings.TrimSpace(*reservation.AcceptedProjectionKey), true, true,
					reservation.AcceptedProjectionQuota, reservation.AcceptedProjectionRequestDelta,
				)
				if err != nil {
					return err
				}
				if !acceptedEvidence {
					return nil
				}
				switch reservation.Kind {
				case AsyncBillingKindTask:
					if reservation.TaskID <= 0 || len(taskOperations) != 1 || len(midjourneyOperations) != 0 {
						return nil
					}
					operation := &taskOperations[0]
					if operation.TaskID != reservation.TaskID || operation.State != TaskBillingOperationStateCompleted ||
						operation.OperationKey == "" || (operation.LogState != TaskBillingOperationLogWritten &&
						operation.LogState != TaskBillingOperationLogNotRequired) {
						return nil
					}
					terminalEvidence, err := completedBillingProjectionEvidenceTx(
						tx, BillingStatsProjectionKindTaskTerminal, operation.ID, operation.OperationKey,
						operation.QuotaDelta > 0, operation.QuotaDelta != 0,
						operation.QuotaDelta, 0,
					)
					if err != nil {
						return err
					}
					if !terminalEvidence {
						return nil
					}
				case AsyncBillingKindMidjourney:
					if reservation.MidjourneyID <= 0 || len(midjourneyOperations) != 1 || len(taskOperations) != 0 {
						return nil
					}
					operation := &midjourneyOperations[0]
					validNoop := operation.Kind == TaskBillingOperationKindNoop && operation.RefundQuota == 0 &&
						(operation.TerminalStatus == "SUCCESS" || operation.TerminalStatus == "FAILURE")
					validRefund := operation.Kind == TaskBillingOperationKindRefund && operation.RefundQuota > 0 &&
						operation.TerminalStatus == "FAILURE"
					if operation.MidjourneyID != reservation.MidjourneyID ||
						operation.State != TaskBillingOperationStateCompleted || operation.OperationKey == "" ||
						(!validNoop && !validRefund) ||
						(operation.LogState != TaskBillingOperationLogWritten &&
							operation.LogState != TaskBillingOperationLogNotRequired) {
						return nil
					}
					terminalEvidence, err := completedBillingProjectionEvidenceTx(
						tx, BillingStatsProjectionKindMidjourneyTerminal, int64(operation.ID),
						operation.OperationKey, false, validRefund, 0, 0,
					)
					if err != nil {
						return err
					}
					if !terminalEvidence {
						return nil
					}
				default:
					return nil
				}
			}

			if reservation.TaskID > 0 {
				var task Task
				if err := tx.Where("id = ?", reservation.TaskID).First(&task).Error; err != nil {
					return err
				}
				if task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure {
					return nil
				}
				privateData := task.PrivateData
				privateData.BillingProtocolVersion = TaskBillingLegacyProtocolVersion
				privateData.AsyncBillingReservationID = 0
				if privateData.DurableBillingContext != nil {
					privateData.BillingContext = privateData.DurableBillingContext
					privateData.DurableBillingContext = nil
				}
				privatePayload, err := common.Marshal(privateData)
				if err != nil {
					return err
				}
				updated := tx.Model(&Task{}).Where(
					"id = ? AND status IN ? AND durable_quota = ?", task.ID,
					[]TaskStatus{TaskStatusSuccess, TaskStatusFailure}, task.DurableQuota,
				).Updates(map[string]any{
					"quota": task.DurableQuota, "durable_quota": 0, "private_data": privatePayload,
					"durable_private_data": []byte(nil), "durable_private_data_hash": "",
				})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrAsyncBillingReservationInvariant
				}
			}
			if reservation.MidjourneyID > 0 {
				var task Midjourney
				if err := tx.Where("id = ?", reservation.MidjourneyID).First(&task).Error; err != nil {
					return err
				}
				if task.Progress != "100%" || (task.Status != "SUCCESS" && task.Status != "FAILURE") {
					return nil
				}
				updated := tx.Model(&Midjourney{}).Where(
					"id = ? AND progress = ? AND durable_quota = ?", task.Id, "100%", task.DurableQuota,
				).Updates(map[string]any{
					"quota": task.DurableQuota, "durable_quota": 0,
					"billing_protocol_version":     TaskBillingLegacyProtocolVersion,
					"async_billing_reservation_id": 0,
				})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrAsyncBillingReservationInvariant
				}
			}

			if err := tx.Where("reservation_id = ?", reservation.ID).Delete(&AsyncBillingManualResolution{}).Error; err != nil {
				return err
			}
			if err := tx.Where("reservation_id = ?", reservation.ID).Delete(&AsyncBillingAttempt{}).Error; err != nil {
				return err
			}
			if err := tx.Where("reservation_id = ?", reservation.ID).Delete(&TaskBillingOperation{}).Error; err != nil {
				return err
			}
			if err := tx.Where("reservation_id = ?", reservation.ID).Delete(&MidjourneyBillingOperation{}).Error; err != nil {
				return err
			}
			result := tx.Where("id = ? AND state = ? AND terminal_time_ms = ?", reservation.ID,
				reservation.State, reservation.TerminalTimeMs).Delete(&AsyncBillingReservation{})
			if result.Error != nil {
				return result.Error
			}
			removed = result.RowsAffected == 1
			return nil
		})
		if err != nil {
			return page, err
		}
		if removed {
			page.Deleted++
		}
	}
	if page.Done {
		page.NextID = 0
	}
	return page, nil
}

func CleanupExpiredAsyncBillingReceipts(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	page, err := CleanupExpiredAsyncBillingReceiptsPage(ctx, cutoff, 0, limit)
	return page.Deleted, err
}

func AsyncBillingManualReviewETag(reservationID, version int64) string {
	return fmt.Sprintf("\"async-billing-review-%d-v%d\"", reservationID, version)
}

func thawAsyncBillingReviewAudit(reservation *AsyncBillingReservation) (*AsyncBillingAcceptedAuditSnapshot, error) {
	if reservation == nil || reservation.ReviewAuditProtocol != AsyncBillingAcceptanceIntentProtocol ||
		len(reservation.ReviewAuditPayload) == 0 || len(reservation.ReviewAuditPayload) > maxAsyncBillingIntentBytes ||
		!validAsyncBillingHash(reservation.ReviewAuditHash) {
		return nil, ErrAsyncBillingReservationInvariant
	}
	digest := sha256.Sum256(reservation.ReviewAuditPayload)
	if !strings.EqualFold(reservation.ReviewAuditHash, hex.EncodeToString(digest[:])) {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var audit AsyncBillingAcceptedAuditSnapshot
	if err := common.Unmarshal(reservation.ReviewAuditPayload, &audit); err != nil ||
		validateAsyncBillingAuditSnapshot(audit) != nil {
		return nil, ErrAsyncBillingReservationInvariant
	}
	return &audit, nil
}

func ListAsyncBillingManualReviewPage(
	ctx context.Context,
	cursor int64,
	limit int,
	canResolve bool,
) (*AsyncBillingManualReviewPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cursor < 0 {
		return nil, ErrAsyncBillingManualDecisionInvalid
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var reservations []AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("state = ? AND id > ?", AsyncBillingReservationStateManualReview, cursor).
		Order("id asc").Limit(limit + 1).Find(&reservations).Error; err != nil {
		return nil, err
	}
	hasMore := len(reservations) > limit
	if hasMore {
		reservations = reservations[:limit]
	}
	page := &AsyncBillingManualReviewPage{
		Items: make([]AsyncBillingManualReviewItem, 0, len(reservations)), HasMore: hasMore,
	}
	if len(reservations) == 0 {
		return page, nil
	}
	reservationIDs := make([]int64, 0, len(reservations))
	for index := range reservations {
		reservationIDs = append(reservationIDs, reservations[index].ID)
	}
	var attempts []AsyncBillingAttempt
	if err := DB.WithContext(ctx).Where("reservation_id IN ?", reservationIDs).
		Order("reservation_id asc, attempt_index asc").Find(&attempts).Error; err != nil {
		return nil, err
	}
	attemptsByReservation := make(map[int64][]AsyncBillingAttempt, len(reservations))
	for index := range attempts {
		attempt := attempts[index]
		attemptsByReservation[attempt.ReservationID] = append(attemptsByReservation[attempt.ReservationID], attempt)
	}
	var operations []TaskBillingOperation
	if err := DB.WithContext(ctx).Where("reservation_id IN ? AND state = ?", reservationIDs, TaskBillingOperationStateManualReview).
		Find(&operations).Error; err != nil {
		return nil, err
	}
	operationsByReservation := make(map[int64]TaskBillingOperation, len(operations))
	for index := range operations {
		operationsByReservation[operations[index].ReservationID] = operations[index]
	}
	for index := range reservations {
		reservation := &reservations[index]
		item := AsyncBillingManualReviewItem{
			ReservationID: reservation.ID, Kind: reservation.Kind, ReviewKind: reservation.ManualReviewKind,
			PublicTaskID: reservation.PublicTaskID, UpstreamTaskID: reservation.UpstreamTaskID,
			UserID: reservation.UserID, State: reservation.State, CurrentQuota: reservation.CurrentQuota,
			AcceptedQuota: reservation.AcceptedQuota, ReviewVersion: reservation.ReviewVersion,
			ETag:                AsyncBillingManualReviewETag(reservation.ID, reservation.ReviewVersion),
			ManualReviewSinceMs: reservation.ManualReviewRequiredMs, Reason: reservation.ManualReviewReason,
			Blockers: make([]string, 0), Attempts: make([]AsyncBillingManualReviewAttempt, 0),
			Consequences: AsyncBillingManualReviewConsequences{
				CurrentCharge: reservation.CurrentQuota, AcceptFinalCharge: reservation.CurrentQuota,
			},
		}
		if !canResolve {
			item.Blockers = append(item.Blockers, "resolve_permission_required")
		}
		authorizedAttempts := 0
		activeSendLease := false
		intentValid := false
		reviewAuditValid := false
		for attemptIndex := range attemptsByReservation[reservation.ID] {
			attempt := &attemptsByReservation[reservation.ID][attemptIndex]
			item.Attempts = append(item.Attempts, AsyncBillingManualReviewAttempt{
				AttemptIndex: attempt.AttemptIndex, State: attempt.State, ChannelID: attempt.ChannelID,
				CredentialID: attempt.CredentialID, ChannelVersion: attempt.ChannelVersion,
				AuthorizedMs: attempt.AuthorizedMs, SendDeadlineMs: attempt.SendDeadlineMs,
				ResolvedMs: attempt.ResolvedMs,
			})
			if attempt.State == AsyncBillingAttemptStateAuthorized {
				authorizedAttempts++
				activeSendLease = activeSendLease || attempt.SendDeadlineMs > time.Now().UnixMilli()
				_, intentErr := thawAsyncBillingAcceptanceIntent(attempt)
				intentValid = intentErr == nil
			}
		}
		if reservation.ManualReviewKind == AsyncBillingReviewKindAcceptanceOverage ||
			reservation.ManualReviewKind == AsyncBillingReviewKindAcceptedHandoff {
			_, reviewAuditErr := thawAsyncBillingReviewAudit(reservation)
			reviewAuditValid = reviewAuditErr == nil
		}
		switch reservation.ManualReviewKind {
		case AsyncBillingReviewKindSendOutcome:
			item.Consequences.RejectRefund = reservation.CurrentQuota
			if authorizedAttempts != 1 {
				item.Blockers = append(item.Blockers, "authorized_attempt_missing_or_ambiguous")
			}
			if !intentValid {
				item.Blockers = append(item.Blockers, "acceptance_intent_missing_or_invalid")
			}
			if activeSendLease {
				item.Blockers = append(item.Blockers, "submission_send_lease_active")
			}
			item.CanAccept = canResolve && authorizedAttempts == 1 && intentValid
			item.CanReject = canResolve && authorizedAttempts == 1 && !activeSendLease
		case AsyncBillingReviewKindAcceptanceOverage:
			if authorizedAttempts != 1 || !intentValid {
				item.Blockers = append(item.Blockers, "acceptance_intent_missing_or_invalid")
			}
			if reservation.ReviewTargetQuota <= reservation.CurrentQuota || !reviewAuditValid {
				item.Blockers = append(item.Blockers, "acceptance_overage_context_missing_or_invalid")
			} else {
				overage := reservation.ReviewTargetQuota - reservation.CurrentQuota
				item.Consequences.AcceptAdditionalCharge = overage
				item.Consequences.AcceptFinalCharge = reservation.ReviewTargetQuota
				item.Consequences.RejectFinalCharge = reservation.CurrentQuota
				item.Consequences.RejectWriteOff = overage
			}
			item.CanAccept = canResolve && authorizedAttempts == 1 && intentValid && reviewAuditValid &&
				reservation.ReviewTargetQuota > reservation.CurrentQuota
			item.CanReject = item.CanAccept
		case AsyncBillingReviewKindAcceptedHandoff:
			if authorizedAttempts != 1 || !intentValid {
				item.Blockers = append(item.Blockers, "acceptance_intent_missing_or_invalid")
			}
			acceptedHandoffContextValid := reservation.UpstreamTaskID != "" && reviewAuditValid &&
				reservation.ReviewTargetQuota >= 0 && reservation.ReviewTargetQuota <= common.MaxQuota
			if !acceptedHandoffContextValid {
				item.Blockers = append(item.Blockers, "accepted_handoff_context_missing_or_invalid")
			} else {
				item.Consequences.AcceptAdditionalCharge = reservation.ReviewTargetQuota - reservation.CurrentQuota
				item.Consequences.AcceptFinalCharge = reservation.ReviewTargetQuota
				item.Consequences.RejectFinalCharge = reservation.CurrentQuota
			}
			item.CanAccept = canResolve && authorizedAttempts == 1 && intentValid && reviewAuditValid &&
				acceptedHandoffContextValid
			item.CanReject = false
		case AsyncBillingReviewKindTerminalOverage:
			operation, ok := operationsByReservation[reservation.ID]
			if !ok || operation.TargetQuota <= reservation.CurrentQuota {
				item.Blockers = append(item.Blockers, "terminal_billing_operation_missing_or_invalid")
			} else {
				overage := operation.TargetQuota - reservation.CurrentQuota
				item.Consequences.AcceptAdditionalCharge = overage
				item.Consequences.AcceptFinalCharge = operation.TargetQuota
				item.Consequences.RejectFinalCharge = reservation.CurrentQuota
				item.Consequences.RejectWriteOff = overage
				item.CanAccept = canResolve
				item.CanReject = canResolve
			}
		case AsyncBillingReviewKindTerminalUsage:
			item.Consequences.AcceptFinalCharge = reservation.CurrentQuota
			item.Consequences.RejectFinalCharge = reservation.CurrentQuota
			item.CanAccept = canResolve
			item.CanReject = false
		default:
			item.Blockers = append(item.Blockers, "unknown_review_kind")
		}
		page.Items = append(page.Items, item)
		page.NextCursor = reservation.ID
	}
	return page, nil
}

func ListAsyncBillingManualReviewItems(limit int) ([]AsyncBillingManualReviewItem, error) {
	page, err := ListAsyncBillingManualReviewPage(context.Background(), 0, limit, true)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func AsyncBillingManualReviewStatsContext(ctx context.Context) (int64, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db := DB.WithContext(ctx)
	var count int64
	if err := db.Model(&AsyncBillingReservation{}).
		Where("state = ?", AsyncBillingReservationStateManualReview).Count(&count).Error; err != nil {
		return 0, 0, err
	}
	var oldest int64
	if count > 0 {
		if err := db.Model(&AsyncBillingReservation{}).
			Where("state = ?", AsyncBillingReservationStateManualReview).
			Select("COALESCE(MIN(manual_review_required_ms), 0)").Scan(&oldest).Error; err != nil {
			return 0, 0, err
		}
	}
	return count, oldest, nil
}

func AsyncBillingManualReviewStats() (int64, int64, error) {
	return AsyncBillingManualReviewStatsContext(context.Background())
}

func HasAsyncBillingRecoveryWorkContext(ctx context.Context, now time.Time) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if DB == nil || ctx.Err() != nil {
		return false
	}
	db := DB.WithContext(ctx)
	if HasPendingIdentityCacheSyncContext(ctx, now) {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	var reservation AsyncBillingReservation
	primaryWork := db.Model(&AsyncBillingReservation{}).
		Select("id").
		Where("state IN ? OR cache_sync_pending = ? OR accepted_projection_state IN ?",
			[]string{AsyncBillingReservationStateReserved, AsyncBillingReservationStateSendAuthorized}, true,
			[]string{AsyncBillingAcceptedProjectionPending, AsyncBillingAcceptedProjectionLogPending}).
		Limit(1).Find(&reservation)
	if primaryWork.Error == nil && primaryWork.RowsAffected > 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	var driftID int64
	taskDrift := db.Table("async_billing_reservations AS reservations").
		Select("reservations.id").
		Joins("JOIN tasks ON tasks.id = reservations.task_id").
		Where("reservations.state = ? AND reservations.kind = ?", AsyncBillingReservationStateAccepted, AsyncBillingKindTask).
		Where("tasks.status IN ?", []TaskStatus{TaskStatusSuccess, TaskStatusFailure}).
		Limit(1).Scan(&driftID)
	if taskDrift.Error == nil && taskDrift.RowsAffected > 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	driftID = 0
	midjourneyDrift := db.Table("async_billing_reservations AS reservations").
		Select("reservations.id").
		Joins("JOIN midjourneys ON midjourneys.id = reservations.midjourney_id").
		Where("reservations.state = ? AND reservations.kind = ?", AsyncBillingReservationStateAccepted, AsyncBillingKindMidjourney).
		Where("midjourneys.progress = ? AND midjourneys.status IN ?", "100%", []string{"SUCCESS", "FAILURE"}).
		Limit(1).Scan(&driftID)
	if midjourneyDrift.Error == nil && midjourneyDrift.RowsAffected > 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if db.Migrator().HasTable(&TaskBillingOperation{}) && db.Migrator().HasTable(&Task{}) {
		var repairID int64
		auditBeforeMs := now.Add(-asyncTerminalSnapshotAuditInterval).UnixMilli()
		repair := db.Table("task_billing_operations AS operations").Select("operations.id").
			Joins("LEFT JOIN tasks ON tasks.id = operations.task_id").
			Where("operations.last_error LIKE ? OR operations.terminal_payload_protocol != ? OR "+
				"operations.terminal_payload_hash = ? OR operations.terminal_payload IS NULL OR "+
				"LENGTH(operations.terminal_payload) = 0 OR tasks.id IS NULL OR "+
				"tasks.status != operations.terminal_status OR tasks.progress != ? OR "+
				"(operations.state = ? AND operations.updated_time_ms <= ?)",
				terminalSnapshotRepairError+"%", asyncTerminalPayloadProtocol, "", "100%",
				TaskBillingOperationStateCompleted, auditBeforeMs).
			Limit(1).Scan(&repairID)
		if repair.Error == nil && repair.RowsAffected > 0 {
			return true
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if db.Migrator().HasTable(&MidjourneyBillingOperation{}) && db.Migrator().HasTable(&Midjourney{}) {
		var repairID int64
		auditBeforeMs := now.Add(-asyncTerminalSnapshotAuditInterval).UnixMilli()
		repair := db.Table("midjourney_billing_operations AS operations").Select("operations.id").
			Joins("LEFT JOIN midjourneys ON midjourneys.id = operations.midjourney_id").
			Where("operations.last_error LIKE ? OR operations.terminal_payload_protocol != ? OR "+
				"operations.terminal_payload_hash = ? OR operations.terminal_payload IS NULL OR "+
				"LENGTH(operations.terminal_payload) = 0 OR midjourneys.id IS NULL OR "+
				"midjourneys.status != operations.terminal_status OR midjourneys.progress != ? OR "+
				"(operations.state = ? AND operations.updated_time_ms <= ?)",
				terminalSnapshotRepairError+"%", asyncTerminalPayloadProtocol, "", "100%",
				TaskBillingOperationStateCompleted, auditBeforeMs).
			Limit(1).Scan(&repairID)
		if repair.Error == nil && repair.RowsAffected > 0 {
			return true
		}
	}
	if ctx.Err() != nil {
		return false
	}
	cutoff := AsyncBillingReceiptRetentionCutoff(now)
	var expired AsyncBillingReservation
	expiredReceipt := db.Model(&AsyncBillingReservation{}).Select("id").
		Where("state IN ? AND terminal_time_ms > 0 AND terminal_time_ms <= ?", []string{
			AsyncBillingReservationStateReleased, AsyncBillingReservationStateTerminal,
		}, cutoff.UnixMilli()).Limit(1).Find(&expired)
	if expiredReceipt.Error == nil && expiredReceipt.RowsAffected > 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	var projectionID int64
	statsProjection := db.Model(&BillingStatsProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingStatsProjectionStatePending, now.UnixMilli(),
			BillingStatsProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(1).Pluck("id", &projectionID)
	if statsProjection.Error == nil && projectionID > 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	projectionID = 0
	logProjection := db.Model(&BillingLogProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingLogProjectionStatePending, now.UnixMilli(),
			BillingLogProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(1).Pluck("id", &projectionID)
	return logProjection.Error == nil && projectionID > 0
}

func HasAsyncBillingRecoveryWork(now time.Time) bool {
	return HasAsyncBillingRecoveryWorkContext(context.Background(), now)
}

func AsyncBillingReceiptRetentionCutoff(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	retentionDays := common.GetEnvOrDefault(asyncBillingReceiptRetentionEnv, asyncBillingReceiptRetentionDays)
	if retentionDays < 30 {
		retentionDays = 30
	} else if retentionDays > 3650 {
		retentionDays = 3650
	}
	return now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
}

func validateAsyncBillingManualDecision(spec *AsyncBillingManualDecisionSpec, now time.Time) error {
	if spec == nil || spec.ReservationID <= 0 || spec.ActorUserID <= 0 || spec.ExpectedVersion <= 0 ||
		(spec.Action != AsyncBillingManualResolutionAccepted && spec.Action != AsyncBillingManualResolutionRejected) ||
		!validAsyncBillingHash(strings.ToLower(strings.TrimSpace(spec.DecisionKeyHash))) ||
		!validAsyncBillingHash(strings.ToLower(strings.TrimSpace(spec.DecisionPayloadHash))) {
		return ErrAsyncBillingManualDecisionInvalid
	}
	spec.ExpectedETag = strings.TrimSpace(spec.ExpectedETag)
	spec.UpstreamTaskID = strings.TrimSpace(spec.UpstreamTaskID)
	spec.ProviderStatus = strings.TrimSpace(spec.ProviderStatus)
	spec.EvidenceReference = strings.TrimSpace(spec.EvidenceReference)
	spec.Reason = boundedAsyncBillingError(spec.Reason)
	if spec.ExpectedETag != AsyncBillingManualReviewETag(spec.ReservationID, spec.ExpectedVersion) ||
		strings.TrimSpace(spec.Reason) == "" || len(spec.UpstreamTaskID) > 191 ||
		len(spec.ProviderStatus) == 0 || len(spec.ProviderStatus) > 64 ||
		len(spec.EvidenceReference) == 0 || len(spec.EvidenceReference) > 512 ||
		spec.ProviderCheckedMs <= 0 || spec.ProviderCheckedMs > now.Add(5*time.Minute).UnixMilli() {
		return ErrAsyncBillingManualDecisionInvalid
	}
	for _, value := range []string{spec.UpstreamTaskID, spec.ProviderStatus, spec.EvidenceReference, spec.Reason} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
			return ErrAsyncBillingManualDecisionInvalid
		}
	}
	return nil
}

func sameAsyncBillingManualDecisionReceipt(receipt *AsyncBillingManualResolution, spec AsyncBillingManualDecisionSpec) bool {
	return receipt != nil && receipt.ReservationID == spec.ReservationID && receipt.Action == spec.Action &&
		receipt.ActorUserID == spec.ActorUserID && receipt.ExpectedVersion == spec.ExpectedVersion &&
		receipt.ExpectedETag == spec.ExpectedETag && receipt.UpstreamTaskID == spec.UpstreamTaskID &&
		receipt.ProviderStatus == spec.ProviderStatus && receipt.ProviderCheckedMs == spec.ProviderCheckedMs &&
		receipt.EvidenceReference == spec.EvidenceReference && receipt.Reason == spec.Reason &&
		strings.EqualFold(receipt.DecisionKeyHash, spec.DecisionKeyHash) &&
		strings.EqualFold(receipt.DecisionPayloadHash, spec.DecisionPayloadHash)
}

func ResolveAsyncBillingManualReview(
	ctx context.Context,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) (*AsyncBillingManualDecisionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := validateAsyncBillingManualDecision(&spec, now); err != nil {
		return nil, err
	}
	spec.DecisionKeyHash = strings.ToLower(strings.TrimSpace(spec.DecisionKeyHash))
	spec.DecisionPayloadHash = strings.ToLower(strings.TrimSpace(spec.DecisionPayloadHash))
	result := &AsyncBillingManualDecisionResult{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", spec.ReservationID).First(&result.Reservation).Error; err != nil {
			return err
		}
		var keyed AsyncBillingManualResolution
		keyQuery := lockForUpdate(tx).Where("decision_key_hash = ?", spec.DecisionKeyHash).Limit(1).Find(&keyed)
		if keyQuery.Error != nil {
			return keyQuery.Error
		}
		if keyQuery.RowsAffected > 0 {
			if !strings.EqualFold(keyed.DecisionPayloadHash, spec.DecisionPayloadHash) {
				return ErrAsyncBillingIdempotencyConflict
			}
			result.Resolution = keyed
			return nil
		}
		var existing AsyncBillingManualResolution
		query := lockForUpdate(tx).Where(
			"reservation_id = ? AND expected_version = ?", spec.ReservationID, spec.ExpectedVersion,
		).
			Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if !sameAsyncBillingManualDecisionReceipt(&existing, spec) {
				return ErrAsyncBillingManualDecisionConflict
			}
			result.Resolution = existing
			return nil
		}
		if result.Reservation.State != AsyncBillingReservationStateManualReview ||
			result.Reservation.ReviewVersion != spec.ExpectedVersion ||
			spec.ExpectedETag != AsyncBillingManualReviewETag(result.Reservation.ID, result.Reservation.ReviewVersion) {
			return ErrAsyncBillingManualDecisionPrecondition
		}
		if spec.ProviderCheckedMs < result.Reservation.ManualReviewRequiredMs ||
			spec.ProviderCheckedMs < result.Reservation.SendAuthorizedMs {
			return ErrAsyncBillingManualDecisionPrecondition
		}
		beforeState := result.Reservation.State
		beforeQuota := result.Reservation.CurrentQuota
		reviewKind := result.Reservation.ManualReviewKind
		providerStatusValid := false
		switch reviewKind {
		case AsyncBillingReviewKindSendOutcome:
			if spec.Action == AsyncBillingManualResolutionAccepted {
				providerStatusValid = spec.UpstreamTaskID != "" &&
					spec.ProviderStatus == AsyncBillingProviderStatusAccepted
			} else {
				providerStatusValid = spec.ProviderStatus == AsyncBillingProviderStatusRejected ||
					spec.ProviderStatus == AsyncBillingProviderStatusNotFound
			}
		case AsyncBillingReviewKindAcceptanceOverage:
			providerStatusValid = spec.ProviderStatus == AsyncBillingProviderStatusAccepted
		case AsyncBillingReviewKindAcceptedHandoff:
			providerStatusValid = spec.Action == AsyncBillingManualResolutionAccepted &&
				spec.ProviderStatus == AsyncBillingProviderStatusAccepted
		case AsyncBillingReviewKindTerminalOverage:
			if result.Reservation.UpstreamTaskID == "" || spec.UpstreamTaskID != result.Reservation.UpstreamTaskID {
				return ErrAsyncBillingManualDecisionPrecondition
			}
			providerStatusValid = spec.ProviderStatus == AsyncBillingProviderStatusTerminalVerified
		case AsyncBillingReviewKindTerminalUsage:
			if result.Reservation.UpstreamTaskID == "" || spec.UpstreamTaskID != result.Reservation.UpstreamTaskID {
				return ErrAsyncBillingManualDecisionPrecondition
			}
			providerStatusValid = spec.Action == AsyncBillingManualResolutionAccepted &&
				spec.ProviderStatus == AsyncBillingProviderStatusTerminalVerified
		}
		if !providerStatusValid {
			return ErrAsyncBillingManualDecisionInvalid
		}
		switch reviewKind {
		case AsyncBillingReviewKindSendOutcome:
			if err := resolveAsyncBillingSendOutcomeTx(tx, &result.Reservation, spec, now); err != nil {
				return err
			}
		case AsyncBillingReviewKindAcceptanceOverage:
			if err := resolveAsyncBillingAcceptanceOverageTx(tx, &result.Reservation, spec, now); err != nil {
				return err
			}
		case AsyncBillingReviewKindAcceptedHandoff:
			if err := resolveAsyncBillingAcceptedHandoffTx(tx, &result.Reservation, spec, now); err != nil {
				return err
			}
		case AsyncBillingReviewKindTerminalOverage:
			if err := resolveAsyncBillingTerminalOverageTx(tx, &result.Reservation, spec, now); err != nil {
				return err
			}
		case AsyncBillingReviewKindTerminalUsage:
			if err := resolveAsyncBillingTerminalUsageTx(tx, &result.Reservation, spec, now); err != nil {
				return err
			}
		default:
			return ErrAsyncBillingManualDecisionBlocked
		}
		result.Resolution = AsyncBillingManualResolution{
			ReservationID: result.Reservation.ID, Action: spec.Action,
			ReviewKind: reviewKind, ActorUserID: spec.ActorUserID,
			ExpectedState: beforeState, ExpectedVersion: spec.ExpectedVersion, ExpectedETag: spec.ExpectedETag,
			UpstreamTaskID: spec.UpstreamTaskID, ProviderStatus: spec.ProviderStatus,
			ProviderCheckedMs: spec.ProviderCheckedMs, EvidenceReference: spec.EvidenceReference,
			Reason: spec.Reason, BeforeState: beforeState, AfterState: result.Reservation.State,
			BeforeQuota: beforeQuota, AfterQuota: result.Reservation.CurrentQuota,
			QuotaDelta:      result.Reservation.CurrentQuota - beforeQuota,
			DecisionKeyHash: spec.DecisionKeyHash, DecisionPayloadHash: spec.DecisionPayloadHash,
			CreatedTimeMs: now.UnixMilli(), ResolvedTimeMs: now.UnixMilli(),
		}
		created := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "decision_key_hash"}},
			DoNothing: true,
		}).Create(&result.Resolution)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected != 1 {
			var winner AsyncBillingManualResolution
			if err := lockForUpdate(tx).Where("decision_key_hash = ?", spec.DecisionKeyHash).
				First(&winner).Error; err != nil {
				return err
			}
			if strings.EqualFold(winner.DecisionPayloadHash, spec.DecisionPayloadHash) &&
				winner.ReservationID == spec.ReservationID && winner.ExpectedVersion == spec.ExpectedVersion {
				result.Resolution = winner
				return nil
			}
			return ErrAsyncBillingIdempotencyConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Reservation.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, result.Reservation.ID, now)
	}
	return result, nil
}

func resolveAsyncBillingSendOutcomeTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) error {
	var attempts []AsyncBillingAttempt
	if err := lockForUpdate(tx).Where("reservation_id = ? AND state = ?", reservation.ID, AsyncBillingAttemptStateAuthorized).
		Find(&attempts).Error; err != nil {
		return err
	}
	if len(attempts) != 1 {
		return ErrAsyncBillingManualDecisionBlocked
	}
	attempt := &attempts[0]
	if reservation.UpstreamTaskID != "" && reservation.UpstreamTaskID != spec.UpstreamTaskID {
		return ErrAsyncBillingManualDecisionPrecondition
	}
	if spec.Action == AsyncBillingManualResolutionRejected {
		if attempt.SendDeadlineMs <= 0 || attempt.SendDeadlineMs > now.UnixMilli() {
			return ErrAsyncBillingManualDecisionBlocked
		}
		if spec.ProviderCheckedMs < attempt.SendDeadlineMs {
			return ErrAsyncBillingManualDecisionPrecondition
		}
		if updated := tx.Model(&AsyncBillingAttempt{}).Where("id = ? AND state = ?", attempt.ID, AsyncBillingAttemptStateAuthorized).
			Updates(map[string]any{
				"state": AsyncBillingAttemptStateRejected, "resolved_ms": now.UnixMilli(),
				"failure_code": "admin_confirmed_rejected",
			}); updated.Error != nil {
			return updated.Error
		} else if updated.RowsAffected != 1 {
			return ErrAsyncBillingManualDecisionConflict
		}
		if reservation.CurrentQuota > 0 {
			if err := applyAsyncBillingReservationDeltaTx(tx, reservation, -reservation.CurrentQuota, now); err != nil {
				return err
			}
		}
		updated := tx.Model(&AsyncBillingReservation{}).Where(
			"id = ? AND state = ? AND review_version = ?", reservation.ID,
			AsyncBillingReservationStateManualReview, spec.ExpectedVersion,
		).Updates(map[string]any{
			"state": AsyncBillingReservationStateReleased, "current_quota": 0,
			"terminal_time_ms": now.UnixMilli(), "updated_time_ms": now.UnixMilli(),
			"last_error": spec.Reason, "manual_review_kind": "", "manual_review_reason": "",
			"manual_review_required_ms": 0, "review_version": gorm.Expr("review_version + ?", 1),
			"cache_sync_version": gorm.Expr("cache_sync_version + ?", 1),
			"cache_sync_pending": true, "cache_sync_next_retry_ms": 0,
		})
		if updated.Error != nil || updated.RowsAffected != 1 {
			if updated.Error != nil {
				return updated.Error
			}
			return ErrAsyncBillingManualDecisionConflict
		}
		reservation.State = AsyncBillingReservationStateReleased
		reservation.CurrentQuota = 0
		reservation.TerminalTimeMs = now.UnixMilli()
		reservation.ManualReviewKind = ""
		reservation.ManualReviewReason = ""
		reservation.ManualReviewRequiredMs = 0
		reservation.ReviewVersion++
		reservation.CacheSyncVersion++
		reservation.CacheSyncPending = true
		return nil
	}
	if spec.UpstreamTaskID == "" {
		return ErrAsyncBillingManualDecisionInvalid
	}
	intent, err := thawAsyncBillingAcceptanceIntent(attempt)
	if err != nil || intent.Kind != reservation.Kind {
		return ErrAsyncBillingManualDecisionBlocked
	}
	manualAudit := intent.Audit
	adminInfo := make(map[string]any, len(manualAudit.AdminInfo)+1)
	for key, value := range manualAudit.AdminInfo {
		adminInfo[key] = value
	}
	adminInfo["billing_context_phase"] = "pre_send_manual_resolution"
	manualAudit.AdminInfo = adminInfo
	replaySpec, err := synthesizeAsyncBillingManualReplay(reservation, intent)
	if err != nil {
		return err
	}
	switch reservation.Kind {
	case AsyncBillingKindTask:
		taskIntent := intent.Task
		if taskIntent == nil || len(intent.Audit.Group) > 50 {
			return ErrAsyncBillingManualDecisionBlocked
		}
		task := &Task{
			TaskID: reservation.PublicTaskID, Platform: taskIntent.Platform, UserId: reservation.UserID,
			Group: intent.Audit.Group, ChannelId: attempt.ChannelID, Quota: reservation.CurrentQuota,
			Action: taskIntent.Action, Status: taskIntent.Status, SubmitTime: taskIntent.SubmitTime,
			Progress: taskIntent.Progress, Properties: taskIntent.Properties,
			PrivateData: TaskPrivateData{BillingContext: taskIntent.BillingContext},
		}
		accepted, _, acceptedIntent, err := acceptAsyncBillingReservationTx(
			tx, reservation.ID, attempt.AttemptIndex, reservation.Kind, spec.UpstreamTaskID,
			reservation.CurrentQuota, &manualAudit, replaySpec, now,
			func(accepted *AsyncBillingReservation, acceptedAttempt *AsyncBillingAttempt, frozen *asyncBillingAcceptanceIntentEnvelope) error {
				task.PrivateData.BillingProtocolVersion = accepted.ProtocolVersion
				task.PrivateData.AsyncBillingReservationID = accepted.ID
				task.PrivateData.BillingSource = accepted.FundingSource
				task.PrivateData.SubscriptionId = accepted.SubscriptionID
				task.PrivateData.TokenId = accepted.TokenID
				task.PrivateData.NodeName = frozen.Audit.NodeName
				task.PrivateData.UpstreamTaskID = spec.UpstreamTaskID
				task.PrivateData.RoutingCredentialID = acceptedAttempt.CredentialID
				task.PrivateData.RoutingChannelGeneration = acceptedAttempt.ChannelVersion
				task.PrivateData.BillingAudit = &frozen.Audit
				if err := task.IsolateV2BillingFromLegacyPollers(accepted.CurrentQuota); err != nil {
					return err
				}
				if err := tx.Create(task).Error; err != nil {
					return err
				}
				accepted.TaskID = task.ID
				return nil
			},
		)
		if err != nil {
			return err
		}
		if err := commitAsyncBillingAcceptedStateTx(tx, accepted, attempt.AttemptIndex, accepted.CurrentQuota,
			task.ID, 0, spec.UpstreamTaskID, AsyncBillingAcceptedProjectionSpec{
				ChannelID: attempt.ChannelID, ModelName: acceptedIntent.Audit.OriginModelName,
				Group: acceptedIntent.Audit.Group, TaskIdentity: task.TaskID,
				Content: acceptedIntent.Audit.Content, Other: asyncBillingAcceptedOther(acceptedIntent.Audit),
				Audit: &acceptedIntent.Audit,
			}, AsyncBillingReservationStateAccepted, 0, now); err != nil {
			return err
		}
		*reservation = *accepted
	case AsyncBillingKindMidjourney:
		mjIntent := intent.Midjourney
		if mjIntent == nil {
			return ErrAsyncBillingManualDecisionBlocked
		}
		task := &Midjourney{
			Code: mjIntent.Code, UserId: reservation.UserID, Action: mjIntent.Action,
			MjId: reservation.PublicTaskID, UpstreamTaskID: spec.UpstreamTaskID,
			RoutingCredentialID: attempt.CredentialID, ChannelGeneration: attempt.ChannelVersion,
			Prompt: mjIntent.Prompt, Description: mjIntent.Description, SubmitTime: mjIntent.SubmitTime,
			Status: mjIntent.Status, Progress: mjIntent.Progress, ChannelId: attempt.ChannelID,
			Quota: reservation.CurrentQuota, Group: intent.Audit.Group,
			BillingProtocolVersion: reservation.ProtocolVersion, AsyncBillingReservationID: reservation.ID,
			BillingSource: reservation.FundingSource, SubscriptionId: reservation.SubscriptionID,
			TokenId: reservation.TokenID, NodeName: intent.Audit.NodeName,
		}
		if task.Status == "" {
			task.Status = "NOT_START"
		}
		if task.Progress == "" {
			task.Progress = "0%"
		}
		auditPayload, err := common.Marshal(manualAudit)
		if err != nil || len(auditPayload) > maxAsyncBillingIntentBytes {
			return ErrAsyncBillingManualDecisionBlocked
		}
		task.BillingAuditPayload = string(auditPayload)
		if err := task.IsolateV2BillingFromLegacyPollers(reservation.CurrentQuota); err != nil {
			return err
		}
		accepted, _, acceptedIntent, err := acceptAsyncBillingReservationTx(
			tx, reservation.ID, attempt.AttemptIndex, reservation.Kind, spec.UpstreamTaskID,
			reservation.CurrentQuota, &manualAudit, replaySpec, now,
			func(accepted *AsyncBillingReservation, _ *AsyncBillingAttempt, _ *asyncBillingAcceptanceIntentEnvelope) error {
				if err := tx.Create(task).Error; err != nil {
					return err
				}
				accepted.MidjourneyID = task.Id
				return nil
			},
		)
		if err != nil {
			return err
		}
		if err := commitAsyncBillingAcceptedStateTx(tx, accepted, attempt.AttemptIndex, accepted.CurrentQuota,
			0, task.Id, spec.UpstreamTaskID, AsyncBillingAcceptedProjectionSpec{
				ChannelID: attempt.ChannelID, ModelName: strings.ToLower(task.Action),
				Group: acceptedIntent.Audit.Group, TaskIdentity: task.MjId,
				Content: acceptedIntent.Audit.Content, Other: asyncBillingAcceptedOther(acceptedIntent.Audit),
				Audit: &acceptedIntent.Audit,
			}, AsyncBillingReservationStateAccepted, 0, now); err != nil {
			return err
		}
		*reservation = *accepted
	default:
		return ErrAsyncBillingManualDecisionBlocked
	}
	reservation.ReviewVersion = spec.ExpectedVersion + 1
	return nil
}

func resolveAsyncBillingAcceptanceOverageTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) error {
	return resolveAsyncBillingTaskAcceptedReviewTx(tx, reservation, spec, now, true)
}

func resolveAsyncBillingTaskAcceptedReviewTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
	allowWriteoff bool,
) error {
	if reservation.Kind != AsyncBillingKindTask || reservation.UpstreamTaskID == "" ||
		spec.UpstreamTaskID != reservation.UpstreamTaskID ||
		(allowWriteoff && reservation.ReviewTargetQuota <= reservation.CurrentQuota) ||
		(!allowWriteoff && spec.Action != AsyncBillingManualResolutionAccepted) {
		return ErrAsyncBillingManualDecisionPrecondition
	}
	var attempts []AsyncBillingAttempt
	if err := lockForUpdate(tx).Where(
		"reservation_id = ? AND state = ?", reservation.ID, AsyncBillingAttemptStateAuthorized,
	).Find(&attempts).Error; err != nil {
		return err
	}
	if len(attempts) != 1 {
		return ErrAsyncBillingManualDecisionBlocked
	}
	attempt := &attempts[0]
	intent, err := thawAsyncBillingAcceptanceIntent(attempt)
	if err != nil || intent.Kind != AsyncBillingKindTask || intent.Task == nil {
		return ErrAsyncBillingManualDecisionBlocked
	}
	finalAudit, err := thawAsyncBillingReviewAudit(reservation)
	if err != nil {
		return ErrAsyncBillingManualDecisionBlocked
	}
	finalQuota := reservation.ReviewTargetQuota
	if allowWriteoff && spec.Action != AsyncBillingManualResolutionAccepted {
		finalQuota = reservation.CurrentQuota
	} else if spec.Action == AsyncBillingManualResolutionAccepted {
		finalQuota = reservation.ReviewTargetQuota
	}
	if allowWriteoff && spec.Action != AsyncBillingManualResolutionAccepted {
		adminInfo := make(map[string]any, len(finalAudit.AdminInfo)+2)
		for key, value := range finalAudit.AdminInfo {
			adminInfo[key] = value
		}
		adminInfo["billing_context_phase"] = "acceptance_overage_writeoff"
		adminInfo["billing_writeoff_target_quota"] = reservation.ReviewTargetQuota
		finalAudit.AdminInfo = adminInfo
	}
	replaySpec, err := synthesizeAsyncBillingManualReplay(reservation, intent)
	if err != nil {
		return err
	}
	taskIntent := intent.Task
	if len(finalAudit.Group) > 50 {
		return ErrAsyncBillingManualDecisionBlocked
	}
	task := &Task{
		TaskID: reservation.PublicTaskID, Platform: taskIntent.Platform, UserId: reservation.UserID,
		Group: finalAudit.Group, ChannelId: attempt.ChannelID, Quota: finalQuota,
		Action: taskIntent.Action, Status: taskIntent.Status, SubmitTime: taskIntent.SubmitTime,
		Progress: taskIntent.Progress, Properties: taskIntent.Properties,
		PrivateData: TaskPrivateData{BillingContext: taskIntent.BillingContext},
	}
	if task.PrivateData.BillingContext != nil {
		contextCopy := *task.PrivateData.BillingContext
		contextCopy.OtherRatios = make(map[string]float64, len(finalAudit.OtherRatios))
		for key, value := range finalAudit.OtherRatios {
			contextCopy.OtherRatios[key] = value
		}
		task.PrivateData.BillingContext = &contextCopy
	}
	accepted, _, acceptedIntent, err := acceptAsyncBillingReservationTx(
		tx, reservation.ID, attempt.AttemptIndex, reservation.Kind, spec.UpstreamTaskID,
		finalQuota, finalAudit, replaySpec, now,
		func(accepted *AsyncBillingReservation, acceptedAttempt *AsyncBillingAttempt, frozen *asyncBillingAcceptanceIntentEnvelope) error {
			task.PrivateData.BillingProtocolVersion = accepted.ProtocolVersion
			task.PrivateData.AsyncBillingReservationID = accepted.ID
			task.PrivateData.BillingSource = accepted.FundingSource
			task.PrivateData.SubscriptionId = accepted.SubscriptionID
			task.PrivateData.TokenId = accepted.TokenID
			task.PrivateData.NodeName = frozen.Audit.NodeName
			task.PrivateData.UpstreamTaskID = spec.UpstreamTaskID
			task.PrivateData.RoutingCredentialID = acceptedAttempt.CredentialID
			task.PrivateData.RoutingChannelGeneration = acceptedAttempt.ChannelVersion
			task.PrivateData.BillingAudit = &frozen.Audit
			if err := task.IsolateV2BillingFromLegacyPollers(finalQuota); err != nil {
				return err
			}
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			accepted.TaskID = task.ID
			return nil
		},
	)
	if err != nil {
		return err
	}
	if err := commitAsyncBillingAcceptedStateTx(tx, accepted, attempt.AttemptIndex, finalQuota,
		task.ID, 0, spec.UpstreamTaskID, AsyncBillingAcceptedProjectionSpec{
			ChannelID: attempt.ChannelID, ModelName: acceptedIntent.Audit.OriginModelName,
			Group: acceptedIntent.Audit.Group, TaskIdentity: task.TaskID,
			Content: acceptedIntent.Audit.Content, Other: asyncBillingAcceptedOther(acceptedIntent.Audit),
			Audit: &acceptedIntent.Audit,
		}, AsyncBillingReservationStateAccepted, 0, now); err != nil {
		return err
	}
	*reservation = *accepted
	return nil
}

func resolveAsyncBillingAcceptedHandoffTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) error {
	if reservation == nil || reservation.UpstreamTaskID == "" ||
		spec.UpstreamTaskID != reservation.UpstreamTaskID ||
		spec.Action != AsyncBillingManualResolutionAccepted ||
		reservation.ReviewTargetQuota < 0 || reservation.ReviewTargetQuota > common.MaxQuota {
		return ErrAsyncBillingManualDecisionPrecondition
	}
	if reservation.Kind == AsyncBillingKindTask {
		return resolveAsyncBillingTaskAcceptedReviewTx(tx, reservation, spec, now, false)
	}
	if reservation.Kind != AsyncBillingKindMidjourney {
		return ErrAsyncBillingManualDecisionBlocked
	}
	var attempts []AsyncBillingAttempt
	if err := lockForUpdate(tx).Where(
		"reservation_id = ? AND state = ?", reservation.ID, AsyncBillingAttemptStateAuthorized,
	).Find(&attempts).Error; err != nil {
		return err
	}
	if len(attempts) != 1 {
		return ErrAsyncBillingManualDecisionBlocked
	}
	attempt := &attempts[0]
	intent, err := thawAsyncBillingAcceptanceIntent(attempt)
	if err != nil || intent.Kind != AsyncBillingKindMidjourney || intent.Midjourney == nil {
		return ErrAsyncBillingManualDecisionBlocked
	}
	finalAudit, err := thawAsyncBillingReviewAudit(reservation)
	if err != nil {
		return ErrAsyncBillingManualDecisionBlocked
	}
	replaySpec, err := synthesizeAsyncBillingManualReplay(reservation, intent)
	if err != nil {
		return err
	}
	taskIntent := intent.Midjourney
	task := &Midjourney{
		Code: taskIntent.Code, UserId: reservation.UserID, Action: taskIntent.Action,
		MjId: reservation.PublicTaskID, UpstreamTaskID: spec.UpstreamTaskID,
		Prompt: taskIntent.Prompt, Description: taskIntent.Description, SubmitTime: taskIntent.SubmitTime,
		Status: taskIntent.Status, Progress: taskIntent.Progress, ChannelId: attempt.ChannelID,
		Quota: reservation.ReviewTargetQuota, Group: finalAudit.Group,
		BillingProtocolVersion: reservation.ProtocolVersion, AsyncBillingReservationID: reservation.ID,
		BillingSource: reservation.FundingSource, SubscriptionId: reservation.SubscriptionID,
		TokenId: reservation.TokenID, NodeName: finalAudit.NodeName,
		RoutingCredentialID: attempt.CredentialID, ChannelGeneration: attempt.ChannelVersion,
	}
	if task.Status == "" {
		task.Status = "NOT_START"
	}
	if task.Progress == "" {
		task.Progress = "0%"
	}
	auditPayload, err := common.Marshal(finalAudit)
	if err != nil || len(auditPayload) > maxAsyncBillingIntentBytes {
		return ErrAsyncBillingManualDecisionBlocked
	}
	task.BillingAuditPayload = string(auditPayload)
	if err := task.IsolateV2BillingFromLegacyPollers(reservation.ReviewTargetQuota); err != nil {
		return err
	}
	accepted, _, acceptedIntent, err := acceptAsyncBillingReservationTx(
		tx, reservation.ID, attempt.AttemptIndex, reservation.Kind, spec.UpstreamTaskID,
		reservation.ReviewTargetQuota, finalAudit, replaySpec, now,
		func(accepted *AsyncBillingReservation, _ *AsyncBillingAttempt, _ *asyncBillingAcceptanceIntentEnvelope) error {
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			accepted.MidjourneyID = task.Id
			return nil
		},
	)
	if err != nil {
		return err
	}
	if err := commitAsyncBillingAcceptedStateTx(tx, accepted, attempt.AttemptIndex,
		reservation.ReviewTargetQuota, 0, task.Id, spec.UpstreamTaskID, AsyncBillingAcceptedProjectionSpec{
			ChannelID: attempt.ChannelID, ModelName: strings.ToLower(task.Action),
			Group: acceptedIntent.Audit.Group, TaskIdentity: task.MjId,
			Content: acceptedIntent.Audit.Content, Other: asyncBillingAcceptedOther(acceptedIntent.Audit),
			Audit: &acceptedIntent.Audit,
		}, AsyncBillingReservationStateAccepted, 0, now); err != nil {
		return err
	}
	*reservation = *accepted
	return nil
}

func resolveAsyncBillingTerminalOverageTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) error {
	if reservation.Kind != AsyncBillingKindTask || reservation.TaskID <= 0 {
		return ErrAsyncBillingManualDecisionBlocked
	}
	var operation TaskBillingOperation
	if err := lockForUpdate(tx).Where("reservation_id = ? AND state = ?", reservation.ID,
		TaskBillingOperationStateManualReview).First(&operation).Error; err != nil {
		return ErrAsyncBillingManualDecisionBlocked
	}
	if operation.TargetQuota <= reservation.CurrentQuota || operation.PreConsumedQuota != reservation.CurrentQuota {
		return ErrAsyncBillingManualDecisionBlocked
	}
	var task Task
	if err := lockForUpdate(tx).Where("id = ?", reservation.TaskID).First(&task).Error; err != nil {
		return err
	}
	if task.EffectiveBillingQuota() != reservation.CurrentQuota || task.UserId != reservation.UserID {
		return ErrAsyncBillingManualDecisionBlocked
	}
	finalQuota := reservation.CurrentQuota
	resolutionNote := "admin_writeoff_terminal_overage"
	if spec.Action == AsyncBillingManualResolutionAccepted {
		delta := operation.TargetQuota - reservation.CurrentQuota
		if err := applyAsyncBillingReservationDeltaTx(tx, reservation, delta, now); err != nil {
			return err
		}
		finalQuota = operation.TargetQuota
		resolutionNote = "admin_approved_terminal_overage"
		quotaColumn := "quota"
		if task.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
			quotaColumn = "durable_quota"
		}
		if updated := tx.Model(&Task{}).Where("id = ? AND "+quotaColumn+" = ?", task.ID, task.EffectiveBillingQuota()).
			Updates(map[string]any{quotaColumn: finalQuota, "updated_at": now.Unix()}); updated.Error != nil {
			return updated.Error
		} else if updated.RowsAffected != 1 {
			return ErrAsyncBillingManualDecisionConflict
		}
		if err := enqueueTaskTerminalBillingProjectionsTx(tx, &operation, &task, delta, now); err != nil {
			return err
		}
	}
	operationUpdates := map[string]any{
		"state": TaskBillingOperationStateCompleted, "lease_owner": "", "lease_until_ms": 0,
		"next_retry_time_ms": 0, "last_error": resolutionNote,
		"updated_time_ms": now.UnixMilli(), "completed_time_ms": now.UnixMilli(),
	}
	if spec.Action == AsyncBillingManualResolutionRejected {
		operationUpdates["kind"] = TaskBillingOperationKindNoop
		operationUpdates["target_quota"] = reservation.CurrentQuota
		operationUpdates["quota_delta"] = 0
		operationUpdates["log_state"] = TaskBillingOperationLogNotRequired
		operationUpdates["log_payload"] = ""
		operationUpdates["log_payload_hash"] = ""
		operationUpdates["log_payload_protocol"] = 0
		operationUpdates["log_updated_time_ms"] = now.UnixMilli()
	}
	if updated := tx.Model(&TaskBillingOperation{}).Where("id = ? AND state = ?", operation.ID,
		TaskBillingOperationStateManualReview).Updates(operationUpdates); updated.Error != nil {
		return updated.Error
	} else if updated.RowsAffected != 1 {
		return ErrAsyncBillingManualDecisionConflict
	}
	cacheChanged := finalQuota != reservation.CurrentQuota
	updates := map[string]any{
		"state": AsyncBillingReservationStateTerminal, "current_quota": finalQuota,
		"terminal_time_ms": now.UnixMilli(), "updated_time_ms": now.UnixMilli(),
		"last_error": resolutionNote, "manual_review_kind": "", "manual_review_reason": "",
		"manual_review_required_ms": 0, "review_version": gorm.Expr("review_version + ?", 1),
	}
	if cacheChanged {
		updates["cache_sync_version"] = gorm.Expr("cache_sync_version + ?", 1)
		updates["cache_sync_pending"] = true
		updates["cache_sync_next_retry_ms"] = 0
	}
	if updated := tx.Model(&AsyncBillingReservation{}).Where(
		"id = ? AND state = ? AND review_version = ?", reservation.ID,
		AsyncBillingReservationStateManualReview, spec.ExpectedVersion,
	).Updates(updates); updated.Error != nil {
		return updated.Error
	} else if updated.RowsAffected != 1 {
		return ErrAsyncBillingManualDecisionConflict
	}
	reservation.State = AsyncBillingReservationStateTerminal
	reservation.CurrentQuota = finalQuota
	reservation.TerminalTimeMs = now.UnixMilli()
	reservation.ManualReviewKind = ""
	reservation.ManualReviewReason = ""
	reservation.ManualReviewRequiredMs = 0
	reservation.ReviewVersion++
	if cacheChanged {
		reservation.CacheSyncVersion++
		reservation.CacheSyncPending = true
	}
	return nil
}

func resolveAsyncBillingTerminalUsageTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingManualDecisionSpec,
	now time.Time,
) error {
	if tx == nil || reservation == nil || reservation.Kind != AsyncBillingKindTask || reservation.TaskID <= 0 ||
		reservation.ManualReviewKind != AsyncBillingReviewKindTerminalUsage ||
		spec.Action != AsyncBillingManualResolutionAccepted {
		return ErrAsyncBillingManualDecisionBlocked
	}
	var task Task
	if err := lockForUpdate(tx).Where("id = ?", reservation.TaskID).First(&task).Error; err != nil {
		return err
	}
	if task.UserId != reservation.UserID || task.Status != TaskStatusSuccess || task.Progress != "100%" ||
		task.PrivateData.AsyncBillingReservationID != reservation.ID ||
		task.EffectiveBillingQuota() != reservation.CurrentQuota {
		return ErrAsyncBillingManualDecisionBlocked
	}
	var operationCount int64
	if err := tx.Model(&TaskBillingOperation{}).Where("task_id = ?", task.ID).Count(&operationCount).Error; err != nil {
		return err
	}
	if operationCount != 0 {
		return ErrAsyncBillingManualDecisionConflict
	}
	billingSource := strings.TrimSpace(task.PrivateData.BillingSource)
	if billingSource == "" {
		billingSource = TaskBillingSourceWallet
	}
	operation := TaskBillingOperation{
		TaskID: task.ID, ReservationID: reservation.ID,
		OperationKey:   fmt.Sprintf("task:%d:terminal:v2", task.ID),
		TerminalStatus: task.Status, Kind: TaskBillingOperationKindNoop,
		State:  TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: billingSource,
		SubscriptionID: task.PrivateData.SubscriptionId, TokenID: task.PrivateData.TokenId,
		PreConsumedQuota: reservation.CurrentQuota, TargetQuota: reservation.CurrentQuota,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		LogState: TaskBillingOperationLogNotRequired, LogUpdatedTimeMs: now.UnixMilli(),
		UsageUserOutcome:    BillingUsageOutcomeNotRequired,
		UsageChannelOutcome: BillingUsageOutcomeNotRequired,
	}
	if err := freezeTaskTerminalSnapshot(&operation, &task); err != nil {
		return err
	}
	if err := tx.Create(&operation).Error; err != nil {
		return err
	}
	resolutionNote := "admin_terminal_usage_charge_preserved"
	updated := tx.Model(&AsyncBillingReservation{}).Where(
		"id = ? AND state = ? AND review_version = ?", reservation.ID,
		AsyncBillingReservationStateManualReview, spec.ExpectedVersion,
	).Updates(map[string]any{
		"state": AsyncBillingReservationStateTerminal, "terminal_time_ms": now.UnixMilli(),
		"updated_time_ms": now.UnixMilli(), "last_error": resolutionNote,
		"manual_review_kind": "", "manual_review_reason": "", "manual_review_required_ms": 0,
		"review_version": gorm.Expr("review_version + ?", 1),
	})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrAsyncBillingManualDecisionConflict
	}
	reservation.State = AsyncBillingReservationStateTerminal
	reservation.TerminalTimeMs = now.UnixMilli()
	reservation.UpdatedTimeMs = now.UnixMilli()
	reservation.LastError = resolutionNote
	reservation.ManualReviewKind = ""
	reservation.ManualReviewReason = ""
	reservation.ManualReviewRequiredMs = 0
	reservation.ReviewVersion++
	return nil
}

func GetAsyncBillingManualResolution(reservationID int64) (*AsyncBillingManualResolution, error) {
	if reservationID <= 0 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var resolution AsyncBillingManualResolution
	if err := DB.Where("reservation_id = ?", reservationID).
		Order("expected_version desc, id desc").First(&resolution).Error; err != nil {
		return nil, err
	}
	return &resolution, nil
}
