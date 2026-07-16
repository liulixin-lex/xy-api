package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
)

type RetryParam struct {
	Ctx          *gin.Context
	TokenGroup   string
	ModelName    string
	RequestPath  string
	Retry        *int
	resetNextTry bool
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) SetRetry(retry int) {
	p.Retry = &retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.resetNextTry {
		p.resetNextTry = false
		return
	}
	if p.Retry == nil {
		p.Retry = new(int)
	}
	*p.Retry++
}

func (p *RetryParam) ResetRetryNextTry() {
	p.resetNextTry = true
}

func autoGroupPriorityRetry(c *gin.Context, globalRetry int) int {
	if globalRetry < 0 {
		globalRetry = 0
	}
	if c == nil {
		return globalRetry
	}
	startRetry := 0
	if stored, exists := common.GetContextKey(c, constant.ContextKeyAutoGroupRetryIndex); exists {
		if value, ok := stored.(int); ok && value >= 0 && value <= globalRetry {
			startRetry = value
		}
	}
	return globalRetry - startRetry
}

func beginAutoGroupAtRetry(c *gin.Context, globalRetry int) {
	if c == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyAutoGroupRetryIndex, max(globalRetry, 0))
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// 尝试获取一个满足要求的随机渠道。
//
// For "auto" tokenGroup with cross-group Retry enabled:
// 对于启用了跨分组重试的 "auto" tokenGroup：
//
//   - Each group will exhaust all its priorities before moving to the next group.
//     每个分组会用完所有优先级后才会切换到下一个分组。
//
//   - Uses ContextKeyAutoGroupIndex to track current group index.
//     使用 ContextKeyAutoGroupIndex 跟踪当前分组索引。
//
//   - Uses ContextKeyAutoGroupRetryIndex to track the global Retry count when current group started.
//     使用 ContextKeyAutoGroupRetryIndex 跟踪当前分组开始时的全局重试次数。
//
//   - priorityRetry = Retry - startRetryIndex, represents the priority level within current group.
//     priorityRetry = Retry - startRetryIndex，表示当前分组内的优先级级别。
//
//   - When GetRandomSatisfiedChannel returns nil (priorities exhausted), moves to next group.
//     当 GetRandomSatisfiedChannel 返回 nil（优先级用完）时，切换到下一个分组。
//
// Example flow (2 groups, each with 2 priorities, RetryTimes=3):
// 示例流程（2个分组，每个有2个优先级，RetryTimes=3）：
//
//	Retry=0: GroupA, priority0 (startRetryIndex=0, priorityRetry=0)
//	         分组A, 优先级0
//
//	Retry=1: GroupA, priority1 (startRetryIndex=0, priorityRetry=1)
//	         分组A, 优先级1
//
//	Retry=2: GroupA exhausted → GroupB, priority0 (startRetryIndex=2, priorityRetry=0)
//	         分组A用完 → 分组B, 优先级0
//
//	Retry=3: GroupB, priority1 (startRetryIndex=2, priorityRetry=1)
//	         分组B, 优先级1
func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	smartSetting := smart_routing_setting.GetSetting()
	effectiveMode := smart_routing_setting.ResolveEffectiveMode(smartSetting)
	if effectiveMode.AllowsEnterpriseFeatures() {
		if channel, selectGroup, handled, err := cacheGetChannelRoutingCanary(param, smartSetting); handled {
			return channel, selectGroup, err
		}
	}
	if effectiveMode.AllowsBalancedDataPlane() {
		if channel, selectGroup, handled, err := cacheGetChannelRoutingBalanced(param, smartSetting); handled {
			return channel, selectGroup, err
		}
	}
	if shouldActivateSmartRouting(smartSetting) {
		channel, selectGroup, handled, err := cacheGetSmartSatisfiedChannel(param, smartSetting)
		if err != nil {
			return channel, selectGroup, err
		}
		if handled {
			return channel, selectGroup, nil
		}
	}
	if shouldObserveSmartRouting(smartSetting) {
		retryIndex := param.GetRetry()
		channel, group, err := cacheGetRandomSatisfiedChannelLegacy(param)
		if err == nil {
			RecordChannelRoutingObserveSelection(param, group, channel, retryIndex)
		}
		return channel, group, err
	}
	return cacheGetRandomSatisfiedChannelLegacy(param)
}

func RecordChannelRoutingObserveSelection(param *RetryParam, actualGroup string, actual *model.Channel, retryIndex int) {
	setting := smart_routing_setting.GetSetting()
	effectiveMode := smart_routing_setting.ResolveEffectiveMode(setting)
	if param == nil || !common.MemoryCacheEnabled ||
		(!effectiveMode.RecordsObserveDecision() && !effectiveMode.RecordsShadowDecision()) {
		return
	}
	if param.Ctx != nil {
		common.SetContextKey(param.Ctx, constant.ContextKeyRoutingObserveDecision, nil)
	}
	if effectiveMode.RecordsShadowDecision() && recordChannelRoutingShadowAudit(param, actualGroup, actual, retryIndex) {
		return
	}
	if actual != nil && actualGroup != "" && actualGroup != "auto" {
		_, _ = selectSmartChannelForGroup(param, actualGroup, setting, false)
	}
	recordChannelRoutingObserveAudit(param, actualGroup, actual, retryIndex)
}

func recordChannelRoutingShadowAudit(param *RetryParam, actualGroup string, actual *model.Channel, retryIndex int) bool {
	if param == nil || param.Ctx == nil || actualGroup == "" || actualGroup == "auto" {
		return false
	}
	promptTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0)
	completionTokens := max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0)
	profile, profileErr := routingRequestProfile(param.Ctx, actualGroup, retryIndex, promptTokens, completionTokens)
	if profileErr != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing request profile dropped: %v", profileErr))
		return true
	}
	replayInput, active, err := channelrouting.CaptureShadowReplayRequest(channelrouting.ShadowRequest{
		RequestID:               common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		RequestPath:             param.RequestPath,
		GroupName:               actualGroup,
		ModelName:               param.ModelName,
		IsStream:                common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		RetryIndex:              retryIndex,
		PromptTokenEstimate:     promptTokens,
		CompletionTokenEstimate: completionTokens,
		CostProfile:             routingCostRequestProfile(param.Ctx),
		Profile:                 profile,
	})
	if !active {
		return false
	}
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing shadow capture dropped: %v", err))
		return true
	}
	replay, err := channelrouting.RunShadowReplay(replayInput)
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing shadow replay dropped: %v", err))
		return true
	}
	actualChannelID := 0
	if actual != nil {
		actualChannelID = actual.Id
	}
	actualCost, actualCostKnown := channelrouting.ShadowExpectedCostForChannel(replayInput, actualChannelID)
	actualCostEstimate, _ := channelrouting.ShadowCostEstimateForChannel(replayInput, actualChannelID)
	observedCostEstimate, _ := channelrouting.ShadowCostEstimateForChannel(replayInput, replay.SelectedChannelID)
	_, err = channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               replayInput.PoolID,
		GroupName:            actualGroup,
		ModelName:            param.ModelName,
		SnapshotRevision:     replayInput.PolicyRevision,
		AlgorithmVersion:     replayInput.AlgorithmVersion,
		RetryIndex:           retryIndex,
		IsStream:             replayInput.Profile.IsStream,
		ActualChannelID:      actualChannelID,
		ObservedChannelID:    replay.SelectedChannelID,
		FilteredOpen:         replay.FilteredOpen,
		FilteredCapacity:     replay.FilteredCapacity,
		BreakerBypassed:      replay.BreakerBypassed,
		Candidates:           replay.Candidates,
		ReplayInput:          &replayInput,
		DifferenceType:       channelrouting.ClassifyShadowDifference(actualChannelID, replay),
		ActualCostKnown:      actualCostKnown,
		ActualExpectedCost:   actualCost,
		ObservedCostKnown:    replay.SelectedCostKnown,
		ObservedExpectedCost: replay.SelectedCost,
		ActualCostEstimate:   actualCostEstimate,
		ObservedCostEstimate: observedCostEstimate,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing shadow audit dropped: %v", err))
	}
	return true
}

