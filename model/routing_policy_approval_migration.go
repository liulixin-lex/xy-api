package model

import (
	"fmt"

	"gorm.io/gorm"
)

func MigrateRoutingPolicyApprovalIntentIndexes(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	specs := []struct {
		model           any
		tableName       string
		legacyIndexName string
		intentIndexName string
		columns         []string
	}{
		{
			model: &RoutingPolicyApproval{}, tableName: (RoutingPolicyApproval{}).TableName(),
			legacyIndexName: routingPolicyApprovalActorLegacyUniqueIndex,
			intentIndexName: routingPolicyApprovalIntentActorUniqueIndex,
			columns:         []string{"draft_id", "draft_version", "activation_hash", "actor_id"},
		},
		{
			model: &RoutingPolicyRollbackApproval{}, tableName: (RoutingPolicyRollbackApproval{}).TableName(),
			legacyIndexName: routingPolicyRollbackApprovalActorLegacyUniqueIndex,
			intentIndexName: routingPolicyRollbackApprovalIntentActorUniqueIndex,
			columns: []string{
				"expected_revision", "expected_activation_id", "target_revision", "activation_hash", "actor_id",
			},
		},
	}
	for _, spec := range specs {
		if !db.Migrator().HasTable(spec.model) {
			return fmt.Errorf("migrate %s: %w", spec.tableName, ErrRoutingSchemaNotReady)
		}
		ready, err := routingCriticalIndexReady(db, spec.tableName, spec.intentIndexName, spec.columns)
		if err != nil {
			return err
		}
		if !ready {
			if db.Migrator().HasIndex(spec.model, spec.intentIndexName) {
				if err := db.Migrator().DropIndex(spec.model, spec.intentIndexName); err != nil {
					return fmt.Errorf("drop invalid %s: %w", spec.intentIndexName, err)
				}
			}
			if err := db.Migrator().CreateIndex(spec.model, spec.intentIndexName); err != nil {
				return fmt.Errorf("create %s: %w", spec.intentIndexName, err)
			}
			ready, err = routingCriticalIndexReady(db, spec.tableName, spec.intentIndexName, spec.columns)
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("verify %s: %w", spec.intentIndexName, ErrRoutingSchemaNotReady)
			}
		}
		if db.Migrator().HasIndex(spec.model, spec.legacyIndexName) {
			if err := db.Migrator().DropIndex(spec.model, spec.legacyIndexName); err != nil {
				return fmt.Errorf("drop legacy %s: %w", spec.legacyIndexName, err)
			}
		}
		if db.Migrator().HasIndex(spec.model, spec.legacyIndexName) {
			return fmt.Errorf("verify legacy %s removal: %w", spec.legacyIndexName, ErrRoutingSchemaNotReady)
		}
	}
	return nil
}
