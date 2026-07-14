package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskPollingFetchAdaptor struct {
	mu           sync.Mutex
	taskIDs      []string
	keys         []string
	fetched      chan string
	blockTaskID  string
	blockStarted chan struct{}
	releaseBlock chan struct{}
	blockOnce    sync.Once
}

func TestSweepTimedOutHistoricalVersionZeroRefundBoundary(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })

	tests := []struct {
		name                string
		id                  int
		submitTime          int64
		expectedQuota       int
		expectedKind        string
		expectPreserveAudit bool
	}{
		{name: "before cutoff", id: 320, submitTime: historicalTaskAutomaticRefundCutoff - 1,
			expectedQuota: 900, expectedKind: model.TaskBillingOperationKindNoop, expectPreserveAudit: true},
		{name: "at cutoff", id: 321, submitTime: historicalTaskAutomaticRefundCutoff,
			expectedQuota: 1000, expectedKind: model.TaskBillingOperationKindRefund},
		{name: "missing submit time", id: 322, submitTime: 0,
			expectedQuota: 900, expectedKind: model.TaskBillingOperationKindNoop, expectPreserveAudit: true},
	}
	taskIDs := make(map[int]int64, len(tests))
	for _, test := range tests {
		require.NoError(t, model.DB.Create(&model.User{
			Id: test.id, Username: fmt.Sprintf("timeout-v0-user-%d", test.id),
			AffCode: fmt.Sprintf("timeout-v0-aff-%d", test.id), Quota: 900,
			Status: common.UserStatusEnabled,
		}).Error)
		seedToken(t, test.id, test.id, fmt.Sprintf("sk-timeout-v0-%d", test.id), 900)
		seedChannel(t, test.id)
		require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", test.id).Update("used_quota", 100).Error)
		task := makeTask(test.id, test.id, 100, test.id, BillingSourceWallet, 0)
		task.TaskID = fmt.Sprintf("task_timeout_v0_%d", test.id)
		task.Platform = constant.TaskPlatform("video")
		task.SubmitTime = test.submitTime
		require.NoError(t, model.DB.Create(task).Error)
		taskIDs[test.id] = task.ID
	}

	sweepTimedOutTasks(context.Background())

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expectedQuota, getUserQuota(t, test.id))
			operation, err := model.GetTaskBillingOperationByTaskID(context.Background(), taskIDs[test.id])
			require.NoError(t, err)
			assert.Equal(t, test.expectedKind, operation.Kind)
			assert.Equal(t, model.TaskBillingOperationStateCompleted, operation.State)
			if test.expectPreserveAudit {
				assert.Contains(t, operation.LastError, "charge retained")
			} else {
				assert.Empty(t, operation.LastError)
			}
		})
	}
}

func (a *taskPollingFetchAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *taskPollingFetchAdaptor) FetchTask(ctx context.Context, _ string, key string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	if taskID == a.blockTaskID && a.releaseBlock != nil {
		a.blockOnce.Do(func() {
			if a.blockStarted != nil {
				close(a.blockStarted)
			}
		})
		select {
		case <-a.releaseBlock:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}

	a.mu.Lock()
	a.taskIDs = append(a.taskIDs, taskID)
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	if a.fetched != nil {
		select {
		case a.fetched <- taskID:
		default:
		}
	}

	response := dto.TaskResponse[model.Task]{
		Code: dto.TaskSuccessCode,
		Data: model.Task{
			TaskID:   taskID,
			Status:   model.TaskStatusInProgress,
			Progress: "30%",
		},
	}
	responseBody, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
	}, nil
}

func (a *taskPollingFetchAdaptor) ParseTaskResult(context.Context, []byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}, nil
}

func (a *taskPollingFetchAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

func (a *taskPollingFetchAdaptor) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.taskIDs)
}

func (a *taskPollingFetchAdaptor) fetchedTaskIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.taskIDs...)
}

func (a *taskPollingFetchAdaptor) fetchedKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.keys...)
}

