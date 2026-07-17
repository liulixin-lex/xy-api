package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

type observedSmartWaitContext struct {
	context.Context
	checks      atomic.Int64
	afterSecond chan struct{}
}

func createRoutingRuntimeBindings(t *testing.T, db *gorm.DB, channelIDs ...int) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}))
	for _, channelID := range channelIDs {
		binding := model.RoutingChannelBinding{
			ChannelID:     channelID,
			UpstreamType:  model.RoutingUpstreamTypeNewAPI,
			BaseURL:       fmt.Sprintf("https://routing-%d.example.com", channelID),
			UpstreamGroup: "default",
			Enabled:       true,
			CreatedTime:   1,
			UpdatedTime:   1,
		}
		require.NoError(t, db.Where("channel_id = ?", channelID).FirstOrCreate(&binding).Error)
	}
}

func (ctx *observedSmartWaitContext) Err() error {
	err := ctx.Context.Err()
	if ctx.checks.Add(1) == 2 {
		close(ctx.afterSecond)
	}
	return err
}

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
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			refreshCount.Add(1)
			refreshRan <- struct{}{}
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushCount.Add(1)
			flushRan <- struct{}{}
			return nil
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
	<-refreshRan
	<-flushRan

	runtime.Close()
	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))

	assert.Equal(t, int64(1), refreshCount.Load())
	assert.Equal(t, int64(2), flushCount.Load())
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
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			refreshCount.Add(1)
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushCount.Add(1)
			return nil
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
	})
	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))

	assert.Zero(t, refreshCount.Load())
	assert.Equal(t, int64(1), flushCount.Load())
}

func TestSmartRoutingRuntimeContextCancellationStopsBlockingCallbacks(t *testing.T) {
	refreshStarted := make(chan struct{}, 1)
	flushStarted := make(chan struct{}, 1)
	refreshStopped := make(chan struct{}, 1)
	flushStopped := make(chan struct{}, 1)
	var flushCalls atomic.Int64

	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            true,
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			refreshStarted <- struct{}{}
			<-ctx.Done()
			refreshStopped <- struct{}{}
			return ctx.Err()
		},
		flush: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			if flushCalls.Add(1) > 1 {
				return nil
			}
			flushStarted <- struct{}{}
			<-ctx.Done()
			flushStopped <- struct{}{}
			return ctx.Err()
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
	})
	<-refreshStarted
	<-flushStarted

	canceledWait, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	require.ErrorIs(t, runtime.Wait(canceledWait), context.Canceled)

	runtime.Close()
	runtime.Close()
	<-refreshStopped
	<-flushStopped
	require.NoError(t, runtime.Wait(context.Background()))

	stats := runtime.Stats()
	assert.Zero(t, stats.RefreshErrors)
	assert.Zero(t, stats.FlushErrors)
	assert.Zero(t, stats.RefreshConsecutiveErrors)
	assert.Zero(t, stats.FlushConsecutiveErrors)
	assert.Equal(t, int64(1), stats.FinalFlushRuns)
}

func TestSmartRoutingRuntimeCanceledWaitAfterWorkersStopDoesNotConsumeFinalFlush(t *testing.T) {
	var flushCalls atomic.Int64
	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            false,
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			flushCalls.Add(1)
			return ctx.Err()
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
	})
	runtime.Close()
	<-runtime.done

	canceledWait, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	for range 16 {
		require.ErrorIs(t, runtime.Wait(canceledWait), context.Canceled)
	}
	assert.Zero(t, flushCalls.Load())
	assert.Zero(t, runtime.Stats().FinalFlushRuns)

	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(1), flushCalls.Load())
	assert.Equal(t, int64(1), runtime.Stats().FinalFlushRuns)
}

func TestSmartRoutingRuntimeBackoffAndRecoveryAreIndependent(t *testing.T) {
	refreshWaits := make(chan time.Duration)
	flushWaits := make(chan time.Duration)
	refreshAdvance := make(chan struct{})
	flushAdvance := make(chan struct{})
	var refreshAttempts atomic.Int64
	var flushAttempts atomic.Int64

	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            true,
				HotcacheRefreshSec: 17,
				FlushIntervalMin:   2,
			}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			if refreshAttempts.Add(1) <= 2 {
				return errors.New("refresh failed")
			}
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			if flushAttempts.Add(1) <= 2 {
				return errors.New("flush failed")
			}
			return nil
		},
		waitRefresh: func(ctx context.Context, duration time.Duration) bool {
			refreshWaits <- duration
			select {
			case <-refreshAdvance:
				return true
			case <-ctx.Done():
				return false
			}
		},
		waitFlush: func(ctx context.Context, duration time.Duration) bool {
			flushWaits <- duration
			select {
			case <-flushAdvance:
				return true
			case <-ctx.Done():
				return false
			}
		},
		jitter: func(max time.Duration) time.Duration { return max },
	})

	assert.Equal(t, time.Second, <-refreshWaits)
	assert.Equal(t, time.Second, <-flushWaits)
	assert.Equal(t, SmartRoutingRuntimeStats{
		RefreshRuns:              1,
		RefreshErrors:            1,
		RefreshConsecutiveErrors: 1,
		FlushRuns:                1,
		FlushErrors:              1,
		FlushConsecutiveErrors:   1,
	}, runtime.Stats())

	refreshAdvance <- struct{}{}
	flushAdvance <- struct{}{}
	assert.Equal(t, 2*time.Second, <-refreshWaits)
	assert.Equal(t, 2*time.Second, <-flushWaits)
	assert.Equal(t, int64(2), runtime.Stats().RefreshConsecutiveErrors)
	assert.Equal(t, int64(2), runtime.Stats().FlushConsecutiveErrors)

	refreshAdvance <- struct{}{}
	flushAdvance <- struct{}{}
	assert.Equal(t, 17*time.Second, <-refreshWaits)
	assert.Equal(t, 2*time.Minute, <-flushWaits)
	assert.Equal(t, SmartRoutingRuntimeStats{
		RefreshRuns:       3,
		RefreshErrors:     2,
		RefreshRecoveries: 1,
		FlushRuns:         3,
		FlushErrors:       2,
		FlushRecoveries:   1,
	}, runtime.Stats())

	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
}

