package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	routingBillingIsolationModel = "routing-billing-isolation-model"
	routingBillingIsolationGroup = "routing-billing-isolation-group"
	routingBillingIsolationUser  = "routing-billing-isolation-user"
)

type synchronousBillingIsolationResult struct {
	PreConsumedQuota int
	WalletAfterPre   int
	FinalWallet      int
	FinalTokenQuota  int
	LogQuota         int
	GroupRatio       float64
	UserGroupRatio   float64
	OtherRatios      map[string]float64
	LogOther         map[string]any
}

type acceptedBillingIsolationTask struct {
	Reservation  *model.AsyncBillingReservation
	Task         *model.Task
	Audit        model.AsyncBillingAcceptedAuditSnapshot
	Replay       model.AsyncBillingReplaySpec
	InitialQuota int
}

func prepareRoutingBillingIsolationConfiguration(t *testing.T, channelID int, multiplier float64) model.RoutingChannelConfiguration {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(
		&model.RoutingChannelConfiguration{},
		&model.RoutingConfigurationEpoch{},
		&model.RoutingChannelConfigurationOutbox{},
		&model.RoutingControlAudit{},
	))
	for _, table := range []string{
		"routing_channel_configuration_outbox",
		"routing_control_audits",
		"routing_channel_configurations",
		"routing_configuration_epochs",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"routing_channel_configuration_outbox",
			"routing_control_audits",
			"routing_channel_configurations",
			"routing_configuration_epochs",
		} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
	})

	require.NoError(t, model.EnsureRoutingConfigurationEpoch(model.DB))
	configuration, err := model.NewDefaultRoutingChannelConfiguration(channelID, time.Now().Unix())
	require.NoError(t, err)
	configuration.UpstreamCostMultiplier = multiplier
	configuration.CostSource = model.RoutingChannelCostSourceManual
	configuration.CostConfirmed = true
	configuration.UpdatedBy = 9001
	require.True(t, model.ValidRoutingChannelConfiguration(configuration))
	require.NoError(t, model.DB.Create(&configuration).Error)
	return configuration
}

func updateRoutingBillingIsolationMultiplier(
	t *testing.T,
	configuration model.RoutingChannelConfiguration,
	multiplier float64,
) model.RoutingChannelConfiguration {
	t.Helper()
	mutation, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(),
		configuration,
		multiplier,
		configuration.TrafficClass,
		configuration.FailureDomainLabel,
		false,
		9001,
	)
	require.NoError(t, err)
	require.Equal(t, multiplier, mutation.Configuration.UpstreamCostMultiplier)
	return mutation.Configuration
}

func configureRoutingBillingIsolationRatios(t *testing.T) {
	t.Helper()
	modelRatios := ratio_setting.ModelRatio2JSONString()
	completionRatios := ratio_setting.CompletionRatio2JSONString()
	groupRatios := ratio_setting.GroupRatio2JSONString()
	groupGroupRatios := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(modelRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(completionRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatios))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(groupGroupRatios))
	})

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(
		fmt.Sprintf(`{"%s":2}`, routingBillingIsolationModel),
	))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(
		fmt.Sprintf(`{"%s":3}`, routingBillingIsolationModel),
	))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		fmt.Sprintf(`{"%s":1.6}`, routingBillingIsolationGroup),
	))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(
		fmt.Sprintf(`{"%s":{"%s":1.25}}`, routingBillingIsolationUser, routingBillingIsolationGroup),
	))
}

func seedRoutingBillingIsolationUser(t *testing.T, userID, tokenID, quota int) string {
	t.Helper()
	username := fmt.Sprintf("routing-billing-user-%d", userID)
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: username, AffCode: username,
		Quota: quota, Status: common.UserStatusEnabled,
	}).Error)
	tokenKey := fmt.Sprintf("routing-billing-token-%d", tokenID)
	require.NoError(t, model.DB.Create(&model.Token{
		Id: tokenID, UserId: userID, Key: tokenKey, Name: tokenKey,
		Status: common.TokenStatusEnabled, RemainQuota: quota,
	}).Error)
	return tokenKey
}

