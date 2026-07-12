package routingbreaker

import (
	"net/http"
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
	config.Consecutive5xxThreshold = 1
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
	breaker.OnReliabilityFailure(oldOpenKey, FailureProvider5xx)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1, Evictions: 1}, breaker.Stats())

	clock.Advance(time.Second)
	breaker.OnSuccess(oldHealthyKey)
	assert.Equal(t, 2, breaker.Stats().Entries)

	clock.Advance(time.Second)
	breaker.OnSuccess(newHealthyKey)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 2}, breaker.Stats())

	clock.Advance(time.Second)
	require.Equal(t, StateDegraded, breaker.OnReliabilityFailure(degradedKey, FailureNetwork).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 3}, breaker.Stats())

	clock.Advance(time.Second)
	breaker.OnReliabilityFailure(newOpenKey, FailureProvider5xx)
	require.Equal(t, StateOpen, breaker.Peek(newOpenKey).State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 2, Evictions: 4}, breaker.Stats())

	clock.Advance(time.Second)
	breaker.OnReliabilityFailure(newestOpenKey, FailureProvider5xx)
	require.Equal(t, StateOpen, breaker.Peek(newestOpenKey).State)
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
	assert.Equal(t, Stats{Entries: 2, Evictions: 2}, breaker.Stats())
	assert.Equal(t, StateOpen, breaker.GetSnapshot(keys[1]).State)
	assert.Equal(t, StateOpen, breaker.GetSnapshot(keys[2]).State)
	assert.Empty(t, breaker.DirtySnapshots())
}

func TestBreakerAdmissionPreservesCriticalStatesOverNewHealthyEntries(t *testing.T) {
	tests := []struct {
		name  string
		admit func(*Breaker, Key, time.Time) (Snapshot, []Snapshot)
	}{
		{
			name: "success",
			admit: func(breaker *Breaker, key Key, _ time.Time) (Snapshot, []Snapshot) {
				return breaker.OnSuccess(key), nil
			},
		},
		{
			name: "reset",
			admit: func(breaker *Breaker, key Key, _ time.Time) (Snapshot, []Snapshot) {
				return breaker.Reset(key), nil
			},
		},
		{
			name: "hydrate",
			admit: func(breaker *Breaker, key Key, now time.Time) (Snapshot, []Snapshot) {
				return Snapshot{}, breaker.Hydrate([]Snapshot{{Key: key, State: StateHealthy, UpdatedAt: now}})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			breaker := New(Config{EntryTTL: time.Hour, MaxEntries: 2, Now: func() time.Time { return now }})
			openKey := Key{ChannelID: 30, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"}
			halfOpenKey := Key{ChannelID: 31, APIKeyIndex: SingleAPIKeyIndex, Model: "half-open", Group: "default"}
			newKey := Key{ChannelID: 32, APIKeyIndex: SingleAPIKeyIndex, Model: "healthy", Group: "default"}
			accepted := breaker.Hydrate([]Snapshot{
				{Key: openKey, State: StateOpen, CooldownUntil: now.Add(time.Hour), UpdatedAt: now.Add(-2 * time.Second)},
				{Key: halfOpenKey, State: StateHalfOpen, UpdatedAt: now.Add(-time.Second)},
			})
			require.Len(t, accepted, 2)

			result, admitted := test.admit(breaker, newKey, now)

			if test.name != "hydrate" {
				assert.Equal(t, StateHealthy, result.State)
			}
			assert.Empty(t, admitted)
			assert.Equal(t, Stats{Entries: 2, Evictions: 1}, breaker.Stats())
			assert.Equal(t, StateOpen, breaker.GetSnapshot(openKey).State)
			assert.Equal(t, StateHalfOpen, breaker.GetSnapshot(halfOpenKey).State)
			assert.Empty(t, breaker.DirtySnapshots())
		})
	}
}

func TestDefaultBreakerDoesNotPublishRejectedHealthyAdmission(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.MaxEntries = 2
	config.Now = func() time.Time { return now }
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	openKey := Key{ChannelID: 40, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"}
	halfOpenKey := Key{ChannelID: 41, APIKeyIndex: SingleAPIKeyIndex, Model: "half-open", Group: "default"}
	newKey := Key{ChannelID: 42, APIKeyIndex: SingleAPIKeyIndex, Model: "healthy", Group: "default"}
	HydrateDefaultSnapshots([]Snapshot{
		{Key: openKey, State: StateOpen, CooldownUntil: now.Add(time.Hour), UpdatedAt: now.Add(-2 * time.Second)},
		{Key: halfOpenKey, State: StateHalfOpen, UpdatedAt: now.Add(-time.Second)},
	})

	openCached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 40, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), openCached.State)
	halfOpenCached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 41, APIKeyIndex: SingleAPIKeyIndex, Model: "half-open", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHalfOpen), halfOpenCached.State)

	snapshot := RecordAttempt(newKey, true, 0, 0)

	assert.Equal(t, StateHealthy, snapshot.State)
	assert.Equal(t, Stats{Entries: 2, Evictions: 1}, RuntimeStats())
	assert.Empty(t, DirtySnapshots())
	_, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 42, APIKeyIndex: SingleAPIKeyIndex, Model: "healthy", Group: "default"})
	assert.False(t, ok)
	openCached, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 40, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), openCached.State)
	halfOpenCached, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 41, APIKeyIndex: SingleAPIKeyIndex, Model: "half-open", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHalfOpen), halfOpenCached.State)
}

