package routingmetrics

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	maxStableModelRunes = 128
	maxStableModelBytes = 512

	maxStableBucketCount       = int64(math.MaxInt32)
	maxStableAttemptTokens     = int64(math.MaxInt32 / 2)
	maxStableRetryAfterMs      = int64((7 * 24 * time.Hour) / time.Millisecond)
	maxStableFutureBucketSkew  = int64((10 * time.Minute) / time.Second)
	maxStableDurationTotalMs   = maxStableBucketCount * routingdistribution.MaxDurationMilliseconds
	maxStableOutputTokensTotal = maxStableBucketCount * maxStableAttemptTokens
	maxStableRetryAfterTotalMs = maxStableBucketCount * maxStableRetryAfterMs
)

// StableKey is the durable in-memory aggregation identity. A policy revision
// is part of the key so samples cannot cross an activation boundary inside the
// same time bucket.
type StableKey struct {
	PoolMemberID      int
	CredentialID      int
	ChannelGeneration string
	Model             string
	BucketTs          int64
	SnapshotRevision  uint64
}

type StableInflightKey struct {
	PoolMemberID      int
	CredentialID      int
	ChannelGeneration string
	Model             string
}

// StableSnapshot contains only mergeable counters, totals, and bounded
// distributions. Percentile scalars are derived after persisted and live
// snapshots have been merged.
type StableSnapshot struct {
	PoolID                     int    `json:"pool_id"`
	PoolMemberID               int    `json:"pool_member_id"`
	CredentialID               int    `json:"credential_id"`
	ChannelID                  int    `json:"channel_id"`
	ChannelGeneration          string `json:"channel_generation,omitempty"`
	Model                      string `json:"model"`
	BucketTs                   int64  `json:"bucket_ts"`
	LastSnapshotRevision       uint64 `json:"last_snapshot_revision"`
	SketchCodecVersion         int    `json:"sketch_codec_version"`
	LatencySampleCount         int64  `json:"latency_sample_count"`
	LatencySketch              []byte `json:"latency_sketch,omitempty"`
	TtftSampleCount            int64  `json:"ttft_sample_count"`
	TtftSketch                 []byte `json:"ttft_sketch,omitempty"`
	RequestCount               int64  `json:"request_count"`
	SuccessCount               int64  `json:"success_count"`
	FailureCount               int64  `json:"failure_count"`
	UnknownClassificationCount int64  `json:"unknown_classification_count"`
	ReliabilityRequestCount    int64  `json:"reliability_request_count"`
	ReliabilityFailureCount    int64  `json:"reliability_failure_count"`
	TotalLatencyMs             int64  `json:"total_latency_ms"`
	TtftSumMs                  int64  `json:"ttft_sum_ms"`
	TtftCount                  int64  `json:"ttft_count"`
	OutputTokens               int64  `json:"output_tokens"`
	GenerationMs               int64  `json:"generation_ms"`
	Err4xx                     int64  `json:"err_4xx"`
	Err5xx                     int64  `json:"err_5xx"`
	Err429                     int64  `json:"err_429"`
	Err529                     int64  `json:"err_529"`
	RetryAfterCount            int64  `json:"retry_after_count"`
	RetryAfterTotalMs          int64  `json:"retry_after_total_ms"`
}

type StableStats struct {
	Initialized             bool  `json:"initialized"`
	Buckets                 int64 `json:"buckets"`
	InflightKeys            int64 `json:"inflight_keys"`
	BucketEvictions         int64 `json:"bucket_evictions"`
	ExpiredBucketEvictions  int64 `json:"expired_bucket_evictions"`
	CapacityBucketEvictions int64 `json:"capacity_bucket_evictions"`
	BucketDrops             int64 `json:"bucket_drops"`
	InflightDrops           int64 `json:"inflight_drops"`
	IdentityDrops           int64 `json:"identity_drops"`
	InflightIdentityDrops   int64 `json:"inflight_identity_drops"`
	DistributionClamps      int64 `json:"distribution_clamps"`
	DistributionDrops       int64 `json:"distribution_drops"`
	CounterSaturations      int64 `json:"counter_saturations"`
	InvalidSnapshotDrops    int64 `json:"invalid_snapshot_drops"`
}

type stableMetadata struct {
	poolID            int
	channelID         int
	channelGeneration string
	revision          uint64
	poolMemberID      int
	credentialID      int
	model             string
}

type stableBucket struct {
	mu                         sync.Mutex
	draining                   bool
	poolID                     int
	channelID                  int
	requestCount               int64
	successCount               int64
	failureCount               int64
	unknownClassificationCount int64
	reliabilityRequestCount    int64
	reliabilityFailureCount    int64
	totalLatencyMs             int64
	ttftSumMs                  int64
	ttftCount                  int64
	outputTokens               int64
	generationMs               int64
	err4xx                     int64
	err5xx                     int64
	err429                     int64
	err529                     int64
	retryAfterCount            int64
	retryAfterTotalMs          int64
	latencySketch              *routingdistribution.DurationSketch
	ttftSketch                 *routingdistribution.DurationSketch
}

type stableInflightCounter struct {
	mu        sync.Mutex
	value     atomic.Int64
	retired   bool
	channelID int
}

