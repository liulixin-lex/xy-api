package routing

import (
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/model"
)

const (
	MaxBalancedCandidates = 4_096

	BalancedExclusionInvalidCandidate              = "invalid_candidate"
	BalancedExclusionCapacityExhausted             = "capacity_exhausted"
	BalancedExclusionCredentialUnavailable         = "credential_unavailable"
	BalancedExclusionBalanceUnavailable            = "balance_unavailable"
	BalancedExclusionReliabilityOpen               = "reliability_breaker_open"
	BalancedExclusionAvailabilityFloor             = "availability_slo_floor"
	BalancedExclusionCostUnknown                   = "cost_unknown"
	BalancedExclusionCostBudget                    = "cost_budget_exceeded"
	BalancedExclusionSlowStartUnavailable          = "slow_start_unavailable"
	BalancedExclusionBusinessTier                  = "business_tier_cascade"
	BalancedExclusionUtilityZero                   = "utility_zero"
	BalancedExclusionRequestExcluded               = "request_excluded"
	BalancedExclusionRuntimeBlocked                = "runtime_blocked"
	balancedExplorationMinBasisPoints              = 100
	balancedExplorationMaxBasisPoints              = 300
	balancedWilsonZMin                             = 1
	balancedWilsonZMax                             = 5
	balancedExclusionReasonMaxBytes                = 128
	balancedDefaultTargetWeight            float64 = 1
	balancedScoreScale                     uint64  = 1_000_000_000
)

var (
	ErrBalancedPolicyInvalid    = errors.New("invalid balanced routing policy")
	ErrBalancedCandidateInvalid = errors.New("invalid balanced routing candidate")
)

type BalancedSettings struct {
	Weights                        Weights
	AvailabilityTarget             float64
	AvailabilityFloor              float64
	LatencyTargetMs                float64
	ThroughputTarget               float64
	CostTarget                     float64
	CostBudget                     float64
	MinimumVolume                  int
	WilsonZ                        float64
	UnknownAvailability            float64
	UnknownLatencyUtility          float64
	UnknownThroughputUtility       float64
	UnknownCostUtility             float64
	ProtectionBandBasisPoints      int
	ExplorationBasisPoints         int
	MinimumExplorationScore        float64
	MaxCapacityUtilization         float64
	AffinityMaxCapacityUtilization float64
	QueueTargetMs                  float64
	DegradedMultiplier             float64
	SoftFallbackMultiplier         float64
	HalfOpenProbes                 int
	SnapshotStaleSec               int
	NowUnix                        int64
	NowUnixMilli                   int64
	RandomSeed                     int64
	PreferredChannelID             int
	PreferTTFT                     bool
	RequireKnownCost               bool
	AllowSoftFailureFallback       bool
	EnforceBusinessTierCascade     bool
}

type BalancedCandidate struct {
	Candidate           Candidate
	BusinessTier        int64
	TargetWeight        float64
	Confidence          float64
	Freshness           float64
	SlowStartFactor     float64
	CapacityUtilization float64
	QueueDelayMs        float64
	MetricUpdatedUnix   int64
	ExplorationEligible bool
	HardExclusionReason string
}

type BalancedRankedCandidate struct {
	Candidate             BalancedCandidate
	Channel               *model.Channel
	Availability          float64
	AvailabilityUtility   float64
	LatencyUtility        float64
	ThroughputUtility     float64
	CostUtility           float64
	CostKnown             bool
	LoadUtility           float64
	BaseUtilityScore      float64
	UtilityScore          float64
	AdjustedScore         float64
	BaseUtilityScoreUnits uint64
	UtilityScoreUnits     uint64
	AdjustedScoreUnits    uint64
	InProtectionBand      bool
	Degraded              bool
	SoftFallback          bool
}

type BalancedExclusion struct {
	ChannelID int
	Reason    string
}

type BalancedDecision struct {
	Ranked            []BalancedRankedCandidate
	Excluded          []BalancedExclusion
	Selected          *BalancedRankedCandidate // Materialized by SelectBalanced, not PreparedBalancedPool.Select.
	SelectedChannelID int                      // Authoritative selected ID for the prepared hot path.
	SampledChannelIDs []int
	AffinityUsed      bool
	ExplorationUsed   bool
	SoftFallback      bool
}

// BalancedRuntimeState is a partial live-state override. Non-zero scalar
// values override by default; use the matching Has field to apply an explicit
// zero without clearing unrelated snapshot state.
type BalancedRuntimeState struct {
	CapacityUtilization    float64
	QueueDelayMs           float64
	Inflight               int64
	HasCapacityUtilization bool
	HasQueueDelay          bool
	HasInflight            bool
	CooldownUntilUnixMilli *int64
	SlowStartFactor        *float64
	Breaker                *BreakerSnapshot
	Cost                   *CostSnapshot
	Admission              BalancedRuntimeAdmission
}

type BalancedRequest struct {
	RandomSeed         int64
	PreferredChannelID int
	NowUnixMilli       int64
	ExcludedChannelIDs map[int]struct{}
	RuntimeByChannelID map[int]BalancedRuntimeState
}

type BalancedRuntimeAdmission string

const (
	BalancedRuntimeAdmissionInherit  BalancedRuntimeAdmission = ""
	BalancedRuntimeAdmissionHealthy  BalancedRuntimeAdmission = "healthy"
	BalancedRuntimeAdmissionDegraded BalancedRuntimeAdmission = "degraded"
	BalancedRuntimeAdmissionSoft     BalancedRuntimeAdmission = "soft_failure"
	BalancedRuntimeAdmissionBlocked  BalancedRuntimeAdmission = "blocked"
)

// PreparedBalancedPool is an immutable, snapshot-scoped selection plan. It
// keeps the full evaluated pool for audit and precompiles weighted draw tables
// so the request path does not rescore or sort every member.
type PreparedBalancedPool struct {
	settings                  BalancedSettings
	normalizedWeights         Weights
	ranked                    []BalancedRankedCandidate
	hot                       []balancedHotCandidate
	excluded                  []BalancedExclusion
	knownChannelIDs           map[int]struct{}
	byChannelID               map[int]int
	softReasonByChannelID     map[int]string
	groups                    []preparedBalancedGroup
	nextBreakerTransitionUnix int64
	breakerTransitionByIndex  []int64
	nextCostExpiryUnix        int64
	costExpiryByIndex         []int64
}

type preparedBalancedGroup struct {
	softFallback  bool
	businessTier  int64
	indexes       []int
	protection    balancedWeightedPool
	protectionSet map[int]struct{}
	exploration   balancedWeightedPool
	resolved      bool
}

const (
	balancedHotDegraded uint8 = 1 << iota
	balancedHotSoftFallback
	balancedHotCostKnown
	balancedHotExplorationEligible
)

type balancedHotCandidate struct {
	channelID              int
	businessTier           int64
	targetWeight           float64
	capacityUtilization    float64
	queueDelayMs           float64
	slowStartFactor        float64
	utilityScore           float64
	utilityScoreUnits      uint64
	inflight               int64
	cooldownUntilUnixMilli int64
	flags                  uint8
}

type balancedWeightedPool struct {
	indexes    []int
	cumulative []float64
	total      float64
}

type balancedEvaluatedCandidate struct {
	candidate    BalancedCandidate
	degraded     bool
	softReason   string
	softFallback bool
}

func SelectBalanced(candidates []BalancedCandidate, settings BalancedSettings) (BalancedDecision, error) {
	prepared, err := PrepareBalanced(candidates, settings)
	if err != nil {
		return BalancedDecision{}, err
	}
	request := BalancedRequest{
		RandomSeed:         settings.RandomSeed,
		PreferredChannelID: settings.PreferredChannelID,
		NowUnixMilli:       settings.NowUnixMilli,
	}
	decision, err := prepared.Select(request)
	if err != nil {
		return BalancedDecision{}, err
	}
	prepared.materializeSelected(request, &decision)
	prepared.decorateDecision(request, &decision)
	return decision, nil
}