func TestSmartRoutingRuntimeDisabledFlushDoesNotCountRunOrRecovery(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	flushWaits := make(chan time.Duration)
	flushAdvance := make(chan struct{})
	var flushCalls atomic.Int64

	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            enabled.Load(),
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   2,
			}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			if flushCalls.Add(1) == 1 {
				return errors.New("flush failed")
			}
			return nil
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush: func(ctx context.Context, duration time.Duration) bool {
			select {
			case flushWaits <- duration:
			case <-ctx.Done():
				return false
			}
			select {
			case <-flushAdvance:
				return true
			case <-ctx.Done():
				return false
			}
		},
		jitter: func(max time.Duration) time.Duration { return max },
	})

	assert.Equal(t, time.Second, <-flushWaits)
	stats := runtime.Stats()
	assert.Equal(t, int64(1), stats.FlushRuns)
	assert.Equal(t, int64(1), stats.FlushErrors)
	assert.Equal(t, int64(1), stats.FlushConsecutiveErrors)

	enabled.Store(false)
	flushAdvance <- struct{}{}
	assert.Equal(t, 2*time.Minute, <-flushWaits)
	assert.Equal(t, int64(1), flushCalls.Load())
	assert.Equal(t, int64(1), runtime.Stats().FlushRuns)
	assert.Equal(t, int64(1), runtime.Stats().FlushConsecutiveErrors)
	assert.Zero(t, runtime.Stats().FlushRecoveries)

	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(2), flushCalls.Load(), "disabled routing still performs the one final flush")
}

func TestSmartRoutingRuntimeFinalFlushReturnsErrorOnlyOnce(t *testing.T) {
	refreshReady := make(chan struct{}, 1)
	flushReady := make(chan struct{}, 1)
	finalErr := errors.New("final flush failed")
	var flushCalls atomic.Int64

	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            false,
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushCalls.Add(1)
			return finalErr
		},
		waitRefresh: func(ctx context.Context, _ time.Duration) bool {
			refreshReady <- struct{}{}
			<-ctx.Done()
			return false
		},
		waitFlush: func(ctx context.Context, _ time.Duration) bool {
			flushReady <- struct{}{}
			<-ctx.Done()
			return false
		},
	})
	<-refreshReady
	<-flushReady

	runtime.Close()
	require.ErrorIs(t, runtime.Wait(context.Background()), finalErr)
	require.ErrorIs(t, runtime.Wait(context.Background()), finalErr)
	assert.Equal(t, int64(1), flushCalls.Load())
	assert.Equal(t, SmartRoutingRuntimeStats{
		RefreshRuns:      1,
		FinalFlushRuns:   1,
		FinalFlushErrors: 1,
	}, runtime.Stats())
}

func TestSmartRoutingRuntimeConcurrentWaiterHonorsOwnContextDuringFinalFlush(t *testing.T) {
	refreshReady := make(chan struct{}, 1)
	flushReady := make(chan struct{}, 1)
	finalFlushStarted := make(chan struct{})
	releaseFinalFlush := make(chan struct{})
	var flushCalls atomic.Int64

	runtime := newSmartRoutingRuntime(context.Background(), smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled:            false,
				HotcacheRefreshSec: 1,
				FlushIntervalMin:   1,
			}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushCalls.Add(1)
			close(finalFlushStarted)
			<-releaseFinalFlush
			return nil
		},
		waitRefresh: func(ctx context.Context, _ time.Duration) bool {
			refreshReady <- struct{}{}
			<-ctx.Done()
			return false
		},
		waitFlush: func(ctx context.Context, _ time.Duration) bool {
			flushReady <- struct{}{}
			<-ctx.Done()
			return false
		},
	})
	<-refreshReady
	<-flushReady
	runtime.Close()

	firstResult := make(chan error, 1)
	go func() { firstResult <- runtime.Wait(context.Background()) }()
	<-finalFlushStarted

	secondBase, cancelSecond := context.WithCancel(context.Background())
	secondContext := &observedSmartWaitContext{Context: secondBase, afterSecond: make(chan struct{})}
	secondResult := make(chan error, 1)
	go func() { secondResult <- runtime.Wait(secondContext) }()
	<-secondContext.afterSecond
	cancelSecond()

	select {
	case err := <-secondResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(releaseFinalFlush)
		require.NoError(t, <-firstResult)
		require.Fail(t, "concurrent Wait ignored its own context")
	}

	close(releaseFinalFlush)
	require.NoError(t, <-firstResult)
	assert.Equal(t, int64(1), flushCalls.Load())
}

func TestFlushRoutingRuntimeStateLockWaitHonorsContext(t *testing.T) {
	smartRoutingRuntimeStateMu.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := flushRoutingRuntimeState(ctx, smart_routing_setting.SmartRoutingSetting{})
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		smartRoutingRuntimeStateMu.Unlock()
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		smartRoutingRuntimeStateMu.Unlock()
		<-result
		require.Fail(t, "routing runtime state lock ignored context cancellation")
	}
}

