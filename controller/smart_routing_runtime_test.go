package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSmartRoutingRuntimeRunsAndStopsWithoutLeakingWorkers(t *testing.T) {
	refreshRan := make(chan struct{}, 1)
	flushRan := make(chan struct{}, 1)
	var refreshCount atomic.Int64
	var flushCount atomic.Int64

	deps := smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            true,
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(smart_routing_setting.SmartRoutingSetting) {
			refreshCount.Add(1)
			refreshRan <- struct{}{}
		},
		flush: func(smart_routing_setting.SmartRoutingSetting) {
			flushCount.Add(1)
			flushRan <- struct{}{}
		},
		waitRefresh: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
		waitFlush: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	}

	runtime := newSmartRoutingRuntime(context.Background(), deps)
	t.Cleanup(runtime.Close)
	select {
	case <-refreshRan:
	case <-time.After(time.Second):
		require.FailNow(t, "smart routing refresh worker did not run")
	}
	select {
	case <-flushRan:
	case <-time.After(time.Second):
		require.FailNow(t, "smart routing flush worker did not run")
	}

	runtime.Close()
	runtime.Close()

	assert.Equal(t, int64(1), refreshCount.Load())
	assert.Equal(t, int64(1), flushCount.Load())
}

func TestSmartRoutingRuntimeDoesNotRunWithCanceledParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	var refreshCount atomic.Int64
	var flushCount atomic.Int64

	runtime := newSmartRoutingRuntime(parent, smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(smart_routing_setting.SmartRoutingSetting) {
			refreshCount.Add(1)
		},
		flush: func(smart_routing_setting.SmartRoutingSetting) {
			flushCount.Add(1)
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
	})
	t.Cleanup(runtime.Close)
	runtime.Close()

	assert.Zero(t, refreshCount.Load())
	assert.Zero(t, flushCount.Load())
}

func TestRoutingBreakerModelsToSnapshotsRejectsLegacySemanticVersion(t *testing.T) {
	states := []model.RoutingBreakerState{
		{ChannelID: 1, APIKeyIndex: -1, ModelName: "legacy", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: 0, UpdatedTime: 100},
		{ChannelID: 2, APIKeyIndex: -1, ModelName: "current", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 100},
		{ChannelID: 3, APIKeyIndex: 2, ModelName: "positive", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 100},
	}

	snapshots := routingBreakerModelsToSnapshots(states)

	require.Len(t, snapshots, 1)
	assert.Equal(t, 2, snapshots[0].Key.ChannelID)
}

func TestRefreshRoutingHotcacheHydratesOnlyCurrentBreakerSemanticVersion(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&model.Channel{Id: 11, Name: "current-single", Key: "single-key"}).Error)
	require.NoError(t, db.Create(&[]model.RoutingBreakerState{
		{ChannelID: 10, APIKeyIndex: -1, ModelName: "legacy", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: 0, UpdatedTime: common.GetTimestamp()},
		{ChannelID: 11, APIKeyIndex: -1, ModelName: "current", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: common.GetTimestamp()},
	}).Error)

	summary, err := refreshRoutingHotcacheFromDB(smart_routing_setting.GetSetting())
	require.NoError(t, err)
	assert.Equal(t, 1, summary["breakers"])
	assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)
}

