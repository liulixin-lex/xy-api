package baidu

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaiduStreamHandlerLeavesMalformedFirstChunkUncommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		IsStream:          true,
		DisablePing:       true,
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: {malformed\n\n"))}

	apiErr, usage := baiduStreamHandler(ctx, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.False(t, ctx.Writer.Written())
	assert.Empty(t, recorder.Body.String())
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestBaiduStreamHandlerReturnsPartialUsageAfterCommittedFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		IsStream:          true,
		DisablePing:       true,
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
	}
	body := `data: {"id":"chatcmpl-test","created":1,"result":"partial","usage":{"prompt_tokens":2,"total_tokens":5}}` + "\n\n" +
		"data: {malformed\n\n" +
		`data: {"id":"chatcmpl-test","created":1,"result":"late","usage":{"prompt_tokens":4,"total_tokens":9}}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	apiErr, usage := baiduStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "invalid character")
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.True(t, ctx.Writer.Written())
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "late")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestBaiduStreamHandlerEstimatesPartialUsageAfterCommittedFailureWithoutUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

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
			UpstreamModelName: "ernie-test",
		},
	}
	info.SetEstimatePromptTokens(4)
	const firstResult = "first partial response "
	const secondResult = "with a second chunk"
	body := `data: {"id":"chatcmpl-test","created":1,"result":"` + firstResult + `"}` + "\n\n" +
		`data: {"id":"chatcmpl-test","created":1,"result":"` + secondResult + `"}` + "\n\n" +
		"data: {malformed\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	apiErr, usage := baiduStreamHandler(ctx, info, resp)

	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	expectedCompletionTokens := service.EstimateTokenByModel(info.UpstreamModelName, firstResult+secondResult)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, expectedCompletionTokens, usage.CompletionTokens)
	assert.Equal(t, 4+expectedCompletionTokens, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), firstResult)
	assert.Contains(t, recorder.Body.String(), secondResult)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}
