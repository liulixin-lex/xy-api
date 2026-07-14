package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	routingCostConnectTimeout        = 5 * time.Second
	routingCostTLSHandshakeTimeout   = 5 * time.Second
	routingCostResponseHeaderTimeout = 10 * time.Second
	routingCostOverallTimeout        = 15 * time.Second
	routingCostIdleConnTimeout       = 90 * time.Second
	routingCostMaxRedirects          = 3
	routingCostTransportCacheMax     = 64
	routingCostTransportCacheTTL     = 30 * time.Minute
	routingCostMaxAllowedCIDRs       = 32
	routingCostCustomCAMaxBytes      = 96 << 10
	routingCostCustomCAMaxCerts      = 64
)

type routingCostResolver interface {
	LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error)
}

type routingCostHTTPClientOptions struct {
	resolver    routingCostResolver
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
	rootCAs     *x509.CertPool
	egress      routingCostEgressContext
}

type routingCostDialer struct {
	resolver    routingCostResolver
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
	egress      routingCostEgressContext
}

type routingCostRoundTripper struct {
	transport *http.Transport
	egress    routingCostEgressContext
}

type routingCostEgressContext struct {
	cacheKey            string
	allowedPrivateCIDRs []netip.Prefix
	rootCAs             *x509.CertPool
}

type routingCostEgressContextKey struct{}

type routingCostTransportCacheEntry struct {
	roundTripper *routingCostRoundTripper
	lastUsed     time.Time
}

type routingCostTransportCache struct {
	mu      sync.Mutex
	entries map[string]routingCostTransportCacheEntry
	now     func() time.Time
}

type routingCostPolicyRoundTripper struct {
	cache *routingCostTransportCache
}

var routingCostNetworkProtection = common.SSRFProtection{
	AllowPrivateIp:         false,
	DomainFilterMode:       false,
	IpFilterMode:           false,
	ApplyIPFilterForDomain: true,
}

var routingCostRejectedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.88.99.2/32"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

var routingCostAllowedPrivatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

var routingCostHTTPClient = newRoutingCostPolicyHTTPClient()

// GetRoutingCostHTTPClient returns the shared, fail-closed client used only for
// smart-routing cost and balance synchronization.
func GetRoutingCostHTTPClient() *http.Client {
	return routingCostHTTPClient
}

// ValidateRoutingCostURL validates the URL-level routing cost egress policy.
// DNS results are validated and pinned immediately before dialing.
func ValidateRoutingCostURL(rawURL string) error {
	return ValidateRoutingCostURLWithEgressPolicy(rawURL, nil)
}

func ValidateRoutingCostURLWithEgressPolicy(rawURL string, allowedPrivateCIDRs []string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid routing cost URL: %w", err)
	}
	_, prefixes, err := normalizeRoutingCostEgressCIDRs(allowedPrivateCIDRs)
	if err != nil {
		return err
	}
	return validateRoutingCostURLWithEgress(parsed, routingCostEgressContext{allowedPrivateCIDRs: prefixes})
}

// WithRoutingCostEgressPolicy validates and attaches the explicit exceptions
// for one cost-source request. Private access remains denied unless every
// resolved address is inside one of the configured private CIDRs. Custom CAs
// extend, rather than replace, the system trust store.
func WithRoutingCostEgressPolicy(
	ctx context.Context,
	allowedPrivateCIDRs []string,
	customCAPEM string,
) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, prefixes, err := normalizeRoutingCostEgressCIDRs(allowedPrivateCIDRs)
	if err != nil {
		return nil, err
	}
	customCAPEM = strings.TrimSpace(customCAPEM)
	if !utf8.ValidString(customCAPEM) || len(customCAPEM) > routingCostCustomCAMaxBytes {
		return nil, errors.New("invalid routing cost custom CA")
	}
	caDigest := sha256.Sum256([]byte(customCAPEM))
	keyPayload, err := common.Marshal(struct {
		AllowedPrivateCIDRs []string `json:"allowed_private_cidrs"`
		CustomCAHash        string   `json:"custom_ca_hash"`
	}{AllowedPrivateCIDRs: normalized, CustomCAHash: fmt.Sprintf("%x", caDigest)})
	if err != nil {
		return nil, err
	}
	keyDigest := sha256.Sum256(keyPayload)
	cacheKey := fmt.Sprintf("%x", keyDigest)
	if current := routingCostEgressFromContext(ctx); current.cacheKey == cacheKey {
		return ctx, nil
	}
	rootCAs, err := routingCostCustomCARoots(customCAPEM)
	if err != nil {
		return nil, err
	}
	egress := routingCostEgressContext{
		cacheKey: cacheKey, allowedPrivateCIDRs: prefixes, rootCAs: rootCAs,
	}
	return context.WithValue(ctx, routingCostEgressContextKey{}, egress), nil
}

