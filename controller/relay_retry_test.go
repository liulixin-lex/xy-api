package controller

import (
	"context"
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
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRoutingBreakerAttemptTest(t *testing.T, enabled bool) {
	t.Helper()
	if enabled {
		t.Setenv("SMART_ROUTING_ENABLED", "true")
	} else {
		t.Setenv("SMART_ROUTING_ENABLED", "false")
	}
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = enabled
	setting.Mode = smart_routing_setting.ModeObserve
	setting.Consecutive5xx = 1
	setting.MinVolume = 1
	setting.FailureRatePct = 100
	setting.BaseCooldownSec = 1
	setting.MaxCooldownSec = 60
	setting.BackoffBaseMs429 = 1000
	smart_routing_setting.UpdateSetting(setting)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	resetRoutingBreakerConfigIdentityForTest()

	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
		resetRoutingBreakerConfigIdentityForTest()
	})
}

func resetRoutingBreakerConfigIdentityForTest() {
	smartRoutingBreakerConfigMu.Lock()
	smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	smartRoutingBreakerConfigMu.Unlock()
}

func TestShouldRetryStopsAfterResponseWasSent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	info := &relaycommon.RelayInfo{SendResponseCount: 1}

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
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

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
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

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1))
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

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1))

	info.ReceivedResponseCount = 1
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
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

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1))
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

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryUsesClassificationBeforeStatusOverlay(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{}
	caller := types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest)
	timeout := types.NewErrorWithStatusCode(errors.New("timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout)

	assert.False(t, shouldRetry(ctx, info, caller, routingerror.Classification{Retryability: routingerror.RetryNever}, 1))
	assert.True(t, shouldRetry(ctx, info, timeout, routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}, 1))
}

func TestClassifyRoutingRelayAttemptDistinguishesStreamCorruptionAndClientGone(t *testing.T) {
	corrupted := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	corrupted.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("scanner failed"))
	classification, success := classifyRoutingRelayAttempt(nil, corrupted)
	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
	assert.Equal(t, routingerror.HealthDegrade, classification.HealthEffect)

	clientGone := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	clientGone.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, context.Canceled)
	classification, success = classifyRoutingRelayAttempt(nil, clientGone)
	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
}

func TestClassifyRoutingRelayAttemptMarksCSAMAsContentSafetyBeforeNormalization(t *testing.T) {
	apiErr := types.NewErrorWithStatusCode(errors.New(service.CSAMViolationMarker), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	apiErr.SetResponseStatusCode(http.StatusBadRequest)

	classification, success := classifyRoutingRelayAttempt(apiErr, nil)
	normalized := service.NormalizeViolationFeeError(apiErr)

	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.RetryNever, classification.Retryability)
	assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
	assert.Equal(t, routingerror.CapacityNone, classification.CapacityEffect)
	assert.Equal(t, types.ErrorCodeViolationFeeGrokCSAM, normalized.GetErrorCode())
}

func TestPrepareRoutingRelayAttemptIsolatesStreamStatusPerAttempt(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responsibility routingerror.Responsibility
		scope          routingerror.Scope
		health         routingerror.HealthEffect
		capacity       routingerror.CapacityEffect
	}{
		{name: "capacity", statusCode: http.StatusTooManyRequests, responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown},
		{name: "provider", statusCode: http.StatusBadGateway, responsibility: routingerror.ResponsibilityProvider, scope: routingerror.ScopePoolMember, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone},
		{name: "credential", statusCode: http.StatusUnauthorized, responsibility: routingerror.ResponsibilityCredential, scope: routingerror.ScopeCredential, health: routingerror.HealthOpen, capacity: routingerror.CapacityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, errors.New("previous attempt timed out"))

			prepareRoutingRelayAttempt(info)
			apiErr := types.NewErrorWithStatusCode(errors.New("new attempt failed"), types.ErrorCodeBadResponseStatusCode, tt.statusCode)
			classification, success := classifyRoutingRelayAttempt(apiErr, info)

			assert.False(t, success)
			assert.Equal(t, tt.responsibility, classification.Responsibility)
			assert.Equal(t, tt.scope, classification.Scope)
			assert.Equal(t, tt.health, classification.HealthEffect)
			assert.Equal(t, tt.capacity, classification.CapacityEffect)
		})
	}
}

