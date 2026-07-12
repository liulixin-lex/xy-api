package model

import (
	"bytes"
	"encoding/json"
	"math"

	"github.com/QuantumNous/new-api/common"
)

const RoutingCanaryCapacityModeLocalSoft = "local_soft"

const (
	routingCanaryCapacityRPMMax       = int64(10_000_000)
	routingCanaryCapacityTPMMax       = int64(math.MaxInt32)
	routingCanaryCapacityInflightMax  = int64(100_000)
	routingCanarySlowStartFactorMin   = 0.01
	routingCanarySlowStartFactorMax   = 0.50
	routingCanarySlowStartRampMinSec  = 30
	routingCanarySlowStartRampMaxSec  = 3_600
	routingCanarySlowStartStateMaxSec = 7 * 24 * 60 * 60
)

// RoutingCanaryPolicy is the schema-v1, fully resolved policy used by a
// published snapshot. Defaults are part of the schema contract and must only
// change together with RoutingPolicySchemaVersion.
type RoutingCanaryPolicy struct {
	HedgingEnabled        bool                          `json:"hedging_enabled"`
	MaxConcurrentAttempts int                           `json:"max_concurrent_attempts"`
	Capacity              RoutingCanaryCapacityPolicy   `json:"capacity"`
	SlowStart             RoutingCanarySlowStartPolicy  `json:"slow_start"`
	Evaluation            RoutingCanaryEvaluationPolicy `json:"evaluation"`
}

type RoutingCanaryCapacityPolicy struct {
	Mode      string `json:"mode"`
	RPM       int64  `json:"rpm"`
	InputTPM  int64  `json:"input_tpm"`
	OutputTPM int64  `json:"output_tpm"`
	Inflight  int64  `json:"inflight"`
}

type RoutingCanarySlowStartPolicy struct {
	MinimumFactor   float64 `json:"minimum_factor"`
	RampSeconds     int     `json:"ramp_seconds"`
	StateTTLSeconds int     `json:"state_ttl_seconds"`
}

type RoutingCanaryEvaluationPolicy struct {
	AutoRollbackEnabled                   bool `json:"auto_rollback_enabled"`
	RollbackOnTelemetryGap                bool `json:"rollback_on_telemetry_gap"`
	WindowSeconds                         int  `json:"window_seconds"`
	EvaluationIntervalSeconds             int  `json:"evaluation_interval_seconds"`
	RolloutGraceSeconds                   int  `json:"rollout_grace_seconds"`
	CheckpointLatenessSeconds             int  `json:"checkpoint_lateness_seconds"`
	MinCanaryRequests                     int  `json:"min_canary_requests"`
	MinControlRequests                    int  `json:"min_control_requests"`
	MinTTFTSamples                        int  `json:"min_ttft_samples"`
	MinCostCoverageBasisPoints            int  `json:"min_cost_coverage_basis_points"`
	MinNodeCoverageBasisPoints            int  `json:"min_node_coverage_basis_points"`
	MaxSuccessRateDropBasisPoints         int  `json:"max_success_rate_drop_basis_points"`
	HardMinSuccessRateBasisPoints         int  `json:"hard_min_success_rate_basis_points"`
	MaxP95TTFTRatioBasisPoints            int  `json:"max_p95_ttft_ratio_basis_points"`
	MinP95TTFTDeltaMilliseconds           int  `json:"min_p95_ttft_delta_ms"`
	MaxCostRatioBasisPoints               int  `json:"max_cost_ratio_basis_points"`
	MaxRetryAmplificationRatioBasisPoints int  `json:"max_retry_amplification_ratio_basis_points"`
	ConsecutiveBreachWindows              int  `json:"consecutive_breach_windows"`
}

func DefaultRoutingCanaryPolicy() RoutingCanaryPolicy {
	return RoutingCanaryPolicy{
		HedgingEnabled:        false,
		MaxConcurrentAttempts: 1,
		Capacity: RoutingCanaryCapacityPolicy{
			Mode:      RoutingCanaryCapacityModeLocalSoft,
			RPM:       600,
			InputTPM:  1_000_000,
			OutputTPM: 250_000,
			Inflight:  32,
		},
		SlowStart: RoutingCanarySlowStartPolicy{
			MinimumFactor:   0.10,
			RampSeconds:     5 * 60,
			StateTTLSeconds: 24 * 60 * 60,
		},
		Evaluation: RoutingCanaryEvaluationPolicy{
			AutoRollbackEnabled:                   true,
			RollbackOnTelemetryGap:                true,
			WindowSeconds:                         5 * 60,
			EvaluationIntervalSeconds:             60,
			RolloutGraceSeconds:                   2 * 60,
			CheckpointLatenessSeconds:             15,
			MinCanaryRequests:                     100,
			MinControlRequests:                    100,
			MinTTFTSamples:                        50,
			MinCostCoverageBasisPoints:            8_000,
			MinNodeCoverageBasisPoints:            8_000,
			MaxSuccessRateDropBasisPoints:         200,
			HardMinSuccessRateBasisPoints:         9_000,
			MaxP95TTFTRatioBasisPoints:            15_000,
			MinP95TTFTDeltaMilliseconds:           250,
			MaxCostRatioBasisPoints:               12_000,
			MaxRetryAmplificationRatioBasisPoints: 12_500,
			ConsecutiveBreachWindows:              2,
		},
	}
}

