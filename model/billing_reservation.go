package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingFundingWallet       = "wallet"
	BillingFundingSubscription = "subscription"

	BillingReservationStatusInitializing = "initializing"
	BillingReservationStatusReserved     = "reserved"
	BillingReservationStatusSettled      = "settled"
	BillingReservationStatusRefunded     = "refunded"

	QuotaLedgerPhaseReserve   = "reserve"
	QuotaLedgerPhaseAdopt     = "legacy_adopt"
	QuotaLedgerPhaseBind      = "bind"
	QuotaLedgerPhaseSettle    = "settle"
	QuotaLedgerPhaseIntent    = "settle_intent"
	QuotaLedgerPhaseShortfall = "settle_shortfall"
	QuotaLedgerPhaseRefund    = "refund"
	QuotaLedgerPhaseReview    = "reconcile_review"

	BillingResourceAsyncTask = "async_task"

	billingTokenModeLimited   = 0
	billingTokenModeUnlimited = 1
	billingTokenModeSkipped   = 2

	BillingSettlementFailureUserQuota         = "insufficient_user_quota"
	BillingSettlementFailureTokenQuota        = "insufficient_token_quota"
	BillingSettlementFailureSubscriptionQuota = "insufficient_subscription_quota"
	BillingSettlementFailureAccountMissing    = "billing_account_not_found"
)

var (
	ErrBillingReservationNotFound    = errors.New("billing reservation not found")
	ErrBillingReservationConflict    = errors.New("billing reservation conflicts with the existing request")
	ErrBillingReservationFinalized   = errors.New("billing reservation is already finalized")
	ErrBillingAccountNotFound        = errors.New("billing account not found")
	ErrInsufficientUserQuota         = errors.New("insufficient user quota")
	ErrInsufficientTokenQuota        = errors.New("insufficient token quota")
	ErrInsufficientSubscriptionQuota = errors.New("insufficient subscription quota")
	ErrNoActiveSubscription          = errors.New("no active subscription")
	ErrQuotaOverflow                 = errors.New("quota update exceeds the supported range")
	ErrUserPaymentFreezeOpen         = errors.New("user has unresolved payment or billing liability")
)

// BillingReservation is the durable, request-scoped state machine for API
// quota pre-consume, settle and refund. RequestId is the idempotency key.
type BillingReservation struct {
	Id                       int64  `json:"id" gorm:"primaryKey"`
	RequestId                string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId                   int    `json:"user_id" gorm:"index"`
	TokenId                  int    `json:"token_id" gorm:"index"`
	FundingSource            string `json:"funding_source" gorm:"type:varchar(32);index"`
	SubscriptionId           int    `json:"subscription_id" gorm:"index"`
	SubscriptionResetAt      int64  `json:"subscription_reset_at" gorm:"index"`
	SubscriptionResetVersion int64  `json:"subscription_reset_version" gorm:"index"`
	LegacyAdopted            bool   `json:"legacy_adopted" gorm:"index"`
	ResourceType             string `json:"resource_type" gorm:"type:varchar(32);index"`
	ResourceId               string `json:"resource_id" gorm:"type:varchar(191);index"`
	InitialQuota             int    `json:"initial_quota"`
	ReservedQuota            int    `json:"reserved_quota"`
	TokenReserved            int    `json:"token_reserved"`
	SettledQuota             int    `json:"settled_quota"`
	SettlementTarget         int    `json:"settlement_target"`
	SettlementPending        bool   `json:"settlement_pending" gorm:"index"`
	SettlementFailureCode    string `json:"settlement_failure_code,omitempty" gorm:"type:varchar(64);index"`
	SettlementShortfallQuota int    `json:"settlement_shortfall_quota"`
	ShortfallFreezeApplied   bool   `json:"shortfall_freeze_applied" gorm:"index"`
	ShortfallPreviousStatus  int    `json:"shortfall_previous_status"`
	ShortfallDetectedAt      int64  `json:"shortfall_detected_at" gorm:"index"`
	ShortfallResolvedAt      int64  `json:"shortfall_resolved_at" gorm:"index"`
	TokenMode                int    `json:"token_mode"`
	Status                   string `json:"status" gorm:"type:varchar(32);index"`
	Version                  int    `json:"version"`
	LastReconciledAt         int64  `json:"last_reconciled_at" gorm:"index"`
	ReconcileNote            string `json:"reconcile_note" gorm:"type:varchar(255)"`
	CreatedAt                int64  `json:"created_at" gorm:"index"`
	UpdatedAt                int64  `json:"updated_at" gorm:"index"`
}

func (r *BillingReservation) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

// QuotaLedgerEntry is an append-only audit record. Each delta mirrors the exact
// arithmetic applied to its named database column.
type QuotaLedgerEntry struct {
	Id                         int64  `json:"id" gorm:"primaryKey"`
	RequestId                  string `json:"request_id" gorm:"type:varchar(64);index;uniqueIndex:idx_quota_ledger_phase_revision,priority:1"`
	Phase                      string `json:"phase" gorm:"type:varchar(24);uniqueIndex:idx_quota_ledger_phase_revision,priority:2"`
	Revision                   int    `json:"revision" gorm:"uniqueIndex:idx_quota_ledger_phase_revision,priority:3"`
	UserId                     int    `json:"user_id" gorm:"index"`
	TokenId                    int    `json:"token_id" gorm:"index"`
	FundingSource              string `json:"funding_source" gorm:"type:varchar(32);index"`
	SubscriptionId             int    `json:"subscription_id" gorm:"index"`
	UserQuotaDelta             int    `json:"user_quota_delta"`
	TokenRemainQuotaDelta      int    `json:"token_remain_quota_delta"`
	TokenUsedQuotaDelta        int    `json:"token_used_quota_delta"`
	SubscriptionUsedDelta      int64  `json:"subscription_used_delta"`
	SubscriptionTotalUsedDelta int64  `json:"subscription_total_used_delta"`
	Note                       string `json:"note" gorm:"type:varchar(255)"`
	CreatedAt                  int64  `json:"created_at" gorm:"index"`
}

func (e *QuotaLedgerEntry) BeforeCreate(_ *gorm.DB) error {
	e.CreatedAt = common.GetTimestamp()
	return nil
}

