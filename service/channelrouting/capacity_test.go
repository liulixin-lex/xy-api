package channelrouting

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routingTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *routingTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *routingTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newCapacityTrackerForTest(t *testing.T, clock Clock, maxEntries int, idleTTL time.Duration) *CapacityTracker {
	t.Helper()
	tracker, err := NewCapacityTracker(CapacityConfig{
		MaxEntries: maxEntries,
		IdleTTL:    idleTTL,
		Shards:     4,
		Clock:      clock,
	})
	require.NoError(t, err)
	return tracker
}

func TestCapacityDimensionEstimatePreservesZeroAndUnknownSemantics(t *testing.T) {
	tests := []struct {
		name     string
		estimate CapacityDimensionEstimate
		limit    int64
		want     int64
		known    bool
	}{
		{
			name:     "not applicable is known zero",
			estimate: CapacityDimensionEstimate{State: CapacityDimensionNotApplicable},
			limit:    1_000, want: 0, known: true,
		},
		{
			name:     "bounded explicit zero stays zero",
			estimate: CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown},
			limit:    1_000, want: 0, known: true,
		},
		{
			name:     "bounded positive value stays bounded",
			estimate: CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 125},
			limit:    1_000, want: 125, known: true,
		},
		{
			name:     "applicable unknown reserves the conservative bound",
			estimate: CapacityDimensionEstimate{State: CapacityDimensionApplicableUnknown},
			limit:    1_000, want: 1_000, known: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			demand, err := test.estimate.Demand(test.limit)
			require.NoError(t, err)
			assert.Equal(t, test.want, demand)
			assert.Equal(t, test.known, test.estimate.Known())
		})
	}

	_, err := (CapacityDimensionEstimate{
		State: CapacityDimensionNotApplicable, Tokens: 1,
	}).Demand(1_000)
	assert.ErrorIs(t, err, ErrCapacityInvalidInput)
}

func TestCapacityReservationLifecycleIsExactAndIdempotent(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Minute)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	limit := Limit{RPM: 10, InputTPM: 1_000, OutputTPM: 500, Inflight: 2}
	demand := Demand{RPM: 1, InputTPM: 100, OutputTPM: 50, Inflight: 1}

	reservation, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)
	assert.Equal(t, CapacityModeLocalSoft, reservation.Mode)
	assert.Equal(t, CapacityStats{
		Mode:    CapacityModeLocalSoft,
		Entries: 1,
		Pending: 1,
	}, tracker.Stats())

	require.NoError(t, reservation.Commit())
	require.NoError(t, reservation.Commit())
	assert.Equal(t, CapacityStats{
		Mode:      CapacityModeLocalSoft,
		Entries:   1,
		Committed: 1,
	}, tracker.Stats())

	second, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)
	require.NoError(t, second.Cancel())
	require.NoError(t, second.Cancel())
	assert.Equal(t, CapacityStats{
		Mode:      CapacityModeLocalSoft,
		Entries:   1,
		Committed: 1,
	}, tracker.Stats())

	assert.ErrorIs(t, reservation.Cancel(), ErrCapacityReservationTransition)
	require.NoError(t, reservation.Release())
	require.NoError(t, reservation.Release())
	assert.ErrorIs(t, reservation.Commit(), ErrCapacityReservationTransition)
	assert.Equal(t, CapacityStats{
		Mode:    CapacityModeLocalSoft,
		Entries: 1,
	}, tracker.Stats())
}

func TestCapacityReservationAdmissionIsImmutable(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 4, time.Hour)
	key := CapacityKey{PoolID: 7, MemberID: 11, Model: "gpt-test"}
	demand := Demand{RPM: 1, InputTPM: 100, OutputTPM: 20, Inflight: 1}
	limit := Limit{RPM: 10, InputTPM: 1_000, OutputTPM: 200, Inflight: 4}
	reservation, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)

	admission := reservation.Admission()
	assert.Equal(t, CapacityAdmission{Mode: CapacityModeLocalSoft, Key: key, Demand: demand, Limit: limit}, admission)
	admission.Key.MemberID = 99
	admission.Demand.RPM = 99
	admission.Limit.RPM = 99
	assert.Equal(t, CapacityAdmission{Mode: CapacityModeLocalSoft, Key: key, Demand: demand, Limit: limit}, reservation.Admission())
}