func cacheGetRandomSatisfiedChannelLegacy(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	if param.TokenGroup == "auto" {
		if len(setting.GetAutoGroups()) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
		}
		autoGroups := GetUserAutoGroup(userGroup)

		// startGroupIndex: the group index to start searching from
		// startGroupIndex: 开始搜索的分组索引
		startGroupIndex := 0
		crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)

		if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
			if idx, ok := lastGroupIndex.(int); ok {
				startGroupIndex = idx
			}
		}

		for i := startGroupIndex; i < len(autoGroups); i++ {
			autoGroup := autoGroups[i]
			// Calculate priorityRetry for current group
			// 计算当前分组的 priorityRetry
			priorityRetry := autoGroupPriorityRetry(param.Ctx, param.GetRetry())
			// If moved to a new group, reset priorityRetry and update startRetryIndex
			// 如果切换到新分组，重置 priorityRetry 并更新 startRetryIndex
			if i > startGroupIndex {
				beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
				priorityRetry = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

			channel, _ = getRandomSatisfiedChannelForRequest(param, autoGroup, priorityRetry)
			if channel == nil {
				// Current group has no available channel for this model, try next group
				// 当前分组没有该模型的可用渠道，尝试下一个分组
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d, trying next group", autoGroup, param.ModelName, priorityRetry)
				// 重置状态以尝试下一个分组
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
				continue
			}
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)

			// Prepare state for next retry
			// 为下一次重试准备状态
			if crossGroupRetry && priorityRetry >= common.RetryTimes {
				// Current group has exhausted all retries, prepare to switch to next group
				// This request still uses current group, but next retry will use next group
				// 当前分组已用完所有重试次数，准备切换到下一个分组
				// 本次请求仍使用当前分组，但下次重试将使用下一个分组
				logger.LogDebug(param.Ctx, "Current group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group for next retry", autoGroup, priorityRetry, common.RetryTimes)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				beginAutoGroupAtRetry(param.Ctx, param.GetRetry()+1)
			} else {
				// Stay in current group, save current state
				// 保持在当前分组，保存当前状态
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			}
			break
		}
	} else {
		channel, err = getRandomSatisfiedChannelForRequest(param, param.TokenGroup, param.GetRetry())
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

func shouldActivateSmartRouting(setting smart_routing_setting.SmartRoutingSetting) bool {
	if !common.MemoryCacheEnabled {
		return false
	}
	return smart_routing_setting.ResolveEffectiveMode(setting).AllowsBalancedDataPlane()
}

func shouldObserveSmartRouting(setting smart_routing_setting.SmartRoutingSetting) bool {
	if !common.MemoryCacheEnabled {
		return false
	}
	mode := smart_routing_setting.ResolveEffectiveMode(setting)
	return mode.RecordsObserveDecision() || mode.RecordsShadowDecision()
}

func recordSmartRoutingDecision(param *RetryParam, setting smart_routing_setting.SmartRoutingSetting) {
	if param == nil {
		return
	}
	if param.Ctx != nil {
		common.SetContextKey(param.Ctx, constant.ContextKeyRoutingObserveDecision, nil)
	}
	if param.TokenGroup == "auto" {
		userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
		for _, group := range GetUserAutoGroup(userGroup) {
			if channel, _ := selectSmartChannelForGroup(param, group, setting, false); channel != nil {
				return
			}
		}
		return
	}
	_, _ = selectSmartChannelForGroup(param, param.TokenGroup, setting, false)
}

func cacheGetSmartSatisfiedChannel(param *RetryParam, smartSetting smart_routing_setting.SmartRoutingSetting) (*model.Channel, string, bool, error) {
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	if param.TokenGroup != "auto" {
		channel, err := selectSmartChannelForGroup(param, param.TokenGroup, smartSetting, true)
		return channel, selectGroup, true, err
	}

	if len(setting.GetAutoGroups()) == 0 {
		return nil, selectGroup, true, errors.New("auto groups is not enabled")
	}
	autoGroups := GetUserAutoGroup(userGroup)
	startGroupIndex := 0
	crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)
	if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex); exists {
		if idx, ok := lastGroupIndex.(int); ok {
			startGroupIndex = idx
		}
	}

	for i := startGroupIndex; i < len(autoGroups); i++ {
		autoGroup := autoGroups[i]
		priorityRetry := autoGroupPriorityRetry(param.Ctx, param.GetRetry())
		if i > startGroupIndex {
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
			priorityRetry = 0
		}
		logger.LogDebug(param.Ctx, "Channel routing auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

		channel, err := selectSmartChannelForGroup(param, autoGroup, smartSetting, true)
		if err != nil {
			return nil, autoGroup, true, err
		}
		if channel == nil {
			logger.LogDebug(param.Ctx, "Channel routing found no available channel in group %s for model %s, trying next group", autoGroup, param.ModelName)
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex, i+1)
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry())
			continue
		}

		common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
		selectGroup = autoGroup
		if crossGroupRetry && priorityRetry >= common.RetryTimes {
			logger.LogDebug(param.Ctx, "Channel routing group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group", autoGroup, priorityRetry, common.RetryTimes)
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex, i+1)
			beginAutoGroupAtRetry(param.Ctx, param.GetRetry()+1)
		} else {
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex, i)
		}
		return channel, selectGroup, true, nil
	}

	return nil, selectGroup, true, nil
}

