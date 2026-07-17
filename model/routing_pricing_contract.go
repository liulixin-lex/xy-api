package model

import (
	"errors"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

const (
	RoutingPricingContractSchemaVersion = 2

	RoutingPricingContractModeDimensions = "dimensions"
	RoutingPricingContractModePerRequest = "per_request"
	RoutingPricingContractModeExpression = "expression"
	RoutingPricingContractModeFree       = "free"
)

var ErrRoutingPricingContractInvalid = errors.New("invalid routing pricing contract v2")

// RoutingPricingContractV2 is a routing-only upstream cost contract. Pointer
// presence is part of the contract: nil means the dimension is absent and
// therefore free, while a pointer to zero is an explicit free price that may
// be shown to operators. Channel multipliers are intentionally stored outside
// this contract.
type RoutingPricingContractV2 struct {
	SchemaVersion int    `json:"schema_version"`
	Mode          string `json:"mode"`
	Currency      string `json:"currency"`

	InputCostPerMillion        *float64 `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion       *float64 `json:"output_cost_per_million,omitempty"`
	CacheReadCostPerMillion    *float64 `json:"cache_read_cost_per_million,omitempty"`
	CacheWriteCostPerMillion   *float64 `json:"cache_write_cost_per_million,omitempty"`
	CacheWrite1hCostPerMillion *float64 `json:"cache_write_1h_cost_per_million,omitempty"`
	ImageInputCostPerMillion   *float64 `json:"image_input_cost_per_million,omitempty"`
	ImageOutputCostPerMillion  *float64 `json:"image_output_cost_per_million,omitempty"`
	PerImageCost               *float64 `json:"per_image_cost,omitempty"`
	AudioInputCostPerMillion   *float64 `json:"audio_input_cost_per_million,omitempty"`
	AudioOutputCostPerMillion  *float64 `json:"audio_output_cost_per_million,omitempty"`
	AudioCostPerSecond         *float64 `json:"audio_cost_per_second,omitempty"`
	VideoCostPerSecond         *float64 `json:"video_cost_per_second,omitempty"`
	PerTaskCost                *float64 `json:"per_task_cost,omitempty"`
	PerRequestCost             *float64 `json:"per_request_cost,omitempty"`

	BillingExpression        string `json:"billing_expression,omitempty"`
	BillingExpressionVersion int    `json:"billing_expression_version,omitempty"`
}

func NormalizeRoutingPricingContractV2(contract RoutingPricingContractV2) (RoutingPricingContractV2, error) {
	if contract.SchemaVersion == 0 {
		contract.SchemaVersion = RoutingPricingContractSchemaVersion
	}
	contract.Mode = strings.ToLower(strings.TrimSpace(contract.Mode))
	contract.Currency = strings.ToUpper(strings.TrimSpace(contract.Currency))
	contract.BillingExpression = strings.TrimSpace(contract.BillingExpression)
	if contract.Currency == "" {
		contract.Currency = "USD"
	}
	if contract.SchemaVersion != RoutingPricingContractSchemaVersion ||
		!validRoutingCostText(contract.Currency, 8) || contract.BillingExpressionVersion < 0 {
		return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
	}
	values := []*float64{
		contract.InputCostPerMillion, contract.OutputCostPerMillion,
		contract.CacheReadCostPerMillion, contract.CacheWriteCostPerMillion,
		contract.CacheWrite1hCostPerMillion, contract.ImageInputCostPerMillion,
		contract.ImageOutputCostPerMillion, contract.PerImageCost,
		contract.AudioInputCostPerMillion, contract.AudioOutputCostPerMillion,
		contract.AudioCostPerSecond, contract.VideoCostPerSecond, contract.PerTaskCost,
		contract.PerRequestCost,
	}
	configuredDimensions := 0
	for _, value := range values {
		if value == nil {
			continue
		}
		configuredDimensions++
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
	}
	switch contract.Mode {
	case RoutingPricingContractModeDimensions:
		if configuredDimensions == 0 || contract.PerRequestCost != nil || contract.BillingExpression != "" ||
			contract.BillingExpressionVersion != 0 {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
	case RoutingPricingContractModePerRequest:
		if contract.PerRequestCost == nil || configuredDimensions != 1 || contract.BillingExpression != "" ||
			contract.BillingExpressionVersion != 0 {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
	case RoutingPricingContractModeExpression:
		if configuredDimensions != 0 || contract.BillingExpression == "" || contract.BillingExpressionVersion <= 0 {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
		if _, err := billingexpr.CompileFromCache(contract.BillingExpression); err != nil {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
	case RoutingPricingContractModeFree:
		if configuredDimensions != 0 || contract.BillingExpression != "" || contract.BillingExpressionVersion != 0 {
			return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
		}
	default:
		return RoutingPricingContractV2{}, ErrRoutingPricingContractInvalid
	}
	return contract, nil
}

func (contract RoutingPricingContractV2) ToRoutingNormalizedPricing(
	billingMode string,
	channelMultiplier float64,
) RoutingNormalizedPricing {
	pricing := RoutingNormalizedPricing{
		BillingMode: billingMode, Currency: contract.Currency, Unit: "mixed",
		GroupRatio: &channelMultiplier, ContractV2: &contract,
		InputCostPerMillion:        contract.InputCostPerMillion,
		OutputCostPerMillion:       contract.OutputCostPerMillion,
		CacheReadCostPerMillion:    contract.CacheReadCostPerMillion,
		CacheWriteCostPerMillion:   contract.CacheWriteCostPerMillion,
		CacheWrite1hCostPerMillion: contract.CacheWrite1hCostPerMillion,
		ImageInputCostPerMillion:   contract.ImageInputCostPerMillion,
		ImageOutputCostPerMillion:  contract.ImageOutputCostPerMillion,
		PerImageCost:               contract.PerImageCost,
		AudioInputCostPerMillion:   contract.AudioInputCostPerMillion,
		AudioOutputCostPerMillion:  contract.AudioOutputCostPerMillion,
		AudioCostPerSecond:         contract.AudioCostPerSecond,
		VideoCostPerSecond:         contract.VideoCostPerSecond,
		PerTaskCost:                contract.PerTaskCost,
		PerRequestCost:             contract.PerRequestCost,
		BillingExpression:          contract.BillingExpression,
	}
	switch contract.Mode {
	case RoutingPricingContractModePerRequest:
		pricing.Unit = "request"
		pricing.ModelPrice = contract.PerRequestCost
	case RoutingPricingContractModeExpression:
		pricing.Unit = "expression"
	case RoutingPricingContractModeFree:
		pricing.Unit = "mixed"
	}
	return pricing
}
