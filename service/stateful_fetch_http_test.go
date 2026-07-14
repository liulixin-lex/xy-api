package service

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticStatefulFetchResolver map[string][]netip.Addr

func (resolver staticStatefulFetchResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addresses, exists := resolver[host]
	if !exists {
		return nil, fmt.Errorf("unexpected DNS lookup for %s", host)
	}
	return append([]netip.Addr(nil), addresses...), nil
}

type sequenceStatefulFetchResolver struct {
	mu        sync.Mutex
	addresses map[string][][]netip.Addr
	calls     map[string]int
}

func (resolver *sequenceStatefulFetchResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	values := resolver.addresses[host]
	index := resolver.calls[host]
	resolver.calls[host] = index + 1
	if index >= len(values) {
		return nil, fmt.Errorf("unexpected DNS lookup %d for %s", index+1, host)
	}
	return append([]netip.Addr(nil), values[index]...), nil
}

func statefulFetchTestProtection(allowPrivate bool) *common.SSRFProtection {
	return &common.SSRFProtection{
		AllowPrivateIp:         allowPrivate,
		DomainFilterMode:       false,
		IpFilterMode:           false,
		ApplyIPFilterForDomain: true,
	}
}

func statefulFetchTestRoots(servers ...*httptest.Server) *x509.CertPool {
	roots := x509.NewCertPool()
	for _, server := range servers {
		if certificate := server.Certificate(); certificate != nil {
			roots.AddCert(certificate)
		}
	}
	return roots
}

func TestStatefulFetchPinsValidatedIPAndPreservesHostAndSNI(t *testing.T) {
	var observedHost string
	var observedSNI string
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedHost = request.Host
		observedSNI = request.TLS.ServerName
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = writer.Write([]byte("video"))
	}))
	defer backend.Close()

	var dialed []string
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com": {netip.MustParseAddr("8.8.8.8")},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	response, err := client.Get("https://example.com/media")
	require.NoError(t, err)
	require.NotNil(t, response)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert.Equal(t, "video", string(body))
	assert.Equal(t, []string{"8.8.8.8:443"}, dialed)
	assert.Equal(t, "example.com", observedHost)
	assert.Equal(t, "example.com", observedSNI)
}

func TestStatefulFetchFallsBackAcrossValidatedTargetAddresses(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = writer.Write([]byte("fallback-video"))
	}))
	defer backend.Close()

	var dialed []string
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com": {
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("1.1.1.1"),
			},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			if address == "1.1.1.1:443" {
				return nil, errors.New("first address unavailable")
			}
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	response, err := client.Get("https://example.com/media")
	require.NoError(t, err)
	require.NotNil(t, response)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	assert.Equal(t, "fallback-video", string(body))
	assert.Equal(t, []string{"1.1.1.1:443", "8.8.8.8:443"}, dialed)
}

func TestStatefulFetchRejectsMixedDNSAnswerBeforeDial(t *testing.T) {
	var dialed atomic.Bool
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com": {
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("127.0.0.1"),
			},
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed.Store(true)
			return nil, errors.New("must not dial")
		},
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodGet, "https://example.com/video?key=do-not-log", nil)
	require.NoError(t, err)
	response, err := DoStatefulFetch(client, request)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.False(t, dialed.Load())
	assert.NotContains(t, err.Error(), "do-not-log")
	assert.Equal(t, "stateful fetch request failed", err.Error())
}

func TestStatefulFetchBoundsResolvedAddressesAfterValidatingEveryAnswer(t *testing.T) {
	parsed, err := parseStatefulFetchURL("https://example.com/video")
	require.NoError(t, err)
	addresses := make([]netip.Addr, 0, statefulFetchMaxResolvedAddresses+2)
	for index := statefulFetchMaxResolvedAddresses + 2; index > 0; index-- {
		addresses = append(addresses, netip.MustParseAddr(fmt.Sprintf("8.8.8.%d", index)))
	}

	resolved, err := resolveStatefulFetchTarget(
		context.Background(), staticStatefulFetchResolver{"example.com": addresses}, parsed,
		statefulFetchTestProtection(false),
	)
	require.NoError(t, err)
	require.Len(t, resolved, statefulFetchMaxResolvedAddresses)
	assert.Equal(t, netip.MustParseAddr("8.8.8.1"), resolved[0])
	assert.Equal(t, netip.MustParseAddr("8.8.8.8"), resolved[len(resolved)-1])

	addresses = append(addresses, netip.MustParseAddr("127.0.0.1"))
	_, err = resolveStatefulFetchTarget(
		context.Background(), staticStatefulFetchResolver{"example.com": addresses}, parsed,
		statefulFetchTestProtection(false),
	)
	require.Error(t, err)
}

