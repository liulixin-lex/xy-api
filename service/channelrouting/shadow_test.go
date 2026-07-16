package channelrouting

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShadowReplayIsDeterministicAndHashBound(t *testing.T) {
	input := shadowReplayInputForTest(t)
	assert.Len(t, input.SnapshotHash, 64)

	first, err := RunShadowReplay(input)
	require.NoError(t, err)
	second, err := RunShadowReplay(input)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotZero(t, first.SelectedChannelID)
	assert.Len(t, first.Ranked, 3)
	assert.Len(t, first.Candidates, 3)

	tampered := input
	tampered.Candidates = append([]ShadowCandidateInput(nil), input.Candidates...)
	cost := *tampered.Candidates[0].Cost
	cost.Cost = 0.01
	tampered.Candidates[0].Cost = &cost
	_, err = RunShadowReplay(tampered)
	assert.ErrorIs(t, err, ErrShadowReplayHash)
}

func TestRequestProfileNeverSerializesCostRequestBodyOrHeaders(t *testing.T) {
	base, err := NewLegacyRequestProfile("/v1/chat/completions", "default", "gpt-test", false, 0, 100, 20)
	require.NoError(t, err)
	first := attachRoutingCostProfile(base, &model.RoutingCostRequestProfile{
		Request: billingexpr.RequestInput{
			Headers: map[string]string{"authorization": "secret-token"},
			Body:    []byte(`{"private":"first"}`),
		},
	}, time.Now().Unix())
	second := attachRoutingCostProfile(base, &model.RoutingCostRequestProfile{
		Request: billingexpr.RequestInput{
			Headers: map[string]string{"authorization": "different-token"},
			Body:    []byte(`{"private":"second"}`),
		},
	}, time.Now().Unix()+1)

	firstHash, err := first.Hash()
	require.NoError(t, err)
	secondHash, err := second.Hash()
	require.NoError(t, err)
	assert.Equal(t, firstHash, secondHash)
	encoded, err := common.Marshal(first)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "secret-token")
	assert.NotContains(t, string(encoded), "private")
}

func TestShadowSeedAndProfileHashAreStableAndScoped(t *testing.T) {
	profile, err := NewLegacyRequestProfile("/v1/responses", "vip", "gpt-test", false, 0, 100, 20)
	require.NoError(t, err)
	firstHash, err := profile.Hash()
	require.NoError(t, err)
	secondHash, err := profile.Hash()
	require.NoError(t, err)
	assert.Equal(t, firstHash, secondHash)

	seed, err := DeriveShadowSeed("request-1", 9, 0)
	require.NoError(t, err)
	same, err := DeriveShadowSeed("request-1", 9, 0)
	require.NoError(t, err)
	retry, err := DeriveShadowSeed("request-1", 9, 1)
	require.NoError(t, err)
	revision, err := DeriveShadowSeed("request-1", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, seed, same)
	assert.NotEqual(t, seed, retry)
	assert.NotEqual(t, seed, revision)
	decisionSeed, err := DeriveDecisionSeed("request-1", 9, 0)
	require.NoError(t, err)
	assert.Equal(t, seed, decisionSeed, "the generalized seed must preserve existing shadow replay")
}

