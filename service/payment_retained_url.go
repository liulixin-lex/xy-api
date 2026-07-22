package service

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/setting"
)

const maxRetainedPaymentURLBytes = 4096

const (
	defaultWaffoWebRedirectHost = "cashier.waffo.com"
	maxWaffoRedirectAllowlist   = 32
)

var (
	waffoPancakeJWTFragmentPattern = regexp.MustCompile(`^token=[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	waffoRedirectHostPattern       = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)
	waffoRedirectSchemePattern     = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,31}$`)
)

var forbiddenWaffoAppRedirectSchemes = map[string]struct{}{
	"about":      {},
	"blob":       {},
	"data":       {},
	"file":       {},
	"ftp":        {},
	"http":       {},
	"https":      {},
	"javascript": {},
	"vbscript":   {},
}

// ValidateCreemCheckoutURL accepts only Creem's exact HTTPS checkout host.
// Provider-owned path, query, and fragment data are preserved verbatim.
func ValidateCreemCheckoutURL(raw string) error {
	return validateExactHTTPSPaymentURL(raw, "checkout.creem.io")
}

// ParseWaffoWebRedirectHosts parses the operator-managed, exact-match host
// allowlist used in addition to cashier.waffo.com. Entries are separated by
// commas, whitespace, or newlines and must be bare DNS hostnames.
func ParseWaffoWebRedirectHosts(raw string) ([]string, error) {
	return parseWaffoRedirectAllowlist(raw, validateWaffoRedirectHost)
}

// ParseWaffoAppRedirectSchemes parses the independently managed allowlist for
// APP-wallet deep links. Web and browser-executable schemes are forbidden even
// when an operator attempts to add them.
func ParseWaffoAppRedirectSchemes(raw string) ([]string, error) {
	return parseWaffoRedirectAllowlist(raw, validateWaffoAppRedirectScheme)
}

// ValidateWaffoWebPaymentURL accepts Waffo's built-in HTTPS cashier and exact
// hosts explicitly trusted by the operator. Provider query parameters and
// fragments are part of the signed upstream response and must be preserved.
func ValidateWaffoWebPaymentURL(raw string, allowedHosts []string) error {
	parsed, err := parseRetainedPaymentURL(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("invalid Waffo web payment URL")
	}
	if parsed.Port() != "" || strings.Contains(parsed.Host, "%") {
		return errors.New("invalid Waffo web payment URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if err := validateWaffoRedirectHost(host); err != nil {
		return errors.New("invalid Waffo web payment URL")
	}
	if host == defaultWaffoWebRedirectHost {
		return nil
	}
	for _, candidate := range allowedHosts {
		if host == strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), ".")) {
			return nil
		}
	}
	return errors.New("unexpected Waffo web payment host")
}

// ValidateWaffoAppPaymentURL accepts a custom-scheme deep link only when that
// scheme was enabled independently by the operator. It intentionally does not
// reuse the HTTPS-host validation path.
func ValidateWaffoAppPaymentURL(raw string, allowedSchemes []string) error {
	parsed, err := parseRetainedPaymentURL(raw)
	if err != nil || parsed.Scheme == "" || parsed.User != nil {
		return errors.New("invalid Waffo app payment URL")
	}
	if parsed.Opaque == "" && parsed.Host == "" && parsed.Path == "" {
		return errors.New("invalid Waffo app payment URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if err := validateWaffoAppRedirectScheme(scheme); err != nil {
		return errors.New("invalid Waffo app payment URL")
	}
	for _, candidate := range allowedSchemes {
		if scheme == strings.ToLower(strings.TrimSpace(candidate)) {
			return nil
		}
	}
	return errors.New("unexpected Waffo app payment scheme")
}

// ValidateConfiguredWaffoWebPaymentURL re-applies the current operator
// allowlist at the browser continuation boundary. A URL that was valid when a
// worker created the order is not allowed to bypass a later emergency config
// change.
func ValidateConfiguredWaffoWebPaymentURL(raw string) error {
	unlock := setting.LockPaymentConfigurationForRead()
	allowlist := setting.WaffoWebRedirectHosts
	unlock()
	hosts, err := ParseWaffoWebRedirectHosts(allowlist)
	if err != nil {
		return err
	}
	return ValidateWaffoWebPaymentURL(raw, hosts)
}

// ValidateConfiguredWaffoAppPaymentURL is intentionally fail-closed: custom
// schemes are accepted only when they remain explicitly configured.
func ValidateConfiguredWaffoAppPaymentURL(raw string) error {
	unlock := setting.LockPaymentConfigurationForRead()
	allowlist := setting.WaffoAppRedirectSchemes
	unlock()
	schemes, err := ParseWaffoAppRedirectSchemes(allowlist)
	if err != nil {
		return err
	}
	return ValidateWaffoAppPaymentURL(raw, schemes)
}

// ValidateWaffoPancakeCheckoutURL accepts the authenticated checkout shape
// produced by Waffo's official Pancake SDK. The JWT remains inside the single
// #token=<JWT> fragment and is never exposed as a separate response field.
func ValidateWaffoPancakeCheckoutURL(raw string) error {
	if err := validateExactHTTPSPaymentURL(raw, "pancake.waffo.ai"); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, "#") != 1 {
		return errors.New("invalid Waffo Pancake checkout fragment")
	}
	fragment := raw[strings.IndexByte(raw, '#')+1:]
	if strings.Contains(fragment, "%") || !waffoPancakeJWTFragmentPattern.MatchString(fragment) {
		return errors.New("invalid Waffo Pancake checkout fragment")
	}
	return nil
}

func validateExactHTTPSPaymentURL(raw string, expectedHost string) error {
	parsed, err := parseRetainedPaymentURL(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("invalid hosted payment URL")
	}
	if parsed.Port() != "" || strings.Contains(parsed.Host, "%") {
		return errors.New("invalid hosted payment URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	expectedHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(expectedHost), "."))
	if host != expectedHost {
		return errors.New("unexpected hosted payment host")
	}
	return nil
}

func parseRetainedPaymentURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxRetainedPaymentURLBytes || strings.ContainsAny(raw, "\r\n\\") {
		return nil, errors.New("invalid payment URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid payment URL")
	}
	return parsed, nil
}

func parseWaffoRedirectAllowlist(raw string, validate func(string) error) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	entries := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(entries) > maxWaffoRedirectAllowlist {
		return nil, errors.New("Waffo redirect allowlist is too large")
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
		if err := validate(entry); err != nil {
			return nil, err
		}
		seen[entry] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for entry := range seen {
		result = append(result, entry)
	}
	sort.Strings(result)
	return result, nil
}

func validateWaffoRedirectHost(host string) error {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || !strings.Contains(host, ".") || !waffoRedirectHostPattern.MatchString(host) {
		return errors.New("invalid Waffo web redirect host")
	}
	return nil
}

func validateWaffoAppRedirectScheme(scheme string) error {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if !waffoRedirectSchemePattern.MatchString(scheme) {
		return errors.New("invalid Waffo app redirect scheme")
	}
	if _, forbidden := forbiddenWaffoAppRedirectSchemes[scheme]; forbidden {
		return errors.New("unsafe Waffo app redirect scheme")
	}
	return nil
}
