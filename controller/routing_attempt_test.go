package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingAttemptGuardOnlyActivatesForBalancedModes(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	ctx := routingAttemptContextForTest()
	info := &relaycommon.RelayInfo{}

	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeObserve
	smart_routing_setting.UpdateSetting(setting)
	assert.Nil(t, newRoutingAttemptGuard(ctx, info))

	setting.Mode = smart_routing_setting.ModeBalanced
	smart_routing_setting.UpdateSetting(setting)
	assert.NotNil(t, newRoutingAttemptGuard(ctx, info))

	setting.Mode = smart_routing_setting.ModeEnterpriseSLO
	smart_routing_setting.UpdateSetting(setting)
	assert.NotNil(t, newRoutingAttemptGuard(ctx, info))
}

func TestRoutingAttemptGuardStopsAfterClientCommit(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 2
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeBalanced
	setting.MaxSwitches = 2
	smart_routing_setting.UpdateSetting(setting)

	ctx := routingAttemptContextForTest()
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 7)
	info := &relaycommon.RelayInfo{}
	info.PriceData.QuotaToPreConsume = 10
	guard := newRoutingAttemptGuard(ctx, info)
	require.NotNil(t, guard)
	defer guard.Complete()

	first, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	require.NoError(t, first.MarkSent())
	first.Finish()

	second, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	require.NoError(t, second.MarkSent())
	require.NoError(t, second.MarkClientCommitted())
	second.Finish()

	_, err = guard.Begin(ctx, info)
	assert.ErrorIs(t, err, channelrouting.ErrAttemptClientCommitted)
}

func TestRoutingAttemptGuardPreservesLegacyRetryPath(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeBalanced
	smart_routing_setting.UpdateSetting(setting)
	ctx := routingAttemptContextForTest()
	info := &relaycommon.RelayInfo{}
	guard := newRoutingAttemptGuard(ctx, info)
	require.NotNil(t, guard)
	defer guard.Complete()

	first, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	assert.Nil(t, first)
	second, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	assert.Nil(t, second)
}

func TestRoutingAttemptGuardBypassesRemainderAfterV2FallsBackToLegacy(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeBalanced
	setting.MaxSwitches = 2
	smart_routing_setting.UpdateSetting(setting)
	ctx := routingAttemptContextForTest()
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 7)
	info := &relaycommon.RelayInfo{}
	guard := newRoutingAttemptGuard(ctx, info)
	require.NotNil(t, guard)
	defer guard.Complete()

	first, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	require.NotNil(t, first)
	first.Finish()

	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 0)
	legacy, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	assert.Nil(t, legacy)
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 8)
	later, err := guard.Begin(ctx, info)
	require.NoError(t, err)
	assert.Nil(t, later)
}

func TestRoutingAttemptGuardsShareBoundedPoolRetryBudget(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	channelrouting.ResetDefaultRetryTokenBudgetForTest(8, time.Minute)
	t.Cleanup(func() { channelrouting.ResetDefaultRetryTokenBudgetForTest(4_096, 30*time.Minute) })
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeBalanced
	setting.MaxSwitches = 1
	setting.RetryTokenCapacity = 1
	setting.RetryTokenRefillPerSec = 0.000_001
	setting.RetryExtraCostMultiplier = 1
	smart_routing_setting.UpdateSetting(setting)

	beginAndFinish := func(ctx *gin.Context, guard *routingAttemptGuard, info *relaycommon.RelayInfo) {
		lease, err := guard.Begin(ctx, info)
		require.NoError(t, err)
		lease.Finish()
	}
	firstContext := routingAttemptContextForTest()
	common.SetContextKey(firstContext, constant.ContextKeyRoutingPoolID, 9)
	firstInfo := &relaycommon.RelayInfo{}
	firstInfo.PriceData.QuotaToPreConsume = 5
	firstGuard := newRoutingAttemptGuard(firstContext, firstInfo)
	beginAndFinish(firstContext, firstGuard, firstInfo)
	beginAndFinish(firstContext, firstGuard, firstInfo)
	firstGuard.Complete()

	secondContext := routingAttemptContextForTest()
	common.SetContextKey(secondContext, constant.ContextKeyRoutingPoolID, 9)
	secondInfo := &relaycommon.RelayInfo{}
	secondInfo.PriceData.QuotaToPreConsume = 5
	secondGuard := newRoutingAttemptGuard(secondContext, secondInfo)
	defer secondGuard.Complete()
	beginAndFinish(secondContext, secondGuard, secondInfo)
	_, err := secondGuard.Begin(secondContext, secondInfo)
	assert.ErrorIs(t, err, channelrouting.ErrRetryTokenBudgetExhausted)
}

