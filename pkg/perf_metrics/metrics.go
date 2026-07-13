package perfmetrics

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

var defaultLimits = Limits{
	MaxBuckets: 20_000,
	BucketTTL:  24 * time.Hour,
}

var hotBuckets sync.Map

// maintenanceMu serializes bucket creation/removal and limit changes. When a
// bucket lock is also needed, maintenanceMu must be acquired first.
var maintenanceMu sync.Mutex
var limits = defaultLimits
var bucketCount atomic.Int64
var bucketEvictionCount atomic.Int64
var evictedSampleCount atomic.Int64
var droppedSampleCount atomic.Int64

// seriesSchema is a stable client cache/schema marker. Do not change it when
// hiding fields or making response-only privacy hardening changes.
const seriesSchema = "dbcd0a3c01b55203"

// Init starts a runtime with a background parent for compatibility. New
// lifecycle-aware callers should use Start and retain the returned Runtime.
func Init() *Runtime {
	return Start(context.Background())
}

func RecordRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	if info == nil {
		return
	}
	now := time.Now()
	hasTtft := info.IsStream && info.HasSendResponse()
	ttftMs := int64(0)
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
	}
	latencyMs := now.Sub(info.StartTime).Milliseconds()
	generationMs := latencyMs
	if hasTtft {
		generationMs = now.Sub(info.FirstResponseTime).Milliseconds()
	}
	if generationMs <= 0 {
		generationMs = latencyMs
	}
	Record(Sample{
		Model:        info.OriginModelName,
		Group:        info.UsingGroup,
		LatencyMs:    latencyMs,
		TtftMs:       ttftMs,
		HasTtft:      hasTtft,
		Success:      success,
		OutputTokens: outputTokens,
		GenerationMs: generationMs,
	})
}

func Record(sample Sample) {
	setting := perf_metrics_setting.GetSetting()
	if !setting.Enabled || sample.Model == "" {
		return
	}
	if sample.Group == "" {
		sample.Group = "default"
	}
	if sample.LatencyMs < 0 {
		sample.LatencyMs = 0
	}

	key := bucketKey{
		model:    sample.Model,
		group:    sample.Group,
		bucketTs: bucketStart(time.Now().Unix()),
	}
	recordSample(key, sample)
}

func RuntimeStats() Stats {
	return Stats{
		Buckets:        bucketCount.Load(),
		DroppedSamples: droppedSampleCount.Load(),
		EvictedBuckets: bucketEvictionCount.Load(),
		EvictedSamples: evictedSampleCount.Load(),
	}
}

func recordSample(key bucketKey, sample Sample) bool {
	return withWritableBucket(key, 1, func(b *bucket) {
		b.addLocked(sample)
	})
}

func withWritableBucket(key bucketKey, droppedSamples int64, write func(*bucket)) bool {
	for {
		actual, ok := hotBuckets.Load(key)
		if !ok {
			created := loadOrCreateBucket(key)
			if created == nil {
				droppedSampleCount.Add(droppedSamples)
				return false
			}
			actual = created
		}
		b := actual.(*bucket)
		b.mu.Lock()
		if b.draining {
			// A bucket being flushed remains in the map as its reserved capacity
			// slot. It stays writable for same-key samples; a detached bucket was
			// removed and must be retried against the current map entry.
			current, ok := hotBuckets.Load(key)
			if !ok || current != b {
				b.mu.Unlock()
				continue
			}
		}
		write(b)
		b.mu.Unlock()
		return true
	}
}

func loadOrCreateBucket(key bucketKey) *bucket {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()

	if actual, ok := hotBuckets.Load(key); ok {
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
		evictStaleBucketsLocked(key.bucketTs - ttlSeconds)
	}
	for bucketCount.Load() >= int64(activeLimits.MaxBuckets) {
		if !evictOldestBucketLocked(key) {
			return nil
		}
	}

	b := &bucket{}
	hotBuckets.Store(key, b)
	bucketCount.Add(1)
	return b
}

func evictStaleBucketsLocked(cutoff int64) {
	hotBuckets.Range(func(key any, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs <= cutoff {
			removeBucketLocked(k, value.(*bucket), true)
		}
		return true
	})
}

