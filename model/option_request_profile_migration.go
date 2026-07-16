package model

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	legacyRequestProfileEnabledOptionKey = "smart_routing_setting.request_profile_v2_enabled"
	requestProfileEnabledOptionKey       = "smart_routing_setting.request_profile_enabled"
)

func migrateRequestProfileEnabledOption(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var legacyOption Option
		result := tx.Where("key = ?", legacyRequestProfileEnabledOptionKey).Limit(1).Find(&legacyOption)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		migrated := Option{Key: requestProfileEnabledOptionKey, Value: legacyOption.Value}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&migrated).Error; err != nil {
			return err
		}
		return tx.Where("key = ?", legacyRequestProfileEnabledOptionKey).Delete(&Option{}).Error
	})
}
