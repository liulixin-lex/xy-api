package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	globalsetting "github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingCanaryUsesCacheAndReplaysAfterCapacityExclusion(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousRuntime := channelRoutingCanaryRuntime
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})

	var err error
	channelRoutingCanaryRuntime, err = newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	canaryPolicy := model.DefaultRoutingCanaryPolicy()
	canaryPolicy.Capacity = model.RoutingCanaryCapacityPolicy{
		Mode: model.RoutingCanaryCapacityModeLocalSoft, RPM: 10, InputTPM: 100, OutputTPM: 100, Inflight: 1,
	}
	canaryPolicy.SlowStart.MinimumFactor = 0.5
	canaryPolicy.SlowStart.RampSeconds = 60
	canaryPolicy.SlowStart.StateTTLSeconds = 3_600

	priority := int64(10)
	weight := uint(10)
	for _, channelID := range []int{101, 102} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "canary", Status: common.ChannelStatusEnabled,
			Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()
	view := channelRoutingCanarySnapshotForTest(11, 401, []int{101, 102})
	view.Pools[0].CanaryPolicy = canaryPolicy
	view.Pools[0].Members[0].Models[0].BreakerKnown = true
	view.Pools[0].Members[0].Models[0].BreakerState = routingselector.BreakerStateHalfOpen
	view.Pools[0].Members[0].Models[0].BreakerUpdatedUnix = time.Now().Unix()
	view.Pools[0].Members[1].Models[0].BreakerKnown = true
	view.Pools[0].Members[1].Models[0].BreakerState = routingselector.BreakerStateDegraded
	view.Pools[0].Members[1].Models[0].BreakerUpdatedUnix = time.Now().Unix()
	channelrouting.SetSnapshotForTest(view)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeEnterpriseSLO,
	})

	held, err := channelRoutingCanaryRuntime.tryReserve(11, canaryPolicy,
		channelrouting.CapacityKey{PoolID: 29, MemberID: 1, Model: "gpt-test"},
		channelrouting.Demand{Inflight: 1},
	)
	require.NoError(t, err)
	require.NoError(t, held.Commit())
	t.Cleanup(func() { require.NoError(t, held.Release()) })
	probeKey := routingbreaker.Key{
		ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key: probeKey, State: routingbreaker.StateHalfOpen, UpdatedAt: time.Now(),
	}})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEstimatedOutput, 20)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutput, 20)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputKnown, true)
	require.NoError(t, ensureRoutingChannelTrafficPolicies(context.Background()))
	oldDB := model.DB
	model.DB = nil
	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})
	model.DB = oldDB

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	assert.Equal(t, 102, channel.Id, "the capacity-exhausted first choice must be excluded and deterministically replayed")
	probeSnapshot, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(probeKey, 1)
	require.True(t, acquired, "capacity rejection must immediately release the unused half-open probe")
	assert.Equal(t, 1, probeSnapshot.HalfOpenInflight)
	routingbreaker.ReleaseDefaultHalfOpenProbe(probeKey)
	identity, ok := GetSelectedRoutingIdentity(ctx, 102)
	require.True(t, ok)
	assert.Equal(t, SelectedRoutingIdentity{
		ChannelID: 102, SnapshotRevision: 11, PoolID: 29, MemberID: 2, CredentialID: 1002,
	}, identity)
	reservation, ok := routingCapacityReservationFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, routingCapacityReservationPending, reservation.state)
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, time.Now()))
	assert.Equal(t, 1, channelrouting.DecisionAuditsStats().Entries)
	flushed, err := channelrouting.FlushDecisionAuditsContext(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("algorithm_version = ?", channelrouting.DecisionAlgorithmCanary).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, int64(401), audit.ActivationID)
	assert.Equal(t, model.RoutingDecisionCohortCanary, audit.Cohort)
	assert.Equal(t, 2, audit.SelectedMemberID)
	assert.Equal(t, 1_002, audit.SelectedCredentialID)
	assert.Equal(t, string(channelrouting.CapacityModeLocalSoft), audit.ReservationMode)
	assert.Equal(t, int64(10), audit.ReservationInputTPM)
	assert.Equal(t, int64(20), audit.ReservationOutputTPM)
	assert.Equal(t, canaryPolicy.Capacity.Inflight, audit.ReservationLimitInflight)
	var exclusionSummary model.RoutingDecisionExclusionSummary
	require.NoError(t, common.UnmarshalJsonStr(audit.ExclusionSummaryJSON, &exclusionSummary))
	assert.Equal(t, audit.CandidateCount-audit.EligibleCount, exclusionSummary.ExcludedCount)
}

