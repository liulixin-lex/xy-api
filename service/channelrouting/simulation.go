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
	PoolID         int
	Cursor         int
	Limit          int
	Selector       SimulationSelectorOverrides
	BalancedPolicy *BalancedPoolPolicy
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
	SimulatedAlgorithm    string  `json:"simulated_algorithm"`
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
	SimulatedAlgorithm     string                       `json:"simulated_algorithm"`
}

func RunHistoricalSimulation(ctx context.Context, options HistoricalSimulationOptions) (HistoricalSimulationResult, error) {
	result := HistoricalSimulationResult{
		PoolID:             options.PoolID,
		Cursor:             options.Cursor,
		Limit:              options.Limit,
		SkipReasons:        make(map[string]int),
		Samples:            []HistoricalSimulationSample{},
		Skipped:            []HistoricalSimulationSkip{},
		SimulatedAlgorithm: DecisionAlgorithmShadowV1,
	}
	if err := validateHistoricalSimulationOptions(options); err != nil {
		return result, err
	}
	if options.BalancedPolicy != nil {
		normalized, err := normalizeBalancedPoolPolicy(*options.BalancedPolicy)
		if err != nil {
			return result, ErrSimulationInvalidOptions
		}
		options.BalancedPolicy = &normalized
		result.SimulatedAlgorithm = DecisionAlgorithmBalancedV1
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
		baseline, simulated, counterfactualHash, err := simulateHistoricalDecision(ctx, audit, options)
		if err != nil {
			result.addSkip(audit.DecisionID, simulationSkipReason(err))
			continue
		}

		sample := HistoricalSimulationSample{
			DecisionID:            audit.DecisionID,
			CreatedTime:           audit.CreatedTime,
			AlgorithmVersion:      audit.AlgorithmVersion,
			ActualChannelID:       audit.ActualChannelID,
			BaselineChannelID:     baseline.channelID,
			SimulatedChannelID:    simulated.channelID,
			MatchesActual:         simulated.channelID > 0 && simulated.channelID == audit.ActualChannelID,
			SelectionChanged:      simulated.channelID != baseline.channelID,
			BaselineCostKnown:     baseline.costKnown,
			BaselineExpectedCost:  baseline.cost,
			SimulatedCostKnown:    simulated.costKnown,
			SimulatedExpectedCost: simulated.cost,
			CounterfactualHash:    counterfactualHash,
			SimulatedAlgorithm:    result.SimulatedAlgorithm,
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
	if options.BalancedPolicy != nil {
		if _, err := normalizeBalancedPoolPolicy(*options.BalancedPolicy); err != nil || simulationSelectorOverridesPresent(options.Selector) {
			return ErrSimulationInvalidOptions
		}
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

type historicalSimulationDecision struct {
	channelID int
	cost      float64
	costKnown bool
}

func simulateHistoricalDecision(
	ctx context.Context,
	audit model.RoutingDecisionAudit,
	options HistoricalSimulationOptions,
) (historicalSimulationDecision, historicalSimulationDecision, string, error) {
	if options.BalancedPolicy != nil {
		return simulateBalancedHistoricalDecision(ctx, audit, *options.BalancedPolicy)
	}
	if audit.AlgorithmVersion != DecisionAlgorithmShadowV1 {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrShadowReplayAlgorithm
	}
	baseline, err := ReplayDecisionAudit(audit)
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
	}
	replayInputJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrShadowReplayInvalid
	}
	var input ShadowReplayInput
	if common.UnmarshalJsonStr(replayInputJSON, &input) != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrShadowReplayInvalid
	}
	applySimulationSelectorOverrides(&input.Settings, options.Selector)
	if !validShadowSettings(input.Settings) {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrSimulationInvalidOptions
	}
	input.SnapshotHash = ""
	counterfactualHash, err := input.computeHash()
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
	}
	input.SnapshotHash = counterfactualHash
	simulated, err := RunShadowReplay(input)
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
	}
	return historicalSimulationDecision{
			channelID: baseline.SelectedChannelID, cost: baseline.SelectedCost, costKnown: baseline.SelectedCostKnown,
		}, historicalSimulationDecision{
			channelID: simulated.SelectedChannelID, cost: simulated.SelectedCost, costKnown: simulated.SelectedCostKnown,
		}, counterfactualHash, nil
}

