package channelrouting

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestRoutingSessionPinsOneSnapshotAndConcretePool(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	_, err := NewRequestRoutingSession("request", "default")
	assert.ErrorIs(t, err, ErrRoutingSessionUnavailable)

	firstView := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	firstView.Pools[0].CanaryPolicy.Capacity.RPM = 321
	SetSnapshotForTest(firstView)
	for _, groupName := range []string{"", " ", "auto", "AUTO"} {
		_, err = NewRequestRoutingSession("request", groupName)
		assert.ErrorIs(t, err, ErrRoutingSessionGroupRequired)
	}
	_, err = NewRequestRoutingSession("", "default")
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)

	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	assert.Equal(t, uint64(11), session.SnapshotRevision())
	assert.Equal(t, uint64(3), session.RuntimeGeneration())
	assert.Equal(t, 29, session.PoolID())
	pinnedPolicy, err := session.CanaryPolicy()
	require.NoError(t, err)
	assert.Equal(t, int64(321), pinnedPolicy.Capacity.RPM)

	secondView := canarySessionSnapshotForTest(12, 4, 402, 29, 201)
	secondView.Pools[0].CanaryPolicy.Capacity.RPM = 654
	SetSnapshotForTest(secondView)
	plan, active, err := session.Plan(RequestRoutingPlanInput{ModelName: "gpt-test"})
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, plan.Gate.InCanary)
	assert.Equal(t, uint64(11), plan.Replay.PolicyRevision)
	assert.Equal(t, uint64(3), plan.Replay.RuntimeGeneration)
	assert.Equal(t, 101, plan.Result.SelectedChannelID)
	assert.Equal(t, Identity{SnapshotRevision: 11, PoolID: 29, MemberID: 11, CredentialID: 1_001}, plan.SelectedIdentity)
	pinnedPolicy, err = session.CanaryPolicy()
	require.NoError(t, err)
	assert.Equal(t, int64(321), pinnedPolicy.Capacity.RPM)

	newSession, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	assert.Equal(t, uint64(12), newSession.SnapshotRevision())
	newPolicy, err := newSession.CanaryPolicy()
	require.NoError(t, err)
	assert.Equal(t, int64(654), newPolicy.Capacity.RPM)
	newPlan, active, err := newSession.Plan(RequestRoutingPlanInput{ModelName: "gpt-test"})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 201, newPlan.Result.SelectedChannelID)
}

func TestRequestRoutingSessionSetPinsOneSnapshotAcrossConcreteGroups(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(21, 4, 501, 41, 301)
	view.Pools = append(view.Pools, PoolSnapshot{
		ID: 42, GroupName: "secondary", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
		Members: []PoolMemberSnapshot{{
			ID: 22, PoolID: 42, ChannelID: 302, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{2_002},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		}},
	})
	view.Channels = append(view.Channels, ChannelSnapshot{ID: 302, Status: common.ChannelStatusEnabled})
	SetSnapshotForTest(view)

	sessions, err := NewRequestRoutingSessionSet("cohort-0005")
	require.NoError(t, err)
	primary, err := sessions.Session("default")
	require.NoError(t, err)
	assert.Equal(t, uint64(21), primary.SnapshotRevision())

	SetSnapshotForTest(canarySessionSnapshotForTest(22, 5, 502, 41, 401))
	secondary, err := sessions.Session("secondary")
	require.NoError(t, err)
	assert.Equal(t, uint64(21), secondary.SnapshotRevision())
	assert.Equal(t, 42, secondary.PoolID())
	_, err = sessions.Session("auto")
	assert.ErrorIs(t, err, ErrRoutingSessionGroupRequired)
}

func TestRequestRoutingSessionReturnsCanaryGateForControlCohort(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))

	session, err := NewRequestRoutingSession("cohort-0027", "default")
	require.NoError(t, err)
	called := false
	plan, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test",
		SlowStartFactor: func(SlowStartKey) (float64, error) {
			called = true
			return 0.5, nil
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.False(t, plan.Gate.InCanary)
	assert.Equal(t, 221, plan.Gate.Bucket)
	assert.False(t, called, "control traffic must preserve the legacy path without building canary candidates")
	assert.Zero(t, plan.Replay)
	assert.Zero(t, plan.Result)
	assert.Zero(t, plan.SelectedIdentity)
}