func TestSmartRoutingRuntimeFinalFlushPersistsDirtyStateWhenDisabled(t *testing.T) {
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

	const channelID = 1201
	require.NoError(t, db.Create(&model.Channel{Id: channelID, Name: "final-flush", Key: "single-key"}).Error)
	createRoutingRuntimeBindings(t, db, channelID)
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:            false,
		HotcacheRefreshSec: 1,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
	}
	refreshReady := make(chan struct{}, 1)
	flushReady := make(chan struct{}, 1)
	deps := defaultSmartRoutingRuntimeDeps()
	deps.getSetting = func() smart_routing_setting.SmartRoutingSetting { return setting }
	baseFlush := deps.flush
	var flushCalls atomic.Int64
	deps.flush = func(ctx context.Context, current smart_routing_setting.SmartRoutingSetting) error {
		flushCalls.Add(1)
		return baseFlush(ctx, current)
	}
	deps.waitRefresh = func(ctx context.Context, _ time.Duration) bool {
		refreshReady <- struct{}{}
		<-ctx.Done()
		return false
	}
	deps.waitFlush = func(ctx context.Context, _ time.Duration) bool {
		flushReady <- struct{}{}
		<-ctx.Done()
		return false
	}

	runtime := newSmartRoutingRuntime(context.Background(), deps)
	<-refreshReady
	<-flushReady
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
		ChannelID:    channelID,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "final-metric",
		Group:        "default",
		BucketTs:     60,
		RequestCount: 1,
	}})
	routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
		ChannelID:   channelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "final-breaker",
		Group:       "default",
	}, routingbreaker.FailureProvider5xx)

	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(1), flushCalls.Load())
	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())

	var metric model.RoutingChannelMetric
	require.NoError(t, db.Where("channel_id = ? AND model_name = ?", channelID, "final-metric").First(&metric).Error)
	assert.Equal(t, int64(1), metric.RequestCount)
	var breaker model.RoutingBreakerState
	require.NoError(t, db.Where("channel_id = ? AND model_name = ?", channelID, "final-breaker").First(&breaker).Error)
	assert.Equal(t, model.RoutingBreakerStateOpen, breaker.State)
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
	channel := model.Channel{Id: 11, Name: "current-single", Key: "single-key"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&[]model.RoutingBreakerState{
		{ChannelID: 10, APIKeyIndex: -1, ModelName: "legacy", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: 0, UpdatedTime: common.GetTimestamp()},
		{ChannelID: 11, ChannelGeneration: channel.RoutingGeneration, APIKeyIndex: -1, ModelName: "current", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: common.GetTimestamp()},
	}).Error)

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.GetSetting())
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

	channels := []model.Channel{
		{Id: 21, Name: "single", Key: "single-key"},
		{Id: 22, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}
	require.NoError(t, db.Create(&channels).Error)
	generationByChannelID := map[int]string{
		channels[0].Id: channels[0].RoutingGeneration,
		channels[1].Id: channels[1].RoutingGeneration,
	}
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
			ChannelID: key.channelID, ChannelGeneration: generationByChannelID[key.channelID], APIKeyIndex: key.apiKeyIndex, ModelName: key.modelName,
			Group: "default", BucketTs: now, RequestCount: 1, SuccessCount: 1,
		})
		breakers = append(breakers, model.RoutingBreakerState{
			ChannelID: key.channelID, ChannelGeneration: generationByChannelID[key.channelID], APIKeyIndex: key.apiKeyIndex, ModelName: key.modelName,
			Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
			State: model.RoutingBreakerStateOpen, UpdatedTime: now,
		})
	}
	require.NoError(t, db.Create(&metrics).Error)
	require.NoError(t, db.Create(&breakers).Error)

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	assert.Equal(t, 1, summary["breakers"])
	assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)

	for _, key := range keys {
		cacheKey := routinghotcache.Key{ChannelID: key.channelID, ChannelGeneration: generationByChannelID[key.channelID], APIKeyIndex: key.apiKeyIndex, Model: key.modelName, Group: "default"}
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
	channels := []model.Channel{
		{Id: 23, Name: "single", Key: "single-key"},
		{Id: 24, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}
	require.NoError(t, db.Create(&channels).Error)

	const invalidRows = 5000
	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 23, ChannelGeneration: channels[0].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "valid-single",
		Group: "default", BucketTs: now, RequestCount: 1, SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID: 23, ChannelGeneration: channels[0].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "valid-single",
		Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
		State: model.RoutingBreakerStateDegraded, UpdatedTime: now,
	}).Error)
	metrics := make([]model.RoutingChannelMetric, 0, invalidRows)
	breakers := make([]model.RoutingBreakerState, 0, invalidRows)
	for i := 0; i < invalidRows; i++ {
		modelName := fmt.Sprintf("legacy-multi-%04d", i)
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID: 24, ChannelGeneration: channels[1].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: modelName,
			Group: "default", BucketTs: now, RequestCount: 1,
		})
		breakers = append(breakers, model.RoutingBreakerState{
			ChannelID: 24, ChannelGeneration: channels[1].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: modelName,
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

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["metrics"])
	assert.Equal(t, 1, summary["breakers"])
	cacheKey := routinghotcache.Key{ChannelID: 23, ChannelGeneration: channels[0].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "valid-single", Group: "default"}
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
	channels := []model.Channel{
		{Id: 25, Name: "expired-multi", Key: "key-0\nkey-1", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 26, Name: "fresh-single", Key: "single-key"},
	}
	require.NoError(t, db.Create(&channels).Error)

	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID: 26, ChannelGeneration: channels[1].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "fresh-single",
		Group: "default", SemanticVersion: model.RoutingBreakerSemanticVersion,
		State: model.RoutingBreakerStateDegraded, UpdatedTime: now,
	}).Error)
	const expiredRows = 5000
	expired := make([]model.RoutingBreakerState, 0, expiredRows)
	for i := 0; i < expiredRows; i++ {
		expired = append(expired, model.RoutingBreakerState{
			ChannelID: 25, ChannelGeneration: channels[0].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
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

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, summary["breakers"])
	assert.Equal(t, 1, breakerPageQueries)
	assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)

	_, freshOK := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID: 26, ChannelGeneration: channels[1].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "fresh-single", Group: "default",
	})
	_, expiredOK := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID: 25, ChannelGeneration: channels[0].RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "expired-multi-0000", Group: "default",
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
	channel := model.Channel{Id: 27, Name: "single", Key: "single-key"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 27, ChannelGeneration: channel.RoutingGeneration, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
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

	_, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec:  60,
		SnapshotStaleSec: 300,
	})
	require.ErrorIs(t, err, forcedErr)
}

