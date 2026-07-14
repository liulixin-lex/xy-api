package channelrouting

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestProfileV2CanonicalWhitelistAndV1HashCompatibility(t *testing.T) {
	legacy, err := NewRequestProfile(
		"/v1/chat/completions", "default", "gpt-test", true, 1, 1_000, 300,
	)
	require.NoError(t, err)
	legacyJSON, err := common.Marshal(legacy)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"schema_version":1,
		"request_path":"/v1/chat/completions",
		"group_name":"default",
		"model_name":"gpt-test",
		"is_stream":true,
		"retry_index":1,
		"prompt_token_estimate":1000,
		"completion_token_estimate":300
	}`, string(legacyJSON))
	legacyHash, err := legacy.Hash()
	require.NoError(t, err)
	assert.Equal(t, "aa067fb3e21983aef15732e59171147b55d3dc5d7b6c05ab3eee0ac0ae370840", legacyHash)

	profile, err := NewRequestProfileV2(RequestProfileV2Input{
		RequestPath:      "/v1/responses",
		GroupName:        "enterprise",
		ModelName:        "gpt-test",
		RequestKind:      RequestKindResponses,
		SourceFormat:     RequestSourceFormatOpenAIResponses,
		InputModalities:  RequestModalityText | RequestModalityImage | RequestModalityAudio,
		OutputModalities: RequestModalityText,
		RequiredCapabilities: RequestCapabilityTools | RequestCapabilityJSONSchema |
			RequestCapabilityVision | RequestCapabilityAudioInput | RequestCapabilityStateful,
		IsStream:              true,
		RetryIndex:            2,
		InputTokens:           KnownRequestQuantity(1_200),
		OutputTokens:          KnownRequestQuantity(400),
		CachedTokens:          KnownRequestQuantity(0),
		ImageUnits:            KnownRequestQuantity(1),
		AudioMillis:           KnownRequestQuantity(750),
		VideoMillis:           NotApplicableRequestQuantity(),
		DeadlineUnixMs:        time.Now().Add(time.Minute).UnixMilli(),
		Region:                "ap-southeast",
		RetrySafety:           RequestRetrySafetyConditional,
		RetryAllowed:          true,
		HedgeAllowed:          false,
		IdempotencyKeyPresent: true,
		TenantTier:            RequestTenantTierEnterprise,
		CostBudgetNanoUSD:     50_000,
	})
	require.NoError(t, err)
	require.NoError(t, profile.Validate())
	assert.Equal(t, RequestProfileSchemaV2, profile.SchemaVersion)
	assert.Equal(t, 1_200, profile.PromptTokenEstimate)
	assert.Equal(t, 400, profile.CompletionTokenEstimate)

	encoded, err := common.Marshal(profile)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"prompt":`)
	assert.NotContains(t, string(encoded), `"tool_name":`)
	assert.NotContains(t, string(encoded), `"schema_body":`)
	assert.NotContains(t, string(encoded), "https://")
	firstHash, err := profile.Hash()
	require.NoError(t, err)
	secondHash, err := profile.Hash()
	require.NoError(t, err)
	assert.Equal(t, firstHash, secondHash)
}

