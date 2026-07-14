package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type taskBillingStub struct{}

func (taskBillingStub) Settle(int) error         { return nil }
func (taskBillingStub) Refund(*gin.Context)      {}
func (taskBillingStub) NeedsRefund() bool        { return false }
func (taskBillingStub) GetPreConsumedQuota() int { return 0 }
func (taskBillingStub) Reserve(int) error        { return nil }

func taskSubmitTestFixture(channelType, channelID int, apiKey, baseURL, modelName string) (*gin.Context, *relaycommon.RelayInfo) {
	ctx, info, _ := taskSubmitTestFixtureWithRecorder(channelType, channelID, apiKey, baseURL, modelName)
	return ctx, info
}

func taskSubmitTestFixtureWithRecorder(channelType, channelID int, apiKey, baseURL, modelName string) (*gin.Context, *relaycommon.RelayInfo, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"prompt":"draw a cat","model":"`+modelName+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("platform", strconv.Itoa(channelType))
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channelID)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, channelType)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, apiKey)
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	return ctx, &relaycommon.RelayInfo{
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: modelName,
		UserSetting:     dto.UserSetting{AcceptUnsetRatioModel: true},
		Billing:         taskBillingStub{},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			AsyncBillingV2Decided: true,
		},
	}, recorder
}

func setupResolveOriginTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/relay-task.db"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RedisEnabled = previousRedisEnabled
	})

	return db
}

func TestResolveOriginTaskKeepsLegacySingleKeyCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)

	baseURL := "https://single.example"
	channel := &model.Channel{
		Id:      9801,
		Type:    constant.ChannelTypeOpenAI,
		Name:    "single origin",
		Key:     "single-actual",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}
	require.NoError(t, db.Create(channel).Error)
	task := &model.Task{
		TaskID: "origin-single", UserId: 77, ChannelId: channel.Id,
	}
	require.NoError(t, db.Create(task).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 9999)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
	info := &relaycommon.RelayInfo{
		UserId:        77,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: task.TaskID},
	}

	require.Nil(t, ResolveOriginTask(ctx, info))
	assert.Equal(t, task.TaskID, info.OriginUpstreamTaskID)
	assert.Equal(t, 9999, common.GetContextKeyInt(ctx, constant.ContextKeyChannelId))
	assert.Equal(t, "stale-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))

	lockedChannel, ok := info.LockedChannel.(*model.Channel)
	require.True(t, ok)
	require.NotNil(t, lockedChannel)
	assert.Equal(t, channel.Id, lockedChannel.Id)
	require.Nil(t, middleware.SetupContextForSelectedChannelMetadata(ctx, lockedChannel, info.OriginModelName))
	require.Nil(t, middleware.CommitSelectedChannelCredential(ctx, lockedChannel))
	assert.Equal(t, "single-actual", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestResolveOriginTaskFailsClosedForLegacyMultiKeyTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)

	channel := &model.Channel{
		Id:     9803,
		Name:   "disabled credentials",
		Key:    "disabled-a\ndisabled-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusManuallyDisabled,
			},
		},
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "origin-disabled", UserId: 77, ChannelId: channel.Id}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 9999)
	info := &relaycommon.RelayInfo{
		UserId:        77,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: "origin-disabled"},
	}

	taskErr := ResolveOriginTask(ctx, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "task_credential_identity_missing", taskErr.Code)
	assert.Equal(t, http.StatusConflict, taskErr.StatusCode)
	assert.Nil(t, info.LockedChannel)
}