func TestHydrateDefaultSnapshotsAdvancesExpiredOpenBeforePublishing(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.Now = clock.Now
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	key := Key{ChannelID: 48, APIKeyIndex: SingleAPIKeyIndex, Model: "expired-open", Group: "default"}
	HydrateDefaultSnapshots([]Snapshot{{
		Key:           key,
		State:         StateOpen,
		CooldownUntil: clock.now.Add(-time.Second),
		UpdatedAt:     clock.now.Add(-2 * time.Second),
	}})

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 48, APIKeyIndex: SingleAPIKeyIndex, Model: "expired-open", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHalfOpen), cached.State)
	assert.Zero(t, cached.HalfOpenInflight)
	dirty := DirtySnapshots()
	require.Len(t, dirty, 1)
	assert.Equal(t, key, dirty[0].Key)
	assert.Equal(t, StateHalfOpen, dirty[0].State)
	assert.Zero(t, dirty[0].HalfOpenInflight)
}

func TestDefaultBreakerEvictionRemovesPublishedSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.MaxEntries = 2
	config.Consecutive5xxThreshold = 1
	config.Now = func() time.Time { return now }
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	oldestKey := Key{ChannelID: 43, APIKeyIndex: SingleAPIKeyIndex, Model: "oldest", Group: "default"}
	newerKey := Key{ChannelID: 44, APIKeyIndex: SingleAPIKeyIndex, Model: "newer", Group: "default"}
	openKey := Key{ChannelID: 45, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"}
	HydrateDefaultSnapshots([]Snapshot{
		{Key: oldestKey, State: StateHealthy, UpdatedAt: now.Add(-2 * time.Second)},
		{Key: newerKey, State: StateHealthy, UpdatedAt: now.Add(-time.Second)},
	})
	require.Equal(t, Stats{Entries: 2}, RuntimeStats())

	opened := RecordAttempt(openKey, false, http.StatusInternalServerError, 0)

	require.Equal(t, StateOpen, opened.State)
	assert.Equal(t, Stats{Entries: 2, Dirty: 1, Evictions: 1}, RuntimeStats())
	_, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 43, APIKeyIndex: SingleAPIKeyIndex, Model: "oldest", Group: "default"})
	assert.False(t, ok)
	_, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 44, APIKeyIndex: SingleAPIKeyIndex, Model: "newer", Group: "default"})
	assert.True(t, ok)
	_, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 45, APIKeyIndex: SingleAPIKeyIndex, Model: "open", Group: "default"})
	assert.True(t, ok)
}

