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
	AsyncBillingKindTask       = "task"
	AsyncBillingKindMidjourney = "midjourney"

	AsyncBillingReservationStateReserved       = "reserved"
	AsyncBillingReservationStateSendAuthorized = "send_authorized"
	AsyncBillingReservationStateManualReview   = "manual_review"
	AsyncBillingReservationStateAccepted       = "accepted"
	AsyncBillingReservationStateReleased       = "released"
	AsyncBillingReservationStateTerminal       = "terminal"

	AsyncBillingReviewKindSendOutcome       = "send_outcome"
	AsyncBillingReviewKindAcceptedHandoff   = "accepted_handoff"
	AsyncBillingReviewKindAcceptanceOverage = "acceptance_overage"
	AsyncBillingReviewKindTerminalOverage   = "terminal_overage"
	AsyncBillingReviewKindTerminalUsage     = "terminal_usage"

	AsyncBillingAttemptStateAuthorized = "authorized"
	AsyncBillingAttemptStateRejected   = "rejected"
	AsyncBillingAttemptStateAccepted   = "accepted"

	AsyncBillingAcceptedProjectionPending    = "pending"
	AsyncBillingAcceptedProjectionLogPending = "log_pending"
	AsyncBillingAcceptedProjectionCompleted  = "completed"

	asyncBillingReservationErrorMaxBytes  = 1024
	asyncBillingPostCommitMutationTimeout = 3 * time.Second
)

var (
	ErrAsyncBillingReservationInvariant  = errors.New("async billing reservation invariant violation")
	ErrAsyncBillingReservationAmbiguous  = errors.New("async billing upstream outcome is ambiguous")
	ErrAsyncBillingReservationAccepted   = errors.New("async billing reservation is already accepted")
	ErrAsyncBillingCacheFencePending     = errors.New("async billing cache fence is pending")
	ErrAsyncBillingBatchUpdateActive     = errors.New("async billing is unavailable while batch updates are active")
	ErrAsyncBillingInsufficientQuota     = errors.New("async billing quota is insufficient")
	ErrAsyncBillingNoActiveSubscription  = errors.New("async billing has no active subscription")
	ErrAsyncBillingSubscriptionExhausted = errors.New("async billing subscription quota is insufficient")
	ErrAsyncBillingTerminalOverage       = errors.New("async billing terminal quota exceeds the accepted reservation")
	ErrAsyncBillingOutstandingReferences = errors.New("async billing has outstanding financial references")
)