func PrepareBalanced(candidates []BalancedCandidate, settings BalancedSettings) (*PreparedBalancedPool, error) {
	if err := validateBalancedSettings(settings); err != nil {
		return nil, err
	}
	if len(candidates) > MaxBalancedCandidates {
		return nil, ErrBalancedCandidateInvalid
	}
	prepared := &PreparedBalancedPool{
		settings:              settings,
		normalizedWeights:     settings.Weights.normalized(),
		ranked:                make([]BalancedRankedCandidate, 0, len(candidates)),
		excluded:              make([]BalancedExclusion, 0, len(candidates)),
		knownChannelIDs:       make(map[int]struct{}, len(candidates)),
		byChannelID:           make(map[int]int, len(candidates)),
		softReasonByChannelID: make(map[int]string),
	}
	for _, source := range candidates {
		channelID, err := validateBalancedCandidate(source)
		if err != nil {
			return nil, err
		}
		if _, exists := prepared.knownChannelIDs[channelID]; exists {
			return nil, ErrBalancedCandidateInvalid
		}
		prepared.knownChannelIDs[channelID] = struct{}{}

		if reason := strings.TrimSpace(source.HardExclusionReason); reason != "" {
			prepared.excluded = append(prepared.excluded, BalancedExclusion{ChannelID: channelID, Reason: reason})
			continue
		}
		costKnown := balancedCostKnown(source.Candidate.Cost, settings)
		if settings.RequireKnownCost && !costKnown {
			prepared.excluded = append(prepared.excluded, BalancedExclusion{ChannelID: channelID, Reason: BalancedExclusionCostUnknown})
			continue
		}
		if costKnown && settings.CostBudget > 0 && source.Candidate.Cost.Cost > settings.CostBudget {
			prepared.excluded = append(prepared.excluded, BalancedExclusion{ChannelID: channelID, Reason: BalancedExclusionCostBudget})
			continue
		}

		evaluated := balancedEvaluatedCandidate{candidate: source}
		breakerReason, degraded, softFailure := balancedBreakerDisposition(source.Candidate.Breaker, settings)
		if breakerReason == BalancedExclusionCredentialUnavailable || breakerReason == BalancedExclusionBalanceUnavailable {
			prepared.excluded = append(prepared.excluded, BalancedExclusion{ChannelID: channelID, Reason: breakerReason})
			continue
		}
		evaluated.degraded = degraded
		if softFailure {
			evaluated.softReason = breakerReason
		}
		availability := balancedAvailability(source.Candidate.Metric, settings)
		if evaluated.softReason == "" && settings.AvailabilityFloor > 0 &&
			balancedHasMinimumVolume(source.Candidate.Metric, settings.MinimumVolume) && availability < settings.AvailabilityFloor {
			evaluated.softReason = BalancedExclusionAvailabilityFloor
		}
		if evaluated.softReason != "" {
			evaluated.softFallback = true
			prepared.softReasonByChannelID[channelID] = evaluated.softReason
		}

		ranked := scoreBalancedCandidate(evaluated, settings, prepared.normalizedWeights)
		if ranked.BaseUtilityScoreUnits == 0 || !balancedFinitePositive(ranked.BaseUtilityScore) {
			prepared.excluded = append(prepared.excluded, BalancedExclusion{ChannelID: channelID, Reason: BalancedExclusionUtilityZero})
			continue
		}
		prepared.ranked = append(prepared.ranked, cloneBalancedRankedCandidate(ranked))
		if breaker := source.Candidate.Breaker; breaker != nil &&
			strings.EqualFold(strings.TrimSpace(breaker.State), BreakerStateOpen) &&
			breaker.CooldownUntilUnix > settings.NowUnix &&
			(prepared.nextBreakerTransitionUnix == 0 || breaker.CooldownUntilUnix < prepared.nextBreakerTransitionUnix) {
			prepared.nextBreakerTransitionUnix = breaker.CooldownUntilUnix
		}
	}

	sort.Slice(prepared.ranked, func(i, j int) bool {
		left := prepared.ranked[i]
		right := prepared.ranked[j]
		if left.SoftFallback != right.SoftFallback {
			return !left.SoftFallback
		}
		if settings.EnforceBusinessTierCascade && left.Candidate.BusinessTier != right.Candidate.BusinessTier {
			return left.Candidate.BusinessTier > right.Candidate.BusinessTier
		}
		if left.UtilityScoreUnits != right.UtilityScoreUnits {
			return left.UtilityScoreUnits > right.UtilityScoreUnits
		}
		if left.AdjustedScoreUnits != right.AdjustedScoreUnits {
			return left.AdjustedScoreUnits > right.AdjustedScoreUnits
		}
		return left.Channel.Id < right.Channel.Id
	})
	prepared.hot = make([]balancedHotCandidate, len(prepared.ranked))
	prepared.breakerTransitionByIndex = make([]int64, len(prepared.ranked))
	prepared.costExpiryByIndex = make([]int64, len(prepared.ranked))
	for index := range prepared.ranked {
		ranked := &prepared.ranked[index]
		prepared.byChannelID[ranked.Channel.Id] = index
		hot := balancedHotCandidate{
			channelID:           ranked.Channel.Id,
			businessTier:        ranked.Candidate.BusinessTier,
			targetWeight:        ranked.Candidate.TargetWeight,
			capacityUtilization: ranked.Candidate.CapacityUtilization,
			queueDelayMs:        ranked.Candidate.QueueDelayMs,
			slowStartFactor:     ranked.Candidate.SlowStartFactor,
			utilityScore:        ranked.UtilityScore,
			utilityScoreUnits:   ranked.UtilityScoreUnits,
			inflight:            inflightCount(ranked.Candidate.Candidate.Metric),
		}
		if ranked.Candidate.Candidate.Capacity != nil {
			hot.cooldownUntilUnixMilli = ranked.Candidate.Candidate.Capacity.CooldownUntilUnixMilli
		}
		if ranked.Degraded {
			hot.flags |= balancedHotDegraded
		}
		if ranked.SoftFallback {
			hot.flags |= balancedHotSoftFallback
		}
		if ranked.CostKnown {
			hot.flags |= balancedHotCostKnown
		}
		if ranked.Candidate.ExplorationEligible {
			hot.flags |= balancedHotExplorationEligible
		}
		prepared.hot[index] = hot

		breaker := ranked.Candidate.Candidate.Breaker
		if breaker != nil && strings.EqualFold(strings.TrimSpace(breaker.State), BreakerStateOpen) &&
			breaker.CooldownUntilUnix > settings.NowUnix {
			prepared.breakerTransitionByIndex[index] = breaker.CooldownUntilUnix
		}
		cost := ranked.Candidate.Candidate.Cost
		if ranked.CostKnown && cost != nil &&
			(settings.RequireKnownCost || settings.Weights.Cost > 0) {
			expiryUnix := int64(1<<63 - 1)
			if cost.UpdatedUnix <= (1<<63-1)-int64(settings.SnapshotStaleSec) {
				expiryUnix = cost.UpdatedUnix + int64(settings.SnapshotStaleSec)
			}
			prepared.costExpiryByIndex[index] = expiryUnix
			if prepared.nextCostExpiryUnix == 0 || expiryUnix < prepared.nextCostExpiryUnix {
				prepared.nextCostExpiryUnix = expiryUnix
			}
		}
	}
	prepared.groups = prepared.buildStaticGroups()
	sort.Slice(prepared.excluded, func(i, j int) bool {
		if prepared.excluded[i].ChannelID != prepared.excluded[j].ChannelID {
			return prepared.excluded[i].ChannelID < prepared.excluded[j].ChannelID
		}
		return prepared.excluded[i].Reason < prepared.excluded[j].Reason
	})
	return prepared, nil
}

// Select keeps the prepared request path allocation-light. Read the selected
// target from SelectedChannelID; Selected and full diagnostics remain nil.
func (prepared *PreparedBalancedPool) Select(request BalancedRequest) (BalancedDecision, error) {
	if err := prepared.validateRequest(request); err != nil {
		return BalancedDecision{}, err
	}
	if prepared.requiresDynamicReplan(request) {
		return prepared.selectDynamic(request), nil
	}
	for _, source := range prepared.groups {
		group, ok := prepared.resolveGroup(source, request)
		if !ok {
			continue
		}
		decision := BalancedDecision{SoftFallback: group.softFallback}
		prepared.selectFromGroup(group, request, &decision)
		return decision, nil
	}
	return BalancedDecision{}, nil
}

// SelectDetailed returns the same deterministic choice as Select together
// with immutable diagnostics for audit, simulation, and replay paths.
func (prepared *PreparedBalancedPool) SelectDetailed(request BalancedRequest) (BalancedDecision, error) {
	decision, err := prepared.Select(request)
	if err != nil {
		return BalancedDecision{}, err
	}
	prepared.materializeSelected(request, &decision)
	prepared.decorateDecision(request, &decision)
	return decision, nil
}

type balancedEffectiveRuntimeState struct {
	capacityUtilization float64
	queueDelayMs        float64
	inflight            int64
}

type balancedRuntimeEvaluation struct {
	state             balancedEffectiveRuntimeState
	breaker           *BreakerSnapshot
	cost              *CostSnapshot
	slowStartFactor   float64
	baseUtilityScore  float64
	costUtility       float64
	utilityScore      float64
	utilityScoreUnits uint64
	degraded          bool
	softFallback      bool
	costKnown         bool
}

