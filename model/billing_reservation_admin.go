package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	QuotaLedgerPhaseAdminResolution = "admin_resolution"

	BillingReservationAdminSettle = "settle"
	BillingReservationAdminRefund = "refund"
)

var (
	ErrBillingReservationReviewRequired  = errors.New("billing reservation must be marked for administrator review")
	ErrBillingReservationVersionConflict = errors.New("billing reservation version has changed")
	ErrBillingAdminResolutionConflict    = errors.New("billing reservation admin resolution conflicts with an existing action")
	ErrBillingAdminResolutionInvalid     = errors.New("invalid billing reservation admin resolution")
)

// BillingReservationAdminResolution is an append-only record of the exact
// administrator instruction that closed a reviewed reservation. The separate
// table preserves reasons up to the API limit without overloading the compact
// QuotaLedgerEntry.Note column.
type BillingReservationAdminResolution struct {
	Id              int64  `json:"id" gorm:"primaryKey"`
	RequestId       string `json:"request_id" gorm:"type:varchar(64);index;uniqueIndex:idx_billing_admin_request_expected,priority:1;uniqueIndex:idx_billing_admin_request_revision,priority:1"`
	Revision        int    `json:"revision" gorm:"uniqueIndex:idx_billing_admin_request_revision,priority:2"`
	ExpectedVersion int    `json:"expected_version" gorm:"uniqueIndex:idx_billing_admin_request_expected,priority:2"`
	AdminId         int    `json:"admin_id" gorm:"index"`
	ActorIp         string `json:"actor_ip" gorm:"type:varchar(64)"`
	Resolution      string `json:"resolution" gorm:"type:varchar(16);index"`
	ActualQuota     *int   `json:"actual_quota,omitempty"`
	Reason          string `json:"reason" gorm:"type:varchar(512)"`
	CreatedAt       int64  `json:"created_at" gorm:"index"`
}

func (r *BillingReservationAdminResolution) BeforeCreate(_ *gorm.DB) error {
	r.CreatedAt = common.GetTimestamp()
	return nil
}

func (*BillingReservationAdminResolution) BeforeUpdate(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

func (*BillingReservationAdminResolution) BeforeDelete(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

type BillingReservationAdminFilters struct {
	RequestId    string
	UserId       int
	ResourceType string
	StaleBefore  int64
	Offset       int
	Limit        int
}

type BillingReservationAdminPage struct {
	Reservations      []BillingReservation `json:"reservations"`
	Total             int64                `json:"total"`
	StaleBefore       int64                `json:"stale_before"`
	StaleAfterSeconds int64                `json:"stale_after_seconds"`
}

type BillingReservationAdminDetail struct {
	Reservation      BillingReservation                  `json:"reservation"`
	Ledger           []QuotaLedgerEntry                  `json:"ledger"`
	AdminResolutions []BillingReservationAdminResolution `json:"admin_resolutions"`
}

type BillingReservationAdminResolutionInput struct {
	RequestId       string
	ExpectedVersion int
	AdminId         int
	ActorIp         string
	Resolution      string
	ActualQuota     *int64
	Reason          string
}

type BillingReservationAdminResolutionResult struct {
	Reservation BillingReservation                `json:"reservation"`
	Resolution  BillingReservationAdminResolution `json:"resolution"`
	Applied     bool                              `json:"applied"`
}

func validateBillingReservationAdminFilters(filters *BillingReservationAdminFilters) error {
	if filters == nil {
		return errors.New("billing reservation filters are required")
	}
	filters.RequestId = strings.TrimSpace(filters.RequestId)
	filters.ResourceType = strings.TrimSpace(filters.ResourceType)
	if len(filters.RequestId) > 64 || len(filters.ResourceType) > 32 || filters.UserId < 0 || filters.StaleBefore <= 0 {
		return errors.New("invalid billing reservation filters")
	}
	if filters.Offset < 0 {
		filters.Offset = 0
	}
	if filters.Limit <= 0 || filters.Limit > 100 {
		filters.Limit = 20
	}
	return nil
}

func billingReservationAdminAttentionQuery(db *gorm.DB, staleBefore int64) *gorm.DB {
	return db.Model(&BillingReservation{}).
		Where("status = ?", BillingReservationStatusReserved).
		Where(
			"(last_reconciled_at > ? OR reconcile_note <> ? OR settlement_pending = ? OR updated_at <= ?)",
			0, "", true, staleBefore,
		)
}

func ListBillingReservationsForAdmin(filters BillingReservationAdminFilters) (*BillingReservationAdminPage, error) {
	if err := validateBillingReservationAdminFilters(&filters); err != nil {
		return nil, err
	}
	query := billingReservationAdminAttentionQuery(DB, filters.StaleBefore)
	if filters.RequestId != "" {
		query = query.Where("request_id = ?", filters.RequestId)
	}
	if filters.UserId > 0 {
		query = query.Where("user_id = ?", filters.UserId)
	}
	if filters.ResourceType != "" {
		query = query.Where("resource_type = ?", filters.ResourceType)
	}

	page := &BillingReservationAdminPage{StaleBefore: filters.StaleBefore}
	if err := query.Count(&page.Total).Error; err != nil {
		return nil, err
	}
	if page.Total == 0 {
		page.Reservations = []BillingReservation{}
		return page, nil
	}
	if err := query.
		Order("settlement_pending DESC, updated_at ASC, id ASC").
		Offset(filters.Offset).
		Limit(filters.Limit).
		Find(&page.Reservations).Error; err != nil {
		return nil, err
	}
	return page, nil
}

func GetBillingReservationAdminDetail(requestId string) (*BillingReservationAdminDetail, error) {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" || len(requestId) > 64 {
		return nil, ErrBillingReservationNotFound
	}
	detail := &BillingReservationAdminDetail{}
	if err := DB.Where("request_id = ?", requestId).First(&detail.Reservation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillingReservationNotFound
		}
		return nil, err
	}
	if err := DB.Where("request_id = ?", requestId).
		Order("revision ASC, id ASC").
		Find(&detail.Ledger).Error; err != nil {
		return nil, err
	}
	if err := DB.Where("request_id = ?", requestId).
		Order("revision ASC, id ASC").
		Find(&detail.AdminResolutions).Error; err != nil {
		return nil, err
	}
	return detail, nil
}

