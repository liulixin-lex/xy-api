package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionRejectsInvalidPriceRatioMapsBeforePersistence(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Option{}))

	tests := []struct {
		key   string
		value string
	}{
		{key: "ModelPrice", value: `{"bad":-1}`},
		{key: "ModelRatio", value: `{"bad":-1}`},
		{key: "CompletionRatio", value: `{"bad":-1}`},
		{key: "CacheRatio", value: `{"bad":-1}`},
		{key: "CreateCacheRatio", value: `{"bad":-1}`},
		{key: "ImageRatio", value: `{"bad":-1}`},
		{key: "AudioRatio", value: `{"bad":-1}`},
		{key: "AudioCompletionRatio", value: `{"bad":-1}`},
		{key: "GroupRatio", value: `{"bad":-1}`},
		{key: "GroupGroupRatio", value: `{"vip":{"bad":-1}}`},
		{key: "tool_price_setting.prices", value: `{"bad":-1}`},
	}

	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			var previous Option
			dbErr := DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), test.key).First(&previous).Error
			previousExists := dbErr == nil
			require.True(t, previousExists || errors.Is(dbErr, gorm.ErrRecordNotFound))
			require.NoError(t, DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), test.key).Delete(&Option{}).Error)

			common.OptionMapRWMutex.Lock()
			mapWasNil := common.OptionMap == nil
			if common.OptionMap == nil {
				common.OptionMap = make(map[string]string)
			}
			previousMapValue, previousMapExists := common.OptionMap[test.key]
			delete(common.OptionMap, test.key)
			common.OptionMapRWMutex.Unlock()

			t.Cleanup(func() {
				require.NoError(t, DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), test.key).Delete(&Option{}).Error)
				if previousExists {
					require.NoError(t, DB.Create(&previous).Error)
				}
				common.OptionMapRWMutex.Lock()
				if mapWasNil {
					common.OptionMap = nil
				} else if previousMapExists {
					common.OptionMap[test.key] = previousMapValue
				} else {
					delete(common.OptionMap, test.key)
				}
				common.OptionMapRWMutex.Unlock()
			})

			require.Error(t, UpdateOption(test.key, test.value))

			var count int64
			require.NoError(t, DB.Model(&Option{}).
				Where(fmt.Sprintf("%s = ?", optionKeyColumn()), test.key).
				Count(&count).Error)
			assert.Zero(t, count)
			common.OptionMapRWMutex.RLock()
			_, mapExists := common.OptionMap[test.key]
			common.OptionMapRWMutex.RUnlock()
			assert.False(t, mapExists)
		})
	}
}

func TestUpdateOptionPreservesZeroRatioSemantics(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Option{}))

	const key = "ModelRatio"
	var previous Option
	dbErr := DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), key).First(&previous).Error
	previousExists := dbErr == nil
	require.True(t, previousExists || errors.Is(dbErr, gorm.ErrRecordNotFound))
	originalRuntime := ratio_setting.ModelRatio2JSONString()
	common.OptionMapRWMutex.Lock()
	mapWasNil := common.OptionMap == nil
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previousMapValue, previousMapExists := common.OptionMap[key]
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRuntime))
		require.NoError(t, DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), key).Delete(&Option{}).Error)
		if previousExists {
			require.NoError(t, DB.Create(&previous).Error)
		}
		common.OptionMapRWMutex.Lock()
		if mapWasNil {
			common.OptionMap = nil
		} else if previousMapExists {
			common.OptionMap[key] = previousMapValue
		} else {
			delete(common.OptionMap, key)
		}
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, UpdateOption(key, `{"free":0}`))
	var stored Option
	require.NoError(t, DB.Where(fmt.Sprintf("%s = ?", optionKeyColumn()), key).First(&stored).Error)
	assert.Equal(t, `{"free":0}`, stored.Value)
	assert.Equal(t, map[string]float64{"free": 0}, ratio_setting.GetModelRatioCopy())
}