func TestCanaryReplayAppliesSlowStartAndRequestExclusions(t *testing.T) {
	profile, err := NewLegacyRequestProfile("/v1/chat/completions", "default", "gpt-test", false, 0, 0, 0)
	require.NoError(t, err)
	seed, err := DeriveDecisionSeed("canary-replay", 7, 0)
	require.NoError(t, err)
	input, err := BuildCanaryReplayInput(5, 7, 3, strings.Repeat("c", 64), profile, routingselector.Settings{
		WeightAvailability: 1,
		AvailabilityFloor:  0,
		MinVolume:          0,
		TopK:               1,
		MaxEjectedPct:      50,
		HalfOpenProbes:     1,
		SnapshotStaleSec:   1_800,
		NowUnix:            1_000,
		NowUnixMilli:       1_000_000,
		RandomSeed:         seed,
	}, []ShadowCandidateInput{
		{PoolMemberID: 11, ChannelID: 101, Priority: 10, Weight: 10, SlowStartFactor: 0.25},
		{PoolMemberID: 12, ChannelID: 102, Priority: 100, Weight: 100, RequestExclusionReason: ExclusionReasonRequestFailed},
		{PoolMemberID: 13, ChannelID: 103, Priority: 10, Weight: 10, SlowStartFactor: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAlgorithmCanary, input.AlgorithmVersion)

	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	require.Len(t, replay.Ranked, 2)
	assert.Equal(t, 103, replay.SelectedChannelID)
	rankedByChannel := make(map[int]DecisionCandidate, len(replay.Ranked))
	for _, candidate := range replay.Ranked {
		rankedByChannel[candidate.ChannelID] = candidate
	}
	assert.InDelta(t, rankedByChannel[103].Score*0.25, rankedByChannel[101].Score, 0.000001)
	for _, candidate := range replay.Candidates {
		if candidate.ChannelID == 102 {
			assert.False(t, candidate.Eligible)
			assert.Equal(t, ExclusionReasonRequestFailed, candidate.ExclusionReason)
		}
	}
}

func TestShadowReplayJSONOmitsCanaryFieldsForLegacyHashCompatibility(t *testing.T) {
	input := shadowReplayInputForTest(t)
	encoded, err := common.Marshal(input)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "slow_start_factor")
	assert.NotContains(t, string(encoded), "request_exclusion_reason")

	var decoded ShadowReplayInput
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	require.NoError(t, decoded.Validate())
	assert.Equal(t, input.SnapshotHash, decoded.SnapshotHash)
}

func TestClassifyShadowDifference(t *testing.T) {
	replay := ShadowReplayResult{
		SelectedChannelID: 2,
		Ranked: []DecisionCandidate{
			{ChannelID: 2, Eligible: true},
			{ChannelID: 1, Eligible: true},
		},
		Candidates: []DecisionCandidate{{ChannelID: 2, Eligible: true}, {ChannelID: 1, Eligible: true}, {ChannelID: 4}},
	}
	assert.Equal(t, "match", ClassifyShadowDifference(2, replay))
	assert.Equal(t, "ranking_difference", ClassifyShadowDifference(1, replay))
	assert.Equal(t, "eligibility_difference", ClassifyShadowDifference(4, replay))
	assert.Equal(t, "legacy_outside_shadow_candidates", ClassifyShadowDifference(3, replay))
	assert.Equal(t, "legacy_unavailable", ClassifyShadowDifference(0, replay))
	assert.Equal(t, "shadow_unavailable", ClassifyShadowDifference(1, ShadowReplayResult{}))
	assert.Equal(t, "both_unavailable", ClassifyShadowDifference(0, ShadowReplayResult{}))
}

func TestCaptureShadowReplayUsesOneImmutablePoolSnapshot(t *testing.T) {
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	now := time.Now().Unix()
	policy, err := resolvePoolSelectorPolicy(model.RoutingPolicyProfileCustom, []byte(`{
		"weight_availability": 1,
		"weight_latency": 0,
		"weight_throughput": 0,
		"weight_cost": 0,
		"min_volume": 1,
		"top_k": 1
	}`))
	require.NoError(t, err)
	groupRatio := 2.0
	perRequestCost := 0.25
	versionedPricing := &model.RoutingNormalizedPricing{
		QuotaType: 1, BillingMode: "per_request", Currency: "USD", Unit: "request",
		GroupRatio: &groupRatio, PerRequestCost: &perRequestCost,
	}
	view := SnapshotView{
		Revision: 17, RuntimeGeneration: 9, PolicyHash: strings.Repeat("b", 64),
		Pools: []PoolSnapshot{
			{
				ID: 5, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageShadow,
				PolicyProfile: model.RoutingPolicyProfileCustom, SelectorPolicy: policy,
				Members: []PoolMemberSnapshot{
					{
						ID: 51, PoolID: 5, ChannelID: 501, PhysicalStatus: common.ChannelStatusEnabled,
						LegacyPriority: 10, LegacyWeight: 10,
						Models: []ModelSnapshot{{
							ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 95,
							ReliabilityRequestCount: 100, ReliabilityFailureCount: 5, P95LatencyKnown: true,
							P95LatencyMs: 300, OutputTokensPerSecond: 50, Inflight: 2,
							CostKnown: true, CostUpdatedUnix: now, CostBillingMode: "per_request",
							CostGroupRatio: 2, CostModelPrice: 0.25,
							CostPricing: versionedPricing, CostPricingHash: strings.Repeat("c", 64),
							CostPricingVersion: "capture-v1", CostObservedTime: now, CostEffectiveTime: now - 60,
							CostExpiresTime: now + 3_600, CostVersionConfidence: model.RoutingCostConfidenceExact,
							CostConfidenceScore: 1, CostFreshness: model.RoutingCostFreshnessFresh,
							CostFreshnessScore: 1,
						}},
					},
					{
						ID: 52, PoolID: 5, ChannelID: 502, PhysicalStatus: common.ChannelStatusEnabled,
						LegacyPriority: 10, LegacyWeight: 10,
						Models: []ModelSnapshot{{
							ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 99,
							ReliabilityRequestCount: 100, ReliabilityFailureCount: 1, P95LatencyKnown: true,
							P95LatencyMs: 100, OutputTokensPerSecond: 80,
						}},
					},
				},
			},
			{
				ID: 6, GroupName: "other", DeploymentStage: model.RoutingDeploymentStageShadow,
				PolicyProfile: model.RoutingPolicyProfileBalanced, SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
				Members: []PoolMemberSnapshot{{
					ID: 61, PoolID: 6, ChannelID: 601, PhysicalStatus: common.ChannelStatusEnabled,
					Models: []ModelSnapshot{{ModelName: "gpt-test"}},
				}},
			},
		},
		Channels: []ChannelSnapshot{
			{ID: 501, Status: common.ChannelStatusEnabled},
			{ID: 502, Status: common.ChannelStatusEnabled},
			{ID: 601, Status: common.ChannelStatusEnabled},
		},
	}
	SetSnapshotForTest(view)
	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 501, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}, routinghotcache.MetricSnapshot{ReliabilityRequestCount: 100, ReliabilityFailureCount: 100})

	input, active, err := CaptureShadowReplayRequest(ShadowRequest{
		RequestID: "capture-request", RequestPath: "/v1/chat/completions", GroupName: "default",
		ModelName: "gpt-test", IsStream: true, RetryIndex: 2, PromptTokenEstimate: 1_000, CompletionTokenEstimate: 200,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 5, input.PoolID)
	assert.Equal(t, uint64(17), input.PolicyRevision)
	assert.Equal(t, uint64(9), input.RuntimeGeneration)
	assert.Equal(t, strings.Repeat("b", 64), input.PolicyHash)
	assert.Equal(t, 1.0, input.Settings.WeightAvailability)
	assert.True(t, input.Settings.PreferTTFT)
	assert.Equal(t, input.Settings.NowUnix, input.Settings.NowUnixMilli/1_000)
	expectedSeed, err := DeriveShadowSeed("capture-request", 17, 2)
	require.NoError(t, err)
	assert.Equal(t, expectedSeed, input.Settings.RandomSeed)
	require.Len(t, input.Candidates, 2)
	assert.Equal(t, []int{51, 52}, []int{input.Candidates[0].PoolMemberID, input.Candidates[1].PoolMemberID})
	require.NotNil(t, input.Candidates[0].Metric)
	assert.Equal(t, int64(5), input.Candidates[0].Metric.ReliabilityFailureCount)
	require.NotNil(t, input.Candidates[0].Cost)
	assert.True(t, input.Candidates[0].Cost.Known)
	assert.Equal(t, 0.5, input.Candidates[0].Cost.Cost)
	fullEstimate, costKnown := ShadowCostEstimateForChannel(input, 501)
	require.True(t, costKnown)
	require.NotNil(t, fullEstimate)
	assert.Equal(t, strings.Repeat("c", 64), fullEstimate.PricingHash)
	encodedReplay, err := common.Marshal(input)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedReplay), fullEstimate.PricingHash)
	assert.NotContains(t, string(encodedReplay), "expected_breakdown")
	var persistedReplay ShadowReplayInput
	require.NoError(t, common.Unmarshal(encodedReplay, &persistedReplay))
	require.NoError(t, persistedReplay.Validate())
	liteEstimate, costKnown := ShadowCostEstimateForChannel(persistedReplay, 501)
	require.True(t, costKnown)
	require.NotNil(t, liteEstimate)
	assert.Empty(t, liteEstimate.PricingHash)
	assert.Equal(t, fullEstimate.Cost, liteEstimate.Cost)

	view.Pools[0].DeploymentStage = model.RoutingDeploymentStageObserve
	SetSnapshotForTest(view)
	t.Setenv("SMART_ROUTING_MODE", "shadow")
	_, active, err = CaptureShadowReplayRequest(ShadowRequest{
		RequestID: "capture-request", GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)
	assert.False(t, active)
}

