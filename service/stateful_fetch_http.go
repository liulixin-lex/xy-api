package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"golang.org/x/net/proxy"
)

const (
	statefulFetchConnectTimeout          = 10 * time.Second
	statefulFetchTLSHandshakeTimeout     = 10 * time.Second
	statefulFetchResponseHeaderTimeout   = 30 * time.Second
	statefulFetchOverallTimeout          = 60 * time.Second
	statefulFetchIdleConnTimeout         = 90 * time.Second
	statefulFetchMaxRedirects            = 3
	statefulFetchMaxResponseHeaderBytes  = 1 << 20
	statefulFetchTransportCacheMax       = 64
	statefulFetchTransportCacheTTL       = 30 * time.Minute
	statefulFetchMaxURLBytes             = 16 << 10
	statefulFetchMaxProxyURLBytes        = 2 << 10
	statefulFetchMaxProxyCredentialBytes = 255
	statefulFetchMaxResolvedAddresses    = 8
)

type statefulFetchResolver interface {
	LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error)
}

type statefulFetchHTTPClientOptions struct {
	resolver      statefulFetchResolver
	dialContext   func(ctx context.Context, network, address string) (net.Conn, error)
	rootCAs       *x509.CertPool
	proxyURL      string
	cache         *statefulFetchTransportCache
	getProtection func() (*common.SSRFProtection, error)
}

type statefulFetchProxyConfig struct {
	scheme string
	url    *url.URL
	host   string
	port   string
}

type statefulFetchProxyRoute struct {
	config          statefulFetchProxyConfig
	pinnedAddress   string
	pinnedAddresses []string
	transportURL    *url.URL
}

type statefulFetchRoute struct {
	targetHost    string
	targetPort    string
	targetAddress string
	proxy         *statefulFetchProxyRoute
}

type statefulFetchRoundTripper struct {
	resolver      statefulFetchResolver
	dialContext   func(ctx context.Context, network, address string) (net.Conn, error)
	rootCAs       *x509.CertPool
	proxy         *statefulFetchProxyConfig
	cache         *statefulFetchTransportCache
	getProtection func() (*common.SSRFProtection, error)
}

type statefulFetchTransportCacheEntry struct {
	transport *http.Transport
	lastUsed  time.Time
}

type statefulFetchTransportCache struct {
	mu      sync.Mutex
	entries map[string]statefulFetchTransportCacheEntry
	now     func() time.Time
}

type statefulFetchContextDialer struct {
	addresses   []string
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

var sharedStatefulFetchTransportCache = &statefulFetchTransportCache{
	entries: make(map[string]statefulFetchTransportCacheEntry),
	now:     time.Now,
}

var statefulFetchPublicAddressProtection = common.SSRFProtection{
	AllowPrivateIp:         false,
	DomainFilterMode:       false,
	IpFilterMode:           false,
	ApplyIPFilterForDomain: true,
}

var statefulFetchRejectedPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.88.99.2/32"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
}

// GetStatefulFetchHTTPClient returns a strict client for stateful task polling
// and result media. Target DNS is revalidated for every request and the actual
// direct, CONNECT, or SOCKS destination is always a validated IP address.
func GetStatefulFetchHTTPClient(proxyURL string) (*http.Client, error) {
	return newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		proxyURL: proxyURL,
		cache:    sharedStatefulFetchTransportCache,
	})
}

