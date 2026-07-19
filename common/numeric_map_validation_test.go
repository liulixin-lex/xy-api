package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateFiniteNonNegativeFloatMapJSON(t *testing.T) {
	for _, jsonStr := range []string{
		`{}`,
		`{"free":0,"paid":1.25}`,
		`{"negative_zero":-0}`,
	} {
		require.NoError(t, ValidateFiniteNonNegativeFloatMapJSON(jsonStr))
	}

	for _, jsonStr := range []string{
		`null`,
		`[]`,
		`{"negative":-0.01}`,
		`{"null":null}`,
		`{"string":"1"}`,
		`{"nan":NaN}`,
		`{"infinity":Infinity}`,
		`{"overflow":1e309}`,
		`{"negative_overflow":-1e309}`,
	} {
		t.Run(jsonStr, func(t *testing.T) {
			require.Error(t, ValidateFiniteNonNegativeFloatMapJSON(jsonStr))
		})
	}
}

func TestValidateFiniteNonNegativeNestedFloatMapJSON(t *testing.T) {
	for _, jsonStr := range []string{
		`{}`,
		`{"vip":{}}`,
		`{"vip":{"default":0,"priority":0.9}}`,
	} {
		require.NoError(t, ValidateFiniteNonNegativeNestedFloatMapJSON(jsonStr))
	}

	for _, jsonStr := range []string{
		`null`,
		`{"vip":null}`,
		`{"vip":{"default":-0.01}}`,
		`{"vip":{"default":null}}`,
		`{"vip":{"default":1e309}}`,
		`{"vip":[]}`,
	} {
		t.Run(jsonStr, func(t *testing.T) {
			require.Error(t, ValidateFiniteNonNegativeNestedFloatMapJSON(jsonStr))
		})
	}
}
