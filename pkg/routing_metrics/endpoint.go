package routingmetrics

import (
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	endpointMetricShardCount = 64
	endpointMetricShardLimit = 320
	endpointMetricTTL        = 10 * time.Minute
)

type EndpointKey struct {
	EndpointAuthority string `json:"endpoint_authority"`
	Region            string `json:"region"`
	BucketTs          int64  `json:"bucket_ts"`
	ResetGeneration   int64  `json:"reset_generation"`
}

type EndpointSnapshot struct {
	EndpointHost        string `json:"endpoint_host"`
	EndpointAuthority   string `json:"endpoint_authority"`
	Region              string `json:"region"`
	BucketTs            int64  `json:"bucket_ts"`
	ResetGeneration     int64  `json:"reset_generation"`
	RequestCount        int64  `json:"request_count"`
	ReachableCount      int64  `json:"reachable_count"`
	NetworkFailureCount int64  `json:"network_failure_count"`
	TotalLatencyMs      int64  `json:"total_latency_ms"`
	TtftSumMs           int64  `json:"ttft_sum_ms"`
	TtftCount           int64  `json:"ttft_count"`
}

type EndpointStats struct {
	Buckets   int64 `json:"buckets"`
	Evictions int64 `json:"evictions"`
	Drops     int64 `json:"drops"`
}

type endpointBucket struct {
	endpointHost        string
	requestCount        int64
	reachableCount      int64
	networkFailureCount int64
	totalLatencyMs      int64
	ttftSumMs           int64
	ttftCount           int64
}

type endpointShard struct {
	sync.Mutex
	buckets map[EndpointKey]*endpointBucket
}

var endpointMetrics = struct {
	shards    [endpointMetricShardCount]endpointShard
	buckets   atomic.Int64
	evictions atomic.Int64
	drops     atomic.Int64
}{}

func EndpointSnapshots() []EndpointSnapshot {
	snapshots := make([]EndpointSnapshot, 0, endpointMetrics.buckets.Load())
	cutoff := bucketStart(time.Now().Add(-endpointMetricTTL).Unix())
	for index := range endpointMetrics.shards {
		shard := &endpointMetrics.shards[index]
		shard.Lock()
		for key, bucket := range shard.buckets {
			if key.BucketTs < cutoff {
				delete(shard.buckets, key)
				decrementCount(&endpointMetrics.buckets)
				endpointMetrics.evictions.Add(1)
				continue
			}
			snapshots = append(snapshots, endpointSnapshot(key, bucket))
		}
		shard.Unlock()
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].BucketTs != snapshots[j].BucketTs {
			return snapshots[i].BucketTs < snapshots[j].BucketTs
		}
		if snapshots[i].EndpointAuthority != snapshots[j].EndpointAuthority {
			return snapshots[i].EndpointAuthority < snapshots[j].EndpointAuthority
		}
		if snapshots[i].Region != snapshots[j].Region {
			return snapshots[i].Region < snapshots[j].Region
		}
		return snapshots[i].ResetGeneration < snapshots[j].ResetGeneration
	})
	return snapshots
}

func EndpointRuntimeStats() EndpointStats {
	return EndpointStats{
		Buckets: endpointMetrics.buckets.Load(), Evictions: endpointMetrics.evictions.Load(), Drops: endpointMetrics.drops.Load(),
	}
}

func ClearEndpoint(endpointAuthority string, region string) {
	endpointAuthority = strings.ToLower(strings.TrimSpace(endpointAuthority))
	region = strings.ToLower(strings.TrimSpace(region))
	if endpointAuthority == "" || region == "" {
		return
	}
	key := EndpointKey{EndpointAuthority: endpointAuthority, Region: region}
	shard := &endpointMetrics.shards[endpointShardIndex(key)]
	shard.Lock()
	for candidate := range shard.buckets {
		if candidate.EndpointAuthority == endpointAuthority && candidate.Region == region {
			delete(shard.buckets, candidate)
			decrementCount(&endpointMetrics.buckets)
		}
	}
	shard.Unlock()
}

