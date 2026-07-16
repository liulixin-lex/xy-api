package channelrouting

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestRoutingSessionPlanBalancedUsesExactRequestCostAndPinnedSnapshot(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-request", "default")
	require.NoError(t, err)

	changed := balancedActiveSnapshotForTest(t, now)
	changed.Revision = 2
	changed.Pools[0].Members[0].Models[0].CostBaseRatio = 0.1
	changed.Pools[0].Members[1].Models[0].CostBaseRatio = 4
	SetSnapshotForTest(changed)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test", IsStream: true,
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, uint64(1), plan.PolicyRevision)
	assert.Equal(t, 102, plan.SelectedChannelID)
	assert.Equal(t, 12, plan.SelectedIdentity.MemberID)
	assert.True(t, plan.SelectedCostKnown)
	assert.InDelta(t, 0.5, plan.SelectedCost, 1e-9)
}

func TestRequestRoutingSessionPlanBalancedExcludesChannelBalanceSignal(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	now := time.Now()
	view := balancedActiveSnapshotForTest(t, now.Unix())
	// Keep the fallback member inside the policy's hard cost budget so this
	// regression isolates the channel-level 402 exclusion signal.
	view.Pools[0].Members[0].Models[0].CostBaseRatio = 1.5
	SetSnapshotForTest(view)
	routinghotcache.SetChannelBalanceUnavailableForTest(102, routinghotcache.ChannelBalanceUnavailableSnapshot{
		SourceStatusCode:       http.StatusPaymentRequired,
		Reason:                 ExclusionReasonChannelBalance,
		CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	})
	session, err := NewRequestRoutingSession("balanced-channel-balance", "default")
	require.NoError(t, err)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 101, plan.SelectedChannelID)
	for _, candidate := range plan.Replay.Candidates {
		if candidate.ChannelID == 102 {
			assert.Equal(t, ExclusionReasonChannelBalance, candidate.HardExclusionReason)
			return
		}
	}
	t.Fatal("channel 102 replay candidate not found")
}

func TestRequestRoutingSessionPlanBalancedExcludesFailedTargetIdentities(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	view.Pools[0].Members = nil
	view.Channels = nil
	for index, channelID := range []int{101, 102, 103, 104, 105} {
		member := balancedSessionMemberForTest(11+index, channelID, 0.5, now)
		member.CredentialIDs = []int{1_001 + index}
		view.Pools[0].Members = append(view.Pools[0].Members, member)
		view.Channels = append(view.Channels, ChannelSnapshot{
			ID: channelID, Name: "candidate", Status: common.ChannelStatusEnabled,
			CredentialRequired: true, CredentialIDs: []int{1_001 + index},
		})
	}
	view.Channels[2].Endpoint = "https://failed-endpoint.example.test/v1"
	view.Channels[3].FailureDomainHash = strings.Repeat("f", 64)
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-request-exclusions", "default")
	require.NoError(t, err)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate:   int(common.QuotaPerUnit),
			ExcludedChannelIDs:    []int{101},
			ExcludedCredentialIDs: []int{1_002},
			ExcludedEndpointIdentities: []RequestEndpointIdentity{{
				EndpointAuthority: "https://failed-endpoint.example.test:443",
				Region:            RoutingRegion(),
			}},
			ExcludedFailureDomainHashes: []string{strings.Repeat("f", 64)},
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 105, plan.SelectedChannelID)

	reasons := make(map[int]string, len(plan.Replay.Candidates))
	for _, candidate := range plan.Replay.Candidates {
		reasons[candidate.ChannelID] = candidate.HardExclusionReason
	}
	assert.Equal(t, ExclusionReasonCredentialRequest, reasons[102])
	assert.Equal(t, ExclusionReasonEndpointRequest, reasons[103])
	assert.Equal(t, ExclusionReasonFailureDomainRequest, reasons[104])
	for _, candidate := range plan.Candidates {
		if candidate.ChannelID == 101 {
			assert.False(t, candidate.Eligible)
			assert.Equal(t, ExclusionReasonRequestFailed, candidate.ExclusionReason)
		}
	}
}

