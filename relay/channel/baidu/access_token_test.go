package baidu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBaiduAccessTokenStopsSynchronousRefreshOnRequestCancel(t *testing.T) {
	service.InitHttpClient()
	apiKey := "cancel-test-client|cancel-test-secret"
	baiduTokenStore.Store(apiKey, BaiduAccessToken{
		AccessToken: "still-valid-token",
		ExpiresAt:   time.Now().Add(30 * time.Minute),
	})
	defer baiduTokenStore.Delete(apiKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	token, err := getBaiduAccessToken(ctx, apiKey, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, token)
}

func TestGetBaiduAccessTokenStopsBeforeNetworkWhenRoutingSendBoundaryFails(t *testing.T) {
	service.InitHttpClient()
	apiKey := "boundary-client|boundary-secret"
	baiduTokenStore.Delete(apiKey)
	want := errors.New("routing send rejected")

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	sendState := relaycommon.NewRoutingUpstreamSendState(func() error { return want })
	relaycommon.BindRoutingUpstreamSendState(c, sendState)

	token, err := getBaiduAccessToken(
		c.Request.Context(),
		apiKey,
		func() error { return relaycommon.MarkRoutingUpstreamSent(c) },
	)

	require.ErrorIs(t, err, want)
	assert.Empty(t, token)
	assert.False(t, sendState.Sent())
}
