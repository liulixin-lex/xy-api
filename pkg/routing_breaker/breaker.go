package routingbreaker

import (
	"container/heap"
	"sort"
	"strings"
	"sync"
	"time"

	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
)

const SingleAPIKeyIndex = -1

type State string

type FailureKind string

type Scope string

const (
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"

	FailureProvider5xx FailureKind = "provider_5xx"
	FailureNetwork     FailureKind = "network"

	ScopeMember   Scope = "member"
	ScopeEndpoint Scope = "endpoint"
)

type Key struct {
	ChannelID         int
	APIKeyIndex       int
	Model             string
	Group             string
	Scope             Scope
	EndpointAuthority string
	Region            string
}

func NewEndpointKey(endpointAuthority string, region string) Key {
	return Key{
		APIKeyIndex:       SingleAPIKeyIndex,
		Scope:             ScopeEndpoint,
		EndpointAuthority: strings.ToLower(strings.TrimSpace(endpointAuthority)),
		Region:            strings.ToLower(strings.TrimSpace(region)),
	}
}

func (key Key) IsEndpointScoped() bool {
	return key.Scope == ScopeEndpoint
}

func (key Key) HotcacheKey() routinghotcache.Key {
	return routinghotcache.Key{
		ChannelID:         key.ChannelID,
		APIKeyIndex:       key.APIKeyIndex,
		Model:             key.Model,
		Group:             key.Group,
		Scope:             string(key.Scope),
		EndpointAuthority: key.EndpointAuthority,
		Region:            key.Region,
	}
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
	ResetGenerationTTL      time.Duration
	MaxResetGenerations     int

	DegradedConsecutiveFailures  int
	DegradedFailureRateThreshold float64
	DegradedMinSamples           int

	Now func() time.Time
}

type Snapshot struct {
	Key                 Key
	ResetGeneration     int64
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
	Entries                   int
	Dirty                     int
	Evictions                 int64
	ResetGenerationTombstones int
	ResetGenerationEvictions  int64
}

type Breaker struct {
	mu     sync.Mutex
	config Config
	states map[Key]*entry
	dirty  map[Key]struct{}
	// Reset tombstones outlive breaker entries; the heap bounds them without scanning request traffic.
	resetGenerations         map[Key]resetGenerationTombstone
	resetGenerationQueue     resetGenerationHeap
	evictions                int64
	resetGenerationEvictions int64
	// Callbacks run while mu is held so default-breaker publication and
	// removal stay linearized. They must not call back into Breaker.
	onRetained     func(Snapshot)
	onEvict        func(Key)
	onClearKey     func(Key)
	onClearChannel func(int)
}

type entry struct {
	snapshot Snapshot
	window   []bool
}

type resetGenerationTombstone struct {
	generation int64
	updatedAt  time.Time
}

type resetGenerationHeapItem struct {
	key        Key
	generation int64
	updatedAt  time.Time
}

type resetGenerationHeap []resetGenerationHeapItem

func (items resetGenerationHeap) Len() int { return len(items) }

func (items resetGenerationHeap) Less(left int, right int) bool {
	if !items[left].updatedAt.Equal(items[right].updatedAt) {
		return items[left].updatedAt.Before(items[right].updatedAt)
	}
	if items[left].key != items[right].key {
		return lessKey(items[left].key, items[right].key)
	}
	return items[left].generation < items[right].generation
}

func (items resetGenerationHeap) Swap(left int, right int) {
	items[left], items[right] = items[right], items[left]
}

func (items *resetGenerationHeap) Push(value any) {
	*items = append(*items, value.(resetGenerationHeapItem))
}

func (items *resetGenerationHeap) Pop() any {
	old := *items
	lastIndex := len(old) - 1
	last := old[lastIndex]
	old[lastIndex] = resetGenerationHeapItem{}
	*items = old[:lastIndex]
	return last
}

type mutationResult struct {
	snapshot Snapshot
	retained bool
}