func selectSmartChannelForGroup(param *RetryParam, group string, setting smart_routing_setting.SmartRoutingSetting, reserveHalfOpen bool) (*model.Channel, error) {
	candidates, err := smartRoutingCandidatesForGroup(param, group)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}

	filtered := filterSmartRoutingExcludedCandidates(
		param.Ctx, group, candidates, smartRoutingSwitchCount(param.Ctx), setting.MaxSwitches,
	)
	if len(filtered) == 0 {
		return nil, nil
	}

	selectorSettings := routingSelectorSettings(setting, param.Ctx)
	for len(filtered) > 0 {
		decision := routingselector.SelectRankedFromCandidates(filtered, selectorSettings)
		if param.Ctx != nil {
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingLastDecision, decision)
			if !reserveHalfOpen {
				common.SetContextKey(param.Ctx, constant.ContextKeyRoutingObserveDecision, smartRoutingObserveEvaluation{
					Group:      group,
					Candidates: append([]routingselector.Candidate(nil), filtered...),
					Decision:   decision,
				})
			}
		}
		if decision.Selected == nil || decision.Selected.Channel == nil {
			return nil, nil
		}
		if !reserveHalfOpen || acquireRoutingHalfOpenProbe(
			param.Ctx, decision.Selected, param.ModelName, group, selectorSettings,
		) {
			return decision.Selected.Channel, nil
		}
		selectedID := decision.Selected.Channel.Id
		next := filtered[:0]
		for _, candidate := range filtered {
			if candidate.Channel == nil || candidate.Channel.Id != selectedID {
				next = append(next, candidate)
			}
		}
		filtered = next
	}
	return nil, nil
}

type smartRoutingObserveEvaluation struct {
	Group      string
	Candidates []routingselector.Candidate
	Decision   routingselector.Decision
}

func recordChannelRoutingObserveAudit(param *RetryParam, actualGroup string, actual *model.Channel, retryIndex int) {
	if param == nil || param.Ctx == nil {
		return
	}
	evaluation, ok := common.GetContextKeyType[smartRoutingObserveEvaluation](param.Ctx, constant.ContextKeyRoutingObserveDecision)
	if !ok || evaluation.Group == "" || evaluation.Group != actualGroup {
		return
	}
	channelIDs := make([]int, 0, len(evaluation.Candidates)+2)
	for _, candidate := range evaluation.Candidates {
		if candidate.Channel != nil {
			channelIDs = append(channelIDs, candidate.Channel.Id)
		}
	}
	if actual != nil {
		channelIDs = append(channelIDs, actual.Id)
	}
	if evaluation.Decision.Selected != nil && evaluation.Decision.Selected.Channel != nil {
		channelIDs = append(channelIDs, evaluation.Decision.Selected.Channel.Id)
	}
	identitySnapshot, identityKnown := channelrouting.ResolveDecisionIdentities(evaluation.Group, channelIDs)
	if !identityKnown {
		_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
			GroupName: evaluation.Group, ModelName: param.ModelName,
		})
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing observe audit dropped: %v", err))
		return
	}

	rankedByChannel := make(map[int]routingselector.RankedCandidate, len(evaluation.Decision.Ranked))
	for _, ranked := range evaluation.Decision.Ranked {
		if ranked.Channel != nil {
			rankedByChannel[ranked.Channel.Id] = ranked
		}
	}
	candidates := make([]channelrouting.DecisionCandidate, 0, len(evaluation.Candidates))
	nowUnix := time.Now().Unix()
	nowUnixMilli := time.Now().UnixMilli()
	for _, candidate := range evaluation.Candidates {
		if candidate.Channel == nil {
			continue
		}
		channelID := candidate.Channel.Id
		memberID := identitySnapshot.MemberIDs[channelID]
		if memberID <= 0 {
			_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
				GroupName: evaluation.Group, ModelName: param.ModelName,
			})
			logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing observe audit dropped: %v", err))
			return
		}
		view := channelrouting.DecisionCandidate{
			PoolMemberID: memberID,
			ChannelID:    channelID,
		}
		if ranked, eligible := rankedByChannel[channelID]; eligible {
			view.Eligible = true
			view.Score = ranked.Score
			view.Availability = ranked.Availability
			view.Latency = ranked.Latency
			view.Throughput = ranked.Throughput
			view.CostScore = ranked.CostScore
			view.CostKnown = ranked.CostKnown
			view.Degraded = ranked.Degraded
			view.Open = ranked.Open
			view.Inflight = ranked.Inflight
		} else {
			view.ExclusionReason = routingObserveExclusionReason(candidate, nowUnix, nowUnixMilli)
		}
		candidates = append(candidates, view)
	}

	actualChannelID := 0
	if actual != nil {
		actualChannelID = actual.Id
		if identitySnapshot.MemberIDs[actualChannelID] <= 0 {
			_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
				GroupName: evaluation.Group, ModelName: param.ModelName,
			})
			logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing observe audit dropped: %v", err))
			return
		}
	}
	observedChannelID := 0
	if evaluation.Decision.Selected != nil && evaluation.Decision.Selected.Channel != nil {
		observedChannelID = evaluation.Decision.Selected.Channel.Id
	}
	_, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:         common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:            identitySnapshot.PoolID,
		GroupName:         evaluation.Group,
		ModelName:         param.ModelName,
		SnapshotRevision:  identitySnapshot.SnapshotRevision,
		AlgorithmVersion:  channelrouting.DecisionAlgorithmLegacy,
		RetryIndex:        retryIndex,
		IsStream:          common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		ActualChannelID:   actualChannelID,
		ObservedChannelID: observedChannelID,
		FilteredOpen:      evaluation.Decision.FilteredOpen,
		FilteredCapacity:  evaluation.Decision.FilteredCapacity,
		BreakerBypassed:   evaluation.Decision.BreakerBypassed,
		Candidates:        candidates,
	})
	if err != nil {
		logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing observe audit dropped: %v", err))
	}
}

func routingObserveExclusionReason(candidate routingselector.Candidate, nowUnix int64, nowUnixMilli int64) string {
	if candidate.Capacity != nil && candidate.Capacity.CooldownUntilUnixMilli > nowUnixMilli {
		return "capacity_cooldown"
	}
	if candidate.Breaker != nil {
		state := strings.ToLower(strings.TrimSpace(candidate.Breaker.State))
		if candidate.Breaker.Reason == routingselector.BreakerReasonAuthFail {
			return "credential_unavailable"
		}
		if candidate.Breaker.Reason == routingselector.BreakerReasonBalance {
			return "balance_unavailable"
		}
		if state == routingselector.BreakerStateOpen && (candidate.Breaker.CooldownUntilUnix == 0 || candidate.Breaker.CooldownUntilUnix > nowUnix) {
			return "reliability_breaker_open"
		}
	}
	return "selector_filtered"
}

