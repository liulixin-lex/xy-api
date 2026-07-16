package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	globalsetting "github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

func cacheGetChannelRoutingCanary(
	param *RetryParam,
	setting smart_routing_setting.SmartRoutingSetting,
) (*model.Channel, string, bool, error) {
	if param == nil || param.Ctx == nil ||
		!smart_routing_setting.ResolveEffectiveMode(setting).AllowsEnterpriseFeatures() ||
		!common.MemoryCacheEnabled {
		return nil, "", false, nil
	}
	if param.TokenGroup != "auto" {
		channel, active, inCanary, err := selectChannelRoutingCanaryForGroup(param, param.TokenGroup)
		if err != nil {
			return nil, param.TokenGroup, true, err
		}
		if !active {
			return nil, param.TokenGroup, false, nil
		}
		if inCanary {
			return channel, param.TokenGroup, true, nil
		}
		channel, group, legacyErr := cacheGetRandomSatisfiedChannelLegacy(param)
		if outcomeErr := enqueueChannelRoutingControlDecision(param, group, channel); outcomeErr != nil {
			return nil, group, true, outcomeErr
		}
		return channel, group, true, legacyErr
	}

	if len(globalsetting.GetAutoGroups()) == 0 {
		return nil, param.TokenGroup, true, errors.New("auto groups is not enabled")
	}
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	autoGroups := GetUserAutoGroup(userGroup)
	if len(autoGroups) == 0 {
		return nil, param.TokenGroup, true, errors.New("auto groups is not enabled")
	}

	hasCanaryPool := false
	for _, group := range autoGroups {
		_, active, err := channelRoutingCanaryGate(param.Ctx, group)
		if err != nil {
			return nil, group, true, err
		}
		if active {
			hasCanaryPool = true
			break
		}
	}
	if !hasCanaryPool {
		return nil, param.TokenGroup, false, nil
	}

	startGroupIndex := 0
	if stored, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
		if index, ok := stored.(int); ok && index >= 0 && index < len(autoGroups) {
			startGroupIndex = index
		}
	}
	crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)
	for index := startGroupIndex; index < len(autoGroups); index++ {
		group := autoGroups[index]
		priorityRetry := autoGroupPriorityRetry(param.Ctx, param.GetRetry())
		if index > startGroupIndex {
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
			priorityRetry = 0
		}

		channel, active, inCanary, err := selectChannelRoutingCanaryForGroup(param, group)
		if err != nil {
			return nil, group, true, err
		}
		if !active {
			var balancedActive bool
			channel, balancedActive, err = selectChannelRoutingBalancedForGroup(param, group)
			if err != nil {
				return nil, group, true, err
			}
			if !balancedActive {
				if shouldActivateSmartRouting(setting) {
					channel, err = selectSmartChannelForGroup(param, group, setting, true)
					if err != nil {
						return nil, group, true, err
					}
				} else {
					channel, _ = getRandomSatisfiedChannelForRequest(param, group, priorityRetry)
				}
			}
		} else if !inCanary {
			channel, _ = getRandomSatisfiedChannelForRequest(param, group, priorityRetry)
			if outcomeErr := enqueueChannelRoutingControlDecision(param, group, channel); outcomeErr != nil {
				return nil, group, true, outcomeErr
			}
		}
		if channel == nil {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index+1)
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
			continue
		}

		common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, group)
		if crossGroupRetry && priorityRetry >= common.RetryTimes {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index+1)
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry()+1)
		} else {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index)
		}
		return channel, group, true, nil
	}
	return nil, param.TokenGroup, true, nil
}