func TestRefreshRoutingHotcacheIgnoresLegacyMultiKeyAndPositiveIndexRows(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})

	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 21, Name: "single", Key: "single-key"},
		{Id: 22, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}).Error)
	now := common.GetTimestamp()
	type routingKey struct {
		channelID   int
		apiKeyIndex int
		modelName   string
	}
	keys := []routingKey{
		{channelID: 21, apiKeyIndex: model.RoutingMetricSingleKeyIndex, modelName: "single-minus-one"},
		{channelID: 21, apiKeyIndex: 1, modelName: "single-positive"},
		{channelID: 22, apiKeyIndex: model.RoutingMetricSingleKeyIndex, modelName: "multi-minus-one"},
		{channelID: 22, apiKeyIndex: 1, modelName: "multi-positive"},
	}
	metrics := make([]model.RoutingChannelMetric, 0, len(keys))
	breakers := make([]model.RoutingBreakerState, 0, len(keys))
	for _, key := range keys {
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, ModelName: key.modelName,
			Group: "default", BucketTs: now, RequestCount: 1, SuccessCount: 1,
		})
		breakers = append(breakers, model.RoutingBreakerState{
			ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, ModelName: key.modelName,
			Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
			State: model.RoutingBreakerStateOpen, UpdatedTime: now,
		})
	}
	require.NoError(t, db.Create(&metrics).Error)
	require.NoError(t, db.Create(&breakers).Error)

	summary, err := refreshRoutingHotcacheFromDB(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	assert.Equal(t, 1, summary["breakers"])
	assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)

	for _, key := range keys {
		cacheKey := routinghotcache.Key{ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, Model: key.modelName, Group: "default"}
		_, metricOK := routinghotcache.GetMetric(cacheKey)
		_, breakerOK := routinghotcache.GetBreaker(cacheKey)
		want := key.channelID == 21 && key.apiKeyIndex == model.RoutingMetricSingleKeyIndex
		assert.Equal(t, want, metricOK, "metric key %+v", key)
		assert.Equal(t, want, breakerOK, "breaker key %+v", key)
	}
}

func TestRefreshRoutingHotcachePagesPastLegacyMultiKeyRows(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 23, Name: "single", Key: "single-key"},
		{Id: 24, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}).Error)

	const invalidRows = 5000
	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 23, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "valid-single",
		Group: "default", BucketTs: now, RequestCount: 1, SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID: 23, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "valid-single",
		Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
		State: model.RoutingBreakerStateDegraded, UpdatedTime: now,
	}).Error)
	metrics := make([]model.RoutingChannelMetric, 0, invalidRows)
	breakers := make([]model.RoutingBreakerState, 0, invalidRows)
	for i := 0; i < invalidRows; i++ {
		modelName := fmt.Sprintf("legacy-multi-%04d", i)
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID: 24, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: modelName,
			Group: "default", BucketTs: now, RequestCount: 1,
		})
		breakers = append(breakers, model.RoutingBreakerState{
			ChannelID: 24, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: modelName,
			Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
			State: model.RoutingBreakerStateOpen, UpdatedTime: now,
		})
	}
	require.NoError(t, db.CreateInBatches(metrics, 500).Error)
	require.NoError(t, db.CreateInBatches(breakers, 500).Error)
	const callbackName = "test:count_recent_breaker_hydration_pages"
	breakerPageQueries := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.RoutingBreakerState{}).TableName() {
			breakerPageQueries++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := refreshRoutingHotcacheFromDB(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	assert.Equal(t, 1, summary["breakers"])
	cacheKey := routinghotcache.Key{ChannelID: 23, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "valid-single", Group: "default"}
	_, metricOK := routinghotcache.GetMetric(cacheKey)
	_, breakerOK := routinghotcache.GetBreaker(cacheKey)
	assert.True(t, metricOK)
	assert.True(t, breakerOK)
	assert.Equal(t, 2, breakerPageQueries)
}

func TestRefreshRoutingHotcacheDoesNotPageThroughExpiredBreakerHistory(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{EntryTTL: 10 * time.Minute})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 25, Name: "expired-multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 26, Name: "fresh-single", Key: "single-key"},
	}).Error)

	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID: 26, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "fresh-single",
		Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
		State: model.RoutingBreakerStateDegraded, UpdatedTime: now,
	}).Error)
	const expiredRows = 5000
	expired := make([]model.RoutingBreakerState, 0, expiredRows)
	for i := 0; i < expiredRows; i++ {
		expired = append(expired, model.RoutingBreakerState{
			ChannelID: 25, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			ModelName: fmt.Sprintf("expired-multi-%04d", i), Group: "default",
			SemanticVersion: model.RoutingBreakerSemanticVersion,
			State:           model.RoutingBreakerStateOpen,
			UpdatedTime:     now - int64((20*time.Minute)/time.Second),
		})
	}
	require.NoError(t, db.CreateInBatches(expired, 500).Error)

	const callbackName = "test:count_fresh_breaker_hydration_pages"
	breakerPageQueries := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.RoutingBreakerState{}).TableName() {
			breakerPageQueries++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := refreshRoutingHotcacheFromDB(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["breakers"])
	assert.Equal(t, 1, breakerPageQueries)
	assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)

	_, freshOK := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID: 26, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "fresh-single", Group: "default",
	})
	_, expiredOK := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID: 25, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "expired-multi-0000", Group: "default",
	})
	assert.True(t, freshOK)
	assert.False(t, expiredOK)
}

