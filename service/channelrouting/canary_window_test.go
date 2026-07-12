package channelrouting

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCanaryWindowFlushesOneAbsoluteCheckpointForBothCohorts(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_010, 0)}
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: 8, Shards: 4, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	db := openCanaryWindowTestDB(t, true)
	withCanaryWindowTestDB(t, db)

	identity := canaryWindowIdentityForTest()
	require.NoError(t, aggregator.Record(CanaryLogicalOutcome{
		Identity: identity, Cohort: model.RoutingDecisionCohortControl, CompletedAt: clock.Now(),
		Success: true, Attempts: 1, CostKnown: true, ExpectedPlatformCostNanoUSD: 100,
		ClientTTFTMilliseconds: 120,
	}))
	require.NoError(t, aggregator.Record(CanaryLogicalOutcome{
		Identity: identity, Cohort: model.RoutingDecisionCohortCanary, CompletedAt: clock.Now(),
		Success: false, RoutingFailure: true, Attempts: 2, CostKnown: true,
		ExpectedPlatformCostNanoUSD: 250,
	}))

	clock.Advance(2 * time.Minute)
	flushed, err := aggregator.FlushContext(context.Background(), 8)
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	assert.Equal(t, 0, aggregator.Stats().Entries)

	var stored model.RoutingRuntimeCheckpoint
	require.NoError(t, db.Where("checkpoint_kind = ?", CanaryCohortWindowCheckpointKind).First(&stored).Error)
	payload, err := DecodeCanaryCohortWindowCheckpoint(stored)
	require.NoError(t, err)
	assert.Equal(t, int64(1), payload.Control.LogicalRequests)
	assert.Equal(t, int64(1), payload.Control.Successes)
	assert.Equal(t, int64(1), payload.Control.Attempts)
	assert.Equal(t, int64(100), payload.Control.ExpectedPlatformCostNanoUSD)
	assert.Equal(t, int64(1), payload.Control.TTFTSampleCount)
	assert.Equal(t, int64(1), payload.Canary.LogicalRequests)
	assert.Equal(t, int64(1), payload.Canary.Failures)
	assert.Equal(t, int64(1), payload.Canary.RoutingFailures)
	assert.Equal(t, int64(2), payload.Canary.Attempts)
	assert.Equal(t, int64(250), payload.Canary.ExpectedPlatformCostNanoUSD)
	sketch, err := routingdistribution.DecodeDurationSketch(
		payload.Control.TTFTSketch, payload.Control.TTFTSketchCodecVersion,
	)
	require.NoError(t, err)
	quantile, err := sketch.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, quantile.Known)
	assert.InDelta(t, 120, quantile.ValueMilliseconds, 3)

	flushed, err = aggregator.FlushContext(context.Background(), 8)
	require.NoError(t, err)
	assert.Zero(t, flushed)
}