func runSynchronousBillingIsolationRequest(
	t *testing.T,
	userID int,
	tokenID int,
	channelID int,
	usage dto.Usage,
) synchronousBillingIsolationResult {
	t.Helper()
	const initialQuota = 100_000
	tokenKey := seedRoutingBillingIsolationUser(t, userID, tokenID, initialQuota)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("token_name", fmt.Sprintf("routing-billing-token-%d", tokenID))
	startedAt := time.Now()

	info := &relaycommon.RelayInfo{
		UserId: userID, TokenId: tokenID, TokenKey: tokenKey,
		UserGroup: routingBillingIsolationUser, UsingGroup: routingBillingIsolationGroup,
		OriginModelName:   routingBillingIsolationModel,
		ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: channelID},
		StartTime:         startedAt,
		FirstResponseTime: startedAt,
		ForcePreConsume:   true,
		UserSetting:       dto.UserSetting{BillingPreference: "wallet_only"},
	}
	priceData, err := relayhelper.ModelPriceHelper(
		ctx, info, 100, &types.TokenCountMeta{MaxTokens: 100},
	)
	require.NoError(t, err)
	priceData.AddOtherRatio("duration", 1.5)
	info.PriceData = priceData

	require.Nil(t, PreConsumeBilling(ctx, priceData.QuotaToPreConsume, info))
	walletAfterPre := getUserQuota(t, userID)
	PostTextConsumeQuota(ctx, info, &usage, nil)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", userID).Order("id desc").First(&log).Error)
	logOther := make(map[string]any)
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &logOther))
	groupRatio, ok := logOther["group_ratio"].(float64)
	require.True(t, ok)
	userGroupRatio, ok := logOther["user_group_ratio"].(float64)
	require.True(t, ok)

	return synchronousBillingIsolationResult{
		PreConsumedQuota: info.Billing.GetPreConsumedQuota(),
		WalletAfterPre:   walletAfterPre,
		FinalWallet:      getUserQuota(t, userID),
		FinalTokenQuota:  getTokenRemainQuota(t, tokenID),
		LogQuota:         log.Quota,
		GroupRatio:       groupRatio,
		UserGroupRatio:   userGroupRatio,
		OtherRatios:      info.PriceData.OtherRatios(),
		LogOther:         logOther,
	}
}

func prepareAsyncRoutingBillingIsolation(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(
		&model.IdentityCacheSync{},
		&model.AsyncBillingReservation{},
		&model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{},
		&model.BillingStatsProjection{},
		&model.BillingLogProjection{},
		&model.QuotaData{},
		&model.TaskBillingOperation{},
	))
	clear := func() {
		for _, table := range []string{
			"async_billing_manual_resolutions",
			"billing_log_projections",
			"billing_stats_projections",
			"task_billing_operations",
			"async_billing_attempts",
			"async_billing_reservations",
			"identity_cache_syncs",
			"quota_data",
		} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
	}
	clear()
	t.Cleanup(clear)
}