func TestClassifyRoutingTaskChannelErrorKeepsContentSafety403OutOfAutoDisable(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusForbidden, End: http.StatusForbidden}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
	})

	apiErr := types.NewErrorWithStatusCode(errors.New(service.CSAMViolationMarker), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	classification := classifyRoutingTaskChannelError(apiErr)

	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.ScopeRequest, classification.Scope)
	assert.Equal(t, routingerror.RetryNever, classification.Retryability)
	assert.False(t, service.ShouldDisableChannel(apiErr, classification))
}

func TestRecordRoutingTaskAttemptCapturesMetricsAndBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	configureRoutingBreakerAttemptTest(t, true)
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	routingSetting.Consecutive5xx = 1
	routingSetting.BaseCooldownSec = 1
	routingSetting.MaxCooldownSec = 1
	smart_routing_setting.UpdateSetting(routingSetting)
	routingmetrics.ResetForTest()
	resetRoutingBreakerConfigIdentityForTest()
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
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

func TestRecordRoutingTaskAttemptIgnoresCurrentMultiKeyWithStaleSingleKeyMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	configureRoutingBreakerAttemptTest(t, true)
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	routingSetting.Consecutive5xx = 1
	smart_routing_setting.UpdateSetting(routingSetting)
	routingmetrics.ResetForTest()
	t.Cleanup(routingmetrics.ResetForTest)

	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "mj-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelIsMultiKey: false},
	}
	taskErr := &dto.TaskError{StatusCode: http.StatusBadGateway, Error: errors.New("upstream failed")}

	recordRoutingTaskAttempt(ctx, info, 37, taskErr)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Equal(t, routingmetrics.Stats{}, routingmetrics.RuntimeStats())
	assert.Empty(t, routingbreaker.DirtySnapshots())
	assert.Equal(t, routingbreaker.Stats{}, routingbreaker.RuntimeStats())
}

func singleKeyRoutingAttemptFixture(t *testing.T, channelID int) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	routingmetrics.ResetForTest()
	t.Cleanup(routingmetrics.ResetForTest)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	return ctx, &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID, ChannelIsMultiKey: false},
	}
}

func TestRecordRoutingAttemptEffectsMatrix(t *testing.T) {
	tests := []struct {
		name              string
		sourceStatus      int
		responseStatus    int
		retryAfterMs      int64
		responsibility    routingerror.Responsibility
		health            routingerror.HealthEffect
		capacity          routingerror.CapacityEffect
		wantReliability   bool
		wantCapacity      bool
		wantBreakerReason string
	}{
		{name: "402 is capacity only", sourceStatus: 402, responseStatus: 402, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true},
		{name: "mapped 429 is capacity only", sourceStatus: 429, responseStatus: 503, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true},
		{name: "529 is capacity only", sourceStatus: 529, responseStatus: 529, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true},
		{name: "502 is reliability only", sourceStatus: 502, responseStatus: 502, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone, wantReliability: true, wantBreakerReason: "5xx"},
		{name: "503 retry after has both effects", sourceStatus: 503, responseStatus: 503, retryAfterMs: 2500, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityCooldown, wantReliability: true, wantCapacity: true, wantBreakerReason: "5xx"},
		{name: "caller 400 has neither effect", sourceStatus: 400, responseStatus: 400, responsibility: routingerror.ResponsibilityCaller, health: routingerror.HealthIgnore, capacity: routingerror.CapacityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureRoutingBreakerAttemptTest(t, true)
			ctx, info := singleKeyRoutingAttemptFixture(t, 71)
			apiErr := types.NewErrorWithStatusCode(errors.New("failed"), types.ErrorCodeBadResponseStatusCode, tt.sourceStatus)
			apiErr.SetResponseStatusCode(tt.responseStatus)
			if tt.retryAfterMs > 0 {
				metadata, err := common.Marshal(map[string]int64{"retry_after_ms": tt.retryAfterMs})
				require.NoError(t, err)
				apiErr.Metadata = metadata
			}
			classification := routingerror.Classification{
				Responsibility: tt.responsibility,
				HealthEffect:   tt.health,
				CapacityEffect: tt.capacity,
				Component:      routingerror.ComponentServing,
			}

			recordRoutingAttemptEffects(ctx, info, 71, false, apiErr, classification)

			key := routinghotcache.Key{ChannelID: 71, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
			capacity, hasCapacity := routinghotcache.GetCapacityCooldown(key)
			assert.Equal(t, tt.wantCapacity, hasCapacity)
			if hasCapacity {
				assert.Equal(t, tt.sourceStatus, capacity.SourceStatusCode)
				assert.Greater(t, capacity.CooldownUntilUnixMilli, capacity.UpdatedUnixMilli)
				if tt.retryAfterMs > 0 {
					assert.Equal(t, tt.retryAfterMs, capacity.RetryAfterMs)
				}
			}
			breaker, hasBreaker := routinghotcache.GetBreaker(key)
			assert.Equal(t, tt.wantReliability, hasBreaker)
			if tt.wantBreakerReason != "" && hasBreaker {
				assert.Equal(t, tt.wantBreakerReason, breaker.Reason)
			}
			metrics := routingmetrics.Snapshots()
			require.Len(t, metrics, 1)
			if tt.wantReliability {
				assert.Equal(t, int64(1), metrics[0].ReliabilityRequestCount)
				assert.Equal(t, int64(1), metrics[0].ReliabilityFailureCount)
			} else {
				assert.Zero(t, metrics[0].ReliabilityRequestCount)
				assert.Zero(t, metrics[0].ReliabilityFailureCount)
			}
		})
	}
}