func TestRefreshRoutingHotcacheContextCancellationStopsDatabaseQuery(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))

	queryStarted := make(chan struct{}, 1)
	const callbackName = "test:block_refresh_routing_metric_query"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingChannelMetric{}).TableName() {
			return
		}
		queryStarted <- struct{}{}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := refreshRoutingHotcacheFromDB(ctx, smart_routing_setting.SmartRoutingSetting{
			MetricBucketSec:  60,
			SnapshotStaleSec: 300,
		})
		result <- err
	}()
	<-queryStarted
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
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
	createRoutingRuntimeBindings(t, db, 31)

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
		routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
			ChannelID: key.channelID, APIKeyIndex: key.apiKeyIndex, Model: key.modelName, Group: "default",
		}, routingbreaker.FailureProvider5xx)
	}
	routingmetrics.RequeueSnapshots(metrics)

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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

func TestFlushRoutingRuntimeStateContextCancellationRequeuesDirtyState(t *testing.T) {
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

	const channelID = 1202
	require.NoError(t, db.Create(&model.Channel{Id: channelID, Name: "cancel-flush", Key: "single-key"}).Error)
	createRoutingRuntimeBindings(t, db, channelID)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
		ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "cancel-metric",
		Group: "default", BucketTs: 60, RequestCount: 1,
	}})
	routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
		ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "cancel-breaker", Group: "default",
	}, routingbreaker.FailureProvider5xx)

	createStarted := make(chan struct{}, 1)
	const callbackName = "test:block_flush_routing_metric_create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingChannelMetric{}).TableName() {
			return
		}
		createStarted <- struct{}{}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := flushRoutingRuntimeState(ctx, smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
		result <- err
	}()
	<-createStarted
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)

	requeuedMetrics := routingmetrics.Snapshots()
	require.Len(t, requeuedMetrics, 1)
	assert.Equal(t, "cancel-metric", requeuedMetrics[0].ModelName)
	requeuedBreakers := routingbreaker.DirtySnapshots()
	require.Len(t, requeuedBreakers, 1)
	assert.Equal(t, "cancel-breaker", requeuedBreakers[0].Key.Model)
}

func TestFlushRoutingRuntimeStateRejectsDirtyStateWhenChannelWasRecreatedBeforeFirstWrite(t *testing.T) {
	testCases := []struct {
		name      string
		channelID int
		metric    bool
	}{
		{name: "metric_bucket_crosses_recreation", channelID: 1203, metric: true},
		{name: "breaker_same_second_as_recreation", channelID: 1204},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingChannelBinding{},
				&model.RoutingChannelMetric{},
				&model.RoutingBreakerState{},
			))
			previousMemoryCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			routingmetrics.ResetForTest()
			routinghotcache.ResetForTest()
			const replacementCreatedTime int64 = 630
			routingbreaker.ResetDefaultForTest(routingbreaker.Config{
				Consecutive5xxThreshold: 1,
				BaseCooldown:            time.Second,
				MaxCooldown:             time.Second,
				EntryTTL:                time.Hour,
				Now: func() time.Time {
					return time.Unix(replacementCreatedTime, 0)
				},
			})
			t.Cleanup(func() {
				common.MemoryCacheEnabled = previousMemoryCache
				routingmetrics.ResetForTest()
				routinghotcache.ResetForTest()
				routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
			})

			oldChannel := model.Channel{
				Id: testCase.channelID, Name: "pre-flush-recreation-" + testCase.name,
				Key: "single-key", CreatedTime: 100,
			}
			require.NoError(t, db.Create(&oldChannel).Error)
			oldBinding := model.RoutingChannelBinding{
				ChannelID:     testCase.channelID,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       "https://old-routing.example.com",
				UpstreamGroup: "default",
				Enabled:       true,
				CreatedTime:   100,
				UpdatedTime:   100,
			}
			require.NoError(t, db.Create(&oldBinding).Error)

			cacheKey := routinghotcache.Key{
				ChannelID:   testCase.channelID,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model:       "old-generation-" + testCase.name,
				Group:       "default",
			}
			if testCase.metric {
				routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					ModelName: cacheKey.Model, Group: cacheKey.Group, BucketTs: 600, RequestCount: 1,
				}})
			} else {
				routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					Model: cacheKey.Model, Group: cacheKey.Group,
				}, routingbreaker.FailureProvider5xx)
			}

			replacementChannel := model.Channel{
				Id: testCase.channelID, Name: "replacement-" + testCase.name,
				Key: "replacement-key", CreatedTime: replacementCreatedTime,
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("id = ?", oldChannel.Id).Delete(&model.Channel{}).Error; err != nil {
					return err
				}
				return tx.Create(&replacementChannel).Error
			}))
			require.NotEqual(t, oldChannel.RoutingGeneration, replacementChannel.RoutingGeneration)

			summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
			require.NoError(t, err)
			assert.Equal(t, 0, summary["metrics"])
			assert.Equal(t, 0, summary["breakers"])

			var currentBinding model.RoutingChannelBinding
			require.NoError(t, db.Where("channel_id = ?", testCase.channelID).First(&currentBinding).Error)
			assert.Equal(t, oldBinding.ID, currentBinding.ID)
			for _, table := range []any{&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}} {
				var count int64
				require.NoError(t, db.Model(table).Where("channel_id = ?", testCase.channelID).Count(&count).Error)
				assert.Zero(t, count)
			}
			_, metricCached := routinghotcache.GetMetric(cacheKey)
			_, breakerCached := routinghotcache.GetBreaker(cacheKey)
			assert.False(t, metricCached)
			assert.False(t, breakerCached)
			assert.Empty(t, routingmetrics.Snapshots())
			assert.Empty(t, routingbreaker.DirtySnapshots())
			assert.Zero(t, routingbreaker.RuntimeStats().Entries)
		})
	}
}