func evictOldestBucketLocked(incoming bucketKey) bool {
	var oldestKey bucketKey
	var oldestBucket *bucket
	hotBuckets.Range(func(key any, value any) bool {
		candidate := key.(bucketKey)
		if candidate.bucketTs >= incoming.bucketTs {
			return true
		}
		candidateBucket := value.(*bucket)
		candidateBucket.mu.Lock()
		draining := candidateBucket.draining
		candidateBucket.mu.Unlock()
		if draining {
			return true
		}
		if oldestBucket == nil || bucketKeyLess(candidate, oldestKey) {
			oldestKey = candidate
			oldestBucket = candidateBucket
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
	if eviction && b.draining {
		b.mu.Unlock()
		return false
	}
	b.draining = true
	droppedSamples := b.counters.requestCount
	deleted := hotBuckets.CompareAndDelete(key, b)
	b.mu.Unlock()
	if !deleted {
		return false
	}
	bucketCount.Add(-1)
	if eviction {
		bucketEvictionCount.Add(1)
		evictedSampleCount.Add(droppedSamples)
	}
	return true
}

func bucketKeyLess(left bucketKey, right bucketKey) bool {
	if left.bucketTs != right.bucketTs {
		return left.bucketTs < right.bucketTs
	}
	if left.model != right.model {
		return left.model < right.model
	}
	return left.group < right.group
}

func normalizedLimits(value Limits) Limits {
	if value.MaxBuckets <= 0 {
		value.MaxBuckets = defaultLimits.MaxBuckets
	}
	if value.BucketTTL <= 0 {
		value.BucketTTL = defaultLimits.BucketTTL
	}
	return value
}

func resetForTest() {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()
	hotBuckets.Range(func(key any, value any) bool {
		removeBucketLocked(key.(bucketKey), value.(*bucket), false)
		return true
	})
	bucketCount.Store(0)
	bucketEvictionCount.Store(0)
	evictedSampleCount.Store(0)
	droppedSampleCount.Store(0)
	limits = defaultLimits
}

func Query(params QueryParams) (QueryResult, error) {
	if params.Hours <= 0 {
		params.Hours = 24
	}
	if params.Hours > 24*30 {
		params.Hours = 24 * 30
	}
	endTs := time.Now().Unix()
	startTs := endTs - int64(params.Hours)*3600

	merged := map[bucketKey]counters{}
	rows, err := model.GetPerfMetrics(params.Model, params.Group, startTs, endTs)
	if err != nil {
		return QueryResult{}, err
	}
	for _, row := range rows {
		mergeCounters(merged, bucketKey{
			model:    row.ModelName,
			group:    row.Group,
			bucketTs: row.BucketTs,
		}, counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			ttftSumMs:      row.TtftSumMs,
			ttftCount:      row.TtftCount,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		})
	}

	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.model != params.Model || k.bucketTs < startTs || k.bucketTs > endTs {
			return true
		}
		if params.Group != "" && k.group != params.Group {
			return true
		}
		mergeCounters(merged, k, value.(*bucket).snapshot())
		return true
	})

	return buildQueryResult(params.Model, merged), nil
}

func QuerySummaryAll(hours int, groups []string) (SummaryAllResult, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	endTs := time.Now().Unix()
	startTs := endTs - int64(hours)*3600
	allowedGroups := allowedGroupSet(groups)

	rows, err := model.GetPerfMetricsSummaryBucketsAll(startTs, endTs, groups)
	if err != nil {
		return SummaryAllResult{}, err
	}

	totals := map[string]counters{}
	modelBuckets := map[string]map[int64]counters{}
	for _, row := range rows {
		value := counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		}
		mergeModelTotals(totals, row.ModelName, value)
		mergeModelBucket(modelBuckets, row.ModelName, row.BucketTs, value)
	}

	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs < startTs || k.bucketTs > endTs {
			return true
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[k.group]; !ok {
				return true
			}
		}
		snap := value.(*bucket).snapshot()
		if snap.requestCount == 0 {
			return true
		}
		mergeModelTotals(totals, k.model, snap)
		mergeModelBucket(modelBuckets, k.model, k.bucketTs, snap)
		return true
	})

	models := make([]ModelSummary, 0, len(totals))
	for name, total := range totals {
		if total.requestCount == 0 {
			continue
		}
		avgLatency := total.totalLatencyMs / total.requestCount
		successRate := float64(total.successCount) / float64(total.requestCount) * 100
		avgTps := 0.0
		if total.generationMs > 0 {
			avgTps = float64(total.outputTokens) / (float64(total.generationMs) / 1000.0)
		}
		models = append(models, ModelSummary{
			ModelName:          name,
			AvgLatencyMs:       avgLatency,
			SuccessRate:        math.Round(successRate*100) / 100,
			AvgTps:             math.Round(avgTps*100) / 100,
			RecentSuccessRates: recentSuccessRates(modelBuckets[name], 3),
			RequestCount:       total.requestCount,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].RequestCount > models[j].RequestCount
	})

	return SummaryAllResult{Models: models}, nil
}

