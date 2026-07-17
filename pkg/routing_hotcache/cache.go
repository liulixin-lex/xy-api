package routinghotcache

import (
	"sort"
	"sync"

	"github.com/QuantumNous/new-api/model"
)

type Key struct {
	ChannelID         int
	ChannelGeneration string
	APIKeyIndex       int
	Model             string
	Group             string
	Scope             string
	EndpointAuthority string
	Region            string
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

type BreakerSnapshot struct {
	State             string
	Reason            string
	CooldownUntilUnix int64
	HalfOpenInflight  int64
	UpdatedUnix       int64
}

type SharedEndpointBreakerSnapshot struct {
	State               string
	Reason              string
	CooldownUntilUnix   int64
	UpdatedUnix         int64
	ExpiresUnix         int64
	EvidenceCount       int64
	NetworkFailureCount int64
	NodeCount           int
	FailureNodeCount    int
}

type SharedEndpointBreakerEntry struct {
	Key      Key
	Snapshot SharedEndpointBreakerSnapshot
}

type HealthMarker struct {
	Marked      bool
	UpdatedUnix int64
}

type ChannelLifecycleKey struct {
	ChannelID         int
	ChannelGeneration string
}

type ChannelTrafficPolicy struct {
	ClaudeCodeOnly bool
}

type channelTrafficPolicyOverride struct {
	ClaudeCodeOnly bool
	UpdatedAtUnix  int64
}

type ChannelTrafficPolicyState struct {
	Initialized        bool
	LoadedAtUnix       int64
	RestrictedChannels int
}

type Limits struct {
	MaxMetrics           int
	MaxBreakers          int
	MaxSharedEndpoints   int
	MaxHealth            int
	MaxCapacityCooldowns int
}

type Stats struct {
	Metrics                   int
	Breakers                  int
	SharedEndpoints           int
	CapacityCooldowns         int
	ChannelBalanceUnavailable int
	AuthFailures              int
	Evictions                 int64
}

var defaultLimits = Limits{
	MaxMetrics:           20_000,
	MaxBreakers:          20_000,
	MaxSharedEndpoints:   20_000,
	MaxHealth:            10_000,
	MaxCapacityCooldowns: 20_000,
}

var cache = struct {
	sync.RWMutex
	metrics                   map[Key]MetricSnapshot
	breakers                  map[Key]BreakerSnapshot
	sharedEndpoints           map[Key]SharedEndpointBreakerSnapshot
	capacityCooldowns         map[Key]CapacityCooldownSnapshot
	channelBalanceUnavailable map[ChannelLifecycleKey]ChannelBalanceUnavailableSnapshot
	authFailures              map[ChannelLifecycleKey]HealthMarker
	channelPolicies           map[int]ChannelTrafficPolicy
	channelOverrides          map[int]channelTrafficPolicyOverride
	policiesLoadedAt          int64
	policiesReady             bool
	limits                    Limits
	evictions                 int64
}{
	metrics:                   map[Key]MetricSnapshot{},
	breakers:                  map[Key]BreakerSnapshot{},
	sharedEndpoints:           map[Key]SharedEndpointBreakerSnapshot{},
	capacityCooldowns:         map[Key]CapacityCooldownSnapshot{},
	channelBalanceUnavailable: map[ChannelLifecycleKey]ChannelBalanceUnavailableSnapshot{},
	authFailures:              map[ChannelLifecycleKey]HealthMarker{},
	channelPolicies:           map[int]ChannelTrafficPolicy{},
	channelOverrides:          map[int]channelTrafficPolicyOverride{},
	limits:                    defaultLimits,
}

func GetMetric(key Key) (MetricSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.metrics[key]
	return snapshot, ok
}

func GetBreaker(key Key) (BreakerSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.breakers[key]
	return snapshot, ok
}

func GetSharedEndpointBreaker(key Key) (SharedEndpointBreakerSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.sharedEndpoints[key]
	return snapshot, ok
}

func ListSharedEndpointBreakers() []SharedEndpointBreakerEntry {
	cache.RLock()
	defer cache.RUnlock()
	entries := make([]SharedEndpointBreakerEntry, 0, len(cache.sharedEndpoints))
	for key, snapshot := range cache.sharedEndpoints {
		entries = append(entries, SharedEndpointBreakerEntry{Key: key, Snapshot: snapshot})
	}
	sort.Slice(entries, func(i, j int) bool { return keyLess(entries[i].Key, entries[j].Key) })
	return entries
}

func ListLocalEndpointBreakers() []SharedEndpointBreakerEntry {
	cache.RLock()
	defer cache.RUnlock()
	entries := make([]SharedEndpointBreakerEntry, 0)
	for key, snapshot := range cache.breakers {
		if key.Scope != "endpoint" || key.EndpointAuthority == "" || key.Region == "" {
			continue
		}
		entries = append(entries, SharedEndpointBreakerEntry{Key: key, Snapshot: SharedEndpointBreakerSnapshot{
			State: snapshot.State, Reason: snapshot.Reason, CooldownUntilUnix: snapshot.CooldownUntilUnix,
			UpdatedUnix: snapshot.UpdatedUnix,
		}})
	}
	sort.Slice(entries, func(i, j int) bool { return keyLess(entries[i].Key, entries[j].Key) })
	return entries
}

func GetAuthFailure(channelID int) (HealthMarker, bool) {
	return GetAuthFailureForGeneration(channelID, "")
}

func GetAuthFailureForGeneration(channelID int, channelGeneration string) (HealthMarker, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.authFailures[ChannelLifecycleKey{
		ChannelID: channelID, ChannelGeneration: channelGeneration,
	}]
	return snapshot, ok
}

func GetChannelTrafficPolicy(channelID int) (ChannelTrafficPolicy, bool) {
	cache.RLock()
	defer cache.RUnlock()
	if !cache.policiesReady || channelID <= 0 {
		return ChannelTrafficPolicy{}, false
	}
	return cache.channelPolicies[channelID], true
}

func ChannelTrafficPoliciesState() ChannelTrafficPolicyState {
	cache.RLock()
	defer cache.RUnlock()
	return ChannelTrafficPolicyState{
		Initialized:        cache.policiesReady,
		LoadedAtUnix:       cache.policiesLoadedAt,
		RestrictedChannels: len(cache.channelPolicies),
	}
}

func ReplaceChannelTrafficConfigurations(configurations []model.RoutingChannelConfiguration, loadedAtUnix int64) {
	policies := make(map[int]ChannelTrafficPolicy)
	for _, configuration := range configurations {
		if configuration.ChannelID > 0 &&
			configuration.TrafficClass == model.RoutingChannelTrafficClassClaudeCodeOnly {
			policies[configuration.ChannelID] = ChannelTrafficPolicy{ClaudeCodeOnly: true}
		}
	}
	replaceChannelTrafficPolicies(policies, loadedAtUnix)
}

func replaceChannelTrafficPolicies(policies map[int]ChannelTrafficPolicy, loadedAtUnix int64) {
	cache.Lock()
	defer cache.Unlock()
	loadedAtUnix = max(loadedAtUnix, int64(0))
	if cache.policiesReady && loadedAtUnix < cache.policiesLoadedAt {
		return
	}
	for channelID, override := range cache.channelOverrides {
		// A database refresh is timestamped before its reads begin. Preserve a
		// targeted mutation that happened during or after that refresh so an
		// older snapshot cannot roll the local policy back. A later refresh is
		// authoritative and retires the temporary override.
		if override.UpdatedAtUnix >= loadedAtUnix {
			if override.ClaudeCodeOnly {
				policies[channelID] = ChannelTrafficPolicy{ClaudeCodeOnly: true}
			} else {
				delete(policies, channelID)
			}
			continue
		}
		delete(cache.channelOverrides, channelID)
	}
	cache.channelPolicies = policies
	cache.policiesLoadedAt = loadedAtUnix
	cache.policiesReady = true
}

func SetChannelTrafficPolicy(channelID int, claudeCodeOnly bool, updatedAtUnix int64) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.channelOverrides[channelID] = channelTrafficPolicyOverride{
		ClaudeCodeOnly: claudeCodeOnly,
		UpdatedAtUnix:  max(updatedAtUnix, int64(0)),
	}
	if claudeCodeOnly {
		cache.channelPolicies[channelID] = ChannelTrafficPolicy{ClaudeCodeOnly: true}
	} else {
		delete(cache.channelPolicies, channelID)
	}
}