func TestRefreshRoutingHotcacheReturnsEligibilityLookupErrors(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&model.Channel{Id: 27, Name: "single", Key: "single-key"}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 27, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
		Group: "default", BucketTs: common.GetTimestamp(), RequestCount: 1,
	}).Error)

	forcedErr := errors.New("forced refresh eligibility lookup failure")
	const callbackName = "test:fail_refresh_channel_eligibility_query"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	_, err := refreshRoutingHotcacheFromDB(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.ErrorIs(t, err, forcedErr)
}

func TestFlushRoutingRuntimeStateDropsInvalidLegacyRoutingState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 31, Name: "single", Key: "single-key"},
		{Id: 32, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}).Error)

	type routingKey struct {
		channelID   int
		apiKeyIndex int
		modelName   string
	}
	keys := []routingKey{
		{channelID: 31, apiKeyIndex: model.RoutingMetricSingleKeyIndex, modelName: "single-minus-one"},
		{channelID: 31, apiKeyIndex: 1, modelName: "single-positive"},
		{channelID: 32, apiKeyIndex: model.RoutingMetricSingleKeyIndex, modelName: "multi-minus-one"},
		{channelID: 32, apiKeyIndex: 1, modelName: "multi-positive"},
	}
	metrics := make([]model.RoutingChannelMetric, 0, len(keys))
	for _, key := range keys {
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, ModelName: key.modelName,
			Group: "default", BucketTs: 60, RequestCount: 1,
		})
		routingbreaker.RecordAttempt(routingbreaker.Key{
			ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, Model: key.modelName, Group: "default",
		}, false, http.StatusBadGateway, 0)
	}
	routingmetrics.RequeueSnapshots(metrics)

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	assert.Equal(t, 1, summary["breakers"])
	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())

	var metricCount int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Count(&metricCount).Error)
	assert.Equal(t, int64(1), metricCount)
	var breakerCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)
}

func TestFlushRoutingRuntimeStateQueriesEligibilityOncePerChannelAcrossMetricsAndBreakers(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	require.NoError(t, db.Create(&model.Channel{Id: 81, Name: "single", Key: "single-key"}).Error)

	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "metric-a", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "metric-b", Group: "default", BucketTs: 60, RequestCount: 1},
	})
	for _, modelName := range []string{"breaker-a", "breaker-b"} {
		routingbreaker.RecordAttempt(routingbreaker.Key{
			ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: modelName, Group: "default",
		}, false, http.StatusBadGateway, 0)
	}

	const callbackName = "test:count_flush_channel_eligibility_queries"
	channelQueryCount := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" {
			channelQueryCount++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.NoError(t, err)
	assert.Equal(t, 2, summary["metrics"])
	assert.Equal(t, 2, summary["breakers"])
	assert.Equal(t, 1, channelQueryCount)
	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())

	var metricCount int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Count(&metricCount).Error)
	assert.Equal(t, int64(2), metricCount)
	var breakerCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Count(&breakerCount).Error)
	assert.Equal(t, int64(2), breakerCount)
}

