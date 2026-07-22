/**
此文件为旧版支付设置文件，如需增加新的参数、变量等，请在 payment_setting.go 中添加
This file is the old version of the payment settings file. If you need to add new parameters, variables, etc., please add them in payment_setting.go
*/

package operation_setting

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
)

var paymentMethodTypePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var publicPaymentAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var paymentMethodAllowedFields = map[string]struct{}{
	"name": {}, "icon": {}, "type": {}, "provider": {}, "flow": {}, "color": {}, "min_topup": {},
	"route_id": {}, "public_method": {}, "channel_alias": {},
}

const maxConfiguredPaymentMethodMinimum = 10_000

var PayAddress = ""
var CustomCallbackAddress = ""
var EpayId = ""
var EpayKey = ""
var Price = 7.3
var MinTopUp = 1
var USDExchangeRate = 7.3

var PayMethods = []map[string]string{
	{
		"name":     "支付宝",
		"icon":     "SiAlipay",
		"type":     "alipay",
		"provider": "epay",
		"flow":     "form_post",
	},
	{
		"name":     "微信",
		"icon":     "SiWechat",
		"type":     "wxpay",
		"provider": "epay",
		"flow":     "form_post",
	},
	{
		"name":      "自定义1",
		"icon":      "LuCreditCard",
		"type":      "custom1",
		"provider":  "epay",
		"flow":      "form_post",
		"min_topup": "50",
	},
}

func UpdatePayMethodsByJsonString(jsonString string) error {
	parsed, err := ParsePayMethodsByJsonString(jsonString)
	if err != nil {
		return err
	}
	PayMethods = parsed
	return nil
}

func ParsePayMethodsByJsonString(jsonString string) ([]map[string]string, error) {
	var parsed []map[string]string
	if err := common.UnmarshalJsonStr(jsonString, &parsed); err != nil {
		return nil, err
	}
	if len(parsed) > 20 {
		return nil, fmt.Errorf("too many payment methods")
	}
	seenMethods := make(map[string]struct{}, len(parsed))
	seenRoutes := make(map[string]struct{}, len(parsed))
	for _, method := range parsed {
		for key := range method {
			if _, ok := paymentMethodAllowedFields[key]; !ok {
				return nil, fmt.Errorf("unsupported payment method field: %s", key)
			}
		}
		provider := strings.ToLower(strings.TrimSpace(method["provider"]))
		if provider == "" {
			provider = "epay"
		}
		methodType := normalizeConfiguredPaymentMethod(provider, method["type"])
		if !paymentMethodTypePattern.MatchString(methodType) {
			return nil, fmt.Errorf("payment method type is required")
		}
		if name := strings.TrimSpace(method["name"]); name == "" || len(name) > 128 {
			return nil, fmt.Errorf("invalid payment method name: %s", methodType)
		}
		if len(method["icon"]) > 64 || len(method["color"]) > 64 {
			return nil, fmt.Errorf("invalid payment method metadata: %s", methodType)
		}
		flow := ""
		switch provider {
		case "epay":
			switch methodType {
			case "stripe", "xorpay_native", "xorpay_alipay", "xorpay_jsapi", "waffo_pancake":
				return nil, fmt.Errorf("epay payment method uses reserved type: %s", methodType)
			}
			flow = "form_post"
		case "stripe":
			if methodType != "stripe" {
				return nil, fmt.Errorf("stripe payment method must use type stripe")
			}
			flow = "hosted_redirect"
		case "xorpay":
			if methodType != "xorpay_native" && methodType != "xorpay_alipay" && methodType != "xorpay_jsapi" {
				return nil, fmt.Errorf("unsupported XORPay payment method: %s", methodType)
			}
			if methodType == "xorpay_jsapi" {
				flow = "jsapi"
			} else {
				flow = "qr"
			}
		case "waffo_pancake":
			if methodType != "waffo_pancake" {
				return nil, fmt.Errorf("Waffo Pancake payment method must use type waffo_pancake")
			}
			flow = "hosted_redirect"
		default:
			return nil, fmt.Errorf("unsupported payment provider: %s", provider)
		}
		identityKey := paymentMethodIdentityKey(provider, methodType)
		if _, ok := seenMethods[identityKey]; ok {
			return nil, fmt.Errorf("duplicate payment method: %s/%s", provider, methodType)
		}
		seenMethods[identityKey] = struct{}{}

		routeID := strings.ToLower(strings.TrimSpace(method["route_id"]))
		if routeID == "" {
			routeID = PublicPaymentRouteID(provider, methodType)
		}
		if !publicPaymentAliasPattern.MatchString(routeID) || ContainsInternalPaymentProviderName(routeID) {
			return nil, fmt.Errorf("invalid public payment route id: %s", routeID)
		}
		if _, ok := seenRoutes[routeID]; ok {
			return nil, fmt.Errorf("duplicate public payment route id: %s", routeID)
		}
		seenRoutes[routeID] = struct{}{}

		publicMethod := strings.ToLower(strings.TrimSpace(method["public_method"]))
		if publicMethod == "" {
			publicMethod = DefaultPublicPaymentMethod(provider, methodType)
		}
		if !publicPaymentAliasPattern.MatchString(publicMethod) || ContainsInternalPaymentProviderName(publicMethod) {
			return nil, fmt.Errorf("invalid public payment method alias: %s", publicMethod)
		}
		channelAlias := strings.ToLower(strings.TrimSpace(method["channel_alias"]))
		if channelAlias == "" {
			channelAlias = DefaultPaymentChannelAlias(provider, methodType)
		}
		if !publicPaymentAliasPattern.MatchString(channelAlias) || ContainsInternalPaymentProviderName(channelAlias) {
			return nil, fmt.Errorf("invalid public payment channel alias: %s", channelAlias)
		}

		method["provider"] = provider
		method["type"] = methodType
		method["flow"] = flow
		method["route_id"] = routeID
		method["public_method"] = publicMethod
		method["channel_alias"] = channelAlias
		if rawMin := strings.TrimSpace(method["min_topup"]); rawMin != "" {
			minTopUp, err := strconv.Atoi(rawMin)
			if err != nil || minTopUp < 1 || minTopUp > maxConfiguredPaymentMethodMinimum {
				return nil, fmt.Errorf("invalid minimum top-up for payment method %s", methodType)
			}
			method["min_topup"] = strconv.Itoa(minTopUp)
		}
	}
	return parsed, nil
}

