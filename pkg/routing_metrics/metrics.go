package routingmetrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type bucketKey struct {
	channelID   int
	apiKeyIndex int
	modelName   string
	group       string
	bucketTs    int64
}

type InflightKey struct {
	ChannelID   int
	APIKeyIndex int
	Model       string
	Group       string
}

type bucket struct {
	mu              sync.Mutex
	draining        bool
	requestCount    int64
	successCount    int64
	totalLatencyMs  int64
	latencySamples  []int64
	latencyP95Ms    int64
	ttftSumMs       int64
	ttftCount       int64
	ttftSamples     []int64
	ttftP95Ms       int64
	outputTokens    int64
	generationMs    int64
	err4xx          int64
	err5xx          int64
	err429          int64
	retryAfterMaxMs int64
}

var buckets sync.Map
var inflight sync.Map

func BeginInflight(c *gin.Context, info *relaycommon.RelayInfo, channelID int) func() {
	key, ok := inflightKey(c, info, channelID)
	if !ok {
		return func() {}
	}
	keys := []InflightKey{key}
	if key.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
		aggregate := key
		aggregate.APIKeyIndex = model.RoutingMetricSingleKeyIndex
		keys = append(keys, aggregate)
	}
	for _, item := range keys {
		inflightCounter(item).Add(1)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, item := range keys {
				counter := inflightCounter(item)
				if counter.Add(-1) <= 0 {
					inflight.Delete(item)
				}
			}
		})
	}
}

func InflightCount(key InflightKey) int64 {
	value, ok := inflight.Load(key)
	if !ok {
		return 0
	}
	count := value.(*atomic.Int64).Load()
	if count < 0 {
		return 0
	}
	return count
}

func inflightCounter(key InflightKey) *atomic.Int64 {
	actual, _ := inflight.LoadOrStore(key, &atomic.Int64{})
	return actual.(*atomic.Int64)
}

func RecordAttempt(c *gin.Context, info *relaycommon.RelayInfo, channelID int, apiErr *types.NewAPIError) {
	now := time.Now()
	key, ok := attemptBucketKey(c, info, channelID, now)
	if !ok {
		return
	}

	latencyMs := now.Sub(info.StartTime).Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}
	ttftMs := int64(0)
	hasTtft := info.IsStream && info.HasSendResponse()
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
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

	recordBucket(key, latencyMs, ttftMs, hasTtft, generationMs, apiErr)
	if key.apiKeyIndex != model.RoutingMetricSingleKeyIndex {
		key.apiKeyIndex = model.RoutingMetricSingleKeyIndex
		recordBucket(key, latencyMs, ttftMs, hasTtft, generationMs, apiErr)
	}
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
	buckets.Range(func(key any, value any) bool {
		k := key.(bucketKey)
		b := value.(*bucket)
		b.mu.Lock()
		b.draining = true
		snapshot := b.snapshotLocked(k)
		b.mu.Unlock()
		if snapshot.RequestCount > 0 {
			snapshots = append(snapshots, snapshot)
		}
		buckets.CompareAndDelete(key, value)
		return true
	})
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
	if channelID <= 0 {
		return
	}
	buckets.Range(func(key any, value any) bool {
		if k, ok := key.(bucketKey); ok && k.channelID == channelID {
			buckets.Delete(key)
		}
		return true
	})
	inflight.Range(func(key any, value any) bool {
		if k, ok := key.(InflightKey); ok && k.ChannelID == channelID {
			inflight.Delete(key)
		}
		return true
	})
}

func ResetForTest() {
	buckets.Range(func(key any, value any) bool {
		buckets.Delete(key)
		return true
	})
	inflight.Range(func(key any, value any) bool {
		inflight.Delete(key)
		return true
	})
}

func recordBucket(key bucketKey, latencyMs int64, ttftMs int64, hasTtft bool, generationMs int64, apiErr *types.NewAPIError) {
	withWritableBucket(key, func(b *bucket) {
		b.addLocked(latencyMs, ttftMs, hasTtft, generationMs, apiErr)
	})
}

func recordSnapshot(snapshot model.RoutingChannelMetric) {
	key := bucketKey{
		channelID:   snapshot.ChannelID,
		apiKeyIndex: snapshot.APIKeyIndex,
		modelName:   snapshot.ModelName,
		group:       snapshot.Group,
		bucketTs:    snapshot.BucketTs,
	}
	withWritableBucket(key, func(b *bucket) {
		b.addSnapshotLocked(snapshot)
	})
}

func withWritableBucket(key bucketKey, write func(*bucket)) {
	for {
		actual, _ := buckets.LoadOrStore(key, &bucket{})
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

func (b *bucket) add(latencyMs int64, ttftMs int64, hasTtft bool, generationMs int64, apiErr *types.NewAPIError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addLocked(latencyMs, ttftMs, hasTtft, generationMs, apiErr)
}

func (b *bucket) addLocked(latencyMs int64, ttftMs int64, hasTtft bool, generationMs int64, apiErr *types.NewAPIError) {
	b.requestCount++
	if apiErr == nil {
		b.successCount++
	}
	b.totalLatencyMs += latencyMs
	b.latencySamples = appendBoundedSample(b.latencySamples, latencyMs)
	if hasTtft {
		b.ttftSumMs += ttftMs
		b.ttftCount++
		b.ttftSamples = appendBoundedSample(b.ttftSamples, ttftMs)
	}
	b.generationMs += generationMs

	statusCode := 0
	if apiErr != nil {
		statusCode = apiErr.StatusCode
	}
	switch {
	case statusCode == 429:
		b.err429++
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
		ChannelID:       key.channelID,
		APIKeyIndex:     key.apiKeyIndex,
		ModelName:       key.modelName,
		Group:           key.group,
		BucketTs:        key.bucketTs,
		RequestCount:    b.requestCount,
		SuccessCount:    b.successCount,
		TotalLatencyMs:  b.totalLatencyMs,
		LatencyP95Ms:    b.latencyP95(),
		TtftSumMs:       b.ttftSumMs,
		TtftCount:       b.ttftCount,
		TtftP95Ms:       b.ttftP95(),
		OutputTokens:    b.outputTokens,
		GenerationMs:    b.generationMs,
		Err4xx:          b.err4xx,
		Err5xx:          b.err5xx,
		Err429:          b.err429,
		RetryAfterMaxMs: b.retryAfterMaxMs,
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

func apiKeyIndex(info *relaycommon.RelayInfo) int {
	if info == nil || info.ChannelMeta == nil || !info.ChannelMeta.ChannelIsMultiKey {
		return model.RoutingMetricSingleKeyIndex
	}
	return info.ChannelMeta.ChannelMultiKeyIndex
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
	return InflightKey{
		ChannelID:   channelID,
		APIKeyIndex: apiKeyIndex(info),
		Model:       info.OriginModelName,
		Group:       group,
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
		channelID:   key.ChannelID,
		apiKeyIndex: key.APIKeyIndex,
		modelName:   key.Model,
		group:       key.Group,
		bucketTs:    bucketStart(now.Unix()),
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