func routingCostCustomCARoots(customCAPEM string) (*x509.CertPool, error) {
	if customCAPEM == "" {
		return nil, nil
	}
	remaining := []byte(customCAPEM)
	certificates := make([]*x509.Certificate, 0, 1)
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("invalid routing cost custom CA")
		}
		block, rest := pem.Decode(remaining)
		if block == nil {
			return nil, errors.New("invalid routing cost custom CA")
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("invalid routing cost custom CA")
		}
		parsed, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(parsed) == 0 || len(certificates)+len(parsed) > routingCostCustomCAMaxCerts {
			return nil, errors.New("invalid routing cost custom CA")
		}
		certificates = append(certificates, parsed...)
		remaining = bytes.TrimSpace(rest)
	}
	if len(certificates) == 0 {
		return nil, errors.New("invalid routing cost custom CA")
	}
	rootCAs, err := x509.SystemCertPool()
	if err != nil || rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	for _, certificate := range certificates {
		rootCAs.AddCert(certificate)
	}
	return rootCAs, nil
}

// NormalizeRoutingCostEgressCIDRs returns the canonical CIDR list accepted by
// the explicit routing-cost egress policy.
func NormalizeRoutingCostEgressCIDRs(values []string) ([]string, error) {
	normalized, _, err := normalizeRoutingCostEgressCIDRs(values)
	return normalized, err
}

func newRoutingCostPolicyHTTPClient() *http.Client {
	return &http.Client{
		Transport: &routingCostPolicyRoundTripper{cache: &routingCostTransportCache{
			entries: make(map[string]routingCostTransportCacheEntry), now: time.Now,
		}},
		CheckRedirect: checkRoutingCostRedirect,
		Timeout:       routingCostOverallTimeout,
	}
}

func newRoutingCostHTTPClient(options routingCostHTTPClientOptions) *http.Client {
	roundTripper := newRoutingCostRoundTripper(options)
	return &http.Client{
		Transport:     roundTripper,
		CheckRedirect: checkRoutingCostRedirect,
		Timeout:       routingCostOverallTimeout,
	}
}

func newRoutingCostRoundTripper(options routingCostHTTPClientOptions) *routingCostRoundTripper {
	if options.resolver == nil {
		options.resolver = net.DefaultResolver
	}
	if options.dialContext == nil {
		netDialer := &net.Dialer{
			Timeout:   routingCostConnectTimeout,
			KeepAlive: 30 * time.Second,
		}
		options.dialContext = netDialer.DialContext
	}

	dialer := &routingCostDialer{
		resolver:    options.resolver,
		dialContext: options.dialContext,
		egress:      options.egress,
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       routingCostIdleConnTimeout,
		TLSHandshakeTimeout:   routingCostTLSHandshakeTimeout,
		ResponseHeaderTimeout: routingCostResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    options.rootCAs,
		},
	}
	return &routingCostRoundTripper{transport: transport, egress: options.egress}
}

func (t *routingCostPolicyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.cache == nil || request == nil {
		return nil, errors.New("invalid routing cost request")
	}
	egress := routingCostEgressFromContext(request.Context())
	roundTripper, err := t.cache.get(egress)
	if err != nil {
		return nil, err
	}
	return roundTripper.RoundTrip(request)
}

func (t *routingCostPolicyRoundTripper) CloseIdleConnections() {
	if t == nil || t.cache == nil {
		return
	}
	t.cache.closeIdleConnections()
}

func (t *routingCostRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("invalid routing cost request")
	}
	if err := validateRoutingCostURLWithEgress(request.URL, t.egress); err != nil {
		return nil, err
	}
	response, err := t.transport.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	switch response.StatusCode {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		location := response.Header.Get("Location")
		if location != "" {
			if _, parseErr := request.URL.Parse(location); parseErr != nil {
				response.Header.Del("Location")
			}
		}
	}
	return response, nil
}

