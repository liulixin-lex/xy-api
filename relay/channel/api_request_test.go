package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var smartRoutingSettingTestMu sync.Mutex

type failingPingWriter struct {
	gin.ResponseWriter
	err error
}

func (w *failingPingWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

type panickingPingWriter struct {
	gin.ResponseWriter
}

func (w *panickingPingWriter) Write(_ []byte) (int, error) {
	panic("ping writer exploded")
}

type testWssAdaptor struct {
	url string
}

func (a testWssAdaptor) Init(info *relaycommon.RelayInfo) {}

func (a testWssAdaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return a.url, nil
}

func (a testWssAdaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	return nil
}

func (a testWssAdaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	return nil, nil
}

func (a testWssAdaptor) GetModelList() []string { return nil }

func (a testWssAdaptor) GetChannelName() string { return "test" }

func (a testWssAdaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, nil
}

func (a testWssAdaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, nil
}

func TestStartPingKeepAlivePropagatesFailureAndCancelsAttempt(t *testing.T) {
	tests := []struct {
		name       string
		writer     func(gin.ResponseWriter) gin.ResponseWriter
		wantReason relaycommon.StreamEndReason
	}{
		{
			name: "write failure",
			writer: func(writer gin.ResponseWriter) gin.ResponseWriter {
				return &failingPingWriter{ResponseWriter: writer, err: errors.New("ping write failed")}
			},
			wantReason: relaycommon.StreamEndReasonPingFail,
		},
		{
			name: "writer panic",
			writer: func(writer gin.ResponseWriter) gin.ResponseWriter {
				return &panickingPingWriter{ResponseWriter: writer}
			},
			wantReason: relaycommon.StreamEndReasonPanic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			attemptCtx, cancelAttempt := context.WithCancel(context.Background())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(attemptCtx)
			ctx.Writer = tt.writer(ctx.Writer)
			info := &relaycommon.RelayInfo{IsStream: true}

			stop, done, errChan := startPingKeepAlive(ctx, info, time.Millisecond, cancelAttempt)
			var pingErr error
			select {
			case pingErr = <-errChan:
			case <-time.After(time.Second):
				require.Fail(t, "ping failure was not propagated")
			}
			stop()
			<-done

			require.Error(t, pingErr)
			assert.ErrorIs(t, attemptCtx.Err(), context.Canceled)
			require.NotNil(t, info.StreamStatus)
			assert.Equal(t, tt.wantReason, info.StreamStatus.EndReason)
			assert.True(t, info.StreamStatus.HasErrors())
		})
	}
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}

func TestShouldStartPreResponseStreamPing_DisabledDuringFirstByteFailover(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	defer smart_routing_setting.ResetForTest()

	info := &relaycommon.RelayInfo{
		IsStream:        true,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 123,
		},
	}

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           3000,
		FirstByteCapMs:           12000,
		FirstByteP95Multiplier:   2,
	})

	assert.False(t, shouldStartPreResponseStreamPing(info))

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeObserve,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           3000,
		FirstByteCapMs:           12000,
		FirstByteP95Multiplier:   2,
	})

	assert.True(t, shouldStartPreResponseStreamPing(info))
}

func TestDoRequestUsesFirstByteTimeoutBeforeResponseHeaders(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	req.Body = http.NoBody
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 124},
	}

	resp, err := doRequest(ctx, req, info)

	require.Nil(t, resp)
	require.Error(t, err)
	assert.Empty(t, recorder.Body.String())
}

func TestDoRequestKeepsStreamAliveAfterResponseHeadersUntilFirstBodyByte(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           100,
		FirstByteCapMs:           100,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("data: ready\n\n"))
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 127},
	}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "data: ready\n\n", string(body))
	assert.NotEqual(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
}

func TestDoRequestTimesOutFirstBodyByteAfterResponseHeaders(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("data: late\n\n"))
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 128},
	}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	_, readErr := io.ReadAll(resp.Body)
	require.Error(t, readErr)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
}

