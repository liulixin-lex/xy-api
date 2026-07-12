package channelrouting

import (
	"errors"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	CapacityModeLocalSoft CapacityMode = "local_soft"
	capacityModelMaxBytes              = 128
	capacityMaxShards                  = 256
	capacityMaxInt64      int64        = 1<<63 - 1
	capacityRateWindow                 = time.Minute
)

var (
	ErrCapacityInvalidConfig         = errors.New("invalid local soft capacity configuration")
	ErrCapacityInvalidInput          = errors.New("invalid local soft capacity input")
	ErrCapacityExhausted             = errors.New("local soft capacity exhausted")
	ErrCapacityOverflow              = errors.New("local soft capacity arithmetic overflow")
	ErrCapacityEntriesFull           = errors.New("local soft capacity entry limit reached")
	ErrCapacityLimitConflict         = errors.New("local soft capacity limit changed while reservations are active")
	ErrCapacityReservationTransition = errors.New("invalid local soft capacity reservation transition")
	ErrCapacityReservationLost       = errors.New("local soft capacity reservation state is unavailable")
)

type CapacityMode string

// Clock keeps capacity and slow-start state deterministic in tests and replay.
type Clock interface {
	Now() time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

type CapacityKey struct {
	PoolID   int    `json:"pool_id"`
	MemberID int    `json:"member_id"`
	Model    string `json:"model"`
}

type Demand struct {
	RPM       int64 `json:"rpm"`
	InputTPM  int64 `json:"input_tpm"`
	OutputTPM int64 `json:"output_tpm"`
	Inflight  int64 `json:"inflight"`
}

type Limit struct {
	RPM       int64 `json:"rpm"`
	InputTPM  int64 `json:"input_tpm"`
	OutputTPM int64 `json:"output_tpm"`
	Inflight  int64 `json:"inflight"`
}

type CapacityAdmission struct {
	Mode   CapacityMode `json:"mode"`
	Key    CapacityKey  `json:"key"`
	Demand Demand       `json:"demand"`
	Limit  Limit        `json:"limit"`
}

type CapacityConfig struct {
	MaxEntries int
	IdleTTL    time.Duration
	Shards     int
	Clock      Clock
}

type CapacityStats struct {
	Mode      CapacityMode `json:"mode"`
	Entries   int          `json:"entries"`
	Pending   int64        `json:"pending"`
	Committed int64        `json:"committed"`
	Evictions int64        `json:"evictions"`
	Drops     int64        `json:"drops"`
}

type CapacitySnapshot struct {
	Mode                  CapacityMode `json:"mode"`
	Key                   CapacityKey  `json:"key"`
	Limit                 Limit        `json:"limit"`
	Pending               Demand       `json:"pending"`
	Committed             Demand       `json:"committed"`
	PendingReservations   int64        `json:"pending_reservations"`
	CommittedReservations int64        `json:"committed_reservations"`
	UpdatedAt             time.Time    `json:"updated_at"`
}

type CapacityTracker struct {
	config CapacityConfig
	shards []capacityShard

	admissionMu sync.Mutex
	entries     atomic.Int64
	pending     atomic.Int64
	committed   atomic.Int64
	evictions   atomic.Int64
	drops       atomic.Int64
}

type capacityShard struct {
	mu      sync.Mutex
	entries map[CapacityKey]*capacityEntry
}

type capacityEntry struct {
	limit                 Limit
	pending               Demand
	committed             Demand
	rateRemainder         capacityRateRemainder
	rateUpdatedAt         time.Time
	pendingReservations   int64
	committedReservations int64
	updatedAt             time.Time
}

type capacityRateRemainder struct {
	rpm       uint64
	inputTPM  uint64
	outputTPM uint64
}

type capacityReservationState uint8

const (
	capacityReservationPending capacityReservationState = iota
	capacityReservationCommitted
	capacityReservationCanceled
	capacityReservationReleased
)

type Reservation struct {
	Mode   CapacityMode `json:"mode"`
	Key    CapacityKey  `json:"key"`
	Demand Demand       `json:"demand"`

	tracker *CapacityTracker
	key     CapacityKey
	demand  Demand
	limit   Limit
	mu      sync.Mutex
	state   capacityReservationState
}

func (reservation *Reservation) Admission() CapacityAdmission {
	if reservation == nil {
		return CapacityAdmission{}
	}
	return CapacityAdmission{
		Mode: CapacityModeLocalSoft, Key: reservation.key, Demand: reservation.demand, Limit: reservation.limit,
	}
}

func NewCapacityTracker(config CapacityConfig) (*CapacityTracker, error) {
	if config.MaxEntries <= 0 || config.IdleTTL <= 0 || config.Shards <= 0 || config.Shards > capacityMaxShards {
		return nil, ErrCapacityInvalidConfig
	}
	if config.Clock == nil {
		config.Clock = wallClock{}
	}
	tracker := &CapacityTracker{
		config: config,
		shards: make([]capacityShard, config.Shards),
	}
	for index := range tracker.shards {
		tracker.shards[index].entries = make(map[CapacityKey]*capacityEntry)
	}
	return tracker, nil
}

func (tracker *CapacityTracker) TryReserve(key CapacityKey, demand Demand, limit Limit) (*Reservation, error) {
	if !validCapacityKey(key) || !validDemand(demand) || !validLimit(limit) || !limitCoversDemand(limit, demand) {
		return nil, ErrCapacityInvalidInput
	}
	if exceedsLimit(demand, limit) {
		tracker.drops.Add(1)
		return nil, ErrCapacityExhausted
	}
	now := tracker.config.Clock.Now()
	shard := tracker.shardFor(key)

	shard.mu.Lock()
	entry, found := shard.entries[key]
	if found {
		tracker.applyRateDecayLocked(entry, now)
	}
	if found && !tracker.expiredIdle(entry, now) {
		reservation, err := tracker.reserveLocked(entry, key, demand, limit, now)
		shard.mu.Unlock()
		return reservation, err
	}
	shard.mu.Unlock()

	return tracker.reserveWithAdmission(key, demand, limit, now)
}

func (tracker *CapacityTracker) Stats() CapacityStats {
	tracker.pruneExpired(tracker.config.Clock.Now())
	return CapacityStats{
		Mode:      CapacityModeLocalSoft,
		Entries:   int(tracker.entries.Load()),
		Pending:   tracker.pending.Load(),
		Committed: tracker.committed.Load(),
		Evictions: tracker.evictions.Load(),
		Drops:     tracker.drops.Load(),
	}
}

func (tracker *CapacityTracker) Has(key CapacityKey) bool {
	_, ok := tracker.Snapshot(key)
	return ok
}

func (tracker *CapacityTracker) Snapshot(key CapacityKey) (CapacitySnapshot, bool) {
	if !validCapacityKey(key) {
		return CapacitySnapshot{}, false
	}
	tracker.admissionMu.Lock()
	defer tracker.admissionMu.Unlock()

	now := tracker.config.Clock.Now()
	shard := tracker.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.entries[key]
	if !ok {
		return CapacitySnapshot{}, false
	}
	tracker.applyRateDecayLocked(entry, now)
	if tracker.expiredIdle(entry, now) {
		tracker.evictLocked(shard, key)
		return CapacitySnapshot{}, false
	}
	return CapacitySnapshot{
		Mode:                  CapacityModeLocalSoft,
		Key:                   key,
		Limit:                 entry.limit,
		Pending:               entry.pending,
		Committed:             entry.committed,
		PendingReservations:   entry.pendingReservations,
		CommittedReservations: entry.committedReservations,
		UpdatedAt:             entry.updatedAt,
	}, true
}

func (reservation *Reservation) Commit() error {
	if reservation == nil || reservation.tracker == nil {
		return ErrCapacityReservationTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	switch reservation.state {
	case capacityReservationCommitted:
		return nil
	case capacityReservationPending:
		if err := reservation.tracker.commit(reservation.key, reservation.demand); err != nil {
			return err
		}
		reservation.state = capacityReservationCommitted
		return nil
	default:
		return ErrCapacityReservationTransition
	}
}

func (reservation *Reservation) Cancel() error {
	if reservation == nil || reservation.tracker == nil {
		return ErrCapacityReservationTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	switch reservation.state {
	case capacityReservationCanceled:
		return nil
	case capacityReservationPending:
		if err := reservation.tracker.cancel(reservation.key, reservation.demand); err != nil {
			return err
		}
		reservation.state = capacityReservationCanceled
		return nil
	default:
		return ErrCapacityReservationTransition
	}
}

func (reservation *Reservation) Release() error {
	if reservation == nil || reservation.tracker == nil {
		return ErrCapacityReservationTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	switch reservation.state {
	case capacityReservationReleased:
		return nil
	case capacityReservationCommitted:
		if err := reservation.tracker.release(reservation.key, reservation.demand); err != nil {
			return err
		}
		reservation.state = capacityReservationReleased
		return nil
	default:
		return ErrCapacityReservationTransition
	}
}

func (tracker *CapacityTracker) reserveWithAdmission(
	key CapacityKey,
	demand Demand,
	limit Limit,
	now time.Time,
) (*Reservation, error) {
	tracker.admissionMu.Lock()
	defer tracker.admissionMu.Unlock()

	tracker.pruneExpiredLocked(now)
	shard := tracker.shardFor(key)
	shard.mu.Lock()
	if entry, ok := shard.entries[key]; ok {
		reservation, err := tracker.reserveLocked(entry, key, demand, limit, now)
		shard.mu.Unlock()
		return reservation, err
	}
	shard.mu.Unlock()

	if tracker.entries.Load() >= int64(tracker.config.MaxEntries) && !tracker.evictOldestIdleLocked() {
		tracker.drops.Add(1)
		return nil, ErrCapacityEntriesFull
	}

	shard.mu.Lock()
	entry := &capacityEntry{limit: limit, rateUpdatedAt: now, updatedAt: now}
	shard.entries[key] = entry
	tracker.entries.Add(1)
	reservation, err := tracker.reserveLocked(entry, key, demand, limit, now)
	if err != nil {
		delete(shard.entries, key)
		tracker.entries.Add(-1)
	}
	shard.mu.Unlock()
	return reservation, err
}

func (tracker *CapacityTracker) reserveLocked(
	entry *capacityEntry,
	key CapacityKey,
	demand Demand,
	limit Limit,
	now time.Time,
) (*Reservation, error) {
	tracker.applyRateDecayLocked(entry, now)
	active := !entry.idle()
	if active && entry.limit != limit {
		tracker.drops.Add(1)
		return nil, ErrCapacityLimitConflict
	}
	if !active {
		entry.limit = limit
	}

	used, ok := addDemand(entry.pending, entry.committed)
	if !ok {
		tracker.drops.Add(1)
		return nil, ErrCapacityOverflow
	}
	next, ok := addDemand(used, demand)
	if !ok || entry.pendingReservations == capacityMaxInt64 {
		tracker.drops.Add(1)
		return nil, ErrCapacityOverflow
	}
	if exceedsLimit(next, limit) {
		tracker.drops.Add(1)
		return nil, ErrCapacityExhausted
	}

	entry.pending, ok = addDemand(entry.pending, demand)
	if !ok {
		tracker.drops.Add(1)
		return nil, ErrCapacityOverflow
	}
	entry.pendingReservations++
	entry.updatedAt = now
	tracker.pending.Add(1)
	return &Reservation{
		Mode:    CapacityModeLocalSoft,
		Key:     key,
		Demand:  demand,
		tracker: tracker,
		key:     key,
		demand:  demand,
		limit:   limit,
		state:   capacityReservationPending,
	}, nil
}

func (tracker *CapacityTracker) commit(key CapacityKey, demand Demand) error {
	shard := tracker.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.entries[key]
	if !ok || entry.pendingReservations <= 0 {
		return ErrCapacityReservationLost
	}
	now := tracker.config.Clock.Now()
	tracker.applyRateDecayLocked(entry, now)
	pending, ok := subtractDemand(entry.pending, demand)
	if !ok || entry.committedReservations == capacityMaxInt64 {
		return ErrCapacityReservationLost
	}
	committed, ok := addDemand(entry.committed, demand)
	if !ok {
		return ErrCapacityReservationLost
	}
	entry.pending = pending
	entry.committed = committed
	entry.pendingReservations--
	entry.committedReservations++
	entry.updatedAt = now
	tracker.pending.Add(-1)
	tracker.committed.Add(1)
	return nil
}

func (tracker *CapacityTracker) cancel(key CapacityKey, demand Demand) error {
	shard := tracker.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.entries[key]
	if !ok || entry.pendingReservations <= 0 {
		return ErrCapacityReservationLost
	}
	now := tracker.config.Clock.Now()
	tracker.applyRateDecayLocked(entry, now)
	pending, ok := subtractDemand(entry.pending, demand)
	if !ok {
		return ErrCapacityReservationLost
	}
	entry.pending = pending
	entry.pendingReservations--
	entry.updatedAt = now
	tracker.pending.Add(-1)
	return nil
}

func (tracker *CapacityTracker) release(key CapacityKey, demand Demand) error {
	shard := tracker.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.entries[key]
	if !ok || entry.committedReservations <= 0 {
		return ErrCapacityReservationLost
	}
	now := tracker.config.Clock.Now()
	tracker.applyRateDecayLocked(entry, now)
	if entry.committed.Inflight < demand.Inflight {
		return ErrCapacityReservationLost
	}
	entry.committed.Inflight -= demand.Inflight
	entry.committedReservations--
	entry.updatedAt = now
	tracker.committed.Add(-1)
	return nil
}

func (tracker *CapacityTracker) pruneExpired(now time.Time) {
	tracker.admissionMu.Lock()
	tracker.pruneExpiredLocked(now)
	tracker.admissionMu.Unlock()
}

func (tracker *CapacityTracker) pruneExpiredLocked(now time.Time) {
	for index := range tracker.shards {
		shard := &tracker.shards[index]
		shard.mu.Lock()
		for key, entry := range shard.entries {
			tracker.applyRateDecayLocked(entry, now)
			if tracker.expiredIdle(entry, now) {
				tracker.evictLocked(shard, key)
			}
		}
		shard.mu.Unlock()
	}
}

func (tracker *CapacityTracker) evictOldestIdleLocked() bool {
	for index := range tracker.shards {
		tracker.shards[index].mu.Lock()
	}
	defer func() {
		for index := len(tracker.shards) - 1; index >= 0; index-- {
			tracker.shards[index].mu.Unlock()
		}
	}()

	var victim CapacityKey
	var victimUpdated time.Time
	found := false
	for index := range tracker.shards {
		shard := &tracker.shards[index]
		for key, entry := range shard.entries {
			if !entry.idle() {
				continue
			}
			if !found || entry.updatedAt.Before(victimUpdated) ||
				(entry.updatedAt.Equal(victimUpdated) && lessCapacityKey(key, victim)) {
				victim = key
				victimUpdated = entry.updatedAt
				found = true
			}
		}
	}
	if !found {
		return false
	}
	shard := tracker.shardFor(victim)
	tracker.evictLocked(shard, victim)
	return true
}

func (tracker *CapacityTracker) evictLocked(shard *capacityShard, key CapacityKey) {
	delete(shard.entries, key)
	tracker.entries.Add(-1)
	tracker.evictions.Add(1)
}

func (tracker *CapacityTracker) expiredIdle(entry *capacityEntry, now time.Time) bool {
	return entry.idle() && !now.Before(entry.updatedAt.Add(tracker.config.IdleTTL))
}

func (entry *capacityEntry) idle() bool {
	return entry.pendingReservations == 0 && entry.committedReservations == 0 &&
		entry.pending == (Demand{}) && entry.committed == (Demand{})
}

func (tracker *CapacityTracker) applyRateDecayLocked(entry *capacityEntry, now time.Time) {
	if entry.rateUpdatedAt.IsZero() {
		entry.rateUpdatedAt = now
		return
	}
	elapsed := now.Sub(entry.rateUpdatedAt)
	if elapsed <= 0 {
		return
	}
	hadRateDebt := entry.committed.RPM != 0 || entry.committed.InputTPM != 0 || entry.committed.OutputTPM != 0
	entry.committed.RPM, entry.rateRemainder.rpm = decayCapacityDebt(
		entry.committed.RPM,
		entry.limit.RPM,
		entry.rateRemainder.rpm,
		elapsed,
	)
	entry.committed.InputTPM, entry.rateRemainder.inputTPM = decayCapacityDebt(
		entry.committed.InputTPM,
		entry.limit.InputTPM,
		entry.rateRemainder.inputTPM,
		elapsed,
	)
	entry.committed.OutputTPM, entry.rateRemainder.outputTPM = decayCapacityDebt(
		entry.committed.OutputTPM,
		entry.limit.OutputTPM,
		entry.rateRemainder.outputTPM,
		elapsed,
	)
	entry.rateUpdatedAt = now
	if hadRateDebt && entry.committed.RPM == 0 && entry.committed.InputTPM == 0 && entry.committed.OutputTPM == 0 {
		entry.updatedAt = now
	}
}

func (tracker *CapacityTracker) shardFor(key CapacityKey) *capacityShard {
	hash := uint64(1469598103934665603)
	mix := func(value uint64) {
		for index := 0; index < 8; index++ {
			hash ^= value & 0xff
			hash *= 1099511628211
			value >>= 8
		}
	}
	mix(uint64(key.PoolID))
	mix(uint64(key.MemberID))
	for index := 0; index < len(key.Model); index++ {
		hash ^= uint64(key.Model[index])
		hash *= 1099511628211
	}
	return &tracker.shards[hash%uint64(len(tracker.shards))]
}

func validCapacityKey(key CapacityKey) bool {
	return key.PoolID > 0 && key.MemberID > 0 && validRoutingModel(key.Model)
}

func validRoutingModel(model string) bool {
	if model == "" || len(model) > capacityModelMaxBytes || !utf8.ValidString(model) {
		return false
	}
	if model[0] == ' ' || model[len(model)-1] == ' ' || model[0] == '\t' || model[len(model)-1] == '\t' ||
		model[0] == '\n' || model[len(model)-1] == '\n' || model[0] == '\r' || model[len(model)-1] == '\r' {
		return false
	}
	return true
}

func validDemand(demand Demand) bool {
	if demand.RPM < 0 || demand.InputTPM < 0 || demand.OutputTPM < 0 || demand.Inflight < 0 {
		return false
	}
	return demand != (Demand{})
}

func validLimit(limit Limit) bool {
	if limit.RPM < 0 || limit.InputTPM < 0 || limit.OutputTPM < 0 || limit.Inflight < 0 {
		return false
	}
	return limit != (Limit{})
}

func limitCoversDemand(limit Limit, demand Demand) bool {
	return (demand.RPM == 0 || limit.RPM > 0) &&
		(demand.InputTPM == 0 || limit.InputTPM > 0) &&
		(demand.OutputTPM == 0 || limit.OutputTPM > 0) &&
		(demand.Inflight == 0 || limit.Inflight > 0)
}

func addDemand(left Demand, right Demand) (Demand, bool) {
	if left.RPM > capacityMaxInt64-right.RPM ||
		left.InputTPM > capacityMaxInt64-right.InputTPM ||
		left.OutputTPM > capacityMaxInt64-right.OutputTPM ||
		left.Inflight > capacityMaxInt64-right.Inflight {
		return Demand{}, false
	}
	return Demand{
		RPM:       left.RPM + right.RPM,
		InputTPM:  left.InputTPM + right.InputTPM,
		OutputTPM: left.OutputTPM + right.OutputTPM,
		Inflight:  left.Inflight + right.Inflight,
	}, true
}

func subtractDemand(left Demand, right Demand) (Demand, bool) {
	if left.RPM < right.RPM || left.InputTPM < right.InputTPM ||
		left.OutputTPM < right.OutputTPM || left.Inflight < right.Inflight {
		return Demand{}, false
	}
	return Demand{
		RPM:       left.RPM - right.RPM,
		InputTPM:  left.InputTPM - right.InputTPM,
		OutputTPM: left.OutputTPM - right.OutputTPM,
		Inflight:  left.Inflight - right.Inflight,
	}, true
}

func exceedsLimit(demand Demand, limit Limit) bool {
	return (limit.RPM > 0 && demand.RPM > limit.RPM) ||
		(limit.InputTPM > 0 && demand.InputTPM > limit.InputTPM) ||
		(limit.OutputTPM > 0 && demand.OutputTPM > limit.OutputTPM) ||
		(limit.Inflight > 0 && demand.Inflight > limit.Inflight)
}

func decayCapacityDebt(debt int64, limit int64, remainder uint64, elapsed time.Duration) (int64, uint64) {
	if debt <= 0 {
		return 0, 0
	}
	if limit <= 0 || elapsed <= 0 {
		return debt, remainder
	}
	if elapsed >= capacityRateWindow {
		return 0, 0
	}
	high, low := bits.Mul64(uint64(limit), uint64(elapsed))
	var carry uint64
	low, carry = bits.Add64(low, remainder, 0)
	high, carry = bits.Add64(high, 0, carry)
	if carry != 0 {
		return 0, 0
	}
	decay, nextRemainder := bits.Div64(high, low, uint64(capacityRateWindow))
	if decay >= uint64(debt) {
		return 0, 0
	}
	return debt - int64(decay), nextRemainder
}

func lessCapacityKey(left CapacityKey, right CapacityKey) bool {
	if left.PoolID != right.PoolID {
		return left.PoolID < right.PoolID
	}
	if left.MemberID != right.MemberID {
		return left.MemberID < right.MemberID
	}
	return left.Model < right.Model
}
