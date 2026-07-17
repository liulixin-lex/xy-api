package channelrouting

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSystemRoutingPricingZeroMultiplierIsKnownWithoutBaseline(t *testing.T) {
	assert.Equal(t, "system_pricing_x_channel_multiplier", SystemRoutingPricingBasis)

	resolution, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "client-model", EffectiveModel: "unpriced-upstream-model",
		UpstreamCostMultiplier: 0, ChannelConfigurationRevision: 7,
	})

	require.NoError(t, err)
	require.True(t, resolution.Known)
	require.NotNil(t, resolution.Pricing.GroupRatio)
	assert.Zero(t, *resolution.Pricing.GroupRatio)
	assert.Equal(t, SystemRoutingPricingBasis, resolution.Pricing.BillingMode)
	assert.True(t, strings.HasSuffix(resolution.PricingIdentity, ":channel-config:7"))

	now := time.Now().Unix()
	estimate, err := model.EstimateRoutingCostSnapshot(model.RoutingCostSnapshotVersion{
		SourceType: "system_pricing", ObservedTime: now, EffectiveTime: now,
		ExpiresTime: now + 86_400, Confidence: model.RoutingCostConfidenceExact,
		ConfidenceScore: 1, Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
	}, resolution.Pricing, model.RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 500, MaximumCompletionTokens: 500,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true, RequestInputKnown: true,
	}, now)
	require.NoError(t, err)
	assert.True(t, estimate.Known)
	assert.True(t, estimate.WorstCaseKnown)
	assert.Zero(t, estimate.ExpectedCost)
	assert.Zero(t, estimate.WorstCaseCost)
}

func TestResolveSystemRoutingPricingAppliesMultiplierWithoutUserGroupRatio(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"routing-cost-test":2}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"routing-cost-test":4}`))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"routing-cost-test":0.25}`))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"routing-cost-test":1.5}`))
	require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"routing-cost-test":3}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"routing-cost-test":5}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"routing-cost-test":6}`))

	half, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "logical", EffectiveModel: "routing-cost-test",
		ModelMappingIdentity: "logical=routing-cost-test", UpstreamCostMultiplier: 0.5,
		ChannelConfigurationRevision: 3,
	})
	require.NoError(t, err)
	double, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "logical", EffectiveModel: "routing-cost-test",
		ModelMappingIdentity: "logical=routing-cost-test", UpstreamCostMultiplier: 2,
		ChannelConfigurationRevision: 3,
	})
	require.NoError(t, err)
	require.True(t, half.Known)
	require.True(t, double.Known)
	require.NotNil(t, half.Pricing.InputCostPerMillion)
	require.NotNil(t, half.Pricing.CacheWriteCostPerMillion)
	assert.Nil(t, half.Pricing.CacheWrite1hCostPerMillion)
	assert.Nil(t, half.Pricing.ImageOutputCostPerMillion)
	assert.InDelta(t, 4, *half.Pricing.InputCostPerMillion, 1e-12)
	assert.InDelta(t, 16, *half.Pricing.OutputCostPerMillion, 1e-12)
	assert.InDelta(t, 1, *half.Pricing.CacheReadCostPerMillion, 1e-12)
	assert.InDelta(t, 6, *half.Pricing.CacheWriteCostPerMillion, 1e-12)
	assert.InDelta(t, 12, *half.Pricing.ImageInputCostPerMillion, 1e-12)
	assert.InDelta(t, 20, *half.Pricing.AudioInputCostPerMillion, 1e-12)
	assert.InDelta(t, 120, *half.Pricing.AudioOutputCostPerMillion, 1e-12)

	profile := model.RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		CacheWriteTokens: 400, CacheWriteOneHourTokens: 500,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true, RequestInputKnown: true,
	}
	now := time.Now().Unix()
	version := model.RoutingCostSnapshotVersion{
		SourceType: "system_pricing", ObservedTime: now, EffectiveTime: now,
		ExpiresTime: now + 86_400, Confidence: model.RoutingCostConfidenceExact,
		ConfidenceScore: 1, Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
	}
	halfEstimate, err := model.EstimateRoutingCostSnapshot(version, half.Pricing, profile, now)
	require.NoError(t, err)
	doubleEstimate, err := model.EstimateRoutingCostSnapshot(version, double.Pricing, profile, now)
	require.NoError(t, err)
	assert.InDelta(t, 0.0012, halfEstimate.ExpectedBreakdown.CacheWrite, 1e-12)
	assert.Zero(t, halfEstimate.ExpectedBreakdown.CacheWrite1h)
	assert.InDelta(t, 0.0048, doubleEstimate.ExpectedBreakdown.CacheWrite, 1e-12)
	assert.Zero(t, doubleEstimate.ExpectedBreakdown.CacheWrite1h)
	assert.InDelta(t, halfEstimate.ExpectedCost*4, doubleEstimate.ExpectedCost, 1e-12)
	assert.NotEqual(t, half.PricingHash, double.PricingHash)
}

