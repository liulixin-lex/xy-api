package openai

import (
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dataThenErrorReader struct {
	data []byte
	err  error
}

type countingOpenAIStreamWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

func (w *countingOpenAIStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func (r *dataThenErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestOaiStreamHandlerFirstByteTimeoutDoesNotFinalize(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

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

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         501,
			UpstreamModelName: "gpt-test",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.Nil(t, apiErr)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Zero(t, info.SendResponseCount)
	assert.Empty(t, recorder.Body.String())
}

func TestOaiStreamHandlerPreCommitSoftErrorDoesNotFinalize(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
		},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {malformed\n\n")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	var syntaxErr *stdjson.SyntaxError
	require.ErrorAs(t, apiErr, &syntaxErr)
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.True(t, info.HTTPStreamFailedBeforeCommit(c))
	assert.False(t, c.Writer.Written())
	assert.Empty(t, recorder.Body.String())
}

func TestOaiStreamHandlerCommittedSoftErrorReturnsPartialUsageWithoutFinalizing(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial output"},"finish_reason":null}]}`,
		`data: {malformed`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
		},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
	info.SetEstimatePromptTokens(7)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	var syntaxErr *stdjson.SyntaxError
	require.ErrorAs(t, apiErr, &syntaxErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, c.Writer.Written())
	assert.Contains(t, recorder.Body.String(), "partial output")
	assert.NotContains(t, recorder.Body.String(), "{malformed")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiStreamHandlerCommittedScannerErrorReturnsPartialUsageAndCause(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"second"},"finish_reason":null}]}`,
		``,
	}, "\n")
	scannerErr := errors.New("upstream scanner failed")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-test",
		},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
	info.SetEstimatePromptTokens(7)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(&dataThenErrorReader{
			data: []byte(body),
			err:  scannerErr,
		}),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, scannerErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, c.Writer.Written())
	assert.Contains(t, recorder.Body.String(), "first")
	assert.NotContains(t, recorder.Body.String(), "second")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiStreamHandlerReturnsUsageWhenThinkingTransitionWriteFails(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_think","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"reasoning_content":"reasoning"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_think","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"final answer"},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writeErr := errors.New("thinking transition write failed")
	c.Writer = &failOnMarkerWriter{ResponseWriter: c.Writer, marker: []byte(`\u003c/think\u003e`), err: writeErr}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
	info.ChannelSetting.ThinkingToContent = true
	info.ThinkingContentInfo.IsFirstThinkingContent = true
	info.SetEstimatePromptTokens(7)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, writeErr)
	assert.Contains(t, recorder.Body.String(), "reasoning")
	assert.NotContains(t, recorder.Body.String(), "final answer")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiStreamHandlerReturnsUsageWhenLastChunkWriteFails(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_last","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_last","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"final answer"},"finish_reason":null}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writeErr := errors.New("last chunk write failed")
	c.Writer = &failOnMarkerWriter{ResponseWriter: c.Writer, marker: []byte("final answer"), err: writeErr}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
	info.SetEstimatePromptTokens(7)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, writeErr)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "final answer")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiResponsesStreamHandlerCommittedScannerErrorReturnsPartialUsageAndCause(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	body := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n"
	scannerErr := errors.New("responses scanner failed")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		DisablePing: true,
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAIResponses,
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(&dataThenErrorReader{
			data: []byte(body),
			err:  scannerErr,
		}),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	require.ErrorIs(t, apiErr, scannerErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), "response.completed")
}

func TestOpenaiTTSHandlerPreCommitUsageParseErrorDoesNotWrite(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

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

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         501,
			UpstreamModelName: "gpt-test",
		},
		DisablePing:     true,
		IsStream:        true,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {\"usage\":malformed\n\n")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage := OpenaiTTSHandler(c, resp, info)

	require.NotNil(t, usage)
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.HTTPStreamFailedBeforeCommit(c))
	assert.False(t, c.Writer.Written())
	assert.Empty(t, recorder.Body.String())
}

func TestOpenaiTTSHandlerStopsAfterDownstreamWriteFailure(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil)
	writer := &countingOpenAIStreamWriter{ResponseWriter: c.Writer, err: errors.New("tts downstream write failed")}
	c.Writer = writer
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "tts-test"},
		DisablePing: true,
		IsStream:    true,
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("data: audio-chunk\n\n", 3))),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage := OpenaiTTSHandler(c, resp, info)

	require.NotNil(t, usage)
	assert.Equal(t, 1, writer.writes)
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
}
