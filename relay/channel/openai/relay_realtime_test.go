package openai

import (
	"context"
	"errors"
	"net"
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

func TestRealtimeFirstByteStateTimeoutWaitsForSuccessfulForward(t *testing.T) {
	state := &realtimeFirstByteState{}
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	forwardDone := make(chan error, 1)
	timeoutStarted := make(chan struct{})
	timeoutDone := make(chan bool, 1)
	committed := false

	go func() {
		forwardDone <- state.forward(func() error {
			close(writeStarted)
			<-releaseWrite
			return nil
		}, func() {
			committed = true
		})
	}()

	<-writeStarted
	go func() {
		close(timeoutStarted)
		timeoutDone <- state.tryTimeout()
	}()
	<-timeoutStarted

	timeoutCompletedDuringWrite := false
	timedOut := false
	select {
	case timedOut = <-timeoutDone:
		timeoutCompletedDuringWrite = true
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseWrite)
	require.NoError(t, <-forwardDone)
	if !timeoutCompletedDuringWrite {
		timedOut = <-timeoutDone
	}
	assert.Falsef(t, timeoutCompletedDuringWrite, "timeout decision completed during write: timedOut=%t", timedOut)
	assert.False(t, timedOut)
	assert.True(t, committed)
	assert.True(t, state.hasCommitted())
}

func TestRealtimeFirstByteStateTimeoutDefersToFailedForward(t *testing.T) {
	state := &realtimeFirstByteState{}
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	forwardDone := make(chan error, 1)
	timeoutStarted := make(chan struct{})
	timeoutDone := make(chan bool, 1)
	writeErr := errors.New("client write failed")
	commitCalled := false

	go func() {
		forwardDone <- state.forward(func() error {
			close(writeStarted)
			<-releaseWrite
			return writeErr
		}, func() {
			commitCalled = true
		})
	}()

	<-writeStarted
	go func() {
		close(timeoutStarted)
		timeoutDone <- state.tryTimeout()
	}()
	<-timeoutStarted

	timeoutCompletedDuringWrite := false
	timedOut := false
	select {
	case timedOut = <-timeoutDone:
		timeoutCompletedDuringWrite = true
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseWrite)
	require.ErrorIs(t, <-forwardDone, writeErr)
	if !timeoutCompletedDuringWrite {
		timedOut = <-timeoutDone
	}
	assert.Falsef(t, timeoutCompletedDuringWrite, "timeout decision completed during write: timedOut=%t", timedOut)
	assert.False(t, timedOut)
	assert.False(t, commitCalled)
	assert.False(t, state.hasCommitted())
}

func TestRealtimeFirstByteStateRejectsForwardAfterTimeout(t *testing.T) {
	state := &realtimeFirstByteState{}
	writeCalled := false
	commitCalled := false

	require.True(t, state.tryTimeout())
	err := state.forward(func() error {
		writeCalled = true
		return nil
	}, func() {
		commitCalled = true
	})

	require.ErrorIs(t, err, errRealtimeFirstByteTimedOut)
	assert.False(t, writeCalled)
	assert.False(t, commitCalled)
	assert.False(t, state.hasCommitted())
}

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
		endReason   relaycommon.StreamEndReason
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
		resultCh <- handlerResult{err: apiErr, replayCount: len(info.RealtimeReplayMessages), endReason: info.StreamStatus.EndReason}
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
	assert.Equal(t, "upstream realtime failed before first response", result.err.Error())
	require.Error(t, result.err.Cause())
	assert.Contains(t, result.err.Cause().Error(), "error reading from target")
	assert.Equal(t, 1, result.replayCount)
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, result.endReason)
}

func TestOpenaiRealtimeHandlerClientDisconnectMarksClientGone(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetRelease := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
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
		err       *types.NewAPIError
		endReason relaycommon.StreamEndReason
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r
		info := relaycommon.GenRelayInfoWs(c, clientConn)
		info.TargetWs = targetConn
		info.OriginModelName = "gpt-realtime"
		info.UsingGroup = "default"
		info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 19}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{err: apiErr, endReason: info.StreamStatus.EndReason}
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+clientServer.URL[len("http"):], nil)
	require.NoError(t, err)
	require.NoError(t, clientConn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"),
		time.Now().Add(time.Second),
	))
	require.NoError(t, clientConn.Close())

	select {
	case result := <-resultCh:
		assert.Nil(t, result.err)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, result.endReason)
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after client disconnect")
	}
}

func TestOpenaiRealtimeHandlerMalformedUpstreamMessageIsPreCommitCorruption(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetRelease := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("{malformed")))
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
		err                   *types.NewAPIError
		endReason             relaycommon.StreamEndReason
		receivedResponseCount int
		sendResponseCount     int
		hasSentResponse       bool
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r
		info := relaycommon.GenRelayInfoWs(c, clientConn)
		info.TargetWs = targetConn
		info.OriginModelName = "gpt-realtime"
		info.UsingGroup = "default"
		info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 20}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{
			err:                   apiErr,
			endReason:             info.StreamStatus.EndReason,
			receivedResponseCount: info.ReceivedResponseCount,
			sendResponseCount:     info.SendResponseCount,
			hasSentResponse:       info.HasSendResponse(),
		}
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+clientServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case result := <-resultCh:
		require.NotNil(t, result.err)
		assert.Equal(t, http.StatusBadGateway, result.err.StatusCode)
		assert.Equal(t, "upstream realtime failed before first response", result.err.Error())
		require.Error(t, result.err.Cause())
		assert.Contains(t, result.err.Cause().Error(), "error unmarshalling message")
		assert.Equal(t, relaycommon.StreamEndReasonScannerErr, result.endReason)
		assert.Zero(t, result.receivedResponseCount)
		assert.Zero(t, result.sendResponseCount)
		assert.False(t, result.hasSentResponse)
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after malformed upstream message")
	}
}

