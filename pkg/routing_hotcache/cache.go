package routinghotcache

import (
	"math"
	"sort"
	"sync"

	"github.com/QuantumNous/new-api/model"
)

type Key struct {
	ChannelID   int
	APIKeyIndex int
	Model       string
	Group       string
}

type CostKey struct {
	ChannelID int
	Model     string
}

type MetricSnapshot struct {
	RequestCount            int64
	SuccessCount            int64
	ReliabilityRequestCount int64
	ReliabilityFailureCount int64
	P95LatencyMs            float64
	P95TTFTMs               float64
	OutputTokens            int64
	GenerationMs            int64
	TPS                     float64
	UpdatedUnix             int64
}

type CostSnapshot struct {
	Known           bool
	Cost            float64
	Confidence      string
	QuotaType       int
	GroupRatio      float64
	BaseRatio       float64
	CompletionRatio float64
	ModelPrice      float64
	BillingMode     string
	UpdatedUnix     int64
}

type BreakerSnapshot struct {
	State             string
	Reason            string
	CooldownUntilUnix int64
	HalfOpenInflight  int64
	UpdatedUnix       int64
}

type HealthMarker struct {
	Marked      bool
	UpdatedUnix int64
}

type BalanceSnapshot struct {
	Known       bool
	Balance     float64
	UpdatedUnix int64
}

type Limits struct {
	MaxMetrics           int
	MaxCosts             int
	MaxBreakers          int
	MaxHealth            int
	MaxCapacityCooldowns int
}

type Stats struct {
	Metrics           int
	Costs             int
	Breakers          int
	CapacityCooldowns int
	AuthFailures      int
	Balances          int
	Evictions         int64
}

var defaultLimits = Limits{
	MaxMetrics:           20_000,
	MaxCosts:             10_000,
	MaxBreakers:          20_000,
	MaxHealth:            10_000,
	MaxCapacityCooldowns: 20_000,
}

var cache = struct {
	sync.RWMutex
	metrics           map[Key]MetricSnapshot
	costs             map[CostKey]CostSnapshot
	breakers          map[Key]BreakerSnapshot
	capacityCooldowns map[Key]CapacityCooldownSnapshot
	authFailures      map[int]HealthMarker
	balances          map[int]BalanceSnapshot
	limits            Limits
	evictions         int64
}{
	metrics:           map[Key]MetricSnapshot{},
	costs:             map[CostKey]CostSnapshot{},
	breakers:          map[Key]BreakerSnapshot{},
	capacityCooldowns: map[Key]CapacityCooldownSnapshot{},
	authFailures:      map[int]HealthMarker{},
	balances:          map[int]BalanceSnapshot{},
	limits:            defaultLimits,
}

func (key Key) CostKey() CostKey {
	return CostKey{ChannelID: key.ChannelID, Model: key.Model}
}

func GetMetric(key Key) (MetricSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.metrics[key]
	return snapshot, ok
}

func GetCost(key CostKey) (CostSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.costs[key]
	return snapshot, ok
}

func GetBreaker(key Key) (BreakerSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.breakers[key]
	return snapshot, ok
}

func GetAuthFailure(channelID int) (HealthMarker, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.authFailures[channelID]
	return snapshot, ok
}

func GetBalance(channelID int) (BalanceSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.balances[channelID]
	return snapshot, ok
}

func RuntimeStats() Stats {
	cache.RLock()
	defer cache.RUnlock()
	return Stats{
		Metrics:           len(cache.metrics),
		Costs:             len(cache.costs),
		Breakers:          len(cache.breakers),
		CapacityCooldowns: len(cache.capacityCooldowns),
		AuthFailures:      len(cache.authFailures),
		Balances:          len(cache.balances),
		Evictions:         cache.evictions,
	}
}

