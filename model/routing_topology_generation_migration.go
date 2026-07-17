package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

const (
	routingPoolMemberLegacyUniqueIndex        = "idx_routing_pool_member"
	routingPoolMemberGenerationUniqueIndex    = "idx_routing_pool_member_generation"
	routingCredentialLegacyUniqueIndex        = "idx_routing_credential_ref"
	routingCredentialGenerationUniqueIndex    = "idx_routing_credential_ref_generation"
	routingTopologyDefaultsMigrationComponent = "channel-routing-topology-defaults-v2"
)

// prepareRoutingTopologyGenerationSchema runs before AutoMigrate. Existing
// installations need the generation columns populated before GORM creates the
// replacement unique indexes, otherwise multiple legacy rows with an empty
// generation would collide during startup.
func prepareRoutingTopologyGenerationSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if db.Migrator().HasTable(&RoutingPoolMember{}) {
		if !db.Migrator().HasColumn(&RoutingPoolMember{}, "channel_generation") {
			if err := db.Migrator().AddColumn(&RoutingPoolMember{}, "ChannelGeneration"); err != nil {
				return err
			}
		}
		cutoverComplete, err := routingTopologyDefaultsCutoverComplete(db)
		if err != nil {
			return err
		}
		if err := backfillRoutingPoolMemberGenerations(db, !cutoverComplete); err != nil {
			return err
		}
		if db.Migrator().HasIndex(&RoutingPoolMember{}, routingPoolMemberLegacyUniqueIndex) {
			if err := db.Migrator().DropIndex(&RoutingPoolMember{}, routingPoolMemberLegacyUniqueIndex); err != nil {
				return err
			}
		}
	}
	if db.Migrator().HasTable(&RoutingCredentialRef{}) {
		if !db.Migrator().HasColumn(&RoutingCredentialRef{}, "channel_generation") {
			if err := db.Migrator().AddColumn(&RoutingCredentialRef{}, "ChannelGeneration"); err != nil {
				return err
			}
		}
		if err := backfillRoutingCredentialGenerations(db); err != nil {
			return err
		}
		if db.Migrator().HasIndex(&RoutingCredentialRef{}, routingCredentialLegacyUniqueIndex) {
			if err := db.Migrator().DropIndex(&RoutingCredentialRef{}, routingCredentialLegacyUniqueIndex); err != nil {
				return err
			}
		}
	}
	return nil
}

func MigrateRoutingTopologyGenerationSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return ErrRoutingSchemaNotReady
	}
	if db.Migrator().HasTable(&RoutingPoolMember{}) &&
		!db.Migrator().HasIndex(&RoutingPoolMember{}, routingPoolMemberGenerationUniqueIndex) {
		if err := db.Migrator().CreateIndex(&RoutingPoolMember{}, routingPoolMemberGenerationUniqueIndex); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(&RoutingCredentialRef{}) &&
		!db.Migrator().HasIndex(&RoutingCredentialRef{}, routingCredentialGenerationUniqueIndex) {
		if err := db.Migrator().CreateIndex(&RoutingCredentialRef{}, routingCredentialGenerationUniqueIndex); err != nil {
			return err
		}
	}
	var marker RoutingSchemaVersion
	err := db.Where("component = ?", routingTopologyDefaultsMigrationComponent).First(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&RoutingPool{}) {
			if err := tx.Model(&RoutingPool{}).Where("1 = 1").Updates(map[string]any{
				"default_enabled": true, "default_priority": int64(0), "default_weight": int64(100),
			}).Error; err != nil {
				return err
			}
		}
		nowMs, err := routingDatabaseNowMs(tx)
		if err != nil {
			return err
		}
		return tx.Create(&RoutingSchemaVersion{
			Component: routingTopologyDefaultsMigrationComponent,
			Version:   "2", UpdatedTimeMs: nowMs,
		}).Error
	})
}

func backfillRoutingPoolMemberGenerations(db *gorm.DB, retireAll bool) error {
	var members []RoutingPoolMember
	query := db.Select("id", "channel_id", "channel_generation", "active").Order("id asc")
	if !retireAll {
		query = query.Where("channel_generation IS NULL OR channel_generation = ?", "")
	}
	if err := query.Find(&members).Error; err != nil {
		return err
	}
	for index := range members {
		generation := routingGenerationForMigration(members[index].ChannelID, "member", members[index].ID)
		// A legacy member row is keyed only by a reusable numeric channel ID.
		// Its lifecycle cannot be proven, even when that ID currently exists, so
		// retire it under a deterministic legacy generation. Reconciliation will
		// create a fresh member ID for the current generation.
		updates := map[string]any{"channel_generation": generation, "active": false}
		if members[index].ChannelGeneration == generation && !members[index].Active {
			continue
		}
		result := db.Model(&RoutingPoolMember{}).Where("id = ?", members[index].ID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingChannelLifecycleConflicted
		}
	}
	return nil
}

func routingTopologyDefaultsCutoverComplete(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return false, nil
	}
	var count int64
	if err := db.Model(&RoutingSchemaVersion{}).
		Where("component = ? AND version = ?", routingTopologyDefaultsMigrationComponent, "2").
		Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

func backfillRoutingCredentialGenerations(db *gorm.DB) error {
	var credentials []RoutingCredentialRef
	if err := db.Select("id", "channel_id", "channel_generation", "active").
		Where("channel_generation IS NULL OR channel_generation = ?", "").
		Order("id asc").Find(&credentials).Error; err != nil {
		return err
	}
	for index := range credentials {
		generation := routingGenerationForMigration(credentials[index].ChannelID, "credential", credentials[index].ID)
		// Empty legacy credential generations are equally unprovable. Retire
		// them instead of attaching their stable ID to the channel that happens
		// to own the numeric ID during this upgrade.
		updates := map[string]any{
			"channel_generation": generation, "active": false, "current_occurrences": 0,
		}
		result := db.Model(&RoutingCredentialRef{}).
			Where("id = ? AND (channel_generation IS NULL OR channel_generation = ?)", credentials[index].ID, "").
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingChannelLifecycleConflicted
		}
	}
	return nil
}

func routingGenerationForMigration(channelID int, kind string, rowID int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("routing-legacy-%s:%d:%d", kind, channelID, rowID)))
	return hex.EncodeToString(digest[:16])
}