func TestDefaultBreakerSerializesPublicationWithConcurrentEviction(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.MaxEntries = 1
	config.Consecutive5xxThreshold = 1
	config.Now = func() time.Time { return now }
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	firstKey := Key{ChannelID: 46, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"}
	secondKey := Key{ChannelID: 47, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"}
	publishStarted := make(chan struct{})
	allowPublish := make(chan struct{})
	defaultBreaker.onRetained = func(snapshot Snapshot) {
		if snapshot.Key == firstKey {
			close(publishStarted)
			<-allowPublish
		}
		publishSnapshot(snapshot)
	}

	firstDone := make(chan struct{})
	go func() {
		RecordAttempt(firstKey, true, 0, 0)
		close(firstDone)
	}()
	<-publishStarted

	mutationLockWasAvailable := defaultBreaker.mu.TryLock()
	if mutationLockWasAvailable {
		defaultBreaker.mu.Unlock()
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(secondStarted)
		RecordAttempt(secondKey, false, http.StatusInternalServerError, 0)
		close(secondDone)
	}()
	<-secondStarted
	close(allowPublish)
	<-firstDone
	<-secondDone

	assert.False(t, mutationLockWasAvailable)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1, Evictions: 1}, RuntimeStats())
	_, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 46, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"})
	assert.False(t, ok)
	secondCached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 47, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), secondCached.State)
}

func TestClearDefaultKeySerializesCacheClearWithConcurrentRecord(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.Consecutive5xxThreshold = 1
	config.Now = func() time.Time { return now }
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	key := Key{ChannelID: 49, APIKeyIndex: SingleAPIKeyIndex, Model: "clear-key", Group: "default"}
	RecordAttempt(key, false, http.StatusInternalServerError, 0)
	require.Equal(t, Stats{Entries: 1, Dirty: 1}, RuntimeStats())

	clearStarted := make(chan bool, 1)
	allowClear := make(chan struct{})
	defaultBreaker.onClearKey = func(clearedKey Key) {
		_, statePresent := defaultBreaker.states[clearedKey]
		_, dirtyPresent := defaultBreaker.dirty[clearedKey]
		clearStarted <- statePresent || dirtyPresent
		<-allowClear
		clearPublishedSnapshot(clearedKey)
	}

	clearDone := make(chan struct{})
	go func() {
		ClearDefaultKey(key)
		close(clearDone)
	}()
	internalStatePresent := <-clearStarted
	mutationLockWasAvailable := defaultBreaker.mu.TryLock()
	if mutationLockWasAvailable {
		defaultBreaker.mu.Unlock()
	}

	recordDone := make(chan struct{})
	if mutationLockWasAvailable {
		RecordAttempt(key, true, 0, 0)
		close(recordDone)
	} else {
		go func() {
			RecordAttempt(key, true, 0, 0)
			close(recordDone)
		}()
	}
	close(allowClear)
	<-clearDone
	<-recordDone

	assert.False(t, internalStatePresent)
	assert.False(t, mutationLockWasAvailable)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1}, RuntimeStats())
	dirty := DirtySnapshots()
	require.Len(t, dirty, 1)
	assert.Equal(t, key, dirty[0].Key)
	assert.Equal(t, StateHealthy, dirty[0].State)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 49, APIKeyIndex: SingleAPIKeyIndex, Model: "clear-key", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHealthy), cached.State)
}