// DoStatefulFetch executes a request without allowing net/http's URL-bearing
// transport errors to escape into polling logs or API errors. Stateful URLs may
// contain signed query parameters, so callers must use this boundary instead of
// calling client.Do directly.
func DoStatefulFetch(client *http.Client, request *http.Request) (*http.Response, error) {
	if client == nil || request == nil {
		return nil, errors.New("stateful fetch request is invalid")
	}
	response, err := client.Do(request)
	if err == nil {
		return response, nil
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if request.Context() != nil && request.Context().Err() != nil {
		return nil, request.Context().Err()
	}
	return nil, errors.New("stateful fetch request failed")
}

// ValidateStatefulFetchURL performs the URL-level portion of the stateful
// egress policy. DNS is validated and pinned immediately before dialing.
func ValidateStatefulFetchURL(rawURL string) error {
	parsed, err := parseStatefulFetchURL(rawURL)
	if err != nil {
		return err
	}
	protection, err := currentStatefulFetchProtection()
	if err != nil || validateStatefulFetchURL(parsed, protection) != nil {
		return errors.New("stateful fetch target rejected")
	}
	return nil
}

func currentStatefulFetchProtection() (*common.SSRFProtection, error) {
	setting := system_setting.GetFetchSetting()
	if setting == nil {
		return nil, errors.New("stateful fetch policy is unavailable")
	}
	// Stateful URLs are upstream-controlled and therefore remain protected even
	// when the legacy generic-fetch switch is disabled. Private RFC1918/ULA
	// access still has an explicit opt-in through AllowPrivateIp.
	return common.NewSSRFProtectionFromFetchSetting(
		setting.AllowPrivateIp,
		setting.DomainFilterMode,
		setting.IpFilterMode,
		setting.DomainList,
		setting.IpList,
		setting.AllowedPorts,
		true,
	)
}

func parseStatefulFetchURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > statefulFetchMaxURLBytes {
		return nil, errors.New("invalid stateful fetch URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Opaque != "" || !parsed.IsAbs() ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		!strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("invalid stateful fetch URL")
	}
	host := parsed.Hostname()
	if strings.Contains(host, "%") || len(host) > 253 || strings.HasSuffix(parsed.Host, ":") {
		return nil, errors.New("invalid stateful fetch URL")
	}
	if port := parsed.Port(); port != "" {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			return nil, errors.New("invalid stateful fetch URL")
		}
	}
	return parsed, nil
}

func validateStatefulFetchURL(parsed *url.URL, protection *common.SSRFProtection) error {
	if parsed == nil || protection == nil {
		return errors.New("stateful fetch policy is unavailable")
	}
	port := 443
	if parsed.Port() != "" {
		parsedPort, err := strconv.Atoi(parsed.Port())
		if err != nil {
			return errors.New("stateful fetch target rejected")
		}
		port = parsedPort
	}
	if err := protection.ValidateNetworkTarget(parsed.Hostname(), port); err != nil {
		return errors.New("stateful fetch target rejected")
	}
	return nil
}

func newStatefulFetchHTTPClient(options statefulFetchHTTPClientOptions) (*http.Client, error) {
	if options.resolver == nil {
		options.resolver = net.DefaultResolver
	}
	if options.dialContext == nil {
		dialer := &net.Dialer{Timeout: statefulFetchConnectTimeout, KeepAlive: 30 * time.Second}
		options.dialContext = dialer.DialContext
	}
	if options.cache == nil {
		options.cache = &statefulFetchTransportCache{
			entries: make(map[string]statefulFetchTransportCacheEntry),
			now:     time.Now,
		}
	}
	if options.getProtection == nil {
		options.getProtection = currentStatefulFetchProtection
	}
	proxyConfig, err := parseStatefulFetchProxy(options.proxyURL)
	if err != nil {
		return nil, err
	}
	roundTripper := &statefulFetchRoundTripper{
		resolver: options.resolver, dialContext: options.dialContext,
		rootCAs: options.rootCAs, proxy: proxyConfig, cache: options.cache,
		getProtection: options.getProtection,
	}
	return &http.Client{
		Transport: roundTripper, CheckRedirect: roundTripper.checkRedirect,
		Timeout: statefulFetchOverallTimeout,
	}, nil
}

