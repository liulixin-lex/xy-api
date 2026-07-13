package zhipu

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

const (
	zhipuDataChunk = "data: partial response\n"
	zhipuMetaChunk = `meta: {"request_id":"req-test","usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n"
)

type zhipuFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

type zhipuCloseNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (w *zhipuCloseNotifyRecorder) CloseNotify() <-chan bool {
	return w.closed
}

func (w *zhipuFailOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newZhipuStreamTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(&zhipuCloseNotifyRecorder{
		ResponseRecorder: recorder,
		closed:           make(chan bool),
	})
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
	return ctx, recorder, info
}

func TestZhipuStreamHandlerLeavesMalformedFirstMetaUncommitted(t *testing.T) {
	ctx, recorder, info := newZhipuStreamTestContext(t)
	body := "meta: {malformed\n" + "data: late response\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := zhipuStreamHandler(ctx, info, resp)

	assert.Nil(t, usage)
	assert.Nil(t, apiErr)
	assert.False(t, ctx.Writer.Written())
	assert.Empty(t, recorder.Body.String())
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestZhipuStreamHandlerReturnsUsageOnCommittedParseError(t *testing.T) {
	ctx, recorder, info := newZhipuStreamTestContext(t)
	body := zhipuDataChunk + zhipuMetaChunk + "meta: {malformed\n" + "data: late response\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := zhipuStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.Contains(t, apiErr.Cause().Error(), "invalid character")
	assert.Contains(t, recorder.Body.String(), "partial response")
	assert.NotContains(t, recorder.Body.String(), "late response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestZhipuStreamHandlerReturnsUsageOnScannerError(t *testing.T) {
	ctx, recorder, info := newZhipuStreamTestContext(t)
	readErr := errors.New("zhipu upstream read failed")
	reader := io.MultiReader(strings.NewReader(zhipuDataChunk), iotest.ErrReader(readErr))
	resp := &http.Response{Body: io.NopCloser(reader)}

	usage, apiErr := zhipuStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	assert.Positive(t, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, readErr)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestZhipuStreamHandlerReturnsUsageOnDownstreamWriteError(t *testing.T) {
	ctx, recorder, info := newZhipuStreamTestContext(t)
	writeErr := errors.New("zhipu downstream write failed")
	ctx.Writer = &zhipuFailOnMarkerWriter{
		ResponseWriter: ctx.Writer,
		marker:         []byte(`"finish_reason":"stop"`),
		err:            writeErr,
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(zhipuDataChunk + zhipuMetaChunk))}

	usage, apiErr := zhipuStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Contains(t, recorder.Body.String(), "partial response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestZhipuStreamHandlerReturnsUsageWhenDoneWriteFails(t *testing.T) {
	ctx, _, info := newZhipuStreamTestContext(t)
	writeErr := errors.New("zhipu done write failed")
	ctx.Writer = &zhipuFailOnMarkerWriter{
		ResponseWriter: ctx.Writer,
		marker:         []byte("[DONE]"),
		err:            writeErr,
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(zhipuDataChunk + zhipuMetaChunk))}

	usage, apiErr := zhipuStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}