// PublicPaymentRouteID returns a deterministic opaque identifier for a
// provider/method pair. It is safe to expose to users because it does not
// contain the internal provider or gateway method name.
func PublicPaymentRouteID(provider, paymentMethod string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "epay"
	}
	paymentMethod = normalizeConfiguredPaymentMethod(provider, paymentMethod)
	digest := sha256.Sum256([]byte(provider + "\x00" + paymentMethod))
	return "pay_" + hex.EncodeToString(digest[:12])
}

func DefaultPublicPaymentMethod(provider, paymentMethod string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = strings.ToLower(strings.TrimSpace(paymentMethod))
	switch {
	case provider == "xorpay" && paymentMethod == "xorpay_alipay":
		return "alipay"
	case provider == "xorpay" && paymentMethod == "xorpay_native":
		return "wechat_pay"
	case provider == "xorpay" && paymentMethod == "xorpay_jsapi":
		return "wechat_pay"
	case provider == "epay" && paymentMethod == "alipay":
		return "alipay"
	case provider == "epay" && (paymentMethod == "wxpay" || paymentMethod == "wechat" || paymentMethod == "wechat_pay"):
		return "wechat_pay"
	case provider == "stripe":
		return "card"
	case provider == "waffo_pancake":
		return "online_payment"
	default:
		return "payment"
	}
}

func DefaultPaymentChannelAlias(provider, paymentMethod string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = strings.ToLower(strings.TrimSpace(paymentMethod))
	switch provider {
	case "xorpay":
		if paymentMethod == "xorpay_jsapi" {
			return "wechat_browser"
		}
		if paymentMethod == "xorpay_native" || paymentMethod == "xorpay_alipay" {
			return "qr"
		}
	case "stripe":
		return "checkout"
	case "waffo_pancake":
		return "hosted_checkout"
	case "epay":
		return "redirect"
	}
	return "payment"
}

func normalizeConfiguredPaymentMethod(provider, paymentMethod string) string {
	paymentMethod = strings.TrimSpace(paymentMethod)
	if provider == "epay" {
		return paymentMethod
	}
	return strings.ToLower(paymentMethod)
}

func paymentMethodIdentityKey(provider, paymentMethod string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider + "\x00" + normalizeConfiguredPaymentMethod(provider, paymentMethod)
}

// ContainsInternalPaymentProviderName detects provider identifiers even when
// punctuation or separators are inserted to make them look user-facing. Epay
// keeps token-boundary handling because ordinary words such as "prepayment"
// contain the same four consecutive letters and must remain valid public copy.
func ContainsInternalPaymentProviderName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}

	var compact strings.Builder
	var tokens []string
	var token strings.Builder
	flushToken := func() {
		if token.Len() == 0 {
			return
		}
		tokens = append(tokens, token.String())
		token.Reset()
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			compact.WriteRune(character)
			token.WriteRune(character)
			continue
		}
		flushToken()
	}
	flushToken()

	compactValue := compact.String()
	for _, internalName := range []string{"xorpay", "stripe", "waffo", "creem"} {
		if strings.Contains(compactValue, internalName) {
			return true
		}
	}
	for start := range tokens {
		candidate := ""
		for index := start; index < len(tokens) && index < start+4; index++ {
			candidate += tokens[index]
			if candidate == "epay" {
				return true
			}
			if !strings.HasPrefix("epay", candidate) {
				break
			}
		}
	}
	return false
}

func PayMethods2JsonString() string {
	jsonBytes, err := common.Marshal(PayMethods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}

func ContainsPayMethod(method string) bool {
	for _, payMethod := range PayMethods {
		provider := payMethod["provider"]
		if provider == "" {
			provider = "epay"
		}
		if provider == "epay" && payMethod["type"] == method {
			return true
		}
	}
	return false
}
