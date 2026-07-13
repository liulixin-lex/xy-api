package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingEnterpriseActiveUsesStrictCapacityAndAuditsAdmission(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousRuntime := channelRoutingCanaryRuntime
	common.MemoryCacheEnabled = true
	var err error
	channelRoutingCanaryRuntime, err = newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	fakeRedis := &enterpriseStrictCapacityRedis{}
	restoreCoordinator := channelrouting.SetDefaultStrictCapacityCoordinatorForTest(
		channelrouting.NewStrictCapacityCoordinator(fakeRedis),
	)
	t.Cleanup(func() {
		restoreCoordinator()
		common.MemoryCacheEnabled = previousMemoryCache
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(100)
	modelMapping := `{"gpt-test":"upstream-gpt"}`
	for _, channelID := range []int{301, 302} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "enterprise", Status: common.ChannelStatusEnabled,
			Group: "enterprise", Models: "gpt-test", Priority: &priority, Weight: &weight,
			ModelMapping: &modelMapping,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "enterprise", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()
	now := time.Now().Unix()
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 5, RuntimeGeneration: 1,
		PolicyHash:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ActivationID: 6, ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 3, GroupName: "enterprise", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileEnterpriseSLO,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{
				channelRoutingEnterpriseMemberForTest(31, 301, 3_001, 1, now),
				channelRoutingEnterpriseMemberForTest(32, 302, 3_002, 2, now),
			},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 301, Name: "first", Status: common.ChannelStatusEnabled, ModelMapping: modelMapping, CredentialIDs: []int{3_001}},
			{ID: 302, Name: "second", Status: common.ChannelStatusEnabled, ModelMapping: modelMapping, CredentialIDs: []int{3_002}},
		},
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: true})

	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(ctx, common.RequestIdKey, "enterprise-active-request")
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 100)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEstimatedOutput, 50)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 100)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 50)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "enterprise", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "enterprise", group)
	reservation, ok := routingCapacityReservationFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, routingCapacityReservationPending, reservation.state)
	assert.True(t, HasRoutingStrictCapacityReservation(ctx))
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	assert.False(t, HasRoutingStrictCapacityReservation(ctx))
	assert.Positive(t, fakeRedis.reserveCalls)
	assert.Positive(t, fakeRedis.cancelCalls)

	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("algorithm_version = ?", channelrouting.DecisionAlgorithmBalancedV1).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, model.RoutingDecisionReservationRedisBlock, audit.ReservationMode)
	assert.Equal(t, int64(150), audit.ReservationTotalTPM)
	assert.Equal(t, int64(1), audit.ReservationInflight)
	assert.Positive(t, audit.ReservationResourceCredentialID)
	assert.Equal(t, "upstream-gpt", audit.ReservationResourceModel)
	assert.NotEmpty(t, audit.ReservationPoolSharesJSON)
}

func TestRoutingStrictCapacityCostUsesCapacityUpperBounds(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(channelrouting.ResetSnapshotForTest)
	now := time.Now().Unix()
	baseRatio := 1.0
	completionRatio := 1.0
	pricing := &model.RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "mixed",
		BaseRatio: &baseRatio, CompletionRatio: &completionRatio,
	}
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 8, RuntimeGeneration: 1,
		PolicyHash:   "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ActivationID: 9, ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 4, GroupName: "enterprise-cost", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileEnterpriseSLO,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{{
				ID: 41, PoolID: 4, ChannelID: 401, PhysicalStatus: common.ChannelStatusEnabled,
				LegacyPriority: 10, LegacyWeight: 100, CredentialIDs: []int{4_001},
				Models: []channelrouting.ModelSnapshot{{
					ModelName: "gpt-test", CostKnown: true, Cost: 1, CostUpdatedUnix: now,
					CostGroupRatio: 1, CostBaseRatio: 1, CostCompletionRatio: 1, CostBillingMode: "token",
					CostPricing: pricing, CostPricingHash: strings.Repeat("d", 64),
					CostPricingVersion: "strict-v1", CostObservedTime: now, CostEffectiveTime: now - 60,
					CostExpiresTime: now + 3_600, CostVersionConfidence: model.RoutingCostConfidenceExact,
					CostConfidenceScore: 1, CostFreshness: model.RoutingCostFreshnessFresh,
					CostFreshnessScore: 1, CostSourceSyncStatus: model.RoutingUpstreamSyncStatusSuccess,
				}},
			}},
		}},
		Channels: []channelrouting.ChannelSnapshot{{ID: 401, Status: common.ChannelStatusEnabled}},
	})
	session, err := channelrouting.NewRequestRoutingSession("strict-cost-upper-bound", "enterprise-cost")
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(nil)
	param := &RetryParam{
		Ctx: ctx, ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	}

	worstCase, known, err := routingStrictCapacityCost(
		session, 401, param,
		channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionBoundedKnown, Tokens: 100},
		channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionBoundedKnown, Tokens: 4_096},
	)
	require.NoError(t, err)
	require.True(t, known)
	expected, expectedKnown, err := session.ExpectedCostForChannel(401, channelrouting.RequestRoutingCostInput{
		RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
		PromptTokenEstimate: 100, CompletionTokenEstimate: 50,
	})
	require.NoError(t, err)
	require.True(t, expectedKnown)
	assert.Greater(t, worstCase, expected)
	assert.InDelta(t, float64(4_196)/common.QuotaPerUnit, worstCase, 1e-12)
}

func channelRoutingEnterpriseMemberForTest(
	memberID int,
	channelID int,
	credentialID int,
	cost float64,
	now int64,
) channelrouting.PoolMemberSnapshot {
	member := channelRoutingBalancedMemberForTest(memberID, channelID, cost, now)
	member.PoolID = 3
	member.CredentialIDs = []int{credentialID}
	return member
}

type enterpriseStrictCapacityRedis struct {
	reserveCalls int
	cancelCalls  int
}

func (fake *enterpriseStrictCapacityRedis) Eval(
	_ context.Context,
	script string,
	_ []string,
	args ...interface{},
) *redis.Cmd {
	if strings.Contains(script, "strict_capacity_reserve_v2") {
		fake.reserveCalls++
		now := time.Now().UnixMilli()
		lease, _ := args[1].(int64)
		return redis.NewCmdResult([]interface{}{int64(1), now, now + lease}, nil)
	}
	if strings.Contains(script, "state_value ~= 'pending'") && strings.Contains(script, "HDEL") {
		fake.cancelCalls++
	}
	return redis.NewCmdResult(int64(1), nil)
}
