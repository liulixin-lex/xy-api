package channelrouting

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSlowStartTrackerForTest(t *testing.T, clock Clock, maxEntries int, stateTTL time.Duration) *SlowStartTracker {
	t.Helper()
	tracker, err := NewSlowStartTracker(SlowStartPolicy{
		MinimumFactor: 0.1,
		RampDuration:  10 * time.Minute,
		StateTTL:      stateTTL,
		MaxEntries:    maxEntries,
	}, clock)
	require.NoError(t, err)
	return tracker
}

func TestSlowStartColdNodeAndNewMemberRampLinearly(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newSlowStartTrackerForTest(t, clock, 8, time.Hour)
	key := SlowStartKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}

	factor, err := tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 0.1, factor, 1e-9, "a cold node must not restore full traffic")
	clock.Advance(5 * time.Minute)
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 0.55, factor, 1e-9)
	clock.Advance(5 * time.Minute)
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 1, factor, 1e-9)
	assert.Equal(t, 0, tracker.Stats().Entries)

	state, err := tracker.StartNew(key)
	require.NoError(t, err)
	assert.Equal(t, SlowStartCauseNew, state.Cause)
	assert.InDelta(t, 0.1, state.Factor, 1e-9)
	clock.Advance(2 * time.Minute)

	duplicate, err := tracker.StartNew(key)
	require.NoError(t, err)
	assert.Equal(t, state.StartedAt, duplicate.StartedAt, "duplicate discovery must not reset the ramp")
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 0.28, factor, 1e-9)
	clock.Advance(8 * time.Minute)
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 1, factor, 1e-9)
	assert.Equal(t, SlowStartStats{Completed: 1}, tracker.Stats())
}

func TestSlowStartHardFailureStaysAtZeroUntilHealthyRecovery(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newSlowStartTrackerForTest(t, clock, 8, time.Hour)
	clock.Advance(10 * time.Minute)
	key := SlowStartKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}

	state, err := tracker.MarkHardFailure(key)
	require.NoError(t, err)
	assert.Equal(t, SlowStartCauseHardFailure, state.Cause)
	assert.Zero(t, state.Factor)
	clock.Advance(20 * time.Minute)
	factor, err := tracker.Factor(key)
	require.NoError(t, err)
	assert.Zero(t, factor)

	recovery, err := tracker.MarkHealthy(key)
	require.NoError(t, err)
	assert.Equal(t, SlowStartCauseRecovery, recovery.Cause)
	assert.InDelta(t, 0.1, recovery.Factor, 1e-9)
	clock.Advance(5 * time.Minute)
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 0.55, factor, 1e-9)
	clock.Advance(5 * time.Minute)
	factor, err = tracker.Factor(key)
	require.NoError(t, err)
	assert.InDelta(t, 1, factor, 1e-9)
	_, ok := tracker.State(key)
	assert.False(t, ok, "healthy completed ramps must be removed")
	assert.Equal(t, SlowStartStats{Completed: 1}, tracker.Stats())
}

func TestSlowStartStateIsBoundedAndTTLPruned(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newSlowStartTrackerForTest(t, clock, 2, 20*time.Minute)
	clock.Advance(10 * time.Minute)
	key1 := SlowStartKey{PoolID: 1, MemberID: 1, Model: "gpt-4o"}
	key2 := SlowStartKey{PoolID: 1, MemberID: 2, Model: "gpt-4o"}
	key3 := SlowStartKey{PoolID: 1, MemberID: 3, Model: "gpt-4o"}

	_, err := tracker.MarkHardFailure(key1)
	require.NoError(t, err)
	clock.Advance(time.Second)
	_, err = tracker.StartNew(key2)
	require.NoError(t, err)
	clock.Advance(time.Second)
	_, err = tracker.StartNew(key3)
	require.NoError(t, err)

	_, ok := tracker.State(key1)
	assert.False(t, ok)
	assert.Equal(t, SlowStartStats{Entries: 2, Evictions: 1}, tracker.Stats())

	clock.Advance(20*time.Minute + time.Nanosecond)
	assert.Equal(t, SlowStartStats{Evictions: 3}, tracker.Stats())
}

func TestSlowStartTrackerRejectsInvalidPolicyAndKeys(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	policies := []SlowStartPolicy{
		{MinimumFactor: 0, RampDuration: time.Minute, StateTTL: time.Hour, MaxEntries: 1},
		{MinimumFactor: 1, RampDuration: time.Minute, StateTTL: time.Hour, MaxEntries: 1},
		{MinimumFactor: 0.1, RampDuration: 0, StateTTL: time.Hour, MaxEntries: 1},
		{MinimumFactor: 0.1, RampDuration: time.Hour, StateTTL: time.Minute, MaxEntries: 1},
		{MinimumFactor: 0.1, RampDuration: time.Minute, StateTTL: time.Hour, MaxEntries: 0},
	}
	for _, policy := range policies {
		_, err := NewSlowStartTracker(policy, clock)
		assert.ErrorIs(t, err, ErrSlowStartInvalidPolicy)
	}

	tracker := newSlowStartTrackerForTest(t, clock, 8, time.Hour)
	invalidKeys := []SlowStartKey{
		{MemberID: 1, Model: "gpt-4o"},
		{PoolID: 1, Model: "gpt-4o"},
		{PoolID: 1, MemberID: 1},
		{PoolID: 1, MemberID: 1, Model: " gpt-4o"},
	}
	for _, key := range invalidKeys {
		_, err := tracker.Factor(key)
		assert.ErrorIs(t, err, ErrSlowStartInvalidKey)
		_, err = tracker.StartNew(key)
		assert.ErrorIs(t, err, ErrSlowStartInvalidKey)
		_, err = tracker.MarkHardFailure(key)
		assert.ErrorIs(t, err, ErrSlowStartInvalidKey)
	}
}

func TestSlowStartTrackerIsConcurrentAndBounded(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newSlowStartTrackerForTest(t, clock, 16, time.Hour)
	clock.Advance(10 * time.Minute)

	const workers = 64
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		index := index
		go func() {
			defer wait.Done()
			key := SlowStartKey{PoolID: 1, MemberID: index + 1, Model: "gpt-4o"}
			_, err := tracker.StartNew(key)
			require.NoError(t, err)
			factor, err := tracker.Factor(key)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, factor, 0.1)
			assert.LessOrEqual(t, factor, 1.0)
		}()
	}
	wait.Wait()

	stats := tracker.Stats()
	assert.Equal(t, 16, stats.Entries)
	assert.Equal(t, int64(workers-16), stats.Evictions)
}