func DeleteChannelTrafficPolicy(channelID int, updatedAtUnix int64) {
	SetChannelTrafficPolicy(channelID, false, updatedAtUnix)
}

func RuntimeStats() Stats {
	cache.RLock()
	defer cache.RUnlock()
	return Stats{
		Metrics:                   len(cache.metrics),
		Breakers:                  len(cache.breakers),
		SharedEndpoints:           len(cache.sharedEndpoints),
		CapacityCooldowns:         len(cache.capacityCooldowns),
		ChannelBalanceUnavailable: len(cache.channelBalanceUnavailable),
		AuthFailures:              len(cache.authFailures),
		Evictions:                 cache.evictions,
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
		for key, snapshot := range cache.breakers {
			if snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff {
				delete(cache.breakers, key)
				deleted++
			}
		}
		for key, snapshot := range cache.sharedEndpoints {
			if snapshot.ExpiresUnix <= nowUnix || (snapshot.UpdatedUnix > 0 && snapshot.UpdatedUnix < cutoff) {
				delete(cache.sharedEndpoints, key)
				deleted++
			}
		}
		for lifecycle, marker := range cache.authFailures {
			if marker.UpdatedUnix > 0 && marker.UpdatedUnix < cutoff {
				delete(cache.authFailures, lifecycle)
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
	for lifecycle, snapshot := range cache.channelBalanceUnavailable {
		if snapshot.CooldownUntilUnixMilli <= deadline {
			delete(cache.channelBalanceUnavailable, lifecycle)
			deleted++
		}
	}

	cache.limits = normalizedLimits(cache.limits)
	deleted += trimBoundedMap(cache.metrics, cache.limits.MaxMetrics, metricUpdatedUnix, keyLess)
	deleted += trimBoundedMap(cache.breakers, cache.limits.MaxBreakers, breakerUpdatedUnix, keyLess)
	deleted += trimBoundedMap(cache.sharedEndpoints, cache.limits.MaxSharedEndpoints, sharedEndpointUpdatedUnix, keyLess)
	deleted += trimBoundedMap(cache.capacityCooldowns, cache.limits.MaxCapacityCooldowns, capacityUpdatedUnixMilli, keyLess)
	deleted += trimBoundedMap(
		cache.channelBalanceUnavailable, cache.limits.MaxHealth,
		channelBalanceUnavailableUpdatedUnixMilli, channelLifecycleLess,
	)
	deleted += trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, channelLifecycleLess)
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

func ClearSharedEndpointBreaker(key Key) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.sharedEndpoints, key)
}

// ReplaceSharedEndpointBreakers atomically replaces the database-backed
// regional view. Local breaker entries remain untouched.
func ReplaceSharedEndpointBreakers(entries []SharedEndpointBreakerEntry) {
	cache.Lock()
	defer cache.Unlock()
	next := make(map[Key]SharedEndpointBreakerSnapshot, len(entries))
	for _, entry := range entries {
		key := entry.Key
		snapshot := entry.Snapshot
		if key.Scope != "endpoint" || key.EndpointAuthority == "" || key.Region == "" ||
			snapshot.State == "" || snapshot.UpdatedUnix <= 0 || snapshot.ExpiresUnix <= snapshot.UpdatedUnix {
			continue
		}
		next[key] = snapshot
	}
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(next, cache.limits.MaxSharedEndpoints, sharedEndpointUpdatedUnix, keyLess))
	cache.sharedEndpoints = next
}