func Prune(nowUnix int64, staleSeconds int64) int {
	cache.Lock()
	defer cache.Unlock()

	deleted := 0
	if staleSeconds > 0 {
		cutoff := nowUnix - staleSeconds
		for key, snapshot := range cache.metrics {
			if snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff {
				delete(cache.metrics, key)
				deleted++
			}
		}
		for key, snapshot := range cache.costs {
			if snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff {
				delete(cache.costs, key)
				deleted++
			}
		}
		for key, snapshot := range cache.breakers {
			if snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff {
				delete(cache.breakers, key)
				deleted++
			}
		}
		for channelID, marker := range cache.authFailures {
			if marker.UpdatedUnix > 0 && marker.UpdatedUnix < cutoff {
				delete(cache.authFailures, channelID)
				deleted++
			}
		}
		for channelID, snapshot := range cache.balances {
			if snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff {
				delete(cache.balances, channelID)
				deleted++
			}
		}
	}
	deadline := nowUnix * 1000
	for key, snapshot := range cache.capacityCooldowns {
		if snapshot.CooldownUntilUnixMilli <= deadline {
			delete(cache.capacityCooldowns, key)
			deleted++
		}
	}

	cache.limits = normalizedLimits(cache.limits)
	deleted += trimBoundedMap(cache.metrics, cache.limits.MaxMetrics, metricUpdatedUnix, keyLess)
	deleted += trimBoundedMap(cache.costs, cache.limits.MaxCosts, costUpdatedUnix, costKeyLess)
	deleted += trimBoundedMap(cache.breakers, cache.limits.MaxBreakers, breakerUpdatedUnix, keyLess)
	deleted += trimBoundedMap(cache.capacityCooldowns, cache.limits.MaxCapacityCooldowns, capacityUpdatedUnixMilli, keyLess)
	deleted += trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, intLess)
	deleted += trimBoundedMap(cache.balances, cache.limits.MaxHealth, balanceUpdatedUnix, intLess)
	cache.evictions += int64(deleted)
	return deleted
}

func SetMetricForTest(key Key, snapshot MetricSnapshot) {
	SetMetric(key, snapshot)
}

func SetMetric(key Key, snapshot MetricSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.metrics[key] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.metrics, cache.limits.MaxMetrics, metricUpdatedUnix, keyLess))
}

func SetCostForTest(key CostKey, snapshot CostSnapshot) {
	SetCost(key, snapshot)
}

func SetCost(key CostKey, snapshot CostSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.costs[key] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.costs, cache.limits.MaxCosts, costUpdatedUnix, costKeyLess))
}

func SetBreakerForTest(key Key, snapshot BreakerSnapshot) {
	SetBreaker(key, snapshot)
}

func SetBreaker(key Key, snapshot BreakerSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.breakers[key] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.breakers, cache.limits.MaxBreakers, breakerUpdatedUnix, keyLess))
}

func ClearBreaker(key Key) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.breakers, key)
}

func SetAuthFailureForTest(channelID int, marker HealthMarker) {
	SetAuthFailure(channelID, marker)
}

func SetAuthFailure(channelID int, marker HealthMarker) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.authFailures[channelID] = marker
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, intLess))
}

func ClearAuthFailure(channelID int) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.authFailures, channelID)
}

func SetBalanceForTest(channelID int, snapshot BalanceSnapshot) {
	SetBalance(channelID, snapshot)
}

func SetBalance(channelID int, snapshot BalanceSnapshot) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.balances[channelID] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.balances, cache.limits.MaxHealth, balanceUpdatedUnix, intLess))
}

func ClearChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	for key := range cache.metrics {
		if key.ChannelID == channelID {
			delete(cache.metrics, key)
		}
	}
	for key := range cache.costs {
		if key.ChannelID == channelID {
			delete(cache.costs, key)
		}
	}
	for key := range cache.breakers {
		if key.ChannelID == channelID {
			delete(cache.breakers, key)
		}
	}
	for key := range cache.capacityCooldowns {
		if key.ChannelID == channelID {
			delete(cache.capacityCooldowns, key)
		}
	}
	delete(cache.authFailures, channelID)
	delete(cache.balances, channelID)
}