func TestOpenaiRealtimeHandlerClientWriteFailureMarksClientGone(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetReceived := make(chan struct{}, 1)
	targetSend := make(chan struct{}, 1)
	targetRelease := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		_, _, err = conn.ReadMessage()
		require.NoError(t, err)
		targetReceived <- struct{}{}
		<-targetSend
		message, err := common.Marshal(dto.RealtimeEvent{
			Type:    dto.RealtimeEventTypeSessionCreated,
			Session: &dto.RealtimeSession{},
		})
		require.NoError(t, err)
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, message))
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
		err                   *types.NewAPIError
		endReason             relaycommon.StreamEndReason
		receivedResponseCount int
		sendResponseCount     int
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()
		tcpConn, ok := clientConn.UnderlyingConn().(*net.TCPConn)
		require.True(t, ok)
		require.NoError(t, tcpConn.CloseWrite())

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r
		info := relaycommon.GenRelayInfoWs(c, clientConn)
		info.TargetWs = targetConn
		info.OriginModelName = "gpt-realtime"
		info.UsingGroup = "default"
		info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 21}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{
			err:                   apiErr,
			endReason:             info.StreamStatus.EndReason,
			receivedResponseCount: info.ReceivedResponseCount,
			sendResponseCount:     info.SendResponseCount,
		}
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
		targetSend <- struct{}{}
	case <-time.After(time.Second):
		require.Fail(t, "target did not receive forwarded realtime request")
	}

	select {
	case result := <-resultCh:
		assert.Nil(t, result.err)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, result.endReason)
		assert.Zero(t, result.receivedResponseCount)
		assert.Zero(t, result.sendResponseCount)
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after client write failure")
	}
}

func TestOpenaiRealtimeHandlerNormalUpstreamCloseSucceeds(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		message, err := common.Marshal(dto.RealtimeEvent{
			Type:    dto.RealtimeEventTypeSessionCreated,
			Session: &dto.RealtimeSession{},
		})
		require.NoError(t, err)
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, message))
		require.NoError(t, conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"),
			time.Now().Add(time.Second),
		))
	}))
	t.Cleanup(targetServer.Close)

	targetConn, _, err := websocket.DefaultDialer.Dial("ws"+targetServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetConn.Close() })

	type handlerResult struct {
		err                   *types.NewAPIError
		endReason             relaycommon.StreamEndReason
		receivedResponseCount int
		sendResponseCount     int
	}
	resultCh := make(chan handlerResult, 1)
	clientServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer clientConn.Close()

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = r
		info := relaycommon.GenRelayInfoWs(c, clientConn)
		info.TargetWs = targetConn
		info.OriginModelName = "gpt-realtime"
		info.UsingGroup = "default"
		info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 22}

		apiErr, _ := OpenaiRealtimeHandler(c, info)
		resultCh <- handlerResult{
			err:                   apiErr,
			endReason:             info.StreamStatus.EndReason,
			receivedResponseCount: info.ReceivedResponseCount,
			sendResponseCount:     info.SendResponseCount,
		}
	}))
	t.Cleanup(clientServer.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+clientServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case result := <-resultCh:
		assert.Nil(t, result.err)
		assert.Equal(t, relaycommon.StreamEndReasonEOF, result.endReason)
		assert.Equal(t, 1, result.receivedResponseCount)
		assert.Equal(t, 1, result.sendResponseCount)
	case <-time.After(time.Second):
		require.Fail(t, "realtime handler did not return after normal upstream close")
	}
}

func TestHandleRealtimeClientMessageTargetWriteFailureIsUpstreamCorruption(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	targetRelease := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		<-targetRelease
	}))
	t.Cleanup(func() {
		close(targetRelease)
		targetServer.Close()
	})

	targetConn, _, err := websocket.DefaultDialer.Dial("ws"+targetServer.URL[len("http"):], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetConn.Close() })
	tcpConn, ok := targetConn.UnderlyingConn().(*net.TCPConn)
	require.True(t, ok)
	require.NoError(t, tcpConn.CloseWrite())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-realtime",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-realtime"},
	}
	message, err := common.Marshal(dto.RealtimeEvent{
		Type:    dto.RealtimeEventTypeSessionUpdate,
		Session: &dto.RealtimeSession{},
	})
	require.NoError(t, err)

	err = handleRealtimeClientMessage(c, info, targetConn, message, &dto.RealtimeUsage{})

	require.Error(t, err)
	var relayErr *realtimeRelayError
	require.True(t, errors.As(err, &relayErr))
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, relayErr.endReason)
}