func TestFlushRoutingRuntimeStateRequeuesMetricsWhenEligibilityLookupFails(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 91, Name: "supported-prefix", Key: "single-key"},
		{Id: 92, Name: "confirmed-multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 93, Name: "lookup-error", Key: "single-key"},
		{Id: 94, Name: "unvisited-suffix", Key: "single-key"},
	}).Error)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 91, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "a-supported-prefix", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 91, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "b-supported-prefix", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 91, APIKeyIndex: 1, ModelName: "c-invalid-positive", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 92, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "d-confirmed-multi", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 93, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "e-lookup-error", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 94, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "f-unvisited-suffix", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 94, APIKeyIndex: 1, ModelName: "g-unvisited-positive", Group: "default", BucketTs: 60, RequestCount: 1},
	})

	forcedErr := errors.New("forced metric eligibility lookup failure")
	const callbackName = "test:fail_third_metric_channel_eligibility_query"
	channelQueryCount := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
			return
		}
		channelQueryCount++
		if channelQueryCount == 3 {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.ErrorIs(t, err, forcedErr)
	assert.Equal(t, 0, summary["metrics"])
	assert.Equal(t, 3, channelQueryCount)

	var persistedCount int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Count(&persistedCount).Error)
	assert.Zero(t, persistedCount)
	requeued := routingmetrics.Snapshots()
	require.Len(t, requeued, 4)
	assert.Equal(t, []string{"a-supported-prefix", "b-supported-prefix", "e-lookup-error", "f-unvisited-suffix"}, []string{
		requeued[0].ModelName, requeued[1].ModelName, requeued[2].ModelName, requeued[3].ModelName,
	})

	require.NoError(t, db.Callback().Query().Remove(callbackName))
	summary, err = flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.NoError(t, err)
	assert.Equal(t, 4, summary["metrics"])
	assert.Empty(t, routingmetrics.Snapshots())

	var persisted []model.RoutingChannelMetric
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 4)
	assert.Equal(t, []string{"a-supported-prefix", "b-supported-prefix", "e-lookup-error", "f-unvisited-suffix"}, []string{
		persisted[0].ModelName, persisted[1].ModelName, persisted[2].ModelName, persisted[3].ModelName,
	})
}

func TestFlushRoutingRuntimeStateRequeuesBreakersWhenEligibilityLookupFails(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 101, Name: "supported-prefix", Key: "single-key"},
		{Id: 102, Name: "confirmed-multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 103, Name: "lookup-error", Key: "single-key"},
		{Id: 104, Name: "unvisited-suffix", Key: "single-key"},
	}).Error)
	keys := []routingbreaker.Key{
		{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "a-supported-prefix", Group: "default"},
		{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "b-supported-prefix", Group: "default"},
		{ChannelID: 101, APIKeyIndex: 1, Model: "c-invalid-positive", Group: "default"},
		{ChannelID: 102, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "d-confirmed-multi", Group: "default"},
		{ChannelID: 103, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "e-lookup-error", Group: "default"},
		{ChannelID: 104, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "f-unvisited-suffix", Group: "default"},
		{ChannelID: 104, APIKeyIndex: 1, Model: "g-unvisited-positive", Group: "default"},
	}
	for _, key := range keys {
		routingbreaker.RecordAttempt(key, false, http.StatusBadGateway, 0)
	}

	forcedErr := errors.New("forced breaker eligibility lookup failure")
	const callbackName = "test:fail_third_breaker_channel_eligibility_query"
	channelQueryCount := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
			return
		}
		channelQueryCount++
		if channelQueryCount == 3 {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{})
	require.ErrorIs(t, err, forcedErr)
	assert.Equal(t, 0, summary["breakers"])
	assert.Equal(t, 3, channelQueryCount)

	var persistedCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Count(&persistedCount).Error)
	assert.Zero(t, persistedCount)
	requeued := routingbreaker.DirtySnapshots()
	require.Len(t, requeued, 4)
	assert.Equal(t, []string{"a-supported-prefix", "b-supported-prefix", "e-lookup-error", "f-unvisited-suffix"}, []string{
		requeued[0].Key.Model, requeued[1].Key.Model, requeued[2].Key.Model, requeued[3].Key.Model,
	})
	routingbreaker.RequeueDirtySnapshots(requeued)

	require.NoError(t, db.Callback().Query().Remove(callbackName))
	summary, err = flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{})
	require.NoError(t, err)
	assert.Equal(t, 4, summary["breakers"])
	assert.Empty(t, routingbreaker.DirtySnapshots())

	var persisted []model.RoutingBreakerState
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 4)
	assert.Equal(t, []string{"a-supported-prefix", "b-supported-prefix", "e-lookup-error", "f-unvisited-suffix"}, []string{
		persisted[0].ModelName, persisted[1].ModelName, persisted[2].ModelName, persisted[3].ModelName,
	})
}