func selectChannelRoutingCanaryForGroup(param *RetryParam, group string) (*model.Channel, bool, bool, error) {
	gate, active, err := channelRoutingCanaryGate(param.Ctx, group)
	if err != nil || !active || !gate.InCanary {
		return nil, active, active && gate.InCanary, err
	}

	sessionSet, err := channelRoutingSessionSet(param.Ctx)
	if err != nil {
		return nil, true, true, err
	}
	session, err := sessionSet.Session(group)
	if err != nil {
		return nil, true, true, err
	}
	canaryPolicy, err := session.CanaryPolicy()
	if err != nil {
		return nil, true, true, err
	}
	if err := PrepareChannelRoutingCanarySelection(param.Ctx, ChannelRoutingCanarySelection{
		Gate:            gate,
		WindowSeconds:   canaryPolicy.Evaluation.WindowSeconds,
		LatenessSeconds: canaryPolicy.Evaluation.CheckpointLatenessSeconds,
	}); err != nil {
		return nil, true, true, err
	}

	allowedChannels, err := model.GetRankedSatisfiedChannels(group, param.ModelName, param.RequestPath)
	if err != nil {
		return nil, true, true, err
	}
	allowedChannels, err = filterRoutingTrafficAdmissibleChannels(param.Ctx, allowedChannels)
	if err != nil {
		return nil, true, true, err
	}
	allowedIDs := make([]int, 0, len(allowedChannels))
	channelByID := make(map[int]*model.Channel, len(allowedChannels))
	for _, channel := range allowedChannels {
		if channel == nil || channel.Id <= 0 {
			return nil, true, true, errors.New("channel routing cache contains an invalid channel")
		}
		allowedIDs = append(allowedIDs, channel.Id)
		channelByID[channel.Id] = channel
	}

	excludedSet := smartRoutingExcludedChannelIDs(param.Ctx)
	excludedIDs := make([]int, 0, len(excludedSet))
	for channelID := range excludedSet {
		excludedIDs = append(excludedIDs, channelID)
	}
	sort.Ints(excludedIDs)
	excludedCredentialSet := smartRoutingExcludedCredentialIDs(param.Ctx)
	excludedCredentialIDs := make([]int, 0, len(excludedCredentialSet))
	for credentialID := range excludedCredentialSet {
		excludedCredentialIDs = append(excludedCredentialIDs, credentialID)
	}
	sort.Ints(excludedCredentialIDs)
	capacityExcluded := make([]int, 0, len(allowedIDs))
	probeExcluded := make([]int, 0, len(allowedIDs))
	capacityInput, capacityOutput := routingCapacityTokenEstimate(param.Ctx)
	promptTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0)
	completionTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0)
	profile, profileErr := routingRequestProfile(
		param.Ctx,
		group,
		param.GetRetry(),
		promptTokens,
		completionTokens,
	)
	if profileErr != nil {
		return nil, true, true, profileErr
	}
	var lastProbeErr error
	for attempts := 0; attempts <= len(allowedIDs); attempts++ {
		plan, planActive, planErr := session.Plan(channelrouting.RequestRoutingPlanInput{
			RequestPath:                 param.RequestPath,
			ModelName:                   param.ModelName,
			IsStream:                    common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
			RetryIndex:                  param.GetRetry(),
			PromptTokenEstimate:         promptTokens,
			CompletionTokenEstimate:     completionTokens,
			CostProfile:                 routingCostRequestProfile(param.Ctx),
			Profile:                     profile,
			AllowedChannelIDs:           allowedIDs,
			ExcludedChannelIDs:          excludedIDs,
			ExcludedCredentialIDs:       excludedCredentialIDs,
			ExcludedEndpointIdentities:  smartRoutingExcludedEndpointList(param.Ctx),
			ExcludedFailureDomainHashes: smartRoutingExcludedFailureDomainList(param.Ctx),
			CapacityExcludedChannelIDs:  capacityExcluded,
			ProbeExcludedChannelIDs:     probeExcluded,
			SlowStartFactor: func(key channelrouting.SlowStartKey) (float64, error) {
				return channelRoutingCanaryRuntime.slowStartFactor(gate.PolicyRevision, canaryPolicy, key)
			},
		})
		if planErr != nil {
			return nil, true, true, planErr
		}
		if !planActive || !plan.Gate.InCanary || plan.Gate != gate {
			return nil, true, true, channelrouting.ErrRoutingSessionInvalid
		}
		if plan.Result.SelectedChannelID <= 0 {
			enqueueChannelRoutingCanaryDecision(param, group, plan, nil)
			if lastProbeErr != nil {
				return nil, true, true, fmt.Errorf("channel routing half-open probe admission failed: %w", lastProbeErr)
			}
			return nil, true, true, nil
		}
		selected := channelByID[plan.Result.SelectedChannelID]
		if selected == nil || plan.SelectedIdentity.MemberID <= 0 || plan.SelectedIdentity.PoolID != gate.PoolID {
			return nil, true, true, channelrouting.ErrRoutingSessionInvalid
		}
		capacityModelName := param.ModelName
		if strings.HasSuffix(capacityModelName, ratio_setting.CompactModelSuffix) {
			capacityModelName = strings.TrimSuffix(capacityModelName, ratio_setting.CompactModelSuffix)
		}
		upstreamModelName, _, mappingErr := model.ResolveChannelModelMapping(selected.GetModelMapping(), capacityModelName)
		if mappingErr != nil {
			return nil, true, true, fmt.Errorf("channel routing upstream model mapping failed: %w", mappingErr)
		}
		if strings.TrimSpace(upstreamModelName) == "" {
			return nil, true, true, errors.New("channel routing upstream model mapping is empty")
		}
		var selectedCandidate *channelrouting.ShadowCandidateInput
		for candidateIndex := range plan.Replay.Candidates {
			candidate := &plan.Replay.Candidates[candidateIndex]
			if candidate.ChannelID == selected.Id {
				selectedCandidate = candidate
				break
			}
		}
		if selectedCandidate == nil || selectedCandidate.PoolMemberID != plan.SelectedIdentity.MemberID {
			return nil, true, true, channelrouting.ErrRoutingSessionInvalid
		}
		var selectedBreaker *routingselector.BreakerSnapshot
		if selectedCandidate.Breaker != nil {
			selectedBreaker = &routingselector.BreakerSnapshot{
				State: selectedCandidate.Breaker.State, Reason: selectedCandidate.Breaker.Reason,
				CooldownUntilUnix: selectedCandidate.Breaker.CooldownUntilUnix,
				HalfOpenInflight:  selectedCandidate.Breaker.HalfOpenInflight,
				UpdatedUnix:       selectedCandidate.Breaker.UpdatedUnix,
			}
		}
		probeSettings := routingselector.Settings{
			HalfOpenProbes: plan.Replay.Settings.HalfOpenProbes, NowUnix: plan.Replay.Settings.NowUnix,
			SnapshotStaleSec: plan.Replay.Settings.SnapshotStaleSec,
		}
		probeKey := routingbreaker.Key{
			ChannelID: selected.Id, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: param.ModelName, Group: group,
		}
		if plan.SelectedBreakerScope == channelrouting.BreakerScopeEndpoint {
			probeKey = routingbreaker.NewEndpointKey(plan.SelectedEndpointAuthority, plan.SelectedRegion)
		}
		probeAcquired, probeErr := acquireRoutingHalfOpenProbeForKey(
			param.Ctx,
			probeKey,
			routingHalfOpenProbeOwner{ChannelID: selected.Id, Model: param.ModelName, Group: group},
			selectedBreaker,
			probeSettings,
			true,
		)
		if !probeAcquired {
			probeExcluded = append(probeExcluded, selected.Id)
			if probeErr != nil {
				lastProbeErr = probeErr
				logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing canary half-open probe unavailable: %v", probeErr))
			}
			continue
		}

		inputDemand, demandErr := capacityInput.Demand(canaryPolicy.Capacity.InputTPM)
		if demandErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, true, fmt.Errorf("channel routing input capacity estimate failed: %w", demandErr)
		}
		outputDemand, demandErr := capacityOutput.Demand(canaryPolicy.Capacity.OutputTPM)
		if demandErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, true, fmt.Errorf("channel routing output capacity estimate failed: %w", demandErr)
		}
		demand := channelrouting.Demand{
			RPM:       1,
			InputTPM:  inputDemand,
			OutputTPM: outputDemand,
			Inflight:  1,
		}
		adaptiveLease, adaptiveErr := reserveRoutingAdaptiveConcurrency(
			gate.PolicyRevision,
			canaryPolicy,
			plan.SelectedIdentity,
			selected.Id,
			param.ModelName,
			upstreamModelName,
			nil,
		)
		if adaptiveErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			if errors.Is(adaptiveErr, channelrouting.ErrAdaptiveConcurrencyExhausted) ||
				errors.Is(adaptiveErr, channelrouting.ErrAdaptiveConcurrencyConflict) {
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, true, fmt.Errorf("channel routing adaptive concurrency admission failed: %w", adaptiveErr)
		}
		reservation, reserveErr := channelRoutingCanaryRuntime.tryReserve(gate.PolicyRevision, canaryPolicy, channelrouting.CapacityKey{
			PoolID:   plan.SelectedIdentity.PoolID,
			MemberID: plan.SelectedIdentity.MemberID,
			Model:    param.ModelName,
		}, demand)
		if reserveErr != nil {
			_ = adaptiveLease.Release()
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			if errors.Is(reserveErr, channelrouting.ErrCapacityExhausted) ||
				errors.Is(reserveErr, channelrouting.ErrCapacityLimitConflict) {
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, true, fmt.Errorf("channel routing capacity admission failed: %w", reserveErr)
		}
		admission := reservation.Admission()
		if err := SetRoutingCapacityReservation(param.Ctx, reservation); err != nil {
			cancelErr := reservation.Cancel()
			adaptiveErr = adaptiveLease.Release()
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, true, errors.Join(err, cancelErr, adaptiveErr)
		}
		if err := AttachRoutingAdaptiveConcurrency(param.Ctx, adaptiveLease); err != nil {
			cancelErr := CancelRoutingCapacityReservation(param.Ctx)
			adaptiveErr = adaptiveLease.Release()
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, true, errors.Join(err, cancelErr, adaptiveErr)
		}
		if err := PrepareChannelRoutingCanarySelection(param.Ctx, ChannelRoutingCanarySelection{
			Gate:              gate,
			WindowSeconds:     canaryPolicy.Evaluation.WindowSeconds,
			LatenessSeconds:   canaryPolicy.Evaluation.CheckpointLatenessSeconds,
			ExpectedCostUSD:   plan.Result.SelectedCost,
			ExpectedCostKnown: plan.Result.SelectedCostKnown,
		}); err != nil {
			cancelErr := CancelRoutingCapacityReservation(param.Ctx)
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, true, errors.Join(err, cancelErr)
		}
		if routingselector.BreakerNeedsHalfOpenProbe(selectedBreaker, probeSettings) {
			if err := prepareRoutingSlowStartProbe(param.Ctx, selected.Id, gate.PolicyRevision, canaryPolicy, channelrouting.SlowStartKey{
				PoolID: gate.PoolID, MemberID: plan.SelectedIdentity.MemberID, Model: param.ModelName,
			}); err != nil {
				cancelErr := CancelRoutingCapacityReservation(param.Ctx)
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				return nil, true, true, errors.Join(err, cancelErr)
			}
		}
		SetSelectedRoutingIdentity(param.Ctx, SelectedRoutingIdentity{
			ChannelID:         selected.Id,
			SnapshotRevision:  plan.SelectedIdentity.SnapshotRevision,
			PoolID:            plan.SelectedIdentity.PoolID,
			MemberID:          plan.SelectedIdentity.MemberID,
			CredentialID:      plan.SelectedIdentity.CredentialID,
			FailureDomainHash: plan.SelectedIdentity.FailureDomainHash,
		})
		enqueueChannelRoutingCanaryDecision(param, group, plan, &admission)
		return selected, true, true, nil
	}
	return nil, true, true, nil
}