func (t *routingCostRoundTripper) CloseIdleConnections() {
	t.transport.CloseIdleConnections()
}

func (cache *routingCostTransportCache) get(egress routingCostEgressContext) (*routingCostRoundTripper, error) {
	if cache == nil {
		return nil, errors.New("routing cost transport cache unavailable")
	}
	if egress.cacheKey == "" {
		defaultDigest := sha256.Sum256([]byte("routing-cost-egress-default"))
		egress.cacheKey = fmt.Sprintf("%x", defaultDigest)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if cache.now != nil {
		now = cache.now()
	}
	for key, entry := range cache.entries {
		if now.Sub(entry.lastUsed) <= routingCostTransportCacheTTL {
			continue
		}
		entry.roundTripper.CloseIdleConnections()
		delete(cache.entries, key)
	}
	if entry, exists := cache.entries[egress.cacheKey]; exists {
		entry.lastUsed = now
		cache.entries[egress.cacheKey] = entry
		return entry.roundTripper, nil
	}
	if len(cache.entries) >= routingCostTransportCacheMax {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range cache.entries {
			if oldestKey == "" || entry.lastUsed.Before(oldest) || entry.lastUsed.Equal(oldest) && key < oldestKey {
				oldestKey = key
				oldest = entry.lastUsed
			}
		}
		if oldestKey != "" {
			cache.entries[oldestKey].roundTripper.CloseIdleConnections()
			delete(cache.entries, oldestKey)
		}
	}
	roundTripper := newRoutingCostRoundTripper(routingCostHTTPClientOptions{
		rootCAs: egress.rootCAs, egress: egress,
	})
	cache.entries[egress.cacheKey] = routingCostTransportCacheEntry{roundTripper: roundTripper, lastUsed: now}
	return roundTripper, nil
}

func (cache *routingCostTransportCache) closeIdleConnections() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, entry := range cache.entries {
		entry.roundTripper.CloseIdleConnections()
	}
}

func (d *routingCostDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid routing cost dial address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid routing cost port %q", port)
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		if err = validateRoutingCostIPWithEgress(host, ip, d.egress); err != nil {
			return nil, err
		}
		return d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	if err = routingCostNetworkProtection.ValidateNetworkTarget(host, portNumber); err != nil {
		return nil, errors.New("routing cost target rejected")
	}

	resolved, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("routing cost DNS resolution failed")
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("routing cost DNS resolution for %s returned no addresses", host)
	}

	verified := make([]netip.Addr, 0, len(resolved))
	for _, address := range resolved {
		if !address.IsValid() || address.Zone() != "" {
			return nil, errors.New("routing cost DNS resolution returned an invalid address")
		}
		address = address.Unmap()
		if err = validateRoutingCostIPWithEgress(host, address, d.egress); err != nil {
			return nil, err
		}
		verified = append(verified, address)
	}

	for _, ip := range verified {
		if !routingCostNetworkAllowsIP(network, ip) {
			continue
		}
		connection, dialErr := d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
	}
	return nil, errors.New("routing cost connection failed")
}

func validateRoutingCostURL(parsed *url.URL) error {
	return validateRoutingCostURLWithEgress(parsed, routingCostEgressContext{})
}

func validateRoutingCostURLWithEgress(parsed *url.URL, egress routingCostEgressContext) error {
	if parsed == nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("invalid routing cost URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("routing cost URL must use https")
	}
	if parsed.User != nil {
		return fmt.Errorf("routing cost URL must not contain userinfo")
	}

	host := parsed.Hostname()
	normalizedHost := strings.TrimSuffix(strings.ToLower(host), ".")
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return fmt.Errorf("routing cost target rejected: localhost is not allowed")
	}
	if strings.Contains(host, "%") {
		return fmt.Errorf("routing cost target rejected: scoped addresses are not allowed")
	}

	port := 443
	if parsed.Port() != "" {
		parsedPort, err := strconv.Atoi(parsed.Port())
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return fmt.Errorf("invalid routing cost port %q", parsed.Port())
		}
		port = parsedPort
	} else if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("invalid routing cost port")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return validateRoutingCostIPWithEgress(host, ip, egress)
	}
	if err := routingCostNetworkProtection.ValidateNetworkTarget(host, port); err != nil {
		return errors.New("routing cost target rejected")
	}
	return nil
}

