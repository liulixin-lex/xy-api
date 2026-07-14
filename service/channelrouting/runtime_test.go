package channelrouting

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestLocalDrainIntervalPreservesAggregationWindow(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	previousRedis := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedis
	})
	assert.Equal(t, observeLocalDrainFallback, observeLocalDrainInterval(smart_routing_setting.SmartRoutingSetting{}))

	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	common.RedisEnabled = true
	common.RDB = client
	assert.Equal(t, observeLocalDrainRedis, observeLocalDrainInterval(smart_routing_setting.SmartRoutingSetting{}))
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

func TestRuntimeActiveProbeRequiresItsFeatureSwitch(t *testing.T) {
	var runs atomic.Int64
	runtime := &Runtime{deps: runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled: true, Mode: smart_routing_setting.ModeBalanced, ActiveProbeEnabled: false,
			}
		},
		wait: func(context.Context, time.Duration) bool { return false },
	}}
	runtime.runWorker(
		context.Background(),
		func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
		activeProbePollInterval,
		&runtime.activeProbeStats,
	)
	assert.Zero(t, runs.Load())
}

func TestRuntimeActiveProbeOperationsDrainWhileRoutingIsDisabled(t *testing.T) {
	var runs atomic.Int64
	runtime := &Runtime{deps: runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: false, ActiveProbeEnabled: false}
		},
		wait: func(context.Context, time.Duration) bool { return false },
	}}
	runtime.runWorker(
		context.Background(),
		func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			runs.Add(1)
			return nil
		},
		activeProbeOperationInterval,
		&runtime.activeProbeOpStats,
	)
	assert.Equal(t, int64(1), runs.Load())
}

func TestRuntimeCloseCancelsActiveProbe(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{
				Enabled: true, Mode: smart_routing_setting.ModeBalanced, ActiveProbeEnabled: true,
				ActiveProbeOpenSec: 10,
			}
		},
		activeProbe: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		},
		wait: waitRuntime,
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active probe worker did not start")
	}
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	select {
	case <-canceled:
	default:
		t.Fatal("active probe worker was not canceled")
	}
	assert.Equal(t, int64(1), runtime.Stats().ActiveProbe.Runs)
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

func TestRuntimeRetentionRunsIndependentlyFromFlushFailure(t *testing.T) {
	ResetDecisionAuditsForTest()
	routingmetrics.ResetForTest()
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(func() {
		ResetDecisionAuditsForTest()
		routingmetrics.ResetForTest()
		ResetRoutingTelemetryTransportForTest()
	})
	retentionRan := make(chan struct{}, 1)
	flushRan := make(chan struct{}, 1)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true, RetentionDays: 7}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			select {
			case flushRan <- struct{}{}:
			default:
			}
			return errors.New("persistent flush failure")
		},
		retention: func(context.Context, int) (int64, error) {
			select {
			case retentionRan <- struct{}{}:
			default:
			}
			return 1, nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
		jitter: func(duration time.Duration) time.Duration { return duration },
	})
	select {
	case <-retentionRan:
	case <-time.After(time.Second):
		t.Fatal("retention was blocked by flush failure")
	}
	select {
	case <-flushRan:
	case <-time.After(time.Second):
		t.Fatal("flush worker did not record its independent failure")
	}
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(1), runtime.Stats().Retention.Runs)
	assert.Equal(t, int64(1), runtime.Stats().Flush.Failures)
}

func TestRuntimeRetentionRunsWhileRoutingIsDisabled(t *testing.T) {
	retentionRan := make(chan struct{}, 1)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: false, RetentionDays: 7}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		flush:   func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		retention: func(context.Context, int) (int64, error) {
			retentionRan <- struct{}{}
			return 1, nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	})
	select {
	case <-retentionRan:
	case <-time.After(time.Second):
		t.Fatal("disabled routing did not retain its cleanup path")
	}
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Equal(t, int64(1), runtime.Stats().Retention.Runs)
	assert.Zero(t, runtime.Stats().Refresh.Runs)
	assert.Zero(t, runtime.Stats().Flush.Runs)
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

