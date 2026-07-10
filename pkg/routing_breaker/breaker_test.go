package routingbreaker

import (
	"testing"
	"time"

	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func testBreaker(t *testing.T) (*Breaker, *fakeClock, Key) {
	t.Helper()

	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{
		BaseCooldown: 10 * time.Second,
		MaxCooldown:  1 * time.Minute,
		Now:          clock.Now,
	})
	key := Key{
		ChannelID:   12,
		APIKeyIndex: SingleAPIKeyIndex,
		Model:       "gpt-4.1",
		Group:       "default",
	}
	return breaker, clock, key
}

func TestBreakerEvictsExpiredAndOldestEntriesAtLimit(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	config := DefaultConfig()
	config.EntryTTL = time.Minute
	config.MaxEntries = 2
	config.Consecutive5xxThreshold = 10
	config.FailureRateMinSamples = 100
	config.WindowSize = 100
	config.DegradedConsecutiveFailures = 1
	config.DegradedMinSamples = 100
	config.Now = clock.Now
	breaker := New(config)

	expiredKey := Key{ChannelID: 1, APIKeyIndex: SingleAPIKeyIndex, Model: "expired", Group: "default"}
	oldOpenKey := Key{ChannelID: 2, APIKeyIndex: SingleAPIKeyIndex, Model: "old-open", Group: "default"}
	oldHealthyKey := Key{ChannelID: 3, APIKeyIndex: SingleAPIKeyIndex, Model: "old-healthy", Group: "default"}
	newHealthyKey := Key{ChannelID: 4, APIKeyIndex: SingleAPIKeyIndex, Model: "new-healthy", Group: "default"}
	degradedKey := Key{ChannelID: 5, APIKeyIndex: SingleAPIKeyIndex, Model: "degraded", Group: "default"}
	newOpenKey := Key{ChannelID: 6, APIKeyIndex: SingleAPIKeyIndex, Model: "new-open", Group: "default"}
	newestOpenKey := Key{ChannelID: 7, APIKeyIndex: SingleAPIKeyIndex, Model: "newest-open", Group: "default"}

	breaker.OnSuccess(expiredKey)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1}, breaker.Stats())

	clock.Advance(61 * time.Second)
	breaker.OnFailure(oldOpenKey, 429, 0)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1, Evictions: 1}, breaker.Stats())

	clock.Advance(time.Second)
	breaker.OnSuccess(oldHealthyKey)
	assert.Equal(t, 2, breaker.Stats().Entries)

	clock.Advance(time.Second)
	breaker.OnSuccess(newHealthyKey)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 2}, breaker.Stats())

	clock.Advance(time.Second)
	require.Equal(t, StateDegraded, breaker.OnFailure(degradedKey, 500, 0).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 3}, breaker.Stats())

	clock.Advance(time.Second)
	require.Equal(t, StateOpen, breaker.OnFailure(newOpenKey, 429, 0).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 4}, breaker.Stats())

	clock.Advance(time.Second)
	require.Equal(t, StateOpen, breaker.OnFailure(newestOpenKey, 429, 0).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 5}, breaker.Stats())

	dirty := breaker.DirtySnapshots()
	require.Len(t, dirty, 2)
	assert.Equal(t, []Key{newOpenKey, newestOpenKey}, []Key{dirty[0].Key, dirty[1].Key})
	assert.Equal(t, []State{StateOpen, StateOpen}, []State{dirty[0].State, dirty[1].State})
	assert.Equal(t, Stats{Entries: 2, Evictions: 5}, breaker.Stats())
}

func TestBreakerHydrateAndResetRespectMaxEntries(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{EntryTTL: time.Hour, MaxEntries: 2, Now: clock.Now})
	keys := []Key{
		{ChannelID: 10, APIKeyIndex: SingleAPIKeyIndex, Model: "oldest", Group: "default"},
		{ChannelID: 11, APIKeyIndex: SingleAPIKeyIndex, Model: "middle", Group: "default"},
		{ChannelID: 12, APIKeyIndex: SingleAPIKeyIndex, Model: "newest", Group: "default"},
	}

	breaker.Hydrate([]Snapshot{
		{Key: keys[0], State: StateOpen, CooldownUntil: clock.now.Add(time.Hour), UpdatedAt: clock.now.Add(-3 * time.Minute)},
		{Key: keys[1], State: StateOpen, CooldownUntil: clock.now.Add(time.Hour), UpdatedAt: clock.now.Add(-2 * time.Minute)},
		{Key: keys[2], State: StateOpen, CooldownUntil: clock.now.Add(time.Hour), UpdatedAt: clock.now.Add(-time.Minute)},
	})

	assert.Equal(t, Stats{Entries: 2, Evictions: 1}, breaker.Stats())
	assert.Equal(t, StateHealthy, breaker.GetSnapshot(keys[0]).State)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(keys[1]).State)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(keys[2]).State)

	resetKey := Key{ChannelID: 13, APIKeyIndex: SingleAPIKeyIndex, Model: "reset", Group: "default"}
	require.Equal(t, StateHealthy, breaker.Reset(resetKey).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 1, Evictions: 2}, breaker.Stats())
	assert.Equal(t, StateHealthy, breaker.GetSnapshot(keys[1]).State)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(keys[2]).State)

	dirty := breaker.DirtySnapshots()
	require.Len(t, dirty, 1)
	assert.Equal(t, resetKey, dirty[0].Key)
}