func (*QuotaLedgerEntry) BeforeUpdate(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

func (*QuotaLedgerEntry) BeforeDelete(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

type BillingReservationInput struct {
	RequestId     string
	UserId        int
	TokenId       int
	FundingSource string
	Quota         int
	SkipToken     bool
}

type BillingReservationResult struct {
	Reservation                 BillingReservation
	SubscriptionAmountTotal     int64
	SubscriptionAmountUsedAfter int64
	SubscriptionPlanId          int
	SubscriptionPlanTitle       string
}

func billingTokenMode(input BillingReservationInput) int {
	if input.SkipToken {
		return billingTokenModeSkipped
	}
	return billingTokenModeLimited
}

func validateBillingReservationInput(input *BillingReservationInput) error {
	if input == nil {
		return errors.New("billing reservation input is nil")
	}
	input.RequestId = strings.TrimSpace(input.RequestId)
	if input.RequestId == "" || len(input.RequestId) > 64 {
		return errors.New("billing requestId is empty or too long")
	}
	if input.UserId <= 0 {
		return errors.New("invalid billing userId")
	}
	if input.Quota < 0 || int64(input.Quota) > int64(math.MaxInt32) {
		return errors.New("invalid billing quota")
	}
	if input.FundingSource != BillingFundingWallet && input.FundingSource != BillingFundingSubscription {
		return errors.New("invalid billing funding source")
	}
	if input.FundingSource == BillingFundingSubscription && input.Quota <= 0 {
		return errors.New("subscription billing quota must be positive")
	}
	if !input.SkipToken && input.TokenId <= 0 {
		return errors.New("invalid billing tokenId")
	}
	return nil
}

func validateExistingBillingReservation(reservation *BillingReservation, input BillingReservationInput) error {
	if reservation == nil ||
		reservation.UserId != input.UserId ||
		reservation.TokenId != input.TokenId ||
		reservation.FundingSource != input.FundingSource ||
		reservation.InitialQuota != input.Quota ||
		(reservation.TokenMode == billingTokenModeSkipped) != input.SkipToken {
		return ErrBillingReservationConflict
	}
	if reservation.Status != BillingReservationStatusReserved {
		return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
	}
	return nil
}

// PreConsumeBillingReservation atomically creates a durable request reservation
// and debits its funding source and token quota. Repeating the exact request is
// a no-op; reusing the request id with different billing inputs is rejected.
func PreConsumeBillingReservation(input BillingReservationInput, tokenKey string) (*BillingReservationResult, error) {
	if err := validateBillingReservationInput(&input); err != nil {
		return nil, err
	}
	dbNow := int64(0)
	if input.FundingSource == BillingFundingSubscription {
		dbNow = GetDBTimestamp()
	}

	result := &BillingReservationResult{}
	userCacheChanged := false
	tokenCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		candidate := BillingReservation{
			RequestId:     input.RequestId,
			UserId:        input.UserId,
			TokenId:       input.TokenId,
			FundingSource: input.FundingSource,
			InitialQuota:  input.Quota,
			ReservedQuota: input.Quota,
			TokenMode:     billingTokenMode(input),
			Status:        BillingReservationStatusInitializing,
			Version:       1,
		}
		createResult := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "request_id"}},
			DoNothing: true,
		}).Create(&candidate)
		if createResult.Error != nil {
			return createResult.Error
		}
		if createResult.RowsAffected == 0 {
			var existing BillingReservation
			if err := lockForUpdate(tx).Where("request_id = ?", input.RequestId).First(&existing).Error; err != nil {
				return err
			}
			if err := validateExistingBillingReservation(&existing, input); err != nil {
				return err
			}
			result.Reservation = existing
			return populateBillingSubscriptionResultTx(tx, result)
		}

		ledger := &QuotaLedgerEntry{
			RequestId:     input.RequestId,
			Phase:         QuotaLedgerPhaseReserve,
			Revision:      1,
			UserId:        input.UserId,
			TokenId:       input.TokenId,
			FundingSource: input.FundingSource,
		}

		switch input.FundingSource {
		case BillingFundingWallet:
			if err := applyUserQuotaDeltaTx(tx, input.UserId, -input.Quota); err != nil {
				return err
			}
			if input.Quota > 0 {
				userCacheChanged = true
			}
			ledger.UserQuotaDelta = -input.Quota
		case BillingFundingSubscription:
			subscriptionResult, err := preConsumeSubscriptionBillingTx(tx, input, dbNow)
			if err != nil {
				return err
			}
			candidate.SubscriptionId = subscriptionResult.UserSubscriptionId
			candidate.SubscriptionResetAt = subscriptionResult.ResetAt
			candidate.SubscriptionResetVersion = subscriptionResult.ResetVersion
			ledger.SubscriptionId = subscriptionResult.UserSubscriptionId
			ledger.SubscriptionUsedDelta = int64(input.Quota)
			ledger.SubscriptionTotalUsedDelta = int64(input.Quota)
			result.SubscriptionAmountTotal = subscriptionResult.AmountTotal
			result.SubscriptionAmountUsedAfter = subscriptionResult.AmountUsedAfter
			result.SubscriptionPlanId = subscriptionResult.PlanId
			result.SubscriptionPlanTitle = subscriptionResult.PlanTitle
		}

		if candidate.TokenMode != billingTokenModeSkipped {
			var token Token
			if err := lockForUpdate(tx).
				Select("id", "user_id", "unlimited_quota").
				Where("id = ? AND user_id = ?", input.TokenId, input.UserId).
				First(&token).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrBillingAccountNotFound
				}
				return err
			}
			if token.UnlimitedQuota {
				candidate.TokenMode = billingTokenModeUnlimited
			}
			remainDelta, usedDelta, err := applyTokenQuotaDeltaTx(
				tx,
				input.UserId,
				input.TokenId,
				input.Quota,
				candidate.TokenMode == billingTokenModeUnlimited,
			)
			if err != nil {
				return err
			}
			candidate.TokenReserved = input.Quota
			ledger.TokenRemainQuotaDelta = remainDelta
			ledger.TokenUsedQuotaDelta = usedDelta
			if input.Quota > 0 {
				tokenCacheChanged = true
			}
		}

		candidate.Status = BillingReservationStatusReserved
		candidate.UpdatedAt = common.GetTimestamp()
		if err := tx.Model(&BillingReservation{}).Where("id = ?", candidate.Id).Updates(map[string]interface{}{
			"subscription_id":            candidate.SubscriptionId,
			"subscription_reset_at":      candidate.SubscriptionResetAt,
			"subscription_reset_version": candidate.SubscriptionResetVersion,
			"token_reserved":             candidate.TokenReserved,
			"token_mode":                 candidate.TokenMode,
			"status":                     candidate.Status,
			"updated_at":                 candidate.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(ledger).Error; err != nil {
			return err
		}
		result.Reservation = candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(input.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return result, nil
}

type subscriptionBillingResult struct {
	UserSubscriptionId int
	AmountTotal        int64
	AmountUsedAfter    int64
	ResetAt            int64
	ResetVersion       int64
	PlanId             int
	PlanTitle          string
}

func preConsumeSubscriptionBillingTx(tx *gorm.DB, input BillingReservationInput, now int64) (*subscriptionBillingResult, error) {
	var existing SubscriptionPreConsumeRecord
	query := tx.Where("request_id = ?", input.RequestId).Limit(1).Find(&existing)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected > 0 {
		return nil, ErrBillingReservationConflict
	}

	var subscriptions []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND status = ? AND end_time > ?", input.UserId, "active", now).
		Order("end_time asc, id asc").
		Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	if len(subscriptions) == 0 {
		return nil, ErrNoActiveSubscription
	}

	for _, candidate := range subscriptions {
		subscription := candidate
		if subscription.AmountTotal < 0 || subscription.AmountUsed < 0 ||
			(subscription.AmountTotal > 0 && subscription.AmountUsed > subscription.AmountTotal) {
			return nil, ErrBillingReservationConflict
		}
		plan, err := getSubscriptionPlanByIdTx(tx, subscription.PlanId)
		if err != nil {
			return nil, err
		}
		if err := maybeResetUserSubscriptionWithPlanTx(tx, &subscription, plan, now); err != nil {
			return nil, err
		}
		if subscription.AmountTotal > 0 && subscription.AmountTotal-subscription.AmountUsed < int64(input.Quota) {
			continue
		}
		if subscription.AmountUsed > math.MaxInt64-int64(input.Quota) {
			return nil, ErrQuotaOverflow
		}
		totalUsed := subscription.AmountUsedTotal
		if totalUsed < subscription.AmountUsed {
			totalUsed = subscription.AmountUsed
		}
		if totalUsed > math.MaxInt64-int64(input.Quota) {
			return nil, ErrQuotaOverflow
		}

		record := &SubscriptionPreConsumeRecord{
			RequestId:          input.RequestId,
			UserId:             input.UserId,
			UserSubscriptionId: subscription.Id,
			PreConsumed:        int64(input.Quota),
			Status:             "consumed",
		}
		if err := tx.Create(record).Error; err != nil {
			return nil, err
		}
		subscription.AmountUsed += int64(input.Quota)
		subscription.AmountUsedTotal = totalUsed + int64(input.Quota)
		if err := tx.Save(&subscription).Error; err != nil {
			return nil, err
		}
		return &subscriptionBillingResult{
			UserSubscriptionId: subscription.Id,
			AmountTotal:        subscription.AmountTotal,
			AmountUsedAfter:    subscription.AmountUsed,
			ResetAt:            subscription.LastResetTime,
			ResetVersion:       subscription.QuotaResetVersion,
			PlanId:             subscription.PlanId,
			PlanTitle:          plan.Title,
		}, nil
	}
	return nil, fmt.Errorf("%w: need=%d", ErrInsufficientSubscriptionQuota, input.Quota)
}

func populateBillingSubscriptionResultTx(tx *gorm.DB, result *BillingReservationResult) error {
	if result == nil || result.Reservation.FundingSource != BillingFundingSubscription {
		return nil
	}
	var subscription UserSubscription
	if err := tx.Where("id = ?", result.Reservation.SubscriptionId).First(&subscription).Error; err != nil {
		return err
	}
	plan, err := getSubscriptionPlanByIdTx(tx, subscription.PlanId)
	if err != nil {
		return err
	}
	result.SubscriptionAmountTotal = subscription.AmountTotal
	result.SubscriptionAmountUsedAfter = subscription.AmountUsed
	result.SubscriptionPlanId = subscription.PlanId
	result.SubscriptionPlanTitle = plan.Title
	return nil
}

func adjustReservedBillingTx(tx *gorm.DB, reservation *BillingReservation, targetQuota int, phase string, allowMissingToken bool) (bool, bool, error) {
	if tx == nil || reservation == nil || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return false, false, errors.New("invalid billing reservation adjustment")
	}
	if reservation.Status != BillingReservationStatusReserved {
		return false, false, fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
	}
	delta := targetQuota - reservation.ReservedQuota
	if delta == 0 {
		return false, false, nil
	}

	ledger := &QuotaLedgerEntry{
		RequestId:      reservation.RequestId,
		Phase:          phase,
		Revision:       reservation.Version + 1,
		UserId:         reservation.UserId,
		TokenId:        reservation.TokenId,
		FundingSource:  reservation.FundingSource,
		SubscriptionId: reservation.SubscriptionId,
	}
	userCacheChanged := false
	tokenCacheChanged := false
	switch reservation.FundingSource {
	case BillingFundingWallet:
		if err := applyUserQuotaDeltaTx(tx, reservation.UserId, -delta); err != nil {
			return false, false, err
		}
		ledger.UserQuotaDelta = -delta
		userCacheChanged = true
	case BillingFundingSubscription:
		periodDelta, totalDelta, err := applySubscriptionReservationDeltaTx(tx, reservation, int64(delta))
		if err != nil {
			return false, false, err
		}
		if !reservation.LegacyAdopted {
			preConsumeQuery := tx.Model(&SubscriptionPreConsumeRecord{}).
				Where("request_id = ? AND status = ?", reservation.RequestId, "consumed")
			if delta < 0 {
				preConsumeQuery = preConsumeQuery.Where("pre_consumed >= ?", -delta)
			}
			updateResult := preConsumeQuery.Updates(map[string]interface{}{
				"pre_consumed": gorm.Expr("pre_consumed + ?", delta),
				"updated_at":   common.GetTimestamp(),
			})
			if updateResult.Error != nil {
				return false, false, updateResult.Error
			}
			if updateResult.RowsAffected != 1 {
				return false, false, ErrBillingReservationConflict
			}
		}
		ledger.SubscriptionUsedDelta = periodDelta
		ledger.SubscriptionTotalUsedDelta = totalDelta
	default:
		return false, false, ErrBillingReservationConflict
	}

	if reservation.TokenMode != billingTokenModeSkipped {
		tokenExists, err := billingTokenExistsTx(tx, reservation.UserId, reservation.TokenId)
		if err != nil {
			return false, false, err
		}
		if !tokenExists && allowMissingToken {
			ledger.Note = "token deleted before async task finalization; token delta skipped"
		} else {
			remainDelta, usedDelta, err := applyTokenQuotaDeltaTx(
				tx,
				reservation.UserId,
				reservation.TokenId,
				delta,
				reservation.TokenMode == billingTokenModeUnlimited,
			)
			if err != nil {
				return false, false, err
			}
			ledger.TokenRemainQuotaDelta = remainDelta
			ledger.TokenUsedQuotaDelta = usedDelta
			tokenCacheChanged = true
		}
		reservation.TokenReserved += delta
		if reservation.TokenReserved < 0 {
			return false, false, ErrBillingReservationConflict
		}
	}

	reservation.ReservedQuota = targetQuota
	reservation.Version++
	reservation.UpdatedAt = common.GetTimestamp()
	if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
		"reserved_quota": reservation.ReservedQuota,
		"token_reserved": reservation.TokenReserved,
		"version":        reservation.Version,
		"updated_at":     reservation.UpdatedAt,
	}).Error; err != nil {
		return false, false, err
	}
	if err := tx.Create(ledger).Error; err != nil {
		return false, false, err
	}
	return userCacheChanged, tokenCacheChanged, nil
}

