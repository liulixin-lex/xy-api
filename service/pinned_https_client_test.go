package service

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePinnedServiceBaseURLPreservesSelfHostedCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "public domain", rawURL: "https://worker.example.com/base"},
		{name: "explicit default port", rawURL: "https://worker.example.com:443/base"},
		{name: "public IPv4", rawURL: "https://8.8.8.8/base"},
		{name: "private HTTP custom port", rawURL: "http://192.168.1.20:8080/base"},
		{name: "localhost development", rawURL: "http://localhost:3000/base"},
		{name: "private HTTPS custom port", rawURL: "https://10.0.0.5:8443/base"},
		{name: "uppercase scheme", rawURL: "HTTPS://worker.example.com/base"},
		{name: "empty", rawURL: "", wantErr: "required"},
		{name: "unsupported scheme", rawURL: "ftp://worker.example.com", wantErr: "HTTP or HTTPS"},
		{name: "credentials", rawURL: "https://user:secret@worker.example.com", wantErr: "credentials"},
		{name: "query", rawURL: "https://worker.example.com/?token=secret", wantErr: "query or fragment"},
		{name: "fragment", rawURL: "https://worker.example.com/#section", wantErr: "query or fragment"},
		{name: "invalid port", rawURL: "https://worker.example.com:70000", wantErr: "invalid port"},
		{name: "missing host", rawURL: "https:///base", wantErr: "invalid host"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePinnedServiceBaseURL(test.rawURL)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestPinnedServiceClientCanonicalizesAndPinsOrigin(t *testing.T) {
	baseURL, client, err := newPinnedServiceClientWithDialer(" HTTPS://WORKER.Example.com.:443/base ", 15*time.Second, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "https://worker.example.com/base", baseURL.String())
	assert.Equal(t, 15*time.Second, client.Timeout)

	roundTripper, ok := client.Transport.(*pinnedServiceRoundTripper)
	require.True(t, ok)
	require.NotNil(t, roundTripper.transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), roundTripper.transport.TLSClientConfig.MinVersion)
	assert.Equal(t, "https", roundTripper.scheme)
	assert.Equal(t, "443", roundTripper.port)

	hostEscape, err := http.NewRequest(http.MethodGet, "https://attacker.example/path", nil)
	require.NoError(t, err)
	response, err := roundTripper.RoundTrip(hostEscape)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "does not match")

	wrongPort, err := http.NewRequest(http.MethodGet, "https://worker.example.com:8443/path", nil)
	require.NoError(t, err)
	response, err = roundTripper.RoundTrip(wrongPort)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "port does not match")

	wrongScheme, err := http.NewRequest(http.MethodGet, "http://worker.example.com/path", nil)
	require.NoError(t, err)
	response, err = roundTripper.RoundTrip(wrongScheme)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "scheme")

	redirectRequest, err := http.NewRequest(http.MethodGet, "https://worker.example.com/next", nil)
	require.NoError(t, err)
	err = client.CheckRedirect(redirectRequest, []*http.Request{redirectRequest})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirects are not allowed")
}

func TestPinnedServiceClientPinsTrustedPrivateDNSAtDialTime(t *testing.T) {
	dialCalled := false
	dialAddress := ""
	dialErr := errors.New("stop after verified dial target")
	_, client, err := newPinnedServiceClientWithDialer(
		"http://worker.example.com:8080",
		5*time.Second,
		staticSSRFResolver{
			"worker.example.com": {{IP: net.ParseIP("127.0.0.1")}},
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialCalled = true
			dialAddress = address
			return nil, dialErr
		},
	)
	require.NoError(t, err)

	request, err := http.NewRequest(http.MethodGet, "http://worker.example.com:8080/path", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.Error(t, err)
	assert.Nil(t, response)
	assert.True(t, dialCalled)
	assert.Equal(t, net.JoinHostPort("127.0.0.1", "8080"), dialAddress)
	assert.ErrorIs(t, err, dialErr)
}

func TestPinnedServiceClientCacheReusesOneTransportAndReturnsURLCopies(t *testing.T) {
	cache := PinnedServiceClientCache{}
	baseURL, firstClient, err := cache.Get("http://127.0.0.1:8080/base", 10*time.Second)
	require.NoError(t, err)
	baseURL.Path = "/mutated-by-caller"

	secondBaseURL, secondClient, err := cache.Get("http://127.0.0.1:8080/base", 10*time.Second)
	require.NoError(t, err)
	assert.Same(t, firstClient, secondClient)
	assert.Equal(t, "/base", secondBaseURL.Path)

	_, replacementClient, err := cache.Get("https://worker.example.com/base", 10*time.Second)
	require.NoError(t, err)
	assert.NotSame(t, firstClient, replacementClient)
	assert.Same(t, replacementClient, cache.client)
}

func TestPinnedServiceClientCacheConcurrentGetReusesClient(t *testing.T) {
	const callerCount = 16
	cache := PinnedServiceClientCache{}
	type getResult struct {
		client *http.Client
		err    error
	}
	results := make(chan getResult, callerCount)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup

	for range callerCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, client, err := cache.Get("https://worker.example.com/base", 5*time.Second)
			results <- getResult{client: client, err: err}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	var first *http.Client
	for result := range results {
		require.NoError(t, result.err)
		client := result.client
		if first == nil {
			first = client
			continue
		}
		assert.Same(t, first, client)
	}
}
