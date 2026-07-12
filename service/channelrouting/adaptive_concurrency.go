package channelrouting

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	AdaptiveConcurrencyScopeMemberModel    = "member_model"
	AdaptiveConcurrencyScopeSharedResource = "shared_resource"

	adaptiveConcurrencyMaximumShards = 256
	adaptiveConcurrencyModelMaxBytes = 128
)

var (
	ErrAdaptiveConcurrencyInvalid   = errors.New("invalid adaptive concurrency input")
	ErrAdaptiveConcurrencyExhausted = errors.New("adaptive concurrency exhausted")
	ErrAdaptiveConcurrencyConflict  = errors.New("adaptive concurrency policy conflict")
	ErrAdaptiveConcurrencyStateFull = errors.New("adaptive concurrency state capacity reached")
	ErrAdaptiveConcurrencyLost      = errors.New("adaptive concurrency lease state is unavailable")
)

type AdaptiveConcurrencyKey struct {
	PolicyRevision uint64 `json:"policy_revision"`
	Scope          string `json:"scope"`
	PoolID         int    `json:"pool_id,omitempty"`
	MemberID       int    `json:"member_id,omitempty"`
	ChannelID      int    `json:"channel_id,omitempty"`
	AccountID      int    `json:"account_id,omitempty"`
	CredentialID   int    `json:"credential_id,omitempty"`
	Model          string `json:"model"`
}

type AdaptiveConcurrencyPolicy struct {
	MinLimit             int64         `json:"min_limit"`
	InitialLimit         int64         `json:"initial_limit"`
	MaxLimit             int64         `json:"max_limit"`
	IncreaseStep         int64         `json:"increase_step"`
	HealthySamples       int           `json:"healthy_samples"`
	IncreaseInterval     time.Duration `json:"increase_interval"`
	DecreaseInterval     time.Duration `json:"decrease_interval"`
	DecreaseBasisPoints  int           `json:"decrease_basis_points"`
	TTFTSpikeBasisPoints int           `json:"ttft_spike_basis_points"`
	TTFTSpikeFloor       time.Duration `json:"ttft_spike_floor"`
	TTFTMinimumSamples   int64         `json:"ttft_minimum_samples"`
}

type AdaptiveConcurrencyTarget struct {
	Key    AdaptiveConcurrencyKey    `json:"key"`
	Policy AdaptiveConcurrencyPolicy `json:"policy"`
}

type AdaptiveConcurrencySignal struct {
	Success          bool          `json:"success"`
	StatusCode       int           `json:"status_code"`
	TTFT             time.Duration `json:"ttft"`
	FirstByteTimeout bool          `json:"first_byte_timeout"`
	Probe            bool          `json:"probe"`
	ObservedAt       time.Time     `json:"observed_at"`
}