func (t *statefulFetchRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.cache == nil || request == nil || request.URL == nil {
		return nil, failStatefulFetchRequest(request, errors.New("invalid stateful fetch request"))
	}
	parsed, err := parseStatefulFetchURL(request.URL.String())
	if err != nil {
		return nil, failStatefulFetchRequest(request, err)
	}
	protection, err := t.getProtection()
	if err != nil || protection == nil || validateStatefulFetchURL(parsed, protection) != nil {
		return nil, failStatefulFetchRequest(request, errors.New("stateful fetch target rejected"))
	}
	targetAddresses, err := resolveStatefulFetchTarget(request.Context(), t.resolver, parsed, protection)
	if err != nil {
		return nil, failStatefulFetchRequest(request, err)
	}
	proxyRoute, err := resolveStatefulFetchProxy(request.Context(), t.resolver, t.proxy)
	if err != nil {
		return nil, failStatefulFetchRequest(request, err)
	}
	targetPort := request.URL.Port()
	if targetPort == "" {
		targetPort = "443"
	}
	targetHost := strings.TrimSuffix(strings.ToLower(request.URL.Hostname()), ".")
	replayable := statefulFetchRedirectMethodAllowed(request)
	for index, targetAddress := range targetAddresses {
		route := statefulFetchRoute{
			targetHost:    targetHost,
			targetPort:    targetPort,
			targetAddress: net.JoinHostPort(targetAddress.String(), targetPort),
			proxy:         proxyRoute,
		}
		transport, transportErr := t.cache.get(route, t.dialContext, t.rootCAs)
		if transportErr != nil {
			if !replayable || index == len(targetAddresses)-1 {
				break
			}
			continue
		}

		clonedRequest := request.Clone(request.Context())
		clonedURL := *parsed
		clonedURL.Host = route.targetAddress
		clonedRequest.URL = &clonedURL
		clonedRequest.Host = request.URL.Host
		clonedRequest.RequestURI = ""
		clonedRequest.Header.Del("Proxy-Authorization")

		response, attemptErr := transport.RoundTrip(clonedRequest)
		if attemptErr != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if request.Context().Err() != nil {
				return nil, failStatefulFetchRequest(request, request.Context().Err())
			}
			if replayable && index < len(targetAddresses)-1 {
				continue
			}
			break
		}
		if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
			_ = response.Body.Close()
			return nil, failStatefulFetchRequest(request, errors.New("stateful fetch compressed response rejected"))
		}
		response.Request = request
		if isStatefulFetchRedirectStatus(response.StatusCode) {
			location := response.Header.Get("Location")
			if location != "" {
				if _, parseErr := request.URL.Parse(location); parseErr != nil {
					response.Header.Del("Location")
				}
			}
		}
		return response, nil
	}
	if request.Context().Err() != nil {
		return nil, failStatefulFetchRequest(request, request.Context().Err())
	}
	return nil, failStatefulFetchRequest(request, errors.New("stateful fetch transport failed"))
}

func failStatefulFetchRequest(request *http.Request, err error) error {
	if request != nil {
		request.URL = &url.URL{Scheme: "https", Host: "redacted.invalid"}
		request.Host = ""
		request.RequestURI = ""
		request.Header = make(http.Header)
	}
	return err
}

func (t *statefulFetchRoundTripper) CloseIdleConnections() {
	if t == nil || t.cache == nil {
		return
	}
	t.cache.closeIdleConnections()
}

func (cache *statefulFetchTransportCache) get(
	route statefulFetchRoute,
	dialContext func(context.Context, string, string) (net.Conn, error),
	rootCAs *x509.CertPool,
) (*http.Transport, error) {
	if cache == nil {
		return nil, errors.New("stateful fetch transport cache unavailable")
	}
	key := statefulFetchRouteKey(route)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]statefulFetchTransportCacheEntry)
	}
	now := time.Now()
	if cache.now != nil {
		now = cache.now()
	}
	for entryKey, entry := range cache.entries {
		if now.Sub(entry.lastUsed) <= statefulFetchTransportCacheTTL {
			continue
		}
		entry.transport.CloseIdleConnections()
		delete(cache.entries, entryKey)
	}
	if entry, exists := cache.entries[key]; exists {
		entry.lastUsed = now
		cache.entries[key] = entry
		return entry.transport, nil
	}
	if len(cache.entries) >= statefulFetchTransportCacheMax {
		oldestKey := ""
		var oldest time.Time
		for entryKey, entry := range cache.entries {
			if oldestKey == "" || entry.lastUsed.Before(oldest) ||
				(entry.lastUsed.Equal(oldest) && entryKey < oldestKey) {
				oldestKey = entryKey
				oldest = entry.lastUsed
			}
		}
		if oldestKey != "" {
			cache.entries[oldestKey].transport.CloseIdleConnections()
			delete(cache.entries, oldestKey)
		}
	}
	transport, err := newStatefulFetchTransport(route, dialContext, rootCAs)
	if err != nil {
		return nil, err
	}
	cache.entries[key] = statefulFetchTransportCacheEntry{transport: transport, lastUsed: now}
	return transport, nil
}

func (cache *statefulFetchTransportCache) closeIdleConnections() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, entry := range cache.entries {
		entry.transport.CloseIdleConnections()
	}
}

