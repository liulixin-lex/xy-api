package service

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	globalsetting "github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

const (
	channelRoutingCanaryMaxEntries = 100_000
	channelRoutingCanaryShards     = 64
)

var (
	channelRoutingCanaryCapacity  *channelrouting.CapacityTracker
	channelRoutingCanarySlowStart *channelrouting.SlowStartTracker
	channelRoutingCanaryLimit     = channelrouting.Limit{
		RPM:       600,
		InputTPM:  1_000_000,
		OutputTPM: 250_000,
		Inflight:  32,
	}
)

func init() {
	var err error
	channelRoutingCanaryCapacity, err = channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: channelRoutingCanaryMaxEntries,
		IdleTTL:    15 * time.Minute,
		Shards:     channelRoutingCanaryShards,
	})
	if err != nil {
		panic(err)
	}
	channelRoutingCanarySlowStart, err = channelrouting.NewSlowStartTracker(channelrouting.SlowStartPolicy{
		MinimumFactor: 0.10,
		RampDuration:  5 * time.Minute,
		StateTTL:      24 * time.Hour,
		MaxEntries:    channelRoutingCanaryMaxEntries,
	}, nil)
	if err != nil {
		panic(err)
	}
}

func cacheGetChannelRoutingCanary(
	param *RetryParam,
	setting smart_routing_setting.SmartRoutingSetting,
) (*model.Channel, string, bool, error) {
	if param == nil || param.Ctx == nil || !setting.Enabled || !common.MemoryCacheEnabled {
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
		if legacyErr == nil {
			enqueueChannelRoutingControlDecision(param, group, channel)
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
		priorityRetry := param.GetRetry()
		if index > startGroupIndex {
			priorityRetry = 0
		}

		channel, active, inCanary, err := selectChannelRoutingCanaryForGroup(param, group)
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
		} else if !inCanary {
			channel, _ = model.GetRandomSatisfiedChannel(group, param.ModelName, priorityRetry, param.RequestPath)
			enqueueChannelRoutingControlDecision(param, group, channel)
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

	allowedChannels, err := model.GetRankedSatisfiedChannels(group, param.ModelName, param.RequestPath)
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
	capacityExcluded := make([]int, 0, len(allowedIDs))
	for attempts := 0; attempts <= len(allowedIDs); attempts++ {
		plan, planActive, planErr := session.Plan(channelrouting.RequestRoutingPlanInput{
			RequestPath:                param.RequestPath,
			ModelName:                  param.ModelName,
			IsStream:                   common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
			RetryIndex:                 param.GetRetry(),
			PromptTokenEstimate:        max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0),
			CompletionTokenEstimate:    max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0),
			AllowedChannelIDs:          allowedIDs,
			ExcludedChannelIDs:         excludedIDs,
			CapacityExcludedChannelIDs: capacityExcluded,
			SlowStartFactor:            channelRoutingCanarySlowStart.Factor,
		})
		if planErr != nil {
			return nil, true, true, planErr
		}
		if !planActive || !plan.Gate.InCanary || plan.Gate != gate {
			return nil, true, true, channelrouting.ErrRoutingSessionInvalid
		}
		if plan.Result.SelectedChannelID <= 0 {
			return nil, true, true, nil
		}
		selected := channelByID[plan.Result.SelectedChannelID]
		if selected == nil || plan.SelectedIdentity.MemberID <= 0 || plan.SelectedIdentity.PoolID != gate.PoolID {
			return nil, true, true, channelrouting.ErrRoutingSessionInvalid
		}

		demand := channelrouting.Demand{
			RPM:       1,
			InputTPM:  int64(max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0)),
			OutputTPM: int64(max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0)),
			Inflight:  1,
		}
		reservation, reserveErr := channelRoutingCanaryCapacity.TryReserve(channelrouting.CapacityKey{
			PoolID:   plan.SelectedIdentity.PoolID,
			MemberID: plan.SelectedIdentity.MemberID,
			Model:    param.ModelName,
		}, demand, channelRoutingCanaryLimit)
		if reserveErr != nil {
			if errors.Is(reserveErr, channelrouting.ErrCapacityExhausted) ||
				errors.Is(reserveErr, channelrouting.ErrCapacityLimitConflict) {
				capacityExcluded = append(capacityExcluded, selected.Id)
				continue
			}
			return nil, true, true, fmt.Errorf("channel routing capacity admission failed: %w", reserveErr)
		}
		admission := reservation.Admission()
		if err := SetRoutingCapacityReservation(param.Ctx, reservation); err != nil {
			_ = reservation.Cancel()
			return nil, true, true, err
		}
		SetSelectedRoutingIdentity(param.Ctx, SelectedRoutingIdentity{
			ChannelID:        selected.Id,
			SnapshotRevision: plan.SelectedIdentity.SnapshotRevision,
			PoolID:           plan.SelectedIdentity.PoolID,
			MemberID:         plan.SelectedIdentity.MemberID,
		})
		enqueueChannelRoutingCanaryDecision(param, group, plan, admission)
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
	if c == nil || group == "" || group == "auto" || !setting.Enabled || !common.MemoryCacheEnabled {
		return false, nil
	}
	gate, active, err := channelRoutingCanaryGate(c, group)
	if err != nil {
		return true, err
	}
	return active && gate.InCanary, nil
}

