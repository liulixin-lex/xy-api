package model

import (
	"errors"
	"fmt"
	"math"

	"gorm.io/gorm"
)

const (
	BillingUsageOutcomeNotRequired     = "not_required"
	BillingUsageOutcomeApplied         = "applied"
	BillingUsageOutcomeSaturated       = "saturated"
	BillingUsageOutcomeSkippedMissing  = "skipped_missing"
	BillingUsageOutcomeSkippedDeleted  = "skipped_deleted"
	BillingUsageOutcomeSkippedOverflow = "skipped_overflow"
)

type billingUsageProjectionSpec struct {
	OperationKey string
	UserID       int
	ChannelID    int
	QuotaDelta   int
	RequestDelta int
}

type billingUsageProjectionResult struct {
	UserOutcome    string
	ChannelOutcome string
}

// applyBillingUsageProjectionTx updates derived usage only. The caller must
// persist its projection stage in the same transaction, making a committed
// stage transition the exactly-once receipt for these mutations.
func applyBillingUsageProjectionTx(tx *gorm.DB, spec billingUsageProjectionSpec) (billingUsageProjectionResult, error) {
	result := billingUsageProjectionResult{
		UserOutcome:    BillingUsageOutcomeNotRequired,
		ChannelOutcome: BillingUsageOutcomeNotRequired,
	}
	if tx == nil || spec.OperationKey == "" || spec.UserID <= 0 || spec.QuotaDelta < 0 ||
		(spec.RequestDelta != 0 && spec.RequestDelta != 1) {
		return result, errors.New("billing usage projection is invalid")
	}

	if spec.QuotaDelta > 0 || spec.RequestDelta > 0 {
		var user User
		err := lockForUpdate(tx.Unscoped()).Where("id = ?", spec.UserID).First(&user).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			result.UserOutcome = BillingUsageOutcomeSkippedMissing
		case err != nil:
			return result, err
		case user.DeletedAt.Valid:
			result.UserOutcome = BillingUsageOutcomeSkippedDeleted
		default:
			saturated := false
			usedQuotaBase := int64(user.UsedQuota)
			if usedQuotaBase < 0 {
				usedQuotaBase = 0
				saturated = true
			} else if usedQuotaBase > math.MaxInt32 {
				usedQuotaBase = math.MaxInt32
				saturated = true
			}
			newUsedQuota := usedQuotaBase
			if int64(spec.QuotaDelta) > math.MaxInt32-usedQuotaBase {
				newUsedQuota = math.MaxInt32
				saturated = true
			} else {
				newUsedQuota += int64(spec.QuotaDelta)
			}

			requestCountBase := int64(user.RequestCount)
			if requestCountBase < 0 {
				requestCountBase = 0
				saturated = true
			} else if requestCountBase > math.MaxInt32 {
				requestCountBase = math.MaxInt32
				saturated = true
			}
			newRequestCount := requestCountBase
			if int64(spec.RequestDelta) > math.MaxInt32-requestCountBase {
				newRequestCount = math.MaxInt32
				saturated = true
			} else {
				newRequestCount += int64(spec.RequestDelta)
			}
			if err := tx.Unscoped().Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{
				"used_quota":    int(newUsedQuota),
				"request_count": int(newRequestCount),
			}).Error; err != nil {
				return result, err
			}
			result.UserOutcome = BillingUsageOutcomeApplied
			if saturated {
				result.UserOutcome = BillingUsageOutcomeSaturated
			}
		}
	}

	if spec.QuotaDelta == 0 || spec.ChannelID <= 0 {
		return result, nil
	}
	var channel Channel
	err := lockForUpdate(tx).Where("id = ?", spec.ChannelID).First(&channel).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		result.ChannelOutcome = BillingUsageOutcomeSkippedMissing
	case err != nil:
		return result, err
	default:
		usedQuotaBase := channel.UsedQuota
		saturated := false
		if usedQuotaBase < 0 {
			usedQuotaBase = 0
			saturated = true
		}
		if usedQuotaBase > math.MaxInt64-int64(spec.QuotaDelta) {
			result.ChannelOutcome = BillingUsageOutcomeSkippedOverflow
			return result, nil
		}
		newUsedQuota := usedQuotaBase + int64(spec.QuotaDelta)
		if err := tx.Model(&Channel{}).Where("id = ?", channel.Id).Update("used_quota", newUsedQuota).Error; err != nil {
			return result, err
		}
		result.ChannelOutcome = BillingUsageOutcomeApplied
		if saturated {
			result.ChannelOutcome = BillingUsageOutcomeSaturated
		}
	}
	return result, nil
}

func billingUsageProjectionWarning(operationKey string, result billingUsageProjectionResult) string {
	if result.UserOutcome == BillingUsageOutcomeApplied &&
		(result.ChannelOutcome == BillingUsageOutcomeApplied || result.ChannelOutcome == BillingUsageOutcomeNotRequired) {
		return ""
	}
	return fmt.Sprintf("billing usage projection completed with audit outcome: operation=%s user=%s channel=%s",
		operationKey, result.UserOutcome, result.ChannelOutcome)
}