func TestFlushRoutingRuntimeStateRequeuesOnlyValidMetricSuffixAfterPersistenceFailure(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 41, Name: "single", Key: "single-key"},
		{Id: 42, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}).Error)

	const callbackName = "test:fail_second_routing_metric_create"
	createCount := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingChannelMetric{}).TableName() {
			return
		}
		createCount++
		if createCount == 2 {
			tx.AddError(errors.New("forced metric persistence failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 41, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "a-valid-prefix", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 41, APIKeyIndex: 1, ModelName: "b-invalid-positive", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 42, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "c-invalid-multi", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 41, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "d-valid-suffix", Group: "default", BucketTs: 60, RequestCount: 1},
	})

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.ErrorContains(t, err, "forced metric persistence failure")
	assert.Equal(t, 0, summary["metrics"])

	var persisted []model.RoutingChannelMetric
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 1)
	assert.Equal(t, "a-valid-prefix", persisted[0].ModelName)
	_, prefixCached := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 41, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "a-valid-prefix", Group: "default"})
	_, suffixCached := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 41, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "d-valid-suffix", Group: "default"})
	assert.True(t, prefixCached)
	assert.False(t, suffixCached)
	requeued := routingmetrics.Snapshots()
	require.Len(t, requeued, 1)
	assert.Equal(t, "d-valid-suffix", requeued[0].ModelName)

	require.NoError(t, db.Callback().Create().Remove(callbackName))
	summary, err = flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, "a-valid-prefix", persisted[0].ModelName)
	assert.Equal(t, "d-valid-suffix", persisted[1].ModelName)
	assert.Empty(t, routingmetrics.Snapshots())
}