func TestClearDefaultChannelWithCacheSerializesAllCacheClearWithConcurrentRecord(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	config := DefaultConfig()
	config.EntryTTL = time.Hour
	config.Consecutive5xxThreshold = 1
	config.Now = func() time.Time { return now }
	ResetDefaultForTest(config)
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	const channelID = 50
	key := Key{ChannelID: channelID, APIKeyIndex: SingleAPIKeyIndex, Model: "clear-channel", Group: "default"}
	cacheKey := routinghotcache.Key{ChannelID: channelID, APIKeyIndex: SingleAPIKeyIndex, Model: "clear-channel", Group: "default"}
	RecordAttempt(key, false, http.StatusInternalServerError, 0)
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{UpdatedUnix: now.Unix()})
	routinghotcache.SetCostForTest(cacheKey.CostKey(), routinghotcache.CostSnapshot{UpdatedUnix: now.Unix()})
	routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(), UpdatedUnixMilli: now.UnixMilli()})
	routinghotcache.SetAuthFailureForTest(channelID, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now.Unix()})
	routinghotcache.SetBalanceForTest(channelID, routinghotcache.BalanceSnapshot{Known: true, UpdatedUnix: now.Unix()})
	require.Equal(t, Stats{Entries: 1, Dirty: 1}, RuntimeStats())

	clearStarted := make(chan bool, 1)
	allowClear := make(chan struct{})
	clearCalls := 0
	clearCache := func(clearedChannelID int) {
		clearCalls++
		internalStatePresent := false
		for stateKey := range defaultBreaker.states {
			if stateKey.ChannelID == clearedChannelID {
				internalStatePresent = true
				break
			}
		}
		if !internalStatePresent {
			for dirtyKey := range defaultBreaker.dirty {
				if dirtyKey.ChannelID == clearedChannelID {
					internalStatePresent = true
					break
				}
			}
		}
		clearStarted <- internalStatePresent
		<-allowClear
		routinghotcache.ClearChannel(clearedChannelID)
	}

	clearDone := make(chan struct{})
	go func() {
		ClearDefaultChannelWithCache(channelID, clearCache)
		close(clearDone)
	}()
	internalStatePresent := <-clearStarted
	mutationLockWasAvailable := defaultBreaker.mu.TryLock()
	if mutationLockWasAvailable {
		defaultBreaker.mu.Unlock()
	}

	recordDone := make(chan struct{})
	if mutationLockWasAvailable {
		RecordAttempt(key, true, 0, 0)
		close(recordDone)
	} else {
		go func() {
			RecordAttempt(key, true, 0, 0)
			close(recordDone)
		}()
	}
	close(allowClear)
	<-clearDone
	<-recordDone

	assert.False(t, internalStatePresent)
	assert.False(t, mutationLockWasAvailable)
	assert.Equal(t, 1, clearCalls)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1}, RuntimeStats())
	_, metricOK := routinghotcache.GetMetric(cacheKey)
	_, costOK := routinghotcache.GetCost(cacheKey.CostKey())
	_, capacityOK := routinghotcache.GetCapacityCooldown(cacheKey)
	_, authOK := routinghotcache.GetAuthFailure(channelID)
	_, balanceOK := routinghotcache.GetBalance(channelID)
	assert.False(t, metricOK)
	assert.False(t, costOK)
	assert.False(t, capacityOK)
	assert.False(t, authOK)
	assert.False(t, balanceOK)
	cached, ok := routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	assert.Equal(t, string(StateHealthy), cached.State)
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
	assert.Equal(t, 24*time.Hour, defaults.ResetGenerationTTL)
	assert.Equal(t, 40_000, defaults.MaxResetGenerations)

	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{EntryTTL: 0, MaxEntries: -1, Now: clock.Now})
	breaker.OnSuccess(Key{ChannelID: 20, APIKeyIndex: SingleAPIKeyIndex, Model: "first", Group: "default"})
	clock.Advance(2 * time.Minute)
	breaker.OnSuccess(Key{ChannelID: 21, APIKeyIndex: SingleAPIKeyIndex, Model: "second", Group: "default"})
	breaker.OnSuccess(Key{ChannelID: 22, APIKeyIndex: SingleAPIKeyIndex, Model: "third", Group: "default"})

	assert.Equal(t, Stats{Entries: 3, Dirty: 3}, breaker.Stats())
}

func TestDefaultEntryTTLReturnsNormalizedActiveConfig(t *testing.T) {
	ResetDefaultForTest(Config{})
	t.Cleanup(func() { ResetDefaultForTest(DefaultConfig()) })

	assert.Equal(t, DefaultConfig().EntryTTL, DefaultEntryTTL())

	ConfigureDefault(Config{EntryTTL: 7 * time.Minute})
	assert.Equal(t, 7*time.Minute, DefaultEntryTTL())
}

func TestBreakerOpensAfterFiveConsecutive5xx(t *testing.T) {
	breaker, clock, key := testBreaker(t)

	for i := 1; i < 5; i++ {
		snapshot := breaker.OnReliabilityFailure(key, FailureProvider5xx)
		assert.NotEqual(t, StateOpen, snapshot.State)
		assert.Equal(t, i, snapshot.Consecutive5xx)
	}

	snapshot := breaker.OnReliabilityFailure(key, FailureProvider5xx)
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
		snapshot = breaker.OnReliabilityFailure(key, FailureProvider5xx)
		if i < 24 {
			snapshot = breaker.OnSuccess(key)
		}
	}
	require.NotEqual(t, StateOpen, snapshot.State)
	assert.Equal(t, 49, snapshot.WindowRequests)
	assert.Equal(t, 25, snapshot.WindowFailures)

	snapshot = breaker.OnReliabilityFailure(key, FailureProvider5xx)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, 50, snapshot.WindowRequests)
	assert.Equal(t, 26, snapshot.WindowFailures)
	assert.Equal(t, 2, snapshot.Consecutive5xx)
}