func acquireRoutingHalfOpenProbe(
	c *gin.Context,
	candidate *routingselector.RankedCandidate,
	modelName string,
	group string,
	settings routingselector.Settings,
) bool {
	if candidate == nil || candidate.Channel == nil || candidate.Candidate.Breaker == nil {
		return true
	}
	key := routingbreaker.Key{
		ChannelID:   candidate.Channel.Id,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       modelName,
		Group:       group,
	}
	acquired, _ := acquireRoutingHalfOpenProbeForKey(
		c,
		key,
		routingHalfOpenProbeOwner{ChannelID: candidate.Channel.Id, Model: modelName, Group: group},
		candidate.Candidate.Breaker,
		settings,
		false,
	)
	return acquired
}

var errRoutingHalfOpenProbeCoordinator = errors.New("routing half-open probe coordinator unavailable")

func acquireRoutingHalfOpenProbeForKey(
	c *gin.Context,
	key routingbreaker.Key,
	probeOwner routingHalfOpenProbeOwner,
	breaker *routingselector.BreakerSnapshot,
	settings routingselector.Settings,
	failClosedOnRedisError bool,
) (bool, error) {
	if !routingselector.BreakerNeedsHalfOpenProbe(breaker, settings) {
		return true, nil
	}
	if routingHalfOpenProbeHeld(c, key) {
		return true, nil
	}
	redisKey, redisOwner, redisAcquired, redisErr := acquireRoutingHalfOpenRedisLease(key)
	if redisErr != nil {
		if failClosedOnRedisError {
			return false, fmt.Errorf("%w: %v", errRoutingHalfOpenProbeCoordinator, redisErr)
		}
		redisKey = ""
		redisOwner = ""
	}
	if redisErr == nil && redisKey != "" && !redisAcquired {
		return false, nil
	}
	_, ok := routingbreaker.AcquireDefaultHalfOpenProbe(key, settings.HalfOpenProbes)
	if !ok {
		releaseRoutingHalfOpenRedisLease(redisKey, redisOwner)
	}
	if ok && c != nil {
		probes, _ := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
		if probes == nil {
			probes = map[routingbreaker.Key]struct{}{}
		}
		probes[key] = struct{}{}
		common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbes, probes)
		owners, _ := common.GetContextKeyType[map[routingbreaker.Key]routingHalfOpenProbeOwner](c, constant.ContextKeyRoutingHalfOpenProbeOwners)
		if owners == nil {
			owners = map[routingbreaker.Key]routingHalfOpenProbeOwner{}
		}
		owners[key] = probeOwner
		common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbeOwners, owners)
		if redisAcquired {
			leases, _ := common.GetContextKeyType[map[routingbreaker.Key]*routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
			if leases == nil {
				leases = map[routingbreaker.Key]*routingHalfOpenRedisLease{}
			}
			leases[key] = startRoutingHalfOpenRedisLeaseRenewal(c, redisKey, redisOwner)
			common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, leases)
		}
	}
	return ok, nil
}

func routingHalfOpenProbeHeld(c *gin.Context, key routingbreaker.Key) bool {
	if c == nil {
		return false
	}
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	if !ok {
		return false
	}
	_, held := probes[key]
	return held
}

type routingHalfOpenProbeOwner struct {
	ChannelID int
	Model     string
	Group     string
}

func (owner routingHalfOpenProbeOwner) matches(channelID int, modelName string, group string) bool {
	return owner.ChannelID == channelID &&
		(modelName == "" || owner.Model == modelName) &&
		(group == "" || owner.Group == group)
}

const (
	routingHalfOpenRedisLeaseTTL       = 30 * time.Second
	routingHalfOpenRedisRenewInterval  = 10 * time.Second
	routingHalfOpenRedisCommandTimeout = 200 * time.Millisecond
)

type routingHalfOpenRedisLease struct {
	Key      string
	Owner    string
	cancel   context.CancelFunc
	done     <-chan struct{}
	stopOnce sync.Once
}

func acquireRoutingHalfOpenRedisLease(key routingbreaker.Key) (string, string, bool, error) {
	if !common.RedisEnabled {
		return "", "", false, nil
	}
	if common.RDB == nil {
		return "", "", false, errors.New("routing half-open Redis client is unavailable")
	}
	redisKey := fmt.Sprintf("routing:halfopen:%d:%d:%s:%s", key.ChannelID, key.APIKeyIndex, key.Model, key.Group)
	if key.IsEndpointScoped() {
		digest := sha256.Sum256([]byte(string(key.Scope) + "\x00" + key.EndpointAuthority + "\x00" + key.Region))
		redisKey = fmt.Sprintf("routing:halfopen:endpoint:%x", digest[:])
	}
	owner := common.GetRandomString(32)
	ctx, cancel := context.WithTimeout(context.Background(), routingHalfOpenRedisCommandTimeout)
	defer cancel()
	acquired, err := common.RDB.SetNX(ctx, redisKey, owner, routingHalfOpenRedisLeaseTTL).Result()
	if err != nil {
		return redisKey, owner, false, err
	}
	return redisKey, owner, acquired, nil
}

func startRoutingHalfOpenRedisLeaseRenewal(c *gin.Context, redisKey string, owner string) *routingHalfOpenRedisLease {
	parent := context.Background()
	if c != nil && c.Request != nil {
		parent = c.Request.Context()
	}
	renewContext, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	lease := &routingHalfOpenRedisLease{Key: redisKey, Owner: owner, cancel: cancel, done: done}
	go func() {
		defer close(done)
		ticker := time.NewTicker(routingHalfOpenRedisRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewContext.Done():
				return
			case <-ticker.C:
				renewed, err := renewRoutingHalfOpenRedisLease(redisKey, owner)
				if err != nil {
					logger.LogWarn(parent, fmt.Sprintf("channel routing half-open Redis lease renewal failed: %v", err))
					continue
				}
				if !renewed {
					logger.LogWarn(parent, "channel routing half-open Redis lease ownership was lost")
					return
				}
			}
		}
	}()
	return lease
}

func renewRoutingHalfOpenRedisLease(redisKey string, owner string) (bool, error) {
	if redisKey == "" || owner == "" || !common.RedisEnabled || common.RDB == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), routingHalfOpenRedisCommandTimeout)
	defer cancel()
	result, err := common.RDB.Eval(
		ctx,
		`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) else return 0 end`,
		[]string{redisKey},
		owner,
		routingHalfOpenRedisLeaseTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func releaseRoutingHalfOpenRedisLease(redisKey string, owner string) {
	if redisKey == "" || owner == "" || !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), routingHalfOpenRedisCommandTimeout)
	defer cancel()
	_ = common.RDB.Eval(ctx, `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`, []string{redisKey}, owner).Err()
}

