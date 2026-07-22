package controller

import (
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

// normalizeRetainedTopUpCredit preserves the legacy Waffo/Pancake Amount
// snapshot semantics while proving that its eventual credit is representable.
// Token display can only store whole base units in the retained TopUp schema,
// so it rejects values that cannot round-trip exactly instead of under- or
// over-crediting them. All division and quota conversion remains decimal-based.
func normalizeRetainedTopUpCredit(amount int64) (int64, int, bool) {
	if amount <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) || common.QuotaPerUnit <= 0 {
		return 0, 0, false
	}

	quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	normalizedAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		requestedCredit := decimal.NewFromInt(amount)
		normalized := requestedCredit.Div(quotaPerUnit)
		if normalized.LessThan(decimal.NewFromInt(1)) ||
			!normalized.Equal(normalized.Truncate(0)) ||
			normalized.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
			return 0, 0, false
		}
		normalizedAmount = normalized.IntPart()
		if !decimal.NewFromInt(normalizedAmount).Mul(quotaPerUnit).Equal(requestedCredit) {
			return 0, 0, false
		}
	}

	creditQuota, clamp := common.QuotaFromDecimalChecked(
		decimal.NewFromInt(normalizedAmount).Mul(quotaPerUnit),
	)
	if clamp != nil || creditQuota <= 0 {
		return 0, 0, false
	}
	return normalizedAmount, creditQuota, true
}
