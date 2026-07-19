package common

import (
	"errors"
	"fmt"
	"math"
)

// ValidateFiniteNonNegativeFloatMapJSON validates a JSON object whose values
// are billing prices or ratios. A pointer value keeps JSON null distinct from
// the valid numeric value 0.
func ValidateFiniteNonNegativeFloatMapJSON(jsonStr string) error {
	values := make(map[string]*float64)
	if err := UnmarshalJsonStr(jsonStr, &values); err != nil {
		return err
	}
	if values == nil {
		return errors.New("value must be a JSON object")
	}
	for key, value := range values {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
			return fmt.Errorf("value for %q must be finite and not less than 0", key)
		}
	}
	return nil
}

// ValidateFiniteNonNegativeNestedFloatMapJSON validates a nested JSON object
// used by inter-group billing overrides. Empty inner objects and numeric 0 are
// valid, while null, negative, NaN, and infinite values are rejected.
func ValidateFiniteNonNegativeNestedFloatMapJSON(jsonStr string) error {
	values := make(map[string]map[string]*float64)
	if err := UnmarshalJsonStr(jsonStr, &values); err != nil {
		return err
	}
	if values == nil {
		return errors.New("value must be a nested JSON object")
	}
	for outerKey, nestedValues := range values {
		if nestedValues == nil {
			return fmt.Errorf("value for %q must be a JSON object", outerKey)
		}
		for innerKey, value := range nestedValues {
			if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
				return fmt.Errorf("value for %q -> %q must be finite and not less than 0", outerKey, innerKey)
			}
		}
	}
	return nil
}