func TestRequestProfileV2RejectsNonCanonicalOrUnsafeValues(t *testing.T) {
	base := RequestProfileV2Input{
		GroupName:    "default",
		ModelName:    "gpt-test",
		RequestKind:  RequestKindChatCompletions,
		SourceFormat: RequestSourceFormatOpenAI,
		InputTokens:  UnknownRequestQuantity(),
		OutputTokens: UnknownRequestQuantity(),
		CachedTokens: NotApplicableRequestQuantity(),
		ImageUnits:   NotApplicableRequestQuantity(),
		AudioMillis:  NotApplicableRequestQuantity(),
		VideoMillis:  NotApplicableRequestQuantity(),
		RetrySafety:  RequestRetrySafetySafe,
		TenantTier:   RequestTenantTierStandard,
	}

	tests := []struct {
		name   string
		mutate func(*RequestProfileV2Input)
	}{
		{name: "unknown request kind", mutate: func(input *RequestProfileV2Input) { input.RequestKind = "custom" }},
		{name: "unknown source format", mutate: func(input *RequestProfileV2Input) { input.SourceFormat = "custom" }},
		{name: "unknown capability bit", mutate: func(input *RequestProfileV2Input) { input.RequiredCapabilities = RequestCapabilityMask(1 << 63) }},
		{name: "unknown modality bit", mutate: func(input *RequestProfileV2Input) { input.InputModalities = RequestModalityMask(1 << 15) }},
		{name: "negative quantity", mutate: func(input *RequestProfileV2Input) {
			input.InputTokens = RequestQuantity{State: RequestQuantityKnown, Value: -1}
		}},
		{name: "unknown quantity state", mutate: func(input *RequestProfileV2Input) { input.InputTokens = RequestQuantity{State: "estimated", Value: 1} }},
		{name: "hedge without retry", mutate: func(input *RequestProfileV2Input) { input.HedgeAllowed = true }},
		{name: "unsafe retry enabled", mutate: func(input *RequestProfileV2Input) {
			input.RetrySafety = RequestRetrySafetyUnsafe
			input.RetryAllowed = true
		}},
		{name: "stateful hedge", mutate: func(input *RequestProfileV2Input) {
			input.RequiredCapabilities = RequestCapabilityStateful
			input.RetryAllowed = true
			input.HedgeAllowed = true
		}},
		{name: "oversized region", mutate: func(input *RequestProfileV2Input) { input.Region = strings.Repeat("r", 65) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			_, err := NewRequestProfileV2(input)
			assert.ErrorIs(t, err, ErrShadowReplayInvalid)
		})
	}
}

func TestRequiredCapabilitiesFailClosedAcrossShadowAndBalanced(t *testing.T) {
	policy, err := normalizeBalancedPoolPolicy(defaultBalancedPoolPolicy(model.RoutingPolicyProfileBalanced))
	require.NoError(t, err)
	pool := PoolSnapshot{
		ID: 1, GroupName: "default", SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		BalancedPolicy: policy, CapabilityRoutingEnabled: true,
	}
	member := PoolMemberSnapshot{
		ID: 11, PoolID: 1, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 100, LegacyWeight: 100,
	}
	channel := ChannelSnapshot{ID: 101, Status: common.ChannelStatusEnabled}
	shadowSettings := pool.SelectorPolicy.selectorSettings(1_000, 1_000_000, 7, false)
	balancedSettings := pool.BalancedPolicy.settings(time.Unix(1_000, 0), 7, 101, false)

	tests := []struct {
		name       string
		kind       RequestKind
		capability RequestCapabilityMask
	}{
		{name: "tools", kind: RequestKindChatCompletions, capability: RequestCapabilityTools},
		{name: "json schema", kind: RequestKindResponses, capability: RequestCapabilityJSONSchema},
		{name: "vision", kind: RequestKindResponses, capability: RequestCapabilityVision},
		{name: "audio", kind: RequestKindResponses, capability: RequestCapabilityAudioInput},
		{name: "realtime", kind: RequestKindRealtime, capability: RequestCapabilityRealtime},
		{name: "stateful", kind: RequestKindResponses, capability: RequestCapabilityStateful},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := requestProfileV2ForCapabilityTest(t, test.kind, test.capability)
			kindMask, ok := test.kind.Mask()
			require.True(t, ok)

			unknown := ModelSnapshot{ModelName: "gpt-test"}
			shadowUnknown, _, err := shadowCandidateFromSnapshot(pool, member, unknown, channel, profile, shadowSettings)
			require.NoError(t, err)
			assert.Equal(t, ExclusionReasonRequestKindUnknown, shadowUnknown.RequestExclusionReason)
			balancedUnknown, err := balancedCandidateFromSnapshot(pool, member, unknown, channel, profile, balancedSettings)
			require.NoError(t, err)
			assert.Equal(t, ExclusionReasonRequestKindUnknown, balancedUnknown.HardExclusionReason)

			unsupported := ModelSnapshot{
				ModelName: "gpt-test", RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
				CapabilitiesKnown: test.capability,
			}
			shadowUnsupported, _, err := shadowCandidateFromSnapshot(pool, member, unsupported, channel, profile, shadowSettings)
			require.NoError(t, err)
			assert.Equal(t, ExclusionReasonCapabilityUnsupported, shadowUnsupported.RequestExclusionReason)
			balancedUnsupported, err := balancedCandidateFromSnapshot(pool, member, unsupported, channel, profile, balancedSettings)
			require.NoError(t, err)
			assert.Equal(t, ExclusionReasonCapabilityUnsupported, balancedUnsupported.HardExclusionReason)

			supported := unsupported
			supported.CapabilitiesSupported = test.capability
			shadowSupported, _, err := shadowCandidateFromSnapshot(pool, member, supported, channel, profile, shadowSettings)
			require.NoError(t, err)
			assert.Empty(t, shadowSupported.RequestExclusionReason)
			balancedSupported, err := balancedCandidateFromSnapshot(pool, member, supported, channel, profile, balancedSettings)
			require.NoError(t, err)
			assert.Empty(t, balancedSupported.HardExclusionReason)
		})
	}
}