func TestBreakerIgnoresCapacityStatusesWithoutCreatingState(t *testing.T) {
	breaker, _, key := testBreaker(t)

	for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests, 529} {
		snapshot := breaker.RecordHTTPAttempt(key, false, status)
		assert.Equal(t, StateHealthy, snapshot.State)
	}

	assert.Equal(t, Stats{}, breaker.Stats())
}

func TestBreakerIgnoresNonReliability5xxWithoutCreatingState(t *testing.T) {
	breaker, _, key := testBreaker(t)

	for _, status := range []int{http.StatusNotImplemented, http.StatusHTTPVersionNotSupported} {
		assert.Equal(t, StateHealthy, breaker.RecordHTTPAttempt(key, false, status).State)
	}

	assert.Equal(t, Stats{}, breaker.Stats())
}

func TestCapacityStatusDoesNotMutateExistingReliabilityState(t *testing.T) {
	breaker, _, key := testBreaker(t)
	before := breaker.OnReliabilityFailure(key, FailureProvider5xx)

	breaker.RecordHTTPAttempt(key, false, http.StatusTooManyRequests)
	breaker.RecordHTTPAttempt(key, false, 529)

	after := breaker.Peek(key)
	assert.Equal(t, before, after)
	assert.Equal(t, Stats{Entries: 1, Dirty: 1}, breaker.Stats())
}

func TestCapacityStatusDoesNotAdvanceOrReopenHalfOpenReliabilityState(t *testing.T) {
	breaker, clock, key := testBreaker(t)
	for range 5 {
		breaker.OnReliabilityFailure(key, FailureProvider5xx)
	}
	opened := breaker.Peek(key)
	clock.Advance(opened.CooldownUntil.Sub(clock.now))

	breaker.RecordHTTPAttempt(key, false, http.StatusTooManyRequests)

	assert.Equal(t, StateOpen, breaker.Peek(key).State)
}

func TestBreakerReliabilityHTTPStatuses(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			breaker, _, key := testBreaker(t)

			snapshot := breaker.RecordHTTPAttempt(key, false, status)

			assert.Equal(t, 1, snapshot.WindowRequests)
			assert.Equal(t, 1, snapshot.WindowFailures)
			assert.Equal(t, 1, snapshot.ConsecutiveFailures)
			assert.Equal(t, 1, snapshot.Consecutive5xx)
			assert.Equal(t, Stats{Entries: 1, Dirty: 1}, breaker.Stats())
		})
	}
}

func TestNetworkFailuresEnterReliabilityWindow(t *testing.T) {
	breaker, _, key := testBreaker(t)

	snapshot := breaker.OnReliabilityFailure(key, FailureNetwork)

	assert.Equal(t, 1, snapshot.WindowRequests)
	assert.Equal(t, 1, snapshot.WindowFailures)
	assert.Equal(t, 1, snapshot.ConsecutiveFailures)
	assert.Zero(t, snapshot.Consecutive5xx)
}

func TestRecordAttemptIgnoresRetryAfter(t *testing.T) {
	breaker, clock, key := testBreaker(t)
	breaker.config.Consecutive5xxThreshold = 1
	previous := defaultBreaker
	defaultBreaker = breaker
	t.Cleanup(func() { defaultBreaker = previous })

	snapshot := RecordAttempt(key, false, http.StatusInternalServerError, 45*time.Second)

	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, clock.now.Add(10*time.Second), snapshot.CooldownUntil)
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
				return breaker.OnReliabilityFailure(key, FailureProvider5xx)
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
				breaker.OnReliabilityFailure(key, FailureProvider5xx)
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
		breaker.OnReliabilityFailure(key, FailureProvider5xx)
	}
	clock.Advance(10 * time.Second)
	require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)

	snapshot := breaker.OnReliabilityFailure(key, FailureProvider5xx)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, clock.now.Add(20*time.Second), snapshot.CooldownUntil)

	clock.Advance(20 * time.Second)
	require.Equal(t, StateHalfOpen, breaker.GetSnapshot(key).State)
	snapshot = breaker.OnReliabilityFailure(key, FailureProvider5xx)
	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, 3, snapshot.EjectionCount)
	assert.Equal(t, clock.now.Add(25*time.Second), snapshot.CooldownUntil)
}

