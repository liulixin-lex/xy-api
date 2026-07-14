package channelrouting

import (
	"math"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

const (
	DecisionAlgorithmBalancedV1 = "channel-routing-balanced-v1"
	DecisionAlgorithmBalancedV2 = "channel-routing-balanced-v2"
)

type BalancedRoutingPlanInput struct {
	RequestRoutingPlanInput
	PreferredChannelID int
	RuntimeByChannelID map[int]routingselector.BalancedRuntimeState
}

type BalancedRoutingPlan struct {
	AlgorithmVersion           string                           `json:"algorithm_version"`
	PoolID                     int                              `json:"pool_id"`
	PolicyRevision             uint64                           `json:"policy_revision"`
	RuntimeGeneration          uint64                           `json:"runtime_generation"`
	ActivationID               int64                            `json:"activation_id"`
	PolicyHash                 string                           `json:"policy_hash"`
	Profile                    RequestProfile                   `json:"profile"`
	Policy                     BalancedPoolPolicy               `json:"policy"`
	SelectedChannelID          int                              `json:"selected_channel_id"`
	SelectedIdentity           Identity                         `json:"selected_identity"`
	SelectedCost               float64                          `json:"selected_cost"`
	SelectedCostKnown          bool                             `json:"selected_cost_known"`
	SelectedWorstCaseCost      float64                          `json:"selected_worst_case_cost,omitempty"`
	SelectedWorstCaseKnown     bool                             `json:"selected_worst_case_known,omitempty"`
	SelectedEffectiveCost      float64                          `json:"selected_effective_cost,omitempty"`
	SelectedEffectiveKnown     bool                             `json:"selected_effective_known,omitempty"`
	SelectedCostCurrency       string                           `json:"selected_cost_currency,omitempty"`
	SelectedCostUnit           string                           `json:"selected_cost_unit,omitempty"`
	SelectedCostPricingHash    string                           `json:"selected_cost_pricing_hash,omitempty"`
	SelectedCostPricingVersion string                           `json:"selected_cost_pricing_version,omitempty"`
	SelectedCostEstimate       *ShadowCostInput                 `json:"selected_cost_estimate,omitempty"`
	SelectedBreaker            *routingselector.BreakerSnapshot `json:"selected_breaker,omitempty"`
	SelectedBreakerScope       string                           `json:"selected_breaker_scope,omitempty"`
	SelectedEndpointAuthority  string                           `json:"selected_endpoint_authority,omitempty"`
	SelectedRegion             string                           `json:"selected_region,omitempty"`
	SampledChannelIDs          []int                            `json:"sampled_channel_ids"`
	AffinityUsed               bool                             `json:"affinity_used"`
	ExplorationUsed            bool                             `json:"exploration_used"`
	SoftFallback               bool                             `json:"soft_fallback"`
	FilteredOpen               int                              `json:"filtered_open"`
	FilteredCapacity           int                              `json:"filtered_capacity"`
	Candidates                 []DecisionCandidate              `json:"candidates"`
	Replay                     BalancedReplayInput              `json:"replay"`
}

func (session *RequestRoutingSession) PlanBalanced(input BalancedRoutingPlanInput) (BalancedRoutingPlan, bool, error) {
	if input.RequiredCredentialID < 0 {
		return BalancedRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	if session == nil || session.snapshot == nil || session.poolIndex < 0 ||
		session.poolIndex >= len(session.snapshot.view.Pools) {
		return BalancedRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	snapshot := session.snapshot
	pool := &snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName {
		return BalancedRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	if pool.DeploymentStage != model.RoutingDeploymentStageActive {
		return BalancedRoutingPlan{}, false, nil
	}
	if snapshot.view.ActivationStage != model.RoutingDeploymentStageActive || session.planningTime.IsZero() {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	if snapshot.view.BuiltAtUnix <= 0 || snapshot.view.ActivationID <= 0 {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	profile, err := resolveRequestProfile(
		input.Profile,
		input.RequestPath,
		session.groupName,
		input.ModelName,
		input.IsStream,
		input.RetryIndex,
		input.PromptTokenEstimate,
		input.CompletionTokenEstimate,
	)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	profile = attachRoutingCostProfile(profile, input.CostProfile, session.planningTime.Unix())
	seed, err := DeriveDecisionSeed(session.requestID, snapshot.view.Revision, input.RetryIndex)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	prepared := snapshot.preparedBalancedPools[balancedPoolModelKey{
		poolID: pool.ID, model: profile.ModelName, preferTTFT: profile.IsStream,
	}]
	if prepared == nil {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	allowedChannels, allowedRestricted, err := routingSessionChannelSet(input.AllowedChannelIDs)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	excludedChannels, _, err := routingSessionChannelSet(input.ExcludedChannelIDs)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	excludedCredentials, _, err := routingSessionChannelSet(input.ExcludedCredentialIDs)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	capacityExcludedChannels, _, err := routingSessionChannelSet(input.CapacityExcludedChannelIDs)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	probeExcludedChannels, _, err := routingSessionChannelSet(input.ProbeExcludedChannelIDs)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	runtimeByChannelID := make(map[int]routingselector.BalancedRuntimeState, len(input.RuntimeByChannelID)+len(pool.Members))
	for channelID, state := range input.RuntimeByChannelID {
		runtimeByChannelID[channelID] = cloneBalancedRuntimeState(state)
	}
	requestExcluded := make(map[int]struct{})
	identities := make(map[int]Identity, len(pool.Members))
	endpointBreakerByChannelID := make(map[int]struct {
		authority string
		region    string
	}, len(pool.Members))
	replayCandidates := make([]BalancedReplayCandidate, 0, len(pool.Members))
	costByChannelID := make(map[int]ShadowCostInput, len(pool.Members))
	preparedAt := time.Unix(snapshot.view.BuiltAtUnix, 0)
	preparedSettings := pool.BalancedPolicy.settings(preparedAt, 1, 0, profile.IsStream)
	preparedProfile := profile
	preparedProfile.PromptTokenEstimate = 0
	preparedProfile.CompletionTokenEstimate = 0
	memberIndexes := snapshot.memberIndexesByPoolModel[poolModelKey{poolID: pool.ID, model: profile.ModelName}]
	for _, memberIndex := range memberIndexes {
		if memberIndex < 0 || memberIndex >= len(pool.Members) {
			return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		member := pool.Members[memberIndex]
		observation, exists := snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: profile.ModelName}]
		if !exists {
			return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		channel, exists := snapshot.channelByID[member.ChannelID]
		if !exists {
			return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		preparedCandidate, candidateErr := balancedCandidateFromSnapshot(
			*pool, member, observation, channel, preparedProfile, preparedSettings,
		)
		if candidateErr != nil {
			return BalancedRoutingPlan{}, true, candidateErr
		}
		preparedCandidate.Candidate.Cost = &routingselector.CostSnapshot{
			Known: true, Cost: preparedSettings.CostTarget, UpdatedUnix: preparedSettings.NowUnix,
		}
		credentialID, credentialReason := snapshot.selectCredential(
			member, profile.ModelName, seed, excludedCredentials, input.RequiredCredentialID, session.planningTime,
		)
		if preparedCandidate.HardExclusionReason == "" && credentialReason != "" {
			preparedCandidate.HardExclusionReason = credentialReason
		}
		if preparedCandidate.HardExclusionReason == "" && observation.upstreamAccountID > 0 {
			if _, blocked := UpstreamAccountRuntimeBlocked(observation.upstreamAccountID, session.planningTime); blocked {
				preparedCandidate.HardExclusionReason = ExclusionReasonUpstreamAccount
			}
		}
		replayCandidate := balancedReplayCandidateFromRouting(member, preparedCandidate)
		replayCandidate.CredentialID = credentialID
		replayCandidates = append(replayCandidates, replayCandidate)
		if preparedCandidate.HardExclusionReason != "" {
			requestExcluded[member.ChannelID] = struct{}{}
		}
		state := runtimeByChannelID[member.ChannelID]
		endpointBreaker, authority, region := endpointBreakerForChannel(channel, session.planningTime, pool.BalancedPolicy.SnapshotStaleSec)
		baseBreaker := preparedCandidate.Candidate.Breaker
		if state.Breaker != nil {
			baseBreaker, _ = mergeRoutingBreaker(baseBreaker, state.Breaker)
		}
		var endpointSelected bool
		state.Breaker, endpointSelected = mergeRoutingBreaker(baseBreaker, endpointBreaker)
		if endpointSelected {
			endpointBreakerByChannelID[member.ChannelID] = struct {
				authority string
				region    string
			}{authority: authority, region: region}
		}
		cost, costErr := shadowExpectedCost(observation, profile)
		if costErr != nil {
			return BalancedRoutingPlan{}, true, costErr
		}
		state.Cost = &routingselector.CostSnapshot{UpdatedUnix: observation.CostUpdatedUnix}
		if cost != nil {
			state.Cost.Known = cost.Known
			state.Cost.Cost = cost.Cost
			state.Cost.UpdatedUnix = cost.UpdatedUnix
			costByChannelID[member.ChannelID] = *cost
		}
		if input.SlowStartFactor != nil {
			factor, factorErr := session.slowStartFactor(SlowStartKey{
				PoolID: pool.ID, MemberID: member.ID, Model: profile.ModelName,
			}, input.SlowStartFactor)
			if factorErr != nil || math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 0 || factor > 1 {
				return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
			}
			state.SlowStartFactor = &factor
		}
		runtimeByChannelID[member.ChannelID] = state
		if routingSessionChannelContains(excludedChannels, member.ChannelID) ||
			routingSessionChannelContains(capacityExcludedChannels, member.ChannelID) ||
			routingSessionChannelContains(probeExcludedChannels, member.ChannelID) ||
			(allowedRestricted && !routingSessionChannelContains(allowedChannels, member.ChannelID)) {
			requestExcluded[member.ChannelID] = struct{}{}
		}
		identity := Identity{
			SnapshotRevision:  snapshot.view.Revision,
			PoolID:            pool.ID,
			MemberID:          member.ID,
			CredentialID:      credentialID,
			UpstreamAccountID: observation.upstreamAccountID,
		}
		identities[member.ChannelID] = identity
	}
	balancedRequest := routingselector.BalancedRequest{
		RandomSeed:         seed,
		PreferredChannelID: input.PreferredChannelID,
		NowUnixMilli:       session.planningTime.UnixMilli(),
		ExcludedChannelIDs: requestExcluded,
		RuntimeByChannelID: runtimeByChannelID,
	}
	decision, err := prepared.SelectDetailed(balancedRequest)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	runtimeStates := make([]BalancedReplayRuntimeState, 0, len(runtimeByChannelID))
	for channelID, state := range runtimeByChannelID {
		runtimeStates = append(runtimeStates, balancedReplayRuntimeFromRouting(channelID, state))
	}
	excludedChannelIDs := make([]int, 0, len(requestExcluded))
	for channelID := range requestExcluded {
		excludedChannelIDs = append(excludedChannelIDs, channelID)
	}
	replay, err := buildBalancedReplayInput(
		pool.ID,
		snapshot.view.Revision,
		snapshot.view.RuntimeGeneration,
		snapshot.view.PolicyHash,
		profile,
		BalancedReplaySettings{
			Policy: pool.BalancedPolicy, PreparedAtUnix: preparedAt.Unix(),
			PreparedAtUnixMilli: preparedAt.UnixMilli(), RequestNowUnixMilli: session.planningTime.UnixMilli(),
			RandomSeed: seed, PreferredChannelID: input.PreferredChannelID, PreferTTFT: profile.IsStream,
		},
		replayCandidates,
		runtimeStates,
		excludedChannelIDs,
	)
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	replayResult, err := balancedReplayResultFromDecision(replay, decision)
	if err != nil || replayResult.SelectedChannelID != decision.SelectedChannelID {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	if !applyBalancedReplayHardExclusionReasons(&replayResult, replayCandidates) {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	plan := BalancedRoutingPlan{
		AlgorithmVersion:  balancedAlgorithmVersion(profile),
		PoolID:            pool.ID,
		PolicyRevision:    snapshot.view.Revision,
		RuntimeGeneration: snapshot.view.RuntimeGeneration,
		ActivationID:      snapshot.view.ActivationID,
		PolicyHash:        snapshot.view.PolicyHash,
		Profile:           profile,
		Policy:            pool.BalancedPolicy,
		SelectedChannelID: replayResult.SelectedChannelID,
		SelectedCost:      replayResult.SelectedCost,
		SelectedCostKnown: replayResult.SelectedCostKnown,
		SampledChannelIDs: append([]int(nil), replayResult.SampledChannelIDs...),
		AffinityUsed:      replayResult.AffinityUsed,
		ExplorationUsed:   replayResult.ExplorationUsed,
		SoftFallback:      replayResult.SoftFallback,
		FilteredOpen:      replayResult.FilteredOpen,
		FilteredCapacity:  replayResult.FilteredCapacity,
		Candidates:        append([]DecisionCandidate(nil), replayResult.Candidates...),
		Replay:            replay,
	}
	if selectedCost, exists := costByChannelID[decision.SelectedChannelID]; exists {
		selectedCostCopy := selectedCost
		plan.SelectedCostEstimate = &selectedCostCopy
		plan.SelectedWorstCaseCost = selectedCost.WorstCaseCost
		plan.SelectedWorstCaseKnown = selectedCost.WorstCaseKnown
		plan.SelectedEffectiveCost = selectedCost.EffectiveCost
		plan.SelectedEffectiveKnown = selectedCost.EffectiveKnown
		plan.SelectedCostCurrency = selectedCost.Currency
		plan.SelectedCostUnit = selectedCost.Unit
		plan.SelectedCostPricingHash = selectedCost.PricingHash
		plan.SelectedCostPricingVersion = selectedCost.PricingVersion
	}
	if decision.SelectedChannelID <= 0 {
		return plan, true, nil
	}
	identity, exists := identities[decision.SelectedChannelID]
	if !exists || identity.MemberID <= 0 {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	plan.SelectedIdentity = identity
	if endpoint, endpointSelected := endpointBreakerByChannelID[decision.SelectedChannelID]; endpointSelected {
		plan.SelectedBreakerScope = BreakerScopeEndpoint
		plan.SelectedEndpointAuthority = endpoint.authority
		plan.SelectedRegion = endpoint.region
	}
	selectedState := runtimeByChannelID[decision.SelectedChannelID]
	if selectedState.Breaker != nil {
		breaker := *selectedState.Breaker
		plan.SelectedBreaker = &breaker
	} else {
		memberIndex := memberIndexesByChannelID(pool.Members, decision.SelectedChannelID)
		if memberIndex < 0 {
			return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		member := pool.Members[memberIndex]
		observation := snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: profile.ModelName}]
		channel := snapshot.channelByID[member.ChannelID]
		candidate, candidateErr := balancedCandidateFromSnapshot(
			*pool, member, observation, channel, profile,
			pool.BalancedPolicy.settings(session.planningTime, seed, input.PreferredChannelID, input.IsStream),
		)
		if candidateErr != nil {
			return BalancedRoutingPlan{}, true, candidateErr
		}
		if candidate.Candidate.Breaker != nil {
			breaker := *candidate.Candidate.Breaker
			plan.SelectedBreaker = &breaker
		}
	}
	return plan, true, nil
}

func applyBalancedReplayHardExclusionReasons(
	result *BalancedReplayResult,
	candidates []BalancedReplayCandidate,
) bool {
	if result == nil {
		return false
	}
	reasonByChannel := make(map[int]string)
	for index := range candidates {
		candidate := candidates[index]
		if candidate.HardExclusionReason != "" {
			reasonByChannel[candidate.ChannelID] = candidate.HardExclusionReason
		}
	}
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		reason := reasonByChannel[candidate.ChannelID]
		if reason == "" {
			continue
		}
		if candidate.Eligible {
			return false
		}
		candidate.ExclusionReason = reason
	}
	return true
}

func cloneBalancedRuntimeState(state routingselector.BalancedRuntimeState) routingselector.BalancedRuntimeState {
	cloned := state
	if state.CooldownUntilUnixMilli != nil {
		value := *state.CooldownUntilUnixMilli
		cloned.CooldownUntilUnixMilli = &value
	}
	if state.SlowStartFactor != nil {
		value := *state.SlowStartFactor
		cloned.SlowStartFactor = &value
	}
	if state.Breaker != nil {
		value := *state.Breaker
		cloned.Breaker = &value
	}
	if state.Cost != nil {
		value := *state.Cost
		cloned.Cost = &value
	}
	return cloned
}

func memberIndexesByChannelID(members []PoolMemberSnapshot, channelID int) int {
	for index := range members {
		if members[index].ChannelID == channelID {
			return index
		}
	}
	return -1
}