var defaultBreaker = newDefaultBreaker(DefaultConfig())
var defaultEndpointBreaker = newDefaultBreaker(DefaultConfig())

func defaultBreakerForKey(key Key) *Breaker {
	if key.IsEndpointScoped() {
		return defaultEndpointBreaker
	}
	return defaultBreaker
}

func RecordReliabilitySuccess(key Key) Snapshot {
	return defaultBreakerForKey(key).onSuccess(key).snapshot
}

func RecordActiveProbeSuccess(key Key) Snapshot {
	return defaultBreakerForKey(key).onActiveProbeSuccess(key).snapshot
}

func RecordReliabilityFailure(key Key, kind FailureKind) Snapshot {
	return defaultBreakerForKey(key).onReliabilityFailure(key, kind).snapshot
}

func RecordAttempt(key Key, success bool, statusCode int, _ time.Duration) Snapshot {
	return defaultBreakerForKey(key).RecordHTTPAttempt(key, success, statusCode)
}

func ConfigureDefault(config Config) {
	defaultBreaker.Configure(config)
	defaultEndpointBreaker.Configure(config)
}

func DefaultEntryTTL() time.Duration {
	defaultBreaker.mu.Lock()
	defer defaultBreaker.mu.Unlock()
	return defaultBreaker.config.EntryTTL
}

func HydrateDefaultSnapshots(snapshots []Snapshot) []Snapshot {
	return defaultBreaker.Hydrate(snapshots)
}

func HydrateDefaultEndpointSnapshots(snapshots []Snapshot) []Snapshot {
	return defaultEndpointBreaker.Hydrate(snapshots)
}

func AcquireDefaultHalfOpenProbe(key Key, maxProbes int) (Snapshot, bool) {
	result, ok := defaultBreakerForKey(key).acquireHalfOpenProbe(key, maxProbes)
	return result.snapshot, ok
}

func ReleaseDefaultHalfOpenProbe(key Key) Snapshot {
	result := defaultBreakerForKey(key).releaseHalfOpenProbe(key)
	return result.snapshot
}

func RuntimeStats() Stats {
	return defaultBreaker.Stats()
}

func EndpointRuntimeStats() Stats {
	return defaultEndpointBreaker.Stats()
}

func DirtySnapshots() []Snapshot {
	return defaultBreaker.DirtySnapshots()
}

func DirtyEndpointSnapshots() []Snapshot {
	return defaultEndpointBreaker.DirtySnapshots()
}

func RequeueDirtySnapshots(snapshots []Snapshot) {
	defaultBreaker.RequeueDirtySnapshots(snapshots)
}

func RequeueDirtyEndpointSnapshots(snapshots []Snapshot) {
	defaultEndpointBreaker.RequeueDirtySnapshots(snapshots)
}

func ResetDefaultKey(key Key) Snapshot {
	result := defaultBreakerForKey(key).reset(key)
	return result.snapshot
}

func ApplyDefaultResetGeneration(key Key, generation int64) (Snapshot, bool) {
	return defaultBreakerForKey(key).applyResetGeneration(key, generation, nil)
}

func ApplyDefaultResetGenerationWithCallback(key Key, generation int64, beforeApply func()) (Snapshot, bool) {
	return defaultBreakerForKey(key).applyResetGeneration(key, generation, beforeApply)
}

func DefaultResetGeneration(key Key) int64 {
	return defaultBreakerForKey(key).resetGeneration(key)
}

func ClearDefaultKey(key Key) {
	defaultBreakerForKey(key).Clear(key)
}

func ClearDefaultChannel(channelID int) {
	defaultBreaker.ClearChannel(channelID)
}

// ClearDefaultChannelWithCache clears a channel and invokes clearCache while
// the default breaker lock is held, keeping both removals linearized.
// clearCache must not call back into this package.
func ClearDefaultChannelWithCache(channelID int, clearCache func(int)) {
	defaultBreaker.clearChannel(channelID, clearCache)
}

