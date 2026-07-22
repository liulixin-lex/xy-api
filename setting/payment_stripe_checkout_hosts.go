package setting

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

const (
	maxStripeCheckoutAllowedHostsBytes = 4096
	maxStripeCheckoutAllowedHostCount  = 32
)

var ErrStripeCheckoutAllowedHostsInvalid = errors.New("invalid Stripe custom Checkout host allowlist")

// StripeCheckoutAllowedHosts contains administrator-approved exact DNS hosts
// for Stripe custom Checkout domains. Stripe-owned subdomains are trusted by
// the Checkout URL validator independently of this optional list.
var StripeCheckoutAllowedHosts = ""

// NormalizeStripeCheckoutAllowedHosts validates and canonicalizes an exact
// hostname allowlist. Entries may be separated by commas or newlines. The
// canonical value is lower-case, de-duplicated, sorted, and comma-separated so
// every application node derives the same configuration fingerprint.
func NormalizeStripeCheckoutAllowedHosts(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > maxStripeCheckoutAllowedHostsBytes {
		return "", fmt.Errorf("%w: list is too large", ErrStripeCheckoutAllowedHostsInvalid)
	}
	entries := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	if len(entries) == 0 || len(entries) > maxStripeCheckoutAllowedHostCount {
		return "", fmt.Errorf("%w: host count is outside the supported range", ErrStripeCheckoutAllowedHostsInvalid)
	}
	hosts := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSpace(entry))
		if !validStripeCheckoutAllowedHost(host) {
			return "", fmt.Errorf("%w: %q is not an exact DNS hostname", ErrStripeCheckoutAllowedHostsInvalid, entry)
		}
		hosts[host] = struct{}{}
	}
	if len(hosts) > maxStripeCheckoutAllowedHostCount {
		return "", fmt.Errorf("%w: too many hosts", ErrStripeCheckoutAllowedHostsInvalid)
	}
	canonical := make([]string, 0, len(hosts))
	for host := range hosts {
		canonical = append(canonical, host)
	}
	sort.Strings(canonical)
	return strings.Join(canonical, ","), nil
}

func StripeCheckoutAllowedHostSet(raw string) (map[string]struct{}, error) {
	canonical, err := NormalizeStripeCheckoutAllowedHosts(raw)
	if err != nil {
		return nil, err
	}
	hosts := make(map[string]struct{})
	if canonical == "" {
		return hosts, nil
	}
	for _, host := range strings.Split(canonical, ",") {
		hosts[host] = struct{}{}
	}
	return hosts, nil
}

func validStripeCheckoutAllowedHost(host string) bool {
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") ||
		strings.ContainsAny(host, "*/\\?#@:") || net.ParseIP(host) != nil ||
		host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	lastHasLetter := false
	for labelIndex, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if char >= 'a' && char <= 'z' {
				if labelIndex == len(labels)-1 {
					lastHasLetter = true
				}
				continue
			}
			if char >= '0' && char <= '9' || char == '-' {
				continue
			}
			return false
		}
	}
	return lastHasLetter
}
