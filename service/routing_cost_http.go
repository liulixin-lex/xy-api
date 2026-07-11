package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	routingCostConnectTimeout        = 5 * time.Second
	routingCostTLSHandshakeTimeout   = 5 * time.Second
	routingCostResponseHeaderTimeout = 10 * time.Second
	routingCostOverallTimeout        = 15 * time.Second
	routingCostIdleConnTimeout       = 90 * time.Second
	routingCostMaxRedirects          = 3
)

type routingCostResolver interface {
	LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error)
}

type routingCostHTTPClientOptions struct {
	resolver    routingCostResolver
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
	rootCAs     *x509.CertPool
}

type routingCostDialer struct {
	resolver    routingCostResolver
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

type routingCostRoundTripper struct {
	transport *http.Transport
}

var routingCostNetworkProtection = common.SSRFProtection{
	AllowPrivateIp:         false,
	DomainFilterMode:       false,
	IpFilterMode:           false,
	ApplyIPFilterForDomain: true,
}

var routingCostRejectedIPv6Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var routingCostHTTPClient = newRoutingCostHTTPClient(routingCostHTTPClientOptions{})

// GetRoutingCostHTTPClient returns the shared, fail-closed client used only for
// smart-routing cost and balance synchronization.
func GetRoutingCostHTTPClient() *http.Client {
	return routingCostHTTPClient
}

// ValidateRoutingCostURL validates the URL-level routing cost egress policy.
// DNS results are validated and pinned immediately before dialing.
func ValidateRoutingCostURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid routing cost URL: %w", err)
	}
	return validateRoutingCostURL(parsed)
}

func newRoutingCostHTTPClient(options routingCostHTTPClientOptions) *http.Client {
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
	return &http.Client{
		Transport:     &routingCostRoundTripper{transport: transport},
		CheckRedirect: checkRoutingCostRedirect,
		Timeout:       routingCostOverallTimeout,
	}
}

func (t *routingCostRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("invalid routing cost request")
	}
	if err := validateRoutingCostURL(request.URL); err != nil {
		return nil, err
	}
	return t.transport.RoundTrip(request)
}

func (t *routingCostRoundTripper) CloseIdleConnections() {
	t.transport.CloseIdleConnections()
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
	if err = routingCostNetworkProtection.ValidateNetworkTarget(host, portNumber); err != nil {
		return nil, errors.New("routing cost target rejected")
	}

	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		if err = validateRoutingCostIP(host, ip); err != nil {
			return nil, err
		}
		return d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
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
		if err = validateRoutingCostIP(host, address); err != nil {
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
	if err := routingCostNetworkProtection.ValidateNetworkTarget(host, port); err != nil {
		return errors.New("routing cost target rejected")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return validateRoutingCostIP(host, ip)
	}
	return nil
}

func validateRoutingCostIP(host string, ip netip.Addr) error {
	if !ip.IsValid() || ip.Is4In6() {
		return errors.New("routing cost target rejected")
	}
	if ip.Is6() {
		for _, prefix := range routingCostRejectedIPv6Prefixes {
			if prefix.Contains(ip) {
				return errors.New("routing cost target rejected")
			}
		}
	}
	if err := routingCostNetworkProtection.ValidateResolvedIP(host, net.IP(ip.AsSlice())); err != nil {
		return errors.New("routing cost target rejected")
	}
	return nil
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
	if err := validateRoutingCostURL(request.URL); err != nil {
		return rejectRoutingCostRedirect(request)
	}

	for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "New-Api-User"} {
		request.Header.Del(header)
	}
	return nil
}

func rejectRoutingCostRedirect(request *http.Request) error {
	if request != nil {
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
