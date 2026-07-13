package channelrouting

import (
	"bytes"
	"container/list"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	EnterpriseHedgeScopeDistinctTarget = "distinct_endpoint_or_account"

	enterpriseHedgeDefaultDelay                  = 500 * time.Millisecond
	enterpriseHedgeDefaultMaxResponseBytes       = 4 << 20
	enterpriseHedgeMinimumDelay                  = 25 * time.Millisecond
	enterpriseHedgeMaximumDelay                  = 10 * time.Second
	enterpriseHedgeMinimumResponseBytes    int64 = 64 << 10
	enterpriseHedgeMaximumResponseBytes    int64 = 64 << 20
	defaultHedgeRatioMaxPools                    = 4_096
)

var (
	ErrHedgePolicyInvalid       = errors.New("invalid enterprise hedge policy")
	ErrHedgeDisabled            = errors.New("channel routing hedge is disabled")
	ErrHedgeAlreadyStarted      = errors.New("channel routing hedge attempt is already started")
	ErrHedgePrimaryRequired     = errors.New("channel routing hedge primary attempt is required")
	ErrHedgeWinnerSelected      = errors.New("channel routing hedge winner is already selected")
	ErrHedgeCostBudgetExceeded  = errors.New("channel routing hedge cost budget exceeded")
	ErrHedgeTargetNotDistinct   = errors.New("channel routing hedge target is not distinct")
	ErrHedgeRatioBudgetExceeded = errors.New("channel routing hedge extra request ratio budget exceeded")
	ErrHedgeLost                = errors.New("channel routing hedge attempt lost")

	defaultHedgeProcessLimiter = &HedgeLimiter{}
	defaultHedgeByteLimiter    = &HedgeByteLimiter{}
	defaultHedgeRatioBudget    = NewHedgeRatioBudget(defaultHedgeRatioMaxPools)
)

type HedgeAttemptRole string

const (
	HedgeAttemptPrimary   HedgeAttemptRole = "primary"
	HedgeAttemptSecondary HedgeAttemptRole = "secondary"
)

type EnterpriseHedgePolicy struct {
	Enabled                bool          `json:"enabled"`
	Delay                  time.Duration `json:"-"`
	MaxExtraCostMultiplier float64       `json:"max_extra_cost_multiplier"`
	MaxResponseBytes       int64         `json:"max_response_bytes"`
	Scope                  string        `json:"scope"`
	CrossRegion            bool          `json:"cross_region"`
	Explicit               bool          `json:"-"`
}

type enterpriseHedgeOverrides struct {
	Enabled                *bool    `json:"enabled"`
	DelayMilliseconds      *int     `json:"delay_ms"`
	MaxExtraCostMultiplier *float64 `json:"max_extra_cost_multiplier"`
	MaxResponseBytes       *int64   `json:"max_response_bytes"`
	Scope                  *string  `json:"scope"`
	CrossRegion            *bool    `json:"cross_region"`
}

type HedgeCoordinatorSnapshot struct {
	PrimaryStarted    bool
	SecondaryStarted  bool
	Winner            HedgeAttemptRole
	PrimaryFinished   bool
	SecondaryFinished bool
}

type HedgeCoordinator struct {
	mu sync.Mutex

	policy            EnterpriseHedgePolicy
	primaryCost       float64
	primaryStarted    bool
	secondaryStarted  bool
	primaryFinished   bool
	secondaryFinished bool
	winner            HedgeAttemptRole
}

type HedgeAttemptLease struct {
	coordinator *HedgeCoordinator
	role        HedgeAttemptRole
	once        sync.Once
}

type HedgeLimiter struct {
	active atomic.Int64
	denied atomic.Int64
}

type HedgeLimiterStats struct {
	Active int64 `json:"active"`
	Denied int64 `json:"denied"`
	Limit  int   `json:"limit"`
}

type HedgeSlot struct {
	limiter *HedgeLimiter
	once    sync.Once
}

type HedgeByteLimiter struct {
	active atomic.Int64
	peak   atomic.Int64
	denied atomic.Int64
}

type HedgeByteLimiterStats struct {
	ActiveBytes int64 `json:"active_bytes"`
	PeakBytes   int64 `json:"peak_bytes"`
	Denied      int64 `json:"denied"`
	LimitBytes  int64 `json:"limit_bytes"`
}