func TestChannelRoutingCanaryControlCohortPreservesLegacyWithoutReservation(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	smart_routing_setting.ResetForTest()
	clock := &canaryOutcomeTestClock{now: time.Now().Truncate(time.Second)}
	aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
		MaxEntries: 8, Shards: 4, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	channelrouting.ResetCanaryWindowAggregatorForTest(aggregator)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		channelrouting.ResetCanaryWindowAggregatorForTest()
		smart_routing_setting.ResetForTest()
	})
	require.NoError(t, model.DB.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 201, Name: "legacy", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-test", ChannelId: 201,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()
	view := channelRoutingCanarySnapshotForTest(21, 501, []int{201})
	view.Pools[0].CanaryPolicy.Evaluation.WindowSeconds = 60
	view.Pools[0].CanaryPolicy.Evaluation.CheckpointLatenessSeconds = 5
	view.Pools[0].Members[0].Models[0] = channelrouting.ModelSnapshot{
		ModelName: "gpt-test", CostKnown: true, CostUpdatedUnix: time.Now().Unix(),
		CostBillingMode: "per_request", CostGroupRatio: 2, CostModelPrice: 0.25,
	}
	channelrouting.SetSnapshotForTest(view)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeEnterpriseSLO,
	})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-0027")
	channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 201, channel.Id)
	_, selected := GetSelectedRoutingIdentity(ctx, 201)
	assert.False(t, selected)
	_, reserved := routingCapacityReservationFromContext(ctx)
	assert.False(t, reserved)
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, true, true, false, 80, clock.Now()))
	clock.Advance(2 * time.Minute)
	flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	var checkpoint model.RoutingRuntimeCheckpoint
	require.NoError(t, model.DB.Where("checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind).First(&checkpoint).Error)
	window, err := channelrouting.DecodeCanaryCohortWindowCheckpoint(checkpoint)
	require.NoError(t, err)
	assert.Equal(t, int64(1), window.Control.LogicalRequests)
	assert.Equal(t, int64(1), window.Control.CostKnownRequests)
	assert.Equal(t, int64(500_000_000), window.Control.ExpectedPlatformCostNanoUSD)
	assert.Equal(t, 60, window.WindowSeconds)
	assert.Equal(t, 1, channelrouting.DecisionAuditsStats().Entries)
	require.NoError(t, model.DB.AutoMigrate(&model.RoutingDecisionAudit{}, &model.RoutingDecisionReplayChunk{}))
	_, err = channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("cohort = ?", model.RoutingDecisionCohortControl).First(&audit).Error)
	assert.Equal(t, 201, audit.ActualChannelID)
	assert.Equal(t, 1, audit.SelectedMemberID)
	assert.Equal(t, 1_001, audit.SelectedCredentialID)
	assert.False(t, audit.Replayable)
	assert.Empty(t, audit.ReservationMode)
	assert.True(t, audit.ActualCostKnown)
	assert.Equal(t, 0.5, audit.ActualExpectedCost)
	assert.True(t, audit.ObservedCostKnown)
	assert.Equal(t, 0.5, audit.ObservedExpectedCost)
	bypass, err := ShouldBypassChannelRoutingAffinity(ctx, "default")
	require.NoError(t, err)
	assert.False(t, bypass)
}