func mergeModelTotals(totals map[string]counters, modelName string, value counters) {
	if value.requestCount == 0 {
		return
	}
	current := totals[modelName]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	totals[modelName] = current
}

func mergeModelBucket(modelBuckets map[string]map[int64]counters, modelName string, bucketTs int64, value counters) {
	if value.requestCount == 0 {
		return
	}
	if _, ok := modelBuckets[modelName]; !ok {
		modelBuckets[modelName] = map[int64]counters{}
	}
	current := modelBuckets[modelName][bucketTs]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	modelBuckets[modelName][bucketTs] = current
}

func recentSuccessRates(buckets map[int64]counters, limit int) []float64 {
	if len(buckets) == 0 || limit <= 0 {
		return nil
	}
	timestamps := make([]int64, 0, len(buckets))
	for ts := range buckets {
		timestamps = append(timestamps, ts)
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	if len(timestamps) > limit {
		timestamps = timestamps[len(timestamps)-limit:]
	}
	rates := make([]float64, 0, len(timestamps))
	for _, ts := range timestamps {
		rates = append(rates, math.Round(successRate(buckets[ts])*100)/100)
	}
	return rates
}

func allowedGroupSet(groups []string) map[string]struct{} {
	if groups == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		allowed[group] = struct{}{}
	}
	return allowed
}

func bucketStart(ts int64) int64 {
	bucketSeconds := perf_metrics_setting.GetBucketSeconds()
	if bucketSeconds <= 0 {
		bucketSeconds = 3600
	}
	return ts - (ts % bucketSeconds)
}

func mergeCounters(merged map[bucketKey]counters, key bucketKey, value counters) {
	if value.requestCount == 0 {
		return
	}
	current := merged[key]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	merged[key] = current
}

func buildQueryResult(modelName string, merged map[bucketKey]counters) QueryResult {
	groupBuckets := map[string]map[int64]counters{}
	for key, value := range merged {
		if value.requestCount == 0 {
			continue
		}
		if _, ok := groupBuckets[key.group]; !ok {
			groupBuckets[key.group] = map[int64]counters{}
		}
		groupBuckets[key.group][key.bucketTs] = value
	}

	groups := make([]string, 0, len(groupBuckets))
	for group := range groupBuckets {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	results := make([]GroupResult, 0, len(groups))
	for _, group := range groups {
		buckets := groupBuckets[group]
		timestamps := make([]int64, 0, len(buckets))
		for ts := range buckets {
			timestamps = append(timestamps, ts)
		}
		sort.Slice(timestamps, func(i, j int) bool {
			return timestamps[i] < timestamps[j]
		})

		total := counters{}
		series := make([]BucketPoint, 0, len(timestamps))
		for _, ts := range timestamps {
			value := buckets[ts]
			total.requestCount += value.requestCount
			total.successCount += value.successCount
			total.totalLatencyMs += value.totalLatencyMs
			total.ttftSumMs += value.ttftSumMs
			total.ttftCount += value.ttftCount
			total.outputTokens += value.outputTokens
			total.generationMs += value.generationMs
			series = append(series, bucketPoint(ts, value))
		}

		results = append(results, GroupResult{
			Group:        group,
			AvgTtftMs:    avg(total.ttftSumMs, total.ttftCount),
			AvgLatencyMs: avg(total.totalLatencyMs, total.requestCount),
			SuccessRate:  successRate(total),
			AvgTps:       avgTps(total),
			Series:       series,
		})
	}

	return QueryResult{
		ModelName:    modelName,
		SeriesSchema: seriesSchema,
		Groups:       results,
	}
}

func bucketPoint(ts int64, value counters) BucketPoint {
	return BucketPoint{
		Ts:           ts,
		AvgTtftMs:    avg(value.ttftSumMs, value.ttftCount),
		AvgLatencyMs: avg(value.totalLatencyMs, value.requestCount),
		SuccessRate:  successRate(value),
		AvgTps:       avgTps(value),
	}
}

func avg(sum int64, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return sum / count
}

func successRate(value counters) float64 {
	if value.requestCount <= 0 {
		return 0
	}
	return float64(value.successCount) / float64(value.requestCount) * 100
}

func avgTps(value counters) float64 {
	if value.outputTokens <= 0 || value.generationMs <= 0 {
		return 0
	}
	return float64(value.outputTokens) / (float64(value.generationMs) / 1000)
}