func TestDoRequestFirstByteDeadlineIsNotRestartedByProductionScanner(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           40,
		FirstByteCapMs:           40,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	defer func() { constant.StreamingTimeout = previousStreamingTimeout }()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("data: late\n\n"))
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 129},
	}

	start := time.Now()
	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	helper.StreamScannerHandler(ctx, resp, info, func(data string, result *helper.StreamResult) {
		result.Error(errors.New("late data must not reach the production scanner handler"))
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Less(t, time.Since(start), 120*time.Millisecond)
	assert.Empty(t, recorder.Body.String())
}

func TestDoRequestSSEKeepaliveDoesNotSatisfyFirstBusinessEventDeadline(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           50,
		FirstByteCapMs:           50,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	defer func() { constant.StreamingTimeout = previousStreamingTimeout }()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte(": keepalive\n\nevent: ping\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 130},
	}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	helper.StreamScannerHandler(ctx, resp, info, func(data string, result *helper.StreamResult) {
		result.Error(fmt.Errorf("transport-only SSE frame reached business handler: %s", data))
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Empty(t, recorder.Body.String())
}

func TestDoRequestDroppedDataFrameDoesNotSatisfyFirstBusinessEventDeadline(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           50,
		FirstByteCapMs:           50,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	defer func() { constant.StreamingTimeout = previousStreamingTimeout }()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\"}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 131},
	}

	start := time.Now()
	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	helper.StreamScannerHandler(ctx, resp, info, func(data string, result *helper.StreamResult) {
		if strings.Contains(data, `"content"`) {
			if writeErr := helper.StringData(ctx, data); writeErr != nil {
				result.Error(writeErr)
			}
		}
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Less(t, time.Since(start), 150*time.Millisecond)
	assert.Zero(t, info.ReceivedResponseCount)
	assert.Empty(t, recorder.Body.String())
}

func TestDoRequestSSEEndBeforeBusinessEventRemainsPreCommitFailure(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           200,
		FirstByteCapMs:           200,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	defer func() { constant.StreamingTimeout = previousStreamingTimeout }()
	service.InitHttpClient()

	tests := []struct {
		name string
		body string
	}{
		{name: "zero length response"},
		{name: "done only", body: "data: [DONE]\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if test.body == "" {
					w.Header().Set("Content-Length", "0")
				}
				w.WriteHeader(http.StatusOK)
				if test.body != "" {
					_, _ = io.WriteString(w, test.body)
				}
			}))
			t.Cleanup(server.Close)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req, err := http.NewRequest(http.MethodPost, server.URL, nil)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{
				IsStream: true, OriginModelName: "gpt-test", UsingGroup: "default",
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 132},
			}

			resp, err := doRequest(ctx, req, info)
			require.NoError(t, err)
			require.NotNil(t, resp)
			marker, ok := resp.Body.(interface{ RoutingMarkFirstByte() bool })
			require.True(t, ok)
			helper.StreamScannerHandler(ctx, resp, info, func(data string, result *helper.StreamResult) {
				result.Stop(fmt.Errorf("empty SSE response reached business handler: %s", data))
			})

			require.NotNil(t, info.StreamStatus)
			assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
			assert.ErrorContains(t, info.StreamStatus.EndError, "before the first business event")
			assert.False(t, marker.RoutingMarkFirstByte())
			assert.Zero(t, info.SendResponseCount)
			assert.Zero(t, info.ReceivedResponseCount)
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestDoRequestDoesNotUseFirstByteTimeoutForNonStream(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()
	service.InitHttpClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	req.Body = http.NoBody
	info := &relaycommon.RelayInfo{
		IsStream:        false,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 125},
	}

	resp, err := doRequest(ctx, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDoRequestCancelsBlockedUpstreamWhenStrictCapacityRenewalFails(t *testing.T) {
	service.InitHttpClient()
	upstreamStarted := make(chan struct{}, 1)
	upstreamCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamStarted <- struct{}{}
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	t.Cleanup(server.Close)

	coordinator := channelrouting.NewStrictCapacityCoordinator(&requestCancelStrictCapacityRedis{})
	reservation, err := coordinator.TryReserve(context.Background(), channelrouting.StrictCapacityRequest{
		Key:            channelrouting.StrictCapacityKey{AccountID: 9001, CredentialID: 9002, Model: "cancel-test"},
		PoolID:         1,
		PolicyRevision: 1,
		Demand:         channelrouting.StrictCapacityDemand{RPM: 1, Inflight: 1},
		Limit:          channelrouting.StrictCapacityLimit{RPM: 10, Inflight: 1},
		PoolShares: []channelrouting.StrictCapacityPoolShare{
			{PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000},
		},
		LeaseTTL: time.Second,
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.NoError(t, service.SetRoutingStrictCapacityReservation(ctx, reservation))
	require.NoError(t, service.CommitRoutingCapacityReservation(ctx))
	t.Cleanup(func() { _ = service.ReleaseRoutingCapacityReservation(ctx) })

	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, requestErr := doRequest(ctx, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
		result <- requestErr
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		require.Fail(t, "upstream request did not start")
	}
	select {
	case requestErr := <-result:
		require.Error(t, requestErr)
	case <-time.After(2 * time.Second):
		require.Fail(t, "upstream request did not stop after strict-capacity renewal failure")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		require.Fail(t, "upstream server did not observe request cancellation")
	}
	assert.ErrorIs(t, context.Cause(ctx.Request.Context()), channelrouting.ErrStrictCapacityUnavailable)
}

type requestCancelStrictCapacityRedis struct{}

func (*requestCancelStrictCapacityRedis) Eval(
	_ context.Context,
	script string,
	_ []string,
	args ...interface{},
) *redis.Cmd {
	if strings.Contains(script, "strict_capacity_reserve_v2") {
		now := time.Now().UnixMilli()
		lease, _ := args[1].(int64)
		return redis.NewCmdResult([]interface{}{int64(1), now, now + lease}, nil)
	}
	if strings.Contains(script, "current_expires") {
		return redis.NewCmdResult(nil, errors.New("redis unavailable"))
	}
	return redis.NewCmdResult(int64(1), nil)
}

func TestDoWssRequestUsesFirstByteTimeoutBeforeUpstreamHandshake(t *testing.T) {
	smartRoutingSettingTestMu.Lock()
	defer smartRoutingSettingTestMu.Unlock()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	defer smart_routing_setting.ResetForTest()

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		OriginModelName: "gpt-realtime",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 126},
	}

	start := time.Now()
	conn, err := DoWssRequest(testWssAdaptor{url: "ws" + server.URL[len("http"):]}, ctx, info, nil)

	require.Nil(t, conn)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 80*time.Millisecond)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
}