type AdaptiveConcurrencySnapshot struct {
	Key            AdaptiveConcurrencyKey `json:"key"`
	Limit          int64                  `json:"limit"`
	Inflight       int64                  `json:"inflight"`
	HealthyStreak  int                    `json:"healthy_streak"`
	TTFTSamples    int64                  `json:"ttft_samples"`
	BaselineTTFTMs float64                `json:"baseline_ttft_ms"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type AdaptiveConcurrencyStats struct {
	Entries    int   `json:"entries"`
	Allowed    int64 `json:"allowed"`
	Denied     int64 `json:"denied"`
	Increases  int64 `json:"increases"`
	Decreases  int64 `json:"decreases"`
	ProbeDrops int64 `json:"probe_drops"`
	Evictions  int64 `json:"evictions"`
}

type AdaptiveConcurrencyConfig struct {
	MaxEntries int
	IdleTTL    time.Duration
	Shards     int
	Clock      Clock
}

type AdaptiveConcurrencyController struct {
	config AdaptiveConcurrencyConfig
	shards []adaptiveConcurrencyShard

	admissionMu sync.Mutex
	entries     atomic.Int64
	allowed     atomic.Int64
	denied      atomic.Int64
	increases   atomic.Int64
	decreases   atomic.Int64
	probeDrops  atomic.Int64
	evictions   atomic.Int64
}

type adaptiveConcurrencyShard struct {
	mu      sync.Mutex
	entries map[AdaptiveConcurrencyKey]*adaptiveConcurrencyState
}

type adaptiveConcurrencyState struct {
	policy          AdaptiveConcurrencyPolicy
	appliedRevision uint64
	limit           int64
	inflight        int64
	healthyStreak   int
	ttftSamples     int64
	baselineTTFTMs  float64
	lastIncreaseAt  time.Time
	lastDecreaseAt  time.Time
	updatedAt       time.Time
}

type AdaptiveConcurrencyLease struct {
	controller *AdaptiveConcurrencyController
	target     AdaptiveConcurrencyTarget
	stateKey   AdaptiveConcurrencyKey

	mu       sync.Mutex
	released bool
}

type AdaptiveConcurrencyLeaseSet struct {
	leases  []*AdaptiveConcurrencyLease
	targets []AdaptiveConcurrencyTarget

	mu       sync.Mutex
	released bool
}

func DefaultAdaptiveConcurrencyPolicy(maxLimit int64) AdaptiveConcurrencyPolicy {
	initial := min(maxLimit, int64(4))
	if initial < 1 {
		initial = 1
	}
	return AdaptiveConcurrencyPolicy{
		MinLimit: 1, InitialLimit: initial, MaxLimit: maxLimit,
		IncreaseStep: 1, HealthySamples: 8,
		IncreaseInterval: time.Second, DecreaseInterval: 250 * time.Millisecond,
		DecreaseBasisPoints: 7_000, TTFTSpikeBasisPoints: 15_000,
		TTFTSpikeFloor: 50 * time.Millisecond, TTFTMinimumSamples: 4,
	}
}

func NewAdaptiveConcurrencyController(config AdaptiveConcurrencyConfig) (*AdaptiveConcurrencyController, error) {
	if config.MaxEntries <= 0 || config.IdleTTL <= 0 || config.Shards <= 0 ||
		config.Shards > adaptiveConcurrencyMaximumShards {
		return nil, ErrAdaptiveConcurrencyInvalid
	}
	if config.Clock == nil {
		config.Clock = wallClock{}
	}
	controller := &AdaptiveConcurrencyController{
		config: config,
		shards: make([]adaptiveConcurrencyShard, config.Shards),
	}
	for index := range controller.shards {
		controller.shards[index].entries = make(map[AdaptiveConcurrencyKey]*adaptiveConcurrencyState)
	}
	return controller, nil
}

func (controller *AdaptiveConcurrencyController) TryAcquire(
	targets []AdaptiveConcurrencyTarget,
) (*AdaptiveConcurrencyLeaseSet, error) {
	if controller == nil || len(targets) == 0 {
		return nil, ErrAdaptiveConcurrencyInvalid
	}
	normalized, err := normalizeAdaptiveConcurrencyTargets(targets)
	if err != nil {
		return nil, err
	}
	leases := make([]*AdaptiveConcurrencyLease, 0, len(normalized))
	for _, target := range normalized {
		lease, acquireErr := controller.acquire(target)
		if acquireErr != nil {
			for index := len(leases) - 1; index >= 0; index-- {
				_ = leases[index].Release()
			}
			return nil, acquireErr
		}
		leases = append(leases, lease)
	}
	return &AdaptiveConcurrencyLeaseSet{leases: leases, targets: normalized}, nil
}

func (controller *AdaptiveConcurrencyController) Observe(
	targets []AdaptiveConcurrencyTarget,
	signal AdaptiveConcurrencySignal,
) []AdaptiveConcurrencySnapshot {
	if controller == nil || len(targets) == 0 {
		return nil
	}
	normalized, err := normalizeAdaptiveConcurrencyTargets(targets)
	if err != nil {
		return nil
	}
	if signal.Probe {
		controller.probeDrops.Add(1)
		return controller.Snapshots(normalized)
	}
	now := signal.ObservedAt
	if now.IsZero() {
		now = controller.config.Clock.Now()
	}
	result := make([]AdaptiveConcurrencySnapshot, 0, len(normalized))
	for _, target := range normalized {
		stateKey := adaptiveConcurrencyStateKey(target.Key)
		shard := controller.shardFor(stateKey)
		shard.mu.Lock()
		state := shard.entries[stateKey]
		if state == nil || state.policy != target.Policy {
			shard.mu.Unlock()
			continue
		}
		controller.observeLocked(state, target.Key.Scope, signal, now)
		result = append(result, adaptiveConcurrencySnapshot(target.Key, state))
		shard.mu.Unlock()
	}
	return result
}

func (controller *AdaptiveConcurrencyController) Snapshots(
	targets []AdaptiveConcurrencyTarget,
) []AdaptiveConcurrencySnapshot {
	if controller == nil || len(targets) == 0 {
		return nil
	}
	normalized, err := normalizeAdaptiveConcurrencyTargets(targets)
	if err != nil {
		return nil
	}
	result := make([]AdaptiveConcurrencySnapshot, 0, len(normalized))
	for _, target := range normalized {
		stateKey := adaptiveConcurrencyStateKey(target.Key)
		shard := controller.shardFor(stateKey)
		shard.mu.Lock()
		if state := shard.entries[stateKey]; state != nil {
			result = append(result, adaptiveConcurrencySnapshot(target.Key, state))
		}
		shard.mu.Unlock()
	}
	return result
}

func (controller *AdaptiveConcurrencyController) Stats() AdaptiveConcurrencyStats {
	if controller == nil {
		return AdaptiveConcurrencyStats{}
	}
	controller.pruneExpired(controller.config.Clock.Now())
	return AdaptiveConcurrencyStats{
		Entries: int(controller.entries.Load()), Allowed: controller.allowed.Load(),
		Denied: controller.denied.Load(), Increases: controller.increases.Load(),
		Decreases: controller.decreases.Load(), ProbeDrops: controller.probeDrops.Load(),
		Evictions: controller.evictions.Load(),
	}
}

func (lease *AdaptiveConcurrencyLease) Release() error {
	if lease == nil || lease.controller == nil {
		return ErrAdaptiveConcurrencyLost
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil
	}
	shard := lease.controller.shardFor(lease.stateKey)
	shard.mu.Lock()
	state := shard.entries[lease.stateKey]
	if state == nil || state.inflight <= 0 || state.policy != lease.target.Policy {
		shard.mu.Unlock()
		return ErrAdaptiveConcurrencyLost
	}
	state.inflight--
	state.updatedAt = lease.controller.config.Clock.Now()
	shard.mu.Unlock()
	lease.released = true
	return nil
}

func (leases *AdaptiveConcurrencyLeaseSet) Release() error {
	if leases == nil {
		return nil
	}
	leases.mu.Lock()
	defer leases.mu.Unlock()
	if leases.released {
		return nil
	}
	var result error
	for index := len(leases.leases) - 1; index >= 0; index-- {
		if err := leases.leases[index].Release(); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result == nil {
		leases.released = true
	}
	return result
}

func (leases *AdaptiveConcurrencyLeaseSet) Targets() []AdaptiveConcurrencyTarget {
	if leases == nil {
		return nil
	}
	return append([]AdaptiveConcurrencyTarget(nil), leases.targets...)
}

func (controller *AdaptiveConcurrencyController) acquire(
	target AdaptiveConcurrencyTarget,
) (*AdaptiveConcurrencyLease, error) {
	now := controller.config.Clock.Now()
	stateKey := adaptiveConcurrencyStateKey(target.Key)
	shard := controller.shardFor(stateKey)
	shard.mu.Lock()
	state := shard.entries[stateKey]
	if state != nil && controller.expiredIdle(state, now) {
		delete(shard.entries, stateKey)
		controller.entries.Add(-1)
		controller.evictions.Add(1)
		state = nil
	}
	if state != nil {
		lease, err := controller.acquireLocked(state, target, stateKey, now)
		shard.mu.Unlock()
		return lease, err
	}
	shard.mu.Unlock()

	controller.admissionMu.Lock()
	defer controller.admissionMu.Unlock()
	controller.pruneExpiredLocked(now)
	shard = controller.shardFor(stateKey)
	shard.mu.Lock()
	if state = shard.entries[stateKey]; state != nil {
		lease, err := controller.acquireLocked(state, target, stateKey, now)
		shard.mu.Unlock()
		return lease, err
	}
	shard.mu.Unlock()
	if controller.entries.Load() >= int64(controller.config.MaxEntries) && !controller.evictOldestIdleLocked() {
		controller.denied.Add(1)
		return nil, ErrAdaptiveConcurrencyStateFull
	}
	shard = controller.shardFor(stateKey)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	state = &adaptiveConcurrencyState{
		policy: target.Policy, appliedRevision: target.Key.PolicyRevision,
		limit: target.Policy.InitialLimit, updatedAt: now,
	}
	shard.entries[stateKey] = state
	controller.entries.Add(1)
	return controller.acquireLocked(state, target, stateKey, now)
}

func (controller *AdaptiveConcurrencyController) acquireLocked(
	state *adaptiveConcurrencyState,
	target AdaptiveConcurrencyTarget,
	stateKey AdaptiveConcurrencyKey,
	now time.Time,
) (*AdaptiveConcurrencyLease, error) {
	revision := target.Key.PolicyRevision
	if revision <= state.appliedRevision && state.policy != target.Policy {
		controller.denied.Add(1)
		return nil, ErrAdaptiveConcurrencyConflict
	}
	if revision > state.appliedRevision {
		if state.policy != target.Policy && state.inflight > 0 {
			controller.denied.Add(1)
			return nil, ErrAdaptiveConcurrencyConflict
		}
		if state.policy != target.Policy {
			state.policy = target.Policy
			state.limit = target.Policy.InitialLimit
			state.healthyStreak = 0
			state.ttftSamples = 0
			state.baselineTTFTMs = 0
			state.lastIncreaseAt = time.Time{}
			state.lastDecreaseAt = time.Time{}
		}
		state.appliedRevision = revision
	}
	if state.inflight >= state.limit {
		controller.denied.Add(1)
		return nil, ErrAdaptiveConcurrencyExhausted
	}
	state.inflight++
	state.updatedAt = now
	controller.allowed.Add(1)
	return &AdaptiveConcurrencyLease{controller: controller, target: target, stateKey: stateKey}, nil
}

func (controller *AdaptiveConcurrencyController) observeLocked(
	state *adaptiveConcurrencyState,
	scope string,
	signal AdaptiveConcurrencySignal,
	now time.Time,
) {
	state.updatedAt = now
	memberScope := scope == AdaptiveConcurrencyScopeMemberModel
	sharedScope := scope == AdaptiveConcurrencyScopeSharedResource
	capacityOverload := signal.StatusCode == 429
	memberOverload := signal.StatusCode == 529 || signal.FirstByteTimeout
	if capacityOverload || (memberScope && memberOverload) {
		controller.decreaseLocked(state, now)
		return
	}
	if !signal.Success {
		state.healthyStreak = 0
		return
	}
	spike := false
	if memberScope && signal.TTFT > 0 {
		ttftMs := float64(signal.TTFT) / float64(time.Millisecond)
		if state.ttftSamples >= state.policy.TTFTMinimumSamples && state.baselineTTFTMs > 0 {
			spikeThreshold := state.baselineTTFTMs * float64(state.policy.TTFTSpikeBasisPoints) / 10_000
			floorThreshold := state.baselineTTFTMs + float64(state.policy.TTFTSpikeFloor)/float64(time.Millisecond)
			spike = ttftMs > spikeThreshold && ttftMs > floorThreshold
		}
		if state.ttftSamples == 0 {
			state.baselineTTFTMs = ttftMs
		} else if !spike {
			state.baselineTTFTMs = state.baselineTTFTMs*0.90 + ttftMs*0.10
		}
		state.ttftSamples++
	}
	if spike {
		controller.decreaseLocked(state, now)
		return
	}
	if sharedScope || memberScope {
		state.healthyStreak++
	}
	if state.healthyStreak < state.policy.HealthySamples ||
		(!state.lastIncreaseAt.IsZero() && now.Sub(state.lastIncreaseAt) < state.policy.IncreaseInterval) ||
		state.limit >= state.policy.MaxLimit {
		return
	}
	state.limit = min(state.policy.MaxLimit, state.limit+state.policy.IncreaseStep)
	state.healthyStreak = 0
	state.lastIncreaseAt = now
	controller.increases.Add(1)
}

func (controller *AdaptiveConcurrencyController) decreaseLocked(state *adaptiveConcurrencyState, now time.Time) {
	state.healthyStreak = 0
	if state.limit <= state.policy.MinLimit ||
		(!state.lastDecreaseAt.IsZero() && now.Sub(state.lastDecreaseAt) < state.policy.DecreaseInterval) {
		return
	}
	next := state.limit * int64(state.policy.DecreaseBasisPoints) / 10_000
	if next >= state.limit {
		next = state.limit - 1
	}
	state.limit = max(state.policy.MinLimit, next)
	state.lastDecreaseAt = now
	controller.decreases.Add(1)
}

func normalizeAdaptiveConcurrencyTargets(
	targets []AdaptiveConcurrencyTarget,
) ([]AdaptiveConcurrencyTarget, error) {
	if len(targets) == 0 || len(targets) > 2 {
		return nil, ErrAdaptiveConcurrencyInvalid
	}
	normalized := append([]AdaptiveConcurrencyTarget(nil), targets...)
	for index := range normalized {
		target := &normalized[index]
		target.Key.Scope = strings.TrimSpace(target.Key.Scope)
		target.Key.Model = strings.TrimSpace(target.Key.Model)
		if !validAdaptiveConcurrencyKey(target.Key) || !validAdaptiveConcurrencyPolicy(target.Policy) {
			return nil, ErrAdaptiveConcurrencyInvalid
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		return lessAdaptiveConcurrencyKey(normalized[left].Key, normalized[right].Key)
	})
	for index := 1; index < len(normalized); index++ {
		if adaptiveConcurrencyStateKey(normalized[index-1].Key) ==
			adaptiveConcurrencyStateKey(normalized[index].Key) {
			return nil, ErrAdaptiveConcurrencyInvalid
		}
	}
	return normalized, nil
}

func adaptiveConcurrencyStateKey(key AdaptiveConcurrencyKey) AdaptiveConcurrencyKey {
	key.PolicyRevision = 0
	return key
}

func validAdaptiveConcurrencyKey(key AdaptiveConcurrencyKey) bool {
	if key.PolicyRevision == 0 || key.Model == "" || len(key.Model) > adaptiveConcurrencyModelMaxBytes ||
		!utf8.ValidString(key.Model) || key.PoolID < 0 || key.MemberID < 0 || key.ChannelID < 0 ||
		key.AccountID < 0 || key.CredentialID < 0 {
		return false
	}
	switch key.Scope {
	case AdaptiveConcurrencyScopeMemberModel:
		return key.PoolID > 0 && key.MemberID > 0 && key.ChannelID == 0 && key.AccountID == 0 && key.CredentialID == 0
	case AdaptiveConcurrencyScopeSharedResource:
		return key.PoolID == 0 && key.MemberID == 0 &&
			(key.ChannelID > 0 || key.AccountID > 0 || key.CredentialID > 0)
	default:
		return false
	}
}

func validAdaptiveConcurrencyPolicy(policy AdaptiveConcurrencyPolicy) bool {
	return policy.MinLimit > 0 && policy.InitialLimit >= policy.MinLimit &&
		policy.MaxLimit >= policy.InitialLimit && policy.IncreaseStep > 0 &&
		policy.IncreaseStep <= policy.MaxLimit && policy.HealthySamples > 0 &&
		policy.IncreaseInterval > 0 && policy.DecreaseInterval > 0 &&
		policy.DecreaseBasisPoints >= 1_000 && policy.DecreaseBasisPoints < 10_000 &&
		policy.TTFTSpikeBasisPoints > 10_000 && policy.TTFTSpikeBasisPoints <= 100_000 &&
		policy.TTFTSpikeFloor >= 0 && policy.TTFTMinimumSamples > 0
}

func adaptiveConcurrencySnapshot(
	key AdaptiveConcurrencyKey,
	state *adaptiveConcurrencyState,
) AdaptiveConcurrencySnapshot {
	return AdaptiveConcurrencySnapshot{
		Key: key, Limit: state.limit, Inflight: state.inflight,
		HealthyStreak: state.healthyStreak, TTFTSamples: state.ttftSamples,
		BaselineTTFTMs: state.baselineTTFTMs, UpdatedAt: state.updatedAt,
	}
}

func (controller *AdaptiveConcurrencyController) pruneExpired(now time.Time) {
	controller.admissionMu.Lock()
	controller.pruneExpiredLocked(now)
	controller.admissionMu.Unlock()
}

func (controller *AdaptiveConcurrencyController) pruneExpiredLocked(now time.Time) {
	for index := range controller.shards {
		shard := &controller.shards[index]
		shard.mu.Lock()
		for key, state := range shard.entries {
			if controller.expiredIdle(state, now) {
				delete(shard.entries, key)
				controller.entries.Add(-1)
				controller.evictions.Add(1)
			}
		}
		shard.mu.Unlock()
	}
}

func (controller *AdaptiveConcurrencyController) evictOldestIdleLocked() bool {
	for index := range controller.shards {
		controller.shards[index].mu.Lock()
	}
	defer func() {
		for index := len(controller.shards) - 1; index >= 0; index-- {
			controller.shards[index].mu.Unlock()
		}
	}()
	var victim AdaptiveConcurrencyKey
	var victimUpdated time.Time
	found := false
	for index := range controller.shards {
		for key, state := range controller.shards[index].entries {
			if state.inflight != 0 {
				continue
			}
			if !found || state.updatedAt.Before(victimUpdated) ||
				(state.updatedAt.Equal(victimUpdated) && lessAdaptiveConcurrencyKey(key, victim)) {
				victim = key
				victimUpdated = state.updatedAt
				found = true
			}
		}
	}
	if !found {
		return false
	}
	shard := controller.shardFor(victim)
	delete(shard.entries, victim)
	controller.entries.Add(-1)
	controller.evictions.Add(1)
	return true
}

func (controller *AdaptiveConcurrencyController) expiredIdle(state *adaptiveConcurrencyState, now time.Time) bool {
	return state != nil && state.inflight == 0 && !now.Before(state.updatedAt.Add(controller.config.IdleTTL))
}

func (controller *AdaptiveConcurrencyController) shardFor(key AdaptiveConcurrencyKey) *adaptiveConcurrencyShard {
	hash := uint64(1469598103934665603)
	mixString := func(value string) {
		for index := 0; index < len(value); index++ {
			hash ^= uint64(value[index])
			hash *= 1099511628211
		}
	}
	mixInt := func(value uint64) {
		for shift := uint(0); shift < 64; shift += 8 {
			hash ^= (value >> shift) & 0xff
			hash *= 1099511628211
		}
	}
	mixInt(key.PolicyRevision)
	mixString(key.Scope)
	mixInt(uint64(key.PoolID))
	mixInt(uint64(key.MemberID))
	mixInt(uint64(key.ChannelID))
	mixInt(uint64(key.AccountID))
	mixInt(uint64(key.CredentialID))
	mixString(key.Model)
	return &controller.shards[hash%uint64(len(controller.shards))]
}

func lessAdaptiveConcurrencyKey(left AdaptiveConcurrencyKey, right AdaptiveConcurrencyKey) bool {
	if left.PolicyRevision != right.PolicyRevision {
		return left.PolicyRevision < right.PolicyRevision
	}
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	if left.PoolID != right.PoolID {
		return left.PoolID < right.PoolID
	}
	if left.MemberID != right.MemberID {
		return left.MemberID < right.MemberID
	}
	if left.ChannelID != right.ChannelID {
		return left.ChannelID < right.ChannelID
	}
	if left.AccountID != right.AccountID {
		return left.AccountID < right.AccountID
	}
	if left.CredentialID != right.CredentialID {
		return left.CredentialID < right.CredentialID
	}
	return left.Model < right.Model
}
