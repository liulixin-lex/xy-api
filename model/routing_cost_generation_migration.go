package model

import (
	"errors"

	"gorm.io/gorm"
)

const (
	routingCostGenerationMigrationComponent = "channel-routing-cost-generation-v2"
	routingCostGenerationMigrationBatch     = 500
)

// MigrateRoutingCostSnapshotGenerationSchema keeps retired connector history
// readable without guessing that an old numeric channel ID belongs to the
// channel currently using that number. New generation-scoped rows are written
// with schema v2; all pre-v2 rows are explicitly marked legacy-unscoped.
func MigrateRoutingCostSnapshotGenerationSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingCostSnapshotVersion{}) ||
		!db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return ErrRoutingSchemaNotReady
	}
	for {
		var rows []RoutingCostSnapshotVersion
		if err := db.Select("id", "schema_version", "routing_identity", "routing_generation", "lifecycle_scope").
			Where("lifecycle_scope IS NULL OR lifecycle_scope = ?", "").
			Order("id ASC").Limit(routingCostGenerationMigrationBatch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			if row.SchemaVersion != routingCostSnapshotVersionSchema ||
				row.RoutingIdentity != "" || row.RoutingGeneration != "" {
				return ErrRoutingCostVersionCorrupt
			}
			ids = append(ids, row.ID)
		}
		result := db.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingCostSnapshotVersion{}).
			Where("id IN ? AND (lifecycle_scope IS NULL OR lifecycle_scope = ?)", ids, "").
			UpdateColumn("lifecycle_scope", RoutingCostLifecycleScopeLegacyUnscoped)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return ErrRoutingCostVersionCorrupt
		}
	}

	var invalidCount int64
	if err := db.Model(&RoutingCostSnapshotVersion{}).Where(
		"(schema_version = ? AND (routing_identity <> ? OR routing_generation <> ? OR lifecycle_scope <> ?)) OR "+
			"(schema_version = ? AND (routing_identity = ? OR routing_generation = ? OR lifecycle_scope <> ?)) OR "+
			"schema_version NOT IN ?",
		routingCostSnapshotVersionSchema, "", "", RoutingCostLifecycleScopeLegacyUnscoped,
		routingCostSnapshotGenerationVersionSchema, "", "", RoutingCostLifecycleScopeGeneration,
		[]int{routingCostSnapshotVersionSchema, routingCostSnapshotGenerationVersionSchema},
	).Count(&invalidCount).Error; err != nil {
		return err
	}
	if invalidCount != 0 {
		return ErrRoutingCostVersionCorrupt
	}

	var marker RoutingSchemaVersion
	err := db.Where("component = ?", routingCostGenerationMigrationComponent).First(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	nowMs, err := routingDatabaseNowMs(db)
	if err != nil {
		return err
	}
	return db.Create(&RoutingSchemaVersion{
		Component: routingCostGenerationMigrationComponent,
		Version:   "2", UpdatedTimeMs: nowMs,
	}).Error
}