func TestRoutingAttemptClientCommitUsesProtocolBoundary(t *testing.T) {
	ctx := routingAttemptContextForTest()
	stream := &relaycommon.RelayInfo{IsStream: true, StreamStatus: relaycommon.NewStreamStatus()}
	assert.False(t, routingAttemptClientCommitted(ctx, stream))
	_, err := ctx.Writer.Write([]byte("data: business\n\n"))
	require.NoError(t, err)
	assert.True(t, routingAttemptClientCommitted(ctx, stream))

	nonStreamContext := routingAttemptContextForTest()
	nonStream := &relaycommon.RelayInfo{}
	assert.False(t, routingAttemptClientCommitted(nonStreamContext, nonStream))
	_, err = nonStreamContext.Writer.Write([]byte("ok"))
	require.NoError(t, err)
	assert.True(t, routingAttemptClientCommitted(nonStreamContext, nonStream))

	realtimeContext := routingAttemptContextForTest()
	realtime := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIRealtime}
	assert.False(t, routingAttemptClientCommitted(realtimeContext, realtime))
	_, err = realtimeContext.Writer.Write([]byte("websocket upgrade complete"))
	require.NoError(t, err)
	assert.True(t, routingAttemptClientCommitted(realtimeContext, realtime))
}

func TestRoutingAttemptRejectionReturnsTerminalErrorBeforeFirstSend(t *testing.T) {
	deadline := routingAttemptRejectionError(channelrouting.ErrAttemptDeadlineExceeded)
	require.NotNil(t, deadline)
	assert.Equal(t, http.StatusGatewayTimeout, deadline.StatusCode)
	assert.True(t, types.IsSkipRetryError(deadline))
	assert.ErrorIs(t, deadline.Err, channelrouting.ErrAttemptDeadlineExceeded)

	budget := routingAttemptRejectionError(channelrouting.ErrRetryTokenBudgetExhausted)
	require.NotNil(t, budget)
	assert.Equal(t, http.StatusServiceUnavailable, budget.StatusCode)
	assert.True(t, types.IsSkipRetryError(budget))
	assert.ErrorIs(t, budget.Err, channelrouting.ErrRetryTokenBudgetExhausted)
}

func TestRoutingSerialAttemptAuditRecordsRetryAndFinalOutcome(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.RoutingHedgeAttemptAudit{}))
	require.NoError(t, model.DB.Where("id > ?", 0).Delete(&model.RoutingHedgeAttemptAudit{}).Error)
	channelrouting.ResetHedgeAttemptAuditsForTest()
	t.Cleanup(func() { channelrouting.ResetHedgeAttemptAuditsForTest() })

	ctx := routingAttemptContextForTest()
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(17))
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 7)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 71)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 701)
	common.SetContextKey(ctx, constant.ContextKeyRoutingDecisionID, "serial-decision-1")
	common.SetContextKey(ctx, constant.ContextKeyRoutingAlgorithmVersion, channelrouting.DecisionAlgorithmBalancedV1)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://api.example.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-east-1")
	channel := &model.Channel{Id: 101}
	info := &relaycommon.RelayInfo{
		RequestId: "serial-request-1", OriginModelName: "gpt-test",
		RequestURLPath: "/v1/chat/completions", RetryIndex: 0,
	}
	info.ResetStreamAttemptState()
	first, err := reserveRoutingSerialAttemptAudit(ctx, info, channel)
	require.NoError(t, err)
	require.NotNil(t, first)
	apiErr := types.NewErrorWithStatusCode(
		assert.AnError,
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	classification := routingerror.Classification{
		Rule: "provider_5xx", Responsibility: routingerror.ResponsibilityProvider,
		Retryability: routingerror.RetryBeforeCommit,
	}
	require.NoError(t, completeRoutingSerialAttemptAudit(
		ctx, info, channel, first, false, apiErr, classification, false, true,
	))

	info.RetryIndex = 1
	info.ResetStreamAttemptState()
	info.FirstResponseTime = info.RoutingAttemptStartTime()
	second, err := reserveRoutingSerialAttemptAudit(ctx, info, channel)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NoError(t, completeRoutingSerialAttemptAudit(
		ctx,
		info,
		channel,
		second,
		true,
		nil,
		routingerror.Classification{},
		true,
		false,
	))
	flushed, err := channelrouting.FlushHedgeAttemptAuditsContext(ctx.Request.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, flushed)

	summary, err := model.GetRoutingHedgeDecisionAuditContext(
		ctx.Request.Context(),
		"serial-decision-1",
		"serial-request-1",
	)
	require.NoError(t, err)
	require.Len(t, summary.Attempts, 2)
	assert.Equal(t, model.RoutingAttemptExecutionSerial, summary.Attempts[0].ExecutionMode)
	assert.True(t, summary.Attempts[0].UpstreamSent)
	assert.True(t, summary.Attempts[0].WillRetry)
	assert.False(t, summary.Attempts[0].FinalAttempt)
	assert.Equal(t, string(routingerror.ResponsibilityProvider), summary.Attempts[0].ErrorResponsibility)
	assert.Equal(t, model.RoutingHedgeAttemptResultSuccess, summary.FinalResult)
	assert.Equal(t, 101, summary.FinalChannelID)
	assert.Equal(t, "us-east-1", summary.FinalRegion)
	assert.NotEmpty(t, summary.FinalNodeEpochID)
}

func routingAttemptContextForTest() *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return ctx
}
