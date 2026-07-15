package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	globalsetting "github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

const ginKeyChannelRoutingBalancedAffinity = "channel_routing_balanced_affinity"

var ErrPinnedRoutingIdentityUnavailable = errors.New("pinned routing identity is unavailable")

type channelRoutingBalancedAffinity struct {
	Group     string
	ChannelID int
}

type channelRoutingBalancedRequirement struct {
	channelID    int
	credentialID int
}

func cacheGetChannelRoutingBalanced(
	param *RetryParam,
	setting smart_routing_setting.SmartRoutingSetting,
) (*model.Channel, string, bool, error) {
	if param == nil || param.Ctx == nil || !setting.Enabled || !common.MemoryCacheEnabled {
		return nil, "", false, nil
	}
	if param.TokenGroup != "auto" {
		channel, active, err := selectChannelRoutingBalancedForGroup(param, param.TokenGroup)
		return channel, param.TokenGroup, active, err
	}
	if len(globalsetting.GetAutoGroups()) == 0 {
		return nil, param.TokenGroup, true, errors.New("auto groups is not enabled")
	}
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	autoGroups := GetUserAutoGroup(userGroup)
	if len(autoGroups) == 0 {
		return nil, param.TokenGroup, true, errors.New("auto groups is not enabled")
	}
	hasActivePool := false
	for _, group := range autoGroups {
		if stage, exists := channelrouting.CurrentPoolDeploymentStage(group); exists && stage == model.RoutingDeploymentStageActive {
			hasActivePool = true
			break
		}
	}
	if !hasActivePool {
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
		priorityRetry := param.GetRetry()
		if index > startGroupIndex {
			priorityRetry = 0
		}
		channel, active, err := selectChannelRoutingBalancedForGroup(param, group)
		if err != nil {
			return nil, group, true, err
		}
		if !active {
			if shouldActivateSmartRouting(setting) {
				channel, err = selectSmartChannelForGroup(param, group, setting, true)
				if err != nil {
					return nil, group, true, err
				}
			} else {
				channel, _ = getRandomSatisfiedChannelForRequest(param, group, priorityRetry)
			}
		}
		if channel == nil {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index+1)
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
			param.SetRetry(0)
			continue
		}
		common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, group)
		if crossGroupRetry && priorityRetry >= common.RetryTimes {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index+1)
			param.SetRetry(0)
			param.ResetRetryNextTry()
		} else {
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, index)
		}
		return channel, group, true, nil
	}
	return nil, param.TokenGroup, true, nil
}

func selectChannelRoutingBalancedForGroup(param *RetryParam, group string) (*model.Channel, bool, error) {
	return selectChannelRoutingBalancedForGroupWithRequirement(param, group, channelRoutingBalancedRequirement{})
}

// ReservePinnedChannelRoutingAttempt admits stateful work through the same
// health, breaker and capacity path as Balanced traffic, while forbidding any
// other channel or Credential from being selected.
func ReservePinnedChannelRoutingAttempt(
	param *RetryParam,
	group string,
	channelID int,
	credentialID int,
) (*model.Channel, bool, error) {
	if param == nil || param.Ctx == nil || strings.TrimSpace(group) == "" || channelID <= 0 || credentialID < 0 {
		return nil, false, ErrPinnedRoutingIdentityUnavailable
	}
	return selectChannelRoutingBalancedForGroupWithRequirement(param, group, channelRoutingBalancedRequirement{
		channelID: channelID, credentialID: credentialID,
	})
}