func TestStatefulFetchPrivatePolicyNeverAllowsSpecialPurposeTargets(t *testing.T) {
	protection := statefulFetchTestProtection(true)
	require.NoError(t, validateStatefulFetchTargetIP("private.example", netip.MustParseAddr("10.1.2.3"), protection))
	require.NoError(t, validateStatefulFetchTargetIP("private.example", netip.MustParseAddr("fd00::123"), protection))

	for _, address := range []string{
		"127.0.0.1",
		"169.254.169.254",
		"224.0.0.1",
		"192.0.2.10",
		"::1",
		"fe80::1",
	} {
		require.Error(t, validateStatefulFetchTargetIP("blocked.example", netip.MustParseAddr(address), protection), address)
	}
}

func TestStatefulFetchURLAndProxyParsingFailClosed(t *testing.T) {
	for _, rawURL := range []string{
		"http://example.com/video",
		"https://user:pass@example.com/video",
		"https://example.com/video#fragment",
		"https://example.com:0/video",
		"https://example.com:/video",
	} {
		_, err := parseStatefulFetchURL(rawURL)
		require.Error(t, err, rawURL)
	}
	_, err := parseStatefulFetchURL("https://example.com/" + strings.Repeat("x", statefulFetchMaxURLBytes))
	require.Error(t, err)

	for _, proxyURL := range []string{
		"http://proxy.example:3128",
		"https://user:pass@proxy.example:8443",
		"socks5://proxy.example:1080",
		"socks5h://proxy.example:1080",
	} {
		_, err := parseStatefulFetchProxy(proxyURL)
		require.NoError(t, err, proxyURL)
	}
	for _, proxyURL := range []string{
		"ftp://proxy.example",
		"http://proxy.example/path",
		"http://proxy.example?token=secret",
		"http://proxy.example:0",
		"http://" + strings.Repeat("u", statefulFetchMaxProxyCredentialBytes+1) + "@proxy.example",
	} {
		_, err := parseStatefulFetchProxy(proxyURL)
		require.Error(t, err, proxyURL)
	}
}

func TestStatefulFetchProxyResolutionAllowsExplicitPrivateAndRejectsMetadata(t *testing.T) {
	config, err := parseStatefulFetchProxy("http://proxy.internal:3128")
	require.NoError(t, err)
	route, err := resolveStatefulFetchProxy(context.Background(), staticStatefulFetchResolver{
		"proxy.internal": {netip.MustParseAddr("10.0.0.20")},
	}, config)
	require.NoError(t, err)
	require.NotNil(t, route)
	assert.Equal(t, "10.0.0.20:3128", route.pinnedAddress)
	assert.Equal(t, []string{"10.0.0.20:3128"}, route.pinnedAddresses)

	_, err = resolveStatefulFetchProxy(context.Background(), staticStatefulFetchResolver{
		"proxy.internal": {netip.MustParseAddr("169.254.169.254")},
	}, config)
	require.Error(t, err)
}

func TestStatefulFetchRevalidatesDNSOnEveryRedirect(t *testing.T) {
	var requests atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Location", "/next")
		writer.WriteHeader(http.StatusFound)
	}))
	defer backend.Close()

	resolver := &sequenceStatefulFetchResolver{
		addresses: map[string][][]netip.Addr{
			"example.com": {
				{netip.MustParseAddr("8.8.8.8")},
				{netip.MustParseAddr("127.0.0.1")},
			},
		},
		calls: make(map[string]int),
	}
	var dials atomic.Int32
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: resolver,
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	response, err := client.Get("https://example.com/start")
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Equal(t, int32(1), requests.Load())
	assert.Equal(t, int32(1), dials.Load())
}