func recordEndpointClassifiedAttempt(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	now time.Time,
	latencyMs int64,
	ttftMs int64,
	hasTtft bool,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	if !smart_routing_setting.Enabled() {
		return
	}
	host, authority, region, ok := endpointIdentity(c, info)
	if !ok {
		endpointMetrics.drops.Add(1)
		return
	}
	sourceStatusCode := 0
	if apiErr != nil {
		sourceStatusCode = apiErr.SourceStatusCode()
	}
	upstreamCallerStatus := sourceStatusCode > 0 && classification.Responsibility == routingerror.ResponsibilityCaller &&
		strings.EqualFold(strings.TrimSpace(classification.Rule), "caller_status")
	networkEvaluated := success || upstreamCallerStatus ||
		classification.Responsibility == routingerror.ResponsibilityProvider ||
		classification.Responsibility == routingerror.ResponsibilityCapacity ||
		classification.Responsibility == routingerror.ResponsibilityCredential ||
		classification.Responsibility == routingerror.ResponsibilityNetwork
	if !networkEvaluated {
		return
	}
	breakerKey := routingbreaker.NewEndpointKey(authority, region)
	key := EndpointKey{
		EndpointAuthority: authority, Region: region, BucketTs: bucketStart(now.Unix()),
		ResetGeneration: routingbreaker.DefaultResetGeneration(breakerKey),
	}
	shard := &endpointMetrics.shards[endpointShardIndex(key)]
	shard.Lock()
	if shard.buckets == nil {
		shard.buckets = make(map[EndpointKey]*endpointBucket)
	}
	cutoff := key.BucketTs - int64(endpointMetricTTL/time.Second)
	for candidate := range shard.buckets {
		if candidate.BucketTs < cutoff {
			delete(shard.buckets, candidate)
			decrementCount(&endpointMetrics.buckets)
			endpointMetrics.evictions.Add(1)
		}
	}
	bucket := shard.buckets[key]
	if bucket == nil {
		if len(shard.buckets) >= endpointMetricShardLimit {
			oldest, found := oldestEndpointKey(shard.buckets)
			if !found {
				endpointMetrics.drops.Add(1)
				shard.Unlock()
				return
			}
			delete(shard.buckets, oldest)
			decrementCount(&endpointMetrics.buckets)
			endpointMetrics.evictions.Add(1)
		}
		bucket = &endpointBucket{endpointHost: host}
		shard.buckets[key] = bucket
		endpointMetrics.buckets.Add(1)
	}
	if bucket.requestCount >= maxStableBucketCount {
		endpointMetrics.drops.Add(1)
		shard.Unlock()
		return
	}
	bucket.requestCount++
	if success || upstreamCallerStatus ||
		classification.Responsibility == routingerror.ResponsibilityProvider ||
		classification.Responsibility == routingerror.ResponsibilityCapacity ||
		classification.Responsibility == routingerror.ResponsibilityCredential {
		addStableCounter(&bucket.reachableCount, 1, maxStableBucketCount)
	}
	if classification.Responsibility == routingerror.ResponsibilityNetwork &&
		classification.Scope == routingerror.ScopeEndpoint &&
		(classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen) {
		addStableCounter(&bucket.networkFailureCount, 1, maxStableBucketCount)
	}
	boundedLatencyMs, _ := boundedStableValue(latencyMs, maxStableDurationTotalMs)
	addStableCounter(&bucket.totalLatencyMs, boundedLatencyMs, maxStableDurationTotalMs)
	if hasTtft {
		boundedTtftMs, _ := boundedStableValue(ttftMs, maxStableDurationTotalMs)
		addStableCounter(&bucket.ttftSumMs, boundedTtftMs, maxStableDurationTotalMs)
		addStableCounter(&bucket.ttftCount, 1, maxStableBucketCount)
	}
	shard.Unlock()
}

func endpointIdentity(c *gin.Context, info *relaycommon.RelayInfo) (string, string, string, bool) {
	host := ""
	authority := ""
	region := ""
	if c != nil {
		host = common.GetContextKeyString(c, constant.ContextKeyRoutingEndpointHost)
		authority = common.GetContextKeyString(c, constant.ContextKeyRoutingEndpointAuthority)
		region = common.GetContextKeyString(c, constant.ContextKeyRoutingRegion)
	}
	if info != nil && info.ChannelMeta != nil {
		if host == "" {
			host = info.ChannelMeta.RoutingEndpointHost
		}
		if authority == "" {
			authority = info.ChannelMeta.RoutingEndpointAuthority
		}
		if region == "" {
			region = info.ChannelMeta.RoutingRegion
		}
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	authority = strings.ToLower(strings.TrimSpace(authority))
	region = strings.ToLower(strings.TrimSpace(region))
	return host, authority, region, host != "" && len(host) <= 255 && utf8.ValidString(host) &&
		authority != "" && len(authority) <= 320 && utf8.ValidString(authority) &&
		region != "" && len(region) <= 64 && utf8.ValidString(region)
}

func endpointShardIndex(key EndpointKey) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key.EndpointAuthority))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(key.Region))
	return hash.Sum32() % endpointMetricShardCount
}

func oldestEndpointKey(buckets map[EndpointKey]*endpointBucket) (EndpointKey, bool) {
	oldest := EndpointKey{}
	found := false
	for key := range buckets {
		if !found || key.BucketTs < oldest.BucketTs ||
			(key.BucketTs == oldest.BucketTs && (key.EndpointAuthority < oldest.EndpointAuthority ||
				(key.EndpointAuthority == oldest.EndpointAuthority && key.Region < oldest.Region))) {
			oldest = key
			found = true
		}
	}
	return oldest, found
}

func endpointSnapshot(key EndpointKey, bucket *endpointBucket) EndpointSnapshot {
	return EndpointSnapshot{
		EndpointHost: bucket.endpointHost, EndpointAuthority: key.EndpointAuthority,
		Region: key.Region, BucketTs: key.BucketTs, ResetGeneration: key.ResetGeneration,
		RequestCount: bucket.requestCount, ReachableCount: bucket.reachableCount,
		NetworkFailureCount: bucket.networkFailureCount, TotalLatencyMs: bucket.totalLatencyMs,
		TtftSumMs: bucket.ttftSumMs, TtftCount: bucket.ttftCount,
	}
}

func resetEndpointMetricsForTest() {
	for index := range endpointMetrics.shards {
		shard := &endpointMetrics.shards[index]
		shard.Lock()
		shard.buckets = nil
		shard.Unlock()
	}
	endpointMetrics.buckets.Store(0)
	endpointMetrics.evictions.Store(0)
	endpointMetrics.drops.Store(0)
}
