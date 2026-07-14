package vertex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExchangeJWTStopsBeforeNetworkWhenRoutingSendBoundaryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	want := errors.New("routing send rejected")
	sendState := relaycommon.NewRoutingUpstreamSendState(func() error { return want })
	relaycommon.BindRoutingUpstreamSendState(c, sendState)

	token, err := exchangeJwtForAccessToken(
		c.Request.Context(),
		"signed-jwt",
		&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}},
		func() error { return relaycommon.MarkRoutingUpstreamSent(c) },
	)

	require.ErrorIs(t, err, want)
	assert.Empty(t, token)
	assert.False(t, sendState.Sent())
}

func TestExchangeJWTWithProxyStopsBeforeNetworkWhenRoutingSendBoundaryFails(t *testing.T) {
	want := errors.New("routing send rejected")
	token, err := exchangeJwtForAccessTokenWithProxy(
		context.Background(),
		"signed-jwt",
		"",
		func() error { return want },
	)

	require.ErrorIs(t, err, want)
	assert.Empty(t, token)
}

func TestVertexAccessTokenCacheKeyUsesStableCredentialIdentity(t *testing.T) {
	first, err := vertexAccessTokenCacheKey(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId: 10, ChannelIsMultiKey: true, ChannelMultiKeyIndex: 0, RoutingCredentialID: 101,
	}})
	require.NoError(t, err)
	second, err := vertexAccessTokenCacheKey(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId: 10, ChannelIsMultiKey: true, ChannelMultiKeyIndex: 0, RoutingCredentialID: 202,
	}})
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.Contains(t, first, "credential-101")

	_, err = vertexAccessTokenCacheKey(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId: 10, ChannelIsMultiKey: true, ChannelMultiKeyIndex: 0,
	}})
	require.Error(t, err)
}

func TestExchangeJWTUsesStatefulClientAndMarksImmediatelyBeforeSend(t *testing.T) {
	previousFactory := vertexTokenHTTPClientFactory
	defer func() { vertexTokenHTTPClientFactory = previousFactory }()

	var marked atomic.Bool
	var capturedProxy string
	var capturedRequest *http.Request
	vertexTokenHTTPClientFactory = func(proxyURL string) (*http.Client, error) {
		capturedProxy = proxyURL
		return &http.Client{Transport: vertexTokenRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			assert.True(t, marked.Load())
			capturedRequest = request.Clone(request.Context())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token-a"}`)),
				Request:    request,
			}, nil
		})}, nil
	}

	token, err := exchangeJwtForAccessTokenWithProxy(
		context.Background(),
		"signed-jwt-value",
		"https://proxy.example:8443",
		func() error {
			marked.Store(true)
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "token-a", token)
	assert.Equal(t, "https://proxy.example:8443", capturedProxy)
	require.NotNil(t, capturedRequest)
	assert.Equal(t, "www.googleapis.com", capturedRequest.URL.Hostname())
	assert.Equal(t, "application/x-www-form-urlencoded", capturedRequest.Header.Get("Content-Type"))
	assert.NotContains(t, capturedRequest.URL.String(), "signed-jwt-value")
}

type vertexTokenRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip vertexTokenRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