func validateBillingReservationAdminResolution(input *BillingReservationAdminResolutionInput) error {
	if input == nil {
		return fmt.Errorf("%w: input is required", ErrBillingAdminResolutionInvalid)
	}
	input.RequestId = strings.TrimSpace(input.RequestId)
	input.Resolution = strings.TrimSpace(input.Resolution)
	input.Reason = strings.TrimSpace(input.Reason)
	input.ActorIp = strings.TrimSpace(input.ActorIp)
	reasonLength := utf8.RuneCountInString(input.Reason)
	if input.RequestId == "" || len(input.RequestId) > 64 || input.ExpectedVersion <= 0 || input.AdminId <= 0 ||
		input.ActorIp == "" || len(input.ActorIp) > 64 || reasonLength < 8 || reasonLength > 512 {
		return ErrBillingAdminResolutionInvalid
	}
	switch input.Resolution {
	case BillingReservationAdminSettle:
		if input.ActualQuota == nil || *input.ActualQuota < 0 || *input.ActualQuota > math.MaxInt32 {
			return fmt.Errorf("%w: settlement requires an explicit int32 actual quota", ErrBillingAdminResolutionInvalid)
		}
	case BillingReservationAdminRefund:
		if input.ActualQuota != nil {
			return fmt.Errorf("%w: refund must not include an actual quota", ErrBillingAdminResolutionInvalid)
		}
	default:
		return ErrBillingAdminResolutionInvalid
	}
	return nil
}

func billingAdminResolutionMatches(existing BillingReservationAdminResolution, input BillingReservationAdminResolutionInput) bool {
	if existing.AdminId != input.AdminId || existing.Resolution != input.Resolution || existing.Reason != input.Reason {
		return false
	}
	if existing.ActualQuota == nil || input.ActualQuota == nil {
		return existing.ActualQuota == nil && input.ActualQuota == nil
	}
	return int64(*existing.ActualQuota) == *input.ActualQuota
}

