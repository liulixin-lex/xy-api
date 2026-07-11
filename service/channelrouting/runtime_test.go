package channelrouting

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeRunsEnabledWorkersAndStopsCleanly(t *testing.T) {
	waits := make(chan time.Duration, 4)
	var refreshRuns atomic.Int64
	var flushRuns atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			refreshRuns.Add(1)
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushRuns.Add(1)
			return nil
		},
		wait: func(ctx context.Context, duration time.Duration) bool {
			select {
			case waits <- duration:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
		jitter: func(duration time.Duration) time.Duration { return duration },
	})

	require.NotZero(t, <-waits)
	require.NotZero(t, <-waits)
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(1), refreshRuns.Load())
	assert.Equal(t, int64(1), flushRuns.Load())
	assert.Equal(t, int64(1), runtime.Stats().Refresh.Runs)
	assert.Equal(t, int64(1), runtime.Stats().Flush.Runs)
}

func TestRuntimeDisabledStateDoesNotInvokeCallbacks(t *testing.T) {
	waitEntered := make(chan struct{}, 2)
	var runs atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: false}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case waitEntered <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
	})

	<-waitEntered
	<-waitEntered
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Zero(t, runs.Load())
	assert.Zero(t, runtime.Stats().Refresh.Runs)
	assert.Zero(t, runtime.Stats().Flush.Runs)
}

func TestRuntimeFailureUsesBackoffAndExposesSanitizedState(t *testing.T) {
	waits := make(chan time.Duration, 2)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			return errors.New("refresh failed with token sk-secret")
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		wait: func(ctx context.Context, duration time.Duration) bool {
			select {
			case waits <- duration:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
		jitter: func(duration time.Duration) time.Duration { return duration },
	})

	first := <-waits
	second := <-waits
	assert.Contains(t, []time.Duration{first, second}, observeBackoffBase)
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	stats := runtime.Stats().Refresh
	assert.Equal(t, int64(1), stats.Runs)
	assert.Equal(t, int64(1), stats.Failures)
	assert.Equal(t, int64(1), stats.ConsecutiveFailures)
	assert.NotEmpty(t, stats.LastError)
}

func TestRuntimeFinalFlushRunsOnceAfterWorkersStop(t *testing.T) {
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)

	waits := make(chan struct{}, 2)
	var flushRuns atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushRuns.Add(1)
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case waits <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
	})

	<-waits
	<-waits
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(2), flushRuns.Load())
}

func TestRuntimeFinalFlushIncludesRecordsEnqueuedAfterWorkersStop(t *testing.T) {
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	waits := make(chan struct{}, 2)
	var flushRuns atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushRuns.Add(1)
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case waits <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
	})
	<-waits
	<-waits
	runtime.Close()
	<-runtime.done
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(2), flushRuns.Load())
}

func TestRuntimeFinalFlushIncludesStableTelemetryAfterWorkersStop(t *testing.T) {
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	waits := make(chan struct{}, 2)
	var flushRuns atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			flushRuns.Add(1)
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case waits <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			<-ctx.Done()
			return false
		},
	})
	<-waits
	<-waits
	runtime.Close()
	<-runtime.done
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 0, ChannelID: 1,
		Model: "gpt-test", BucketTs: 60, LastSnapshotRevision: 1, RequestCount: 1,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(2), flushRuns.Load())
}

func TestRuntimeCanceledParentDoesNotStartWorkers(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	var runs atomic.Int64
	runtime := newRuntime(parent, runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Zero(t, runs.Load())
}

func TestRuntimeRetentionFailureRemainsImmediatelyRetryable(t *testing.T) {
	var calls atomic.Int64
	runtime := &Runtime{deps: runtimeDeps{
		retention: func(context.Context, int) (int64, error) {
			if calls.Add(1) == 1 {
				return 0, errors.New("temporary retention failure")
			}
			return 3, nil
		},
	}}

	require.Error(t, runtime.runRetention(context.Background(), 7))
	assert.Zero(t, runtime.lastRetentionUnix.Load())
	require.NoError(t, runtime.runRetention(context.Background(), 7))
	assert.Equal(t, int64(2), calls.Load())
	assert.Positive(t, runtime.lastRetentionUnix.Load())
	require.NoError(t, runtime.runRetention(context.Background(), 7))
	assert.Equal(t, int64(2), calls.Load())
}

func TestRuntimeTopologyChangeWakesRefreshWithoutBlockingPublisher(t *testing.T) {
	changes := make(chan struct{}, 1)
	refreshRuns := make(chan struct{}, 2)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true, HotcacheRefreshSec: 300}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			refreshRuns <- struct{}{}
			return nil
		},
		flush:           func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		topologyChanges: changes,
		wait:            waitRuntime,
	})

	<-refreshRuns
	changes <- struct{}{}
	<-refreshRuns
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
}

func TestRoutingTopologyChangeSignalCoalesces(t *testing.T) {
	model.ResetRoutingTopologyChangesForTest()
	t.Cleanup(model.ResetRoutingTopologyChangesForTest)
	model.NotifyRoutingTopologyChanged()
	model.NotifyRoutingTopologyChanged()

	select {
	case <-model.RoutingTopologyChanges():
	default:
		t.Fatal("expected topology change signal")
	}
	select {
	case <-model.RoutingTopologyChanges():
		t.Fatal("topology change signal should be coalesced")
	default:
	}
}
