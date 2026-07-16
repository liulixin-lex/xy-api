package channelrouting

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

const (
	SystemRoutingPricingBasis      = "system_pricing_x_channel_multiplier"
	SystemRoutingPricingSourceType = "system_pricing"

	SystemRoutingPricingUnknownModel             = "effective_model_missing"
	SystemRoutingPricingUnknownBaseline          = "system_pricing_missing"
	SystemRoutingPricingUnknownExpression        = "billing_expression_missing"
	SystemRoutingPricingUnknownExpressionInvalid = "billing_expression_invalid"
	SystemRoutingPricingUnknownInvalid           = "system_pricing_invalid"
)

var ErrSystemRoutingPricingInput = errors.New("invalid system routing pricing input")

type SystemRoutingPricingInput struct {
	LogicalModel                 string
	EffectiveModel               string
	ModelMappingIdentity         string
	UpstreamCostMultiplier       float64
	ChannelConfigurationRevision int64
}

type SystemRoutingPricingResolution struct {
	Known                    bool
	UnknownReason            string
	Pricing                  model.RoutingNormalizedPricing
	PricingHash              string
	PricingIdentity          string
	BillingExpressionVersion int
}

// ResolveSystemRoutingPricing builds a routing-only platform cost contract.
// It deliberately reads no user group ratio and never creates or mutates
// PriceData, quota reservations, settlements, wallets, or consume logs.
func ResolveSystemRoutingPricing(input SystemRoutingPricingInput) (SystemRoutingPricingResolution, error) {
	input.LogicalModel = strings.TrimSpace(input.LogicalModel)
	input.EffectiveModel = strings.TrimSpace(input.EffectiveModel)
	input.ModelMappingIdentity = strings.TrimSpace(input.ModelMappingIdentity)
	if input.ChannelConfigurationRevision < 0 ||
		!finiteSystemRoutingPrice(input.UpstreamCostMultiplier) ||
		input.UpstreamCostMultiplier < 0 ||
		input.UpstreamCostMultiplier > model.RoutingChannelUpstreamCostMultiplierMaximum ||
		len(input.LogicalModel) > 256 || len(input.EffectiveModel) > 256 ||
		len(input.ModelMappingIdentity) > 4_096 {
		return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
	}
	if input.EffectiveModel == "" {
		return SystemRoutingPricingResolution{UnknownReason: SystemRoutingPricingUnknownModel}, nil
	}

	multiplier := input.UpstreamCostMultiplier
	pricing := model.RoutingNormalizedPricing{
		BillingMode: SystemRoutingPricingBasis,
		Currency:    "USD",
		Unit:        "mixed",
		GroupRatio:  &multiplier,
	}
	manifest := struct {
		Basis                    string   `json:"basis"`
		LogicalModel             string   `json:"logical_model"`
		EffectiveModel           string   `json:"effective_model"`
		ModelMappingIdentity     string   `json:"model_mapping_identity,omitempty"`
		ChannelConfigRevision    int64    `json:"channel_config_revision"`
		UpstreamCostMultiplier   float64  `json:"upstream_cost_multiplier"`
		QuotaPerUnit             float64  `json:"quota_per_unit"`
		BillingMode              string   `json:"billing_mode"`
		BillingExpression        string   `json:"billing_expression,omitempty"`
		BillingExpressionVersion int      `json:"billing_expression_version"`
		ModelPrice               *float64 `json:"model_price,omitempty"`
		ModelRatio               *float64 `json:"model_ratio,omitempty"`
		CompletionRatio          *float64 `json:"completion_ratio,omitempty"`
		CacheReadRatio           *float64 `json:"cache_read_ratio,omitempty"`
		CacheWriteRatio          *float64 `json:"cache_write_ratio,omitempty"`
		CacheWriteOneHourRatio   *float64 `json:"cache_write_1h_ratio,omitempty"`
		ImageInputRatio          *float64 `json:"image_input_ratio,omitempty"`
		AudioInputPrice          *float64 `json:"audio_input_price,omitempty"`
		AudioInputRatio          *float64 `json:"audio_input_ratio,omitempty"`
		AudioOutputRatio         *float64 `json:"audio_output_ratio,omitempty"`
	}{
		Basis:                  SystemRoutingPricingBasis,
		LogicalModel:           input.LogicalModel,
		EffectiveModel:         input.EffectiveModel,
		ModelMappingIdentity:   input.ModelMappingIdentity,
		ChannelConfigRevision:  input.ChannelConfigurationRevision,
		UpstreamCostMultiplier: multiplier,
		QuotaPerUnit:           common.QuotaPerUnit,
	}

	resolution := SystemRoutingPricingResolution{Pricing: pricing}
	if multiplier == 0 {
		manifest.BillingMode = "zero_multiplier"
		return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
	}
	if !finiteSystemRoutingPrice(common.QuotaPerUnit) || common.QuotaPerUnit <= 0 {
		return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
	}

	if billing_setting.GetBillingMode(input.EffectiveModel) == billing_setting.BillingModeTieredExpr {
		expression, exists := billing_setting.GetBillingExpr(input.EffectiveModel)
		expression = strings.TrimSpace(expression)
		if !exists || expression == "" {
			resolution.UnknownReason = SystemRoutingPricingUnknownExpression
			return resolution, nil
		}
		if _, err := billingexpr.CompileFromCache(expression); err != nil {
			resolution.UnknownReason = SystemRoutingPricingUnknownExpressionInvalid
			return resolution, nil
		}
		resolution.Pricing.BillingExpression = expression
		resolution.Pricing.Unit = "expression"
		resolution.BillingExpressionVersion = billingexpr.ExprVersion(expression)
		manifest.BillingMode = billing_setting.BillingModeTieredExpr
		manifest.BillingExpression = expression
		manifest.BillingExpressionVersion = resolution.BillingExpressionVersion
		return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
	}

	if modelPrice, exists := ratio_setting.GetModelPrice(input.EffectiveModel, false); exists {
		if !finiteSystemRoutingPrice(modelPrice) || modelPrice < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
		resolution.Pricing.Unit = "request"
		resolution.Pricing.ModelPrice = systemRoutingFloatPointer(modelPrice)
		resolution.Pricing.PerRequestCost = systemRoutingFloatPointer(modelPrice)
		manifest.BillingMode = "per_request"
		manifest.ModelPrice = systemRoutingFloatPointer(modelPrice)
		return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
	}

	modelRatio, exists, _ := ratio_setting.GetModelRatio(input.EffectiveModel)
	if !exists {
		resolution.UnknownReason = SystemRoutingPricingUnknownBaseline
		return resolution, nil
	}
	completionRatio := ratio_setting.GetCompletionRatio(input.EffectiveModel)
	cacheReadRatio, cacheReadConfigured := ratio_setting.GetCacheRatio(input.EffectiveModel)
	cacheWriteRatio, _ := ratio_setting.GetCreateCacheRatio(input.EffectiveModel)
	cacheWriteOneHourRatio := cacheWriteRatio * (6.0 / 3.75)
	imageInputRatio, _ := ratio_setting.GetImageRatio(input.EffectiveModel)
	audioInputRatio := ratio_setting.GetAudioRatio(input.EffectiveModel)
	audioOutputRatio := audioInputRatio * ratio_setting.GetAudioCompletionRatio(input.EffectiveModel)
	audioInputPrice := operation_setting.GetGeminiInputAudioPricePerMillionTokens(input.EffectiveModel)
	values := []float64{
		modelRatio, completionRatio, cacheReadRatio, cacheWriteRatio,
		cacheWriteOneHourRatio, imageInputRatio, audioInputRatio,
		audioOutputRatio, audioInputPrice,
	}
	for _, value := range values {
		if !finiteSystemRoutingPrice(value) || value < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
	}

	inputRate := modelRatio * 1_000_000 / common.QuotaPerUnit
	outputRate := inputRate * completionRatio
	cacheReadRate := inputRate * cacheReadRatio
	cacheWriteRate := inputRate * cacheWriteRatio
	cacheWriteOneHourRate := inputRate * cacheWriteOneHourRatio
	imageInputRate := inputRate * imageInputRatio
	audioInputRate := inputRate * audioInputRatio
	if audioInputPrice > 0 {
		audioInputRate = audioInputPrice
	}
	audioOutputRate := inputRate * audioOutputRatio
	for _, value := range []float64{
		inputRate, outputRate, cacheReadRate, cacheWriteRate,
		cacheWriteOneHourRate, imageInputRate, audioInputRate, audioOutputRate,
	} {
		if !finiteSystemRoutingPrice(value) || value < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
	}

	resolution.Pricing.BaseRatio = systemRoutingFloatPointer(modelRatio)
	resolution.Pricing.CompletionRatio = systemRoutingFloatPointer(completionRatio)
	resolution.Pricing.InputCostPerMillion = systemRoutingFloatPointer(inputRate)
	resolution.Pricing.OutputCostPerMillion = systemRoutingFloatPointer(outputRate)
	if cacheReadConfigured {
		resolution.Pricing.CacheReadCostPerMillion = systemRoutingFloatPointer(cacheReadRate)
	}
	resolution.Pricing.CacheWriteCostPerMillion = systemRoutingFloatPointer(cacheWriteRate)
	resolution.Pricing.CacheWrite1hCostPerMillion = systemRoutingFloatPointer(cacheWriteOneHourRate)
	resolution.Pricing.ImageInputCostPerMillion = systemRoutingFloatPointer(imageInputRate)
	resolution.Pricing.ImageOutputCostPerMillion = systemRoutingFloatPointer(outputRate)
	resolution.Pricing.AudioInputCostPerMillion = systemRoutingFloatPointer(audioInputRate)
	resolution.Pricing.AudioOutputCostPerMillion = systemRoutingFloatPointer(audioOutputRate)
	manifest.BillingMode = "ratio"
	manifest.ModelRatio = systemRoutingFloatPointer(modelRatio)
	manifest.CompletionRatio = systemRoutingFloatPointer(completionRatio)
	if cacheReadConfigured {
		manifest.CacheReadRatio = systemRoutingFloatPointer(cacheReadRatio)
	}
	manifest.CacheWriteRatio = systemRoutingFloatPointer(cacheWriteRatio)
	manifest.CacheWriteOneHourRatio = systemRoutingFloatPointer(cacheWriteOneHourRatio)
	manifest.ImageInputRatio = systemRoutingFloatPointer(imageInputRatio)
	manifest.AudioInputPrice = systemRoutingFloatPointer(audioInputPrice)
	manifest.AudioInputRatio = systemRoutingFloatPointer(audioInputRatio)
	manifest.AudioOutputRatio = systemRoutingFloatPointer(audioOutputRatio)
	return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
}

func finalizeSystemRoutingPricing[T any](
	resolution SystemRoutingPricingResolution,
	manifest T,
	revision int64,
) (SystemRoutingPricingResolution, error) {
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return SystemRoutingPricingResolution{}, err
	}
	digest := sha256.Sum256(encoded)
	resolution.PricingHash = hex.EncodeToString(digest[:])
	resolution.PricingIdentity = fmt.Sprintf("billing:%s:channel-config:%d", resolution.PricingHash, revision)
	metadata, err := common.Marshal(map[string]any{
		"pricing_basis":              SystemRoutingPricingBasis,
		"pricing_identity":           resolution.PricingIdentity,
		"billing_expression_version": resolution.BillingExpressionVersion,
		"catalog_scope":              SystemRoutingPricingSourceType,
	})
	if err != nil {
		return SystemRoutingPricingResolution{}, err
	}
	resolution.Pricing.Extras = metadata
	resolution.Known = true
	resolution.UnknownReason = ""
	return resolution, nil
}

func finiteSystemRoutingPrice(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func systemRoutingFloatPointer(value float64) *float64 {
	return &value
}