func TestChannelRoutingCanaryReplaysWhenHalfOpenProbeLeaseIsUnavailable(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousRedisEnabled := common.RedisEnabled
	previousRuntime := channelRoutingCanaryRuntime
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		common.RedisEnabled = previousRedisEnabled
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})
	runtimeClock := &channelRoutingCanaryTestClock{now: time.Now()}
	var err error
	channelRoutingCanaryRuntime, err = newChannelRoutingCanaryRuntimeManager(runtimeClock)
	require.NoError(t, err)

	priority := int64(10)
	weight := uint(10)
	for _, channelID := range []int{101, 102} {
		require.NoError(t, model.DB.Create(&model.Channel{
			Id: channelID, Name: "canary-probe", Status: common.ChannelStatusEnabled,
			Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		}).Error)
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "gpt-test", ChannelId: channelID,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()
	view := channelRoutingCanarySnapshotForTest(11, 701, []int{101, 102})
	canaryPolicy := view.Pools[0].CanaryPolicy
	for memberID := 1; memberID <= 2; memberID++ {
		factor, factorErr := channelRoutingCanaryRuntime.slowStartFactor(11, canaryPolicy, channelrouting.SlowStartKey{
			PoolID: 29, MemberID: memberID, Model: "gpt-test",
		})
		require.NoError(t, factorErr)
		assert.Equal(t, canaryPolicy.SlowStart.MinimumFactor, factor)
	}
	runtimeClock.Advance(time.Duration(canaryPolicy.SlowStart.RampSeconds) * time.Second)
	for memberIndex := range view.Pools[0].Members {
		view.Pools[0].Members[memberIndex].Models[0].BreakerKnown = true
		view.Pools[0].Members[memberIndex].Models[0].BreakerState = routingselector.BreakerStateHalfOpen
		view.Pools[0].Members[memberIndex].Models[0].BreakerUpdatedUnix = time.Now().Unix()
	}
	channelrouting.SetSnapshotForTest(view)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeEnterpriseSLO,
	})

	probeKey := routingbreaker.Key{
		ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	secondProbeKey := probeKey
	secondProbeKey.ChannelID = 102
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{
		{Key: probeKey, State: routingbreaker.StateHalfOpen, UpdatedAt: time.Now()},
		{Key: secondProbeKey, State: routingbreaker.StateHalfOpen, UpdatedAt: time.Now()},
	})
	_, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(probeKey, 1)
	require.True(t, acquired)
	t.Cleanup(func() { routingbreaker.ReleaseDefaultHalfOpenProbe(probeKey) })

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
	channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 102, channel.Id, "a saturated half-open probe must be excluded before replay")
	recoveryFactor, err := channelRoutingCanaryRuntime.slowStartFactor(11, canaryPolicy, channelrouting.SlowStartKey{
		PoolID: 29, MemberID: 2, Model: "gpt-test",
	})
	require.NoError(t, err)
	assert.Equal(t, 1.0, recoveryFactor, "admission alone must not restart slow start before the probe outcome")
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](ctx, constant.ContextKeyRoutingHalfOpenProbes)
	require.True(t, ok)
	_, secondProbeHeld := probes[secondProbeKey]
	assert.True(t, secondProbeHeld)
	require.NoError(t, ObserveRoutingSlowStartProbe(ctx, true, false))
	recoveryFactor, err = channelRoutingCanaryRuntime.slowStartFactor(11, canaryPolicy, channelrouting.SlowStartKey{
		PoolID: 29, MemberID: 2, Model: "gpt-test",
	})
	require.NoError(t, err)
	assert.Equal(t, canaryPolicy.SlowStart.MinimumFactor, recoveryFactor,
		"a successful half-open outcome must restart slow start from the minimum")
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, time.Now()))
	ReleaseAllRoutingHalfOpenProbes(ctx)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Order("id desc").First(&audit).Error)
	var payload struct {
		Candidates []channelrouting.DecisionCandidate `json:"candidates"`
	}
	require.NoError(t, common.UnmarshalJsonStr(audit.CandidatesJSON, &payload))
	reasonByChannel := make(map[int]string, len(payload.Candidates))
	for _, candidate := range payload.Candidates {
		reasonByChannel[candidate.ChannelID] = candidate.ExclusionReason
	}
	assert.Equal(t, channelrouting.ExclusionReasonHalfOpenProbe, reasonByChannel[101], "%+v", payload.Candidates)
	assert.Empty(t, reasonByChannel[102])
	probeSnapshot := routingbreaker.ReleaseDefaultHalfOpenProbe(probeKey)
	assert.Zero(t, probeSnapshot.HalfOpenInflight)
}

