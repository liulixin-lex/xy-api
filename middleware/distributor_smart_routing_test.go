package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelUsesOperationalMultiKeyStateOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
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