func simulateBalancedHistoricalDecision(
	ctx context.Context,
	audit model.RoutingDecisionAudit,
	policy BalancedPoolPolicy,
) (historicalSimulationDecision, historicalSimulationDecision, string, error) {
	replayInputJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrBalancedReplayInvalid
	}
	var baseline historicalSimulationDecision
	var input BalancedReplayInput
	switch audit.AlgorithmVersion {
	case DecisionAlgorithmBalancedV1:
		baselineResult, replayErr := ReplayBalancedDecisionAudit(audit)
		if replayErr != nil {
			return historicalSimulationDecision{}, historicalSimulationDecision{}, "", replayErr
		}
		if common.UnmarshalJsonStr(replayInputJSON, &input) != nil {
			return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrBalancedReplayInvalid
		}
		baseline = historicalSimulationDecision{
			channelID: baselineResult.SelectedChannelID,
			cost:      baselineResult.SelectedCost, costKnown: baselineResult.SelectedCostKnown,
		}
	case DecisionAlgorithmShadowV1, DecisionAlgorithmCanaryV1:
		baselineResult, replayErr := ReplayDecisionAudit(audit)
		if replayErr != nil {
			return historicalSimulationDecision{}, historicalSimulationDecision{}, "", replayErr
		}
		var shadowInput ShadowReplayInput
		if common.UnmarshalJsonStr(replayInputJSON, &shadowInput) != nil || shadowInput.Validate() != nil {
			return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrShadowReplayInvalid
		}
		input, err = balancedReplayInputFromShadow(shadowInput, policy)
		if err != nil {
			return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
		}
		baseline = historicalSimulationDecision{
			channelID: baselineResult.SelectedChannelID,
			cost:      baselineResult.SelectedCost, costKnown: baselineResult.SelectedCostKnown,
		}
	default:
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", ErrShadowReplayAlgorithm
	}
	input.Settings.Policy = policy
	input.SnapshotHash = ""
	counterfactualHash, err := input.computeHash()
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
	}
	input.SnapshotHash = counterfactualHash
	simulated, err := RunBalancedReplay(input)
	if err != nil {
		return historicalSimulationDecision{}, historicalSimulationDecision{}, "", err
	}
	return baseline, historicalSimulationDecision{
		channelID: simulated.SelectedChannelID,
		cost:      simulated.SelectedCost,
		costKnown: simulated.SelectedCostKnown,
	}, counterfactualHash, nil
}

func balancedReplayInputFromShadow(input ShadowReplayInput, policy BalancedPoolPolicy) (BalancedReplayInput, error) {
	candidates := make([]BalancedReplayCandidate, 0, len(input.Candidates))
	for index := range input.Candidates {
		source := input.Candidates[index]
		confidence := 0.50
		explorationEligible := true
		if source.Metric != nil {
			confidence = 1
			explorationEligible = source.Metric.ReliabilityRequestCount < int64(policy.MinVolume)
			if explorationEligible {
				confidence = 0.75
			}
		}
		slowStartFactor := source.SlowStartFactor
		if input.AlgorithmVersion == DecisionAlgorithmShadowV1 && slowStartFactor == 0 {
			slowStartFactor = 1
		}
		metricUpdatedUnix := int64(0)
		if source.Metric != nil {
			metricUpdatedUnix = input.Settings.NowUnix
		}
		candidates = append(candidates, BalancedReplayCandidate{
			PoolMemberID: source.PoolMemberID, ChannelID: source.ChannelID, CredentialID: source.CredentialID,
			BusinessTier: source.Priority, TargetWeight: float64(source.Weight), Confidence: confidence,
			Freshness: 1, SlowStartFactor: slowStartFactor, MetricUpdatedUnix: metricUpdatedUnix,
			ExplorationEligible: explorationEligible, HardExclusionReason: source.RequestExclusionReason,
			Metric: source.Metric, Cost: source.Cost, Breaker: source.Breaker, Capacity: source.Capacity,
		})
	}
	return buildBalancedReplayInput(
		input.PoolID,
		input.PolicyRevision,
		input.RuntimeGeneration,
		input.PolicyHash,
		input.Profile,
		BalancedReplaySettings{
			Policy: policy, PreparedAtUnix: input.Settings.NowUnix,
			PreparedAtUnixMilli: input.Settings.NowUnixMilli, RequestNowUnixMilli: input.Settings.NowUnixMilli,
			RandomSeed: input.Settings.RandomSeed, PreferTTFT: input.Settings.PreferTTFT,
		},
		candidates,
		nil,
		nil,
	)
}

func simulationSelectorOverridesPresent(overrides SimulationSelectorOverrides) bool {
	return overrides.WeightAvailability != nil || overrides.WeightLatency != nil ||
		overrides.WeightThroughput != nil || overrides.WeightCost != nil ||
		overrides.AvailabilityFloor != nil || overrides.MinVolume != nil || overrides.TopK != nil ||
		overrides.MaxEjectedPct != nil || overrides.HalfOpenProbes != nil || overrides.SnapshotStaleSec != nil
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
	case errors.Is(err, ErrBalancedReplayHash):
		return "hash_mismatch"
	case errors.Is(err, ErrBalancedReplayInvalid):
		return "invalid_replay"
	case errors.Is(err, ErrSimulationInvalidOptions):
		return "invalid_options"
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