func TestV2ReplayHardCapabilityExclusionSurvivesAffinityAndSoftFallback(t *testing.T) {
	profile := requestProfileV2ForCapabilityTest(t, RequestKindResponses, RequestCapabilityJSONSchema)
	kindMask, ok := RequestKindResponses.Mask()
	require.True(t, ok)
	policy, err := normalizeBalancedPoolPolicy(defaultBalancedPoolPolicy(model.RoutingPolicyProfileBalanced))
	require.NoError(t, err)
	pool := PoolSnapshot{
		ID: 5, GroupName: "default", SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		BalancedPolicy: policy, CapabilityRoutingEnabled: true,
	}
	settings := pool.SelectorPolicy.selectorSettings(1_000, 1_000_000, 99, false)
	unsupportedMember := PoolMemberSnapshot{
		ID: 51, PoolID: 5, ChannelID: 501, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 1_000, LegacyWeight: 1_000,
	}
	supportedMember := PoolMemberSnapshot{
		ID: 52, PoolID: 5, ChannelID: 502, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 1, LegacyWeight: 1,
	}
	unsupportedObservation := ModelSnapshot{
		ModelName: "gpt-test", RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
		CapabilitiesKnown: RequestCapabilityJSONSchema,
	}
	supportedObservation := unsupportedObservation
	supportedObservation.CapabilitiesSupported = RequestCapabilityJSONSchema
	unsupported, _, err := shadowCandidateFromSnapshot(
		pool, unsupportedMember, unsupportedObservation,
		ChannelSnapshot{ID: 501, Status: common.ChannelStatusEnabled}, profile, settings,
	)
	require.NoError(t, err)
	supported, _, err := shadowCandidateFromSnapshot(
		pool, supportedMember, supportedObservation,
		ChannelSnapshot{ID: 502, Status: common.ChannelStatusEnabled}, profile, settings,
	)
	require.NoError(t, err)
	shadowInput, err := BuildShadowReplayInput(
		5, 7, 3, strings.Repeat("a", 64), profile, settings, []ShadowCandidateInput{unsupported, supported},
	)
	require.NoError(t, err)
	profile.InputTokens.Value = 99_999
	assert.Equal(t, int64(0), shadowInput.Profile.InputTokens.Value, "replay input must own its profile quantities")
	require.NoError(t, shadowInput.Validate())
	profile.InputTokens.Value = 0
	assert.Equal(t, shadowReplaySchemaVersionV2, shadowInput.SchemaVersion)
	assert.Equal(t, DecisionAlgorithmShadowV2, shadowInput.AlgorithmVersion)
	shadowResult, err := RunShadowReplay(shadowInput)
	require.NoError(t, err)
	assert.Equal(t, 502, shadowResult.SelectedChannelID)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, decisionExclusionReason(t, shadowResult.Candidates, 501))

	balancedSettings := BalancedReplaySettings{
		Policy: policy, PreparedAtUnix: 1_000, PreparedAtUnixMilli: 1_000_000,
		RequestNowUnixMilli: 1_000_000, RandomSeed: 99, PreferredChannelID: 501,
	}
	balancedInput, err := buildBalancedReplayInput(
		5, 7, 3, strings.Repeat("a", 64), profile, balancedSettings,
		[]BalancedReplayCandidate{
			{PoolMemberID: 51, ChannelID: 501, BusinessTier: 1_000, TargetWeight: 1_000,
				Confidence: 1, Freshness: 1, SlowStartFactor: 1, ExplorationEligible: true,
				HardExclusionReason: ExclusionReasonCapabilityUnsupported,
				Cost:                &ShadowReplayCostInput{Known: true, Cost: 1, UpdatedUnix: 1_000}},
			{PoolMemberID: 52, ChannelID: 502, BusinessTier: 1, TargetWeight: 1,
				Confidence: 1, Freshness: 1, SlowStartFactor: 1, ExplorationEligible: true,
				Cost: &ShadowReplayCostInput{Known: true, Cost: 1, UpdatedUnix: 1_000}},
		}, nil, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, balancedReplaySchemaVersionV2, balancedInput.SchemaVersion)
	assert.Equal(t, DecisionAlgorithmBalancedV2, balancedInput.AlgorithmVersion)
	balancedResult, err := RunBalancedReplay(balancedInput)
	require.NoError(t, err)
	assert.Equal(t, 502, balancedResult.SelectedChannelID)
	assert.False(t, balancedResult.AffinityUsed)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, decisionExclusionReason(t, balancedResult.Candidates, 501))
}