type stableStore struct {
	maintenanceMu sync.Mutex
	buckets       sync.Map
	inflight      sync.Map

	bucketCount                 atomic.Int64
	inflightKeyCount            atomic.Int64
	bucketEvictionCount         atomic.Int64
	expiredBucketEvictionCount  atomic.Int64
	capacityBucketEvictionCount atomic.Int64
	bucketDropCount             atomic.Int64
	inflightDropCount           atomic.Int64
	identityDropCount           atomic.Int64
	inflightIdentityDropCount   atomic.Int64
	distributionClampCount      atomic.Int64
	distributionDropCount       atomic.Int64
	counterSaturationCount      atomic.Int64
	invalidSnapshotDropCount    atomic.Int64
}

var stableStorePointer atomic.Pointer[stableStore]
var stableStoreInitMu sync.Mutex

func StableSnapshots() []StableSnapshot {
	store := stableStorePointer.Load()
	if store == nil {
		return nil
	}
	snapshots := make([]StableSnapshot, 0)
	store.buckets.Range(func(key any, value any) bool {
		stableKey := key.(StableKey)
		snapshot := value.(*stableBucket).snapshot(stableKey)
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
		}
		return true
	})
	sortStableSnapshots(snapshots)
	return snapshots
}

func DrainStableSnapshots() []StableSnapshot {
	return drainStableSnapshots(0, 0)
}

func DrainStableSnapshotsLimited(maxItems int, maxBytes int) []StableSnapshot {
	if maxItems <= 0 || maxBytes <= 0 {
		return nil
	}
	return drainStableSnapshots(maxItems, maxBytes)
}

func drainStableSnapshots(maxItems int, maxBytes int) []StableSnapshot {
	store := stableStorePointer.Load()
	if store == nil {
		return nil
	}

	type drainEntry struct {
		key    StableKey
		bucket *stableBucket
	}
	entries := make([]drainEntry, 0)
	store.maintenanceMu.Lock()
	store.buckets.Range(func(key any, value any) bool {
		entries = append(entries, drainEntry{key: key.(StableKey), bucket: value.(*stableBucket)})
		return true
	})
	sort.Slice(entries, func(left int, right int) bool {
		return stableKeyLess(entries[left].key, entries[right].key)
	})
	snapshots := make([]StableSnapshot, 0, len(entries))
	totalBytes := 0
	for index := range entries {
		if maxItems > 0 && len(snapshots) >= maxItems {
			break
		}
		stableKey := entries[index].key
		bucket := entries[index].bucket
		bucket.mu.Lock()
		snapshot := bucket.snapshotLocked(stableKey)
		snapshotBytes := stableSnapshotApproxBytes(snapshot)
		if maxBytes > 0 && snapshotBytes > maxBytes-totalBytes {
			bucket.mu.Unlock()
			break
		}
		bucket.draining = true
		deleted := store.buckets.CompareAndDelete(stableKey, bucket)
		bucket.mu.Unlock()
		if !deleted {
			continue
		}
		decrementCount(&store.bucketCount)
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
			totalBytes += snapshotBytes
		}
	}
	store.maintenanceMu.Unlock()
	return snapshots
}

func stableSnapshotApproxBytes(snapshot StableSnapshot) int {
	return len(snapshot.Model) + len(snapshot.LatencySketch) + len(snapshot.TtftSketch) + 256
}

func RequeueStableSnapshots(snapshots []StableSnapshot) {
	if len(snapshots) == 0 {
		return
	}
	store := stableStorePointer.Load()
	if store == nil {
		if !smart_routing_setting.Enabled() {
			return
		}
		store = loadOrCreateStableStore()
	}
	for i := range snapshots {
		snapshot := snapshots[i]
		if !validStableSnapshot(snapshot) {
			store.invalidSnapshotDropCount.Add(1)
			continue
		}
		key := StableKey{
			PoolMemberID:      snapshot.PoolMemberID,
			CredentialID:      snapshot.CredentialID,
			ChannelGeneration: snapshot.ChannelGeneration,
			Model:             snapshot.Model,
			BucketTs:          snapshot.BucketTs,
			SnapshotRevision:  snapshot.LastSnapshotRevision,
		}
		metadata := stableMetadata{
			poolID:            snapshot.PoolID,
			channelID:         snapshot.ChannelID,
			channelGeneration: snapshot.ChannelGeneration,
			revision:          snapshot.LastSnapshotRevision,
			poolMemberID:      snapshot.PoolMemberID,
			credentialID:      snapshot.CredentialID,
			model:             snapshot.Model,
		}
		withWritableStableBucket(store, key, metadata, func(bucket *stableBucket) {
			distributionDrops, counterSaturations := bucket.addSnapshotLocked(snapshot)
			store.distributionDropCount.Add(distributionDrops)
			store.counterSaturationCount.Add(counterSaturations)
		})
	}
}

func StableInflightCount(key StableInflightKey) int64 {
	store := stableStorePointer.Load()
	if store == nil {
		return 0
	}
	value, ok := store.inflight.Load(key)
	if !ok {
		return 0
	}
	count := value.(*stableInflightCounter).value.Load()
	if count < 0 {
		return 0
	}
	return count
}