func TestFlushRoutingRuntimeStateDoesNotRecreateStateAfterRemoteChannelDelete(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
	))
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

	const channelID = 1203
	require.NoError(t, db.Create(&model.Channel{Id: channelID, Name: "remote-delete", Key: "single-key"}).Error)
	binding := model.RoutingChannelBinding{
		ChannelID:     channelID,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "default",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
		ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "deleted-metric",
		Group: "default", BucketTs: 60, RequestCount: 1,
	}})
	routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
		ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "deleted-breaker", Group: "default",
	}, routingbreaker.FailureProvider5xx)

	// Simulate another node deleting the physical channel without touching this process's dirty state.
	require.NoError(t, db.Delete(&model.Channel{}, channelID).Error)

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
	require.NoError(t, err)
	assert.Equal(t, 0, summary["metrics"])
	assert.Equal(t, 0, summary["breakers"])
	for _, table := range []any{&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}} {
		var count int64
		require.NoError(t, db.Model(table).Where("channel_id = ?", channelID).Count(&count).Error)
		assert.Zero(t, count)
	}
	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())
}

func TestFlushRoutingRuntimeStateClearsCommittedStateWhenChannelIsDeletedBeforePublish(t *testing.T) {
	testCases := []struct {
		name           string
		channelID      int
		metric         bool
		afterPostCheck bool
	}{
		{name: "metric_before_post_check", channelID: 1204, metric: true},
		{name: "breaker_before_post_check", channelID: 1205},
		{name: "metric_after_post_check", channelID: 1206, metric: true, afterPostCheck: true},
		{name: "breaker_after_post_check", channelID: 1207, afterPostCheck: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingChannelBinding{},
				&model.RoutingChannelMetric{},
				&model.RoutingBreakerState{},
			))
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

			require.NoError(t, db.Create(&model.Channel{
				Id: testCase.channelID, Name: "flush-first-" + testCase.name, Key: "single-key",
			}).Error)
			binding := model.RoutingChannelBinding{
				ChannelID:     testCase.channelID,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       "https://routing.example.com",
				UpstreamGroup: "default",
				Enabled:       true,
				CreatedTime:   1,
				UpdatedTime:   1,
			}
			require.NoError(t, db.Create(&binding).Error)

			cacheKey := routinghotcache.Key{
				ChannelID:   testCase.channelID,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model:       "committed-" + testCase.name,
				Group:       "default",
			}
			if testCase.metric {
				routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					ModelName: cacheKey.Model, Group: cacheKey.Group, BucketTs: 60, RequestCount: 1,
				}})
			} else {
				routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					Model: cacheKey.Model, Group: cacheKey.Group,
				}, routingbreaker.FailureProvider5xx)
			}

			afterCommit := make(chan struct{})
			releasePostCommitCheck := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(releasePostCommitCheck)
				}
			}()
			var channelQueries atomic.Int64
			callbackName := "test:block_flush_post_commit_channel_check_" + testCase.name
			blockPostWriteChannelQuery := func(tx *gorm.DB) {
				if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
					return
				}
				if channelQueries.Add(1) == 3 {
					close(afterCommit)
					<-releasePostCommitCheck
				}
			}
			if testCase.afterPostCheck {
				require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, blockPostWriteChannelQuery))
			} else {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, blockPostWriteChannelQuery))
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			type flushResult struct {
				summary map[string]any
				err     error
			}
			result := make(chan flushResult, 1)
			go func() {
				summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
				result <- flushResult{summary: summary, err: err}
			}()

			select {
			case <-afterCommit:
			case <-time.After(time.Second):
				require.FailNow(t, "flush did not reach the post-commit channel check")
			}

			persistedModel := any(&model.RoutingBreakerState{})
			if testCase.metric {
				persistedModel = &model.RoutingChannelMetric{}
			}
			var committedCount int64
			require.NoError(t, db.Model(persistedModel).
				Where("channel_id = ? AND model_name = ?", testCase.channelID, cacheKey.Model).
				Count(&committedCount).Error)
			assert.Equal(t, int64(1), committedCount)

			// Simulate the physical channel and its persisted runtime state being deleted by another node
			// after this flush commits. The after-query cases delete after the first post-check
			// has already observed the channel, covering the check-to-publish/return window.
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("id = ?", testCase.channelID).Delete(&model.Channel{}).Error; err != nil {
					return err
				}
				if err := tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingChannelMetric{}).Error; err != nil {
					return err
				}
				return tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingBreakerState{}).Error
			}))
			close(releasePostCommitCheck)
			released = true

			flush := <-result
			require.NoError(t, flush.err)
			assert.Equal(t, 0, flush.summary["metrics"])
			assert.Equal(t, 0, flush.summary["breakers"])

			for _, table := range []any{&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}} {
				var count int64
				require.NoError(t, db.Model(table).Where("channel_id = ?", testCase.channelID).Count(&count).Error)
				assert.Zero(t, count)
			}
			_, metricCached := routinghotcache.GetMetric(cacheKey)
			_, breakerCached := routinghotcache.GetBreaker(cacheKey)
			assert.False(t, metricCached)
			assert.False(t, breakerCached)
			assert.Empty(t, routingmetrics.Snapshots())
			assert.Empty(t, routingbreaker.DirtySnapshots())
			assert.Zero(t, routingbreaker.RuntimeStats().Entries)
		})
	}
}

