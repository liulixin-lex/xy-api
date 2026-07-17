package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const routingHedgeIntegrationRedisDB = 13

type routingHedgeIntegrationFixture struct {
	db           *gorm.DB
	ctx          *gin.Context
	recorder     *httptest.ResponseRecorder
	info         *relaycommon.RelayInfo
	primary      *model.Channel
	retry        *service.RetryParam
	attemptLease *channelrouting.AttemptLease
	bodyStorage  common.BodyStorage
	strict       *channelrouting.StrictCapacityCoordinator
}

func TestRoutingHedgeRealRedisSecondaryWinsWithOneSettlementAndTwoAudits(t *testing.T) {
	arrived := make(chan int, 2)
	releaseSecondary := make(chan struct{})
	releasePrimaryHandler := make(chan struct{})
	primaryHandlerExited := make(chan struct{})
	primaryExecutionReturned := make(chan struct{})
	releasePrimaryExecution := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releasePrimaryHandler:
		default:
			close(releasePrimaryHandler)
		}
		select {
		case <-releasePrimaryExecution:
		default:
			close(releasePrimaryExecution)
		}
	})
	var upstreamCalls atomic.Int64
	fixture := newRoutingHedgeIntegrationFixture(t, 2, true, func(writer http.ResponseWriter, _ *http.Request) {
		attempt := int(upstreamCalls.Add(1))
		arrived <- attempt
		if attempt == 1 {
			<-releasePrimaryHandler
			close(primaryHandlerExited)
			return
		}
		<-releaseSecondary
		writeRoutingHedgeIntegrationSuccess(writer)
	})

	var settlements atomic.Int64
	previousExecute := routingHedgeExecuteText
	previousFinalize := routingHedgeFinalizeText
	previousLimiter := routingHedgeProcessLimiter
	routingHedgeExecuteText = func(
		ctx *gin.Context,
		info *relaycommon.RelayInfo,
	) (*relay.TextResponseCapture, *types.NewAPIError) {
		capture, apiErr := previousExecute(ctx, info)
		if common.GetContextKeyInt(ctx, constant.ContextKeyChannelId) == fixture.primary.Id {
			close(primaryExecutionReturned)
			<-releasePrimaryExecution
		}
		return capture, apiErr
	}
	routingHedgeFinalizeText = func(*gin.Context, *relaycommon.RelayInfo, *relay.TextResponseCapture) error {
		settlements.Add(1)
		return nil
	}
	routingHedgeProcessLimiter = &channelrouting.HedgeLimiter{}
	t.Cleanup(func() {
		routingHedgeExecuteText = previousExecute
		routingHedgeFinalizeText = previousFinalize
		routingHedgeProcessLimiter = previousLimiter
	})

	type result struct {
		outcome routingHedgeOutcome
		handled bool
	}
	completed := make(chan result, 1)
	go func() {
		outcome, handled := maybeExecuteRoutingHedge(
			fixture.ctx, fixture.info, fixture.primary, fixture.retry,
			fixture.attemptLease, fixture.bodyStorage,
		)
		completed <- result{outcome: outcome, handled: handled}
	}()

	require.Equal(t, 1, waitRoutingHedgeIntegrationArrival(t, arrived))
	require.Equal(t, 2, waitRoutingHedgeIntegrationArrival(t, arrived))
	close(releaseSecondary)

	var execution result
	select {
	case execution = <-completed:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "routing hedge did not complete after releasing the secondary")
	}
	require.True(t, execution.handled)
	require.True(t, execution.outcome.success)
	assert.Nil(t, execution.outcome.apiErr)
	assert.Equal(t, int64(1), settlements.Load())
	assert.Equal(t, int64(2), upstreamCalls.Load())
	assert.Equal(t, http.StatusOK, fixture.recorder.Code)
	assert.Contains(t, fixture.recorder.Body.String(), `"id":"hedge-secondary"`)
	select {
	case <-primaryExecutionReturned:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "routing hedge primary execution did not return after cancellation")
	}

	stats := fixture.strict.Stats()
	assert.Equal(t, int64(2), stats.Allowed)
	assert.Equal(t, int64(2), stats.Committed)
	assert.Equal(t, int64(1), stats.Released)
	assert.Zero(t, stats.Unavailable)
	assert.Zero(t, stats.TransitionErr)
	close(releasePrimaryHandler)
	select {
	case <-primaryHandlerExited:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "routing hedge primary handler did not exit")
	}
	close(releasePrimaryExecution)
	require.Eventually(t, func() bool {
		return fixture.strict.Stats().Released == 2
	}, 3*time.Second, 5*time.Millisecond)

	audits := loadRoutingHedgeIntegrationAudits(t, fixture.db)
	require.Len(t, audits, 2)
	auditPayload, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(auditPayload), "hedge integration secret")
	assert.NotContains(t, string(auditPayload), "hedge-serving-secret")
	assert.NotEqual(t, audits[0].ChannelID, audits[1].ChannelID)
	winnerCount := 0
	var winnerAudit model.RoutingHedgeAttemptAudit
	for _, audit := range audits {
		assert.Equal(t, model.RoutingHedgeAttemptStateCompleted, audit.State)
		assert.True(t, audit.CostKnown)
		assert.False(t, audit.CrossRegion)
		assert.NotContains(t, audit.CostBreakdownJSON, "hedge integration secret")
		if audit.Winner {
			winnerCount++
			winnerAudit = audit
			assert.Equal(t, model.RoutingHedgeAttemptRoleSecondary, audit.Role)
			assert.Equal(t, model.RoutingHedgeAttemptResultSuccess, audit.Result)
			assert.True(t, audit.ClientCommitted)
			assert.True(t, audit.FinalAttempt)
			assert.False(t, audit.WillRetry)
		} else {
			assert.Equal(t, model.RoutingHedgeAttemptRolePrimary, audit.Role)
			assert.Equal(t, model.RoutingHedgeAttemptResultHedgeLost, audit.Result)
		}
	}
	assert.Equal(t, 1, winnerCount)
	summary, err := model.GetRoutingHedgeDecisionAuditContext(
		context.Background(), winnerAudit.DecisionID, fixture.info.RequestId,
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingHedgeAttemptRoleSecondary, summary.WinnerRole)
	assert.Equal(t, winnerAudit.MemberID, summary.FinalMemberID)
	assert.Equal(t, winnerAudit.ChannelID, summary.FinalChannelID)
	assert.True(t, summary.DuplicateActualCostKnown, "%+v", audits)
}

