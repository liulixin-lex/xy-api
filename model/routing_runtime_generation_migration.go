package model

import (
	"errors"

	"gorm.io/gorm"
)

const routingRuntimeGenerationMigrationBatch = 500

// prepareRoutingRuntimeGenerationSchema runs before AutoMigrate. The legacy
// channel-scoped runtime tables cannot prove which lifecycle produced a row,
// so they intentionally cold-start instead of binding state to a reused ID.
func prepareRoutingRuntimeGenerationSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	states := []struct {
		model       any
		field       string
		legacyIndex string
	}{
		{model: &RoutingChannelMetric{}, field: "ChannelGeneration", legacyIndex: routingChannelMetricLegacyUniqueIndex},
		{model: &RoutingBreakerState{}, field: "ChannelGeneration", legacyIndex: routingBreakerLegacyUniqueIndex},
		{model: &RoutingChannelHealthState{}, field: "ChannelGeneration", legacyIndex: routingChannelHealthLegacyUniqueIndex},
		{model: &RoutingCredentialHealthState{}, field: "ChannelGeneration"},
	}
	for _, state := range states {
		if !db.Migrator().HasTable(state.model) {
			continue
		}
		missingGeneration := !db.Migrator().HasColumn(state.model, "channel_generation")
		legacyIndexExists := state.legacyIndex != "" && db.Migrator().HasIndex(state.model, state.legacyIndex)
		if missingGeneration {
			if err := db.Migrator().AddColumn(state.model, state.field); err != nil {
				return err
			}
		}
		// MySQL DDL is not transactional. A previous startup may have added
		// the generation column and stopped before clearing legacy state. Always
		// remove only unscoped rows so retrying converges without discarding any
		// generation-fenced state already written by a completed v2 cutover.
		if err := db.Where("channel_generation IS NULL OR channel_generation = ?", "").
			Delete(state.model).Error; err != nil {
			return err
		}
		if legacyIndexExists {
			if err := db.Migrator().DropIndex(state.model, state.legacyIndex); err != nil {
				return err
			}
		}
	}
	return nil
}

func MigrateRoutingRuntimeGenerationSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if err := backfillRoutingMetricRollupGenerations(db); err != nil {
		return err
	}
	if err := backfillRoutingProbeGenerations(db); err != nil {
		return err
	}
	return backfillRoutingDecisionGenerations(db)
}

func backfillRoutingMetricRollupGenerations(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingMetricRollup{}) ||
		!db.Migrator().HasColumn(&RoutingMetricRollup{}, "channel_generation") {
		return nil
	}
	lastID := 0
	for {
		var rows []RoutingMetricRollup
		if err := db.Select("id", "member_id", "channel_id", "channel_generation", "schema_version", "last_snapshot_revision").
			Where("id > ? AND (channel_generation IS NULL OR channel_generation = ?)", lastID, "").
			Order("id asc").Limit(routingRuntimeGenerationMigrationBatch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for index := range rows {
			row := rows[index]
			lastID = row.ID
			var member RoutingPoolMember
			err := db.Select("id", "channel_id", "channel_generation").Where("id = ?", row.MemberID).First(&member).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if member.ChannelID != row.ChannelID || !validRoutingIdentity(member.ChannelGeneration) {
				continue
			}
			updates := map[string]any{"channel_generation": member.ChannelGeneration}
			if row.SchemaVersion == 3 && row.LastSnapshotRevision > 0 {
				updates["schema_version"] = RoutingMetricRollupSchemaVersion
			}
			if err := db.Model(&RoutingMetricRollup{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
}

func backfillRoutingProbeGenerations(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingProbeResult{}) ||
		!db.Migrator().HasColumn(&RoutingProbeResult{}, "channel_generation") {
		return nil
	}
	lastID := 0
	for {
		var rows []RoutingProbeResult
		if err := db.Select("id", "member_id", "channel_id", "channel_generation").
			Where("id > ? AND (channel_generation IS NULL OR channel_generation = ?)", lastID, "").
			Order("id asc").Limit(routingRuntimeGenerationMigrationBatch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for index := range rows {
			row := rows[index]
			lastID = row.ID
			var member RoutingPoolMember
			err := db.Select("id", "channel_id", "channel_generation").Where("id = ?", row.MemberID).First(&member).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if member.ChannelID != row.ChannelID || !validRoutingIdentity(member.ChannelGeneration) {
				continue
			}
			if err := db.Model(&RoutingProbeResult{}).Where("id = ?", row.ID).
				Update("channel_generation", member.ChannelGeneration).Error; err != nil {
				return err
			}
		}
	}
}

func backfillRoutingDecisionGenerations(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingDecisionAudit{}) ||
		!db.Migrator().HasColumn(&RoutingDecisionAudit{}, "selected_channel_generation") {
		return nil
	}
	lastID := 0
	for {
		var rows []RoutingDecisionAudit
		if err := db.Select(
			"id", "actual_channel_id", "observed_channel_id", "selected_member_id", "selected_channel_generation",
		).Where(
			"id > ? AND selected_member_id > 0 AND (selected_channel_generation IS NULL OR selected_channel_generation = ?)",
			lastID, "",
		).Order("id asc").Limit(routingRuntimeGenerationMigrationBatch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for index := range rows {
			row := rows[index]
			lastID = row.ID
			var member RoutingPoolMember
			err := db.Select("id", "channel_id", "channel_generation").Where("id = ?", row.SelectedMemberID).First(&member).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if member.ChannelID != row.ActualChannelID || member.ChannelID != row.ObservedChannelID ||
				!validRoutingIdentity(member.ChannelGeneration) {
				continue
			}
			if err := db.Model(&RoutingDecisionAudit{}).Where("id = ?", row.ID).Updates(map[string]any{
				"actual_channel_generation":   member.ChannelGeneration,
				"observed_channel_generation": member.ChannelGeneration,
				"selected_channel_generation": member.ChannelGeneration,
			}).Error; err != nil {
				return err
			}
		}
	}
}