func TestStatefulFetchAllowsValidatedCrossOriginRedirectAndStripsSensitiveHeaders(t *testing.T) {
	var requests atomic.Int32
	var firstAuthorization string
	var redirectedAuthorization string
	var redirectedCookie string
	var redirectedAPIKey string
	var redirectedToken string
	var redirectedSecurityToken string
	var redirectedReferer string
	var redirectedRange string
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path == "/start" {
			firstAuthorization = request.Header.Get("Authorization")
			writer.Header().Set("Location", "https://example.com:8443/video?key=redirect-secret")
			writer.WriteHeader(http.StatusFound)
			return
		}
		redirectedAuthorization = request.Header.Get("Authorization")
		redirectedCookie = request.Header.Get("Cookie")
		redirectedAPIKey = request.Header.Get("X-API-Key")
		redirectedToken = request.Header.Get("X-Auth-Token")
		redirectedSecurityToken = request.Header.Get("X-Amz-Security-Token")
		redirectedReferer = request.Header.Get("Referer")
		redirectedRange = request.Header.Get("Range")
		_, _ = writer.Write([]byte("redirected-video"))
	}))
	defer backend.Close()

	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com": {netip.MustParseAddr("8.8.8.8")},
		},
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodGet, "https://example.com/start", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer redirect-secret")
	request.Header.Set("Cookie", "session=redirect-secret")
	request.Header.Set("X-API-Key", "redirect-secret")
	request.Header.Set("X-Auth-Token", "redirect-secret")
	request.Header.Set("X-Amz-Security-Token", "redirect-secret")
	request.Header.Set("Referer", "https://origin.example/path?token=redirect-secret")
	request.Header.Set("Range", "bytes=0-1023")
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NotNil(t, response)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	assert.Equal(t, "redirected-video", string(body))
	assert.Equal(t, int32(2), requests.Load())
	assert.Equal(t, "Bearer redirect-secret", firstAuthorization)
	assert.Empty(t, redirectedAuthorization)
	assert.Empty(t, redirectedCookie)
	assert.Empty(t, redirectedAPIKey)
	assert.Empty(t, redirectedToken)
	assert.Empty(t, redirectedSecurityToken)
	assert.Empty(t, redirectedReferer)
	assert.Equal(t, "bytes=0-1023", redirectedRange)
}

func TestStatefulFetchKeepsSensitiveHeadersStrippedAfterAnyCrossOriginHop(t *testing.T) {
	tests := []struct {
		name            string
		firstLocation   string
		secondLocation  string
		wantCredentials []string
	}{
		{
			name:            "cross origin then same origin",
			firstLocation:   "https://example.com:8443/middle",
			secondLocation:  "https://example.com:8443/final",
			wantCredentials: []string{"secret", "", ""},
		},
		{
			name:            "cross origin then another origin",
			firstLocation:   "https://example.com:8443/middle",
			secondLocation:  "https://example.com:9443/final",
			wantCredentials: []string{"secret", "", ""},
		},
		{
			name:            "same origin chain",
			firstLocation:   "https://example.com/middle",
			secondLocation:  "https://example.com/final",
			wantCredentials: []string{"secret", "secret", "secret"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := make([]string, 0, 3)
			backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				credentials = append(credentials, request.Header.Get("X-Goog-Api-Key"))
				switch request.URL.Path {
				case "/start":
					writer.Header().Set("Location", test.firstLocation)
					writer.WriteHeader(http.StatusFound)
				case "/middle":
					writer.Header().Set("Location", test.secondLocation)
					writer.WriteHeader(http.StatusFound)
				default:
					_, _ = writer.Write([]byte("ok"))
				}
			}))
			defer backend.Close()

			client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
				resolver: staticStatefulFetchResolver{
					"example.com": {netip.MustParseAddr("8.8.8.8")},
				},
				dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
				},
				rootCAs: statefulFetchTestRoots(backend),
				getProtection: func() (*common.SSRFProtection, error) {
					return statefulFetchTestProtection(false), nil
				},
			})
			require.NoError(t, err)

			request, err := http.NewRequest(http.MethodGet, "https://example.com/start", nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("X-Goog-Api-Key", "secret")
			request.Header.Set("X-Custom-Credential", "secret")
			response, err := client.Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())

			assert.Equal(t, test.wantCredentials, credentials)
		})
	}
}

