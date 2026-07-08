package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryStopsAfterResponseWasSent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	info := &relaycommon.RelayInfo{SendResponseCount: 1}

	assert.False(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestShouldRetryStopsAfterFirstResponseTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	start := time.Now().Add(-time.Second)
	info := &relaycommon.RelayInfo{
		StartTime:         start,
		FirstResponseTime: start.Add(100 * time.Millisecond),
	}

	assert.False(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestShouldRetryStillRetriesBeforeAnyResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
	}

	assert.True(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestStreamFirstByteTimeoutErrorOnlyBeforeClientWrite(t *testing.T) {
	info := &relaycommon.RelayInfo{
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	apiErr := streamFirstByteTimeoutError(info)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)

	info.SendResponseCount = 1
	assert.Nil(t, streamFirstByteTimeoutError(info))
}

func TestShouldRetryAllowsFirstByteTimeoutBeforeClientWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	assert.True(t, shouldRetry(ctx, info, apiErr, 1))

	info.ReceivedResponseCount = 1
	assert.False(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestShouldRetryAllowsRealtimeFirstByteTimeoutAfterWebSocketUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	_, err := ctx.Writer.Write([]byte("websocket upgrade complete"))
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatOpenAIRealtime,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	assert.True(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestShouldRetryBlocksFirstByteTimeoutAfterHTTPWriterWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	_, err := ctx.Writer.Write([]byte("response already started"))
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatOpenAI,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	assert.False(t, shouldRetry(ctx, info, apiErr, 1))
}

func TestRecordRoutingTaskAttemptCapturesMetricsAndBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	start := time.Now().Add(-1500 * time.Millisecond)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "mj-test",
		StartTime:       start,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelIsMultiKey: false,
		},
	}
	taskErr := &dto.TaskError{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream failed"),
	}

	recordRoutingTaskAttempt(ctx, info, 31, taskErr)

	metrics := routingmetrics.Snapshots()
	require.Len(t, metrics, 1)
	assert.Equal(t, 31, metrics[0].ChannelID)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, metrics[0].APIKeyIndex)
	assert.Equal(t, "mj-test", metrics[0].ModelName)
	assert.Equal(t, "vip", metrics[0].Group)
	assert.Equal(t, int64(1), metrics[0].RequestCount)
	assert.Equal(t, int64(0), metrics[0].SuccessCount)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 1)
	assert.Equal(t, 31, breakers[0].Key.ChannelID)
	assert.Equal(t, routingbreaker.StateOpen, breakers[0].State)
	assert.Equal(t, "5xx", breakers[0].Reason)
}

func TestRecordRoutingBreakerAttemptRespectsRetryAfterMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	routinghotcache.ResetForTest()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 5,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Minute,
		Now: func() time.Time {
			return now
		},
	})
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 4500})
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelIsMultiKey: false,
		},
	}
	apiErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	apiErr.Metadata = metadata

	recordRoutingBreakerAttempt(ctx, info, 32, apiErr)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 1)
	assert.Equal(t, routingbreaker.StateOpen, breakers[0].State)
	assert.Equal(t, now.Add(4500*time.Millisecond), breakers[0].CooldownUntil)
}

func TestRoutingRetryBackoffDurationUsesExponentialJitterAndRetryAfter(t *testing.T) {
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		BackoffBaseMs5xx: 50,
		BackoffBaseMs429: 1000,
		BackoffCapMs:     20_000,
	}

	serverErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	assert.Equal(t, 100*time.Millisecond, routingRetryBackoffDuration(setting, serverErr, 2, 0.5))

	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 4500})
	require.NoError(t, err)
	rateLimitErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	rateLimitErr.Metadata = metadata
	assert.Equal(t, 5500*time.Millisecond, routingRetryBackoffDuration(setting, rateLimitErr, 1, 0.5))
}
