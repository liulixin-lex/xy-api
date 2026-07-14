package model

import (
	"errors"
	"math"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	BillingQuotaDataOutcomeNotRequired    = "not_required"
	BillingQuotaDataOutcomeApplied        = "applied"
	BillingQuotaDataOutcomeAppliedSplit   = "applied_split"
	BillingQuotaDataOutcomeRepairedSplit  = "repaired_split"
	BillingQuotaDataOutcomeSkippedInvalid = "skipped_invalid"

	billingQuotaDataDimensionMaxBytes = 64
)

type billingQuotaDataProjectionSpec struct {
	Required  bool
	UserID    int
	Username  string
	ModelName string
	CreatedAt int64
	Quota     int
	TokenUsed int
	UseGroup  string
	TokenID   int
	ChannelID int
	NodeName  string
}

// applyBillingQuotaDataProjectionTx bypasses the legacy in-memory aggregation
// cache so the durable stats receipt and the export mutation commit atomically.
// If one aggregate row reaches an int32 storage boundary, a second row keeps
// the exported totals exact instead of wrapping or discarding the observation.
func applyBillingQuotaDataProjectionTx(tx *gorm.DB, spec billingQuotaDataProjectionSpec) (string, error) {
	if !spec.Required {
		return BillingQuotaDataOutcomeNotRequired, nil
	}
	if tx == nil || spec.UserID <= 0 || spec.CreatedAt <= 0 || spec.Quota < 0 ||
		spec.Quota > math.MaxInt32 || spec.TokenUsed < 0 || spec.TokenUsed > math.MaxInt32 ||
		spec.TokenID < 0 || spec.ChannelID <= 0 ||
		!validBillingQuotaDataDimension(spec.Username) || !validBillingQuotaDataDimension(spec.ModelName) ||
		!validBillingQuotaDataDimension(spec.UseGroup) || !validBillingQuotaDataDimension(spec.NodeName) {
		return BillingQuotaDataOutcomeSkippedInvalid, nil
	}

	createdAt := spec.CreatedAt - spec.CreatedAt%3600
	query := lockForUpdate(tx).Where(
		"user_id = ? AND username = ? AND model_name = ? AND created_at = ? AND use_group = ? AND token_id = ? AND channel_id = ? AND node_name = ?",
		spec.UserID, spec.Username, spec.ModelName, createdAt, spec.UseGroup, spec.TokenID, spec.ChannelID, spec.NodeName,
	).Order("id desc")
	var current QuotaData
	err := query.First(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	newRow := errors.Is(err, gorm.ErrRecordNotFound)
	repairedCurrent := false
	if !newRow {
		countBase := int64(current.Count)
		quotaBase := int64(current.Quota)
		tokenUsedBase := int64(current.TokenUsed)
		if countBase < 0 || countBase > math.MaxInt32 || quotaBase < 0 || quotaBase > math.MaxInt32 ||
			tokenUsedBase < 0 || tokenUsedBase > math.MaxInt32 {
			if countBase < 0 {
				countBase = 0
			} else if countBase > math.MaxInt32 {
				countBase = math.MaxInt32
			}
			if quotaBase < 0 {
				quotaBase = 0
			} else if quotaBase > math.MaxInt32 {
				quotaBase = math.MaxInt32
			}
			if tokenUsedBase < 0 {
				tokenUsedBase = 0
			} else if tokenUsedBase > math.MaxInt32 {
				tokenUsedBase = math.MaxInt32
			}
			updated := tx.Model(&QuotaData{}).Where("id = ?", current.Id).Updates(map[string]any{
				"count": int(countBase), "quota": int(quotaBase), "token_used": int(tokenUsedBase),
			})
			if updated.Error != nil {
				return "", updated.Error
			}
			if updated.RowsAffected != 1 {
				return "", errors.New("billing quota data projection repair lost")
			}
			repairedCurrent = true
			newRow = true
		} else if countBase >= math.MaxInt32 || int64(spec.Quota) > math.MaxInt32-quotaBase ||
			int64(spec.TokenUsed) > math.MaxInt32-tokenUsedBase {
			newRow = true
		} else {
			newCount := countBase + 1
			newQuota := quotaBase + int64(spec.Quota)
			newTokenUsed := tokenUsedBase + int64(spec.TokenUsed)
			updated := tx.Model(&QuotaData{}).Where("id = ?", current.Id).Updates(map[string]any{
				"count":      int(newCount),
				"quota":      int(newQuota),
				"token_used": int(newTokenUsed),
			})
			if updated.Error != nil {
				return "", updated.Error
			}
			if updated.RowsAffected != 1 {
				return "", errors.New("billing quota data projection update lost")
			}
			return BillingQuotaDataOutcomeApplied, nil
		}
	}

	row := &QuotaData{
		UserID:    spec.UserID,
		Username:  spec.Username,
		ModelName: spec.ModelName,
		CreatedAt: createdAt,
		UseGroup:  spec.UseGroup,
		TokenID:   spec.TokenID,
		ChannelID: spec.ChannelID,
		NodeName:  spec.NodeName,
		TokenUsed: spec.TokenUsed,
		Count:     1,
		Quota:     spec.Quota,
	}
	if err := tx.Create(row).Error; err != nil {
		return "", err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BillingQuotaDataOutcomeApplied, nil
	}
	if repairedCurrent {
		return BillingQuotaDataOutcomeRepairedSplit, nil
	}
	return BillingQuotaDataOutcomeAppliedSplit, nil
}

func validBillingQuotaDataDimension(value string) bool {
	return len(value) <= billingQuotaDataDimensionMaxBytes && utf8.ValidString(value)
}
