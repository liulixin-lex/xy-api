package channelrouting

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AttemptStatePlanned         AttemptState = "planned"
	AttemptStateReserved        AttemptState = "reserved"
	AttemptStateSent            AttemptState = "sent"
	AttemptStateClientCommitted AttemptState = "client_committed"
	AttemptStateCompleted       AttemptState = "completed"

	defaultRetryBudgetMaxPools = 4_096
	defaultRetryBudgetEntryTTL = 30 * time.Minute
)

var (
	ErrAttemptCompleted          = errors.New("channel routing logical request is already completed")
	ErrAttemptClientCommitted    = errors.New("channel routing client commit boundary was crossed")
	ErrAttemptAlreadyInFlight    = errors.New("channel routing already has an in-flight attempt")
	ErrAttemptLimitExceeded      = errors.New("channel routing attempt limit exceeded")
	ErrAttemptDeadlineExceeded   = errors.New("channel routing failover deadline exceeded")
	ErrAttemptCostBudgetExceeded = errors.New("channel routing retry cost budget exhausted")
	ErrRetryTokenBudgetExhausted = errors.New("channel routing pool retry token budget exhausted")
	ErrAttemptInvalidPool        = errors.New("channel routing retry pool is invalid")
	ErrAttemptHedgingDisabled    = errors.New("channel routing hedging is disabled")
	defaultRetryTokenBudget      atomic.Pointer[RetryTokenBudget]
)

func init() {
	defaultRetryTokenBudget.Store(NewRetryTokenBudget(defaultRetryBudgetMaxPools, defaultRetryBudgetEntryTTL))
}

type AttemptState string

type AttemptPolicy struct {
	MaxAttempts          int
	Deadline             time.Time
	ExtraCostBudgetUnits int64
	RetryTokenCapacity   int
	RetryTokenRefill     float64
	RetryTokens          *RetryTokenBudget
	Now                  func() time.Time
}

type AttemptInput struct {
	PoolID             int
	EstimatedCostUnits int64
	Hedge              bool
}

type AttemptSnapshot struct {
	State              AttemptState
	AttemptsStarted    int
	RetryCostUsedUnits int64
	ClientCommitted    bool
	InFlight           bool
}

type AttemptCoordinator struct {
	mu sync.Mutex

	policy             AttemptPolicy
	state              AttemptState
	attemptsStarted    int
	retryCostUsedUnits int64
	clientCommitted    bool
	inFlight           bool
	nextLeaseID        uint64
}

type AttemptLease struct {
	coordinator *AttemptCoordinator
	id          uint64
	once        sync.Once
}

type RetryTokenBudgetStats struct {
	Pools       int
	Allowed     int64
	Denied      int64
	Evictions   int64
	MaxPools    int
	EntryTTLSec int64
}

type RetryTokenBudget struct {
	mu sync.Mutex

	maxPools  int
	entryTTL  time.Duration
	buckets   map[int]*retryTokenBucket
	allowed   int64
	denied    int64
	evictions int64
}

type retryTokenBucket struct {
	tokens   float64
	lastFill time.Time
	lastSeen time.Time
}

func NewAttemptCoordinator(policy AttemptPolicy) *AttemptCoordinator {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if policy.ExtraCostBudgetUnits < 0 {
		policy.ExtraCostBudgetUnits = 0
	}
	if policy.RetryTokenCapacity < 1 {
		policy.RetryTokenCapacity = 1
	}
	if policy.RetryTokenRefill <= 0 || math.IsNaN(policy.RetryTokenRefill) || math.IsInf(policy.RetryTokenRefill, 0) {
		policy.RetryTokenRefill = 1
	}
	if policy.RetryTokens == nil {
		policy.RetryTokens = defaultRetryTokenBudget.Load()
	}
	if policy.Now == nil {
		policy.Now = time.Now
	}
	return &AttemptCoordinator{policy: policy, state: AttemptStatePlanned}
}