func (prepared *PreparedBalancedPool) runtimeEvaluation(
	index int,
	request *BalancedRequest,
	evaluation *balancedRuntimeEvaluation,
	full bool,
) string {
	if prepared == nil || index < 0 || index >= len(prepared.hot) {
		return BalancedExclusionInvalidCandidate
	}
	hot := &prepared.hot[index]
	channelID := hot.channelID
	if _, excluded := request.ExcludedChannelIDs[channelID]; excluded {
		return BalancedExclusionRequestExcluded
	}
	cooldownUntilUnixMilli := hot.cooldownUntilUnixMilli
	state := balancedEffectiveRuntimeState{
		capacityUtilization: hot.capacityUtilization,
		queueDelayMs:        hot.queueDelayMs,
		inflight:            hot.inflight,
	}
	admission := BalancedRuntimeAdmissionInherit
	var slowStartOverride *float64
	var breakerOverride *BreakerSnapshot
	var costOverride *CostSnapshot
	if current, exists := request.RuntimeByChannelID[channelID]; exists {
		if current.HasCapacityUtilization || current.CapacityUtilization != 0 {
			state.capacityUtilization = current.CapacityUtilization
		}
		if current.HasQueueDelay || current.QueueDelayMs != 0 {
			state.queueDelayMs = current.QueueDelayMs
		}
		if current.HasInflight || current.Inflight != 0 {
			state.inflight = current.Inflight
		}
		admission = current.Admission
		if current.CooldownUntilUnixMilli != nil {
			cooldownUntilUnixMilli = *current.CooldownUntilUnixMilli
		}
		slowStartOverride = current.SlowStartFactor
		breakerOverride = current.Breaker
		costOverride = current.Cost
	}
	evaluation.state = state
	if admission == BalancedRuntimeAdmissionBlocked {
		return BalancedExclusionRuntimeBlocked
	}
	nowUnixMilli := request.NowUnixMilli
	if nowUnixMilli == 0 {
		nowUnixMilli = prepared.settings.NowUnixMilli
	}
	if cooldownUntilUnixMilli > nowUnixMilli {
		return BalancedExclusionCapacityExhausted
	}
	if state.capacityUtilization >= prepared.settings.MaxCapacityUtilization {
		return BalancedExclusionCapacityExhausted
	}
	planOverride := admission != BalancedRuntimeAdmissionInherit ||
		slowStartOverride != nil || breakerOverride != nil || costOverride != nil
	breakerTransitioned := index < len(prepared.breakerTransitionByIndex) &&
		prepared.breakerTransitionByIndex[index] > 0 &&
		nowUnixMilli/1_000 >= prepared.breakerTransitionByIndex[index]
	costExpired := index < len(prepared.costExpiryByIndex) && prepared.costExpiryByIndex[index] > 0 &&
		nowUnixMilli/1_000 > prepared.costExpiryByIndex[index]
	if !planOverride && !breakerTransitioned && !costExpired {
		softFallback := hot.flags&balancedHotSoftFallback != 0
		if softFallback && !prepared.settings.AllowSoftFailureFallback {
			reason := prepared.softReasonByChannelID[channelID]
			if reason == "" {
				reason = BalancedExclusionReliabilityOpen
			}
			return reason
		}
		if hot.slowStartFactor <= 0 {
			return BalancedExclusionSlowStartUnavailable
		}
		if hot.utilityScoreUnits == 0 || !balancedFinitePositive(hot.utilityScore) {
			return BalancedExclusionUtilityZero
		}
		evaluation.slowStartFactor = hot.slowStartFactor
		evaluation.utilityScore = hot.utilityScore
		evaluation.utilityScoreUnits = hot.utilityScoreUnits
		evaluation.degraded = hot.flags&balancedHotDegraded != 0
		evaluation.softFallback = softFallback
		evaluation.costKnown = hot.flags&balancedHotCostKnown != 0
		if full {
			candidate := &prepared.ranked[index]
			evaluation.breaker = candidate.Candidate.Candidate.Breaker
			evaluation.cost = candidate.Candidate.Candidate.Cost
			evaluation.baseUtilityScore = candidate.BaseUtilityScore
			evaluation.costUtility = candidate.CostUtility
		}
		return ""
	}
	candidate := &prepared.ranked[index]

	softReason := prepared.softReasonByChannelID[channelID]
	nonBreakerSoftFailure := softReason != "" && softReason != BalancedExclusionReliabilityOpen
	degraded := candidate.Degraded
	breakerSoftFailure := softReason == BalancedExclusionReliabilityOpen
	breaker := candidate.Candidate.Candidate.Breaker
	if breakerOverride != nil {
		breaker = breakerOverride
	}
	evaluation.breaker = breaker
	if breaker != nil {
		breakerSettings := prepared.settings
		breakerSettings.NowUnix = nowUnixMilli / 1_000
		breakerReason, currentDegraded, currentSoftFailure := balancedBreakerDisposition(breaker, breakerSettings)
		if breakerReason == BalancedExclusionCredentialUnavailable || breakerReason == BalancedExclusionBalanceUnavailable {
			return breakerReason
		}
		degraded = currentDegraded
		breakerSoftFailure = currentSoftFailure
		if currentSoftFailure {
			softReason = breakerReason
		}
	}
	switch admission {
	case BalancedRuntimeAdmissionHealthy:
		degraded = false
		breakerSoftFailure = false
	case BalancedRuntimeAdmissionDegraded:
		degraded = true
		breakerSoftFailure = false
	case BalancedRuntimeAdmissionSoft:
		degraded = false
		breakerSoftFailure = true
		softReason = BalancedExclusionReliabilityOpen
	}
	softFallback := nonBreakerSoftFailure || breakerSoftFailure
	if softFallback && !prepared.settings.AllowSoftFailureFallback {
		if softReason == "" {
			softReason = BalancedExclusionReliabilityOpen
		}
		return softReason
	}

	slowStartFactor := candidate.Candidate.SlowStartFactor
	if slowStartOverride != nil {
		slowStartFactor = *slowStartOverride
	}
	if slowStartFactor <= 0 {
		return BalancedExclusionSlowStartUnavailable
	}
	evaluation.slowStartFactor = slowStartFactor
	evaluation.degraded = degraded
	evaluation.softFallback = softFallback
	evaluation.baseUtilityScore = candidate.BaseUtilityScore
	evaluation.costUtility = candidate.CostUtility
	evaluation.costKnown = candidate.CostKnown
	evaluation.cost = candidate.Candidate.Candidate.Cost
	currentCost := candidate.Candidate.Candidate.Cost
	if costOverride != nil {
		currentCost = costOverride
		evaluation.cost = costOverride
	}
	if costOverride != nil || costExpired {
		costSettings := prepared.settings
		costSettings.NowUnix = nowUnixMilli / 1_000
		costKnown := balancedCostKnown(currentCost, costSettings)
		if prepared.settings.RequireKnownCost && !costKnown {
			return BalancedExclusionCostUnknown
		}
		if costKnown && prepared.settings.CostBudget > 0 && currentCost.Cost > prepared.settings.CostBudget {
			return BalancedExclusionCostBudget
		}
		evaluation.costKnown = costKnown
		evaluation.costUtility = prepared.settings.UnknownCostUtility
		if costKnown {
			evaluation.costUtility = balancedLowerIsBetterUtility(currentCost.Cost, prepared.settings.CostTarget)
		}
		geometric := balancedGeometricUtility(
			prepared.normalizedWeights,
			candidate.AvailabilityUtility,
			candidate.LatencyUtility,
			candidate.ThroughputUtility,
			evaluation.costUtility,
		)
		evaluation.baseUtilityScore = clamp01(geometric * candidate.Candidate.Confidence * candidate.Candidate.Freshness)
	}
	if balancedScoreUnits(evaluation.baseUtilityScore) == 0 || !balancedFinitePositive(evaluation.baseUtilityScore) {
		return BalancedExclusionUtilityZero
	}
	evaluation.utilityScore = evaluation.baseUtilityScore * slowStartFactor
	if degraded {
		evaluation.utilityScore *= prepared.settings.DegradedMultiplier
	}
	if softFallback {
		evaluation.utilityScore *= prepared.settings.SoftFallbackMultiplier
	}
	evaluation.utilityScore = clamp01(evaluation.utilityScore)
	evaluation.utilityScoreUnits = balancedScoreUnits(evaluation.utilityScore)
	if evaluation.utilityScoreUnits == 0 || !balancedFinitePositive(evaluation.utilityScore) {
		return BalancedExclusionUtilityZero
	}
	return ""
}

func (prepared *PreparedBalancedPool) runtimeCandidate(
	index int,
	request BalancedRequest,
) (BalancedRankedCandidate, balancedEffectiveRuntimeState, string) {
	evaluation := balancedRuntimeEvaluation{}
	reason := prepared.runtimeEvaluation(index, &request, &evaluation, true)
	if reason != "" {
		return BalancedRankedCandidate{}, evaluation.state, reason
	}
	candidate := prepared.ranked[index]
	candidate.Candidate.Candidate.Breaker = evaluation.breaker
	if evaluation.cost == nil {
		candidate.Candidate.Candidate.Cost = nil
	} else {
		cost := *evaluation.cost
		candidate.Candidate.Candidate.Cost = &cost
	}
	candidate.Candidate.SlowStartFactor = evaluation.slowStartFactor
	candidate.Candidate.CapacityUtilization = evaluation.state.capacityUtilization
	candidate.Candidate.QueueDelayMs = evaluation.state.queueDelayMs
	candidate.Degraded = evaluation.degraded
	candidate.SoftFallback = evaluation.softFallback
	candidate.BaseUtilityScore = evaluation.baseUtilityScore
	candidate.BaseUtilityScoreUnits = balancedScoreUnits(evaluation.baseUtilityScore)
	candidate.CostUtility = evaluation.costUtility
	candidate.CostKnown = evaluation.costKnown
	candidate.LoadUtility = balancedLoadUtilityValues(
		evaluation.state.capacityUtilization,
		evaluation.state.queueDelayMs,
		prepared.settings,
	)
	candidate.UtilityScore = evaluation.utilityScore
	candidate.AdjustedScore = candidate.UtilityScore * candidate.LoadUtility
	candidate.UtilityScoreUnits = evaluation.utilityScoreUnits
	candidate.AdjustedScoreUnits = balancedScoreUnits(candidate.AdjustedScore)
	return candidate, evaluation.state, ""
}

