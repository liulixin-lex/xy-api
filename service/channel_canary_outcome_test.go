package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type canaryOutcomeTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *canaryOutcomeTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *canaryOutcomeTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func TestChannelRoutingCanaryOutcomeTracksAttemptsCostAndClientTTFT(t *testing.T) {
	truncate(t)
	clock := &canaryOutcomeTestClock{now: time.Now().Truncate(time.Second)}
	aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
		MaxEntries: 8, Shards: 4, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	channelrouting.ResetCanaryWindowAggregatorForTest(aggregator)
	t.Cleanup(func() { channelrouting.ResetCanaryWindowAggregatorForTest() })
	require.NoError(t, model.DB.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))

	gate, err := channelrouting.EvaluateCanaryGate(29, 401, 11, "cohort-0005", 100)
	require.NoError(t, err)
	require.True(t, gate.InCanary)
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
		ExpectedCostKnown: true, ExpectedCostUSD: 0.000001,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
		ExpectedCostKnown: true, ExpectedCostUSD: 0.000002,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, true, true, false, 125, clock.Now()))

	clock.Advance(2 * time.Minute)
	flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	var stored model.RoutingRuntimeCheckpoint
	require.NoError(t, model.DB.Where("checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind).First(&stored).Error)
	payload, err := channelrouting.DecodeCanaryCohortWindowCheckpoint(stored)
	require.NoError(t, err)
	assert.Equal(t, int64(1), payload.Canary.LogicalRequests)
	assert.Equal(t, int64(1), payload.Canary.Successes)
	assert.Equal(t, int64(2), payload.Canary.Attempts)
	assert.Equal(t, int64(1), payload.Canary.CostKnownRequests)
	assert.Equal(t, int64(3_000), payload.Canary.ExpectedPlatformCostNanoUSD)
	assert.Equal(t, int64(1), payload.Canary.TTFTSampleCount)
}

func TestChannelRoutingCanaryOutcomeCrossPoolClosesPriorUnitAsFailure(t *testing.T) {
	truncate(t)
	clock := &canaryOutcomeTestClock{now: time.Now().Truncate(time.Second)}
	aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
		MaxEntries: 8, Shards: 4, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	channelrouting.ResetCanaryWindowAggregatorForTest(aggregator)
	t.Cleanup(func() { channelrouting.ResetCanaryWindowAggregatorForTest() })
	require.NoError(t, model.DB.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))

	firstGate := channelrouting.CanaryGate{
		PoolID: 29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		Bucket: 0, InCanary: true, RolloutKey: channelrouting.RolloutKey(strings.Repeat("a", 64)),
	}
	secondGate := firstGate
	secondGate.PoolID = 30
	secondGate.RolloutKey = channelrouting.RolloutKey(strings.Repeat("b", 64))
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: firstGate, WindowSeconds: 60, LatenessSeconds: 5,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: secondGate, WindowSeconds: 60, LatenessSeconds: 5,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, true, true, false, 80, clock.Now()))

	clock.Advance(2 * time.Minute)
	flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, flushed)
	var checkpoints []model.RoutingRuntimeCheckpoint
	require.NoError(t, model.DB.Where("checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind).Find(&checkpoints).Error)
	require.Len(t, checkpoints, 2)
	byPool := make(map[int]channelrouting.CanaryCohortWindowCheckpoint, 2)
	for index := range checkpoints {
		payload, decodeErr := channelrouting.DecodeCanaryCohortWindowCheckpoint(checkpoints[index])
		require.NoError(t, decodeErr)
		byPool[payload.PoolID] = payload
	}
	assert.Equal(t, int64(1), byPool[29].Canary.Failures)
	assert.Equal(t, int64(1), byPool[29].Canary.RoutingFailures)
	assert.Equal(t, int64(1), byPool[30].Canary.Successes)
}

func TestChannelRoutingCanaryOutcomeIgnoresCallerOwnedResult(t *testing.T) {
	clock := &canaryOutcomeTestClock{now: time.Unix(1_700_000_010, 0)}
	aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
		MaxEntries: 4, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	channelrouting.ResetCanaryWindowAggregatorForTest(aggregator)
	t.Cleanup(func() { channelrouting.ResetCanaryWindowAggregatorForTest() })
	gate := channelrouting.CanaryGate{
		PoolID: 29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		Bucket: 0, InCanary: true, RolloutKey: channelrouting.RolloutKey(strings.Repeat("a", 64)),
	}
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, clock.Now()))
	assert.Zero(t, aggregator.Stats().Recorded)
	_, exists := common.GetContextKeyType[*channelRoutingCanaryOutcomeContext](ctx, constant.ContextKeyRoutingCanaryOutcome)
	assert.False(t, exists)
}

func TestChannelRoutingCanaryOutcomeMarksZeroAttemptFailureAsRoutingFailure(t *testing.T) {
	truncate(t)
	clock := &canaryOutcomeTestClock{now: time.Now().Truncate(time.Second)}
	aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
		MaxEntries: 4, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	channelrouting.ResetCanaryWindowAggregatorForTest(aggregator)
	t.Cleanup(func() { channelrouting.ResetCanaryWindowAggregatorForTest() })
	require.NoError(t, model.DB.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))

	gate := channelrouting.CanaryGate{
		PoolID: 29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		Bucket: 0, InCanary: true, RolloutKey: channelrouting.RolloutKey(strings.Repeat("a", 64)),
	}
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
	}))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, true, false, false, 0, clock.Now()))

	clock.Advance(2 * time.Minute)
	flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	var stored model.RoutingRuntimeCheckpoint
	require.NoError(t, model.DB.Where("checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind).First(&stored).Error)
	payload, err := channelrouting.DecodeCanaryCohortWindowCheckpoint(stored)
	require.NoError(t, err)
	assert.Equal(t, int64(1), payload.Canary.LogicalRequests)
	assert.Equal(t, int64(1), payload.Canary.Failures)
	assert.Equal(t, int64(1), payload.Canary.RoutingFailures)
	assert.Zero(t, payload.Canary.Attempts)
}
