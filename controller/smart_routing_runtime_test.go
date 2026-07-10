package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