func LoadCostSnapshots(snapshots []model.RoutingCostSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.limits = normalizedLimits(cache.limits)
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" {
			continue
		}
		cost := routingSnapshotCost(snapshot)
		key := CostKey{ChannelID: snapshot.ChannelID, Model: snapshot.ModelName}
		if existing, ok := cache.costs[key]; ok && existing.UpdatedUnix >= snapshot.SnapshotTS {
			continue
		}
		cache.costs[key] = CostSnapshot{
			Known:           routingSnapshotCostKnown(snapshot, cost),
			Cost:            cost,
			Confidence:      snapshot.Confidence,
			QuotaType:       snapshot.QuotaType,
			GroupRatio:      snapshot.GroupRatio,
			BaseRatio:       snapshot.BaseRatio,
			CompletionRatio: snapshot.CompletionRatio,
			ModelPrice:      snapshot.ModelPrice,
			BillingMode:     snapshot.BillingMode,
			UpdatedUnix:     snapshot.SnapshotTS,
		}
	}
	cache.evictions += int64(trimBoundedMap(cache.costs, cache.limits.MaxCosts, costUpdatedUnix, costKeyLess))
}

func LoadMetricSnapshots(snapshots []model.RoutingChannelMetric, _ int) {
	cache.Lock()
	defer cache.Unlock()
	cache.limits = normalizedLimits(cache.limits)
	for _, snapshot := range snapshots {
		if snapshot.APIKeyIndex != model.RoutingMetricSingleKeyIndex ||
			snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" || snapshot.RequestCount <= 0 {
			continue
		}
		key := Key{
			ChannelID:   snapshot.ChannelID,
			APIKeyIndex: snapshot.APIKeyIndex,
			Model:       snapshot.ModelName,
			Group:       snapshot.Group,
		}
		if existing, ok := cache.metrics[key]; ok {
			if existing.UpdatedUnix > snapshot.BucketTs {
				continue
			}
			if existing.UpdatedUnix == snapshot.BucketTs && existing.RequestCount >= snapshot.RequestCount {
				continue
			}
		}
		cache.metrics[key] = metricSnapshotFromModel(snapshot)
	}
	cache.evictions += int64(trimBoundedMap(cache.metrics, cache.limits.MaxMetrics, metricUpdatedUnix, keyLess))
}

func ApplyMetricDeltas(deltas []model.RoutingChannelMetric, _ int) {
	cache.Lock()
	defer cache.Unlock()
	cache.limits = normalizedLimits(cache.limits)
	for _, delta := range deltas {
		if delta.APIKeyIndex != model.RoutingMetricSingleKeyIndex ||
			delta.ChannelID <= 0 || delta.ModelName == "" || delta.Group == "" || delta.RequestCount <= 0 {
			continue
		}
		key := Key{
			ChannelID:   delta.ChannelID,
			APIKeyIndex: delta.APIKeyIndex,
			Model:       delta.ModelName,
			Group:       delta.Group,
		}
		incoming := metricSnapshotFromModel(delta)
		existing, ok := cache.metrics[key]
		if !ok || existing.UpdatedUnix < delta.BucketTs {
			cache.metrics[key] = incoming
			continue
		}
		if existing.UpdatedUnix > delta.BucketTs {
			continue
		}
		existing.RequestCount += delta.RequestCount
		existing.SuccessCount += delta.SuccessCount
		existing.ReliabilityRequestCount += delta.ReliabilityRequestCount
		existing.ReliabilityFailureCount += delta.ReliabilityFailureCount
		existing.OutputTokens += delta.OutputTokens
		existing.GenerationMs += delta.GenerationMs
		if incoming.P95LatencyMs > existing.P95LatencyMs {
			existing.P95LatencyMs = incoming.P95LatencyMs
		}
		if incoming.P95TTFTMs > existing.P95TTFTMs {
			existing.P95TTFTMs = incoming.P95TTFTMs
		}
		existing.TPS = tokenThroughput(existing.OutputTokens, existing.GenerationMs)
		cache.metrics[key] = existing
	}
	cache.evictions += int64(trimBoundedMap(cache.metrics, cache.limits.MaxMetrics, metricUpdatedUnix, keyLess))
}