func channelRoutingSessionSet(c *gin.Context) (*channelrouting.RequestRoutingSessionSet, error) {
	if c == nil {
		return nil, channelrouting.ErrRoutingSessionInvalid
	}
	if sessions, ok := common.GetContextKeyType[*channelrouting.RequestRoutingSessionSet](c, constant.ContextKeyRoutingSessionSet); ok && sessions != nil {
		return sessions, nil
	}
	requestID := common.GetContextKeyString(c, common.RequestIdKey)
	sessions, err := channelrouting.NewRequestRoutingSessionSet(requestID)
	if err != nil {
		return nil, err
	}
	common.SetContextKey(c, constant.ContextKeyRoutingSessionSet, sessions)
	return sessions, nil
}

func channelRoutingCanaryGate(c *gin.Context, group string) (channelrouting.CanaryGate, bool, error) {
	if c == nil || strings.TrimSpace(group) == "" ||
		!smart_routing_setting.CurrentEffectiveMode().AllowsEnterpriseFeatures() || !common.MemoryCacheEnabled {
		return channelrouting.CanaryGate{}, false, nil
	}
	sessions, pinned := common.GetContextKeyType[*channelrouting.RequestRoutingSessionSet](c, constant.ContextKeyRoutingSessionSet)
	if !pinned || sessions == nil {
		stage, exists := channelrouting.CurrentPoolDeploymentStage(group)
		if !exists || stage != model.RoutingDeploymentStageCanary {
			return channelrouting.CanaryGate{}, false, nil
		}
		var err error
		sessions, err = channelRoutingSessionSet(c)
		if err != nil {
			if errors.Is(err, channelrouting.ErrRoutingSessionUnavailable) {
				return channelrouting.CanaryGate{}, false, nil
			}
			return channelrouting.CanaryGate{}, false, err
		}
	}
	session, err := sessions.Session(group)
	if err != nil {
		if errors.Is(err, channelrouting.ErrRoutingSessionPoolNotFound) {
			return channelrouting.CanaryGate{}, false, nil
		}
		return channelrouting.CanaryGate{}, false, err
	}
	return session.Gate()
}