func enqueueChannelRoutingCanaryDecision(
	param *RetryParam,
	group string,
	plan channelrouting.RequestRoutingPlan,
	admission channelrouting.CapacityAdmission,
) {
	actualCost, actualCostKnown := channelrouting.ShadowExpectedCostForChannel(plan.Replay, plan.Result.SelectedChannelID)
	_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               plan.Gate.PoolID,
		GroupName:            group,
		ModelName:            param.ModelName,
		SnapshotRevision:     plan.Gate.PolicyRevision,
		AlgorithmVersion:     channelrouting.DecisionAlgorithmCanaryV1,
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
		Gate:                 &plan.Gate,
		SelectedIdentity:     plan.SelectedIdentity,
		CapacityAdmission:    &admission,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing canary audit dropped: %v", err))
	}
}

func enqueueChannelRoutingControlDecision(param *RetryParam, group string, selected *model.Channel) {
	if param == nil || param.Ctx == nil || group == "" || group == "auto" {
		return
	}
	gate, active, err := channelRoutingCanaryGate(param.Ctx, group)
	if err != nil || !active || gate.InCanary {
		if err != nil {
			logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit gate dropped: %v", err))
		}
		return
	}
	sessions, err := channelRoutingSessionSet(param.Ctx)
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit session dropped: %v", err))
		return
	}
	session, err := sessions.Session(group)
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit session dropped: %v", err))
		return
	}
	channelID := 0
	identity := channelrouting.Identity{}
	candidates := []channelrouting.DecisionCandidate(nil)
	if selected != nil {
		channelID = selected.Id
		var known bool
		identity, known = session.IdentityForChannel(channelID)
		if !known {
			logger.LogWarn(param.Ctx, "channel routing control audit identity dropped: selected channel is outside the pinned pool")
			return
		}
		candidates = []channelrouting.DecisionCandidate{{
			PoolMemberID: identity.MemberID,
			ChannelID:    channelID,
			Eligible:     true,
		}}
	}
	_, err = channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:         common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:            gate.PoolID,
		GroupName:         group,
		ModelName:         param.ModelName,
		SnapshotRevision:  gate.PolicyRevision,
		AlgorithmVersion:  channelrouting.DecisionAlgorithmCanaryV1,
		RetryIndex:        param.GetRetry(),
		IsStream:          common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		ActualChannelID:   channelID,
		ObservedChannelID: channelID,
		Candidates:        candidates,
		DifferenceType:    "control_legacy",
		Gate:              &gate,
		SelectedIdentity:  identity,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing control audit dropped: %v", err))
	}
}
