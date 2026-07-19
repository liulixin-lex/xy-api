package billing_setting_test

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmokeTestExprRejectsInvalidBillingResults(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "NaN",
			expr: `tier("bad", (p * 1e308) - (p * 1e308))`,
		},
		{
			name: "positive infinity",
			expr: `tier("bad", p * 1e308)`,
		},
		{
			name: "negative",
			expr: `tier("bad", -1)`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := billing_setting.SmokeTestExpr(test.expr)

			require.Error(t, err)
			assert.ErrorContains(t, err, "expr result must be finite and non-negative")
		})
	}
}

func TestSmokeTestExprAcceptsFiniteNonNegativeResults(t *testing.T) {
	require.NoError(t, billing_setting.SmokeTestExpr(`tier("base", p * 2 + c * 4)`))
}