// AsyncBillingReservation is the durable handoff between quota reservation,
// the upstream send boundary, accepted task creation, and terminal settlement.
// CurrentQuota is always the amount already reflected in the funding source and
// token rows.
type AsyncBillingReservation struct {
	ID                int64   `json:"id" gorm:"primaryKey;autoIncrement"`
	ReservationKey    string  `json:"reservation_key" gorm:"type:varchar(191);not null;uniqueIndex:uidx_async_billing_reservation_key"`
	ProtocolVersion   int     `json:"protocol_version" gorm:"not null"`
	Kind              string  `json:"kind" gorm:"type:varchar(20);not null;index"`
	PublicTaskID      string  `json:"public_task_id" gorm:"type:varchar(191);not null;uniqueIndex:uidx_async_billing_public_task"`
	State             string  `json:"state" gorm:"type:varchar(24);not null;index:idx_async_billing_recovery,priority:1"`
	ClientKeyHash     *string `json:"-" gorm:"type:varchar(64);uniqueIndex:uidx_async_billing_client_key"`
	ClientPayloadHash string  `json:"-" gorm:"type:varchar(64)"`
	ClientScope       string  `json:"-" gorm:"type:varchar(191)"`

	UserID        int    `json:"user_id" gorm:"not null;index"`
	TokenID       int    `json:"token_id" gorm:"index"`
	FundingSource string `json:"funding_source" gorm:"type:varchar(20);not null"`

	SubscriptionID       int    `json:"subscription_id" gorm:"index"`
	SubscriptionPeriodID int64  `json:"subscription_period_id" gorm:"index"`
	SubscriptionPlanID   int    `json:"subscription_plan_id"`
	SubscriptionPlanName string `json:"subscription_plan_name" gorm:"type:varchar(128)"`
	SubscriptionTotal    int64  `json:"subscription_total" gorm:"not null"`

	InitialQuota  int `json:"initial_quota" gorm:"not null"`
	CurrentQuota  int `json:"current_quota" gorm:"not null"`
	AcceptedQuota int `json:"accepted_quota" gorm:"not null"`
	// Attempt zero is valid; State distinguishes it from an unaccepted row.
	AcceptedAttemptIndex int `json:"accepted_attempt_index" gorm:"not null"`

	TaskID         int64  `json:"task_id" gorm:"index"`
	MidjourneyID   int    `json:"midjourney_id" gorm:"index"`
	UpstreamTaskID string `json:"upstream_task_id" gorm:"type:varchar(191);index"`

	CreatedTimeMs    int64  `json:"created_time_ms" gorm:"not null"`
	UpdatedTimeMs    int64  `json:"updated_time_ms" gorm:"not null;index:idx_async_billing_recovery,priority:2"`
	SendAuthorizedMs int64  `json:"send_authorized_ms"`
	AcceptedTimeMs   int64  `json:"accepted_time_ms"`
	TerminalTimeMs   int64  `json:"terminal_time_ms"`
	LastError        string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`

	ManualReviewRequiredMs int64  `json:"manual_review_required_ms" gorm:"index"`
	ManualReviewReason     string `json:"manual_review_reason,omitempty" gorm:"type:varchar(1024)"`
	ManualReviewKind       string `json:"manual_review_kind,omitempty" gorm:"type:varchar(32);index"`
	ReviewVersion          int64  `json:"review_version" gorm:"not null"`
	ReviewTargetQuota      int    `json:"review_target_quota" gorm:"not null"`
	ReviewAuditProtocol    int    `json:"-" gorm:"not null"`
	ReviewAuditHash        string `json:"-" gorm:"type:varchar(64)"`
	ReviewAuditPayload     []byte `json:"-"`

	ReplayProtocol    int    `json:"-" gorm:"not null"`
	ReplayReady       bool   `json:"-" gorm:"index"`
	ReplayStatusCode  int    `json:"-"`
	ReplayContentType string `json:"-" gorm:"type:varchar(128)"`
	ReplayHeadersJSON string `json:"-" gorm:"type:text"`
	ReplayBody        []byte `json:"-"`
	ReplayHash        string `json:"-" gorm:"type:varchar(64)"`

	// Balance mutations increment CacheSyncVersion in the same transaction.
	// A cache invalidation may clear pending only for the version it observed.
	CacheSyncVersion     int64  `json:"cache_sync_version" gorm:"not null"`
	CacheSyncedVersion   int64  `json:"cache_synced_version" gorm:"not null"`
	CacheSyncPending     bool   `json:"cache_sync_pending" gorm:"index:idx_async_billing_cache_sync,priority:1"`
	CacheSyncNextRetryMs int64  `json:"cache_sync_next_retry_ms" gorm:"index:idx_async_billing_cache_sync,priority:2"`
	CacheSyncAttempts    int    `json:"cache_sync_attempts" gorm:"not null"`
	CacheSyncLastError   string `json:"cache_sync_last_error,omitempty" gorm:"type:varchar(1024)"`

	AcceptedProjectionKey            *string `json:"accepted_projection_key,omitempty" gorm:"type:varchar(64);uniqueIndex"`
	AcceptedProjectionState          string  `json:"accepted_projection_state,omitempty" gorm:"type:varchar(20);index:idx_async_billing_accepted_projection,priority:1"`
	AcceptedProjectionChannelID      int     `json:"accepted_projection_channel_id"`
	AcceptedProjectionModelName      string  `json:"accepted_projection_model_name,omitempty" gorm:"type:varchar(191)"`
	AcceptedProjectionGroup          string  `json:"accepted_projection_group,omitempty" gorm:"type:varchar(64)"`
	AcceptedProjectionTaskIdentity   string  `json:"accepted_projection_task_identity,omitempty" gorm:"type:varchar(191)"`
	AcceptedProjectionQuota          int     `json:"accepted_projection_quota" gorm:"not null"`
	AcceptedProjectionRequestDelta   int     `json:"accepted_projection_request_delta" gorm:"not null"`
	AcceptedProjectionLogProtocol    int     `json:"accepted_projection_log_protocol" gorm:"not null"`
	AcceptedProjectionLogHash        string  `json:"accepted_projection_log_hash,omitempty" gorm:"type:varchar(64)"`
	AcceptedProjectionLogPayload     string  `json:"accepted_projection_log_payload,omitempty" gorm:"type:text"`
	AcceptedProjectionCreatedMs      int64   `json:"accepted_projection_created_ms"`
	AcceptedProjectionNextRetryMs    int64   `json:"accepted_projection_next_retry_ms" gorm:"index:idx_async_billing_accepted_projection,priority:2"`
	AcceptedProjectionAttempts       int     `json:"accepted_projection_attempts" gorm:"not null"`
	AcceptedProjectionLastError      string  `json:"accepted_projection_last_error,omitempty" gorm:"type:varchar(1024)"`
	AcceptedProjectionUserOutcome    string  `json:"accepted_projection_user_outcome,omitempty" gorm:"type:varchar(32)"`
	AcceptedProjectionChannelOutcome string  `json:"accepted_projection_channel_outcome,omitempty" gorm:"type:varchar(32)"`
	AcceptedProjectionWarning        string  `json:"accepted_projection_warning,omitempty" gorm:"type:varchar(1024)"`
}

type AsyncBillingAttempt struct {
	ID                int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	ReservationID     int64  `json:"reservation_id" gorm:"not null;uniqueIndex:uidx_async_billing_attempt,priority:1;index"`
	AttemptIndex      int    `json:"attempt_index" gorm:"not null;uniqueIndex:uidx_async_billing_attempt,priority:2"`
	State             string `json:"state" gorm:"type:varchar(20);not null;index"`
	ChannelID         int    `json:"channel_id" gorm:"index"`
	CredentialID      int    `json:"credential_id" gorm:"index"`
	ChannelVersion    string `json:"channel_version" gorm:"type:varchar(32)"`
	AuthorizedMs      int64  `json:"authorized_ms" gorm:"not null"`
	SendDeadlineMs    int64  `json:"send_deadline_ms" gorm:"not null;index"`
	ResolvedMs        int64  `json:"resolved_ms"`
	FailureCode       string `json:"failure_code,omitempty" gorm:"type:varchar(128)"`
	IntentProtocol    int    `json:"-" gorm:"not null"`
	IntentPayloadHash string `json:"-" gorm:"type:varchar(64)"`
	IntentPayload     []byte `json:"-"`
}

type AsyncBillingReservationSpec struct {
	ReservationKey    string
	ProtocolVersion   int
	Kind              string
	PublicTaskID      string
	UserID            int
	TokenID           int
	IsPlayground      bool
	BillingPreference string
	Quota             int
	ClientKeyHash     string
	ClientPayloadHash string
	ClientScope       string
}

type AsyncBillingAttemptSpec struct {
	AttemptIndex     int
	ChannelID        int
	CredentialID     int
	ChannelVersion   string
	SendDeadlineMs   int64
	AcceptanceIntent *AsyncBillingAcceptanceIntentSpec
}

type AsyncBillingAcceptedProjectionSpec struct {
	ChannelID    int
	ModelName    string
	Group        string
	TaskIdentity string
	Content      string
	Other        map[string]any
	Audit        *AsyncBillingAcceptedAuditSnapshot
}

func asyncBillingAcceptedOther(audit AsyncBillingAcceptedAuditSnapshot) map[string]any {
	other := map[string]any{
		"request_path": audit.RequestPath,
		"action":       audit.Action,
		"model_price":  audit.ModelPrice,
		"group_ratio":  audit.GroupRatio,
	}
	if audit.ModelRatio > 0 {
		other["model_ratio"] = audit.ModelRatio
	}
	if audit.UserGroupRatio != nil {
		other["user_group_ratio"] = *audit.UserGroupRatio
	}
	if audit.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = audit.UpstreamModelName
	}
	for key, value := range audit.OtherRatios {
		other[key] = value
	}
	adminInfo := make(map[string]any, len(audit.AdminInfo)+1)
	for key, value := range audit.AdminInfo {
		adminInfo[key] = value
	}
	if audit.QuotaClamp != nil {
		adminInfo["quota_saturation"] = audit.QuotaClamp.AuditMap()
	}
	if len(adminInfo) > 0 {
		other["admin_info"] = adminInfo
	}
	return other
}

func GetAsyncBillingReservation(ctx context.Context, reservationID int64) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var reservation AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
		return nil, err
	}
	return &reservation, nil
}

func GetAsyncBillingReservationByKey(ctx context.Context, reservationKey string) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reservationKey = strings.TrimSpace(reservationKey)
	if reservationKey == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var reservation AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("reservation_key = ?", reservationKey).First(&reservation).Error; err != nil {
		return nil, err
	}
	return &reservation, nil
}

func GetAsyncBillingReservationByClientIdentity(
	ctx context.Context,
	clientKeyHash string,
	payloadHash string,
	clientScope string,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientKeyHash = strings.ToLower(strings.TrimSpace(clientKeyHash))
	payloadHash = strings.ToLower(strings.TrimSpace(payloadHash))
	clientScope = strings.TrimSpace(clientScope)
	if !validAsyncBillingHash(clientKeyHash) || !validAsyncBillingHash(payloadHash) || clientScope == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var reservation AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("client_key_hash = ?", clientKeyHash).First(&reservation).Error; err != nil {
		return nil, err
	}
	if reservation.ClientKeyHash == nil || !strings.EqualFold(*reservation.ClientKeyHash, clientKeyHash) ||
		!strings.EqualFold(reservation.ClientPayloadHash, payloadHash) || reservation.ClientScope != clientScope {
		return nil, ErrAsyncBillingIdempotencyConflict
	}
	return &reservation, nil
}

func validateAsyncBillingReservationSpec(spec AsyncBillingReservationSpec) error {
	spec.ReservationKey = strings.TrimSpace(spec.ReservationKey)
	spec.Kind = strings.TrimSpace(spec.Kind)
	spec.PublicTaskID = strings.TrimSpace(spec.PublicTaskID)
	if spec.ReservationKey == "" || len(spec.ReservationKey) > 191 || spec.PublicTaskID == "" || len(spec.PublicTaskID) > 191 ||
		spec.ProtocolVersion != TaskBillingProtocolVersion || spec.UserID <= 0 || spec.Quota < 0 || spec.Quota > common.MaxQuota {
		return ErrAsyncBillingReservationInvariant
	}
	if spec.Kind != AsyncBillingKindTask && spec.Kind != AsyncBillingKindMidjourney {
		return ErrAsyncBillingReservationInvariant
	}
	if !spec.IsPlayground && spec.TokenID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	spec.ClientKeyHash = strings.ToLower(strings.TrimSpace(spec.ClientKeyHash))
	spec.ClientPayloadHash = strings.ToLower(strings.TrimSpace(spec.ClientPayloadHash))
	spec.ClientScope = strings.TrimSpace(spec.ClientScope)
	if (spec.ClientKeyHash == "") != (spec.ClientPayloadHash == "") ||
		(spec.ClientKeyHash != "" && (!validAsyncBillingHash(spec.ClientKeyHash) ||
			!validAsyncBillingHash(spec.ClientPayloadHash) || spec.ClientScope == "" ||
			len(spec.ClientScope) > maxAsyncBillingRequestScopeBytes || !strings.HasPrefix(spec.ReservationKey, "client:"))) {
		return ErrAsyncBillingReservationInvariant
	}
	return nil
}

func sameAsyncBillingReservationSpec(reservation *AsyncBillingReservation, spec AsyncBillingReservationSpec) bool {
	if reservation == nil || reservation.ReservationKey != strings.TrimSpace(spec.ReservationKey) ||
		reservation.ProtocolVersion != spec.ProtocolVersion || reservation.Kind != strings.TrimSpace(spec.Kind) ||
		reservation.UserID != spec.UserID || reservation.TokenID != spec.TokenID {
		return false
	}
	if strings.TrimSpace(spec.ClientKeyHash) != "" {
		return reservation.ClientKeyHash != nil && *reservation.ClientKeyHash == strings.ToLower(strings.TrimSpace(spec.ClientKeyHash)) &&
			reservation.ClientPayloadHash == strings.ToLower(strings.TrimSpace(spec.ClientPayloadHash)) &&
			reservation.ClientScope == strings.TrimSpace(spec.ClientScope)
	}
	return reservation.ClientKeyHash == nil &&
		reservation.ProtocolVersion == spec.ProtocolVersion && reservation.Kind == strings.TrimSpace(spec.Kind) &&
		reservation.PublicTaskID == strings.TrimSpace(spec.PublicTaskID) && reservation.UserID == spec.UserID &&
		reservation.TokenID == spec.TokenID && reservation.InitialQuota == spec.Quota
}

// CreateAsyncBillingReservation charges the funding source and token in the
// same primary-database transaction that creates the reservation. It never uses
// the process-local batch updater.
func CreateAsyncBillingReservation(
	ctx context.Context,
	spec AsyncBillingReservationSpec,
	now time.Time,
) (*AsyncBillingReservation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAsyncBillingReservationSpec(spec); err != nil {
		return nil, false, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	spec.ReservationKey = strings.TrimSpace(spec.ReservationKey)
	spec.PublicTaskID = strings.TrimSpace(spec.PublicTaskID)
	spec.Kind = strings.TrimSpace(spec.Kind)
	spec.ClientKeyHash = strings.ToLower(strings.TrimSpace(spec.ClientKeyHash))
	spec.ClientPayloadHash = strings.ToLower(strings.TrimSpace(spec.ClientPayloadHash))
	spec.ClientScope = strings.TrimSpace(spec.ClientScope)

	if existing, err := GetAsyncBillingReservationByKey(ctx, spec.ReservationKey); err == nil {
		if !sameAsyncBillingReservationSpec(existing, spec) {
			if spec.ClientKeyHash != "" && existing.ClientKeyHash != nil && *existing.ClientKeyHash == spec.ClientKeyHash {
				return nil, false, ErrAsyncBillingIdempotencyConflict
			}
			return nil, false, ErrAsyncBillingReservationInvariant
		}
		if existing.CacheSyncPending {
			syncAsyncBillingReservationCachesBestEffort(ctx, existing.ID, now)
		}
		return existing, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	var createdReservation AsyncBillingReservation
	created := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx.Unscoped()).Where("id = ?", spec.UserID).First(&user).Error; err != nil {
			return err
		}
		var existing AsyncBillingReservation
		query := tx.Where("reservation_key = ?", spec.ReservationKey).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if !sameAsyncBillingReservationSpec(&existing, spec) {
				if spec.ClientKeyHash != "" && existing.ClientKeyHash != nil && *existing.ClientKeyHash == spec.ClientKeyHash {
					return ErrAsyncBillingIdempotencyConflict
				}
				return ErrAsyncBillingReservationInvariant
			}
			createdReservation = existing
			return nil
		}
		if user.DeletedAt.Valid || user.Status != common.UserStatusEnabled {
			return ErrTokenInvalid
		}
		if spec.TokenID > 0 {
			var token Token
			if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", spec.TokenID, spec.UserID).First(&token).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTokenInvalid
				}
				return err
			}
			if token.Status != common.TokenStatusEnabled ||
				(token.ExpiredTime != -1 && token.ExpiredTime < now.Unix()) ||
				(!token.UnlimitedQuota && token.RemainQuota <= 0) {
				return ErrTokenInvalid
			}
		}

		fundingSource := TaskBillingSourceWallet
		subscriptionID := 0
		periodID := int64(0)
		planID := 0
		planName := ""
		subscriptionTotal := int64(0)
		if spec.Quota > 0 {
			preference := common.NormalizeBillingPreference(spec.BillingPreference)
			useWallet := func() error {
				if user.Quota < spec.Quota {
					return ErrAsyncBillingInsufficientQuota
				}
				fundingSource = TaskBillingSourceWallet
				return nil
			}
			useSubscription := func() error {
				var subscriptions []UserSubscription
				if err := lockForUpdate(tx).
					Where("user_id = ? AND status = ? AND end_time > ?", spec.UserID, "active", now.Unix()).
					Order("end_time asc, id asc").Find(&subscriptions).Error; err != nil {
					return err
				}
				if len(subscriptions) == 0 {
					return ErrAsyncBillingNoActiveSubscription
				}
				for index := range subscriptions {
					subscription := &subscriptions[index]
					plan, err := getSubscriptionPlanByIdTx(tx, subscription.PlanId)
					if err != nil {
						return err
					}
					if err := maybeResetUserSubscriptionWithPlanTx(tx, subscription, plan, now.Unix()); err != nil {
						return err
					}
					if subscription.AmountTotal > 0 && subscription.AmountTotal-subscription.AmountUsed < int64(spec.Quota) {
						continue
					}
					period, err := ensureSubscriptionBillingPeriodTx(tx, subscription, now.Unix())
					if err != nil {
						return err
					}
					fundingSource = TaskBillingSourceSubscription
					subscriptionID = subscription.Id
					periodID = period.ID
					planID = subscription.PlanId
					planName = plan.Title
					subscriptionTotal = subscription.AmountTotal
					return nil
				}
				return ErrAsyncBillingSubscriptionExhausted
			}

			switch preference {
			case "wallet_only":
				if err := useWallet(); err != nil {
					return err
				}
			case "subscription_only":
				if err := useSubscription(); err != nil {
					return err
				}
			case "wallet_first":
				if err := useWallet(); err != nil {
					if !errors.Is(err, ErrAsyncBillingInsufficientQuota) {
						return err
					}
					if subErr := useSubscription(); subErr != nil {
						return subErr
					}
				}
			default:
				if err := useSubscription(); err != nil {
					if !errors.Is(err, ErrAsyncBillingNoActiveSubscription) && !errors.Is(err, ErrAsyncBillingSubscriptionExhausted) {
						return err
					}
					var strictCount int64
					if countErr := tx.Model(&UserSubscription{}).
						Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?", spec.UserID, "active", now.Unix(), false).
						Count(&strictCount).Error; countErr != nil {
						return countErr
					}
					if strictCount > 0 {
						return err
					}
					if walletErr := useWallet(); walletErr != nil {
						return walletErr
					}
				}
			}
		}

		var clientKeyHash *string
		if spec.ClientKeyHash != "" {
			value := spec.ClientKeyHash
			clientKeyHash = &value
		}
		createdReservation = AsyncBillingReservation{
			ReservationKey: spec.ReservationKey, ProtocolVersion: spec.ProtocolVersion,
			Kind: spec.Kind, PublicTaskID: spec.PublicTaskID, State: AsyncBillingReservationStateReserved,
			ClientKeyHash: clientKeyHash, ClientPayloadHash: spec.ClientPayloadHash, ClientScope: spec.ClientScope,
			UserID: spec.UserID, TokenID: spec.TokenID, FundingSource: fundingSource,
			SubscriptionID: subscriptionID, SubscriptionPeriodID: periodID,
			SubscriptionPlanID: planID, SubscriptionPlanName: planName, SubscriptionTotal: subscriptionTotal,
			InitialQuota: spec.Quota, CurrentQuota: 0,
			CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
		}
		if spec.Quota > 0 {
			if err := applyAsyncBillingReservationDeltaTx(tx, &createdReservation, spec.Quota, now); err != nil {
				return err
			}
			createdReservation.CurrentQuota = spec.Quota
			createdReservation.CacheSyncVersion = 1
			createdReservation.CacheSyncPending = true
		}
		inserted := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "reservation_key"}},
			DoNothing: true,
		}).Create(&createdReservation)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if createdReservation.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, createdReservation.ID, now)
	}
	return &createdReservation, created, nil
}

func applyAsyncBillingReservationDeltaTx(tx *gorm.DB, reservation *AsyncBillingReservation, delta int, now time.Time) error {
	if tx == nil || reservation == nil || reservation.UserID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	if delta == 0 {
		return nil
	}
	var user User
	if err := lockForUpdate(tx.Unscoped()).Where("id = ?", reservation.UserID).First(&user).Error; err != nil {
		return err
	}
	switch reservation.FundingSource {
	case TaskBillingSourceWallet:
		newQuota := int64(user.Quota) - int64(delta)
		if newQuota < 0 {
			return ErrAsyncBillingInsufficientQuota
		}
		if newQuota > int64(common.MaxQuota) {
			return ErrAsyncBillingReservationInvariant
		}
		updated := tx.Unscoped().Model(&User{}).Where("id = ? AND quota = ?", reservation.UserID, user.Quota).
			Update("quota", int(newQuota))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
	case TaskBillingSourceSubscription:
		if reservation.SubscriptionID <= 0 || reservation.SubscriptionPeriodID <= 0 {
			return ErrAsyncBillingReservationInvariant
		}
		if err := applySubscriptionBillingPeriodDeltaTx(
			tx, reservation.SubscriptionID, reservation.SubscriptionPeriodID,
			reservation.UserID, int64(delta), now.Unix(),
		); err != nil {
			if delta > 0 {
				return fmt.Errorf("%w: %v", ErrAsyncBillingSubscriptionExhausted, err)
			}
			return err
		}
	default:
		return ErrAsyncBillingReservationInvariant
	}

	if reservation.TokenID > 0 {
		var token Token
		if err := lockForUpdate(tx.Unscoped()).Where("id = ?", reservation.TokenID).First(&token).Error; err != nil {
			return err
		}
		if token.UserId != reservation.UserID {
			return ErrAsyncBillingReservationInvariant
		}
		newRemain := int64(token.RemainQuota) - int64(delta)
		newUsed := int64(token.UsedQuota) + int64(delta)
		if !token.UnlimitedQuota && newRemain < 0 {
			return ErrAsyncBillingInsufficientQuota
		}
		if newRemain < int64(common.MinQuota) || newRemain > int64(common.MaxQuota) || newUsed < 0 || newUsed > int64(common.MaxQuota) {
			return ErrAsyncBillingReservationInvariant
		}
		updated := tx.Unscoped().Model(&Token{}).Where(
			"id = ? AND remain_quota = ? AND used_quota = ?", reservation.TokenID, token.RemainQuota, token.UsedQuota,
		).Updates(map[string]any{
			"remain_quota": int(newRemain), "used_quota": int(newUsed), "accessed_time": now.Unix(),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
	}
	return nil
}

func ReserveAsyncBillingQuota(ctx context.Context, reservationID int64, targetQuota int, now time.Time) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 || targetQuota < 0 || targetQuota > common.MaxQuota {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	var result AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&result).Error; err != nil {
			return err
		}
		if result.State != AsyncBillingReservationStateReserved && result.State != AsyncBillingReservationStateSendAuthorized {
			if targetQuota <= result.CurrentQuota {
				return nil
			}
			return ErrAsyncBillingReservationAccepted
		}
		if targetQuota <= result.CurrentQuota {
			return nil
		}
		if result.State == AsyncBillingReservationStateSendAuthorized {
			var unresolved int64
			if err := tx.Model(&AsyncBillingAttempt{}).
				Where("reservation_id = ? AND state = ?", result.ID, AsyncBillingAttemptStateAuthorized).
				Count(&unresolved).Error; err != nil {
				return err
			}
			if unresolved > 0 {
				return ErrAsyncBillingReservationAmbiguous
			}
		}
		delta := targetQuota - result.CurrentQuota
		if err := applyAsyncBillingReservationDeltaTx(tx, &result, delta, now); err != nil {
			return err
		}
		updated := tx.Model(&AsyncBillingReservation{}).Where("id = ? AND current_quota = ?", result.ID, result.CurrentQuota).
			Updates(map[string]any{
				"current_quota": targetQuota, "updated_time_ms": now.UnixMilli(),
				"cache_sync_version": gorm.Expr("cache_sync_version + ?", 1),
				"cache_sync_pending": true, "cache_sync_next_retry_ms": 0,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		result.CurrentQuota = targetQuota
		result.UpdatedTimeMs = now.UnixMilli()
		result.CacheSyncVersion++
		result.CacheSyncPending = true
		return nil
	})
	if err == nil && result.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, result.ID, now)
	}
	return &result, err
}

func AuthorizeAsyncBillingAttempt(
	ctx context.Context,
	reservationID int64,
	spec AsyncBillingAttemptSpec,
	now time.Time,
) (*AsyncBillingAttempt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	spec.ChannelVersion = strings.TrimSpace(spec.ChannelVersion)
	if reservationID <= 0 || spec.AttemptIndex < 0 || spec.ChannelID <= 0 || spec.CredentialID < 0 ||
		len(spec.ChannelVersion) > 32 || !utf8.ValidString(spec.ChannelVersion) || strings.ContainsRune(spec.ChannelVersion, '\x00') ||
		spec.AcceptanceIntent == nil || spec.SendDeadlineMs <= now.UnixMilli() ||
		spec.SendDeadlineMs > now.Add(30*time.Minute).UnixMilli() {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if common.BatchUpdateEnabled {
		return nil, ErrAsyncBillingBatchUpdateActive
	}
	if err := SyncAsyncBillingReservationCaches(ctx, reservationID, now); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAsyncBillingCacheFencePending, err)
	}
	var attempt AsyncBillingAttempt
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservation AsyncBillingReservation
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.CacheSyncPending || reservation.CacheSyncedVersion < reservation.CacheSyncVersion {
			return ErrAsyncBillingCacheFencePending
		}
		if reservation.State != AsyncBillingReservationStateReserved && reservation.State != AsyncBillingReservationStateSendAuthorized {
			return ErrAsyncBillingReservationAccepted
		}
		var channel Channel
		if err := lockForUpdate(tx).Select("id", "routing_generation").
			Where("id = ?", spec.ChannelID).First(&channel).Error; err != nil {
			return err
		}
		if channel.RoutingGeneration == "" || channel.RoutingGeneration != spec.ChannelVersion {
			return ErrAsyncBillingReservationInvariant
		}
		intentPayload, intentHash, err := freezeAsyncBillingAcceptanceIntent(&reservation, spec)
		if err != nil {
			return err
		}
		var unresolved []AsyncBillingAttempt
		if err := lockForUpdate(tx).Where(
			"reservation_id = ? AND state = ?", reservation.ID, AsyncBillingAttemptStateAuthorized,
		).Find(&unresolved).Error; err != nil {
			return err
		}
		for index := range unresolved {
			if unresolved[index].AttemptIndex != spec.AttemptIndex {
				return ErrAsyncBillingReservationAmbiguous
			}
		}
		attempt = AsyncBillingAttempt{
			ReservationID: reservation.ID, AttemptIndex: spec.AttemptIndex,
			State: AsyncBillingAttemptStateAuthorized, ChannelID: spec.ChannelID,
			CredentialID: spec.CredentialID, ChannelVersion: spec.ChannelVersion,
			AuthorizedMs: now.UnixMilli(), SendDeadlineMs: spec.SendDeadlineMs,
			IntentProtocol:    AsyncBillingAcceptanceIntentProtocol,
			IntentPayloadHash: intentHash, IntentPayload: intentPayload,
		}
		created := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "reservation_id"}, {Name: "attempt_index"}},
			DoNothing: true,
		}).Create(&attempt)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			if err := tx.Where("reservation_id = ? AND attempt_index = ?", reservation.ID, spec.AttemptIndex).
				First(&attempt).Error; err != nil {
				return err
			}
			if attempt.State != AsyncBillingAttemptStateAuthorized || attempt.ChannelID != spec.ChannelID ||
				attempt.CredentialID != spec.CredentialID || attempt.ChannelVersion != spec.ChannelVersion ||
				attempt.SendDeadlineMs != spec.SendDeadlineMs ||
				attempt.IntentProtocol != AsyncBillingAcceptanceIntentProtocol ||
				!strings.EqualFold(attempt.IntentPayloadHash, intentHash) {
				return ErrAsyncBillingReservationInvariant
			}
		}
		updated := tx.Model(&AsyncBillingReservation{}).Where("id = ? AND state IN ?", reservation.ID,
			[]string{AsyncBillingReservationStateReserved, AsyncBillingReservationStateSendAuthorized}).
			Updates(map[string]any{
				"state": AsyncBillingReservationStateSendAuthorized, "send_authorized_ms": now.UnixMilli(),
				"updated_time_ms": now.UnixMilli(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		return nil
	})
	return &attempt, err
}

func RejectAsyncBillingAttempt(ctx context.Context, reservationID int64, attemptIndex int, failureCode string, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 || attemptIndex < 0 {
		return ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	failureCode = boundedAsyncBillingText(failureCode, 128)
	if strings.TrimSpace(failureCode) == "" {
		return ErrAsyncBillingReservationInvariant
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservation AsyncBillingReservation
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.State == AsyncBillingReservationStateAccepted || reservation.State == AsyncBillingReservationStateTerminal {
			return nil
		}
		var attempt AsyncBillingAttempt
		if err := lockForUpdate(tx).Where("reservation_id = ? AND attempt_index = ?", reservationID, attemptIndex).
			First(&attempt).Error; err != nil {
			return err
		}
		if attempt.State == AsyncBillingAttemptStateRejected {
			return nil
		}
		if attempt.State != AsyncBillingAttemptStateAuthorized {
			return ErrAsyncBillingReservationInvariant
		}
		updated := tx.Model(&AsyncBillingAttempt{}).Where("id = ? AND state = ?", attempt.ID, AsyncBillingAttemptStateAuthorized).
			Updates(map[string]any{
				"state": AsyncBillingAttemptStateRejected, "resolved_ms": now.UnixMilli(), "failure_code": failureCode,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		return tx.Model(&AsyncBillingReservation{}).Where("id = ?", reservation.ID).
			Updates(map[string]any{"last_error": failureCode, "updated_time_ms": now.UnixMilli()}).Error
	})
}

func ReleaseAsyncBillingReservation(ctx context.Context, reservationID int64, now time.Time) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	var result AsyncBillingReservation
	hadQuota := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&result).Error; err != nil {
			return err
		}
		if result.State == AsyncBillingReservationStateReleased {
			return nil
		}
		if result.State == AsyncBillingReservationStateAccepted || result.State == AsyncBillingReservationStateTerminal {
			return ErrAsyncBillingReservationAccepted
		}
		if result.State != AsyncBillingReservationStateReserved &&
			result.State != AsyncBillingReservationStateSendAuthorized &&
			result.State != AsyncBillingReservationStateManualReview {
			return ErrAsyncBillingReservationInvariant
		}
		var unresolved int64
		if err := tx.Model(&AsyncBillingAttempt{}).
			Where("reservation_id = ? AND state = ?", result.ID, AsyncBillingAttemptStateAuthorized).
			Count(&unresolved).Error; err != nil {
			return err
		}
		if unresolved > 0 {
			return ErrAsyncBillingReservationAmbiguous
		}
		if result.CurrentQuota > 0 {
			hadQuota = true
			if err := applyAsyncBillingReservationDeltaTx(tx, &result, -result.CurrentQuota, now); err != nil {
				return err
			}
		}
		updates := map[string]any{
			"state": AsyncBillingReservationStateReleased, "current_quota": 0,
			"updated_time_ms": now.UnixMilli(), "terminal_time_ms": now.UnixMilli(),
		}
		if hadQuota {
			updates["cache_sync_version"] = gorm.Expr("cache_sync_version + ?", 1)
			updates["cache_sync_pending"] = true
			updates["cache_sync_next_retry_ms"] = 0
		}
		updated := tx.Model(&AsyncBillingReservation{}).Where("id = ? AND state IN ?", result.ID,
			[]string{
				AsyncBillingReservationStateReserved,
				AsyncBillingReservationStateSendAuthorized,
				AsyncBillingReservationStateManualReview,
			}).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		result.State = AsyncBillingReservationStateReleased
		result.CurrentQuota = 0
		result.UpdatedTimeMs = now.UnixMilli()
		result.TerminalTimeMs = now.UnixMilli()
		if hadQuota {
			result.CacheSyncVersion++
			result.CacheSyncPending = true
		}
		return nil
	})
	if err == nil && result.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, result.ID, now)
	}
	return &result, err
}

func freezeAsyncBillingAcceptedProjectionTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	spec AsyncBillingAcceptedProjectionSpec,
	now time.Time,
) error {
	if tx == nil || reservation == nil || reservation.ID <= 0 ||
		(reservation.State != AsyncBillingReservationStateSendAuthorized &&
			!(reservation.State == AsyncBillingReservationStateManualReview &&
				(reservation.ManualReviewKind == AsyncBillingReviewKindSendOutcome ||
					reservation.ManualReviewKind == AsyncBillingReviewKindAcceptedHandoff ||
					reservation.ManualReviewKind == AsyncBillingReviewKindAcceptanceOverage))) ||
		spec.ChannelID <= 0 || strings.TrimSpace(spec.TaskIdentity) == "" ||
		len(strings.TrimSpace(spec.TaskIdentity)) > 191 || reservation.AcceptedQuota < 0 ||
		reservation.AcceptedQuota > common.MaxQuota {
		return ErrAsyncBillingReservationInvariant
	}
	modelName := strings.TrimSpace(spec.ModelName)
	group := strings.TrimSpace(spec.Group)
	if len(modelName) > 191 || len(group) > 64 || spec.Audit == nil || validateAsyncBillingAuditSnapshot(*spec.Audit) != nil {
		return ErrAsyncBillingReservationInvariant
	}
	projectionKey := fmt.Sprintf("async:%d:accepted:v1", reservation.ID)
	if len(projectionKey) > maxBillingLogOperationKeyBytes {
		return ErrAsyncBillingReservationInvariant
	}

	username, tokenName, err := snapshotBillingLogNames(tx, reservation.UserID, reservation.TokenID)
	if err != nil {
		return err
	}
	other := make(map[string]any, len(spec.Other)+7)
	for key, value := range spec.Other {
		other[key] = value
	}
	other["is_task"] = true
	other["task_kind"] = reservation.Kind
	other["task_id"] = strings.TrimSpace(spec.TaskIdentity)
	other["billing_operation"] = projectionKey
	other["billing_source"] = reservation.FundingSource
	other["accepted_quota"] = reservation.AcceptedQuota
	other["request_delta"] = 1
	otherJSON, err := common.Marshal(other)
	if err != nil {
		return err
	}
	content := strings.TrimSpace(spec.Content)
	if content == "" {
		content = "async task accepted consumption"
	}
	logEntry := &Log{
		UserId: reservation.UserID, CreatedAt: now.Unix(), Type: LogTypeConsume,
		Content: content, Username: username, TokenName: tokenName,
		ModelName: modelName, Quota: reservation.AcceptedQuota,
		ChannelId: spec.ChannelID, TokenId: reservation.TokenID, Group: group,
		Ip: spec.Audit.ClientIP, RequestId: spec.Audit.RequestID,
		UpstreamRequestId: reservation.UpstreamTaskID, Other: string(otherJSON),
	}
	if _, _, err := CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
		OperationKey: projectionKey, Kind: BillingLogProjectionKindAccepted,
		ReferenceID: reservation.ID, Required: common.LogConsumeEnabled, Entry: logEntry,
	}, now); err != nil {
		return err
	}
	if _, _, err := CreateBillingStatsProjectionTx(tx, BillingStatsProjectionSpec{
		OperationKey: projectionKey, Kind: BillingStatsProjectionKindAccepted,
		ReferenceID: reservation.ID, UserID: reservation.UserID, ChannelID: spec.ChannelID,
		QuotaDelta: reservation.AcceptedQuota, RequestDelta: 1,
		DataExportRequired: common.LogConsumeEnabled && spec.Audit.DataExportEnabled,
		DataExportUsername: username, DataExportModelName: modelName,
		DataExportCreatedAt: now.Unix(), DataExportTokenUsed: 0,
		DataExportUseGroup: group, DataExportTokenID: reservation.TokenID,
		DataExportNodeName: spec.Audit.NodeName,
	}, now); err != nil {
		return err
	}
	reservation.AcceptedProjectionKey = &projectionKey
	reservation.AcceptedProjectionState = AsyncBillingAcceptedProjectionCompleted
	reservation.AcceptedProjectionChannelID = spec.ChannelID
	reservation.AcceptedProjectionModelName = modelName
	reservation.AcceptedProjectionGroup = group
	reservation.AcceptedProjectionTaskIdentity = strings.TrimSpace(spec.TaskIdentity)
	reservation.AcceptedProjectionQuota = reservation.AcceptedQuota
	reservation.AcceptedProjectionRequestDelta = 1
	reservation.AcceptedProjectionLogProtocol = 0
	reservation.AcceptedProjectionLogHash = ""
	reservation.AcceptedProjectionLogPayload = ""
	reservation.AcceptedProjectionCreatedMs = now.UnixMilli()
	reservation.AcceptedProjectionNextRetryMs = 0
	return nil
}

func acceptAsyncBillingReservationTx(
	tx *gorm.DB,
	reservationID int64,
	attemptIndex int,
	kind string,
	upstreamTaskID string,
	finalQuota int,
	auditOverride *AsyncBillingAcceptedAuditSnapshot,
	replaySpec AsyncBillingReplaySpec,
	now time.Time,
	createTask func(*AsyncBillingReservation, *AsyncBillingAttempt, *asyncBillingAcceptanceIntentEnvelope) error,
) (*AsyncBillingReservation, *AsyncBillingAttempt, *asyncBillingAcceptanceIntentEnvelope, error) {
	if tx == nil || reservationID <= 0 || attemptIndex < 0 || finalQuota < 0 || finalQuota > common.MaxQuota || createTask == nil {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if upstreamTaskID == "" || len(upstreamTaskID) > 191 || !utf8.ValidString(upstreamTaskID) ||
		strings.ContainsAny(upstreamTaskID, "\r\n\x00") {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	normalizedReplay, replayHash, err := normalizeAsyncBillingReplaySpec(replaySpec)
	if err != nil {
		return nil, nil, nil, err
	}
	var reservation AsyncBillingReservation
	if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
		return nil, nil, nil, err
	}
	if reservation.Kind != kind {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	if reservation.State == AsyncBillingReservationStateAccepted || reservation.State == AsyncBillingReservationStateTerminal {
		if reservation.AcceptedAttemptIndex != attemptIndex || reservation.UpstreamTaskID != upstreamTaskID ||
			reservation.AcceptedQuota != finalQuota || !reservation.ReplayReady ||
			!strings.EqualFold(reservation.ReplayHash, replayHash) {
			return nil, nil, nil, ErrAsyncBillingReservationInvariant
		}
		var acceptedAttempt AsyncBillingAttempt
		if err := tx.Where("reservation_id = ? AND attempt_index = ?", reservation.ID, attemptIndex).
			First(&acceptedAttempt).Error; err != nil {
			return nil, nil, nil, err
		}
		if acceptedAttempt.State != AsyncBillingAttemptStateAccepted {
			return nil, nil, nil, ErrAsyncBillingReservationInvariant
		}
		intent, err := thawAsyncBillingAcceptanceIntent(&acceptedAttempt)
		if err != nil {
			return nil, nil, nil, err
		}
		if auditOverride != nil {
			if validateAsyncBillingAuditSnapshot(*auditOverride) != nil {
				return nil, nil, nil, ErrAsyncBillingReservationInvariant
			}
			intent.Audit = *auditOverride
		}
		return &reservation, &acceptedAttempt, intent, nil
	}
	if reservation.State != AsyncBillingReservationStateSendAuthorized &&
		!(reservation.State == AsyncBillingReservationStateManualReview &&
			(reservation.ManualReviewKind == AsyncBillingReviewKindSendOutcome ||
				reservation.ManualReviewKind == AsyncBillingReviewKindAcceptedHandoff ||
				reservation.ManualReviewKind == AsyncBillingReviewKindAcceptanceOverage)) {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	var attempt AsyncBillingAttempt
	if err := lockForUpdate(tx).Where("reservation_id = ? AND attempt_index = ?", reservation.ID, attemptIndex).
		First(&attempt).Error; err != nil {
		return nil, nil, nil, err
	}
	if attempt.State != AsyncBillingAttemptStateAuthorized {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	intent, err := thawAsyncBillingAcceptanceIntent(&attempt)
	if err != nil || intent.Kind != kind {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	if auditOverride != nil {
		if validateAsyncBillingAuditSnapshot(*auditOverride) != nil {
			return nil, nil, nil, ErrAsyncBillingReservationInvariant
		}
		intent.Audit = *auditOverride
	}
	if delta := finalQuota - reservation.CurrentQuota; delta != 0 {
		if err := applyAsyncBillingReservationDeltaTx(tx, &reservation, delta, now); err != nil {
			return nil, nil, nil, err
		}
	}
	reservation.CurrentQuota = finalQuota
	reservation.AcceptedQuota = finalQuota
	reservation.AcceptedAttemptIndex = attemptIndex
	reservation.UpstreamTaskID = upstreamTaskID
	reservation.ReplayProtocol = AsyncBillingReplayProtocol
	reservation.ReplayReady = true
	reservation.ReplayStatusCode = normalizedReplay.StatusCode
	reservation.ReplayContentType = normalizedReplay.ContentType
	reservation.ReplayHeadersJSON = normalizedReplay.HeadersJSON
	reservation.ReplayBody = append([]byte(nil), normalizedReplay.Body...)
	reservation.ReplayHash = replayHash
	if err := createTask(&reservation, &attempt, intent); err != nil {
		return nil, nil, nil, err
	}
	if updated := tx.Model(&AsyncBillingAttempt{}).Where("id = ? AND state = ?", attempt.ID, AsyncBillingAttemptStateAuthorized).
		Updates(map[string]any{"state": AsyncBillingAttemptStateAccepted, "resolved_ms": now.UnixMilli()}); updated.Error != nil {
		return nil, nil, nil, updated.Error
	} else if updated.RowsAffected != 1 {
		return nil, nil, nil, ErrAsyncBillingReservationInvariant
	}
	attempt.State = AsyncBillingAttemptStateAccepted
	attempt.ResolvedMs = now.UnixMilli()
	return &reservation, &attempt, intent, nil
}

func commitAsyncBillingAcceptedStateTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	attemptIndex int,
	finalQuota int,
	taskID int64,
	midjourneyID int,
	upstreamTaskID string,
	projectionSpec AsyncBillingAcceptedProjectionSpec,
	state string,
	terminalTimeMs int64,
	now time.Time,
) error {
	if tx == nil || reservation == nil || reservation.ID <= 0 || attemptIndex < 0 ||
		finalQuota < 0 || finalQuota > common.MaxQuota ||
		(state != AsyncBillingReservationStateAccepted && state != AsyncBillingReservationStateTerminal) {
		return ErrAsyncBillingReservationInvariant
	}
	if err := freezeAsyncBillingAcceptedProjectionTx(tx, reservation, projectionSpec, now); err != nil {
		return err
	}
	updates := map[string]any{
		"state": state, "current_quota": finalQuota, "accepted_quota": finalQuota,
		"accepted_attempt_index": attemptIndex, "upstream_task_id": strings.TrimSpace(upstreamTaskID),
		"accepted_time_ms": now.UnixMilli(), "terminal_time_ms": terminalTimeMs,
		"updated_time_ms": now.UnixMilli(), "last_error": "",
		"cache_sync_version": gorm.Expr("cache_sync_version + ?", 1),
		"cache_sync_pending": true, "cache_sync_next_retry_ms": 0,
		"accepted_projection_key":           reservation.AcceptedProjectionKey,
		"accepted_projection_state":         reservation.AcceptedProjectionState,
		"accepted_projection_channel_id":    reservation.AcceptedProjectionChannelID,
		"accepted_projection_model_name":    reservation.AcceptedProjectionModelName,
		"accepted_projection_group":         reservation.AcceptedProjectionGroup,
		"accepted_projection_task_identity": reservation.AcceptedProjectionTaskIdentity,
		"accepted_projection_quota":         reservation.AcceptedProjectionQuota,
		"accepted_projection_request_delta": reservation.AcceptedProjectionRequestDelta,
		"accepted_projection_log_protocol":  reservation.AcceptedProjectionLogProtocol,
		"accepted_projection_log_hash":      reservation.AcceptedProjectionLogHash,
		"accepted_projection_log_payload":   reservation.AcceptedProjectionLogPayload,
		"accepted_projection_created_ms":    reservation.AcceptedProjectionCreatedMs,
		"accepted_projection_next_retry_ms": 0,
		"manual_review_kind":                "", "manual_review_reason": "", "manual_review_required_ms": 0,
		"review_target_quota": 0, "review_audit_protocol": 0,
		"review_audit_hash": "", "review_audit_payload": []byte(nil),
		"replay_protocol": reservation.ReplayProtocol, "replay_ready": reservation.ReplayReady,
		"replay_status_code": reservation.ReplayStatusCode, "replay_content_type": reservation.ReplayContentType,
		"replay_headers_json": reservation.ReplayHeadersJSON, "replay_body": reservation.ReplayBody,
		"replay_hash": reservation.ReplayHash,
	}
	if taskID > 0 {
		updates["task_id"] = taskID
	}
	if midjourneyID > 0 {
		updates["midjourney_id"] = midjourneyID
	}
	wasManualReview := reservation.State == AsyncBillingReservationStateManualReview
	if wasManualReview {
		updates["review_version"] = gorm.Expr("review_version + ?", 1)
	}
	updated := tx.Model(&AsyncBillingReservation{}).Where("id = ? AND state IN ?", reservation.ID,
		[]string{AsyncBillingReservationStateSendAuthorized, AsyncBillingReservationStateManualReview}).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrAsyncBillingReservationInvariant
	}
	reservation.State = state
	reservation.TaskID = taskID
	reservation.MidjourneyID = midjourneyID
	reservation.UpstreamTaskID = strings.TrimSpace(upstreamTaskID)
	reservation.AcceptedTimeMs = now.UnixMilli()
	reservation.TerminalTimeMs = terminalTimeMs
	reservation.ManualReviewKind = ""
	reservation.ManualReviewReason = ""
	reservation.ManualReviewRequiredMs = 0
	reservation.ReviewTargetQuota = 0
	reservation.ReviewAuditProtocol = 0
	reservation.ReviewAuditHash = ""
	reservation.ReviewAuditPayload = nil
	reservation.CacheSyncVersion++
	reservation.CacheSyncPending = true
	if wasManualReview {
		reservation.ReviewVersion++
	}
	return nil
}

func AcceptAsyncTaskReservation(
	ctx context.Context,
	reservationID int64,
	attemptIndex int,
	task *Task,
	upstreamTaskID string,
	finalQuota int,
	finalAudit AsyncBillingAcceptedAuditSnapshot,
	replaySpec AsyncBillingReplaySpec,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if task == nil || strings.TrimSpace(upstreamTaskID) == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	var result *AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		reservation, attempt, intent, err := acceptAsyncBillingReservationTx(
			tx, reservationID, attemptIndex, AsyncBillingKindTask, strings.TrimSpace(upstreamTaskID), finalQuota, &finalAudit, replaySpec, now,
			func(reservation *AsyncBillingReservation, attempt *AsyncBillingAttempt, intent *asyncBillingAcceptanceIntentEnvelope) error {
				if reservation.TaskID > 0 {
					var existing Task
					if err := tx.Where("id = ?", reservation.TaskID).First(&existing).Error; err != nil {
						return err
					}
					*task = existing
					return nil
				}
				task.TaskID = reservation.PublicTaskID
				task.UserId = reservation.UserID
				task.ChannelId = attempt.ChannelID
				task.Group = intent.Audit.Group
				task.Quota = finalQuota
				task.PrivateData.BillingProtocolVersion = reservation.ProtocolVersion
				task.PrivateData.AsyncBillingReservationID = reservation.ID
				task.PrivateData.BillingSource = reservation.FundingSource
				task.PrivateData.SubscriptionId = reservation.SubscriptionID
				task.PrivateData.TokenId = reservation.TokenID
				task.PrivateData.UpstreamTaskID = strings.TrimSpace(upstreamTaskID)
				task.PrivateData.RoutingCredentialID = attempt.CredentialID
				task.PrivateData.RoutingChannelGeneration = attempt.ChannelVersion
				task.PrivateData.BillingAudit = &intent.Audit
				if err := task.IsolateV2BillingFromLegacyPollers(finalQuota); err != nil {
					return err
				}
				if err := tx.Create(task).Error; err != nil {
					return err
				}
				reservation.TaskID = task.ID
				return nil
			},
		)
		if err != nil {
			return err
		}
		if attempt == nil || intent == nil || intent.Task == nil {
			return ErrAsyncBillingReservationInvariant
		}
		if reservation.State == AsyncBillingReservationStateAccepted || reservation.State == AsyncBillingReservationStateTerminal {
			if reservation.TaskID > 0 && task.ID == 0 {
				if err := tx.Where("id = ?", reservation.TaskID).First(task).Error; err != nil {
					return err
				}
			}
			result = reservation
			return nil
		}
		modelName := strings.TrimSpace(intent.Audit.OriginModelName)
		if modelName == "" && task.PrivateData.BillingContext != nil {
			modelName = strings.TrimSpace(task.PrivateData.BillingContext.OriginModelName)
		}
		if modelName == "" {
			modelName = strings.TrimSpace(task.Properties.UpstreamModelName)
		}
		if err := commitAsyncBillingAcceptedStateTx(tx, reservation, attemptIndex, finalQuota,
			task.ID, 0, upstreamTaskID, AsyncBillingAcceptedProjectionSpec{
				ChannelID: task.ChannelId, ModelName: modelName, Group: intent.Audit.Group,
				TaskIdentity: task.TaskID, Content: intent.Audit.Content,
				Other: asyncBillingAcceptedOther(intent.Audit),
				Audit: &intent.Audit,
			}, AsyncBillingReservationStateAccepted, 0, now); err != nil {
			return err
		}
		result = reservation
		return nil
	})
	if err == nil && result != nil && result.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, result.ID, now)
	}
	return result, err
}

func AcceptAsyncMidjourneyReservation(
	ctx context.Context,
	reservationID int64,
	attemptIndex int,
	task *Midjourney,
	upstreamTaskID string,
	finalQuota int,
	finalAudit AsyncBillingAcceptedAuditSnapshot,
	replaySpec AsyncBillingReplaySpec,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if task == nil || strings.TrimSpace(upstreamTaskID) == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if validateAsyncBillingAuditSnapshot(finalAudit) != nil {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	var result *AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		reservation, attempt, intent, err := acceptAsyncBillingReservationTx(
			tx, reservationID, attemptIndex, AsyncBillingKindMidjourney, strings.TrimSpace(upstreamTaskID), finalQuota, &finalAudit, replaySpec, now,
			func(reservation *AsyncBillingReservation, attempt *AsyncBillingAttempt, intent *asyncBillingAcceptanceIntentEnvelope) error {
				if reservation.MidjourneyID > 0 {
					var existing Midjourney
					if err := tx.Where("id = ?", reservation.MidjourneyID).First(&existing).Error; err != nil {
						return err
					}
					*task = existing
					return nil
				}
				task.MjId = reservation.PublicTaskID
				task.UpstreamTaskID = strings.TrimSpace(upstreamTaskID)
				task.UserId = reservation.UserID
				task.ChannelId = attempt.ChannelID
				task.Group = intent.Audit.Group
				task.Quota = finalQuota
				task.BillingProtocolVersion = reservation.ProtocolVersion
				task.AsyncBillingReservationID = reservation.ID
				task.BillingSource = reservation.FundingSource
				task.SubscriptionId = reservation.SubscriptionID
				task.TokenId = reservation.TokenID
				task.RoutingCredentialID = attempt.CredentialID
				task.ChannelGeneration = attempt.ChannelVersion
				if err := task.IsolateV2BillingFromLegacyPollers(finalQuota); err != nil {
					return err
				}
				auditPayload, marshalErr := common.Marshal(intent.Audit)
				if marshalErr != nil || len(auditPayload) > maxAsyncBillingIntentBytes {
					return ErrAsyncBillingReservationInvariant
				}
				task.BillingAuditPayload = string(auditPayload)
				if err := tx.Create(task).Error; err != nil {
					return err
				}
				reservation.MidjourneyID = task.Id
				return nil
			},
		)
		if err != nil {
			return err
		}
		if attempt == nil || intent == nil || intent.Midjourney == nil {
			return ErrAsyncBillingReservationInvariant
		}
		if reservation.State == AsyncBillingReservationStateAccepted || reservation.State == AsyncBillingReservationStateTerminal {
			if reservation.MidjourneyID > 0 && task.Id == 0 {
				if err := tx.Where("id = ?", reservation.MidjourneyID).First(task).Error; err != nil {
					return err
				}
			}
			result = reservation
			return nil
		}
		modelName := strings.ToLower(strings.TrimSpace(task.Action))
		state := AsyncBillingReservationStateAccepted
		terminalTime := int64(0)
		if task.Progress == "100%" && (task.Status == "SUCCESS" || task.Status == "FAILURE") {
			operation, operationErr := createAcceptedMidjourneyTerminalOperationTx(tx, task, now)
			if operationErr != nil {
				return operationErr
			}
			if task.Status == "SUCCESS" || operation.Kind == TaskBillingOperationKindNoop {
				state = AsyncBillingReservationStateTerminal
				terminalTime = now.UnixMilli()
			}
		}
		if err := commitAsyncBillingAcceptedStateTx(tx, reservation, attemptIndex, finalQuota,
			0, task.Id, upstreamTaskID, AsyncBillingAcceptedProjectionSpec{
				ChannelID: task.ChannelId, ModelName: modelName, Group: intent.Audit.Group,
				TaskIdentity: task.MjId, Content: intent.Audit.Content,
				Other: asyncBillingAcceptedOther(intent.Audit), Audit: &intent.Audit,
			}, state, terminalTime, now); err != nil {
			return err
		}
		result = reservation
		return nil
	})
	if err == nil && result != nil && result.CacheSyncPending {
		syncAsyncBillingReservationCachesBestEffort(ctx, result.ID, now)
	}
	return result, err
}

// CompleteAsyncBillingReservationTx is called from a terminal billing
// transaction. It applies the final delta and marks the reservation terminal in
// the same commit as the task quota and terminal operation.
func CompleteAsyncBillingReservationTx(
	tx *gorm.DB,
	reservationID int64,
	userID int,
	currentQuota int,
	targetQuota int,
	now time.Time,
) error {
	if tx == nil || reservationID <= 0 || userID <= 0 || currentQuota < 0 || targetQuota < 0 ||
		currentQuota > common.MaxQuota || targetQuota > common.MaxQuota {
		return ErrAsyncBillingReservationInvariant
	}
	var reservation AsyncBillingReservation
	if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
		return err
	}
	if reservation.UserID != userID || reservation.CurrentQuota != currentQuota ||
		(reservation.State != AsyncBillingReservationStateAccepted && reservation.State != AsyncBillingReservationStateTerminal) {
		return ErrAsyncBillingReservationInvariant
	}
	if reservation.State == AsyncBillingReservationStateTerminal {
		if reservation.CurrentQuota == targetQuota {
			return nil
		}
		return ErrAsyncBillingReservationInvariant
	}
	if targetQuota > currentQuota {
		reason := boundedAsyncBillingError(fmt.Sprintf(
			"terminal_quota_exceeds_reservation: reserved=%d target=%d", currentQuota, targetQuota,
		))
		updated := tx.Model(&AsyncBillingReservation{}).Where(
			"id = ? AND state = ? AND current_quota = ?", reservation.ID, AsyncBillingReservationStateAccepted, currentQuota,
		).Updates(map[string]any{
			"state":                     AsyncBillingReservationStateManualReview,
			"manual_review_kind":        AsyncBillingReviewKindTerminalOverage,
			"manual_review_required_ms": now.UnixMilli(),
			"manual_review_reason":      reason,
			"last_error":                reason,
			"review_version":            gorm.Expr("review_version + ?", 1),
			"updated_time_ms":           now.UnixMilli(),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		return ErrAsyncBillingTerminalOverage
	}
	if delta := targetQuota - currentQuota; delta != 0 {
		if err := applyAsyncBillingReservationDeltaTx(tx, &reservation, delta, now); err != nil {
			return err
		}
	}
	updated := tx.Model(&AsyncBillingReservation{}).Where(
		"id = ? AND state = ? AND current_quota = ?", reservation.ID, AsyncBillingReservationStateAccepted, currentQuota,
	).Updates(map[string]any{
		"state": AsyncBillingReservationStateTerminal, "current_quota": targetQuota,
		"terminal_time_ms": now.UnixMilli(), "updated_time_ms": now.UnixMilli(),
		"cache_sync_version": gorm.Expr("cache_sync_version + ?", 1),
		"cache_sync_pending": true, "cache_sync_next_retry_ms": 0,
	})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrAsyncBillingReservationInvariant
	}
	return nil
}

func FindRecoverableAsyncBillingReservationIDsContext(
	ctx context.Context,
	now time.Time,
	staleReservedAfter time.Duration,
	limit int,
) ([]int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if staleReservedAfter <= 0 {
		staleReservedAfter = 2 * time.Minute
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	cutoff := now.Add(-staleReservedAfter).UnixMilli()
	var ids []int64
	err := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("(state IN ? AND updated_time_ms <= ?) AND NOT EXISTS ("+
			"SELECT 1 FROM async_billing_attempts WHERE async_billing_attempts.reservation_id = async_billing_reservations.id AND async_billing_attempts.state = ?)",
			[]string{AsyncBillingReservationStateReserved, AsyncBillingReservationStateSendAuthorized}, cutoff,
			AsyncBillingAttemptStateAuthorized).
		Order("id asc").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

func FindRecoverableAsyncBillingReservationIDs(now time.Time, staleReservedAfter time.Duration, limit int) ([]int64, error) {
	return FindRecoverableAsyncBillingReservationIDsContext(
		context.Background(), now, staleReservedAfter, limit,
	)
}

func HasRecoverableAsyncBillingReservationsContext(
	ctx context.Context,
	now time.Time,
	staleReservedAfter time.Duration,
) bool {
	ids, err := FindRecoverableAsyncBillingReservationIDsContext(ctx, now, staleReservedAfter, 1)
	return err == nil && len(ids) > 0
}

func HasRecoverableAsyncBillingReservations(now time.Time, staleReservedAfter time.Duration) bool {
	return HasRecoverableAsyncBillingReservationsContext(context.Background(), now, staleReservedAfter)
}

func HasOutstandingAsyncBillingReservationsForUser(userID int) (bool, error) {
	if userID <= 0 {
		return false, ErrAsyncBillingReservationInvariant
	}
	var count int64
	err := DB.Model(&AsyncBillingReservation{}).Where("user_id = ? AND (state IN ? OR accepted_projection_state IN ?)", userID,
		[]string{
			AsyncBillingReservationStateReserved,
			AsyncBillingReservationStateSendAuthorized,
			AsyncBillingReservationStateManualReview,
			AsyncBillingReservationStateAccepted,
		}, []string{AsyncBillingAcceptedProjectionPending, AsyncBillingAcceptedProjectionLogPending}).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// EnsureNoAsyncBillingReferencesForUserTx is called only after the caller has
// locked the user row. Creation and balance mutation also lock that row, so the
// reference check and a hard delete cannot race each other.
func EnsureNoAsyncBillingReferencesForUserTx(tx *gorm.DB, userID int) error {
	if tx == nil || userID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	tables := []struct {
		model any
		where string
		args  []any
	}{
		{&AsyncBillingReservation{}, "user_id = ?", []any{userID}},
		{&TaskBillingOperation{}, "user_id = ?", []any{userID}},
		{&MidjourneyBillingOperation{}, "user_id = ?", []any{userID}},
		{&BillingStatsProjection{}, "user_id = ? AND state IN ?", []any{userID, []string{
			BillingStatsProjectionStatePending, BillingStatsProjectionStateRunning,
		}}},
	}
	for _, table := range tables {
		if !tx.Migrator().HasTable(table.model) {
			continue
		}
		var count int64
		if err := tx.Model(table.model).Where(table.where, table.args...).Limit(1).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAsyncBillingOutstandingReferences
		}
	}
	legacyReference, err := hasUnmaterializedLegacyBillingReferenceTx(tx, userID, 0)
	if err != nil {
		return err
	}
	if legacyReference {
		return ErrAsyncBillingOutstandingReferences
	}
	return nil
}

// EnsureNoAsyncBillingReferencesForSubscriptionTx is called with the owning
// user and subscription rows locked in that order. Period ledgers are retained;
// a subscription cannot be removed while any durable receipt may still use it.
func EnsureNoAsyncBillingReferencesForSubscriptionTx(tx *gorm.DB, subscriptionID int, userID int) error {
	if tx == nil || subscriptionID <= 0 || userID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	tables := []struct {
		model any
		where string
	}{
		{&AsyncBillingReservation{}, "subscription_id = ?"},
		{&TaskBillingOperation{}, "subscription_id = ?"},
		{&MidjourneyBillingOperation{}, "subscription_id = ?"},
	}
	for _, table := range tables {
		if !tx.Migrator().HasTable(table.model) {
			continue
		}
		var count int64
		if err := tx.Model(table.model).Where(table.where, subscriptionID).Limit(1).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAsyncBillingOutstandingReferences
		}
	}
	legacyReference, err := hasUnmaterializedLegacyBillingReferenceTx(tx, userID, subscriptionID)
	if err != nil {
		return err
	}
	if legacyReference {
		return ErrAsyncBillingOutstandingReferences
	}
	return nil
}

func hasUnmaterializedLegacyBillingReferenceTx(tx *gorm.DB, userID, subscriptionID int) (bool, error) {
	if tx == nil || userID <= 0 || subscriptionID < 0 {
		return false, ErrAsyncBillingReservationInvariant
	}
	if tx.Migrator().HasTable(&Task{}) {
		const batchSize = 256
		var cursor int64
		for {
			var tasks []Task
			query := tx.Where("user_id = ? AND id > ?", userID, cursor)
			if tx.Migrator().HasTable(&TaskBillingOperation{}) {
				query = query.Where("NOT EXISTS (SELECT 1 FROM task_billing_operations WHERE task_billing_operations.task_id = tasks.id)")
			}
			if err := query.Order("id asc").Limit(batchSize).Find(&tasks).Error; err != nil {
				return false, err
			}
			for index := range tasks {
				task := &tasks[index]
				if !SupportsDurableTaskBillingProtocol(task.PrivateData.BillingProtocolVersion) {
					continue
				}
				if task.PrivateData.BillingProtocolVersion == TaskBillingHistoricalProtocolVersion &&
					(task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure) {
					continue
				}
				if subscriptionID == 0 || task.PrivateData.BillingProtocolVersion == TaskBillingHistoricalProtocolVersion ||
					(strings.TrimSpace(task.PrivateData.BillingSource) == TaskBillingSourceSubscription &&
						task.PrivateData.SubscriptionId == subscriptionID) {
					return true, nil
				}
			}
			if len(tasks) < batchSize {
				break
			}
			cursor = tasks[len(tasks)-1].ID
		}
	}
	if tx.Migrator().HasTable(&Midjourney{}) {
		historicalPredicate := "(billing_protocol_version IS NULL OR billing_protocol_version = ?) AND " +
			"(COALESCE(progress, '') != ? OR COALESCE(status, '') NOT IN ?)"
		var query *gorm.DB
		if subscriptionID > 0 {
			query = tx.Model(&Midjourney{}).Where(
				"user_id = ? AND ((billing_protocol_version BETWEEN ? AND ? AND billing_source = ? AND subscription_id = ?) OR ("+
					historicalPredicate+"))",
				userID, TaskBillingLegacyProtocolVersion, TaskBillingProtocolVersion,
				TaskBillingSourceSubscription, subscriptionID,
				TaskBillingHistoricalProtocolVersion, "100%", []string{"SUCCESS", "FAILURE"},
			)
		} else {
			query = tx.Model(&Midjourney{}).Where(
				"user_id = ? AND (billing_protocol_version BETWEEN ? AND ? OR ("+historicalPredicate+"))",
				userID, TaskBillingLegacyProtocolVersion, TaskBillingProtocolVersion,
				TaskBillingHistoricalProtocolVersion, "100%", []string{"SUCCESS", "FAILURE"},
			)
		}
		if tx.Migrator().HasTable(&MidjourneyBillingOperation{}) {
			query = query.Where("NOT EXISTS (SELECT 1 FROM midjourney_billing_operations WHERE midjourney_billing_operations.midjourney_id = midjourneys.id)")
		}
		var count int64
		if err := query.Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func MarkAsyncBillingAcceptanceOverageManualReview(
	ctx context.Context,
	reservationID int64,
	targetQuota int,
	audit AsyncBillingAcceptedAuditSnapshot,
	upstreamTaskID string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	return markAsyncBillingAcceptedHandoffManualReview(
		ctx, reservationID, AsyncBillingReviewKindAcceptanceOverage, targetQuota, audit,
		upstreamTaskID, nil, "", now,
	)
}

func MarkAsyncBillingAcceptanceOverageManualReviewWithReplay(
	ctx context.Context,
	reservationID int64,
	targetQuota int,
	audit AsyncBillingAcceptedAuditSnapshot,
	upstreamTaskID string,
	replay AsyncBillingReplaySpec,
	now time.Time,
) (*AsyncBillingReservation, error) {
	return markAsyncBillingAcceptedHandoffManualReview(
		ctx, reservationID, AsyncBillingReviewKindAcceptanceOverage, targetQuota, audit,
		upstreamTaskID, &replay, "", now,
	)
}

func MarkAsyncBillingAcceptedHandoffManualReview(
	ctx context.Context,
	reservationID int64,
	targetQuota int,
	audit AsyncBillingAcceptedAuditSnapshot,
	upstreamTaskID string,
	replay AsyncBillingReplaySpec,
	failureReason string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	return markAsyncBillingAcceptedHandoffManualReview(
		ctx, reservationID, AsyncBillingReviewKindAcceptedHandoff, targetQuota, audit,
		upstreamTaskID, &replay, failureReason, now,
	)
}

func MarkAsyncBillingAcceptedHandoffManualReviewFromAttempt(
	ctx context.Context,
	reservationID int64,
	targetQuota int,
	upstreamTaskID string,
	replay AsyncBillingReplaySpec,
	failureReason string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var attempts []AsyncBillingAttempt
	if err := DB.WithContext(ctx).Where(
		"reservation_id = ? AND state = ?", reservationID, AsyncBillingAttemptStateAuthorized,
	).Limit(2).Find(&attempts).Error; err != nil {
		return nil, err
	}
	if len(attempts) != 1 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	intent, err := thawAsyncBillingAcceptanceIntent(&attempts[0])
	if err != nil {
		return nil, err
	}
	audit := intent.Audit
	adminInfo := make(map[string]any, len(audit.AdminInfo)+2)
	for key, value := range audit.AdminInfo {
		adminInfo[key] = value
	}
	adminInfo["billing_context_phase"] = "post_submit_final_fallback"
	adminInfo["billing_audit_fallback_reason"] = boundedAsyncBillingError(failureReason)
	audit.AdminInfo = adminInfo
	return markAsyncBillingAcceptedHandoffManualReview(
		ctx, reservationID, AsyncBillingReviewKindAcceptedHandoff, targetQuota, audit,
		upstreamTaskID, &replay, failureReason, now,
	)
}

func markAsyncBillingAcceptedHandoffManualReview(
	ctx context.Context,
	reservationID int64,
	reviewKind string,
	targetQuota int,
	audit AsyncBillingAcceptedAuditSnapshot,
	upstreamTaskID string,
	replay *AsyncBillingReplaySpec,
	failureReason string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 || targetQuota < 0 || targetQuota > common.MaxQuota ||
		(reviewKind != AsyncBillingReviewKindAcceptanceOverage && reviewKind != AsyncBillingReviewKindAcceptedHandoff) ||
		validateAsyncBillingAuditSnapshot(audit) != nil {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if upstreamTaskID == "" || len(upstreamTaskID) > 191 || !utf8.ValidString(upstreamTaskID) ||
		strings.ContainsAny(upstreamTaskID, "\r\n\x00") {
		return nil, ErrAsyncBillingReservationInvariant
	}
	auditPayload, err := common.Marshal(audit)
	if err != nil || len(auditPayload) == 0 || len(auditPayload) > maxAsyncBillingIntentBytes {
		return nil, ErrAsyncBillingReservationInvariant
	}
	auditDigest := sha256.Sum256(auditPayload)
	auditHash := hex.EncodeToString(auditDigest[:])
	var normalizedReplay AsyncBillingReplaySpec
	var replayHash string
	if replay != nil {
		normalizedReplay, replayHash, err = normalizeAsyncBillingReplaySpec(*replay)
		if err != nil {
			return nil, err
		}
	}
	reason := boundedAsyncBillingError(failureReason)
	var result AsyncBillingReservation
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&result).Error; err != nil {
			return err
		}
		if reviewKind == AsyncBillingReviewKindAcceptanceOverage &&
			(targetQuota <= result.CurrentQuota || result.Kind != AsyncBillingKindTask) {
			return ErrAsyncBillingReservationInvariant
		}
		if reason == "" {
			reason = boundedAsyncBillingError(fmt.Sprintf(
				"accepted_submission_handoff_failed: reserved=%d target=%d", result.CurrentQuota, targetQuota,
			))
		}
		if reviewKind == AsyncBillingReviewKindAcceptanceOverage {
			reason = boundedAsyncBillingError(fmt.Sprintf(
				"accepted_submission_quota_exceeds_reservation: reserved=%d target=%d", result.CurrentQuota, targetQuota,
			))
		}
		if result.State == AsyncBillingReservationStateManualReview {
			if result.ManualReviewKind == reviewKind &&
				result.ReviewTargetQuota == targetQuota && result.ReviewAuditProtocol == AsyncBillingAcceptanceIntentProtocol &&
				strings.EqualFold(result.ReviewAuditHash, auditHash) && result.UpstreamTaskID == upstreamTaskID &&
				(replay == nil || strings.EqualFold(result.ReplayHash, replayHash)) {
				return nil
			}
			return ErrAsyncBillingReservationInvariant
		}
		if result.State != AsyncBillingReservationStateSendAuthorized {
			return ErrAsyncBillingReservationInvariant
		}
		var authorized int64
		if err := tx.Model(&AsyncBillingAttempt{}).Where(
			"reservation_id = ? AND state = ?", result.ID, AsyncBillingAttemptStateAuthorized,
		).Count(&authorized).Error; err != nil {
			return err
		}
		if authorized != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		updates := map[string]any{
			"state":                     AsyncBillingReservationStateManualReview,
			"manual_review_kind":        reviewKind,
			"manual_review_required_ms": now.UnixMilli(), "manual_review_reason": reason,
			"review_target_quota": targetQuota, "review_audit_protocol": AsyncBillingAcceptanceIntentProtocol,
			"review_audit_hash": auditHash, "review_audit_payload": auditPayload,
			"upstream_task_id": upstreamTaskID, "last_error": reason,
			"review_version": gorm.Expr("review_version + ?", 1), "updated_time_ms": now.UnixMilli(),
		}
		if replay != nil {
			updates["replay_protocol"] = AsyncBillingReplayProtocol
			updates["replay_ready"] = true
			updates["replay_status_code"] = normalizedReplay.StatusCode
			updates["replay_content_type"] = normalizedReplay.ContentType
			updates["replay_headers_json"] = normalizedReplay.HeadersJSON
			updates["replay_body"] = normalizedReplay.Body
			updates["replay_hash"] = replayHash
		}
		updated := tx.Model(&AsyncBillingReservation{}).Where(
			"id = ? AND state = ?", result.ID, AsyncBillingReservationStateSendAuthorized,
		).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		result.State = AsyncBillingReservationStateManualReview
		result.ManualReviewKind = reviewKind
		result.ManualReviewRequiredMs = now.UnixMilli()
		result.ManualReviewReason = reason
		result.ReviewTargetQuota = targetQuota
		result.ReviewAuditProtocol = AsyncBillingAcceptanceIntentProtocol
		result.ReviewAuditHash = auditHash
		result.ReviewAuditPayload = append([]byte(nil), auditPayload...)
		result.UpstreamTaskID = upstreamTaskID
		if replay != nil {
			result.ReplayProtocol = AsyncBillingReplayProtocol
			result.ReplayReady = true
			result.ReplayStatusCode = normalizedReplay.StatusCode
			result.ReplayContentType = normalizedReplay.ContentType
			result.ReplayHeadersJSON = normalizedReplay.HeadersJSON
			result.ReplayBody = append([]byte(nil), normalizedReplay.Body...)
			result.ReplayHash = replayHash
		}
		result.ReviewVersion++
		return nil
	})
	return &result, err
}

// MarkAsyncBillingReservationManualReview makes an ambiguous send outcome
// explicit without changing any balance. Manual-review reservations are never
// selected by automatic refund recovery.
func MarkAsyncBillingReservationManualReview(
	ctx context.Context,
	reservationID int64,
	reason string,
	upstreamTaskID string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	return MarkAsyncBillingReservationManualReviewKind(
		ctx, reservationID, AsyncBillingReviewKindSendOutcome, reason, upstreamTaskID, now,
	)
}

func MarkAsyncBillingReservationManualReviewKind(
	ctx context.Context,
	reservationID int64,
	reviewKind string,
	reason string,
	upstreamTaskID string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	reviewKind = strings.TrimSpace(reviewKind)
	if reviewKind != AsyncBillingReviewKindSendOutcome && reviewKind != AsyncBillingReviewKindTerminalOverage {
		return nil, ErrAsyncBillingReservationInvariant
	}
	reason = boundedAsyncBillingError(reason)
	if strings.TrimSpace(reason) == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if len(upstreamTaskID) > 191 || !utf8.ValidString(upstreamTaskID) || strings.ContainsAny(upstreamTaskID, "\r\n\x00") {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var result AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&result).Error; err != nil {
			return err
		}
		if result.State == AsyncBillingReservationStateManualReview {
			if result.ManualReviewKind != reviewKind || result.ManualReviewReason != reason ||
				(upstreamTaskID != "" && result.UpstreamTaskID != "" && result.UpstreamTaskID != upstreamTaskID) {
				return ErrAsyncBillingReservationInvariant
			}
			return nil
		}
		expectedState := AsyncBillingReservationStateSendAuthorized
		if reviewKind == AsyncBillingReviewKindTerminalOverage {
			expectedState = AsyncBillingReservationStateAccepted
		}
		if result.State != expectedState {
			return ErrAsyncBillingReservationInvariant
		}
		updates := map[string]any{
			"state":                     AsyncBillingReservationStateManualReview,
			"manual_review_required_ms": now.UnixMilli(),
			"manual_review_reason":      reason,
			"manual_review_kind":        reviewKind,
			"review_version":            gorm.Expr("review_version + ?", 1),
			"last_error":                reason,
			"updated_time_ms":           now.UnixMilli(),
		}
		if upstreamTaskID != "" {
			updates["upstream_task_id"] = upstreamTaskID
		}
		updated := tx.Model(&AsyncBillingReservation{}).
			Where("id = ? AND state = ?", result.ID, expectedState).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		result.State = AsyncBillingReservationStateManualReview
		result.ManualReviewRequiredMs = now.UnixMilli()
		result.ManualReviewReason = reason
		result.ManualReviewKind = reviewKind
		result.ReviewVersion++
		result.LastError = reason
		if upstreamTaskID != "" {
			result.UpstreamTaskID = upstreamTaskID
		}
		return nil
	})
	if err == nil {
		common.SysError(fmt.Sprintf("async billing reservation requires manual review: id=%d kind=%s reason=%s",
			result.ID, result.Kind, reason))
	}
	return &result, err
}

func markAsyncBillingTerminalUsageManualReviewTx(
	tx *gorm.DB,
	reservation *AsyncBillingReservation,
	task *Task,
	reason string,
	now time.Time,
) error {
	if tx == nil || reservation == nil || task == nil ||
		reservation.Kind != AsyncBillingKindTask || reservation.TaskID != task.ID ||
		task.UserId != reservation.UserID || task.Status != TaskStatusSuccess || task.Progress != "100%" ||
		task.PrivateData.AsyncBillingReservationID != reservation.ID ||
		task.EffectiveBillingQuota() != reservation.CurrentQuota {
		return ErrAsyncBillingReservationInvariant
	}
	if reservation.State == AsyncBillingReservationStateManualReview {
		if reservation.ManualReviewKind != AsyncBillingReviewKindTerminalUsage {
			return ErrAsyncBillingReservationInvariant
		}
		return nil
	}
	if reservation.State != AsyncBillingReservationStateAccepted {
		return ErrAsyncBillingReservationInvariant
	}
	var operationCount int64
	if err := tx.Model(&TaskBillingOperation{}).Where("task_id = ?", task.ID).Count(&operationCount).Error; err != nil {
		return err
	}
	if operationCount != 0 {
		return ErrAsyncBillingReservationInvariant
	}
	updated := tx.Model(&AsyncBillingReservation{}).
		Where("id = ? AND state = ?", reservation.ID, AsyncBillingReservationStateAccepted).
		Updates(map[string]any{
			"state":                     AsyncBillingReservationStateManualReview,
			"manual_review_required_ms": now.UnixMilli(),
			"manual_review_reason":      reason,
			"manual_review_kind":        AsyncBillingReviewKindTerminalUsage,
			"review_target_quota":       reservation.CurrentQuota,
			"review_version":            gorm.Expr("review_version + ?", 1),
			"last_error":                reason,
			"updated_time_ms":           now.UnixMilli(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrAsyncBillingReservationInvariant
	}
	reservation.State = AsyncBillingReservationStateManualReview
	reservation.ManualReviewRequiredMs = now.UnixMilli()
	reservation.ManualReviewReason = reason
	reservation.ManualReviewKind = AsyncBillingReviewKindTerminalUsage
	reservation.ReviewTargetQuota = reservation.CurrentQuota
	reservation.ReviewVersion++
	reservation.LastError = reason
	reservation.UpdatedTimeMs = now.UnixMilli()
	return nil
}

func MarkAsyncBillingTerminalUsageManualReview(
	ctx context.Context,
	reservationID int64,
	reason string,
	now time.Time,
) (*AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	reason = boundedAsyncBillingError(reason)
	if strings.TrimSpace(reason) == "" {
		return nil, ErrAsyncBillingReservationInvariant
	}
	var result AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&result).Error; err != nil {
			return err
		}
		if result.Kind != AsyncBillingKindTask || result.TaskID <= 0 {
			return ErrAsyncBillingReservationInvariant
		}
		var task Task
		if err := lockForUpdate(tx).Where("id = ?", result.TaskID).First(&task).Error; err != nil {
			return err
		}
		return markAsyncBillingTerminalUsageManualReviewTx(tx, &result, &task, reason, now)
	})
	if err == nil {
		common.SysError(fmt.Sprintf(
			"async billing terminal usage requires manual review: id=%d reason=%s", result.ID, reason,
		))
	}
	return &result, err
}

func FinalizeTaskTerminalUsageManualReview(
	ctx context.Context,
	reservationID int64,
	update TaskTerminalUpdate,
	reason string,
	now time.Time,
) (*Task, *AsyncBillingReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 || update.TaskID <= 0 || update.TerminalStatus != TaskStatusSuccess {
		return nil, nil, ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	reason = boundedAsyncBillingError(reason)
	if strings.TrimSpace(reason) == "" {
		return nil, nil, ErrAsyncBillingReservationInvariant
	}
	var task Task
	var reservation AsyncBillingReservation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.Kind != AsyncBillingKindTask || reservation.TaskID != update.TaskID {
			return ErrAsyncBillingReservationInvariant
		}
		if err := lockForUpdate(tx).Where("id = ?", update.TaskID).First(&task).Error; err != nil {
			return err
		}
		if task.PrivateData.BillingProtocolVersion != TaskBillingProtocolVersion ||
			task.PrivateData.AsyncBillingReservationID != reservation.ID {
			return ErrAsyncBillingReservationInvariant
		}
		if task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure {
			progress := "100%"
			finishTime := update.FinishTime
			if finishTime <= 0 {
				finishTime = now.Unix()
			}
			privateData := task.PrivateData
			privateData.UpstreamResultURL = strings.TrimSpace(update.UpstreamResultURL)
			task.PrivateData = privateData
			if err := task.freezeV2PrivateData(); err != nil {
				return err
			}
			updates := map[string]any{
				"status":                    TaskStatusSuccess,
				"progress":                  progress,
				"finish_time":               finishTime,
				"fail_reason":               boundedTaskBillingError(update.FailReason),
				"private_data":              privateData,
				"durable_private_data":      task.DurablePrivateDataPayload,
				"durable_private_data_hash": task.DurablePrivateDataHash,
				"updated_at":                now.Unix(),
			}
			if update.SubmitTime > 0 {
				updates["submit_time"] = update.SubmitTime
			}
			if update.StartTime > 0 {
				updates["start_time"] = update.StartTime
			}
			if update.Data != nil {
				updates["data"] = append([]byte(nil), update.Data...)
			}
			updated := tx.Model(&Task{}).
				Where("id = ? AND status NOT IN ?", task.ID, []TaskStatus{TaskStatusSuccess, TaskStatusFailure}).
				Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrAsyncBillingReservationInvariant
			}
			task.Status = TaskStatusSuccess
			task.Progress = progress
			if update.SubmitTime > 0 {
				task.SubmitTime = update.SubmitTime
			}
			if update.StartTime > 0 {
				task.StartTime = update.StartTime
			}
			task.FinishTime = finishTime
			task.FailReason = boundedTaskBillingError(update.FailReason)
			task.UpdatedAt = now.Unix()
			if update.Data != nil {
				task.Data = append([]byte(nil), update.Data...)
			}
		}
		return markAsyncBillingTerminalUsageManualReviewTx(tx, &reservation, &task, reason, now)
	})
	if err == nil {
		common.SysError(fmt.Sprintf(
			"async billing terminal usage requires manual review: id=%d reason=%s", reservation.ID, reason,
		))
	}
	return &task, &reservation, err
}

func FindAsyncBillingManualReviewReservations(limit int) ([]AsyncBillingReservation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var reservations []AsyncBillingReservation
	err := DB.Where("state = ?", AsyncBillingReservationStateManualReview).
		Order("manual_review_required_ms asc, id asc").Limit(limit).Find(&reservations).Error
	return reservations, err
}

func FindStaleAmbiguousAsyncBillingReservationIDsContext(
	ctx context.Context,
	now time.Time,
	staleAfter time.Duration,
	limit int,
) ([]int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if staleAfter <= 0 {
		staleAfter = 15 * time.Minute
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var ids []int64
	err := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("state = ? AND EXISTS ("+
			"SELECT 1 FROM async_billing_attempts WHERE async_billing_attempts.reservation_id = async_billing_reservations.id "+
			"AND async_billing_attempts.state = ? AND async_billing_attempts.send_deadline_ms > 0 "+
			"AND async_billing_attempts.send_deadline_ms <= ?)",
			AsyncBillingReservationStateSendAuthorized, AsyncBillingAttemptStateAuthorized,
			now.Add(-staleAfter).UnixMilli()).
		Order("id asc").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

func FindStaleAmbiguousAsyncBillingReservationIDs(now time.Time, staleAfter time.Duration, limit int) ([]int64, error) {
	return FindStaleAmbiguousAsyncBillingReservationIDsContext(context.Background(), now, staleAfter, limit)
}

// SyncAsyncBillingReservationCaches invalidates derived Redis state for one
// committed balance version. A concurrent newer mutation leaves pending set.
func SyncAsyncBillingReservationCaches(ctx context.Context, reservationID int64, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}
	var reservation AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
		return err
	}
	if !reservation.CacheSyncPending || reservation.CacheSyncedVersion >= reservation.CacheSyncVersion {
		return nil
	}
	version := reservation.CacheSyncVersion
	var cacheErr error
	if common.RedisEnabled {
		cacheErr = errors.Join(
			common.RedisBumpCacheEpochAndDeleteContext(
				ctx, getUserCacheEpochKey(reservation.UserID), getUserCacheKey(reservation.UserID),
			),
			invalidateAsyncBillingTokenCacheContext(ctx, reservation.TokenID, reservation.UserID),
		)
	}
	if cacheErr != nil {
		attempts := reservation.CacheSyncAttempts + 1
		shift := attempts - 1
		if shift > 6 {
			shift = 6
		}
		delay := time.Second * time.Duration(1<<shift)
		detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBillingPostCommitMutationTimeout)
		defer cancel()
		markErr := DB.WithContext(detachedCtx).Model(&AsyncBillingReservation{}).
			Where("id = ? AND cache_sync_version = ?", reservation.ID, version).
			Updates(map[string]any{
				"cache_sync_pending":       true,
				"cache_sync_attempts":      gorm.Expr("cache_sync_attempts + ?", 1),
				"cache_sync_next_retry_ms": now.Add(delay).UnixMilli(),
				"cache_sync_last_error":    boundedAsyncBillingError(cacheErr.Error()),
			}).Error
		return errors.Join(cacheErr, markErr)
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBillingPostCommitMutationTimeout)
	defer cancel()
	updated := DB.WithContext(detachedCtx).Model(&AsyncBillingReservation{}).
		Where("id = ? AND cache_sync_version = ? AND cache_sync_pending = ?", reservation.ID, version, true).
		Updates(map[string]any{
			"cache_synced_version":     version,
			"cache_sync_pending":       false,
			"cache_sync_next_retry_ms": 0,
			"cache_sync_last_error":    "",
		})
	return updated.Error
}

func invalidateAsyncBillingTokenCacheContext(ctx context.Context, tokenID int, userID int) error {
	if !common.RedisEnabled || tokenID <= 0 {
		return nil
	}
	if userID <= 0 {
		return errors.New("userId invalid")
	}
	var token Token
	err := DB.WithContext(ctx).Unscoped().Select("id", commonKeyCol).
		Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error
	if err == nil && token.Key != "" {
		return common.RedisBumpCacheEpochAndDeleteContext(
			ctx, getTokenCacheEpochKey(token.Key), getTokenCacheKey(token.Key),
		)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var tokens []Token
	if err := DB.WithContext(ctx).Unscoped().Select("id", commonKeyCol).
		Where("user_id = ?", userID).Find(&tokens).Error; err != nil {
		return err
	}
	var firstErr error
	for index := range tokens {
		if ctx.Err() != nil {
			return errors.Join(firstErr, ctx.Err())
		}
		if tokens[index].Key == "" {
			continue
		}
		if err := common.RedisBumpCacheEpochAndDeleteContext(
			ctx, getTokenCacheEpochKey(tokens[index].Key), getTokenCacheKey(tokens[index].Key),
		); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func FindPendingAsyncBillingCacheSyncIDsContext(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var ids []int64
	err := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("cache_sync_pending = ? AND cache_sync_next_retry_ms <= ?", true, now.UnixMilli()).
		Order("id asc").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

func FindPendingAsyncBillingCacheSyncIDs(now time.Time, limit int) ([]int64, error) {
	return FindPendingAsyncBillingCacheSyncIDsContext(context.Background(), now, limit)
}

func FindPendingAsyncBillingAcceptedProjectionIDsContext(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var ids []int64
	err := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("accepted_projection_state IN ? AND accepted_projection_next_retry_ms <= ?",
			[]string{AsyncBillingAcceptedProjectionPending, AsyncBillingAcceptedProjectionLogPending}, now.UnixMilli()).
		Order("id asc").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

func FindPendingAsyncBillingAcceptedProjectionIDs(now time.Time, limit int) ([]int64, error) {
	return FindPendingAsyncBillingAcceptedProjectionIDsContext(context.Background(), now, limit)
}

func HasPendingAsyncBillingAcceptedProjectionsContext(ctx context.Context, now time.Time) bool {
	ids, err := FindPendingAsyncBillingAcceptedProjectionIDsContext(ctx, now, 1)
	return err == nil && len(ids) == 1
}

func HasPendingAsyncBillingAcceptedProjections(now time.Time) bool {
	return HasPendingAsyncBillingAcceptedProjectionsContext(context.Background(), now)
}

// ProcessAsyncBillingAcceptedProjection materializes accepted usage in two
// recoverable stages. Main-database statistics commit first and advance the
// stage atomically; the external log sink is idempotent by projection key.
func ProcessAsyncBillingAcceptedProjection(ctx context.Context, reservationID int64, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservationID <= 0 {
		return ErrAsyncBillingReservationInvariant
	}
	if now.IsZero() {
		now = time.Now()
	}

	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservation AsyncBillingReservation
		if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.AcceptedProjectionState != AsyncBillingAcceptedProjectionPending {
			return nil
		}
		if reservation.AcceptedProjectionKey == nil || strings.TrimSpace(*reservation.AcceptedProjectionKey) == "" ||
			reservation.AcceptedProjectionQuota < 0 || reservation.AcceptedProjectionQuota > common.MaxQuota ||
			reservation.AcceptedProjectionRequestDelta != 1 || reservation.AcceptedProjectionChannelID <= 0 {
			return ErrAsyncBillingReservationInvariant
		}
		projectionKey := strings.TrimSpace(*reservation.AcceptedProjectionKey)
		var durableLog BillingLogProjection
		logQuery := lockForUpdate(tx).Where(
			"kind = ? AND reference_id = ?", BillingLogProjectionKindAccepted, reservation.ID,
		).Limit(1).Find(&durableLog)
		if logQuery.Error != nil {
			return logQuery.Error
		}
		var durableStats BillingStatsProjection
		statsQuery := lockForUpdate(tx).Where(
			"kind = ? AND reference_id = ?", BillingStatsProjectionKindAccepted, reservation.ID,
		).Limit(1).Find(&durableStats)
		if statsQuery.Error != nil {
			return statsQuery.Error
		}
		hasDurableLog := logQuery.RowsAffected == 1
		hasDurableStats := statsQuery.RowsAffected == 1
		if hasDurableLog || hasDurableStats {
			if !hasDurableLog || !hasDurableStats {
				return fmt.Errorf("%w: durable accepted projection receipts are incomplete", ErrAsyncBillingReservationInvariant)
			}
			expectedStats := BillingStatsProjectionSpec{
				OperationKey: projectionKey, Kind: BillingStatsProjectionKindAccepted,
				ReferenceID: reservation.ID, UserID: reservation.UserID,
				ChannelID:    reservation.AcceptedProjectionChannelID,
				QuotaDelta:   reservation.AcceptedProjectionQuota,
				RequestDelta: reservation.AcceptedProjectionRequestDelta,
			}
			expectedStats, _ = normalizeBillingStatsProjectionSpec(expectedStats)
			logRequired := reservation.AcceptedProjectionLogProtocol != 0 ||
				reservation.AcceptedProjectionLogHash != "" || reservation.AcceptedProjectionLogPayload != ""
			expectedLog := &BillingLogProjection{
				OperationKey: projectionKey, ProtocolVersion: BillingLogProjectionProtocol,
				Kind: BillingLogProjectionKindAccepted, ReferenceID: reservation.ID,
				Required: logRequired,
			}
			if logRequired {
				expectedLog.Disposition = BillingLogProjectionDispositionPending
				expectedLog.PayloadProtocol = reservation.AcceptedProjectionLogProtocol
				expectedLog.PayloadHash = reservation.AcceptedProjectionLogHash
				expectedLog.Payload = reservation.AcceptedProjectionLogPayload
			} else {
				expectedLog.Disposition = BillingLogProjectionDispositionNotRequired
			}
			if !sameBillingStatsProjectionSpec(&durableStats, expectedStats) ||
				!sameBillingLogProjectionIntent(&durableLog, expectedLog) {
				return fmt.Errorf("%w: durable accepted projection receipts conflict with legacy intent", ErrAsyncBillingReservationInvariant)
			}
			updated := tx.Model(&AsyncBillingReservation{}).
				Where("id = ? AND accepted_projection_state = ?", reservation.ID, AsyncBillingAcceptedProjectionPending).
				Updates(map[string]any{
					"accepted_projection_state":         AsyncBillingAcceptedProjectionCompleted,
					"accepted_projection_next_retry_ms": 0,
					"accepted_projection_last_error":    "",
					"accepted_projection_warning": boundedAsyncBillingError(
						fmt.Sprintf("legacy accepted projection superseded by durable receipts: operation=%s", projectionKey),
					),
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrAsyncBillingReservationInvariant
			}
			return nil
		}

		usageResult, err := applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: projectionKey,
			UserID:       reservation.UserID,
			ChannelID:    reservation.AcceptedProjectionChannelID,
			QuotaDelta:   reservation.AcceptedProjectionQuota,
			RequestDelta: reservation.AcceptedProjectionRequestDelta,
		})
		if err != nil {
			return err
		}
		warning := boundedAsyncBillingError(billingUsageProjectionWarning(projectionKey, usageResult))

		updatedReservation := tx.Model(&AsyncBillingReservation{}).
			Where("id = ? AND accepted_projection_state = ?", reservation.ID, AsyncBillingAcceptedProjectionPending).
			Updates(map[string]any{
				"accepted_projection_state":           AsyncBillingAcceptedProjectionLogPending,
				"accepted_projection_next_retry_ms":   0,
				"accepted_projection_last_error":      "",
				"accepted_projection_user_outcome":    usageResult.UserOutcome,
				"accepted_projection_channel_outcome": usageResult.ChannelOutcome,
				"accepted_projection_warning":         warning,
			})
		if updatedReservation.Error != nil {
			return updatedReservation.Error
		}
		if updatedReservation.RowsAffected != 1 {
			return ErrAsyncBillingReservationInvariant
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return retryAsyncBillingAcceptedProjection(ctx, reservationID, now, err)
	}

	var reservation AsyncBillingReservation
	if err := DB.WithContext(ctx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
		return err
	}
	if reservation.AcceptedProjectionState == AsyncBillingAcceptedProjectionCompleted {
		return nil
	}
	if reservation.AcceptedProjectionState != AsyncBillingAcceptedProjectionLogPending ||
		reservation.AcceptedProjectionKey == nil || LOG_DB == nil {
		err := ErrAsyncBillingReservationInvariant
		if LOG_DB == nil {
			err = errors.New("accepted billing log database is unavailable")
		}
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBillingPostCommitMutationTimeout)
		defer cancel()
		return retryAsyncBillingAcceptedProjection(retryCtx, reservationID, now, err)
	}
	if err := writeFrozenBillingLog(
		ctx,
		*reservation.AcceptedProjectionKey,
		reservation.AcceptedProjectionLogPayload,
		reservation.AcceptedProjectionLogHash,
		reservation.AcceptedProjectionLogProtocol,
	); err != nil {
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBillingPostCommitMutationTimeout)
		defer cancel()
		return retryAsyncBillingAcceptedProjection(retryCtx, reservationID, now, err)
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBillingPostCommitMutationTimeout)
	defer cancel()
	updated := DB.WithContext(ackCtx).Model(&AsyncBillingReservation{}).
		Where("id = ? AND accepted_projection_state = ?", reservation.ID, AsyncBillingAcceptedProjectionLogPending).
		Updates(map[string]any{
			"accepted_projection_state":         AsyncBillingAcceptedProjectionCompleted,
			"accepted_projection_next_retry_ms": 0,
			"accepted_projection_last_error":    "",
		})
	return updated.Error
}

func retryAsyncBillingAcceptedProjection(ctx context.Context, reservationID int64, now time.Time, cause error) error {
	if cause == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var reservation AsyncBillingReservation
	readErr := DB.WithContext(ctx).Select("id", "accepted_projection_attempts").
		Where("id = ?", reservationID).First(&reservation).Error
	if readErr != nil {
		return errors.Join(cause, readErr)
	}
	attempts := reservation.AcceptedProjectionAttempts + 1
	shift := attempts - 1
	if shift > 6 {
		shift = 6
	}
	delay := 5 * time.Second * time.Duration(1<<shift)
	updated := DB.WithContext(ctx).Model(&AsyncBillingReservation{}).
		Where("id = ? AND accepted_projection_state IN ?", reservationID,
			[]string{AsyncBillingAcceptedProjectionPending, AsyncBillingAcceptedProjectionLogPending}).
		Updates(map[string]any{
			"accepted_projection_attempts":      gorm.Expr("accepted_projection_attempts + ?", 1),
			"accepted_projection_next_retry_ms": now.Add(delay).UnixMilli(),
			"accepted_projection_last_error":    boundedAsyncBillingError(cause.Error()),
		})
	common.SysError(fmt.Sprintf("async billing accepted projection failed: id=%d error=%s",
		reservationID, boundedAsyncBillingError(cause.Error())))
	return errors.Join(cause, updated.Error)
}

func syncAsyncBillingReservationCachesBestEffort(ctx context.Context, reservationID int64, now time.Time) {
	if err := SyncAsyncBillingReservationCaches(ctx, reservationID, now); err != nil {
		common.SysError(fmt.Sprintf("sync async billing reservation cache failed: id=%d error=%s",
			reservationID, boundedAsyncBillingError(err.Error())))
	}
}

func boundedAsyncBillingError(message string) string {
	return boundedAsyncBillingText(message, asyncBillingReservationErrorMaxBytes)
}

func boundedAsyncBillingText(message string, maxBytes int) string {
	message = common.SanitizeErrorMessage(message)
	if maxBytes <= 0 {
		return ""
	}
	if len(message) <= maxBytes {
		return message
	}
	message = message[:maxBytes]
	for !utf8.ValidString(message) && len(message) > 0 {
		message = message[:len(message)-1]
	}
	return message
}
