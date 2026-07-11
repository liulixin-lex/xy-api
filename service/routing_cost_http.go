package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
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
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
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
		return nil, fmt.Errorf("routing cost target rejected: %w", err)
	}

	if ip := net.ParseIP(host); ip != nil {
		return d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	resolved, err := d.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("routing cost DNS resolution failed for %s: %w", host, err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("routing cost DNS resolution for %s returned no addresses", host)
	}

	verified := make([]net.IP, 0, len(resolved))
	for _, address := range resolved {
		if address.IP == nil || address.Zone != "" {
			return nil, fmt.Errorf("routing cost DNS resolution for %s returned an invalid address", host)
		}
		if err = routingCostNetworkProtection.ValidateResolvedIP(host, address.IP); err != nil {
			return nil, fmt.Errorf("routing cost target rejected: %w", err)
		}
		verified = append(verified, address.IP)
	}

	var lastDialError error
	for _, ip := range verified {
		if !networkAllowsIP(network, ip) {
			continue
		}
		connection, dialErr := d.dialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastDialError = dialErr
	}
	if lastDialError != nil {
		return nil, lastDialError
	}
	return nil, fmt.Errorf("routing cost DNS resolution for %s returned no usable addresses", host)
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
		return fmt.Errorf("routing cost target rejected: %w", err)
	}
	return nil
}

func checkRoutingCostRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) == 0 {
		return fmt.Errorf("invalid routing cost redirect")
	}
	if len(via) > routingCostMaxRedirects {
		return fmt.Errorf("routing cost request stopped after %d redirects", routingCostMaxRedirects)
	}

	original := via[0]
	if !routingCostRedirectMethodAllowed(original.Method) || routingCostRequestHasBody(original) {
		return fmt.Errorf("routing cost redirects require a GET or HEAD request without a body")
	}
	if !routingCostRedirectMethodAllowed(request.Method) || routingCostRequestHasBody(request) {
		return fmt.Errorf("routing cost redirects require a GET or HEAD request without a body")
	}
	if !strings.EqualFold(request.URL.Scheme, original.URL.Scheme) || !strings.EqualFold(request.URL.Host, original.URL.Host) {
		return fmt.Errorf("routing cost redirect must keep the original scheme and host")
	}
	if err := validateRoutingCostURL(request.URL); err != nil {
		return fmt.Errorf("routing cost redirect rejected: %w", err)
	}

	for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "New-Api-User"} {
		request.Header.Del(header)
	}
	return nil
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
