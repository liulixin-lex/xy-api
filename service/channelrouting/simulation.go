package channelrouting

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	DefaultSimulationLimit = 50
	MaxSimulationLimit     = 100

	maxSimulationMinVolume        = 10_000_000
	maxSimulationHalfOpenProbes   = 1_000
	maxSimulationSnapshotStaleSec = 30 * 24 * 60 * 60
)

var ErrSimulationInvalidOptions = errors.New("invalid channel routing simulation options")

type SimulationSelectorOverrides struct {
	WeightAvailability *float64 `json:"weight_availability,omitempty"`
	WeightLatency      *float64 `json:"weight_latency,omitempty"`
	WeightThroughput   *float64 `json:"weight_throughput,omitempty"`
	WeightCost         *float64 `json:"weight_cost,omitempty"`
	AvailabilityFloor  *float64 `json:"availability_floor,omitempty"`
	MinVolume          *int     `json:"min_volume,omitempty"`
	TopK               *int     `json:"top_k,omitempty"`
	MaxEjectedPct      *int     `json:"max_ejected_pct,omitempty"`
	HalfOpenProbes     *int     `json:"half_open_probes,omitempty"`
	SnapshotStaleSec   *int     `json:"snapshot_stale_sec,omitempty"`
}

type HistoricalSimulationOptions struct {
	PoolID   int
	Cursor   int
	Limit    int
	Selector SimulationSelectorOverrides
}

type HistoricalSimulationSample struct {
	DecisionID            string  `json:"decision_id"`
	CreatedTime           int64   `json:"created_time"`
	AlgorithmVersion      string  `json:"algorithm_version"`
	ActualChannelID       int     `json:"actual_channel_id"`
	BaselineChannelID     int     `json:"baseline_channel_id"`
	SimulatedChannelID    int     `json:"simulated_channel_id"`
	MatchesActual         bool    `json:"matches_actual"`
	SelectionChanged      bool    `json:"selection_changed"`
	BaselineCostKnown     bool    `json:"baseline_cost_known"`
	BaselineExpectedCost  float64 `json:"baseline_expected_cost"`
	SimulatedCostKnown    bool    `json:"simulated_cost_known"`
	SimulatedExpectedCost float64 `json:"simulated_expected_cost"`
	ExpectedCostDelta     float64 `json:"expected_cost_delta"`
	CounterfactualHash    string  `json:"counterfactual_hash"`
}

type HistoricalSimulationSkip struct {
	DecisionID string `json:"decision_id"`
	Reason     string `json:"reason"`
}

type HistoricalSimulationResult struct {
	PoolID                 int                          `json:"pool_id"`
	Cursor                 int                          `json:"cursor"`
	NextCursor             int                          `json:"next_cursor"`
	Limit                  int                          `json:"limit"`
	ScannedSamples         int                          `json:"scanned_samples"`
	EvaluatedSamples       int                          `json:"evaluated_samples"`
	ActualMatchCount       int                          `json:"actual_match_count"`
	ActualMatchRate        *float64                     `json:"actual_match_rate,omitempty"`
	SelectionChangedCount  int                          `json:"selection_changed_count"`
	SelectionChangeRate    *float64                     `json:"selection_change_rate,omitempty"`
	CostKnownSamples       int                          `json:"cost_known_samples"`
	TotalExpectedCostDelta float64                      `json:"total_expected_cost_delta"`
	AverageCostDelta       *float64                     `json:"average_expected_cost_delta,omitempty"`
	SkipReasons            map[string]int               `json:"skip_reasons"`
	Samples                []HistoricalSimulationSample `json:"samples"`
	Skipped                []HistoricalSimulationSkip   `json:"skipped"`
}

