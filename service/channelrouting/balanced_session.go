package channelrouting

import (
	"math"

	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

const DecisionAlgorithmBalancedV1 = "channel-routing-balanced-v1"

type BalancedRoutingPlanInput struct {
	RequestRoutingPlanInput
	PreferredChannelID int
	RuntimeByChannelID map[int]routingselector.BalancedRuntimeState
}

type BalancedRoutingPlan struct {
	AlgorithmVersion  string                           `json:"algorithm_version"`
	PoolID            int                              `json:"pool_id"`
	PolicyRevision    uint64                           `json:"policy_revision"`
	RuntimeGeneration uint64                           `json:"runtime_generation"`
	PolicyHash        string                           `json:"policy_hash"`
	Profile           RequestProfile                   `json:"profile"`
	Policy            BalancedPoolPolicy               `json:"policy"`
	SelectedChannelID int                              `json:"selected_channel_id"`
	SelectedIdentity  Identity                         `json:"selected_identity"`
	SelectedCost      float64                          `json:"selected_cost"`
	SelectedCostKnown bool                             `json:"selected_cost_known"`
	SelectedBreaker   *routingselector.BreakerSnapshot `json:"selected_breaker,omitempty"`
	SampledChannelIDs []int                            `json:"sampled_channel_ids"`
	AffinityUsed      bool                             `json:"affinity_used"`
	ExplorationUsed   bool                             `json:"exploration_used"`
	SoftFallback      bool                             `json:"soft_fallback"`
}

func (session *RequestRoutingSession) PlanBalanced(input BalancedRoutingPlanInput) (BalancedRoutingPlan, bool, error) {
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
	profile, err := NewRequestProfile(
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
		state := runtimeByChannelID[member.ChannelID]
		cost, costErr := shadowExpectedCost(observation, profile)
		if costErr != nil {
			return BalancedRoutingPlan{}, true, costErr
		}
		state.Cost = &routingselector.CostSnapshot{UpdatedUnix: observation.CostUpdatedUnix}
		if cost != nil {
			state.Cost.Known = cost.Known
			state.Cost.Cost = cost.Cost
			state.Cost.UpdatedUnix = cost.UpdatedUnix
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
		identity := Identity{SnapshotRevision: snapshot.view.Revision, PoolID: pool.ID, MemberID: member.ID}
		if len(member.CredentialIDs) == 1 {
			identity.CredentialID = member.CredentialIDs[0]
		}
		identities[member.ChannelID] = identity
	}
	decision, err := prepared.Select(routingselector.BalancedRequest{
		RandomSeed:         seed,
		PreferredChannelID: input.PreferredChannelID,
		NowUnixMilli:       session.planningTime.UnixMilli(),
		ExcludedChannelIDs: requestExcluded,
		RuntimeByChannelID: runtimeByChannelID,
	})
	if err != nil {
		return BalancedRoutingPlan{}, true, err
	}
	plan := BalancedRoutingPlan{
		AlgorithmVersion:  DecisionAlgorithmBalancedV1,
		PoolID:            pool.ID,
		PolicyRevision:    snapshot.view.Revision,
		RuntimeGeneration: snapshot.view.RuntimeGeneration,
		PolicyHash:        snapshot.view.PolicyHash,
		Profile:           profile,
		Policy:            pool.BalancedPolicy,
		SelectedChannelID: decision.SelectedChannelID,
		SampledChannelIDs: append([]int(nil), decision.SampledChannelIDs...),
		AffinityUsed:      decision.AffinityUsed,
		ExplorationUsed:   decision.ExplorationUsed,
		SoftFallback:      decision.SoftFallback,
	}
	if decision.SelectedChannelID <= 0 {
		return plan, true, nil
	}
	identity, exists := identities[decision.SelectedChannelID]
	if !exists || identity.MemberID <= 0 {
		return BalancedRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	plan.SelectedIdentity = identity
	selectedState := runtimeByChannelID[decision.SelectedChannelID]
	if selectedState.Cost != nil && balancedRequestCostKnown(selectedState.Cost, pool.BalancedPolicy, session.planningTime.Unix()) {
		plan.SelectedCost = selectedState.Cost.Cost
		plan.SelectedCostKnown = true
	}
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

func balancedRequestCostKnown(cost *routingselector.CostSnapshot, policy BalancedPoolPolicy, nowUnix int64) bool {
	if cost == nil || !cost.Known || cost.UpdatedUnix <= 0 || cost.UpdatedUnix > nowUnix ||
		!finiteShadowNumber(cost.Cost) || cost.Cost < 0 {
		return false
	}
	return policy.SnapshotStaleSec > 0 && nowUnix-cost.UpdatedUnix <= int64(policy.SnapshotStaleSec)
}

func memberIndexesByChannelID(members []PoolMemberSnapshot, channelID int) int {
	for index := range members {
		if members[index].ChannelID == channelID {
			return index
		}
	}
	return -1
}