func TestFlushRoutingRuntimeStateFailsClosedWhenFinalChannelCheckFails(t *testing.T) {
	testCases := []struct {
		name      string
		channelID int
		metric    bool
	}{
		{name: "metric", channelID: 1208, metric: true},
		{name: "breaker", channelID: 1209},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingChannelBinding{},
				&model.RoutingChannelMetric{},
				&model.RoutingBreakerState{},
			))
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

			require.NoError(t, db.Create(&model.Channel{
				Id: testCase.channelID, Name: "final-check-error-" + testCase.name, Key: "single-key",
			}).Error)
			require.NoError(t, db.Create(&model.RoutingChannelBinding{
				ChannelID:     testCase.channelID,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       "https://routing.example.com",
				UpstreamGroup: "default",
				Enabled:       true,
				CreatedTime:   1,
				UpdatedTime:   1,
			}).Error)
			cacheKey := routinghotcache.Key{
				ChannelID:   testCase.channelID,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model:       "final-check-error-" + testCase.name,
				Group:       "default",
			}
			if testCase.metric {
				routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					ModelName: cacheKey.Model, Group: cacheKey.Group, BucketTs: 60, RequestCount: 1,
				}})
			} else {
				routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					Model: cacheKey.Model, Group: cacheKey.Group,
				}, routingbreaker.FailureProvider5xx)
			}

			forcedErr := errors.New("forced final channel verification failure")
			var channelQueries atomic.Int64
			callbackName := "test:fail_final_flush_channel_check_" + testCase.name
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" &&
					channelQueries.Add(1) == 4 {
					tx.AddError(forcedErr)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
			require.ErrorIs(t, err, forcedErr)
			assert.Equal(t, 0, summary["metrics"])
			assert.Equal(t, 0, summary["breakers"])

			persistedModel := any(&model.RoutingBreakerState{})
			if testCase.metric {
				persistedModel = &model.RoutingChannelMetric{}
			}
			var persistedCount int64
			require.NoError(t, db.Model(persistedModel).
				Where("channel_id = ? AND model_name = ?", testCase.channelID, cacheKey.Model).
				Count(&persistedCount).Error)
			assert.Equal(t, int64(1), persistedCount)
			_, metricCached := routinghotcache.GetMetric(cacheKey)
			_, breakerCached := routinghotcache.GetBreaker(cacheKey)
			assert.False(t, metricCached)
			assert.False(t, breakerCached)
			assert.Zero(t, routingbreaker.RuntimeStats().Entries)
		})
	}
}

func TestFlushRoutingRuntimeStateFailsClosedWhenInitialChannelCheckFails(t *testing.T) {
	testCases := []struct {
		name      string
		channelID int
		metric    bool
	}{
		{name: "metric", channelID: 1210, metric: true},
		{name: "breaker", channelID: 1211},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingChannelBinding{},
				&model.RoutingChannelMetric{},
				&model.RoutingBreakerState{},
			))
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

			require.NoError(t, db.Create(&model.Channel{
				Id: testCase.channelID, Name: "initial-check-error-" + testCase.name, Key: "single-key",
			}).Error)
			require.NoError(t, db.Create(&model.RoutingChannelBinding{
				ChannelID:     testCase.channelID,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       "https://routing.example.com",
				UpstreamGroup: "default",
				Enabled:       true,
				CreatedTime:   1,
				UpdatedTime:   1,
			}).Error)
			cacheKey := routinghotcache.Key{
				ChannelID:   testCase.channelID,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model:       "initial-check-error-" + testCase.name,
				Group:       "default",
			}
			if testCase.metric {
				routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					ModelName: cacheKey.Model, Group: cacheKey.Group, BucketTs: 60, RequestCount: 1,
				}})
				routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{RequestCount: 99})
			} else {
				routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
					ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					Model: cacheKey.Model, Group: cacheKey.Group,
				}, routingbreaker.FailureProvider5xx)
			}

			forcedErr := errors.New("forced initial channel verification failure")
			initialCheckStarted := make(chan struct{})
			releaseInitialCheck := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(releaseInitialCheck)
				}
			}()
			var channelQueries atomic.Int64
			callbackName := "test:fail_initial_flush_channel_check_" + testCase.name
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" &&
					channelQueries.Add(1) == 3 {
					close(initialCheckStarted)
					<-releaseInitialCheck
					tx.AddError(forcedErr)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			type flushResult struct {
				summary map[string]any
				err     error
			}
			result := make(chan flushResult, 1)
			go func() {
				summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
				result <- flushResult{summary: summary, err: err}
			}()
			select {
			case <-initialCheckStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "flush did not reach the initial channel check")
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("id = ?", testCase.channelID).Delete(&model.Channel{}).Error; err != nil {
					return err
				}
				if err := tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingChannelMetric{}).Error; err != nil {
					return err
				}
				return tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingBreakerState{}).Error
			}))
			close(releaseInitialCheck)
			released = true

			flush := <-result
			require.ErrorIs(t, flush.err, forcedErr)
			assert.Equal(t, 0, flush.summary["metrics"])
			assert.Equal(t, 0, flush.summary["breakers"])

			persistedModel := any(&model.RoutingBreakerState{})
			if testCase.metric {
				persistedModel = &model.RoutingChannelMetric{}
			}
			var persistedCount int64
			require.NoError(t, db.Model(persistedModel).
				Where("channel_id = ? AND model_name = ?", testCase.channelID, cacheKey.Model).
				Count(&persistedCount).Error)
			assert.Zero(t, persistedCount)
			_, metricCached := routinghotcache.GetMetric(cacheKey)
			_, breakerCached := routinghotcache.GetBreaker(cacheKey)
			assert.False(t, metricCached)
			assert.False(t, breakerCached)
			assert.Zero(t, routingbreaker.RuntimeStats().Entries)
		})
	}
}