func StableRuntimeStats() StableStats {
	store := stableStorePointer.Load()
	if store == nil {
		return StableStats{}
	}
	return StableStats{
		Initialized:             true,
		Buckets:                 store.bucketCount.Load(),
		InflightKeys:            store.inflightKeyCount.Load(),
		BucketEvictions:         store.bucketEvictionCount.Load(),
		ExpiredBucketEvictions:  store.expiredBucketEvictionCount.Load(),
		CapacityBucketEvictions: store.capacityBucketEvictionCount.Load(),
		BucketDrops:             store.bucketDropCount.Load(),
		InflightDrops:           store.inflightDropCount.Load(),
		IdentityDrops:           store.identityDropCount.Load(),
		InflightIdentityDrops:   store.inflightIdentityDropCount.Load(),
		DistributionClamps:      store.distributionClampCount.Load(),
		DistributionDrops:       store.distributionDropCount.Load(),
		CounterSaturations:      store.counterSaturationCount.Load(),
		InvalidSnapshotDrops:    store.invalidSnapshotDropCount.Load(),
	}
}

func beginStableInflight(c *gin.Context, info *relaycommon.RelayInfo, channelID int) func() {
	metadata, ok := stableIdentity(c, info, channelID)
	if !ok {
		loadOrCreateStableStore().inflightIdentityDropCount.Add(1)
		return nil
	}
	key := StableInflightKey{
		PoolMemberID:      metadata.poolMemberID,
		CredentialID:      metadata.credentialID,
		ChannelGeneration: metadata.channelGeneration,
		Model:             metadata.model,
	}
	store := loadOrCreateStableStore()
	counter, acquired := store.acquireInflightCounter(key, metadata.channelID)
	if !acquired {
		return nil
	}
	return func() {
		store.releaseInflightCounter(key, counter)
	}
}

func recordStableClassifiedAttempt(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channelID int,
	now time.Time,
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	generationMs int64,
	outputTokens int64,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	metadata, ok := stableIdentity(c, info, channelID)
	if !ok {
		loadOrCreateStableStore().identityDropCount.Add(1)
		return
	}
	key := StableKey{
		PoolMemberID:      metadata.poolMemberID,
		CredentialID:      metadata.credentialID,
		ChannelGeneration: metadata.channelGeneration,
		Model:             metadata.model,
		BucketTs:          bucketStart(now.Unix()),
		SnapshotRevision:  metadata.revision,
	}
	store := loadOrCreateStableStore()
	withWritableStableBucket(store, key, metadata, func(bucket *stableBucket) {
		clamps, drops, counterSaturations := bucket.addLocked(latencyMs, ttftMs, hasTtft, generationMs, outputTokens, success, apiErr, classification)
		store.distributionClampCount.Add(clamps)
		store.distributionDropCount.Add(drops)
		store.counterSaturationCount.Add(counterSaturations)
	})
}

func stableIdentity(c *gin.Context, info *relaycommon.RelayInfo, channelID int) (stableMetadata, bool) {
	if info == nil || !validStableModel(info.OriginModelName) {
		return stableMetadata{}, false
	}
	multiKey := info.CurrentAttemptIsMultiKey(c)
	metadata := stableMetadata{
		channelID: channelID,
		model:     info.OriginModelName,
	}
	if metadata.channelID <= 0 && c != nil {
		metadata.channelID = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	}
	if metadata.channelID <= 0 && info.ChannelMeta != nil {
		metadata.channelID = info.ChannelMeta.ChannelId
	}
	if c != nil {
		metadata.channelGeneration = common.GetContextKeyString(c, constant.ContextKeyRoutingGeneration)
	}
	if metadata.channelGeneration == "" && info.ChannelMeta != nil {
		metadata.channelGeneration = info.ChannelMeta.RoutingGeneration
	}

	if value, ok := stableContextInt(c, constant.ContextKeyRoutingPoolID); ok {
		metadata.poolID = value
	} else if info.ChannelMeta != nil {
		metadata.poolID = info.ChannelMeta.RoutingPoolID
	}
	if value, ok := stableContextInt(c, constant.ContextKeyRoutingMemberID); ok {
		metadata.poolMemberID = value
	} else if info.ChannelMeta != nil {
		metadata.poolMemberID = info.ChannelMeta.RoutingMemberID
	}
	if value, ok := stableContextInt(c, constant.ContextKeyRoutingCredentialID); ok {
		metadata.credentialID = value
	} else if info.ChannelMeta != nil {
		metadata.credentialID = info.ChannelMeta.RoutingCredentialID
	}
	if value, ok := stableContextUint64(c, constant.ContextKeyRoutingSnapshotRevision); ok {
		metadata.revision = value
	} else if info.ChannelMeta != nil {
		metadata.revision = info.ChannelMeta.RoutingSnapshotRevision
	}

	if metadata.poolID <= 0 || metadata.channelID <= 0 || metadata.poolMemberID <= 0 ||
		metadata.credentialID < 0 || metadata.revision == 0 {
		return stableMetadata{}, false
	}
	if multiKey && metadata.credentialID <= 0 {
		return stableMetadata{}, false
	}
	if !multiKey && metadata.credentialID == 0 && stableSelectedKey(c, info) != "" {
		return stableMetadata{}, false
	}
	return metadata, true
}