// ExtendBillingReservation raises a reservation to targetQuota. Lower targets
// intentionally remain a no-op for the public reserve contract.
func ExtendBillingReservation(requestId string, targetQuota int, tokenKey string) (*BillingReservation, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return nil, errors.New("invalid billing reservation extension")
	}

	var reservation BillingReservation
	userCacheChanged := false
	tokenCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if targetQuota <= reservation.ReservedQuota {
			if reservation.Status != BillingReservationStatusReserved {
				return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
			}
			return nil
		}
		var err error
		userCacheChanged, tokenCacheChanged, err = adjustReservedBillingTx(tx, &reservation, targetQuota, QuotaLedgerPhaseReserve, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return &reservation, nil
}

// BillingFinalizationResult reports whether this invocation performed the
// terminal transition. A repeated callback can therefore skip duplicate logs.
type BillingFinalizationResult struct {
	Reservation           BillingReservation
	Applied               bool
	PreviousReservedQuota int
	UserCacheChanged      bool
	TokenCacheChanged     bool
}

type billingSettlementIntentMode int

const (
	billingSettlementIntentStrict billingSettlementIntentMode = iota
	billingSettlementIntentTaskAuthoritative
	billingSettlementIntentUnboundIncrease
)

func persistBillingReservationSettlementIntentTx(tx *gorm.DB, reservation *BillingReservation, targetQuota int, mode billingSettlementIntentMode) error {
	if tx == nil || reservation == nil || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return errors.New("invalid billing settlement intent transaction")
	}
	if reservation.Status == BillingReservationStatusSettled {
		if reservation.SettledQuota == targetQuota {
			return nil
		}
		return ErrBillingReservationConflict
	}
	if reservation.Status != BillingReservationStatusReserved {
		return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
	}
	if reservation.SettlementPending {
		if reservation.SettlementTarget == targetQuota {
			return nil
		}
		switch mode {
		case billingSettlementIntentTaskAuthoritative:
			// A target persisted with a terminal task status is the source of
			// truth and may repair an older reservation-only intent.
		case billingSettlementIntentUnboundIncrease:
			if reservation.ResourceType != "" || reservation.ResourceId != "" || targetQuota <= reservation.SettlementTarget {
				return ErrBillingReservationConflict
			}
		default:
			return ErrBillingReservationConflict
		}
	} else if mode == billingSettlementIntentUnboundIncrease &&
		(reservation.ResourceType != "" || reservation.ResourceId != "") {
		return ErrBillingReservationConflict
	}
	reservation.SettlementPending = true
	reservation.SettlementTarget = targetQuota
	reservation.Version++
	reservation.UpdatedAt = common.GetTimestamp()
	if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
		"settlement_pending": reservation.SettlementPending,
		"settlement_target":  reservation.SettlementTarget,
		"version":            reservation.Version,
		"updated_at":         reservation.UpdatedAt,
	}).Error; err != nil {
		return err
	}
	return tx.Create(&QuotaLedgerEntry{
		RequestId:      reservation.RequestId,
		Phase:          QuotaLedgerPhaseIntent,
		Revision:       reservation.Version,
		UserId:         reservation.UserId,
		TokenId:        reservation.TokenId,
		FundingSource:  reservation.FundingSource,
		SubscriptionId: reservation.SubscriptionId,
		Note:           fmt.Sprintf("target_quota=%d", targetQuota),
	}).Error
}

func prepareBillingReservationSettlement(requestId string, targetQuota int, mode billingSettlementIntentMode) error {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return errors.New("invalid billing settlement intent")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		return persistBillingReservationSettlementIntentTx(tx, &reservation, targetQuota, mode)
	})
}

// PrepareBillingReservationSettlement durably records one immutable exact
// target before settlement. Repeating the same target is idempotent; a
// different concurrent target conflicts instead of silently replacing it.
func PrepareBillingReservationSettlement(requestId string, targetQuota int) error {
	return prepareBillingReservationSettlement(requestId, targetQuota, billingSettlementIntentStrict)
}

// AdvanceUnboundBillingReservationSettlementIntent records monotonically
// increasing realtime usage before a request has been bound to any async
// resource. This is intentionally narrower than arbitrary target replacement.
func AdvanceUnboundBillingReservationSettlementIntent(requestId string, targetQuota int) error {
	return prepareBillingReservationSettlement(requestId, targetQuota, billingSettlementIntentUnboundIncrease)
}

// BillingSettlementFailureCode returns a stable audit code only for
// deterministic settlement shortages. Transient database/provider errors must
// never disable a user.
func BillingSettlementFailureCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInsufficientUserQuota):
		return BillingSettlementFailureUserQuota, true
	case errors.Is(err, ErrInsufficientTokenQuota):
		return BillingSettlementFailureTokenQuota, true
	case errors.Is(err, ErrInsufficientSubscriptionQuota), errors.Is(err, ErrNoActiveSubscription):
		return BillingSettlementFailureSubscriptionQuota, true
	case errors.Is(err, ErrBillingAccountNotFound):
		return BillingSettlementFailureAccountMissing, true
	default:
		return "", false
	}
}

func validBillingSettlementFailureCode(code string) bool {
	switch code {
	case BillingSettlementFailureUserQuota,
		BillingSettlementFailureTokenQuota,
		BillingSettlementFailureSubscriptionQuota,
		BillingSettlementFailureAccountMissing:
		return true
	default:
		return false
	}
}

