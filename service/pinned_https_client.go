package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	pinnedServiceDialTimeout            = 5 * time.Second
	pinnedServiceTLSHandshakeTimeout    = 5 * time.Second
	pinnedServiceResponseHeaderTimeout  = 10 * time.Second
	pinnedServiceMaxResponseHeaderBytes = 1 << 20
)

type pinnedServiceRoundTripper struct {
	scheme    string
	host      string
	port      string
	transport *http.Transport
}

// PinnedServiceClientCache retains at most one client for a trusted,
// operator-configured service. Reusing the transport keeps connection and
// goroutine counts bounded; replacing a setting closes the old idle pool.
type PinnedServiceClientCache struct {
	mutex   sync.Mutex
	key     string
	baseURL url.URL
	client  *http.Client
}

// ValidatePinnedServiceBaseURL validates the stable shape of an
// operator-configured base URL. HTTP, private hosts, and custom ports remain
// supported for v0.1.6 self-hosted compatibility; the corresponding client
// still pins the exact scheme, host, and port and forbids redirects.
func ValidatePinnedServiceBaseURL(rawURL string) error {
	_, err := parsePinnedServiceBaseURL(rawURL)
	return err
}

func (cache *PinnedServiceClientCache) Get(rawURL string, timeout time.Duration) (*url.URL, *http.Client, error) {
	baseURL, err := parsePinnedServiceBaseURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		return nil, nil, errors.New("service request timeout must be positive")
	}
	key := baseURL.String() + "\x00" + timeout.String()

	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if cache.client != nil && cache.key == key {
		baseURLCopy := cache.baseURL
		return &baseURLCopy, cache.client, nil
	}

	baseURL, client, err := newPinnedServiceClientFromURL(baseURL, timeout, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	oldClient := cache.client
	cache.key = key
	cache.baseURL = *baseURL
	cache.client = client
	if oldClient != nil {
		oldClient.CloseIdleConnections()
	}
	baseURLCopy := cache.baseURL
	return &baseURLCopy, cache.client, nil
}

func newPinnedServiceClientWithDialer(rawURL string, timeout time.Duration, resolver ssrfResolver, dialContext func(context.Context, string, string) (net.Conn, error)) (*url.URL, *http.Client, error) {
	baseURL, err := parsePinnedServiceBaseURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
	return newPinnedServiceClientFromURL(baseURL, timeout, resolver, dialContext)
}

func newPinnedServiceClientFromURL(baseURL *url.URL, timeout time.Duration, resolver ssrfResolver, dialContext func(context.Context, string, string) (net.Conn, error)) (*url.URL, *http.Client, error) {
	if timeout <= 0 {
		return nil, nil, errors.New("service request timeout must be positive")
	}

	host := normalizePinnedHostname(baseURL.Hostname())
	port := baseURL.Port()
	if port == "" {
		if baseURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return nil, nil, errors.New("service URL has an invalid port")
	}
	protection := &common.SSRFProtection{
		AllowPrivateIp:         true,
		DomainFilterMode:       true,
		DomainList:             []string{host},
		IpFilterMode:           false,
		AllowedPorts:           []int{portNumber},
		ApplyIPFilterForDomain: true,
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dialContext == nil {
		netDialer := &net.Dialer{Timeout: pinnedServiceDialTimeout, KeepAlive: 30 * time.Second}
		dialContext = netDialer.DialContext
	}

	protectedDialer := &protectedFetchDialer{
		resolver:    resolver,
		dialContext: dialContext,
		getProtection: func() (*common.SSRFProtection, bool, error) {
			return protection, true, nil
		},
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            protectedDialer.DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    pinnedServiceTLSHandshakeTimeout,
		ResponseHeaderTimeout:  pinnedServiceResponseHeaderTimeout,
		MaxResponseHeaderBytes: pinnedServiceMaxResponseHeaderBytes,
		IdleConnTimeout:        60 * time.Second,
		MaxIdleConns:           20,
		MaxIdleConnsPerHost:    10,
		MaxConnsPerHost:        20,
		ForceAttemptHTTP2:      true,
	}
	client := &http.Client{
		Transport: &pinnedServiceRoundTripper{
			scheme:    baseURL.Scheme,
			host:      host,
			port:      port,
			transport: transport,
		},
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not allowed for pinned service requests")
		},
	}
	return baseURL, client, nil
}

func parsePinnedServiceBaseURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("service URL is required")
	}
	if strings.ContainsAny(rawURL, "\r\n") {
		return nil, errors.New("service URL contains invalid control characters")
	}
	if strings.Contains(rawURL, "#") {
		return nil, errors.New("service URL must not contain a query or fragment")
	}

	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid service URL: %w", err)
	}
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	if (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Opaque != "" {
		return nil, errors.New("service URL must use HTTP or HTTPS")
	}
	if parsedURL.User != nil {
		return nil, errors.New("service URL must not contain credentials")
	}
	if parsedURL.RawQuery != "" || parsedURL.ForceQuery || parsedURL.Fragment != "" {
		return nil, errors.New("service URL must not contain a query or fragment")
	}
	host := normalizePinnedHostname(parsedURL.Hostname())
	if host == "" || strings.Contains(host, "%") {
		return nil, errors.New("service URL has an invalid host")
	}
	port := parsedURL.Port()
	if port == "" {
		if parsedURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("service URL has an invalid port")
	}

	defaultPort := (parsedURL.Scheme == "https" && port == "443") || (parsedURL.Scheme == "http" && port == "80")
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		parsedURL.Host = "[" + ip.String() + "]"
	} else {
		parsedURL.Host = host
	}
	if !defaultPort {
		parsedURL.Host = net.JoinHostPort(host, port)
	}
	return parsedURL, nil
}

func normalizePinnedHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func (t *pinnedServiceRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("invalid pinned service request")
	}
	if strings.ToLower(request.URL.Scheme) != t.scheme || request.URL.Opaque != "" || request.URL.User != nil {
		return nil, errors.New("pinned request scheme or credentials do not match the configured service")
	}
	if normalizePinnedHostname(request.URL.Hostname()) != t.host {
		return nil, errors.New("pinned request host does not match the configured service")
	}
	requestPort := request.URL.Port()
	if requestPort == "" {
		if t.scheme == "https" {
			requestPort = "443"
		} else {
			requestPort = "80"
		}
	}
	if requestPort != t.port {
		return nil, errors.New("pinned request port does not match the configured service")
	}

	// The base origin is operator-controlled, while request data is restricted to
	// its path/query/body. The exact scheme, host, and port are pinned above;
	// redirects are disabled and DNS is resolved once immediately before dialing.
	// codeql[go/request-forgery]
	return t.transport.RoundTrip(request)
}

func (t *pinnedServiceRoundTripper) CloseIdleConnections() {
	t.transport.CloseIdleConnections()
}
