package routingmetrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type bucketKey struct {
	channelID         int
	channelGeneration string
	apiKeyIndex       int
	modelName         string
	group             string
	bucketTs          int64
}

type InflightKey struct {
	ChannelID         int
	ChannelGeneration string
	APIKeyIndex       int
	Model             string
	Group             string
}

type Limits struct {
	MaxBuckets      int
	BucketTTL       time.Duration
	MaxInflightKeys int
}

type Stats struct {
	Buckets         int64
	InflightKeys    int64
	BucketEvictions int64
	InflightDrops   int64
}

type bucket struct {
	mu                      sync.Mutex
	draining                bool
	requestCount            int64
	successCount            int64
	reliabilityRequestCount int64
	reliabilityFailureCount int64
	totalLatencyMs          int64
	latencySamples          []int64
	latencyP95Ms            int64
	ttftSumMs               int64
	ttftCount               int64
	ttftSamples             []int64
	ttftP95Ms               int64
	outputTokens            int64
	generationMs            int64
	err4xx                  int64
	err5xx                  int64
	err429                  int64
	err529                  int64
	retryAfterMaxMs         int64
}

type inflightCounter struct {
	mu      sync.Mutex
	value   atomic.Int64
	retired bool
}

var defaultLimits = Limits{
	MaxBuckets:      20_000,
	BucketTTL:       10 * time.Minute,
	MaxInflightKeys: 20_000,
}

var buckets sync.Map
var inflight sync.Map

// maintenanceMu serializes entry creation/removal and limit changes. When an
// entry lock is also needed, maintenanceMu must be acquired first.
var maintenanceMu sync.Mutex
var limits = defaultLimits
var bucketCount atomic.Int64
var inflightKeyCount atomic.Int64
var bucketEvictionCount atomic.Int64
var inflightDropCount atomic.Int64

func noopInflightRelease() {}

func BeginInflight(c *gin.Context, info *relaycommon.RelayInfo, channelID int) func() {
	if !smart_routing_setting.Enabled() {
		return noopInflightRelease
	}
	releases := make([]func(), 0, 2)
	if release := beginStableInflight(c, info, channelID); release != nil {
		releases = append(releases, release)
	}
	if !info.CurrentAttemptIsMultiKey(c) {
		if key, ok := inflightKey(c, info, channelID); ok {
			if counter, acquired := acquireInflightCounter(key); acquired {
				releases = append(releases, func() {
					releaseInflightCounter(key, counter)
				})
			}
		}
	}
	if len(releases) == 0 {
		return noopInflightRelease
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, release := range releases {
				release()
			}
		})
	}
}

func InflightCount(key InflightKey) int64 {
	value, ok := inflight.Load(key)
	if !ok {
		return 0
	}
	count := value.(*inflightCounter).value.Load()
	if count < 0 {
		return 0
	}
	return count
}

func RuntimeStats() Stats {
	return Stats{
		Buckets:         bucketCount.Load(),
		InflightKeys:    inflightKeyCount.Load(),
		BucketEvictions: bucketEvictionCount.Load(),
		InflightDrops:   inflightDropCount.Load(),
	}
}

func RecordClassifiedAttempt(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channelID int,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	if !smart_routing_setting.Enabled() {
		return
	}
	if info == nil {
		recordStableClassifiedAttempt(c, info, channelID, time.Now(), 0, 0, false, 0, 0, success, apiErr, classification)
		return
	}
	now := info.RoutingAttemptEndTime()
	if now.IsZero() {
		now = time.Now()
	}
	attemptStart := info.RoutingAttemptStartTime()
	if attemptStart.IsZero() {
		attemptStart = now
	}

	latencyMs := now.Sub(attemptStart).Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}
	ttftMs := int64(0)
	hasTtft := info.IsStream && info.FirstResponseTime.After(attemptStart)
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(attemptStart).Milliseconds()
		if ttftMs < 0 {
			ttftMs = 0
		}
	}
	generationMs := latencyMs
	if hasTtft {
		generationMs = now.Sub(info.FirstResponseTime).Milliseconds()
		if generationMs < 0 {
			generationMs = 0
		}
	}

	outputTokens := info.RoutingOutputTokens()
	recordEndpointClassifiedAttempt(c, info, now, latencyMs, ttftMs, hasTtft, success, apiErr, classification)
	recordStableClassifiedAttempt(
		c,
		info,
		channelID,
		now,
		latencyMs,
		ttftMs,
		hasTtft,
		generationMs,
		outputTokens,
		success,
		apiErr,
		classification,
	)
	if info.CurrentAttemptIsMultiKey(c) {
		return
	}

	key, ok := attemptBucketKey(c, info, channelID, now)
	if !ok {
		return
	}
	recordBucket(key, latencyMs, ttftMs, hasTtft, generationMs, outputTokens, success, apiErr, classification)
}