func TestCapacityReservationRejectsExhaustionAndArithmeticOverflow(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Minute)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}

	first, err := tracker.TryReserve(
		key,
		Demand{RPM: math.MaxInt64 - 1, Inflight: 1},
		Limit{RPM: math.MaxInt64, Inflight: 2},
	)
	require.NoError(t, err)
	require.NoError(t, first.Commit())

	_, err = tracker.TryReserve(
		key,
		Demand{RPM: 2, Inflight: 1},
		Limit{RPM: math.MaxInt64, Inflight: 2},
	)
	assert.ErrorIs(t, err, ErrCapacityOverflow)

	otherKey := CapacityKey{PoolID: 1, MemberID: 12, Model: "gpt-4o"}
	one, err := tracker.TryReserve(otherKey, Demand{Inflight: 1}, Limit{Inflight: 1})
	require.NoError(t, err)
	_, err = tracker.TryReserve(otherKey, Demand{Inflight: 1}, Limit{Inflight: 1})
	assert.ErrorIs(t, err, ErrCapacityExhausted)

	stats := tracker.Stats()
	assert.Equal(t, int64(2), stats.Drops)
	require.NoError(t, one.Cancel())
	require.NoError(t, first.Release())
}

func TestCapacityRejectedNewKeyDoesNotEvictRetainedState(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 1, time.Hour)
	retainedKey := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	retained, err := tracker.TryReserve(retainedKey, Demand{Inflight: 1}, Limit{Inflight: 1})
	require.NoError(t, err)
	require.NoError(t, retained.Cancel())

	rejectedKey := CapacityKey{PoolID: 1, MemberID: 12, Model: "gpt-4o"}
	_, err = tracker.TryReserve(rejectedKey, Demand{Inflight: 2}, Limit{Inflight: 1})
	assert.ErrorIs(t, err, ErrCapacityExhausted)
	assert.True(t, tracker.Has(retainedKey))
	assert.False(t, tracker.Has(rejectedKey))
	assert.Equal(t, CapacityStats{
		Mode:    CapacityModeLocalSoft,
		Entries: 1,
		Drops:   1,
	}, tracker.Stats())
}

func TestCapacityCommittedRateDebtSurvivesReleaseAndRefillsLinearly(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Hour)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	limit := Limit{RPM: 2, InputTPM: 200, OutputTPM: 100, Inflight: 1}
	demand := Demand{RPM: 1, InputTPM: 100, OutputTPM: 50, Inflight: 1}

	for index := 0; index < 2; index++ {
		reservation, err := tracker.TryReserve(key, demand, limit)
		require.NoError(t, err)
		require.NoError(t, reservation.Commit())
		require.NoError(t, reservation.Release())
	}
	assert.Equal(t, int64(0), tracker.Stats().Committed)
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, Demand{RPM: 2, InputTPM: 200, OutputTPM: 100}, snapshot.Committed)

	_, err := tracker.TryReserve(key, demand, limit)
	assert.ErrorIs(t, err, ErrCapacityExhausted, "fast completions must still consume rate capacity")

	clock.Advance(30 * time.Second)
	snapshot, ok = tracker.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, Demand{RPM: 1, InputTPM: 100, OutputTPM: 50}, snapshot.Committed)
	recovered, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)
	require.NoError(t, recovered.Cancel())
}

func TestCapacityRateDebtPinsLimitUntilItRefills(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Hour)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	demand := Demand{RPM: 1, Inflight: 1}

	reservation, err := tracker.TryReserve(key, demand, Limit{RPM: 2, Inflight: 1})
	require.NoError(t, err)
	require.NoError(t, reservation.Commit())
	require.NoError(t, reservation.Release())
	_, err = tracker.TryReserve(key, demand, Limit{RPM: 3, Inflight: 1})
	assert.ErrorIs(t, err, ErrCapacityLimitConflict)

	clock.Advance(time.Minute)
	updated, err := tracker.TryReserve(key, demand, Limit{RPM: 3, Inflight: 1})
	require.NoError(t, err)
	require.NoError(t, updated.Cancel())
}

