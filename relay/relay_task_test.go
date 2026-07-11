package relay

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
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
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
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
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
	}
}

func setupResolveOriginTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/relay-task.db"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
	})

	return db
}

func TestResolveOriginTaskSynchronizesCredentialContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupResolveOriginTaskTestDB(t)

	singleBaseURL := "https://single.example"
	multiBaseURL := "https://multi.example"
	channels := []*model.Channel{
		{
			Id:      9801,
			Type:    constant.ChannelTypeOpenAI,
			Name:    "single origin",
			Key:     "single-actual",
			Status:  common.ChannelStatusEnabled,
			BaseURL: &singleBaseURL,
		},
		{
			Id:      9802,
			Type:    constant.ChannelTypeAnthropic,
			Name:    "multi origin",
			Key:     "multi-disabled\nmulti-enabled",
			Status:  common.ChannelStatusEnabled,
			BaseURL: &multiBaseURL,
			ChannelInfo: model.ChannelInfo{
				IsMultiKey:   true,
				MultiKeySize: 2,
				MultiKeyStatusList: map[int]int{
					0: common.ChannelStatusAutoDisabled,
					1: common.ChannelStatusEnabled,
				},
				MultiKeyMode: constant.MultiKeyModeRandom,
			},
		},
	}
	for _, channel := range channels {
		require.NoError(t, db.Create(channel).Error)
	}

	tasks := []*model.Task{
		{TaskID: "origin-single", UserId: 77, ChannelId: channels[0].Id},
		{TaskID: "origin-multi", UserId: 77, ChannelId: channels[1].Id},
	}
	for _, task := range tasks {
		require.NoError(t, db.Create(task).Error)
	}

	tests := []struct {
		name          string
		originTaskID  string
		target        *model.Channel
		expectedKey   string
		expectedMulti bool
		expectedIndex int
	}{
		{
			name:          "single key",
			originTaskID:  tasks[0].TaskID,
			target:        channels[0],
			expectedKey:   "single-actual",
			expectedMulti: false,
			expectedIndex: model.RoutingMetricSingleKeyIndex,
		},
		{
			name:          "multi key skips disabled credential",
			originTaskID:  tasks[1].TaskID,
			target:        channels[1],
			expectedKey:   "multi-enabled",
			expectedMulti: true,
			expectedIndex: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			common.SetContextKey(ctx, constant.ContextKeyChannelId, 9999)
			common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeGemini)
			common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://stale.example")
			common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
			common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
			common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 7)

			info := &relaycommon.RelayInfo{
				UserId:        77,
				ChannelMeta:   nil,
				TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: test.originTaskID},
			}

			taskErr := ResolveOriginTask(ctx, info)

			require.Nil(t, taskErr)
			assert.Equal(t, test.target.Id, common.GetContextKeyInt(ctx, constant.ContextKeyChannelId))
			assert.Equal(t, test.target.Type, common.GetContextKeyInt(ctx, constant.ContextKeyChannelType))
			assert.Equal(t, test.target.GetBaseURL(), common.GetContextKeyString(ctx, constant.ContextKeyChannelBaseUrl))
			assert.Equal(t, test.expectedKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
			assert.Equal(t, test.expectedMulti, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
			assert.Equal(t, test.expectedIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))

			lockedChannel, ok := info.LockedChannel.(*model.Channel)
			require.True(t, ok)
			require.NotNil(t, lockedChannel)
			assert.Equal(t, test.target.Id, lockedChannel.Id)

			info.InitChannelMeta(ctx)
			require.NotNil(t, info.ChannelMeta)
			assert.Equal(t, test.target.Id, info.ChannelId)
			assert.Equal(t, test.target.Type, info.ChannelType)
			assert.Equal(t, test.target.GetBaseURL(), info.ChannelBaseUrl)
			assert.Equal(t, test.expectedKey, info.ApiKey)
			assert.Equal(t, test.expectedMulti, info.ChannelIsMultiKey)
			assert.Equal(t, test.expectedIndex, info.ChannelMultiKeyIndex)
		})
	}
}

func TestResolveOriginTaskSynchronizesKeySelectionError(t *testing.T) {
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
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeChannelNoAvailableKey), taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
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

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, string(types.ErrorCodeModelPriceError), taskErr.Code)
	assert.True(t, taskErr.LocalError)
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

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	assert.ErrorContains(t, taskErr.Error, "failed to decode credentials")
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

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.False(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusBadGateway, taskErr.StatusCode)
	var transportErr *types.NewAPIError
	require.ErrorAs(t, taskErr.Error, &transportErr)
	assert.Equal(t, types.ErrorCodeDoRequestFailed, transportErr.GetErrorCode())
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

	_, taskErr := RelayTaskSubmit(ctx, info)

	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.Equal(t, string(types.ErrorCodeDoRequestFailed), taskErr.Code)
	assert.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	assert.ErrorContains(t, taskErr.Error, "invalid header field value")
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