func TestPlanBalancedAppliesV2CapabilityFilterBeforeAffinity(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	view.Pools[0].CapabilityRoutingEnabled = true
	view.Pools[0].BalancedPolicy.ProtectionBandBasisPoints = 10_000
	kindMask, ok := RequestKindResponses.Mask()
	require.True(t, ok)
	for index := range view.Pools[0].Members {
		observation := &view.Pools[0].Members[index].Models[0]
		observation.RequestKindsKnown = kindMask
		observation.RequestKindsSupported = kindMask
		observation.CapabilitiesKnown = RequestCapabilityJSONSchema
	}
	view.Pools[0].Members[1].Models[0].CapabilitiesSupported = RequestCapabilityJSONSchema
	SetSnapshotForTest(view)

	profile := requestProfileV2ForCapabilityTest(t, RequestKindResponses, RequestCapabilityJSONSchema)
	profile.RequestPath = "/v1/responses"
	session, err := NewRequestRoutingSession("balanced-v2-capability", "default")
	require.NoError(t, err)
	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/responses", ModelName: "gpt-test", Profile: &profile,
		},
		PreferredChannelID: 101,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, DecisionAlgorithmBalancedV2, plan.AlgorithmVersion)
	assert.Equal(t, DecisionAlgorithmBalancedV2, plan.Replay.AlgorithmVersion)
	assert.Equal(t, 102, plan.SelectedChannelID)
	assert.False(t, plan.AffinityUsed)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, decisionExclusionReason(t, plan.Candidates, 101))
}

