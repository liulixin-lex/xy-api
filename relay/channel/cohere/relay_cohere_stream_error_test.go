package cohere

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

type cohereFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker             []byte
	err                error
	failed             bool
	writesAfterFailure int
}

func (w *cohereFailOnMarkerWriter) Write(data []byte) (int, error) {
	if w.failed {
		w.writesAfterFailure++
	}
	if !w.failed && bytes.Contains(data, w.marker) {
		w.failed = true
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newCohereStreamContext(t *testing.T) (*gin.Context, *closeNotifyRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	recorder := &closeNotifyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeCh:          make(chan bool, 1),
	}
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		IsStream:          true,
		DisablePing:       true,
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
		ChannelMeta:       &relaycommon.ChannelMeta{UpstreamModelName: "test-model"},
	}
	return ctx, recorder, info
}

func cohereStreamChunk(content string) string {
	return `{"is_finished":false,"text":"` + content + `"}` + "\n"
}

func TestCohereStreamHandlerStopsOnMalformedChunk(t *testing.T) {
	ctx, recorder, info := newCohereStreamContext(t)
	body := cohereStreamChunk("partial") + "{malformed\n" + cohereStreamChunk("after-error")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := cohereStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "invalid character")
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "after-error")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCohereStreamHandlerStopsOnScannerError(t *testing.T) {
	ctx, recorder, info := newCohereStreamContext(t)
	readErr := errors.New("cohere upstream read failed")
	reader := io.MultiReader(strings.NewReader(cohereStreamChunk("partial")), iotest.ErrReader(readErr))
	resp := &http.Response{Body: io.NopCloser(reader)}

	usage, apiErr := cohereStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, readErr)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCohereStreamHandlerStopsOnDownstreamWriteFailure(t *testing.T) {
	ctx, recorder, info := newCohereStreamContext(t)
	writeErr := errors.New("cohere downstream write failed")
	writer := &cohereFailOnMarkerWriter{ResponseWriter: ctx.Writer, marker: []byte("second"), err: writeErr}
	ctx.Writer = writer
	body := cohereStreamChunk("first") + cohereStreamChunk("second") + cohereStreamChunk("after-error")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := cohereStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.True(t, writer.failed)
	assert.Zero(t, writer.writesAfterFailure)
	assert.Contains(t, recorder.Body.String(), "first")
	assert.NotContains(t, recorder.Body.String(), "after-error")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestCohereStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	ctx, _, info := newCohereStreamContext(t)
	writeErr := errors.New("cohere done write failed")
	writer := &cohereFailOnMarkerWriter{ResponseWriter: ctx.Writer, marker: []byte("[DONE]"), err: writeErr}
	ctx.Writer = writer
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(cohereStreamChunk("partial")))}

	usage, apiErr := cohereStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.True(t, writer.failed)
}
