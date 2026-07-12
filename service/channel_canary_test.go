package service

import (
	"context"
	"strings"
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

func TestChannelRoutingCanaryUsesCacheAndReplaysAfterCapacityExclusion(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	previousCapacity := channelRoutingCanaryCapacity
	previousSlowStart := channelRoutingCanarySlowStart
	previousLimit := channelRoutingCanaryLimit
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelRoutingCanaryCapacity = previousCapacity
		channelRoutingCanarySlowStart = previousSlowStart
		channelRoutingCanaryLimit = previousLimit
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		smart_routing_setting.ResetForTest()
	})

	var err error
	channelRoutingCanaryCapacity, err = channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 16,
		IdleTTL:    time.Hour,
		Shards:     4,
	})
	require.NoError(t, err)
	channelRoutingCanarySlowStart, err = channelrouting.NewSlowStartTracker(channelrouting.SlowStartPolicy{
		MinimumFactor: 0.5,
		RampDuration:  time.Minute,
		StateTTL:      time.Hour,
		MaxEntries:    16,
	}, nil)
	require.NoError(t, err)
	channelRoutingCanaryLimit = channelrouting.Limit{RPM: 10, InputTPM: 100, OutputTPM: 100, Inflight: 1}

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
	channelrouting.SetSnapshotForTest(channelRoutingCanarySnapshotForTest(11, 401, []int{101, 102}))
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeObserve,
	})

	held, err := channelRoutingCanaryCapacity.TryReserve(
		channelrouting.CapacityKey{PoolID: 29, MemberID: 1, Model: "gpt-test"},
		channelrouting.Demand{Inflight: 1},
		channelRoutingCanaryLimit,
	)
	require.NoError(t, err)
	require.NoError(t, held.Commit())
	t.Cleanup(func() { require.NoError(t, held.Release()) })

	ctx, _ := gin.CreateTestContext(nil)
	common.SetContextKey(ctx, common.RequestIdKey, "cohort-both-7500")
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 10)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEstimatedOutput, 20)
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
	identity, ok := GetSelectedRoutingIdentity(ctx, 102)
	require.True(t, ok)
	assert.Equal(t, SelectedRoutingIdentity{ChannelID: 102, SnapshotRevision: 11, PoolID: 29, MemberID: 2}, identity)
	reservation, ok := routingCapacityReservationFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, routingCapacityReservationPending, reservation.state)
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	assert.Equal(t, 1, channelrouting.DecisionAuditsStats().Entries)
	flushed, err := channelrouting.FlushDecisionAuditsContext(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, model.DB.Where("algorithm_version = ?", channelrouting.DecisionAlgorithmCanaryV1).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, int64(401), audit.ActivationID)
	assert.Equal(t, model.RoutingDecisionCohortCanary, audit.Cohort)
	assert.Equal(t, 2, audit.SelectedMemberID)
	assert.Equal(t, 1_002, audit.SelectedCredentialID)
	assert.Equal(t, string(channelrouting.CapacityModeLocalSoft), audit.ReservationMode)
	assert.Equal(t, int64(10), audit.ReservationInputTPM)
	assert.Equal(t, int64(20), audit.ReservationOutputTPM)
	assert.Equal(t, channelRoutingCanaryLimit.Inflight, audit.ReservationLimitInflight)
	var exclusionSummary model.RoutingDecisionExclusionSummary
	require.NoError(t, common.UnmarshalJsonStr(audit.ExclusionSummaryJSON, &exclusionSummary))
	assert.Equal(t, audit.CandidateCount-audit.EligibleCount, exclusionSummary.ExcludedCount)
}

func TestChannelRoutingCanaryControlCohortPreservesLegacyWithoutReservation(t *testing.T) {
	truncate(t)
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		smart_routing_setting.ResetForTest()
	})

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
	channelrouting.SetSnapshotForTest(channelRoutingCanarySnapshotForTest(21, 501, []int{201}))
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
	bypass, err := ShouldBypassChannelRoutingAffinity(ctx, "default")
	require.NoError(t, err)
	assert.False(t, bypass)
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
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: true})

	view := channelRoutingCanarySnapshotForTest(31, 601, []int{301})
	view.Pools = append(view.Pools, channelrouting.PoolSnapshot{
		ID: 30, GroupName: "secondary", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: view.Pools[0].SelectorPolicy,
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
			SelectorPolicy: channelrouting.PoolSelectorPolicy{
				WeightAvailability: 1, AvailabilityFloor: 0.95, MinVolume: 50,
				TopK: 1, MaxEjectedPct: 50, HalfOpenProbes: 1, SnapshotStaleSec: 1_800,
			},
			Members: members,
		}},
		Channels: channels,
	}
}