func TestStatefulFetchBlocksCrossOriginRedirectToRejectedDNSAddress(t *testing.T) {
	var requests atomic.Int32
	var dials atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Location", "https://other.example/video?key=redirect-secret")
		writer.WriteHeader(http.StatusFound)
	}))
	defer backend.Close()

	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com":   {netip.MustParseAddr("8.8.8.8")},
			"other.example": {netip.MustParseAddr("127.0.0.1")},
		},
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodGet, "https://example.com/start", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer redirect-secret")
	response, err := DoStatefulFetch(client, request)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Equal(t, "stateful fetch request failed", err.Error())
	assert.NotContains(t, err.Error(), "redirect-secret")
	assert.Equal(t, int32(1), requests.Load())
	assert.Equal(t, int32(1), dials.Load())
}

func TestStatefulFetchRejectsCompressedResponses(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		_, _ = writer.Write([]byte("compressed-content"))
	}))
	defer backend.Close()

	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{"example.com": {netip.MustParseAddr("8.8.8.8")}},
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
		},
		rootCAs: statefulFetchTestRoots(backend),
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodGet, "https://example.com/video?key=compressed-secret", nil)
	require.NoError(t, err)
	response, err := DoStatefulFetch(client, request)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Equal(t, "stateful fetch request failed", err.Error())
	assert.NotContains(t, err.Error(), "compressed-secret")
}

func TestStatefulFetchHTTPAndHTTPSConnectPinProxyAndTarget(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		name := "http"
		if secureProxy {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			var observedHost string
			var observedSNI string
			backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				observedHost = request.Host
				observedSNI = request.TLS.ServerName
				writer.Header().Set("Content-Type", "video/mp4")
				_, _ = writer.Write([]byte("proxied"))
			}))
			defer backend.Close()

			proxyCapture := make(chan capturedConnectRequest, 1)
			proxyServer := newStatefulFetchConnectProxy(t, secureProxy, backend.Listener.Addr().String(), proxyCapture)
			defer proxyServer.Close()

			proxyHost := "proxy.internal"
			proxyAddress := netip.MustParseAddr("10.0.0.8")
			if secureProxy {
				// httptest's certificate is valid for example.com.
				proxyHost = "example.com"
				proxyAddress = netip.MustParseAddr("8.8.8.8")
			}
			proxyURL := fmt.Sprintf("%s://proxy-user:proxy-pass@%s:3128", name, proxyHost)
			resolver := staticStatefulFetchResolver{
				"example.com": {netip.MustParseAddr("8.8.8.8")},
				proxyHost:     {proxyAddress},
			}
			var dialed []string
			client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
				resolver: resolver,
				dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					dialed = append(dialed, address)
					return (&net.Dialer{}).DialContext(ctx, network, proxyServer.Listener.Addr().String())
				},
				rootCAs:  statefulFetchTestRoots(backend, proxyServer),
				proxyURL: proxyURL,
				getProtection: func() (*common.SSRFProtection, error) {
					return statefulFetchTestProtection(false), nil
				},
			})
			require.NoError(t, err)

			response, err := client.Get("https://example.com/video")
			require.NoError(t, err)
			require.NotNil(t, response)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			client.CloseIdleConnections()

			capture := <-proxyCapture
			assert.Equal(t, "8.8.8.8:443", capture.target)
			assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-pass")), capture.authorization)
			assert.Equal(t, []string{net.JoinHostPort(proxyAddress.String(), "3128")}, dialed)
			assert.Equal(t, "proxied", string(body))
			assert.Equal(t, "example.com", observedHost)
			assert.Equal(t, "example.com", observedSNI)
		})
	}
}

