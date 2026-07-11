package dify

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingFailWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

type difyFailOnDoneWriter struct {
	gin.ResponseWriter
	err error
}

func (w *difyFailOnDoneWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("[DONE]")) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func (w *countingFailWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func TestDifyStreamHandlerDoesNotCommitAfterPreCommitFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	tests := []struct {
		name          string
		body          string
		wantEndReason relaycommon.StreamEndReason
	}{
		{
			name: "malformed first chunk",
			body: "data: {malformed\n\n",
		},
		{
			name:          "provider error event",
			body:          `data: {"event":"error"}` + "\n\n" + strings.Repeat("data: {}\n\n", 20),
			wantEndReason: relaycommon.StreamEndReasonHandlerStop,
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
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tt.body))}

			usage, apiErr := difyStreamHandler(ctx, info, resp)

			require.Nil(t, apiErr)
			assert.Nil(t, usage)
			assert.False(t, ctx.Writer.Written())
			assert.Empty(t, recorder.Body.String())
			require.NotNil(t, info.StreamStatus)
			assert.True(t, info.StreamStatus.HasErrors())
			if tt.wantEndReason != relaycommon.StreamEndReasonNone {
				assert.Equal(t, tt.wantEndReason, info.StreamStatus.EndReason)
			}
		})
	}
}

func TestDifyStreamHandlerDoesNotFinalizeAfterCommittedFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	validChunk := `data: {"event":"message","answer":"partial"}` + "\n\n"
	tests := []struct {
		name        string
		body        string
		wantErrText string
	}{
		{
			name:        "malformed later chunk",
			body:        validChunk + "data: {malformed\n\n",
			wantErrText: "invalid character",
		},
		{
			name:        "provider error after partial response",
			body:        validChunk + `data: {"event":"error"}` + "\n\n" + strings.Repeat("data: {}\n\n", 20),
			wantErrText: "dify error event",
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
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tt.body))}

			usage, apiErr := difyStreamHandler(ctx, info, resp)

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
			assert.True(t, info.StreamStatus.HasErrors())
		})
	}
}

func TestDifyStreamHandlerStopsAfterDownstreamWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writer := &countingFailWriter{ResponseWriter: ctx.Writer, err: errors.New("dify downstream write failed")}
	ctx.Writer = writer
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		StartTime:   time.Now(),
		UserQuota:   1_000_000,
	}
	body := strings.Repeat(`data: {"event":"message","answer":"partial"}`+"\n\n", 3)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := difyStreamHandler(ctx, info, resp)

	assert.Nil(t, usage)
	assert.Nil(t, apiErr)
	assert.Equal(t, 1, writer.writes)
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestDifyStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writeErr := errors.New("dify done write failed")
	ctx.Writer = &difyFailOnDoneWriter{ResponseWriter: ctx.Writer, err: writeErr}
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
		StartTime:   time.Now(),
	}
	body := `data: {"event":"message","answer":"partial"}` + "\n\n" +
		`data: {"event":"message_end","metadata":{"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := difyStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
}