func TestBreakerCapacityEvictionUsesStableKeyOrder(t *testing.T) {
	tests := []struct {
		name string
		keys []Key
	}{
		{
			name: "channel",
			keys: []Key{
				{ChannelID: 1, APIKeyIndex: 3, Model: "c", Group: "c"},
				{ChannelID: 2, APIKeyIndex: 2, Model: "b", Group: "b"},
				{ChannelID: 3, APIKeyIndex: 1, Model: "a", Group: "a"},
			},
		},
		{
			name: "api key",
			keys: []Key{
				{ChannelID: 10, APIKeyIndex: 1, Model: "c", Group: "c"},
				{ChannelID: 10, APIKeyIndex: 2, Model: "b", Group: "b"},
				{ChannelID: 10, APIKeyIndex: 3, Model: "a", Group: "a"},
			},
		},
		{
			name: "model",
			keys: []Key{
				{ChannelID: 10, APIKeyIndex: 5, Model: "a", Group: "c"},
				{ChannelID: 10, APIKeyIndex: 5, Model: "b", Group: "b"},
				{ChannelID: 10, APIKeyIndex: 5, Model: "c", Group: "a"},
			},
		},
		{
			name: "group",
			keys: []Key{
				{ChannelID: 10, APIKeyIndex: 5, Model: "same", Group: "a"},
				{ChannelID: 10, APIKeyIndex: 5, Model: "same", Group: "b"},
				{ChannelID: 10, APIKeyIndex: 5, Model: "same", Group: "c"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			breaker := New(Config{EntryTTL: time.Hour, MaxEntries: 2, Now: func() time.Time { return now }})
			breaker.Hydrate([]Snapshot{
				{Key: test.keys[0], State: StateOpen, CooldownUntil: now.Add(time.Hour), UpdatedAt: now},
				{Key: test.keys[1], State: StateOpen, CooldownUntil: now.Add(time.Hour), UpdatedAt: now},
				{Key: test.keys[2], State: StateOpen, CooldownUntil: now.Add(time.Hour), UpdatedAt: now},
			})

			assert.Equal(t, StateHealthy, breaker.GetSnapshot(test.keys[0]).State)
			assert.Equal(t, StateOpen, breaker.GetSnapshot(test.keys[1]).State)
			assert.Equal(t, StateOpen, breaker.GetSnapshot(test.keys[2]).State)
			assert.Equal(t, Stats{Entries: 2, Evictions: 1}, breaker.Stats())
		})
	}
}

func TestBreakerDefaultsAndNormalizesEntryRetentionLimits(t *testing.T) {
	defaults := DefaultConfig()
	assert.Equal(t, 30*time.Minute, defaults.EntryTTL)
	assert.Equal(t, 20_000, defaults.MaxEntries)

	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{EntryTTL: 0, MaxEntries: -1, Now: clock.Now})
	breaker.OnSuccess(Key{ChannelID: 20, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"})
	clock.Advance(2 * time.Minute)
	breaker.OnSuccess(Key{ChannelID: 21, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"})
	breaker.OnSuccess(Key{ChannelID: 22, APIKeyIndex: SingleAPIKeyIndex, Model: "third", Group: "default"})

	assert.Equal(t, Stats{Entries: 3, Dirty: 3}, breaker.Stats())
}

func TestBreakerOpensAfterFiveConsecutive5xx(t *testing.T) {
	breaker, clock, key := testBreaker(t)

	for i := 1; i < 5; i++ {
		snapshot := breaker.OnFailure(key, 502, 0)
		assert.NotEqual(t, StateOpen, snapshot.State)
		assert.Equal(t, i, snapshot.Consecutive5xx)
	}

	snapshot := breaker.OnFailure(key, 502, 0)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, 5, snapshot.ConsecutiveFailures)
	assert.Equal(t, 5, snapshot.Consecutive5xx)
	assert.Equal(t, 1, snapshot.EjectionCount)
	assert.Equal(t, clock.now.Add(10*time.Second), snapshot.CooldownUntil)
}

func TestBreakerOpensWhenWindowFailureRateExceedsThreshold(t *testing.T) {
	breaker, _, key := testBreaker(t)

	var snapshot Snapshot
	for i := 0; i < 25; i++ {
		snapshot = breaker.OnFailure(key, 503, 0)
		if i < 24 {
			snapshot = breaker.OnSuccess(key)
		}
	}
	require.NotEqual(t, StateOpen, snapshot.State)
	assert.Equal(t, 49, snapshot.WindowRequests)
	assert.Equal(t, 25, snapshot.WindowFailures)

	snapshot = breaker.OnFailure(key, 503, 0)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, 50, snapshot.WindowRequests)
	assert.Equal(t, 26, snapshot.WindowFailures)
	assert.Equal(t, 2, snapshot.Consecutive5xx)
}

func TestBreaker429OpensAndHonorsRetryAfter(t *testing.T) {
	breaker, clock, key := testBreaker(t)

	snapshot := breaker.OnFailure(key, 429, 45*time.Second)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, clock.now.Add(45*time.Second), snapshot.CooldownUntil)

	clock.Advance(44 * time.Second)
	snapshot = breaker.GetSnapshot(key)
	require.Equal(t, StateOpen, snapshot.State)

	clock.Advance(time.Second)
	snapshot = breaker.GetSnapshot(key)
	require.Equal(t, StateHalfOpen, snapshot.State)
}

func TestBreakerHalfOpenTransitions(t *testing.T) {
	tests := []struct {
		name              string
		event             func(*Breaker, Key) Snapshot
		wantState         State
		wantEjectionCount int
		wantCooldown      time.Duration
	}{
		{
			name: "success returns healthy",
			event: func(breaker *Breaker, key Key) Snapshot {
				return breaker.OnSuccess(key)
			},
			wantState:         StateHealthy,
			wantEjectionCount: 0,
		},
		{
			name: "failure reopens",
			event: func(breaker *Breaker, key Key) Snapshot {
				return breaker.OnFailure(key, 500, 0)
			},
			wantState:         StateOpen,
			wantEjectionCount: 2,
			wantCooldown:      20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breaker, clock, key := testBreaker(t)
			for i := 0; i < 5; i++ {
				breaker.OnFailure(key, 500, 0)
			}
			clock.Advance(10 * time.Second)
			require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)

			snapshot := tt.event(breaker, key)
			require.Equal(t, tt.wantState, snapshot.State)
			assert.Equal(t, tt.wantEjectionCount, snapshot.EjectionCount)
			if tt.wantCooldown > 0 {
				assert.Equal(t, clock.now.Add(tt.wantCooldown), snapshot.CooldownUntil)
			} else {
				assert.True(t, snapshot.CooldownUntil.IsZero())
			}
		})
	}
}