func ResetDefaultForTest(config Config) {
	defaultBreaker = newDefaultBreaker(config)
	defaultEndpointBreaker = newDefaultBreaker(config)
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
		ResetGenerationTTL:           24 * time.Hour,
		MaxResetGenerations:          40_000,
		DegradedConsecutiveFailures:  2,
		DegradedFailureRateThreshold: 0.2,
		DegradedMinSamples:           10,
		Now:                          time.Now,
	}
}

func New(config Config) *Breaker {
	return &Breaker{
		config:               normalizeConfig(config),
		states:               make(map[Key]*entry),
		dirty:                make(map[Key]struct{}),
		resetGenerations:     make(map[Key]resetGenerationTombstone),
		resetGenerationQueue: make(resetGenerationHeap, 0),
	}
}

func newDefaultBreaker(config Config) *Breaker {
	breaker := New(config)
	breaker.onRetained = publishSnapshot
	breaker.onEvict = clearPublishedSnapshot
	breaker.onClearKey = clearPublishedSnapshot
	breaker.onClearChannel = routinghotcache.ClearChannel
	return breaker
}

func (b *Breaker) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	b.pruneLocked(now, 0)
	b.pruneResetGenerationsLocked(now, 0)
	return Stats{
		Entries:                   len(b.states),
		Dirty:                     len(b.dirty),
		Evictions:                 b.evictions,
		ResetGenerationTombstones: len(b.resetGenerations),
		ResetGenerationEvictions:  b.resetGenerationEvictions,
	}
}

func (b *Breaker) OnSuccess(key Key) Snapshot {
	return b.onSuccess(key).snapshot
}

func (b *Breaker) RecordHTTPAttempt(key Key, success bool, statusCode int) Snapshot {
	if success {
		return b.OnSuccess(key)
	}
	if !isReliabilityHTTPStatus(statusCode) {
		return b.Peek(key)
	}
	return b.OnReliabilityFailure(key, FailureProvider5xx)
}

func (b *Breaker) onSuccess(key Key) mutationResult {
	return b.recordSuccess(key, false)
}

func (b *Breaker) onActiveProbeSuccess(key Key) mutationResult {
	return b.recordSuccess(key, true)
}

func (b *Breaker) recordSuccess(key Key, activeProbe bool) mutationResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, created := b.getOrCreate(key, now)
	wasOpen := record.snapshot.State == StateOpen
	advancedOpen := b.advanceOpen(record, now)
	if activeProbe && wasOpen {
		// One active observation may open the recovery gate, but it must not
		// also erase the passive failure evidence that opened the breaker.
		if advancedOpen {
			b.markDirty(key)
		}
		return b.finishMutationLocked(record, created, now)
	}

	switch record.snapshot.State {
	case StateOpen:
		return b.finishMutationLocked(record, created, now)
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
	return b.finishMutationLocked(record, created, now)
}

func (b *Breaker) OnReliabilityFailure(key Key, kind FailureKind) Snapshot {
	return b.onReliabilityFailure(key, kind).snapshot
}

func (b *Breaker) onReliabilityFailure(key Key, kind FailureKind) mutationResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, created := b.getOrCreate(key, now)
	b.advanceOpen(record, now)

	if record.snapshot.State == StateOpen {
		return b.finishMutationLocked(record, created, now)
	}

	failedHalfOpen := record.snapshot.State == StateHalfOpen
	record.addOutcome(true, b.config.WindowSize)
	record.snapshot.ConsecutiveFailures++
	if kind == FailureProvider5xx {
		record.snapshot.Consecutive5xx++
	} else {
		record.snapshot.Consecutive5xx = 0
	}

	consecutiveLimitReached := record.snapshot.Consecutive5xx >= b.config.Consecutive5xxThreshold
	if kind == FailureNetwork && key.IsEndpointScoped() {
		consecutiveLimitReached = record.snapshot.ConsecutiveFailures >= b.config.Consecutive5xxThreshold
	}
	if failedHalfOpen ||
		consecutiveLimitReached ||
		b.exceedsFailureRate(record) {
		b.open(record, now, b.failureReason(record, kind, failedHalfOpen))
		return b.finishMutationLocked(record, created, now)
	}

	record.snapshot.State = b.closedState(record)
	if record.snapshot.State == StateDegraded {
		record.snapshot.Reason = b.degradedReason(record, kind)
	} else {
		record.snapshot.Reason = ""
	}
	record.snapshot.UpdatedAt = now
	b.markDirty(key)
	return b.finishMutationLocked(record, created, now)
}