func TestCapacityRateRefillPreservesFractionAcrossFrequentReads(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Hour)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	limit := Limit{RPM: 2, Inflight: 1}
	demand := Demand{RPM: 2, Inflight: 1}

	reservation, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)
	require.NoError(t, reservation.Commit())
	require.NoError(t, reservation.Release())
	for index := 0; index < 30; index++ {
		clock.Advance(time.Second)
		_, ok := tracker.Snapshot(key)
		require.True(t, ok)
	}
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, int64(1), snapshot.Committed.RPM)
}

func TestCapacityRateRefillDoesNotOverflowAtMaximumLimit(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Hour)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	limit := Limit{RPM: math.MaxInt64, Inflight: 1}
	demand := Demand{RPM: math.MaxInt64 - 1, Inflight: 1}

	reservation, err := tracker.TryReserve(key, demand, limit)
	require.NoError(t, err)
	require.NoError(t, reservation.Commit())
	require.NoError(t, reservation.Release())
	clock.Advance(30 * time.Second)
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, int64(4_611_686_018_427_387_903), snapshot.Committed.RPM)
}

func TestCapacityTrackerRejectsInvalidInputsAndActiveLimitChanges(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Minute)
	validKey := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}

	tests := []struct {
		name   string
		key    CapacityKey
		demand Demand
		limit  Limit
	}{
		{name: "missing pool", key: CapacityKey{MemberID: 11, Model: "gpt-4o"}, demand: Demand{RPM: 1}, limit: Limit{RPM: 1}},
		{name: "missing member", key: CapacityKey{PoolID: 1, Model: "gpt-4o"}, demand: Demand{RPM: 1}, limit: Limit{RPM: 1}},
		{name: "missing model", key: CapacityKey{PoolID: 1, MemberID: 11}, demand: Demand{RPM: 1}, limit: Limit{RPM: 1}},
		{name: "untrimmed model", key: CapacityKey{PoolID: 1, MemberID: 11, Model: " gpt-4o"}, demand: Demand{RPM: 1}, limit: Limit{RPM: 1}},
		{name: "empty demand", key: validKey, demand: Demand{}, limit: Limit{RPM: 1}},
		{name: "negative demand", key: validKey, demand: Demand{InputTPM: -1}, limit: Limit{InputTPM: 1}},
		{name: "empty limit", key: validKey, demand: Demand{RPM: 1}, limit: Limit{}},
		{name: "negative limit", key: validKey, demand: Demand{RPM: 1}, limit: Limit{RPM: -1}},
		{name: "unbounded demand dimension", key: validKey, demand: Demand{RPM: 1}, limit: Limit{Inflight: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := tracker.TryReserve(test.key, test.demand, test.limit)
			assert.ErrorIs(t, err, ErrCapacityInvalidInput)
		})
	}

	reservation, err := tracker.TryReserve(validKey, Demand{Inflight: 1}, Limit{Inflight: 2})
	require.NoError(t, err)
	_, err = tracker.TryReserve(validKey, Demand{Inflight: 1}, Limit{Inflight: 3})
	assert.ErrorIs(t, err, ErrCapacityLimitConflict)
	require.NoError(t, reservation.Cancel())

	updated, err := tracker.TryReserve(validKey, Demand{Inflight: 1}, Limit{Inflight: 3})
	require.NoError(t, err)
	require.NoError(t, updated.Cancel())
	assert.Equal(t, int64(1), tracker.Stats().Drops)
}

func TestCapacityTrackerSharesLimitsAcrossPolicyRevisions(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 4, time.Minute)
	base := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	demand := Demand{Inflight: 1}

	firstKey := base
	firstKey.PolicyRevision = 11
	first, err := tracker.TryReserve(firstKey, demand, Limit{Inflight: 1})
	require.NoError(t, err)
	require.NoError(t, first.Commit())

	secondKey := base
	secondKey.PolicyRevision = 12
	_, err = tracker.TryReserve(secondKey, demand, Limit{Inflight: 2})
	assert.ErrorIs(t, err, ErrCapacityExhausted,
		"a new policy revision must not allocate a second capacity budget for the same resource")
	require.NoError(t, first.Release())

	second, err := tracker.TryReserve(secondKey, demand, Limit{Inflight: 2})
	require.NoError(t, err)
	require.NoError(t, second.Cancel())
}