func TestCaptureShadowReplayUsesV2CapabilityFilter(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	kindMask, ok := RequestKindResponsesCompact.Mask()
	require.True(t, ok)
	view := SnapshotView{
		Revision: 7, RuntimeGeneration: 3, PolicyHash: strings.Repeat("b", 64), BuiltAtUnix: time.Now().Unix(),
		Pools: []PoolSnapshot{{
			ID: 5, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:            model.RoutingPolicyProfileBalanced,
			CapabilityRoutingEnabled: true,
			SelectorPolicy:           defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
			Members: []PoolMemberSnapshot{
				{
					ID: 51, PoolID: 5, ChannelID: 501, PhysicalStatus: common.ChannelStatusEnabled,
					LegacyPriority: 1_000, LegacyWeight: 1_000,
					Models: []ModelSnapshot{{
						ModelName: "gpt-test", RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
						CapabilitiesKnown: RequestCapabilityJSONSchema,
					}},
				},
				{
					ID: 52, PoolID: 5, ChannelID: 502, PhysicalStatus: common.ChannelStatusEnabled,
					LegacyPriority: 1, LegacyWeight: 1,
					Models: []ModelSnapshot{{
						ModelName: "gpt-test", RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
						CapabilitiesKnown:     RequestCapabilityJSONSchema,
						CapabilitiesSupported: RequestCapabilityJSONSchema,
					}},
				},
			},
		}},
		Channels: []ChannelSnapshot{
			{ID: 501, Status: common.ChannelStatusEnabled},
			{ID: 502, Status: common.ChannelStatusEnabled},
		},
	}
	SetSnapshotForTest(view)
	profile := requestProfileV2ForCapabilityTest(t, RequestKindResponsesCompact, RequestCapabilityJSONSchema)
	profile.RequestPath = "/v1/responses/compact"
	input, active, err := CaptureShadowReplayRequest(ShadowRequest{
		RequestID: "shadow-v2-capability", RequestPath: "/v1/responses/compact",
		GroupName: "default", ModelName: "gpt-test", Profile: &profile,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, DecisionAlgorithmShadowV2, input.AlgorithmVersion)
	result, err := RunShadowReplay(input)
	require.NoError(t, err)
	assert.Equal(t, 502, result.SelectedChannelID)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, decisionExclusionReason(t, result.Candidates, 501))

	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err = EnqueueDecision(DecisionInput{
		RequestID: "shadow-v2-capability", PoolID: input.PoolID,
		GroupName: input.Profile.GroupName, ModelName: input.Profile.ModelName,
		SnapshotRevision: input.PolicyRevision, AlgorithmVersion: input.AlgorithmVersion,
		RetryIndex: input.Profile.RetryIndex, IsStream: input.Profile.IsStream,
		ActualChannelID: 502, ObservedChannelID: result.SelectedChannelID,
		FilteredOpen: result.FilteredOpen, FilteredCapacity: result.FilteredCapacity,
		BreakerBypassed: result.BreakerBypassed, Candidates: result.Candidates,
		ReplayInput: &input, DifferenceType: ClassifyShadowDifference(502, result),
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)
	assert.Equal(t, DecisionAlgorithmShadowV2, audits[0].AlgorithmVersion)
	replayed, err := ReplayDecisionAudit(audits[0])
	require.NoError(t, err)
	assert.Equal(t, result, replayed)
}

func TestSnapshotModelPreResolvesUpstreamModelWithoutMutatingCapabilityEvidence(t *testing.T) {
	mapping := `{"gpt-test":"upstream-model"}`
	view, invalid := snapshotModel(
		model.Channel{ModelMapping: &mapping}, 11, nil, "default", "gpt-test", nil, false,
	)
	assert.Zero(t, invalid)
	assert.True(t, view.UpstreamModelKnown)
	assert.Equal(t, "upstream-model", view.UpstreamModelName)
	assert.Zero(t, view.RequestKindsKnown)
	assert.Zero(t, view.RequestKindsSupported)
	assert.Zero(t, view.CapabilitiesKnown)
	assert.Zero(t, view.CapabilitiesSupported)
}

func TestLegacyV1ReplayRemainsValidAfterV2Introduction(t *testing.T) {
	input := shadowReplayInputForTest(t)
	assert.Equal(t, shadowReplaySchemaVersionV1, input.SchemaVersion)
	assert.Equal(t, RequestProfileSchemaV1, input.Profile.SchemaVersion)
	assert.Equal(t, DecisionAlgorithmShadowV1, input.AlgorithmVersion)
	result, err := RunShadowReplay(input)
	require.NoError(t, err)
	assert.Positive(t, result.SelectedChannelID)

	encoded, err := common.Marshal(input)
	require.NoError(t, err)
	var decoded ShadowReplayInput
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	require.NoError(t, decoded.Validate())
	replayed, err := RunShadowReplay(decoded)
	require.NoError(t, err)
	assert.Equal(t, result, replayed)
}

func requestProfileV2ForCapabilityTest(
	t *testing.T,
	kind RequestKind,
	capability RequestCapabilityMask,
) RequestProfile {
	t.Helper()
	sourceFormat := requestSourceFormatForKind(kind)
	inputModalities := RequestModalityText
	outputModalities := RequestModalityText
	if capability&RequestCapabilityVision != 0 {
		inputModalities |= RequestModalityImage
	}
	if capability&RequestCapabilityAudioInput != 0 {
		inputModalities |= RequestModalityAudio
	}
	if capability&RequestCapabilityAudioOutput != 0 {
		outputModalities |= RequestModalityAudio
	}
	if capability&RequestCapabilityImageOutput != 0 {
		outputModalities |= RequestModalityImage
	}
	if capability&RequestCapabilityFileInput != 0 {
		inputModalities |= RequestModalityFile
	}
	if capability&RequestCapabilityVideoInput != 0 {
		inputModalities |= RequestModalityVideo
	}
	if capability&RequestCapabilityVideoOutput != 0 {
		outputModalities |= RequestModalityVideo
	}
	profile, err := NewRequestProfileV2(RequestProfileV2Input{
		GroupName:            "default",
		ModelName:            "gpt-test",
		RequestKind:          kind,
		SourceFormat:         sourceFormat,
		InputModalities:      inputModalities,
		OutputModalities:     outputModalities,
		RequiredCapabilities: capability,
		InputTokens:          UnknownRequestQuantity(),
		OutputTokens:         UnknownRequestQuantity(),
		CachedTokens:         NotApplicableRequestQuantity(),
		ImageUnits:           NotApplicableRequestQuantity(),
		AudioMillis:          NotApplicableRequestQuantity(),
		VideoMillis:          NotApplicableRequestQuantity(),
		RetrySafety:          RequestRetrySafetySafe,
		RetryAllowed:         true,
		TenantTier:           RequestTenantTierStandard,
	})
	require.NoError(t, err)
	return profile
}

func requestSourceFormatForKind(kind RequestKind) RequestSourceFormat {
	switch kind {
	case RequestKindChatCompletions:
		return RequestSourceFormatOpenAI
	case RequestKindResponses:
		return RequestSourceFormatOpenAIResponses
	case RequestKindResponsesCompact:
		return RequestSourceFormatOpenAIResponsesCompaction
	case RequestKindClaudeMessages:
		return RequestSourceFormatClaude
	case RequestKindGeminiGenerate:
		return RequestSourceFormatGemini
	case RequestKindImage:
		return RequestSourceFormatOpenAIImage
	case RequestKindAudio:
		return RequestSourceFormatOpenAIAudio
	case RequestKindEmbedding:
		return RequestSourceFormatEmbedding
	case RequestKindRerank:
		return RequestSourceFormatRerank
	case RequestKindRealtime:
		return RequestSourceFormatOpenAIRealtime
	case RequestKindTask:
		return RequestSourceFormatTask
	case RequestKindMidjourney:
		return RequestSourceFormatMidjourney
	case RequestKindSuno:
		return RequestSourceFormatSuno
	default:
		return ""
	}
}

func decisionExclusionReason(t *testing.T, candidates []DecisionCandidate, channelID int) string {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.ChannelID == channelID {
			return candidate.ExclusionReason
		}
	}
	require.Failf(t, "candidate not found", "channel_id=%d", channelID)
	return ""
}