func TestFlushRoutingRuntimeStateRejectsRemainingDirtyStateAfterChannelRecreation(t *testing.T) {
	testCases := []struct {
		name      string
		channelID int
		metric    bool
	}{
		{name: "metric", channelID: 1212, metric: true},
		{name: "breaker", channelID: 1213},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingChannelBinding{},
				&model.RoutingChannelMetric{},
				&model.RoutingBreakerState{},
			))
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

			oldChannel := model.Channel{
				Id: testCase.channelID, Name: "channel-recreated-" + testCase.name,
				Key: "single-key", CreatedTime: 1,
			}
			require.NoError(t, db.Create(&oldChannel).Error)
			oldBinding := model.RoutingChannelBinding{
				ChannelID:     testCase.channelID,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       "https://old-routing.example.com",
				UpstreamGroup: "default",
				Enabled:       true,
				CreatedTime:   1,
				UpdatedTime:   1,
			}
			require.NoError(t, db.Create(&oldBinding).Error)

			cacheKeys := make([]routinghotcache.Key, 0, 3)
			if testCase.metric {
				metrics := make([]model.RoutingChannelMetric, 0, 3)
				for index, modelName := range []string{"a-old-generation", "b-old-generation", "c-old-generation"} {
					cacheKeys = append(cacheKeys, routinghotcache.Key{
						ChannelID:   testCase.channelID,
						APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						Model:       modelName,
						Group:       "default",
					})
					metrics = append(metrics, model.RoutingChannelMetric{
						ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						ModelName: modelName, Group: "default", BucketTs: int64(60 + index), RequestCount: 1,
					})
				}
				routingmetrics.RequeueSnapshots(metrics)
			} else {
				for _, modelName := range []string{"a-old-generation", "b-old-generation", "c-old-generation"} {
					cacheKey := routinghotcache.Key{
						ChannelID:   testCase.channelID,
						APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						Model:       modelName,
						Group:       "default",
					}
					cacheKeys = append(cacheKeys, cacheKey)
					routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
						ChannelID: testCase.channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						Model: modelName, Group: "default",
					}, routingbreaker.FailureProvider5xx)
				}
			}

			afterFirstPostCheck := make(chan struct{})
			releaseFirstPostCheck := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(releaseFirstPostCheck)
				}
			}()
			var channelQueries atomic.Int64
			callbackName := "test:block_flush_before_channel_recreation_" + testCase.name
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
					return
				}
				if channelQueries.Add(1) == 3 {
					close(afterFirstPostCheck)
					<-releaseFirstPostCheck
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			type flushResult struct {
				summary map[string]any
				err     error
			}
			result := make(chan flushResult, 1)
			go func() {
				summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
				result <- flushResult{summary: summary, err: err}
			}()

			select {
			case <-afterFirstPostCheck:
			case <-time.After(time.Second):
				require.FailNow(t, "flush did not finish the first channel post-check")
			}

			persistedModel := any(&model.RoutingBreakerState{})
			if testCase.metric {
				persistedModel = &model.RoutingChannelMetric{}
			}
			var committedCount int64
			require.NoError(t, db.Model(persistedModel).
				Where("channel_id = ?", testCase.channelID).
				Count(&committedCount).Error)
			assert.Equal(t, int64(1), committedCount)

			replacementChannel := model.Channel{
				Id: testCase.channelID, Name: "replacement-" + testCase.name,
				Key: "replacement-key", CreatedTime: 2,
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("id = ?", oldChannel.Id).Delete(&model.Channel{}).Error; err != nil {
					return err
				}
				if err := tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingChannelMetric{}).Error; err != nil {
					return err
				}
				if err := tx.Where("channel_id = ?", testCase.channelID).Delete(&model.RoutingBreakerState{}).Error; err != nil {
					return err
				}
				return tx.Create(&replacementChannel).Error
			}))
			require.NotEqual(t, oldChannel.RoutingGeneration, replacementChannel.RoutingGeneration)
			close(releaseFirstPostCheck)
			released = true

			flush := <-result
			require.NoError(t, flush.err)
			assert.Equal(t, 0, flush.summary["metrics"])
			assert.Equal(t, 0, flush.summary["breakers"])
			assert.Equal(t, int64(4), channelQueries.Load(), "the rejected channel must not query or write the third dirty item")

			var currentBinding model.RoutingChannelBinding
			require.NoError(t, db.Where("channel_id = ?", testCase.channelID).First(&currentBinding).Error)
			assert.Equal(t, oldBinding.ID, currentBinding.ID)
			for _, table := range []any{&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}} {
				var count int64
				require.NoError(t, db.Model(table).Where("channel_id = ?", testCase.channelID).Count(&count).Error)
				assert.Zero(t, count)
			}
			for _, cacheKey := range cacheKeys {
				_, metricCached := routinghotcache.GetMetric(cacheKey)
				_, breakerCached := routinghotcache.GetBreaker(cacheKey)
				assert.False(t, metricCached)
				assert.False(t, breakerCached)
			}
			assert.Empty(t, routingmetrics.Snapshots())
			assert.Empty(t, routingbreaker.DirtySnapshots())
			assert.Zero(t, routingbreaker.RuntimeStats().Entries)
		})
	}
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
	createRoutingRuntimeBindings(t, db, 81)

	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "metric-a", Group: "default", BucketTs: 60, RequestCount: 1},
		{ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "metric-b", Group: "default", BucketTs: 60, RequestCount: 1},
	})
	for _, modelName := range []string{"breaker-a", "breaker-b"} {
		routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
			ChannelID: 81, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: modelName, Group: "default",
		}, routingbreaker.FailureProvider5xx)
	}

	const callbackName = "test:count_flush_channel_eligibility_queries"
	channelQueryCount := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" &&
			len(tx.Statement.Selects) == 1 && tx.Statement.Selects[0] == "channel_info" {
			channelQueryCount++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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
	createRoutingRuntimeBindings(t, db, 91, 93, 94)
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

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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
	summary, err = flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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
	createRoutingRuntimeBindings(t, db, 101, 103, 104)
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
		routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx)
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

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{})
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
	summary, err = flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{})
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
	createRoutingRuntimeBindings(t, db, 41)

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

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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
	summary, err = flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{MetricBucketSec: 60})
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
	createRoutingRuntimeBindings(t, db, 51)

	keys := []routingbreaker.Key{
		{ChannelID: 51, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "a-valid-prefix", Group: "default"},
		{ChannelID: 51, APIKeyIndex: 1, Model: "b-invalid-positive", Group: "default"},
		{ChannelID: 52, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "c-invalid-multi", Group: "default"},
		{ChannelID: 51, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "d-valid-suffix", Group: "default"},
	}
	for _, key := range keys {
		routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx)
	}

	const callbackName = "test:fail_second_routing_breaker_upsert"
	upsertCount := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingBreakerState{}).TableName() {
			return
		}
		upsertCount++
		if upsertCount == 2 {
			tx.AddError(errors.New("forced breaker persistence failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{})
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

	require.NoError(t, db.Callback().Create().Remove(callbackName))
	summary, err = flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{})
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
	deps.flush = func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil }
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
	<-refreshCompleted
	_, cached := routinghotcache.GetMetric(key)
	assert.False(t, cached)

	runtime.Close()
	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
}

