package channelrouting

import (
	"errors"
	"math"
	"sync"
	"time"
)

var (
	ErrSlowStartInvalidPolicy = errors.New("invalid slow start policy")
	ErrSlowStartInvalidKey    = errors.New("invalid slow start key")
)

type SlowStartCause string

const (
	SlowStartCauseNew         SlowStartCause = "new"
	SlowStartCauseRecovery    SlowStartCause = "recovery"
	SlowStartCauseHardFailure SlowStartCause = "hard_failure"
)

type SlowStartKey struct {
	PoolID   int    `json:"pool_id"`
	MemberID int    `json:"member_id"`
	Model    string `json:"model"`
}

type SlowStartPolicy struct {
	MinimumFactor float64       `json:"minimum_factor"`
	RampDuration  time.Duration `json:"ramp_duration"`
	StateTTL      time.Duration `json:"state_ttl"`
	MaxEntries    int           `json:"max_entries"`
}

type SlowStartState struct {
	Key       SlowStartKey   `json:"key"`
	Cause     SlowStartCause `json:"cause"`
	Factor    float64        `json:"factor"`
	StartedAt time.Time      `json:"started_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type SlowStartStats struct {
	Entries   int   `json:"entries"`
	Evictions int64 `json:"evictions"`
	Completed int64 `json:"completed"`
}

type SlowStartTracker struct {
	mu        sync.Mutex
	policy    SlowStartPolicy
	clock     Clock
	startedAt time.Time
	states    map[SlowStartKey]*slowStartEntry
	evictions int64
	completed int64
}

type slowStartEntry struct {
	cause     SlowStartCause
	startedAt time.Time
	updatedAt time.Time
}

func NewSlowStartTracker(policy SlowStartPolicy, clock Clock) (*SlowStartTracker, error) {
	if math.IsNaN(policy.MinimumFactor) || math.IsInf(policy.MinimumFactor, 0) ||
		policy.MinimumFactor <= 0 || policy.MinimumFactor >= 1 || policy.RampDuration <= 0 ||
		policy.StateTTL < policy.RampDuration || policy.MaxEntries <= 0 {
		return nil, ErrSlowStartInvalidPolicy
	}
	if clock == nil {
		clock = wallClock{}
	}
	return &SlowStartTracker{
		policy:    policy,
		clock:     clock,
		startedAt: clock.Now(),
		states:    make(map[SlowStartKey]*slowStartEntry),
	}, nil
}

func (tracker *SlowStartTracker) StartNew(key SlowStartKey) (SlowStartState, error) {
	return tracker.start(key, SlowStartCauseNew)
}

func (tracker *SlowStartTracker) StartRecovery(key SlowStartKey) (SlowStartState, error) {
	return tracker.start(key, SlowStartCauseRecovery)
}

func (tracker *SlowStartTracker) MarkHardFailure(key SlowStartKey) (SlowStartState, error) {
	if !validSlowStartKey(key) {
		return SlowStartState{}, ErrSlowStartInvalidKey
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.clock.Now()
	tracker.pruneExpiredLocked(now)
	entry, ok := tracker.states[key]
	if !ok {
		tracker.makeRoomLocked()
		entry = &slowStartEntry{startedAt: now}
		tracker.states[key] = entry
	} else if entry.cause != SlowStartCauseHardFailure {
		entry.startedAt = now
	}
	entry.cause = SlowStartCauseHardFailure
	entry.updatedAt = now
	return tracker.snapshotLocked(key, entry, now), nil
}

func (tracker *SlowStartTracker) MarkHealthy(key SlowStartKey) (SlowStartState, error) {
	if !validSlowStartKey(key) {
		return SlowStartState{}, ErrSlowStartInvalidKey
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.clock.Now()
	entry, ok := tracker.states[key]
	if ok && tracker.expiredLocked(entry, now) {
		tracker.deleteEvictedLocked(key)
		entry = nil
		ok = false
	}
	if !ok {
		return SlowStartState{
			Key:       key,
			Factor:    tracker.coldFactorLocked(now),
			StartedAt: tracker.startedAt,
			UpdatedAt: now,
		}, nil
	}
	if entry.cause == SlowStartCauseHardFailure {
		entry.cause = SlowStartCauseRecovery
		entry.startedAt = now
		entry.updatedAt = now
		return tracker.snapshotLocked(key, entry, now), nil
	}
	factor := tracker.rampFactorLocked(entry.startedAt, now)
	if factor >= 1 {
		delete(tracker.states, key)
		tracker.completed++
		return SlowStartState{
			Key:       key,
			Cause:     entry.cause,
			Factor:    1,
			StartedAt: entry.startedAt,
			UpdatedAt: now,
		}, nil
	}
	entry.updatedAt = now
	return tracker.snapshotLocked(key, entry, now), nil
}

func (tracker *SlowStartTracker) Factor(key SlowStartKey) (float64, error) {
	if !validSlowStartKey(key) {
		return 0, ErrSlowStartInvalidKey
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.clock.Now()
	entry, ok := tracker.states[key]
	if ok && tracker.expiredLocked(entry, now) {
		tracker.deleteEvictedLocked(key)
		entry = nil
		ok = false
	}
	if !ok {
		return tracker.coldFactorLocked(now), nil
	}
	if entry.cause == SlowStartCauseHardFailure {
		entry.updatedAt = now
		return 0, nil
	}
	factor := tracker.rampFactorLocked(entry.startedAt, now)
	if factor >= 1 {
		delete(tracker.states, key)
		tracker.completed++
		return 1, nil
	}
	entry.updatedAt = now
	return factor, nil
}

func (tracker *SlowStartTracker) State(key SlowStartKey) (SlowStartState, bool) {
	if !validSlowStartKey(key) {
		return SlowStartState{}, false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.clock.Now()
	entry, ok := tracker.states[key]
	if !ok {
		return SlowStartState{}, false
	}
	if tracker.expiredLocked(entry, now) {
		tracker.deleteEvictedLocked(key)
		return SlowStartState{}, false
	}
	if entry.cause != SlowStartCauseHardFailure && tracker.rampFactorLocked(entry.startedAt, now) >= 1 {
		delete(tracker.states, key)
		tracker.completed++
		return SlowStartState{}, false
	}
	return tracker.snapshotLocked(key, entry, now), true
}

func (tracker *SlowStartTracker) Stats() SlowStartStats {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.pruneExpiredLocked(tracker.clock.Now())
	return SlowStartStats{
		Entries:   len(tracker.states),
		Evictions: tracker.evictions,
		Completed: tracker.completed,
	}
}

func (tracker *SlowStartTracker) start(key SlowStartKey, cause SlowStartCause) (SlowStartState, error) {
	if !validSlowStartKey(key) {
		return SlowStartState{}, ErrSlowStartInvalidKey
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.clock.Now()
	tracker.pruneExpiredLocked(now)
	if entry, ok := tracker.states[key]; ok {
		if entry.cause == SlowStartCauseHardFailure && cause == SlowStartCauseNew {
			entry.updatedAt = now
			return tracker.snapshotLocked(key, entry, now), nil
		}
		if entry.cause == cause {
			entry.updatedAt = now
			return tracker.snapshotLocked(key, entry, now), nil
		}
		entry.cause = cause
		entry.startedAt = now
		entry.updatedAt = now
		return tracker.snapshotLocked(key, entry, now), nil
	}
	tracker.makeRoomLocked()
	entry := &slowStartEntry{
		cause:     cause,
		startedAt: now,
		updatedAt: now,
	}
	tracker.states[key] = entry
	return tracker.snapshotLocked(key, entry, now), nil
}

func (tracker *SlowStartTracker) makeRoomLocked() {
	if len(tracker.states) < tracker.policy.MaxEntries {
		return
	}
	var victim SlowStartKey
	var victimUpdated time.Time
	found := false
	for key, entry := range tracker.states {
		if !found || entry.updatedAt.Before(victimUpdated) ||
			(entry.updatedAt.Equal(victimUpdated) && lessSlowStartKey(key, victim)) {
			victim = key
			victimUpdated = entry.updatedAt
			found = true
		}
	}
	if found {
		delete(tracker.states, victim)
		tracker.evictions++
	}
}

func (tracker *SlowStartTracker) pruneExpiredLocked(now time.Time) {
	for key, entry := range tracker.states {
		if tracker.expiredLocked(entry, now) {
			tracker.deleteEvictedLocked(key)
		}
	}
}

func (tracker *SlowStartTracker) expiredLocked(entry *slowStartEntry, now time.Time) bool {
	return !now.Before(entry.updatedAt.Add(tracker.policy.StateTTL))
}

func (tracker *SlowStartTracker) deleteEvictedLocked(key SlowStartKey) {
	delete(tracker.states, key)
	tracker.evictions++
}

func (tracker *SlowStartTracker) snapshotLocked(
	key SlowStartKey,
	entry *slowStartEntry,
	now time.Time,
) SlowStartState {
	factor := 0.0
	if entry.cause != SlowStartCauseHardFailure {
		factor = tracker.rampFactorLocked(entry.startedAt, now)
	}
	return SlowStartState{
		Key:       key,
		Cause:     entry.cause,
		Factor:    factor,
		StartedAt: entry.startedAt,
		UpdatedAt: entry.updatedAt,
	}
}

func (tracker *SlowStartTracker) coldFactorLocked(now time.Time) float64 {
	return tracker.rampFactorLocked(tracker.startedAt, now)
}

func (tracker *SlowStartTracker) rampFactorLocked(startedAt time.Time, now time.Time) float64 {
	elapsed := now.Sub(startedAt)
	if elapsed <= 0 {
		return tracker.policy.MinimumFactor
	}
	if elapsed >= tracker.policy.RampDuration {
		return 1
	}
	progress := float64(elapsed) / float64(tracker.policy.RampDuration)
	return tracker.policy.MinimumFactor + (1-tracker.policy.MinimumFactor)*progress
}

func validSlowStartKey(key SlowStartKey) bool {
	return key.PoolID > 0 && key.MemberID > 0 && validRoutingModel(key.Model)
}

func lessSlowStartKey(left SlowStartKey, right SlowStartKey) bool {
	if left.PoolID != right.PoolID {
		return left.PoolID < right.PoolID
	}
	if left.MemberID != right.MemberID {
		return left.MemberID < right.MemberID
	}
	return left.Model < right.Model
}
