package channelrouting

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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

func TestShadowSeedAndProfileHashAreStableAndScoped(t *testing.T) {
	profile, err := NewRequestProfile("/v1/responses", "vip", "gpt-test", false, 0, 100, 20)
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
		AlgorithmVersion:     DecisionAlgorithmShadowV1,
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

func shadowReplayInputForTest(t *testing.T) ShadowReplayInput {
	t.Helper()
	profile, err := NewRequestProfile("/v1/chat/completions", "default", "gpt-test", true, 1, 1_000, 300)
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
			Cost:   &ShadowCostInput{Known: true, Cost: 2, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 12, ChannelID: 102, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 98, ReliabilityRequestCount: 100, ReliabilityFailureCount: 2, P95LatencyMs: 250, P95TTFTMs: 80, OutputTokensPerSecond: 60},
			Cost:   &ShadowCostInput{Known: true, Cost: 1, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 13, ChannelID: 103, Priority: 5, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 100, ReliabilityRequestCount: 100, P95LatencyMs: 100, P95TTFTMs: 50, OutputTokensPerSecond: 80},
			Cost:   &ShadowCostInput{Known: true, Cost: 0.5, UpdatedUnix: 990},
		},
	})
	require.NoError(t, err)
	return input
}
