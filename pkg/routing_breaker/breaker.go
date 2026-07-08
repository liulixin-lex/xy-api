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

type Breaker struct {
	mu     sync.Mutex
	config Config
	states map[Key]*entry
	dirty  map[Key]struct{}
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
	routinghotcache.SetBreaker(routinghotcache.Key{
		ChannelID:   key.ChannelID,
		APIKeyIndex: key.APIKeyIndex,
		Model:       key.Model,
		Group:       key.Group,
	}, routinghotcache.BreakerSnapshot{
		State:       string(snapshot.State),
		Reason:      snapshot.Reason,
		UpdatedUnix: snapshot.UpdatedAt.Unix(),
	})
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
	routinghotcache.SetBreaker(routinghotcache.Key{
		ChannelID:   key.ChannelID,
		APIKeyIndex: key.APIKeyIndex,
		Model:       key.Model,
		Group:       key.Group,
	}, routinghotcache.BreakerSnapshot{
		State:       string(snapshot.State),
		Reason:      snapshot.Reason,
		UpdatedUnix: snapshot.UpdatedAt.Unix(),
	})
	return snapshot
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
	record := &entry{
		snapshot: Snapshot{
			Key:       key,
			State:     StateHealthy,
			Reason:    "",
			UpdatedAt: now,
		},
	}
	b.states[key] = record
	b.markDirty(key)
	return record.snapshot
}

func (b *Breaker) DirtySnapshots() []Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

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

	for _, snapshot := range snapshots {
		if snapshot.Key.ChannelID <= 0 || snapshot.Key.Model == "" || snapshot.Key.Group == "" {
			continue
		}
		b.dirty[snapshot.Key] = struct{}{}
	}
}

func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.Consecutive5xxThreshold <= 0 {
		config.Consecutive5xxThreshold = defaults.Consecutive5xxThreshold
	}
	if config.FailureRateThreshold <= 0 || config.FailureRateThreshold >= 1 {
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
	if ok {
		return record
	}
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

func (b *Breaker) advanceOpen(record *entry, now time.Time) bool {
	if record.snapshot.State != StateOpen {
		return false
	}
	if now.Before(record.snapshot.CooldownUntil) {
		return false
	}
	record.snapshot.State = StateHalfOpen
	record.snapshot.UpdatedAt = now
	return true
}

func (b *Breaker) open(record *entry, now time.Time, retryAfter time.Duration, reason string) {
	record.snapshot.State = StateOpen
	record.snapshot.Reason = reason
	record.snapshot.EjectionCount++
	record.snapshot.OpenedAt = now
	cooldown := b.cooldown(record.snapshot.EjectionCount)
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
