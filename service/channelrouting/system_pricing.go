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
	Contract                 model.RoutingPricingContractV2
	UpstreamCostMultiplier   float64
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
	manifest := struct {
		SchemaVersion          int                            `json:"schema_version"`
		Basis                  string                         `json:"basis"`
		LogicalModel           string                         `json:"logical_model"`
		EffectiveModel         string                         `json:"effective_model"`
		ModelMappingIdentity   string                         `json:"model_mapping_identity,omitempty"`
		ChannelConfigRevision  int64                          `json:"channel_config_revision"`
		UpstreamCostMultiplier float64                        `json:"upstream_cost_multiplier"`
		QuotaPerUnit           float64                        `json:"quota_per_unit"`
		Contract               model.RoutingPricingContractV2 `json:"contract"`
		ModelRatio             *float64                       `json:"model_ratio,omitempty"`
		CompletionRatio        *float64                       `json:"completion_ratio,omitempty"`
		CacheReadRatio         *float64                       `json:"cache_read_ratio,omitempty"`
		CacheWriteRatio        *float64                       `json:"cache_write_ratio,omitempty"`
		ImageInputRatio        *float64                       `json:"image_input_ratio,omitempty"`
		AudioInputPrice        *float64                       `json:"audio_input_price,omitempty"`
		AudioInputRatio        *float64                       `json:"audio_input_ratio,omitempty"`
		AudioCompletionRatio   *float64                       `json:"audio_completion_ratio,omitempty"`
	}{
		SchemaVersion:          model.RoutingPricingContractSchemaVersion,
		Basis:                  SystemRoutingPricingBasis,
		LogicalModel:           input.LogicalModel,
		EffectiveModel:         input.EffectiveModel,
		ModelMappingIdentity:   input.ModelMappingIdentity,
		ChannelConfigRevision:  input.ChannelConfigurationRevision,
		UpstreamCostMultiplier: multiplier,
		QuotaPerUnit:           common.QuotaPerUnit,
	}

	resolution := SystemRoutingPricingResolution{UpstreamCostMultiplier: multiplier}
	if multiplier == 0 {
		contract, contractErr := model.NormalizeRoutingPricingContractV2(model.RoutingPricingContractV2{
			SchemaVersion: model.RoutingPricingContractSchemaVersion,
			Mode:          model.RoutingPricingContractModeFree, Currency: "USD",
		})
		if contractErr != nil {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
		resolution.Contract = contract
		resolution.Pricing = contract.ToRoutingNormalizedPricing(SystemRoutingPricingBasis, multiplier)
		manifest.Contract = contract
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
		resolution.BillingExpressionVersion = billingexpr.ExprVersion(expression)
		contract, contractErr := model.NormalizeRoutingPricingContractV2(model.RoutingPricingContractV2{
			SchemaVersion: model.RoutingPricingContractSchemaVersion,
			Mode:          model.RoutingPricingContractModeExpression, Currency: "USD",
			BillingExpression: expression, BillingExpressionVersion: resolution.BillingExpressionVersion,
		})
		if contractErr != nil {
			resolution.UnknownReason = SystemRoutingPricingUnknownExpressionInvalid
			return resolution, nil
		}
		resolution.Contract = contract
		resolution.Pricing = contract.ToRoutingNormalizedPricing(SystemRoutingPricingBasis, multiplier)
		manifest.Contract = contract
		return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
	}

	if modelPrice, exists := ratio_setting.GetModelPrice(input.EffectiveModel, false); exists {
		if !finiteSystemRoutingPrice(modelPrice) || modelPrice < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
		contract, contractErr := model.NormalizeRoutingPricingContractV2(model.RoutingPricingContractV2{
			SchemaVersion: model.RoutingPricingContractSchemaVersion,
			Mode:          model.RoutingPricingContractModePerRequest, Currency: "USD",
			PerRequestCost: systemRoutingFloatPointer(modelPrice),
		})
		if contractErr != nil {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
		resolution.Contract = contract
		resolution.Pricing = contract.ToRoutingNormalizedPricing(SystemRoutingPricingBasis, multiplier)
		manifest.Contract = contract
		return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
	}

	modelRatio, exists := explicitSystemRoutingModelRatio(input.EffectiveModel)
	if !exists {
		resolution.UnknownReason = SystemRoutingPricingUnknownBaseline
		return resolution, nil
	}
	completionRatio, completionConfigured := explicitSystemRoutingCompletionRatio(input.EffectiveModel)
	cacheReadRatio, cacheReadConfigured := ratio_setting.GetCacheRatio(input.EffectiveModel)
	cacheWriteRatio, cacheWriteConfigured := ratio_setting.GetCreateCacheRatio(input.EffectiveModel)
	imageInputRatio, imageInputConfigured := ratio_setting.GetImageRatio(input.EffectiveModel)
	audioInputRatio := ratio_setting.GetAudioRatio(input.EffectiveModel)
	audioInputConfigured := ratio_setting.ContainsAudioRatio(input.EffectiveModel)
	audioCompletionRatio := ratio_setting.GetAudioCompletionRatio(input.EffectiveModel)
	audioCompletionConfigured := ratio_setting.ContainsAudioCompletionRatio(input.EffectiveModel)
	audioInputPrice := operation_setting.GetGeminiInputAudioPricePerMillionTokens(input.EffectiveModel)
	values := []float64{
		modelRatio, completionRatio, cacheReadRatio, cacheWriteRatio,
		imageInputRatio, audioInputRatio, audioCompletionRatio, audioInputPrice,
	}
	for _, value := range values {
		if !finiteSystemRoutingPrice(value) || value < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
	}

	inputRate := modelRatio * 1_000_000 / common.QuotaPerUnit
	contract := model.RoutingPricingContractV2{
		SchemaVersion: model.RoutingPricingContractSchemaVersion,
		Mode:          model.RoutingPricingContractModeDimensions, Currency: "USD",
		InputCostPerMillion: systemRoutingFloatPointer(inputRate),
	}
	if completionConfigured {
		contract.OutputCostPerMillion = systemRoutingFloatPointer(inputRate * completionRatio)
	}
	if cacheReadConfigured {
		contract.CacheReadCostPerMillion = systemRoutingFloatPointer(inputRate * cacheReadRatio)
	}
	if cacheWriteConfigured {
		contract.CacheWriteCostPerMillion = systemRoutingFloatPointer(inputRate * cacheWriteRatio)
	}
	if imageInputConfigured {
		contract.ImageInputCostPerMillion = systemRoutingFloatPointer(inputRate * imageInputRatio)
	}
	audioInputRate := 0.0
	if audioInputConfigured {
		audioInputRate = inputRate * audioInputRatio
	}
	if audioInputPrice > 0 {
		audioInputRate = audioInputPrice
		audioInputConfigured = true
	}
	if audioInputConfigured {
		contract.AudioInputCostPerMillion = systemRoutingFloatPointer(audioInputRate)
	}
	if audioInputConfigured && audioCompletionConfigured {
		contract.AudioOutputCostPerMillion = systemRoutingFloatPointer(audioInputRate * audioCompletionRatio)
	}
	for _, value := range []*float64{
		contract.InputCostPerMillion, contract.OutputCostPerMillion,
		contract.CacheReadCostPerMillion, contract.CacheWriteCostPerMillion,
		contract.ImageInputCostPerMillion, contract.AudioInputCostPerMillion,
		contract.AudioOutputCostPerMillion,
	} {
		if value == nil {
			continue
		}
		if !finiteSystemRoutingPrice(*value) || *value < 0 {
			return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
		}
	}
	contract, contractErr := model.NormalizeRoutingPricingContractV2(contract)
	if contractErr != nil {
		return SystemRoutingPricingResolution{}, ErrSystemRoutingPricingInput
	}
	resolution.Contract = contract
	resolution.Pricing = contract.ToRoutingNormalizedPricing(SystemRoutingPricingBasis, multiplier)
	resolution.Pricing.BaseRatio = systemRoutingFloatPointer(modelRatio)
	if completionConfigured {
		resolution.Pricing.CompletionRatio = systemRoutingFloatPointer(completionRatio)
	}
	manifest.Contract = contract
	manifest.ModelRatio = systemRoutingFloatPointer(modelRatio)
	if completionConfigured {
		manifest.CompletionRatio = systemRoutingFloatPointer(completionRatio)
	}
	if cacheReadConfigured {
		manifest.CacheReadRatio = systemRoutingFloatPointer(cacheReadRatio)
	}
	if cacheWriteConfigured {
		manifest.CacheWriteRatio = systemRoutingFloatPointer(cacheWriteRatio)
	}
	if imageInputConfigured {
		manifest.ImageInputRatio = systemRoutingFloatPointer(imageInputRatio)
	}
	if audioInputPrice > 0 {
		manifest.AudioInputPrice = systemRoutingFloatPointer(audioInputPrice)
	}
	if ratio_setting.ContainsAudioRatio(input.EffectiveModel) {
		manifest.AudioInputRatio = systemRoutingFloatPointer(audioInputRatio)
	}
	if audioCompletionConfigured {
		manifest.AudioCompletionRatio = systemRoutingFloatPointer(audioCompletionRatio)
	}
	return finalizeSystemRoutingPricing(resolution, manifest, input.ChannelConfigurationRevision)
}

func explicitSystemRoutingModelRatio(name string) (float64, bool) {
	name = ratio_setting.FormatMatchingModelName(name)
	ratios := ratio_setting.GetModelRatioCopy()
	if ratio, exists := ratios[name]; exists {
		return ratio, true
	}
	if strings.HasSuffix(name, ratio_setting.CompactModelSuffix) {
		ratio, exists := ratios[ratio_setting.CompactWildcardModelKey]
		return ratio, exists
	}
	return 0, false
}

func explicitSystemRoutingCompletionRatio(name string) (float64, bool) {
	name = ratio_setting.FormatMatchingModelName(name)
	if ratio, exists := ratio_setting.GetCompletionRatioCopy()[name]; exists {
		return ratio, true
	}
	info := ratio_setting.GetCompletionRatioInfo(name)
	return info.Ratio, info.Locked
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
		"contract_schema_version":    resolution.Contract.SchemaVersion,
		"contract_mode":              resolution.Contract.Mode,
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