func selectChannelRoutingBalancedForGroupWithRequirement(
	param *RetryParam,
	group string,
	requirement channelRoutingBalancedRequirement,
) (*model.Channel, bool, error) {
	stage, exists := channelrouting.CurrentPoolDeploymentStage(group)
	if !exists || stage != model.RoutingDeploymentStageActive {
		return nil, false, nil
	}
	sessionSet, err := channelRoutingSessionSet(param.Ctx)
	if err != nil {
		return nil, true, err
	}
	session, err := sessionSet.Session(group)
	if err != nil {
		return nil, true, err
	}
	capacityPolicy, err := session.CanaryPolicy()
	if err != nil {
		return nil, true, err
	}
	allowedChannels, err := model.GetRankedSatisfiedChannels(group, param.ModelName, param.RequestPath)
	if err != nil {
		return nil, true, err
	}
	allowedChannels, err = filterRoutingTrafficAdmissibleChannels(param.Ctx, allowedChannels)
	if err != nil {
		return nil, true, err
	}
	allowedIDs := make([]int, 0, len(allowedChannels))
	channelByID := make(map[int]*model.Channel, len(allowedChannels))
	for _, channel := range allowedChannels {
		if channel == nil || channel.Id <= 0 {
			return nil, true, errors.New("channel routing cache contains an invalid channel")
		}
		allowedIDs = append(allowedIDs, channel.Id)
		channelByID[channel.Id] = channel
	}
	if requirement.channelID > 0 {
		requiredChannel := channelByID[requirement.channelID]
		if requiredChannel == nil {
			return nil, true, ErrPinnedRoutingIdentityUnavailable
		}
		allowedIDs = []int{requirement.channelID}
		channelByID = map[int]*model.Channel{requirement.channelID: requiredChannel}
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
	preferredChannelID := channelRoutingBalancedPreferredChannel(param.Ctx, group)
	policyRevision := session.SnapshotRevision()
	if policyRevision == 0 {
		return nil, true, channelrouting.ErrRoutingSessionInvalid
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
		return nil, true, profileErr
	}
	capacityInput, capacityOutput := routingCapacityTokenEstimate(param.Ctx)
	capacityExcluded := make([]int, 0, len(allowedIDs))
	probeExcluded := make([]int, 0, len(allowedIDs))
	var lastProbeErr error
	var lastCapacityErr error
	for attempts := 0; attempts <= len(allowedIDs); attempts++ {
		plan, active, planErr := session.PlanBalanced(channelrouting.BalancedRoutingPlanInput{
			RequestRoutingPlanInput: channelrouting.RequestRoutingPlanInput{
				RequestPath:                param.RequestPath,
				ModelName:                  param.ModelName,
				IsStream:                   common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
				RetryIndex:                 param.GetRetry(),
				PromptTokenEstimate:        promptTokens,
				CompletionTokenEstimate:    completionTokens,
				CostProfile:                routingCostRequestProfile(param.Ctx),
				Profile:                    profile,
				AllowedChannelIDs:          allowedIDs,
				ExcludedChannelIDs:         excludedIDs,
				ExcludedCredentialIDs:      excludedCredentialIDs,
				RequiredCredentialID:       requirement.credentialID,
				CapacityExcludedChannelIDs: capacityExcluded,
				ProbeExcludedChannelIDs:    probeExcluded,
				SlowStartFactor: func(key channelrouting.SlowStartKey) (float64, error) {
					return channelRoutingCanaryRuntime.slowStartFactor(policyRevision, capacityPolicy, key)
				},
			},
			PreferredChannelID: preferredChannelID,
			RuntimeByChannelID: channelRoutingBalancedRuntimeByChannelID(
				session, channelByID, param.ModelName, group,
			),
		})
		if planErr != nil {
			return nil, true, planErr
		}
		if !active || plan.PolicyRevision == 0 || plan.PoolID <= 0 {
			return nil, true, channelrouting.ErrRoutingSessionInvalid
		}
		if plan.SelectedChannelID <= 0 {
			enqueueChannelRoutingBalancedDecision(param, group, plan, nil)
			if requirement.channelID > 0 && lastCapacityErr != nil {
				return nil, true, lastCapacityErr
			}
			if lastProbeErr != nil {
				return nil, true, fmt.Errorf("channel routing half-open probe admission failed: %w", lastProbeErr)
			}
			if requirement.channelID > 0 {
				return nil, true, ErrPinnedRoutingIdentityUnavailable
			}
			return nil, true, nil
		}
		selected := channelByID[plan.SelectedChannelID]
		if selected == nil || plan.SelectedIdentity.MemberID <= 0 || plan.SelectedIdentity.PoolID != plan.PoolID {
			return nil, true, channelrouting.ErrRoutingSessionInvalid
		}
		capacityModelName := param.ModelName
		if strings.HasSuffix(capacityModelName, ratio_setting.CompactModelSuffix) {
			capacityModelName = strings.TrimSuffix(capacityModelName, ratio_setting.CompactModelSuffix)
		}
		upstreamModelName, _, mappingErr := model.ResolveChannelModelMapping(selected.GetModelMapping(), capacityModelName)
		if mappingErr != nil {
			return nil, true, fmt.Errorf("channel routing upstream model mapping failed: %w", mappingErr)
		}
		if strings.TrimSpace(upstreamModelName) == "" {
			return nil, true, errors.New("channel routing upstream model mapping is empty")
		}
		probeSettings := routingselector.Settings{
			HalfOpenProbes:   plan.Policy.HalfOpenProbes,
			NowUnix:          common.GetTimestamp(),
			SnapshotStaleSec: plan.Policy.SnapshotStaleSec,
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
			plan.SelectedBreaker,
			probeSettings,
			true,
		)
		if !probeAcquired {
			probeExcluded = append(probeExcluded, selected.Id)
			if probeErr != nil {
				lastProbeErr = probeErr
				logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing balanced half-open probe unavailable: %v", probeErr))
			}
			continue
		}
		localInputDemand, demandErr := capacityInput.Demand(capacityPolicy.Capacity.InputTPM)
		if demandErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, fmt.Errorf("channel routing input capacity estimate failed: %w", demandErr)
		}
		localOutputDemand, demandErr := capacityOutput.Demand(capacityPolicy.Capacity.OutputTPM)
		if demandErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, fmt.Errorf("channel routing output capacity estimate failed: %w", demandErr)
		}
		demand := channelrouting.Demand{
			RPM:       1,
			InputTPM:  localInputDemand,
			OutputTPM: localOutputDemand,
			Inflight:  1,
		}
		strictCost, strictCostKnown, costErr := routingStrictCapacityCost(
			session, group, selected.Id, param, capacityInput, capacityOutput,
		)
		if costErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, fmt.Errorf("channel routing strict capacity cost failed: %w", costErr)
		}
		strictRequest, strict, strictErr := session.StrictCapacityRequest(
			plan.SelectedIdentity, param.ModelName, upstreamModelName,
			capacityInput, capacityOutput,
			strictCost, strictCostKnown,
		)
		if strictErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			if errors.Is(strictErr, channelrouting.ErrStrictCapacityExhausted) ||
				errors.Is(strictErr, channelrouting.ErrEnterpriseCapacityCostUnknown) {
				lastCapacityErr = strictErr
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, fmt.Errorf("channel routing strict capacity plan failed: %w", strictErr)
		}
		var strictRequestForAdaptive *channelrouting.StrictCapacityRequest
		if strict {
			strictRequestForAdaptive = &strictRequest
		}
		adaptiveLease, adaptiveErr := reserveRoutingAdaptiveConcurrency(
			plan.PolicyRevision,
			capacityPolicy,
			plan.SelectedIdentity,
			selected.Id,
			param.ModelName,
			upstreamModelName,
			strictRequestForAdaptive,
		)
		if adaptiveErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			if errors.Is(adaptiveErr, channelrouting.ErrAdaptiveConcurrencyExhausted) ||
				errors.Is(adaptiveErr, channelrouting.ErrAdaptiveConcurrencyConflict) {
				lastCapacityErr = adaptiveErr
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, fmt.Errorf("channel routing adaptive concurrency admission failed: %w", adaptiveErr)
		}
		var admission channelrouting.CapacityAdmission
		if strict {
			reserveContext := context.Background()
			if param.Ctx.Request != nil {
				reserveContext = param.Ctx.Request.Context()
			}
			reservation, reserveErr := channelrouting.DefaultStrictCapacityCoordinator().TryReserve(reserveContext, strictRequest)
			if reserveErr != nil {
				_ = adaptiveLease.Release()
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				if errors.Is(reserveErr, channelrouting.ErrStrictCapacityExhausted) {
					lastCapacityErr = reserveErr
					capacityExcluded = append(capacityExcluded, selected.Id)
					continue
				}
				return nil, true, fmt.Errorf("channel routing strict capacity admission failed: %w", reserveErr)
			}
			admission, reserveErr = channelrouting.CapacityAdmissionFromStrict(
				plan.SelectedIdentity, param.ModelName, reservation.Admission(),
			)
			if reserveErr == nil {
				reserveErr = SetRoutingStrictCapacityReservation(param.Ctx, reservation)
			}
			if reserveErr != nil {
				cancelErr := reservation.Cancel(context.Background())
				adaptiveErr = adaptiveLease.Release()
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				return nil, true, errors.Join(reserveErr, cancelErr, adaptiveErr)
			}
		} else {
			reservation, reserveErr := channelRoutingCanaryRuntime.tryReserve(
				plan.PolicyRevision, capacityPolicy,
				channelrouting.CapacityKey{PoolID: plan.PoolID, MemberID: plan.SelectedIdentity.MemberID, Model: param.ModelName},
				demand,
			)
			if reserveErr != nil {
				_ = adaptiveLease.Release()
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				if errors.Is(reserveErr, channelrouting.ErrCapacityExhausted) ||
					errors.Is(reserveErr, channelrouting.ErrCapacityLimitConflict) {
					lastCapacityErr = reserveErr
					capacityExcluded = append(capacityExcluded, selected.Id)
					continue
				}
				return nil, true, fmt.Errorf("channel routing capacity admission failed: %w", reserveErr)
			}
			admission = reservation.Admission()
			if err := SetRoutingCapacityReservation(param.Ctx, reservation); err != nil {
				cancelErr := reservation.Cancel()
				adaptiveErr = adaptiveLease.Release()
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				return nil, true, errors.Join(err, cancelErr, adaptiveErr)
			}
		}
		if err := AttachRoutingAdaptiveConcurrency(param.Ctx, adaptiveLease); err != nil {
			cancelErr := CancelRoutingCapacityReservation(param.Ctx)
			adaptiveErr = adaptiveLease.Release()
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, errors.Join(err, cancelErr, adaptiveErr)
		}
		if routingselector.BreakerNeedsHalfOpenProbe(plan.SelectedBreaker, probeSettings) {
			if err := prepareRoutingSlowStartProbe(param.Ctx, selected.Id, plan.PolicyRevision, capacityPolicy, channelrouting.SlowStartKey{
				PoolID: plan.PoolID, MemberID: plan.SelectedIdentity.MemberID, Model: param.ModelName,
			}); err != nil {
				cancelErr := CancelRoutingCapacityReservation(param.Ctx)
				ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
				return nil, true, errors.Join(err, cancelErr)
			}
		}
		SetSelectedRoutingIdentity(param.Ctx, SelectedRoutingIdentity{
			ChannelID: selected.Id, SnapshotRevision: plan.SelectedIdentity.SnapshotRevision,
			PoolID: plan.SelectedIdentity.PoolID, MemberID: plan.SelectedIdentity.MemberID,
			CredentialID:      plan.SelectedIdentity.CredentialID,
			UpstreamAccountID: plan.SelectedIdentity.UpstreamAccountID,
		})
		if plan.AffinityUsed {
			MarkChannelAffinityUsed(param.Ctx, group, selected.Id)
		}
		enqueueChannelRoutingBalancedDecision(param, group, plan, &admission)
		return selected, true, nil
	}
	return nil, true, nil
}