func TestChannelRoutingCanaryFailsClosedAndAuditsRedisProbeCoordinatorError(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousRedisEnabled := common.RedisEnabled
	previousRedis := common.RDB
	previousRuntime := channelRoutingCanaryRuntime
	common.MemoryCacheEnabled = true
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("redis unavailable")
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.MemoryCacheEnabled = previousMemoryCache
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedis
		channelRoutingCanaryRuntime = previousRuntime
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})
	var err error
	channelRoutingCanaryRuntime, err = newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 101, Name: "canary-redis-error", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-test", ChannelId: 101,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()
	view := channelRoutingCanarySnapshotForTest(11, 702, []int{101})
	view.Pools[0].Members[0].Models[0].BreakerKnown = true
	view.Pools[0].Members[0].Models[0].BreakerState = routingselector.BreakerStateHalfOpen
	view.Pools[0].Members[0].Models[0].BreakerUpdatedUnix = time.Now().Unix()
	channelrouting.SetSnapshotForTest(view)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: true, Mode: smart_routing_setting.ModeEnterpriseSLO})
	breakerKey := routingbreaker.Key{
		ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key: breakerKey, State: routingbreaker.StateHalfOpen, UpdatedAt: time.Now(),
	}})

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
	channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
		RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	})

	assert.Nil(t, channel)
	assert.ErrorIs(t, err, errRoutingHalfOpenProbeCoordinator)
	_, reserved := routingCapacityReservationFromContext(ctx)
	assert.False(t, reserved)
	assert.Equal(t, 1, channelrouting.DecisionAuditsStats().Entries)
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, time.Now()))
	_, flushErr := channelrouting.FlushDecisionAuditsContext(ctx)
	require.NoError(t, flushErr)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("request_id = ?", "cohort-both-7500").Order("id desc").First(&audit).Error)
	var exclusionSummary model.RoutingDecisionExclusionSummary
	require.NoError(t, common.UnmarshalJsonStr(audit.ExclusionSummaryJSON, &exclusionSummary))
	assert.Contains(t, exclusionSummary.Reasons, model.RoutingDecisionExclusionCount{
		Reason: channelrouting.ExclusionReasonHalfOpenProbe, Count: 1,
	})
	probeSnapshot, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(breakerKey, 1)
	require.True(t, acquired, "a Redis coordinator error must not consume a local Canary probe")
	assert.Equal(t, 1, probeSnapshot.HalfOpenInflight)
	routingbreaker.ReleaseDefaultHalfOpenProbe(breakerKey)
}

func TestChannelRoutingAffinityGatesAutoCanaryBeforeProbeAndPreservesControl(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousRedisEnabled := common.RedisEnabled
	previousAutoGroups := globalsetting.AutoGroups2JsonString()
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	require.NoError(t, globalsetting.UpdateAutoGroupsByJsonString(`["default"]`))
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		common.RedisEnabled = previousRedisEnabled
		require.NoError(t, globalsetting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		channelrouting.ResetSnapshotForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 301, Name: "affinity-half-open", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-test", ChannelId: 301,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()
	channelrouting.SetSnapshotForTest(channelRoutingCanarySnapshotForTest(33, 703, []int{301}))
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeEnterpriseSLO,
		WeightAvailability: 1, TopK: 1, MinVolume: 10,
		HalfOpenProbes: 1, MaxEjectedPct: 100, SnapshotStaleSec: 300,
	})
	now := time.Now()
	breakerKey := routingbreaker.Key{
		ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	cacheKey := routinghotcache.Key{
		ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key: breakerKey, State: routingbreaker.StateHalfOpen, UpdatedAt: now,
	}})
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{
		RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100,
		ReliabilityFailureCount: 1, P95LatencyMs: 100, TPS: 10,
	})
	routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{
		State: routingselector.BreakerStateHalfOpen, UpdatedUnix: now.Unix(),
	})

	canaryCtx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(canaryCtx, common.RequestIdKey, "cohort-both-7500")
	common.SetContextKey(canaryCtx, constant.ContextKeyUserGroup, "default")
	preferred, group, bypass, err := GetAdmissibleAffinityChannelWithRoutingGate(
		canaryCtx, 301, "gpt-test", "auto", "/v1/chat/completions",
	)
	require.NoError(t, err)
	assert.Nil(t, preferred)
	assert.Equal(t, "default", group)
	assert.True(t, bypass)
	_, probeHeld := common.GetContextKey(canaryCtx, constant.ContextKeyRoutingHalfOpenProbes)
	assert.False(t, probeHeld)

	controlCtx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(controlCtx, common.RequestIdKey, "cohort-0027")
	common.SetContextKey(controlCtx, constant.ContextKeyUserGroup, "default")
	preferred, group, bypass, err = GetAdmissibleAffinityChannelWithRoutingGate(
		controlCtx, 301, "gpt-test", "auto", "/v1/chat/completions",
	)
	require.NoError(t, err)
	require.NotNil(t, preferred)
	assert.Equal(t, 301, preferred.Id)
	assert.Equal(t, "default", group)
	assert.False(t, bypass)
	probes, probeHeld := common.GetContextKeyType[map[routingbreaker.Key]struct{}](controlCtx, constant.ContextKeyRoutingHalfOpenProbes)
	require.True(t, probeHeld)
	assert.Contains(t, probes, breakerKey)
	ReleaseAllRoutingHalfOpenProbes(controlCtx)
}