func stableContextInt(c *gin.Context, key constant.ContextKey) (int, bool) {
	if c == nil {
		return 0, false
	}
	value, ok := common.GetContextKey(c, key)
	if !ok {
		return 0, false
	}
	result, _ := value.(int)
	return result, true
}

func stableContextUint64(c *gin.Context, key constant.ContextKey) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	value, ok := common.GetContextKey(c, key)
	if !ok {
		return 0, false
	}
	result, _ := value.(uint64)
	return result, true
}

func stableSelectedKey(c *gin.Context, info *relaycommon.RelayInfo) string {
	if c != nil {
		if _, ok := common.GetContextKey(c, constant.ContextKeyChannelKey); ok {
			return common.GetContextKeyString(c, constant.ContextKeyChannelKey)
		}
	}
	if info != nil && info.ChannelMeta != nil {
		return info.ChannelMeta.ApiKey
	}
	return ""
}

func loadOrCreateStableStore() *stableStore {
	if store := stableStorePointer.Load(); store != nil {
		return store
	}
	stableStoreInitMu.Lock()
	defer stableStoreInitMu.Unlock()
	if store := stableStorePointer.Load(); store != nil {
		return store
	}
	store := &stableStore{}
	stableStorePointer.Store(store)
	return store
}

func currentRoutingMetricLimits() Limits {
	maintenanceMu.Lock()
	activeLimits := normalizedLimits(limits)
	limits = activeLimits
	maintenanceMu.Unlock()
	return activeLimits
}

func withWritableStableBucket(store *stableStore, key StableKey, metadata stableMetadata, write func(*stableBucket)) {
	for {
		actual, ok := store.buckets.Load(key)
		if !ok {
			bucket := store.loadOrCreateBucket(key, metadata)
			if bucket == nil {
				return
			}
			actual = bucket
		}
		bucket := actual.(*stableBucket)
		bucket.mu.Lock()
		if bucket.draining {
			bucket.mu.Unlock()
			continue
		}
		bucket.updateMetadataLocked(metadata)
		write(bucket)
		bucket.mu.Unlock()
		return
	}
}

func (store *stableStore) loadOrCreateBucket(key StableKey, metadata stableMetadata) *stableBucket {
	activeLimits := currentRoutingMetricLimits()
	store.maintenanceMu.Lock()
	defer store.maintenanceMu.Unlock()

	if actual, ok := store.buckets.Load(key); ok {
		return actual.(*stableBucket)
	}

	ttlSeconds := int64(activeLimits.BucketTTL / time.Second)
	if activeLimits.BucketTTL%time.Second != 0 {
		ttlSeconds++
	}
	const minBucketTimestamp int64 = -1 << 63
	if key.BucketTs >= minBucketTimestamp+ttlSeconds {
		store.evictExpiredBucketsLocked(key.BucketTs - ttlSeconds)
	}
	for store.bucketCount.Load() >= int64(activeLimits.MaxBuckets) {
		if !store.evictOldestBucketLocked() {
			store.bucketDropCount.Add(1)
			return nil
		}
	}

	bucket := &stableBucket{
		poolID:    metadata.poolID,
		channelID: metadata.channelID,
	}
	store.buckets.Store(key, bucket)
	store.bucketCount.Add(1)
	return bucket
}

func (store *stableStore) evictExpiredBucketsLocked(cutoff int64) {
	store.buckets.Range(func(key any, value any) bool {
		stableKey := key.(StableKey)
		if stableKey.BucketTs <= cutoff {
			store.removeBucketLocked(stableKey, value.(*stableBucket), stableEvictionExpired)
		}
		return true
	})
}

func (store *stableStore) evictOldestBucketLocked() bool {
	var oldestKey StableKey
	var oldestBucket *stableBucket
	store.buckets.Range(func(key any, value any) bool {
		candidate := key.(StableKey)
		if oldestBucket == nil || stableKeyLess(candidate, oldestKey) {
			oldestKey = candidate
			oldestBucket = value.(*stableBucket)
		}
		return true
	})
	if oldestBucket == nil {
		return false
	}
	return store.removeBucketLocked(oldestKey, oldestBucket, stableEvictionCapacity)
}

type stableEvictionReason uint8

const (
	stableEvictionNone stableEvictionReason = iota
	stableEvictionExpired
	stableEvictionCapacity
)

func (store *stableStore) removeBucketLocked(key StableKey, bucket *stableBucket, reason stableEvictionReason) bool {
	bucket.mu.Lock()
	bucket.draining = true
	deleted := store.buckets.CompareAndDelete(key, bucket)
	bucket.mu.Unlock()
	if !deleted {
		return false
	}
	decrementCount(&store.bucketCount)
	if reason != stableEvictionNone {
		store.bucketEvictionCount.Add(1)
	}
	if reason == stableEvictionExpired {
		store.expiredBucketEvictionCount.Add(1)
	}
	if reason == stableEvictionCapacity {
		store.capacityBucketEvictionCount.Add(1)
	}
	return true
}