func (prepared *PreparedBalancedPool) drawAvailable(
	pool balancedWeightedPool,
	request BalancedRequest,
	stream uint64,
) (int, balancedEffectiveRuntimeState, bool) {
	if prepared == nil || len(pool.indexes) == 0 {
		return 0, balancedEffectiveRuntimeState{}, false
	}
	probeCount := min(len(pool.indexes), 8)
	for attempt := 0; attempt < probeCount; attempt++ {
		index := pool.sample(request.RandomSeed, stream+uint64(attempt))
		_, state, reason := prepared.runtimeCandidate(index, request)
		if reason == "" {
			return index, state, true
		}
	}
	start := int(balancedMix64(uint64(request.RandomSeed)^stream) % uint64(len(pool.indexes)))
	for offset := 0; offset < len(pool.indexes); offset++ {
		index := pool.indexes[(start+offset)%len(pool.indexes)]
		_, state, reason := prepared.runtimeCandidate(index, request)
		if reason == "" {
			return index, state, true
		}
	}
	return 0, balancedEffectiveRuntimeState{}, false
}

type balancedRuntimeEntry struct {
	index     int
	candidate BalancedRankedCandidate
}

func (prepared *PreparedBalancedPool) validateRequest(request BalancedRequest) error {
	if prepared == nil || len(prepared.ranked) > MaxBalancedCandidates || len(prepared.hot) != len(prepared.ranked) ||
		len(prepared.knownChannelIDs) > MaxBalancedCandidates || request.PreferredChannelID < 0 || request.NowUnixMilli < 0 ||
		(request.NowUnixMilli > 0 && request.NowUnixMilli < prepared.settings.NowUnixMilli) ||
		len(request.ExcludedChannelIDs) > MaxBalancedCandidates || len(request.RuntimeByChannelID) > MaxBalancedCandidates {
		return ErrBalancedCandidateInvalid
	}
	for channelID := range request.ExcludedChannelIDs {
		if _, exists := prepared.knownChannelIDs[channelID]; channelID <= 0 || !exists {
			return ErrBalancedCandidateInvalid
		}
	}
	for channelID, state := range request.RuntimeByChannelID {
		if _, exists := prepared.knownChannelIDs[channelID]; channelID <= 0 || !exists ||
			!balancedFiniteNonNegative(state.CapacityUtilization) || !balancedFiniteNonNegative(state.QueueDelayMs) ||
			state.Inflight < 0 || (state.CooldownUntilUnixMilli != nil && *state.CooldownUntilUnixMilli < 0) ||
			(state.SlowStartFactor != nil && !balancedUnitInterval(*state.SlowStartFactor, true)) ||
			!balancedRuntimeAdmissionValid(state.Admission) || !balancedBreakerSnapshotValid(state.Breaker) ||
			!balancedCostSnapshotValid(state.Cost) {
			return ErrBalancedCandidateInvalid
		}
	}
	return nil
}

func (prepared *PreparedBalancedPool) requiresDynamicReplan(request BalancedRequest) bool {
	nowUnixMilli := request.NowUnixMilli
	if nowUnixMilli == 0 {
		nowUnixMilli = prepared.settings.NowUnixMilli
	}
	if prepared.nextBreakerTransitionUnix > 0 && nowUnixMilli/1_000 >= prepared.nextBreakerTransitionUnix {
		return true
	}
	if prepared.nextCostExpiryUnix > 0 && nowUnixMilli/1_000 > prepared.nextCostExpiryUnix {
		return true
	}
	for _, state := range request.RuntimeByChannelID {
		if state.Admission != BalancedRuntimeAdmissionInherit || state.SlowStartFactor != nil ||
			state.Breaker != nil || state.Cost != nil {
			return true
		}
	}
	return false
}

func balancedCostSnapshotValid(cost *CostSnapshot) bool {
	return cost == nil || (cost.UpdatedUnix >= 0 && (!cost.Known || balancedFiniteNonNegative(cost.Cost)))
}

type balancedDynamicStage struct {
	found          bool
	softFallback   bool
	businessTier   int64
	bestScoreUnits uint64
}

type balancedWeightedChoice struct {
	found     bool
	index     int
	channelID int
}

type balancedWeightedSampler struct {
	maxWeight float64
	total     float64
	first     balancedWeightedChoice
	second    balancedWeightedChoice
}

func (prepared *PreparedBalancedPool) selectDynamic(request BalancedRequest) BalancedDecision {
	primary := balancedDynamicStage{}
	soft := balancedDynamicStage{softFallback: true}
	for index := range prepared.ranked {
		evaluation := balancedRuntimeEvaluation{}
		reason := prepared.runtimeEvaluation(index, &request, &evaluation, false)
		if reason != "" {
			continue
		}
		hot := &prepared.hot[index]
		if evaluation.softFallback {
			updateBalancedDynamicStage(
				&soft,
				hot.businessTier,
				evaluation.utilityScoreUnits,
				prepared.settings.EnforceBusinessTierCascade,
			)
		} else {
			updateBalancedDynamicStage(
				&primary,
				hot.businessTier,
				evaluation.utilityScoreUnits,
				prepared.settings.EnforceBusinessTierCascade,
			)
		}
	}
	stage := primary
	if !stage.found {
		stage = soft
	}
	if !stage.found {
		return BalancedDecision{}
	}

	decision := BalancedDecision{SoftFallback: stage.softFallback}
	thresholdUnits := balancedProtectionThreshold(stage.bestScoreUnits, prepared.settings.ProtectionBandBasisPoints)
	preferredChannelID := request.PreferredChannelID
	if preferredChannelID == 0 {
		preferredChannelID = prepared.settings.PreferredChannelID
	}
	if preferredIndex, exists := prepared.byChannelID[preferredChannelID]; preferredChannelID > 0 && exists {
		evaluation := balancedRuntimeEvaluation{}
		reason := prepared.runtimeEvaluation(preferredIndex, &request, &evaluation, false)
		hot := &prepared.hot[preferredIndex]
		if reason == "" && balancedCandidateMatchesStage(
			evaluation.softFallback,
			hot.businessTier,
			stage,
			prepared.settings.EnforceBusinessTierCascade,
		) && evaluation.utilityScoreUnits >= thresholdUnits && !evaluation.degraded && !evaluation.softFallback &&
			evaluation.state.capacityUtilization <= prepared.settings.AffinityMaxCapacityUtilization {
			selected, _, _ := prepared.runtimeCandidate(preferredIndex, request)
			selected.InProtectionBand = true
			balancedSetSelected(&decision, selected)
			decision.SampledChannelIDs = []int{selected.Channel.Id}
			decision.AffinityUsed = true
			return decision
		}
	}

	explorationActive := balancedExplorationBucket(request.RandomSeed) < prepared.settings.ExplorationBasisPoints
	minimumExplorationUnits := balancedScoreUnits(prepared.settings.MinimumExplorationScore)
	protectionSampler := balancedWeightedSampler{}
	explorationSampler := balancedWeightedSampler{}
	for index := range prepared.ranked {
		evaluation := balancedRuntimeEvaluation{}
		reason := prepared.runtimeEvaluation(index, &request, &evaluation, false)
		hot := &prepared.hot[index]
		if reason != "" || !balancedCandidateMatchesStage(
			evaluation.softFallback,
			hot.businessTier,
			stage,
			prepared.settings.EnforceBusinessTierCascade,
		) {
			continue
		}
		if evaluation.utilityScoreUnits >= thresholdUnits {
			protectionSampler.add(
				index,
				hot.channelID,
				hot.targetWeight,
				request.RandomSeed,
				0x5032432d46495253,
				0x5032432d5345434f,
				true,
			)
			continue
		}
		if explorationActive && hot.flags&balancedHotExplorationEligible != 0 &&
			evaluation.utilityScoreUnits >= minimumExplorationUnits {
			explorationSampler.add(
				index,
				hot.channelID,
				hot.targetWeight,
				request.RandomSeed,
				0x4558504c4f5245,
				0,
				false,
			)
		}
	}
	if explorationActive && explorationSampler.first.found {
		candidate, _, reason := prepared.runtimeCandidate(explorationSampler.first.index, request)
		if reason != "" {
			return decision
		}
		candidate.InProtectionBand = false
		balancedSetSelected(&decision, candidate)
		decision.SampledChannelIDs = []int{candidate.Channel.Id}
		decision.ExplorationUsed = true
		return decision
	}
	if !protectionSampler.first.found {
		return decision
	}
	first := protectionSampler.first
	firstCandidate, firstState, firstReason := prepared.runtimeCandidate(first.index, request)
	if firstReason != "" {
		return decision
	}
	firstCandidate.InProtectionBand = true
	decision.SampledChannelIDs = append(decision.SampledChannelIDs, firstCandidate.Channel.Id)
	if !protectionSampler.second.found {
		balancedSetSelected(&decision, firstCandidate)
		return decision
	}
	second := protectionSampler.second
	secondCandidate, secondState, secondReason := prepared.runtimeCandidate(second.index, request)
	if secondReason != "" {
		balancedSetSelected(&decision, firstCandidate)
		return decision
	}
	secondCandidate.InProtectionBand = true
	decision.SampledChannelIDs = append(decision.SampledChannelIDs, secondCandidate.Channel.Id)
	if second.index == first.index ||
		!balancedPreparedP2CBetter(secondCandidate, secondState, firstCandidate, firstState) {
		balancedSetSelected(&decision, firstCandidate)
		return decision
	}
	balancedSetSelected(&decision, secondCandidate)
	return decision
}