func acceptRoutingBillingIsolationTask(
	t *testing.T,
	channel model.Channel,
	userID int,
	reservationQuota int,
	now time.Time,
) acceptedBillingIsolationTask {
	t.Helper()
	const initialQuota = 2_000
	seedRoutingBillingIsolationUser(t, userID, userID, initialQuota)
	reservation, created, err := model.CreateAsyncBillingReservation(
		context.Background(),
		model.AsyncBillingReservationSpec{
			ReservationKey:    fmt.Sprintf("routing-billing-reservation-%d", userID),
			ProtocolVersion:   model.TaskBillingProtocolVersion,
			Kind:              model.AsyncBillingKindTask,
			PublicTaskID:      fmt.Sprintf("routing_billing_task_%d", userID),
			UserID:            userID,
			TokenID:           userID,
			BillingPreference: "wallet_only",
			Quota:             reservationQuota,
		},
		now,
	)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, initialQuota-reservationQuota, getUserQuota(t, userID))
	require.Equal(t, initialQuota-reservationQuota, getTokenRemainQuota(t, userID))

	billingContext := &model.TaskBillingContext{
		ModelRatio:      2,
		GroupRatio:      1.25,
		OtherRatios:     map[string]float64{"duration": 1.5},
		OriginModelName: routingBillingIsolationModel,
	}
	userGroupRatio := 1.25
	audit := model.AsyncBillingAcceptedAuditSnapshot{
		RequestID:       fmt.Sprintf("routing-billing-request-%d", userID),
		RequestPath:     "/v1/videos",
		Action:          "generate",
		Content:         "submit asynchronous task",
		OriginModelName: routingBillingIsolationModel,
		Group:           routingBillingIsolationGroup,
		ModelRatio:      billingContext.ModelRatio,
		GroupRatio:      billingContext.GroupRatio,
		UserGroupRatio:  &userGroupRatio,
		OtherRatios:     map[string]float64{"duration": 1.5},
	}
	task := &model.Task{
		Platform: constant.TaskPlatformSuno,
		Action:   "generate",
		Status:   model.TaskStatusNotStart,
		Group:    routingBillingIsolationGroup,
		Properties: model.Properties{
			OriginModelName: routingBillingIsolationModel,
		},
		PrivateData: model.TaskPrivateData{BillingContext: billingContext},
	}
	attempt, err := model.AuthorizeAsyncBillingAttempt(
		context.Background(),
		reservation.ID,
		model.AsyncBillingAttemptSpec{
			AttemptIndex:   0,
			ChannelID:      channel.Id,
			CredentialID:   0,
			ChannelVersion: channel.RoutingGeneration,
			SendDeadlineMs: now.Add(time.Minute).UnixMilli(),
			AcceptanceIntent: &model.AsyncBillingAcceptanceIntentSpec{
				Task:  task,
				Audit: audit,
			},
		},
		now.Add(time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, model.AsyncBillingAttemptStateAuthorized, attempt.State)

	replay := model.AsyncBillingReplaySpec{
		StatusCode:  http.StatusOK,
		ContentType: "application/json",
		Body:        []byte(fmt.Sprintf(`{"id":"routing_billing_task_%d"}`, userID)),
	}
	accepted, err := model.AcceptAsyncTaskReservation(
		context.Background(),
		reservation.ID,
		0,
		task,
		fmt.Sprintf("provider-routing-billing-%d", userID),
		reservationQuota,
		audit,
		replay,
		now.Add(2*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, model.AsyncBillingReservationStateAccepted, accepted.State)
	require.Positive(t, task.ID)
	require.Equal(t, reservationQuota, task.EffectiveBillingQuota())

	return acceptedBillingIsolationTask{
		Reservation:  accepted,
		Task:         task,
		Audit:        audit,
		Replay:       replay,
		InitialQuota: initialQuota,
	}
}

func processAcceptedBillingIsolationProjections(t *testing.T, reservationID int64, owner string) {
	t.Helper()
	stats, hadStats, err := ProcessNextBillingStatsProjection(context.Background(), owner+"-stats", time.Minute)
	require.NoError(t, err)
	require.True(t, hadStats)
	require.Equal(t, model.BillingStatsProjectionStateCompleted, stats.State)
	logProjection, hadLog, err := ProcessNextBillingLogProjection(context.Background(), owner+"-log", time.Minute)
	require.NoError(t, err)
	require.True(t, hadLog)
	require.Equal(t, model.BillingLogProjectionStateCompleted, logProjection.State)
	require.NoError(t, model.ProcessAsyncBillingAcceptedProjection(context.Background(), reservationID, time.Now()))

	var reservation model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&reservation, reservationID).Error)
	require.Equal(t, model.AsyncBillingAcceptedProjectionCompleted, reservation.AcceptedProjectionState)
}

func loadRoutingBillingIsolationLogOther(t *testing.T, log model.Log) map[string]any {
	t.Helper()
	other := make(map[string]any)
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	return other
}

func assertFrozenRoutingBillingFields(t *testing.T, other map[string]any) {
	t.Helper()
	assert.Equal(t, 1.25, other["group_ratio"])
	assert.Equal(t, 1.25, other["user_group_ratio"])
	assert.Equal(t, 1.5, other["duration"])
	assert.NotContains(t, other, "upstream_cost_multiplier")
	assert.NotContains(t, other, "channel_multiplier")
}