type HedgeByteSlot struct {
	limiter *HedgeByteLimiter
	bytes   int64
	once    sync.Once
}

type HedgeRatioBudget struct {
	mu sync.Mutex

	maxPools  int
	buckets   map[int]*hedgeRatioBucket
	order     list.List
	primary   int64
	allowed   int64
	denied    int64
	evictions int64
}

type hedgeRatioBucket struct {
	windowStart time.Time
	lastSeen    time.Time
	primary     int64
	extra       int64
	element     *list.Element
}

type HedgeRatioBudgetStats struct {
	Pools               int   `json:"pools"`
	PrimaryRequests     int64 `json:"primary_requests"`
	ExtraAllowed        int64 `json:"extra_allowed"`
	ExtraDenied         int64 `json:"extra_denied"`
	Evictions           int64 `json:"evictions"`
	MaxPools            int   `json:"max_pools"`
	WindowSeconds       int64 `json:"window_seconds"`
	MaxExtraBasisPoints int   `json:"max_extra_basis_points"`
}

type HedgeRuntimeStats struct {
	Concurrency HedgeLimiterStats     `json:"concurrency"`
	BufferBytes HedgeByteLimiterStats `json:"buffer_bytes"`
	ExtraRatio  HedgeRatioBudgetStats `json:"extra_ratio"`
}

func defaultEnterpriseHedgePolicy() EnterpriseHedgePolicy {
	return EnterpriseHedgePolicy{
		Delay:                  enterpriseHedgeDefaultDelay,
		MaxExtraCostMultiplier: 1,
		MaxResponseBytes:       enterpriseHedgeDefaultMaxResponseBytes,
		Scope:                  EnterpriseHedgeScopeDistinctTarget,
	}
}

func (session *RequestRoutingSession) EnterpriseHedgePolicy() (EnterpriseHedgePolicy, bool, error) {
	if session == nil || session.snapshot == nil || session.poolIndex < 0 ||
		session.poolIndex >= len(session.snapshot.view.Pools) {
		return EnterpriseHedgePolicy{}, false, ErrRoutingSessionInvalid
	}
	pool := &session.snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName {
		return EnterpriseHedgePolicy{}, false, ErrRoutingSessionInvalid
	}
	if pool.DeploymentStage != model.RoutingDeploymentStageActive ||
		pool.PolicyProfile != model.RoutingPolicyProfileEnterpriseSLO {
		return defaultEnterpriseHedgePolicy(), false, nil
	}
	policy := pool.enterprisePolicy.Hedge
	if policy.Scope == "" {
		policy = defaultEnterpriseHedgePolicy()
	}
	if err := validateEnterpriseHedgePolicy(pool.PolicyProfile, policy); err != nil {
		return EnterpriseHedgePolicy{}, true, err
	}
	return policy, true, nil
}

func resolveEnterpriseHedgePolicy(profile string, policyJSON json.RawMessage) (EnterpriseHedgePolicy, error) {
	policy := defaultEnterpriseHedgePolicy()
	if len(bytes.TrimSpace(policyJSON)) == 0 {
		policyJSON = json.RawMessage(`{}`)
	}
	var root map[string]json.RawMessage
	if err := common.Unmarshal(policyJSON, &root); err != nil || root == nil {
		return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
	}
	rawEnterprise, exists := root["enterprise"]
	if !exists {
		return policy, nil
	}
	var enterprise map[string]json.RawMessage
	if bytes.Equal(bytes.TrimSpace(rawEnterprise), []byte("null")) ||
		common.Unmarshal(rawEnterprise, &enterprise) != nil || enterprise == nil {
		return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
	}
	rawHedge, exists := enterprise["hedge"]
	if !exists {
		return policy, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawHedge), []byte("null")) {
		return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(rawHedge, &fields); err != nil || fields == nil {
		return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
	}
	allowed := map[string]struct{}{
		"enabled": {}, "delay_ms": {}, "max_extra_cost_multiplier": {},
		"max_response_bytes": {}, "scope": {}, "cross_region": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
		}
	}
	var overrides enterpriseHedgeOverrides
	if err := common.Unmarshal(rawHedge, &overrides); err != nil || overrides.Enabled == nil {
		return EnterpriseHedgePolicy{}, ErrHedgePolicyInvalid
	}
	policy.Explicit = true
	policy.Enabled = *overrides.Enabled
	if overrides.DelayMilliseconds != nil {
		policy.Delay = time.Duration(*overrides.DelayMilliseconds) * time.Millisecond
	}
	if overrides.MaxExtraCostMultiplier != nil {
		policy.MaxExtraCostMultiplier = *overrides.MaxExtraCostMultiplier
	}
	if overrides.MaxResponseBytes != nil {
		policy.MaxResponseBytes = *overrides.MaxResponseBytes
	}
	if overrides.Scope != nil {
		policy.Scope = *overrides.Scope
	}
	if overrides.CrossRegion != nil {
		policy.CrossRegion = *overrides.CrossRegion
	}
	if err := validateEnterpriseHedgePolicy(profile, policy); err != nil {
		return EnterpriseHedgePolicy{}, err
	}
	return policy, nil
}

