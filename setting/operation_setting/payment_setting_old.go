/**
此文件为旧版支付设置文件，如需增加新的参数、变量等，请在 payment_setting.go 中添加
This file is the old version of the payment settings file. If you need to add new parameters, variables, etc., please add them in payment_setting.go
*/

package operation_setting

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

var paymentMethodTypePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var paymentMethodAllowedFields = map[string]struct{}{
	"name": {}, "icon": {}, "type": {}, "provider": {}, "flow": {}, "color": {}, "min_topup": {},
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
	seen := make(map[string]struct{}, len(parsed))
	for _, method := range parsed {
		for key := range method {
			if _, ok := paymentMethodAllowedFields[key]; !ok {
				return nil, fmt.Errorf("unsupported payment method field: %s", key)
			}
		}
		methodType := strings.TrimSpace(method["type"])
		if !paymentMethodTypePattern.MatchString(methodType) {
			return nil, fmt.Errorf("payment method type is required")
		}
		if name := strings.TrimSpace(method["name"]); name == "" || len(name) > 128 {
			return nil, fmt.Errorf("invalid payment method name: %s", methodType)
		}
		if len(method["icon"]) > 64 || len(method["color"]) > 64 {
			return nil, fmt.Errorf("invalid payment method metadata: %s", methodType)
		}
		if _, ok := seen[methodType]; ok {
			return nil, fmt.Errorf("duplicate payment method type: %s", methodType)
		}
		seen[methodType] = struct{}{}
		provider := strings.TrimSpace(method["provider"])
		if provider == "" {
			provider = "epay"
		}
		flow := ""
		switch provider {
		case "epay":
			switch methodType {
			case "stripe", "xorpay_native", "xorpay_alipay", "waffo_pancake":
				return nil, fmt.Errorf("epay payment method uses reserved type: %s", methodType)
			}
			flow = "form_post"
		case "stripe":
			if methodType != "stripe" {
				return nil, fmt.Errorf("stripe payment method must use type stripe")
			}
			flow = "hosted_redirect"
		case "xorpay":
			if methodType != "xorpay_native" && methodType != "xorpay_alipay" {
				return nil, fmt.Errorf("unsupported XORPay payment method: %s", methodType)
			}
			flow = "qr"
		case "waffo_pancake":
			if methodType != "waffo_pancake" {
				return nil, fmt.Errorf("Waffo Pancake payment method must use type waffo_pancake")
			}
			flow = "hosted_redirect"
		default:
			return nil, fmt.Errorf("unsupported payment provider: %s", provider)
		}
		method["provider"] = provider
		method["flow"] = flow
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