func ShouldBypassChannelRoutingAffinity(c *gin.Context, group string) (bool, error) {
	setting := smart_routing_setting.GetSetting()
	mode := smart_routing_setting.ResolveEffectiveMode(setting)
	if c == nil || group == "" || group == "auto" || !mode.AllowsAffinityRouting() || !common.MemoryCacheEnabled {
		return false, nil
	}
	if stage, exists := channelrouting.CurrentPoolDeploymentStage(group); exists && stage == model.RoutingDeploymentStageActive {
		return true, nil
	}
	gate, active, err := channelRoutingCanaryGate(c, group)
	if err != nil {
		return true, err
	}
	return active && gate.InCanary, nil
}

func GetAdmissibleAffinityChannelWithRoutingGate(
	c *gin.Context,
	preferredID int,
	modelName string,
	usingGroup string,
	requestPath string,
) (*model.Channel, string, bool, error) {
	setting := smart_routing_setting.GetSetting()
	mode := smart_routing_setting.ResolveEffectiveMode(setting)
	if c == nil || preferredID <= 0 || modelName == "" || usingGroup == "" ||
		!mode.AllowsAffinityRouting() || !common.MemoryCacheEnabled {
		channel, group, _ := GetAdmissibleAffinityChannel(c, preferredID, modelName, usingGroup, requestPath)
		return channel, group, false, nil
	}

	groups := []string{usingGroup}
	if usingGroup == "auto" {
		userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		groups = GetUserAutoGroup(userGroup)
	}
	for _, group := range groups {
		allowedChannels, err := model.GetRankedSatisfiedChannels(group, modelName, requestPath)
		if err != nil {
			return nil, group, true, err
		}
		preferredAllowed := false
		for _, channel := range allowedChannels {
			if channel != nil && channel.Id == preferredID {
				preferredAllowed = true
				break
			}
		}
		if !preferredAllowed {
			continue
		}
		trafficAdmissible, trafficErr := ChannelRoutingTrafficAdmissible(c, preferredID)
		if trafficErr != nil {
			return nil, group, true, trafficErr
		}
		if !trafficAdmissible {
			continue
		}
		if stage, exists := channelrouting.CurrentPoolDeploymentStage(group); exists && stage == model.RoutingDeploymentStageActive {
			setChannelRoutingBalancedAffinity(c, group, preferredID)
			return nil, group, true, nil
		}

		bypass, err := ShouldBypassChannelRoutingAffinity(c, group)
		if err != nil {
			return nil, group, true, err
		}
		if bypass {
			return nil, group, true, nil
		}

		var preferred *model.Channel
		if shouldActivateSmartRouting(setting) {
			preferred, preferredAllowed = admissibleAffinityChannelForSmartGroup(
				c, preferredID, modelName, group, requestPath, setting,
			)
		} else {
			preferred, _, preferredAllowed = admissibleAffinityChannelLegacy(
				c, preferredID, modelName, group, requestPath,
			)
		}
		if !preferredAllowed {
			continue
		}
		if usingGroup == "auto" {
			common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
		}
		return preferred, group, false, nil
	}
	return nil, "", false, nil
}