func metricSnapshotFromModel(snapshot model.RoutingChannelMetric) MetricSnapshot {
	latencyMs := float64(snapshot.LatencyP95Ms)
	if latencyMs <= 0 {
		latencyMs = float64(snapshot.TotalLatencyMs) / float64(snapshot.RequestCount)
	}
	ttftMs := float64(snapshot.TtftP95Ms)
	if ttftMs <= 0 && snapshot.TtftCount > 0 {
		ttftMs = float64(snapshot.TtftSumMs) / float64(snapshot.TtftCount)
	}
	return MetricSnapshot{
		RequestCount:            snapshot.RequestCount,
		SuccessCount:            snapshot.SuccessCount,
		ReliabilityRequestCount: snapshot.ReliabilityRequestCount,
		ReliabilityFailureCount: snapshot.ReliabilityFailureCount,
		P95LatencyMs:            latencyMs,
		P95TTFTMs:               ttftMs,
		OutputTokens:            snapshot.OutputTokens,
		GenerationMs:            snapshot.GenerationMs,
		TPS:                     tokenThroughput(snapshot.OutputTokens, snapshot.GenerationMs),
		UpdatedUnix:             snapshot.BucketTs,
	}
}

func tokenThroughput(outputTokens int64, generationMs int64) float64 {
	if outputTokens <= 0 || generationMs <= 0 {
		return 0
	}
	return float64(outputTokens) / (float64(generationMs) / 1000)
}

func LoadBreakerSnapshots(snapshots []model.RoutingBreakerState) {
	cache.Lock()
	defer cache.Unlock()
	cache.limits = normalizedLimits(cache.limits)
	for _, snapshot := range snapshots {
		if snapshot.SemanticVersion != model.RoutingBreakerSemanticVersion ||
			snapshot.APIKeyIndex != model.RoutingMetricSingleKeyIndex ||
			snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" {
			continue
		}
		key := Key{
			ChannelID:   snapshot.ChannelID,
			APIKeyIndex: snapshot.APIKeyIndex,
			Model:       snapshot.ModelName,
			Group:       snapshot.Group,
		}
		if existing, ok := cache.breakers[key]; ok && existing.UpdatedUnix >= snapshot.UpdatedTime {
			continue
		}
		cache.breakers[key] = BreakerSnapshot{
			State:             snapshot.State,
			Reason:            snapshot.Reason,
			CooldownUntilUnix: snapshot.CooldownUntil,
			HalfOpenInflight:  snapshot.HalfOpenInflight,
			UpdatedUnix:       snapshot.UpdatedTime,
		}
	}
	cache.evictions += int64(trimBoundedMap(cache.breakers, cache.limits.MaxBreakers, breakerUpdatedUnix, keyLess))
}

func LoadHealthSnapshots(snapshots []model.RoutingChannelHealthState, nowUnix int64) {
	cache.Lock()
	defer cache.Unlock()
	cache.limits = normalizedLimits(cache.limits)
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 {
			continue
		}
		if snapshot.AuthFailure && (snapshot.AuthFailureUntil <= 0 || snapshot.AuthFailureUntil > nowUnix) {
			cache.authFailures[snapshot.ChannelID] = HealthMarker{
				Marked:      true,
				UpdatedUnix: snapshot.UpdatedTime,
			}
		} else {
			delete(cache.authFailures, snapshot.ChannelID)
		}
		if snapshot.BalanceKnown {
			cache.balances[snapshot.ChannelID] = BalanceSnapshot{
				Known:       true,
				Balance:     snapshot.Balance,
				UpdatedUnix: snapshot.BalanceUpdatedTime,
			}
		}
	}
	cache.evictions += int64(trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, intLess))
	cache.evictions += int64(trimBoundedMap(cache.balances, cache.limits.MaxHealth, balanceUpdatedUnix, intLess))
}