func TestResolveOriginTaskUsesPersistedCredentialWithoutRuntimeSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingCredentialRef{}))
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(channelrouting.ResetSnapshotForTest)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "persisted-origin-task-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	channel := &model.Channel{
		Id: 9804, Name: "stable origin", Key: "key-b\nkey-a",
		Status:      common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}
	require.NoError(t, db.Create(channel).Error)
	require.NotEmpty(t, channel.RoutingGeneration)
	fingerprint, err := model.RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, "key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 702, ChannelID: channel.Id, ChannelGeneration: channel.RoutingGeneration,
		Fingerprint: fingerprint, FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active: true, CurrentOccurrences: 1,
	}).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "origin-stable", UserId: 77, Group: "origin-group", ChannelId: channel.Id,
		PrivateData: model.TaskPrivateData{
			RoutingCredentialID: 702,
			UpstreamTaskID:      "provider/task?stable",
		},
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	info := &relaycommon.RelayInfo{
		UserId: 77, UsingGroup: "origin-group", TokenGroup: "origin-group", UserGroup: "origin-group",
		TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: "origin-stable"},
	}

	require.Nil(t, ResolveOriginTask(ctx, info))
	assert.Equal(t, "origin-stable", info.OriginTaskID)
	assert.Equal(t, "provider/task?stable", info.OriginUpstreamTaskID)
	assert.Equal(t, "origin-group", info.LockedRoutingGroup)
	assert.Equal(t, 702, info.LockedRoutingCredentialID)
	locked, ok := info.LockedChannel.(*model.Channel)
	require.True(t, ok)
	assert.Equal(t, []string{"key-a"}, locked.GetKeys())
}

func TestResolveOriginTaskRejectsInvalidPersistedUpstreamIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)
	channel := &model.Channel{
		Id: 9808, Name: "invalid upstream identity", Key: "single-key", Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "origin-invalid-upstream", UserId: 77, ChannelId: channel.Id,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider\nidentity"},
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	info := &relaycommon.RelayInfo{
		UserId: 77,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			OriginTaskID: "origin-invalid-upstream",
		},
	}

	taskErr := ResolveOriginTask(ctx, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "task_upstream_identity_invalid", taskErr.Code)
	assert.Equal(t, http.StatusConflict, taskErr.StatusCode)
	assert.Empty(t, info.OriginUpstreamTaskID)
	assert.Nil(t, info.LockedChannel)
}

func TestResolveOriginTaskRejectsRoutingGroupEscalation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)
	channel := &model.Channel{
		Id: 9807, Name: "origin group channel", Key: "single-key", Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "origin-group-task", UserId: 77, Group: "vip", ChannelId: channel.Id,
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	info := &relaycommon.RelayInfo{
		UserId: 77, TokenGroup: "default", UserGroup: "default",
		TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: "origin-group-task"},
	}
	taskErr := ResolveOriginTask(ctx, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "task_routing_group_forbidden", taskErr.Code)
	assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
	assert.Nil(t, info.LockedChannel)
}

func TestLockOriginTaskCredentialPinsStableIdentityAfterKeyReorder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/task/remix", nil)
	channel := &model.Channel{
		Id:  9805,
		Key: "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	info := &relaycommon.RelayInfo{UsingGroup: "default", OriginModelName: "video-test"}

	credential, taskErr := lockOriginTaskCredentialWithResolvers(
		ctx,
		info,
		channel,
		701,
		func(actualChannel *model.Channel, credentialID int) (string, int, bool) {
			assert.Same(t, channel, actualChannel)
			assert.Equal(t, 701, credentialID)
			return "key-a", 1, true
		},
		func(group string, channelID int, credential string) (channelrouting.Identity, bool) {
			assert.Equal(t, "default", group)
			assert.Equal(t, channel.Id, channelID)
			assert.Equal(t, "key-a", credential)
			return channelrouting.Identity{
				SnapshotRevision: 11,
				PoolID:           13,
				MemberID:         17,
				CredentialID:     701,
			}, true
		},
	)

	require.Nil(t, taskErr)
	assert.Equal(t, "key-a", credential)
	identity, planned := service.GetSelectedRoutingIdentity(ctx, channel.Id)
	require.True(t, planned)
	assert.Equal(t, 701, identity.CredentialID)
	assert.Equal(t, uint64(11), identity.SnapshotRevision)
}