func validateRoutingCostIP(host string, ip netip.Addr) error {
	return validateRoutingCostIPWithEgress(host, ip, routingCostEgressContext{})
}

func validateRoutingCostIPWithEgress(host string, ip netip.Addr, egress routingCostEgressContext) error {
	if !ip.IsValid() || ip.Is4In6() {
		return errors.New("routing cost target rejected")
	}
	for _, prefix := range routingCostRejectedPrefixes {
		if prefix.Contains(ip) {
			return errors.New("routing cost target rejected")
		}
	}
	if err := routingCostNetworkProtection.ValidateResolvedIP(host, net.IP(ip.AsSlice())); err == nil {
		return nil
	}
	if !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return errors.New("routing cost target rejected")
	}
	for _, prefix := range egress.allowedPrivateCIDRs {
		if prefix.Contains(ip) {
			return nil
		}
	}
	return errors.New("routing cost target rejected")
}

func routingCostNetworkAllowsIP(network string, ip netip.Addr) bool {
	switch network {
	case "tcp4":
		return ip.Is4()
	case "tcp6":
		return ip.Is6()
	default:
		return true
	}
}

func checkRoutingCostRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) == 0 {
		return rejectRoutingCostRedirect(request)
	}
	if len(via) > routingCostMaxRedirects {
		return rejectRoutingCostRedirect(request)
	}

	original := via[0]
	if !routingCostRedirectMethodAllowed(original.Method) || routingCostRequestHasBody(original) {
		return rejectRoutingCostRedirect(request)
	}
	if !routingCostRedirectMethodAllowed(request.Method) || routingCostRequestHasBody(request) {
		return rejectRoutingCostRedirect(request)
	}
	if !strings.EqualFold(request.URL.Scheme, original.URL.Scheme) || !strings.EqualFold(request.URL.Host, original.URL.Host) {
		return rejectRoutingCostRedirect(request)
	}
	if err := validateRoutingCostURLWithEgress(request.URL, routingCostEgressFromContext(request.Context())); err != nil {
		return rejectRoutingCostRedirect(request)
	}

	for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "New-Api-User"} {
		request.Header.Del(header)
	}
	return nil
}

func rejectRoutingCostRedirect(request *http.Request) error {
	if request != nil {
		if request.Response != nil {
			request.Response.Header.Del("Location")
		}
		request.URL = &url.URL{Scheme: "https", Host: "redacted.invalid"}
		request.Host = ""
		request.RequestURI = ""
		request.Header = make(http.Header)
	}
	return http.ErrUseLastResponse
}

func routingCostRedirectMethodAllowed(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func routingCostRequestHasBody(request *http.Request) bool {
	if request == nil {
		return true
	}
	if request.Body != nil && request.Body != http.NoBody {
		return true
	}
	return request.GetBody != nil || request.ContentLength > 0
}

func routingCostEgressFromContext(ctx context.Context) routingCostEgressContext {
	if ctx == nil {
		return routingCostEgressContext{}
	}
	egress, _ := ctx.Value(routingCostEgressContextKey{}).(routingCostEgressContext)
	return egress
}

func normalizeRoutingCostEgressCIDRs(values []string) ([]string, []netip.Prefix, error) {
	if len(values) > routingCostMaxAllowedCIDRs {
		return nil, nil, errors.New("too many routing cost egress CIDRs")
	}
	unique := make(map[string]netip.Prefix, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" || len(raw) > 64 {
			return nil, nil, errors.New("invalid routing cost egress CIDR")
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().IsValid() || prefix.Addr().Is4In6() {
			return nil, nil, errors.New("invalid routing cost egress CIDR")
		}
		prefix = prefix.Masked()
		address := prefix.Addr()
		privateRange := false
		for _, allowed := range routingCostAllowedPrivatePrefixes {
			if prefix.Bits() >= allowed.Bits() && allowed.Contains(address) {
				privateRange = true
				break
			}
		}
		if !privateRange || address.IsLoopback() || address.IsLinkLocalUnicast() ||
			address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return nil, nil, errors.New("routing cost egress CIDR must be private")
		}
		unique[prefix.String()] = prefix
	}
	normalized := make([]string, 0, len(unique))
	for value := range unique {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	prefixes := make([]netip.Prefix, 0, len(normalized))
	for _, value := range normalized {
		prefixes = append(prefixes, unique[value])
	}
	return normalized, prefixes, nil
}