func Snapshots() []model.RoutingChannelMetric {
	snapshots := make([]model.RoutingChannelMetric, 0)
	buckets.Range(func(key any, value any) bool {
		k := key.(bucketKey)
		snapshot := value.(*bucket).snapshot(k)
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
		}
		return true
	})
	sortRoutingMetrics(snapshots)
	return snapshots
}

func DrainSnapshots() []model.RoutingChannelMetric {
	snapshots := make([]model.RoutingChannelMetric, 0)
	maintenanceMu.Lock()
	buckets.Range(func(key any, value any) bool {
		k := key.(bucketKey)
		b := value.(*bucket)
		b.mu.Lock()
		b.draining = true
		snapshot := b.snapshotLocked(k)
		deleted := buckets.CompareAndDelete(key, value)
		b.mu.Unlock()
		if !deleted {
			return true
		}
		decrementCount(&bucketCount)
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
		}
		return true
	})
	maintenanceMu.Unlock()
	sortRoutingMetrics(snapshots)
	return snapshots
}

func RequeueSnapshots(snapshots []model.RoutingChannelMetric) {
	for i := range snapshots {
		snapshot := snapshots[i]
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" || snapshot.RequestCount <= 0 {
			continue
		}
		recordSnapshot(snapshot)
	}
}

func ClearChannel(channelID int) {
	ClearLegacyChannel(channelID)
}

// ClearLegacyChannel removes only the index-based compatibility state. Stable
// member/credential deltas have an independent persistence lifecycle and must
// not be discarded when a legacy cost binding changes.
func ClearLegacyChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	maintenanceMu.Lock()
	buckets.Range(func(key any, value any) bool {
		if k, ok := key.(bucketKey); ok && k.channelID == channelID {
			removeBucketLocked(k, value.(*bucket), false)
		}
		return true
	})
	inflight.Range(func(key any, value any) bool {
		if k, ok := key.(InflightKey); ok && k.ChannelID == channelID {
			removeInflightLocked(k, value.(*inflightCounter))
		}
		return true
	})
	maintenanceMu.Unlock()
}

func ClearStableChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	clearStableChannel(channelID)
}

func ResetForTest() {
	maintenanceMu.Lock()
	buckets.Range(func(key any, value any) bool {
		removeBucketLocked(key.(bucketKey), value.(*bucket), false)
		return true
	})
	inflight.Range(func(key any, value any) bool {
		removeInflightLocked(key.(InflightKey), value.(*inflightCounter))
		return true
	})
	bucketCount.Store(0)
	inflightKeyCount.Store(0)
	bucketEvictionCount.Store(0)
	inflightDropCount.Store(0)
	limits = defaultLimits
	maintenanceMu.Unlock()
	resetStableForTest()
	resetEndpointMetricsForTest()
}

func recordBucket(
	key bucketKey,
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	generationMs int64,
	outputTokens int64,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	withWritableBucket(key, func(b *bucket) {
		b.addLocked(latencyMs, ttftMs, hasTtft, generationMs, outputTokens, success, apiErr, classification)
	})
}

func recordSnapshot(snapshot model.RoutingChannelMetric) {
	key := bucketKey{
		channelID:         snapshot.ChannelID,
		channelGeneration: snapshot.ChannelGeneration,
		apiKeyIndex:       snapshot.APIKeyIndex,
		modelName:         snapshot.ModelName,
		group:             snapshot.Group,
		bucketTs:          snapshot.BucketTs,
	}
	withWritableBucket(key, func(b *bucket) {
		b.addSnapshotLocked(snapshot)
	})
}

func withWritableBucket(key bucketKey, write func(*bucket)) {
	for {
		actual, ok := buckets.Load(key)
		if !ok {
			b := loadOrCreateBucket(key)
			if b == nil {
				return
			}
			actual = b
		}
		b := actual.(*bucket)
		b.mu.Lock()
		if b.draining {
			b.mu.Unlock()
			continue
		}
		write(b)
		b.mu.Unlock()
		return
	}
}

