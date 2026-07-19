package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	BillingResourceMidjourneySubmission = "midjourney_submit"
	BillingResourceMidjourneyTask       = "midjourney_task"
)

func midjourneyBillingResourceId(id int) string {
	return strconv.Itoa(id)
}

func legacyMidjourneyBillingRequestId(id int) string {
	return fmt.Sprintf("legacy-mj-%d", id)
}

func validateMidjourneyBillingLink(task *Midjourney, reservation *BillingReservation, requestId string) error {
	if task == nil || reservation == nil || task.Id <= 0 ||
		task.UserId != reservation.UserId ||
		task.PrivateData.BillingRequestId != requestId ||
		reservation.ResourceType != BillingResourceMidjourneyTask ||
		reservation.ResourceId != midjourneyBillingResourceId(task.Id) {
		return ErrBillingReservationConflict
	}
	return nil
}

func prepareMidjourneyBillingReservationSettlement(taskId int, requestId string, actualQuota int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var task Midjourney
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != MidjourneyStatusSuccess {
			return ErrBillingReservationConflict
		}
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateMidjourneyBillingLink(&task, &reservation, requestId); err != nil {
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

// ClaimMidjourneyBillingReservation marks the request before dispatching it
// upstream. Only the first caller may dispatch; a concurrent/replayed request
// with the same request id observes claimed=false and must not create a second
// provider job against the same debit.
func ClaimMidjourneyBillingReservation(requestId string) (bool, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return false, errors.New("invalid midjourney billing claim")
	}
	claimed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if reservation.Status != BillingReservationStatusReserved {
			return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
		}
		if reservation.ResourceType != "" {
			if reservation.ResourceType == BillingResourceMidjourneySubmission && reservation.ResourceId == requestId {
				return nil
			}
			return ErrBillingReservationConflict
		}
		reservation.ResourceType = BillingResourceMidjourneySubmission
		reservation.ResourceId = requestId
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
		if err := tx.Create(&QuotaLedgerEntry{
			RequestId:      reservation.RequestId,
			Phase:          QuotaLedgerPhaseBind,
			Revision:       reservation.Version,
			UserId:         reservation.UserId,
			TokenId:        reservation.TokenId,
			FundingSource:  reservation.FundingSource,
			SubscriptionId: reservation.SubscriptionId,
			Note:           "resource=midjourney_submission:" + requestId,
		}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

// RefundUnclaimedMidjourneyBillingReservation reverses a pre-consume only when
// the same transaction proves that no dispatcher has claimed the reservation.
// This closes the pre-consume/claim failure gap without refunding a concurrent
// request that may already be creating an upstream job.
func RefundUnclaimedMidjourneyBillingReservation(requestId string, tokenKey string) (bool, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return false, errors.New("invalid midjourney billing refund")
	}
	var reservation BillingReservation
	userCacheChanged := false
	tokenCacheChanged := false
	applied := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if reservation.ResourceType != "" || reservation.ResourceId != "" {
			return nil
		}
		var err error
		applied, userCacheChanged, tokenCacheChanged, err = refundBillingReservationTx(tx, &reservation, false)
		return err
	})
	if err != nil {
		return false, err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return applied, nil
}

// CreateMidjourneyWithBillingReservation persists an accepted upstream task and
// binds it to its pre-consumed quota in one transaction. A bound reservation is
// deliberately kept open until a terminal polling/callback result is observed.
func CreateMidjourneyWithBillingReservation(task *Midjourney, requestId string, targetQuota int, tokenKey string) error {
	requestId = strings.TrimSpace(requestId)
	if task == nil || task.UserId <= 0 || requestId == "" || targetQuota < 0 || int64(targetQuota) > int64(math.MaxInt32) {
		return errors.New("invalid midjourney billing binding")
	}
	task.Quota = targetQuota
	task.PrivateData.BillingRequestId = requestId
	if task.Status == MidjourneyStatusSuccess {
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
		if reservation.ResourceType == BillingResourceMidjourneyTask {
			if reservation.ResourceId == "" || reservation.ReservedQuota != targetQuota {
				return ErrBillingReservationConflict
			}
			existingId, err := strconv.Atoi(reservation.ResourceId)
			if err != nil || existingId <= 0 || (task.Id > 0 && task.Id != existingId) {
				return ErrBillingReservationConflict
			}
			var existing Midjourney
			if err := lockForUpdate(tx).Where("id = ?", existingId).First(&existing).Error; err != nil {
				return err
			}
			if err := validateMidjourneyBillingLink(&existing, &reservation, requestId); err != nil {
				return err
			}
			task.Id = existing.Id
			return nil
		}
		if reservation.ResourceType != "" &&
			(reservation.ResourceType != BillingResourceMidjourneySubmission || reservation.ResourceId != requestId) {
			return ErrBillingReservationConflict
		}

		var err error
		userCacheChanged, tokenCacheChanged, err = adjustReservedBillingTx(tx, &reservation, targetQuota, QuotaLedgerPhaseReserve, false)
		if err != nil {
			return err
		}
		if err := tx.Create(task).Error; err != nil {
			return err
		}

		reservation.ResourceType = BillingResourceMidjourneyTask
		reservation.ResourceId = midjourneyBillingResourceId(task.Id)
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
			Note:           "resource=midjourney_task:" + reservation.ResourceId,
		}).Error
	})
	if err != nil {
		return err
	}
	invalidateBillingQuotaCaches(reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	return nil
}