func TestCapacityTrackerInterleavedOldRevisionCannotRestoreNewerLimit(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 4, time.Minute)
	base := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	demand := Demand{Inflight: 1}
	revisionOne := base
	revisionOne.PolicyRevision = 1
	revisionTwo := base
	revisionTwo.PolicyRevision = 2

	first, err := tracker.TryReserve(revisionOne, demand, Limit{Inflight: 100})
	require.NoError(t, err)
	second, err := tracker.TryReserve(revisionTwo, demand, Limit{Inflight: 50})
	require.NoError(t, err)
	stale, err := tracker.TryReserve(revisionOne, demand, Limit{Inflight: 100})
	require.NoError(t, err, "a pinned old revision may share the conservative newer limit")
	assert.Equal(t, int64(50), stale.Admission().Limit.Inflight)
	require.NoError(t, first.Cancel())
	require.NoError(t, second.Cancel())
	require.NoError(t, stale.Cancel())

	idleStale, err := tracker.TryReserve(revisionOne, demand, Limit{Inflight: 100})
	require.NoError(t, err)
	assert.Equal(t, int64(50), idleStale.Admission().Limit.Inflight,
		"draining must not let a stale revision restore its old limit")
	require.NoError(t, idleStale.Cancel())

	revisionThree := base
	revisionThree.PolicyRevision = 3
	newer, err := tracker.TryReserve(revisionThree, demand, Limit{Inflight: 80})
	require.NoError(t, err)
	assert.Equal(t, int64(80), newer.Admission().Limit.Inflight,
		"a higher revision may deliberately adopt a relaxed limit after drain")
	require.NoError(t, newer.Cancel())
}

func TestCapacityTrackerAdoptsLatestRevisionLimitAfterTransitionDrains(t *testing.T) {
	tests := []struct {
		name       string
		oldLimit   Limit
		newLimit   Limit
		transition Limit
	}{
		{
			name:       "relaxed limit",
			oldLimit:   Limit{RPM: 100, InputTPM: 1_000, OutputTPM: 400, Inflight: 10},
			newLimit:   Limit{RPM: 200, InputTPM: 2_000, OutputTPM: 800, Inflight: 20},
			transition: Limit{RPM: 100, InputTPM: 1_000, OutputTPM: 400, Inflight: 10},
		},
		{
			name:       "tightened limit",
			oldLimit:   Limit{RPM: 100, InputTPM: 1_000, OutputTPM: 400, Inflight: 10},
			newLimit:   Limit{RPM: 50, InputTPM: 500, OutputTPM: 200, Inflight: 5},
			transition: Limit{RPM: 50, InputTPM: 500, OutputTPM: 200, Inflight: 5},
		},
		{
			name:       "mixed multidimensional limit",
			oldLimit:   Limit{RPM: 100, InputTPM: 1_000, OutputTPM: 400, Inflight: 10},
			newLimit:   Limit{RPM: 200, InputTPM: 500, OutputTPM: 0, Inflight: 20},
			transition: Limit{RPM: 100, InputTPM: 500, OutputTPM: 400, Inflight: 10},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
			tracker := newCapacityTrackerForTest(t, clock, 4, time.Minute)
			base := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
			revisionOne := base
			revisionOne.PolicyRevision = 1
			revisionTwo := base
			revisionTwo.PolicyRevision = 2
			demand := Demand{Inflight: 1}

			oldReservation, err := tracker.TryReserve(revisionOne, demand, test.oldLimit)
			require.NoError(t, err)
			transitionReservation, err := tracker.TryReserve(revisionTwo, demand, test.newLimit)
			require.NoError(t, err)
			assert.Equal(t, test.transition, transitionReservation.Admission().Limit)

			require.NoError(t, oldReservation.Cancel())
			require.NoError(t, transitionReservation.Cancel())

			settledReservation, err := tracker.TryReserve(revisionTwo, demand, test.newLimit)
			require.NoError(t, err,
				"the latest revision must converge from its conservative transition limit after drain")
			assert.Equal(t, test.newLimit, settledReservation.Admission().Limit)
			require.NoError(t, settledReservation.Cancel())
		})
	}
}