func TestFlushRoutingRuntimeStateRequeuesOnlyValidBreakerSuffixAfterPersistenceFailure(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
	})
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 51, Name: "single", Key: "single-key"},
		{Id: 52, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}).Error)

	keys := []routingbreaker.Key{
		{ChannelID: 51, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "a-valid-prefix", Group: "default"},
		{ChannelID: 51, APIKeyIndex: 1, Model: "b-invalid-positive", Group: "default"},
		{ChannelID: 52, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "c-invalid-multi", Group: "default"},
		{ChannelID: 51, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "d-valid-suffix", Group: "default"},
	}
	for _, key := range keys {
		routingbreaker.RecordAttempt(key, false, http.StatusBadGateway, 0)
	}

	const callbackName = "test:fail_second_routing_breaker_update"
	updateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingBreakerState{}).TableName() {
			return
		}
		updateCount++
		if updateCount == 2 {
			tx.AddError(errors.New("forced breaker persistence failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{})
	require.ErrorContains(t, err, "forced breaker persistence failure")
	assert.Equal(t, 0, summary["breakers"])

	var persisted []model.RoutingBreakerState
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 1)
	assert.Equal(t, "a-valid-prefix", persisted[0].ModelName)
	requeued := routingbreaker.DirtySnapshots()
	require.Len(t, requeued, 1)
	assert.Equal(t, "d-valid-suffix", requeued[0].Key.Model)
	routingbreaker.RequeueDirtySnapshots(requeued)

	require.NoError(t, db.Callback().Update().Remove(callbackName))
	summary, err = flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["breakers"])
	require.NoError(t, db.Order("model_name asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, "a-valid-prefix", persisted[0].ModelName)
	assert.Equal(t, "d-valid-suffix", persisted[1].ModelName)
	assert.Empty(t, routingbreaker.DirtySnapshots())
}

func TestSmartRoutingRuntimePrunesStaleHotcacheWhenDisabled(t *testing.T) {
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            false,
		Mode:               smart_routing_setting.ModeObserve,
		HotcacheRefreshSec: 1,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		SnapshotLiveSec:    0,
		SnapshotStaleSec:   0,
	})
	key := routinghotcache.Key{
		ChannelID:   905,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "stale-model",
		Group:       "default",
	}
	routinghotcache.SetMetricForTest(key, routinghotcache.MetricSnapshot{
		RequestCount: 1,
		UpdatedUnix:  common.GetTimestamp() - 2000,
	})

	refreshCompleted := make(chan struct{}, 1)
	deps := defaultSmartRoutingRuntimeDeps()
	deps.waitRefresh = func(ctx context.Context, _ time.Duration) bool {
		refreshCompleted <- struct{}{}
		<-ctx.Done()
		return false
	}
	deps.waitFlush = func(ctx context.Context, _ time.Duration) bool {
		<-ctx.Done()
		return false
	}
	runtime := newSmartRoutingRuntime(context.Background(), deps)
	t.Cleanup(runtime.Close)
	select {
	case <-refreshCompleted:
	case <-time.After(time.Second):
		require.FailNow(t, "smart routing runtime did not finish initial refresh")
	}
	_, cached := routinghotcache.GetMetric(key)
	assert.False(t, cached)

	runtime.Close()
	runtime.Close()
}

func TestFlushRoutingRuntimeStateAppliesConfiguredRetention(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	smartRoutingRetentionLast.Store(0)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})

	retentionSetting := smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		SyncIntervalMin:    1,
		HotcacheRefreshSec: 1,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		RetentionDays:      1,
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		SyncIntervalMin:    1,
		HotcacheRefreshSec: 1,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		RetentionDays:      30,
	})
	now := common.GetTimestamp()
	expired := model.RoutingChannelMetric{
		ChannelID:    901,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "expired-model",
		Group:        "default",
		BucketTs:     now - 2*86400,
		RequestCount: 1,
	}
	fresh := model.RoutingChannelMetric{
		ChannelID:    902,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "fresh-model",
		Group:        "default",
		BucketTs:     now,
		RequestCount: 1,
	}
	require.NoError(t, db.Create(&expired).Error)
	require.NoError(t, db.Create(&fresh).Error)

	summary, err := flushRoutingRuntimeState(retentionSetting)
	require.NoError(t, err)
	assert.Equal(t, int64(1), summary["retained_metrics_deleted"])

	var remaining []model.RoutingChannelMetric
	require.NoError(t, db.Order("bucket_ts asc").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, fresh.BucketTs, remaining[0].BucketTs)
}