// AdoptLegacyMidjourneyBillingReservation creates an audit baseline without
// changing balances for a non-terminal row charged by the historical direct
// Midjourney path. This makes later settlement/refund idempotent and crash-safe.
func AdoptLegacyMidjourneyBillingReservation(taskId int) (*BillingReservation, error) {
	if taskId <= 0 {
		return nil, ErrTaskNotPersisted
	}
	var reservation BillingReservation
	var task Midjourney
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.IsTerminal() || task.Progress == "100%" {
			return ErrBillingReservationFinalized
		}
		if task.Quota < 0 || int64(task.Quota) > int64(math.MaxInt32) {
			return ErrBillingReservationConflict
		}
		if requestId := strings.TrimSpace(task.PrivateData.BillingRequestId); requestId != "" {
			if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&reservation).Error; err != nil {
				return err
			}
			return validateMidjourneyBillingLink(&task, &reservation, requestId)
		}

		requestId := legacyMidjourneyBillingRequestId(task.Id)
		query := lockForUpdate(tx).Where("request_id = ?", requestId).Limit(1).Find(&reservation)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if reservation.UserId != task.UserId || reservation.ResourceType != BillingResourceMidjourneyTask || reservation.ResourceId != midjourneyBillingResourceId(task.Id) {
				return ErrBillingReservationConflict
			}
			task.PrivateData.BillingRequestId = requestId
			task.PrivateData.BillingSource = reservation.FundingSource
			task.PrivateData.SubscriptionId = reservation.SubscriptionId
			task.PrivateData.TokenId = reservation.TokenId
			return tx.Model(&Midjourney{}).Where("id = ?", task.Id).Update("private_data", task.PrivateData).Error
		}

		reservation = BillingReservation{
			RequestId:     requestId,
			UserId:        task.UserId,
			TokenId:       task.PrivateData.TokenId,
			FundingSource: BillingFundingWallet,
			ResourceType:  BillingResourceMidjourneyTask,
			ResourceId:    midjourneyBillingResourceId(task.Id),
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
			Note:          fmt.Sprintf("legacy midjourney charged baseline adopted without mutation; quota=%d", task.Quota),
		}
		if task.PrivateData.BillingSource == BillingFundingSubscription && task.PrivateData.SubscriptionId > 0 {
			var subscription UserSubscription
			if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", task.PrivateData.SubscriptionId, task.UserId).First(&subscription).Error; err != nil {
				return err
			}
			reservation.FundingSource = BillingFundingSubscription
			reservation.SubscriptionId = subscription.Id
			submitUnix := task.SubmitTime
			if submitUnix > 100000000000 {
				submitUnix /= 1000
			}
			if submitUnix >= subscription.LastResetTime {
				reservation.SubscriptionResetAt = subscription.LastResetTime
			} else {
				reservation.SubscriptionResetAt = -1
			}
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
		} else {
			ledger.Note += "; legacy token id unavailable"
		}
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		if err := tx.Create(ledger).Error; err != nil {
			return err
		}
		task.PrivateData.BillingRequestId = requestId
		task.PrivateData.BillingSource = reservation.FundingSource
		task.PrivateData.SubscriptionId = reservation.SubscriptionId
		return tx.Model(&Midjourney{}).Where("id = ?", task.Id).Update("private_data", task.PrivateData).Error
	})
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

// SettleMidjourneyBillingReservation closes a bound successful task exactly
// once and applies any final quota delta atomically with the task quota update.
func SettleMidjourneyBillingReservation(taskId int, requestId string, actualQuota int, tokenKey string) (*BillingFinalizationResult, error) {
	requestId = strings.TrimSpace(requestId)
	if taskId <= 0 || requestId == "" || actualQuota < 0 || int64(actualQuota) > int64(math.MaxInt32) {
		return nil, errors.New("invalid midjourney billing settlement")
	}
	if err := prepareMidjourneyBillingReservationSettlement(taskId, requestId, actualQuota); err != nil {
		return nil, err
	}

	result := &BillingFinalizationResult{}
	var task Midjourney
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != MidjourneyStatusSuccess {
			return ErrBillingReservationConflict
		}
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&result.Reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateMidjourneyBillingLink(&task, &result.Reservation, requestId); err != nil {
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
		return tx.Model(&Midjourney{}).Where("id = ?", task.Id).Updates(map[string]interface{}{
			"quota":        task.Quota,
			"private_data": task.PrivateData,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(result.Reservation.UserId, tokenKey, result.UserCacheChanged, result.TokenCacheChanged)
	return result, nil
}

// RefundMidjourneyBillingReservation verifies provider failure before reversing
// funding and token quota. Repeated polling/callback events are no-ops.
func RefundMidjourneyBillingReservation(taskId int, requestId string, tokenKey string) (*BillingFinalizationResult, error) {
	requestId = strings.TrimSpace(requestId)
	if taskId <= 0 || requestId == "" {
		return nil, errors.New("invalid midjourney billing refund")
	}
	result := &BillingFinalizationResult{}
	var task Midjourney
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", taskId).First(&task).Error; err != nil {
			return err
		}
		if task.Status != MidjourneyStatusFailure {
			return ErrBillingReservationConflict
		}
		if err := lockForUpdate(tx).Where("request_id = ?", requestId).First(&result.Reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}
		if err := validateMidjourneyBillingLink(&task, &result.Reservation, requestId); err != nil {
			return err
		}
		var err error
		result.Applied, result.UserCacheChanged, result.TokenCacheChanged, err = refundBillingReservationTx(tx, &result.Reservation, true)
		if err != nil {
			return err
		}
		if _, hasIntent := task.PrivateData.BillingTargetQuotaIntent(); hasIntent {
			task.PrivateData.ClearBillingTargetQuota()
			return tx.Model(&Midjourney{}).Where("id = ?", task.Id).Update("private_data", task.PrivateData).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingQuotaCaches(result.Reservation.UserId, tokenKey, result.UserCacheChanged, result.TokenCacheChanged)
	return result, nil
}
