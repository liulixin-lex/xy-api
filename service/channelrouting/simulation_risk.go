package channelrouting

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

const (
	PolicySimulationStatusPass    = "pass"
	PolicySimulationStatusFail    = "fail"
	PolicySimulationStatusUnknown = "unknown"

	PolicySimulationRiskReady   = PolicySimulationStatusPass
	PolicySimulationRiskBlocked = PolicySimulationStatusFail
	PolicySimulationRiskUnknown = PolicySimulationStatusUnknown

	PolicySimulationEvidenceKnown   = "known"
	PolicySimulationEvidenceUnknown = "unknown"

	PolicySimulationCapacitySufficient   = PolicySimulationStatusPass
	PolicySimulationCapacityInsufficient = PolicySimulationStatusFail
	PolicySimulationCapacityUnknown      = PolicySimulationStatusUnknown

	PolicySimulationTrafficWithinLimit  = PolicySimulationStatusPass
	PolicySimulationTrafficExceedsLimit = PolicySimulationStatusFail
	PolicySimulationTrafficUnknown      = PolicySimulationStatusUnknown

	maxPolicySimulationImpactItems = 100
)

type PolicySimulationImpactScope struct {
	AffectedPoolCount    int      `json:"affected_pool_count"`
	AffectedPoolIDs      []int    `json:"affected_pool_ids"`
	UnsimulatedPoolCount int      `json:"unsimulated_pool_count"`
	UnsimulatedPoolIDs   []int    `json:"unsimulated_pool_ids"`
	AffectedChannelCount int      `json:"affected_channel_count"`
	AffectedChannelIDs   []int    `json:"affected_channel_ids"`
	AffectedModelCount   int      `json:"affected_model_count"`
	AffectedModels       []string `json:"affected_models"`
	ModelEvidenceState   string   `json:"model_evidence_state"`
	Truncated            bool     `json:"truncated"`
}

type PolicySimulationStructuralChanges struct {
	AddedPools              int  `json:"added_pools"`
	RemovedPools            int  `json:"removed_pools"`
	PolicyChanges           int  `json:"policy_changes"`
	DisplayNameChanges      int  `json:"display_name_changes"`
	GroupChanges            int  `json:"group_changes"`
	DeploymentStageChanges  int  `json:"deployment_stage_changes"`
	PolicyProfileChanges    int  `json:"policy_profile_changes"`
	PolicyConfigChanges     int  `json:"policy_config_changes"`
	AddedMembers            int  `json:"added_members"`
	RemovedMembers          int  `json:"removed_members"`
	ChangedMembers          int  `json:"changed_members"`
	MemberChannelChanges    int  `json:"member_channel_changes"`
	MemberEnablementChanges int  `json:"member_enablement_changes"`
	MemberPriorityChanges   int  `json:"member_priority_changes"`
	MemberWeightChanges     int  `json:"member_weight_changes"`
	MemberCredentialChanges int  `json:"member_credential_changes"`
	MemberOverrideChanges   int  `json:"member_override_changes"`
	TrafficAffecting        bool `json:"traffic_affecting"`
}

type PolicySimulationSLOImpact struct {
	State                   string   `json:"state"`
	KnownSamples            int      `json:"known_samples"`
	TotalSamples            int      `json:"total_samples"`
	AverageSuccessRateDelta *float64 `json:"average_success_rate_delta,omitempty"`
	AverageLatencyDeltaMs   *float64 `json:"average_latency_delta_ms,omitempty"`
	LatencyMetric           string   `json:"latency_metric,omitempty"`
	Assessment              string   `json:"assessment"`
}

type PolicySimulationCapacityAssessment struct {
	State                  string   `json:"state"`
	KnownSamples           int      `json:"known_samples"`
	TotalSamples           int      `json:"total_samples"`
	ExceededSamples        int      `json:"exceeded_samples"`
	MaxObservedUtilization *float64 `json:"max_observed_utilization,omitempty"`
	UtilizationLimit       *float64 `json:"utilization_limit,omitempty"`
}