func TestPinTaskChannelCredentialKeepsEveryRetryOnSameKey(t *testing.T) {
	channel := &model.Channel{
		Id:   9806,
		Key:  "key-b\nkey-a",
		Keys: []string{"key-b", "key-a"},
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled,
				1: common.ChannelStatusEnabled,
			},
			MultiKeyMode: constant.MultiKeyModePolling,
		},
	}

	locked := pinTaskChannelCredential(channel, "key-a")

	require.NotSame(t, channel, locked)
	assert.Equal(t, []string{"key-b", "key-a"}, channel.GetKeys())
	assert.Equal(t, []string{"key-a"}, locked.GetKeys())
	assert.True(t, locked.ChannelInfo.IsMultiKey)
	for range 3 {
		key, index, apiErr := locked.GetNextEnabledKey()
		require.Nil(t, apiErr)
		assert.Equal(t, "key-a", key)
		assert.Zero(t, index)
	}
}

func TestInitTaskPersistsCredentialIdentityWithoutPlaintextKey(t *testing.T) {
	info := &relaycommon.RelayInfo{
		UserId:          77,
		UsingGroup:      "default",
		OriginModelName: "veo-test",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:           88,
			ChannelType:         constant.ChannelTypeGemini,
			ApiKey:              "plaintext-must-not-be-persisted",
			RoutingCredentialID: 901,
		},
	}

	task := model.InitTask(constant.TaskPlatform("video"), info)

	assert.Equal(t, 901, task.PrivateData.RoutingCredentialID)
	assert.Empty(t, task.PrivateData.Key)
	assert.Equal(t, "task_public", task.TaskID)
}

func TestValidateTaskSubmitCredentialIdentityRejectsAmbiguousMultiKey(t *testing.T) {
	taskErr := validateTaskSubmitCredentialIdentity(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId:         89,
		ChannelIsMultiKey: true,
	}})

	require.NotNil(t, taskErr)
	assert.Equal(t, "task_credential_identity_missing", taskErr.Code)
	assert.Equal(t, http.StatusServiceUnavailable, taskErr.StatusCode)
	assert.True(t, taskErr.LocalError)

	assert.Nil(t, validateTaskSubmitCredentialIdentity(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId:           89,
		ChannelIsMultiKey:   true,
		RoutingCredentialID: 901,
	}}))
	assert.Nil(t, validateTaskSubmitCredentialIdentity(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId: 90,
	}}))
}

func TestReadTaskFetchResponseEnforcesBound(t *testing.T) {
	body, err := readTaskFetchResponse(strings.NewReader("1234"), 4)
	require.NoError(t, err)
	assert.Equal(t, []byte("1234"), body)

	_, err = readTaskFetchResponse(strings.NewReader("12345"), 4)
	require.Error(t, err)
	assert.ErrorIs(t, err, errTaskFetchResponseTooLarge)
}

func TestResolveTaskFetchCredentialUsesStableIdentityAndRejectsLegacyMultiKey(t *testing.T) {
	channel := &model.Channel{
		Id:  9807,
		Key: "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}

	credential, err := resolveTaskFetchCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{RoutingCredentialID: 702},
		func(_ context.Context, actualChannel *model.Channel, credentialID int) (string, int, error) {
			assert.Same(t, channel, actualChannel)
			assert.Equal(t, 702, credentialID)
			return "key-a", 1, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "key-a", credential)

	_, err = resolveTaskFetchCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{},
		func(context.Context, *model.Channel, int) (string, int, error) {
			t.Fatal("legacy multi-key fetch must fail before credential resolution")
			return "", 0, nil
		},
	)
	assert.ErrorIs(t, err, errTaskCredentialIdentityMissing)

	credential, err = resolveTaskFetchCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
			Key:                    " historical-fetch-key ",
		},
		func(context.Context, *model.Channel, int) (string, int, error) {
			t.Fatal("historical plaintext fallback must not call stable credential resolution")
			return "", 0, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "historical-fetch-key", credential)

	_, err = resolveTaskFetchCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingProtocolVersion,
			Key:                    "v2-plaintext-must-not-be-used",
		},
		func(context.Context, *model.Channel, int) (string, int, error) {
			t.Fatal("v2 rows without stable identity must fail before credential resolution")
			return "", 0, nil
		},
	)
	assert.ErrorIs(t, err, errTaskCredentialIdentityMissing)
}