func TestRoutingHedgeBothFailuresContinueRetryExcludingBothAttempts(t *testing.T) {
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	arrived := make(chan int, 3)
	releaseFailures := make(chan struct{})
	var upstreamCalls atomic.Int64
	fixture := newRoutingHedgeIntegrationFixture(t, 3, true, func(writer http.ResponseWriter, _ *http.Request) {
		attempt := int(upstreamCalls.Add(1))
		arrived <- attempt
		if attempt <= 2 {
			<-releaseFailures
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":{"message":"temporary upstream failure","type":"upstream_error"}}`)
			return
		}
		writeRoutingHedgeIntegrationSuccess(writer)
	})

	var settlements atomic.Int64
	previousFinalize := routingHedgeFinalizeText
	previousLimiter := routingHedgeProcessLimiter
	routingHedgeFinalizeText = func(*gin.Context, *relaycommon.RelayInfo, *relay.TextResponseCapture) error {
		settlements.Add(1)
		return nil
	}
	routingHedgeProcessLimiter = &channelrouting.HedgeLimiter{}
	t.Cleanup(func() {
		routingHedgeFinalizeText = previousFinalize
		routingHedgeProcessLimiter = previousLimiter
	})

	type result struct {
		outcome routingHedgeOutcome
		handled bool
	}
	completed := make(chan result, 1)
	go func() {
		outcome, handled := maybeExecuteRoutingHedge(
			fixture.ctx, fixture.info, fixture.primary, fixture.retry,
			fixture.attemptLease, fixture.bodyStorage,
		)
		completed <- result{outcome: outcome, handled: handled}
	}()

	require.Equal(t, 1, waitRoutingHedgeIntegrationArrival(t, arrived))
	require.Equal(t, 2, waitRoutingHedgeIntegrationArrival(t, arrived))
	close(releaseFailures)

	var execution result
	select {
	case execution = <-completed:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "routing hedge failures did not complete")
	}
	require.True(t, execution.handled)
	assert.False(t, execution.outcome.success)
	require.NotNil(t, execution.outcome.apiErr)
	assert.True(t, execution.outcome.retryDecided)
	assert.True(t, execution.outcome.willRetry)
	assert.Zero(t, settlements.Load())
	assert.False(t, fixture.ctx.Writer.Written())
	assert.True(t, shouldRetry(
		fixture.ctx, fixture.info, execution.outcome.apiErr, execution.outcome.classification, 1,
	))

	used := fixture.ctx.GetStringSlice("use_channel")
	require.Len(t, used, 2)
	assert.NotEqual(t, used[0], used[1])
	fixture.retry.IncreaseRetry()
	next, apiErr := getChannel(fixture.ctx, fixture.info, fixture.retry)
	require.Nil(t, apiErr)
	require.NotNil(t, next)
	assert.NotContains(t, used, strconv.Itoa(next.Id))
	assert.True(t, service.HasRoutingStrictCapacityReservation(fixture.ctx))
	cancelRoutingCapacityReservation(fixture.ctx)
	service.ReleaseRoutingHalfOpenProbe(fixture.ctx, next.Id, fixture.info.OriginModelName, fixture.info.UsingGroup)

	stats := fixture.strict.Stats()
	assert.Equal(t, int64(3), stats.Allowed)
	assert.Equal(t, int64(2), stats.Committed)
	assert.Equal(t, int64(2), stats.Released)
	assert.Equal(t, int64(1), stats.Canceled)
	assert.Zero(t, stats.Unavailable)

	audits := loadRoutingHedgeIntegrationAudits(t, fixture.db)
	require.Len(t, audits, 2)
	willRetryCount := 0
	for _, audit := range audits {
		assert.Equal(t, model.RoutingHedgeAttemptStateCompleted, audit.State)
		assert.Equal(t, model.RoutingHedgeAttemptResultUpstreamError, audit.Result)
		assert.False(t, audit.Winner)
		assert.False(t, audit.FinalAttempt)
		if audit.WillRetry {
			willRetryCount++
		}
	}
	assert.Equal(t, 1, willRetryCount)
}