func newStatefulFetchTransport(
	route statefulFetchRoute,
	dialContext func(context.Context, string, string) (net.Conn, error),
	rootCAs *x509.CertPool,
) (*http.Transport, error) {
	if route.targetHost == "" || route.targetAddress == "" || dialContext == nil {
		return nil, errors.New("invalid stateful fetch route")
	}
	targetDialer := &statefulFetchContextDialer{addresses: []string{route.targetAddress}, dialContext: dialContext}
	transport := &http.Transport{
		DialContext:            targetDialer.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		MaxIdleConns:           64,
		MaxIdleConnsPerHost:    8,
		MaxConnsPerHost:        32,
		IdleConnTimeout:        statefulFetchIdleConnTimeout,
		TLSHandshakeTimeout:    statefulFetchTLSHandshakeTimeout,
		ResponseHeaderTimeout:  statefulFetchResponseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: statefulFetchMaxResponseHeaderBytes,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: route.targetHost,
			RootCAs:    rootCAs,
		},
	}
	if route.proxy == nil {
		return transport, nil
	}

	proxyDialer := &statefulFetchContextDialer{
		addresses: route.proxy.pinnedAddresses, dialContext: dialContext,
	}
	switch route.proxy.config.scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(route.proxy.transportURL)
		transport.DialContext = proxyDialer.DialContext
		if route.proxy.config.scheme == "https" {
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				connection, err := proxyDialer.DialContext(ctx, network, route.proxy.pinnedAddress)
				if err != nil {
					return nil, err
				}
				tlsConnection := tls.Client(connection, &tls.Config{
					MinVersion: tls.VersionTLS12,
					ServerName: route.proxy.config.host,
					RootCAs:    rootCAs,
				})
				handshakeContext, cancel := context.WithTimeout(ctx, statefulFetchTLSHandshakeTimeout)
				defer cancel()
				if err := tlsConnection.HandshakeContext(handshakeContext); err != nil {
					_ = connection.Close()
					return nil, err
				}
				return tlsConnection, nil
			}
		}
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if route.proxy.config.url.User != nil {
			password, _ := route.proxy.config.url.User.Password()
			auth = &proxy.Auth{User: route.proxy.config.url.User.Username(), Password: password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", route.proxy.pinnedAddress, auth, proxyDialer)
		if err != nil {
			return nil, errors.New("invalid stateful SOCKS proxy")
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("stateful SOCKS proxy does not support cancellation")
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != route.targetAddress {
				return nil, errors.New("stateful SOCKS target changed")
			}
			proxyContext, cancel := context.WithTimeout(ctx, statefulFetchConnectTimeout)
			defer cancel()
			return contextDialer.DialContext(proxyContext, network, route.targetAddress)
		}
	default:
		return nil, errors.New("unsupported stateful fetch proxy")
	}
	return transport, nil
}

func (dialer *statefulFetchContextDialer) Dial(network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statefulFetchConnectTimeout)
	defer cancel()
	return dialer.DialContext(ctx, network, address)
}

