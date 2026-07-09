package routing

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

const (
	availabilityNeutralPrior = 0.9
	degradedScoreMultiplier  = 0.5
)

var defaultWeights = Weights{
	Availability: 0.45,
	Latency:      0.25,
	Throughput:   0.10,
	Cost:         0.20,
}

type candidateHealth struct {
	candidate Candidate
	index     int
	degraded  bool
	open      bool
	hardOpen  bool
}

type scoreBounds struct {
	minLatency  float64
	maxLatency  float64
	maxTPS      float64
	minCost     float64
	hasLatency  bool
	hasTPS      bool
	hasCost     bool
	hasPaidCost bool
}

func RankCandidates(candidates []Candidate, settings Settings) Decision {
	health := make([]candidateHealth, 0, len(candidates))
	openCount := 0
	for i, candidate := range candidates {
		degraded, open, hardOpen := classifyBreaker(candidate.Breaker, settings)
		if open && !hardOpen {
			openCount++
		}
		health = append(health, candidateHealth{
			candidate: candidate,
			index:     i,
			degraded:  degraded,
			open:      open,
			hardOpen:  hardOpen,
		})
	}

	breakerBypassed := shouldBypassOpenFilter(openCount, len(candidates), settings.MaxEjectedPct)
	included := make([]candidateHealth, 0, len(health))
	bypassedOpen := make([]candidateHealth, 0)
	filteredOpen := 0
	for _, item := range health {
		if item.hardOpen {
			filteredOpen++
			continue
		}
		if item.open {
			if breakerBypassed {
				item.degraded = true
				bypassedOpen = append(bypassedOpen, item)
			} else {
				filteredOpen++
			}
			continue
		}
		if belowAvailabilityFloor(item.candidate.Metric, settings) {
			continue
		}
		included = append(included, item)
	}
	if len(included) == 0 && breakerBypassed {
		included = append(included, bypassedOpen...)
	} else {
		filteredOpen += len(bypassedOpen)
	}

	bounds := collectScoreBounds(included, settings)
	weights := normalizeWeights(settings, bounds.hasCost)
	ranked := make([]RankedCandidate, 0, len(included))
	for _, item := range included {
		ranked = append(ranked, scoreCandidate(item, bounds, weights, settings))
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		left := ranked[i]
		right := ranked[j]
		if left.healthSortOrder != right.healthSortOrder {
			return left.healthSortOrder < right.healthSortOrder
		}
		leftPriority := channelPriority(left.Channel)
		rightPriority := channelPriority(right.Channel)
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		if !almostEqual(left.Score, right.Score) {
			return left.Score > right.Score
		}
		if left.Inflight != right.Inflight {
			return left.Inflight < right.Inflight
		}
		leftID := channelID(left.Channel)
		rightID := channelID(right.Channel)
		if leftID != rightID {
			return leftID < rightID
		}
		return left.originalIndex < right.originalIndex
	})

	return Decision{
		Ranked:          ranked,
		Weights:         weights,
		BreakerBypassed: breakerBypassed,
		FilteredOpen:    filteredOpen,
	}
}

func SelectRankedFromCandidates(candidates []Candidate, settings Settings) Decision {
	decision := RankCandidates(candidates, settings)
	if len(decision.Ranked) == 0 {
		return decision
	}

	topK := settings.TopK
	if topK < 1 {
		topK = 1
	}
	if topK > len(decision.Ranked) {
		topK = len(decision.Ranked)
	}

	poolSize := topKSelectionPoolSize(decision.Ranked, topK)
	selectedIndex := 0
	if poolSize > 1 {
		selectedIndex = weightedTopKIndex(decision.Ranked[:poolSize], settings.RandomSeed)
	}
	decision.Selected = &decision.Ranked[selectedIndex]
	return decision
}

func topKSelectionPoolSize(ranked []RankedCandidate, topK int) int {
	if len(ranked) == 0 {
		return 0
	}
	if topK < 1 {
		topK = 1
	}
	if topK > len(ranked) {
		topK = len(ranked)
	}
	firstHealthOrder := ranked[0].healthSortOrder
	firstPriority := channelPriority(ranked[0].Channel)
	poolSize := 1
	for poolSize < topK {
		candidate := ranked[poolSize]
		if candidate.healthSortOrder != firstHealthOrder || channelPriority(candidate.Channel) != firstPriority {
			break
		}
		poolSize++
	}
	return poolSize
}

