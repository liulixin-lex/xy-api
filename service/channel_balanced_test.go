package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
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
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
	})

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
	require.NoError(t, model.DB.Where("algorithm_version = ?", channelrouting.DecisionAlgorithmBalanced).
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
	smart_routing_setting.ResetForTest()
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
		smart_routing_setting.ResetForTest()
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
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
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

func TestChannelRoutingBalancedUsesLiveSingleKeyBreakerAndCapacityCooldown(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	routinghotcache.ResetForTest()
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
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(100)
	for _, channelID := range []int{301, 302} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "balanced-live", Status: common.ChannelStatusEnabled,
			Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()

	now := time.Now()
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 3, PolicyHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		RuntimeGeneration: 3, ActivationID: 3,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now.Unix(),
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{
				channelRoutingBalancedMemberForTest(31, 301, 1.5, now.Unix()),
				channelRoutingBalancedMemberForTest(32, 302, 0.5, now.Unix()),
			},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 301, Name: "fallback", Status: common.ChannelStatusEnabled},
			{ID: 302, Name: "preferred", Status: common.ChannelStatusEnabled},
		},
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
	})
	key := routinghotcache.Key{
		ChannelID: 302, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	routinghotcache.SetBreakerForTest(key, routinghotcache.BreakerSnapshot{
		State: routingselector.BreakerStateOpen, CooldownUntilUnix: now.Add(time.Minute).Unix(),
		UpdatedUnix: now.Unix(),
	})

	breakerCtx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(breakerCtx, common.RequestIdKey, "balanced-live-breaker")
	common.SetContextKey(breakerCtx, constant.ContextKeyRoutingCapacityInput, 1)
	common.SetContextKey(breakerCtx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(breakerCtx, constant.ContextKeyRoutingCapacityOutput, 1)
	common.SetContextKey(breakerCtx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	selected, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: breakerCtx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 301, selected.Id, "a live breaker must override the still-healthy frozen snapshot")
	require.NoError(t, CancelRoutingCapacityReservation(breakerCtx))

	routinghotcache.ClearBreaker(key)
	routinghotcache.SetCapacityCooldownForTest(key, routinghotcache.CapacityCooldownSnapshot{
		SourceStatusCode: 429, CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli: now.UnixMilli(),
	})
	capacityCtx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(capacityCtx, common.RequestIdKey, "balanced-live-capacity")
	common.SetContextKey(capacityCtx, constant.ContextKeyRoutingCapacityInput, 1)
	common.SetContextKey(capacityCtx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(capacityCtx, constant.ContextKeyRoutingCapacityOutput, 1)
	common.SetContextKey(capacityCtx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	selected, _, err = CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: capacityCtx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 301, selected.Id, "a live capacity cooldown must override the frozen snapshot")
	require.NoError(t, CancelRoutingCapacityReservation(capacityCtx))
}

func TestChannelRoutingBalancedLiveRuntimePreservesMultiKeyIsolation(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		channelrouting.ResetSnapshotForTest()
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
	})

	now := time.Now()
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 4, PolicyHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		RuntimeGeneration: 4, ActivationID: 4,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now.Unix(),
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{
				channelRoutingBalancedMemberForTest(41, 401, 1, now.Unix()),
				channelRoutingBalancedMemberForTest(42, 402, 1, now.Unix()),
			},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 401, Name: "single", Status: common.ChannelStatusEnabled},
			{ID: 402, Name: "multi", Status: common.ChannelStatusEnabled, MultiKey: true},
		},
	})
	session, err := channelrouting.NewRequestRoutingSession("balanced-live-runtime", "default")
	require.NoError(t, err)

	keyFor := func(channelID int) routinghotcache.Key {
		return routinghotcache.Key{
			ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: "gpt-test", Group: "default",
		}
	}
	for _, channelID := range []int{401, 402} {
		key := keyFor(channelID)
		routinghotcache.SetBreakerForTest(key, routinghotcache.BreakerSnapshot{
			State: routingselector.BreakerStateOpen, CooldownUntilUnix: now.Add(time.Minute).Unix(),
			UpdatedUnix: now.Unix(),
		})
		routinghotcache.SetCapacityCooldownForTest(key, routinghotcache.CapacityCooldownSnapshot{
			SourceStatusCode: 429, CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
			UpdatedUnixMilli: now.UnixMilli(),
		})
	}
	releaseInflight := routingmetrics.BeginInflight(nil, &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 401, RoutingPoolID: 1, RoutingMemberID: 41,
			RoutingSnapshotRevision: 4,
		},
	}, 401)
	t.Cleanup(releaseInflight)

	runtimeByChannelID := channelRoutingBalancedRuntimeByChannelID(session, map[int]*model.Channel{
		401: {Id: 401},
		402: {Id: 402, ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		999: {Id: 999},
	}, "gpt-test", "default")

	require.Contains(t, runtimeByChannelID, 401)
	single := runtimeByChannelID[401]
	assert.True(t, single.HasInflight)
	assert.Equal(t, int64(1), single.Inflight)
	require.NotNil(t, single.Breaker)
	assert.Equal(t, routingselector.BreakerStateOpen, single.Breaker.State)
	require.NotNil(t, single.CooldownUntilUnixMilli)
	assert.Greater(t, *single.CooldownUntilUnixMilli, now.UnixMilli())
	assert.NotContains(t, runtimeByChannelID, 402, "aggregate multi-key runtime must not poison credential routing")
	assert.NotContains(t, runtimeByChannelID, 999, "channels outside the pinned pool snapshot must be ignored")
}