func TestResolveSystemRoutingPricingOnlyRequiresKnownCacheReadsWhenTheirPriceDiffers(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"routing-cache-default":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"routing-cache-default":2}`))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{}`))

	resolve := func() SystemRoutingPricingResolution {
		t.Helper()
		resolution, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
			LogicalModel: "routing-cache-default", EffectiveModel: "routing-cache-default",
			UpstreamCostMultiplier: 1, ChannelConfigurationRevision: 1,
		})
		require.NoError(t, err)
		require.True(t, resolution.Known)
		return resolution
	}
	estimate := func(pricing model.RoutingNormalizedPricing) model.RoutingCostEstimate {
		t.Helper()
		now := time.Now().Unix()
		result, err := model.EstimateRoutingCostSnapshot(model.RoutingCostSnapshotVersion{
			SourceType: SystemRoutingPricingSourceType, ObservedTime: now, EffectiveTime: now,
			ExpiresTime: now + 86_400, Confidence: model.RoutingCostConfidenceExact,
			ConfidenceScore: 1, Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
		}, pricing, model.RoutingCostRequestProfile{
			PromptTokens: 1_000, MaximumPromptTokens: 1_000,
			ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
			MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
			MaximumCompletionKnown: true, CacheReadTokensKnown: false,
			CacheWriteTokensKnown: true, CacheWriteOneHourTokensKnown: true,
			ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
			AudioInputTokensKnown: true, AudioOutputTokensKnown: true, RequestInputKnown: true,
		}, now)
		require.NoError(t, err)
		return result
	}

	defaultCache := resolve()
	assert.Nil(t, defaultCache.Pricing.CacheReadCostPerMillion)
	assert.True(t, estimate(defaultCache.Pricing).ExpectedKnown)

	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"routing-cache-default":0.5}`))
	discountedCache := resolve()
	require.NotNil(t, discountedCache.Pricing.CacheReadCostPerMillion)
	assert.False(t, estimate(discountedCache.Pricing).ExpectedKnown)
}

func TestSystemRoutingPricingIsPinnedToTheRequestSnapshot(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"routing-pricing-refresh":0.25}`))

	now := time.Now().Unix()
	profile := model.RoutingCostRequestProfile{
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
		RequestInputKnown: true, RequestPricingFeaturesKnown: true,
	}
	resolved, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "routing-pricing-refresh", EffectiveModel: "routing-pricing-refresh",
		UpstreamCostMultiplier: 2, ChannelConfigurationRevision: 11,
	})
	require.NoError(t, err)
	require.True(t, resolved.Known)
	pricing := resolved.Pricing
	first, cached, exists, err := estimateModelSnapshotRoutingCost(ModelSnapshot{
		ModelName:                    "routing-pricing-refresh",
		UpstreamModelName:            "routing-pricing-refresh",
		ChannelConfigurationRevision: 11,
		CostUpstreamMultiplier:       2,
		CostPricing:                  &pricing,
		CostPricingHash:              resolved.PricingHash,
		CostPricingVersion:           resolved.PricingIdentity,
		CostPricingIdentity:          resolved.PricingIdentity,
		CostObservedTime:             now,
		CostEffectiveTime:            now,
		CostExpiresTime:              math.MaxInt64,
		CostVersionConfidence:        model.RoutingCostConfidenceExact,
		CostConfidenceScore:          1,
		CostFreshness:                model.RoutingCostFreshnessFresh,
		CostFreshnessScore:           1,
		systemPricing:                true,
	}, profile, now)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, first.ExpectedKnown)
	assert.InDelta(t, 0.5, first.ExpectedCost, 1e-12)
	firstIdentity := cached.CostPricingIdentity
	require.NotEmpty(t, firstIdentity)

	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"routing-pricing-refresh":0.75}`))

	second, pinned, exists, err := estimateModelSnapshotRoutingCost(cached, profile, now)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, second.ExpectedKnown)
	assert.InDelta(t, 0.5, second.ExpectedCost, 1e-12)
	assert.Equal(t, firstIdentity, pinned.CostPricingIdentity)
	assert.Equal(t, cached.CostPricingHash, pinned.CostPricingHash)

	newSnapshotPricing, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "routing-pricing-refresh", EffectiveModel: "routing-pricing-refresh",
		UpstreamCostMultiplier: 2, ChannelConfigurationRevision: 11,
	})
	require.NoError(t, err)
	assert.NotEqual(t, firstIdentity, newSnapshotPricing.PricingIdentity)
	newEstimate, err := model.EstimateRoutingCostSnapshot(model.RoutingCostSnapshotVersion{
		SourceType: SystemRoutingPricingSourceType, ObservedTime: now, EffectiveTime: now,
		ExpiresTime: math.MaxInt64, Confidence: model.RoutingCostConfidenceExact,
		ConfidenceScore: 1, Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
	}, newSnapshotPricing.Pricing, profile, now)
	require.NoError(t, err)
	assert.InDelta(t, 1.5, newEstimate.ExpectedCost, 1e-12)
}

func TestSystemRoutingPricingDoesNotTreatUncataloguedMediaSurchargesAsFree(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"routing-media-cost":0.25}`))
	resolution, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "routing-media-cost", EffectiveModel: "routing-media-cost",
		UpstreamCostMultiplier: 1, ChannelConfigurationRevision: 1,
	})
	require.NoError(t, err)
	require.True(t, resolution.Known)
	pricing := resolution.Pricing
	now := time.Now().Unix()
	estimate, _, exists, err := estimateModelSnapshotRoutingCost(ModelSnapshot{
		ModelName: "routing-media-cost", CostPricing: &pricing, systemPricing: true,
		CostConfidenceScore: 1, CostFreshnessScore: 1,
	}, model.RoutingCostRequestProfile{
		MaxAttempts: 1, KnowledgeSpecified: true, RequestPricingFeaturesKnown: true,
		VideoSeconds: 8, VideoDurationKnown: true, UncataloguedSurchargePossible: true,
	}, now)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.False(t, estimate.ExpectedKnown)
	assert.Equal(t, model.RoutingCostUnknownMissingContext, estimate.UnknownReason)
	assert.Equal(t, []string{"uncatalogued_surcharge"}, estimate.MissingContext)
	assert.Zero(t, estimate.ExpectedCost)
}

