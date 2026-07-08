package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenaiRealtimeHandlerFirstMessageTimeoutReturnsRetryableError(t *testing.T) {
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetReceived := make(chan struct{}, 1)
	targetRelease := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		_, _, err = conn.ReadMessage()
		if err == nil {
			targetReceived <- struct{}{}
		}
		<-targetRelease
	}))
	t.Cleanup(func() {
		close(targetRelease)
		targetServer.Close()
	})

	targetConn, _, err := websocket.DefaultDialer.Dial("ws"+targetServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetConn.Close() })

	type handlerResult struct {
		err         *types.NewAPIError
		elapsed     time.Duration
		replayCount int
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()

		requestCtx, cancel := context.WithTimeout(r.Context(), 150*time.Millisecond)
		defer cancel()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r.WithContext(requestCtx)
		start := time.Now()
		info := &relaycommon.RelayInfo{
			ClientWs:          clientConn,
			TargetWs:          targetConn,
			RelayFormat:       types.RelayFormatOpenAIRealtime,
			IsStream:          true,
			StartTime:         start,
			FirstResponseTime: start.Add(-time.Second),
			OriginModelName:   "gpt-realtime",
			UsingGroup:        "default",
			ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: 17},
		}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{err: apiErr, elapsed: time.Since(start), replayCount: len(info.RealtimeReplayMessages)}
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+clientServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	sessionUpdate, err := common.Marshal(dto.RealtimeEvent{
		Type: dto.RealtimeEventTypeSessionUpdate,
		Session: &dto.RealtimeSession{
			Instructions: "hello",
		},
	})
	require.NoError(t, err)
	require.NoError(t, clientConn.WriteMessage(websocket.TextMessage, sessionUpdate))

	select {
	case <-targetReceived:
	case <-time.After(time.Second):
		require.Fail(t, "target did not receive forwarded realtime request")
	}

	var result handlerResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after first-message timeout")
	}

	require.NotNil(t, result.err)
	assert.Equal(t, http.StatusGatewayTimeout, result.err.StatusCode)
	assert.Less(t, result.elapsed, 100*time.Millisecond)
	assert.Equal(t, 1, result.replayCount)
}

func TestOpenaiRealtimeHandlerTargetCloseBeforeFirstMessageReturnsRetryableError(t *testing.T) {
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           500,
		FirstByteCapMs:           500,
		FirstByteP95Multiplier:   1,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetReceived := make(chan struct{}, 1)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		_, _, err = conn.ReadMessage()
		require.NoError(t, err)
		targetReceived <- struct{}{}
	}))
	t.Cleanup(targetServer.Close)

	targetConn, _, err := websocket.DefaultDialer.Dial("ws"+targetServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetConn.Close() })

	type handlerResult struct {
		err         *types.NewAPIError
		replayCount int
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r
		start := time.Now()
		info := &relaycommon.RelayInfo{
			ClientWs:          clientConn,
			TargetWs:          targetConn,
			RelayFormat:       types.RelayFormatOpenAIRealtime,
			IsStream:          true,
			StartTime:         start,
			FirstResponseTime: start.Add(-time.Second),
			OriginModelName:   "gpt-realtime",
			UsingGroup:        "default",
			ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: 18},
		}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{err: apiErr, replayCount: len(info.RealtimeReplayMessages)}
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+clientServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	sessionUpdate, err := common.Marshal(dto.RealtimeEvent{
		Type:    dto.RealtimeEventTypeSessionUpdate,
		Session: &dto.RealtimeSession{},
	})
	require.NoError(t, err)
	require.NoError(t, clientConn.WriteMessage(websocket.TextMessage, sessionUpdate))

	select {
	case <-targetReceived:
	case <-time.After(time.Second):
		require.Fail(t, "target did not receive forwarded realtime request")
	}

	var result handlerResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after target closed")
	}

	require.NotNil(t, result.err)
	assert.Equal(t, http.StatusBadGateway, result.err.StatusCode)
	assert.Equal(t, 1, result.replayCount)
}