func (b *Breaker) Peek(key Key) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.config.Now()
	if record, ok := b.states[key]; ok {
		return record.snapshot
	}
	return Snapshot{Key: key, ResetGeneration: b.resetGenerationValueLocked(key, now), State: StateHealthy}
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
		return Snapshot{
			Key: key, ResetGeneration: b.resetGenerationValueLocked(key, now),
			State: StateHealthy, UpdatedAt: now,
		}
	}
	if b.advanceOpen(record, now) {
		b.markDirty(key)
	}
	return record.snapshot
}

func (b *Breaker) Reset(key Key) Snapshot {
	return b.reset(key).snapshot
}

func (b *Breaker) reset(key Key) mutationResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, created := b.getOrCreate(key, now)
	generation := record.snapshot.ResetGeneration
	record.snapshot = Snapshot{
		Key: key, ResetGeneration: generation, State: StateHealthy, Reason: "", UpdatedAt: now,
	}
	record.window = nil
	b.markDirty(key)
	return b.finishMutationLocked(record, created, now)
}

func (b *Breaker) applyResetGeneration(key Key, generation int64, beforeApply func()) (Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	b.pruneLocked(now, 0)
	b.pruneResetGenerationsLocked(now, 0)
	currentGeneration := b.resetGenerationValueLocked(key, now)
	if record, ok := b.states[key]; ok && record.snapshot.ResetGeneration > currentGeneration {
		currentGeneration = record.snapshot.ResetGeneration
	}
	if generation <= currentGeneration {
		if record, ok := b.states[key]; ok {
			return record.snapshot, false
		}
		return Snapshot{
			Key: key, ResetGeneration: currentGeneration, State: StateHealthy, UpdatedAt: now,
		}, false
	}
	record, created := b.getOrCreate(key, now)
	if beforeApply != nil {
		beforeApply()
	}
	b.rememberResetGenerationLocked(key, generation, now)
	record.snapshot = Snapshot{
		Key: key, ResetGeneration: generation, State: StateHealthy, UpdatedAt: now,
	}
	record.window = nil
	delete(b.dirty, key)
	return b.finishMutationLocked(record, created, now).snapshot, true
}

func (b *Breaker) resetGeneration(key Key) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.config.Now()
	generation := b.resetGenerationValueLocked(key, now)
	if record, ok := b.states[key]; ok && record.snapshot.ResetGeneration > generation {
		generation = record.snapshot.ResetGeneration
	}
	return generation
}

func (b *Breaker) Clear(key Key) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.states, key)
	delete(b.dirty, key)
	if b.onClearKey != nil {
		b.onClearKey(key)
	}
}

func (b *Breaker) ClearChannel(channelID int) {
	b.clearChannel(channelID, b.onClearChannel)
}

func (b *Breaker) clearChannel(channelID int, clearCache func(int)) {
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
	if clearCache != nil {
		clearCache(channelID)
	}
}

func (b *Breaker) AcquireHalfOpenProbe(key Key, maxProbes int) (Snapshot, bool) {
	result, ok := b.acquireHalfOpenProbe(key, maxProbes)
	return result.snapshot, ok
}

