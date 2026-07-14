package volcengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleTTSWebSocketResponseStopsOnRequestCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	requestReceived := make(chan struct{})
	connectionClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		close(requestReceived)
		if _, _, err := conn.ReadMessage(); err != nil {
			close(connectionClosed)
		}
	}))
	defer server.Close()

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil).WithContext(requestCtx)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "app|token"},
	}
	result := make(chan *types.NewAPIError, 1)
	go func() {
		_, apiErr := handleTTSWebSocketResponse(
			c,
			"ws"+strings.TrimPrefix(server.URL, "http"),
			VolcengineTTSRequest{},
			info,
			"mp3",
		)
		result <- apiErr
	}()

	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		require.Fail(t, "volcengine TTS upstream did not receive the request")
	}
	cancel()

	select {
	case apiErr := <-result:
		require.NotNil(t, apiErr)
		assert.ErrorIs(t, apiErr, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "volcengine TTS handler stayed blocked after request cancellation")
	}
	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		require.Fail(t, "volcengine TTS upstream connection stayed open after request cancellation")
	}
}

func TestHandleTTSWebSocketResponsePreservesHandshakeStatus(t *testing.T) {
	statusCodes := []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusPaymentRequired,
		http.StatusTooManyRequests,
	}
	for _, statusCode := range statusCodes {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(statusCode)
			}))
			t.Cleanup(server.Close)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil)
			_, apiErr := handleTTSWebSocketResponse(
				c,
				"ws"+strings.TrimPrefix(server.URL, "http"),
				VolcengineTTSRequest{},
				&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "app|token"}},
				"mp3",
			)

			require.NotNil(t, apiErr)
			assert.Equal(t, statusCode, apiErr.SourceStatusCode())
		})
	}
}