func TestTaskModel2DtoSanitizesHistoricalDataAndUsesLocalResultURL(t *testing.T) {
	task := &model.Task{
		TaskID:     "task/public id",
		Platform:   constant.TaskPlatform("video"),
		Status:     model.TaskStatusSuccess,
		FailReason: "Authorization: Bearer historical-secret",
		Data: []byte(`{
			"authorization":"Bearer persisted-secret",
			"url":"https://media.example/video.mp4?X-Amz-Signature=signed-secret",
			"nested":{"api_key":"nested-secret"}
		}`),
	}

	result := TaskModel2Dto(task)

	assert.Equal(t, "/v1/videos/task%2Fpublic%20id/content", result.ResultURL)
	assert.NotContains(t, result.FailReason, "historical-secret")
	assert.NotContains(t, string(result.Data), "persisted-secret")
	assert.NotContains(t, string(result.Data), "signed-secret")
	assert.NotContains(t, string(result.Data), "nested-secret")
	assert.Contains(t, string(result.Data), "[redacted_external_url]")
}

func TestApplyRealtimeTaskObservationFinalizesBillingAndKeepsProviderURLPrivate(t *testing.T) {
	db := setupResolveOriginTaskTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TaskBillingOperation{}))
	task := &model.Task{
		TaskID: "task_realtime_terminal", UserId: 77, ChannelId: 91,
		Platform: constant.TaskPlatform("video"), Status: model.TaskStatusInProgress,
		Progress: "50%", CreatedAt: 100, UpdatedAt: 100,
	}
	require.NoError(t, db.Create(task).Error)
	providerURL := "https://media.example/video.mp4?X-Amz-Signature=provider-secret"
	body := []byte(`{
		"done":true,
		"authorization":"Bearer response-secret",
		"response":{"url":"https://media.example/video.mp4?X-Amz-Signature=provider-secret"}
	}`)

	err := applyRealtimeTaskObservation(context.Background(), task, &relaycommon.TaskInfo{
		Status: model.TaskStatusSuccess, Progress: "100%", RemoteUrl: providerURL,
	}, body, 0)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
	assert.Equal(t, providerURL, task.GetUpstreamResultURL())
	assert.Equal(t, "/v1/videos/task_realtime_terminal/content", task.GetResultURL())
	assert.NotContains(t, string(task.Data), "response-secret")
	assert.NotContains(t, string(task.Data), "provider-secret")
	operation, err := model.GetTaskBillingOperationByTaskID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, operation.State)
	assert.Equal(t, model.TaskBillingOperationLogNotRequired, operation.LogState)
	publicTask := TaskModel2Dto(task)
	assert.Equal(t, "/v1/videos/task_realtime_terminal/content", publicTask.ResultURL)
	assert.NotContains(t, string(publicTask.Data), "provider-secret")
}

func TestApplyRealtimeTaskObservationCASLoserReloadsPersistentWinner(t *testing.T) {
	db := setupResolveOriginTaskTestDB(t)
	stored := &model.Task{
		TaskID: "task_realtime_cas", UserId: 78, ChannelId: 92,
		Platform: constant.TaskPlatform("video"), Status: model.TaskStatusSuccess,
		Progress: "100%", Data: []byte(`{"winner":true}`),
	}
	require.NoError(t, db.Create(stored).Error)
	stale := *stored
	stale.Status = model.TaskStatusInProgress
	stale.Progress = "40%"
	stale.Data = []byte(`{"stale":true}`)

	err := applyRealtimeTaskObservation(context.Background(), &stale, &relaycommon.TaskInfo{
		Status: model.TaskStatusInProgress, Progress: "60%",
	}, []byte(`{"loser":true}`), 0)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), stale.Status)
	assert.Equal(t, "100%", stale.Progress)
	assert.JSONEq(t, `{"winner":true}`, string(stale.Data))
}

