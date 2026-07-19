package billingexpr

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
)

// quotaConversion converts raw expression output to quota based on the
// expression version. This is the central dispatch point for future versions
// that may use a different conversion formula.
func quotaConversion(exprOutput float64, snap *BillingSnapshot) float64 {
	switch snap.ExprVersion {
	default: // v1: coefficients are $/1M tokens prices
		return exprOutput / 1_000_000 * snap.QuotaPerUnit
	}
}

// ComputeTieredQuota runs the Expr from a frozen BillingSnapshot against
// actual token counts and returns the settlement result.
func ComputeTieredQuota(snap *BillingSnapshot, params TokenParams) (TieredResult, error) {
	return ComputeTieredQuotaWithRequest(snap, params, RequestInput{})
}

func ComputeTieredQuotaWithRequest(snap *BillingSnapshot, params TokenParams, request RequestInput) (TieredResult, error) {
	if snap == nil {
		return TieredResult{}, errors.New("billing snapshot is nil")
	}
	if err := validateBillingAmount("quota per unit", snap.QuotaPerUnit); err != nil {
		return TieredResult{}, err
	}
	if err := validateBillingAmount("group ratio", snap.GroupRatio); err != nil {
		return TieredResult{}, err
	}

	cost, trace, err := RunExprByHashWithRequest(snap.ExprString, snap.ExprHash, params, request)
	if err != nil {
		return TieredResult{}, err
	}

	quotaBeforeGroup := quotaConversion(cost, snap)
	if err := validateBillingAmount("tiered quota before group", quotaBeforeGroup); err != nil {
		return TieredResult{}, err
	}
	quotaAfterGroup := quotaBeforeGroup * snap.GroupRatio
	if err := validateBillingAmount("tiered quota after group", quotaAfterGroup); err != nil {
		return TieredResult{}, err
	}
	afterGroup, clamp := common.QuotaRoundChecked(quotaAfterGroup)
	crossed := trace.MatchedTier != snap.EstimatedTier

	return TieredResult{
		ActualQuotaBeforeGroup: quotaBeforeGroup,
		ActualQuotaAfterGroup:  afterGroup,
		MatchedTier:            trace.MatchedTier,
		CrossedTier:            crossed,
		Clamp:                  clamp,
	}, nil
}
