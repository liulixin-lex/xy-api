package channelrouting

import (
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

type BalancedPoolPolicy struct {
	WeightAvailability             float64 `json:"weight_availability"`
	WeightLatency                  float64 `json:"weight_latency"`
	WeightThroughput               float64 `json:"weight_throughput"`
	WeightCost                     float64 `json:"weight_cost"`
	AvailabilityTarget             float64 `json:"availability_target"`
	AvailabilityFloor              float64 `json:"availability_floor"`
	LatencyTargetMs                float64 `json:"latency_target_ms"`
	ThroughputTarget               float64 `json:"throughput_target"`
	CostTarget                     float64 `json:"cost_target"`
	CostBudget                     float64 `json:"cost_budget"`
	MinVolume                      int     `json:"min_volume"`
	WilsonZ                        float64 `json:"wilson_z"`
	UnknownAvailability            float64 `json:"unknown_availability"`
	UnknownLatencyUtility          float64 `json:"unknown_latency_utility"`
	UnknownThroughputUtility       float64 `json:"unknown_throughput_utility"`
	UnknownCostUtility             float64 `json:"unknown_cost_utility"`
	ProtectionBandBasisPoints      int     `json:"protection_band_basis_points"`
	ExplorationBasisPoints         int     `json:"exploration_basis_points"`
	MinimumExplorationScore        float64 `json:"minimum_exploration_score"`
	MaxCapacityUtilization         float64 `json:"max_capacity_utilization"`
	AffinityMaxCapacityUtilization float64 `json:"affinity_max_capacity_utilization"`
	QueueTargetMs                  float64 `json:"queue_target_ms"`
	DegradedMultiplier             float64 `json:"degraded_multiplier"`
	SoftFallbackMultiplier         float64 `json:"soft_fallback_multiplier"`
	HalfOpenProbes                 int     `json:"half_open_probes"`
	SnapshotStaleSec               int     `json:"snapshot_stale_sec"`
	BalanceMarginUSD               float64 `json:"balance_margin_usd"`
	RequireKnownCost               bool    `json:"require_known_cost"`
	AllowSoftFailureFallback       bool    `json:"allow_soft_failure_fallback"`
	EnforceBusinessTierCascade     bool    `json:"enforce_business_tier_cascade"`
}

type balancedPoolPolicyOverrides struct {
	WeightAvailability             *float64 `json:"weight_availability"`
	WeightLatency                  *float64 `json:"weight_latency"`
	WeightThroughput               *float64 `json:"weight_throughput"`
	WeightCost                     *float64 `json:"weight_cost"`
	AvailabilityTarget             *float64 `json:"availability_target"`
	AvailabilityFloor              *float64 `json:"availability_floor"`
	LatencyTargetMs                *float64 `json:"latency_target_ms"`
	ThroughputTarget               *float64 `json:"throughput_target"`
	CostTarget                     *float64 `json:"cost_target"`
	CostBudget                     *float64 `json:"cost_budget"`
	MinVolume                      *int     `json:"min_volume"`
	WilsonZ                        *float64 `json:"wilson_z"`
	UnknownAvailability            *float64 `json:"unknown_availability"`
	UnknownLatencyUtility          *float64 `json:"unknown_latency_utility"`
	UnknownThroughputUtility       *float64 `json:"unknown_throughput_utility"`
	UnknownCostUtility             *float64 `json:"unknown_cost_utility"`
	ProtectionBandBasisPoints      *int     `json:"protection_band_basis_points"`
	ExplorationBasisPoints         *int     `json:"exploration_basis_points"`
	MinimumExplorationScore        *float64 `json:"minimum_exploration_score"`
	MaxCapacityUtilization         *float64 `json:"max_capacity_utilization"`
	AffinityMaxCapacityUtilization *float64 `json:"affinity_max_capacity_utilization"`
	QueueTargetMs                  *float64 `json:"queue_target_ms"`
	DegradedMultiplier             *float64 `json:"degraded_multiplier"`
	SoftFallbackMultiplier         *float64 `json:"soft_fallback_multiplier"`
	HalfOpenProbes                 *int     `json:"half_open_probes"`
	SnapshotStaleSec               *int     `json:"snapshot_stale_sec"`
	BalanceMarginUSD               *float64 `json:"balance_margin_usd"`
	RequireKnownCost               *bool    `json:"require_known_cost"`
	AllowSoftFailureFallback       *bool    `json:"allow_soft_failure_fallback"`
	EnforceBusinessTierCascade     *bool    `json:"enforce_business_tier_cascade"`
}

func resolveBalancedPoolPolicy(profile string, policyJSON []byte) (BalancedPoolPolicy, error) {
	policy := defaultBalancedPoolPolicy(profile)
	if policy == (BalancedPoolPolicy{}) {
		return BalancedPoolPolicy{}, routingselector.ErrBalancedPolicyInvalid
	}
	if len(policyJSON) > 0 {
		var overrides balancedPoolPolicyOverrides
		if err := common.Unmarshal(policyJSON, &overrides); err != nil {
			return BalancedPoolPolicy{}, err
		}
		applyBalancedPoolPolicyOverrides(&policy, overrides)
	}
	return normalizeBalancedPoolPolicy(policy)
}

func defaultBalancedPoolPolicy(profile string) BalancedPoolPolicy {
	policy := BalancedPoolPolicy{
		WeightAvailability:             0.45,
		WeightLatency:                  0.25,
		WeightThroughput:               0.10,
		WeightCost:                     0.20,
		AvailabilityTarget:             0.99,
		AvailabilityFloor:              0.95,
		LatencyTargetMs:                200,
		ThroughputTarget:               20,
		CostTarget:                     1,
		MinVolume:                      50,
		WilsonZ:                        1.96,
		UnknownAvailability:            0.50,
		UnknownLatencyUtility:          0.50,
		UnknownThroughputUtility:       0.50,
		UnknownCostUtility:             0.40,
		ProtectionBandBasisPoints:      1_000,
		ExplorationBasisPoints:         100,
		MinimumExplorationScore:        0.05,
		MaxCapacityUtilization:         1,
		AffinityMaxCapacityUtilization: 0.80,
		QueueTargetMs:                  50,
		DegradedMultiplier:             0.50,
		SoftFallbackMultiplier:         0.10,
		HalfOpenProbes:                 1,
		SnapshotStaleSec:               1_800,
		BalanceMarginUSD:               1,
		AllowSoftFailureFallback:       true,
	}
	switch profile {
	case model.RoutingPolicyProfileBalanced, model.RoutingPolicyProfileCustom:
	case model.RoutingPolicyProfileReliabilityFirst:
		policy.WeightAvailability = 0.65
		policy.WeightLatency = 0.20
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.05
		policy.AvailabilityTarget = 0.995
		policy.AvailabilityFloor = 0.98
	case model.RoutingPolicyProfileCostAware:
		policy.WeightAvailability = 0.30
		policy.WeightLatency = 0.15
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.45
		policy.AvailabilityTarget = 0.98
		policy.AvailabilityFloor = 0.90
	case model.RoutingPolicyProfileEnterpriseSLO:
		policy.WeightAvailability = 0.55
		policy.WeightLatency = 0.30
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.05
		policy.AvailabilityTarget = 0.999
		policy.AvailabilityFloor = 0.98
		policy.LatencyTargetMs = 150
		policy.ThroughputTarget = 30
	default:
		return BalancedPoolPolicy{}
	}
	return policy
}

func applyBalancedPoolPolicyOverrides(policy *BalancedPoolPolicy, overrides balancedPoolPolicyOverrides) {
	if policy == nil {
		return
	}
	applyFloat := func(target *float64, value *float64) {
		if value != nil {
			*target = *value
		}
	}
	applyInt := func(target *int, value *int) {
		if value != nil {
			*target = *value
		}
	}
	applyBool := func(target *bool, value *bool) {
		if value != nil {
			*target = *value
		}
	}
	applyFloat(&policy.WeightAvailability, overrides.WeightAvailability)
	applyFloat(&policy.WeightLatency, overrides.WeightLatency)
	applyFloat(&policy.WeightThroughput, overrides.WeightThroughput)
	applyFloat(&policy.WeightCost, overrides.WeightCost)
	applyFloat(&policy.AvailabilityTarget, overrides.AvailabilityTarget)
	applyFloat(&policy.AvailabilityFloor, overrides.AvailabilityFloor)
	applyFloat(&policy.LatencyTargetMs, overrides.LatencyTargetMs)
	applyFloat(&policy.ThroughputTarget, overrides.ThroughputTarget)
	applyFloat(&policy.CostTarget, overrides.CostTarget)
	applyFloat(&policy.CostBudget, overrides.CostBudget)
	applyInt(&policy.MinVolume, overrides.MinVolume)
	applyFloat(&policy.WilsonZ, overrides.WilsonZ)
	applyFloat(&policy.UnknownAvailability, overrides.UnknownAvailability)
	applyFloat(&policy.UnknownLatencyUtility, overrides.UnknownLatencyUtility)
	applyFloat(&policy.UnknownThroughputUtility, overrides.UnknownThroughputUtility)
	applyFloat(&policy.UnknownCostUtility, overrides.UnknownCostUtility)
	applyInt(&policy.ProtectionBandBasisPoints, overrides.ProtectionBandBasisPoints)
	applyInt(&policy.ExplorationBasisPoints, overrides.ExplorationBasisPoints)
	applyFloat(&policy.MinimumExplorationScore, overrides.MinimumExplorationScore)
	applyFloat(&policy.MaxCapacityUtilization, overrides.MaxCapacityUtilization)
	applyFloat(&policy.AffinityMaxCapacityUtilization, overrides.AffinityMaxCapacityUtilization)
	applyFloat(&policy.QueueTargetMs, overrides.QueueTargetMs)
	applyFloat(&policy.DegradedMultiplier, overrides.DegradedMultiplier)
	applyFloat(&policy.SoftFallbackMultiplier, overrides.SoftFallbackMultiplier)
	applyInt(&policy.HalfOpenProbes, overrides.HalfOpenProbes)
	applyInt(&policy.SnapshotStaleSec, overrides.SnapshotStaleSec)
	applyFloat(&policy.BalanceMarginUSD, overrides.BalanceMarginUSD)
	applyBool(&policy.RequireKnownCost, overrides.RequireKnownCost)
	applyBool(&policy.AllowSoftFailureFallback, overrides.AllowSoftFailureFallback)
	applyBool(&policy.EnforceBusinessTierCascade, overrides.EnforceBusinessTierCascade)
}

func normalizeBalancedPoolPolicy(policy BalancedPoolPolicy) (BalancedPoolPolicy, error) {
	weightTotal := policy.WeightAvailability + policy.WeightLatency + policy.WeightThroughput + policy.WeightCost
	if math.IsNaN(weightTotal) || math.IsInf(weightTotal, 0) || weightTotal <= 0 ||
		math.IsNaN(policy.BalanceMarginUSD) || math.IsInf(policy.BalanceMarginUSD, 0) || policy.BalanceMarginUSD < 0 {
		return BalancedPoolPolicy{}, routingselector.ErrBalancedPolicyInvalid
	}
	policy.WeightAvailability /= weightTotal
	policy.WeightLatency /= weightTotal
	policy.WeightThroughput /= weightTotal
	policy.WeightCost /= weightTotal
	if _, err := routingselector.PrepareBalanced(nil, policy.settings(time.Unix(1_000, 0), 1, 0, false)); err != nil {
		return BalancedPoolPolicy{}, err
	}
	return policy, nil
}

func (policy BalancedPoolPolicy) settings(now time.Time, seed int64, preferredChannelID int, preferTTFT bool) routingselector.BalancedSettings {
	return routingselector.BalancedSettings{
		Weights: routingselector.Weights{
			Availability: policy.WeightAvailability,
			Latency:      policy.WeightLatency,
			Throughput:   policy.WeightThroughput,
			Cost:         policy.WeightCost,
		},
		AvailabilityTarget:             policy.AvailabilityTarget,
		AvailabilityFloor:              policy.AvailabilityFloor,
		LatencyTargetMs:                policy.LatencyTargetMs,
		ThroughputTarget:               policy.ThroughputTarget,
		CostTarget:                     policy.CostTarget,
		CostBudget:                     policy.CostBudget,
		MinimumVolume:                  policy.MinVolume,
		WilsonZ:                        policy.WilsonZ,
		UnknownAvailability:            policy.UnknownAvailability,
		UnknownLatencyUtility:          policy.UnknownLatencyUtility,
		UnknownThroughputUtility:       policy.UnknownThroughputUtility,
		UnknownCostUtility:             policy.UnknownCostUtility,
		ProtectionBandBasisPoints:      policy.ProtectionBandBasisPoints,
		ExplorationBasisPoints:         policy.ExplorationBasisPoints,
		MinimumExplorationScore:        policy.MinimumExplorationScore,
		MaxCapacityUtilization:         policy.MaxCapacityUtilization,
		AffinityMaxCapacityUtilization: policy.AffinityMaxCapacityUtilization,
		QueueTargetMs:                  policy.QueueTargetMs,
		DegradedMultiplier:             policy.DegradedMultiplier,
		SoftFallbackMultiplier:         policy.SoftFallbackMultiplier,
		HalfOpenProbes:                 policy.HalfOpenProbes,
		SnapshotStaleSec:               policy.SnapshotStaleSec,
		NowUnix:                        now.Unix(),
		NowUnixMilli:                   now.UnixMilli(),
		RandomSeed:                     seed,
		PreferredChannelID:             preferredChannelID,
		PreferTTFT:                     preferTTFT,
		RequireKnownCost:               policy.RequireKnownCost,
		AllowSoftFailureFallback:       policy.AllowSoftFailureFallback,
		EnforceBusinessTierCascade:     policy.EnforceBusinessTierCascade,
	}
}
