package routingmetrics

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	maxStableModelRunes = 128
	maxStableModelBytes = 512
)

// StableKey is the durable in-memory aggregation identity. Pool, physical
// channel, and topology revision are metadata because they may change without
// changing the member or credential identity.
type StableKey struct {
	PoolMemberID int
	CredentialID int
	Model        string
	BucketTs     int64
}

type StableInflightKey struct {
	PoolMemberID int
	CredentialID int
	Model        string
}

// StableSnapshot intentionally contains only mergeable counters and totals.
// Percentile scalars are excluded until the routing data plane has a
// mergeable distribution representation.
type StableSnapshot struct {
	PoolID                     int    `json:"pool_id"`
	PoolMemberID               int    `json:"pool_member_id"`
	CredentialID               int    `json:"credential_id"`
	ChannelID                  int    `json:"channel_id"`
	Model                      string `json:"model"`
	BucketTs                   int64  `json:"bucket_ts"`
	LastSnapshotRevision       uint64 `json:"last_snapshot_revision"`
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
}

type stableMetadata struct {
	poolID       int
	channelID    int
	revision     uint64
	poolMemberID int
	credentialID int
	model        string
}

type stableBucket struct {
	mu                         sync.Mutex
	draining                   bool
	poolID                     int
	channelID                  int
	lastSnapshotRevision       uint64
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
	store := stableStorePointer.Load()
	if store == nil {
		return nil
	}

	snapshots := make([]StableSnapshot, 0)
	store.maintenanceMu.Lock()
	store.buckets.Range(func(key any, value any) bool {
		stableKey := key.(StableKey)
		bucket := value.(*stableBucket)
		bucket.mu.Lock()
		bucket.draining = true
		snapshot := bucket.snapshotLocked(stableKey)
		deleted := store.buckets.CompareAndDelete(key, value)
		bucket.mu.Unlock()
		if !deleted {
			return true
		}
		decrementCount(&store.bucketCount)
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
		}
		return true
	})
	store.maintenanceMu.Unlock()
	sortStableSnapshots(snapshots)
	return snapshots
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
		if snapshot.PoolID <= 0 || snapshot.PoolMemberID <= 0 || snapshot.ChannelID <= 0 || snapshot.CredentialID < 0 ||
			snapshot.LastSnapshotRevision == 0 || !validStableModel(snapshot.Model) || snapshot.RequestCount <= 0 {
			continue
		}
		key := StableKey{
			PoolMemberID: snapshot.PoolMemberID,
			CredentialID: snapshot.CredentialID,
			Model:        snapshot.Model,
			BucketTs:     snapshot.BucketTs,
		}
		metadata := stableMetadata{
			poolID:       snapshot.PoolID,
			channelID:    snapshot.ChannelID,
			revision:     snapshot.LastSnapshotRevision,
			poolMemberID: snapshot.PoolMemberID,
			credentialID: snapshot.CredentialID,
			model:        snapshot.Model,
		}
		withWritableStableBucket(store, key, metadata, func(bucket *stableBucket) {
			bucket.addSnapshotLocked(snapshot)
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
	}
}

