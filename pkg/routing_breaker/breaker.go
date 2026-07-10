package routingbreaker

import (
	"sort"
	"sync"
	"time"

	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
)

const SingleAPIKeyIndex = -1

type State string

const (
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

type Key struct {
	ChannelID   int
	APIKeyIndex int
	Model       string
	Group       string
}

type Config struct {
	Consecutive5xxThreshold int
	FailureRateThreshold    float64
	FailureRateMinSamples   int
	WindowSize              int
	BaseCooldown            time.Duration
	MaxCooldown             time.Duration
	EntryTTL                time.Duration
	MaxEntries              int

	DegradedConsecutiveFailures  int
	DegradedFailureRateThreshold float64
	DegradedMinSamples           int

	Now func() time.Time
}

type Snapshot struct {
	Key                 Key
	State               State
	Reason              string
	ConsecutiveFailures int
	Consecutive5xx      int
	EjectionCount       int
	OpenedAt            time.Time
	CooldownUntil       time.Time
	HalfOpenInflight    int
	WindowRequests      int
	WindowFailures      int
	UpdatedAt           time.Time
}

type Stats struct {
	Entries   int
	Dirty     int
	Evictions int64
}

type Breaker struct {
	mu        sync.Mutex
	config    Config
	states    map[Key]*entry
	dirty     map[Key]struct{}
	evictions int64
}

type entry struct {
	snapshot Snapshot
	window   []bool
}

var defaultBreaker = New(DefaultConfig())

func RecordAttempt(key Key, success bool, statusCode int, retryAfter time.Duration) Snapshot {
	var snapshot Snapshot
	if success {
		snapshot = defaultBreaker.OnSuccess(key)
	} else {
		snapshot = defaultBreaker.OnFailure(key, statusCode, retryAfter)
	}
	publishSnapshot(snapshot)
	return snapshot
}

func ConfigureDefault(config Config) {
	defaultBreaker.Configure(config)
}

func HydrateDefaultSnapshots(snapshots []Snapshot) {
	accepted := defaultBreaker.Hydrate(snapshots)
	for _, snapshot := range accepted {
		publishSnapshot(defaultBreaker.GetSnapshot(snapshot.Key))
	}
}

func AcquireDefaultHalfOpenProbe(key Key, maxProbes int) (Snapshot, bool) {
	snapshot, ok := defaultBreaker.AcquireHalfOpenProbe(key, maxProbes)
	publishSnapshot(snapshot)
	return snapshot, ok
}

func ReleaseDefaultHalfOpenProbe(key Key) Snapshot {
	snapshot := defaultBreaker.ReleaseHalfOpenProbe(key)
	publishSnapshot(snapshot)
	return snapshot
}

func DirtySnapshots() []Snapshot {
	return defaultBreaker.DirtySnapshots()
}

func RequeueDirtySnapshots(snapshots []Snapshot) {
	defaultBreaker.RequeueDirtySnapshots(snapshots)
}

func ResetDefaultKey(key Key) Snapshot {
	snapshot := defaultBreaker.Reset(key)
	publishSnapshot(snapshot)
	return snapshot
}

func ClearDefaultKey(key Key) {
	defaultBreaker.Clear(key)
	routinghotcache.ClearBreaker(routinghotcache.Key{
		ChannelID:   key.ChannelID,
		APIKeyIndex: key.APIKeyIndex,
		Model:       key.Model,
		Group:       key.Group,
	})
}

func ClearDefaultChannel(channelID int) {
	defaultBreaker.ClearChannel(channelID)
	routinghotcache.ClearChannel(channelID)
}

func ResetDefaultForTest(config Config) {
	defaultBreaker = New(config)
}

func DefaultConfig() Config {
	return Config{
		Consecutive5xxThreshold:      5,
		FailureRateThreshold:         0.5,
		FailureRateMinSamples:        50,
		WindowSize:                   100,
		BaseCooldown:                 30 * time.Second,
		MaxCooldown:                  5 * time.Minute,
		EntryTTL:                     30 * time.Minute,
		MaxEntries:                   20_000,
		DegradedConsecutiveFailures:  2,
		DegradedFailureRateThreshold: 0.2,
		DegradedMinSamples:           10,
		Now:                          time.Now,
	}
}

func New(config Config) *Breaker {
	return &Breaker{
		config: normalizeConfig(config),
		states: make(map[Key]*entry),
		dirty:  make(map[Key]struct{}),
	}
}

func (b *Breaker) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneLocked(b.config.Now(), 0)
	return Stats{
		Entries:   len(b.states),
		Dirty:     len(b.dirty),
		Evictions: b.evictions,
	}
}

