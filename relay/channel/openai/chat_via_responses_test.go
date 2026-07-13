package openai

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cancelOnCloseBody struct {
	io.Reader
	cancel context.CancelFunc
	ready  <-chan struct{}
}

func (b *cancelOnCloseBody) Close() error {
	if b.ready != nil {
		<-b.ready
	}
	b.cancel()
	return nil
}

type markerFlushWriter struct {
	gin.ResponseWriter
	marker      []byte
	pending     bytes.Buffer
	flushed     chan struct{}
	flushedOnce sync.Once
}

func (w *markerFlushWriter) Write(data []byte) (int, error) {
	_, _ = w.pending.Write(data)
	n, err := w.ResponseWriter.Write(data)
	return n, err
}

func (w *markerFlushWriter) WriteString(data string) (int, error) {
	_, _ = w.pending.WriteString(data)
	n, err := w.ResponseWriter.WriteString(data)
	return n, err
}

func (w *markerFlushWriter) Flush() {
	w.ResponseWriter.Flush()
	if bytes.Contains(w.pending.Bytes(), w.marker) {
		w.flushedOnce.Do(func() { close(w.flushed) })
	}
	w.pending.Reset()
}

type clearContextWriterAfterFlush struct {
	gin.ResponseWriter
	context    *gin.Context
	flushCount int
	clearAfter int
}

func (w *clearContextWriterAfterFlush) Flush() {
	w.ResponseWriter.Flush()
	w.flushCount++
	if w.flushCount == w.clearAfter {
		w.context.Writer = nil
	}
}

type failOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

func (w *failOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newResponsesChatTestContext(t *testing.T, body string, isStream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "responses-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:           isStream,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	return c, recorder, resp, info
}

func TestOaiResponsesToChatStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

	usage, err := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `"role":"assistant"`)
	require.Contains(t, got, `"content":"hello"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
	require.Contains(t, got, `"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`)
	require.Contains(t, got, `data: [DONE]`)
	requireOrderedSubstrings(t, got,
		`"role":"assistant"`,
		`"content":"hello"`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
		`"finish_reason":"tool_calls"`,
		`"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5`,
		`data: [DONE]`,
	)
}

func TestOaiResponsesToChatStreamHandlerReturnsPartialUsageOnCommittedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial","model":"test-model","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"partial output"}`,
		`data: {"type":"response.failed","response":{"status":"failed"}}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.ChannelMeta.UpstreamModelName = "test-model"

	usage, newAPIError := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, newAPIError)
	require.NotNil(t, usage)
	require.Positive(t, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.Contains(t, recorder.Body.String(), `"content":"partial output"`)
	require.NotContains(t, recorder.Body.String(), `data: [DONE]`)
}

func TestOaiResponsesToChatStreamHandlerPreCommitSoftErrorDoesNotFinalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newResponsesChatTestContext(t, "data: {malformed\n\n", true)

	_, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	var syntaxErr *stdjson.SyntaxError
	require.ErrorAs(t, apiErr, &syntaxErr)
	require.NotNil(t, info.StreamStatus)
	require.True(t, info.StreamStatus.HasErrors())
	require.True(t, info.HTTPStreamFailedBeforeCommit(c))
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestOaiResponsesToChatStreamHandlerReturnsUsageWhenFinalizationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial","model":"test-model","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"partial output"}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	requestCtx, cancel := context.WithCancel(c.Request.Context())
	t.Cleanup(cancel)
	c.Request = c.Request.WithContext(requestCtx)
	flushed := make(chan struct{})
	c.Writer = &markerFlushWriter{
		ResponseWriter: c.Writer,
		marker:         []byte("partial output"),
		flushed:        flushed,
	}
	resp.Body = &cancelOnCloseBody{Reader: strings.NewReader(body), cancel: cancel, ready: flushed}

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)

	require.Error(t, requestCtx.Err())
	require.NotNilf(t, apiErr, "response body=%q context error=%v", recorder.Body.String(), requestCtx.Err())
	require.ErrorIs(t, apiErr, context.Canceled)
	require.NotNil(t, usage)
	require.Positive(t, usage.CompletionTokens)
}

func TestOaiResponsesToChatStreamHandlerReturnsUsageWhenFinalUsageWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial","model":"test-model","created_at":1710000000}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, _, resp, info := newResponsesChatTestContext(t, body, true)
	info.SetEstimatePromptTokens(4)
	writer := &clearContextWriterAfterFlush{
		ResponseWriter: c.Writer,
		context:        c,
		clearAfter:     2,
	}
	c.Writer = writer

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNilf(t, apiErr, "flush count=%d", writer.flushCount)
	require.Contains(t, apiErr.Cause().Error(), "context or writer is nil")
	require.NotNil(t, usage)
	require.Equal(t, 4, usage.PromptTokens)
}

func TestOaiResponsesToChatStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_done","model":"test-model","created_at":1710000000}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	info.ShouldIncludeUsage = false
	info.SetEstimatePromptTokens(4)
	doneErr := errors.New("responses-to-chat done write failed")
	c.Writer = &failOnMarkerWriter{
		ResponseWriter: c.Writer,
		marker:         []byte("[DONE]"),
		err:            doneErr,
	}

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, usage)
	require.Equal(t, 4, usage.PromptTokens)
	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, doneErr)
	require.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiResponsesToChatStreamHandlerCommittedScannerErrorReturnsUsageAndCause(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial","model":"test-model","created_at":1710000000}}`,
		`data: {"type":"response.output_text.delta","delta":"partial output"}`,
		``,
	}, "\n")
	scannerErr := errors.New("responses-to-chat scanner failed")
	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	resp.Body = io.NopCloser(&dataThenErrorReader{data: []byte(body), err: scannerErr})

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, scannerErr)
	require.NotNil(t, usage)
	require.Positive(t, usage.CompletionTokens)
	require.Contains(t, recorder.Body.String(), "partial output")
	require.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiResponsesToChatBufferedStreamHandlerReturnsJSONFromSSE(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"buffered text"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.done","response":{"model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, false)

	usage, err := OaiResponsesToChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.TotalTokens)

	got := recorder.Body.String()
	require.NotContains(t, got, `data:`)
	require.Contains(t, got, `"object":"chat.completion"`)
	require.Contains(t, got, `"content":"buffered text"`)
	require.Contains(t, got, `"name":"lookup"`)
	require.Contains(t, got, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `"finish_reason":"tool_calls"`)
}

func TestOaiChatToResponsesStreamHandlerConvertsSSEOrderAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 5, usage.TotalTokens)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `event: response.created`)
	require.Contains(t, got, `event: response.output_text.delta`)
	require.Contains(t, got, `"delta":"hello"`)
	require.Contains(t, got, `event: response.function_call_arguments.delta`)
	require.Contains(t, got, `"delta":"{\"q\":\"x\"}"`)
	require.Contains(t, got, `event: response.completed`)
	require.Contains(t, got, `"input_tokens":2`)
	require.Contains(t, got, `"output_tokens":3`)
	requireOrderedSubstrings(t, got,
		`event: response.created`,
		`event: response.output_item.added`,
		`event: response.output_text.delta`,
		`event: response.output_item.added`,
		`event: response.function_call_arguments.delta`,
		`event: response.output_text.done`,
		`event: response.function_call_arguments.done`,
		`event: response.completed`,
	)
}

func TestOaiChatToResponsesStreamHandlerReturnsPartialUsageOnCommittedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"partial output"},"finish_reason":null}]}`,
		`data: {"error":{"message":"upstream failed","type":"server_error","code":"server_error"}}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info.ChannelMeta.UpstreamModelName = "test-model"

	usage, newAPIError := OaiChatToResponsesStreamHandler(c, info, resp)

	require.NotNil(t, newAPIError)
	require.NotNil(t, usage)
	require.Positive(t, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.Contains(t, recorder.Body.String(), `event: response.output_text.delta`)
	require.NotContains(t, recorder.Body.String(), `event: response.completed`)
}

func TestOaiChatToResponsesStreamHandlerPreCommitSoftErrorDoesNotFinalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newResponsesChatTestContext(t, "data: {malformed\n\n", true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	_, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	var syntaxErr *stdjson.SyntaxError
	require.ErrorAs(t, apiErr, &syntaxErr)
	require.NotNil(t, info.StreamStatus)
	require.True(t, info.StreamStatus.HasErrors())
	require.True(t, info.HTTPStreamFailedBeforeCommit(c))
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestOaiChatToResponsesStreamHandlerReturnsUsageWhenFinalizationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","created":1710000000,"model":"test-model","choices":[{"index":0,"delta":{"content":"partial output"},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, _, resp, info := newResponsesChatTestContext(t, body, true)
	requestCtx, cancel := context.WithCancel(c.Request.Context())
	t.Cleanup(cancel)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	flushed := make(chan struct{})
	c.Writer = &markerFlushWriter{
		ResponseWriter: c.Writer,
		marker:         []byte("partial output"),
		flushed:        flushed,
	}
	resp.Body = &cancelOnCloseBody{Reader: strings.NewReader(body), cancel: cancel, ready: flushed}

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)

	require.Error(t, requestCtx.Err())
	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, context.Canceled)
	require.NotNil(t, usage)
	require.Positive(t, usage.CompletionTokens)
}

func requireOrderedSubstrings(t *testing.T, s string, parts ...string) {
	t.Helper()

	offset := 0
	for _, part := range parts {
		idx := strings.Index(s[offset:], part)
		require.NotEqualf(t, -1, idx, "missing %q after byte offset %d", part, offset)
		offset += idx + len(part)
	}
}