func TestRoutingChannelMultiplierDoesNotAffectSynchronousUserBilling(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	configureRoutingBillingIsolationRatios(t)
	const channelID = 88001
	seedChannel(t, channelID)
	configuration := prepareRoutingBillingIsolationConfiguration(t, channelID, 0.5)

	testCases := []struct {
		name          string
		usage         dto.Usage
		baselineUser  int
		updatedUser   int
		expectedQuota int
	}{
		{
			name: "refund remains based on user sale price",
			usage: dto.Usage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
			},
			baselineUser: 88011, updatedUser: 88012, expectedQuota: 600,
		},
		{
			name: "additional charge remains based on user sale price",
			usage: dto.Usage{
				PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
			},
			baselineUser: 88013, updatedUser: 88014, expectedQuota: 15_000,
		},
	}

	baseline := make(map[string]synchronousBillingIsolationResult, len(testCases))
	for _, testCase := range testCases {
		baseline[testCase.name] = runSynchronousBillingIsolationRequest(
			t, testCase.baselineUser, testCase.baselineUser, channelID, testCase.usage,
		)
	}
	configuration = updateRoutingBillingIsolationMultiplier(t, configuration, 2)
	require.Equal(t, 2.0, configuration.UpstreamCostMultiplier)

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			updated := runSynchronousBillingIsolationRequest(
				t, testCase.updatedUser, testCase.updatedUser, channelID, testCase.usage,
			)
			before := baseline[testCase.name]
			assert.Equal(t, before.PreConsumedQuota, updated.PreConsumedQuota)
			assert.Equal(t, before.WalletAfterPre, updated.WalletAfterPre)
			assert.Equal(t, before.FinalWallet, updated.FinalWallet)
			assert.Equal(t, before.FinalTokenQuota, updated.FinalTokenQuota)
			assert.Equal(t, before.LogQuota, updated.LogQuota)
			assert.Equal(t, testCase.expectedQuota, updated.LogQuota)
			assert.Equal(t, 100_000-testCase.expectedQuota, updated.FinalWallet)
			assert.Equal(t, 100_000-testCase.expectedQuota, updated.FinalTokenQuota)
			assert.Equal(t, 1.25, updated.GroupRatio)
			assert.Equal(t, 1.25, updated.UserGroupRatio)
			assert.Equal(t, map[string]float64{"duration": 1.5}, updated.OtherRatios)
			assert.Equal(t, before.OtherRatios, updated.OtherRatios)
			assert.NotContains(t, updated.LogOther, "upstream_cost_multiplier")
			assert.NotContains(t, updated.LogOther, "channel_multiplier")
		})
	}
}

