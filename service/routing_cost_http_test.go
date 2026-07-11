package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routingCostStaticResolver map[string][]netip.Addr

func (r routingCostStaticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, fmt.Errorf("unexpected DNS lookup for %s", host)
	}
	return addresses, nil
}

type routingCostResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolve routingCostResolverFunc) LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error) {
	return resolve(ctx, network, host)
}

func TestRoutingCostHTTPClientUsesDedicatedTransport(t *testing.T) {
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{})
	roundTripper, ok := client.Transport.(*routingCostRoundTripper)
	require.True(t, ok)
	require.NotNil(t, roundTripper.transport)

	transport := roundTripper.transport
	assert.Nil(t, transport.Proxy)
	assert.True(t, transport.DisableCompression)
	assert.True(t, transport.ForceAttemptHTTP2)
	assert.Positive(t, transport.TLSHandshakeTimeout)
	assert.Positive(t, transport.ResponseHeaderTimeout)
	assert.Positive(t, transport.IdleConnTimeout)
	require.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.False(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.Positive(t, client.Timeout)
	assert.Same(t, routingCostHTTPClient, GetRoutingCostHTTPClient())
	assert.Same(t, GetRoutingCostHTTPClient().Transport, GetRoutingCostHTTPClient().Transport)
}

func TestRoutingCostHTTPClientIgnoresHTTPSProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	response, err := client.Get(baseURL + "/api/pricing")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "direct", string(body))
}

func TestRoutingCostURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "public HTTPS", rawURL: "https://example.com/api/pricing"},
		{name: "public HTTPS custom port", rawURL: "https://example.com:8443/api/pricing"},
		{name: "plain HTTP", rawURL: "http://example.com/api/pricing", wantErr: true},
		{name: "userinfo", rawURL: "https://token@example.com/api/pricing", wantErr: true},
		{name: "loopback hostname", rawURL: "https://localhost/api/pricing", wantErr: true},
		{name: "loopback IP", rawURL: "https://127.0.0.1/api/pricing", wantErr: true},
		{name: "RFC1918", rawURL: "https://10.0.0.1/api/pricing", wantErr: true},
		{name: "link local metadata", rawURL: "https://169.254.169.254/api/pricing", wantErr: true},
		{name: "Alibaba metadata", rawURL: "https://100.100.100.200/api/pricing", wantErr: true},
		{name: "multicast", rawURL: "https://224.0.0.1/api/pricing", wantErr: true},
		{name: "special use", rawURL: "https://192.0.2.10/api/pricing", wantErr: true},
		{name: "IPv6 loopback", rawURL: "https://[::1]/api/pricing", wantErr: true},
		{name: "IPv4 mapped IPv6", rawURL: "https://[::ffff:8.8.8.8]/api/pricing", wantErr: true},
		{name: "local NAT64 prefix", rawURL: "https://[64:ff9b:1::a9fe:a9fe]/api/pricing", wantErr: true},
		{name: "dummy IPv6 prefix", rawURL: "https://[100:0:0:1::1]/api/pricing", wantErr: true},
		{name: "6to4 prefix", rawURL: "https://[2002:a9fe:a9fe::]/api/pricing", wantErr: true},
		{name: "segment routing SIDs", rawURL: "https://[5f00::1]/api/pricing", wantErr: true},
		{name: "6a44 relay anycast", rawURL: "https://192.88.99.2/api/pricing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRoutingCostURL(tt.rawURL)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRoutingCostDialerRejectsSpecialUseAddressWithoutDialing(t *testing.T) {
	for _, resolvedIP := range []string{
		"::ffff:10.0.0.1",
		"64:ff9b:1::a9fe:a9fe",
		"100:0:0:1::1",
		"2002:a9fe:a9fe::",
		"5f00::1",
		"192.88.99.2",
	} {
		t.Run(resolvedIP, func(t *testing.T) {
			var dialCount atomic.Int32
			client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
				resolver: routingCostStaticResolver{
					"routing.example.com": {netip.MustParseAddr(resolvedIP)},
				},
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					dialCount.Add(1)
					return nil, errors.New("unsafe target must not be dialed")
				},
			})
			request, err := http.NewRequest(http.MethodGet, "https://routing.example.com/api/pricing", nil)
			require.NoError(t, err)

			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			require.Error(t, err)
			assert.Zero(t, dialCount.Load())
		})
	}
}