func loadOrCreateBucket(key bucketKey) *bucket {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()

	if actual, ok := buckets.Load(key); ok {
		return actual.(*bucket)
	}

	activeLimits := normalizedLimits(limits)
	limits = activeLimits
	ttlSeconds := int64(activeLimits.BucketTTL / time.Second)
	if activeLimits.BucketTTL%time.Second != 0 {
		ttlSeconds++
	}
	const minBucketTimestamp int64 = -1 << 63
	if key.bucketTs >= minBucketTimestamp+ttlSeconds {
		evictExpiredBucketsLocked(key.bucketTs - ttlSeconds)
	}
	for bucketCount.Load() >= int64(activeLimits.MaxBuckets) {
		if !evictOldestBucketLocked() {
			return nil
		}
	}

	b := &bucket{}
	buckets.Store(key, b)
	bucketCount.Add(1)
	return b
}

func evictExpiredBucketsLocked(cutoff int64) {
	buckets.Range(func(key any, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs <= cutoff {
			removeBucketLocked(k, value.(*bucket), true)
		}
		return true
	})
}

func evictOldestBucketLocked() bool {
	var oldestKey bucketKey
	var oldestBucket *bucket
	buckets.Range(func(key any, value any) bool {
		candidate := key.(bucketKey)
		if oldestBucket == nil || bucketKeyLess(candidate, oldestKey) {
			oldestKey = candidate
			oldestBucket = value.(*bucket)
		}
		return true
	})
	if oldestBucket == nil {
		return false
	}
	return removeBucketLocked(oldestKey, oldestBucket, true)
}

func removeBucketLocked(key bucketKey, b *bucket, eviction bool) bool {
	b.mu.Lock()
	b.draining = true
	deleted := buckets.CompareAndDelete(key, b)
	b.mu.Unlock()
	if !deleted {
		return false
	}
	decrementCount(&bucketCount)
	if eviction {
		bucketEvictionCount.Add(1)
	}
	return true
}

func bucketKeyLess(left bucketKey, right bucketKey) bool {
	if left.bucketTs != right.bucketTs {
		return left.bucketTs < right.bucketTs
	}
	if left.channelID != right.channelID {
		return left.channelID < right.channelID
	}
	if left.apiKeyIndex != right.apiKeyIndex {
		return left.apiKeyIndex < right.apiKeyIndex
	}
	if left.modelName != right.modelName {
		return left.modelName < right.modelName
	}
	return left.group < right.group
}

func acquireInflightCounter(key InflightKey) (*inflightCounter, bool) {
	for {
		if actual, ok := inflight.Load(key); ok {
			counter := actual.(*inflightCounter)
			counter.mu.Lock()
			if counter.retired {
				counter.mu.Unlock()
				continue
			}
			counter.value.Add(1)
			counter.mu.Unlock()
			return counter, true
		}

		maintenanceMu.Lock()
		if _, ok := inflight.Load(key); ok {
			maintenanceMu.Unlock()
			continue
		}
		activeLimits := normalizedLimits(limits)
		limits = activeLimits
		if inflightKeyCount.Load() >= int64(activeLimits.MaxInflightKeys) {
			inflightDropCount.Add(1)
			maintenanceMu.Unlock()
			return nil, false
		}
		counter := &inflightCounter{}
		counter.value.Store(1)
		inflight.Store(key, counter)
		inflightKeyCount.Add(1)
		maintenanceMu.Unlock()
		return counter, true
	}
}

func releaseInflightCounter(key InflightKey, counter *inflightCounter) {
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

	// Retire the final reference under the maintenance lock. Releasing the
	// counter lock first preserves lock order; the count is checked again below.
	maintenanceMu.Lock()
	counter.mu.Lock()
	defer counter.mu.Unlock()
	defer maintenanceMu.Unlock()
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
	if inflight.CompareAndDelete(key, counter) {
		decrementCount(&inflightKeyCount)
	}
}

func removeInflightLocked(key InflightKey, counter *inflightCounter) bool {
	counter.mu.Lock()
	counter.retired = true
	deleted := inflight.CompareAndDelete(key, counter)
	counter.mu.Unlock()
	if deleted {
		decrementCount(&inflightKeyCount)
	}
	return deleted
}

func normalizedLimits(value Limits) Limits {
	if value.MaxBuckets <= 0 {
		value.MaxBuckets = defaultLimits.MaxBuckets
	}
	if value.BucketTTL <= 0 {
		value.BucketTTL = defaultLimits.BucketTTL
	}
	if value.MaxInflightKeys <= 0 {
		value.MaxInflightKeys = defaultLimits.MaxInflightKeys
	}
	return value
}