func (dialer *statefulFetchContextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if dialer == nil || len(dialer.addresses) == 0 || dialer.addresses[0] == "" ||
		dialer.dialContext == nil || address != dialer.addresses[0] {
		return nil, errors.New("stateful fetch dial target changed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, candidate := range dialer.addresses {
		if candidate == "" {
			return nil, errors.New("stateful fetch dial target changed")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		connection, err := dialer.dialContext(ctx, network, candidate)
		if err == nil {
			return connection, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("stateful fetch dial failed")
}

func resolveStatefulFetchTarget(
	ctx context.Context,
	resolver statefulFetchResolver,
	parsed *url.URL,
	protection *common.SSRFProtection,
) ([]netip.Addr, error) {
	if parsed == nil || resolver == nil || protection == nil {
		return nil, errors.New("invalid stateful fetch target")
	}
	if err := validateStatefulFetchURL(parsed, protection); err != nil {
		return nil, errors.New("stateful fetch target rejected")
	}
	host := parsed.Hostname()
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if err := validateStatefulFetchTargetIP(host, address, protection); err != nil {
			return nil, errors.New("stateful fetch target rejected")
		}
		return []netip.Addr{address}, nil
	}
	resolved, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("stateful fetch DNS resolution failed")
	}
	addresses := make([]netip.Addr, 0, len(resolved))
	seen := make(map[netip.Addr]struct{}, len(resolved))
	for _, address := range resolved {
		if !address.IsValid() || address.Zone() != "" {
			return nil, errors.New("stateful fetch DNS resolution returned an invalid address")
		}
		address = address.Unmap()
		if err := validateStatefulFetchTargetIP(host, address, protection); err != nil {
			return nil, errors.New("stateful fetch DNS resolution rejected")
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return nil, errors.New("stateful fetch DNS resolution returned no usable addresses")
	}
	addresses = sortedStatefulFetchAddresses(addresses)
	if len(addresses) > statefulFetchMaxResolvedAddresses {
		addresses = addresses[:statefulFetchMaxResolvedAddresses]
	}
	return addresses, nil
}

func validateStatefulFetchTargetIP(host string, address netip.Addr, protection *common.SSRFProtection) error {
	if !address.IsValid() || address.Is4In6() || protection == nil {
		return errors.New("stateful fetch target rejected")
	}
	address = address.Unmap()
	if err := protection.ValidateResolvedIP(host, net.IP(address.AsSlice())); err != nil {
		return errors.New("stateful fetch target rejected")
	}
	// The standard public-address policy rejects special-purpose ranges. An
	// explicit private egress opt-in may add only RFC1918/ULA addresses; it never
	// opens loopback, link-local/cloud-metadata, multicast, or documentation nets.
	if validateStatefulFetchPublicIP(host, address) == nil {
		return nil
	}
	if address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() &&
		!address.IsLinkLocalMulticast() && !address.IsMulticast() && !address.IsUnspecified() {
		return nil
	}
	return errors.New("stateful fetch target rejected")
}

func validateStatefulFetchPublicIP(host string, address netip.Addr) error {
	if !address.IsValid() || address.Is4In6() {
		return errors.New("stateful fetch target rejected")
	}
	address = address.Unmap()
	for _, prefix := range statefulFetchRejectedPublicPrefixes {
		if prefix.Contains(address) {
			return errors.New("stateful fetch target rejected")
		}
	}
	if err := statefulFetchPublicAddressProtection.ValidateResolvedIP(host, net.IP(address.AsSlice())); err != nil {
		return errors.New("stateful fetch target rejected")
	}
	return nil
}

func parseStatefulFetchProxy(rawURL string) (*statefulFetchProxyConfig, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, nil
	}
	if len(rawURL) > statefulFetchMaxProxyURLBytes {
		return nil, errors.New("invalid stateful fetch proxy")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("invalid stateful fetch proxy")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return nil, errors.New("unsupported stateful fetch proxy")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid stateful fetch proxy")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") || len(host) > 253 {
		return nil, errors.New("invalid stateful fetch proxy")
	}
	port := parsed.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 || strings.HasSuffix(parsed.Host, ":") {
		return nil, errors.New("invalid stateful fetch proxy")
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		if len(parsed.User.Username()) > statefulFetchMaxProxyCredentialBytes ||
			len(password) > statefulFetchMaxProxyCredentialBytes {
			return nil, errors.New("invalid stateful fetch proxy")
		}
	}
	cloned := *parsed
	cloned.Scheme = scheme
	cloned.Host = net.JoinHostPort(host, port)
	cloned.Path = ""
	return &statefulFetchProxyConfig{scheme: scheme, url: &cloned, host: host, port: port}, nil
}

func resolveStatefulFetchProxy(
	ctx context.Context,
	resolver statefulFetchResolver,
	config *statefulFetchProxyConfig,
) (*statefulFetchProxyRoute, error) {
	if config == nil {
		return nil, nil
	}
	if resolver == nil {
		return nil, errors.New("stateful fetch proxy resolver unavailable")
	}
	var addresses []netip.Addr
	if address, err := netip.ParseAddr(config.host); err == nil {
		address = address.Unmap()
		if err := validateStatefulFetchLiteralProxyIP(address); err != nil {
			return nil, err
		}
		addresses = []netip.Addr{address}
	} else {
		resolved, err := resolver.LookupNetIP(ctx, "ip", config.host)
		if err != nil || len(resolved) == 0 {
			return nil, errors.New("stateful fetch proxy DNS resolution failed")
		}
		seen := make(map[netip.Addr]struct{}, len(resolved))
		for _, address := range resolved {
			if !address.IsValid() || address.Zone() != "" {
				return nil, errors.New("stateful fetch proxy DNS resolution returned an invalid address")
			}
			address = address.Unmap()
			if err := validateStatefulFetchProxyIP(address); err != nil {
				return nil, errors.New("stateful fetch proxy DNS resolution rejected")
			}
			if _, exists := seen[address]; exists {
				continue
			}
			seen[address] = struct{}{}
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("stateful fetch proxy DNS resolution returned no usable addresses")
	}
	addresses = sortedStatefulFetchAddresses(addresses)
	if len(addresses) > statefulFetchMaxResolvedAddresses {
		addresses = addresses[:statefulFetchMaxResolvedAddresses]
	}
	pinnedAddresses := make([]string, 0, len(addresses))
	for _, address := range addresses {
		pinnedAddresses = append(pinnedAddresses, net.JoinHostPort(address.String(), config.port))
	}
	pinnedAddress := pinnedAddresses[0]
	transportURL := *config.url
	transportURL.Scheme = "http"
	transportURL.Host = pinnedAddress
	return &statefulFetchProxyRoute{
		config: *config, pinnedAddress: pinnedAddress,
		pinnedAddresses: pinnedAddresses, transportURL: &transportURL,
	}, nil
}

func validateStatefulFetchLiteralProxyIP(address netip.Addr) error {
	return validateStatefulFetchProxyIP(address)
}

func validateStatefulFetchProxyIP(address netip.Addr) error {
	if !address.IsValid() || address.Is4In6() {
		return errors.New("stateful fetch proxy target rejected")
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() {
		return errors.New("stateful fetch proxy target rejected")
	}
	if address.IsPrivate() || address.IsLoopback() || validateStatefulFetchPublicIP("", address) == nil {
		return nil
	}
	return errors.New("stateful fetch proxy target rejected")
}

func statefulFetchRouteKey(route statefulFetchRoute) string {
	parts := []string{route.targetHost, route.targetPort, route.targetAddress}
	if route.proxy != nil {
		parts = append(parts, route.proxy.config.scheme, strings.Join(route.proxy.pinnedAddresses, ","), route.proxy.config.url.String())
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%x", digest)
}

func (t *statefulFetchRoundTripper) checkRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) == 0 || via[0] == nil || via[0].URL == nil ||
		len(via) > statefulFetchMaxRedirects ||
		!statefulFetchRedirectMethodAllowed(via[0]) || !statefulFetchRedirectMethodAllowed(request) {
		return rejectStatefulFetchRedirect(request)
	}
	parsed, err := parseStatefulFetchURL(request.URL.String())
	if err != nil || t == nil || t.getProtection == nil {
		return rejectStatefulFetchRedirect(request)
	}
	protection, err := t.getProtection()
	if err != nil || validateStatefulFetchURL(parsed, protection) != nil {
		return rejectStatefulFetchRedirect(request)
	}
	stripSensitive := !sameStatefulFetchOrigin(via[len(via)-1].URL, request.URL)
	for index := 1; !stripSensitive && index < len(via); index++ {
		if via[index] == nil || via[index].URL == nil ||
			!sameStatefulFetchOrigin(via[index-1].URL, via[index].URL) {
			stripSensitive = true
		}
	}
	if stripSensitive {
		stripStatefulFetchCrossOriginHeaders(request.Header)
	}
	request.Header.Del("Proxy-Authorization")
	return nil
}

func stripStatefulFetchCrossOriginHeaders(header http.Header) {
	for name := range header {
		normalized := strings.ToLower(strings.TrimSpace(name))
		switch normalized {
		case "authorization", "proxy-authorization", "cookie", "cookie2", "origin", "referer":
			header.Del(name)
			continue
		}
		if strings.Contains(normalized, "api-key") || strings.Contains(normalized, "apikey") ||
			strings.Contains(normalized, "auth-token") || strings.Contains(normalized, "access-token") ||
			strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "credential") || strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "signature") {
			header.Del(name)
		}
	}
}

func statefulFetchRedirectMethodAllowed(request *http.Request) bool {
	if request == nil || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
		return false
	}
	return (request.Body == nil || request.Body == http.NoBody) && request.GetBody == nil && request.ContentLength <= 0
}

func rejectStatefulFetchRedirect(request *http.Request) error {
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

func sameStatefulFetchOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) ||
		!strings.EqualFold(strings.TrimSuffix(left.Hostname(), "."), strings.TrimSuffix(right.Hostname(), ".")) {
		return false
	}
	return statefulFetchURLPort(left) == statefulFetchURLPort(right)
}

func statefulFetchURLPort(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return "443"
	}
	return ""
}

func isStatefulFetchRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func sortedStatefulFetchAddresses(values []netip.Addr) []netip.Addr {
	addresses := append([]netip.Addr(nil), values...)
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Compare(addresses[j]) < 0 })
	return addresses
}
