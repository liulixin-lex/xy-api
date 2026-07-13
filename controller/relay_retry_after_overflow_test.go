package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRetryAfterOverflowRoutingTest(t *testing.T, channelID int) (*gin.Context, *relaycommon.RelayInfo, routinghotcache.Key, time.Duration) {
	t.Helper()

	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.Mode = smart_routing_setting.ModeObserve
	setting.Consecutive5xx = 1
	setting.MinVolume = 1
	setting.FailureRatePct = 100
	setting.BaseCooldownSec = 1
	setting.MaxCooldownSec = 2
	setting.BackoffBaseMs429 = 1000
	smart_routing_setting.UpdateSetting(setting)

	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smartRoutingBreakerConfigMu.Lock()
	smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	smartRoutingBreakerConfigMu.Unlock()
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smartRoutingBreakerConfigMu.Lock()
		smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
		smartRoutingBreakerConfigMu.Unlock()
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "overflow-test")
	info := &relaycommon.RelayInfo{
		UsingGroup:      "overflow-test",
		OriginModelName: "gpt-overflow-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         channelID,
			ChannelIsMultiKey: false,
		},
	}
	key := routinghotcache.Key{
		ChannelID:   channelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       info.OriginModelName,
		Group:       info.UsingGroup,
	}
	return ctx, info, key, time.Duration(setting.MaxCooldownSec) * time.Second
}

func TestRelayHugeRetryAfterRecordsCappedCapacityCooldown(t *testing.T) {
	const channelID = 801
	ctx, info, key, maxCooldown := setupRetryAfterOverflowRoutingTest(t, channelID)
	response := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"1e20"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"temporarily unavailable","type":"server_error","code":"server_error"}}`,
		)),
	}

	apiErr := service.RelayErrorHandler(context.Background(), response, false)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationRelay,
	})
	require.Equal(t, routingerror.CapacityCooldown, classification.CapacityEffect)

	recordRoutingAttemptEffects(ctx, info, channelID, false, apiErr, classification)

	snapshot, ok := routinghotcache.GetCapacityCooldown(key)
	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, snapshot.SourceStatusCode)
	assert.Equal(t, maxCooldown.Milliseconds(), snapshot.RetryAfterMs)
	assert.Equal(t, maxCooldown.Milliseconds(), snapshot.CooldownUntilUnixMilli-snapshot.UpdatedUnixMilli)
}

func TestTaskHugeRetryAfterRecordsCappedCapacityCooldown(t *testing.T) {
	const channelID = 802
	ctx, info, key, maxCooldown := setupRetryAfterOverflowRoutingTest(t, channelID)
	response := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"1e20"}},
	}

	taskErr := service.TaskErrorFromUpstreamResponse(response, errors.New("temporarily unavailable"), time.Now())
	apiErr := taskErrorToAPIError(taskErr)
	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationTaskSubmit,
	})
	require.Equal(t, routingerror.CapacityCooldown, classification.CapacityEffect)

	recordRoutingAttemptEffects(ctx, info, channelID, false, apiErr, classification)

	snapshot, ok := routinghotcache.GetCapacityCooldown(key)
	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, snapshot.SourceStatusCode)
	assert.Equal(t, maxCooldown.Milliseconds(), snapshot.RetryAfterMs)
	assert.Equal(t, maxCooldown.Milliseconds(), snapshot.CooldownUntilUnixMilli-snapshot.UpdatedUnixMilli)
}