func decrementCount(counter *atomic.Int64) {
	for {
		current := counter.Load()
		if current <= 0 {
			return
		}
		if counter.CompareAndSwap(current, current-1) {
			return
		}
	}
}

func (b *bucket) add(
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	generationMs int64,
	outputTokens int64,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addLocked(latencyMs, ttftMs, hasTtft, generationMs, outputTokens, success, apiErr, classification)
}

func (b *bucket) addLocked(
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	generationMs int64,
	outputTokens int64,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	b.requestCount++
	if success {
		b.successCount++
		b.reliabilityRequestCount++
	} else if (classification.Responsibility == routingerror.ResponsibilityProvider ||
		(classification.Responsibility == routingerror.ResponsibilityNetwork && classification.Scope != routingerror.ScopeEndpoint)) &&
		(classification.HealthEffect == routingerror.HealthDegrade ||
			classification.HealthEffect == routingerror.HealthOpen) {
		b.reliabilityRequestCount++
		b.reliabilityFailureCount++
	}
	b.totalLatencyMs += latencyMs
	b.latencySamples = appendBoundedSample(b.latencySamples, latencyMs)
	if hasTtft {
		b.ttftSumMs += ttftMs
		b.ttftCount++
		b.ttftSamples = appendBoundedSample(b.ttftSamples, ttftMs)
	}
	if outputTokens > 0 && generationMs > 0 {
		b.outputTokens += outputTokens
		b.generationMs += generationMs
	}

	statusCode := 0
	if apiErr != nil {
		statusCode = apiErr.SourceStatusCode()
	}
	switch {
	case statusCode == 429:
		b.err429++
	case statusCode == 529:
		b.err529++
	case statusCode >= 500 && statusCode <= 599:
		b.err5xx++
	case statusCode >= 400 && statusCode <= 499:
		b.err4xx++
	}
	if retryAfterMs := retryAfterMaxMS(apiErr); retryAfterMs > b.retryAfterMaxMs {
		b.retryAfterMaxMs = retryAfterMs
	}
}

func (b *bucket) addSnapshot(snapshot model.RoutingChannelMetric) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addSnapshotLocked(snapshot)
}

func (b *bucket) addSnapshotLocked(snapshot model.RoutingChannelMetric) {
	b.requestCount += snapshot.RequestCount
	b.successCount += snapshot.SuccessCount
	b.reliabilityRequestCount += snapshot.ReliabilityRequestCount
	b.reliabilityFailureCount += snapshot.ReliabilityFailureCount
	b.totalLatencyMs += snapshot.TotalLatencyMs
	if snapshot.LatencyP95Ms > b.latencyP95Ms {
		b.latencyP95Ms = snapshot.LatencyP95Ms
	}
	b.ttftSumMs += snapshot.TtftSumMs
	b.ttftCount += snapshot.TtftCount
	if snapshot.TtftP95Ms > b.ttftP95Ms {
		b.ttftP95Ms = snapshot.TtftP95Ms
	}
	b.outputTokens += snapshot.OutputTokens
	b.generationMs += snapshot.GenerationMs
	b.err4xx += snapshot.Err4xx
	b.err5xx += snapshot.Err5xx
	b.err429 += snapshot.Err429
	b.err529 += snapshot.Err529
	if snapshot.RetryAfterMaxMs > b.retryAfterMaxMs {
		b.retryAfterMaxMs = snapshot.RetryAfterMaxMs
	}
}

func retryAfterMaxMS(apiErr *types.NewAPIError) int64 {
	if apiErr == nil || len(apiErr.Metadata) == 0 {
		return 0
	}
	var metadata struct {
		RetryAfterMS int64 `json:"retry_after_ms"`
	}
	if err := common.Unmarshal(apiErr.Metadata, &metadata); err != nil || metadata.RetryAfterMS <= 0 {
		return 0
	}
	return metadata.RetryAfterMS
}

func (b *bucket) snapshot(key bucketKey) model.RoutingChannelMetric {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked(key)
}