func TestCaptureShadowReplayRequestKeepsCandidatesBeyondAuditLimit(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	members := make([]PoolMemberSnapshot, MaxDecisionCandidates+1)
	channels := make([]ChannelSnapshot, MaxDecisionCandidates+1)
	for index := range members {
		channelID := index + 1
		members[index] = PoolMemberSnapshot{
			ID: index + 1, PoolID: 1, ChannelID: channelID,
			PhysicalStatus: common.ChannelStatusEnabled, LegacyWeight: 1,
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		}
		channels[index] = ChannelSnapshot{ID: channelID, Status: common.ChannelStatusEnabled}
	}
	SetSnapshotForTest(SnapshotView{
		Revision: 10, RuntimeGeneration: 2, PolicyHash: strings.Repeat("d", 64),
		Pools: []PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageShadow,
			SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced), Members: members,
		}},
		Channels: channels,
	})

	input, active, err := CaptureShadowReplayRequest(ShadowRequest{
		RequestID: "large-capture", GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)
	require.True(t, active)
	require.Len(t, input.Candidates, MaxDecisionCandidates+1)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	assert.Len(t, replay.Candidates, MaxDecisionCandidates+1)
}

func TestShadowDecisionAuditReplaysExactlyAndRejectsTampering(t *testing.T) {
	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	input := shadowReplayInputForTest(t)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	actualChannelID := 101
	actualCost, actualCostKnown := ShadowExpectedCostForChannel(input, actualChannelID)
	_, err = EnqueueDecision(DecisionInput{
		RequestID:            "request-123",
		PoolID:               input.PoolID,
		GroupName:            input.Profile.GroupName,
		ModelName:            input.Profile.ModelName,
		SnapshotRevision:     input.PolicyRevision,
		AlgorithmVersion:     input.AlgorithmVersion,
		RetryIndex:           input.Profile.RetryIndex,
		IsStream:             input.Profile.IsStream,
		ActualChannelID:      actualChannelID,
		ObservedChannelID:    replay.SelectedChannelID,
		FilteredOpen:         replay.FilteredOpen,
		FilteredCapacity:     replay.FilteredCapacity,
		BreakerBypassed:      replay.BreakerBypassed,
		Candidates:           replay.Candidates,
		ReplayInput:          &input,
		DifferenceType:       ClassifyShadowDifference(actualChannelID, replay),
		ActualCostKnown:      actualCostKnown,
		ActualExpectedCost:   actualCost,
		ObservedCostKnown:    replay.SelectedCostKnown,
		ObservedExpectedCost: replay.SelectedCost,
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)

	first, err := ReplayDecisionAudit(audits[0])
	require.NoError(t, err)
	second, err := ReplayDecisionAudit(audits[0])
	require.NoError(t, err)
	assert.Equal(t, replay, first)
	assert.Equal(t, first, second)

	tamperedIdentity := audits[0]
	tamperedIdentity.RequestID = "different-request"
	_, err = ReplayDecisionAudit(tamperedIdentity)
	assert.ErrorIs(t, err, ErrShadowReplayAudit)

	tamperedReplay := audits[0]
	var decoded ShadowReplayInput
	require.NoError(t, common.UnmarshalJsonStr(tamperedReplay.ReplayInputJSON, &decoded))
	decoded.Candidates[0].Cost.Cost = 0.01
	encoded, err := common.Marshal(decoded)
	require.NoError(t, err)
	tamperedReplay.ReplayInputJSON = string(encoded)
	_, err = ReplayDecisionAudit(tamperedReplay)
	assert.ErrorIs(t, err, ErrShadowReplayHash)
}

func TestCanaryDecisionAuditReplaysExactly(t *testing.T) {
	ResetSnapshotForTest()
	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		ResetDecisionAuditsForTest()
	})
	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))
	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	plan, active, err := session.Plan(RequestRoutingPlanInput{
		RequestPath: "/v1/chat/completions",
		ModelName:   "gpt-test",
		RetryIndex:  1,
	})
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, plan.Gate.InCanary)
	tracker, err := NewCapacityTracker(CapacityConfig{MaxEntries: 4, IdleTTL: time.Hour, Shards: 1})
	require.NoError(t, err)
	reservation, err := tracker.TryReserve(
		CapacityKey{
			PolicyRevision: plan.Replay.PolicyRevision, PoolID: plan.SelectedIdentity.PoolID,
			MemberID: plan.SelectedIdentity.MemberID, Model: "gpt-test",
		},
		Demand{RPM: 1, InputTPM: 10, OutputTPM: 5, Inflight: 1},
		Limit{RPM: 10, InputTPM: 100, OutputTPM: 50, Inflight: 2},
	)
	require.NoError(t, err)
	admission := reservation.Admission()

	_, err = EnqueueDecision(DecisionInput{
		RequestID:         "cohort-0005",
		PoolID:            plan.Replay.PoolID,
		GroupName:         plan.Replay.Profile.GroupName,
		ModelName:         plan.Replay.Profile.ModelName,
		SnapshotRevision:  plan.Replay.PolicyRevision,
		AlgorithmVersion:  DecisionAlgorithmCanary,
		RetryIndex:        plan.Replay.Profile.RetryIndex,
		ActualChannelID:   plan.Result.SelectedChannelID,
		ObservedChannelID: plan.Result.SelectedChannelID,
		FilteredOpen:      plan.Result.FilteredOpen,
		FilteredCapacity:  plan.Result.FilteredCapacity,
		BreakerBypassed:   plan.Result.BreakerBypassed,
		Candidates:        plan.Result.Candidates,
		ReplayInput:       &plan.Replay,
		DifferenceType:    ClassifyShadowDifference(plan.Result.SelectedChannelID, plan.Result),
		Gate:              &plan.Gate,
		SelectedIdentity:  plan.SelectedIdentity,
		CapacityAdmission: &admission,
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)
	assert.Equal(t, DecisionAlgorithmCanary, audits[0].AlgorithmVersion)
	assert.Equal(t, plan.Gate.ActivationID, audits[0].ActivationID)
	assert.Equal(t, string(plan.Gate.RolloutKey), audits[0].RolloutKey)
	assert.Equal(t, model.RoutingDecisionCohortCanary, audits[0].Cohort)
	assert.Equal(t, plan.SelectedIdentity.MemberID, audits[0].SelectedMemberID)
	assert.Equal(t, string(CapacityModeLocalSoft), audits[0].ReservationMode)

	replayed, err := ReplayDecisionAudit(audits[0])
	require.NoError(t, err)
	assert.Equal(t, plan.Result, replayed)

	tamperedGate := audits[0]
	tamperedGate.CanaryBucket++
	_, err = ReplayDecisionAudit(tamperedGate)
	assert.ErrorIs(t, err, ErrShadowReplayAudit)
	tamperedMember := audits[0]
	tamperedMember.SelectedMemberID++
	_, err = ReplayDecisionAudit(tamperedMember)
	assert.ErrorIs(t, err, ErrShadowReplayAudit)
	tamperedCredential := audits[0]
	tamperedCredential.SelectedCredentialID++
	_, err = ReplayDecisionAudit(tamperedCredential)
	assert.ErrorIs(t, err, ErrShadowReplayAudit)
	tamperedAdmission := audits[0]
	tamperedAdmission.ReservationLimitInflight = 0
	_, err = ReplayDecisionAudit(tamperedAdmission)
	assert.ErrorIs(t, err, ErrShadowReplayAudit)
}