type PolicySimulationTrafficRateAssessment struct {
	State                        string   `json:"state"`
	EstimatedSelectionChangeRate *float64 `json:"estimated_selection_change_rate,omitempty"`
	ConfiguredRateLimit          *float64 `json:"configured_rate_limit,omitempty"`
	Reason                       string   `json:"reason,omitempty"`
}

type PolicySimulationRiskAssessment struct {
	State    string                                `json:"state"`
	Reasons  []string                              `json:"reasons"`
	Scope    PolicySimulationImpactScope           `json:"scope"`
	Changes  PolicySimulationStructuralChanges     `json:"changes"`
	SLO      PolicySimulationSLOImpact             `json:"slo"`
	Capacity PolicySimulationCapacityAssessment    `json:"capacity"`
	Traffic  PolicySimulationTrafficRateAssessment `json:"traffic_change_rate"`
}

func assessPolicySimulationRisk(
	ctx context.Context,
	baseDocument model.RoutingPolicyDocument,
	draftDocument model.RoutingPolicyDocument,
	simulatedPoolID int,
	result HistoricalSimulationResult,
) PolicySimulationRiskAssessment {
	scope, changes := policySimulationImpactScope(ctx, baseDocument, draftDocument, simulatedPoolID)
	totalEvidenceSamples := result.ScannedSamples
	assessment := PolicySimulationRiskAssessment{
		State: PolicySimulationRiskUnknown, Reasons: []string{}, Scope: scope, Changes: changes,
		SLO: PolicySimulationSLOImpact{
			State: PolicySimulationStatusUnknown, KnownSamples: result.riskSLOKnownSamples,
			TotalSamples: totalEvidenceSamples, Assessment: "unknown",
		},
		Capacity: PolicySimulationCapacityAssessment{
			State: PolicySimulationCapacityUnknown, KnownSamples: result.riskCapacityKnownSamples,
			TotalSamples: totalEvidenceSamples, ExceededSamples: result.riskCapacityExceededSamples,
		},
		Traffic: PolicySimulationTrafficRateAssessment{
			State:                        PolicySimulationTrafficUnknown,
			EstimatedSelectionChangeRate: cloneSimulationFloat(result.SelectionChangeRate),
		},
	}
	if !changes.TrafficAffecting {
		assessment.State = PolicySimulationRiskReady
		assessment.SLO.State = PolicySimulationStatusPass
		assessment.SLO.Assessment = "stable"
		assessment.Capacity.State = PolicySimulationCapacitySufficient
		assessment.Traffic.State = PolicySimulationTrafficWithinLimit
		return assessment
	}

	if result.riskSLOKnownSamples > 0 && !result.riskLatencyMetricMixed {
		successDelta := result.riskSuccessRateDeltaTotal / float64(result.riskSLOKnownSamples)
		latencyDelta := result.riskLatencyDeltaTotal / float64(result.riskSLOKnownSamples)
		assessment.SLO.AverageSuccessRateDelta = &successDelta
		assessment.SLO.AverageLatencyDeltaMs = &latencyDelta
		assessment.SLO.LatencyMetric = result.riskLatencyMetric
		assessment.SLO.Assessment = classifyPolicySimulationSLO(successDelta, latencyDelta)
	}
	if totalEvidenceSamples > 0 && result.riskSLOKnownSamples == totalEvidenceSamples &&
		len(result.Skipped) == 0 && scope.UnsimulatedPoolCount == 0 && !result.riskLatencyMetricMixed {
		switch assessment.SLO.Assessment {
		case "stable", "improved":
			assessment.SLO.State = PolicySimulationStatusPass
		case "degraded":
			assessment.SLO.State = PolicySimulationStatusFail
		}
	}
	if result.riskCapacityKnownSamples > 0 {
		maxUtilization := result.riskMaxCapacityUtilization
		assessment.Capacity.MaxObservedUtilization = &maxUtilization
		if result.riskCapacityLimitKnown {
			limit := result.riskCapacityLimit
			assessment.Capacity.UtilizationLimit = &limit
		}
	}
	if result.riskCapacityExceededSamples > 0 {
		assessment.Capacity.State = PolicySimulationCapacityInsufficient
	} else if totalEvidenceSamples > 0 && result.riskCapacityKnownSamples == totalEvidenceSamples &&
		len(result.Skipped) == 0 && scope.UnsimulatedPoolCount == 0 {
		assessment.Capacity.State = PolicySimulationCapacitySufficient
	}

	blocked := false
	unknown := false
	if assessment.SLO.State == PolicySimulationStatusFail {
		assessment.Reasons = append(assessment.Reasons, "slo_degradation_detected")
		blocked = true
	} else if assessment.SLO.State == PolicySimulationStatusUnknown {
		if assessment.SLO.Assessment == "mixed" {
			assessment.Reasons = append(assessment.Reasons, "slo_tradeoff_requires_review")
		} else {
			assessment.Reasons = append(assessment.Reasons, "slo_evidence_incomplete")
		}
		unknown = true
	}
	if assessment.Capacity.State == PolicySimulationCapacityInsufficient {
		assessment.Reasons = append(assessment.Reasons, "capacity_insufficient")
		blocked = true
	} else if assessment.Capacity.State == PolicySimulationCapacityUnknown {
		assessment.Reasons = append(assessment.Reasons, "capacity_evidence_incomplete")
		unknown = true
	}
	assessment.Traffic.Reason = "traffic_change_rate_limit_unconfigured"
	assessment.Reasons = append(assessment.Reasons, assessment.Traffic.Reason)
	unknown = true
	if scope.ModelEvidenceState != PolicySimulationEvidenceKnown {
		assessment.Reasons = append(assessment.Reasons, "affected_model_scope_incomplete")
		unknown = true
	}
	if scope.UnsimulatedPoolCount > 0 {
		assessment.Reasons = append(assessment.Reasons, "changed_pools_not_simulated")
		unknown = true
	}
	if blocked {
		assessment.State = PolicySimulationRiskBlocked
	} else if unknown {
		assessment.State = PolicySimulationRiskUnknown
	} else {
		assessment.State = PolicySimulationRiskReady
	}
	return assessment
}