func SetAuthFailureForTest(channelID int, marker HealthMarker) {
	SetAuthFailure(channelID, marker)
}

func SetAuthFailure(channelID int, marker HealthMarker) {
	SetAuthFailureForGeneration(channelID, "", marker)
}

func SetAuthFailureForGeneration(channelID int, channelGeneration string, marker HealthMarker) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.authFailures[ChannelLifecycleKey{
		ChannelID: channelID, ChannelGeneration: channelGeneration,
	}] = marker
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, channelLifecycleLess))
}

func ClearAuthFailure(channelID int) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	for lifecycle := range cache.authFailures {
		if lifecycle.ChannelID == channelID {
			delete(cache.authFailures, lifecycle)
		}
	}
}

func ClearAuthFailureForGeneration(channelID int, channelGeneration string) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.authFailures, ChannelLifecycleKey{
		ChannelID: channelID, ChannelGeneration: channelGeneration,
	})
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
	for lifecycle := range cache.channelBalanceUnavailable {
		if lifecycle.ChannelID == channelID {
			delete(cache.channelBalanceUnavailable, lifecycle)
		}
	}
	for lifecycle := range cache.authFailures {
		if lifecycle.ChannelID == channelID {
			delete(cache.authFailures, lifecycle)
		}
	}
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
			ChannelID:         snapshot.ChannelID,
			ChannelGeneration: snapshot.ChannelGeneration,
			APIKeyIndex:       snapshot.APIKeyIndex,
			Model:             snapshot.ModelName,
			Group:             snapshot.Group,
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
			ChannelID:         snapshot.ChannelID,
			ChannelGeneration: snapshot.ChannelGeneration,
			APIKeyIndex:       snapshot.APIKeyIndex,
			Model:             snapshot.ModelName,
			Group:             snapshot.Group,
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
			cache.authFailures[ChannelLifecycleKey{
				ChannelID: snapshot.ChannelID, ChannelGeneration: snapshot.ChannelGeneration,
			}] = HealthMarker{
				Marked:      true,
				UpdatedUnix: snapshot.UpdatedTime,
			}
		} else {
			delete(cache.authFailures, ChannelLifecycleKey{
				ChannelID: snapshot.ChannelID, ChannelGeneration: snapshot.ChannelGeneration,
			})
		}
	}
	cache.evictions += int64(trimBoundedMap(cache.authFailures, cache.limits.MaxHealth, healthUpdatedUnix, channelLifecycleLess))
}