func truncateBillingAdminLedgerNote(value string) string {
	const maxBytes = 255
	if len(value) <= maxBytes {
		return value
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func ResolveBillingReservationByAdmin(input BillingReservationAdminResolutionInput) (*BillingReservationAdminResolutionResult, error) {
	if err := validateBillingReservationAdminResolution(&input); err != nil {
		return nil, err
	}

	result := &BillingReservationAdminResolutionResult{}
	userCacheChanged := false
	tokenCacheChanged := false
	tokenKey := ""
	err := DB.Transaction(func(tx *gorm.DB) error {
		var reservation BillingReservation
		if err := lockForUpdate(tx).Where("request_id = ?", input.RequestId).First(&reservation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingReservationNotFound
			}
			return err
		}

		var existing BillingReservationAdminResolution
		existingQuery := tx.Where("request_id = ? AND expected_version = ?", input.RequestId, input.ExpectedVersion).
			Limit(1).
			Find(&existing)
		if existingQuery.Error != nil {
			return existingQuery.Error
		}
		if existingQuery.RowsAffected > 0 {
			if !billingAdminResolutionMatches(existing, input) {
				return ErrBillingAdminResolutionConflict
			}
			result.Reservation = reservation
			result.Resolution = existing
			result.Applied = false
			return nil
		}

		if reservation.Version != input.ExpectedVersion {
			return ErrBillingReservationVersionConflict
		}
		if reservation.Status != BillingReservationStatusReserved {
			return fmt.Errorf("%w: status=%s", ErrBillingReservationFinalized, reservation.Status)
		}
		if reservation.LastReconciledAt <= 0 || strings.TrimSpace(reservation.ReconcileNote) == "" {
			return ErrBillingReservationReviewRequired
		}
		if reservation.TokenId > 0 {
			var token Token
			query := tx.Select("key").
				Where("id = ? AND user_id = ?", reservation.TokenId, reservation.UserId).
				Limit(1).
				Find(&token)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected > 0 {
				tokenKey = token.Key
			}
		}

		var applied bool
		var finalizeErr error
		switch input.Resolution {
		case BillingReservationAdminSettle:
			applied, userCacheChanged, tokenCacheChanged, finalizeErr = settleBillingReservationTx(
				tx, &reservation, int(*input.ActualQuota), true,
			)
		case BillingReservationAdminRefund:
			applied, userCacheChanged, tokenCacheChanged, finalizeErr = refundBillingReservationTx(tx, &reservation, true)
		}
		if finalizeErr != nil {
			return finalizeErr
		}
		if !applied {
			return ErrBillingAdminResolutionConflict
		}

		resolution := BillingReservationAdminResolution{
			RequestId:       reservation.RequestId,
			Revision:        reservation.Version + 1,
			ExpectedVersion: input.ExpectedVersion,
			AdminId:         input.AdminId,
			ActorIp:         input.ActorIp,
			Resolution:      input.Resolution,
			Reason:          input.Reason,
		}
		if input.ActualQuota != nil {
			actualQuota := int(*input.ActualQuota)
			resolution.ActualQuota = &actualQuota
		}
		if err := tx.Create(&resolution).Error; err != nil {
			return err
		}

		ledgerNote := truncateBillingAdminLedgerNote(fmt.Sprintf(
			"admin_id=%d resolution=%s reason=%s", input.AdminId, input.Resolution, input.Reason,
		))
		if err := tx.Create(&QuotaLedgerEntry{
			RequestId:      reservation.RequestId,
			Phase:          QuotaLedgerPhaseAdminResolution,
			Revision:       resolution.Revision,
			UserId:         reservation.UserId,
			TokenId:        reservation.TokenId,
			FundingSource:  reservation.FundingSource,
			SubscriptionId: reservation.SubscriptionId,
			Note:           ledgerNote,
		}).Error; err != nil {
			return err
		}

		now := common.GetTimestamp()
		reconcileNote := truncateBillingAdminLedgerNote(fmt.Sprintf(
			"admin %s by user %d: %s", input.Resolution, input.AdminId, input.Reason,
		))
		update := tx.Model(&BillingReservation{}).
			Where("id = ? AND version = ?", reservation.Id, reservation.Version).
			Updates(map[string]interface{}{
				"version":            resolution.Revision,
				"last_reconciled_at": now,
				"reconcile_note":     reconcileNote,
				"updated_at":         now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrBillingReservationVersionConflict
		}

		reservation.Version = resolution.Revision
		reservation.LastReconciledAt = now
		reservation.ReconcileNote = reconcileNote
		reservation.UpdatedAt = now
		result.Reservation = reservation
		result.Resolution = resolution
		result.Applied = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Applied {
		invalidateBillingQuotaCaches(result.Reservation.UserId, tokenKey, userCacheChanged, tokenCacheChanged)
	}
	return result, nil
}