func (store *stableStore) acquireInflightCounter(key StableInflightKey, channelID int) (*stableInflightCounter, bool) {
	activeLimits := currentRoutingMetricLimits()
	for {
		if actual, ok := store.inflight.Load(key); ok {
			counter := actual.(*stableInflightCounter)
			counter.mu.Lock()
			if counter.retired {
				counter.mu.Unlock()
				continue
			}
			if channelID > 0 {
				counter.channelID = channelID
			}
			counter.value.Add(1)
			counter.mu.Unlock()
			return counter, true
		}

		store.maintenanceMu.Lock()
		if _, ok := store.inflight.Load(key); ok {
			store.maintenanceMu.Unlock()
			continue
		}
		if store.inflightKeyCount.Load() >= int64(activeLimits.MaxInflightKeys) {
			store.inflightDropCount.Add(1)
			store.maintenanceMu.Unlock()
			return nil, false
		}
		counter := &stableInflightCounter{channelID: channelID}
		counter.value.Store(1)
		store.inflight.Store(key, counter)
		store.inflightKeyCount.Add(1)
		store.maintenanceMu.Unlock()
		return counter, true
	}
}

func (store *stableStore) releaseInflightCounter(key StableInflightKey, counter *stableInflightCounter) {
	counter.mu.Lock()
	count := counter.value.Load()
	if counter.retired || count <= 0 {
		counter.mu.Unlock()
		return
	}
	if count > 1 {
		counter.value.Add(-1)
		counter.mu.Unlock()
		return
	}
	counter.mu.Unlock()

	store.maintenanceMu.Lock()
	counter.mu.Lock()
	defer counter.mu.Unlock()
	defer store.maintenanceMu.Unlock()
	count = counter.value.Load()
	if counter.retired || count <= 0 {
		return
	}
	if count > 1 {
		counter.value.Add(-1)
		return
	}
	counter.value.Store(0)
	counter.retired = true
	if store.inflight.CompareAndDelete(key, counter) {
		decrementCount(&store.inflightKeyCount)
	}
}

func (bucket *stableBucket) updateMetadataLocked(metadata stableMetadata) {
	if bucket.poolID <= 0 && metadata.poolID > 0 {
		bucket.poolID = metadata.poolID
	}
	if bucket.channelID <= 0 && metadata.channelID > 0 {
		bucket.channelID = metadata.channelID
	}
}

func (bucket *stableBucket) addLocked(
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	generationMs int64,
	outputTokens int64,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) (int64, int64, int64) {
	var distributionClamps int64
	var distributionDrops int64
	var counterSaturations int64
	if bucket.requestCount >= maxStableBucketCount {
		return 0, 0, 1
	}

	boundedLatencyMs, clamped := boundedStableValue(latencyMs, routingdistribution.MaxDurationMilliseconds)
	if clamped {
		counterSaturations++
	}
	boundedTtftMs, clamped := boundedStableValue(ttftMs, routingdistribution.MaxDurationMilliseconds)
	if clamped {
		counterSaturations++
	}
	boundedGenerationMs, clamped := boundedStableValue(generationMs, routingdistribution.MaxDurationMilliseconds)
	if clamped {
		counterSaturations++
	}
	boundedOutputTokens, clamped := boundedStableValue(outputTokens, maxStableAttemptTokens)
	if clamped {
		counterSaturations++
	}

	addStableCounter(&bucket.requestCount, 1, maxStableBucketCount)
	if success {
		addStableCounter(&bucket.successCount, 1, maxStableBucketCount)
		addStableCounter(&bucket.reliabilityRequestCount, 1, maxStableBucketCount)
	} else {
		addStableCounter(&bucket.failureCount, 1, maxStableBucketCount)
		if unknownStableClassification(classification) {
			addStableCounter(&bucket.unknownClassificationCount, 1, maxStableBucketCount)
		}
		if (classification.Responsibility == routingerror.ResponsibilityProvider ||
			(classification.Responsibility == routingerror.ResponsibilityNetwork && classification.Scope != routingerror.ScopeEndpoint)) &&
			(classification.HealthEffect == routingerror.HealthDegrade ||
				classification.HealthEffect == routingerror.HealthOpen) {
			addStableCounter(&bucket.reliabilityRequestCount, 1, maxStableBucketCount)
			addStableCounter(&bucket.reliabilityFailureCount, 1, maxStableBucketCount)
		}
	}
	if addStableCounter(&bucket.totalLatencyMs, boundedLatencyMs, maxStableDurationTotalMs) {
		counterSaturations++
	}
	if bucket.latencySketch == nil {
		bucket.latencySketch = routingdistribution.NewDurationSketch()
	}
	if result, err := bucket.latencySketch.AddMillis(latencyMs); err != nil {
		distributionDrops++
	} else if result.Clamped {
		distributionClamps++
	}
	if hasTtft {
		if addStableCounter(&bucket.ttftSumMs, boundedTtftMs, maxStableDurationTotalMs) {
			counterSaturations++
		}
		addStableCounter(&bucket.ttftCount, 1, maxStableBucketCount)
		if bucket.ttftSketch == nil {
			bucket.ttftSketch = routingdistribution.NewDurationSketch()
		}
		if result, err := bucket.ttftSketch.AddMillis(ttftMs); err != nil {
			distributionDrops++
		} else if result.Clamped {
			distributionClamps++
		}
	}
	if boundedOutputTokens > 0 && boundedGenerationMs > 0 {
		if addStableCounter(&bucket.outputTokens, boundedOutputTokens, maxStableOutputTokensTotal) {
			counterSaturations++
		}
		if addStableCounter(&bucket.generationMs, boundedGenerationMs, maxStableDurationTotalMs) {
			counterSaturations++
		}
	}

	statusCode := 0
	if apiErr != nil {
		statusCode = apiErr.SourceStatusCode()
	}
	switch {
	case statusCode == 429:
		addStableCounter(&bucket.err429, 1, maxStableBucketCount)
	case statusCode == 529:
		addStableCounter(&bucket.err529, 1, maxStableBucketCount)
	case statusCode >= 500 && statusCode <= 599:
		addStableCounter(&bucket.err5xx, 1, maxStableBucketCount)
	case statusCode >= 400 && statusCode <= 499:
		addStableCounter(&bucket.err4xx, 1, maxStableBucketCount)
	}
	if retryAfterMs := retryAfterMaxMS(apiErr); retryAfterMs > 0 {
		boundedRetryAfterMs, retryAfterClamped := boundedStableValue(retryAfterMs, maxStableRetryAfterMs)
		if retryAfterClamped {
			counterSaturations++
		}
		addStableCounter(&bucket.retryAfterCount, 1, maxStableBucketCount)
		if addStableCounter(&bucket.retryAfterTotalMs, boundedRetryAfterMs, maxStableRetryAfterTotalMs) {
			counterSaturations++
		}
	}
	return distributionClamps, distributionDrops, counterSaturations
}