func updateBalancedDynamicStage(
	stage *balancedDynamicStage,
	businessTier int64,
	utilityScoreUnits uint64,
	enforceBusinessTierCascade bool,
) {
	tier := int64(0)
	if enforceBusinessTierCascade {
		tier = businessTier
	}
	if !stage.found || tier > stage.businessTier {
		stage.found = true
		stage.businessTier = tier
		stage.bestScoreUnits = utilityScoreUnits
		return
	}
	if tier == stage.businessTier && utilityScoreUnits > stage.bestScoreUnits {
		stage.bestScoreUnits = utilityScoreUnits
	}
}

func balancedCandidateMatchesStage(
	softFallback bool,
	businessTier int64,
	stage balancedDynamicStage,
	enforceBusinessTierCascade bool,
) bool {
	return softFallback == stage.softFallback &&
		(!enforceBusinessTierCascade || businessTier == stage.businessTier)
}

func (sampler *balancedWeightedSampler) add(
	index int,
	channelID int,
	weight float64,
	seed int64,
	firstStream uint64,
	secondStream uint64,
	includeSecond bool,
) {
	if weight <= 0 {
		weight = balancedDefaultTargetWeight
	}
	if sampler.maxWeight == 0 {
		sampler.maxWeight = weight
	} else if weight > sampler.maxWeight {
		sampler.total *= sampler.maxWeight / weight
		sampler.maxWeight = weight
	}
	normalizedWeight := weight / sampler.maxWeight
	if normalizedWeight <= 0 {
		return
	}
	sampler.total += normalizedWeight
	balancedWeightedChoiceAdd(
		&sampler.first,
		index,
		channelID,
		balancedWeightedUnit(seed, firstStream, channelID)*sampler.total < normalizedWeight,
	)
	if includeSecond {
		balancedWeightedChoiceAdd(
			&sampler.second,
			index,
			channelID,
			balancedWeightedUnit(seed, secondStream, channelID)*sampler.total < normalizedWeight,
		)
	}
}

func balancedWeightedChoiceAdd(
	choice *balancedWeightedChoice,
	index int,
	channelID int,
	replace bool,
) {
	if !choice.found || replace {
		choice.found = true
		choice.index = index
		choice.channelID = channelID
	}
}

func balancedWeightedUnit(seed int64, stream uint64, channelID int) float64 {
	mixed := balancedMix64(uint64(seed) ^ stream ^ balancedMix64(uint64(channelID)))
	return (float64(mixed>>11) + 0.5) * (1.0 / (1 << 53))
}

func (prepared *PreparedBalancedPool) buildStaticGroups() []preparedBalancedGroup {
	entries := make([]balancedRuntimeEntry, 0, len(prepared.ranked))
	for index := range prepared.ranked {
		candidate := prepared.ranked[index]
		if candidate.UtilityScoreUnits == 0 {
			continue
		}
		entries = append(entries, balancedRuntimeEntry{index: index, candidate: candidate})
	}
	groups := prepared.buildGroups(entries)
	for groupIndex := range groups {
		groups[groupIndex].resolved = false
		for _, index := range groups[groupIndex].protection.indexes {
			prepared.ranked[index].InProtectionBand = true
		}
	}
	return groups
}

func (prepared *PreparedBalancedPool) buildGroups(entries []balancedRuntimeEntry) []preparedBalancedGroup {
	primary := make([]balancedRuntimeEntry, 0, len(entries))
	soft := make([]balancedRuntimeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.candidate.SoftFallback {
			soft = append(soft, entry)
		} else {
			primary = append(primary, entry)
		}
	}
	groups := prepared.buildStageGroups(primary, false)
	if prepared.settings.AllowSoftFailureFallback {
		groups = append(groups, prepared.buildStageGroups(soft, true)...)
	}
	return groups
}

func (prepared *PreparedBalancedPool) buildStageGroups(
	entries []balancedRuntimeEntry,
	softFallback bool,
) []preparedBalancedGroup {
	if len(entries) == 0 {
		return nil
	}
	if !prepared.settings.EnforceBusinessTierCascade {
		sortBalancedRuntimeEntries(entries)
		return []preparedBalancedGroup{prepared.newResolvedGroup(entries, softFallback, 0)}
	}
	entriesByTier := make(map[int64][]balancedRuntimeEntry)
	for _, entry := range entries {
		tier := entry.candidate.Candidate.BusinessTier
		entriesByTier[tier] = append(entriesByTier[tier], entry)
	}
	tiers := make([]int64, 0, len(entriesByTier))
	for tier := range entriesByTier {
		tiers = append(tiers, tier)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i] > tiers[j] })
	groups := make([]preparedBalancedGroup, 0, len(tiers))
	for _, tier := range tiers {
		tierEntries := entriesByTier[tier]
		sortBalancedRuntimeEntries(tierEntries)
		groups = append(groups, prepared.newResolvedGroup(tierEntries, softFallback, tier))
	}
	return groups
}

func (prepared *PreparedBalancedPool) newResolvedGroup(
	entries []balancedRuntimeEntry,
	softFallback bool,
	businessTier int64,
) preparedBalancedGroup {
	group := preparedBalancedGroup{
		softFallback:  softFallback,
		businessTier:  businessTier,
		indexes:       make([]int, 0, len(entries)),
		protectionSet: make(map[int]struct{}),
		resolved:      true,
	}
	if len(entries) == 0 {
		return group
	}
	bestScoreUnits := entries[0].candidate.UtilityScoreUnits
	thresholdUnits := balancedProtectionThreshold(bestScoreUnits, prepared.settings.ProtectionBandBasisPoints)
	protectionIndexes := make([]int, 0, len(entries))
	explorationIndexes := make([]int, 0, len(entries))
	minimumExplorationUnits := balancedScoreUnits(prepared.settings.MinimumExplorationScore)
	for _, entry := range entries {
		group.indexes = append(group.indexes, entry.index)
		if entry.candidate.UtilityScoreUnits >= thresholdUnits {
			protectionIndexes = append(protectionIndexes, entry.index)
			group.protectionSet[entry.index] = struct{}{}
			continue
		}
		if entry.candidate.Candidate.ExplorationEligible && entry.candidate.UtilityScoreUnits >= minimumExplorationUnits {
			explorationIndexes = append(explorationIndexes, entry.index)
		}
	}
	group.protection = newBalancedWeightedPool(prepared.ranked, protectionIndexes)
	group.exploration = newBalancedWeightedPool(prepared.ranked, explorationIndexes)
	return group
}

func (prepared *PreparedBalancedPool) resolveGroup(
	group preparedBalancedGroup,
	request BalancedRequest,
) (preparedBalancedGroup, bool) {
	if len(group.indexes) == 0 {
		return preparedBalancedGroup{}, false
	}
	if group.resolved {
		return group, true
	}
	if _, _, reason := prepared.runtimeCandidate(group.indexes[0], request); reason == "" {
		group.resolved = true
		return group, true
	}
	entries := make([]balancedRuntimeEntry, 0, len(group.indexes))
	for _, index := range group.indexes {
		candidate, _, reason := prepared.runtimeCandidate(index, request)
		if reason == "" {
			entries = append(entries, balancedRuntimeEntry{index: index, candidate: candidate})
		}
	}
	if len(entries) == 0 {
		return preparedBalancedGroup{}, false
	}
	return prepared.newResolvedGroup(entries, group.softFallback, group.businessTier), true
}

func (prepared *PreparedBalancedPool) selectFromGroup(
	group preparedBalancedGroup,
	request BalancedRequest,
	decision *BalancedDecision,
) {
	preferredChannelID := request.PreferredChannelID
	if preferredChannelID == 0 {
		preferredChannelID = prepared.settings.PreferredChannelID
	}
	if preferredIndex, exists := prepared.byChannelID[preferredChannelID]; preferredChannelID > 0 && exists {
		_, inProtectionBand := group.protectionSet[preferredIndex]
		if !inProtectionBand {
			preferredIndex = -1
		}
		if preferredIndex >= 0 {
			candidate, state, reason := prepared.runtimeCandidate(preferredIndex, request)
			if reason == "" && !candidate.Degraded && !candidate.SoftFallback &&
				state.capacityUtilization <= prepared.settings.AffinityMaxCapacityUtilization {
				candidate.InProtectionBand = true
				balancedSetSelected(decision, candidate)
				decision.SampledChannelIDs = []int{candidate.Channel.Id}
				decision.AffinityUsed = true
				return
			}
		}
	}

	if balancedExplorationBucket(request.RandomSeed) < prepared.settings.ExplorationBasisPoints {
		if index, _, ok := prepared.drawAvailable(group.exploration, request, 0x4558504c4f5245); ok {
			candidate, _, reason := prepared.runtimeCandidate(index, request)
			if reason == "" {
				candidate.InProtectionBand = false
				balancedSetSelected(decision, candidate)
				decision.SampledChannelIDs = []int{candidate.Channel.Id}
				decision.ExplorationUsed = true
				return
			}
		}
	}

	firstIndex, firstState, ok := prepared.drawAvailable(group.protection, request, 0x5032432d46495253)
	if !ok {
		return
	}
	first, _, firstReason := prepared.runtimeCandidate(firstIndex, request)
	if firstReason != "" {
		return
	}
	first.InProtectionBand = true
	decision.SampledChannelIDs = append(decision.SampledChannelIDs, first.Channel.Id)
	secondIndex, secondState, secondOK := prepared.drawAvailable(group.protection, request, 0x5032432d5345434f)
	if !secondOK {
		balancedSetSelected(decision, first)
		return
	}
	second, _, secondReason := prepared.runtimeCandidate(secondIndex, request)
	if secondReason != "" {
		balancedSetSelected(decision, first)
		return
	}
	second.InProtectionBand = true
	decision.SampledChannelIDs = append(decision.SampledChannelIDs, second.Channel.Id)
	if secondIndex == firstIndex || !balancedPreparedP2CBetter(second, secondState, first, firstState) {
		balancedSetSelected(decision, first)
		return
	}
	balancedSetSelected(decision, second)
}

