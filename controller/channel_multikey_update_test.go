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
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
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

func TestManageMultiKeysDeleteActionsRemapStateByRawKey(t *testing.T) {
	tests := []struct {
		name           string
		action         string
		keyIndex       *int
		keys           string
		status         map[int]int
		reason         map[int]string
		disabledTime   map[int]int64
		wantKeys       string
		wantStatus     map[int]int
		wantReason     map[int]string
		wantDisabledAt map[int]int64
	}{
		{
			name:     "delete key clears ambiguous duplicate state",
			action:   "delete_key",
			keyIndex: common.GetPointer(3),
			keys:     "dup\ndup\nunique\nremove",
			status: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusManuallyDisabled,
				2: common.ChannelStatusManuallyDisabled,
			},
			reason: map[int]string{
				0: "duplicate automatic failure",
				1: "duplicate manual operation",
				2: "unique manual operation",
			},
			disabledTime: map[int]int64{0: 101, 1: 202, 2: 303},
			wantKeys:     "dup\ndup\nunique",
			wantStatus:   map[int]int{2: common.ChannelStatusManuallyDisabled},
			wantReason:   map[int]string{2: "unique manual operation"},
			wantDisabledAt: map[int]int64{
				2: 303,
			},
		},
		{
			name:   "delete disabled keys clears state from an ambiguous survivor",
			action: "delete_disabled_keys",
			keys:   "dup\ndup\nunique",
			status: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusManuallyDisabled,
				2: common.ChannelStatusManuallyDisabled,
			},
			reason: map[int]string{
				0: "duplicate automatic failure",
				1: "duplicate manual operation",
				2: "unique manual operation",
			},
			disabledTime: map[int]int64{0: 101, 1: 202, 2: 303},
			wantKeys:     "dup\nunique",
			wantStatus:   map[int]int{1: common.ChannelStatusManuallyDisabled},
			wantReason:   map[int]string{1: "unique manual operation"},
			wantDisabledAt: map[int]int64{
				1: 303,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupMultiKeyUpdateControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			common.MemoryCacheEnabled = false

			channel := &model.Channel{
				Name:   test.name,
				Key:    test.keys,
				Status: common.ChannelStatusEnabled,
				Models: "gpt-test",
				Group:  "default",
				ChannelInfo: model.ChannelInfo{
					IsMultiKey:             true,
					MultiKeySize:           len((&model.Channel{Key: test.keys}).GetKeys()),
					MultiKeyStatusList:     test.status,
					MultiKeyDisabledReason: test.reason,
					MultiKeyDisabledTime:   test.disabledTime,
					MultiKeyPollingIndex:   1,
				},
			}
			require.NoError(t, db.Create(channel).Error)

			request := map[string]any{
				"channel_id": channel.Id,
				"action":     test.action,
			}
			if test.keyIndex != nil {
				request["key_index"] = *test.keyIndex
			}
			requestBody, err := common.Marshal(request)
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 1)
			ctx.Set("role", common.RoleRootUser)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(requestBody))
			ctx.Request.Header.Set("Content-Type", "application/json")

			ManageMultiKeys(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success, response.Message)

			updated, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, test.wantKeys, updated.Key)
			assert.Equal(t, len((&model.Channel{Key: test.wantKeys}).GetKeys()), updated.ChannelInfo.MultiKeySize)
			assert.Equal(t, test.wantStatus, updated.ChannelInfo.MultiKeyStatusList)
			assert.Equal(t, test.wantReason, updated.ChannelInfo.MultiKeyDisabledReason)
			assert.Equal(t, test.wantDisabledAt, updated.ChannelInfo.MultiKeyDisabledTime)
			assert.Zero(t, updated.ChannelInfo.MultiKeyPollingIndex)
		})
	}
}

func TestManageMultiKeysDeleteDisabledKeysRejectsDeletingAllKeys(t *testing.T) {
	db := setupMultiKeyUpdateControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	common.MemoryCacheEnabled = false

	channel := &model.Channel{
		Name:   "reject deleting all multi-keys",
		Key:    "raw-a\nraw-b",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-test",
		Group:  "default",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusAutoDisabled,
			},
			MultiKeyDisabledReason: map[int]string{
				0: "automatic failure a",
				1: "automatic failure b",
			},
			MultiKeyDisabledTime: map[int]int64{
				0: 101,
				1: 202,
			},
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, db.Create(channel).Error)

	requestBody, err := common.Marshal(map[string]any{
		"channel_id": channel.Id,
		"action":     "delete_disabled_keys",
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(requestBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	ManageMultiKeys(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)

	updated, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "raw-a\nraw-b", updated.Key)
	assert.Equal(t, 2, updated.ChannelInfo.MultiKeySize)
	assert.Equal(t, map[int]int{
		0: common.ChannelStatusAutoDisabled,
		1: common.ChannelStatusAutoDisabled,
	}, updated.ChannelInfo.MultiKeyStatusList)
	assert.Equal(t, map[int]string{
		0: "automatic failure a",
		1: "automatic failure b",
	}, updated.ChannelInfo.MultiKeyDisabledReason)
	assert.Equal(t, map[int]int64{0: 101, 1: 202}, updated.ChannelInfo.MultiKeyDisabledTime)
	assert.Equal(t, 1, updated.ChannelInfo.MultiKeyPollingIndex)
}
