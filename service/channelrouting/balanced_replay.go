package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

const balancedReplaySchemaVersion = 1

var (
	ErrBalancedReplayInvalid = errors.New("invalid balanced routing replay")
	ErrBalancedReplayHash    = errors.New("balanced routing replay hash mismatch")
)

type BalancedReplayCandidate struct {
	PoolMemberID        int                  `json:"pool_member_id"`
	ChannelID           int                  `json:"channel_id"`
	CredentialID        int                  `json:"credential_id,omitempty"`
	BusinessTier        int64                `json:"business_tier"`
	TargetWeight        float64              `json:"target_weight"`
	Confidence          float64              `json:"confidence"`
	Freshness           float64              `json:"freshness"`
	SlowStartFactor     float64              `json:"slow_start_factor"`
	CapacityUtilization float64              `json:"capacity_utilization"`
	QueueDelayMs        float64              `json:"queue_delay_ms"`
	MetricUpdatedUnix   int64                `json:"metric_updated_unix"`
	ExplorationEligible bool                 `json:"exploration_eligible"`
	HardExclusionReason string               `json:"hard_exclusion_reason,omitempty"`
	Metric              *ShadowMetricInput   `json:"metric,omitempty"`
	Cost                *ShadowCostInput     `json:"cost,omitempty"`
	Breaker             *ShadowBreakerInput  `json:"breaker,omitempty"`
	Capacity            *ShadowCapacityInput `json:"capacity,omitempty"`
}

type BalancedReplayRuntimeState struct {
	ChannelID              int                 `json:"channel_id"`
	CapacityUtilization    float64             `json:"capacity_utilization"`
	QueueDelayMs           float64             `json:"queue_delay_ms"`
	Inflight               int64               `json:"inflight"`
	HasCapacityUtilization bool                `json:"has_capacity_utilization"`
	HasQueueDelay          bool                `json:"has_queue_delay"`
	HasInflight            bool                `json:"has_inflight"`
	CooldownUntilUnixMilli *int64              `json:"cooldown_until_unix_milli,omitempty"`
	SlowStartFactor        *float64            `json:"slow_start_factor,omitempty"`
	Breaker                *ShadowBreakerInput `json:"breaker,omitempty"`
	Cost                   *ShadowCostInput    `json:"cost,omitempty"`
	Admission              string              `json:"admission,omitempty"`
}

type BalancedReplaySettings struct {
	Policy              BalancedPoolPolicy `json:"policy"`
	PreparedAtUnix      int64              `json:"prepared_at_unix"`
	PreparedAtUnixMilli int64              `json:"prepared_at_unix_milli"`
	RequestNowUnixMilli int64              `json:"request_now_unix_milli"`
	RandomSeed          int64              `json:"random_seed"`
	PreferredChannelID  int                `json:"preferred_channel_id"`
	PreferTTFT          bool               `json:"prefer_ttft"`
}

type BalancedReplayInput struct {
	SchemaVersion      int                          `json:"schema_version"`
	AlgorithmVersion   string                       `json:"algorithm_version"`
	PoolID             int                          `json:"pool_id"`
	PolicyRevision     uint64                       `json:"policy_revision"`
	RuntimeGeneration  uint64                       `json:"runtime_generation"`
	PolicyHash         string                       `json:"policy_hash"`
	SnapshotHash       string                       `json:"snapshot_hash"`
	Profile            RequestProfile               `json:"profile"`
	Settings           BalancedReplaySettings       `json:"settings"`
	Candidates         []BalancedReplayCandidate    `json:"candidates"`
	RuntimeStates      []BalancedReplayRuntimeState `json:"runtime_states"`
	ExcludedChannelIDs []int                        `json:"excluded_channel_ids"`
}

type BalancedReplayResult struct {
	SelectedChannelID    int                 `json:"selected_channel_id"`
	SelectedMemberID     int                 `json:"selected_member_id"`
	SelectedCredentialID int                 `json:"selected_credential_id"`
	SelectedCost         float64             `json:"selected_cost"`
	SelectedCostKnown    bool                `json:"selected_cost_known"`
	SampledChannelIDs    []int               `json:"sampled_channel_ids"`
	AffinityUsed         bool                `json:"affinity_used"`
	ExplorationUsed      bool                `json:"exploration_used"`
	SoftFallback         bool                `json:"soft_fallback"`
	FilteredOpen         int                 `json:"filtered_open"`
	FilteredCapacity     int                 `json:"filtered_capacity"`
	Candidates           []DecisionCandidate `json:"candidates"`
}