func (coordinator *AttemptCoordinator) BeginAttempt(input AttemptInput) (*AttemptLease, error) {
	if coordinator == nil {
		return nil, ErrAttemptCompleted
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	if input.Hedge {
		return nil, ErrAttemptHedgingDisabled
	}
	if coordinator.state == AttemptStateCompleted {
		return nil, ErrAttemptCompleted
	}
	if coordinator.clientCommitted || coordinator.state == AttemptStateClientCommitted {
		return nil, ErrAttemptClientCommitted
	}
	if coordinator.inFlight {
		return nil, ErrAttemptAlreadyInFlight
	}
	if coordinator.attemptsStarted >= coordinator.policy.MaxAttempts {
		return nil, ErrAttemptLimitExceeded
	}
	now := coordinator.policy.Now()
	if !coordinator.policy.Deadline.IsZero() && !now.Before(coordinator.policy.Deadline) {
		return nil, ErrAttemptDeadlineExceeded
	}

	costUnits := normalizeAttemptCostUnits(input.EstimatedCostUnits)
	isRetry := coordinator.attemptsStarted > 0
	if isRetry {
		if input.PoolID <= 0 {
			return nil, ErrAttemptInvalidPool
		}
		if costUnits > coordinator.policy.ExtraCostBudgetUnits-coordinator.retryCostUsedUnits {
			return nil, ErrAttemptCostBudgetExceeded
		}
		if !coordinator.policy.RetryTokens.Allow(
			input.PoolID,
			now,
			coordinator.policy.RetryTokenCapacity,
			coordinator.policy.RetryTokenRefill,
		) {
			return nil, ErrRetryTokenBudgetExhausted
		}
		coordinator.retryCostUsedUnits += costUnits
	}

	coordinator.attemptsStarted++
	coordinator.inFlight = true
	coordinator.state = AttemptStateReserved
	coordinator.nextLeaseID++
	return &AttemptLease{coordinator: coordinator, id: coordinator.nextLeaseID}, nil
}

func (coordinator *AttemptCoordinator) Snapshot() AttemptSnapshot {
	if coordinator == nil {
		return AttemptSnapshot{State: AttemptStateCompleted}
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return AttemptSnapshot{
		State:              coordinator.state,
		AttemptsStarted:    coordinator.attemptsStarted,
		RetryCostUsedUnits: coordinator.retryCostUsedUnits,
		ClientCommitted:    coordinator.clientCommitted,
		InFlight:           coordinator.inFlight,
	}
}

func (coordinator *AttemptCoordinator) Complete() {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	coordinator.inFlight = false
	coordinator.state = AttemptStateCompleted
	coordinator.mu.Unlock()
}

func (lease *AttemptLease) MarkSent() error {
	return lease.transition(func(coordinator *AttemptCoordinator) error {
		if coordinator.state != AttemptStateReserved {
			return ErrAttemptCompleted
		}
		coordinator.state = AttemptStateSent
		return nil
	})
}

func (lease *AttemptLease) MarkClientCommitted() error {
	return lease.transition(func(coordinator *AttemptCoordinator) error {
		if coordinator.state != AttemptStateReserved && coordinator.state != AttemptStateSent {
			return ErrAttemptCompleted
		}
		coordinator.clientCommitted = true
		coordinator.state = AttemptStateClientCommitted
		return nil
	})
}

func (lease *AttemptLease) Finish() {
	if lease == nil || lease.coordinator == nil {
		return
	}
	lease.once.Do(func() {
		coordinator := lease.coordinator
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if !coordinator.inFlight || lease.id != coordinator.nextLeaseID {
			return
		}
		coordinator.inFlight = false
		if coordinator.clientCommitted {
			coordinator.state = AttemptStateClientCommitted
			return
		}
		coordinator.state = AttemptStatePlanned
	})
}

func (lease *AttemptLease) transition(update func(*AttemptCoordinator) error) error {
	if lease == nil || lease.coordinator == nil {
		return ErrAttemptCompleted
	}
	coordinator := lease.coordinator
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.inFlight || lease.id != coordinator.nextLeaseID {
		return ErrAttemptCompleted
	}
	return update(coordinator)
}

func NewRetryTokenBudget(maxPools int, entryTTL time.Duration) *RetryTokenBudget {
	if maxPools < 1 {
		maxPools = 1
	}
	if entryTTL <= 0 {
		entryTTL = time.Minute
	}
	return &RetryTokenBudget{
		maxPools: maxPools,
		entryTTL: entryTTL,
		buckets:  make(map[int]*retryTokenBucket),
	}
}

func (budget *RetryTokenBudget) Allow(poolID int, now time.Time, capacity int, refillPerSecond float64) bool {
	if budget == nil || poolID <= 0 || capacity < 1 || refillPerSecond <= 0 ||
		math.IsNaN(refillPerSecond) || math.IsInf(refillPerSecond, 0) {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()

	budget.pruneLocked(now)
	bucket, exists := budget.buckets[poolID]
	if !exists {
		if len(budget.buckets) >= budget.maxPools {
			budget.evictOldestLocked()
		}
		bucket = &retryTokenBucket{tokens: float64(capacity), lastFill: now, lastSeen: now}
		budget.buckets[poolID] = bucket
	}
	if now.After(bucket.lastFill) {
		elapsed := now.Sub(bucket.lastFill).Seconds()
		bucket.tokens = math.Min(float64(capacity), bucket.tokens+elapsed*refillPerSecond)
		bucket.lastFill = now
	} else if bucket.tokens > float64(capacity) {
		bucket.tokens = float64(capacity)
	}
	bucket.lastSeen = now
	if bucket.tokens < 1 {
		budget.denied++
		return false
	}
	bucket.tokens--
	budget.allowed++
	return true
}

func (budget *RetryTokenBudget) Stats() RetryTokenBudgetStats {
	if budget == nil {
		return RetryTokenBudgetStats{}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return RetryTokenBudgetStats{
		Pools:       len(budget.buckets),
		Allowed:     budget.allowed,
		Denied:      budget.denied,
		Evictions:   budget.evictions,
		MaxPools:    budget.maxPools,
		EntryTTLSec: int64(budget.entryTTL / time.Second),
	}
}

func DefaultRetryTokenBudgetStats() RetryTokenBudgetStats {
	return defaultRetryTokenBudget.Load().Stats()
}

func ResetDefaultRetryTokenBudgetForTest(maxPools int, entryTTL time.Duration) {
	defaultRetryTokenBudget.Store(NewRetryTokenBudget(maxPools, entryTTL))
}

func AttemptDeadline(ctx context.Context, now time.Time, maximum time.Duration) time.Time {
	deadline := time.Time{}
	if maximum > 0 {
		deadline = now.Add(maximum)
	}
	if ctx == nil {
		return deadline
	}
	requestDeadline, ok := ctx.Deadline()
	if !ok || (!deadline.IsZero() && !requestDeadline.Before(deadline)) {
		return deadline
	}
	return requestDeadline
}

func AttemptExtraCostBudget(baselineCostUnits int64, multiplier float64) int64 {
	baselineCostUnits = normalizeAttemptCostUnits(baselineCostUnits)
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return 0
	}
	if multiplier >= float64(math.MaxInt64)/float64(baselineCostUnits) {
		return math.MaxInt64
	}
	value := float64(baselineCostUnits) * multiplier
	if value >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(value)
}

func normalizeAttemptCostUnits(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}

func (budget *RetryTokenBudget) pruneLocked(now time.Time) {
	cutoff := now.Add(-budget.entryTTL)
	for poolID, bucket := range budget.buckets {
		if bucket.lastSeen.Before(cutoff) {
			delete(budget.buckets, poolID)
			budget.evictions++
		}
	}
}

func (budget *RetryTokenBudget) evictOldestLocked() {
	oldestPoolID := 0
	oldest := time.Time{}
	for poolID, bucket := range budget.buckets {
		if oldestPoolID == 0 || bucket.lastSeen.Before(oldest) ||
			(bucket.lastSeen.Equal(oldest) && poolID < oldestPoolID) {
			oldestPoolID = poolID
			oldest = bucket.lastSeen
		}
	}
	if oldestPoolID > 0 {
		delete(budget.buckets, oldestPoolID)
		budget.evictions++
	}
}