func policySimulationImpactScope(
	ctx context.Context,
	baseDocument model.RoutingPolicyDocument,
	draftDocument model.RoutingPolicyDocument,
	simulatedPoolID int,
) (PolicySimulationImpactScope, PolicySimulationStructuralChanges) {
	basePools := make(map[int]model.RoutingPolicyPoolContent, len(baseDocument.Pools))
	for index := range baseDocument.Pools {
		basePools[baseDocument.Pools[index].PoolID] = baseDocument.Pools[index]
	}
	draftPools := make(map[int]model.RoutingPolicyPoolContent, len(draftDocument.Pools))
	for index := range draftDocument.Pools {
		draftPools[draftDocument.Pools[index].PoolID] = draftDocument.Pools[index]
	}
	poolIDs := make(map[int]struct{}, len(basePools)+len(draftPools))
	for poolID := range basePools {
		poolIDs[poolID] = struct{}{}
	}
	for poolID := range draftPools {
		poolIDs[poolID] = struct{}{}
	}
	orderedPoolIDs := make([]int, 0, len(poolIDs))
	for poolID := range poolIDs {
		orderedPoolIDs = append(orderedPoolIDs, poolID)
	}
	sort.Ints(orderedPoolIDs)
	changedPools := make([]int, 0, len(orderedPoolIDs))
	channels := make(map[int]struct{})
	changes := PolicySimulationStructuralChanges{}
	for _, poolID := range orderedPoolIDs {
		basePool, baseExists := basePools[poolID]
		draftPool, draftExists := draftPools[poolID]
		changed, trafficAffecting := policySimulationPoolChanges(basePool, baseExists, draftPool, draftExists, &changes)
		if !changed {
			continue
		}
		changedPools = append(changedPools, poolID)
		changes.TrafficAffecting = changes.TrafficAffecting || trafficAffecting
		for _, pool := range []model.RoutingPolicyPoolContent{basePool, draftPool} {
			for memberIndex := range pool.Members {
				channels[pool.Members[memberIndex].ChannelID] = struct{}{}
			}
		}
	}
	channelIDs := make([]int, 0, len(channels))
	for channelID := range channels {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)
	models, modelState := policySimulationAffectedModels(ctx, channelIDs)
	unsimulated := make([]int, 0, len(changedPools))
	for _, poolID := range changedPools {
		if poolID != simulatedPoolID {
			unsimulated = append(unsimulated, poolID)
		}
	}
	scope := PolicySimulationImpactScope{
		AffectedPoolCount: len(changedPools), AffectedPoolIDs: truncateSimulationInts(changedPools),
		UnsimulatedPoolCount: len(unsimulated), UnsimulatedPoolIDs: truncateSimulationInts(unsimulated),
		AffectedChannelCount: len(channelIDs), AffectedChannelIDs: truncateSimulationInts(channelIDs),
		AffectedModelCount: len(models), AffectedModels: truncateSimulationStrings(models),
		ModelEvidenceState: modelState,
	}
	scope.Truncated = len(scope.AffectedPoolIDs) < scope.AffectedPoolCount ||
		len(scope.UnsimulatedPoolIDs) < scope.UnsimulatedPoolCount ||
		len(scope.AffectedChannelIDs) < scope.AffectedChannelCount ||
		len(scope.AffectedModels) < scope.AffectedModelCount || modelState != PolicySimulationEvidenceKnown
	return scope, changes
}

