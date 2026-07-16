package model

import (
	"fmt"
	"maps"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRequestProfileEnabledOptionMigrationPreservesConfiguredValue(t *testing.T) {
	previousDB := DB
	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		DB = previousDB
		smart_routing_setting.ResetForTest()
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	tests := []struct {
		name            string
		initialEnabled  bool
		options         []Option
		expectedEnabled bool
		expectedValue   string
	}{
		{
			name:           "explicit legacy false is preserved",
			initialEnabled: true,
			options: []Option{
				{Key: legacyRequestProfileEnabledOptionKey, Value: "false"},
			},
			expectedEnabled: false,
			expectedValue:   "false",
		},
		{
			name: "legacy true is migrated",
			options: []Option{
				{Key: legacyRequestProfileEnabledOptionKey, Value: "true"},
			},
			expectedEnabled: true,
			expectedValue:   "true",
		},
		{
			name:           "new false takes priority over legacy true",
			initialEnabled: true,
			options: []Option{
				{Key: legacyRequestProfileEnabledOptionKey, Value: "true"},
				{Key: requestProfileEnabledOptionKey, Value: "false"},
			},
			expectedEnabled: false,
			expectedValue:   "false",
		},
		{
			name: "new true takes priority over legacy false",
			options: []Option{
				{Key: legacyRequestProfileEnabledOptionKey, Value: "false"},
				{Key: requestProfileEnabledOptionKey, Value: "true"},
			},
			expectedEnabled: true,
			expectedValue:   "true",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := fmt.Sprintf("file:request-profile-migration-%d?mode=memory&cache=shared", index)
			db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			DB = db
			require.NoError(t, db.AutoMigrate(&Option{}))
			require.NoError(t, db.Create(&test.options).Error)

			smart_routing_setting.ResetForTest()
			initial := smart_routing_setting.GetStoredSetting()
			initial.RequestProfileEnabled = test.initialEnabled
			smart_routing_setting.UpdateSetting(initial)
			common.OptionMapRWMutex.Lock()
			common.OptionMap = make(map[string]string)
			common.OptionMapRWMutex.Unlock()

			require.NoError(t, migrateRequestProfileEnabledOption(db))
			loadOptionsFromDatabase()

			stored := smart_routing_setting.GetStoredSetting()
			assert.Equal(t, test.expectedEnabled, stored.RequestProfileEnabled)

			var persisted []Option
			require.NoError(t, db.Order("key").Find(&persisted).Error)
			assert.Equal(t, []Option{{
				Key:   requestProfileEnabledOptionKey,
				Value: test.expectedValue,
			}}, persisted)

			common.OptionMapRWMutex.RLock()
			publishedValue := common.OptionMap[requestProfileEnabledOptionKey]
			_, legacyPublished := common.OptionMap[legacyRequestProfileEnabledOptionKey]
			common.OptionMapRWMutex.RUnlock()
			assert.Equal(t, test.expectedValue, publishedValue)
			assert.False(t, legacyPublished)
		})
	}
}