func TestFlushRoutingRuntimeStateMergesRepeatedBucketDeltasIntoHotcache(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routingmetrics.ResetForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingmetrics.ResetForTest()
		routinghotcache.ResetForTest()
	})

	const (
		channelID = 906
		bucketTs  = 120
	)
	require.NoError(t, db.Create(&model.Channel{Id: channelID, Name: "single", Key: "single-key"}).Error)
	key := routinghotcache.Key{
		ChannelID:   channelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}
	firstDelta := model.RoutingChannelMetric{
		ChannelID:               channelID,
		APIKeyIndex:             model.RoutingMetricSingleKeyIndex,
		ModelName:               key.Model,
		Group:                   key.Group,
		BucketTs:                bucketTs,
		RequestCount:            10,
		SuccessCount:            1,
		ReliabilityRequestCount: 10,
		ReliabilityFailureCount: 9,
	}
	secondDelta := model.RoutingChannelMetric{
		ChannelID:               channelID,
		APIKeyIndex:             model.RoutingMetricSingleKeyIndex,
		ModelName:               key.Model,
		Group:                   key.Group,
		BucketTs:                bucketTs,
		RequestCount:            10,
		SuccessCount:            10,
		ReliabilityRequestCount: 10,
	}
	setting := smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60}

	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{firstDelta})
	_, err := flushRoutingRuntimeState(setting)
	require.NoError(t, err)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{secondDelta})
	_, err = flushRoutingRuntimeState(setting)
	require.NoError(t, err)

	var persisted model.RoutingChannelMetric
	require.NoError(t, db.Where(&model.RoutingChannelMetric{
		ChannelID:   channelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		ModelName:   key.Model,
		Group:       key.Group,
		BucketTs:    bucketTs,
	}).First(&persisted).Error)
	assert.Equal(t, int64(20), persisted.RequestCount)
	assert.Equal(t, int64(11), persisted.SuccessCount)
	assert.Equal(t, int64(20), persisted.ReliabilityRequestCount)
	assert.Equal(t, int64(9), persisted.ReliabilityFailureCount)

	cached, ok := routinghotcache.GetMetric(key)
	require.True(t, ok)
	assert.Equal(t, persisted.RequestCount, cached.RequestCount)
	assert.Equal(t, persisted.SuccessCount, cached.SuccessCount)
	assert.Equal(t, persisted.ReliabilityRequestCount, cached.ReliabilityRequestCount)
	assert.Equal(t, persisted.ReliabilityFailureCount, cached.ReliabilityFailureCount)
}

func TestFlushRoutingRuntimeStateSkipsRetentionWithinThrottleWindow(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routingmetrics.ResetForTest()
	now := common.GetTimestamp()
	smartRoutingRetentionLast.Store(now)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})

	expired := model.RoutingChannelMetric{
		ChannelID:    905,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "throttled-expired-model",
		Group:        "default",
		BucketTs:     now - 2*86400,
		RequestCount: 1,
	}
	require.NoError(t, db.Create(&expired).Error)

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec: 60,
		RetentionDays:   1,
	})
	require.NoError(t, err)
	assert.NotContains(t, summary, "retained_metrics_deleted")

	var remaining int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Where("channel_id = ?", expired.ChannelID).Count(&remaining).Error)
	assert.Equal(t, int64(1), remaining)
}

func TestFlushRoutingRuntimeStateDoesNotOverflowRetentionCutoff(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routingmetrics.ResetForTest()
	smartRoutingRetentionLast.Store(0)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})

	now := common.GetTimestamp()
	metrics := []model.RoutingChannelMetric{
		{
			ChannelID:    903,
			APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
			ModelName:    "historical-model",
			Group:        "default",
			BucketTs:     now - 365*86400,
			RequestCount: 1,
		},
		{
			ChannelID:    904,
			APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
			ModelName:    "current-model",
			Group:        "default",
			BucketTs:     now,
			RequestCount: 1,
		},
	}
	require.NoError(t, db.Create(&metrics).Error)

	summary, err := flushRoutingRuntimeState(smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec: 60,
		RetentionDays:   int(^uint(0) >> 1),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), summary["retained_metrics_deleted"])

	var remaining int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Count(&remaining).Error)
	assert.Equal(t, int64(2), remaining)
}
