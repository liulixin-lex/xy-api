package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingPricingContractV2PreservesExplicitFreeAndOmitsAbsentDimensions(t *testing.T) {
	input := 2.5
	freeCacheRead := 0.0
	contract, err := NormalizeRoutingPricingContractV2(RoutingPricingContractV2{
		SchemaVersion:           RoutingPricingContractSchemaVersion,
		Mode:                    RoutingPricingContractModeDimensions,
		Currency:                "usd",
		InputCostPerMillion:     &input,
		CacheReadCostPerMillion: &freeCacheRead,
	})
	require.NoError(t, err)
	encoded, err := common.Marshal(contract)
	require.NoError(t, err)
	jsonText := string(encoded)
	assert.Contains(t, jsonText, `"cache_read_cost_per_million":0`)
	assert.NotContains(t, jsonText, "output_cost_per_million")
	assert.NotContains(t, jsonText, "cache_write_1h_cost_per_million")
}

func TestRoutingPricingContractV2ExpressionIsExclusive(t *testing.T) {
	input := 1.0
	_, err := NormalizeRoutingPricingContractV2(RoutingPricingContractV2{
		SchemaVersion:            RoutingPricingContractSchemaVersion,
		Mode:                     RoutingPricingContractModeExpression,
		Currency:                 "USD",
		InputCostPerMillion:      &input,
		BillingExpression:        `v1:tier("base", p * 2)`,
		BillingExpressionVersion: 1,
	})
	assert.ErrorIs(t, err, ErrRoutingPricingContractInvalid)
}

func TestRoutingPricingContractV2FreeModeIsKnownWithoutAZeroMultiplier(t *testing.T) {
	contract, err := NormalizeRoutingPricingContractV2(RoutingPricingContractV2{
		SchemaVersion: RoutingPricingContractSchemaVersion,
		Mode:          RoutingPricingContractModeFree,
		Currency:      "USD",
	})
	require.NoError(t, err)
	pricing := contract.ToRoutingNormalizedPricing("routing_contract_v2", 1)
	now := common.GetTimestamp()
	estimate, err := EstimateRoutingCostSnapshot(
		freshRoutingCostVersionForTest(now, ""), pricing,
		RoutingCostRequestProfile{MaxAttempts: 1, KnowledgeSpecified: true}, now,
	)
	require.NoError(t, err)
	assert.True(t, estimate.Known)
	assert.True(t, estimate.ExpectedKnown)
	assert.Zero(t, estimate.ExpectedCost)
}

func TestRoutingCostDoesNotDeriveMissingSubtypePrices(t *testing.T) {
	now := common.GetTimestamp()
	input := 2.0
	multiplier := 1.0
	pricing := RoutingNormalizedPricing{
		BillingMode: "routing_contract_v2", Currency: "USD", Unit: "mixed",
		GroupRatio: &multiplier, InputCostPerMillion: &input,
	}
	estimate, err := EstimateRoutingCostSnapshot(freshRoutingCostVersionForTest(now, ""), pricing, RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 500, MaximumCompletionTokens: 500,
		CacheReadTokens: 400, CacheWriteTokens: 300, CacheWriteOneHourTokens: 200,
		ImageInputTokens: 100, ImageOutputTokens: 100, AudioInputTokens: 100, AudioOutputTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
	}, now)
	require.NoError(t, err)
	require.True(t, estimate.ExpectedKnown)
	assert.InDelta(t, 0.002, estimate.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.002, estimate.ExpectedBreakdown.Input, 1e-12)
	assert.Zero(t, estimate.ExpectedBreakdown.Output)
	assert.Zero(t, estimate.ExpectedBreakdown.CacheRead)
	assert.Zero(t, estimate.ExpectedBreakdown.CacheWrite)
	assert.Zero(t, estimate.ExpectedBreakdown.CacheWrite1h)
	assert.Zero(t, estimate.ExpectedBreakdown.ImageInput)
	assert.Zero(t, estimate.ExpectedBreakdown.ImageOutput)
	assert.Zero(t, estimate.ExpectedBreakdown.AudioInput)
	assert.Zero(t, estimate.ExpectedBreakdown.AudioOutput)
}

func TestRoutingCostChargedDimensionWithUnknownQuantityReturnsMissingContext(t *testing.T) {
	now := common.GetTimestamp()
	cacheRead := 0.25
	pricing := RoutingNormalizedPricing{
		BillingMode: "routing_contract_v2", Currency: "USD", Unit: "mixed",
		CacheReadCostPerMillion: &cacheRead,
	}
	estimate, err := EstimateRoutingCostSnapshot(freshRoutingCostVersionForTest(now, ""), pricing, RoutingCostRequestProfile{
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheReadTokensKnown: false,
	}, now)
	require.NoError(t, err)
	assert.False(t, estimate.Known)
	assert.Equal(t, RoutingCostUnknownMissingContext, estimate.UnknownReason)
	assert.Equal(t, []string{"cache_read_tokens"}, estimate.MissingContext)
	assert.False(t, strings.Contains(estimate.UnknownReason, "free"))
}