// MarkBillingReservationSettlementShortfall records an exact, request-scoped
// settlement liability and freezes the user in the same transaction. Repeated
// calls with the same target and cause are idempotent. The reservation remains
// open for an automatic retry or an administrator settle/refund decision.
func MarkBillingReservationSettlementShortfall(requestId string, targetQuota int, failureCode string) (*BillingReservation, error) {
	requestId = strings.TrimSpace(requestId)
	failureCode = strings.TrimSpace(failureCode)
	if requestId == "" || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) || !validBillingSettlementFailureCode(failureCode) {
		return nil, errors.New("invalid billing settlement shortfall")
	}

	var reservation BillingReservation
	userCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if reservation.Status == BillingReservationStatusSettled || reservation.Status == BillingReservationStatusRefunded {
			return nil
		}
		if reservation.Status != BillingReservationStatusReserved {
			return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
		}
		if reservation.SettlementPending && reservation.SettlementTarget != targetQuota {
			// Only an unbound realtime liability may grow monotonically. Bound
			// task targets and lower/arbitrary replacements are immutable.
			if reservation.ResourceType != "" || reservation.ResourceId != "" || targetQuota <= reservation.SettlementTarget {
				return ErrBillingReservationConflict
			}
		}
		shortfall := targetQuota - reservation.ReservedQuota
		if shortfall <= 0 {
			return ErrBillingReservationConflict
		}

		var user User
		if err := lockForUpdate(tx).Select("id", "status", "payment_frozen").Where("id = ?", reservation.UserId).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingAccountNotFound
			}
			return err
		}
		previousStatus, err := paymentFreezePreviousUserStatusTx(tx, &user)
		if err != nil {
			return err
		}

		sameShortfall := reservation.ShortfallFreezeApplied &&
			reservation.SettlementPending &&
			reservation.SettlementTarget == targetQuota &&
			reservation.SettlementFailureCode == failureCode &&
			reservation.SettlementShortfallQuota == shortfall
		if !sameShortfall {
			now := common.GetTimestamp()
			if reservation.ShortfallDetectedAt == 0 {
				reservation.ShortfallDetectedAt = now
			}
			reservation.SettlementPending = true
			reservation.SettlementTarget = targetQuota
			reservation.SettlementFailureCode = failureCode
			reservation.SettlementShortfallQuota = shortfall
			reservation.ShortfallFreezeApplied = true
			reservation.ShortfallPreviousStatus = previousStatus
			reservation.ShortfallResolvedAt = 0
			reservation.LastReconciledAt = now
			reservation.ReconcileNote = fmt.Sprintf("settlement shortfall requires review: code=%s target=%d reserved=%d", failureCode, targetQuota, reservation.ReservedQuota)
			reservation.Version++
			reservation.UpdatedAt = now
			if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
				"settlement_pending":         reservation.SettlementPending,
				"settlement_target":          reservation.SettlementTarget,
				"settlement_failure_code":    reservation.SettlementFailureCode,
				"settlement_shortfall_quota": reservation.SettlementShortfallQuota,
				"shortfall_freeze_applied":   reservation.ShortfallFreezeApplied,
				"shortfall_previous_status":  reservation.ShortfallPreviousStatus,
				"shortfall_detected_at":      reservation.ShortfallDetectedAt,
				"shortfall_resolved_at":      reservation.ShortfallResolvedAt,
				"last_reconciled_at":         reservation.LastReconciledAt,
				"reconcile_note":             reservation.ReconcileNote,
				"version":                    reservation.Version,
				"updated_at":                 reservation.UpdatedAt,
			}).Error; err != nil {
				return err
			}
			if err := tx.Create(&QuotaLedgerEntry{
				RequestId:      reservation.RequestId,
				Phase:          QuotaLedgerPhaseShortfall,
				Revision:       reservation.Version,
				UserId:         reservation.UserId,
				TokenId:        reservation.TokenId,
				FundingSource:  reservation.FundingSource,
				SubscriptionId: reservation.SubscriptionId,
				Note:           fmt.Sprintf("code=%s target_quota=%d reserved_quota=%d outstanding_quota=%d", failureCode, targetQuota, reservation.ReservedQuota, shortfall),
			}).Error; err != nil {
				return err
			}
		}

		update := tx.Model(&User{}).Where("id = ?", reservation.UserId).Updates(map[string]interface{}{
			"status":         common.UserStatusDisabled,
			"payment_frozen": true,
		})
		if update.Error != nil {
			return update.Error
		}
		userCacheChanged = !user.PaymentFrozen || user.Status != common.UserStatusDisabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(reservation.UserId, "", userCacheChanged, false)
	return &reservation, nil
}

func settleBillingReservationTx(tx *gorm.DB, reservation *BillingReservation, actualQuota int, allowMissingToken bool) (bool, bool, bool, error) {
	if reservation == nil || tx == nil {
		return false, false, false, errors.New("invalid billing settlement transaction")
	}
	if reservation.Status == BillingReservationStatusSettled {
		if reservation.SettledQuota == actualQuota {
			return false, false, false, nil
		}
		return false, false, false, ErrBillingReservationConflict
	}
	if reservation.Status != BillingReservationStatusReserved {
		return false, false, false, fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
	}
	userCacheChanged, tokenCacheChanged := false, false
	delta := actualQuota - reservation.ReservedQuota
	if delta != 0 {
		var err error
		userCacheChanged, tokenCacheChanged, err = adjustReservedBillingTx(tx, reservation, actualQuota, QuotaLedgerPhaseSettle, allowMissingToken)
		if err != nil {
			return false, false, false, err
		}
	} else {
		if err := tx.Create(&QuotaLedgerEntry{
			RequestId:      reservation.RequestId,
			Phase:          QuotaLedgerPhaseSettle,
			Revision:       reservation.Version + 1,
			UserId:         reservation.UserId,
			TokenId:        reservation.TokenId,
			FundingSource:  reservation.FundingSource,
			SubscriptionId: reservation.SubscriptionId,
		}).Error; err != nil {
			return false, false, false, err
		}
	}
	reservation.Status = BillingReservationStatusSettled
	reservation.SettledQuota = actualQuota
	reservation.SettlementPending = false
	reservation.SettlementTarget = 0
	if reservation.ShortfallFreezeApplied && reservation.ShortfallResolvedAt == 0 {
		reservation.ShortfallResolvedAt = common.GetTimestamp()
	}
	reservation.Version++
	reservation.UpdatedAt = common.GetTimestamp()
	if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
		"status":                reservation.Status,
		"settled_quota":         reservation.SettledQuota,
		"settlement_pending":    reservation.SettlementPending,
		"settlement_target":     reservation.SettlementTarget,
		"shortfall_resolved_at": reservation.ShortfallResolvedAt,
		"version":               reservation.Version,
		"updated_at":            reservation.UpdatedAt,
	}).Error; err != nil {
		return false, false, false, err
	}
	if reservation.ShortfallFreezeApplied {
		freezeChanged, err := releaseUserPaymentFreezeTx(tx, reservation.UserId, reservation.ShortfallPreviousStatus)
		if err != nil {
			return false, false, false, err
		}
		userCacheChanged = userCacheChanged || freezeChanged
	}
	return true, userCacheChanged, tokenCacheChanged, nil
}

// SettleBillingReservation applies actualQuota exactly once. Funding and token
// adjustments are committed atomically.
func SettleBillingReservation(requestId string, actualQuota int, tokenKey string) (*BillingReservation, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" || actualQuota < 0 || int64(actualQuota) > int64(math.MaxInt32) {
		return nil, errors.New("invalid billing settlement")
	}

	var reservation BillingReservation
	userCacheChanged := false
	tokenCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		var settleErr error
		_, userCacheChanged, tokenCacheChanged, settleErr = settleBillingReservationTx(tx, &reservation, actualQuota, true)
		return settleErr
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return &reservation, nil
}

func refundBillingReservationTx(tx *gorm.DB, reservation *BillingReservation, allowMissingToken bool) (bool, bool, bool, error) {
	if tx == nil || reservation == nil {
		return false, false, false, errors.New("invalid billing refund transaction")
	}
	if reservation.Status == BillingReservationStatusRefunded {
		return false, false, false, nil
	}
	if reservation.Status != BillingReservationStatusReserved {
		return false, false, false, fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
	}

	ledger := &QuotaLedgerEntry{
		RequestId:      reservation.RequestId,
		Phase:          QuotaLedgerPhaseRefund,
		Revision:       reservation.Version + 1,
		UserId:         reservation.UserId,
		TokenId:        reservation.TokenId,
		FundingSource:  reservation.FundingSource,
		SubscriptionId: reservation.SubscriptionId,
	}
	userCacheChanged := false
	tokenCacheChanged := false
	switch reservation.FundingSource {
	case BillingFundingWallet:
		if err := applyUserQuotaDeltaTx(tx, reservation.UserId, reservation.ReservedQuota); err != nil {
			return false, false, false, err
		}
		ledger.UserQuotaDelta = reservation.ReservedQuota
		userCacheChanged = reservation.ReservedQuota > 0
	case BillingFundingSubscription:
		periodDelta, totalDelta, err := applySubscriptionReservationDeltaTx(tx, reservation, -int64(reservation.ReservedQuota))
		if err != nil {
			return false, false, false, err
		}
		if !reservation.LegacyAdopted {
			updateResult := tx.Model(&SubscriptionPreConsumeRecord{}).
				Where("request_id = ? AND status = ?", reservation.RequestId, "consumed").
				Updates(map[string]interface{}{
					"status":     "refunded",
					"updated_at": common.GetTimestamp(),
				})
			if updateResult.Error != nil {
				return false, false, false, updateResult.Error
			}
			if updateResult.RowsAffected != 1 {
				return false, false, false, ErrBillingReservationConflict
			}
		}
		ledger.SubscriptionUsedDelta = periodDelta
		ledger.SubscriptionTotalUsedDelta = totalDelta
	default:
		return false, false, false, ErrBillingReservationConflict
	}

	if reservation.TokenMode != billingTokenModeSkipped && reservation.TokenReserved > 0 {
		tokenExists, err := billingTokenExistsTx(tx, reservation.UserId, reservation.TokenId)
		if err != nil {
			return false, false, false, err
		}
		if !tokenExists && allowMissingToken {
			ledger.Note = "token deleted before async refund; token delta skipped"
		} else {
			remainDelta, usedDelta, err := applyTokenQuotaDeltaTx(
				tx,
				reservation.UserId,
				reservation.TokenId,
				-reservation.TokenReserved,
				reservation.TokenMode == billingTokenModeUnlimited,
			)
			if err != nil {
				return false, false, false, err
			}
			ledger.TokenRemainQuotaDelta = remainDelta
			ledger.TokenUsedQuotaDelta = usedDelta
			tokenCacheChanged = true
		}
	}

	reservation.Status = BillingReservationStatusRefunded
	reservation.SettlementPending = false
	reservation.SettlementTarget = 0
	if reservation.ShortfallFreezeApplied && reservation.ShortfallResolvedAt == 0 {
		reservation.ShortfallResolvedAt = common.GetTimestamp()
	}
	reservation.Version++
	reservation.UpdatedAt = common.GetTimestamp()
	if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
		"status":                reservation.Status,
		"settlement_pending":    reservation.SettlementPending,
		"settlement_target":     reservation.SettlementTarget,
		"shortfall_resolved_at": reservation.ShortfallResolvedAt,
		"version":               reservation.Version,
		"updated_at":            reservation.UpdatedAt,
	}).Error; err != nil {
		return false, false, false, err
	}
	if err := tx.Create(ledger).Error; err != nil {
		return false, false, false, err
	}
	if reservation.ShortfallFreezeApplied {
		freezeChanged, err := releaseUserPaymentFreezeTx(tx, reservation.UserId, reservation.ShortfallPreviousStatus)
		if err != nil {
			return false, false, false, err
		}
		userCacheChanged = userCacheChanged || freezeChanged
	}
	return true, userCacheChanged, tokenCacheChanged, nil
}