func channelRoutingBalancedRuntimeByChannelID(
	session *channelrouting.RequestRoutingSession,
	channels map[int]*model.Channel,
	modelName string,
	group string,
) map[int]routingselector.BalancedRuntimeState {
	runtimeByChannelID := make(map[int]routingselector.BalancedRuntimeState, len(channels))
	for channelID, channel := range channels {
		if channelID <= 0 || channel == nil || channel.Id != channelID || channel.ChannelInfo.IsMultiKey {
			continue
		}
		identity, exists := session.IdentityForChannel(channelID)
		if !exists || identity.MemberID <= 0 {
			continue
		}
		key := routinghotcache.Key{
			ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model: modelName, Group: group,
		}
		state := routingselector.BalancedRuntimeState{
			Inflight: routingmetrics.StableInflightCount(routingmetrics.StableInflightKey{
				PoolMemberID: identity.MemberID,
				CredentialID: identity.CredentialID,
				Model:        modelName,
			}),
			HasInflight: true,
		}
		if capacity, ok := routinghotcache.GetCapacityCooldown(key); ok {
			cooldownUntil := max(capacity.CooldownUntilUnixMilli, int64(0))
			state.CooldownUntilUnixMilli = &cooldownUntil
		}
		if breaker, ok := routinghotcache.GetBreaker(key); ok {
			state.Breaker = &routingselector.BreakerSnapshot{
				State: breaker.State, Reason: breaker.Reason,
				CooldownUntilUnix: breaker.CooldownUntilUnix,
				HalfOpenInflight:  breaker.HalfOpenInflight,
				UpdatedUnix:       breaker.UpdatedUnix,
			}
		}
		runtimeByChannelID[channelID] = state
	}
	return runtimeByChannelID
}

