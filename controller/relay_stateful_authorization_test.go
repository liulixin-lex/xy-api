package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTaskFetchAuthorizationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/task-fetch-authorization.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))

	previousDB := model.DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, previousLogType)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.MemoryCacheEnabled = previousMemoryCache
	})
	return db
}

func TestRelayTaskRemixRejectsTokenPinnedToDifferentChannel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/task-routing-authorization.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))

	previousDB := model.DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, previousLogType)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.MemoryCacheEnabled = previousMemoryCache
	})

	channel := &model.Channel{
		Id: 7101, Name: "origin channel", Key: "origin-key", Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task_origin_authorized", UserId: 77, Group: "default", ChannelId: channel.Id,
		Properties: model.Properties{OriginModelName: "video-model"},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "video_id", Value: "task_origin_authorized"}}
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/videos/task_origin_authorized/remix",
		strings.NewReader(`{"prompt":"remix this"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyUserId, 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"video-model": true})
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "7102")

	RelayTask(ctx)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"access_denied"`)
}

func TestRelayTaskFetchAuthorizesSunoByIDUsingStoredModel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupTaskFetchAuthorizationTestDB(t)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "suno-task-a", UserId: 77,
		Properties: model.Properties{OriginModelName: "suno-model-a"},
	}).Error)

	for _, test := range []struct {
		name       string
		models     map[string]bool
		statusCode int
	}{
		{name: "allowed", models: map[string]bool{"suno-model-a": true}, statusCode: http.StatusOK},
		{name: "forbidden", models: map[string]bool{"suno-model-b": true}, statusCode: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/suno/fetch/suno-task-a", nil)
			ctx.Params = gin.Params{{Key: "id", Value: "suno-task-a"}}
			ctx.Set("relay_mode", relayconstant.RelayModeSunoFetchByID)
			ctx.Set("id", 77)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, test.models)
			common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "9999")

			RelayTaskFetch(ctx)

			assert.Equal(t, test.statusCode, recorder.Code)
			if test.statusCode == http.StatusOK {
				assert.Contains(t, recorder.Body.String(), `"code":"success"`)
			} else {
				assert.Contains(t, recorder.Body.String(), `"code":"access_denied"`)
			}
		})
	}
}

func TestRelayTaskFetchAuthorizesEverySunoBatchModel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupTaskFetchAuthorizationTestDB(t)
	require.NoError(t, db.Create([]*model.Task{
		{TaskID: "suno-batch-a", UserId: 77, Properties: model.Properties{OriginModelName: "suno-model-a"}},
		{TaskID: "suno-batch-b", UserId: 77, Properties: model.Properties{OriginModelName: "suno-model-b"}},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/suno/fetch",
		strings.NewReader(`{"ids":["suno-batch-a","suno-batch-b"]}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("relay_mode", relayconstant.RelayModeSunoFetch)
	ctx.Set("id", 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"suno-model-a": true})

	RelayTaskFetch(ctx)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"access_denied"`)
}

func TestRelayTaskFetchAllowsEmptySunoBatchWithModelLimits(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	setupTaskFetchAuthorizationTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/suno/fetch", strings.NewReader(`{"ids":[]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("relay_mode", relayconstant.RelayModeSunoFetch)
	ctx.Set("id", 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"suno-model-a": true})

	RelayTaskFetch(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"code":"success","message":"","data":[]}`, recorder.Body.String())
}

func TestRelayTaskFetchTerminalVideoUsesStoredModelWithoutSpecificChannel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupTaskFetchAuthorizationTestDB(t)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task_terminal_origin", UserId: 77, ChannelId: 7103,
		Status:     model.TaskStatusSuccess,
		Properties: model.Properties{OriginModelName: "video-model"},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/task_terminal_origin", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_terminal_origin"}}
	ctx.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
	ctx.Set("id", 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"video-model": true})
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "7104")

	RelayTaskFetch(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"task_id":"task_terminal_origin"`)
}

func TestRelayTaskFetchRejectsRealtimePollBeforeUpstreamIO(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := setupTaskFetchAuthorizationTestDB(t)
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"done":false}`))
	}))
	t.Cleanup(server.Close)

	channel := &model.Channel{
		Id: 7103, Type: constant.ChannelTypeGemini, Name: "realtime origin", Key: "origin-key",
		Status: common.ChannelStatusEnabled, BaseURL: &server.URL,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "task_realtime_origin", UserId: 77, ChannelId: channel.Id,
		Status:      model.TaskStatusInProgress,
		Properties:  model.Properties{OriginModelName: "video-model"},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-realtime"},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/task_realtime_origin", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_realtime_origin"}}
	ctx.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
	ctx.Set("id", 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"video-model": true})
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "7104")

	RelayTaskFetch(ctx)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"access_denied"`)
	assert.Zero(t, upstreamCalls.Load())
}
