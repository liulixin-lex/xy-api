package operation_setting

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateToolPricesByJSONString(t *testing.T) {
	require.NoError(t, ValidateToolPricesByJSONString(`{"free":0,"web_search":10}`))

	for _, invalid := range []string{
		`null`,
		`{"negative":-0.1}`,
		`{"null":null}`,
		`{"overflow":1e309}`,
		`{"nan":NaN}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			require.Error(t, ValidateToolPricesByJSONString(invalid))
		})
	}
}

func TestRebuildToolPriceIndexIgnoresInvalidLegacyValues(t *testing.T) {
	original := toolPriceSetting.Prices
	t.Cleanup(func() {
		toolPriceSetting.Prices = original
		RebuildToolPriceIndex()
	})

	toolPriceSetting.Prices = map[string]float64{
		"negative_only": -1,
		"nan_only":      math.NaN(),
		"infinite_only": math.Inf(1),
		"free":          0,
		"valid":         2.5,
	}
	RebuildToolPriceIndex()

	index := currentIndex.Load()
	require.NotNil(t, index)
	_, negativeExists := index.defaults["negative_only"]
	_, nanExists := index.defaults["nan_only"]
	_, infiniteExists := index.defaults["infinite_only"]
	freePrice, freeExists := index.defaults["free"]
	assert.False(t, negativeExists)
	assert.False(t, nanExists)
	assert.False(t, infiniteExists)
	assert.True(t, freeExists)
	assert.Zero(t, freePrice)
	assert.Equal(t, 2.5, index.defaults["valid"])
}