func seedTaskPollingChannel(t *testing.T, id int, disableSleep bool) {
	t.Helper()
	ch := &model.Channel{
		Id:     id,
		Type:   constant.ChannelTypeKling,
		Name:   "polling_channel",
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}
	if disableSleep {
		ch.SetOtherSettings(dto.ChannelOtherSettings{DisableTaskPollingSleep: true})
	}
	require.NoError(t, model.DB.Create(ch).Error)
}

func seedPollingTask(t *testing.T, channelID int, publicID string, upstreamID string) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    publicID,
		Platform:  constant.TaskPlatform("kling"),
		UserId:    1,
		ChannelId: channelID,
		Action:    constant.TaskActionGenerate,
		Status:    model.TaskStatusInProgress,
		Progress:  "30%",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: upstreamID,
		},
	}
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestFinalizePolledTaskMovesUnverifiedDynamicUsageToManualReview(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.AsyncBillingReservation{}, &model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{}, &model.BillingStatsProjection{}, &model.BillingLogProjection{},
	))
	for _, table := range []string{
		"async_billing_manual_resolutions", "async_billing_attempts", "async_billing_reservations",
		"billing_stats_projections", "billing_log_projections",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"async_billing_manual_resolutions", "async_billing_attempts", "async_billing_reservations",
			"billing_stats_projections", "billing_log_projections",
		} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
	})
	now := time.Now()
	const userID = 9981
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: "poll-terminal-usage", AffCode: "poll-terminal-usage",
		Status: common.UserStatusEnabled, Quota: 900,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		Id: userID, UserId: userID, Key: "sk-poll-terminal-usage", Name: "poll-terminal-usage",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	reservation := model.AsyncBillingReservation{
		ID: 9981, ReservationKey: "poll-terminal-usage", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "task_poll_terminal_usage",
		State: model.AsyncBillingReservationStateAccepted, UserID: userID, TokenID: userID,
		FundingSource: model.TaskBillingSourceWallet, InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		AcceptedProjectionState: model.AsyncBillingAcceptedProjectionCompleted,
		UpstreamTaskID:          "provider-poll-terminal-usage",
		CreatedTimeMs:           now.Add(-time.Minute).UnixMilli(), UpdatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, model.DB.Create(&reservation).Error)
	task := model.Task{
		TaskID: reservation.PublicTaskID, Platform: constant.TaskPlatform("kling"),
		UserId: userID, ChannelId: 9981, Status: model.TaskStatusInProgress, Progress: "50%",
		SubmitTime: now.Add(-time.Minute).Unix(), StartTime: now.Add(-30 * time.Second).Unix(),
		PrivateData: model.TaskPrivateData{
			BillingProtocolVersion:    model.TaskBillingProtocolVersion,
			AsyncBillingReservationID: reservation.ID,
			BillingSource:             model.TaskBillingSourceWallet, TokenId: userID,
			BillingContext: &model.TaskBillingContext{
				ModelRatio: 1, GroupRatio: 1, OriginModelName: "dynamic-video",
			},
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(100))
	require.NoError(t, model.DB.Create(&task).Error)
	require.NoError(t, model.DB.Model(&model.AsyncBillingReservation{}).Where("id = ?", reservation.ID).
		Update("task_id", task.ID).Error)

	observation := TaskFinalizationObservation{
		TaskID: task.ID, TerminalStatus: model.TaskStatusSuccess, Progress: "100%",
		SubmitTime: task.SubmitTime, StartTime: task.StartTime, FinishTime: now.Unix(),
		Data: json.RawMessage(`{"result":"ready"}`), TotalTokens: 0,
	}
	stored, err := finalizePolledTask(context.Background(), observation)
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), stored.Status)
	assert.Equal(t, "100%", stored.Progress)
	var reviewed model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&reviewed, reservation.ID).Error)
	assert.Equal(t, model.AsyncBillingReservationStateManualReview, reviewed.State)
	assert.Equal(t, model.AsyncBillingReviewKindTerminalUsage, reviewed.ManualReviewKind)
	assert.Equal(t, 100, reviewed.CurrentQuota)
	version := reviewed.ReviewVersion

	stored, err = finalizePolledTask(context.Background(), observation)
	require.NoError(t, err)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), stored.Status)
	require.NoError(t, model.DB.First(&reviewed, reservation.ID).Error)
	assert.Equal(t, version, reviewed.ReviewVersion)
	var operationCount int64
	require.NoError(t, model.DB.Model(&model.TaskBillingOperation{}).
		Where("reservation_id = ?", reservation.ID).Count(&operationCount).Error)
	assert.Zero(t, operationCount)
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.First(&token, userID).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
}