func (b *Breaker) OnSuccess(key Key) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record := b.getOrCreate(key, now)
	b.advanceOpen(record, now)

	switch record.snapshot.State {
	case StateOpen:
		return record.snapshot
	case StateHalfOpen:
		record.resetWindow()
		record.addOutcome(false, b.config.WindowSize)
		record.snapshot.State = StateHealthy
		record.snapshot.Reason = ""
		record.snapshot.ConsecutiveFailures = 0
		record.snapshot.Consecutive5xx = 0
		record.snapshot.EjectionCount = 0
		record.snapshot.OpenedAt = time.Time{}
		record.snapshot.CooldownUntil = time.Time{}
		record.snapshot.HalfOpenInflight = 0
	default:
		record.addOutcome(false, b.config.WindowSize)
		record.snapshot.ConsecutiveFailures = 0
		record.snapshot.Consecutive5xx = 0
		record.snapshot.State = b.closedState(record)
		if record.snapshot.State == StateHealthy {
			record.snapshot.Reason = ""
		}
	}

	record.snapshot.UpdatedAt = now
	b.markDirty(key)
	return record.snapshot
}

func (b *Breaker) OnFailure(key Key, statusCode int, retryAfter time.Duration) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record := b.getOrCreate(key, now)
	b.advanceOpen(record, now)

	if record.snapshot.State == StateOpen {
		if statusCode == 429 && retryAfter > 0 {
			if retryAfter > b.config.MaxCooldown {
				retryAfter = b.config.MaxCooldown
			}
			cooldownUntil := now.Add(retryAfter)
			if cooldownUntil.After(record.snapshot.CooldownUntil) {
				record.snapshot.CooldownUntil = cooldownUntil
				record.snapshot.UpdatedAt = now
				b.markDirty(key)
			}
		}
		return record.snapshot
	}

	failedHalfOpen := record.snapshot.State == StateHalfOpen
	record.addOutcome(true, b.config.WindowSize)
	record.snapshot.ConsecutiveFailures++
	if is5xx(statusCode) {
		record.snapshot.Consecutive5xx++
	} else {
		record.snapshot.Consecutive5xx = 0
	}

	if failedHalfOpen ||
		statusCode == 429 ||
		record.snapshot.Consecutive5xx >= b.config.Consecutive5xxThreshold ||
		b.exceedsFailureRate(record) {
		b.open(record, now, retryAfter, b.failureReason(record, statusCode, failedHalfOpen))
		return record.snapshot
	}

	record.snapshot.State = b.closedState(record)
	if record.snapshot.State == StateDegraded {
		record.snapshot.Reason = b.degradedReason(record, statusCode)
	} else {
		record.snapshot.Reason = ""
	}
	record.snapshot.UpdatedAt = now
	b.markDirty(key)
	return record.snapshot
}

func (b *Breaker) GetSnapshot(key Key) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, ok := b.states[key]
	if ok && record.snapshot.UpdatedAt.Before(now.Add(-b.config.EntryTTL)) {
		b.evictLocked(key)
		ok = false
	}
	if !ok {
		return Snapshot{Key: key, State: StateHealthy, UpdatedAt: now}
	}
	if b.advanceOpen(record, now) {
		b.markDirty(key)
	}
	return record.snapshot
}

func (b *Breaker) Reset(key Key) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record := b.getOrCreate(key, now)
	record.snapshot = Snapshot{
		Key:       key,
		State:     StateHealthy,
		Reason:    "",
		UpdatedAt: now,
	}
	record.window = nil
	b.markDirty(key)
	return record.snapshot
}

func (b *Breaker) Clear(key Key) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.states, key)
	delete(b.dirty, key)
}