func TestRoutingCostChargedSubdimensionsRequireIndependentQuantities(t *testing.T) {
	now := common.GetTimestamp()
	rate := 1.0
	tests := []struct {
		name       string
		missingKey string
		configure  func(*RoutingNormalizedPricing)
		markKnown  func(*RoutingCostRequestProfile)
	}{
		{
			name: "cache write", missingKey: "cache_write_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.CacheWriteCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.CacheWriteTokensKnown = true },
		},
		{
			name: "one hour cache write", missingKey: "cache_write_1h_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.CacheWrite1hCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.CacheWriteOneHourTokensKnown = true },
		},
		{
			name: "image input tokens", missingKey: "image_input_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.ImageInputCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.ImageInputTokensKnown = true },
		},
		{
			name: "image output tokens", missingKey: "image_output_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.ImageOutputCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.ImageOutputTokensKnown = true },
		},
		{
			name: "image units", missingKey: "image_units",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.PerImageCost = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.ImageUnitsKnown = true },
		},
		{
			name: "audio input tokens", missingKey: "audio_input_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.AudioInputCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.AudioInputTokensKnown = true },
		},
		{
			name: "audio output tokens", missingKey: "audio_output_tokens",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.AudioOutputCostPerMillion = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.AudioOutputTokensKnown = true },
		},
		{
			name: "audio seconds", missingKey: "audio_seconds",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.AudioCostPerSecond = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.AudioDurationKnown = true },
		},
		{
			name: "video seconds", missingKey: "video_seconds",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.VideoCostPerSecond = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.VideoDurationKnown = true },
		},
		{
			name: "task units", missingKey: "task_units",
			configure: func(pricing *RoutingNormalizedPricing) { pricing.PerTaskCost = &rate },
			markKnown: func(profile *RoutingCostRequestProfile) { profile.TaskUnitsKnown = true },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pricing := RoutingNormalizedPricing{
				BillingMode: "routing_contract_v2", Currency: "USD", Unit: "mixed",
			}
			test.configure(&pricing)
			profile := RoutingCostRequestProfile{MaxAttempts: 1, KnowledgeSpecified: true}

			unknown, err := EstimateRoutingCostSnapshot(
				freshRoutingCostVersionForTest(now, ""), pricing, profile, now,
			)
			require.NoError(t, err)
			assert.False(t, unknown.ExpectedKnown)
			assert.Equal(t, RoutingCostUnknownMissingContext, unknown.UnknownReason)
			assert.Equal(t, []string{test.missingKey}, unknown.MissingContext)

			test.markKnown(&profile)
			knownZero, err := EstimateRoutingCostSnapshot(
				freshRoutingCostVersionForTest(now, ""), pricing, profile, now,
			)
			require.NoError(t, err)
			assert.True(t, knownZero.ExpectedKnown)
			assert.Zero(t, knownZero.ExpectedCost)
		})
	}
}

func TestRoutingCostExpressionTracksCacheAndMediaSubdimensionsIndependently(t *testing.T) {
	now := common.GetTimestamp()
	pricing := RoutingNormalizedPricing{
		BillingMode: "tiered_expr", Currency: "USD", Unit: "expression",
		BillingExpression: "p + cc + cc1h * 2 + img * 3 + img_o * 4 + ai * 5 + ao * 6",
	}
	profile := RoutingCostRequestProfile{
		PromptTokens: 1, MaximumPromptTokens: 1, MaxAttempts: 1,
		KnowledgeSpecified: true, InputTokensKnown: true,
		CacheWriteTokensKnown: true, ImageInputTokensKnown: true, AudioInputTokensKnown: true,
	}

	estimate, err := EstimateRoutingCostSnapshot(
		freshRoutingCostVersionForTest(now, ""), pricing, profile, now,
	)
	require.NoError(t, err)
	assert.False(t, estimate.ExpectedKnown)
	assert.Equal(t, []string{
		"cache_write_1h_tokens", "image_output_tokens", "audio_output_tokens",
	}, estimate.MissingContext)
}

func TestRoutingCostBusinessUnitContractUsesExplicitRequestFacts(t *testing.T) {
	audioRate := 0.01
	videoRate := 0.02
	taskRate := 0.3
	contract, err := NormalizeRoutingPricingContractV2(RoutingPricingContractV2{
		SchemaVersion:      RoutingPricingContractSchemaVersion,
		Mode:               RoutingPricingContractModeDimensions,
		Currency:           "USD",
		AudioCostPerSecond: &audioRate,
		VideoCostPerSecond: &videoRate,
		PerTaskCost:        &taskRate,
	})
	require.NoError(t, err)
	pricing := contract.ToRoutingNormalizedPricing("routing_contract_v2", 2)
	now := common.GetTimestamp()
	estimate, err := EstimateRoutingCostSnapshot(
		freshRoutingCostVersionForTest(now, ""), pricing,
		RoutingCostRequestProfile{
			AudioSeconds: 10, VideoSeconds: 5, TaskUnits: 2, MaxAttempts: 1,
			KnowledgeSpecified: true, AudioDurationKnown: true, VideoDurationKnown: true, TaskUnitsKnown: true,
		}, now,
	)
	require.NoError(t, err)
	require.True(t, estimate.ExpectedKnown)
	assert.InDelta(t, 0.2, estimate.ExpectedBreakdown.AudioSeconds, 1e-12)
	assert.InDelta(t, 0.2, estimate.ExpectedBreakdown.VideoSeconds, 1e-12)
	assert.InDelta(t, 1.2, estimate.ExpectedBreakdown.TaskUnits, 1e-12)
	assert.InDelta(t, 1.6, estimate.ExpectedCost, 1e-12)

	missingVideo, err := EstimateRoutingCostSnapshot(
		freshRoutingCostVersionForTest(now, ""), pricing,
		RoutingCostRequestProfile{
			AudioSeconds: 10, TaskUnits: 2, MaxAttempts: 1, KnowledgeSpecified: true,
			AudioDurationKnown: true, VideoDurationKnown: false, TaskUnitsKnown: true,
		}, now,
	)
	require.NoError(t, err)
	assert.False(t, missingVideo.ExpectedKnown)
	assert.Equal(t, RoutingCostUnknownMissingContext, missingVideo.UnknownReason)
	assert.Equal(t, []string{"video_seconds"}, missingVideo.MissingContext)
}