func validateEnterpriseHedgePolicy(profile string, policy EnterpriseHedgePolicy) error {
	if !policy.Enabled && !policy.Explicit {
		return nil
	}
	if profile != model.RoutingPolicyProfileEnterpriseSLO || !policy.Explicit ||
		policy.Delay < enterpriseHedgeMinimumDelay || policy.Delay > enterpriseHedgeMaximumDelay ||
		policy.MaxExtraCostMultiplier < 1 || policy.MaxExtraCostMultiplier > 4 ||
		math.IsNaN(policy.MaxExtraCostMultiplier) || math.IsInf(policy.MaxExtraCostMultiplier, 0) ||
		policy.MaxResponseBytes < enterpriseHedgeMinimumResponseBytes ||
		policy.MaxResponseBytes > enterpriseHedgeMaximumResponseBytes ||
		policy.Scope != EnterpriseHedgeScopeDistinctTarget || policy.CrossRegion {
		return ErrHedgePolicyInvalid
	}
	return nil
}

func NewHedgeCoordinator(policy EnterpriseHedgePolicy, primaryCost float64) (*HedgeCoordinator, error) {
	if err := validateEnterpriseHedgePolicy(model.RoutingPolicyProfileEnterpriseSLO, policy); err != nil ||
		!policy.Enabled || !policy.Explicit {
		return nil, ErrHedgeDisabled
	}
	if primaryCost < 0 || math.IsNaN(primaryCost) || math.IsInf(primaryCost, 0) {
		return nil, ErrHedgePolicyInvalid
	}
	return &HedgeCoordinator{policy: policy, primaryCost: primaryCost}, nil
}