func TestChannelRoutingCanaryAffinityAndAutoGroupsSharePinnedSnapshot(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelrouting.ResetSnapshotForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeEnterpriseSLO,
	})

	view := channelRoutingCanarySnapshotForTest(31, 601, []int{301})
	view.Pools = append(view.Pools, channelrouting.PoolSnapshot{
		ID: 30, GroupName: "secondary", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: view.Pools[0].SelectorPolicy, CanaryPolicy: view.Pools[0].CanaryPolicy,
		Members: []channelrouting.PoolMemberSnapshot{{
			ID: 3, PoolID: 30, ChannelID: 302, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{3_002},
			Models: []channelrouting.ModelSnapshot{{ModelName: "gpt-test"}},
		}},
	})
	view.Channels = append(view.Channels, channelrouting.ChannelSnapshot{ID: 302, Status: common.ChannelStatusEnabled})
	channelrouting.SetSnapshotForTest(view)

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
	bypass, err := ShouldBypassChannelRoutingAffinity(ctx, "default")
	require.NoError(t, err)
	assert.True(t, bypass)

	channelrouting.SetSnapshotForTest(channelRoutingCanarySnapshotForTest(32, 602, []int{401}))
	gate, active, err := channelRoutingCanaryGate(ctx, "secondary")
	require.NoError(t, err)
	require.True(t, active)
	assert.True(t, gate.InCanary)
	assert.Equal(t, uint64(31), gate.PolicyRevision, "all concrete auto groups in one request must retain the same runtime snapshot")
}

func TestGlobalModePreventsCanaryAffinityBypass(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelrouting.ResetSnapshotForTest()
		smart_routing_setting.ResetForTest()
	})
	channelrouting.SetSnapshotForTest(channelRoutingCanarySnapshotForTest(41, 801, []int{501}))

	for _, mode := range []string{
		smart_routing_setting.ModeObserve,
		smart_routing_setting.ModeShadow,
		smart_routing_setting.ModeBalanced,
	} {
		t.Run(mode, func(t *testing.T) {
			setting := smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: true, Mode: mode})
			ctx, _ := gin.CreateTestContext(nil)
			common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
			channel, _, handled, err := cacheGetChannelRoutingCanary(&RetryParam{
				Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test",
				RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
			}, setting)
			require.NoError(t, err)
			assert.Nil(t, channel)
			assert.False(t, handled)

			bypass, err := ShouldBypassChannelRoutingAffinity(ctx, "default")
			require.NoError(t, err)
			assert.False(t, bypass)
		})
	}
}

func channelRoutingCanarySnapshotForTest(revision uint64, activationID int64, channelIDs []int) channelrouting.SnapshotView {
	members := make([]channelrouting.PoolMemberSnapshot, 0, len(channelIDs))
	channels := make([]channelrouting.ChannelSnapshot, 0, len(channelIDs))
	for index, channelID := range channelIDs {
		members = append(members, channelrouting.PoolMemberSnapshot{
			ID: index + 1, PoolID: 29, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_000 + index + 1},
			Models: []channelrouting.ModelSnapshot{{ModelName: "gpt-test"}},
		})
		channels = append(channels, channelrouting.ChannelSnapshot{ID: channelID, Status: common.ChannelStatusEnabled})
	}
	return channelrouting.SnapshotView{
		Revision: revision, RuntimeGeneration: revision,
		PolicyHash: strings.Repeat("a", 64), ActivationID: activationID,
		ActivationStage: model.RoutingDeploymentStageCanary, TrafficBasisPoints: 100,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 29, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			PolicyProfile: model.RoutingPolicyProfileBalanced,
			CanaryPolicy:  model.DefaultRoutingCanaryPolicy(),
			SelectorPolicy: channelrouting.PoolSelectorPolicy{
				WeightAvailability: 1, AvailabilityFloor: 0.95, MinVolume: 50,
				TopK: 1, MaxEjectedPct: 50, HalfOpenProbes: 1, SnapshotStaleSec: 1_800,
			},
			Members: members,
		}},
		Channels: channels,
	}
}