func TestStatefulFetchProxyDialFallsBackAcrossValidatedAddresses(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("proxied-fallback"))
	}))
	defer backend.Close()

	proxyCapture := make(chan capturedConnectRequest, 1)
	proxyServer := newStatefulFetchConnectProxy(t, false, backend.Listener.Addr().String(), proxyCapture)
	defer proxyServer.Close()

	var dialed []string
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com":    {netip.MustParseAddr("8.8.8.8")},
			"proxy.internal": {netip.MustParseAddr("10.0.0.9"), netip.MustParseAddr("10.0.0.8")},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			if address == "10.0.0.8:3128" {
				return nil, errors.New("first proxy address unavailable")
			}
			return (&net.Dialer{}).DialContext(ctx, network, proxyServer.Listener.Addr().String())
		},
		rootCAs:  statefulFetchTestRoots(backend),
		proxyURL: "http://proxy.internal:3128",
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	response, err := client.Get("https://example.com/video")
	require.NoError(t, err)
	require.NotNil(t, response)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	capture := <-proxyCapture
	assert.Equal(t, "8.8.8.8:443", capture.target)
	assert.Equal(t, "proxied-fallback", string(body))
	assert.Equal(t, []string{"10.0.0.8:3128", "10.0.0.9:3128"}, dialed)
}

type capturedConnectRequest struct {
	target        string
	authorization string
}

func newStatefulFetchConnectProxy(
	t *testing.T,
	secure bool,
	backendAddress string,
	capture chan<- capturedConnectRequest,
) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			http.Error(writer, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		capture <- capturedConnectRequest{
			target: request.Host, authorization: request.Header.Get("Proxy-Authorization"),
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			http.Error(writer, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		clientConnection, buffer, err := hijacker.Hijack()
		if err != nil {
			return
		}
		backendConnection, err := net.Dial("tcp", backendAddress)
		if err != nil {
			_ = clientConnection.Close()
			return
		}
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		go func() {
			_, _ = io.Copy(backendConnection, clientConnection)
			_ = backendConnection.Close()
		}()
		_, _ = io.Copy(clientConnection, backendConnection)
		_ = clientConnection.Close()
	})
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = false
	if secure {
		server.StartTLS()
	} else {
		server.Start()
	}
	return server
}

func TestStatefulFetchSOCKS5HPinsValidatedTargetIP(t *testing.T) {
	var observedSNI string
	backend := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedSNI = request.TLS.ServerName
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = writer.Write([]byte("socks"))
	}))
	defer backend.Close()

	socksAddress, socksCapture, closeSOCKS := newStatefulFetchSOCKSProxy(t, backend.Listener.Addr().String())
	defer closeSOCKS()
	var dialed []string
	client, err := newStatefulFetchHTTPClient(statefulFetchHTTPClientOptions{
		resolver: staticStatefulFetchResolver{
			"example.com":    {netip.MustParseAddr("8.8.8.8")},
			"proxy.internal": {netip.MustParseAddr("10.0.0.9")},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return (&net.Dialer{}).DialContext(ctx, network, socksAddress)
		},
		rootCAs:  statefulFetchTestRoots(backend),
		proxyURL: "socks5h://socks-user:socks-pass@proxy.internal:1080",
		getProtection: func() (*common.SSRFProtection, error) {
			return statefulFetchTestProtection(false), nil
		},
	})
	require.NoError(t, err)

	response, err := client.Get("https://example.com/video")
	require.NoError(t, err)
	require.NotNil(t, response)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	client.CloseIdleConnections()

	capture := <-socksCapture
	assert.Equal(t, "8.8.8.8:443", capture.target)
	assert.Equal(t, "socks-user", capture.username)
	assert.Equal(t, "socks-pass", capture.password)
	assert.Equal(t, []string{"10.0.0.9:1080"}, dialed)
	assert.Equal(t, "socks", string(body))
	assert.Equal(t, "example.com", observedSNI)
}

type capturedSOCKSRequest struct {
	target   string
	username string
	password string
}

