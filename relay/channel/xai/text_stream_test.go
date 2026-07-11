package xai

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failOnDoneWriter struct {
	gin.ResponseWriter
	err error
}

func (w *failOnDoneWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("[DONE]")) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func TestXAIStreamHandlerDoesNotCommitAfterPreCommitFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed first chunk", body: "data: {malformed\n\n"},
		{name: "null first chunk", body: "data: null\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			start := time.Now()
			info := &relaycommon.RelayInfo{
				IsStream:          true,
				DisablePing:       true,
				StartTime:         start,
				FirstResponseTime: start.Add(-time.Second),
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "test-model",
				},
			}
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tt.body))}

			usage, apiErr := xAIStreamHandler(ctx, info, resp)

			require.Nil(t, apiErr)
			assert.Nil(t, usage)
			assert.False(t, ctx.Writer.Written())
			assert.Empty(t, recorder.Body.String())
			require.NotNil(t, info.StreamStatus)
			assert.True(t, info.StreamStatus.HasErrors())
		})
	}
}

func TestXAIStreamHandlerDoesNotFinalizeAfterCommittedFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	validChunk := `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"grok-test","choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"
	tests := []struct {
		name        string
		reader      io.Reader
		wantErrText string
	}{
		{
			name:        "malformed later chunk",
			reader:      strings.NewReader(validChunk + "data: {malformed\n\n"),
			wantErrText: "invalid character",
		},
		{
			name:        "null later chunk",
			reader:      strings.NewReader(validChunk + "data: null\n\n"),
			wantErrText: "null stream response",
		},
		{
			name: "scanner error after partial response",
			reader: io.MultiReader(
				strings.NewReader(validChunk),
				iotest.ErrReader(errors.New("upstream read failed")),
			),
			wantErrText: "upstream read failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			start := time.Now()
			info := &relaycommon.RelayInfo{
				IsStream:          true,
				DisablePing:       true,
				StartTime:         start,
				FirstResponseTime: start.Add(-time.Second),
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "test-model",
				},
			}
			resp := &http.Response{Body: io.NopCloser(tt.reader)}

			usage, apiErr := xAIStreamHandler(ctx, info, resp)

			require.NotNil(t, usage)
			assert.Positive(t, usage.CompletionTokens)
			assert.Positive(t, usage.TotalTokens)
			require.NotNil(t, apiErr)
			require.Error(t, apiErr.Cause())
			assert.Contains(t, apiErr.Cause().Error(), tt.wantErrText)
			assert.True(t, ctx.Writer.Written())
			assert.Contains(t, recorder.Body.String(), "partial")
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			require.NotNil(t, info.StreamStatus)
		})
	}
}

func TestXAIStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writeErr := errors.New("xai done write failed")
	ctx.Writer = &failOnDoneWriter{ResponseWriter: ctx.Writer, err: writeErr}
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		StartTime:   time.Now(),
	}
	body := `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"grok-test","choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\ndata: [DONE]\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := xAIStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
}
