package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelUsesOperationalMultiKeyStateOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		SnapshotStaleSec: 300,
	})

	now := time.Now()
	for _, apiKeyIndex := range []int{model.RoutingMetricSingleKeyIndex, 1} {
		cacheKey := routinghotcache.Key{ChannelID: 9201, APIKeyIndex: apiKeyIndex, Model: "gpt-test", Group: "vip"}
		routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{
			State:             routingselector.BreakerStateOpen,
			CooldownUntilUnix: now.Add(time.Minute).Unix(),
			UpdatedUnix:       now.Unix(),
		})
		routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{
			SourceStatusCode:       http.StatusTooManyRequests,
			CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
			UpdatedUnixMilli:       now.UnixMilli(),
		})
	}
	breakerKey := routingbreaker.Key{ChannelID: 9201, APIKeyIndex: 1, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       breakerKey,
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: now,
	}})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9201,
		Key: "disabled-key\nenabled-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeyMode:       constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled, 1: common.ChannelStatusEnabled},
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "enabled-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	_, hasProbe := common.GetContextKey(ctx, constant.ContextKeyRoutingHalfOpenProbes)
	_, hasLease := common.GetContextKey(ctx, constant.ContextKeyRoutingHalfOpenLeases)
	assert.False(t, hasProbe)
	assert.False(t, hasLease)

	service.ReleaseRoutingHalfOpenProbe(ctx, channel.Id, "gpt-test", "vip")
	snapshot, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(breakerKey, 1)
	require.True(t, acquired)
	assert.Equal(t, 1, snapshot.HalfOpenInflight)
	snapshot = routingbreaker.ReleaseDefaultHalfOpenProbe(breakerKey)
	assert.Zero(t, snapshot.HalfOpenInflight)
}

func TestSetupContextForSelectedChannelResetsSingleKeyMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 7)
	channel := &model.Channel{Id: 9202, Key: "single-key"}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "single-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetRoutingPromptCostProxyCapturesStreamWithoutConsumingJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		body       string
		wantExists bool
		wantStream bool
	}{
		{name: "true", body: `{"model":"gpt-test","stream":true}`, wantExists: true, wantStream: true},
		{name: "false", body: `{"model":"gpt-test","stream":false}`, wantExists: true, wantStream: false},
		{name: "absent", body: `{"model":"gpt-test"}`, wantExists: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() {
				common.CleanupBodyStorage(ctx)
			})

			setRoutingPromptCostProxy(ctx)

			stream, exists := common.GetContextKey(ctx, constant.ContextKeyIsStream)
			assert.Equal(t, test.wantExists, exists)
			if test.wantExists {
				assert.Equal(t, test.wantStream, stream)
			}

			var replayed struct {
				Model  string `json:"model"`
				Stream *bool  `json:"stream"`
			}
			require.NoError(t, common.UnmarshalBodyReusable(ctx, &replayed))
			assert.Equal(t, "gpt-test", replayed.Model)
			if test.wantExists {
				require.NotNil(t, replayed.Stream)
				assert.Equal(t, test.wantStream, *replayed.Stream)
			} else {
				assert.Nil(t, replayed.Stream)
			}
		})
	}
}
