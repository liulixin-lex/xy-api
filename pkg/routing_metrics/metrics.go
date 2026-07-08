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
	requestCount    int64
	successCount    int64
	totalLatencyMs  int64
	ttftSumMs       int64
	ttftCount       int64
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
	counter := inflightCounter(key)
	counter.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			if counter.Add(-1) <= 0 {
				inflight.Delete(key)
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

	actual, _ := buckets.LoadOrStore(key, &bucket{})
	actual.(*bucket).add(latencyMs, ttftMs, hasTtft, generationMs, apiErr)
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
	snapshots := Snapshots()
	buckets.Range(func(key any, value any) bool {
		buckets.Delete(key)
		return true
	})
	return snapshots
}

func RequeueSnapshots(snapshots []model.RoutingChannelMetric) {
	for i := range snapshots {
		snapshot := snapshots[i]
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" || snapshot.RequestCount <= 0 {
			continue
		}
		key := bucketKey{
			channelID:   snapshot.ChannelID,
			apiKeyIndex: snapshot.APIKeyIndex,
			modelName:   snapshot.ModelName,
			group:       snapshot.Group,
			bucketTs:    snapshot.BucketTs,
		}
		actual, _ := buckets.LoadOrStore(key, &bucket{})
		actual.(*bucket).addSnapshot(snapshot)
	}
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

func (b *bucket) add(latencyMs int64, ttftMs int64, hasTtft bool, generationMs int64, apiErr *types.NewAPIError) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.requestCount++
	if apiErr == nil {
		b.successCount++
	}
	b.totalLatencyMs += latencyMs
	if hasTtft {
		b.ttftSumMs += ttftMs
		b.ttftCount++
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

	b.requestCount += snapshot.RequestCount
	b.successCount += snapshot.SuccessCount
	b.totalLatencyMs += snapshot.TotalLatencyMs
	b.ttftSumMs += snapshot.TtftSumMs
	b.ttftCount += snapshot.TtftCount
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
	return model.RoutingChannelMetric{
		ChannelID:       key.channelID,
		APIKeyIndex:     key.apiKeyIndex,
		ModelName:       key.modelName,
		Group:           key.group,
		BucketTs:        key.bucketTs,
		RequestCount:    b.requestCount,
		SuccessCount:    b.successCount,
		TotalLatencyMs:  b.totalLatencyMs,
		TtftSumMs:       b.ttftSumMs,
		TtftCount:       b.ttftCount,
		OutputTokens:    b.outputTokens,
		GenerationMs:    b.generationMs,
		Err4xx:          b.err4xx,
		Err5xx:          b.err5xx,
		Err429:          b.err429,
		RetryAfterMaxMs: b.retryAfterMaxMs,
	}
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