func (b *bucket) snapshotLocked(key bucketKey) model.RoutingChannelMetric {
	return model.RoutingChannelMetric{
		ChannelID:               key.channelID,
		ChannelGeneration:       key.channelGeneration,
		APIKeyIndex:             key.apiKeyIndex,
		ModelName:               key.modelName,
		Group:                   key.group,
		BucketTs:                key.bucketTs,
		RequestCount:            b.requestCount,
		SuccessCount:            b.successCount,
		ReliabilityRequestCount: b.reliabilityRequestCount,
		ReliabilityFailureCount: b.reliabilityFailureCount,
		TotalLatencyMs:          b.totalLatencyMs,
		LatencyP95Ms:            b.latencyP95(),
		TtftSumMs:               b.ttftSumMs,
		TtftCount:               b.ttftCount,
		TtftP95Ms:               b.ttftP95(),
		OutputTokens:            b.outputTokens,
		GenerationMs:            b.generationMs,
		Err4xx:                  b.err4xx,
		Err5xx:                  b.err5xx,
		Err429:                  b.err429,
		Err529:                  b.err529,
		RetryAfterMaxMs:         b.retryAfterMaxMs,
	}
}

func (b *bucket) latencyP95() int64 {
	if len(b.latencySamples) > 0 {
		return percentileNearestRank(b.latencySamples, 0.95)
	}
	return b.latencyP95Ms
}

func (b *bucket) ttftP95() int64 {
	if len(b.ttftSamples) > 0 {
		return percentileNearestRank(b.ttftSamples, 0.95)
	}
	return b.ttftP95Ms
}

func inflightKey(c *gin.Context, info *relaycommon.RelayInfo, channelID int) (InflightKey, bool) {
	if info == nil {
		return InflightKey{}, false
	}
	if channelID <= 0 && info.ChannelMeta != nil {
		channelID = info.ChannelMeta.ChannelId
	}
	if channelID <= 0 || info.OriginModelName == "" {
		return InflightKey{}, false
	}
	group := info.UsingGroup
	if group == "" && c != nil {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	if group == "" {
		group = "default"
	}
	channelGeneration := ""
	if c != nil {
		channelGeneration = common.GetContextKeyString(c, constant.ContextKeyRoutingGeneration)
	}
	if channelGeneration == "" && info.ChannelMeta != nil {
		channelGeneration = info.ChannelMeta.RoutingGeneration
	}
	return InflightKey{
		ChannelID:         channelID,
		ChannelGeneration: channelGeneration,
		APIKeyIndex:       model.RoutingMetricSingleKeyIndex,
		Model:             info.OriginModelName,
		Group:             group,
	}, true
}

func appendBoundedSample(samples []int64, value int64) []int64 {
	const maxSamples = 128
	if value < 0 {
		value = 0
	}
	if len(samples) >= maxSamples {
		copy(samples, samples[1:])
		samples[len(samples)-1] = value
		return samples
	}
	return append(samples, value)
}

func percentileNearestRank(samples []int64, percentile float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	copySamples := append([]int64(nil), samples...)
	sort.Slice(copySamples, func(i, j int) bool {
		return copySamples[i] < copySamples[j]
	})
	index := int(percentile*float64(len(copySamples))+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copySamples) {
		index = len(copySamples) - 1
	}
	return copySamples[index]
}

func attemptBucketKey(c *gin.Context, info *relaycommon.RelayInfo, channelID int, now time.Time) (bucketKey, bool) {
	key, ok := inflightKey(c, info, channelID)
	if !ok {
		return bucketKey{}, false
	}
	return bucketKey{
		channelID:         key.ChannelID,
		channelGeneration: key.ChannelGeneration,
		apiKeyIndex:       key.APIKeyIndex,
		modelName:         key.Model,
		group:             key.Group,
		bucketTs:          bucketStart(now.Unix()),
	}, true
}

func bucketStart(ts int64) int64 {
	bucketSeconds := int64(smart_routing_setting.GetSetting().MetricBucketSec)
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	return ts - ts%bucketSeconds
}

func sortRoutingMetrics(metrics []model.RoutingChannelMetric) {
	sort.Slice(metrics, func(i, j int) bool {
		left := metrics[i]
		right := metrics[j]
		if left.ChannelID != right.ChannelID {
			return left.ChannelID < right.ChannelID
		}
		if left.ChannelGeneration != right.ChannelGeneration {
			return left.ChannelGeneration < right.ChannelGeneration
		}
		if left.APIKeyIndex != right.APIKeyIndex {
			return left.APIKeyIndex < right.APIKeyIndex
		}
		if left.ModelName != right.ModelName {
			return left.ModelName < right.ModelName
		}
		if left.Group != right.Group {
			return left.Group < right.Group
		}
		return left.BucketTs < right.BucketTs
	})
}