func TestBreakerResetClearsState(t *testing.T) {
	breaker, _, key := testBreaker(t)
	for i := 0; i < 5; i++ {
		breaker.OnReliabilityFailure(key, FailureProvider5xx)
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
		breaker.OnReliabilityFailure(key, FailureProvider5xx)
	}
	clock.Advance(10 * time.Second)

	first, ok := breaker.AcquireHalfOpenProbe(key, 1)
	require.True(t, ok)
	require.Equal(t, StateHalfOpen, first.State)
	assert.Equal(t, 1, first.HalfOpenInflight)

	second, ok := breaker.AcquireHalfOpenProbe(key, 1)
	require.False(t, ok)
	assert.Equal(t, 1, second.HalfOpenInflight)

	reopened := breaker.OnReliabilityFailure(key, FailureProvider5xx)
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
	snapshot := breaker.OnReliabilityFailure(key, FailureProvider5xx)

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
	local := breaker.OnReliabilityFailure(key, FailureProvider5xx)
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

func TestRecordAttemptStoresReliabilityReasonAndResetDefaultKeyPublishesHealthy(t *testing.T) {
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
	snapshot := RecordAttempt(key, false, http.StatusInternalServerError, 2*time.Second)

	require.Equal(t, StateOpen, snapshot.State)
	assert.Equal(t, "5xx", snapshot.Reason)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), cached.State)
	assert.Equal(t, "5xx", cached.Reason)

	reset := ResetDefaultKey(key)

	require.Equal(t, StateHealthy, reset.State)
	assert.Empty(t, reset.Reason)
	cached, ok = routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 88, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, string(StateHealthy), cached.State)
	assert.Empty(t, cached.Reason)
}

func TestResetDefaultKeyDoesNotClearCapacityCooldown(t *testing.T) {
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

	key := Key{ChannelID: 92, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	cacheKey := routinghotcache.Key{ChannelID: key.ChannelID, APIKeyIndex: key.APIKeyIndex, Model: key.Model, Group: key.Group}
	routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{
		SourceStatusCode:       http.StatusTooManyRequests,
		CooldownUntilUnixMilli: clock.now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       clock.now.UnixMilli(),
	})
	RecordReliabilityFailure(key, FailureProvider5xx)

	ResetDefaultKey(key)

	_, ok := routinghotcache.GetCapacityCooldown(cacheKey)
	assert.True(t, ok)
	ClearDefaultChannel(key.ChannelID)
	_, ok = routinghotcache.GetCapacityCooldown(cacheKey)
	assert.False(t, ok)
}

func TestEndpointBreakerIsIsolatedFromMemberAndRegion(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}
	ResetDefaultForTest(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown:            time.Minute,
		MaxCooldown:             time.Minute,
		EntryTTL:                time.Hour,
		MaxEntries:              64,
		Now:                     clock.Now,
	})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetDefaultForTest(DefaultConfig())
		routinghotcache.ResetForTest()
	})

	memberKey := Key{ChannelID: 93, APIKeyIndex: SingleAPIKeyIndex, Model: "gpt-test", Group: "default"}
	endpointEast := NewEndpointKey("https://api.example.test:443", "us-east-1")
	endpointWest := NewEndpointKey("https://api.example.test:443", "us-west-1")

	recorded := RecordReliabilityFailure(endpointEast, FailureNetwork)
	assert.Equal(t, StateOpen, recorded.State)
	assert.Empty(t, DirtySnapshots(), "endpoint failures must not enter the member persistence queue")
	endpointDirty := DirtyEndpointSnapshots()
	require.Len(t, endpointDirty, 1)
	assert.Equal(t, endpointEast, endpointDirty[0].Key)

	east, ok := routinghotcache.GetBreaker(endpointEast.HotcacheKey())
	require.True(t, ok)
	assert.Equal(t, string(StateOpen), east.State)
	_, westExists := routinghotcache.GetBreaker(endpointWest.HotcacheKey())
	assert.False(t, westExists)
	_, memberExists := routinghotcache.GetBreaker(memberKey.HotcacheKey())
	assert.False(t, memberExists)

	RecordReliabilityFailure(memberKey, FailureProvider5xx)
	memberDirty := DirtySnapshots()
	require.Len(t, memberDirty, 1)
	assert.Equal(t, memberKey, memberDirty[0].Key)
	assert.Empty(t, DirtyEndpointSnapshots())
}