func weightedTopKIndex(ranked []RankedCandidate, seed int64) int {
	total := 0.0
	for _, candidate := range ranked {
		if candidate.Score > 0 && !math.IsNaN(candidate.Score) && !math.IsInf(candidate.Score, 0) {
			total += candidate.Score
		}
	}
	if total <= 0 {
		return rand.New(rand.NewSource(seed)).Intn(len(ranked))
	}
	target := rand.New(rand.NewSource(seed)).Float64() * total
	for index, candidate := range ranked {
		score := candidate.Score
		if score <= 0 || math.IsNaN(score) || math.IsInf(score, 0) {
			continue
		}
		target -= score
		if target <= 0 {
			return index
		}
	}
	return len(ranked) - 1
}

func collectScoreBounds(candidates []candidateHealth, settings Settings) scoreBounds {
	bounds := scoreBounds{
		minLatency: math.Inf(1),
		maxLatency: math.Inf(-1),
		minCost:    math.Inf(1),
	}
	for _, item := range candidates {
		if item.candidate.Metric != nil {
			latency := item.candidate.Metric.P95LatencyMs
			if finitePositive(latency) {
				bounds.hasLatency = true
				if latency < bounds.minLatency {
					bounds.minLatency = latency
				}
				if latency > bounds.maxLatency {
					bounds.maxLatency = latency
				}
			}
			tps := item.candidate.Metric.TPS
			if finitePositive(tps) {
				bounds.hasTPS = true
				if tps > bounds.maxTPS {
					bounds.maxTPS = tps
				}
			}
		}
		if costKnown(item.candidate.Cost, settings) {
			bounds.hasCost = true
			if item.candidate.Cost.Cost > 0 {
				bounds.hasPaidCost = true
				if item.candidate.Cost.Cost < bounds.minCost {
					bounds.minCost = item.candidate.Cost.Cost
				}
			}
		}
	}
	return bounds
}

func scoreCandidate(item candidateHealth, bounds scoreBounds, weights Weights, settings Settings) RankedCandidate {
	availability := availabilityScore(item.candidate.Metric, settings.MinVolume)
	latency := latencyScore(item.candidate.Metric, bounds)
	throughput := throughputScore(item.candidate.Metric, bounds)
	cost, knownCost := costScore(item.candidate.Cost, bounds, settings)

	score, healthOrder := weightedScore(weights, availability, latency, throughput, cost, knownCost), 0
	if item.degraded {
		score *= degradedScoreMultiplier
		healthOrder = 1
	}

	return RankedCandidate{
		Candidate:       item.candidate,
		Channel:         item.candidate.Channel,
		Score:           score,
		Availability:    availability,
		Latency:         latency,
		Throughput:      throughput,
		CostScore:       cost,
		CostKnown:       knownCost,
		Degraded:        item.degraded,
		Open:            item.open,
		Inflight:        inflightCount(item.candidate.Metric),
		originalIndex:   item.index,
		healthSortOrder: healthOrder,
	}
}

func inflightCount(metric *MetricSnapshot) int64 {
	if metric == nil || metric.Inflight < 0 {
		return 0
	}
	return metric.Inflight
}

func weightedScore(weights Weights, availability float64, latency float64, throughput float64, cost float64, costKnown bool) float64 {
	numerator := availability*weights.Availability + latency*weights.Latency + throughput*weights.Throughput
	denominator := weights.Availability + weights.Latency + weights.Throughput
	if costKnown {
		numerator += cost * weights.Cost
		denominator += weights.Cost
	}
	if denominator <= 0 {
		return 0
	}
	return clamp01(numerator / denominator)
}

func availabilityScore(metric *MetricSnapshot, minVolume int) float64 {
	if metric == nil {
		return availabilityNeutralPrior
	}
	if minVolume < 0 {
		minVolume = 0
	}
	if metric.RequestCount < int64(minVolume) {
		return availabilityNeutralPrior
	}
	if metric.RequestCount <= 0 {
		return availabilityNeutralPrior
	}
	return clamp01(float64(metric.SuccessCount) / float64(metric.RequestCount))
}

func belowAvailabilityFloor(metric *MetricSnapshot, settings Settings) bool {
	floor := settings.AvailabilityFloor
	if floor <= 0 || floor > 1 || metric == nil {
		return false
	}
	minVolume := settings.MinVolume
	if minVolume < 0 {
		minVolume = 0
	}
	if metric.RequestCount < int64(minVolume) || metric.RequestCount <= 0 {
		return false
	}
	return float64(metric.SuccessCount)/float64(metric.RequestCount) < floor
}

func latencyScore(metric *MetricSnapshot, bounds scoreBounds) float64 {
	if !bounds.hasLatency {
		return 1
	}
	if metric == nil || !finitePositive(metric.P95LatencyMs) {
		return 0
	}
	if almostEqual(bounds.minLatency, bounds.maxLatency) {
		return 1
	}
	return clamp01((bounds.maxLatency - metric.P95LatencyMs) / (bounds.maxLatency - bounds.minLatency))
}