func TestRoutingChannelMultiplierDoesNotAffectAcceptedAsyncTaskBilling(t *testing.T) {
	truncate(t)
	prepareAsyncRoutingBillingIsolation(t)
	const channelID = 88101
	seedChannel(t, channelID)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, channelID).Error)
	configuration := prepareRoutingBillingIsolationConfiguration(t, channelID, 0.5)
	now := time.Unix(1_900_000_000, 0)
	accepted := acceptRoutingBillingIsolationTask(t, channel, 88111, 600, now)

	configuration = updateRoutingBillingIsolationMultiplier(t, configuration, 2)
	require.Equal(t, 2.0, configuration.UpstreamCostMultiplier)

	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, accepted.Task.ID).Error)
	frozen := persisted.EffectiveBillingContext()
	require.NotNil(t, frozen)
	assert.Equal(t, 2.0, frozen.ModelRatio)
	assert.Equal(t, 1.25, frozen.GroupRatio)
	assert.Equal(t, map[string]float64{"duration": 1.5}, frozen.OtherRatios)
	assert.NotContains(t, frozen.OtherRatios, "upstream_cost_multiplier")

	replayTask := &model.Task{}
	replayedReservation, err := model.AcceptAsyncTaskReservation(
		context.Background(),
		accepted.Reservation.ID,
		0,
		replayTask,
		accepted.Reservation.UpstreamTaskID,
		600,
		accepted.Audit,
		accepted.Replay,
		now.Add(3*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, accepted.Task.ID, replayTask.ID)
	assert.Equal(t, accepted.Reservation.ID, replayedReservation.ID)
	assert.Equal(t, accepted.InitialQuota-600, getUserQuota(t, 88111))
	replayResponse, err := model.GetAsyncBillingReplayResponse(replayedReservation)
	require.NoError(t, err)
	assert.Equal(t, accepted.Replay.Body, replayResponse.Body)

	processAcceptedBillingIsolationProjections(t, accepted.Reservation.ID, "routing-billing-accepted")
	var acceptedLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", 88111, model.LogTypeConsume).
		Order("id asc").First(&acceptedLog).Error)
	assert.Equal(t, 600, acceptedLog.Quota)
	assertFrozenRoutingBillingFields(t, loadRoutingBillingIsolationLogOther(t, acceptedLog))

	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         accepted.Task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		Progress:       "100%",
		FinishTime:     now.Add(4 * time.Second).Unix(),
		TotalTokens:    100,
	}, now.Add(4*time.Second))
	require.NoError(t, err)
	require.NotNil(t, finalized.Operation)
	assert.Equal(t, 375, finalized.Operation.TargetQuota)
	assert.Equal(t, -225, finalized.Operation.QuotaDelta)
	assert.Equal(t, model.TaskBillingOperationKindSettle, finalized.Operation.Kind)

	completed, err := processTaskBillingOperationAt(
		context.Background(), finalized.Operation.ID, "routing-billing-terminal", now.Add(5*time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, accepted.InitialQuota-375, getUserQuota(t, 88111))
	assert.Equal(t, accepted.InitialQuota-375, getTokenRemainQuota(t, 88111))

	replayedOperation, err := processTaskBillingOperationAt(
		context.Background(), completed.ID, "routing-billing-terminal-replay", now.Add(6*time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, replayedOperation.State)
	assert.Equal(t, accepted.InitialQuota-375, getUserQuota(t, 88111))
	assert.Equal(t, accepted.InitialQuota-375, getTokenRemainQuota(t, 88111))

	terminalProjection, hadTerminalProjection, err := ProcessNextBillingLogProjection(
		context.Background(), "routing-billing-terminal-projection", time.Minute,
	)
	require.NoError(t, err)
	require.True(t, hadTerminalProjection)
	require.Equal(t, model.BillingLogProjectionStateCompleted, terminalProjection.State)

	var terminalLog model.Log
	require.NoError(t, model.LOG_DB.Where("billing_operation_key = ?", completed.OperationKey).First(&terminalLog).Error)
	assert.Equal(t, model.LogTypeRefund, terminalLog.Type)
	assert.Equal(t, 225, terminalLog.Quota)
	terminalOther := loadRoutingBillingIsolationLogOther(t, terminalLog)
	assertFrozenRoutingBillingFields(t, terminalOther)
	assert.Equal(t, float64(600), terminalOther["pre_consumed_quota"])
	assert.Equal(t, float64(375), terminalOther["actual_quota"])

	var terminalLogCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("billing_operation_key = ?", completed.OperationKey).Count(&terminalLogCount).Error)
	assert.Equal(t, int64(1), terminalLogCount)
	var terminalReservation model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&terminalReservation, accepted.Reservation.ID).Error)
	assert.Equal(t, model.AsyncBillingReservationStateTerminal, terminalReservation.State)
	assert.Equal(t, 375, terminalReservation.CurrentQuota)
	require.NoError(t, model.DB.First(&persisted, accepted.Task.ID).Error)
	assert.Equal(t, 375, persisted.EffectiveBillingQuota())
	assert.Equal(t, map[string]float64{"duration": 1.5}, persisted.EffectiveBillingContext().OtherRatios)
}