func TestRecordRoutingAttemptEffectsSuccessOnlyRecordsReliability(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 72)
	classification := routingerror.ClassifyAPIError(nil, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 72, true, nil, classification)

	key := routinghotcache.Key{ChannelID: 72, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	_, hasCapacity := routinghotcache.GetCapacityCooldown(key)
	assert.False(t, hasCapacity)
	breaker, hasBreaker := routinghotcache.GetBreaker(key)
	require.True(t, hasBreaker)
	assert.Equal(t, string(routingbreaker.StateHealthy), breaker.State)
	metrics := routingmetrics.Snapshots()
	require.Len(t, metrics, 1)
	assert.Equal(t, int64(1), metrics[0].SuccessCount)
	assert.Equal(t, int64(1), metrics[0].ReliabilityRequestCount)
	assert.Zero(t, metrics[0].ReliabilityFailureCount)
}

func TestRecordRoutingAttemptEffectsDoesNothingWhenDisabled(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, false)
	ctx, info := singleKeyRoutingAttemptFixture(t, 30)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 30, false, apiErr, classification)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())
	key := routinghotcache.Key{ChannelID: 30, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	_, hasBreaker := routinghotcache.GetBreaker(key)
	_, hasCapacity := routinghotcache.GetCapacityCooldown(key)
	assert.False(t, hasBreaker)
	assert.False(t, hasCapacity)
}

func TestRecordRoutingAttemptEffectsIgnoresCurrentMultiKeyWithStaleSingleKeyMeta(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 34)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 2)
	apiErr := types.NewErrorWithStatusCode(errors.New("temporarily unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	apiErr.Metadata = metadata
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 34, false, apiErr, classification)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Equal(t, routingmetrics.Stats{}, routingmetrics.RuntimeStats())
	assert.Empty(t, routingbreaker.DirtySnapshots())
	assert.Equal(t, routingbreaker.Stats{}, routingbreaker.RuntimeStats())
	_, aggregate := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, perKey := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	_, aggregateCapacity := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, perKeyCapacity := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 34, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	assert.False(t, aggregate)
	assert.False(t, perKey)
	assert.False(t, aggregateCapacity)
	assert.False(t, perKeyCapacity)
}

func TestRecordRoutingAttemptEffectsUsesOnlyMinusOneForCurrentSingleKeyWithStaleMultiKeyMeta(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 36)
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 36, ChannelIsMultiKey: true, ChannelMultiKeyIndex: 2}
	apiErr := types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 36, false, apiErr, classification)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 1)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, breakers[0].Key.APIKeyIndex)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 36, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cached.State)
	_, perKey := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 36, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	assert.False(t, perKey)
}

func TestRecordRoutingAttemptEffectsUsesSmartRoutingBreakerSettings(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		Consecutive5xx:     2,
		FailureRatePct:     90,
		BaseCooldownSec:    7,
		MaxCooldownSec:     7,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		SyncIntervalMin:    1,
		HotcacheRefreshSec: 1,
	})
	resetRoutingBreakerConfigIdentityForTest()
	ctx, info := singleKeyRoutingAttemptFixture(t, 33)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 33, false, apiErr, classification)
	recordRoutingAttemptEffects(ctx, info, 33, false, apiErr, classification)

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 33, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cached.State)
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
