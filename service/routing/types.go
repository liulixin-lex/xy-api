package routing

import "github.com/QuantumNous/new-api/model"

const (
	BreakerStateHealthy  = "healthy"
	BreakerStateDegraded = "degraded"
	BreakerStateOpen     = "open"
	BreakerStateHalfOpen = "half_open"

	BreakerReasonAuthFail = "authfail"
	BreakerReasonBalance  = "balance"
)

type Candidate struct {
	Channel  *model.Channel
	Metric   *MetricSnapshot
	Cost     *CostSnapshot
	Breaker  *BreakerSnapshot
	Capacity *CapacityCooldownSnapshot
}

type MetricSnapshot struct {
	RequestCount            int64
	SuccessCount            int64
	ReliabilityRequestCount int64
	ReliabilityFailureCount int64
	P95LatencyMs            float64
	TPS                     float64
	Inflight                int64
}

type CostSnapshot struct {
	Known       bool
	Cost        float64
	UpdatedUnix int64
}

type BreakerSnapshot struct {
	State             string
	Reason            string
	CooldownUntilUnix int64
	HalfOpenInflight  int64
	UpdatedUnix       int64
}

type CapacityCooldownSnapshot struct {
	SourceStatusCode       int
	CooldownUntilUnixMilli int64
	UpdatedUnixMilli       int64
}

type Settings struct {
	WeightAvailability float64
	WeightLatency      float64
	WeightThroughput   float64
	WeightCost         float64
	AvailabilityFloor  float64
	MinVolume          int
	TopK               int
	MaxEjectedPct      int
	HalfOpenProbes     int
	SnapshotStaleSec   int
	NowUnix            int64
	NowUnixMilli       int64
	RandomSeed         int64
}

type Weights struct {
	Availability float64
	Latency      float64
	Throughput   float64
	Cost         float64
}

type Decision struct {
	Ranked           []RankedCandidate
	Selected         *RankedCandidate
	Weights          Weights
	BreakerBypassed  bool
	FilteredOpen     int
	FilteredCapacity int
}

type RankedCandidate struct {
	Candidate       Candidate
	Channel         *model.Channel
	Score           float64
	Availability    float64
	Latency         float64
	Throughput      float64
	CostScore       float64
	CostKnown       bool
	Degraded        bool
	Open            bool
	Inflight        int64
	originalIndex   int
	healthSortOrder int
}