func TestBreakerCooldownIsCapped(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{
		BaseCooldown: 10 * time.Second,
		MaxCooldown:  25 * time.Second,
		Now:          clock.Now,
	})
	key := Key{ChannelID: 77, APIKeyIndex: SingleAPIKeyIndex, Model: "claude-sonnet-4", Group: "vip"}

	for i := 0; i < 5; i++ {
		breaker.OnFailure(key, 500, 0)
	}
	clock.Advance(10 * time.Second)
	require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)

	snapshot := breaker.OnFailure(key, 500, 0)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, clock.now.Add(20*time.Second), snapshot.CooldownUntil)

	clock.Advance(20 * time.Second)
	require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)
	snapshot = breaker.OnFailure(key, 500, 0)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, 3, snapshot.EjectionCount)
	assert.Equal(t, clock.now.Add(25*time.Second), snapshot.CooldownUntil)
}

func TestBreakerResetClearsState(t *testing.T) {
	breaker, _, key := testBreaker(t)
	for i := 0; i < 5; i++ {
		breaker.OnFailure(key, 500, 0)
	}
	require.Equal(t, StateOpen, breaker.GetSnapshot(key).State)

	snapshot := breaker.Reset(key)
	require.Equal(t, StateHealthy, snapshot.State)
	assert.Zero(t, snapshot.ConsecutiveFailures)
	assert.Zero(t, snapshot.Consecutive5xx)
	assert.Zero(t, snapshot.EjectionCount)
	assert.Zero(t, snapshot.WindowRequests)
	assert.Zero(t, snapshot.WindowFailures)
	assert.True(t, snapshot.CooldownUntil.IsZero())

	dirty := breaker.DirtySnapshots()
	require.Len(t, dirty, 1)
	assert.Equal(t, StateHealthy, dirty[0].State)
	assert.Equal(t, key, dirty[0].Key)

	assert.Empty(t, breaker.DirtySnapshots())
}

