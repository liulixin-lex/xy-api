package service

import (
	"errors"
	"net"
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
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if parsed.Scheme == "http" {
		if allowLocalHTTP && isLocalDevelopmentHost(host) {
			return nil
		}
		return errors.New("payment callback address must use HTTPS")
	}
	if parsed.Scheme != "https" {
		return errors.New("payment callback address must use HTTPS")
	}
	if isLocalDevelopmentHost(host) {
		return errors.New("payment callback address must use a public HTTPS origin")
	}
	ipHost := host
	if zoneIndex := strings.LastIndexByte(ipHost, '%'); zoneIndex >= 0 {
		ipHost = ipHost[:zoneIndex]
	}
	if ip := net.ParseIP(ipHost); ip != nil {
		if common.IsPrivateIP(ip) {
			return errors.New("payment callback address must use a public HTTPS origin")
		}
	} else if isAmbiguousIPv4Literal(ipHost) {
		return errors.New("payment callback address must use a canonical public hostname")
	}
	return nil
}

// Some URL clients interpret decimal, octal, hexadecimal, or shortened IPv4
// text as an address even though net.ParseIP treats it as a hostname. Reject
// these ambiguous forms so a callback cannot appear public here and resolve to
// loopback or private space at the provider.
func isAmbiguousIPv4Literal(host string) bool {
	parts := strings.Split(strings.TrimSpace(host), ".")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		digits := part
		base := byte(10)
		if len(digits) > 2 && digits[0] == '0' && (digits[1] == 'x' || digits[1] == 'X') {
			digits = digits[2:]
			base = 16
		}
		if digits == "" {
			return false
		}
		for index := 0; index < len(digits); index++ {
			character := digits[index]
			if character >= '0' && character <= '9' {
				continue
			}
			if base == 16 && (character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
				continue
			}
			return false
		}
	}
	return true
}

func PaymentReturnURL(suffix string) string {
	base := strings.TrimRight(operation_setting.CustomCallbackAddress, "/")
	if base == "" {
		base = strings.TrimRight(system_setting.ServerAddress, "/")
	}
	return base + common.ThemeAwarePath(suffix)
}
