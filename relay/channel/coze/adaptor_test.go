package coze

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequestStopsDuringCozePollingWaitOnRequestCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	firstPollCompleted := make(chan struct{})
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/chat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":"chat-1","conversation_id":"conversation-1"}}`))
		case "/v3/chat/retrieve":
			count := pollCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"in_progress"}}`))
			w.(http.Flusher).Flush()
			if count == 1 {
				close(firstPollCompleted)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestCtx)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:            "test-key",
			ChannelBaseUrl:    server.URL,
			UpstreamModelName: "coze-test",
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"bot_id":"bot"}`))
		result <- err
	}()

	select {
	case <-firstPollCompleted:
	case <-time.After(time.Second):
		require.Fail(t, "coze polling request did not complete")
	}
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "coze polling wait stayed blocked after request cancellation")
	}
	assert.Equal(t, int32(1), pollCount.Load())
}