func enqueueChannelRoutingCanaryDecision(
	param *RetryParam,
	group string,
	plan channelrouting.RequestRoutingPlan,
	admission *channelrouting.CapacityAdmission,
) {
	actualCost, actualCostKnown := channelrouting.ShadowExpectedCostForChannel(plan.Replay, plan.Result.SelectedChannelID)
	selectedCostEstimate, _ := channelrouting.ShadowCostEstimateForChannel(plan.Replay, plan.Result.SelectedChannelID)
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               plan.Gate.PoolID,
		GroupName:            group,
		ModelName:            param.ModelName,
		SnapshotRevision:     plan.Gate.PolicyRevision,
		AlgorithmVersion:     plan.Replay.AlgorithmVersion,
		RetryIndex:           param.GetRetry(),
		IsStream:             plan.Replay.Profile.IsStream,
		ActualChannelID:      plan.Result.SelectedChannelID,
		ObservedChannelID:    plan.Result.SelectedChannelID,
		FilteredOpen:         plan.Result.FilteredOpen,
		FilteredCapacity:     plan.Result.FilteredCapacity,
		BreakerBypassed:      plan.Result.BreakerBypassed,
		Candidates:           plan.Result.Candidates,
		ReplayInput:          &plan.Replay,
		DifferenceType:       channelrouting.ClassifyShadowDifference(plan.Result.SelectedChannelID, plan.Result),
		ActualCostKnown:      actualCostKnown,
		ActualExpectedCost:   actualCost,
		ObservedCostKnown:    plan.Result.SelectedCostKnown,
		ObservedExpectedCost: plan.Result.SelectedCost,
		ActualCostEstimate:   selectedCostEstimate,
		ObservedCostEstimate: selectedCostEstimate,
		Gate:                 &plan.Gate,
		SelectedIdentity:     plan.SelectedIdentity,
		CapacityAdmission:    admission,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing canary audit dropped: %v", err))
		return
	}
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingDecisionID, decisionID)
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAlgorithmVersion, plan.Replay.AlgorithmVersion)
}