func (bucket *stableBucket) addSnapshotLocked(snapshot StableSnapshot) (int64, int64) {
	if !bucket.canAddSnapshotLocked(snapshot) {
		return 0, 1
	}
	var distributionDrops int64
	addStableCounter(&bucket.requestCount, snapshot.RequestCount, maxStableBucketCount)
	addStableCounter(&bucket.successCount, snapshot.SuccessCount, maxStableBucketCount)
	addStableCounter(&bucket.failureCount, snapshot.FailureCount, maxStableBucketCount)
	addStableCounter(&bucket.unknownClassificationCount, snapshot.UnknownClassificationCount, maxStableBucketCount)
	addStableCounter(&bucket.reliabilityRequestCount, snapshot.ReliabilityRequestCount, maxStableBucketCount)
	addStableCounter(&bucket.reliabilityFailureCount, snapshot.ReliabilityFailureCount, maxStableBucketCount)
	addStableCounter(&bucket.totalLatencyMs, snapshot.TotalLatencyMs, maxStableDurationTotalMs)
	addStableCounter(&bucket.ttftSumMs, snapshot.TtftSumMs, maxStableDurationTotalMs)
	addStableCounter(&bucket.ttftCount, snapshot.TtftCount, maxStableBucketCount)
	addStableCounter(&bucket.outputTokens, snapshot.OutputTokens, maxStableOutputTokensTotal)
	addStableCounter(&bucket.generationMs, snapshot.GenerationMs, maxStableDurationTotalMs)
	addStableCounter(&bucket.err4xx, snapshot.Err4xx, maxStableBucketCount)
	addStableCounter(&bucket.err5xx, snapshot.Err5xx, maxStableBucketCount)
	addStableCounter(&bucket.err429, snapshot.Err429, maxStableBucketCount)
	addStableCounter(&bucket.err529, snapshot.Err529, maxStableBucketCount)
	addStableCounter(&bucket.retryAfterCount, snapshot.RetryAfterCount, maxStableBucketCount)
	addStableCounter(&bucket.retryAfterTotalMs, snapshot.RetryAfterTotalMs, maxStableRetryAfterTotalMs)
	if snapshot.LatencySampleCount > 0 {
		incoming, err := routingdistribution.DecodeDurationSketch(snapshot.LatencySketch, snapshot.SketchCodecVersion)
		if err != nil || incoming.Count() != snapshot.LatencySampleCount {
			distributionDrops++
		} else if bucket.latencySketch == nil {
			bucket.latencySketch = incoming
		} else if err := bucket.latencySketch.Merge(incoming); err != nil {
			distributionDrops++
		}
	} else if len(snapshot.LatencySketch) != 0 {
		distributionDrops++
	}
	if snapshot.TtftSampleCount > 0 {
		incoming, err := routingdistribution.DecodeDurationSketch(snapshot.TtftSketch, snapshot.SketchCodecVersion)
		if err != nil || incoming.Count() != snapshot.TtftSampleCount {
			distributionDrops++
		} else if bucket.ttftSketch == nil {
			bucket.ttftSketch = incoming
		} else if err := bucket.ttftSketch.Merge(incoming); err != nil {
			distributionDrops++
		}
	} else if len(snapshot.TtftSketch) != 0 {
		distributionDrops++
	}
	return distributionDrops, 0
}

