package routinghotcache

import (
	"math"
	"sync"

	"github.com/QuantumNous/new-api/model"
)

type Key struct {
	ChannelID   int
	APIKeyIndex int
	Model       string
	Group       string
}

type CostKey struct {
	ChannelID int
	Model     string
}

type MetricSnapshot struct {
	RequestCount int64
	SuccessCount int64
	P95LatencyMs float64
	TPS          float64
	UpdatedUnix  int64
}

type CostSnapshot struct {
	Known       bool
	Cost        float64
	Confidence  string
	UpdatedUnix int64
}

type BreakerSnapshot struct {
	State       string
	Reason      string
	UpdatedUnix int64
}

type HealthMarker struct {
	Marked      bool
	UpdatedUnix int64
}

type BalanceSnapshot struct {
	Known       bool
	Balance     float64
	UpdatedUnix int64
}

var cache = struct {
	sync.RWMutex
	metrics      map[Key]MetricSnapshot
	costs        map[CostKey]CostSnapshot
	breakers     map[Key]BreakerSnapshot
	authFailures map[int]HealthMarker
	balances     map[int]BalanceSnapshot
}{
	metrics:      map[Key]MetricSnapshot{},
	costs:        map[CostKey]CostSnapshot{},
	breakers:     map[Key]BreakerSnapshot{},
	authFailures: map[int]HealthMarker{},
	balances:     map[int]BalanceSnapshot{},
}

func (key Key) CostKey() CostKey {
	return CostKey{ChannelID: key.ChannelID, Model: key.Model}
}

func GetMetric(key Key) (MetricSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.metrics[key]
	return snapshot, ok
}

func GetCost(key CostKey) (CostSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.costs[key]
	return snapshot, ok
}

func GetBreaker(key Key) (BreakerSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.breakers[key]
	return snapshot, ok
}

func GetAuthFailure(channelID int) (HealthMarker, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.authFailures[channelID]
	return snapshot, ok
}

func GetBalance(channelID int) (BalanceSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.balances[channelID]
	return snapshot, ok
}

func SetMetricForTest(key Key, snapshot MetricSnapshot) {
	SetMetric(key, snapshot)
}

func SetMetric(key Key, snapshot MetricSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.metrics[key] = snapshot
}

func SetCostForTest(key CostKey, snapshot CostSnapshot) {
	SetCost(key, snapshot)
}

func SetCost(key CostKey, snapshot CostSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.costs[key] = snapshot
}

func SetBreakerForTest(key Key, snapshot BreakerSnapshot) {
	SetBreaker(key, snapshot)
}

func SetBreaker(key Key, snapshot BreakerSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	cache.breakers[key] = snapshot
}

func SetAuthFailureForTest(channelID int, marker HealthMarker) {
	SetAuthFailure(channelID, marker)
}

func SetAuthFailure(channelID int, marker HealthMarker) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.authFailures[channelID] = marker
}

func ClearAuthFailure(channelID int) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.authFailures, channelID)
}

func SetBalanceForTest(channelID int, snapshot BalanceSnapshot) {
	SetBalance(channelID, snapshot)
}

func SetBalance(channelID int, snapshot BalanceSnapshot) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	cache.balances[channelID] = snapshot
}

func LoadCostSnapshots(snapshots []model.RoutingCostSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" {
			continue
		}
		cost := routingSnapshotCost(snapshot)
		cache.costs[CostKey{ChannelID: snapshot.ChannelID, Model: snapshot.ModelName}] = CostSnapshot{
			Known:       snapshot.Confidence != model.RoutingCostConfidenceUnknown && !math.IsNaN(cost) && !math.IsInf(cost, 0),
			Cost:        cost,
			Confidence:  snapshot.Confidence,
			UpdatedUnix: snapshot.SnapshotTS,
		}
	}
}

func LoadMetricSnapshots(snapshots []model.RoutingChannelMetric, bucketSeconds int) {
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	cache.Lock()
	defer cache.Unlock()
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" || snapshot.RequestCount <= 0 {
			continue
		}
		latencyMs := float64(snapshot.TotalLatencyMs) / float64(snapshot.RequestCount)
		cache.metrics[Key{
			ChannelID:   snapshot.ChannelID,
			APIKeyIndex: snapshot.APIKeyIndex,
			Model:       snapshot.ModelName,
			Group:       snapshot.Group,
		}] = MetricSnapshot{
			RequestCount: snapshot.RequestCount,
			SuccessCount: snapshot.SuccessCount,
			P95LatencyMs: latencyMs,
			TPS:          float64(snapshot.RequestCount) / float64(bucketSeconds),
			UpdatedUnix:  snapshot.BucketTs,
		}
	}
}

func routingSnapshotCost(snapshot model.RoutingCostSnapshot) float64 {
	if snapshot.ModelPrice > 0 {
		return snapshot.ModelPrice
	}
	return snapshot.GroupRatio * snapshot.BaseRatio
}

func ResetForTest() {
	cache.Lock()
	defer cache.Unlock()
	cache.metrics = map[Key]MetricSnapshot{}
	cache.costs = map[CostKey]CostSnapshot{}
	cache.breakers = map[Key]BreakerSnapshot{}
	cache.authFailures = map[int]HealthMarker{}
	cache.balances = map[int]BalanceSnapshot{}
}