func TestRecordAttemptUpdatesHotcacheAndDirtySnapshots(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	ResetDefaultForTest(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
		Now:                     clock.Now,
	})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	key := Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	snapshot := RecordAttempt(key, false, 502, 0)

	require.Equal(t, StateOpen, snapshot.State)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), cached.State)
	assert.Equal(t, clock.now.Unix(), cached.UpdatedUnix)

	dirty := DirtySnapshots()
	require.Len(t, dirty, 1)
	assert.Equal(t, key, dirty[0].Key)
	assert.Equal(t, StateOpen, dirty[0].State)
	assert.Empty(t, DirtySnapshots())
}

func TestRequeueDirtySnapshotsRestoresPersistenceRetry(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	ResetDefaultForTest(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
		Now:                     clock.Now,
	})
	t.Cleanup(func() { ResetDefaultForTest(DefaultConfig()) })

	key := Key{ChannelID: 89, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	RecordAttempt(key, false, 502, 0)
	drained := DirtySnapshots()
	require.Len(t, drained, 1)
	require.Empty(t, DirtySnapshots())

	RequeueDirtySnapshots(drained)

	requeued := DirtySnapshots()
	require.Len(t, requeued, 1)
	assert.Equal(t, key, requeued[0].Key)
	assert.Equal(t, StateOpen, requeued[0].State)
}

func TestAcquireHalfOpenProbeLimitsConcurrentProbes(t *testing.T) {
	breaker, clock, key := testBreaker(t)
	for i := 0; i < 5; i++ {
		breaker.OnFailure(key, 500, 0)
	}
	clock.Advance(10 * time.Second)

	first, ok := breaker.AcquireHalfOpenProbe(key, 1)
	require.True(t, ok)
	require.Equal(t, StateHalfOpen, first.State)
	assert.Equal(t, 1, first.HalfOpenInflight)

	second, ok := breaker.AcquireHalfOpenProbe(key, 1)
	require.False(t, ok)
	assert.Equal(t, 1, second.HalfOpenInflight)

	reopened := breaker.OnFailure(key, 500, 0)
	require.Equal(t, StateOpen, reopened.State)
	assert.Zero(t, reopened.HalfOpenInflight)
}

func TestBreakerHydrateRestoresOpenStateForHalfOpenFailure(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 30, 0, time.UTC)}
	breaker := New(Config{
		Consecutive5xxThreshold: 5,
		BaseCooldown:            10 * time.Second,
		MaxCooldown:             time.Minute,
		Now:                     clock.Now,
	})
	key := Key{ChannelID: 90, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	breaker.Hydrate([]Snapshot{{
		Key:                 key,
		State:               StateOpen,
		Reason:              "5xx",
		ConsecutiveFailures: 5,
		Consecutive5xx:      5,
		EjectionCount:       1,
		OpenedAt:            clock.now.Add(-30 * time.Second),
		CooldownUntil:       clock.now.Add(-20 * time.Second),
		UpdatedAt:           clock.now.Add(-20 * time.Second),
	}})

	require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)
	snapshot := breaker.OnFailure(key, 500, 0)

	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, "half_open_failure", snapshot.Reason)
	assert.Equal(t, 2, snapshot.EjectionCount)
}

func TestBreakerHydrateDoesNotOverwriteDirtyOrNewerLocalState(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 30, 0, time.UTC)}
	breaker := New(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            10 * time.Second,
		MaxCooldown:             time.Minute,
		Now:                     clock.Now,
	})
	key := Key{ChannelID: 91, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	local := breaker.OnFailure(key, 500, 0)
	require.Equal(t, StateOpen, local.State)

	accepted := breaker.Hydrate([]Snapshot{{
		Key:       key,
		State:     StateHealthy,
		UpdatedAt: clock.now.Add(time.Second),
	}})
	assert.Empty(t, accepted)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(key).State)

	breaker.DirtySnapshots()
	accepted = breaker.Hydrate([]Snapshot{{
		Key:       key,
		State:     StateHealthy,
		UpdatedAt: clock.now.Add(-time.Second),
	}})
	assert.Empty(t, accepted)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(key).State)
}

func TestRecordAttemptStoresReasonAndResetDefaultKeyClearsHotcache(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	ResetDefaultForTest(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Second,
		Now:                     clock.Now,
	})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	key := Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	snapshot := RecordAttempt(key, false, 429, 2*time.Second)

	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, "rate_limit", snapshot.Reason)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), cached.State)
	assert.Equal(t, "rate_limit", cached.Reason)

	reset := ResetDefaultKey(key)

	require.Equal(t, StateHealthy, reset.State)
	assert.Empty(t, reset.Reason)
	cached, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHealthy), cached.State)
	assert.Empty(t, cached.Reason)
}