func (lease *routingHalfOpenRedisLease) stop() {
	if lease == nil {
		return
	}
	lease.stopOnce.Do(func() {
		if lease.cancel != nil {
			lease.cancel()
		}
		if lease.done != nil {
			<-lease.done
		}
		releaseRoutingHalfOpenRedisLease(lease.Key, lease.Owner)
	})
}

func ReleaseRoutingHalfOpenProbe(c *gin.Context, channelID int, infoModel string, group string) {
	if channelID <= 0 || c == nil {
		return
	}
	clearRoutingSlowStartProbe(c, channelID)
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	if !ok {
		return
	}
	owners, _ := common.GetContextKeyType[map[routingbreaker.Key]routingHalfOpenProbeOwner](c, constant.ContextKeyRoutingHalfOpenProbeOwners)
	leases, _ := common.GetContextKeyType[map[routingbreaker.Key]*routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
	released := false
	for key := range probes {
		if owner, exists := owners[key]; exists {
			if !owner.matches(channelID, infoModel, group) {
				continue
			}
		} else if key.IsEndpointScoped() || key.ChannelID != channelID ||
			(infoModel != "" && key.Model != infoModel) || (group != "" && key.Group != group) {
			continue
		}
		delete(probes, key)
		delete(owners, key)
		if lease, exists := leases[key]; exists {
			lease.stop()
			delete(leases, key)
		}
		routingbreaker.ReleaseDefaultHalfOpenProbe(key)
		released = true
	}
	if !released {
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbes, probes)
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbeOwners, owners)
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, leases)
}

func ReleaseAllRoutingHalfOpenProbes(c *gin.Context) {
	if c == nil {
		return
	}
	clearRoutingSlowStartProbe(c, 0)
	probes, _ := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	leases, _ := common.GetContextKeyType[map[routingbreaker.Key]*routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
	if len(probes) == 0 && len(leases) == 0 {
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbes, map[routingbreaker.Key]struct{}{})
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbeOwners, map[routingbreaker.Key]routingHalfOpenProbeOwner{})
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, map[routingbreaker.Key]*routingHalfOpenRedisLease{})
	for key := range probes {
		routingbreaker.ReleaseDefaultHalfOpenProbe(key)
	}
	for _, lease := range leases {
		lease.stop()
	}
}

type smartRoutingCandidateMemo map[string][]routingselector.Candidate

func smartRoutingCandidatesForGroup(param *RetryParam, group string) ([]routingselector.Candidate, error) {
	if param == nil {
		return nil, errors.New("routing param is nil")
	}
	memoKey := smartRoutingMemoKey(group, param.ModelName, param.RequestPath)
	if param.Ctx != nil {
		if memo, ok := common.GetContextKeyType[smartRoutingCandidateMemo](param.Ctx, constant.ContextKeyRoutingCandidateMemo); ok {
			if candidates, exists := memo[memoKey]; exists {
				refreshed := append([]routingselector.Candidate(nil), candidates...)
				for i := range refreshed {
					refreshed[i].Capacity = nil
					if refreshed[i].Channel == nil {
						continue
					}
					cacheKey := routinghotcache.Key{
						ChannelID:   refreshed[i].Channel.Id,
						APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						Model:       param.ModelName,
						Group:       group,
					}
					refreshed[i].Capacity = routingCapacityForKey(
						cacheKey,
						refreshed[i].Channel.ChannelInfo.IsMultiKey,
					)
				}
				return refreshed, nil
			}
		}
	}

	channels, err := model.GetRankedSatisfiedChannels(group, param.ModelName, param.RequestPath)
	if err != nil || len(channels) == 0 {
		return nil, err
	}
	routingSetting := smart_routing_setting.GetSetting()
	useObserveSnapshot := shouldObserveSmartRouting(routingSetting)
	candidates := make([]routingselector.Candidate, 0, len(channels))
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		trafficAdmissible, trafficErr := ChannelRoutingTrafficAdmissible(param.Ctx, channel.Id)
		if trafficErr != nil {
			return nil, trafficErr
		}
		if !trafficAdmissible {
			continue
		}
		cacheKey := routinghotcache.Key{
			ChannelID:   channel.Id,
			APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model:       param.ModelName,
			Group:       group,
		}
		candidate := routingselector.Candidate{Channel: channel}
		candidate.Capacity = routingCapacityForKey(cacheKey, channel.ChannelInfo.IsMultiKey)
		observation, _, observationKnown := channelrouting.ResolveObserveModelSnapshot(group, channel.Id, param.ModelName)
		if observationKnown {
			profile := routingCostRequestProfile(param.Ctx)
			if profile == nil {
				profile = &model.RoutingCostRequestProfile{MaxAttempts: 1}
			}
			estimate, exists, estimateErr := channelrouting.EstimateModelSnapshotRoutingCost(
				observation, *profile, time.Now().Unix(),
			)
			if estimateErr != nil {
				logger.LogWarn(param.Ctx, fmt.Sprintf("channel routing system cost unavailable for channel %d: %v", channel.Id, estimateErr))
			} else if exists {
				candidate.Cost = &routingselector.CostSnapshot{
					Known: estimate.ExpectedKnown, Cost: estimate.ExpectedCost, UpdatedUnix: 0,
				}
			}
		}
		if useObserveSnapshot && observationKnown && observation.MetricKnown {
			candidate.Metric = &routingselector.MetricSnapshot{
				RequestCount:            observation.RequestCount,
				SuccessCount:            observation.SuccessCount,
				ReliabilityRequestCount: observation.ReliabilityRequestCount,
				ReliabilityFailureCount: observation.ReliabilityFailureCount,
				P95LatencyMs:            observation.P95LatencyMs,
				P95TTFTMs:               observation.P95TTFTMs,
				TPS:                     observation.OutputTokensPerSecond,
			}
		}
		if !channel.ChannelInfo.IsMultiKey {
			if candidate.Metric == nil {
				if metric, ok := routinghotcache.GetMetric(cacheKey); ok {
					candidate.Metric = &routingselector.MetricSnapshot{
						RequestCount:            metric.RequestCount,
						SuccessCount:            metric.SuccessCount,
						ReliabilityRequestCount: metric.ReliabilityRequestCount,
						ReliabilityFailureCount: metric.ReliabilityFailureCount,
						P95LatencyMs:            metric.P95LatencyMs,
						P95TTFTMs:               metric.P95TTFTMs,
						TPS:                     metric.TPS,
					}
				}
			}
			inflight := routingmetrics.InflightCount(routingmetrics.InflightKey{
				ChannelID:   channel.Id,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex,
				Model:       param.ModelName,
				Group:       group,
			})
			if candidate.Metric != nil {
				candidate.Metric.Inflight = inflight
			} else if inflight > 0 {
				candidate.Metric = &routingselector.MetricSnapshot{Inflight: inflight}
			}
			if breaker, ok := routinghotcache.GetBreaker(cacheKey); ok {
				candidate.Breaker = &routingselector.BreakerSnapshot{
					State:             breaker.State,
					Reason:            breaker.Reason,
					CooldownUntilUnix: breaker.CooldownUntilUnix,
					HalfOpenInflight:  breaker.HalfOpenInflight,
					UpdatedUnix:       breaker.UpdatedUnix,
				}
			}
		}
		applyRoutingHealthMarkers(&candidate, channel.Id, routingSetting)
		candidates = append(candidates, candidate)
	}

	if param.Ctx != nil {
		memo, _ := common.GetContextKeyType[smartRoutingCandidateMemo](param.Ctx, constant.ContextKeyRoutingCandidateMemo)
		if memo == nil {
			memo = smartRoutingCandidateMemo{}
		}
		memo[memoKey] = candidates
		common.SetContextKey(param.Ctx, constant.ContextKeyRoutingCandidateMemo, memo)
	}
	return candidates, nil
}