func (input BalancedReplayInput) Validate() error {
	if err := input.validateWithoutHash(); err != nil {
		return err
	}
	if !validShadowHash(input.SnapshotHash) {
		return ErrBalancedReplayInvalid
	}
	hash, err := input.computeHash()
	if err != nil {
		return err
	}
	if hash != input.SnapshotHash {
		return ErrBalancedReplayHash
	}
	return nil
}

func RunBalancedReplay(input BalancedReplayInput) (BalancedReplayResult, error) {
	if err := input.Validate(); err != nil {
		return BalancedReplayResult{}, err
	}
	return runBalancedReplayValidated(input)
}

func runBalancedReplayValidated(input BalancedReplayInput) (BalancedReplayResult, error) {
	preparedAt := time.UnixMilli(input.Settings.PreparedAtUnixMilli)
	settings := input.Settings.Policy.settings(preparedAt, 1, 0, input.Settings.PreferTTFT)
	candidates := make([]routingselector.BalancedCandidate, len(input.Candidates))
	for index := range input.Candidates {
		candidates[index] = input.Candidates[index].routingCandidate()
	}
	prepared, err := routingselector.PrepareBalanced(candidates, settings)
	if err != nil {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	runtimeByChannelID := make(map[int]routingselector.BalancedRuntimeState, len(input.RuntimeStates))
	for index := range input.RuntimeStates {
		state := input.RuntimeStates[index]
		runtimeByChannelID[state.ChannelID] = state.routingState()
	}
	excluded := make(map[int]struct{}, len(input.ExcludedChannelIDs))
	for _, channelID := range input.ExcludedChannelIDs {
		excluded[channelID] = struct{}{}
	}
	decision, err := prepared.SelectDetailed(routingselector.BalancedRequest{
		RandomSeed:         input.Settings.RandomSeed,
		PreferredChannelID: input.Settings.PreferredChannelID,
		NowUnixMilli:       input.Settings.RequestNowUnixMilli,
		ExcludedChannelIDs: excluded,
		RuntimeByChannelID: runtimeByChannelID,
	})
	if err != nil {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	return balancedReplayResultFromDecision(input, decision)
}

func ReplayBalancedDecisionAudit(audit model.RoutingDecisionAudit) (BalancedReplayResult, error) {
	if !audit.Replayable || audit.AlgorithmVersion != DecisionAlgorithmBalancedV1 || audit.PoolID <= 0 ||
		audit.SnapshotRevision <= 0 || audit.RuntimeGeneration <= 0 || audit.ActivationID <= 0 ||
		audit.ActivationStage != model.RoutingDeploymentStageActive {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	replayJSON, err := model.LoadRoutingDecisionReplayInputContext(context.Background(), audit)
	if err != nil {
		if errors.Is(err, model.ErrRoutingDecisionReplayIntegrity) {
			return BalancedReplayResult{}, ErrBalancedReplayHash
		}
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	var input BalancedReplayInput
	if common.UnmarshalJsonStr(replayJSON, &input) != nil {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	if err := input.Validate(); err != nil {
		if errors.Is(err, ErrBalancedReplayHash) {
			return BalancedReplayResult{}, err
		}
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	expectedSeed, err := DeriveDecisionSeed(audit.RequestID, uint64(audit.SnapshotRevision), audit.RetryIndex)
	if err != nil || input.PoolID != audit.PoolID || input.PolicyRevision != uint64(audit.SnapshotRevision) ||
		input.RuntimeGeneration != uint64(audit.RuntimeGeneration) || input.PolicyHash != audit.PolicyHash ||
		input.SnapshotHash != audit.SnapshotHash || input.Settings.RandomSeed != expectedSeed ||
		input.Profile.GroupName != audit.GroupName || input.Profile.ModelName != audit.ModelName ||
		input.Profile.RetryIndex != audit.RetryIndex || input.Profile.IsStream != audit.IsStream {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	profileHash, err := input.Profile.Hash()
	if err != nil || profileHash != audit.ProfileHash {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	result, err := runBalancedReplayValidated(input)
	if err != nil {
		return BalancedReplayResult{}, err
	}
	differenceType := "active_unavailable"
	if result.SelectedChannelID > 0 {
		differenceType = "active_selected"
	}
	eligibleCount := 0
	for index := range result.Candidates {
		if result.Candidates[index].Eligible {
			eligibleCount++
		}
	}
	if result.SelectedChannelID != audit.ObservedChannelID || audit.ActualChannelID != audit.ObservedChannelID ||
		result.SelectedMemberID != audit.SelectedMemberID || result.SelectedCredentialID != audit.SelectedCredentialID ||
		result.SelectedCostKnown != audit.ObservedCostKnown || result.SelectedCost != audit.ObservedExpectedCost ||
		audit.ActualCostKnown != audit.ObservedCostKnown || audit.ActualExpectedCost != audit.ObservedExpectedCost ||
		audit.ExpectedCostDelta != 0 || audit.DifferenceType != differenceType ||
		audit.FilteredOpen != result.FilteredOpen || audit.FilteredCapacity != result.FilteredCapacity ||
		audit.CandidateCount != len(result.Candidates) || audit.EligibleCount != eligibleCount ||
		audit.ObservedMatchesActual != (result.SelectedChannelID > 0) {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	expectedSummary, err := marshalDecisionExclusionSummary(result.Candidates)
	if err != nil || string(expectedSummary) != audit.ExclusionSummaryJSON {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	var stored struct {
		Truncated  bool                `json:"truncated"`
		Candidates []DecisionCandidate `json:"candidates"`
	}
	expectedCandidates := result.Candidates
	expectedTruncated := len(expectedCandidates) > MaxDecisionCandidates
	if expectedTruncated {
		expectedCandidates = expectedCandidates[:MaxDecisionCandidates]
	}
	if common.UnmarshalJsonStr(audit.CandidatesJSON, &stored) != nil || stored.Truncated != expectedTruncated ||
		!reflect.DeepEqual(stored.Candidates, expectedCandidates) {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	return result, nil
}

func balancedReplayResultFromDecision(
	input BalancedReplayInput,
	decision routingselector.BalancedDecision,
) (BalancedReplayResult, error) {
	memberByChannel := make(map[int]int, len(input.Candidates))
	credentialByChannel := make(map[int]int, len(input.Candidates))
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		memberByChannel[candidate.ChannelID] = candidate.PoolMemberID
		credentialByChannel[candidate.ChannelID] = candidate.CredentialID
	}
	result := BalancedReplayResult{
		SelectedChannelID: decision.SelectedChannelID,
		SampledChannelIDs: append([]int(nil), decision.SampledChannelIDs...),
		AffinityUsed:      decision.AffinityUsed,
		ExplorationUsed:   decision.ExplorationUsed,
		SoftFallback:      decision.SoftFallback,
		Candidates:        make([]DecisionCandidate, 0, len(input.Candidates)),
	}
	seen := make(map[int]struct{}, len(input.Candidates))
	for index := range decision.Ranked {
		ranked := decision.Ranked[index]
		channelID := ranked.Channel.Id
		result.Candidates = append(result.Candidates, DecisionCandidate{
			PoolMemberID: memberByChannel[channelID], ChannelID: channelID, Eligible: true,
			Score: ranked.UtilityScore, Availability: ranked.Availability,
			Latency: ranked.LatencyUtility, Throughput: ranked.ThroughputUtility,
			CostScore: ranked.CostUtility, CostKnown: ranked.CostKnown,
			Degraded: ranked.Degraded, Inflight: balancedReplayInflight(ranked.Candidate.Candidate.Metric),
		})
		seen[channelID] = struct{}{}
	}
	for index := range decision.Excluded {
		exclusion := decision.Excluded[index]
		if _, exists := seen[exclusion.ChannelID]; exists {
			continue
		}
		result.Candidates = append(result.Candidates, DecisionCandidate{
			PoolMemberID: memberByChannel[exclusion.ChannelID], ChannelID: exclusion.ChannelID,
			ExclusionReason: exclusion.Reason,
		})
		seen[exclusion.ChannelID] = struct{}{}
		if balancedReplayOpenExclusion(exclusion.Reason) {
			result.FilteredOpen++
		}
		if exclusion.Reason == routingselector.BalancedExclusionCapacityExhausted {
			result.FilteredCapacity++
		}
	}
	if len(seen) != len(input.Candidates) {
		return BalancedReplayResult{}, ErrBalancedReplayInvalid
	}
	if decision.Selected != nil && decision.Selected.Channel != nil {
		channelID := decision.Selected.Channel.Id
		result.SelectedMemberID = memberByChannel[channelID]
		result.SelectedCredentialID = credentialByChannel[channelID]
		if decision.Selected.CostKnown && decision.Selected.Candidate.Candidate.Cost != nil {
			result.SelectedCost = decision.Selected.Candidate.Candidate.Cost.Cost
			result.SelectedCostKnown = true
		}
	}
	return result, nil
}

func buildBalancedReplayInput(
	poolID int,
	policyRevision uint64,
	runtimeGeneration uint64,
	policyHash string,
	profile RequestProfile,
	settings BalancedReplaySettings,
	candidates []BalancedReplayCandidate,
	runtimeStates []BalancedReplayRuntimeState,
	excludedChannelIDs []int,
) (BalancedReplayInput, error) {
	input := BalancedReplayInput{
		SchemaVersion: balancedReplaySchemaVersion, AlgorithmVersion: DecisionAlgorithmBalancedV1,
		PoolID: poolID, PolicyRevision: policyRevision, RuntimeGeneration: runtimeGeneration,
		PolicyHash: policyHash, Profile: profile, Settings: settings,
		Candidates:         append([]BalancedReplayCandidate(nil), candidates...),
		RuntimeStates:      append([]BalancedReplayRuntimeState(nil), runtimeStates...),
		ExcludedChannelIDs: append([]int(nil), excludedChannelIDs...),
	}
	sort.Slice(input.Candidates, func(i, j int) bool { return input.Candidates[i].ChannelID < input.Candidates[j].ChannelID })
	sort.Slice(input.RuntimeStates, func(i, j int) bool { return input.RuntimeStates[i].ChannelID < input.RuntimeStates[j].ChannelID })
	sort.Ints(input.ExcludedChannelIDs)
	if err := input.validateWithoutHash(); err != nil {
		return BalancedReplayInput{}, err
	}
	hash, err := input.computeHash()
	if err != nil {
		return BalancedReplayInput{}, err
	}
	input.SnapshotHash = hash
	return input, nil
}

func (input BalancedReplayInput) validateWithoutHash() error {
	if input.SchemaVersion != balancedReplaySchemaVersion || input.AlgorithmVersion != DecisionAlgorithmBalancedV1 ||
		input.PoolID <= 0 || input.PolicyRevision == 0 || input.RuntimeGeneration == 0 ||
		!validShadowHash(input.PolicyHash) || input.Profile.Validate() != nil ||
		len(input.Candidates) > routingselector.MaxBalancedCandidates ||
		len(input.RuntimeStates) > routingselector.MaxBalancedCandidates ||
		len(input.ExcludedChannelIDs) > routingselector.MaxBalancedCandidates ||
		input.Settings.PreparedAtUnix <= 0 || input.Settings.PreparedAtUnixMilli <= 0 ||
		input.Settings.PreparedAtUnixMilli/1_000 != input.Settings.PreparedAtUnix ||
		input.Settings.RequestNowUnixMilli < input.Settings.PreparedAtUnixMilli || input.Settings.PreferredChannelID < 0 {
		return ErrBalancedReplayInvalid
	}
	normalizedPolicy, err := normalizeBalancedPoolPolicy(input.Settings.Policy)
	if err != nil || !reflect.DeepEqual(normalizedPolicy, input.Settings.Policy) {
		return ErrBalancedReplayInvalid
	}
	seenChannels := make(map[int]struct{}, len(input.Candidates))
	seenMembers := make(map[int]struct{}, len(input.Candidates))
	previousChannelID := 0
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		if candidate.ChannelID <= previousChannelID || !validBalancedReplayCandidate(candidate) {
			return ErrBalancedReplayInvalid
		}
		previousChannelID = candidate.ChannelID
		if _, exists := seenChannels[candidate.ChannelID]; exists {
			return ErrBalancedReplayInvalid
		}
		if _, exists := seenMembers[candidate.PoolMemberID]; exists {
			return ErrBalancedReplayInvalid
		}
		seenChannels[candidate.ChannelID] = struct{}{}
		seenMembers[candidate.PoolMemberID] = struct{}{}
	}
	previousChannelID = 0
	for index := range input.RuntimeStates {
		state := input.RuntimeStates[index]
		if state.ChannelID <= previousChannelID || !validBalancedReplayRuntimeState(state) {
			return ErrBalancedReplayInvalid
		}
		previousChannelID = state.ChannelID
		if _, exists := seenChannels[state.ChannelID]; !exists {
			return ErrBalancedReplayInvalid
		}
	}
	previousChannelID = 0
	for _, channelID := range input.ExcludedChannelIDs {
		if channelID <= previousChannelID {
			return ErrBalancedReplayInvalid
		}
		previousChannelID = channelID
		if _, exists := seenChannels[channelID]; !exists {
			return ErrBalancedReplayInvalid
		}
	}
	return nil
}

func validBalancedReplayCandidate(candidate BalancedReplayCandidate) bool {
	if candidate.PoolMemberID <= 0 || candidate.ChannelID <= 0 || candidate.CredentialID < 0 ||
		!finiteShadowNumber(candidate.TargetWeight) || candidate.TargetWeight < 0 ||
		!balancedReplayUnitInterval(candidate.Confidence) || !balancedReplayUnitInterval(candidate.Freshness) ||
		!balancedReplayUnitInterval(candidate.SlowStartFactor) ||
		!finiteShadowNumber(candidate.CapacityUtilization) || candidate.CapacityUtilization < 0 ||
		!finiteShadowNumber(candidate.QueueDelayMs) || candidate.QueueDelayMs < 0 ||
		candidate.MetricUpdatedUnix < 0 || !validShadowText(candidate.HardExclusionReason, MaxDecisionReasonRunes) ||
		len(candidate.HardExclusionReason) > MaxDecisionReasonRunes ||
		!validBalancedReplayMetric(candidate.Metric) || !validBalancedReplayCost(candidate.Cost) ||
		!validBalancedReplayBreaker(candidate.Breaker) || !validBalancedReplayCapacity(candidate.Capacity) {
		return false
	}
	return true
}

func validBalancedReplayRuntimeState(state BalancedReplayRuntimeState) bool {
	if state.ChannelID <= 0 || !finiteShadowNumber(state.CapacityUtilization) || state.CapacityUtilization < 0 ||
		!finiteShadowNumber(state.QueueDelayMs) || state.QueueDelayMs < 0 || state.Inflight < 0 ||
		(state.CooldownUntilUnixMilli != nil && *state.CooldownUntilUnixMilli < 0) ||
		(state.SlowStartFactor != nil && !balancedReplayUnitInterval(*state.SlowStartFactor)) ||
		!validBalancedReplayBreaker(state.Breaker) || !validBalancedReplayCost(state.Cost) {
		return false
	}
	switch routingselector.BalancedRuntimeAdmission(state.Admission) {
	case routingselector.BalancedRuntimeAdmissionInherit,
		routingselector.BalancedRuntimeAdmissionHealthy,
		routingselector.BalancedRuntimeAdmissionDegraded,
		routingselector.BalancedRuntimeAdmissionSoft,
		routingselector.BalancedRuntimeAdmissionBlocked:
		return true
	default:
		return false
	}
}

func validBalancedReplayMetric(metric *ShadowMetricInput) bool {
	if metric == nil {
		return true
	}
	return metric.RequestCount >= 0 && metric.SuccessCount >= 0 && metric.SuccessCount <= metric.RequestCount &&
		metric.ReliabilityRequestCount >= 0 && metric.ReliabilityFailureCount >= 0 &&
		metric.ReliabilityFailureCount <= metric.ReliabilityRequestCount && metric.Inflight >= 0 &&
		finiteShadowNumber(metric.P95LatencyMs) && metric.P95LatencyMs >= 0 &&
		finiteShadowNumber(metric.P95TTFTMs) && metric.P95TTFTMs >= 0 &&
		finiteShadowNumber(metric.OutputTokensPerSecond) && metric.OutputTokensPerSecond >= 0
}

func validBalancedReplayCost(cost *ShadowCostInput) bool {
	return cost == nil || (finiteShadowNumber(cost.Cost) && cost.Cost >= 0 && cost.UpdatedUnix >= 0)
}

func validBalancedReplayBreaker(breaker *ShadowBreakerInput) bool {
	if breaker == nil {
		return true
	}
	if !validShadowText(breaker.State, 32) || !validShadowText(breaker.Reason, 64) ||
		breaker.CooldownUntilUnix < 0 || breaker.HalfOpenInflight < 0 || breaker.UpdatedUnix < 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(breaker.State)) {
	case routingselector.BreakerStateHealthy,
		routingselector.BreakerStateDegraded,
		routingselector.BreakerStateOpen,
		routingselector.BreakerStateHalfOpen,
		routingselector.BreakerReasonAuthFail,
		routingselector.BreakerReasonBalance:
		return true
	default:
		return false
	}
}

func validBalancedReplayCapacity(capacity *ShadowCapacityInput) bool {
	return capacity == nil || (capacity.SourceStatusCode >= 0 && capacity.CooldownUntilUnixMilli >= 0 &&
		capacity.UpdatedUnixMilli >= 0)
}

func balancedReplayUnitInterval(value float64) bool {
	return finiteShadowNumber(value) && value >= 0 && value <= 1
}

func (input BalancedReplayInput) computeHash() (string, error) {
	input.SnapshotHash = ""
	encoded, err := common.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (candidate BalancedReplayCandidate) routingCandidate() routingselector.BalancedCandidate {
	result := routingselector.BalancedCandidate{
		Candidate:    routingselector.Candidate{Channel: &model.Channel{Id: candidate.ChannelID}},
		BusinessTier: candidate.BusinessTier, TargetWeight: candidate.TargetWeight,
		Confidence: candidate.Confidence, Freshness: candidate.Freshness,
		SlowStartFactor: candidate.SlowStartFactor, CapacityUtilization: candidate.CapacityUtilization,
		QueueDelayMs: candidate.QueueDelayMs, MetricUpdatedUnix: candidate.MetricUpdatedUnix,
		ExplorationEligible: candidate.ExplorationEligible, HardExclusionReason: candidate.HardExclusionReason,
	}
	if candidate.Metric != nil {
		result.Candidate.Metric = &routingselector.MetricSnapshot{
			RequestCount: candidate.Metric.RequestCount, SuccessCount: candidate.Metric.SuccessCount,
			ReliabilityRequestCount: candidate.Metric.ReliabilityRequestCount,
			ReliabilityFailureCount: candidate.Metric.ReliabilityFailureCount,
			P95LatencyMs:            candidate.Metric.P95LatencyMs, P95TTFTMs: candidate.Metric.P95TTFTMs,
			TPS: candidate.Metric.OutputTokensPerSecond, Inflight: candidate.Metric.Inflight,
		}
	}
	if candidate.Cost != nil {
		result.Candidate.Cost = &routingselector.CostSnapshot{
			Known: candidate.Cost.Known, Cost: candidate.Cost.Cost, UpdatedUnix: candidate.Cost.UpdatedUnix,
		}
	}
	if candidate.Breaker != nil {
		result.Candidate.Breaker = &routingselector.BreakerSnapshot{
			State: candidate.Breaker.State, Reason: candidate.Breaker.Reason,
			CooldownUntilUnix: candidate.Breaker.CooldownUntilUnix,
			HalfOpenInflight:  candidate.Breaker.HalfOpenInflight, UpdatedUnix: candidate.Breaker.UpdatedUnix,
		}
	}
	if candidate.Capacity != nil {
		result.Candidate.Capacity = &routingselector.CapacityCooldownSnapshot{
			SourceStatusCode:       candidate.Capacity.SourceStatusCode,
			CooldownUntilUnixMilli: candidate.Capacity.CooldownUntilUnixMilli,
			UpdatedUnixMilli:       candidate.Capacity.UpdatedUnixMilli,
		}
	}
	return result
}

func (state BalancedReplayRuntimeState) routingState() routingselector.BalancedRuntimeState {
	result := routingselector.BalancedRuntimeState{
		CapacityUtilization: state.CapacityUtilization, QueueDelayMs: state.QueueDelayMs,
		Inflight: state.Inflight, HasCapacityUtilization: state.HasCapacityUtilization,
		HasQueueDelay: state.HasQueueDelay, HasInflight: state.HasInflight,
		CooldownUntilUnixMilli: state.CooldownUntilUnixMilli, SlowStartFactor: state.SlowStartFactor,
		Admission: routingselector.BalancedRuntimeAdmission(state.Admission),
	}
	if state.Breaker != nil {
		result.Breaker = &routingselector.BreakerSnapshot{
			State: state.Breaker.State, Reason: state.Breaker.Reason,
			CooldownUntilUnix: state.Breaker.CooldownUntilUnix,
			HalfOpenInflight:  state.Breaker.HalfOpenInflight, UpdatedUnix: state.Breaker.UpdatedUnix,
		}
	}
	if state.Cost != nil {
		result.Cost = &routingselector.CostSnapshot{
			Known: state.Cost.Known, Cost: state.Cost.Cost, UpdatedUnix: state.Cost.UpdatedUnix,
		}
	}
	return result
}

func balancedReplayCandidateFromRouting(
	member PoolMemberSnapshot,
	candidate routingselector.BalancedCandidate,
) BalancedReplayCandidate {
	result := BalancedReplayCandidate{
		PoolMemberID: member.ID, ChannelID: member.ChannelID,
		BusinessTier: candidate.BusinessTier, TargetWeight: candidate.TargetWeight,
		Confidence: candidate.Confidence, Freshness: candidate.Freshness,
		SlowStartFactor: candidate.SlowStartFactor, CapacityUtilization: candidate.CapacityUtilization,
		QueueDelayMs: candidate.QueueDelayMs, MetricUpdatedUnix: candidate.MetricUpdatedUnix,
		ExplorationEligible: candidate.ExplorationEligible, HardExclusionReason: candidate.HardExclusionReason,
	}
	if len(member.CredentialIDs) == 1 {
		result.CredentialID = member.CredentialIDs[0]
	}
	if metric := candidate.Candidate.Metric; metric != nil {
		result.Metric = &ShadowMetricInput{
			RequestCount: metric.RequestCount, SuccessCount: metric.SuccessCount,
			ReliabilityRequestCount: metric.ReliabilityRequestCount,
			ReliabilityFailureCount: metric.ReliabilityFailureCount,
			P95LatencyMs:            metric.P95LatencyMs, P95TTFTMs: metric.P95TTFTMs,
			OutputTokensPerSecond: metric.TPS, Inflight: metric.Inflight,
		}
	}
	if cost := candidate.Candidate.Cost; cost != nil {
		result.Cost = &ShadowCostInput{Known: cost.Known, Cost: cost.Cost, UpdatedUnix: cost.UpdatedUnix}
	}
	if breaker := candidate.Candidate.Breaker; breaker != nil {
		result.Breaker = &ShadowBreakerInput{
			State: breaker.State, Reason: breaker.Reason, CooldownUntilUnix: breaker.CooldownUntilUnix,
			HalfOpenInflight: breaker.HalfOpenInflight, UpdatedUnix: breaker.UpdatedUnix,
		}
	}
	if capacity := candidate.Candidate.Capacity; capacity != nil {
		result.Capacity = &ShadowCapacityInput{
			SourceStatusCode:       capacity.SourceStatusCode,
			CooldownUntilUnixMilli: capacity.CooldownUntilUnixMilli,
			UpdatedUnixMilli:       capacity.UpdatedUnixMilli,
		}
	}
	return result
}

func balancedReplayRuntimeFromRouting(channelID int, state routingselector.BalancedRuntimeState) BalancedReplayRuntimeState {
	result := BalancedReplayRuntimeState{
		ChannelID: channelID, CapacityUtilization: state.CapacityUtilization,
		QueueDelayMs: state.QueueDelayMs, Inflight: state.Inflight,
		HasCapacityUtilization: state.HasCapacityUtilization, HasQueueDelay: state.HasQueueDelay,
		HasInflight: state.HasInflight, CooldownUntilUnixMilli: state.CooldownUntilUnixMilli,
		SlowStartFactor: state.SlowStartFactor, Admission: string(state.Admission),
	}
	if state.Breaker != nil {
		result.Breaker = &ShadowBreakerInput{
			State: state.Breaker.State, Reason: state.Breaker.Reason,
			CooldownUntilUnix: state.Breaker.CooldownUntilUnix,
			HalfOpenInflight:  state.Breaker.HalfOpenInflight, UpdatedUnix: state.Breaker.UpdatedUnix,
		}
	}
	if state.Cost != nil {
		result.Cost = &ShadowCostInput{Known: state.Cost.Known, Cost: state.Cost.Cost, UpdatedUnix: state.Cost.UpdatedUnix}
	}
	return result
}

func balancedReplayInflight(metric *routingselector.MetricSnapshot) int64 {
	if metric == nil || metric.Inflight < 0 {
		return 0
	}
	return metric.Inflight
}

func balancedReplayOpenExclusion(reason string) bool {
	switch strings.TrimSpace(reason) {
	case routingselector.BalancedExclusionCredentialUnavailable,
		routingselector.BalancedExclusionBalanceUnavailable,
		routingselector.BalancedExclusionReliabilityOpen:
		return true
	default:
		return false
	}
}