func (prepared *PreparedBalancedPool) Snapshot() BalancedDecision {
	if prepared == nil {
		return BalancedDecision{}
	}
	decision := BalancedDecision{
		Ranked:   make([]BalancedRankedCandidate, len(prepared.ranked)),
		Excluded: append([]BalancedExclusion(nil), prepared.excluded...),
	}
	for index := range prepared.ranked {
		decision.Ranked[index] = cloneBalancedRankedCandidate(prepared.ranked[index])
	}
	return decision
}

func (prepared *PreparedBalancedPool) decorateDecision(request BalancedRequest, decision *BalancedDecision) {
	if prepared == nil || decision == nil {
		return
	}
	decision.Excluded = append([]BalancedExclusion(nil), prepared.excluded...)
	selectedSoftFallback := decision.Selected != nil && decision.Selected.SoftFallback
	selectedTier := int64(0)
	if decision.Selected != nil {
		selectedTier = decision.Selected.Candidate.BusinessTier
	}
	for index := range prepared.ranked {
		candidate, _, reason := prepared.runtimeCandidate(index, request)
		if reason != "" {
			decision.Excluded = append(decision.Excluded, BalancedExclusion{ChannelID: prepared.ranked[index].Channel.Id, Reason: reason})
			continue
		}
		if decision.Selected != nil && candidate.SoftFallback != selectedSoftFallback {
			if candidate.SoftFallback {
				reason = prepared.softReasonByChannelID[candidate.Channel.Id]
				if reason == "" {
					reason = BalancedExclusionReliabilityOpen
				}
				decision.Excluded = append(decision.Excluded, BalancedExclusion{ChannelID: candidate.Channel.Id, Reason: reason})
			}
			continue
		}
		if decision.Selected != nil && prepared.settings.EnforceBusinessTierCascade &&
			candidate.Candidate.BusinessTier != selectedTier {
			decision.Excluded = append(decision.Excluded, BalancedExclusion{ChannelID: candidate.Channel.Id, Reason: BalancedExclusionBusinessTier})
			continue
		}
		decision.Ranked = append(decision.Ranked, cloneBalancedRankedCandidate(candidate))
	}
	sort.Slice(decision.Ranked, func(i, j int) bool {
		left := decision.Ranked[i]
		right := decision.Ranked[j]
		if left.UtilityScoreUnits != right.UtilityScoreUnits {
			return left.UtilityScoreUnits > right.UtilityScoreUnits
		}
		if left.AdjustedScoreUnits != right.AdjustedScoreUnits {
			return left.AdjustedScoreUnits > right.AdjustedScoreUnits
		}
		return left.Channel.Id < right.Channel.Id
	})
	if len(decision.Ranked) > 0 {
		thresholdUnits := balancedProtectionThreshold(
			decision.Ranked[0].UtilityScoreUnits,
			prepared.settings.ProtectionBandBasisPoints,
		)
		for index := range decision.Ranked {
			decision.Ranked[index].InProtectionBand = decision.Ranked[index].UtilityScoreUnits >= thresholdUnits
		}
	}
	sort.Slice(decision.Excluded, func(i, j int) bool {
		if decision.Excluded[i].ChannelID != decision.Excluded[j].ChannelID {
			return decision.Excluded[i].ChannelID < decision.Excluded[j].ChannelID
		}
		return decision.Excluded[i].Reason < decision.Excluded[j].Reason
	})
}

func sortBalancedRuntimeEntries(entries []balancedRuntimeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i].candidate
		right := entries[j].candidate
		if left.UtilityScoreUnits != right.UtilityScoreUnits {
			return left.UtilityScoreUnits > right.UtilityScoreUnits
		}
		if left.AdjustedScoreUnits != right.AdjustedScoreUnits {
			return left.AdjustedScoreUnits > right.AdjustedScoreUnits
		}
		return left.Channel.Id < right.Channel.Id
	})
}

func balancedSetSelected(decision *BalancedDecision, candidate BalancedRankedCandidate) {
	decision.SelectedChannelID = candidate.Channel.Id
}

func (prepared *PreparedBalancedPool) materializeSelected(request BalancedRequest, decision *BalancedDecision) {
	if prepared == nil || decision == nil || decision.SelectedChannelID <= 0 || decision.Selected != nil {
		return
	}
	index, exists := prepared.byChannelID[decision.SelectedChannelID]
	if !exists {
		return
	}
	candidate, _, reason := prepared.runtimeCandidate(index, request)
	if reason != "" {
		return
	}
	selected := cloneBalancedRankedCandidate(candidate)
	decision.Selected = &selected
}

func balancedProtectionThreshold(bestScoreUnits uint64, basisPoints int) uint64 {
	thresholdNumerator := bestScoreUnits * uint64(10_000-basisPoints)
	return (thresholdNumerator + 9_999) / 10_000
}

func balancedRuntimeAdmissionValid(admission BalancedRuntimeAdmission) bool {
	switch admission {
	case BalancedRuntimeAdmissionInherit, BalancedRuntimeAdmissionHealthy, BalancedRuntimeAdmissionDegraded,
		BalancedRuntimeAdmissionSoft, BalancedRuntimeAdmissionBlocked:
		return true
	default:
		return false
	}
}

func balancedBreakerSnapshotValid(breaker *BreakerSnapshot) bool {
	if breaker == nil {
		return true
	}
	if breaker.CooldownUntilUnix < 0 || breaker.HalfOpenInflight < 0 || breaker.UpdatedUnix < 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(breaker.State)) {
	case BreakerStateHealthy, BreakerStateDegraded, BreakerStateOpen, BreakerStateHalfOpen,
		BreakerReasonAuthFail, BreakerReasonBalance:
		return true
	default:
		return false
	}
}

func newBalancedWeightedPool(ranked []BalancedRankedCandidate, indexes []int) balancedWeightedPool {
	pool := balancedWeightedPool{indexes: append([]int(nil), indexes...)}
	if len(indexes) == 0 {
		return pool
	}
	maxWeight := 0.0
	for _, index := range indexes {
		weight := ranked[index].Candidate.TargetWeight
		if weight <= 0 {
			weight = balancedDefaultTargetWeight
		}
		if weight > maxWeight {
			maxWeight = weight
		}
	}
	pool.cumulative = make([]float64, len(indexes))
	for position, index := range indexes {
		weight := ranked[index].Candidate.TargetWeight
		if weight <= 0 {
			weight = balancedDefaultTargetWeight
		}
		pool.total += weight / maxWeight
		pool.cumulative[position] = pool.total
	}
	return pool
}

func (pool balancedWeightedPool) sample(seed int64, stream uint64) int {
	if len(pool.indexes) == 1 {
		return pool.indexes[0]
	}
	draw := float64(balancedMix64(uint64(seed)^stream)>>11) * (1.0 / (1 << 53)) * pool.total
	position := sort.Search(len(pool.cumulative), func(index int) bool {
		return pool.cumulative[index] >= draw
	})
	if position >= len(pool.indexes) {
		position = len(pool.indexes) - 1
	}
	return pool.indexes[position]
}

