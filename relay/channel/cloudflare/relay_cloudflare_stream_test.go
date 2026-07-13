package cloudflare

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

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cfFailMatchingWriter struct {
	gin.ResponseWriter
	err                error
	match              []byte
	failed             bool
	writesAfterFailure int
}

func (w *cfFailMatchingWriter) Write(data []byte) (int, error) {
	if w.failed {
		w.writesAfterFailure++
	}
	if !w.failed && bytes.Contains(data, w.match) {
		w.failed = true
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newCFStreamContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:           true,
		ShouldIncludeUsage: false,
		StartTime:          time.Now(),
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
	}
	return ctx, recorder, info
}

func cfChunk(content string) string {
	return `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"cf-test","choices":[{"index":0,"delta":{"content":"` + content + `"}}]}` + "\n\n"
}

func TestCFStreamHandlerStopsOnChunkWriteFailure(t *testing.T) {
	ctx, recorder, info := newCFStreamContext(t)
	writeErr := errors.New("cloudflare chunk write failed")
	writer := &cfFailMatchingWriter{ResponseWriter: ctx.Writer, err: writeErr, match: []byte("second")}
	ctx.Writer = writer
	info.ShouldIncludeUsage = true
	body := cfChunk("first") + cfChunk("second") + "data: [DONE]\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	apiErr, usage := cfStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, writer.failed)
	assert.Zero(t, writer.writesAfterFailure)
	assert.Contains(t, recorder.Body.String(), "first")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCFStreamHandlerStopsOnMalformedChunk(t *testing.T) {
	ctx, recorder, info := newCFStreamContext(t)
	body := cfChunk("first") + "data: {malformed\n\n" + cfChunk("after-error") + "data: [DONE]\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	apiErr, usage := cfStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "invalid character")
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), "first")
	assert.NotContains(t, recorder.Body.String(), "after-error")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCFStreamHandlerStopsOnScannerError(t *testing.T) {
	ctx, recorder, info := newCFStreamContext(t)
	scanErr := errors.New("cloudflare upstream read failed")
	reader := io.MultiReader(strings.NewReader(cfChunk("partial")), iotest.ErrReader(scanErr))
	resp := &http.Response{Body: io.NopCloser(reader)}

	apiErr, usage := cfStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, scanErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCFStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	ctx, _, info := newCFStreamContext(t)
	writeErr := errors.New("cloudflare done write failed")
	writer := &cfFailMatchingWriter{ResponseWriter: ctx.Writer, err: writeErr, match: []byte("[DONE]")}
	ctx.Writer = writer
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(cfChunk("partial") + "data: [DONE]\n\n"))}

	apiErr, usage := cfStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, writer.failed)
}

func TestCFStreamHandlerDoesNotSendDoneAfterFinalUsageWriteFailure(t *testing.T) {
	ctx, recorder, info := newCFStreamContext(t)
	writeErr := errors.New("cloudflare final usage write failed")
	writer := &cfFailMatchingWriter{ResponseWriter: ctx.Writer, err: writeErr, match: []byte(`"choices":[]`)}
	ctx.Writer = writer
	info.ShouldIncludeUsage = true
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(cfChunk("partial") + "data: [DONE]\n\n"))}

	apiErr, usage := cfStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.True(t, writer.failed)
	assert.Zero(t, writer.writesAfterFailure)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}
