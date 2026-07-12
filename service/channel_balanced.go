package service

import (
	"errors"
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	globalsetting "github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

const ginKeyChannelRoutingBalancedAffinity = "channel_routing_balanced_affinity"

type channelRoutingBalancedAffinity struct {
	Group     string
	ChannelID int
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
				channel, _ = model.GetRandomSatisfiedChannel(group, param.ModelName, priorityRetry, param.RequestPath)
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
	allowedIDs := make([]int, 0, len(allowedChannels))
	channelByID := make(map[int]*model.Channel, len(allowedChannels))
	for _, channel := range allowedChannels {
		if channel == nil || channel.Id <= 0 {
			return nil, true, errors.New("channel routing cache contains an invalid channel")
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
	preferredChannelID := channelRoutingBalancedPreferredChannel(param.Ctx, group)
	policyRevision := session.SnapshotRevision()
	if policyRevision == 0 {
		return nil, true, channelrouting.ErrRoutingSessionInvalid
	}
	capacityExcluded := make([]int, 0, len(allowedIDs))
	probeExcluded := make([]int, 0, len(allowedIDs))
	var lastProbeErr error
	for attempts := 0; attempts <= len(allowedIDs); attempts++ {
		plan, active, planErr := session.PlanBalanced(channelrouting.BalancedRoutingPlanInput{
			RequestRoutingPlanInput: channelrouting.RequestRoutingPlanInput{
				RequestPath:                param.RequestPath,
				ModelName:                  param.ModelName,
				IsStream:                   common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
				RetryIndex:                 param.GetRetry(),
				PromptTokenEstimate:        max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0),
				CompletionTokenEstimate:    max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0),
				AllowedChannelIDs:          allowedIDs,
				ExcludedChannelIDs:         excludedIDs,
				CapacityExcludedChannelIDs: capacityExcluded,
				ProbeExcludedChannelIDs:    probeExcluded,
				SlowStartFactor: func(key channelrouting.SlowStartKey) (float64, error) {
					return channelRoutingCanaryRuntime.slowStartFactor(policyRevision, capacityPolicy, key)
				},
			},
			PreferredChannelID: preferredChannelID,
		})
		if planErr != nil {
			return nil, true, planErr
		}
		if !active || plan.PolicyRevision == 0 || plan.PoolID <= 0 {
			return nil, true, channelrouting.ErrRoutingSessionInvalid
		}
		if plan.SelectedChannelID <= 0 {
			enqueueChannelRoutingBalancedDecision(param, group, plan, nil)
			if lastProbeErr != nil {
				return nil, true, fmt.Errorf("channel routing half-open probe admission failed: %w", lastProbeErr)
			}
			return nil, true, nil
		}
		selected := channelByID[plan.SelectedChannelID]
		if selected == nil || plan.SelectedIdentity.MemberID <= 0 || plan.SelectedIdentity.PoolID != plan.PoolID {
			return nil, true, channelrouting.ErrRoutingSessionInvalid
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
		probeAcquired, probeErr := acquireRoutingHalfOpenProbeForKey(
			param.Ctx, probeKey, plan.SelectedBreaker, probeSettings, true,
		)
		if !probeAcquired {
			probeExcluded = append(probeExcluded, selected.Id)
			if probeErr != nil {
				lastProbeErr = probeErr
				logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing balanced half-open probe unavailable: %v", probeErr))
			}
			continue
		}
		demand := channelrouting.Demand{
			RPM:       1,
			InputTPM:  int64(max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0)),
			OutputTPM: int64(max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0)),
			Inflight:  1,
		}
		reservation, reserveErr := channelRoutingCanaryRuntime.tryReserve(
			plan.PolicyRevision, capacityPolicy,
			channelrouting.CapacityKey{PoolID: plan.PoolID, MemberID: plan.SelectedIdentity.MemberID, Model: param.ModelName},
			demand,
		)
		if reserveErr != nil {
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			if errors.Is(reserveErr, channelrouting.ErrCapacityExhausted) ||
				errors.Is(reserveErr, channelrouting.ErrCapacityLimitConflict) {
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, fmt.Errorf("channel routing capacity admission failed: %w", reserveErr)
		}
		admission := reservation.Admission()
		if err := SetRoutingCapacityReservation(param.Ctx, reservation); err != nil {
			cancelErr := reservation.Cancel()
			ReleaseRoutingHalfOpenProbe(param.Ctx, selected.Id, param.ModelName, group)
			return nil, true, errors.Join(err, cancelErr)
		}
		if routingselector.BreakerNeedsHalfOpenProbe(plan.SelectedBreaker, probeSettings) {
			if err := channelRoutingCanaryRuntime.startRecovery(plan.PolicyRevision, capacityPolicy, channelrouting.SlowStartKey{
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
		})
		if plan.AffinityUsed {
			MarkChannelAffinityUsed(param.Ctx, group, selected.Id)
		}
		enqueueChannelRoutingBalancedDecision(param, group, plan, &admission)
		return selected, true, nil
	}
	return nil, true, nil
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
	_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               plan.PoolID,
		GroupName:            group,
		ModelName:            param.ModelName,
		SnapshotRevision:     plan.PolicyRevision,
		AlgorithmVersion:     channelrouting.DecisionAlgorithmBalancedV1,
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
		SelectedIdentity:     plan.SelectedIdentity,
		CapacityAdmission:    admission,
		ActivationID:         plan.ActivationID,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing balanced audit dropped: %v", err))
	}
}