// RefundBillingReservation synchronously and idempotently reverses an open
// reservation. A committed settlement cannot be refunded through this failure
// path; external reversals use the payment/debt ledger instead.
func RefundBillingReservation(requestId string, tokenKey string) (*BillingReservation, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return nil, errors.New("billing requestId is empty")
	}

	var reservation BillingReservation
	userCacheChanged := false
	tokenCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		var refundErr error
		_, userCacheChanged, tokenCacheChanged, refundErr = refundBillingReservationTx(tx, &reservation, true)
		return refundErr
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return &reservation, nil
}

func GetBillingReservation(requestId string) (*BillingReservation, error) {
	var reservation BillingReservation
	if err := DB.Where("request_id = ?", strings.TrimSpace(requestId)).First(&reservation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillingReservationNotFound
		}
		return nil, err
	}
	return &reservation, nil
}

// CreateTaskWithBillingReservation atomically persists an async task, adjusts
// its final submit-time reservation, and binds both records once the upstream
// submission response has been accepted. If the process dies before this
// transaction, the unbound reservation remains reviewable rather than being
// guessed into a refund.
func CreateTaskWithBillingReservation(task *Task, requestId string, targetQuota int, tokenKey string) error {
	requestId = strings.TrimSpace(requestId)
	if task == nil || task.UserId <= 0 || strings.TrimSpace(task.TaskID) == "" || requestId == "" ||
		targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return errors.New("invalid async task billing binding")
	}
	task.Quota = targetQuota
	task.PrivateData.BillingRequestId = requestId
	if task.Status == TaskStatusSuccess {
		if err := task.PrivateData.RecordBillingTargetQuota(targetQuota); err != nil {
			return err
		}
	} else {
		task.PrivateData.ClearBillingTargetQuota()
	}

	var reservation BillingReservation
	userCacheChanged := false
	tokenCacheChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if reservation.UserId != task.UserId || reservation.Status != BillingReservationStatusReserved {
			return ErrBillingReservationConflict
		}
		if reservation.ResourceType != "" &&
			(reservation.ResourceType != BillingResourceAsyncTask || reservation.ResourceId != task.TaskID) {
			return ErrBillingReservationConflict
		}

		var err error
		userCacheChanged, tokenCacheChanged, err = adjustReservedBillingTx(tx, &reservation, targetQuota, QuotaLedgerPhaseReserve, false)
		if err != nil {
			return err
		}

		var existing Task
		query := tx.Where("user_id = ? AND task_id = ?", task.UserId, task.TaskID).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.PrivateData.BillingRequestId != requestId {
				return ErrBillingReservationConflict
			}
			task.ID = existing.ID
		} else if err := tx.Create(task).Error; err != nil {
			return err
		}

		if reservation.ResourceType == BillingResourceAsyncTask && reservation.ResourceId == task.TaskID {
			return nil
		}
		reservation.ResourceType = BillingResourceAsyncTask
		reservation.ResourceId = task.TaskID
		reservation.Version++
		reservation.UpdatedAt = common.GetTimestamp()
		if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
			"resource_type": reservation.ResourceType,
			"resource_id":   reservation.ResourceId,
			"version":       reservation.Version,
			"updated_at":    reservation.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&QuotaLedgerEntry{
			RequestId:      reservation.RequestId,
			Phase:          QuotaLedgerPhaseBind,
			Revision:       reservation.Version,
			UserId:         reservation.UserId,
			TokenId:        reservation.TokenId,
			FundingSource:  reservation.FundingSource,
			SubscriptionId: reservation.SubscriptionId,
			Note:           "resource=async_task:" + task.TaskID,
		}).Error
	})
	if err != nil {
		return err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return nil
}

// AdoptLegacyTaskBillingReservation creates a zero-mutation durable baseline
// for an unfinished task that was charged before BillingReservation existed.
// Only unfinished rows are adopted, so historical terminal tasks cannot be
// replayed. After adoption, normal terminal settlement/refund and stale
// reconciliation use the same state machine as newly submitted tasks.
func AdoptLegacyTaskBillingReservation(taskId int64) (*BillingReservation, error) {
	if taskId <= 0 {
		return nil, ErrTaskNotPersisted
	}
	var reservation BillingReservation
	var task Task
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure {
			return ErrBillingReservationFinalized
		}
		if task.Quota < 0 || int64(task.Quota) > int64(math.MaxInt32) {
			return ErrBillingReservationConflict
		}
		if requestId := strings.TrimSpace(task.PrivateData.BillingRequestId); requestId != "" {
			if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
				return err
			}
			return validateTaskBillingLink(&task, &reservation, requestId)
		}

		requestId := legacyTaskBillingRequestId(task.ID)
		query := lockForUpdate(tx).Where("request_id = ?", requestId).Limit(1).Find(&reservation)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if reservation.UserId != task.UserId || reservation.ResourceType != BillingResourceAsyncTask || reservation.ResourceId != task.TaskID {
				return ErrBillingReservationConflict
			}
			task.PrivateData.BillingRequestId = requestId
			return tx.Model(&Task{}).Where("id = ?", task.ID).Update("private_data", task.PrivateData).Error
		}

		reservation = BillingReservation{
			RequestId:     requestId,
			UserId:        task.UserId,
			TokenId:       task.PrivateData.TokenId,
			FundingSource: BillingFundingWallet,
			ResourceType:  BillingResourceAsyncTask,
			ResourceId:    task.TaskID,
			InitialQuota:  task.Quota,
			ReservedQuota: task.Quota,
			Status:        BillingReservationStatusReserved,
			Version:       1,
			LegacyAdopted: true,
			TokenMode:     billingTokenModeSkipped,
		}
		ledger := &QuotaLedgerEntry{
			RequestId:     requestId,
			Phase:         QuotaLedgerPhaseAdopt,
			Revision:      1,
			UserId:        task.UserId,
			TokenId:       task.PrivateData.TokenId,
			FundingSource: BillingFundingWallet,
			Note:          fmt.Sprintf("legacy charged baseline adopted without mutation; quota=%d", task.Quota),
		}
		if task.PrivateData.BillingSource == BillingFundingSubscription && task.PrivateData.SubscriptionId > 0 {
			var subscription UserSubscription
			if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", task.PrivateData.SubscriptionId, task.UserId).First(&subscription).Error; err != nil {
				return err
			}
			reservation.FundingSource = BillingFundingSubscription
			reservation.SubscriptionId = subscription.Id
			captureLegacySubscriptionReservationEpoch(&reservation, &task, &subscription)
			ledger.FundingSource = BillingFundingSubscription
			ledger.SubscriptionId = subscription.Id
		}
		if task.PrivateData.TokenId > 0 {
			var token Token
			tokenQuery := tx.Select("id", "unlimited_quota").Where("id = ? AND user_id = ?", task.PrivateData.TokenId, task.UserId).Limit(1).Find(&token)
			if tokenQuery.Error != nil {
				return tokenQuery.Error
			}
			if tokenQuery.RowsAffected > 0 {
				reservation.TokenReserved = task.Quota
				reservation.TokenMode = billingTokenModeLimited
				if token.UnlimitedQuota {
					reservation.TokenMode = billingTokenModeUnlimited
				}
			} else {
				ledger.Note += "; token already deleted"
			}
		}
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		if err := tx.Create(ledger).Error; err != nil {
			return err
		}
		task.PrivateData.BillingRequestId = requestId
		return tx.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"private_data": task.PrivateData,
			"updated_at":   common.GetTimestamp(),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

func validateTaskBillingLink(task *Task, reservation *BillingReservation, requestId string) error {
	if task == nil || reservation == nil ||
		task.UserId != reservation.UserId ||
		task.PrivateData.BillingRequestId != requestId ||
		reservation.ResourceType != BillingResourceAsyncTask ||
		reservation.ResourceId != task.TaskID {
		return ErrBillingReservationConflict
	}
	return nil
}

func prepareTaskBillingReservationSettlement(taskId int64, requestId string, actualQuota int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var task Task
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != TaskStatusSuccess {
			return ErrBillingReservationConflict
		}
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateTaskBillingLink(&task, &reservation, requestId); err != nil {
			return err
		}
		mode := billingSettlementIntentStrict
		if intendedQuota, hasTaskIntent := task.PrivateData.BillingTargetQuotaIntent(); hasTaskIntent {
			if intendedQuota != actualQuota {
				return ErrBillingReservationConflict
			}
			mode = billingSettlementIntentTaskAuthoritative
		}
		return persistBillingReservationSettlementIntentTx(tx, &reservation, actualQuota, mode)
	})
}