func TestRoutingHedgeSameEndpointAndAccountCannotStartSecondary(t *testing.T) {
	var upstreamCalls atomic.Int64
	fixture := newRoutingHedgeIntegrationFixture(t, 2, false, func(writer http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		writeRoutingHedgeIntegrationSuccess(writer)
	})
	primaryCost, known, err := service.ChannelRoutingHedgeCostEstimate(
		fixture.ctx, fixture.primary.Id, fixture.info.OriginModelName, fixture.retry.RequestPath, fixture.retry.GetRetry(),
	)
	require.NoError(t, err)
	require.True(t, known, "%+v", primaryCost)
	policy, active, err := service.ChannelRoutingEnterpriseHedgePolicy(fixture.ctx)
	require.NoError(t, err)
	require.True(t, active)
	coordinator, err := channelrouting.NewHedgeCoordinator(policy, primaryCost.WorstCaseCost)
	require.NoError(t, err)
	primaryLease, err := coordinator.BeginPrimary()
	require.NoError(t, err)
	t.Cleanup(primaryLease.Finish)
	body, err := fixture.bodyStorage.Bytes()
	require.NoError(t, err)

	secondary, _, err := prepareRoutingHedgeSecondary(
		fixture.ctx, fixture.info, fixture.primary, fixture.retry, coordinator, primaryCost,
		body, policy.MaxResponseBytes,
	)

	assert.Nil(t, secondary)
	assert.ErrorIs(t, err, channelrouting.ErrHedgeTargetNotDistinct)
	assert.Zero(t, upstreamCalls.Load())
}