func routingCapacityForKey(
	key routinghotcache.Key,
	multiKey bool,
) *routingselector.CapacityCooldownSnapshot {
	capacity := routinghotcache.CapacityCooldownSnapshot{}
	capacityKnown := false
	if !multiKey {
		capacity, capacityKnown = routinghotcache.GetCapacityCooldown(key)
	}
	channelCapacity, channelCapacityKnown := routinghotcache.GetChannelBalanceUnavailable(key.ChannelID)
	if !capacityKnown && !channelCapacityKnown {
		return nil
	}
	if channelCapacityKnown && (!capacityKnown ||
		channelCapacity.CooldownUntilUnixMilli > capacity.CooldownUntilUnixMilli ||
		(channelCapacity.CooldownUntilUnixMilli == capacity.CooldownUntilUnixMilli &&
			channelCapacity.UpdatedUnixMilli >= capacity.UpdatedUnixMilli)) {
		return &routingselector.CapacityCooldownSnapshot{
			SourceStatusCode:       channelCapacity.SourceStatusCode,
			CooldownUntilUnixMilli: channelCapacity.CooldownUntilUnixMilli,
			UpdatedUnixMilli:       channelCapacity.UpdatedUnixMilli,
		}
	}
	return &routingselector.CapacityCooldownSnapshot{
		SourceStatusCode:       capacity.SourceStatusCode,
		CooldownUntilUnixMilli: capacity.CooldownUntilUnixMilli,
		UpdatedUnixMilli:       capacity.UpdatedUnixMilli,
	}
}

func smartRoutingMemoKey(group string, modelName string, requestPath string) string {
	return group + "\x00" + modelName + "\x00" + requestPath
}

func routingSelectorSettings(setting smart_routing_setting.SmartRoutingSetting, c *gin.Context) routingselector.Settings {
	now := time.Now()
	return routingselector.Settings{
		WeightAvailability: setting.WeightAvailability,
		WeightLatency:      setting.WeightLatency,
		WeightThroughput:   setting.WeightThroughput,
		WeightCost:         setting.WeightCost,
		AvailabilityFloor:  setting.AvailabilityFloor,
		MinVolume:          setting.MinVolume,
		TopK:               setting.TopK,
		MaxEjectedPct:      setting.MaxEjectedPct,
		HalfOpenProbes:     setting.HalfOpenProbes,
		SnapshotStaleSec:   setting.SnapshotStaleSec,
		NowUnix:            now.Unix(),
		NowUnixMilli:       now.UnixMilli(),
		RandomSeed:         now.UnixNano(),
		PreferTTFT:         c != nil && common.GetContextKeyBool(c, constant.ContextKeyIsStream),
	}
}

func smartRoutingExcludedChannelIDs(c *gin.Context) map[int]struct{} {
	excluded := map[int]struct{}{}
	if c == nil {
		return excluded
	}
	if stored, ok := common.GetContextKey(c, constant.ContextKeyRoutingExcludedChannels); ok {
		switch typed := stored.(type) {
		case map[int]struct{}:
			for id := range typed {
				excluded[id] = struct{}{}
			}
		case []int:
			for _, id := range typed {
				if id > 0 {
					excluded[id] = struct{}{}
				}
			}
		}
	}
	return excluded
}

func smartRoutingExcludedCredentialIDs(c *gin.Context) map[int]struct{} {
	excluded := map[int]struct{}{}
	if c == nil {
		return excluded
	}
	if stored, ok := common.GetContextKey(c, constant.ContextKeyRoutingExcludedCredentials); ok {
		switch typed := stored.(type) {
		case map[int]struct{}:
			for id := range typed {
				if id > 0 {
					excluded[id] = struct{}{}
				}
			}
		case []int:
			for _, id := range typed {
				if id > 0 {
					excluded[id] = struct{}{}
				}
			}
		}
	}
	return excluded
}

func smartRoutingExcludedEndpointIdentities(c *gin.Context) map[string]channelrouting.RequestEndpointIdentity {
	excluded := map[string]channelrouting.RequestEndpointIdentity{}
	if c == nil {
		return excluded
	}
	if stored, ok := common.GetContextKey(c, constant.ContextKeyRoutingExcludedEndpoints); ok {
		if typed, valid := stored.(map[string]channelrouting.RequestEndpointIdentity); valid {
			for key, identity := range typed {
				if key != "" && identity.EndpointAuthority != "" && identity.Region != "" {
					excluded[key] = identity
				}
			}
		}
	}
	return excluded
}

func smartRoutingExcludedFailureDomainHashes(c *gin.Context) map[string]struct{} {
	excluded := map[string]struct{}{}
	if c == nil {
		return excluded
	}
	if stored, ok := common.GetContextKey(c, constant.ContextKeyRoutingExcludedDomains); ok {
		if typed, valid := stored.(map[string]struct{}); valid {
			for hash := range typed {
				hash = strings.ToLower(strings.TrimSpace(hash))
				if hash != "" {
					excluded[hash] = struct{}{}
				}
			}
		}
	}
	return excluded
}

func smartRoutingExcludedEndpointList(c *gin.Context) []channelrouting.RequestEndpointIdentity {
	set := smartRoutingExcludedEndpointIdentities(c)
	result := make([]channelrouting.RequestEndpointIdentity, 0, len(set))
	for _, identity := range set {
		result = append(result, identity)
	}
	return result
}

func smartRoutingExcludedFailureDomainList(c *gin.Context) []string {
	set := smartRoutingExcludedFailureDomainHashes(c)
	result := make([]string, 0, len(set))
	for hash := range set {
		result = append(result, hash)
	}
	return result
}

func routingRequestEndpointIdentityKey(endpointAuthority string, region string) string {
	endpointAuthority = strings.ToLower(strings.TrimSpace(endpointAuthority))
	region = strings.ToLower(strings.TrimSpace(region))
	if endpointAuthority == "" || region == "" {
		return ""
	}
	return endpointAuthority + "\x00" + region
}