func cloneBalancedRankedCandidate(candidate BalancedRankedCandidate) BalancedRankedCandidate {
	cloned := candidate
	if candidate.Channel != nil {
		channel := *candidate.Channel
		if candidate.Channel.OpenAIOrganization != nil {
			value := *candidate.Channel.OpenAIOrganization
			channel.OpenAIOrganization = &value
		}
		if candidate.Channel.TestModel != nil {
			value := *candidate.Channel.TestModel
			channel.TestModel = &value
		}
		if candidate.Channel.Weight != nil {
			value := *candidate.Channel.Weight
			channel.Weight = &value
		}
		if candidate.Channel.BaseURL != nil {
			value := *candidate.Channel.BaseURL
			channel.BaseURL = &value
		}
		if candidate.Channel.ModelMapping != nil {
			value := *candidate.Channel.ModelMapping
			channel.ModelMapping = &value
		}
		if candidate.Channel.StatusCodeMapping != nil {
			value := *candidate.Channel.StatusCodeMapping
			channel.StatusCodeMapping = &value
		}
		if candidate.Channel.Priority != nil {
			value := *candidate.Channel.Priority
			channel.Priority = &value
		}
		if candidate.Channel.AutoBan != nil {
			value := *candidate.Channel.AutoBan
			channel.AutoBan = &value
		}
		if candidate.Channel.Tag != nil {
			value := *candidate.Channel.Tag
			channel.Tag = &value
		}
		if candidate.Channel.Setting != nil {
			value := *candidate.Channel.Setting
			channel.Setting = &value
		}
		if candidate.Channel.ParamOverride != nil {
			value := *candidate.Channel.ParamOverride
			channel.ParamOverride = &value
		}
		if candidate.Channel.HeaderOverride != nil {
			value := *candidate.Channel.HeaderOverride
			channel.HeaderOverride = &value
		}
		if candidate.Channel.Remark != nil {
			value := *candidate.Channel.Remark
			channel.Remark = &value
		}
		channel.Keys = append([]string(nil), candidate.Channel.Keys...)
		if candidate.Channel.ChannelInfo.MultiKeyStatusList != nil {
			channel.ChannelInfo.MultiKeyStatusList = make(map[int]int, len(candidate.Channel.ChannelInfo.MultiKeyStatusList))
			for key, value := range candidate.Channel.ChannelInfo.MultiKeyStatusList {
				channel.ChannelInfo.MultiKeyStatusList[key] = value
			}
		}
		if candidate.Channel.ChannelInfo.MultiKeyDisabledReason != nil {
			channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string, len(candidate.Channel.ChannelInfo.MultiKeyDisabledReason))
			for key, value := range candidate.Channel.ChannelInfo.MultiKeyDisabledReason {
				channel.ChannelInfo.MultiKeyDisabledReason[key] = value
			}
		}
		if candidate.Channel.ChannelInfo.MultiKeyDisabledTime != nil {
			channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64, len(candidate.Channel.ChannelInfo.MultiKeyDisabledTime))
			for key, value := range candidate.Channel.ChannelInfo.MultiKeyDisabledTime {
				channel.ChannelInfo.MultiKeyDisabledTime[key] = value
			}
		}
		cloned.Channel = &channel
		cloned.Candidate.Candidate.Channel = &channel
	}
	if candidate.Candidate.Candidate.Metric != nil {
		metric := *candidate.Candidate.Candidate.Metric
		cloned.Candidate.Candidate.Metric = &metric
	}
	if candidate.Candidate.Candidate.Cost != nil {
		cost := *candidate.Candidate.Candidate.Cost
		cloned.Candidate.Candidate.Cost = &cost
	}
	if candidate.Candidate.Candidate.Breaker != nil {
		breaker := *candidate.Candidate.Candidate.Breaker
		cloned.Candidate.Candidate.Breaker = &breaker
	}
	if candidate.Candidate.Candidate.Capacity != nil {
		capacity := *candidate.Candidate.Candidate.Capacity
		cloned.Candidate.Candidate.Capacity = &capacity
	}
	return cloned
}

func balancedPreparedP2CBetter(
	left BalancedRankedCandidate,
	leftState balancedEffectiveRuntimeState,
	right BalancedRankedCandidate,
	rightState balancedEffectiveRuntimeState,
) bool {
	if left.AdjustedScoreUnits != right.AdjustedScoreUnits {
		return left.AdjustedScoreUnits > right.AdjustedScoreUnits
	}
	if leftState.capacityUtilization != rightState.capacityUtilization {
		return leftState.capacityUtilization < rightState.capacityUtilization
	}
	if leftState.inflight != rightState.inflight {
		return leftState.inflight < rightState.inflight
	}
	if left.UtilityScoreUnits != right.UtilityScoreUnits {
		return left.UtilityScoreUnits > right.UtilityScoreUnits
	}
	return left.Channel.Id < right.Channel.Id
}

func validateBalancedSettings(settings BalancedSettings) error {
	weights := []float64{
		settings.Weights.Availability,
		settings.Weights.Latency,
		settings.Weights.Throughput,
		settings.Weights.Cost,
	}
	weightSum := 0.0
	for _, weight := range weights {
		if !balancedFinite(weight) || weight < 0 {
			return ErrBalancedPolicyInvalid
		}
		weightSum += weight
	}
	if !balancedFinite(weightSum) || weightSum <= 0 ||
		!balancedUnitInterval(settings.AvailabilityTarget, false) ||
		!balancedUnitInterval(settings.AvailabilityFloor, true) ||
		settings.AvailabilityFloor > settings.AvailabilityTarget ||
		settings.MinimumVolume < 0 || !balancedFinitePositive(settings.WilsonZ) ||
		settings.WilsonZ < balancedWilsonZMin || settings.WilsonZ > balancedWilsonZMax ||
		!balancedUnknownUtilityValid(settings.UnknownAvailability, settings.Weights.Availability) ||
		!balancedUnknownUtilityValid(settings.UnknownLatencyUtility, settings.Weights.Latency) ||
		!balancedUnknownUtilityValid(settings.UnknownThroughputUtility, settings.Weights.Throughput) ||
		!balancedUnknownUtilityValid(settings.UnknownCostUtility, settings.Weights.Cost) ||
		settings.ProtectionBandBasisPoints < 0 || settings.ProtectionBandBasisPoints > 10_000 ||
		settings.ExplorationBasisPoints < 0 || settings.ExplorationBasisPoints > balancedExplorationMaxBasisPoints ||
		(settings.ExplorationBasisPoints > 0 && settings.ExplorationBasisPoints < balancedExplorationMinBasisPoints) ||
		!balancedUnitInterval(settings.MinimumExplorationScore, true) ||
		!balancedUnitInterval(settings.MaxCapacityUtilization, false) ||
		!balancedUnitInterval(settings.AffinityMaxCapacityUtilization, true) ||
		settings.AffinityMaxCapacityUtilization > settings.MaxCapacityUtilization ||
		!balancedFiniteNonNegative(settings.QueueTargetMs) ||
		!balancedUnitInterval(settings.DegradedMultiplier, false) ||
		!balancedUnitInterval(settings.SoftFallbackMultiplier, false) ||
		settings.HalfOpenProbes < 1 || settings.SnapshotStaleSec < 1 ||
		settings.NowUnix <= 0 || settings.NowUnixMilli <= 0 || settings.PreferredChannelID < 0 ||
		!balancedFiniteNonNegative(settings.CostBudget) || (settings.CostBudget > 0 && !settings.RequireKnownCost) {
		return ErrBalancedPolicyInvalid
	}
	if settings.Weights.Latency > 0 && !balancedFinitePositive(settings.LatencyTargetMs) {
		return ErrBalancedPolicyInvalid
	}
	if settings.Weights.Throughput > 0 && !balancedFinitePositive(settings.ThroughputTarget) {
		return ErrBalancedPolicyInvalid
	}
	if settings.Weights.Cost > 0 && !balancedFinitePositive(settings.CostTarget) {
		return ErrBalancedPolicyInvalid
	}
	secondsFromMillis := settings.NowUnixMilli / 1_000
	clockDelta := secondsFromMillis - settings.NowUnix
	if clockDelta < 0 {
		clockDelta = -clockDelta
	}
	if clockDelta > 2 {
		return ErrBalancedPolicyInvalid
	}
	return nil
}

func validateBalancedCandidate(candidate BalancedCandidate) (int, error) {
	if candidate.Candidate.Channel == nil || candidate.Candidate.Channel.Id <= 0 ||
		!balancedFiniteNonNegative(candidate.TargetWeight) ||
		!balancedUnitInterval(candidate.Confidence, true) ||
		!balancedUnitInterval(candidate.Freshness, true) ||
		!balancedUnitInterval(candidate.SlowStartFactor, true) ||
		!balancedFiniteNonNegative(candidate.CapacityUtilization) ||
		!balancedFiniteNonNegative(candidate.QueueDelayMs) || candidate.MetricUpdatedUnix < 0 ||
		!utf8.ValidString(candidate.HardExclusionReason) || len(candidate.HardExclusionReason) > balancedExclusionReasonMaxBytes {
		return 0, ErrBalancedCandidateInvalid
	}
	if metric := candidate.Candidate.Metric; metric != nil {
		if metric.RequestCount < 0 || metric.SuccessCount < 0 || metric.SuccessCount > metric.RequestCount ||
			metric.ReliabilityRequestCount < 0 ||
			metric.ReliabilityFailureCount < 0 || metric.ReliabilityFailureCount > metric.ReliabilityRequestCount ||
			metric.Inflight < 0 || !balancedFiniteNonNegative(metric.P95LatencyMs) ||
			!balancedFiniteNonNegative(metric.P95TTFTMs) || !balancedFiniteNonNegative(metric.TPS) {
			return 0, ErrBalancedCandidateInvalid
		}
	}
	if cost := candidate.Candidate.Cost; cost != nil {
		if cost.UpdatedUnix < 0 || (cost.Known && !balancedFiniteNonNegative(cost.Cost)) {
			return 0, ErrBalancedCandidateInvalid
		}
	}
	if !balancedBreakerSnapshotValid(candidate.Candidate.Breaker) {
		return 0, ErrBalancedCandidateInvalid
	}
	if capacity := candidate.Candidate.Capacity; capacity != nil &&
		(capacity.CooldownUntilUnixMilli < 0 || capacity.UpdatedUnixMilli < 0) {
		return 0, ErrBalancedCandidateInvalid
	}
	return candidate.Candidate.Channel.Id, nil
}

