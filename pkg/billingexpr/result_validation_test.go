package billingexpr_test

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunExprRejectsInvalidBillingResults(t *testing.T) {
	tests := []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative", value: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := billingexpr.RunExpr(
				`tier("base", p)`,
				billingexpr.TokenParams{P: test.value},
			)

			require.Error(t, err)
			assert.ErrorContains(t, err, "expr result must be finite and non-negative")
		})
	}
}

func TestComputeTieredQuotaRejectsInvalidSnapshotAmounts(t *testing.T) {
	tests := []struct {
		name         string
		quotaPerUnit float64
		groupRatio   float64
		wantError    string
	}{
		{name: "NaN quota per unit", quotaPerUnit: math.NaN(), groupRatio: 1, wantError: "quota per unit"},
		{name: "infinite quota per unit", quotaPerUnit: math.Inf(1), groupRatio: 1, wantError: "quota per unit"},
		{name: "negative quota per unit", quotaPerUnit: -1, groupRatio: 1, wantError: "quota per unit"},
		{name: "NaN group ratio", quotaPerUnit: 500_000, groupRatio: math.NaN(), wantError: "group ratio"},
		{name: "infinite group ratio", quotaPerUnit: 500_000, groupRatio: math.Inf(1), wantError: "group ratio"},
		{name: "negative group ratio", quotaPerUnit: 500_000, groupRatio: -1, wantError: "group ratio"},
	}

	exprStr := `tier("base", p)`
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := &billingexpr.BillingSnapshot{
				ExprString:   exprStr,
				ExprHash:     billingexpr.ExprHashString(exprStr),
				QuotaPerUnit: test.quotaPerUnit,
				GroupRatio:   test.groupRatio,
			}

			_, err := billingexpr.ComputeTieredQuota(snapshot, billingexpr.TokenParams{P: 1})

			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantError+" must be finite and non-negative")
		})
	}
}

func TestComputeTieredQuotaRejectsNonFiniteIntermediateQuota(t *testing.T) {
	tests := []struct {
		name         string
		expr         string
		params       billingexpr.TokenParams
		quotaPerUnit float64
		groupRatio   float64
		wantError    string
	}{
		{
			name:         "quota conversion overflow",
			expr:         `tier("base", p * 1e308)`,
			params:       billingexpr.TokenParams{P: 1},
			quotaPerUnit: 1e308,
			groupRatio:   1,
			wantError:    "tiered quota before group",
		},
		{
			name:         "group multiplication overflow",
			expr:         `tier("base", p)`,
			params:       billingexpr.TokenParams{P: 1_000_000},
			quotaPerUnit: math.MaxFloat64,
			groupRatio:   2,
			wantError:    "tiered quota after group",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := &billingexpr.BillingSnapshot{
				ExprString:   test.expr,
				ExprHash:     billingexpr.ExprHashString(test.expr),
				QuotaPerUnit: test.quotaPerUnit,
				GroupRatio:   test.groupRatio,
			}

			_, err := billingexpr.ComputeTieredQuota(snapshot, test.params)

			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantError+" must be finite and non-negative")
		})
	}
}

func TestComputeTieredQuotaRejectsNilSnapshot(t *testing.T) {
	_, err := billingexpr.ComputeTieredQuota(nil, billingexpr.TokenParams{})

	require.EqualError(t, err, "billing snapshot is nil")
}