func newStatefulFetchSOCKSProxy(
	t *testing.T,
	backendAddress string,
) (string, <-chan capturedSOCKSRequest, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := make(chan capturedSOCKSRequest, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		reader := bufio.NewReader(accepted)
		version, readErr := reader.ReadByte()
		if readErr != nil || version != 5 {
			_ = accepted.Close()
			return
		}
		methodCount, readErr := reader.ReadByte()
		if readErr != nil {
			_ = accepted.Close()
			return
		}
		methods := make([]byte, int(methodCount))
		if _, readErr = io.ReadFull(reader, methods); readErr != nil {
			_ = accepted.Close()
			return
		}
		_, _ = accepted.Write([]byte{5, 2})

		if authVersion, _ := reader.ReadByte(); authVersion != 1 {
			_ = accepted.Close()
			return
		}
		usernameLength, _ := reader.ReadByte()
		username := make([]byte, int(usernameLength))
		_, _ = io.ReadFull(reader, username)
		passwordLength, _ := reader.ReadByte()
		password := make([]byte, int(passwordLength))
		_, _ = io.ReadFull(reader, password)
		_, _ = accepted.Write([]byte{1, 0})

		header := make([]byte, 4)
		if _, readErr = io.ReadFull(reader, header); readErr != nil || header[0] != 5 || header[1] != 1 {
			_ = accepted.Close()
			return
		}
		host := ""
		switch header[3] {
		case 1:
			address := make([]byte, net.IPv4len)
			_, _ = io.ReadFull(reader, address)
			host = net.IP(address).String()
		case 4:
			address := make([]byte, net.IPv6len)
			_, _ = io.ReadFull(reader, address)
			host = net.IP(address).String()
		case 3:
			length, _ := reader.ReadByte()
			address := make([]byte, int(length))
			_, _ = io.ReadFull(reader, address)
			host = string(address)
		default:
			_ = accepted.Close()
			return
		}
		portBytes := make([]byte, 2)
		_, _ = io.ReadFull(reader, portBytes)
		port := int(portBytes[0])<<8 | int(portBytes[1])
		capture <- capturedSOCKSRequest{
			target:   net.JoinHostPort(host, strconv.Itoa(port)),
			username: string(username), password: string(password),
		}

		backendConnection, dialErr := net.Dial("tcp", backendAddress)
		if dialErr != nil {
			_ = accepted.Close()
			return
		}
		_, _ = accepted.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		go func() {
			_, _ = io.Copy(backendConnection, reader)
			_ = backendConnection.Close()
		}()
		_, _ = io.Copy(accepted, backendConnection)
		_ = accepted.Close()
	}()
	return listener.Addr().String(), capture, func() {
		_ = listener.Close()
	}
}

func TestStatefulFetchTransportCacheIsBoundedAndExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := &statefulFetchTransportCache{
		entries: make(map[string]statefulFetchTransportCacheEntry),
		now:     func() time.Time { return now },
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("not expected")
	}
	firstRoute := statefulFetchRoute{
		targetHost: "host-0.example", targetPort: "443", targetAddress: "8.8.8.8:443",
	}
	firstKey := statefulFetchRouteKey(firstRoute)
	for index := 0; index <= statefulFetchTransportCacheMax; index++ {
		route := statefulFetchRoute{
			targetHost: fmt.Sprintf("host-%d.example", index),
			targetPort: "443", targetAddress: "8.8.8.8:443",
		}
		_, err := cache.get(route, dial, nil)
		require.NoError(t, err)
		now = now.Add(time.Second)
	}
	require.Len(t, cache.entries, statefulFetchTransportCacheMax)
	_, exists := cache.entries[firstKey]
	assert.False(t, exists)

	now = now.Add(statefulFetchTransportCacheTTL + time.Second)
	_, err := cache.get(statefulFetchRoute{
		targetHost: "fresh.example", targetPort: "443", targetAddress: "1.1.1.1:443",
	}, dial, nil)
	require.NoError(t, err)
	assert.Len(t, cache.entries, 1)
	cache.closeIdleConnections()
}

func TestStatefulFetchTransportLimitsAreConfigured(t *testing.T) {
	transport, err := newStatefulFetchTransport(statefulFetchRoute{
		targetHost: "example.com", targetPort: "443", targetAddress: "8.8.8.8:443",
	}, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("not expected")
	}, nil)
	require.NoError(t, err)

	assert.True(t, transport.DisableCompression)
	assert.Equal(t, statefulFetchTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
	assert.Equal(t, statefulFetchResponseHeaderTimeout, transport.ResponseHeaderTimeout)
	assert.Equal(t, statefulFetchMaxResponseHeaderBytes, int(transport.MaxResponseHeaderBytes))
	assert.Equal(t, statefulFetchIdleConnTimeout, transport.IdleConnTimeout)
	assert.Equal(t, 32, transport.MaxConnsPerHost)
}