func ResolveRoutingCanaryPolicy(policy json.RawMessage) (RoutingCanaryPolicy, error) {
	resolved := DefaultRoutingCanaryPolicy()
	var root map[string]json.RawMessage
	if err := common.Unmarshal(policy, &root); err != nil || root == nil {
		return RoutingCanaryPolicy{}, ErrRoutingPolicyInvalid
	}
	canaryJSON, exists := root["canary"]
	if !exists {
		return NormalizeRoutingCanaryPolicy(resolved)
	}
	canary, err := routingCanaryObject(canaryJSON)
	if err != nil {
		return RoutingCanaryPolicy{}, err
	}
	if err := decodeRoutingCanaryField(canary, "hedging_enabled", &resolved.HedgingEnabled); err != nil {
		return RoutingCanaryPolicy{}, err
	}
	if err := decodeRoutingCanaryField(canary, "max_concurrent_attempts", &resolved.MaxConcurrentAttempts); err != nil {
		return RoutingCanaryPolicy{}, err
	}
	if raw, ok := canary["capacity"]; ok {
		capacity, err := routingCanaryObject(raw)
		if err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(capacity, "mode", &resolved.Capacity.Mode); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(capacity, "rpm", &resolved.Capacity.RPM); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(capacity, "input_tpm", &resolved.Capacity.InputTPM); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(capacity, "output_tpm", &resolved.Capacity.OutputTPM); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(capacity, "inflight", &resolved.Capacity.Inflight); err != nil {
			return RoutingCanaryPolicy{}, err
		}
	}
	if raw, ok := canary["slow_start"]; ok {
		slowStart, err := routingCanaryObject(raw)
		if err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(slowStart, "minimum_factor", &resolved.SlowStart.MinimumFactor); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(slowStart, "ramp_seconds", &resolved.SlowStart.RampSeconds); err != nil {
			return RoutingCanaryPolicy{}, err
		}
		if err := decodeRoutingCanaryField(slowStart, "state_ttl_seconds", &resolved.SlowStart.StateTTLSeconds); err != nil {
			return RoutingCanaryPolicy{}, err
		}
	}
	if raw, ok := canary["evaluation"]; ok {
		evaluation, err := routingCanaryObject(raw)
		if err != nil {
			return RoutingCanaryPolicy{}, err
		}
		fields := []struct {
			name   string
			target any
		}{
			{name: "auto_rollback_enabled", target: &resolved.Evaluation.AutoRollbackEnabled},
			{name: "rollback_on_telemetry_gap", target: &resolved.Evaluation.RollbackOnTelemetryGap},
			{name: "window_seconds", target: &resolved.Evaluation.WindowSeconds},
			{name: "evaluation_interval_seconds", target: &resolved.Evaluation.EvaluationIntervalSeconds},
			{name: "rollout_grace_seconds", target: &resolved.Evaluation.RolloutGraceSeconds},
			{name: "checkpoint_lateness_seconds", target: &resolved.Evaluation.CheckpointLatenessSeconds},
			{name: "min_canary_requests", target: &resolved.Evaluation.MinCanaryRequests},
			{name: "min_control_requests", target: &resolved.Evaluation.MinControlRequests},
			{name: "min_ttft_samples", target: &resolved.Evaluation.MinTTFTSamples},
			{name: "min_cost_coverage_basis_points", target: &resolved.Evaluation.MinCostCoverageBasisPoints},
			{name: "min_node_coverage_basis_points", target: &resolved.Evaluation.MinNodeCoverageBasisPoints},
			{name: "max_success_rate_drop_basis_points", target: &resolved.Evaluation.MaxSuccessRateDropBasisPoints},
			{name: "hard_min_success_rate_basis_points", target: &resolved.Evaluation.HardMinSuccessRateBasisPoints},
			{name: "max_p95_ttft_ratio_basis_points", target: &resolved.Evaluation.MaxP95TTFTRatioBasisPoints},
			{name: "min_p95_ttft_delta_ms", target: &resolved.Evaluation.MinP95TTFTDeltaMilliseconds},
			{name: "max_cost_ratio_basis_points", target: &resolved.Evaluation.MaxCostRatioBasisPoints},
			{name: "max_retry_amplification_ratio_basis_points", target: &resolved.Evaluation.MaxRetryAmplificationRatioBasisPoints},
			{name: "consecutive_breach_windows", target: &resolved.Evaluation.ConsecutiveBreachWindows},
		}
		for _, field := range fields {
			if err := decodeRoutingCanaryField(evaluation, field.name, field.target); err != nil {
				return RoutingCanaryPolicy{}, err
			}
		}
	}
	return NormalizeRoutingCanaryPolicy(resolved)
}