func policySimulationPoolChanges(
	base model.RoutingPolicyPoolContent,
	baseExists bool,
	draft model.RoutingPolicyPoolContent,
	draftExists bool,
	changes *PolicySimulationStructuralChanges,
) (bool, bool) {
	if !baseExists {
		changes.AddedPools++
		changes.AddedMembers += len(draft.Members)
		return true, true
	}
	if !draftExists {
		changes.RemovedPools++
		changes.RemovedMembers += len(base.Members)
		return true, true
	}
	changed := false
	trafficAffecting := false
	if base.DisplayName != draft.DisplayName {
		changes.DisplayNameChanges++
		changed = true
	}
	policyChanged := false
	if base.GroupName != draft.GroupName {
		changes.GroupChanges++
		policyChanged = true
	}
	if base.DeploymentStage != draft.DeploymentStage {
		changes.DeploymentStageChanges++
		policyChanged = true
	}
	if base.PolicyProfile != draft.PolicyProfile {
		changes.PolicyProfileChanges++
		policyChanged = true
	}
	if !bytes.Equal(base.Policy, draft.Policy) {
		changes.PolicyConfigChanges++
		policyChanged = true
	}
	if policyChanged {
		changes.PolicyChanges++
		changed = true
		trafficAffecting = true
	}
	baseMembers := make(map[int]model.RoutingPolicyMemberContent, len(base.Members))
	for index := range base.Members {
		baseMembers[base.Members[index].MemberID] = base.Members[index]
	}
	draftMembers := make(map[int]model.RoutingPolicyMemberContent, len(draft.Members))
	for index := range draft.Members {
		draftMembers[draft.Members[index].MemberID] = draft.Members[index]
	}
	for memberID, baseMember := range baseMembers {
		draftMember, exists := draftMembers[memberID]
		if !exists {
			changes.RemovedMembers++
			changed = true
			trafficAffecting = true
			continue
		}
		if recordPolicySimulationMemberChanges(baseMember, draftMember, changes) {
			changes.ChangedMembers++
			changed = true
			trafficAffecting = true
		}
	}
	for memberID := range draftMembers {
		if _, exists := baseMembers[memberID]; !exists {
			changes.AddedMembers++
			changed = true
			trafficAffecting = true
		}
	}
	return changed, trafficAffecting
}