func throughputScore(metric *MetricSnapshot, bounds scoreBounds) float64 {
	if !bounds.hasTPS || bounds.maxTPS <= 0 {
		return 0
	}
	if metric == nil || !finitePositive(metric.TPS) {
		return 0
	}
	return clamp01(metric.TPS / bounds.maxTPS)
}

func costScore(cost *CostSnapshot, bounds scoreBounds, settings Settings) (float64, bool) {
	if !costKnown(cost, settings) {
		return 0, false
	}
	if cost.Cost <= 0 || !bounds.hasPaidCost {
		return 1, true
	}
	score := math.Pow(bounds.minCost/cost.Cost, 2)
	return clamp01(score), true
}

func normalizeWeights(settings Settings, includeCost bool) Weights {
	weights := Weights{
		Availability: sanitizeWeight(settings.WeightAvailability),
		Latency:      sanitizeWeight(settings.WeightLatency),
		Throughput:   sanitizeWeight(settings.WeightThroughput),
		Cost:         sanitizeWeight(settings.WeightCost),
	}
	if !includeCost {
		weights.Cost = 0
	}
	if weights.sum() <= 0 {
		weights = defaultWeights
		if !includeCost {
			weights.Cost = 0
		}
	}
	if weights.sum() <= 0 {
		return Weights{Availability: 1}
	}
	return weights.normalized()
}

func classifyBreaker(breaker *BreakerSnapshot, settings Settings) (bool, bool, bool) {
	if breaker == nil || breakerStale(breaker, settings) {
		return false, false, false
	}
	state := strings.ToLower(strings.TrimSpace(breaker.State))
	reason := strings.ToLower(strings.TrimSpace(breaker.Reason))
	switch {
	case state == BreakerReasonAuthFail,
		reason == BreakerReasonAuthFail:
		return false, true, true
	case state == BreakerReasonBalance,
		reason == BreakerReasonBalance:
		return false, true, true
	case state == BreakerStateDegraded:
		return true, false, false
	case state == BreakerStateHalfOpen:
		if settings.HalfOpenProbes > 0 && breaker.HalfOpenInflight >= int64(settings.HalfOpenProbes) {
			return false, true, false
		}
		return true, false, false
	case state == BreakerStateOpen:
		if settings.NowUnix > 0 && breaker.CooldownUntilUnix > 0 && settings.NowUnix >= breaker.CooldownUntilUnix {
			return true, false, false
		}
		return false, true, false
	default:
		return false, false, false
	}
}

func breakerStale(breaker *BreakerSnapshot, settings Settings) bool {
	if breaker.UpdatedUnix <= 0 || settings.NowUnix <= 0 || settings.SnapshotStaleSec <= 0 {
		return false
	}
	return settings.NowUnix-breaker.UpdatedUnix > int64(settings.SnapshotStaleSec)
}

func shouldBypassOpenFilter(openCount int, total int, maxEjectedPct int) bool {
	if openCount == 0 || total <= 0 {
		return false
	}
	if maxEjectedPct < 0 {
		maxEjectedPct = 0
	}
	if maxEjectedPct > 100 {
		maxEjectedPct = 100
	}
	return openCount*100 > total*maxEjectedPct
}

func costKnown(cost *CostSnapshot, settings Settings) bool {
	return cost != nil && cost.Known && !costStale(cost, settings) && !math.IsNaN(cost.Cost) && !math.IsInf(cost.Cost, 0)
}

func costStale(cost *CostSnapshot, settings Settings) bool {
	if cost == nil || cost.UpdatedUnix <= 0 || settings.NowUnix <= 0 || settings.SnapshotStaleSec <= 0 {
		return false
	}
	return settings.NowUnix-cost.UpdatedUnix > int64(settings.SnapshotStaleSec)
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sanitizeWeight(weight float64) float64 {
	if weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return 0
	}
	return weight
}

func (weights Weights) sum() float64 {
	return weights.Availability + weights.Latency + weights.Throughput + weights.Cost
}

func (weights Weights) normalized() Weights {
	total := weights.sum()
	if total <= 0 {
		return Weights{}
	}
	return Weights{
		Availability: weights.Availability / total,
		Latency:      weights.Latency / total,
		Throughput:   weights.Throughput / total,
		Cost:         weights.Cost / total,
	}
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func almostEqual(left float64, right float64) bool {
	return math.Abs(left-right) <= 1e-12
}

func channelID(channel *model.Channel) int {
	if channel == nil {
		return 0
	}
	return channel.Id
}

func channelPriority(channel *model.Channel) int64 {
	if channel == nil {
		return 0
	}
	return channel.GetPriority()
}