func TestSunoContinuationResolvesOriginTaskChannelAfterValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)
	baseURL := "https://suno-origin.example"
	channel := &model.Channel{
		Id: 9804, Type: constant.ChannelTypeSunoAPI, Name: "suno origin", Key: "suno-key",
		Status: common.ChannelStatusEnabled, BaseURL: &baseURL,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task-public", UserId: 77, ChannelId: channel.Id,
		Properties: model.Properties{OriginModelName: "suno_music"},
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/suno/submit/MUSIC",
		strings.NewReader(`{"task_id":"task-public","continue_clip_id":"clip-1"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "action", Value: constant.SunoActionMusic}}
	ctx.Set("platform", string(constant.TaskPlatformSuno))
	info := &relaycommon.RelayInfo{
		UserId: 77, OriginModelName: "suno_music", ChannelMeta: &relaycommon.ChannelMeta{},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}

	require.Nil(t, ValidateTaskRequestForRouting(ctx, info))
	require.Equal(t, "task-public", info.OriginTaskID)
	require.Nil(t, ResolveOriginTask(ctx, info))
	lockedChannel, ok := info.LockedChannel.(*model.Channel)
	require.True(t, ok)
	require.NotNil(t, lockedChannel)
	assert.Equal(t, channel.Id, lockedChannel.Id)
}

func TestRelayTaskSubmitMarksModelPriceFailureLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"prompt":"draw a cat","model":"phase0b-unpriced-task"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("platform", strconv.Itoa(constant.ChannelTypeGemini))
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 71)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeGemini)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "test-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://example.invalid")
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	info := &relaycommon.RelayInfo{
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: "phase0b-unpriced-task",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
	}
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, string(types.ErrorCodeModelPriceError), taskErr.Code)
	assert.True(t, taskErr.LocalError)
	assert.False(t, sendState.Sent())
}

func TestRelayTaskSubmitPreservesRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(server.Close)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"prompt":"draw a cat","model":"veo-3.0-generate-001"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("platform", strconv.Itoa(constant.ChannelTypeGemini))
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 72)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeGemini)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "test-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, server.URL)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	info := &relaycommon.RelayInfo{
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: "veo-3.0-generate-001",
		Billing:         taskBillingStub{},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
	}

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, string(types.ErrorCodeBadResponseStatusCode), taskErr.Code)
	assert.Equal(t, http.StatusTooManyRequests, taskErr.StatusCode)
	assert.Equal(t, int64(2000), taskErr.RetryAfterMs)
	assert.False(t, taskErr.LocalError)
}

func TestRelayTaskSubmitMarksVertexCredentialPreparationFailureLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, info := taskSubmitTestFixture(
		constant.ChannelTypeVertexAi,
		73,
		"not-valid-vertex-credential-json",
		"https://example.invalid",
		"veo-3.0-generate-001",
	)
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	assert.ErrorContains(t, taskErr.Error, "failed to decode credentials")
	assert.False(t, sendState.Sent())
}

func TestRelayTaskSubmitKeepsTransportFailureNonLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL
	server.Close()
	ctx, info := taskSubmitTestFixture(
		constant.ChannelTypeGemini,
		74,
		"test-key",
		baseURL,
		"veo-3.0-generate-001",
	)
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.False(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusBadGateway, taskErr.StatusCode)
	var transportErr *types.NewAPIError
	require.ErrorAs(t, taskErr.Error, &transportErr)
	assert.Equal(t, types.ErrorCodeDoRequestFailed, transportErr.GetErrorCode())
	assert.True(t, sendState.Sent())
}

func TestRelayTaskSubmitMarksInvalidTaskHeaderLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	ctx, info := taskSubmitTestFixture(
		constant.ChannelTypeGemini,
		77,
		"invalid\napi-key",
		"https://example.invalid",
		"veo-3.0-generate-001",
	)
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	assert.ErrorContains(t, taskErr.Error, "invalid header field value")
	assert.False(t, sendState.Sent())
}

func TestRelayTaskSubmitMarksViduEnvelopeRejectionUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"failed","task_id":""}`))
	}))
	t.Cleanup(server.Close)
	ctx, info := taskSubmitTestFixture(
		constant.ChannelTypeVidu,
		75,
		"test-key",
		server.URL,
		"viduq2",
	)

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.False(t, taskErr.LocalError)
	assert.Equal(t, "task_failed", taskErr.Code)
	assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
}