func TestRoutingCostDialerUnmapsResolvedPublicIPv4BeforeDialing(t *testing.T) {
	var dialed []string
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
		resolver: routingCostStaticResolver{
			"routing.example.com": {netip.MustParseAddr("::ffff:8.8.8.8")},
		},
		dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			clientConn, serverConn := net.Pipe()
			serverConn.Close()
			return clientConn, nil
		},
	})
	request, err := http.NewRequest(http.MethodGet, "https://routing.example.com/api/pricing", nil)
	require.NoError(t, err)

	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.Equal(t, []string{"8.8.8.8:443"}, dialed)
}

func TestRoutingCostMalformedRedirectDoesNotExposeLocation(t *testing.T) {
	const encodedSecret = "%73%65%63%72%65%74"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Location", "/"+encodedSecret+"/%zz")
		w.Header().Add("Location", "https://other.example.com/"+encodedSecret)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	response, err := client.Get(baseURL + "/start")
	require.NoError(t, err)
	require.NotNil(t, response)
	defer response.Body.Close()
	assert.Equal(t, http.StatusFound, response.StatusCode)
	assert.Empty(t, response.Header.Values("Location"))
	assert.Equal(t, baseURL+"/start", response.Request.URL.String())
	assert.NotContains(t, response.Request.URL.String(), encodedSecret)
}

func TestRoutingCostRedirectRejectionDoesNotExposeRejectedLocation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://user:password@other.example.com/path?access_token=redirect-secret")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	response, err := client.Get(baseURL + "/start")
	require.NoError(t, err)
	require.NotNil(t, response)
	defer response.Body.Close()
	assert.Equal(t, http.StatusFound, response.StatusCode)
	assert.Empty(t, response.Header.Values("Location"))
	assert.Equal(t, baseURL+"/start", response.Request.URL.String())
	for _, secret := range []string{"user", "password", "access_token", "redirect-secret", "other.example.com"} {
		assert.NotContains(t, response.Request.URL.String(), secret)
	}
}

func TestRoutingCostDialerRejectsMixedResolvedIPsWithoutDialing(t *testing.T) {
	var dialCount atomic.Int32
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
		resolver: routingCostStaticResolver{
			"routing.example.com": {
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("10.0.0.1"),
			},
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, errors.New("unsafe target must not be dialed")
		},
	})
	request, err := http.NewRequest(http.MethodGet, "https://routing.example.com/api/pricing", nil)
	require.NoError(t, err)

	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.Zero(t, dialCount.Load())
	assert.NotContains(t, err.Error(), "10.0.0.1")
}

func TestRoutingCostDialerPinsConnectionToValidatedIP(t *testing.T) {
	var dialed []string
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
		resolver: routingCostStaticResolver{
			"routing.example.com": {netip.MustParseAddr("8.8.8.8")},
		},
		dialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			clientConn, serverConn := net.Pipe()
			serverConn.Close()
			return clientConn, nil
		},
	})
	request, err := http.NewRequest(http.MethodGet, "https://routing.example.com/api/pricing", nil)
	require.NoError(t, err)

	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.Equal(t, []string{"8.8.8.8:443"}, dialed)
}

func TestRoutingCostTLSVerifiesOriginalHostname(t *testing.T) {
	serverName := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverName <- r.TLS.ServerName
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	response, err := client.Get(baseURL + "/api/pricing")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "routing.example.com", <-serverName)
}

func TestRoutingCostTLSIgnoresGlobalInsecureSetting(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "must not be accepted")
	}))
	t.Cleanup(server.Close)

	previous := common.TLSInsecureSkipVerify
	common.TLSInsecureSkipVerify = true
	t.Cleanup(func() { common.TLSInsecureSkipVerify = previous })

	client, baseURL := newRoutingCostTLSTestClient(t, server, false)
	response, err := client.Get(baseURL + "/api/pricing")
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
}