func TestCapacityTrackerEvictsOnlyIdleEntriesAndExpiresThem(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 2, time.Minute)
	limit := Limit{Inflight: 1}
	key1 := CapacityKey{PoolID: 1, MemberID: 1, Model: "gpt-4o"}
	key2 := CapacityKey{PoolID: 1, MemberID: 2, Model: "gpt-4o"}
	key3 := CapacityKey{PoolID: 1, MemberID: 3, Model: "gpt-4o"}

	first, err := tracker.TryReserve(key1, Demand{Inflight: 1}, limit)
	require.NoError(t, err)
	require.NoError(t, first.Cancel())
	clock.Advance(time.Second)

	active, err := tracker.TryReserve(key2, Demand{Inflight: 1}, limit)
	require.NoError(t, err)
	third, err := tracker.TryReserve(key3, Demand{Inflight: 1}, limit)
	require.NoError(t, err)
	assert.False(t, tracker.Has(key1))
	assert.True(t, tracker.Has(key2))
	assert.True(t, tracker.Has(key3))
	assert.Equal(t, int64(1), tracker.Stats().Evictions)

	blockedKey := CapacityKey{PoolID: 1, MemberID: 4, Model: "gpt-4o"}
	_, err = tracker.TryReserve(blockedKey, Demand{Inflight: 1}, limit)
	assert.ErrorIs(t, err, ErrCapacityEntriesFull)
	assert.Equal(t, int64(1), tracker.Stats().Drops)

	clock.Advance(2 * time.Minute)
	assert.Equal(t, 2, tracker.Stats().Entries, "active reservations must not expire")
	require.NoError(t, active.Cancel())
	require.NoError(t, third.Cancel())
	clock.Advance(time.Minute + time.Nanosecond)
	assert.Equal(t, 0, tracker.Stats().Entries)
	assert.Equal(t, int64(3), tracker.Stats().Evictions)
}

func TestCapacityTrackerSerializesConcurrentAdmissionAndLifecycle(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tracker := newCapacityTrackerForTest(t, clock, 8, time.Minute)
	key := CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-4o"}
	limit := Limit{Inflight: 8}

	const workers = 64
	var attempted sync.WaitGroup
	var finished sync.WaitGroup
	var admitted atomic.Int64
	release := make(chan struct{})
	attempted.Add(workers)
	finished.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer finished.Done()
			reservation, err := tracker.TryReserve(key, Demand{Inflight: 1}, limit)
			if err != nil {
				assert.True(t, errors.Is(err, ErrCapacityExhausted))
				attempted.Done()
				return
			}
			admitted.Add(1)
			attempted.Done()
			<-release
			require.NoError(t, reservation.Commit())
			require.NoError(t, reservation.Release())
		}()
	}

	attempted.Wait()
	assert.Equal(t, int64(8), admitted.Load())
	assert.Equal(t, int64(8), tracker.Stats().Pending)
	close(release)
	finished.Wait()
	assert.Equal(t, CapacityStats{
		Mode:    CapacityModeLocalSoft,
		Entries: 1,
		Drops:   workers - 8,
	}, tracker.Stats())
}

func TestNewCapacityTrackerRejectsUnboundedConfiguration(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_700_000_000, 0)}
	tests := []CapacityConfig{
		{MaxEntries: 0, IdleTTL: time.Minute, Shards: 1, Clock: clock},
		{MaxEntries: 1, IdleTTL: 0, Shards: 1, Clock: clock},
		{MaxEntries: 1, IdleTTL: time.Minute, Shards: 0, Clock: clock},
	}
	for _, config := range tests {
		_, err := NewCapacityTracker(config)
		assert.ErrorIs(t, err, ErrCapacityInvalidConfig)
	}
}