func TestChannelRoutingCanaryRuntimeSharesCapacityAcrossPolicyRevisions(t *testing.T) {
	manager, err := newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Capacity.Inflight = 1
	key := channelrouting.CapacityKey{PoolID: 29, MemberID: 1, Model: "gpt-test"}
	demand := channelrouting.Demand{Inflight: 1}

	first, err := manager.tryReserve(11, policy, key, demand)
	require.NoError(t, err)
	require.NoError(t, first.Commit())
	_, err = manager.tryReserve(11, policy, key, demand)
	assert.ErrorIs(t, err, channelrouting.ErrCapacityExhausted)

	_, err = manager.tryReserve(12, policy, key, demand)
	assert.ErrorIs(t, err, channelrouting.ErrCapacityExhausted)
	require.NoError(t, first.Release())

	second, err := manager.tryReserve(12, policy, key, demand)
	require.NoError(t, err)
	require.NoError(t, second.Cancel())
}

func TestChannelRoutingCanaryRuntimeSlowStartsColdNodeNewMemberAndRevision(t *testing.T) {
	clock := &channelRoutingCanaryTestClock{now: time.Unix(1_000, 0)}
	manager, err := newChannelRoutingCanaryRuntimeManager(clock)
	require.NoError(t, err)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.SlowStart.MinimumFactor = 0.20
	policy.SlowStart.RampSeconds = 100
	policy.SlowStart.StateTTLSeconds = 1_000
	firstKey := channelrouting.SlowStartKey{PoolID: 29, MemberID: 1, Model: "gpt-test"}

	factor, err := manager.slowStartFactor(11, policy, firstKey)
	require.NoError(t, err)
	assert.InDelta(t, 0.20, factor, 1e-9)
	clock.Advance(100 * time.Second)
	factor, err = manager.slowStartFactor(11, policy, firstKey)
	require.NoError(t, err)
	assert.Equal(t, 1.0, factor)
	require.NoError(t, manager.startRecovery(11, policy, firstKey))
	factor, err = manager.slowStartFactor(11, policy, firstKey)
	require.NoError(t, err)
	assert.InDelta(t, 0.20, factor, 1e-9, "a recovered member must restart its ramp")

	newMember := channelrouting.SlowStartKey{PoolID: 29, MemberID: 2, Model: "gpt-test"}
	factor, err = manager.slowStartFactor(11, policy, newMember)
	require.NoError(t, err)
	assert.InDelta(t, 0.20, factor, 1e-9, "a member first observed after the node is warm must still ramp")

	factor, err = manager.slowStartFactor(12, policy, firstKey)
	require.NoError(t, err)
	assert.InDelta(t, 0.20, factor, 1e-9, "a new policy revision must not inherit incompatible slow-start state")
}

func TestRoutingHalfOpenSlowStartRestartsFromMinimumAfterFailureThenSuccess(t *testing.T) {
	clock := &channelRoutingCanaryTestClock{now: time.Unix(2_000, 0)}
	manager, err := newChannelRoutingCanaryRuntimeManager(clock)
	require.NoError(t, err)
	previous := channelRoutingCanaryRuntime
	channelRoutingCanaryRuntime = manager
	t.Cleanup(func() { channelRoutingCanaryRuntime = previous })
	policy := model.DefaultRoutingCanaryPolicy()
	policy.SlowStart.MinimumFactor = 0.25
	policy.SlowStart.RampSeconds = 60
	policy.SlowStart.StateTTLSeconds = 600
	key := channelrouting.SlowStartKey{PoolID: 31, MemberID: 7, Model: "gpt-test"}
	ctx, _ := gin.CreateTestContext(nil)

	require.NoError(t, prepareRoutingSlowStartProbe(ctx, 101, 21, policy, key))
	require.NoError(t, ObserveRoutingSlowStartProbe(ctx, false, true))
	factor, err := manager.slowStartFactor(21, policy, key)
	require.NoError(t, err)
	assert.Zero(t, factor, "a failed half-open probe must hard-stop the member")

	clock.Advance(2 * time.Minute)
	factor, err = manager.slowStartFactor(21, policy, key)
	require.NoError(t, err)
	assert.Zero(t, factor, "elapsed time alone must not recover a failed half-open probe")

	require.NoError(t, prepareRoutingSlowStartProbe(ctx, 101, 21, policy, key))
	require.NoError(t, ObserveRoutingSlowStartProbe(ctx, true, false))
	factor, err = manager.slowStartFactor(21, policy, key)
	require.NoError(t, err)
	assert.InDelta(t, 0.25, factor, 1e-9,
		"the next successful half-open probe must restart recovery from the minimum factor")
}

type channelRoutingCanaryTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *channelRoutingCanaryTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *channelRoutingCanaryTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}
