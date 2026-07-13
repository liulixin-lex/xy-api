package xunfei

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingXunfeiStreamWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

func (w *failingXunfeiStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func (w *failingXunfeiStreamWriter) WriteString(_ string) (int, error) {
	w.writes++
	return 0, w.err
}

type xunfeiFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

func (w *xunfeiFailOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newXunfeiStreamTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		StartTime:   time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "SparkDesk"},
	}
	info.SetEstimatePromptTokens(4)
	return ctx, recorder, info
}

func xunfeiStreamResponse(content string, usage dto.Usage) XunfeiChatResponse {
	var response XunfeiChatResponse
	response.Payload.Choices.Text = []XunfeiChatResponseTextItem{{Role: "assistant", Content: content}}
	response.Payload.Usage.Text = usage
	return response
}

func xunfeiStreamChannels(response XunfeiChatResponse, end xunfeiStreamEnd) (chan XunfeiChatResponse, chan xunfeiStreamEnd) {
	dataChan := make(chan XunfeiChatResponse)
	endChan := make(chan xunfeiStreamEnd, 1)
	go func() {
		dataChan <- response
		endChan <- end
	}()
	return dataChan, endChan
}

func TestXunfeiStreamReturnsPartialUsageAndOriginalReadError(t *testing.T) {
	ctx, recorder, info := newXunfeiStreamTestContext(t)
	readErr := errors.New("xunfei upstream read failed")
	response := xunfeiStreamResponse("partial", dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	dataChan, endChan := xunfeiStreamChannels(response, xunfeiStreamEnd{Reason: relaycommon.StreamEndReasonScannerErr, Err: readErr})

	usage, apiErr := relayXunfeiStream(ctx, info, dataChan, endChan, func() error { return nil })

	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, readErr)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	assert.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
}

func TestXunfeiStreamPreservesMalformedFrameCause(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{malformed"))
	}))
	defer server.Close()

	ctx, recorder, info := newXunfeiStreamTestContext(t)
	authURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dataChan, stopChan, closeUpstream, err := xunfeiMakeRequest(ctx.Request.Context(), info, dto.GeneralOpenAIRequest{}, "general", authURL, "app")
	require.NoError(t, err)

	usage, apiErr := relayXunfeiStream(ctx, info, dataChan, stopChan, closeUpstream)

	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	require.NotNil(t, apiErr)
	var syntaxErr *stdjson.SyntaxError
	assert.ErrorAs(t, apiErr, &syntaxErr)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestXunfeiStreamStopsOnFirstDownstreamWriteError(t *testing.T) {
	ctx, _, info := newXunfeiStreamTestContext(t)
	writeErr := errors.New("xunfei downstream write failed")
	writer := &failingXunfeiStreamWriter{ResponseWriter: ctx.Writer, err: writeErr}
	ctx.Writer = writer
	response := xunfeiStreamResponse("partial", dto.Usage{})
	dataChan, endChan := xunfeiStreamChannels(response, xunfeiStreamEnd{})

	usage, apiErr := relayXunfeiStream(ctx, info, dataChan, endChan, func() error { return nil })

	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Equal(t, 1, writer.writes)
}

func TestXunfeiStreamReturnsUsageWhenDoneWriteFails(t *testing.T) {
	ctx, recorder, info := newXunfeiStreamTestContext(t)
	doneErr := errors.New("xunfei done write failed")
	ctx.Writer = &xunfeiFailOnMarkerWriter{ResponseWriter: ctx.Writer, marker: []byte("[DONE]"), err: doneErr}
	response := xunfeiStreamResponse("partial", dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	dataChan, endChan := xunfeiStreamChannels(response, xunfeiStreamEnd{})

	usage, apiErr := relayXunfeiStream(ctx, info, dataChan, endChan, func() error { return nil })

	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, doneErr)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestXunfeiStreamRequestCancellationWinsOverUpstreamStop(t *testing.T) {
	ctx, recorder, info := newXunfeiStreamTestContext(t)
	requestCtx, cancel := context.WithCancel(ctx.Request.Context())
	ctx.Request = ctx.Request.WithContext(requestCtx)
	endChan := make(chan xunfeiStreamEnd, 1)
	endChan <- xunfeiStreamEnd{}
	cancel()

	usage, apiErr := relayXunfeiStream(ctx, info, make(chan XunfeiChatResponse), endChan, func() error { return nil })

	require.NotNil(t, usage)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, context.Canceled)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestXunfeiMakeRequestCloseSignalsStop(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	authURL := "ws" + strings.TrimPrefix(server.URL, "http")
	_, endChan, closeUpstream, err := xunfeiMakeRequest(context.Background(), &relaycommon.RelayInfo{}, dto.GeneralOpenAIRequest{}, "general", authURL, "app")
	require.NoError(t, err)
	require.NoError(t, closeUpstream.Close())

	select {
	case <-endChan:
	case <-time.After(time.Second):
		require.Fail(t, "xunfei stream producer did not signal stop after close")
	}
}

func TestXunfeiMakeRequestClosesConnectedUpstreamOnRequestCancel(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authURL := "ws" + strings.TrimPrefix(server.URL, "http")
	_, endChan, closeUpstream, err := xunfeiMakeRequest(ctx, &relaycommon.RelayInfo{}, dto.GeneralOpenAIRequest{}, "general", authURL, "app")
	require.NoError(t, err)
	defer closeUpstream.Close()

	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		require.Fail(t, "xunfei upstream did not receive the request")
	}
	cancel()

	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		require.Fail(t, "xunfei upstream connection stayed open after request cancellation")
	}
	select {
	case <-endChan:
	case <-time.After(time.Second):
		require.Fail(t, "xunfei stream producer did not stop after request cancellation")
	}
}
