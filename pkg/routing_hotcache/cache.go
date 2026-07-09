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
	P95TTFTMs    float64
	TPS          float64
	UpdatedUnix  int64
}

type CostSnapshot struct {
	Known           bool
	Cost            float64
	Confidence      string
	QuotaType       int
	GroupRatio      float64
	BaseRatio       float64
	CompletionRatio float64
	ModelPrice      float64
	BillingMode     string
	UpdatedUnix     int64
}

type BreakerSnapshot struct {
	State             string
	Reason            string
	CooldownUntilUnix int64
	HalfOpenInflight  int64
	UpdatedUnix       int64
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

func ClearBreaker(key Key) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.breakers, key)
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

func ClearChannel(channelID int) {
	if channelID <= 0 {
		return
	}
	cache.Lock()
	defer cache.Unlock()
	for key := range cache.metrics {
		if key.ChannelID == channelID {
			delete(cache.metrics, key)
		}
	}
	for key := range cache.costs {
		if key.ChannelID == channelID {
			delete(cache.costs, key)
		}
	}
	for key := range cache.breakers {
		if key.ChannelID == channelID {
			delete(cache.breakers, key)
		}
	}
	delete(cache.authFailures, channelID)
	delete(cache.balances, channelID)
}

func LoadCostSnapshots(snapshots []model.RoutingCostSnapshot) {
	cache.Lock()
	defer cache.Unlock()
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" {
			continue
		}
		cost := routingSnapshotCost(snapshot)
		key := CostKey{ChannelID: snapshot.ChannelID, Model: snapshot.ModelName}
		if existing, ok := cache.costs[key]; ok && existing.UpdatedUnix >= snapshot.SnapshotTS {
			continue
		}
		cache.costs[key] = CostSnapshot{
			Known:           routingSnapshotCostKnown(snapshot, cost),
			Cost:            cost,
			Confidence:      snapshot.Confidence,
			QuotaType:       snapshot.QuotaType,
			GroupRatio:      snapshot.GroupRatio,
			BaseRatio:       snapshot.BaseRatio,
			CompletionRatio: snapshot.CompletionRatio,
			ModelPrice:      snapshot.ModelPrice,
			BillingMode:     snapshot.BillingMode,
			UpdatedUnix:     snapshot.SnapshotTS,
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
		latencyMs := float64(snapshot.LatencyP95Ms)
		if latencyMs <= 0 {
			latencyMs = float64(snapshot.TotalLatencyMs) / float64(snapshot.RequestCount)
		}
		ttftMs := float64(snapshot.TtftP95Ms)
		if ttftMs <= 0 && snapshot.TtftCount > 0 {
			ttftMs = float64(snapshot.TtftSumMs) / float64(snapshot.TtftCount)
		}
		key := Key{
			ChannelID:   snapshot.ChannelID,
			APIKeyIndex: snapshot.APIKeyIndex,
			Model:       snapshot.ModelName,
			Group:       snapshot.Group,
		}
		if existing, ok := cache.metrics[key]; ok {
			if existing.UpdatedUnix > snapshot.BucketTs {
				continue
			}
			if existing.UpdatedUnix == snapshot.BucketTs && existing.RequestCount >= snapshot.RequestCount {
				continue
			}
		}
		cache.metrics[key] = MetricSnapshot{
			RequestCount: snapshot.RequestCount,
			SuccessCount: snapshot.SuccessCount,
			P95LatencyMs: latencyMs,
			P95TTFTMs:    ttftMs,
			TPS:          float64(snapshot.RequestCount) / float64(bucketSeconds),
			UpdatedUnix:  snapshot.BucketTs,
		}
	}
}

func LoadBreakerSnapshots(snapshots []model.RoutingBreakerState) {
	cache.Lock()
	defer cache.Unlock()
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 || snapshot.ModelName == "" || snapshot.Group == "" {
			continue
		}
		key := Key{
			ChannelID:   snapshot.ChannelID,
			APIKeyIndex: snapshot.APIKeyIndex,
			Model:       snapshot.ModelName,
			Group:       snapshot.Group,
		}
		if existing, ok := cache.breakers[key]; ok && existing.UpdatedUnix >= snapshot.UpdatedTime {
			continue
		}
		cache.breakers[key] = BreakerSnapshot{
			State:             snapshot.State,
			Reason:            snapshot.Reason,
			CooldownUntilUnix: snapshot.CooldownUntil,
			HalfOpenInflight:  snapshot.HalfOpenInflight,
			UpdatedUnix:       snapshot.UpdatedTime,
		}
	}
}

func LoadHealthSnapshots(snapshots []model.RoutingChannelHealthState, nowUnix int64) {
	cache.Lock()
	defer cache.Unlock()
	for _, snapshot := range snapshots {
		if snapshot.ChannelID <= 0 {
			continue
		}
		if snapshot.AuthFailure && (snapshot.AuthFailureUntil <= 0 || snapshot.AuthFailureUntil > nowUnix) {
			cache.authFailures[snapshot.ChannelID] = HealthMarker{
				Marked:      true,
				UpdatedUnix: snapshot.UpdatedTime,
			}
		} else {
			delete(cache.authFailures, snapshot.ChannelID)
		}
		if snapshot.BalanceKnown {
			cache.balances[snapshot.ChannelID] = BalanceSnapshot{
				Known:       true,
				Balance:     snapshot.Balance,
				UpdatedUnix: snapshot.BalanceUpdatedTime,
			}
		}
	}
}

func routingSnapshotCost(snapshot model.RoutingCostSnapshot) float64 {
	if snapshot.ModelPrice > 0 {
		return snapshot.ModelPrice
	}
	return snapshot.GroupRatio * snapshot.BaseRatio
}

func routingSnapshotCostKnown(snapshot model.RoutingCostSnapshot, cost float64) bool {
	if snapshot.Confidence == model.RoutingCostConfidenceUnknown || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return false
	}
	if snapshot.QuotaType == 1 {
		return snapshot.ModelPrice > 0
	}
	if snapshot.BaseRatio <= 0 && snapshot.ModelPrice <= 0 {
		return false
	}
	return true
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