func RunHistoricalSimulation(ctx context.Context, options HistoricalSimulationOptions) (HistoricalSimulationResult, error) {
	result := HistoricalSimulationResult{
		PoolID:      options.PoolID,
		Cursor:      options.Cursor,
		Limit:       options.Limit,
		SkipReasons: make(map[string]int),
		Samples:     []HistoricalSimulationSample{},
		Skipped:     []HistoricalSimulationSkip{},
	}
	if err := validateHistoricalSimulationOptions(options); err != nil {
		return result, err
	}

	query := model.DB.WithContext(ctx).
		Where("pool_id = ? AND replayable = ?", options.PoolID, true)
	if options.Cursor > 0 {
		query = query.Where("id < ?", options.Cursor)
	}
	var audits []model.RoutingDecisionAudit
	if err := query.Order("id desc").Limit(options.Limit + 1).Find(&audits).Error; err != nil {
		return result, err
	}
	if len(audits) > options.Limit {
		audits = audits[:options.Limit]
		result.NextCursor = audits[len(audits)-1].ID
	}
	result.ScannedSamples = len(audits)
	result.Samples = make([]HistoricalSimulationSample, 0, len(audits))
	result.Skipped = make([]HistoricalSimulationSkip, 0, len(audits))

	for index := range audits {
		audit := audits[index]
		if audit.AlgorithmVersion != DecisionAlgorithmShadowV1 {
			result.addSkip(audit.DecisionID, "unsupported_algorithm")
			continue
		}
		baseline, err := ReplayDecisionAudit(audit)
		if err != nil {
			result.addSkip(audit.DecisionID, simulationSkipReason(err))
			continue
		}

		replayInputJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
		if err != nil {
			result.addSkip(audit.DecisionID, "invalid_replay")
			continue
		}
		var input ShadowReplayInput
		if err := common.UnmarshalJsonStr(replayInputJSON, &input); err != nil {
			result.addSkip(audit.DecisionID, "invalid_replay")
			continue
		}
		applySimulationSelectorOverrides(&input.Settings, options.Selector)
		if !validShadowSettings(input.Settings) {
			return result, ErrSimulationInvalidOptions
		}
		input.SnapshotHash = ""
		counterfactualHash, err := input.computeHash()
		if err != nil {
			result.addSkip(audit.DecisionID, "counterfactual_failed")
			continue
		}
		input.SnapshotHash = counterfactualHash
		simulated, err := RunShadowReplay(input)
		if err != nil {
			result.addSkip(audit.DecisionID, "counterfactual_failed")
			continue
		}

		sample := HistoricalSimulationSample{
			DecisionID:            audit.DecisionID,
			CreatedTime:           audit.CreatedTime,
			AlgorithmVersion:      audit.AlgorithmVersion,
			ActualChannelID:       audit.ActualChannelID,
			BaselineChannelID:     baseline.SelectedChannelID,
			SimulatedChannelID:    simulated.SelectedChannelID,
			MatchesActual:         simulated.SelectedChannelID > 0 && simulated.SelectedChannelID == audit.ActualChannelID,
			SelectionChanged:      simulated.SelectedChannelID != baseline.SelectedChannelID,
			BaselineCostKnown:     baseline.SelectedCostKnown,
			BaselineExpectedCost:  baseline.SelectedCost,
			SimulatedCostKnown:    simulated.SelectedCostKnown,
			SimulatedExpectedCost: simulated.SelectedCost,
			CounterfactualHash:    counterfactualHash,
		}
		if sample.BaselineCostKnown && sample.SimulatedCostKnown {
			delta := sample.SimulatedExpectedCost - sample.BaselineExpectedCost
			nextTotal := result.TotalExpectedCostDelta + delta
			if simulationFinite(delta) && simulationFinite(nextTotal) {
				sample.ExpectedCostDelta = delta
				result.CostKnownSamples++
				result.TotalExpectedCostDelta = nextTotal
			}
		}
		if sample.MatchesActual {
			result.ActualMatchCount++
		}
		if sample.SelectionChanged {
			result.SelectionChangedCount++
		}
		result.Samples = append(result.Samples, sample)
		result.EvaluatedSamples++
	}

	if result.EvaluatedSamples > 0 {
		matchRate := float64(result.ActualMatchCount) / float64(result.EvaluatedSamples)
		changeRate := float64(result.SelectionChangedCount) / float64(result.EvaluatedSamples)
		result.ActualMatchRate = &matchRate
		result.SelectionChangeRate = &changeRate
	}
	if result.CostKnownSamples > 0 {
		average := result.TotalExpectedCostDelta / float64(result.CostKnownSamples)
		result.AverageCostDelta = &average
	}
	return result, nil
}

