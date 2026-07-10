package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
	assert.Equal(t, types.ErrorCodeFirstByteTimeout, apiErr.GetErrorCode())

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
	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	smart_routing_setting.ResetForTest()
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	routingSetting.Consecutive5xx = 1
	routingSetting.BaseCooldownSec = 1
	routingSetting.MaxCooldownSec = 1
	smart_routing_setting.UpdateSetting(routingSetting)
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
		smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
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
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 32, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, now.Add(4500*time.Millisecond).Unix(), cached.CooldownUntilUnix)
}

func TestRecordRoutingBreakerAttemptAlsoUpdatesAggregateForMultiKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	routinghotcache.ResetForTest()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
		Now: func() time.Time {
			return now
		},
	})
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	apiErr := types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	recordRoutingBreakerAttempt(ctx, info, 34, apiErr)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 2)
	cachedAggregate, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cachedAggregate.State)
	cachedKey, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cachedKey.State)
}

func TestRecordRoutingBreakerAttemptUsesSmartRoutingBreakerSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		Consecutive5xx:     2,
		FailureRatePct:     90,
		BaseCooldownSec:    7,
		MaxCooldownSec:     7,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		SyncIntervalMin:    1,
		HotcacheRefreshSec: 1,
	})
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelIsMultiKey: false},
	}
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	recordRoutingBreakerAttempt(ctx, info, 33, apiErr)
	recordRoutingBreakerAttempt(ctx, info, 33, apiErr)

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 33, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cached.State)
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

func TestRoutingRetryBackoffDurationCapsRetryAfterAndJitter(t *testing.T) {
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		BackoffBaseMs429: 1000,
		BackoffCapMs:     20_000,
	}
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 120_000})
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	apiErr.Metadata = metadata

	assert.Equal(t, 20*time.Second, routingRetryBackoffDuration(setting, apiErr, 5, 1))
}

func TestRoutingRetryBackoffDurationCapsHugeRetryAfterBeforeDurationConversion(t *testing.T) {
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		BackoffBaseMs429: 1000,
		BackoffCapMs:     20_000,
	}
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 1 << 62})
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	apiErr.Metadata = metadata

	assert.Equal(t, 20*time.Second, routingRetryBackoffDuration(setting, apiErr, 1, 0))
}

func TestRelayInvalidRequestReleasesReservedHalfOpenProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
	key := routingbreaker.Key{ChannelID: 35, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       key,
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: now,
	}})
	_, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(key, 1)
	require.True(t, acquired)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(ctx, constant.ContextKeyRoutingHalfOpenProbes, map[routingbreaker.Key]struct{}{key: {}})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	Relay(ctx, types.RelayFormatOpenAI)

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 35, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Zero(t, cached.HalfOpenInflight)
}
