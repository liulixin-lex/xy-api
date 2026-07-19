package ratio_setting

import "github.com/QuantumNous/new-api/common"

// ValidatePriceRatioOption validates every persisted flat or nested numeric
// price/ratio map owned by this package. Unknown option keys are ignored so
// callers can apply it safely at shared option-persistence boundaries.
func ValidatePriceRatioOption(key, jsonStr string) error {
	switch key {
	case "ModelPrice", "ModelRatio", "CompletionRatio", "CacheRatio",
		"CreateCacheRatio", "ImageRatio", "AudioRatio",
		"AudioCompletionRatio", "GroupRatio":
		return common.ValidateFiniteNonNegativeFloatMapJSON(jsonStr)
	case "GroupGroupRatio":
		return common.ValidateFiniteNonNegativeNestedFloatMapJSON(jsonStr)
	default:
		return nil
	}
}
