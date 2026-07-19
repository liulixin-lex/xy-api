package common

import (
	"strings"

	"golang.org/x/text/currency"
)

// PaymentCurrencyExponent returns the ISO 4217 minor-unit exponent used by
// payment providers. Unknown three-letter currencies use the common two-digit
// exponent; settings validation is responsible for requiring a valid code.
func PaymentCurrencyExponent(currency string) int32 {
	exponent, ok := PaymentCurrencyExponentOK(currency)
	if !ok {
		return 2
	}
	return exponent
}

func PaymentCurrencyExponentOK(value string) (int32, bool) {
	code := strings.ToUpper(strings.TrimSpace(value))
	if len(code) != 3 {
		return 0, false
	}
	unit, err := currency.ParseISO(code)
	if err != nil || unit.String() == "XXX" {
		return 0, false
	}
	scale, _ := currency.Standard.Rounding(unit)
	if scale < 0 || scale > 3 {
		return 0, false
	}
	return int32(scale), true
}

func PaymentCurrencyMinorUnit(currency string) int64 {
	switch PaymentCurrencyExponent(currency) {
	case 0:
		return 1
	case 3:
		return 1000
	default:
		return 100
	}
}

// PaymentProviderCurrencyExponentOK applies provider-specific charge amount
// rules on top of ISO 4217. Stripe retains two-decimal charge semantics for
// ISK and UGX for backwards compatibility even though both currencies are
// zero-decimal in ISO metadata.
func PaymentProviderCurrencyExponentOK(provider, value string) (int32, bool) {
	exponent, ok := PaymentCurrencyExponentOK(value)
	if !ok {
		return 0, false
	}
	if strings.EqualFold(strings.TrimSpace(provider), "stripe") {
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "ISK", "UGX":
			return 2, true
		}
	}
	return exponent, true
}

func PaymentProviderCurrencyExponent(provider, currency string) int32 {
	exponent, ok := PaymentProviderCurrencyExponentOK(provider, currency)
	if !ok {
		return 2
	}
	return exponent
}

func PaymentProviderCurrencyMinorUnit(provider, currency string) int64 {
	switch PaymentProviderCurrencyExponent(provider, currency) {
	case 0:
		return 1
	case 3:
		return 1000
	default:
		return 100
	}
}