func TestFlushRoutingRuntimeStateAppliesConfiguredRetention(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingHedgeAttemptAudit{},
	))
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	smartRoutingRetentionLast.Store(0)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})

	retentionSetting := smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                 true,
		Mode:                    smart_routing_setting.ModeObserve,
		SyncIntervalMin:         1,
		HotcacheRefreshSec:      1,
		MetricBucketSec:         60,
		FlushIntervalMin:        1,
		RetentionDays:           1,
		HedgeAuditRetentionDays: 1,
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
	require.NoError(t, db.Create(&[]model.RoutingHedgeAttemptAudit{
		{
			AttemptKey: strings.Repeat("a", 64), State: model.RoutingHedgeAttemptStateCompleted,
			CompletedTimeMs: (now - 2*86400) * 1_000,
		},
		{
			AttemptKey: strings.Repeat("b", 64), State: model.RoutingHedgeAttemptStateCompleted,
			CompletedTimeMs: now * 1_000,
		},
		{
			AttemptKey: strings.Repeat("c", 64), State: model.RoutingHedgeAttemptStateStarted,
			CompletedTimeMs: (now - 2*86400) * 1_000,
		},
	}).Error)

	summary, err := flushRoutingRuntimeState(context.Background(), retentionSetting)
	require.NoError(t, err)
	assert.Equal(t, int64(1), summary["retained_metrics_deleted"])
	assert.Equal(t, int64(1), summary["retained_hedge_audits_deleted"])

	var remaining []model.RoutingChannelMetric
	require.NoError(t, db.Order("bucket_ts asc").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, fresh.BucketTs, remaining[0].BucketTs)
	var remainingHedgeAudits int64
	require.NoError(t, db.Model(&model.RoutingHedgeAttemptAudit{}).Count(&remainingHedgeAudits).Error)
	assert.Equal(t, int64(2), remainingHedgeAudits)
}

func TestFlushRoutingRuntimeStateDoesNotAdvanceRetentionThrottleAfterHedgeCleanupFailure(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	require.NoError(t, db.Migrator().DropTable(&model.RoutingHedgeAttemptAudit{}))
	routingmetrics.ResetForTest()
	smartRoutingRetentionLast.Store(0)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})

	_, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec: 60, HedgeAuditRetentionDays: 1,
	})
	require.Error(t, err)
	assert.Zero(t, smartRoutingRetentionLast.Load())
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
	createRoutingRuntimeBindings(t, db, channelID)
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
	_, err := flushRoutingRuntimeState(context.Background(), setting)
	require.NoError(t, err)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{secondDelta})
	_, err = flushRoutingRuntimeState(context.Background(), setting)
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

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{
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

	summary, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.SmartRoutingSetting{
		MetricBucketSec: 60,
		RetentionDays:   int(^uint(0) >> 1),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), summary["retained_metrics_deleted"])

	var remaining int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Count(&remaining).Error)
	assert.Equal(t, int64(2), remaining)
}