func (bucket *stableBucket) canAddSnapshotLocked(snapshot StableSnapshot) bool {
	targets := []int64{
		bucket.requestCount,
		bucket.successCount,
		bucket.failureCount,
		bucket.unknownClassificationCount,
		bucket.reliabilityRequestCount,
		bucket.reliabilityFailureCount,
		bucket.totalLatencyMs,
		bucket.ttftSumMs,
		bucket.ttftCount,
		bucket.outputTokens,
		bucket.generationMs,
		bucket.err4xx,
		bucket.err5xx,
		bucket.err429,
		bucket.err529,
		bucket.retryAfterCount,
		bucket.retryAfterTotalMs,
	}
	deltas := []int64{
		snapshot.RequestCount,
		snapshot.SuccessCount,
		snapshot.FailureCount,
		snapshot.UnknownClassificationCount,
		snapshot.ReliabilityRequestCount,
		snapshot.ReliabilityFailureCount,
		snapshot.TotalLatencyMs,
		snapshot.TtftSumMs,
		snapshot.TtftCount,
		snapshot.OutputTokens,
		snapshot.GenerationMs,
		snapshot.Err4xx,
		snapshot.Err5xx,
		snapshot.Err429,
		snapshot.Err529,
		snapshot.RetryAfterCount,
		snapshot.RetryAfterTotalMs,
	}
	limits := []int64{
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableDurationTotalMs,
		maxStableDurationTotalMs,
		maxStableBucketCount,
		maxStableOutputTokensTotal,
		maxStableDurationTotalMs,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableBucketCount,
		maxStableRetryAfterTotalMs,
	}
	for index := range targets {
		if targets[index] < 0 || deltas[index] < 0 || targets[index] > limits[index]-deltas[index] {
			return false
		}
	}
	return true
}

func addStableCounter(target *int64, delta int64, limit int64) bool {
	if delta <= 0 {
		return delta < 0
	}
	if *target < 0 {
		*target = 0
		return true
	}
	if *target > limit-delta {
		*target = limit
		return true
	}
	*target += delta
	return false
}

func boundedStableValue(value int64, limit int64) (int64, bool) {
	if value < 0 {
		return 0, true
	}
	if value > limit {
		return limit, true
	}
	return value, false
}

func (bucket *stableBucket) snapshot(key StableKey) StableSnapshot {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	return bucket.snapshotLocked(key)
}

func (bucket *stableBucket) snapshotLocked(key StableKey) StableSnapshot {
	snapshot := StableSnapshot{
		PoolID:                     bucket.poolID,
		PoolMemberID:               key.PoolMemberID,
		CredentialID:               key.CredentialID,
		ChannelID:                  bucket.channelID,
		ChannelGeneration:          key.ChannelGeneration,
		Model:                      key.Model,
		BucketTs:                   key.BucketTs,
		LastSnapshotRevision:       key.SnapshotRevision,
		RequestCount:               bucket.requestCount,
		SuccessCount:               bucket.successCount,
		FailureCount:               bucket.failureCount,
		UnknownClassificationCount: bucket.unknownClassificationCount,
		ReliabilityRequestCount:    bucket.reliabilityRequestCount,
		ReliabilityFailureCount:    bucket.reliabilityFailureCount,
		TotalLatencyMs:             bucket.totalLatencyMs,
		TtftSumMs:                  bucket.ttftSumMs,
		TtftCount:                  bucket.ttftCount,
		OutputTokens:               bucket.outputTokens,
		GenerationMs:               bucket.generationMs,
		Err4xx:                     bucket.err4xx,
		Err5xx:                     bucket.err5xx,
		Err429:                     bucket.err429,
		Err529:                     bucket.err529,
		RetryAfterCount:            bucket.retryAfterCount,
		RetryAfterTotalMs:          bucket.retryAfterTotalMs,
	}
	if bucket.latencySketch != nil && bucket.latencySketch.Count() > 0 {
		if data, err := bucket.latencySketch.MarshalBinary(); err == nil {
			snapshot.SketchCodecVersion = routingdistribution.SketchCodecVersion
			snapshot.LatencySampleCount = bucket.latencySketch.Count()
			snapshot.LatencySketch = data
		}
	}
	if bucket.ttftSketch != nil && bucket.ttftSketch.Count() > 0 {
		if data, err := bucket.ttftSketch.MarshalBinary(); err == nil {
			snapshot.SketchCodecVersion = routingdistribution.SketchCodecVersion
			snapshot.TtftSampleCount = bucket.ttftSketch.Count()
			snapshot.TtftSketch = data
		}
	}
	return snapshot
}

func validStableModel(model string) bool {
	return model != "" && utf8.ValidString(model) && len(model) <= maxStableModelBytes && utf8.RuneCountInString(model) <= maxStableModelRunes
}