func smartRoutingSwitchCount(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return common.GetContextKeyInt(c, constant.ContextKeyRoutingSwitchCount)
}

func MarkRoutingTried(c *gin.Context, channelID int) int {
	if c == nil || channelID <= 0 {
		return 0
	}
	excluded := smartRoutingExcludedChannelIDs(c)
	excluded[channelID] = struct{}{}
	switchCount := len(excluded) - 1
	if switchCount < 0 {
		switchCount = 0
	}
	common.SetContextKey(c, constant.ContextKeyRoutingExcludedChannels, excluded)
	common.SetContextKey(c, constant.ContextKeyRoutingSwitchCount, switchCount)
	return switchCount
}

func MarkRoutingTargetTried(c *gin.Context, channelID int, credentialID int, multiKey bool) int {
	if c == nil || channelID <= 0 {
		return 0
	}
	var switchCount int
	if !multiKey || credentialID <= 0 {
		switchCount = MarkRoutingTried(c, channelID)
	} else {
		excluded := smartRoutingExcludedCredentialIDs(c)
		excluded[credentialID] = struct{}{}
		common.SetContextKey(c, constant.ContextKeyRoutingExcludedCredentials, excluded)
		switchCount = len(smartRoutingExcludedChannelIDs(c)) + len(excluded) - 1
		if switchCount < 0 {
			switchCount = 0
		}
		common.SetContextKey(c, constant.ContextKeyRoutingSwitchCount, switchCount)
	}
	return switchCount
}

func MarkRoutingTargetFailure(c *gin.Context, channelID int, scope routingerror.Scope) int {
	if c == nil || channelID <= 0 {
		return 0
	}
	switch scope {
	case routingerror.ScopeEndpoint:
		endpointAuthority := common.GetContextKeyString(c, constant.ContextKeyRoutingEndpointAuthority)
		region := common.GetContextKeyString(c, constant.ContextKeyRoutingRegion)
		if endpointKey := routingRequestEndpointIdentityKey(endpointAuthority, region); endpointKey != "" {
			excludedEndpoints := smartRoutingExcludedEndpointIdentities(c)
			excludedEndpoints[endpointKey] = channelrouting.RequestEndpointIdentity{
				EndpointAuthority: endpointAuthority,
				Region:            region,
			}
			common.SetContextKey(c, constant.ContextKeyRoutingExcludedEndpoints, excludedEndpoints)
		}
	case routingerror.ScopeAccount:
		failureDomainHash := strings.ToLower(strings.TrimSpace(
			common.GetContextKeyString(c, constant.ContextKeyRoutingFailureDomainHash),
		))
		if failureDomainHash == "" {
			return MarkRoutingTried(c, channelID)
		}
		excludedFailureDomains := smartRoutingExcludedFailureDomainHashes(c)
		excludedFailureDomains[failureDomainHash] = struct{}{}
		common.SetContextKey(c, constant.ContextKeyRoutingExcludedDomains, excludedFailureDomains)
	case routingerror.ScopeChannel, routingerror.ScopePoolMember, routingerror.ScopeModel:
		return MarkRoutingTried(c, channelID)
	}
	return smartRoutingSwitchCount(c)
}

func MergeRoutingTargetExclusions(target *gin.Context, source *gin.Context) {
	if target == nil || source == nil {
		return
	}
	channels := smartRoutingExcludedChannelIDs(target)
	for channelID := range smartRoutingExcludedChannelIDs(source) {
		channels[channelID] = struct{}{}
	}
	common.SetContextKey(target, constant.ContextKeyRoutingExcludedChannels, channels)

	credentials := smartRoutingExcludedCredentialIDs(target)
	for credentialID := range smartRoutingExcludedCredentialIDs(source) {
		credentials[credentialID] = struct{}{}
	}
	common.SetContextKey(target, constant.ContextKeyRoutingExcludedCredentials, credentials)

	endpoints := smartRoutingExcludedEndpointIdentities(target)
	for key, identity := range smartRoutingExcludedEndpointIdentities(source) {
		endpoints[key] = identity
	}
	common.SetContextKey(target, constant.ContextKeyRoutingExcludedEndpoints, endpoints)

	failureDomains := smartRoutingExcludedFailureDomainHashes(target)
	for hash := range smartRoutingExcludedFailureDomainHashes(source) {
		failureDomains[hash] = struct{}{}
	}
	common.SetContextKey(target, constant.ContextKeyRoutingExcludedDomains, failureDomains)

	switchCount := len(channels) + len(credentials) - 1
	if switchCount < 0 {
		switchCount = 0
	}
	common.SetContextKey(target, constant.ContextKeyRoutingSwitchCount, switchCount)
}

func MarkRoutingChannelFailed(c *gin.Context, channelID int) int {
	return MarkRoutingTried(c, channelID)
}

func AffinityAdmissible(channelID int) bool {
	if channelID <= 0 {
		return true
	}
	if routinghotcache.ChannelBalanceUnavailableActive(channelID, time.Now()) {
		return false
	}
	return true
}

func GetAdmissibleAffinityChannel(c *gin.Context, preferredID int, modelName string, usingGroup string, requestPath string) (*model.Channel, string, bool) {
	if preferredID <= 0 || modelName == "" {
		return nil, "", false
	}
	smartSetting := smart_routing_setting.GetSetting()
	if shouldActivateSmartRouting(smartSetting) {
		if usingGroup == "auto" {
			userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
			for _, group := range GetUserAutoGroup(userGroup) {
				if channel, ok := admissibleAffinityChannelForSmartGroup(c, preferredID, modelName, group, requestPath, smartSetting); ok {
					common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
					return channel, group, true
				}
			}
			return nil, "", false
		}
		returnValue, ok := admissibleAffinityChannelForSmartGroup(c, preferredID, modelName, usingGroup, requestPath, smartSetting)
		if !ok {
			return nil, "", false
		}
		return returnValue, usingGroup, true
	}
	return admissibleAffinityChannelLegacy(c, preferredID, modelName, usingGroup, requestPath)
}