func setChannelRoutingBalancedAffinity(c *gin.Context, group string, channelID int) {
	if c == nil || group == "" || channelID <= 0 {
		return
	}
	c.Set(ginKeyChannelRoutingBalancedAffinity, channelRoutingBalancedAffinity{Group: group, ChannelID: channelID})
}

func channelRoutingBalancedPreferredChannel(c *gin.Context, group string) int {
	if c == nil || group == "" {
		return 0
	}
	value, exists := c.Get(ginKeyChannelRoutingBalancedAffinity)
	if !exists {
		return 0
	}
	affinity, ok := value.(channelRoutingBalancedAffinity)
	if !ok || affinity.Group != group {
		return 0
	}
	return affinity.ChannelID
}

func enqueueChannelRoutingBalancedDecision(
	param *RetryParam,
	group string,
	plan channelrouting.BalancedRoutingPlan,
	admission *channelrouting.CapacityAdmission,
) {
	differenceType := "active_unavailable"
	if plan.SelectedChannelID > 0 {
		differenceType = "active_selected"
	}
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               plan.PoolID,
		GroupName:            group,
		ModelName:            param.ModelName,
		SnapshotRevision:     plan.PolicyRevision,
		AlgorithmVersion:     plan.AlgorithmVersion,
		RetryIndex:           param.GetRetry(),
		IsStream:             plan.Profile.IsStream,
		ActualChannelID:      plan.SelectedChannelID,
		ObservedChannelID:    plan.SelectedChannelID,
		FilteredOpen:         plan.FilteredOpen,
		FilteredCapacity:     plan.FilteredCapacity,
		Candidates:           plan.Candidates,
		BalancedReplayInput:  &plan.Replay,
		DifferenceType:       differenceType,
		ActualCostKnown:      plan.SelectedCostKnown,
		ActualExpectedCost:   plan.SelectedCost,
		ObservedCostKnown:    plan.SelectedCostKnown,
		ObservedExpectedCost: plan.SelectedCost,
		ActualCostEstimate:   plan.SelectedCostEstimate,
		ObservedCostEstimate: plan.SelectedCostEstimate,
		SelectedIdentity:     plan.SelectedIdentity,
		CapacityAdmission:    admission,
		ActivationID:         plan.ActivationID,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing balanced audit dropped: %v", err))
		return
	}
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingDecisionID, decisionID)
	common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAlgorithmVersion, plan.AlgorithmVersion)
}