func (coordinator *HedgeCoordinator) BeginPrimary() (*HedgeAttemptLease, error) {
	if coordinator == nil {
		return nil, ErrHedgeDisabled
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.winner != "" {
		return nil, ErrHedgeWinnerSelected
	}
	if coordinator.primaryStarted {
		return nil, ErrHedgeAlreadyStarted
	}
	coordinator.primaryStarted = true
	return &HedgeAttemptLease{coordinator: coordinator, role: HedgeAttemptPrimary}, nil
}

func (coordinator *HedgeCoordinator) BeginSecondary(cost float64, targetDistinct bool) (*HedgeAttemptLease, error) {
	if coordinator == nil {
		return nil, ErrHedgeDisabled
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.primaryStarted {
		return nil, ErrHedgePrimaryRequired
	}
	if coordinator.winner != "" {
		return nil, ErrHedgeWinnerSelected
	}
	if coordinator.secondaryStarted {
		return nil, ErrHedgeAlreadyStarted
	}
	if !targetDistinct {
		return nil, ErrHedgeTargetNotDistinct
	}
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) ||
		cost > coordinator.primaryCost*coordinator.policy.MaxExtraCostMultiplier {
		return nil, ErrHedgeCostBudgetExceeded
	}
	coordinator.secondaryStarted = true
	return &HedgeAttemptLease{coordinator: coordinator, role: HedgeAttemptSecondary}, nil
}

func (lease *HedgeAttemptLease) TryWin() bool {
	if lease == nil || lease.coordinator == nil {
		return false
	}
	coordinator := lease.coordinator
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.winner != "" {
		return coordinator.winner == lease.role
	}
	coordinator.winner = lease.role
	return true
}

func (lease *HedgeAttemptLease) Finish() {
	if lease == nil || lease.coordinator == nil {
		return
	}
	lease.once.Do(func() {
		coordinator := lease.coordinator
		coordinator.mu.Lock()
		if lease.role == HedgeAttemptPrimary {
			coordinator.primaryFinished = true
		} else if lease.role == HedgeAttemptSecondary {
			coordinator.secondaryFinished = true
		}
		coordinator.mu.Unlock()
	})
}

func (coordinator *HedgeCoordinator) Snapshot() HedgeCoordinatorSnapshot {
	if coordinator == nil {
		return HedgeCoordinatorSnapshot{}
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return HedgeCoordinatorSnapshot{
		PrimaryStarted: coordinator.primaryStarted, SecondaryStarted: coordinator.secondaryStarted,
		Winner: coordinator.winner, PrimaryFinished: coordinator.primaryFinished,
		SecondaryFinished: coordinator.secondaryFinished,
	}
}

func (limiter *HedgeLimiter) TryAcquire(limit int) *HedgeSlot {
	if limiter == nil || limit < 1 {
		return nil
	}
	for {
		active := limiter.active.Load()
		if active >= int64(limit) {
			limiter.denied.Add(1)
			return nil
		}
		if limiter.active.CompareAndSwap(active, active+1) {
			return &HedgeSlot{limiter: limiter}
		}
	}
}

func (slot *HedgeSlot) Release() {
	if slot == nil || slot.limiter == nil {
		return
	}
	slot.once.Do(func() {
		slot.limiter.active.Add(-1)
	})
}

func (limiter *HedgeLimiter) Stats(limit ...int) HedgeLimiterStats {
	if limiter == nil {
		return HedgeLimiterStats{}
	}
	configuredLimit := 0
	if len(limit) > 0 && limit[0] > 0 {
		configuredLimit = limit[0]
	}
	return HedgeLimiterStats{Active: limiter.active.Load(), Denied: limiter.denied.Load(), Limit: configuredLimit}
}

func (limiter *HedgeByteLimiter) TryAcquire(bytes int64, limit int64) *HedgeByteSlot {
	if limiter == nil || bytes < 1 || limit < 1 || bytes > limit {
		if limiter != nil {
			limiter.denied.Add(1)
		}
		return nil
	}
	for {
		active := limiter.active.Load()
		if active < 0 || active > limit-bytes {
			limiter.denied.Add(1)
			return nil
		}
		next := active + bytes
		if !limiter.active.CompareAndSwap(active, next) {
			continue
		}
		for {
			peak := limiter.peak.Load()
			if next <= peak || limiter.peak.CompareAndSwap(peak, next) {
				break
			}
		}
		return &HedgeByteSlot{limiter: limiter, bytes: bytes}
	}
}

func (slot *HedgeByteSlot) Release() {
	if slot == nil || slot.limiter == nil || slot.bytes < 1 {
		return
	}
	slot.once.Do(func() {
		slot.limiter.active.Add(-slot.bytes)
	})
}

func (limiter *HedgeByteLimiter) Stats(limitBytes ...int64) HedgeByteLimiterStats {
	if limiter == nil {
		return HedgeByteLimiterStats{}
	}
	configuredLimit := int64(0)
	if len(limitBytes) > 0 && limitBytes[0] > 0 {
		configuredLimit = limitBytes[0]
	}
	return HedgeByteLimiterStats{
		ActiveBytes: limiter.active.Load(), PeakBytes: limiter.peak.Load(),
		Denied: limiter.denied.Load(), LimitBytes: configuredLimit,
	}
}

func NewHedgeRatioBudget(maxPools int) *HedgeRatioBudget {
	if maxPools < 1 {
		maxPools = 1
	}
	return &HedgeRatioBudget{maxPools: maxPools, buckets: make(map[int]*hedgeRatioBucket)}
}

func (budget *HedgeRatioBudget) ObservePrimary(poolID int, now time.Time, window time.Duration) bool {
	if budget == nil || poolID <= 0 || now.IsZero() || window <= 0 {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	bucket := budget.bucketLocked(poolID, now, window)
	if bucket == nil {
		return false
	}
	bucket.primary++
	budget.primary++
	return true
}

func (budget *HedgeRatioBudget) AllowSecondary(
	poolID int,
	now time.Time,
	window time.Duration,
	maxExtraBasisPoints int,
) bool {
	if budget == nil || poolID <= 0 || now.IsZero() || window <= 0 ||
		maxExtraBasisPoints < 1 || maxExtraBasisPoints > 10_000 {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	bucket := budget.bucketLocked(poolID, now, window)
	if bucket == nil || bucket.primary < 1 {
		budget.denied++
		return false
	}
	allowed := (bucket.primary/10_000)*int64(maxExtraBasisPoints) +
		(bucket.primary%10_000)*int64(maxExtraBasisPoints)/10_000
	if allowed < 1 {
		allowed = 1
	}
	if bucket.extra >= allowed {
		budget.denied++
		return false
	}
	bucket.extra++
	budget.allowed++
	return true
}

func (budget *HedgeRatioBudget) Stats(
	window time.Duration,
	maxExtraBasisPoints int,
) HedgeRatioBudgetStats {
	if budget == nil {
		return HedgeRatioBudgetStats{}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return HedgeRatioBudgetStats{
		Pools: len(budget.buckets), PrimaryRequests: budget.primary,
		ExtraAllowed: budget.allowed, ExtraDenied: budget.denied, Evictions: budget.evictions,
		MaxPools: budget.maxPools, WindowSeconds: int64(window / time.Second),
		MaxExtraBasisPoints: maxExtraBasisPoints,
	}
}

func (budget *HedgeRatioBudget) bucketLocked(
	poolID int,
	now time.Time,
	window time.Duration,
) *hedgeRatioBucket {
	budget.pruneOneLocked(now.Add(-2 * window))
	bucket := budget.buckets[poolID]
	if bucket == nil {
		if len(budget.buckets) >= budget.maxPools {
			budget.evictOldestLocked()
		}
		bucket = &hedgeRatioBucket{windowStart: now, lastSeen: now}
		bucket.element = budget.order.PushBack(poolID)
		budget.buckets[poolID] = bucket
	}
	if now.Before(bucket.windowStart) || !now.Before(bucket.windowStart.Add(window)) {
		bucket.windowStart = now
		bucket.primary = 0
		bucket.extra = 0
	}
	bucket.lastSeen = now
	budget.order.MoveToBack(bucket.element)
	return bucket
}

func (budget *HedgeRatioBudget) pruneOneLocked(cutoff time.Time) {
	element := budget.order.Front()
	if element == nil {
		return
	}
	poolID, _ := element.Value.(int)
	bucket := budget.buckets[poolID]
	if bucket == nil {
		budget.order.Remove(element)
		return
	}
	if bucket.lastSeen.Before(cutoff) {
		delete(budget.buckets, poolID)
		budget.order.Remove(element)
		budget.evictions++
	}
}

func (budget *HedgeRatioBudget) evictOldestLocked() {
	element := budget.order.Front()
	if element == nil {
		return
	}
	poolID, _ := element.Value.(int)
	delete(budget.buckets, poolID)
	budget.order.Remove(element)
	budget.evictions++
}

func DefaultHedgeProcessLimiter() *HedgeLimiter {
	return defaultHedgeProcessLimiter
}

func DefaultHedgeByteLimiter() *HedgeByteLimiter {
	return defaultHedgeByteLimiter
}

func DefaultHedgeRatioBudget() *HedgeRatioBudget {
	return defaultHedgeRatioBudget
}

func DefaultHedgeRuntimeStats(
	maxConcurrent int,
	maxBufferedBytes int64,
	ratioWindow time.Duration,
	maxExtraBasisPoints int,
) HedgeRuntimeStats {
	return HedgeRuntimeStats{
		Concurrency: defaultHedgeProcessLimiter.Stats(maxConcurrent),
		BufferBytes: defaultHedgeByteLimiter.Stats(maxBufferedBytes),
		ExtraRatio:  defaultHedgeRatioBudget.Stats(ratioWindow, maxExtraBasisPoints),
	}
}