func admissibleAffinityChannelForSmartGroup(c *gin.Context, preferredID int, modelName string, group string, requestPath string, setting smart_routing_setting.SmartRoutingSetting) (*model.Channel, bool) {
	param := &RetryParam{
		Ctx:         c,
		TokenGroup:  group,
		ModelName:   modelName,
		RequestPath: requestPath,
		Retry:       common.GetPointer(0),
	}
	candidates, err := smartRoutingCandidatesForGroup(param, group)
	if err != nil || len(candidates) == 0 {
		return nil, false
	}
	filtered := filterSmartRoutingExcludedCandidates(c, group, candidates, smartRoutingSwitchCount(c), setting.MaxSwitches)
	if len(filtered) == 0 {
		return nil, false
	}
	selectorSettings := routingSelectorSettings(setting, c)
	decision := routingselector.RankCandidates(filtered, selectorSettings)
	if c != nil {
		common.SetContextKey(c, constant.ContextKeyRoutingLastDecision, decision)
	}
	for index := range decision.Ranked {
		ranked := &decision.Ranked[index]
		if ranked.Channel == nil || ranked.Channel.Id != preferredID {
			continue
		}
		if !acquireRoutingHalfOpenProbe(
			c, ranked, modelName, group, selectorSettings,
		) {
			return nil, false
		}
		return ranked.Channel, true
	}
	return nil, false
}

func admissibleAffinityChannelLegacy(c *gin.Context, preferredID int, modelName string, usingGroup string, requestPath string) (*model.Channel, string, bool) {
	preferred, err := model.CacheGetChannel(preferredID)
	setting := smart_routing_setting.GetSetting()
	if err != nil || preferred == nil || preferred.Status != common.ChannelStatusEnabled ||
		!channelSupportsSmartRoutingRequestPath(preferred, requestPath) || !AffinityAdmissible(preferred.Id) ||
		(smart_routing_setting.ResolveEffectiveMode(setting).AllowsAffinityRouting() &&
			routingChannelBalanceBelowMargin(preferred, setting)) {
		return nil, "", false
	}
	trafficAdmissible, trafficErr := ChannelRoutingTrafficAdmissible(c, preferred.Id)
	if trafficErr != nil || !trafficAdmissible {
		return nil, "", false
	}
	if usingGroup == "auto" {
		userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		for _, group := range GetUserAutoGroup(userGroup) {
			if model.IsChannelEnabledForGroupModel(group, modelName, preferred.Id) {
				common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
				return preferred, group, true
			}
		}
		return nil, "", false
	}
	if model.IsChannelEnabledForGroupModel(usingGroup, modelName, preferred.Id) {
		return preferred, usingGroup, true
	}
	return nil, "", false
}

func channelSupportsSmartRoutingRequestPath(channel *model.Channel, requestPath string) bool {
	if channel == nil {
		return false
	}
	if channel.Type != constant.ChannelTypeAdvancedCustom {
		return true
	}
	config := channel.GetOtherSettings().AdvancedCustom
	return config != nil && config.SupportsPath(requestPath)
}

func filterSmartRoutingExcludedCandidates(
	c *gin.Context,
	group string,
	candidates []routingselector.Candidate,
	switchCount int,
	maxSwitches int,
) []routingselector.Candidate {
	if len(candidates) == 0 {
		return candidates
	}
	excludedChannels := smartRoutingExcludedChannelIDs(c)
	excludedEndpoints := smartRoutingExcludedEndpointIdentities(c)
	excludedFailureDomains := smartRoutingExcludedFailureDomainHashes(c)
	if len(excludedChannels) == 0 && len(excludedEndpoints) == 0 && len(excludedFailureDomains) == 0 {
		return candidates
	}
	if maxSwitches >= 0 && switchCount >= maxSwitches {
		return nil
	}
	filtered := make([]routingselector.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Channel == nil {
			continue
		}
		if _, found := excludedChannels[candidate.Channel.Id]; found {
			continue
		}
		endpointKey := routingRequestEndpointIdentityKey(
			channelrouting.EndpointAuthority(candidate.Channel.GetBaseURL(), candidate.Channel.Id),
			channelrouting.RoutingRegion(),
		)
		if _, found := excludedEndpoints[endpointKey]; endpointKey != "" && found {
			continue
		}
		identity, _ := channelrouting.ResolveIdentity(group, candidate.Channel.Id, "")
		if _, found := excludedFailureDomains[strings.ToLower(strings.TrimSpace(identity.FailureDomainHash))]; found {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func routingChannelRequestIdentityAdmissible(c *gin.Context, group string, channel *model.Channel) bool {
	if channel == nil || channel.Id <= 0 {
		return false
	}
	if _, excluded := smartRoutingExcludedChannelIDs(c)[channel.Id]; excluded {
		return false
	}
	endpointKey := routingRequestEndpointIdentityKey(
		channelrouting.EndpointAuthority(channel.GetBaseURL(), channel.Id),
		channelrouting.RoutingRegion(),
	)
	if _, excluded := smartRoutingExcludedEndpointIdentities(c)[endpointKey]; endpointKey != "" && excluded {
		return false
	}
	identity, _ := channelrouting.ResolveIdentity(group, channel.Id, "")
	failureDomainHash := strings.ToLower(strings.TrimSpace(identity.FailureDomainHash))
	if _, excluded := smartRoutingExcludedFailureDomainHashes(c)[failureDomainHash]; failureDomainHash != "" && excluded {
		return false
	}
	return true
}

func applyRoutingHealthMarkers(candidate *routingselector.Candidate, channelID int, setting smart_routing_setting.SmartRoutingSetting) {
	if candidate == nil || candidate.Channel == nil || channelID <= 0 {
		return
	}
	if unavailable, ok := routinghotcache.GetChannelBalanceUnavailable(channelID); ok &&
		unavailable.CooldownUntilUnixMilli > time.Now().UnixMilli() {
		candidate.Breaker = &routingselector.BreakerSnapshot{
			State:             routingselector.BreakerStateOpen,
			Reason:            routingselector.BreakerReasonBalance,
			CooldownUntilUnix: unavailable.CooldownUntilUnixMilli / int64(time.Second/time.Millisecond),
			UpdatedUnix:       unavailable.UpdatedUnixMilli / int64(time.Second/time.Millisecond),
		}
		return
	}
	if routingChannelBalanceBelowMargin(candidate.Channel, setting) {
		candidate.Breaker = &routingselector.BreakerSnapshot{
			State:       routingselector.BreakerStateOpen,
			Reason:      routingselector.BreakerReasonBalance,
			UpdatedUnix: candidate.Channel.BalanceUpdatedTime,
		}
	}
}

func routingChannelBalanceBelowMargin(channel *model.Channel, setting smart_routing_setting.SmartRoutingSetting) bool {
	return channel != nil && channel.BalanceUpdatedTime > 0 &&
		!math.IsNaN(channel.Balance) && !math.IsInf(channel.Balance, 0) &&
		routingMarkerFresh(channel.BalanceUpdatedTime, setting) && channel.Balance < setting.BalanceMarginUSD
}

func routingMarkerFresh(updatedUnix int64, setting smart_routing_setting.SmartRoutingSetting) bool {
	if updatedUnix <= 0 {
		return false
	}
	if setting.SnapshotStaleSec <= 0 {
		return true
	}
	return common.GetTimestamp()-updatedUnix <= int64(setting.SnapshotStaleSec)
}