func TestRuntimeFinalFlushRetriesPendingTelemetryEnvelope(t *testing.T) {
	db := openRuntimeTestDB(t)
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 0, ChannelID: 1,
		Model: "gpt-test", BucketTs: 60, LastSnapshotRevision: 1,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}})

	_, err := FlushStableTelemetryContext(context.Background())
	require.Error(t, err)
	assert.Zero(t, routingmetrics.StableRuntimeStats().Buckets)
	assert.Equal(t, 1, RoutingTelemetryTransportRuntimeStats().PendingEnvelopes)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))

	deps := defaultRuntimeDeps()
	runtime := &Runtime{deps: deps, finalDone: make(chan struct{})}
	runtime.startFinalFlush(context.Background())
	select {
	case <-runtime.finalDone:
	case <-time.After(time.Second):
		t.Fatal("pending-only final flush did not finish")
	}
	runtime.finalMu.Lock()
	finalErr := runtime.finalErr
	runtime.finalMu.Unlock()
	require.NoError(t, finalErr)
	assert.Zero(t, RoutingTelemetryTransportRuntimeStats().PendingEnvelopes)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(1), persisted)
}

func TestRuntimeFinalFlushDrainsMoreThanOneTelemetryBudget(t *testing.T) {
	db := openRuntimeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	snapshotCount := stableTelemetryFlushMaxEnvelopes*model.RoutingTelemetryMaxItems + 1
	snapshots := make([]routingmetrics.StableSnapshot, snapshotCount)
	for index := range snapshots {
		snapshots[index] = routingmetrics.StableSnapshot{
			PoolID: 1, PoolMemberID: index + 1, CredentialID: index + 1, ChannelID: index + 1,
			Model: "gpt-test", BucketTs: 60, LastSnapshotRevision: 1,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		}
	}
	routingmetrics.RequeueStableSnapshots(snapshots)

	deps := defaultRuntimeDeps()
	runtime := &Runtime{deps: deps, finalDone: make(chan struct{})}
	runtime.startFinalFlush(context.Background())
	select {
	case <-runtime.finalDone:
	case <-time.After(3 * time.Second):
		t.Fatal("multi-budget final flush did not finish")
	}
	runtime.finalMu.Lock()
	finalErr := runtime.finalErr
	runtime.finalMu.Unlock()
	require.NoError(t, finalErr)
	assert.Zero(t, routingmetrics.StableRuntimeStats().Buckets)
	assert.Zero(t, RoutingTelemetryTransportRuntimeStats().PendingEnvelopes)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(snapshotCount), persisted)
}

func TestRuntimeFinalFlushRetriesTransientFailure(t *testing.T) {
	db := openRuntimeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)

	var calls atomic.Int64
	runtime := &Runtime{
		deps: runtimeDeps{
			getSetting: smart_routing_setting.GetSetting,
			flush: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
				if calls.Add(1) == 1 {
					return errors.New("transient final flush failure")
				}
				_, err := FlushDecisionAuditsContext(ctx)
				return err
			},
		},
		finalDone: make(chan struct{}),
	}
	runtime.startFinalFlush(context.Background())
	select {
	case <-runtime.finalDone:
	case <-time.After(time.Second):
		t.Fatal("transient final flush retry did not finish")
	}
	runtime.finalMu.Lock()
	finalErr := runtime.finalErr
	runtime.finalMu.Unlock()
	require.NoError(t, finalErr)
	assert.Equal(t, int64(2), calls.Load())
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).Count(&persisted).Error)
	assert.Equal(t, int64(1), persisted)
}

func TestRuntimeDisabledStateDrainsBufferedWrites(t *testing.T) {
	db := openRuntimeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)

	flushStarted := make(chan struct{}, 1)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: false}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			t.Fatal("disabled refresh must not run")
			return nil
		},
		flush: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			select {
			case flushStarted <- struct{}{}:
			default:
			}
			_, err := FlushDecisionAuditsContext(ctx)
			return err
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	})
	select {
	case <-flushStarted:
	case <-time.After(time.Second):
		t.Fatal("disabled runtime did not drain buffered writes")
	}
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
	assert.Zero(t, DecisionAuditsStats().Entries)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).Count(&persisted).Error)
	assert.Equal(t, int64(1), persisted)
}

func openRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime.db"), &gorm.Config{})
	require.NoError(t, err)
	return db
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

	require.Error(t, runtime.runRetention(context.Background(), 7, 0))
	assert.Zero(t, runtime.lastRetentionUnix.Load())
	require.NoError(t, runtime.runRetention(context.Background(), 7, 0))
	assert.Equal(t, int64(2), calls.Load())
	assert.Positive(t, runtime.lastRetentionUnix.Load())
	require.NoError(t, runtime.runRetention(context.Background(), 7, 0))
	assert.Equal(t, int64(2), calls.Load())
}

func TestRuntimeHedgeRetentionUsesIndependentBoundedContext(t *testing.T) {
	var ordinaryDone <-chan struct{}
	runtime := &Runtime{deps: runtimeDeps{
		retention: func(ctx context.Context, _ int) (int64, error) {
			_, hasDeadline := ctx.Deadline()
			require.True(t, hasDeadline)
			ordinaryDone = ctx.Done()
			return 1, nil
		},
		hedgeRetention: func(ctx context.Context, _ int) (int64, error) {
			_, hasDeadline := ctx.Deadline()
			require.True(t, hasDeadline)
			assert.NotEqual(t, ordinaryDone, ctx.Done())
			assert.NoError(t, ctx.Err())
			return 1, nil
		},
	}}

	require.NoError(t, runtime.runRetention(context.Background(), 7, 30))
	assert.Positive(t, runtime.lastRetentionUnix.Load())
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

func TestRuntimeUsesCheapRefreshUntilTopologyChanges(t *testing.T) {
	changes := make(chan struct{}, 1)
	periodicRuns := make(chan struct{}, 1)
	forcedRuns := make(chan struct{}, 1)
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true, HotcacheRefreshSec: 300}
		},
		refresh: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			forcedRuns <- struct{}{}
			return nil
		},
		refreshCurrent: func(context.Context, smart_routing_setting.SmartRoutingSetting) error {
			periodicRuns <- struct{}{}
			return nil
		},
		flush:           func(context.Context, smart_routing_setting.SmartRoutingSetting) error { return nil },
		topologyChanges: changes,
		wait:            waitRuntime,
	})

	select {
	case <-periodicRuns:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not use the cheap policy-head path")
	}
	changes <- struct{}{}
	select {
	case <-forcedRuns:
	case <-time.After(time.Second):
		t.Fatal("topology change did not trigger leased reconcile path")
	}
	runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Wait(ctx))
}

func TestRefreshSnapshotIfNeededReturnsLightweightMetadataForCurrentRevision(t *testing.T) {
	db := openRuntimeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingPolicyHead{}))
	withSnapshotTestDB(t, db)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	now := common.GetTimestamp()
	policyHash := strings.Repeat("a", 64)
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID:                  1,
		CurrentRevision:     7,
		CurrentActivationID: 11,
		CurrentHash:         policyHash,
		CurrentStage:        model.RoutingDeploymentStageShadow,
		CreatedTime:         now,
		UpdatedTime:         now,
	}).Error)
	SetSnapshotForTest(SnapshotView{
		Revision:        7,
		PolicyHash:      policyHash,
		ActivationID:    11,
		ActivationStage: model.RoutingDeploymentStageShadow,
		BuiltAtUnix:     now,
		Pools:           []PoolSnapshot{{ID: 1, Members: []PoolMemberSnapshot{{ID: 1}}}},
		Channels:        []ChannelSnapshot{{ID: 1, CredentialIDs: []int{1}}},
		Stats:           SnapshotStats{PoolCount: 1, MemberCount: 1, ChannelCount: 1},
	})

	metadata, err := refreshSnapshotIfNeededContext(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), metadata.Revision)
	assert.Equal(t, policyHash, metadata.PolicyHash)
	assert.Equal(t, int64(11), metadata.ActivationID)
	assert.Equal(t, model.RoutingDeploymentStageShadow, metadata.ActivationStage)
	assert.Equal(t, 1, metadata.Stats.MemberCount)
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
