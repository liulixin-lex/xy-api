package routingbreaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyResetGenerationIsMonotonicAndDuplicateEventsDoNotEraseNewFailures(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	breaker := New(Config{
		Consecutive5xxThreshold: 1, FailureRateThreshold: 1, FailureRateMinSamples: 1,
		WindowSize: 4, BaseCooldown: time.Minute, MaxCooldown: time.Minute,
		EntryTTL: time.Hour, MaxEntries: 8, Now: func() time.Time { return now },
	})
	key := Key{ChannelID: 7, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-reset", Group: "default"}
	require.Equal(t, StateOpen, breaker.OnReliabilityFailure(key, FailureProvider5xx).State)

	snapshot, applied := breaker.applyResetGeneration(key, 1, nil)
	require.True(t, applied)
	assert.Equal(t, StateHealthy, snapshot.State)
	assert.Equal(t, int64(1), snapshot.ResetGeneration)
	breaker.Clear(key)
	require.Equal(t, StateOpen, breaker.OnReliabilityFailure(key, FailureProvider5xx).State)

	snapshot, applied = breaker.applyResetGeneration(key, 1, nil)
	assert.False(t, applied)
	assert.Equal(t, StateOpen, snapshot.State, "a duplicate reset event must not erase failures recorded after that reset")

	snapshot, applied = breaker.applyResetGeneration(key, 2, nil)
	require.True(t, applied)
	assert.Equal(t, StateHealthy, snapshot.State)
	assert.Equal(t, int64(2), snapshot.ResetGeneration)
}

func TestResetGenerationSurvivesEntryEviction(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	breaker := New(Config{EntryTTL: time.Second, MaxEntries: 8, Now: func() time.Time { return now }})
	key := Key{ChannelID: 8, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-reset", Group: "default"}
	_, applied := breaker.applyResetGeneration(key, 3, nil)
	require.True(t, applied)

	now = now.Add(2 * time.Second)
	assert.Equal(t, Stats{
		Evictions: 1, ResetGenerationTombstones: 1,
	}, breaker.Stats())
	snapshot := breaker.OnSuccess(key)
	assert.Equal(t, int64(3), snapshot.ResetGeneration)
}

func TestResetGenerationTombstonesEnforceIndependentCapacityAndTTL(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{
		EntryTTL: time.Hour, MaxEntries: 8,
		ResetGenerationTTL: time.Minute, MaxResetGenerations: 2, Now: clock.Now,
	})
	keys := []Key{
		{ChannelID: 1, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"},
		{ChannelID: 2, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"},
		{ChannelID: 3, APIKeyIndex: SingleAPIKeyIndex, Model: "third", Group: "default"},
	}
	for index, key := range keys {
		_, applied := breaker.applyResetGeneration(key, int64(index+1), nil)
		require.True(t, applied)
		breaker.Clear(key)
		clock.Advance(time.Second)
	}

	assert.Equal(t, Stats{
		ResetGenerationTombstones: 2, ResetGenerationEvictions: 1,
	}, breaker.Stats())
	assert.Zero(t, breaker.resetGeneration(keys[0]))
	assert.Equal(t, int64(2), breaker.resetGeneration(keys[1]))
	assert.Equal(t, int64(3), breaker.resetGeneration(keys[2]))

	clock.Advance(time.Minute)
	assert.Equal(t, Stats{ResetGenerationEvictions: 3}, breaker.Stats())
}

func TestResetGenerationTombstoneCapacityUsesDeterministicKeyTieBreak(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	breaker := New(Config{
		EntryTTL: time.Hour, MaxEntries: 8,
		ResetGenerationTTL: time.Hour, MaxResetGenerations: 2, Now: func() time.Time { return now },
	})
	keys := []Key{
		{ChannelID: 3, APIKeyIndex: SingleAPIKeyIndex, Model: "same", Group: "default"},
		{ChannelID: 1, APIKeyIndex: SingleAPIKeyIndex, Model: "same", Group: "default"},
		{ChannelID: 2, APIKeyIndex: SingleAPIKeyIndex, Model: "same", Group: "default"},
	}
	for _, key := range keys {
		_, applied := breaker.applyResetGeneration(key, 1, nil)
		require.True(t, applied)
		breaker.Clear(key)
	}

	assert.NotContains(t, breaker.resetGenerations, keys[1])
	assert.Contains(t, breaker.resetGenerations, keys[0])
	assert.Contains(t, breaker.resetGenerations, keys[2])
	assert.Equal(t, Stats{
		ResetGenerationTombstones: 2, ResetGenerationEvictions: 1,
	}, breaker.Stats())
}

func TestHydratedResetGenerationTombstonesRemainBounded(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	breaker := New(Config{
		EntryTTL: time.Hour, MaxEntries: 3,
		ResetGenerationTTL: time.Hour, MaxResetGenerations: 2, Now: func() time.Time { return now },
	})
	keys := []Key{
		{ChannelID: 1, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"},
		{ChannelID: 2, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"},
		{ChannelID: 3, APIKeyIndex: SingleAPIKeyIndex, Model: "third", Group: "default"},
	}
	accepted := breaker.Hydrate([]Snapshot{
		{Key: keys[0], ResetGeneration: 1, State: StateHealthy, UpdatedAt: now},
		{Key: keys[1], ResetGeneration: 2, State: StateHealthy, UpdatedAt: now},
		{Key: keys[2], ResetGeneration: 3, State: StateHealthy, UpdatedAt: now},
	})

	require.Len(t, accepted, 3)
	assert.NotContains(t, breaker.resetGenerations, keys[0])
	assert.Contains(t, breaker.resetGenerations, keys[1])
	assert.Contains(t, breaker.resetGenerations, keys[2])
	assert.Equal(t, Stats{
		Entries: 3, ResetGenerationTombstones: 2, ResetGenerationEvictions: 1,
	}, breaker.Stats())
}

func TestResetGenerationHotPathDoesNotScanGlobalTombstones(t *testing.T) {
	const maximum = 40_000
	clock := &fakeClock{now: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{
		EntryTTL: time.Hour, MaxEntries: 1,
		ResetGenerationTTL: time.Minute, MaxResetGenerations: maximum, Now: clock.Now,
	})
	for index := 0; index < maximum; index++ {
		key := Key{
			ChannelID: index + 1, APIKeyIndex: SingleAPIKeyIndex,
			Model: "gpt-reset", Group: "default",
		}
		_, applied := breaker.applyResetGeneration(key, 1, nil)
		require.True(t, applied)
		breaker.Clear(key)
	}
	require.Len(t, breaker.resetGenerations, maximum)
	require.LessOrEqual(t, len(breaker.resetGenerationQueue), maximum)

	clock.Advance(2 * time.Minute)
	probeKey := Key{
		ChannelID: maximum + 1, APIKeyIndex: SingleAPIKeyIndex,
		Model: "gpt-reset", Group: "default",
	}
	assert.Equal(t, StateHealthy, breaker.Peek(probeKey).State)
	assert.Len(t, breaker.resetGenerations, maximum)
	assert.Zero(t, breaker.resetGenerationEvictions)
	assert.Equal(t, StateHealthy, breaker.OnSuccess(probeKey).State)
	assert.Len(t, breaker.resetGenerations, maximum)
	assert.Zero(t, breaker.resetGenerationEvictions)

	assert.Equal(t, Stats{
		Entries: 1, Dirty: 1, ResetGenerationEvictions: maximum,
	}, breaker.Stats())
}