func TestCanaryWindowDatabaseFailureRetainsFrozenPayloadForIdempotentRetry(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_010, 0)}
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: 4, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, aggregator.Record(CanaryLogicalOutcome{
		Identity: canaryWindowIdentityForTest(), Cohort: model.RoutingDecisionCohortCanary,
		CompletedAt: clock.Now(), Success: true, Attempts: 1, ClientTTFTMilliseconds: 90,
	}))
	clock.Advance(2 * time.Minute)

	flushed, err := aggregator.FlushContext(context.Background(), 4)
	assert.Error(t, err)
	assert.Zero(t, flushed)
	assert.Equal(t, 1, aggregator.Stats().Entries)
	clock.Advance(3 * time.Hour)
	assert.Equal(t, 1, aggregator.Stats().Entries, "a frozen checkpoint must survive TTL pruning until persistence succeeds")
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	flushed, err = aggregator.FlushContext(context.Background(), 4)
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)

	var count int64
	require.NoError(t, db.Model(&model.RoutingRuntimeCheckpoint{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCanaryCostNanoUSDRejectsNonFiniteNegativeAndInt64Boundary(t *testing.T) {
	for _, cost := range []float64{
		math.NaN(), math.Inf(1), math.Inf(-1), -0.01, float64(math.MaxInt64) / 1_000_000_000,
	} {
		value, known := CanaryCostNanoUSD(cost)
		assert.False(t, known)
		assert.Zero(t, value)
	}

	value, known := CanaryCostNanoUSD(0.000001)
	require.True(t, known)
	assert.Equal(t, int64(1_000), value)
}

func TestCanaryWindowConcurrentRecordingIsExactAndBounded(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_010, 0)}
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: 2, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	identity := canaryWindowIdentityForTest()

	const workers = 16
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			errorsByWorker <- aggregator.Record(CanaryLogicalOutcome{
				Identity: identity, Cohort: model.RoutingDecisionCohortCanary,
				CompletedAt: clock.Now(), Success: true, Attempts: 1,
			})
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for recordErr := range errorsByWorker {
		require.NoError(t, recordErr)
	}
	assert.Equal(t, int64(workers), aggregator.Stats().Recorded)

	clock.Advance(2 * time.Minute)
	other := identity
	other.PoolID++
	err = aggregator.Record(CanaryLogicalOutcome{
		Identity: other, Cohort: model.RoutingDecisionCohortControl,
		CompletedAt: clock.Now(), Success: true, Attempts: 1,
	})
	require.NoError(t, err)
	clock.Advance(2 * time.Minute)
	third := other
	third.PoolID++
	err = aggregator.Record(CanaryLogicalOutcome{
		Identity: third, Cohort: model.RoutingDecisionCohortControl,
		CompletedAt: clock.Now(), Success: true, Attempts: 1,
	})
	assert.ErrorIs(t, err, ErrCanaryWindowEntriesFull, "a frozen window must not be evicted before checkpoint persistence")
}

func TestCanaryWindowRejectsInvalidOutcomeAndTamperedCheckpoint(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_010, 0)}
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: 2, Shards: 1, TTL: time.Minute, Clock: clock,
	})
	require.NoError(t, err)
	identity := canaryWindowIdentityForTest()
	err = aggregator.Record(CanaryLogicalOutcome{
		Identity: identity, Cohort: model.RoutingDecisionCohortCanary,
		CompletedAt: clock.Now(), Success: true, Attempts: 1,
	})
	assert.ErrorIs(t, err, ErrCanaryWindowInvalid, "TTL shorter than window plus lateness must be rejected")

	payload := CanaryCohortWindowCheckpoint{
		SchemaVersion: canaryCohortWindowSchemaVersion,
		PoolID:        29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		RolloutKey: RolloutKey(strings.Repeat("a", 64)), WindowSeconds: 60,
		WindowStartUnixMs: 1_700_000_040_000, WindowEndUnixMs: 1_700_000_100_000,
		Control: CanaryCohortWindowStats{LogicalRequests: 1, Successes: 1, Attempts: 1},
	}
	checkpoint, err := model.NewRoutingRuntimeCheckpoint(
		"node", CanaryCohortWindowCheckpointKind, CanaryWindowCheckpointScope(payload),
		11, 1, payload, payload.WindowEndUnixMs/1_000, payload.WindowEndUnixMs/1_000+3_600,
	)
	require.NoError(t, err)
	decoded, err := DecodeCanaryCohortWindowCheckpoint(checkpoint)
	require.NoError(t, err)
	assert.Equal(t, payload, decoded)
	checkpoint.Scope += "/tampered"
	_, err = DecodeCanaryCohortWindowCheckpoint(checkpoint)
	assert.ErrorIs(t, err, ErrCanaryWindowInvalid)
}

func openCanaryWindowTestDB(t *testing.T, migrate bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	if migrate {
		require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	}
	return db
}

func withCanaryWindowTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousType, previousLogType)
	})
}

func canaryWindowIdentityForTest() CanaryWindowIdentity {
	return CanaryWindowIdentity{
		PoolID: 29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		RolloutKey: RolloutKey(strings.Repeat("a", 64)), WindowSeconds: 60, LatenessSeconds: 5,
	}
}
