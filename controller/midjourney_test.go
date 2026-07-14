package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	projecti18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExecuteMidjourneyRoutingAttemptTreatsIdempotentReplayAsLocalShortCircuit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/imagine", bytes.NewReader([]byte(`{}`)))
	info := &relaycommon.RelayInfo{
		OriginModelName: "mj_imagine", UsingGroup: "default", TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}
	channel := &model.Channel{Id: 7001, Type: constant.ChannelTypeMidjourney, Name: "replay-channel"}

	outcome := executeMidjourneyRoutingAttempt(ctx, info, channel, func() relay.MidjourneyRelayOutcome {
		return relay.MidjourneyRelayOutcome{
			StatusCode: http.StatusOK, UpstreamAccepted: true, IdempotentReplay: true,
		}
	})

	assert.True(t, outcome.IdempotentReplay)
	assert.Nil(t, outcome.Error)
	assert.Equal(t, http.StatusOK, outcome.StatusCode)
}

func TestRunMidjourneyTaskUpdateOnceBatchesPollingRequests(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	previousUpdateTask := constant.UpdateTask
	constant.UpdateTask = true
	t.Cleanup(func() { constant.UpdateTask = previousUpdateTask })
	service.InitHttpClient()
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}, &model.MidjourneyBillingOperation{}, &model.Ability{}))

	var (
		mu         sync.Mutex
		batchSizes []int
		handlerErr error
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			IDs []string `json:"ids"`
		}
		if err := common.DecodeJson(r.Body, &request); err != nil {
			mu.Lock()
			handlerErr = err
			mu.Unlock()
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		batchSizes = append(batchSizes, len(request.IDs))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)
	require.NoError(t, db.Create(&model.Channel{
		Id: 801, Name: "midjourney-poll", BaseURL: &server.URL, Key: "stable-key",
		Status: common.ChannelStatusEnabled,
	}).Error)

	tasks := make([]model.Midjourney, midjourneyPollingBatchSize+1)
	for index := range tasks {
		tasks[index] = model.Midjourney{
			UserId: 1, MjId: fmt.Sprintf("task_public_%d", index),
			UpstreamTaskID: fmt.Sprintf("upstream_%d", index), ChannelId: 801,
			Status: "NOT_START", Progress: "0%", SubmitTime: time.Now().UnixMilli(),
		}
	}
	require.NoError(t, db.CreateInBatches(&tasks, 25).Error)
	model.InitChannelCache()

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Equal(t, 1, summary.ChannelsScanned)
	assert.Equal(t, len(tasks), summary.UnfinishedTasks)
	mu.Lock()
	require.NoError(t, handlerErr)
	assert.Equal(t, []int{midjourneyPollingBatchSize, 1}, batchSizes)
	mu.Unlock()
}

func TestMidjourneyPollHandlerKeepsDurableRefundRecoveryEnabled(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}, &model.MidjourneyBillingOperation{}))
	previousUpdateTask := constant.UpdateTask
	constant.UpdateTask = false
	t.Cleanup(func() { constant.UpdateTask = previousUpdateTask })

	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 1, MjId: "task_unfinished_disabled", Status: "IN_PROGRESS", Progress: "50%",
	}).Error)
	assert.False(t, (midjourneyPollHandler{}).Enabled(), "disabled polling must still suppress ordinary upstream polls")

	require.NoError(t, db.Create(&model.MidjourneyBillingOperation{
		MidjourneyID:  999,
		OperationKey:  "midjourney:999:refund:v1",
		Kind:          model.TaskBillingOperationKindRefund,
		State:         model.TaskBillingOperationStatePending,
		BillingSource: model.TaskBillingSourceWallet,
		RefundQuota:   100,
		LogState:      model.TaskBillingOperationLogPending,
	}).Error)
	assert.True(t, (midjourneyPollHandler{}).Enabled(), "durable refunds must recover even when upstream polling is disabled")
}