func TestRequestRoutingSessionReadsFreshExpectedCostWithoutPlanningControlCandidates(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members[0].Models[0] = ModelSnapshot{
		ModelName: "gpt-test", CostKnown: true, CostUpdatedUnix: time.Now().Unix(),
		CostBillingMode: "per_request", CostGroupRatio: 2, CostModelPrice: 0.25,
	}
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("cohort-0027", "default")
	require.NoError(t, err)

	cost, known, err := session.ExpectedCostForChannel(101, RequestRoutingCostInput{
		RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
	})
	require.NoError(t, err)
	require.True(t, known)
	assert.Equal(t, 0.5, cost)

	view.Pools[0].Members[0].Models[0].CostUpdatedUnix = time.Now().Add(-time.Hour).Unix()
	SetSnapshotForTest(view)
	staleSession, err := NewRequestRoutingSession("cohort-0027", "default")
	require.NoError(t, err)
	cost, known, err = staleSession.ExpectedCostForChannel(101, RequestRoutingCostInput{
		RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
	})
	require.NoError(t, err)
	assert.False(t, known)
	assert.Zero(t, cost)
}

func TestRequestRoutingSessionUsesVersionedExpectedAndWorstCost(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	inputRate := 2.0
	outputRate := 10.0
	pricing := &model.RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
		InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
	}
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members[0].Models[0] = ModelSnapshot{
		ModelName: "gpt-test", CostPricing: pricing, CostPricingHash: strings.Repeat("a", 64),
		CostPricingVersion: "provider-v1", CostObservedTime: now, CostEffectiveTime: now,
		CostExpiresTime: now + 3600, CostVersionConfidence: model.RoutingCostConfidenceExact,
		CostConfidenceScore: 1, CostFreshness: model.RoutingCostFreshnessFresh, CostFreshnessScore: 1,
	}
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("cohort-0027", "default")
	require.NoError(t, err)
	profile := &model.RoutingCostRequestProfile{
		PromptTokens: 1_000, ExpectedCompletionTokens: 500, MaximumCompletionTokens: 1_000,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
		RequestInputKnown: true,
	}
	input := RequestRoutingCostInput{
		RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
		PromptTokenEstimate: 1_000, CompletionTokenEstimate: 500, CostProfile: profile,
	}

	estimate, available, err := session.CostEstimateForChannel(101, input)
	require.NoError(t, err)
	require.True(t, available)
	assert.True(t, estimate.Known)
	assert.True(t, estimate.WorstCaseKnown)
	assert.InDelta(t, 0.007, estimate.Cost, 1e-12)
	assert.InDelta(t, 0.012, estimate.WorstCaseCost, 1e-12)
	assert.Equal(t, "token", estimate.PricingBasis)

	profile.InputTokensKnown = false
	estimate, available, err = session.CostEstimateForChannel(101, input)
	require.NoError(t, err)
	require.True(t, available)
	assert.False(t, estimate.Known, "a charged input dimension with unknown quantity is not fully comparable")
	assert.False(t, estimate.WorstCaseKnown)
	_, worstKnown, err := session.WorstCaseCostForChannel(101, input)
	require.NoError(t, err)
	assert.False(t, worstKnown)
}