func recordPolicySimulationMemberChanges(
	left model.RoutingPolicyMemberContent,
	right model.RoutingPolicyMemberContent,
	changes *PolicySimulationStructuralChanges,
) bool {
	changed := false
	if left.ChannelID != right.ChannelID {
		changes.MemberChannelChanges++
		changed = true
	}
	if left.Enabled != right.Enabled {
		changes.MemberEnablementChanges++
		changed = true
	}
	if left.Priority != right.Priority {
		changes.MemberPriorityChanges++
		changed = true
	}
	if left.Weight != right.Weight {
		changes.MemberWeightChanges++
		changed = true
	}
	if !bytes.Equal(left.Overrides, right.Overrides) {
		changes.MemberOverrideChanges++
		changed = true
	}
	credentialsChanged := len(left.CredentialIDs) != len(right.CredentialIDs)
	if !credentialsChanged {
		for index := range left.CredentialIDs {
			if left.CredentialIDs[index] != right.CredentialIDs[index] {
				credentialsChanged = true
				break
			}
		}
	}
	if credentialsChanged {
		changes.MemberCredentialChanges++
		changed = true
	}
	return changed
}

func policySimulationAffectedModels(ctx context.Context, channelIDs []int) ([]string, string) {
	if len(channelIDs) == 0 {
		return []string{}, PolicySimulationEvidenceKnown
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if model.DB == nil || !model.DB.Migrator().HasTable(&model.Channel{}) {
		return []string{}, PolicySimulationEvidenceUnknown
	}
	models := make(map[string]struct{})
	foundChannels := make(map[int]struct{}, len(channelIDs))
	complete := true
	for start := 0; start < len(channelIDs); start += snapshotMetricRollupPageSize {
		end := min(start+snapshotMetricRollupPageSize, len(channelIDs))
		var channels []model.Channel
		if err := model.DB.WithContext(ctx).
			Select("id", "models").
			Where("id IN ?", channelIDs[start:end]).
			Find(&channels).Error; err != nil {
			return []string{}, PolicySimulationEvidenceUnknown
		}
		for index := range channels {
			channel := channels[index]
			foundChannels[channel.Id] = struct{}{}
			if channel.Models == "" || len(channel.Models) > DefaultSnapshotLimits.MaxModelBytesPerChannel ||
				strings.Count(channel.Models, ",") >= DefaultSnapshotLimits.MaxModelsPerChannel {
				complete = false
				continue
			}
			for _, modelName := range channel.GetModels() {
				modelName = strings.TrimSpace(strings.TrimSuffix(modelName, ratio_setting.CompactModelSuffix))
				if modelName == "" {
					complete = false
					continue
				}
				if len(models) >= DefaultSnapshotLimits.MaxTotalModelSnapshots {
					if _, exists := models[modelName]; !exists {
						complete = false
					}
					continue
				}
				models[modelName] = struct{}{}
			}
		}
	}
	if len(foundChannels) != len(channelIDs) {
		complete = false
	}
	ordered := make([]string, 0, len(models))
	for modelName := range models {
		ordered = append(ordered, modelName)
	}
	sort.Strings(ordered)
	if !complete {
		return ordered, PolicySimulationEvidenceUnknown
	}
	return ordered, PolicySimulationEvidenceKnown
}

func classifyPolicySimulationSLO(successDelta float64, latencyDelta float64) string {
	const epsilon = 1e-9
	worseSuccess := successDelta < -epsilon
	worseLatency := latencyDelta > epsilon
	betterSuccess := successDelta > epsilon
	betterLatency := latencyDelta < -epsilon
	if (worseSuccess || worseLatency) && (betterSuccess || betterLatency) {
		return "mixed"
	}
	if worseSuccess || worseLatency {
		return "degraded"
	}
	if betterSuccess || betterLatency {
		return "improved"
	}
	return "stable"
}

func (result *HistoricalSimulationResult) addRiskEvidence(
	baseline historicalSimulationDecision,
	simulated historicalSimulationDecision,
) {
	if result == nil {
		return
	}
	if baseline.successRateKnown && simulated.successRateKnown && baseline.latencyKnown && simulated.latencyKnown &&
		baseline.latencyMetric != "" && baseline.latencyMetric == simulated.latencyMetric {
		result.riskSLOKnownSamples++
		result.riskSuccessRateDeltaTotal += simulated.successRate - baseline.successRate
		result.riskLatencyDeltaTotal += simulated.latencyMs - baseline.latencyMs
		if result.riskLatencyMetric == "" {
			result.riskLatencyMetric = baseline.latencyMetric
		} else if result.riskLatencyMetric != baseline.latencyMetric {
			result.riskLatencyMetricMixed = true
		}
	}
	if simulated.capacityKnown && result.riskCapacityLimitKnown {
		result.riskCapacityKnownSamples++
		if result.riskCapacityKnownSamples == 1 || simulated.capacityUtilization > result.riskMaxCapacityUtilization {
			result.riskMaxCapacityUtilization = simulated.capacityUtilization
		}
		if simulated.capacityUtilization >= result.riskCapacityLimit {
			result.riskCapacityExceededSamples++
		}
	}
}

func attachShadowSimulationEvidence(
	decision historicalSimulationDecision,
	input ShadowReplayInput,
) historicalSimulationDecision {
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		if candidate.ChannelID == decision.channelID {
			return attachSimulationMetricEvidence(decision, candidate.Metric, input.Settings.PreferTTFT)
		}
	}
	return decision
}