func newRoutingHedgeIntegrationFixture(
	t *testing.T,
	channelCount int,
	distinctEndpoints bool,
	handler http.HandlerFunc,
) routingHedgeIntegrationFixture {
	t.Helper()
	redisAddress := os.Getenv("ROUTING_TEST_REDIS_ADDR")
	if redisAddress == "" {
		t.Skip("ROUTING_TEST_REDIS_ADDR is not set")
	}
	require.GreaterOrEqual(t, channelCount, 2)
	gin.SetMode(gin.TestMode)
	t.Setenv("ROUTING_REGION", "hedge-test-region")
	service.InitHttpClient()

	redisClient := redis.NewClient(&redis.Options{Addr: redisAddress, DB: routingHedgeIntegrationRedisDB})
	require.NoError(t, redisClient.Ping(context.Background()).Err())
	require.NoError(t, redisClient.FlushDB(context.Background()).Err())
	t.Cleanup(func() {
		_ = redisClient.FlushDB(context.Background()).Err()
		_ = redisClient.Close()
	})
	strict := channelrouting.NewStrictCapacityCoordinator(redisClient)
	restoreStrict := channelrouting.SetDefaultStrictCapacityCoordinatorForTest(strict)
	t.Cleanup(restoreStrict)
	channelrouting.ResetHedgeAttemptAuditsForTest()
	t.Cleanup(func() { channelrouting.ResetHedgeAttemptAuditsForTest() })
	previousProcessLimiter := routingHedgeProcessLimiter
	previousByteLimiter := routingHedgeProcessByteLimiter
	previousRatioBudget := routingHedgeRatioBudget
	routingHedgeProcessLimiter = &channelrouting.HedgeLimiter{}
	routingHedgeProcessByteLimiter = &channelrouting.HedgeByteLimiter{}
	routingHedgeRatioBudget = channelrouting.NewHedgeRatioBudget(16)
	t.Cleanup(func() {
		routingHedgeProcessLimiter = previousProcessLimiter
		routingHedgeProcessByteLimiter = previousByteLimiter
		routingHedgeRatioBudget = previousRatioBudget
	})

	db := openChannelRoutingControllerDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.Ability{},
		&model.RoutingHedgeAttemptAudit{},
	))
	withChannelRoutingControllerState(t, db)

	globalModelSetting := *model_setting.GetGlobalSettings()
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = false
	model_setting.GetGlobalSettings().ChatCompletionsToResponsesPolicy.Enabled = false
	previousAutomaticDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = false
	t.Cleanup(func() {
		*model_setting.GetGlobalSettings() = globalModelSetting
		common.AutomaticDisableChannelEnabled = previousAutomaticDisable
	})
	previousModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-hedge-integration":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
	})

	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeEnterpriseSLO
	setting.HedgeEnabled = true
	setting.HedgeMaxConcurrent = 4
	setting.HedgeMaxResponseBytes = 1 << 20
	setting.HedgeMaxBufferedBytes = 8 << 20
	setting.HedgeRatioWindowSec = 60
	setting.HedgeMaxExtraBasisPoints = 10_000
	setting.MaxSwitches = channelCount
	setting.SnapshotStaleSec = 300
	smart_routing_setting.UpdateSetting(setting)

	priority := int64(100)
	weight := uint(100)
	channels := make([]model.Channel, 0, channelCount)
	abilities := make([]model.Ability, 0, channelCount)
	servers := make([]*httptest.Server, channelCount)
	if !distinctEndpoints {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		for index := range servers {
			servers[index] = server
		}
	}
	for index := 0; index < channelCount; index++ {
		channelID := 98_100 + index
		if distinctEndpoints {
			servers[index] = httptest.NewServer(handler)
			t.Cleanup(servers[index].Close)
		}
		baseURL := servers[index].URL
		channels = append(channels, model.Channel{
			Id: channelID, Type: constant.ChannelTypeOpenAI, Key: "hedge-serving-secret-" + strconv.Itoa(index),
			Status: common.ChannelStatusEnabled, Name: "hedge-integration-" + strconv.Itoa(index),
			Weight: &weight, Priority: &priority, BaseURL: &baseURL,
			Group: "hedge-enterprise", Models: "gpt-hedge-integration",
		})
		abilities = append(abilities, model.Ability{
			Group: "hedge-enterprise", Model: "gpt-hedge-integration", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		})
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&abilities).Error)
	model.InitChannelCache()

	_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	var pool model.RoutingPool
	require.NoError(t, db.Where("group_name = ?", "hedge-enterprise").First(&pool).Error)
	var members []model.RoutingPoolMember
	require.NoError(t, db.Where("pool_id = ?", pool.ID).Order("channel_id asc").Find(&members).Error)
	require.Len(t, members, channelCount)
	var credentials []model.RoutingCredentialRef
	channelIDs := make([]int, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.Id)
	}
	require.NoError(t, db.Where("channel_id IN ? AND active = ?", channelIDs, true).
		Order("channel_id asc").Find(&credentials).Error)
	require.Len(t, credentials, channelCount)
	credentialByChannel := make(map[int]int, len(credentials))
	for _, credential := range credentials {
		credentialByChannel[credential.ChannelID] = credential.ID
	}

	document := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: pool.ID, GroupName: pool.GroupName, DisplayName: "Hedge Enterprise",
			DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:   model.RoutingPolicyProfileEnterpriseSLO,
			Policy: []byte(`{"enterprise":{"capacity":{"mode":"redis_strict","scope":"credential"},` +
				`"hedge":{"enabled":true,"delay_ms":25,"max_extra_cost_multiplier":1,` +
				`"max_response_bytes":1048576,"scope":"distinct_endpoint_or_account","cross_region":false}}}`),
			Members: make([]model.RoutingPolicyMemberContent, 0, len(members)),
		}},
	}
	for _, member := range members {
		credentialID := credentialByChannel[member.ChannelID]
		require.Positive(t, credentialID)
		document.Pools[0].Members = append(document.Pools[0].Members, model.RoutingPolicyMemberContent{
			MemberID: member.ID, ChannelID: member.ChannelID, Enabled: true,
			Priority: member.LegacyPriority, Weight: member.LegacyWeight,
			CredentialIDs: []int{credentialID}, Overrides: []byte(`{}`),
		})
	}
	head, err := model.GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	_, err = model.PublishRoutingPolicyRevisionContext(
		context.Background(), head.CurrentRevision, document,
		model.RoutingPolicyActivationSpec{
			Stage: model.RoutingDeploymentStageActive, ActorID: 1, Reason: "routing hedge integration",
		},
	)
	require.NoError(t, err)

	view, err := channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	pricedMembers := 0
	for _, snapshotPool := range view.Pools {
		if snapshotPool.GroupName != "hedge-enterprise" {
			continue
		}
		for _, member := range snapshotPool.Members {
			require.Len(t, member.Models, 1)
			require.NotNil(t, member.Models[0].CostPricing)
			assert.Equal(t, channelrouting.SystemRoutingPricingBasis, member.Models[0].CostPricing.BillingMode)
			assert.Equal(t, float64(1), member.Models[0].CostUpstreamMultiplier)
			assert.Contains(t, member.Models[0].CostPricingIdentity, ":channel-config:1")
			pricedMembers++
		}
	}
	require.Equal(t, channelCount, pricedMembers)

	body := []byte(`{"model":"gpt-hedge-integration","messages":[{"role":"user","content":"hedge integration secret"}],"max_tokens":10}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, common.RequestIdKey, "routing-hedge-integration-request")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "hedge-enterprise")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "hedge-enterprise")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "hedge-enterprise")
	common.SetContextKey(ctx, constant.ContextKeyEstimatedTokens, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEstimatedOutput, 5)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCostProfile, &model.RoutingCostRequestProfile{
		PromptTokens: 10, MaximumPromptTokens: int64(len(body)),
		ExpectedCompletionTokens: 5, MaximumCompletionTokens: 10, MaxAttempts: 1,
		KnowledgeSpecified: true, InputTokensKnown: true, MaximumCompletionKnown: true,
		CacheTokensKnown: false, CacheWriteTokensKnown: true,
		CacheWriteOneHourTokensKnown: true,
		ImageInputTokensKnown:        true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true, RequestInputKnown: true,
		RequestPricingFeaturesKnown: true,
	})

	retry := &service.RetryParam{
		Ctx: ctx, TokenGroup: "hedge-enterprise", ModelName: "gpt-hedge-integration",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	}
	primary, group, err := service.CacheGetRandomSatisfiedChannel(retry)
	require.NoError(t, err)
	require.NotNil(t, primary)
	require.Equal(t, "hedge-enterprise", group)
	require.Nil(t, middleware.SetupContextForSelectedChannel(ctx, primary, "gpt-hedge-integration"))
	addUsedChannel(ctx, primary.Id)
	require.True(t, service.HasRoutingStrictCapacityReservation(ctx))

	request := &dto.GeneralOpenAIRequest{
		Model: "gpt-hedge-integration", Messages: []dto.Message{{Role: "user", Content: "hedge integration secret"}},
		MaxTokens: common.GetPointer(uint(10)),
	}
	info, err := relaycommon.GenRelayInfo(ctx, types.RelayFormatOpenAI, request, nil)
	require.NoError(t, err)
	info.SetEstimatePromptTokens(10)
	attemptCoordinator := channelrouting.NewAttemptCoordinator(channelrouting.AttemptPolicy{
		MaxAttempts: 3, Deadline: time.Now().Add(5 * time.Second), ExtraCostBudgetUnits: 10,
		RetryTokenCapacity: 10, RetryTokenRefill: 10,
	})
	attemptLease, err := attemptCoordinator.BeginAttempt(channelrouting.AttemptInput{
		PoolID: common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID), EstimatedCostUnits: 1,
	})
	require.NoError(t, err)
	bodyStorage, err := common.GetBodyStorage(ctx)
	require.NoError(t, err)
	ctx.Request.Body = io.NopCloser(bodyStorage)
	t.Cleanup(func() {
		attemptCoordinator.Complete()
		cancelRoutingCapacityReservation(ctx)
		service.ReleaseAllRoutingHalfOpenProbes(ctx)
		common.CleanupBodyStorage(ctx)
	})

	return routingHedgeIntegrationFixture{
		db: db, ctx: ctx, recorder: recorder, info: info, primary: primary, retry: retry,
		attemptLease: attemptLease, bodyStorage: bodyStorage, strict: strict,
	}
}

func waitRoutingHedgeIntegrationArrival(t *testing.T, arrived <-chan int) int {
	t.Helper()
	select {
	case attempt := <-arrived:
		return attempt
	case <-time.After(3 * time.Second):
		require.FailNow(t, "routing hedge upstream did not arrive")
		return 0
	}
}

func writeRoutingHedgeIntegrationSuccess(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, `{"id":"hedge-secondary","object":"chat.completion","created":1,`+
		`"model":"gpt-hedge-integration","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},`+
		`"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
}

func loadRoutingHedgeIntegrationAudits(t *testing.T, db *gorm.DB) []model.RoutingHedgeAttemptAudit {
	t.Helper()
	for channelrouting.HedgeAttemptAuditsStats().Entries > 0 {
		flushed, err := channelrouting.FlushHedgeAttemptAuditsContext(context.Background())
		require.NoError(t, err)
		require.Positive(t, flushed)
	}
	var audits []model.RoutingHedgeAttemptAudit
	require.NoError(t, db.Order("id asc").Find(&audits).Error)
	return audits
}
