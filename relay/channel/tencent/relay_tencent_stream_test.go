package tencent

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

type tencentFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

func (w *tencentFailOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newTencentStreamTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)

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
	return ctx, recorder, info
}

func TestTencentStreamHandlerLeavesMalformedFirstChunkUncommitted(t *testing.T) {
	ctx, recorder, info := newTencentStreamTestContext(t)
	body := "data: {malformed\n\n" +
		`data: {"Choices":[{"Delta":{"Role":"assistant","Content":"late"}}]}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := tencentStreamHandler(ctx, info, resp)

	assert.Nil(t, usage)
	assert.Nil(t, apiErr)
	assert.False(t, ctx.Writer.Written())
	assert.Empty(t, recorder.Body.String())
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
}

func TestTencentStreamHandlerReturnsPartialUsageOnScannerError(t *testing.T) {
	ctx, recorder, info := newTencentStreamTestContext(t)
	readErr := errors.New("tencent upstream read failed")
	validChunk := `data: {"Choices":[{"Delta":{"Role":"assistant","Content":"partial response"}}]}` + "\n\n"
	reader := io.MultiReader(strings.NewReader(validChunk), iotest.ErrReader(readErr))
	resp := &http.Response{Body: io.NopCloser(reader)}

	usage, apiErr := tencentStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, readErr)
	assert.Contains(t, recorder.Body.String(), "partial response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestTencentStreamHandlerStopsAfterDownstreamWriteFailure(t *testing.T) {
	ctx, recorder, info := newTencentStreamTestContext(t)
	writeErr := errors.New("tencent downstream write failed")
	writer := &tencentFailOnMarkerWriter{
		ResponseWriter: ctx.Writer,
		marker:         []byte("second response"),
		err:            writeErr,
	}
	ctx.Writer = writer
	body := `data: {"Choices":[{"Delta":{"Role":"assistant","Content":"first response"}}]}` + "\n\n" +
		`data: {"Choices":[{"Delta":{"Role":"assistant","Content":"second response"}}]}` + "\n\n" +
		`data: {"Choices":[{"Delta":{"Role":"assistant","Content":"late response"}}]}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := tencentStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Contains(t, recorder.Body.String(), "first response")
	assert.NotContains(t, recorder.Body.String(), "late response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestTencentStreamHandlerReturnsPartialUsageWhenDoneWriteFails(t *testing.T) {
	ctx, _, info := newTencentStreamTestContext(t)
	writeErr := errors.New("tencent done write failed")
	writer := &tencentFailOnMarkerWriter{
		ResponseWriter: ctx.Writer,
		marker:         []byte("[DONE]"),
		err:            writeErr,
	}
	ctx.Writer = writer
	body := `data: {"Choices":[{"Delta":{"Role":"assistant","Content":"partial response"}}]}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := tencentStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestTencentStreamHandlerStopsOnProviderErrorFrame(t *testing.T) {
	ctx, recorder, info := newTencentStreamTestContext(t)
	body := `data: {"Choices":[{"Delta":{"Role":"assistant","Content":"partial response"}}]}` + "\n\n" +
		`data: {"Error":{"Code":1234,"Message":"tencent provider rejected request"}}` + "\n\n" +
		`data: {"Choices":[{"Delta":{"Role":"assistant","Content":"late response"}}]}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	usage, apiErr := tencentStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "tencent provider rejected request")
	assert.Contains(t, recorder.Body.String(), "partial response")
	assert.NotContains(t, recorder.Body.String(), "late response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}
