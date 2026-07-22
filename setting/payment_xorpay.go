package setting

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	XorPayMethodNative = "native"
	XorPayMethodAlipay = "alipay"
	XorPayMethodJSAPI  = "jsapi"
)

var XorPayAid = ""
var XorPayAppSecret = ""
var XorPayCredentialGeneration int64 = 1
var XorPayAidPrevious = ""
var XorPayAppSecretPrevious = ""
var XorPayPreviousCredentialGeneration int64
var XorPayPreviousValidBefore int64
var XorPayPreviousExpiresAt int64
var XorPayUnitPrice = 7.3
var XorPayMinTopUp = 1
var XorPayCurrency = "CNY"
var XorPayEnabledMethods = []string{XorPayMethodNative, XorPayMethodAlipay}

func XorPayPreviousCredentialActive() bool {
	return XorPayPreviousCredentialGeneration > 0 &&
		XorPayPreviousExpiresAt > time.Now().Unix() &&
		XorPayAidPrevious != "" && XorPayAppSecretPrevious != ""
}

func XorPayEnabledMethods2JsonString() string {
	data, err := common.Marshal(XorPayEnabledMethods)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func UpdateXorPayEnabledMethodsByJsonString(value string) error {
	methods, err := ParseXorPayEnabledMethods(value)
	if err != nil {
		return err
	}
	XorPayEnabledMethods = methods
	return nil
}

// ParseXorPayEnabledMethods validates a stored provider capability snapshot
// without mutating process-global settings. Startup compatibility migrations
// use it before committing any catalog changes.
func ParseXorPayEnabledMethods(value string) ([]string, error) {
	var methods []string
	if err := common.UnmarshalJsonStr(value, &methods); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(methods))
	normalized := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToLower(strings.TrimSpace(method))
		if method != XorPayMethodNative && method != XorPayMethodAlipay && method != XorPayMethodJSAPI {
			return nil, fmt.Errorf("unsupported XORPay method: %s", method)
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		normalized = append(normalized, method)
	}
	return normalized, nil
}

func IsXorPayMethodEnabled(method string) bool {
	method = strings.ToLower(strings.TrimSpace(method))
	for _, enabled := range XorPayEnabledMethods {
		if enabled == method {
			return true
		}
	}
	return false
}