func TestRequestRoutingSessionPlanUsesPoolModelIndexAndFailsClosed(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members = []PoolMemberSnapshot{
		{
			ID: 11, PoolID: 29, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_001},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 12, PoolID: 29, ChannelID: 102, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_002},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 13, PoolID: 29, ChannelID: 103, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_003},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 14, PoolID: 29, ChannelID: 104, PhysicalStatus: common.ChannelStatusEnabled, MultiKey: true,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_004, 1_005},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 15, PoolID: 29, ChannelID: 105, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_006},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 16, PoolID: 29, ChannelID: 106, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_007},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 17, PoolID: 29, ChannelID: 107, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_008},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
	}
	view.Pools = append(view.Pools, PoolSnapshot{
		ID: 30, GroupName: "other", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		Members: []PoolMemberSnapshot{{
			ID: 99, PoolID: 30, ChannelID: 999, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 1_000, LegacyWeight: 1_000, Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		}},
	})
	view.Channels = []ChannelSnapshot{
		{ID: 101, Status: common.ChannelStatusEnabled},
		{ID: 102, Status: common.ChannelStatusEnabled},
		{ID: 103, Status: common.ChannelStatusEnabled},
		{ID: 104, Status: common.ChannelStatusEnabled, MultiKey: true},
		{ID: 105, Status: common.ChannelStatusEnabled},
		{ID: 106, Status: common.ChannelStatusEnabled, Endpoint: "https://failed-endpoint.example.test/v1"},
		{ID: 107, Status: common.ChannelStatusEnabled, FailureDomainHash: strings.Repeat("f", 64)},
		{ID: 999, Status: common.ChannelStatusEnabled},
	}
	SetSnapshotForTest(view)

	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	var slowStartKeys []SlowStartKey
	plan, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName:                  "gpt-test",
		AllowedChannelIDs:          []int{101, 102, 103, 104, 105, 106, 107},
		CapacityExcludedChannelIDs: []int{103},
		ProbeExcludedChannelIDs:    []int{105},
		ExcludedChannelIDs: []int{
			102,
		},
		ExcludedCredentialIDs: []int{1_004, 1_005},
		ExcludedEndpointIdentities: []RequestEndpointIdentity{{
			EndpointAuthority: "https://failed-endpoint.example.test:443",
			Region:            RoutingRegion(),
		}},
		ExcludedFailureDomainHashes: []string{strings.Repeat("f", 64)},
		SlowStartFactor: func(key SlowStartKey) (float64, error) {
			slowStartKeys = append(slowStartKeys, key)
			return 0.25, nil
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, plan.Gate.InCanary)
	assert.Equal(t, DecisionAlgorithmCanary, plan.Replay.AlgorithmVersion)
	require.Len(t, plan.Replay.Candidates, 7, "only the target pool/model index may feed the plan")
	assert.Equal(t, []SlowStartKey{{PoolID: 29, MemberID: 11, Model: "gpt-test"}}, slowStartKeys)
	assert.Equal(t, 0.25, plan.Replay.Candidates[0].SlowStartFactor)
	assert.Equal(t, ExclusionReasonRequestFailed, plan.Replay.Candidates[1].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonLocalCapacity, plan.Replay.Candidates[2].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonCredentialRequest, plan.Replay.Candidates[3].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonHalfOpenProbe, plan.Replay.Candidates[4].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonEndpointRequest, plan.Replay.Candidates[5].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonFailureDomainRequest, plan.Replay.Candidates[6].RequestExclusionReason)

	assert.Equal(t, 101, plan.Result.SelectedChannelID)
	assert.Equal(t, Identity{SnapshotRevision: 11, PoolID: 29, MemberID: 11, CredentialID: 1_001}, plan.SelectedIdentity)
	candidates := make(map[int]DecisionCandidate, len(plan.Result.Candidates))
	for _, candidate := range plan.Result.Candidates {
		candidates[candidate.ChannelID] = candidate
	}
	assert.True(t, candidates[101].Eligible)
	assert.False(t, candidates[102].Eligible, "an explicitly failed request target must never remain eligible")
	assert.Equal(t, ExclusionReasonRequestFailed, candidates[102].ExclusionReason)
	assert.False(t, candidates[103].Eligible)
	assert.Equal(t, ExclusionReasonLocalCapacity, candidates[103].ExclusionReason)
	assert.False(t, candidates[104].Eligible)
	assert.False(t, candidates[105].Eligible)
	assert.Equal(t, ExclusionReasonHalfOpenProbe, candidates[105].ExclusionReason)
	assert.False(t, candidates[106].Eligible)
	assert.Equal(t, ExclusionReasonEndpointRequest, candidates[106].ExclusionReason)
	assert.False(t, candidates[107].Eligible)
	assert.Equal(t, ExclusionReasonFailureDomainRequest, candidates[107].ExclusionReason)
	_, crossedPool := candidates[999]
	assert.False(t, crossedPool)

	pinned, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName:            "gpt-test",
		AllowedChannelIDs:    []int{104},
		RequiredCredentialID: 1_005,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 104, pinned.Result.SelectedChannelID)
	assert.Equal(t, 1_005, pinned.SelectedIdentity.CredentialID)

	now := time.Now()
	routinghotcache.SetChannelBalanceUnavailableForTest(104, routinghotcache.ChannelBalanceUnavailableSnapshot{
		SourceStatusCode:       http.StatusPaymentRequired,
		Reason:                 ExclusionReasonChannelBalance,
		CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	})
	blocked, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName:            "gpt-test",
		AllowedChannelIDs:    []int{104},
		RequiredCredentialID: 1_005,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Zero(t, blocked.Result.SelectedChannelID)
	require.Len(t, blocked.Replay.Candidates, 7)
	assert.Equal(t, ExclusionReasonChannelBalance, blocked.Replay.Candidates[3].RequestExclusionReason)

	unavailable, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName:            "gpt-test",
		AllowedChannelIDs:    []int{104},
		RequiredCredentialID: 9_999,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Zero(t, unavailable.Result.SelectedChannelID)
	require.Len(t, unavailable.Replay.Candidates, 7)
	assert.Equal(t, ExclusionReasonCredentialUnavailable, unavailable.Replay.Candidates[3].RequestExclusionReason)
}