func TestRunTaskPollingOnceRecoversBillingWithoutPollingWhenDisabled(t *testing.T) {
	truncate(t)

	previousUpdateTask := constant.UpdateTask
	previousFactory := GetTaskAdaptorFunc
	constant.UpdateTask = false
	t.Cleanup(func() {
		constant.UpdateTask = previousUpdateTask
		GetTaskAdaptorFunc = previousFactory
	})

	terminal := &model.Task{
		TaskID: "task_terminal_billing", UserId: 1, ChannelId: 0,
		Status: model.TaskStatusSuccess, Progress: "100%",
	}
	require.NoError(t, model.DB.Create(terminal).Error)
	require.NoError(t, model.DB.Create(&model.TaskBillingOperation{
		TaskID: terminal.ID, OperationKey: "task:disabled-poll:terminal:v1",
		TerminalStatus: model.TaskStatusSuccess,
		Kind:           model.TaskBillingOperationKindNoop,
		State:          model.TaskBillingOperationStatePending,
		UserID:         terminal.UserId,
		BillingSource:  model.TaskBillingSourceWallet,
		LogState:       model.TaskBillingOperationLogNotRequired,
	}).Error)
	seedPollingTask(t, 701, "task_unfinished_disabled", "upstream_disabled")

	var adaptorRequests int
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor {
		adaptorRequests++
		return &taskPollingFetchAdaptor{}
	}

	summary := RunTaskPollingOnce(context.Background(), nil)
	assert.Equal(t, 1, summary.BillingOperationsProcessed)
	assert.Zero(t, summary.UnfinishedTasks)
	assert.Zero(t, summary.PlatformsScanned)
	assert.Zero(t, adaptorRequests)

	operation, err := model.GetTaskBillingOperationByTaskID(context.Background(), terminal.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, operation.State)
	var unfinished model.Task
	require.NoError(t, model.DB.Where("task_id = ?", "task_unfinished_disabled").First(&unfinished).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), unfinished.Status)
}

func TestUpdateVideoTasksDefaultSleepWaitsBetweenTasks(t *testing.T) {
	truncate(t)

	const channelID = 101
	seedTaskPollingChannel(t, channelID, false)
	first := seedPollingTask(t, channelID, "task_public_1", "upstream_1")
	second := seedPollingTask(t, channelID, "task_public_2", "upstream_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), []*model.Task{first, second})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, adaptor.fetchCount())
}

func TestUpdateVideoTasksCanSkipPollingSleepPerChannel(t *testing.T) {
	truncate(t)

	const channelID = 102
	seedTaskPollingChannel(t, channelID, true)
	first := seedPollingTask(t, channelID, "task_public_3", "upstream_3")
	second := seedPollingTask(t, channelID, "task_public_4", "upstream_4")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), []*model.Task{first, second})

	require.NoError(t, err)
	assert.Equal(t, 2, adaptor.fetchCount())
}

func TestUpdateVideoTasksDefaultSleepDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const firstChannelID = 201
	const secondChannelID = 202
	seedTaskPollingChannel(t, firstChannelID, false)
	seedTaskPollingChannel(t, secondChannelID, false)
	firstChannelFirst := seedPollingTask(t, firstChannelID, "task_public_5", "upstream_a_1")
	firstChannelSecond := seedPollingTask(t, firstChannelID, "task_public_6", "upstream_a_2")
	secondChannelFirst := seedPollingTask(t, secondChannelID, "task_public_7", "upstream_b_1")
	secondChannelSecond := seedPollingTask(t, secondChannelID, "task_public_8", "upstream_b_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), []*model.Task{
		firstChannelFirst,
		firstChannelSecond,
		secondChannelFirst,
		secondChannelSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_a_1", "upstream_b_1"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksSlowChannelDoesNotBlockOtherChannels(t *testing.T) {
	truncate(t)

	const slowChannelID = 251
	const fastChannelID = 252
	seedTaskPollingChannel(t, slowChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	slowTask := seedPollingTask(t, slowChannelID, "task_public_slow", "upstream_slow_1")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_fast_1", "upstream_fast_parallel_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_fast_2", "upstream_fast_parallel_2")
	slowTaskID := slowTask.GetUpstreamTaskID()
	fastFirstID := fastFirst.GetUpstreamTaskID()
	fastSecondID := fastSecond.GetUpstreamTaskID()

	adaptor := &taskPollingFetchAdaptor{
		fetched:      make(chan string, 4),
		blockTaskID:  slowTaskID,
		blockStarted: make(chan struct{}),
		releaseBlock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedTask := func() {
		releaseOnce.Do(func() {
			close(adaptor.releaseBlock)
		})
	}
	t.Cleanup(releaseBlockedTask)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	errCh := make(chan error, 1)
	gopool.Go(func() {
		errCh <- UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), []*model.Task{
			slowTask,
			fastFirst,
			fastSecond,
		})
	})

	select {
	case <-adaptor.blockStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow channel did not start blocking")
	}

	require.Eventually(t, func() bool {
		fetchedTaskIDs := adaptor.fetchedTaskIDs()
		return len(fetchedTaskIDs) == 2 &&
			fetchedTaskIDs[0] == fastFirstID &&
			fetchedTaskIDs[1] == fastSecondID
	}, 500*time.Millisecond, 10*time.Millisecond)

	releaseBlockedTask()
	require.NoError(t, <-errCh)
	assert.ElementsMatch(t, []string{
		slowTaskID,
		fastFirstID,
		fastSecondID,
	}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksCancelsInFlightFetch(t *testing.T) {
	truncate(t)

	const channelID = 253
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_public_cancel", "upstream_cancel")
	adaptor := &taskPollingFetchAdaptor{
		blockTaskID:  task.GetUpstreamTaskID(),
		blockStarted: make(chan struct{}),
		releaseBlock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedTask := func() {
		releaseOnce.Do(func() {
			close(adaptor.releaseBlock)
		})
	}
	t.Cleanup(releaseBlockedTask)
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	gopool.Go(func() {
		result <- UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), []*model.Task{task})
	})

	select {
	case <-adaptor.blockStarted:
	case <-time.After(time.Second):
		require.Fail(t, "task fetch did not start")
	}
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "task worker stayed blocked after cancellation")
	}
}

func TestUpdateVideoTasksMixedChannelSleepSettings(t *testing.T) {
	truncate(t)

	const sleepyChannelID = 301
	const fastChannelID = 302
	seedTaskPollingChannel(t, sleepyChannelID, false)
	seedTaskPollingChannel(t, fastChannelID, true)
	sleepyFirst := seedPollingTask(t, sleepyChannelID, "task_public_9", "upstream_sleepy_1")
	sleepySecond := seedPollingTask(t, sleepyChannelID, "task_public_10", "upstream_sleepy_2")
	fastFirst := seedPollingTask(t, fastChannelID, "task_public_11", "upstream_fast_1")
	fastSecond := seedPollingTask(t, fastChannelID, "task_public_12", "upstream_fast_2")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("kling"), []*model.Task{
		sleepyFirst,
		sleepySecond,
		fastFirst,
		fastSecond,
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ElementsMatch(t, []string{"upstream_sleepy_1", "upstream_fast_1", "upstream_fast_2"}, adaptor.fetchedTaskIDs())
}

func TestUpdateVideoTasksKeepsSharedUpstreamIDsSeparatedByChannel(t *testing.T) {
	truncate(t)

	const firstChannelID = 401
	const secondChannelID = 402
	seedTaskPollingChannel(t, firstChannelID, true)
	seedTaskPollingChannel(t, secondChannelID, true)
	first := seedPollingTask(t, firstChannelID, "task_shared_a", "shared_upstream_id")
	second := seedPollingTask(t, secondChannelID, "task_shared_b", "shared_upstream_id")
	first.Progress = "0%"
	second.Progress = "0%"
	require.NoError(t, model.DB.Model(first).Update("progress", first.Progress).Error)
	require.NoError(t, model.DB.Model(second).Update("progress", second.Progress).Error)

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	require.NoError(t, UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), []*model.Task{first, second}))
	assert.Equal(t, 2, adaptor.fetchCount())
	assert.Equal(t, "30%", first.Progress)
	assert.Equal(t, "30%", second.Progress)

	firstStored, exists, err := model.GetByTaskId(first.UserId, first.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	secondStored, exists, err := model.GetByTaskId(second.UserId, second.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, "30%", firstStored.Progress)
	assert.Equal(t, "30%", secondStored.Progress)
}