func (b *Breaker) ClearChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	for key := range b.states {
		if key.ChannelID == channelID {
			delete(b.states, key)
		}
	}
	for key := range b.dirty {
		if key.ChannelID == channelID {
			delete(b.dirty, key)
		}
	}
}

func (b *Breaker) AcquireHalfOpenProbe(key Key, maxProbes int) (Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record := b.getOrCreate(key, now)
	if b.advanceOpen(record, now) {
		b.markDirty(key)
	}
	if record.snapshot.State != StateHalfOpen {
		return record.snapshot, true
	}
	if maxProbes <= 0 {
		maxProbes = 1
	}
	if record.snapshot.HalfOpenInflight >= maxProbes {
		return record.snapshot, false
	}
	record.snapshot.HalfOpenInflight++
	record.snapshot.UpdatedAt = now
	b.markDirty(key)
	return record.snapshot, true
}

func (b *Breaker) ReleaseHalfOpenProbe(key Key) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record := b.getOrCreate(key, now)
	if record.snapshot.State == StateHalfOpen && record.snapshot.HalfOpenInflight > 0 {
		record.snapshot.HalfOpenInflight--
		record.snapshot.UpdatedAt = now
		b.markDirty(key)
	}
	return record.snapshot
}

func (b *Breaker) Hydrate(snapshots []Snapshot) []Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	b.pruneLocked(now, 0)
	accepted := make([]Snapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Key.ChannelID <= 0 || snapshot.Key.Model == "" || snapshot.Key.Group == "" {
			continue
		}
		snapshot.State = normalizeState(snapshot.State)
		if snapshot.UpdatedAt.IsZero() {
			snapshot.UpdatedAt = now
		}
		if snapshot.UpdatedAt.Before(now.Add(-b.config.EntryTTL)) {
			continue
		}
		if _, dirty := b.dirty[snapshot.Key]; dirty {
			continue
		}
		if existing, ok := b.states[snapshot.Key]; ok {
			if !snapshot.UpdatedAt.After(existing.snapshot.UpdatedAt) {
				continue
			}
		} else {
			b.pruneLocked(now, 1)
		}
		b.states[snapshot.Key] = &entry{
			snapshot: snapshot,
			window:   reconstructWindow(snapshot.WindowRequests, snapshot.WindowFailures),
		}
		accepted = append(accepted, snapshot)
	}

	retained := accepted[:0]
	for _, snapshot := range accepted {
		record, ok := b.states[snapshot.Key]
		if ok && record.snapshot.UpdatedAt.Equal(snapshot.UpdatedAt) {
			retained = append(retained, record.snapshot)
		}
	}
	return retained
}

func (b *Breaker) DirtySnapshots() []Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneLocked(b.config.Now(), 0)
	if len(b.dirty) == 0 {
		return nil
	}

	keys := make([]Key, 0, len(b.dirty))
	for key := range b.dirty {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return lessKey(keys[i], keys[j])
	})

	snapshots := make([]Snapshot, 0, len(keys))
	for _, key := range keys {
		if record, ok := b.states[key]; ok {
			snapshots = append(snapshots, record.snapshot)
		}
	}
	b.dirty = make(map[Key]struct{})
	return snapshots
}

func (b *Breaker) RequeueDirtySnapshots(snapshots []Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneLocked(b.config.Now(), 0)
	for _, snapshot := range snapshots {
		if snapshot.Key.ChannelID <= 0 || snapshot.Key.Model == "" || snapshot.Key.Group == "" {
			continue
		}
		if _, ok := b.states[snapshot.Key]; ok {
			b.dirty[snapshot.Key] = struct{}{}
		}
	}
}

func (b *Breaker) Configure(config Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = normalizeConfig(config)
	b.pruneLocked(b.config.Now(), 0)
}

