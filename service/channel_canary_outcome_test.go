package service

import (
	"context"
	"errors"
	"math"
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
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
		ExpectedCostKnown: true, ExpectedCostUSD: 0.000002,
	}))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
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

func TestChannelRoutingCanaryOutcomeDistinguishesKnownZeroFromUnknownAndOverflowingCosts(t *testing.T) {
	tests := []struct {
		name                  string
		selections            []ChannelRoutingCanarySelection
		wantCostKnownRequests int64
	}{
		{
			name: "known zero cost",
			selections: []ChannelRoutingCanarySelection{
				{ExpectedCostKnown: true, ExpectedCostUSD: 0},
			},
			wantCostKnownRequests: 1,
		},
		{
			name: "explicit unknown cost",
			selections: []ChannelRoutingCanarySelection{
				{ExpectedCostKnown: false, ExpectedCostUSD: 1},
			},
		},
		{
			name: "non-finite known cost fails closed",
			selections: []ChannelRoutingCanarySelection{
				{ExpectedCostKnown: true, ExpectedCostUSD: math.NaN()},
			},
		},
		{
			name: "attempt cost sum overflow fails closed",
			selections: []ChannelRoutingCanarySelection{
				{ExpectedCostKnown: true, ExpectedCostUSD: 5_000_000_000},
				{ExpectedCostKnown: true, ExpectedCostUSD: 5_000_000_000},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			for _, selection := range test.selections {
				selection.Gate = gate
				selection.WindowSeconds = 60
				selection.LatenessSeconds = 5
				require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, selection))
				require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
				require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
			}
			require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, true, true, false, 0, clock.Now()))

			clock.Advance(2 * time.Minute)
			flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, flushed)
			var stored model.RoutingRuntimeCheckpoint
			require.NoError(t, model.DB.Where(
				"checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind,
			).First(&stored).Error)
			payload, err := channelrouting.DecodeCanaryCohortWindowCheckpoint(stored)
			require.NoError(t, err)
			assert.Equal(t, int64(1), payload.Canary.LogicalRequests)
			assert.Equal(t, int64(1), payload.Canary.Successes)
			assert.Equal(t, int64(len(test.selections)), payload.Canary.Attempts)
			assert.Equal(t, test.wantCostKnownRequests, payload.Canary.CostKnownRequests)
			assert.Zero(t, payload.Canary.ExpectedPlatformCostNanoUSD)
		})
	}
}

func TestChannelRoutingCanaryOutcomeCrossUnitPreservesAttemptAndRoutingFailureSemantics(t *testing.T) {
	for _, test := range []struct {
		name               string
		crossRollout       bool
		startAttempt       bool
		wantAttempts       int64
		wantRoutingFailure int64
	}{
		{name: "cross pool after attempt", startAttempt: true, wantAttempts: 1},
		{name: "cross rollout after attempt", crossRollout: true, startAttempt: true, wantAttempts: 1},
		{name: "cross pool before attempt", wantRoutingFailure: 1},
		{name: "cross rollout before attempt", crossRollout: true, wantRoutingFailure: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			truncate(t)
			clock := &canaryOutcomeTestClock{now: time.Now().Truncate(time.Second)}
			aggregator, err := channelrouting.NewCanaryWindowAggregator(channelrouting.CanaryWindowAggregatorConfig{
				MaxEntries: 4, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
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
			secondGate.RolloutKey = channelrouting.RolloutKey(strings.Repeat("b", 64))
			if test.crossRollout {
				secondGate.ActivationID++
				secondGate.PolicyRevision++
			} else {
				secondGate.PoolID++
			}

			ctx, _ := gin.CreateTestContext(nil)
			require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
				Gate: firstGate, WindowSeconds: 60, LatenessSeconds: 5,
			}))
			if test.startAttempt {
				require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
				require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
			}
			require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, ChannelRoutingCanarySelection{
				Gate: secondGate, WindowSeconds: 60, LatenessSeconds: 5,
			}))
			require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, clock.Now()))

			clock.Advance(2 * time.Minute)
			flushed, err := channelrouting.FlushCanaryOutcomeCheckpointsContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, flushed)
			var checkpoint model.RoutingRuntimeCheckpoint
			require.NoError(t, model.DB.Where(
				"checkpoint_kind = ?", channelrouting.CanaryCohortWindowCheckpointKind,
			).First(&checkpoint).Error)
			payload, err := channelrouting.DecodeCanaryCohortWindowCheckpoint(checkpoint)
			require.NoError(t, err)
			assert.Equal(t, int64(1), payload.Canary.LogicalRequests)
			assert.Equal(t, int64(1), payload.Canary.Failures)
			assert.Equal(t, test.wantAttempts, payload.Canary.Attempts)
			assert.Equal(t, test.wantRoutingFailure, payload.Canary.RoutingFailures)
		})
	}
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
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
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

func TestChannelRoutingCanaryOutcomeRejectsOverlappingUpstreamAttempts(t *testing.T) {
	gate := channelrouting.CanaryGate{
		PoolID: 29, ActivationID: 401, PolicyRevision: 11, TrafficBasisPoints: 100,
		Bucket: 0, InCanary: true, RolloutKey: channelrouting.RolloutKey(strings.Repeat("a", 64)),
	}
	ctx, _ := gin.CreateTestContext(nil)
	selection := ChannelRoutingCanarySelection{Gate: gate, WindowSeconds: 60, LatenessSeconds: 5}
	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, selection))
	start := make(chan struct{})
	errs := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(len(errs))
	for index := range errs {
		go func(index int) {
			defer wait.Done()
			<-start
			errs[index] = MarkChannelRoutingCanaryAttemptStarted(ctx)
		}(index)
	}
	close(start)
	wait.Wait()
	succeeded := 0
	rejected := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrChannelRoutingCanaryOutcomeInvalid) {
			rejected++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, rejected)
	assert.ErrorIs(t, PrepareChannelRoutingCanarySelection(ctx, selection), ErrChannelRoutingCanaryOutcomeInvalid)
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))

	require.NoError(t, PrepareChannelRoutingCanarySelection(ctx, selection))
	require.NoError(t, MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, FinishChannelRoutingCanaryAttempt(ctx))
	require.NoError(t, FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, time.Now()))
}