func shadowReplayInputForTest(t *testing.T) ShadowReplayInput {
	t.Helper()
	profile, err := NewLegacyRequestProfile("/v1/chat/completions", "default", "gpt-test", true, 1, 1_000, 300)
	require.NoError(t, err)
	seed, err := DeriveShadowSeed("request-123", 7, profile.RetryIndex)
	require.NoError(t, err)
	input, err := BuildShadowReplayInput(5, 7, 3, strings.Repeat("a", 64), profile, routingselector.Settings{
		WeightAvailability: 0.45,
		WeightLatency:      0.25,
		WeightThroughput:   0.10,
		WeightCost:         0.20,
		AvailabilityFloor:  0.90,
		MinVolume:          10,
		TopK:               2,
		MaxEjectedPct:      50,
		HalfOpenProbes:     1,
		SnapshotStaleSec:   1_800,
		NowUnix:            1_000,
		NowUnixMilli:       1_000_000,
		RandomSeed:         seed,
		PreferTTFT:         true,
	}, []ShadowCandidateInput{
		{
			PoolMemberID: 11, ChannelID: 101, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100, ReliabilityFailureCount: 1, P95LatencyMs: 300, P95TTFTMs: 100, OutputTokensPerSecond: 50},
			Cost:   &ShadowReplayCostInput{Known: true, Cost: 2, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 12, ChannelID: 102, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 98, ReliabilityRequestCount: 100, ReliabilityFailureCount: 2, P95LatencyMs: 250, P95TTFTMs: 80, OutputTokensPerSecond: 60},
			Cost:   &ShadowReplayCostInput{Known: true, Cost: 1, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 13, ChannelID: 103, Priority: 5, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 100, ReliabilityRequestCount: 100, P95LatencyMs: 100, P95TTFTMs: 50, OutputTokensPerSecond: 80},
			Cost:   &ShadowReplayCostInput{Known: true, Cost: 0.5, UpdatedUnix: 990},
		},
	})
	require.NoError(t, err)
	return input
}