func enqueueChannelRoutingControlDecision(param *RetryParam, group string, selected *model.Channel) error {
	if param == nil || param.Ctx == nil || group == "" || group == "auto" {
		return nil
	}
	gate, active, err := channelRoutingCanaryGate(param.Ctx, group)
	if err != nil || !active || gate.InCanary {
		if err != nil {
			logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit gate dropped: %v", err))
		}
		return err
	}
	sessions, err := channelRoutingSessionSet(param.Ctx)
	if err != nil {
		return err
	}
	session, err := sessions.Session(group)
	if err != nil {
		return err
	}
	canaryPolicy, err := session.CanaryPolicy()
	if err != nil {
		return err
	}
	channelID := 0
	identity := channelrouting.Identity{}
	candidates := []channelrouting.DecisionCandidate(nil)
	expectedCost := float64(0)
	expectedCostKnown := false
	if selected != nil {
		channelID = selected.Id
		var known bool
		identity, known = session.IdentityForChannel(channelID)
		if !known {
			return channelrouting.ErrRoutingSessionInvalid
		}
		promptTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0)
		completionTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0)
		profile, profileErr := routingRequestProfile(
			param.Ctx,
			group,
			param.GetRetry(),
			promptTokens,
			completionTokens,
		)
		if profileErr != nil {
			return profileErr
		}
		expectedCost, expectedCostKnown, err = session.ExpectedCostForChannel(channelID, channelrouting.RequestRoutingCostInput{
			RequestPath:             param.RequestPath,
			ModelName:               param.ModelName,
			IsStream:                common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
			RetryIndex:              param.GetRetry(),
			PromptTokenEstimate:     promptTokens,
			CompletionTokenEstimate: completionTokens,
			Profile:                 profile,
		})
		if err != nil {
			return err
		}
		candidates = []channelrouting.DecisionCandidate{{
			PoolMemberID: identity.MemberID,
			ChannelID:    channelID,
			Eligible:     true,
		}}
	}
	if err := PrepareChannelRoutingCanarySelection(param.Ctx, ChannelRoutingCanarySelection{
		Gate:              gate,
		WindowSeconds:     canaryPolicy.Evaluation.WindowSeconds,
		LatenessSeconds:   canaryPolicy.Evaluation.CheckpointLatenessSeconds,
		ExpectedCostUSD:   expectedCost,
		ExpectedCostKnown: expectedCostKnown,
	}); err != nil {
		return err
	}
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               gate.PoolID,
		GroupName:            group,
		ModelName:            param.ModelName,
		SnapshotRevision:     gate.PolicyRevision,
		AlgorithmVersion:     channelrouting.DecisionAlgorithmCanary,
		RetryIndex:           param.GetRetry(),
		IsStream:             common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		ActualChannelID:      channelID,
		ObservedChannelID:    channelID,
		Candidates:           candidates,
		DifferenceType:       "control_legacy",
		ActualCostKnown:      expectedCostKnown,
		ActualExpectedCost:   expectedCost,
		ObservedCostKnown:    expectedCostKnown,
		ObservedExpectedCost: expectedCost,
		Gate:                 &gate,
		SelectedIdentity:     identity,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit dropped: %v", err))
		return nil
	}
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingDecisionID, decisionID)
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAlgorithmVersion, channelrouting.DecisionAlgorithmCanary)
	return nil
}