func beginStableInflight(c *gin.Context, info *relaycommon.RelayInfo, channelID int) func() {
	metadata, ok := stableIdentity(c, info, channelID)
	if !ok {
		loadOrCreateStableStore().inflightIdentityDropCount.Add(1)
		return nil
	}
	key := StableInflightKey{
		PoolMemberID: metadata.poolMemberID,
		CredentialID: metadata.credentialID,
		Model:        metadata.model,
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
		PoolMemberID: metadata.poolMemberID,
		CredentialID: metadata.credentialID,
		Model:        metadata.model,
		BucketTs:     bucketStart(now.Unix()),
	}
	store := loadOrCreateStableStore()
	withWritableStableBucket(store, key, metadata, func(bucket *stableBucket) {
		bucket.addLocked(latencyMs, ttftMs, hasTtft, generationMs, outputTokens, success, apiErr, classification)
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
		poolID:               metadata.poolID,
		channelID:            metadata.channelID,
		lastSnapshotRevision: metadata.revision,
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
	if metadata.revision > bucket.lastSnapshotRevision {
		bucket.lastSnapshotRevision = metadata.revision
		if metadata.poolID > 0 {
			bucket.poolID = metadata.poolID
		}
		if metadata.channelID > 0 {
			bucket.channelID = metadata.channelID
		}
		return
	}
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
) {
	bucket.requestCount++
	if success {
		bucket.successCount++
		bucket.reliabilityRequestCount++
	} else {
		bucket.failureCount++
		if unknownStableClassification(classification) {
			bucket.unknownClassificationCount++
		}
		if (classification.Responsibility == routingerror.ResponsibilityProvider ||
			classification.Responsibility == routingerror.ResponsibilityNetwork) &&
			(classification.HealthEffect == routingerror.HealthDegrade ||
				classification.HealthEffect == routingerror.HealthOpen) {
			bucket.reliabilityRequestCount++
			bucket.reliabilityFailureCount++
		}
	}
	bucket.totalLatencyMs += latencyMs
	if hasTtft {
		bucket.ttftSumMs += ttftMs
		bucket.ttftCount++
	}
	if outputTokens > 0 && generationMs > 0 {
		bucket.outputTokens += outputTokens
		bucket.generationMs += generationMs
	}

	statusCode := 0
	if apiErr != nil {
		statusCode = apiErr.SourceStatusCode()
	}
	switch {
	case statusCode == 429:
		bucket.err429++
	case statusCode == 529:
		bucket.err529++
	case statusCode >= 500 && statusCode <= 599:
		bucket.err5xx++
	case statusCode >= 400 && statusCode <= 499:
		bucket.err4xx++
	}
	if retryAfterMs := retryAfterMaxMS(apiErr); retryAfterMs > 0 {
		bucket.retryAfterCount++
		bucket.retryAfterTotalMs += retryAfterMs
	}
}

func (bucket *stableBucket) addSnapshotLocked(snapshot StableSnapshot) {
	bucket.requestCount += snapshot.RequestCount
	bucket.successCount += snapshot.SuccessCount
	bucket.failureCount += snapshot.FailureCount
	bucket.unknownClassificationCount += snapshot.UnknownClassificationCount
	bucket.reliabilityRequestCount += snapshot.ReliabilityRequestCount
	bucket.reliabilityFailureCount += snapshot.ReliabilityFailureCount
	bucket.totalLatencyMs += snapshot.TotalLatencyMs
	bucket.ttftSumMs += snapshot.TtftSumMs
	bucket.ttftCount += snapshot.TtftCount
	bucket.outputTokens += snapshot.OutputTokens
	bucket.generationMs += snapshot.GenerationMs
	bucket.err4xx += snapshot.Err4xx
	bucket.err5xx += snapshot.Err5xx
	bucket.err429 += snapshot.Err429
	bucket.err529 += snapshot.Err529
	bucket.retryAfterCount += snapshot.RetryAfterCount
	bucket.retryAfterTotalMs += snapshot.RetryAfterTotalMs
}

func (bucket *stableBucket) snapshot(key StableKey) StableSnapshot {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	return bucket.snapshotLocked(key)
}

func (bucket *stableBucket) snapshotLocked(key StableKey) StableSnapshot {
	return StableSnapshot{
		PoolID:                     bucket.poolID,
		PoolMemberID:               key.PoolMemberID,
		CredentialID:               key.CredentialID,
		ChannelID:                  bucket.channelID,
		Model:                      key.Model,
		BucketTs:                   key.BucketTs,
		LastSnapshotRevision:       bucket.lastSnapshotRevision,
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
}

func validStableModel(model string) bool {
	return model != "" && utf8.ValidString(model) && len(model) <= maxStableModelBytes && utf8.RuneCountInString(model) <= maxStableModelRunes
}

func unknownStableClassification(classification routingerror.Classification) bool {
	rule := strings.ToLower(strings.TrimSpace(classification.Rule))
	return rule == "" || strings.Contains(rule, "fallback")
}

func stableKeyLess(left StableKey, right StableKey) bool {
	if left.BucketTs != right.BucketTs {
		return left.BucketTs < right.BucketTs
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
			PoolMemberID: snapshots[i].PoolMemberID,
			CredentialID: snapshots[i].CredentialID,
			Model:        snapshots[i].Model,
			BucketTs:     snapshots[i].BucketTs,
		}
		right := StableKey{
			PoolMemberID: snapshots[j].PoolMemberID,
			CredentialID: snapshots[j].CredentialID,
			Model:        snapshots[j].Model,
			BucketTs:     snapshots[j].BucketTs,
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
