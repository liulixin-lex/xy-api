package operation_setting

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// LegacyImplicitPaymentRoutes describes routes that older releases published
// from provider settings even when PayMethods did not contain a matching
// catalog entry. It is consumed only by the one-time database migration.
type LegacyImplicitPaymentRoutes struct {
	Stripe       bool
	Creem        bool
	Waffo        bool
	WaffoPancake bool
	XorPay       []string
}

// MergeLegacyImplicitPaymentRoutes converts the old implicit route set into a
// canonical PayMethods snapshot. Existing entries and their ordering win;
// missing compatibility routes are appended in the historical implicit order.
func MergeLegacyImplicitPaymentRoutes(methods []map[string]string, legacy LegacyImplicitPaymentRoutes) ([]map[string]string, int, error) {
	encoded, err := common.Marshal(methods)
	if err != nil {
		return nil, 0, err
	}
	canonical, err := ParsePayMethodsByJsonString(string(encoded))
	if err != nil {
		return nil, 0, err
	}

	seen := make(map[string]struct{}, len(canonical))
	for _, method := range canonical {
		seen[paymentMethodIdentityKey(method["provider"], method["type"])] = struct{}{}
	}
	added := 0
	appendMissing := func(provider, methodType, name, icon string) {
		identity := paymentMethodIdentityKey(provider, methodType)
		if _, exists := seen[identity]; exists {
			return
		}
		seen[identity] = struct{}{}
		method := map[string]string{
			"name": name, "type": methodType, "provider": provider,
		}
		if icon != "" {
			method["icon"] = icon
		}
		canonical = append(canonical, method)
		added++
	}

	if legacy.Stripe {
		appendMissing("stripe", "stripe", "Card", "LuCreditCard")
	}
	if legacy.Creem {
		appendMissing("creem", "creem", "Online payment", "LuCreditCard")
	}
	if legacy.Waffo {
		appendMissing("waffo", "waffo", "Online payment", "LuCreditCard")
	}
	if legacy.WaffoPancake {
		appendMissing("waffo_pancake", "waffo_pancake", "Online payment", "LuCreditCard")
	}
	for _, methodType := range legacy.XorPay {
		switch methodType {
		case "xorpay_native":
			appendMissing("xorpay", methodType, "WeChat Pay", "SiWechat")
		case "xorpay_alipay":
			appendMissing("xorpay", methodType, "Alipay", "SiAlipay")
		case "xorpay_jsapi":
			appendMissing("xorpay", methodType, "WeChat Pay", "SiWechat")
		default:
			return nil, 0, fmt.Errorf("unsupported legacy XORPay route: %s", methodType)
		}
	}

	encoded, err = common.Marshal(canonical)
	if err != nil {
		return nil, 0, err
	}
	canonical, err = ParsePayMethodsByJsonString(string(encoded))
	if err != nil {
		return nil, 0, err
	}
	return canonical, added, nil
}