func validateHistoricalSimulationOptions(options HistoricalSimulationOptions) error {
	if options.PoolID <= 0 || options.Cursor < 0 || options.Limit < 1 || options.Limit > MaxSimulationLimit {
		return ErrSimulationInvalidOptions
	}
	for _, value := range []*float64{
		options.Selector.WeightAvailability,
		options.Selector.WeightLatency,
		options.Selector.WeightThroughput,
		options.Selector.WeightCost,
	} {
		if value != nil && (!finiteShadowNumber(*value) || *value < 0 || *value > 1) {
			return ErrSimulationInvalidOptions
		}
	}
	if options.Selector.WeightAvailability != nil && options.Selector.WeightLatency != nil &&
		options.Selector.WeightThroughput != nil && options.Selector.WeightCost != nil &&
		*options.Selector.WeightAvailability+*options.Selector.WeightLatency+
			*options.Selector.WeightThroughput+*options.Selector.WeightCost <= 0 {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.AvailabilityFloor; value != nil &&
		(!finiteShadowNumber(*value) || *value < 0 || *value > 1) {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.MinVolume; value != nil && (*value < 0 || *value > maxSimulationMinVolume) {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.TopK; value != nil && (*value < 1 || *value > MaxDecisionCandidates) {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.MaxEjectedPct; value != nil && (*value < 0 || *value > 100) {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.HalfOpenProbes; value != nil && (*value < 1 || *value > maxSimulationHalfOpenProbes) {
		return ErrSimulationInvalidOptions
	}
	if value := options.Selector.SnapshotStaleSec; value != nil && (*value < 1 || *value > maxSimulationSnapshotStaleSec) {
		return ErrSimulationInvalidOptions
	}
	return nil
}

func applySimulationSelectorOverrides(settings *ShadowSelectorSettings, overrides SimulationSelectorOverrides) {
	if overrides.WeightAvailability != nil {
		settings.WeightAvailability = *overrides.WeightAvailability
	}
	if overrides.WeightLatency != nil {
		settings.WeightLatency = *overrides.WeightLatency
	}
	if overrides.WeightThroughput != nil {
		settings.WeightThroughput = *overrides.WeightThroughput
	}
	if overrides.WeightCost != nil {
		settings.WeightCost = *overrides.WeightCost
	}
	if overrides.AvailabilityFloor != nil {
		settings.AvailabilityFloor = *overrides.AvailabilityFloor
	}
	if overrides.MinVolume != nil {
		settings.MinVolume = *overrides.MinVolume
	}
	if overrides.TopK != nil {
		settings.TopK = *overrides.TopK
	}
	if overrides.MaxEjectedPct != nil {
		settings.MaxEjectedPct = *overrides.MaxEjectedPct
	}
	if overrides.HalfOpenProbes != nil {
		settings.HalfOpenProbes = *overrides.HalfOpenProbes
	}
	if overrides.SnapshotStaleSec != nil {
		settings.SnapshotStaleSec = *overrides.SnapshotStaleSec
	}
}

func simulationSkipReason(err error) string {
	switch {
	case errors.Is(err, ErrShadowReplayHash):
		return "hash_mismatch"
	case errors.Is(err, ErrShadowReplayAlgorithm):
		return "unsupported_algorithm"
	case errors.Is(err, ErrShadowReplayAudit):
		return "audit_mismatch"
	case errors.Is(err, ErrShadowReplayInvalid):
		return "invalid_replay"
	default:
		return "replay_failed"
	}
}

func (result *HistoricalSimulationResult) addSkip(decisionID string, reason string) {
	result.SkipReasons[reason]++
	result.Skipped = append(result.Skipped, HistoricalSimulationSkip{DecisionID: decisionID, Reason: reason})
}

func simulationFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