func TestUpdateVideoTasksFailsClosedForLegacyMultiKeyTask(t *testing.T) {
	truncate(t)

	const channelID = 403
	channel := &model.Channel{
		Id:     channelID,
		Type:   constant.ChannelTypeKling,
		Name:   "legacy_multi_key",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}
	require.NoError(t, model.DB.Create(channel).Error)
	task := seedPollingTask(t, channelID, "task_legacy_multi", "upstream_legacy_multi")

	adaptor := &taskPollingFetchAdaptor{}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := UpdateVideoTasks(context.Background(), constant.TaskPlatform("kling"), []*model.Task{task})

	require.Error(t, err)
	assert.ErrorIs(t, err, errTaskPollingCredentialIdentityMissing)
	assert.Zero(t, adaptor.fetchCount())
}

func TestResolveTaskPollingCredentialUsesStableCredentialAfterReorder(t *testing.T) {
	channel := &model.Channel{
		Id:  404,
		Key: "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}

	credential, err := resolveTaskPollingCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{RoutingCredentialID: 77},
		func(_ context.Context, actualChannel *model.Channel, credentialID int) (string, int, error) {
			assert.Same(t, channel, actualChannel)
			assert.Equal(t, 77, credentialID)
			return "key-a", 1, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "key-a", credential)

	_, err = resolveTaskPollingCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{},
		func(context.Context, *model.Channel, int) (string, int, error) {
			t.Fatal("legacy multi-key task must fail before credential resolution")
			return "", 0, nil
		},
	)
	assert.ErrorIs(t, err, errTaskPollingCredentialIdentityMissing)

	credential, err = resolveTaskPollingCredentialWithResolver(
		context.Background(),
		channel,
		model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
			Key:                    " historical-poll-key ",
		},
		func(context.Context, *model.Channel, int) (string, int, error) {
			t.Fatal("historical plaintext fallback must not call stable credential resolution")
			return "", 0, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "historical-poll-key", credential)

	_, err = resolveTaskPollingCredentialWithResolver(
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
	assert.ErrorIs(t, err, errTaskPollingCredentialIdentityMissing)
}

func TestGroupTaskPollingTargetsSeparatesSharedIDByCredential(t *testing.T) {
	first := &model.Task{
		ChannelId: 501,
		PrivateData: model.TaskPrivateData{
			RoutingCredentialID: 1001,
			UpstreamTaskID:      "shared-id",
		},
	}
	second := &model.Task{
		ChannelId: 501,
		PrivateData: model.TaskPrivateData{
			RoutingCredentialID: 1002,
			UpstreamTaskID:      "shared-id",
		},
	}

	targets, groups := groupTaskPollingTargets([]*model.Task{first, second})

	require.Len(t, targets, 2)
	assert.Equal(t, taskPollingTarget{ChannelID: 501, CredentialID: 1001}, targets[0])
	assert.Equal(t, taskPollingTarget{ChannelID: 501, CredentialID: 1002}, targets[1])
	assert.Equal(t, first, groups[targets[0]][0])
	assert.Equal(t, second, groups[targets[1]][0])
	assert.NotEqual(t,
		taskPollingKey{taskPollingTarget: targets[0], UpstreamTaskID: "shared-id"},
		taskPollingKey{taskPollingTarget: targets[1], UpstreamTaskID: "shared-id"},
	)
}

func TestGroupTaskPollingTargetsSeparatesHistoricalPlaintextCredentials(t *testing.T) {
	first := &model.Task{
		ChannelId: 502,
		PrivateData: model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
			Key:                    "legacy-key-a",
			UpstreamTaskID:         "shared-id",
		},
	}
	second := &model.Task{
		ChannelId: 502,
		PrivateData: model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
			Key:                    "legacy-key-b",
			UpstreamTaskID:         "shared-id",
		},
	}

	targets, groups := groupTaskPollingTargets([]*model.Task{first, second})

	require.Len(t, targets, 2)
	assert.NotEmpty(t, targets[0].LegacyCredentialDigest)
	assert.NotEmpty(t, targets[1].LegacyCredentialDigest)
	assert.NotEqual(t, targets[0].LegacyCredentialDigest, targets[1].LegacyCredentialDigest)
	assert.Len(t, groups[targets[0]], 1)
	assert.Len(t, groups[targets[1]], 1)
	assert.NotContains(t, targets[0].LegacyCredentialDigest, "legacy-key")
	assert.NotContains(t, targets[1].LegacyCredentialDigest, "legacy-key")
}

