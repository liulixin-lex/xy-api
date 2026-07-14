package model

import (
	"context"
)

type billingProjectionReference struct {
	ProjectionID int64
	Kind         string
	ReferenceID  int64
	OperationKey string
}

// billingProjectionReferencesProtected checks source identities and external
// sink receipts in bounded set queries. Unknown or unavailable evidence stays
// protected. A projection becomes eligible only after its authoritative source
// receipt is gone and no external sink receipt or open conflict still needs it.
func billingProjectionReferencesProtected(
	ctx context.Context,
	references []billingProjectionReference,
) (map[int64]bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	protected := make(map[int64]bool, len(references))
	byKind := make(map[string][]billingProjectionReference, 3)
	for _, reference := range references {
		protected[reference.ProjectionID] = true
		if reference.ProjectionID > 0 && reference.ReferenceID > 0 && reference.OperationKey != "" {
			byKind[reference.Kind] = append(byKind[reference.Kind], reference)
		}
	}
	if DB == nil || len(references) == 0 {
		return protected, nil
	}
	openConflictKeys := make(map[string]struct{})
	if DB.Migrator().HasTable(&BillingLogSinkConflictAudit{}) {
		operationKeys := make([]string, 0, len(references))
		for _, reference := range references {
			if reference.OperationKey != "" {
				operationKeys = append(operationKeys, reference.OperationKey)
			}
		}
		if len(operationKeys) > 0 {
			var keys []string
			if err := DB.WithContext(ctx).Model(&BillingLogSinkConflictAudit{}).
				Where("state = ? AND operation_key IN ?", BillingLogSinkConflictStateOpen, operationKeys).
				Pluck("operation_key", &keys).Error; err != nil {
				return protected, err
			}
			for _, operationKey := range keys {
				openConflictKeys[operationKey] = struct{}{}
			}
		}
	}

	sourceReleased := make([]billingProjectionReference, 0, len(references))
	if accepted := byKind[BillingStatsProjectionKindAccepted]; len(accepted) > 0 &&
		DB.Migrator().HasTable(&AsyncBillingReservation{}) {
		ids := make([]int64, 0, len(accepted))
		for _, reference := range accepted {
			ids = append(ids, reference.ReferenceID)
		}
		var reservations []AsyncBillingReservation
		if err := DB.WithContext(ctx).Select("id").Where("id IN ?", ids).Find(&reservations).Error; err != nil {
			return protected, err
		}
		byID := make(map[int64]struct{}, len(reservations))
		for _, reservation := range reservations {
			byID[reservation.ID] = struct{}{}
		}
		for _, reference := range accepted {
			if _, exists := byID[reference.ReferenceID]; !exists {
				sourceReleased = append(sourceReleased, reference)
			}
		}
	}

	if taskTerminal := byKind[BillingStatsProjectionKindTaskTerminal]; len(taskTerminal) > 0 &&
		DB.Migrator().HasTable(&TaskBillingOperation{}) {
		ids := make([]int64, 0, len(taskTerminal))
		for _, reference := range taskTerminal {
			ids = append(ids, reference.ReferenceID)
		}
		var operations []TaskBillingOperation
		if err := DB.WithContext(ctx).Select("id").
			Where("id IN ?", ids).Find(&operations).Error; err != nil {
			return protected, err
		}
		byID := make(map[int64]struct{}, len(operations))
		for _, operation := range operations {
			byID[operation.ID] = struct{}{}
		}
		for _, reference := range taskTerminal {
			if _, exists := byID[reference.ReferenceID]; !exists {
				sourceReleased = append(sourceReleased, reference)
			}
		}
	}

	if midjourneyTerminal := byKind[BillingStatsProjectionKindMidjourneyTerminal]; len(midjourneyTerminal) > 0 &&
		DB.Migrator().HasTable(&MidjourneyBillingOperation{}) {
		ids := make([]int64, 0, len(midjourneyTerminal))
		for _, reference := range midjourneyTerminal {
			ids = append(ids, reference.ReferenceID)
		}
		var operations []MidjourneyBillingOperation
		if err := DB.WithContext(ctx).Select("id").
			Where("id IN ?", ids).Find(&operations).Error; err != nil {
			return protected, err
		}
		byID := make(map[int64]struct{}, len(operations))
		for _, operation := range operations {
			byID[operation.ID] = struct{}{}
		}
		for _, reference := range midjourneyTerminal {
			if _, exists := byID[reference.ReferenceID]; !exists {
				sourceReleased = append(sourceReleased, reference)
			}
		}
	}

	if len(sourceReleased) == 0 || LOG_DB == nil || !LOG_DB.Migrator().HasTable(&Log{}) {
		return protected, nil
	}
	operationKeys := make([]string, 0, len(sourceReleased))
	for _, reference := range sourceReleased {
		operationKeys = append(operationKeys, reference.OperationKey)
	}
	var storedKeys []string
	if err := LOG_DB.WithContext(ctx).Table("logs").Distinct("billing_operation_key").
		Where("billing_operation_key IN ?", operationKeys).Pluck("billing_operation_key", &storedKeys).Error; err != nil {
		return protected, err
	}
	stored := make(map[string]struct{}, len(storedKeys))
	for _, operationKey := range storedKeys {
		stored[operationKey] = struct{}{}
	}
	for _, reference := range sourceReleased {
		if _, openConflict := openConflictKeys[reference.OperationKey]; openConflict {
			continue
		}
		if _, exists := stored[reference.OperationKey]; !exists {
			protected[reference.ProjectionID] = false
		}
	}
	return protected, nil
}