func normalizedLimits(value Limits) Limits {
	if value.MaxMetrics <= 0 {
		value.MaxMetrics = defaultLimits.MaxMetrics
	}
	if value.MaxBreakers <= 0 {
		value.MaxBreakers = defaultLimits.MaxBreakers
	}
	if value.MaxSharedEndpoints <= 0 {
		value.MaxSharedEndpoints = defaultLimits.MaxSharedEndpoints
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

func breakerUpdatedUnix(snapshot BreakerSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func sharedEndpointUpdatedUnix(snapshot SharedEndpointBreakerSnapshot) int64 {
	return snapshot.UpdatedUnix
}

func capacityUpdatedUnixMilli(snapshot CapacityCooldownSnapshot) int64 {
	return snapshot.UpdatedUnixMilli
}

func channelBalanceUnavailableUpdatedUnixMilli(snapshot ChannelBalanceUnavailableSnapshot) int64 {
	return snapshot.UpdatedUnixMilli
}

func healthUpdatedUnix(marker HealthMarker) int64 {
	return marker.UpdatedUnix
}

func keyLess(left Key, right Key) bool {
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	if left.EndpointAuthority != right.EndpointAuthority {
		return left.EndpointAuthority < right.EndpointAuthority
	}
	if left.Region != right.Region {
		return left.Region < right.Region
	}
	if left.ChannelID != right.ChannelID {
		return left.ChannelID < right.ChannelID
	}
	if left.ChannelGeneration != right.ChannelGeneration {
		return left.ChannelGeneration < right.ChannelGeneration
	}
	if left.APIKeyIndex != right.APIKeyIndex {
		return left.APIKeyIndex < right.APIKeyIndex
	}
	if left.Model != right.Model {
		return left.Model < right.Model
	}
	return left.Group < right.Group
}

func intLess(left int, right int) bool {
	return left < right
}

func channelLifecycleLess(left ChannelLifecycleKey, right ChannelLifecycleKey) bool {
	if left.ChannelID != right.ChannelID {
		return left.ChannelID < right.ChannelID
	}
	return left.ChannelGeneration < right.ChannelGeneration
}

func ResetForTest() {
	cache.Lock()
	defer cache.Unlock()
	cache.metrics = map[Key]MetricSnapshot{}
	cache.breakers = map[Key]BreakerSnapshot{}
	cache.sharedEndpoints = map[Key]SharedEndpointBreakerSnapshot{}
	cache.capacityCooldowns = map[Key]CapacityCooldownSnapshot{}
	cache.channelBalanceUnavailable = map[ChannelLifecycleKey]ChannelBalanceUnavailableSnapshot{}
	cache.authFailures = map[ChannelLifecycleKey]HealthMarker{}
	cache.channelPolicies = map[int]ChannelTrafficPolicy{}
	cache.channelOverrides = map[int]channelTrafficPolicyOverride{}
	cache.policiesLoadedAt = 0
	cache.policiesReady = false
	cache.limits = defaultLimits
	cache.evictions = 0
}