func TestRequestRoutingSessionPlanRejectsInvalidBoundedInputs(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))
	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)

	_, active, err := session.Plan(RequestRoutingPlanInput{ModelName: "gpt-test", AllowedChannelIDs: []int{0}})
	assert.True(t, active)
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)

	_, active, err = session.Plan(RequestRoutingPlanInput{ModelName: "gpt-test", ProbeExcludedChannelIDs: []int{0}})
	assert.True(t, active)
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)

	_, active, err = session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test",
		SlowStartFactor: func(SlowStartKey) (float64, error) {
			return 1.01, nil
		},
	})
	assert.True(t, active)
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)
}

func TestRequestRoutingSessionReplanPinsTimeAndSlowStartFactors(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members = append(view.Pools[0].Members, PoolMemberSnapshot{
		ID: 12, PoolID: 29, ChannelID: 102, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_002},
		Models: []ModelSnapshot{{ModelName: "gpt-test"}},
	})
	view.Channels = append(view.Channels, ChannelSnapshot{ID: 102, Status: common.ChannelStatusEnabled})
	SetSnapshotForTest(view)

	sessions, err := NewRequestRoutingSessionSet("cohort-0005")
	require.NoError(t, err)
	session, err := sessions.Session("default")
	require.NoError(t, err)
	calls := 0
	factor := func(SlowStartKey) (float64, error) {
		calls++
		return float64(calls) / 10, nil
	}
	first, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test", AllowedChannelIDs: []int{101, 102}, SlowStartFactor: factor,
	})
	require.NoError(t, err)
	require.True(t, active)
	secondSession, err := sessions.Session("default")
	require.NoError(t, err)
	second, active, err := secondSession.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test", AllowedChannelIDs: []int{101, 102},
		CapacityExcludedChannelIDs: []int{first.Result.SelectedChannelID}, SlowStartFactor: factor,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Same(t, session, secondSession)
	assert.Equal(t, 2, calls, "each member slow-start factor must be captured once per logical request")
	assert.Equal(t, first.Replay.Settings.NowUnix, second.Replay.Settings.NowUnix)
	assert.Equal(t, first.Replay.Settings.NowUnixMilli, second.Replay.Settings.NowUnixMilli)
	assert.Equal(t, first.Replay.Candidates[1].SlowStartFactor, second.Replay.Candidates[1].SlowStartFactor)
}

func canarySessionSnapshotForTest(revision uint64, generation uint64, activationID int64, poolID int, channelID int) SnapshotView {
	return SnapshotView{
		Revision:           revision,
		RuntimeGeneration:  generation,
		PolicyHash:         strings.Repeat("a", 64),
		ActivationID:       activationID,
		ActivationStage:    model.RoutingDeploymentStageCanary,
		TrafficBasisPoints: 100,
		Pools: []PoolSnapshot{{
			ID: poolID, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
			CanaryPolicy:   model.DefaultRoutingCanaryPolicy(),
			Members: []PoolMemberSnapshot{{
				ID: 11, PoolID: poolID, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
				LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_001},
				Models: []ModelSnapshot{{ModelName: "gpt-test"}},
			}},
		}},
		Channels: []ChannelSnapshot{{ID: channelID, Status: common.ChannelStatusEnabled}},
	}
}