func TestRelayTaskSubmitRejectsViduSuccessWithoutTaskIDBeforeWritingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"created","task_id":""}`))
	}))
	t.Cleanup(server.Close)
	ctx, info := taskSubmitTestFixture(
		constant.ChannelTypeVidu,
		76,
		"test-key",
		server.URL,
		"viduq2",
	)

	result, taskErr := RelayTaskSubmit(ctx, info)

	assert.Nil(t, result)
	require.NotNil(t, taskErr)
	assert.False(t, taskErr.LocalError)
	assert.Equal(t, "invalid_response", taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	assert.False(t, ctx.Writer.Written())
	assert.Equal(t, -1, ctx.Writer.Size())
}

func TestRelayPreparedTaskSubmitMarksViduMissingTaskIDForManualReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	db := setupResolveOriginTaskTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AsyncBillingReservation{}))
	reservation := &model.AsyncBillingReservation{
		ReservationKey:  "vidu-missing-task-id",
		ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind:            model.AsyncBillingKindTask,
		PublicTaskID:    "task_vidu_missing_id",
		State:           model.AsyncBillingReservationStateSendAuthorized,
		UserID:          76,
		FundingSource:   model.TaskBillingSourceWallet,
		CreatedTimeMs:   1,
		UpdatedTimeMs:   1,
	}
	require.NoError(t, db.Create(reservation).Error)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"created","task_id":""}`))
	}))
	t.Cleanup(server.Close)
	ctx, info, recorder := taskSubmitTestFixtureWithRecorder(
		constant.ChannelTypeVidu,
		76,
		"test-key",
		server.URL,
		"viduq2",
	)
	info.AsyncBillingV2Enabled = true
	info.AsyncBillingReservationID = reservation.ID
	info.PublicTaskID = reservation.PublicTaskID
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)
	platform := constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeVidu))
	prepared := &PreparedTaskAttempt{
		adaptor:     GetTaskAdaptor(platform),
		platform:    platform,
		requestBody: strings.NewReader(`{}`),
	}

	result, taskErr := RelayPreparedTaskSubmit(ctx, info, prepared)

	assert.Nil(t, result)
	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, "task_submit_outcome_ambiguous", taskErr.Code)
	assert.Equal(t, http.StatusBadGateway, taskErr.StatusCode)
	assert.True(t, info.AsyncBillingManualReviewMarked)
	assert.True(t, sendState.Sent())
	assert.False(t, ctx.Writer.Written())
	assert.Empty(t, recorder.Body.String())

	var persisted model.AsyncBillingReservation
	require.NoError(t, db.First(&persisted, reservation.ID).Error)
	assert.Equal(t, model.AsyncBillingReservationStateManualReview, persisted.State)
	assert.Equal(t, model.AsyncBillingReviewKindSendOutcome, persisted.ManualReviewKind)
	assert.Contains(t, persisted.ManualReviewReason, "task_id is empty")
}

func TestRelayTaskSubmitBuffersAcceptedResponseUntilDurableCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"created","task_id":"upstream-task-id"}`))
	}))
	t.Cleanup(server.Close)
	ctx, info, recorder := taskSubmitTestFixtureWithRecorder(
		constant.ChannelTypeVidu,
		78,
		"test-key",
		server.URL,
		"viduq2",
	)

	result, taskErr := RelayTaskSubmit(ctx, info)

	require.Nil(t, taskErr)
	require.NotNil(t, result)
	assert.Equal(t, "upstream-task-id", result.UpstreamTaskID)
	assert.False(t, ctx.Writer.Written())
	assert.Empty(t, recorder.Body.String())

	require.NoError(t, result.CommitResponse(ctx))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), info.PublicTaskID)
	assert.NotContains(t, recorder.Body.String(), "upstream-task-id")
}

func TestNormalizeAcceptedUpstreamTaskIDRejectsUnsafeIdentityWithoutEchoingIt(t *testing.T) {
	normalized, err := normalizeAcceptedUpstreamTaskID("  provider-task-1  ")
	require.NoError(t, err)
	assert.Equal(t, "provider-task-1", normalized)

	for _, unsafe := range []string{strings.Repeat("x", 192), "provider\r\ntask", "provider\x00task"} {
		_, err = normalizeAcceptedUpstreamTaskID(unsafe)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sha256=")
		assert.NotContains(t, err.Error(), unsafe)
	}
}