func routingSnapshotCost(snapshot model.RoutingCostSnapshot) float64 {
	if snapshot.ModelPrice > 0 {
		return snapshot.ModelPrice
	}
	return snapshot.GroupRatio * snapshot.BaseRatio
}

func routingSnapshotCostKnown(snapshot model.RoutingCostSnapshot, cost float64) bool {
	if snapshot.Confidence == model.RoutingCostConfidenceUnknown || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return false
	}
	if snapshot.QuotaType == 1 {
		return snapshot.ModelPrice > 0
	}
	if snapshot.BaseRatio <= 0 && snapshot.ModelPrice <= 0 {
		return false
	}
	return true
}

func normalizedLimits(value Limits) Limits {
	if value.MaxMetrics <= 0 {
		value.MaxMetrics = defaultLimits.MaxMetrics
	}
	if value.MaxCosts <= 0 {
		value.MaxCosts = defaultLimits.MaxCosts
	}
	if value.MaxBreakers <= 0 {
		value.MaxBreakers = defaultLimits.MaxBreakers
	}
	if value.MaxHealth <= 0 {
		value.MaxHealth = defaultLimits.MaxHealth
	}
	if value.MaxCapacityCooldowns <= 0 {
		value.MaxCapacityCooldowns = defaultLimits.MaxCapacityCooldowns
	}
	return value
}

func trimBoundedMap[K comparable, V any](entries map[K]V, limit int, updatedUnix func(V) int64, less func(K, K) bool) int {
	if limit < 0 {
		limit = 0
	}
	overflow := len(entries) - limit
	if overflow <= 0 {
		return 0
	}
	if overflow == 1 {
		var oldestKey K
		var oldestUpdated int64
		found := false
		for key, snapshot := range entries {
			updated := updatedUnix(snapshot)
			if !found || updated < oldestUpdated || (updated == oldestUpdated && less(key, oldestKey)) {
				oldestKey = key
				oldestUpdated = updated
				found = true
			}
		}
		if !found {
			return 0
		}
		delete(entries, oldestKey)
		return 1
	}

	keys := make([]K, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		leftUpdated := updatedUnix(entries[keys[i]])
		rightUpdated := updatedUnix(entries[keys[j]])
		if leftUpdated != rightUpdated {
			return leftUpdated < rightUpdated
		}
		return less(keys[i], keys[j])
	})
	for _, key := range keys[:overflow] {
		delete(entries, key)
	}
	return overflow
}

func metricUpdatedUnix(snapshot MetricSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func costUpdatedUnix(snapshot CostSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func breakerUpdatedUnix(snapshot BreakerSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func capacityUpdatedUnixMilli(snapshot CapacityCooldownSnapshot) int64 {
	return snapshot.UpdatedUnixMilli
}

func healthUpdatedUnix(marker HealthMarker) int64 {
	return marker.UpdatedUnix
}

func balanceUpdatedUnix(snapshot BalanceSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func keyLess(left Key, right Key) bool {
	if left.ChannelID != right.ChannelID {
		return left.ChannelID < right.ChannelID
	}
	if left.APIKeyIndex != right.APIKeyIndex {
		return left.APIKeyIndex < right.APIKeyIndex
	}
	if left.Model != right.Model {
		return left.Model < right.Model
	}
	return left.Group < right.Group
}

func costKeyLess(left CostKey, right CostKey) bool {
	if left.ChannelID != right.ChannelID {
		return left.ChannelID < right.ChannelID
	}
	return left.Model < right.Model
}

func intLess(left int, right int) bool {
	return left < right
}

func ResetForTest() {
	cache.Lock()
	defer cache.Unlock()
	cache.metrics = map[Key]MetricSnapshot{}
	cache.costs = map[CostKey]CostSnapshot{}
	cache.breakers = map[Key]BreakerSnapshot{}
	cache.capacityCooldowns = map[Key]CapacityCooldownSnapshot{}
	cache.authFailures = map[int]HealthMarker{}
	cache.balances = map[int]BalanceSnapshot{}
	cache.limits = defaultLimits
	cache.evictions = 0
}