func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.Consecutive5xxThreshold <= 0 {
		config.Consecutive5xxThreshold = defaults.Consecutive5xxThreshold
	}
	if config.FailureRateThreshold <= 0 || config.FailureRateThreshold > 1 {
		config.FailureRateThreshold = defaults.FailureRateThreshold
	}
	if config.FailureRateMinSamples <= 0 {
		config.FailureRateMinSamples = defaults.FailureRateMinSamples
	}
	if config.WindowSize <= 0 {
		config.WindowSize = defaults.WindowSize
	}
	if config.WindowSize < config.FailureRateMinSamples {
		config.WindowSize = config.FailureRateMinSamples
	}
	if config.BaseCooldown <= 0 {
		config.BaseCooldown = defaults.BaseCooldown
	}
	if config.MaxCooldown <= 0 {
		config.MaxCooldown = defaults.MaxCooldown
	}
	if config.EntryTTL <= 0 {
		config.EntryTTL = defaults.EntryTTL
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = defaults.MaxEntries
	}
	if config.DegradedConsecutiveFailures <= 0 {
		config.DegradedConsecutiveFailures = defaults.DegradedConsecutiveFailures
	}
	if config.DegradedFailureRateThreshold <= 0 || config.DegradedFailureRateThreshold >= config.FailureRateThreshold {
		config.DegradedFailureRateThreshold = defaults.DegradedFailureRateThreshold
	}
	if config.DegradedMinSamples <= 0 {
		config.DegradedMinSamples = defaults.DegradedMinSamples
	}
	if config.DegradedMinSamples > config.WindowSize {
		config.DegradedMinSamples = config.WindowSize
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	return config
}

func (b *Breaker) getOrCreate(key Key, now time.Time) *entry {
	record, ok := b.states[key]
	if ok && !record.snapshot.UpdatedAt.Before(now.Add(-b.config.EntryTTL)) {
		return record
	}
	if ok {
		b.evictLocked(key)
	}
	b.pruneLocked(now, 1)
	record = &entry{
		snapshot: Snapshot{
			Key:       key,
			State:     StateHealthy,
			UpdatedAt: now,
		},
	}
	b.states[key] = record
	return record
}

func (b *Breaker) pruneLocked(now time.Time, reserve int) {
	cutoff := now.Add(-b.config.EntryTTL)
	for key, record := range b.states {
		if record.snapshot.UpdatedAt.Before(cutoff) {
			b.evictLocked(key)
		}
	}

	if reserve < 0 {
		reserve = 0
	}
	limit := b.config.MaxEntries - reserve
	if limit < 0 {
		limit = 0
	}
	if len(b.states) <= limit {
		return
	}

	keys := make([]Key, 0, len(b.states))
	for key := range b.states {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := b.states[keys[i]].snapshot
		right := b.states[keys[j]].snapshot
		leftPriority := evictionPriority(left.State)
		rightPriority := evictionPriority(right.State)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.Before(right.UpdatedAt)
		}
		return lessKey(keys[i], keys[j])
	})
	for _, key := range keys[:len(b.states)-limit] {
		b.evictLocked(key)
	}
}

func (b *Breaker) evictLocked(key Key) {
	if _, ok := b.states[key]; !ok {
		return
	}
	// At extreme key cardinality, the hard memory boundary takes precedence
	// over persisting every dirty transition. The independently bounded hot
	// cache keeps the most recently published snapshot until its own eviction.
	delete(b.states, key)
	delete(b.dirty, key)
	b.evictions++
}

func evictionPriority(state State) int {
	switch state {
	case StateHealthy:
		return 0
	case StateDegraded:
		return 1
	case StateOpen, StateHalfOpen:
		return 2
	default:
		return 0
	}
}

func (b *Breaker) advanceOpen(record *entry, now time.Time) bool {
	if record.snapshot.State != StateOpen {
		return false
	}
	if now.Before(record.snapshot.CooldownUntil) {
		return false
	}
	record.snapshot.State = StateHalfOpen
	record.snapshot.HalfOpenInflight = 0
	record.snapshot.UpdatedAt = now
	return true
}

func (b *Breaker) open(record *entry, now time.Time, retryAfter time.Duration, reason string) {
	record.snapshot.State = StateOpen
	record.snapshot.Reason = reason
	record.snapshot.EjectionCount++
	record.snapshot.OpenedAt = now
	record.snapshot.HalfOpenInflight = 0
	cooldown := b.cooldown(record.snapshot.EjectionCount)
	if retryAfter > b.config.MaxCooldown {
		retryAfter = b.config.MaxCooldown
	}
	if retryAfter > cooldown {
		cooldown = retryAfter
	}
	record.snapshot.CooldownUntil = now.Add(cooldown)
	record.snapshot.UpdatedAt = now
	b.markDirty(record.snapshot.Key)
}