// SettleTaskBillingReservation performs the financial terminal transition and
// the task quota/private-data update in one database transaction. An intent is
// persisted first so a transient failure remains exactly retryable.
func SettleTaskBillingReservation(taskId int64, requestId string, actualQuota int, tokenKey string) (*BillingFinalizationResult, error) {
	requestId = strings.TrimSpace(requestId)
	if taskId <= 0 || requestId == "" || actualQuota < 0 || int64(actualQuota) > int64(math.MaxInt32) {
		return nil, errors.New("invalid async task billing settlement")
	}
	if err := prepareTaskBillingReservationSettlement(taskId, requestId, actualQuota); err != nil {
		return nil, err
	}

	result := &BillingFinalizationResult{}
	var task Task
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != TaskStatusSuccess {
			return ErrBillingReservationConflict
		}
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&result.Reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateTaskBillingLink(&task, &result.Reservation, requestId); err != nil {
			return err
		}
		if intendedQuota, ok := task.PrivateData.BillingTargetQuotaIntent(); ok && intendedQuota != actualQuota {
			return ErrBillingReservationConflict
		}
		result.PreviousReservedQuota = result.Reservation.ReservedQuota
		var err error
		result.Applied, result.UserCacheChanged, result.TokenCacheChanged, err = settleBillingReservationTx(tx, &result.Reservation, actualQuota, true)
		if err != nil {
			return err
		}
		task.Quota = actualQuota
		task.PrivateData.ClearBillingTargetQuota()
		return tx.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"quota":        task.Quota,
			"private_data": task.PrivateData,
			"updated_at":   common.GetTimestamp(),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(result.Reservation.UserId, tokenKey, result.UserCacheChanged, result.TokenCacheChanged)
	return result, nil
}

// RefundTaskBillingReservation verifies the bound task is failed, then refunds
// funding and token quota exactly once in the same transaction.
func RefundTaskBillingReservation(taskId int64, requestId string, tokenKey string) (*BillingFinalizationResult, error) {
	requestId = strings.TrimSpace(requestId)
	if taskId <= 0 || requestId == "" {
		return nil, errors.New("invalid async task billing refund")
	}
	result := &BillingFinalizationResult{}
	var task Task
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != TaskStatusFailure {
			return ErrBillingReservationConflict
		}
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&result.Reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateTaskBillingLink(&task, &result.Reservation, requestId); err != nil {
			return err
		}
		var err error
		result.Applied, result.UserCacheChanged, result.TokenCacheChanged, err = refundBillingReservationTx(tx, &result.Reservation, true)
		if err != nil {
			return err
		}
		if _, hasIntent := task.PrivateData.BillingTargetQuotaIntent(); hasIntent {
			task.PrivateData.ClearBillingTargetQuota()
			return tx.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
				"private_data": task.PrivateData,
				"updated_at":   common.GetTimestamp(),
			}).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(result.Reservation.UserId, tokenKey, result.UserCacheChanged, result.TokenCacheChanged)
	return result, nil
}

// LegacyTaskBillingResult is the compatibility result for tasks created before
// durable request reservations were introduced.
type LegacyTaskBillingResult struct {
	Applied           bool
	PreviousQuota     int
	FinalQuota        int
	UserCacheChanged  bool
	TokenCacheChanged bool
}

func legacyTaskBillingRequestId(taskId int64) string {
	return fmt.Sprintf("legacy-task-%d", taskId)
}

func finalizeLegacyTaskBilling(taskId int64, actualQuota int, outcome string) (*LegacyTaskBillingResult, error) {
	if taskId <= 0 || actualQuota < 0 || int64(actualQuota) > int64(math.MaxInt32) ||
		(outcome != "settle" && outcome != "refund") {
		return nil, errors.New("invalid legacy task billing finalization")
	}
	result := &LegacyTaskBillingResult{FinalQuota: actualQuota}
	var task Task
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if strings.TrimSpace(task.PrivateData.BillingRequestId) != "" {
			return ErrBillingReservationConflict
		}
		if outcome == "settle" && task.Status != TaskStatusSuccess {
			return ErrBillingReservationConflict
		}
		if outcome == "refund" && task.Status != TaskStatusFailure {
			return ErrBillingReservationConflict
		}
		if task.PrivateData.LegacyBillingFinalized {
			if task.PrivateData.LegacyBillingOutcome == outcome && task.PrivateData.LegacyBillingFinalQuota == actualQuota {
				return nil
			}
			return ErrBillingReservationConflict
		}
		result.PreviousQuota = task.Quota
		if outcome == "refund" {
			result.FinalQuota = 0
		}
		delta := actualQuota - task.Quota
		if outcome == "refund" {
			delta = -task.Quota
		}
		ledger := &QuotaLedgerEntry{
			RequestId:     legacyTaskBillingRequestId(task.ID),
			Phase:         QuotaLedgerPhaseSettle,
			Revision:      1,
			UserId:        task.UserId,
			TokenId:       task.PrivateData.TokenId,
			FundingSource: BillingFundingWallet,
			Note:          "legacy task billing compatibility finalization",
		}
		if outcome == "refund" {
			ledger.Phase = QuotaLedgerPhaseRefund
		}
		userCacheChanged := false
		tokenCacheChanged := false
		if task.PrivateData.BillingSource == BillingFundingSubscription && task.PrivateData.SubscriptionId > 0 {
			ledger.FundingSource = BillingFundingSubscription
			reservation := &BillingReservation{
				RequestId:                ledger.RequestId,
				UserId:                   task.UserId,
				SubscriptionId:           task.PrivateData.SubscriptionId,
				SubscriptionResetAt:      0,
				SubscriptionResetVersion: 0,
			}
			var subscription UserSubscription
			if err := lockForUpdate(tx).Where("id = ?", task.PrivateData.SubscriptionId).First(&subscription).Error; err != nil {
				return err
			}
			captureLegacySubscriptionReservationEpoch(reservation, &task, &subscription)
			periodDelta, totalDelta, err := applySubscriptionReservationDeltaTx(tx, reservation, int64(delta))
			if err != nil {
				return err
			}
			ledger.SubscriptionId = reservation.SubscriptionId
			ledger.SubscriptionUsedDelta = periodDelta
			ledger.SubscriptionTotalUsedDelta = totalDelta
		} else {
			if err := applyUserQuotaDeltaTx(tx, task.UserId, -delta); err != nil {
				return err
			}
			ledger.UserQuotaDelta = -delta
			userCacheChanged = delta != 0
		}

		if task.PrivateData.TokenId > 0 {
			tokenExists, err := billingTokenExistsTx(tx, task.UserId, task.PrivateData.TokenId)
			if err != nil {
				return err
			}
			if tokenExists {
				var token Token
				if err := lockForUpdate(tx).Select("id", "unlimited_quota").Where("id = ? AND user_id = ?", task.PrivateData.TokenId, task.UserId).First(&token).Error; err != nil {
					return err
				}
				remainDelta, usedDelta, err := applyTokenQuotaDeltaTx(tx, task.UserId, task.PrivateData.TokenId, delta, token.UnlimitedQuota)
				if err != nil {
					return err
				}
				ledger.TokenRemainQuotaDelta = remainDelta
				ledger.TokenUsedQuotaDelta = usedDelta
				tokenCacheChanged = delta != 0
			} else {
				ledger.Note += "; token deleted, token delta skipped"
			}
		}

		task.PrivateData.LegacyBillingFinalized = true
		task.PrivateData.LegacyBillingOutcome = outcome
		task.PrivateData.LegacyBillingFinalQuota = actualQuota
		if outcome == "settle" {
			task.Quota = actualQuota
		}
		if err := tx.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"quota":        task.Quota,
			"private_data": task.PrivateData,
			"updated_at":   common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(ledger).Error; err != nil {
			return err
		}
		result.Applied = true
		result.UserCacheChanged = userCacheChanged
		result.TokenCacheChanged = tokenCacheChanged
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Applied {
		var reservation BillingReservation
		reservation.UserId = task.UserId
		reservation.TokenId = task.PrivateData.TokenId
		invalidateBillingQuotaCaches(task.UserId, billingReservationTokenKey(reservation), result.UserCacheChanged, result.TokenCacheChanged)
	}
	return result, nil
}

func SettleLegacyTaskBilling(taskId int64, actualQuota int) (*LegacyTaskBillingResult, error) {
	return finalizeLegacyTaskBilling(taskId, actualQuota, "settle")
}

func RefundLegacyTaskBilling(taskId int64) (*LegacyTaskBillingResult, error) {
	return finalizeLegacyTaskBilling(taskId, 0, "refund")
}

type BillingReconcileSummary struct {
	Scanned        int `json:"scanned"`
	Settled        int `json:"settled"`
	Refunded       int `json:"refunded"`
	Pending        int `json:"pending"`
	ReviewRequired int `json:"review_required"`
	Errors         int `json:"errors"`
}