func balancedBreakerDisposition(breaker *BreakerSnapshot, settings BalancedSettings) (string, bool, bool) {
	if breaker == nil {
		return "", false, false
	}
	state := strings.ToLower(strings.TrimSpace(breaker.State))
	reason := strings.ToLower(strings.TrimSpace(breaker.Reason))
	switch {
	case state == BreakerReasonAuthFail || reason == BreakerReasonAuthFail:
		return BalancedExclusionCredentialUnavailable, false, false
	case state == BreakerReasonBalance || reason == BreakerReasonBalance:
		return BalancedExclusionBalanceUnavailable, false, false
	}
	if breakerStale(breaker, Settings{NowUnix: settings.NowUnix, SnapshotStaleSec: settings.SnapshotStaleSec}) {
		switch state {
		case BreakerStateOpen, BreakerStateHalfOpen:
			return BalancedExclusionReliabilityOpen, false, true
		case BreakerStateDegraded:
			return "", true, false
		default:
			return "", false, false
		}
	}
	switch {
	case state == BreakerStateOpen &&
		(settings.NowUnix <= 0 || breaker.CooldownUntilUnix <= 0 || settings.NowUnix < breaker.CooldownUntilUnix):
		return BalancedExclusionReliabilityOpen, false, true
	case state == BreakerStateHalfOpen && breaker.HalfOpenInflight >= int64(settings.HalfOpenProbes):
		return BalancedExclusionReliabilityOpen, false, true
	case state == BreakerStateDegraded || state == BreakerStateHalfOpen ||
		(state == BreakerStateOpen && settings.NowUnix >= breaker.CooldownUntilUnix):
		return "", true, false
	default:
		return "", false, false
	}
}

func scoreBalancedCandidate(
	evaluated balancedEvaluatedCandidate,
	settings BalancedSettings,
	weights Weights,
) BalancedRankedCandidate {
	candidate := evaluated.candidate
	availability := balancedAvailability(candidate.Candidate.Metric, settings)
	availabilityUtility := balancedHigherIsBetterUtility(availability, settings.AvailabilityTarget)
	latencyUtility := 1.0
	if settings.Weights.Latency > 0 {
		latency := candidateLatencyMs(candidate.Candidate.Metric, Settings{PreferTTFT: settings.PreferTTFT})
		latencyUtility = settings.UnknownLatencyUtility
		if balancedFinitePositive(latency) {
			latencyUtility = balancedLowerIsBetterUtility(latency, settings.LatencyTargetMs)
		}
	}
	throughputUtility := 1.0
	if settings.Weights.Throughput > 0 {
		throughputUtility = settings.UnknownThroughputUtility
		if candidate.Candidate.Metric != nil && balancedFinitePositive(candidate.Candidate.Metric.TPS) {
			throughputUtility = balancedHigherIsBetterUtility(candidate.Candidate.Metric.TPS, settings.ThroughputTarget)
		}
	}
	costKnown := balancedCostKnown(candidate.Candidate.Cost, settings)
	costUtility := settings.UnknownCostUtility
	if costKnown {
		costUtility = balancedLowerIsBetterUtility(candidate.Candidate.Cost.Cost, settings.CostTarget)
	}

	geometric := balancedGeometricUtility(weights, availabilityUtility, latencyUtility, throughputUtility, costUtility)
	baseUtilityScore := clamp01(geometric * candidate.Confidence * candidate.Freshness)
	utilityScore := baseUtilityScore * candidate.SlowStartFactor
	if evaluated.degraded {
		utilityScore *= settings.DegradedMultiplier
	}
	if evaluated.softFallback {
		utilityScore *= settings.SoftFallbackMultiplier
	}
	utilityScore = clamp01(utilityScore)
	loadUtility := balancedLoadUtility(candidate, settings)
	adjustedScore := utilityScore * loadUtility
	return BalancedRankedCandidate{
		Candidate:             candidate,
		Channel:               candidate.Candidate.Channel,
		Availability:          availability,
		AvailabilityUtility:   availabilityUtility,
		LatencyUtility:        latencyUtility,
		ThroughputUtility:     throughputUtility,
		CostUtility:           costUtility,
		CostKnown:             costKnown,
		LoadUtility:           loadUtility,
		BaseUtilityScore:      baseUtilityScore,
		UtilityScore:          utilityScore,
		AdjustedScore:         adjustedScore,
		BaseUtilityScoreUnits: balancedScoreUnits(baseUtilityScore),
		UtilityScoreUnits:     balancedScoreUnits(utilityScore),
		AdjustedScoreUnits:    balancedScoreUnits(adjustedScore),
		Degraded:              evaluated.degraded,
		SoftFallback:          evaluated.softFallback,
	}
}

func balancedAvailability(metric *MetricSnapshot, settings BalancedSettings) float64 {
	if metric == nil || metric.ReliabilityRequestCount <= 0 {
		return settings.UnknownAvailability
	}
	n := float64(metric.ReliabilityRequestCount)
	failures := min(metric.ReliabilityFailureCount, metric.ReliabilityRequestCount)
	successes := float64(metric.ReliabilityRequestCount - failures)
	p := successes / n
	z2 := settings.WilsonZ * settings.WilsonZ
	center := p + z2/(2*n)
	margin := settings.WilsonZ * math.Sqrt((p*(1-p)+z2/(4*n))/n)
	return clamp01((center - margin) / (1 + z2/n))
}

func balancedHasMinimumVolume(metric *MetricSnapshot, minimum int) bool {
	return metric != nil && metric.ReliabilityRequestCount >= int64(max(minimum, 0))
}

func balancedCostKnown(cost *CostSnapshot, settings BalancedSettings) bool {
	return cost != nil && cost.UpdatedUnix > 0 && cost.UpdatedUnix <= settings.NowUnix &&
		costKnown(cost, Settings{NowUnix: settings.NowUnix, SnapshotStaleSec: settings.SnapshotStaleSec}) && cost.Cost >= 0
}

func balancedHigherIsBetterUtility(value float64, target float64) float64 {
	if target <= 0 {
		return 1
	}
	if !balancedFiniteNonNegative(value) {
		return 0
	}
	if value >= target {
		return 1
	}
	ratio := value / target
	return clamp01(ratio * ratio)
}

func balancedLowerIsBetterUtility(value float64, target float64) float64 {
	if target <= 0 {
		return 1
	}
	if !balancedFiniteNonNegative(value) {
		return 0
	}
	if value <= target {
		return 1
	}
	ratio := target / value
	return clamp01(ratio * ratio)
}

func balancedGeometricUtility(
	weights Weights,
	availability float64,
	latency float64,
	throughput float64,
	cost float64,
) float64 {
	utilities := []struct {
		weight  float64
		utility float64
	}{
		{weight: weights.Availability, utility: availability},
		{weight: weights.Latency, utility: latency},
		{weight: weights.Throughput, utility: throughput},
		{weight: weights.Cost, utility: cost},
	}
	logUtility := 0.0
	for _, item := range utilities {
		if item.weight <= 0 {
			continue
		}
		if item.utility <= 0 {
			return 0
		}
		logUtility += item.weight * math.Log(item.utility)
	}
	return clamp01(math.Exp(logUtility))
}

func balancedLoadUtility(candidate BalancedCandidate, settings BalancedSettings) float64 {
	return balancedLoadUtilityValues(candidate.CapacityUtilization, candidate.QueueDelayMs, settings)
}

func balancedLoadUtilityValues(
	capacityUtilization float64,
	queueDelayMs float64,
	settings BalancedSettings,
) float64 {
	capacityUtility := clamp01((settings.MaxCapacityUtilization - capacityUtilization) / settings.MaxCapacityUtilization)
	queueUtility := 1.0
	if settings.QueueTargetMs > 0 {
		queueUtility = balancedLowerIsBetterUtility(queueDelayMs, settings.QueueTargetMs)
	}
	return clamp01(capacityUtility * queueUtility)
}

func balancedMix64(value uint64) uint64 {
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func balancedExplorationBucket(seed int64) int {
	return int(uint64(seed) % 10_000)
}

func balancedUnitInterval(value float64, allowZero bool) bool {
	if !balancedFinite(value) || value > 1 {
		return false
	}
	if allowZero {
		return value >= 0
	}
	return value > 0
}

func balancedUnknownUtilityValid(value float64, weight float64) bool {
	if !balancedUnitInterval(value, true) {
		return false
	}
	return weight <= 0 || (value > 0 && value < 1)
}

func balancedScoreUnits(value float64) uint64 {
	value = clamp01(value)
	return uint64(math.Round(value * float64(balancedScoreScale)))
}

func balancedFinitePositive(value float64) bool {
	return value > 0 && balancedFinite(value)
}

func balancedFiniteNonNegative(value float64) bool {
	return value >= 0 && balancedFinite(value)
}

func balancedFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