func (b *Breaker) failureReason(record *entry, statusCode int, failedHalfOpen bool) string {
	if failedHalfOpen {
		return "half_open_failure"
	}
	if statusCode == 429 {
		return "rate_limit"
	}
	if is5xx(statusCode) && record.snapshot.Consecutive5xx >= b.config.Consecutive5xxThreshold {
		return "5xx"
	}
	return "failure_rate"
}

func (b *Breaker) degradedReason(record *entry, statusCode int) string {
	if is5xx(statusCode) && record.snapshot.ConsecutiveFailures >= b.config.DegradedConsecutiveFailures {
		return "5xx"
	}
	return "failure_rate"
}

func (b *Breaker) cooldown(ejectionCount int) time.Duration {
	cooldown := b.config.BaseCooldown * time.Duration(ejectionCount)
	if cooldown > b.config.MaxCooldown {
		return b.config.MaxCooldown
	}
	return cooldown
}

func (b *Breaker) exceedsFailureRate(record *entry) bool {
	if record.snapshot.WindowRequests < b.config.FailureRateMinSamples {
		return false
	}
	rate := float64(record.snapshot.WindowFailures) / float64(record.snapshot.WindowRequests)
	return rate > b.config.FailureRateThreshold
}

func (b *Breaker) closedState(record *entry) State {
	if record.snapshot.ConsecutiveFailures >= b.config.DegradedConsecutiveFailures {
		return StateDegraded
	}
	if record.snapshot.WindowRequests >= b.config.DegradedMinSamples {
		rate := float64(record.snapshot.WindowFailures) / float64(record.snapshot.WindowRequests)
		if rate >= b.config.DegradedFailureRateThreshold {
			return StateDegraded
		}
	}
	return StateHealthy
}

func (b *Breaker) markDirty(key Key) {
	b.dirty[key] = struct{}{}
}

func (e *entry) addOutcome(failed bool, windowSize int) {
	e.window = append(e.window, failed)
	if failed {
		e.snapshot.WindowFailures++
	}
	if len(e.window) > windowSize {
		if e.window[0] {
			e.snapshot.WindowFailures--
		}
		e.window = e.window[1:]
	}
	e.snapshot.WindowRequests = len(e.window)
}

func (e *entry) resetWindow() {
	e.window = nil
	e.snapshot.WindowRequests = 0
	e.snapshot.WindowFailures = 0
}

func is5xx(statusCode int) bool {
	return statusCode >= 500 && statusCode < 600
}

func lessKey(a, b Key) bool {
	if a.ChannelID != b.ChannelID {
		return a.ChannelID < b.ChannelID
	}
	if a.APIKeyIndex != b.APIKeyIndex {
		return a.APIKeyIndex < b.APIKeyIndex
	}
	if a.Model != b.Model {
		return a.Model < b.Model
	}
	return a.Group < b.Group
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func publishSnapshot(snapshot Snapshot) {
	routinghotcache.SetBreaker(routinghotcache.Key{
		ChannelID:   snapshot.Key.ChannelID,
		APIKeyIndex: snapshot.Key.APIKeyIndex,
		Model:       snapshot.Key.Model,
		Group:       snapshot.Key.Group,
	}, routinghotcache.BreakerSnapshot{
		State:             string(snapshot.State),
		Reason:            snapshot.Reason,
		CooldownUntilUnix: unixOrZero(snapshot.CooldownUntil),
		HalfOpenInflight:  int64(snapshot.HalfOpenInflight),
		UpdatedUnix:       snapshot.UpdatedAt.Unix(),
	})
}

func normalizeState(state State) State {
	switch state {
	case StateHealthy, StateDegraded, StateOpen, StateHalfOpen:
		return state
	default:
		return StateHealthy
	}
}

func reconstructWindow(requests int, failures int) []bool {
	if requests <= 0 {
		return nil
	}
	if failures < 0 {
		failures = 0
	}
	if failures > requests {
		failures = requests
	}
	window := make([]bool, 0, requests)
	for range requests - failures {
		window = append(window, false)
	}
	for range failures {
		window = append(window, true)
	}
	return window
}