func TestRoutingChannelMultiplierDoesNotAffectAsyncManualReview(t *testing.T) {
	truncate(t)
	prepareAsyncRoutingBillingIsolation(t)
	const channelID = 88201
	seedChannel(t, channelID)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, channelID).Error)
	configuration := prepareRoutingBillingIsolationConfiguration(t, channelID, 0.5)
	now := time.Unix(1_900_100_000, 0)
	accepted := acceptRoutingBillingIsolationTask(t, channel, 88211, 600, now)
	configuration = updateRoutingBillingIsolationMultiplier(t, configuration, 2)
	require.Equal(t, 2.0, configuration.UpstreamCostMultiplier)
	processAcceptedBillingIsolationProjections(t, accepted.Reservation.ID, "routing-billing-review-accepted")

	result, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         accepted.Task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		Progress:       "100%",
		FinishTime:     now.Add(4 * time.Second).Unix(),
	}, now.Add(4*time.Second))
	require.NoError(t, err)
	require.True(t, result.ManualReview)
	require.Nil(t, result.Operation)
	assert.Equal(t, accepted.InitialQuota-600, getUserQuota(t, 88211))
	assert.Equal(t, accepted.InitialQuota-600, getTokenRemainQuota(t, 88211))

	var review model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&review, accepted.Reservation.ID).Error)
	require.Equal(t, model.AsyncBillingReservationStateManualReview, review.State)
	require.Equal(t, model.AsyncBillingReviewKindTerminalUsage, review.ManualReviewKind)
	require.Equal(t, 600, review.CurrentQuota)

	decision := model.AsyncBillingManualDecisionSpec{
		ReservationID:       review.ID,
		Action:              model.AsyncBillingManualResolutionAccepted,
		ActorUserID:         9001,
		ExpectedVersion:     review.ReviewVersion,
		ExpectedETag:        model.AsyncBillingManualReviewETag(review.ID, review.ReviewVersion),
		UpstreamTaskID:      review.UpstreamTaskID,
		ProviderStatus:      model.AsyncBillingProviderStatusTerminalVerified,
		ProviderCheckedMs:   now.Add(5 * time.Second).UnixMilli(),
		EvidenceReference:   "routing-billing-terminal-usage-evidence",
		Reason:              "preserve the frozen user charge after terminal verification",
		DecisionKeyHash:     strings.Repeat("7", 64),
		DecisionPayloadHash: strings.Repeat("8", 64),
	}
	resolved, err := model.ResolveAsyncBillingManualReview(
		context.Background(), decision, now.Add(6*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, model.AsyncBillingReservationStateTerminal, resolved.Reservation.State)
	require.Equal(t, 600, resolved.Reservation.CurrentQuota)
	replayed, err := model.ResolveAsyncBillingManualReview(
		context.Background(), decision, now.Add(7*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, resolved.Resolution.ID, replayed.Resolution.ID)
	assert.Equal(t, accepted.InitialQuota-600, getUserQuota(t, 88211))
	assert.Equal(t, accepted.InitialQuota-600, getTokenRemainQuota(t, 88211))

	var operation model.TaskBillingOperation
	require.NoError(t, model.DB.Where("reservation_id = ?", review.ID).First(&operation).Error)
	assert.Equal(t, model.TaskBillingOperationKindNoop, operation.Kind)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, operation.State)
	assert.Equal(t, 600, operation.TargetQuota)
	assert.Zero(t, operation.QuotaDelta)
	var resolutionCount int64
	require.NoError(t, model.DB.Model(&model.AsyncBillingManualResolution{}).
		Where("reservation_id = ?", review.ID).Count(&resolutionCount).Error)
	assert.Equal(t, int64(1), resolutionCount)

	var acceptedLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", 88211).First(&acceptedLog).Error)
	assert.Equal(t, 600, acceptedLog.Quota)
	assertFrozenRoutingBillingFields(t, loadRoutingBillingIsolationLogOther(t, acceptedLog))
	var userLogCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ?", 88211).Count(&userLogCount).Error)
	assert.Equal(t, int64(1), userLogCount)

	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, accepted.Task.ID).Error)
	require.NotNil(t, persisted.EffectiveBillingContext())
	assert.Equal(t, 2.0, persisted.EffectiveBillingContext().ModelRatio)
	assert.Equal(t, 1.25, persisted.EffectiveBillingContext().GroupRatio)
	assert.Equal(t, map[string]float64{"duration": 1.5}, persisted.EffectiveBillingContext().OtherRatios)
}