func HasStaleBillingReservations(cutoffUnix int64) bool {
	if cutoffUnix <= 0 {
		return false
	}
	var id int64
	err := DB.Model(&BillingReservation{}).
		Where("status = ? AND updated_at <= ?", BillingReservationStatusReserved, cutoffUnix).
		Where("last_reconciled_at = 0 OR last_reconciled_at <= ?", cutoffUnix).
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func recordBillingReconcileReview(requestId string, note string) error {
	if len(note) > 255 {
		note = note[:255]
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			return err
		}
		if reservation.Status != BillingReservationStatusReserved {
			return nil
		}
		now := common.GetTimestamp()
		if err := tx.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
			"last_reconciled_at": now,
			"reconcile_note":     note,
		}).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&QuotaLedgerEntry{
			RequestId:      reservation.RequestId,
			Phase:          QuotaLedgerPhaseReview,
			Revision:       reservation.Version,
			UserId:         reservation.UserId,
			TokenId:        reservation.TokenId,
			FundingSource:  reservation.FundingSource,
			SubscriptionId: reservation.SubscriptionId,
			Note:           note,
		}).Error
	})
}

func billingReservationTokenKey(reservation BillingReservation) string {
	if reservation.TokenId <= 0 {
		return ""
	}
	var key string
	_ = DB.Model(&Token{}).Where("id = ?", reservation.TokenId).Pluck("key", &key).Error
	return key
}

// ReconcileStaleBillingReservations only auto-mutates balances when a bound
// task provides an unambiguous terminal outcome. Unbound, missing, or active
// tasks retain their reservation and receive an append-only review marker.
func ReconcileStaleBillingReservations(cutoffUnix int64, limit int) BillingReconcileSummary {
	summary := BillingReconcileSummary{}
	if cutoffUnix <= 0 {
		return summary
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var reservations []BillingReservation
	if err := DB.Where("status = ? AND updated_at <= ?", BillingReservationStatusReserved, cutoffUnix).
		Where("last_reconciled_at = 0 OR last_reconciled_at <= ?", cutoffUnix).
		Order("updated_at asc, id asc").
		Limit(limit).
		Find(&reservations).Error; err != nil {
		summary.Errors++
		return summary
	}
	for _, reservation := range reservations {
		summary.Scanned++
		if strings.TrimSpace(reservation.ResourceId) == "" {
			summary.ReviewRequired++
			if err := recordBillingReconcileReview(reservation.RequestId, "stale reservation has no unambiguous task binding"); err != nil {
				summary.Errors++
			}
			continue
		}
		tokenKey := billingReservationTokenKey(reservation)
		switch reservation.ResourceType {
		case BillingResourceAsyncTask:
			var task Task
			if err := DB.Where("user_id = ? AND task_id = ?", reservation.UserId, reservation.ResourceId).First(&task).Error; err != nil {
				summary.ReviewRequired++
				if err2 := recordBillingReconcileReview(reservation.RequestId, "bound async task is missing"); err2 != nil {
					summary.Errors++
				}
				continue
			}
			switch task.Status {
			case TaskStatusSuccess:
				targetQuota := task.Quota
				if taskTarget, ok := task.PrivateData.BillingTargetQuotaIntent(); ok {
					targetQuota = taskTarget
				} else if reservation.SettlementPending {
					targetQuota = reservation.SettlementTarget
				}
				result, err := SettleTaskBillingReservation(task.ID, reservation.RequestId, targetQuota, tokenKey)
				if err != nil {
					summary.Errors++
					summary.ReviewRequired++
					if reviewErr := recordBillingReconcileReview(reservation.RequestId, "terminal success settlement retry failed: "+err.Error()); reviewErr != nil {
						summary.Errors++
					}
					continue
				}
				if result.Applied {
					summary.Settled++
				}
			case TaskStatusFailure:
				result, err := RefundTaskBillingReservation(task.ID, reservation.RequestId, tokenKey)
				if err != nil {
					summary.Errors++
					summary.ReviewRequired++
					if reviewErr := recordBillingReconcileReview(reservation.RequestId, "terminal failure refund retry failed: "+err.Error()); reviewErr != nil {
						summary.Errors++
					}
					continue
				}
				if result.Applied {
					summary.Refunded++
				}
			default:
				summary.Pending++
				if err := recordBillingReconcileReview(reservation.RequestId, "bound async task is still non-terminal"); err != nil {
					summary.Errors++
				}
			}
		case BillingResourceMidjourneyTask:
			midjourneyId, err := strconv.Atoi(reservation.ResourceId)
			if err != nil || midjourneyId <= 0 {
				summary.ReviewRequired++
				if reviewErr := recordBillingReconcileReview(reservation.RequestId, "bound midjourney task id is invalid"); reviewErr != nil {
					summary.Errors++
				}
				continue
			}
			var task Midjourney
			if err := DB.Where("id = ? AND user_id = ?", midjourneyId, reservation.UserId).First(&task).Error; err != nil {
				summary.ReviewRequired++
				if reviewErr := recordBillingReconcileReview(reservation.RequestId, "bound midjourney task is missing"); reviewErr != nil {
					summary.Errors++
				}
				continue
			}
			switch task.Status {
			case MidjourneyStatusSuccess:
				targetQuota := task.Quota
				if taskTarget, ok := task.PrivateData.BillingTargetQuotaIntent(); ok {
					targetQuota = taskTarget
				} else if reservation.SettlementPending {
					targetQuota = reservation.SettlementTarget
				}
				result, err := SettleMidjourneyBillingReservation(task.Id, reservation.RequestId, targetQuota, tokenKey)
				if err != nil {
					summary.Errors++
					summary.ReviewRequired++
					if reviewErr := recordBillingReconcileReview(reservation.RequestId, "midjourney success settlement retry failed: "+err.Error()); reviewErr != nil {
						summary.Errors++
					}
					continue
				}
				if result.Applied {
					summary.Settled++
				}
			case MidjourneyStatusFailure:
				result, err := RefundMidjourneyBillingReservation(task.Id, reservation.RequestId, tokenKey)
				if err != nil {
					summary.Errors++
					summary.ReviewRequired++
					if reviewErr := recordBillingReconcileReview(reservation.RequestId, "midjourney failure refund retry failed: "+err.Error()); reviewErr != nil {
						summary.Errors++
					}
					continue
				}
				if result.Applied {
					summary.Refunded++
				}
			default:
				summary.Pending++
				if err := recordBillingReconcileReview(reservation.RequestId, "bound midjourney task is still non-terminal"); err != nil {
					summary.Errors++
				}
			}
		default:
			summary.ReviewRequired++
			if err := recordBillingReconcileReview(reservation.RequestId, "stale reservation has an unsupported resource type"); err != nil {
				summary.Errors++
			}
		}
	}
	return summary
}

func applyUserQuotaDeltaTx(tx *gorm.DB, userId int, delta int) error {
	if tx == nil || userId <= 0 {
		return ErrBillingAccountNotFound
	}
	if delta == 0 {
		var count int64
		if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrBillingAccountNotFound
		}
		return nil
	}

	query := tx.Model(&User{}).Where("id = ?", userId)
	if delta < 0 {
		amount := -delta
		query = query.Where("quota >= ?", amount).
			UpdateColumn("quota", gorm.Expr("quota - ?", amount))
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 {
			return ErrInsufficientUserQuota
		}
		return nil
	}

	query = query.Where("quota <= ?", math.MaxInt32-delta).
		UpdateColumn("quota", gorm.Expr("quota + ?", delta))
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected != 1 {
		return ErrQuotaOverflow
	}
	return nil
}

// applyTokenQuotaDeltaTx applies a usage delta: positive consumes, negative
// refunds. Unlimited tokens retain remain_quota and only track used_quota.
func applyTokenQuotaDeltaTx(tx *gorm.DB, userId int, tokenId int, delta int, unlimited bool) (int, int, error) {
	if tx == nil || userId <= 0 || tokenId <= 0 {
		return 0, 0, ErrBillingAccountNotFound
	}
	if delta == 0 {
		var count int64
		if err := tx.Model(&Token{}).Where("id = ? AND user_id = ?", tokenId, userId).Count(&count).Error; err != nil {
			return 0, 0, err
		}
		if count != 1 {
			return 0, 0, ErrBillingAccountNotFound
		}
		return 0, 0, nil
	}

	now := common.GetTimestamp()
	query := tx.Model(&Token{}).Where("id = ? AND user_id = ?", tokenId, userId)
	if delta > 0 {
		query = query.Where("used_quota <= ?", math.MaxInt32-delta)
		updates := map[string]interface{}{
			"used_quota":    gorm.Expr("used_quota + ?", delta),
			"accessed_time": now,
		}
		remainDelta := 0
		if !unlimited {
			query = query.Where("remain_quota >= ?", delta)
			updates["remain_quota"] = gorm.Expr("remain_quota - ?", delta)
			remainDelta = -delta
		}
		query = query.Updates(updates)
		if query.Error != nil {
			return 0, 0, query.Error
		}
		if query.RowsAffected != 1 {
			return 0, 0, ErrInsufficientTokenQuota
		}
		return remainDelta, delta, nil
	}

	amount := -delta
	query = query.Where("used_quota >= ?", math.MinInt32+amount)
	updates := map[string]interface{}{
		"used_quota":    gorm.Expr("used_quota - ?", amount),
		"accessed_time": now,
	}
	remainDelta := 0
	if !unlimited {
		query = query.Where("remain_quota <= ?", math.MaxInt32-amount)
		updates["remain_quota"] = gorm.Expr("remain_quota + ?", amount)
		remainDelta = amount
	}
	query = query.Updates(updates)
	if query.Error != nil {
		return 0, 0, query.Error
	}
	if query.RowsAffected != 1 {
		return 0, 0, ErrQuotaOverflow
	}
	return remainDelta, -amount, nil
}