func TestCapabilityMaskValidationRejectsPartialKnownState(t *testing.T) {
	profile := requestProfileV2ForCapabilityTest(
		t, RequestKindResponses, RequestCapabilityTools|RequestCapabilityJSONSchema,
	)
	kindMask, ok := RequestKindResponses.Mask()
	require.True(t, ok)
	observation := ModelSnapshot{
		RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
		CapabilitiesKnown:     RequestCapabilityTools,
		CapabilitiesSupported: RequestCapabilityTools | RequestCapabilityJSONSchema,
	}
	assert.Equal(t, ExclusionReasonCapabilityUnknown, requestCapabilityExclusionReason(profile, observation))
	assert.False(t, observation.ValidCapabilityState())

	observation.CapabilitiesKnown |= RequestCapabilityJSONSchema
	assert.True(t, observation.ValidCapabilityState())
	assert.Empty(t, requestCapabilityExclusionReason(profile, observation))
}

func TestV2RealtimeAndStatefulProfilesAreNeverHedgeEligible(t *testing.T) {
	for _, capability := range []RequestCapabilityMask{RequestCapabilityRealtime, RequestCapabilityStateful} {
		_, err := NewRequestProfileV2(RequestProfileV2Input{
			GroupName: "default", ModelName: "gpt-test", RequestKind: RequestKindRealtime,
			SourceFormat:         RequestSourceFormatOpenAIRealtime,
			RequiredCapabilities: capability, InputTokens: UnknownRequestQuantity(), OutputTokens: UnknownRequestQuantity(),
			CachedTokens: NotApplicableRequestQuantity(), ImageUnits: NotApplicableRequestQuantity(),
			AudioMillis: NotApplicableRequestQuantity(), VideoMillis: NotApplicableRequestQuantity(),
			RetrySafety: RequestRetrySafetyConditional, RetryAllowed: true, HedgeAllowed: true,
			TenantTier: RequestTenantTierEnterprise,
		})
		assert.ErrorIs(t, err, ErrShadowReplayInvalid)
	}
}