func TestRequestRoutingSessionPlanBalancedScopesEndpointBreakerByRegion(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1, BaseCooldown: time.Minute, MaxCooldown: time.Minute,
		EntryTTL: time.Hour, MaxEntries: 64,
	})
	t.Setenv("ROUTING_REGION", "us-east-1")
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	view.Pools[0].Members[0].Models[0].CostBaseRatio = 1.9
	firstEndpoint := "https://first.example.test/v1"
	secondEndpoint := "https://second.example.test/v1"
	view.Channels[0].Endpoint = firstEndpoint
	view.Channels[1].Endpoint = secondEndpoint
	SetSnapshotForTest(view)

	westKey := routingbreaker.NewEndpointKey(EndpointAuthority(secondEndpoint, 102), "us-west-1")
	routingbreaker.RecordReliabilityFailure(westKey, routingbreaker.FailureNetwork)
	westSession, err := NewRequestRoutingSession("balanced-endpoint-west", "default")
	require.NoError(t, err)
	westPlan, active, err := westSession.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 102, westPlan.SelectedChannelID, "another region must not affect this node")

	eastKey := routingbreaker.NewEndpointKey(EndpointAuthority(secondEndpoint, 102), "us-east-1")
	routingbreaker.RecordReliabilityFailure(eastKey, routingbreaker.FailureNetwork)
	eastCached, eastCachedOK := routinghotcache.GetBreaker(eastKey.HotcacheKey())
	require.True(t, eastCachedOK)
	assert.Equal(t, string(routingbreaker.StateOpen), eastCached.State)
	endpointBreaker, _, _ := endpointBreakerForChannel(view.Channels[1], time.Now(), view.Pools[0].BalancedPolicy.SnapshotStaleSec)
	require.NotNil(t, endpointBreaker)
	assert.Equal(t, string(routingbreaker.StateOpen), endpointBreaker.State)
	eastSession, err := NewRequestRoutingSession("balanced-endpoint-east", "default")
	require.NoError(t, err)
	eastPlan, active, err := eastSession.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	var replayBreaker *ShadowBreakerInput
	for _, state := range eastPlan.Replay.RuntimeStates {
		if state.ChannelID == 102 {
			replayBreaker = state.Breaker
		}
	}
	require.NotNil(t, replayBreaker)
	assert.Equal(t, routingselector.BreakerStateOpen, replayBreaker.State)
	assert.Greater(t, replayBreaker.CooldownUntilUnix, time.Now().Unix())
	assert.Equal(t, 101, eastPlan.SelectedChannelID)
	assert.Positive(t, eastPlan.FilteredOpen)
}

func TestRequestRoutingSessionPlanBalancedUsesAffinityOnlyInsideProtectionBand(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	view.Pools[0].BalancedPolicy.CostBudget = 0
	view.Pools[0].BalancedPolicy.RequireKnownCost = false
	view.Pools[0].BalancedPolicy.ProtectionBandBasisPoints = 10_000
	view.Pools[0].Members[0].Models[0].CostBaseRatio = 0.5
	view.Pools[0].Members[1].Models[0].CostBaseRatio = 4
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-affinity", "default")
	require.NoError(t, err)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
		PreferredChannelID: 102,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 102, plan.SelectedChannelID)
	assert.True(t, plan.AffinityUsed)

	view.Pools[0].BalancedPolicy.ProtectionBandBasisPoints = 0
	SetSnapshotForTest(view)
	session, err = NewRequestRoutingSession("balanced-affinity-outside", "default")
	require.NoError(t, err)
	plan, active, err = session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
		PreferredChannelID: 102,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 101, plan.SelectedChannelID)
	assert.False(t, plan.AffinityUsed)
}

func balancedActiveSnapshotForTest(t *testing.T, now int64) SnapshotView {
	t.Helper()
	policy, err := resolveBalancedPoolPolicy(model.RoutingPolicyProfileBalanced, []byte(`{
		"weight_availability": 0.1,
		"weight_latency": 0.1,
		"weight_throughput": 0.1,
		"weight_cost": 0.7,
		"cost_budget": 2,
		"require_known_cost": true,
		"exploration_basis_points": 0
	}`))
	require.NoError(t, err)
	return SnapshotView{
		Revision: 1, PolicyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RuntimeGeneration: 1, ActivationID: 1,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile: model.RoutingPolicyProfileBalanced, BalancedPolicy: policy,
			Members: []PoolMemberSnapshot{
				balancedSessionMemberForTest(11, 101, 3, now),
				balancedSessionMemberForTest(12, 102, 0.5, now),
			},
		}},
		Channels: []ChannelSnapshot{
			{ID: 101, Name: "first", Status: common.ChannelStatusEnabled},
			{ID: 102, Name: "second", Status: common.ChannelStatusEnabled},
		},
	}
}

func balancedSessionMemberForTest(memberID int, channelID int, baseRatio float64, now int64) PoolMemberSnapshot {
	return PoolMemberSnapshot{
		ID: memberID, PoolID: 1, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyWeight: 100,
		Models: []ModelSnapshot{{
			ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 100,
			ReliabilityRequestCount: 100, P95LatencyKnown: true, P95LatencyMs: 100,
			P95TTFTKnown: true, P95TTFTMs: 80, OutputTokensPerSecond: 20, MetricUpdatedUnix: now,
			CostKnown: true, Cost: 1, CostUpdatedUnix: now, CostGroupRatio: 1,
			CostBaseRatio: baseRatio, CostCompletionRatio: 1, CostBillingMode: "token",
		}},
	}
}