func billingTokenExistsTx(tx *gorm.DB, userId int, tokenId int) (bool, error) {
	if tx == nil || userId <= 0 || tokenId <= 0 {
		return false, ErrBillingAccountNotFound
	}
	var count int64
	if err := tx.Model(&Token{}).Where("id = ? AND user_id = ?", tokenId, userId).Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

// applySubscriptionReservationDeltaTx updates the current-period and lifetime
// counters with reset awareness. A task reserved before a quota reset must not
// refund from, or add usage to, the new period; its lifetime accounting still
// moves by the exact reservation delta.
func applySubscriptionReservationDeltaTx(tx *gorm.DB, reservation *BillingReservation, delta int64) (int64, int64, error) {
	if tx == nil || reservation == nil || reservation.SubscriptionId <= 0 {
		return 0, 0, ErrBillingAccountNotFound
	}
	if delta == 0 {
		return 0, 0, nil
	}
	var subscription UserSubscription
	if err := lockForUpdate(tx).Where("id = ?", reservation.SubscriptionId).First(&subscription).Error; err != nil {
		return 0, 0, err
	}

	totalUsed := subscription.AmountUsedTotal
	if totalUsed < subscription.AmountUsed {
		totalUsed = subscription.AmountUsed
	}
	if delta > 0 && totalUsed > math.MaxInt64-delta {
		return 0, 0, ErrQuotaOverflow
	}
	newTotalUsed := totalUsed + delta
	if newTotalUsed < 0 {
		return 0, 0, ErrBillingReservationConflict
	}

	periodDelta := int64(0)
	newPeriodUsed := subscription.AmountUsed
	if subscription.LastResetTime == reservation.SubscriptionResetAt &&
		subscription.QuotaResetVersion == reservation.SubscriptionResetVersion {
		if delta > 0 && subscription.AmountUsed > math.MaxInt64-delta {
			return 0, 0, ErrQuotaOverflow
		}
		newPeriodUsed = subscription.AmountUsed + delta
		if newPeriodUsed < 0 {
			return 0, 0, ErrBillingReservationConflict
		}
		if subscription.AmountTotal > 0 && newPeriodUsed > subscription.AmountTotal {
			return 0, 0, fmt.Errorf("%w: need=%d remaining=%d", ErrInsufficientSubscriptionQuota, delta, subscription.AmountTotal-subscription.AmountUsed)
		}
		periodDelta = delta
	}

	updates := map[string]interface{}{
		"amount_used":       newPeriodUsed,
		"amount_used_total": newTotalUsed,
	}
	if err := tx.Model(&UserSubscription{}).Where("id = ?", reservation.SubscriptionId).Updates(updates).Error; err != nil {
		return 0, 0, err
	}
	return periodDelta, delta, nil
}

func paymentFreezePreviousUserStatusTx(tx *gorm.DB, user *User) (int, error) {
	if tx == nil || user == nil || user.Id <= 0 {
		return 0, ErrBillingAccountNotFound
	}
	if !user.PaymentFrozen {
		return user.Status, nil
	}

	var restorablePaymentDebts int64
	if err := tx.Model(&PaymentDebt{}).
		Where("user_id = ? AND status = ? AND freeze_applied = ? AND previous_user_status = ?", user.Id, PaymentDebtStatusOpen, true, common.UserStatusEnabled).
		Count(&restorablePaymentDebts).Error; err != nil {
		return 0, err
	}
	var restorableBillingShortfalls int64
	if err := tx.Model(&BillingReservation{}).
		Where("user_id = ? AND status = ? AND shortfall_freeze_applied = ? AND shortfall_previous_status = ?", user.Id, BillingReservationStatusReserved, true, common.UserStatusEnabled).
		Count(&restorableBillingShortfalls).Error; err != nil {
		return 0, err
	}
	if restorablePaymentDebts > 0 || restorableBillingShortfalls > 0 {
		return common.UserStatusEnabled, nil
	}
	return user.Status, nil
}

func hasOpenUserPaymentFreezeTx(tx *gorm.DB, userId int) (bool, error) {
	if tx == nil || userId <= 0 {
		return false, ErrBillingAccountNotFound
	}
	var paymentDebtCount int64
	if err := tx.Model(&PaymentDebt{}).
		Where("user_id = ? AND status = ?", userId, PaymentDebtStatusOpen).
		Count(&paymentDebtCount).Error; err != nil {
		return false, err
	}
	if paymentDebtCount > 0 {
		return true, nil
	}
	var billingShortfallCount int64
	if err := tx.Model(&BillingReservation{}).
		Where("user_id = ? AND status = ? AND shortfall_freeze_applied = ?", userId, BillingReservationStatusReserved, true).
		Count(&billingShortfallCount).Error; err != nil {
		return false, err
	}
	return billingShortfallCount > 0, nil
}

// HasOpenUserPaymentFreeze prevents manual status changes from bypassing either
// payment reversal debt or an unresolved API-usage settlement shortfall.
func HasOpenUserPaymentFreeze(userId int) (bool, error) {
	return hasOpenUserPaymentFreezeTx(DB, userId)
}

func EnableUserIfNoPaymentFreeze(userId int) error {
	if userId <= 0 {
		return ErrBillingAccountNotFound
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingAccountNotFound
			}
			return err
		}
		open, err := hasOpenUserPaymentFreezeTx(tx, userId)
		if err != nil {
			return err
		}
		if open {
			return ErrUserPaymentFreezeOpen
		}
		return tx.Model(&User{}).Where("id = ?", userId).Updates(map[string]interface{}{
			"status":         common.UserStatusEnabled,
			"payment_frozen": false,
		}).Error
	})
}

func releaseUserPaymentFreezeTx(tx *gorm.DB, userId int, previousStatus int) (bool, error) {
	open, err := hasOpenUserPaymentFreezeTx(tx, userId)
	if err != nil || open {
		return false, err
	}

	var user User
	if err := lockForUpdate(tx).Select("id", "status", "payment_frozen").Where("id = ?", userId).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrBillingAccountNotFound
		}
		return false, err
	}
	if !user.PaymentFrozen {
		return false, nil
	}

	updates := map[string]interface{}{"payment_frozen": false}
	if previousStatus == common.UserStatusEnabled && user.Status == common.UserStatusDisabled {
		updates["status"] = common.UserStatusEnabled
	}
	result := tx.Model(&User{}).Where("id = ? AND payment_frozen = ?", userId, true).Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// captureLegacySubscriptionReservationEpoch is deliberately conservative.
// Legacy tasks do not carry a reset version. Once this deployment has observed
// any reset, assigning such a task to the current period would risk mutating a
// newer period after an administrator reset that kept LastResetTime unchanged.
func captureLegacySubscriptionReservationEpoch(reservation *BillingReservation, task *Task, subscription *UserSubscription) {
	if reservation == nil || task == nil || subscription == nil {
		return
	}
	reservation.SubscriptionResetAt = -1
	reservation.SubscriptionResetVersion = -1
	if subscription.QuotaResetVersion == 0 && task.SubmitTime >= subscription.LastResetTime {
		reservation.SubscriptionResetAt = subscription.LastResetTime
		reservation.SubscriptionResetVersion = 0
	}
}

func adjustTokenQuotaById(id int, delta int) error {
	if id <= 0 {
		return ErrBillingAccountNotFound
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := lockForUpdate(tx).Select("id", "user_id", "unlimited_quota").Where("id = ?", id).First(&token).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingAccountNotFound
			}
			return err
		}
		_, _, err := applyTokenQuotaDeltaTx(tx, token.UserId, token.Id, delta, token.UnlimitedQuota)
		return err
	})
}

// creditTokenQuotaById preserves the legacy net-adjustment contract used by
// asynchronous task billing: used_quota may become negative when a refund is
// recorded without a matching token pre-consume row. Durable reservations
// prevent duplicate credits with their request-scoped state machine.
func creditTokenQuotaById(id int, quota int) error {
	if id <= 0 {
		return ErrBillingAccountNotFound
	}
	if quota <= 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := lockForUpdate(tx).
			Select("id", "unlimited_quota").
			Where("id = ?", id).
			First(&token).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingAccountNotFound
			}
			return err
		}
		query := tx.Model(&Token{}).
			Where("id = ?", id).
			Where("used_quota >= ?", math.MinInt32+quota)
		updates := map[string]interface{}{
			"used_quota":    gorm.Expr("used_quota - ?", quota),
			"accessed_time": common.GetTimestamp(),
		}
		if !token.UnlimitedQuota {
			query = query.Where("remain_quota <= ?", math.MaxInt32-quota)
			updates["remain_quota"] = gorm.Expr("remain_quota + ?", quota)
		}
		query = query.Updates(updates)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 {
			return ErrQuotaOverflow
		}
		return nil
	})
}

func invalidateBillingQuotaCaches(userId int, tokenKey string, userChanged bool, tokenChanged bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	if userChanged {
		if err := invalidateUserCache(userId); err != nil {
			common.SysLog("failed to invalidate user quota cache after billing transaction: " + err.Error())
		}
	}
	if tokenChanged && strings.TrimSpace(tokenKey) != "" {
		if err := cacheDeleteToken(tokenKey); err != nil {
			common.SysLog("failed to invalidate token quota cache after billing transaction: " + err.Error())
		}
	}
}