func TestRunMidjourneyTaskUpdateOnceRecoversRefundWithoutPollingWhenDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/midjourney-refund-recovery.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Midjourney{},
		&model.MidjourneyBillingOperation{},
	))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousUpdateTask := constant.UpdateTask
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	constant.UpdateTask = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.RedisEnabled = previousRedisEnabled
		constant.UpdateTask = previousUpdateTask
	})

	const id = 77
	require.NoError(t, db.Create(&model.User{
		Id: id, Username: "midjourney-recovery-user", AffCode: "midjourney-recovery-aff",
		Quota: 900, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: id, UserId: id, Key: "sk-midjourney-recovery", Name: "midjourney-recovery",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100,
	}).Error)
	task := &model.Midjourney{
		UserId: id, Action: constant.MjActionImagine, MjId: "task_midjourney_recovery",
		Status: "IN_PROGRESS", Progress: "50%", ChannelId: id, Quota: 100,
		Group: "default", BillingSource: service.BillingSourceWallet, TokenId: id,
		BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
	}
	require.NoError(t, db.Create(task).Error)
	observed := *task
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	observed.FailReason = "upstream failed"
	finalized, err := service.FinalizeMidjourneyFailure(context.Background(), &observed, task.Status)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	assert.False(t, model.HasUnfinishedMidjourneyTasks())
	unfinished := &model.Midjourney{
		UserId: id, MjId: "task_midjourney_polling_disabled",
		UpstreamTaskID: "upstream_midjourney_polling_disabled",
		Status:         "IN_PROGRESS", Progress: "50%", ChannelId: 999,
	}
	require.NoError(t, db.Create(unfinished).Error)

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Equal(t, 1, summary.BillingOperations)
	assert.Zero(t, summary.UnfinishedTasks)
	assert.Zero(t, summary.ChannelsScanned)
	var user model.User
	var token model.Token
	var persisted model.Midjourney
	require.NoError(t, db.First(&user, id).Error)
	require.NoError(t, db.First(&token, id).Error)
	require.NoError(t, db.First(&persisted, task.Id).Error)
	require.NoError(t, db.First(unfinished, unfinished.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, persisted.Quota)
	assert.Equal(t, "IN_PROGRESS", unfinished.Status)
}

func TestRunMidjourneyTaskUpdateOnceHonorsBillingLogRetryDeadline(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/midjourney-log-retry.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Midjourney{},
		&model.MidjourneyBillingOperation{},
	))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousUpdateTask := constant.UpdateTask
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	constant.UpdateTask = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.RedisEnabled = previousRedisEnabled
		constant.UpdateTask = previousUpdateTask
	})

	const userID = 78
	require.NoError(t, db.Create(&model.User{
		Id: userID, Username: "midjourney-log-retry-user", AffCode: "midjourney-log-retry-aff",
		Quota: 900, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: userID, UserId: userID, Key: "sk-midjourney-log-retry", Name: "midjourney-log-retry",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100,
	}).Error)
	task := &model.Midjourney{
		UserId: userID, Action: constant.MjActionImagine, MjId: "task_midjourney_log_retry",
		Status: "IN_PROGRESS", Progress: "50%", ChannelId: userID, Quota: 100,
		Group: "default", BillingSource: service.BillingSourceWallet, TokenId: userID,
		BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
	}
	require.NoError(t, db.Create(task).Error)
	observed := *task
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	observed.FailReason = "upstream failed"
	finalized, err := service.FinalizeMidjourneyFailure(context.Background(), &observed, task.Status)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	completedAt := time.Now()
	const owner = "midjourney-log-retry-fixture"
	_, claimed, err := model.ClaimMidjourneyBillingOperation(
		context.Background(), finalized.Operation.ID, owner, completedAt, time.Minute,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	operation, err := model.CompleteMidjourneyBillingOperation(
		context.Background(), finalized.Operation.ID, owner, completedAt,
	)
	require.NoError(t, err)
	retryAt := time.Now().Add(time.Minute).UnixMilli()
	require.NoError(t, db.Model(&model.MidjourneyBillingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"log_state":         model.TaskBillingOperationLogFailed,
		"log_attempts":      1,
		"log_next_retry_ms": retryAt,
	}).Error)
	assert.False(t, (midjourneyPollHandler{}).Enabled(), "a deferred log retry must not schedule empty polling passes")

	summary := runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Zero(t, summary.BillingOperations)
	var deferred model.MidjourneyBillingOperation
	require.NoError(t, db.First(&deferred, operation.ID).Error)
	assert.Equal(t, 1, deferred.LogAttempts)
	assert.Equal(t, model.TaskBillingOperationLogFailed, deferred.LogState)

	require.NoError(t, db.Model(&model.MidjourneyBillingOperation{}).Where("id = ?", operation.ID).
		Update("log_next_retry_ms", time.Now().Add(-time.Second).UnixMilli()).Error)
	assert.True(t, (midjourneyPollHandler{}).Enabled())
	summary = runMidjourneyTaskUpdateOnce(context.Background(), nil)
	assert.Equal(t, 1, summary.BillingOperations)
	require.NoError(t, db.First(&deferred, operation.ID).Error)
	assert.Equal(t, 2, deferred.LogAttempts)
	assert.Equal(t, model.TaskBillingOperationLogWritten, deferred.LogState)
	assert.Zero(t, deferred.LogNextRetryMs)
}

