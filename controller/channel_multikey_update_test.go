package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMultiKeyUpdateControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousMemoryCacheEnabled := common.MemoryCacheEnabled

	db := setupModelListControllerTestDB(t)
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})
	return db
}

func TestUpdateChannelRemapsMultiKeyState(t *testing.T) {
	tests := []struct {
		name           string
		keyMode        string
		requestKey     string
		expectedKey    string
		expectedStatus map[int]int
		expectedReason map[int]string
		expectedTime   map[int]int64
	}{
		{
			name:           "replace",
			keyMode:        "replace",
			requestKey:     "raw-b\nraw-new\nraw-a",
			expectedKey:    "raw-b\nraw-new\nraw-a",
			expectedStatus: map[int]int{0: common.ChannelStatusManuallyDisabled},
			expectedReason: map[int]string{0: "manual operation"},
			expectedTime:   map[int]int64{0: 123456},
		},
		{
			name:           "append",
			keyMode:        "append",
			requestKey:     "raw-new",
			expectedKey:    "raw-a\nraw-b\nraw-new",
			expectedStatus: map[int]int{1: common.ChannelStatusManuallyDisabled},
			expectedReason: map[int]string{1: "manual operation"},
			expectedTime:   map[int]int64{1: 123456},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupMultiKeyUpdateControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Log{}))

			common.MemoryCacheEnabled = false

			channel := &model.Channel{
				Name:   "multi-key update",
				Key:    "raw-a\nraw-b",
				Status: common.ChannelStatusEnabled,
				Models: "gpt-test",
				Group:  "default",
				ChannelInfo: model.ChannelInfo{
					IsMultiKey:             true,
					MultiKeySize:           2,
					MultiKeyStatusList:     map[int]int{1: common.ChannelStatusManuallyDisabled},
					MultiKeyDisabledReason: map[int]string{1: "manual operation"},
					MultiKeyDisabledTime:   map[int]int64{1: 123456},
					MultiKeyPollingIndex:   1,
				},
			}
			require.NoError(t, db.Create(channel).Error)

			requestBody, err := common.Marshal(map[string]any{
				"id":       channel.Id,
				"key":      test.requestKey,
				"key_mode": test.keyMode,
			})
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 1)
			ctx.Set("role", common.RoleRootUser)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/", bytes.NewReader(requestBody))
			ctx.Request.Header.Set("Content-Type", "application/json")

			UpdateChannel(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success, response.Message)

			updated, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, test.expectedKey, updated.Key)
			assert.Equal(t, 3, updated.ChannelInfo.MultiKeySize)
			assert.Equal(t, test.expectedStatus, updated.ChannelInfo.MultiKeyStatusList)
			assert.Equal(t, test.expectedReason, updated.ChannelInfo.MultiKeyDisabledReason)
			assert.Equal(t, test.expectedTime, updated.ChannelInfo.MultiKeyDisabledTime)
			assert.NotContains(t, updated.ChannelInfo.MultiKeyStatusList, 2)
			assert.Zero(t, updated.ChannelInfo.MultiKeyPollingIndex)
		})
	}
}

func TestUpdateChannelRemapsMultiKeyPollingStateInCache(t *testing.T) {
	db := setupMultiKeyUpdateControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	common.MemoryCacheEnabled = true

	channel := &model.Channel{
		Id:     987654,
		Name:   "multi-key cache update",
		Key:    "raw-a\nraw-b",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-test",
		Group:  "default",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyPollingIndex: 1,
			MultiKeyMode:         constant.MultiKeyModePolling,
		},
	}
	require.NoError(t, channel.Insert())
	model.InitChannelCache()

	requestBody, err := common.Marshal(map[string]any{
		"id":       channel.Id,
		"key":      "raw-b\nraw-new\nraw-a",
		"key_mode": "replace",
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/", bytes.NewReader(requestBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, response.Message)

	updated, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Zero(t, updated.ChannelInfo.MultiKeyPollingIndex)
	cachedInfo, err := model.CacheGetChannelInfo(channel.Id)
	require.NoError(t, err)
	assert.Zero(t, cachedInfo.MultiKeyPollingIndex)
}
