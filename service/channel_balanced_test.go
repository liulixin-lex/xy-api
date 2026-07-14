package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingBalancedActiveUsesExactCostWithoutBypassingAffinityPolicy(t *testing.T) {
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
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(100)
	for _, channelID := range []int{101, 102} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "balanced", Status: common.ChannelStatusEnabled,
			Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()
	now := time.Now().Unix()
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, PolicyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RuntimeGeneration: 1, ActivationID: 1,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{
				channelRoutingBalancedMemberForTest(11, 101, 4, now),
				channelRoutingBalancedMemberForTest(12, 102, 0.5, now),
			},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 101, Name: "expensive", Status: common.ChannelStatusEnabled},
			{ID: 102, Name: "cheap", Status: common.ChannelStatusEnabled},
		},
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: true})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "balanced-active-request")
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, int(common.QuotaPerUnit))
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, int(common.QuotaPerUnit))
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 0)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	preferred, group, bypass, err := GetAdmissibleAffinityChannelWithRoutingGate(
		ctx, 101, "gpt-test", "default", "/v1/chat/completions",
	)
	require.NoError(t, err)
	assert.Nil(t, preferred)
	assert.Equal(t, "default", group)
	assert.True(t, bypass, "active affinity must be evaluated by the protection band")

	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	assert.Equal(t, 102, channel.Id, "an out-of-band expensive affinity target must not bypass Balanced")
	identity, ok := GetSelectedRoutingIdentity(ctx, 102)
	require.True(t, ok)
	assert.Equal(t, 12, identity.MemberID)
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	assert.Equal(t, 1, channelrouting.DecisionAuditsStats().Entries)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("algorithm_version = ?", channelrouting.DecisionAlgorithmBalancedV1).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, model.RoutingDeploymentStageActive, audit.ActivationStage)
	assert.Equal(t, 12, audit.SelectedMemberID)
	assert.True(t, audit.Replayable)
	replayed, err := channelrouting.ReplayBalancedDecisionAudit(audit)
	require.NoError(t, err)
	assert.Equal(t, 102, replayed.SelectedChannelID)
}

func TestReservePinnedChannelRoutingAttemptKeepsExactIdentityAndCapacity(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	previousRuntime := channelRoutingCanaryRuntime
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	var err error
	channelRoutingCanaryRuntime, err = newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
	})

	priority := int64(10)
	weight := uint(100)
	for _, channelID := range []int{201, 202} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "pinned", Status: common.ChannelStatusEnabled,
			Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()

	now := time.Now().Unix()
	capacityPolicy := model.DefaultRoutingCanaryPolicy()
	capacityPolicy.Capacity.RPM = 1
	firstMember := channelRoutingBalancedMemberForTest(21, 201, 4, now)
	firstMember.CredentialIDs = []int{2_001, 2_002}
	secondMember := channelRoutingBalancedMemberForTest(22, 202, 0.5, now)
	secondMember.CredentialIDs = []int{2_003}
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 2, PolicyHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RuntimeGeneration: 2, ActivationID: 2,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   capacityPolicy,
			Members:        []channelrouting.PoolMemberSnapshot{firstMember, secondMember},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 201, Name: "expensive", Status: common.ChannelStatusEnabled, MultiKey: true,
				CredentialRequired: true, CredentialIDs: []int{2_001, 2_002}},
			{ID: 202, Name: "cheap", Status: common.ChannelStatusEnabled,
				CredentialRequired: true, CredentialIDs: []int{2_003}},
		},
	})

	newParam := func(requestID string) *RetryParam {
		ctx, _ := gin.CreateTestContext(nil)
		common.SetContextKey(ctx, common.RequestIdKey, requestID)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 1)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 1)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
		return &RetryParam{
			Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
			RequestPath: "/v1/videos", Retry: common.GetPointer(0),
		}
	}

	firstParam := newParam("pinned-first")
	selected, active, err := ReservePinnedChannelRoutingAttempt(firstParam, "default", 201, 2_002)
	require.NoError(t, err)
	require.True(t, active)
	require.NotNil(t, selected)
	assert.Equal(t, 201, selected.Id)
	identity, planned := GetSelectedRoutingIdentity(firstParam.Ctx, 201)
	require.True(t, planned)
	assert.Equal(t, 2_002, identity.CredentialID)

	secondParam := newParam("pinned-second")
	selected, active, err = ReservePinnedChannelRoutingAttempt(secondParam, "default", 201, 2_002)
	require.Error(t, err)
	assert.ErrorIs(t, err, channelrouting.ErrCapacityExhausted)
	assert.True(t, active)
	assert.Nil(t, selected, "capacity pressure on the pinned target must not borrow another channel")

	require.NoError(t, CancelRoutingCapacityReservation(firstParam.Ctx))
	unavailableParam := newParam("pinned-unavailable")
	selected, active, err = ReservePinnedChannelRoutingAttempt(unavailableParam, "default", 201, 9_999)
	require.ErrorIs(t, err, ErrPinnedRoutingIdentityUnavailable)
	assert.True(t, active)
	assert.Nil(t, selected)
}

func channelRoutingBalancedPolicyForTest(protectionBand int) channelrouting.BalancedPoolPolicy {
	return channelrouting.BalancedPoolPolicy{
		WeightAvailability: 0.1, WeightLatency: 0.1, WeightThroughput: 0.1, WeightCost: 0.7,
		AvailabilityTarget: 0.99, AvailabilityFloor: 0.95, LatencyTargetMs: 200,
		ThroughputTarget: 20, CostTarget: 1, CostBudget: 2, MinVolume: 50, WilsonZ: 1.96,
		UnknownAvailability: 0.5, UnknownLatencyUtility: 0.5,
		UnknownThroughputUtility: 0.5, UnknownCostUtility: 0.4,
		ProtectionBandBasisPoints: protectionBand, MinimumExplorationScore: 0.05,
		MaxCapacityUtilization: 1, AffinityMaxCapacityUtilization: 0.8, QueueTargetMs: 50,
		DegradedMultiplier: 0.5, SoftFallbackMultiplier: 0.1,
		HalfOpenProbes: 1, SnapshotStaleSec: 1_800, BalanceMarginUSD: 1,
		RequireKnownCost: true, AllowSoftFailureFallback: true,
	}
}

func channelRoutingBalancedMemberForTest(memberID int, channelID int, baseRatio float64, now int64) channelrouting.PoolMemberSnapshot {
	return channelrouting.PoolMemberSnapshot{
		ID: memberID, PoolID: 1, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 10, LegacyWeight: 100,
		Models: []channelrouting.ModelSnapshot{{
			ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 100,
			ReliabilityRequestCount: 100, P95LatencyKnown: true, P95LatencyMs: 100,
			OutputTokensPerSecond: 20, MetricUpdatedUnix: now,
			CostKnown: true, Cost: 1, CostUpdatedUnix: now, CostGroupRatio: 1,
			CostBaseRatio: baseRatio, CostCompletionRatio: 1, CostBillingMode: "token",
		}},
	}
}