func TestRoutingCostTLSRequiresTLS12OrNewer(t *testing.T) {
	tests := []struct {
		name    string
		version uint16
		wantErr bool
	}{
		{name: "TLS 1.1 rejected", version: tls.VersionTLS11, wantErr: true},
		{name: "TLS 1.2 accepted", version: tls.VersionTLS12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			}))
			server.TLS = &tls.Config{
				MinVersion: tt.version,
				MaxVersion: tt.version,
			}
			server.StartTLS()
			t.Cleanup(server.Close)

			client, baseURL := newRoutingCostTLSTestClient(t, server, true)
			response, err := client.Get(baseURL + "/api/pricing")
			if response != nil {
				response.Body.Close()
			}
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRoutingCostRedirectAllowsThreeSameOriginHopsAndStripsCredentials(t *testing.T) {
	var hits atomic.Int32
	redirectedHeaders := make(chan http.Header, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/0", "/1", "/2":
			step, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/%d", step+1), http.StatusFound)
		case "/3":
			redirectedHeaders <- r.Header.Clone()
			_, _ = io.WriteString(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	request, err := http.NewRequest(http.MethodGet, baseURL+"/0", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Proxy-Authorization", "Basic secret")
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("New-Api-User", "42")

	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.EqualValues(t, 4, hits.Load())
	headers := <-redirectedHeaders
	assert.Empty(t, headers.Get("Authorization"))
	assert.Empty(t, headers.Get("Proxy-Authorization"))
	assert.Empty(t, headers.Get("Cookie"))
	assert.Empty(t, headers.Get("New-Api-User"))
}

func TestRoutingCostRedirectRejectsUnsafeRedirects(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       io.Reader
		statusCode int
		location   func(*http.Request) string
	}{
		{
			name:       "cross host",
			method:     http.MethodGet,
			statusCode: http.StatusFound,
			location: func(r *http.Request) string {
				_, port, _ := net.SplitHostPort(r.Host)
				return "https://other.example.com:" + port + "/final"
			},
		},
		{
			name:       "HTTPS downgrade",
			method:     http.MethodGet,
			statusCode: http.StatusFound,
			location: func(r *http.Request) string {
				return "http://" + r.Host + "/final"
			},
		},
		{
			name:       "POST rewritten to GET",
			method:     http.MethodPost,
			statusCode: http.StatusFound,
			location: func(*http.Request) string {
				return "/final"
			},
		},
		{
			name:       "GET with body",
			method:     http.MethodGet,
			body:       strings.NewReader("payload"),
			statusCode: http.StatusTemporaryRedirect,
			location: func(*http.Request) string {
				return "/final"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var finalHits atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/final" {
					finalHits.Add(1)
					_, _ = io.WriteString(w, "unexpected")
					return
				}
				w.Header().Set("Location", tt.location(r))
				w.WriteHeader(tt.statusCode)
			}))
			t.Cleanup(server.Close)

			client, baseURL := newRoutingCostTLSTestClient(t, server, true)
			request, err := http.NewRequest(tt.method, baseURL+"/start", tt.body)
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)
			require.NotNil(t, response)
			response.Body.Close()
			assert.Equal(t, tt.statusCode, response.StatusCode)
			assert.Zero(t, finalHits.Load())
		})
	}
}

func TestRoutingCostRedirectRejectsFourthHop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		step, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%d", step+1), http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client, baseURL := newRoutingCostTLSTestClient(t, server, true)
	response, err := client.Get(baseURL + "/0")
	require.NoError(t, err)
	require.NotNil(t, response)
	response.Body.Close()
	assert.Equal(t, http.StatusFound, response.StatusCode)
	assert.EqualValues(t, 4, hits.Load())
}

func TestRoutingCostRedirectRevalidatesDNSWhenAReconnectIsRequired(t *testing.T) {
	var finalHits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Connection", "close")
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalHits.Add(1)
			_, _ = io.WriteString(w, "unexpected")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())

	var resolveCalls atomic.Int32
	var dialCalls atomic.Int32
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
		resolver: routingCostResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			if resolveCalls.Add(1) == 1 {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		}),
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialCalls.Add(1)
			dialer := &net.Dialer{Timeout: time.Second}
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
		rootCAs: rootCAs,
	})

	response, err := client.Get("https://routing.example.com:" + port + "/start")
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.Equal(t, int32(2), resolveCalls.Load())
	assert.Equal(t, int32(1), dialCalls.Load())
	assert.Zero(t, finalHits.Load())
}

func newRoutingCostTLSTestClient(t *testing.T, server *httptest.Server, trustServer bool) (*http.Client, string) {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)

	var rootCAs *x509.CertPool
	if trustServer {
		rootCAs = x509.NewCertPool()
		rootCAs.AddCert(server.Certificate())
	}
	client := newRoutingCostHTTPClient(routingCostHTTPClientOptions{
		resolver: routingCostStaticResolver{
			"routing.example.com": {netip.MustParseAddr("8.8.8.8")},
		},
		dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: time.Second}
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
		rootCAs: rootCAs,
	})
	return client, "https://routing.example.com:" + port
}