type sunoBusinessFailureAdaptor struct{}

func (sunoBusinessFailureAdaptor) Init(*relaycommon.RelayInfo) {}

func (sunoBusinessFailureAdaptor) FetchTask(context.Context, string, string, map[string]any, string) (*http.Response, error) {
	body, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code:    "provider_error",
		Message: "provider rejected polling request",
		Data:    []dto.SunoDataResponse{},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (sunoBusinessFailureAdaptor) ParseTaskResult(context.Context, []byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (sunoBusinessFailureAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func TestUpdateSunoTasksReturnsBusinessFailure(t *testing.T) {
	truncate(t)

	const channelID = 405
	seedTaskPollingChannel(t, channelID, true)
	task := seedPollingTask(t, channelID, "task_suno_failure", "upstream_suno_failure")
	task.Platform = constant.TaskPlatformSuno

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return sunoBusinessFailureAdaptor{} }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	err := UpdateSunoTasks(context.Background(), []*model.Task{task})

	require.Error(t, err)
	assert.ErrorContains(t, err, "provider_error")
}

func TestReadTaskPollingResponseEnforcesBound(t *testing.T) {
	body, err := readTaskPollingResponse(bytes.NewBufferString("1234"), 4)
	require.NoError(t, err)
	assert.Equal(t, []byte("1234"), body)

	_, err = readTaskPollingResponse(bytes.NewBufferString("12345"), 4)
	require.Error(t, err)
	assert.ErrorIs(t, err, errTaskPollingResponseTooLarge)
}

func TestFailTaskPollingTargetUsesStatusCAS(t *testing.T) {
	truncate(t)

	const channelID = 406
	seedTaskPollingChannel(t, channelID, true)
	seedPollingTask(t, channelID, "task_cas_failure", "upstream_cas_failure")
	first, exists, err := model.GetByTaskId(1, "task_cas_failure")
	require.NoError(t, err)
	require.True(t, exists)
	second, exists, err := model.GetByTaskId(1, "task_cas_failure")
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, failTaskPollingTarget(context.Background(), []*model.Task{first}, "channel unavailable"))
	require.NoError(t, failTaskPollingTarget(context.Background(), []*model.Task{second}, "second poller"))

	stored, exists, err := model.GetByTaskId(1, "task_cas_failure")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), stored.Status)
	assert.Equal(t, "channel unavailable", stored.FailReason)
	assert.Equal(t, taskcommon.ProgressComplete, stored.Progress)
}
