package perfmetrics

import (
	"sync"
	"time"
)

type Store interface {
	Record(sample Sample)
	Query(params QueryParams) (QueryResult, error)
}

type Sample struct {
	Model        string
	Group        string
	LatencyMs    int64
	TtftMs       int64
	HasTtft      bool
	Success      bool
	OutputTokens int64
	GenerationMs int64
}

type QueryParams struct {
	Model string
	Group string
	Hours int
}

type BucketPoint struct {
	Ts           int64   `json:"ts"`
	AvgTtftMs    int64   `json:"avg_ttft_ms"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
	AvgTps       float64 `json:"avg_tps"`
}

type GroupResult struct {
	Group        string        `json:"group"`
	AvgTtftMs    int64         `json:"avg_ttft_ms"`
	AvgLatencyMs int64         `json:"avg_latency_ms"`
	SuccessRate  float64       `json:"success_rate"`
	AvgTps       float64       `json:"avg_tps"`
	Series       []BucketPoint `json:"series"`
}

type QueryResult struct {
	ModelName    string        `json:"model_name"`
	SeriesSchema string        `json:"series_schema"`
	Groups       []GroupResult `json:"groups"`
}

type ModelSummary struct {
	ModelName          string    `json:"model_name"`
	AvgLatencyMs       int64     `json:"avg_latency_ms"`
	SuccessRate        float64   `json:"success_rate"`
	AvgTps             float64   `json:"avg_tps"`
	RecentSuccessRates []float64 `json:"recent_success_rates,omitempty"`
	RequestCount       int64     `json:"-"`
}

type SummaryAllResult struct {
	Models []ModelSummary `json:"models"`
}

type Limits struct {
	MaxBuckets int
	BucketTTL  time.Duration
}

type Stats struct {
	Buckets        int64
	DroppedSamples int64
	EvictedBuckets int64
	EvictedSamples int64
}

type bucketKey struct {
	model    string
	group    string
	bucketTs int64
}

type counters struct {
	requestCount   int64
	successCount   int64
	totalLatencyMs int64
	ttftSumMs      int64
	ttftCount      int64
	outputTokens   int64
	generationMs   int64
}

type bucket struct {
	mu       sync.Mutex
	draining bool
	counters counters
}

func (b *bucket) addLocked(sample Sample) {
	b.counters.requestCount++
	if sample.Success {
		b.counters.successCount++
	}
	if sample.LatencyMs > 0 {
		b.counters.totalLatencyMs += sample.LatencyMs
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		b.counters.ttftSumMs += sample.TtftMs
		b.counters.ttftCount++
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		b.counters.outputTokens += sample.OutputTokens
		b.counters.generationMs += sample.GenerationMs
	}
}

func (b *bucket) snapshot() counters {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counters
}

func (b *bucket) addCountersLocked(c counters) {
	b.counters.requestCount += c.requestCount
	b.counters.successCount += c.successCount
	b.counters.totalLatencyMs += c.totalLatencyMs
	b.counters.ttftSumMs += c.ttftSumMs
	b.counters.ttftCount += c.ttftCount
	b.counters.outputTokens += c.outputTokens
	b.counters.generationMs += c.generationMs
}