func TestCancelMidjourneyPinnedRoutingAdmissionReleasesPendingCapacity(t *testing.T) {
	tracker, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 4, IdleTTL: time.Hour, Shards: 1,
	})
	require.NoError(t, err)
	key := channelrouting.CapacityKey{PoolID: 1, MemberID: 2, Model: "mj_upscale"}
	reservation, err := tracker.TryReserve(
		key,
		channelrouting.Demand{Inflight: 1},
		channelrouting.Limit{Inflight: 1},
	)
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, service.SetRoutingCapacityReservation(ctx, reservation))

	cancelMidjourneyPinnedRoutingAdmission(ctx, 99)

	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Zero(t, snapshot.PendingReservations)
	require.NoError(t, service.CommitRoutingCapacityReservation(ctx))
}

func TestGroupMidjourneyPollingTasksIsolatesChannelCredentialAndGeneration(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tasks := []*model.Midjourney{
		{Id: 1, MjId: "task_public_1", UpstreamTaskID: "same-upstream", ChannelId: 10, RoutingCredentialID: 100, ChannelGeneration: "generation-a"},
		{Id: 2, MjId: "task_public_2", UpstreamTaskID: "same-upstream", ChannelId: 10, RoutingCredentialID: 101, ChannelGeneration: "generation-a"},
		{Id: 3, MjId: "task_public_3", UpstreamTaskID: "same-upstream", ChannelId: 11, RoutingCredentialID: 100, ChannelGeneration: "generation-b"},
		{Id: 4, MjId: "legacy-upstream", ChannelId: 12},
	}

	targets, groups, unrecoverable, ambiguous := groupMidjourneyPollingTasks(tasks, now)
	require.Len(t, targets, 4)
	assert.Empty(t, unrecoverable)
	assert.Empty(t, ambiguous)
	for _, target := range targets {
		assert.Len(t, groups[target], 1)
	}
	assert.Equal(t, 10, targets[0].ChannelID)
	assert.Equal(t, 100, targets[0].CredentialID)
	assert.Equal(t, 101, targets[1].CredentialID)
	assert.Equal(t, 11, targets[2].ChannelID)
	assert.Equal(t, 12, targets[3].ChannelID)
}

func TestGroupMidjourneyPollingTasksAllowsSubmissionRecoveryWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	recent := &model.Midjourney{
		Id: 1, MjId: "task_recent", ChannelId: 10, ChannelGeneration: "generation-a",
		SubmitTime: now.Add(-time.Minute).UnixMilli(),
	}
	stale := &model.Midjourney{
		Id: 2, MjId: "task_stale", ChannelId: 10, ChannelGeneration: "generation-a",
		SubmitTime: now.Add(-3 * time.Minute).UnixMilli(),
	}
	missingPublicID := &model.Midjourney{Id: 3, ChannelId: 10}

	targets, groups, unrecoverable, ambiguous := groupMidjourneyPollingTasks(
		[]*model.Midjourney{recent, stale, missingPublicID}, now,
	)
	assert.Empty(t, targets)
	assert.Empty(t, groups)
	assert.ElementsMatch(t, []*model.Midjourney{stale, missingPublicID}, unrecoverable)
	assert.Empty(t, ambiguous)
}