func TestChannelRoutingBalancedCredentialFailureRetriesSameMultiKeyChannelWithHealthyCredential(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	routinghotcache.ResetForTest()
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
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(100)
	const channelID = 501
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: channelID, Name: "credential-failover", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		Key: "key-a\nkey-b", ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-test", ChannelId: channelID,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()

	now := time.Now().Unix()
	member := channelRoutingBalancedMemberForTest(51, channelID, 1, now)
	member.MultiKey = true
	member.CredentialIDs = []int{5_001, 5_002}
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 5, PolicyHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		RuntimeGeneration: 5, ActivationID: 5,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members:        []channelrouting.PoolMemberSnapshot{member},
		}},
		Channels: []channelrouting.ChannelSnapshot{{
			ID: channelID, Name: "credential-failover", Status: common.ChannelStatusEnabled,
			MultiKey: true, CredentialRequired: true, CredentialIDs: []int{5_001, 5_002},
		}},
	})
	routinghotcache.ReplaceChannelTrafficConfigurations(nil, now)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
	})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "balanced-credential-failover")
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 1)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 1)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	param := &RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	}

	first, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	firstIdentity, ok := GetSelectedRoutingIdentity(ctx, channelID)
	require.True(t, ok)
	require.Contains(t, []int{5_001, 5_002}, firstIdentity.CredentialID)
	require.NoError(t, CancelRoutingCapacityReservation(ctx))

	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://shared.example.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-east-1")
	common.SetContextKey(ctx, constant.ContextKeyRoutingFailureDomainHash, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	MarkRoutingTargetTried(ctx, channelID, firstIdentity.CredentialID, true)
	MarkRoutingTargetFailure(ctx, channelID, routingerror.ScopeCredential)
	param.SetRetry(1)

	second, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	secondIdentity, ok := GetSelectedRoutingIdentity(ctx, channelID)
	require.True(t, ok)
	assert.Equal(t, channelID, second.Id)
	assert.NotEqual(t, firstIdentity.CredentialID, secondIdentity.CredentialID)
	assert.Empty(t, smartRoutingExcludedEndpointIdentities(ctx))
	assert.Empty(t, smartRoutingExcludedFailureDomainHashes(ctx))
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
}

func TestChannelRoutingBalancedEndpointFailureExcludesSharedEndpointButNotIndependentFailureDomainPeer(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(channelrouting.ResetSnapshotForTest)
	now := time.Now().Unix()
	const sharedFailureDomain = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 6, PolicyHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		RuntimeGeneration: 6, ActivationID: 6,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:  model.RoutingPolicyProfileBalanced,
			BalancedPolicy: channelRoutingBalancedPolicyForTest(0),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []channelrouting.PoolMemberSnapshot{
				channelRoutingBalancedMemberForTest(61, 601, 1, now),
				channelRoutingBalancedMemberForTest(62, 602, 0.1, now),
				channelRoutingBalancedMemberForTest(63, 603, 0.5, now),
			},
		}},
		Channels: []channelrouting.ChannelSnapshot{
			{ID: 601, Name: "failed", Status: common.ChannelStatusEnabled,
				Endpoint: "https://shared.example.test/v1", FailureDomainHash: sharedFailureDomain},
			{ID: 602, Name: "same-endpoint", Status: common.ChannelStatusEnabled,
				Endpoint: "https://shared.example.test/compatible", FailureDomainHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{ID: 603, Name: "same-account-independent-endpoint", Status: common.ChannelStatusEnabled,
				Endpoint: "https://independent.example.test/v1", FailureDomainHash: sharedFailureDomain},
		},
	})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority,
		channelrouting.EndpointAuthority("https://shared.example.test/v1", 601))
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, channelrouting.RoutingRegion())
	common.SetContextKey(ctx, constant.ContextKeyRoutingFailureDomainHash, sharedFailureDomain)
	MarkRoutingTargetTried(ctx, 601, 0, false)
	MarkRoutingTargetFailure(ctx, 601, routingerror.ScopeEndpoint)

	session, err := channelrouting.NewRequestRoutingSession("balanced-endpoint-scope", "default")
	require.NoError(t, err)
	plan, active, err := session.PlanBalanced(channelrouting.BalancedRoutingPlanInput{
		RequestRoutingPlanInput: channelrouting.RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			AllowedChannelIDs:           []int{601, 602, 603},
			ExcludedChannelIDs:          []int{601},
			ExcludedEndpointIdentities:  smartRoutingExcludedEndpointList(ctx),
			ExcludedFailureDomainHashes: smartRoutingExcludedFailureDomainList(ctx),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 603, plan.SelectedChannelID)
	assert.Empty(t, smartRoutingExcludedFailureDomainHashes(ctx))
	reasons := make(map[int]string, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		reasons[candidate.ChannelID] = candidate.ExclusionReason
	}
	assert.Equal(t, channelrouting.ExclusionReasonRequestFailed, reasons[601])
	assert.Equal(t, channelrouting.ExclusionReasonEndpointRequest, reasons[602])
	assert.Empty(t, reasons[603])
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