func TestResolveSystemRoutingPricingSupportsFixedAndExpressionPricing(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixed-routing-cost":0.75}`))

	fixed, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "fixed-routing-cost", EffectiveModel: "fixed-routing-cost",
		UpstreamCostMultiplier: 2, ChannelConfigurationRevision: 1,
	})
	require.NoError(t, err)
	require.True(t, fixed.Known)
	require.NotNil(t, fixed.Pricing.PerRequestCost)
	assert.InDelta(t, 0.75, *fixed.Pricing.PerRequestCost, 1e-12)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"expression-routing-cost":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"expression-routing-cost":"v1:tier(\"base\", p * 2 + c * 8)"}`,
	}))

	expression, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "logical", EffectiveModel: "expression-routing-cost",
		UpstreamCostMultiplier: 0.5, ChannelConfigurationRevision: 9,
	})
	require.NoError(t, err)
	require.True(t, expression.Known)
	assert.Equal(t, 1, expression.BillingExpressionVersion)
	assert.Contains(t, expression.Pricing.BillingExpression, "p * 2")
	assert.True(t, strings.HasSuffix(expression.PricingIdentity, ":channel-config:9"))
}

func TestShadowExpectedCostIncludesSystemBaselineAndChannelMultiplier(t *testing.T) {
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"baseline-routing-cost":0.75}`))
	now := time.Now().Unix()
	resolution, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		LogicalModel: "baseline-routing-cost", EffectiveModel: "baseline-routing-cost",
		UpstreamCostMultiplier: 2, ChannelConfigurationRevision: 9,
	})
	require.NoError(t, err)
	require.True(t, resolution.Known)
	pricing := resolution.Pricing
	cost, err := shadowExpectedCost(ModelSnapshot{
		ModelName:                    "baseline-routing-cost",
		UpstreamModelName:            "baseline-routing-cost",
		ChannelConfigurationRevision: 9,
		CostUpstreamMultiplier:       2,
		CostBillingMode:              SystemRoutingPricingBasis,
		CostPricing:                  &pricing,
		CostPricingHash:              resolution.PricingHash,
		CostPricingVersion:           resolution.PricingIdentity,
		CostPricingIdentity:          resolution.PricingIdentity,
		CostObservedTime:             now,
		CostEffectiveTime:            now,
		CostExpiresTime:              math.MaxInt64,
		CostVersionConfidence:        model.RoutingCostConfidenceExact,
		CostConfidenceScore:          1,
		CostFreshness:                model.RoutingCostFreshnessFresh,
		CostFreshnessScore:           1,
		systemPricing:                true,
	}, RequestProfile{
		ModelName:  "baseline-routing-cost",
		costAtUnix: now,
		costProfile: &model.RoutingCostRequestProfile{
			MaxAttempts:        1,
			KnowledgeSpecified: true, InputTokensKnown: true, MaximumCompletionKnown: true,
			CacheTokensKnown: true, ImageInputTokensKnown: true, ImageOutputTokensKnown: true,
			ImageUnitsKnown: true, AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
			RequestInputKnown:           true,
			RequestPricingFeaturesKnown: true,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, cost)
	assert.True(t, cost.Known)
	assert.InDelta(t, 1.5, cost.Cost, 1e-12)
	assert.True(t, cost.BaselineExpectedKnown)
	assert.InDelta(t, 0.75, cost.BaselineExpectedCost, 1e-12)
	assert.True(t, cost.BaselineWorstCaseKnown)
	assert.InDelta(t, 0.75, cost.BaselineWorstCaseCost, 1e-12)
	assert.Equal(t, 2.0, cost.UpstreamCostMultiplier)
	assert.Equal(t, int64(9), cost.ConfigurationRevision)
	assert.Contains(t, cost.PricingIdentity, ":channel-config:9")
}

func TestSystemRoutingCostEstimateFailsClosedWhenFiniteInputsOverflow(t *testing.T) {
	now := time.Now().Unix()
	groupRatio := 1.0
	inputRate := math.MaxFloat64
	_, err := model.EstimateRoutingCostSnapshot(model.RoutingCostSnapshotVersion{
		SourceType: SystemRoutingPricingSourceType, ObservedTime: now, EffectiveTime: now,
		ExpiresTime: now + 86_400, Confidence: model.RoutingCostConfidenceExact,
		ConfidenceScore: 1, Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
	}, model.RoutingNormalizedPricing{
		BillingMode: SystemRoutingPricingBasis, Currency: "USD", Unit: "mixed",
		GroupRatio: &groupRatio, InputCostPerMillion: &inputRate,
	}, model.RoutingCostRequestProfile{
		PromptTokens: 1_000_000_000_000, MaximumPromptTokens: 1_000_000_000_000,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		ImageInputTokensKnown: true, ImageOutputTokensKnown: true, ImageUnitsKnown: true,
		AudioInputTokensKnown: true, AudioOutputTokensKnown: true, RequestInputKnown: true,
	}, now)
	assert.ErrorIs(t, err, model.ErrRoutingCostInvalid)
}

func TestResolveSystemRoutingPricingFailsClosedForInvalidInputsAndMissingBaseline(t *testing.T) {
	for _, multiplier := range []float64{-1, 1000.0001, math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
			EffectiveModel: "anything", UpstreamCostMultiplier: multiplier,
		})
		assert.ErrorIs(t, err, ErrSystemRoutingPricingInput)
	}

	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"overflow-routing-cost":1e308}`))
	_, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		EffectiveModel: "overflow-routing-cost", UpstreamCostMultiplier: 1,
		ChannelConfigurationRevision: 1,
	})
	assert.ErrorIs(t, err, ErrSystemRoutingPricingInput)

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	resolution, err := ResolveSystemRoutingPricing(SystemRoutingPricingInput{
		EffectiveModel: "missing-routing-cost", UpstreamCostMultiplier: 1,
		ChannelConfigurationRevision: 1,
	})
	require.NoError(t, err)
	assert.False(t, resolution.Known)
	assert.Equal(t, SystemRoutingPricingUnknownBaseline, resolution.UnknownReason)
}

func restoreSystemRoutingRatioSettings(t *testing.T) {
	t.Helper()
	modelPrices := ratio_setting.ModelPrice2JSONString()
	modelRatios := ratio_setting.ModelRatio2JSONString()
	completionRatios := ratio_setting.CompletionRatio2JSONString()
	cacheRatios := ratio_setting.CacheRatio2JSONString()
	cacheWriteRatios := ratio_setting.CreateCacheRatio2JSONString()
	imageRatios := ratio_setting.ImageRatio2JSONString()
	audioRatios := ratio_setting.AudioRatio2JSONString()
	audioCompletionRatios := ratio_setting.AudioCompletionRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(modelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(modelRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(completionRatios))
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(cacheRatios))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(cacheWriteRatios))
		require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(imageRatios))
		require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(audioRatios))
		require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(audioCompletionRatios))
	})
}