func attachBalancedSimulationEvidence(
	decision historicalSimulationDecision,
	input BalancedReplayInput,
) historicalSimulationDecision {
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		if candidate.ChannelID != decision.channelID {
			continue
		}
		decision = attachSimulationMetricEvidence(decision, candidate.Metric, input.Settings.PreferTTFT)
		if candidate.CapacityUtilization > 0 {
			decision.capacityUtilization = candidate.CapacityUtilization
			decision.capacityKnown = true
		}
		break
	}
	for index := range input.RuntimeStates {
		state := input.RuntimeStates[index]
		if state.ChannelID == decision.channelID && state.HasCapacityUtilization {
			decision.capacityUtilization = state.CapacityUtilization
			decision.capacityKnown = true
			break
		}
	}
	return decision
}

func attachSimulationMetricEvidence(
	decision historicalSimulationDecision,
	metric *ShadowMetricInput,
	preferTTFT bool,
) historicalSimulationDecision {
	if metric == nil {
		return decision
	}
	if metric.ReliabilityRequestCount > 0 {
		failures := min(metric.ReliabilityFailureCount, metric.ReliabilityRequestCount)
		decision.successRate = 1 - float64(failures)/float64(metric.ReliabilityRequestCount)
		decision.successRateKnown = true
	}
	if preferTTFT {
		if metric.P95TTFTMs > 0 {
			decision.latencyMs = metric.P95TTFTMs
			decision.latencyKnown = true
			decision.latencyMetric = "p95_ttft_ms"
		}
		return decision
	}
	if metric.P95LatencyMs > 0 {
		decision.latencyMs = metric.P95LatencyMs
		decision.latencyKnown = true
		decision.latencyMetric = "p95_latency_ms"
	}
	return decision
}

func truncateSimulationInts(values []int) []int {
	limit := min(len(values), maxPolicySimulationImpactItems)
	return append([]int(nil), values[:limit]...)
}

func truncateSimulationStrings(values []string) []string {
	limit := min(len(values), maxPolicySimulationImpactItems)
	return append([]string(nil), values[:limit]...)
}

func cloneSimulationFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
