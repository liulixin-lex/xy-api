package service

import (
	"errors"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func ValidatePaymentCallbackOrigin(raw string, allowLocalHTTP bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return errors.New("payment callback address is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("payment callback address is invalid")
	}
	if escapedPath := parsed.EscapedPath(); escapedPath != "" && escapedPath != "/" {
		return errors.New("payment callback address must be an origin without a path")
	}
	if parsed.Scheme != "https" && !(allowLocalHTTP && parsed.Scheme == "http" && isLocalDevelopmentHost(parsed.Hostname())) {
		return errors.New("payment callback address must use HTTPS")
	}
	return nil
}

func PaymentReturnURL(suffix string) string {
	base := strings.TrimRight(operation_setting.CustomCallbackAddress, "/")
	if base == "" {
		base = strings.TrimRight(system_setting.ServerAddress, "/")
	}
	return base + common.ThemeAwarePath(suffix)
}
