package baidu

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"

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
	token, err := getBaiduAccessToken(ctx, apiKey)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, token)
}