func TestGroupMidjourneyPollingTasksRejectsDuplicateCompositeIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	first := &model.Midjourney{
		Id: 1, MjId: "task_public_1", UpstreamTaskID: "same-upstream",
		ChannelId: 10, RoutingCredentialID: 100, ChannelGeneration: "generation-a",
	}
	second := &model.Midjourney{
		Id: 2, MjId: "task_public_2", UpstreamTaskID: "same-upstream",
		ChannelId: 10, RoutingCredentialID: 100, ChannelGeneration: "generation-a",
	}
	isolated := &model.Midjourney{
		Id: 3, MjId: "task_public_3", UpstreamTaskID: "same-upstream",
		ChannelId: 10, RoutingCredentialID: 101, ChannelGeneration: "generation-a",
	}

	targets, groups, unrecoverable, ambiguous := groupMidjourneyPollingTasks(
		[]*model.Midjourney{first, second, isolated}, now,
	)

	require.Len(t, targets, 1)
	assert.Equal(t, 101, targets[0].CredentialID)
	assert.Equal(t, []*model.Midjourney{isolated}, groups[targets[0]])
	assert.Empty(t, unrecoverable)
	assert.ElementsMatch(t, []*model.Midjourney{first, second}, ambiguous)
}

func TestPrepareMidjourneyOriginChannelRejectsLostRoutingGroupPermission(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 77, MjId: "task_restricted", Group: "restricted", ChannelId: 901,
		Status: "SUCCESS", Progress: "100%",
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/action", nil)
	common.SetContextKey(ctx, constant.ContextKeyUserId, 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")

	_, _, apiErr := prepareMidjourneyOriginChannel(ctx, "task_restricted", "mj_variation", true)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Contains(t, apiErr.Error(), "task_routing_group_forbidden")
}

func TestPrepareMidjourneyOriginChannelRejectsDifferentTokenChannel(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	channel := &model.Channel{
		Id: 902, Name: "midjourney origin", Key: "stable-key", Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 77, MjId: "task_channel_restricted", Group: "default", ChannelId: channel.Id,
		Action: constant.MjActionImagine, Status: "SUCCESS", Progress: "100%",
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/action", nil)
	common.SetContextKey(ctx, constant.ContextKeyUserId, 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"mj_variation": true})
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "903")

	_, _, apiErr := prepareMidjourneyOriginChannel(ctx, "task_channel_restricted", "mj_variation", true)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, "access_denied", string(apiErr.GetErrorCode()))
}

func TestRelayMidjourneyFetchEnforcesTokenModelLimit(t *testing.T) {
	require.NoError(t, projecti18n.Init())
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	require.NoError(t, db.Create(&model.Midjourney{
		UserId: 77, MjId: "task_fetch_restricted", ChannelId: 908,
		Action: constant.MjActionImagine, Status: "SUCCESS", Progress: "100%",
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mj/task/task_fetch_restricted/fetch", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "task_fetch_restricted"}}
	ctx.Set("relay_mode", relayconstant.RelayModeMidjourneyTaskFetch)
	common.SetContextKey(ctx, constant.ContextKeyUserId, 77)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"mj_variation": true})

	RelayMidjourney(ctx)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "no access to model")
}

func TestReadMidjourneyPollingResponseEnforcesHardLimit(t *testing.T) {
	body, err := readMidjourneyPollingResponse(bytes.NewBufferString("1234"), 4)
	require.NoError(t, err)
	assert.Equal(t, "1234", string(body))

	_, err = readMidjourneyPollingResponse(bytes.NewBufferString("12345"), 4)
	assert.ErrorIs(t, err, errMidjourneyPollingResponseTooLarge)
}
