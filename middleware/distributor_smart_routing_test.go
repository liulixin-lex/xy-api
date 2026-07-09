package middleware

import (
	"net/http/httptest"
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

func TestSetupContextForSelectedChannelSkipsFreshOpenMultiKeyIndex(t *testing.T) {
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
	now := common.GetTimestamp()
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 9201, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:             routingselector.BreakerStateOpen,
		CooldownUntilUnix: now + 60,
		UpdatedUnix:       now,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9201,
		Key: "open-key\nhealthy-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "healthy-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelFailsWhenAllMultiKeysAreRoutingFiltered(t *testing.T) {
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
	now := common.GetTimestamp()
	for index := range 2 {
		routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 9202, APIKeyIndex: index, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
			State:             routingselector.BreakerStateOpen,
			CooldownUntilUnix: now + 60,
			UpdatedUnix:       now,
		})
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9202,
		Key: "open-key-0\nopen-key-1",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.NotNil(t, err)
	assert.Empty(t, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
}

func TestSetupContextForSelectedChannelSkipsMultiKeyHalfOpenWhenProbeLimitReached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		HalfOpenProbes:   1,
		SnapshotStaleSec: 300,
	})

	breakerKey := routingbreaker.Key{ChannelID: 9203, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:           breakerKey,
		State:         routingbreaker.StateHalfOpen,
		EjectionCount: 1,
		UpdatedAt:     time.Now(),
	}})
	_, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(breakerKey, 1)
	require.True(t, acquired)
	now := common.GetTimestamp()
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 9203, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:            routingselector.BreakerStateHalfOpen,
		HalfOpenInflight: 1,
		UpdatedUnix:      now,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9203,
		Key: "probing-key\nhealthy-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "healthy-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelReservesAndReleasesMultiKeyHalfOpenProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		HalfOpenProbes:   1,
		SnapshotStaleSec: 300,
	})

	breakerKey := routingbreaker.Key{ChannelID: 9204, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:           breakerKey,
		State:         routingbreaker.StateHalfOpen,
		EjectionCount: 1,
		UpdatedAt:     time.Now(),
	}})
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 9204, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:       routingselector.BreakerStateHalfOpen,
		UpdatedUnix: common.GetTimestamp(),
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9204,
		Key: "probing-key\nhealthy-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "probing-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.Equal(t, 0, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 9204, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, int64(1), cached.HalfOpenInflight)

	service.ReleaseRoutingHalfOpenProbe(ctx, 9204, "gpt-test", "vip")

	cached, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 9204, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Zero(t, cached.HalfOpenInflight)
}