func NormalizeRoutingCanaryPolicy(policy RoutingCanaryPolicy) (RoutingCanaryPolicy, error) {
	capacity := policy.Capacity
	slowStart := policy.SlowStart
	evaluation := policy.Evaluation
	if policy.HedgingEnabled || policy.MaxConcurrentAttempts != 1 ||
		capacity.Mode != RoutingCanaryCapacityModeLocalSoft || capacity.RPM < 1 || capacity.RPM > routingCanaryCapacityRPMMax ||
		capacity.InputTPM < 1 || capacity.InputTPM > routingCanaryCapacityTPMMax ||
		capacity.OutputTPM < 1 || capacity.OutputTPM > routingCanaryCapacityTPMMax ||
		capacity.Inflight < 1 || capacity.Inflight > routingCanaryCapacityInflightMax ||
		math.IsNaN(slowStart.MinimumFactor) || math.IsInf(slowStart.MinimumFactor, 0) ||
		slowStart.MinimumFactor < routingCanarySlowStartFactorMin || slowStart.MinimumFactor > routingCanarySlowStartFactorMax ||
		slowStart.RampSeconds < routingCanarySlowStartRampMinSec || slowStart.RampSeconds > routingCanarySlowStartRampMaxSec ||
		slowStart.StateTTLSeconds < slowStart.RampSeconds || slowStart.StateTTLSeconds > routingCanarySlowStartStateMaxSec ||
		evaluation.WindowSeconds < 60 || evaluation.WindowSeconds > 3_600 ||
		evaluation.EvaluationIntervalSeconds < 10 || evaluation.EvaluationIntervalSeconds > 300 ||
		evaluation.WindowSeconds%evaluation.EvaluationIntervalSeconds != 0 ||
		evaluation.RolloutGraceSeconds < 0 || evaluation.RolloutGraceSeconds > 3_600 ||
		evaluation.CheckpointLatenessSeconds < 5 || evaluation.CheckpointLatenessSeconds > 60 ||
		evaluation.CheckpointLatenessSeconds > evaluation.EvaluationIntervalSeconds ||
		evaluation.MinCanaryRequests < 10 || evaluation.MinCanaryRequests > 100_000 ||
		evaluation.MinControlRequests < 10 || evaluation.MinControlRequests > 1_000_000 ||
		evaluation.MinTTFTSamples < 10 || evaluation.MinTTFTSamples > 100_000 ||
		!routingCanaryBasisPoints(evaluation.MinCostCoverageBasisPoints) ||
		evaluation.MinNodeCoverageBasisPoints < 1 || evaluation.MinNodeCoverageBasisPoints > 10_000 ||
		evaluation.MaxSuccessRateDropBasisPoints < 0 || evaluation.MaxSuccessRateDropBasisPoints > 5_000 ||
		!routingCanaryBasisPoints(evaluation.HardMinSuccessRateBasisPoints) ||
		evaluation.MaxP95TTFTRatioBasisPoints < 10_000 || evaluation.MaxP95TTFTRatioBasisPoints > 100_000 ||
		evaluation.MinP95TTFTDeltaMilliseconds < 0 || evaluation.MinP95TTFTDeltaMilliseconds > 60_000 ||
		evaluation.MaxCostRatioBasisPoints < 10_000 || evaluation.MaxCostRatioBasisPoints > 100_000 ||
		evaluation.MaxRetryAmplificationRatioBasisPoints < 10_000 || evaluation.MaxRetryAmplificationRatioBasisPoints > 100_000 ||
		evaluation.ConsecutiveBreachWindows < 1 || evaluation.ConsecutiveBreachWindows > 10 {
		return RoutingCanaryPolicy{}, ErrRoutingPolicyInvalid
	}
	return policy, nil
}

func routingCanaryObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrRoutingPolicyInvalid
	}
	var object map[string]json.RawMessage
	if err := common.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, ErrRoutingPolicyInvalid
	}
	return object, nil
}

func decodeRoutingCanaryField(object map[string]json.RawMessage, name string, target any) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || common.Unmarshal(raw, target) != nil {
		return ErrRoutingPolicyInvalid
	}
	return nil
}

func routingCanaryBasisPoints(value int) bool {
	return value >= 0 && value <= 10_000
}