func validStableSnapshot(snapshot StableSnapshot) bool {
	maxBucketTs := int64(math.MaxInt64)
	nowUnix := time.Now().Unix()
	if nowUnix <= math.MaxInt64-maxStableFutureBucketSkew {
		maxBucketTs = nowUnix + maxStableFutureBucketSkew
	}
	if snapshot.PoolID <= 0 || snapshot.PoolMemberID <= 0 || snapshot.ChannelID <= 0 || snapshot.CredentialID < 0 ||
		snapshot.LastSnapshotRevision == 0 || !validStableModel(snapshot.Model) || snapshot.BucketTs < 0 || snapshot.BucketTs > maxBucketTs ||
		snapshot.RequestCount <= 0 || snapshot.RequestCount > maxStableBucketCount {
		return false
	}
	for _, counter := range []int64{
		snapshot.SuccessCount,
		snapshot.FailureCount,
		snapshot.UnknownClassificationCount,
		snapshot.ReliabilityRequestCount,
		snapshot.ReliabilityFailureCount,
		snapshot.TtftCount,
		snapshot.Err4xx,
		snapshot.Err5xx,
		snapshot.Err429,
		snapshot.Err529,
		snapshot.RetryAfterCount,
		snapshot.LatencySampleCount,
		snapshot.TtftSampleCount,
	} {
		if counter < 0 || counter > maxStableBucketCount {
			return false
		}
	}
	if snapshot.TotalLatencyMs < 0 || snapshot.TotalLatencyMs > snapshot.RequestCount*routingdistribution.MaxDurationMilliseconds ||
		snapshot.TtftSumMs < 0 || snapshot.TtftSumMs > snapshot.TtftCount*routingdistribution.MaxDurationMilliseconds ||
		snapshot.GenerationMs < 0 || snapshot.GenerationMs > snapshot.RequestCount*routingdistribution.MaxDurationMilliseconds ||
		snapshot.OutputTokens < 0 || snapshot.OutputTokens > snapshot.RequestCount*maxStableAttemptTokens ||
		snapshot.RetryAfterTotalMs < 0 || snapshot.RetryAfterTotalMs > snapshot.RetryAfterCount*maxStableRetryAfterMs {
		return false
	}
	if snapshot.SuccessCount > snapshot.RequestCount-snapshot.FailureCount ||
		snapshot.UnknownClassificationCount > snapshot.FailureCount ||
		snapshot.ReliabilityRequestCount > snapshot.RequestCount ||
		snapshot.ReliabilityFailureCount > snapshot.ReliabilityRequestCount ||
		snapshot.TtftCount > snapshot.RequestCount ||
		snapshot.RetryAfterCount > snapshot.RequestCount ||
		snapshot.LatencySampleCount > snapshot.RequestCount ||
		snapshot.TtftSampleCount > snapshot.TtftCount {
		return false
	}
	errorCount := snapshot.Err4xx + snapshot.Err5xx + snapshot.Err429 + snapshot.Err529
	return errorCount >= 0 && errorCount <= snapshot.RequestCount
}

func unknownStableClassification(classification routingerror.Classification) bool {
	rule := strings.ToLower(strings.TrimSpace(classification.Rule))
	return rule == "" || strings.Contains(rule, "fallback")
}

func stableKeyLess(left StableKey, right StableKey) bool {
	if left.BucketTs != right.BucketTs {
		return left.BucketTs < right.BucketTs
	}
	if left.SnapshotRevision != right.SnapshotRevision {
		return left.SnapshotRevision < right.SnapshotRevision
	}
	if left.PoolMemberID != right.PoolMemberID {
		return left.PoolMemberID < right.PoolMemberID
	}
	if left.CredentialID != right.CredentialID {
		return left.CredentialID < right.CredentialID
	}
	return left.Model < right.Model
}

func sortStableSnapshots(snapshots []StableSnapshot) {
	sort.Slice(snapshots, func(i, j int) bool {
		left := StableKey{
			PoolMemberID:     snapshots[i].PoolMemberID,
			CredentialID:     snapshots[i].CredentialID,
			Model:            snapshots[i].Model,
			BucketTs:         snapshots[i].BucketTs,
			SnapshotRevision: snapshots[i].LastSnapshotRevision,
		}
		right := StableKey{
			PoolMemberID:     snapshots[j].PoolMemberID,
			CredentialID:     snapshots[j].CredentialID,
			Model:            snapshots[j].Model,
			BucketTs:         snapshots[j].BucketTs,
			SnapshotRevision: snapshots[j].LastSnapshotRevision,
		}
		return stableKeyLess(left, right)
	})
}

func clearStableChannel(channelID int) {
	store := stableStorePointer.Load()
	if store == nil {
		return
	}
	store.maintenanceMu.Lock()
	store.buckets.Range(func(key any, value any) bool {
		stableKey := key.(StableKey)
		bucket := value.(*stableBucket)
		bucket.mu.Lock()
		if bucket.channelID != channelID {
			bucket.mu.Unlock()
			return true
		}
		bucket.draining = true
		deleted := store.buckets.CompareAndDelete(stableKey, bucket)
		bucket.mu.Unlock()
		if deleted {
			decrementCount(&store.bucketCount)
		}
		return true
	})
	store.inflight.Range(func(key any, value any) bool {
		stableKey := key.(StableInflightKey)
		counter := value.(*stableInflightCounter)
		counter.mu.Lock()
		if counter.channelID != channelID {
			counter.mu.Unlock()
			return true
		}
		counter.retired = true
		deleted := store.inflight.CompareAndDelete(stableKey, counter)
		counter.mu.Unlock()
		if deleted {
			decrementCount(&store.inflightKeyCount)
		}
		return true
	})
	store.maintenanceMu.Unlock()
}

func resetStableForTest() {
	stableStoreInitMu.Lock()
	stableStorePointer.Store(nil)
	stableStoreInitMu.Unlock()
}