func (b *Breaker) acquireHalfOpenProbe(key Key, maxProbes int) (mutationResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, created := b.getOrCreate(key, now)
	if b.advanceOpen(record, now) {
		b.markDirty(key)
	}
	if record.snapshot.State != StateHalfOpen {
		return b.finishMutationLocked(record, created, now), true
	}
	if maxProbes <= 0 {
		maxProbes = 1
	}
	if record.snapshot.HalfOpenInflight >= maxProbes {
		return b.finishMutationLocked(record, created, now), false
	}
	record.snapshot.HalfOpenInflight++
	record.snapshot.UpdatedAt = now
	b.markDirty(key)
	return b.finishMutationLocked(record, created, now), true
}

func (b *Breaker) ReleaseHalfOpenProbe(key Key) Snapshot {
	return b.releaseHalfOpenProbe(key).snapshot
}

func (b *Breaker) releaseHalfOpenProbe(key Key) mutationResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	record, created := b.getOrCreate(key, now)
	if record.snapshot.State == StateHalfOpen && record.snapshot.HalfOpenInflight > 0 {
		record.snapshot.HalfOpenInflight--
		record.snapshot.UpdatedAt = now
		b.markDirty(key)
	}
	return b.finishMutationLocked(record, created, now)
}

func (b *Breaker) Hydrate(snapshots []Snapshot) []Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.config.Now()
	b.pruneLocked(now, 0)
	b.pruneResetGenerationsLocked(now, 0)
	accepted := make([]Snapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !validBreakerKey(snapshot.Key) || snapshot.ResetGeneration < 0 {
			continue
		}
		snapshot.State = normalizeState(snapshot.State)
		if snapshot.UpdatedAt.IsZero() {
			snapshot.UpdatedAt = now
		}
		if snapshot.UpdatedAt.Before(now.Add(-b.config.EntryTTL)) {
			continue
		}
		if existing, ok := b.states[snapshot.Key]; ok {
			if snapshot.ResetGeneration < existing.snapshot.ResetGeneration ||
				(snapshot.ResetGeneration == existing.snapshot.ResetGeneration && !snapshot.UpdatedAt.After(existing.snapshot.UpdatedAt)) {
				continue
			}
			if _, dirty := b.dirty[snapshot.Key]; dirty && snapshot.ResetGeneration <= existing.snapshot.ResetGeneration {
				continue
			}
		}
		resetGeneration := b.resetGenerationValueLocked(snapshot.Key, now)
		if snapshot.ResetGeneration < resetGeneration {
			continue
		}
		if snapshot.ResetGeneration > resetGeneration {
			b.rememberResetGenerationLocked(snapshot.Key, snapshot.ResetGeneration, now)
		}
		delete(b.dirty, snapshot.Key)
		b.states[snapshot.Key] = &entry{
			snapshot: snapshot,
			window:   reconstructWindow(snapshot.WindowRequests, snapshot.WindowFailures),
		}
		accepted = append(accepted, snapshot)
	}
	b.pruneLocked(now, 0)
	b.pruneResetGenerationsLocked(now, 0)

	retained := accepted[:0]
	for _, snapshot := range accepted {
		record, ok := b.states[snapshot.Key]
		if ok && record.snapshot.UpdatedAt.Equal(snapshot.UpdatedAt) {
			if b.advanceOpen(record, now) {
				b.markDirty(snapshot.Key)
			}
			retained = append(retained, record.snapshot)
			if b.onRetained != nil {
				b.onRetained(record.snapshot)
			}
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
		if !validBreakerKey(snapshot.Key) {
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
	now := b.config.Now()
	b.pruneLocked(now, 0)
	b.pruneResetGenerationsLocked(now, 0)
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
	if config.ResetGenerationTTL <= 0 {
		config.ResetGenerationTTL = defaults.ResetGenerationTTL
	}
	if config.MaxResetGenerations <= 0 {
		config.MaxResetGenerations = defaults.MaxResetGenerations
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

func (b *Breaker) getOrCreate(key Key, now time.Time) (*entry, bool) {
	record, ok := b.states[key]
	if ok && !record.snapshot.UpdatedAt.Before(now.Add(-b.config.EntryTTL)) {
		return record, false
	}
	if ok {
		b.evictLocked(key)
	}
	record = &entry{
		snapshot: Snapshot{
			Key: key, ResetGeneration: b.resetGenerationValueLocked(key, now), State: StateHealthy, UpdatedAt: now,
		},
	}
	b.states[key] = record
	return record, true
}

func (b *Breaker) finishMutationLocked(record *entry, created bool, now time.Time) mutationResult {
	if created {
		b.pruneLocked(now, 0)
	}
	retained, ok := b.states[record.snapshot.Key]
	result := mutationResult{
		snapshot: record.snapshot,
		retained: ok && retained == record,
	}
	if result.retained && b.onRetained != nil {
		b.onRetained(result.snapshot)
	}
	return result
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
	overflow := len(b.states) - limit
	if overflow == 1 {
		var victim Key
		found := false
		for key := range b.states {
			if !found || b.lessEvictionCandidate(key, victim) {
				victim = key
				found = true
			}
		}
		if found {
			b.evictLocked(victim)
		}
		return
	}

	keys := make([]Key, 0, len(b.states))
	for key := range b.states {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return b.lessEvictionCandidate(keys[i], keys[j])
	})
	for _, key := range keys[:overflow] {
		b.evictLocked(key)
	}
}

func (b *Breaker) resetGenerationValueLocked(key Key, now time.Time) int64 {
	tombstone, ok := b.resetGenerations[key]
	if !ok {
		return 0
	}
	if tombstone.updatedAt.Before(now.Add(-b.config.ResetGenerationTTL)) {
		b.evictResetGenerationLocked(key)
		return 0
	}
	return tombstone.generation
}

func (b *Breaker) rememberResetGenerationLocked(key Key, generation int64, now time.Time) {
	if generation <= 0 {
		return
	}
	tombstone := resetGenerationTombstone{generation: generation, updatedAt: now}
	b.resetGenerations[key] = tombstone
	heap.Push(&b.resetGenerationQueue, resetGenerationHeapItem{
		key: key, generation: generation, updatedAt: now,
	})
	b.pruneResetGenerationsLocked(now, 0)
}

func (b *Breaker) pruneResetGenerationsLocked(now time.Time, reserve int) {
	cutoff := now.Add(-b.config.ResetGenerationTTL)
	if reserve < 0 {
		reserve = 0
	}
	limit := b.config.MaxResetGenerations - reserve
	if limit < 0 {
		limit = 0
	}
	for len(b.resetGenerationQueue) > 0 {
		candidate := b.resetGenerationQueue[0]
		current, exists := b.resetGenerations[candidate.key]
		if !exists || current.generation != candidate.generation || !current.updatedAt.Equal(candidate.updatedAt) {
			heap.Pop(&b.resetGenerationQueue)
			continue
		}
		if !current.updatedAt.Before(cutoff) && len(b.resetGenerations) <= limit {
			break
		}
		heap.Pop(&b.resetGenerationQueue)
		delete(b.resetGenerations, candidate.key)
		b.resetGenerationEvictions++
	}
	if len(b.resetGenerationQueue) > b.config.MaxResetGenerations {
		queue := make(resetGenerationHeap, 0, len(b.resetGenerations))
		for key, tombstone := range b.resetGenerations {
			queue = append(queue, resetGenerationHeapItem{
				key: key, generation: tombstone.generation, updatedAt: tombstone.updatedAt,
			})
		}
		heap.Init(&queue)
		b.resetGenerationQueue = queue
	}
}

func (b *Breaker) evictResetGenerationLocked(key Key) {
	if _, ok := b.resetGenerations[key]; !ok {
		return
	}
	delete(b.resetGenerations, key)
	b.resetGenerationEvictions++
	// The stale heap item is discarded or compacted on the next control-plane prune.
}

func (b *Breaker) lessEvictionCandidate(leftKey Key, rightKey Key) bool {
	left := b.states[leftKey].snapshot
	right := b.states[rightKey].snapshot
	leftPriority := evictionPriority(left.State)
	rightPriority := evictionPriority(right.State)
	if leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.Before(right.UpdatedAt)
	}
	return lessKey(leftKey, rightKey)
}

func (b *Breaker) evictLocked(key Key) {
	if _, ok := b.states[key]; !ok {
		return
	}
	// At extreme key cardinality, the hard memory boundary takes precedence
	// over persisting every dirty transition. The default breaker removes the
	// corresponding published snapshot through onEvict before releasing mu.
	delete(b.states, key)
	delete(b.dirty, key)
	b.evictions++
	if b.onEvict != nil {
		b.onEvict(key)
	}
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

func (b *Breaker) open(record *entry, now time.Time, reason string) {
	record.snapshot.State = StateOpen
	record.snapshot.Reason = reason
	record.snapshot.EjectionCount++
	record.snapshot.OpenedAt = now
	record.snapshot.HalfOpenInflight = 0
	cooldown := b.cooldown(record.snapshot.EjectionCount)
	record.snapshot.CooldownUntil = now.Add(cooldown)
	record.snapshot.UpdatedAt = now
	b.markDirty(record.snapshot.Key)
}

func (b *Breaker) failureReason(record *entry, kind FailureKind, failedHalfOpen bool) string {
	if failedHalfOpen {
		return "half_open_failure"
	}
	if kind == FailureProvider5xx && record.snapshot.Consecutive5xx >= b.config.Consecutive5xxThreshold {
		return "5xx"
	}
	if kind == FailureNetwork {
		return "network"
	}
	return "failure_rate"
}

func (b *Breaker) degradedReason(record *entry, kind FailureKind) string {
	if kind == FailureProvider5xx && record.snapshot.Consecutive5xx >= b.config.DegradedConsecutiveFailures {
		return "5xx"
	}
	if kind == FailureNetwork {
		return "network"
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

func isReliabilityHTTPStatus(statusCode int) bool {
	switch statusCode {
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func lessKey(a, b Key) bool {
	if a.Scope != b.Scope {
		return a.Scope < b.Scope
	}
	if a.EndpointAuthority != b.EndpointAuthority {
		return a.EndpointAuthority < b.EndpointAuthority
	}
	if a.Region != b.Region {
		return a.Region < b.Region
	}
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

func validBreakerKey(key Key) bool {
	if key.IsEndpointScoped() {
		return key.ChannelID == 0 && key.Model == "" && key.Group == "" &&
			key.EndpointAuthority != "" && len(key.EndpointAuthority) <= 320 &&
			key.Region != "" && len(key.Region) <= 64
	}
	return (key.Scope == "" || key.Scope == ScopeMember) &&
		key.ChannelID > 0 && key.Model != "" && key.Group != ""
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func publishSnapshot(snapshot Snapshot) {
	routinghotcache.SetBreaker(routinghotcache.Key{
		ChannelID:         snapshot.Key.ChannelID,
		APIKeyIndex:       snapshot.Key.APIKeyIndex,
		Model:             snapshot.Key.Model,
		Group:             snapshot.Key.Group,
		Scope:             string(snapshot.Key.Scope),
		EndpointAuthority: snapshot.Key.EndpointAuthority,
		Region:            snapshot.Key.Region,
	}, routinghotcache.BreakerSnapshot{
		State:             string(snapshot.State),
		Reason:            snapshot.Reason,
		CooldownUntilUnix: unixOrZero(snapshot.CooldownUntil),
		HalfOpenInflight:  int64(snapshot.HalfOpenInflight),
		UpdatedUnix:       snapshot.UpdatedAt.Unix(),
	})
}

func clearPublishedSnapshot(key Key) {
	routinghotcache.ClearBreaker(routinghotcache.Key{
		ChannelID:         key.ChannelID,
		APIKeyIndex:       key.APIKeyIndex,
		Model:             key.Model,
		Group:             key.Group,
		Scope:             string(key.Scope),
		EndpointAuthority: key.EndpointAuthority,
		Region:            key.Region,
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
