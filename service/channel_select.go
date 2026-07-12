package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
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
	if channel, selectGroup, handled, err := cacheGetChannelRoutingCanary(param, smartSetting); handled {
		return channel, selectGroup, err
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
	if param == nil || !shouldObserveSmartRouting(setting) {
		return
	}
	if param.Ctx != nil {
		common.SetContextKey(param.Ctx, constant.ContextKeyRoutingObserveDecision, nil)
	}
	if recordChannelRoutingShadowAudit(param, actualGroup, actual, retryIndex) {
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
	replayInput, active, err := channelrouting.CaptureShadowReplayRequest(channelrouting.ShadowRequest{
		RequestID:               common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		RequestPath:             param.RequestPath,
		GroupName:               actualGroup,
		ModelName:               param.ModelName,
		IsStream:                common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream),
		RetryIndex:              retryIndex,
		PromptTokenEstimate:     max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingPromptProxy), 0),
		CompletionTokenEstimate: max(common.GetContextKeyInt(param.Ctx, constant.ContextKeyRoutingEstimatedOutput), 0),
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
	_, err = channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            common.GetContextKeyString(param.Ctx, common.RequestIdKey),
		PoolID:               replayInput.PoolID,
		GroupName:            actualGroup,
		ModelName:            param.ModelName,
		SnapshotRevision:     replayInput.PolicyRevision,
		AlgorithmVersion:     channelrouting.DecisionAlgorithmShadowV1,
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
			priorityRetry := param.GetRetry()
			// If moved to a new group, reset priorityRetry and update startRetryIndex
			// 如果切换到新分组，重置 priorityRetry 并更新 startRetryIndex
			if i > startGroupIndex {
				priorityRetry = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

			channel, _ = model.GetRandomSatisfiedChannel(autoGroup, param.ModelName, priorityRetry, param.RequestPath)
			if channel == nil {
				// Current group has no available channel for this model, try next group
				// 当前分组没有该模型的可用渠道，尝试下一个分组
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d, trying next group", autoGroup, param.ModelName, priorityRetry)
				// 重置状态以尝试下一个分组
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
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
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				param.ResetRetryNextTry()
			} else {
				// Stay in current group, save current state
				// 保持在当前分组，保存当前状态
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			}
			break
		}
	} else {
		channel, err = model.GetRandomSatisfiedChannel(param.TokenGroup, param.ModelName, param.GetRetry(), param.RequestPath)
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

func shouldActivateSmartRouting(setting smart_routing_setting.SmartRoutingSetting) bool {
	if !setting.Enabled {
		return false
	}
	if !common.MemoryCacheEnabled {
		return false
	}
	return setting.Mode == smart_routing_setting.ModeBalanced || setting.Mode == smart_routing_setting.ModeEnterpriseSLO
}

func shouldObserveSmartRouting(setting smart_routing_setting.SmartRoutingSetting) bool {
	if !setting.Enabled {
		return false
	}
	if !common.MemoryCacheEnabled {
		return false
	}
	return setting.Mode == smart_routing_setting.ModeObserve || setting.Mode == smart_routing_setting.ModeShadow
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
		priorityRetry := param.GetRetry()
		if i > startGroupIndex {
			priorityRetry = 0
		}
		logger.LogDebug(param.Ctx, "Smart routing auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)

		channel, err := selectSmartChannelForGroup(param, autoGroup, smartSetting, true)
		if err != nil {
			return nil, autoGroup, true, err
		}
		if channel == nil {
			logger.LogDebug(param.Ctx, "Smart routing found no available channel in group %s for model %s, trying next group", autoGroup, param.ModelName)
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex, i+1)
			param.SetRetry(0)
			continue
		}

		common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
		selectGroup = autoGroup
		if crossGroupRetry && priorityRetry >= common.RetryTimes {
			logger.LogDebug(param.Ctx, "Smart routing group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group", autoGroup, priorityRetry, common.RetryTimes)
			common.SetContextKey(param.Ctx, constant.ContextKeyRoutingAutoGroupIndex, i+1)
			param.SetRetry(0)
			param.ResetRetryNextTry()
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

	filtered := filterSmartRoutingExcludedCandidates(candidates, smartRoutingExcludedChannelIDs(param.Ctx), smartRoutingSwitchCount(param.Ctx), setting.MaxSwitches)
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
		if !reserveHalfOpen || acquireRoutingHalfOpenProbe(param.Ctx, decision.Selected, param.ModelName, group, selectorSettings.HalfOpenProbes, selectorSettings.NowUnix) {
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
		AlgorithmVersion:  channelrouting.DecisionAlgorithmObserveV1,
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

func acquireRoutingHalfOpenProbe(c *gin.Context, candidate *routingselector.RankedCandidate, modelName string, group string, maxProbes int, nowUnix int64) bool {
	if candidate == nil || candidate.Channel == nil || candidate.Candidate.Breaker == nil {
		return true
	}
	key := routingbreaker.Key{
		ChannelID:   candidate.Channel.Id,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       modelName,
		Group:       group,
	}
	return acquireRoutingHalfOpenProbeForKey(c, key, candidate.Candidate.Breaker, maxProbes, nowUnix)
}

func acquireRoutingHalfOpenProbeForKey(c *gin.Context, key routingbreaker.Key, breaker *routingselector.BreakerSnapshot, maxProbes int, nowUnix int64) bool {
	if breaker == nil {
		return true
	}
	state := strings.ToLower(strings.TrimSpace(breaker.State))
	needsProbe := state == routingselector.BreakerStateHalfOpen
	if state == routingselector.BreakerStateOpen && breaker.CooldownUntilUnix > 0 && nowUnix >= breaker.CooldownUntilUnix {
		needsProbe = true
	}
	if !needsProbe {
		return true
	}
	if routingHalfOpenProbeHeld(c, key) {
		return true
	}
	redisKey, redisOwner, redisAcquired, redisAvailable := acquireRoutingHalfOpenRedisLease(key)
	if redisAvailable && !redisAcquired {
		return false
	}
	_, ok := routingbreaker.AcquireDefaultHalfOpenProbe(key, maxProbes)
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
		if redisAcquired {
			leases, _ := common.GetContextKeyType[map[routingbreaker.Key]routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
			if leases == nil {
				leases = map[routingbreaker.Key]routingHalfOpenRedisLease{}
			}
			leases[key] = routingHalfOpenRedisLease{Key: redisKey, Owner: redisOwner}
			common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, leases)
		}
	}
	return ok
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

type routingHalfOpenRedisLease struct {
	Key   string
	Owner string
}

func acquireRoutingHalfOpenRedisLease(key routingbreaker.Key) (string, string, bool, bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return "", "", false, false
	}
	redisKey := fmt.Sprintf("routing:halfopen:%d:%d:%s:%s", key.ChannelID, key.APIKeyIndex, key.Model, key.Group)
	owner := common.GetRandomString(32)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	acquired, err := common.RDB.SetNX(ctx, redisKey, owner, 30*time.Second).Result()
	if err != nil {
		return "", "", false, false
	}
	return redisKey, owner, acquired, true
}

func releaseRoutingHalfOpenRedisLease(redisKey string, owner string) {
	if redisKey == "" || owner == "" || !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = common.RDB.Eval(ctx, `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`, []string{redisKey}, owner).Err()
}

func ReleaseRoutingHalfOpenProbe(c *gin.Context, channelID int, infoModel string, group string) {
	if channelID <= 0 || c == nil {
		return
	}
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	if !ok {
		return
	}
	leases, _ := common.GetContextKeyType[map[routingbreaker.Key]routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
	released := false
	for key := range probes {
		if key.ChannelID != channelID {
			continue
		}
		delete(probes, key)
		if lease, exists := leases[key]; exists {
			releaseRoutingHalfOpenRedisLease(lease.Key, lease.Owner)
			delete(leases, key)
		}
		routingbreaker.ReleaseDefaultHalfOpenProbe(key)
		released = true
	}
	if !released {
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbes, probes)
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, leases)
}

func ReleaseAllRoutingHalfOpenProbes(c *gin.Context) {
	if c == nil {
		return
	}
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	if !ok || len(probes) == 0 {
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenProbes, map[routingbreaker.Key]struct{}{})
	leases, _ := common.GetContextKeyType[map[routingbreaker.Key]routingHalfOpenRedisLease](c, constant.ContextKeyRoutingHalfOpenLeases)
	common.SetContextKey(c, constant.ContextKeyRoutingHalfOpenLeases, map[routingbreaker.Key]routingHalfOpenRedisLease{})
	for key := range probes {
		routingbreaker.ReleaseDefaultHalfOpenProbe(key)
	}
	for _, lease := range leases {
		releaseRoutingHalfOpenRedisLease(lease.Key, lease.Owner)
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
					if refreshed[i].Channel == nil || refreshed[i].Channel.ChannelInfo.IsMultiKey {
						continue
					}
					cacheKey := routinghotcache.Key{
						ChannelID:   refreshed[i].Channel.Id,
						APIKeyIndex: model.RoutingMetricSingleKeyIndex,
						Model:       param.ModelName,
						Group:       group,
					}
					refreshed[i].Capacity = routingCapacityForKey(cacheKey)
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
		cacheKey := routinghotcache.Key{
			ChannelID:   channel.Id,
			APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model:       param.ModelName,
			Group:       group,
		}
		candidate := routingselector.Candidate{Channel: channel}
		if cost, ok := routinghotcache.GetCost(cacheKey.CostKey()); ok {
			candidate.Cost = routingCostForRequest(param.Ctx, cost)
		}
		if observation, _, ok := channelrouting.ResolveObserveModelSnapshot(group, channel.Id, param.ModelName); useObserveSnapshot && ok && observation.MetricKnown {
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
			candidate.Capacity = routingCapacityForKey(cacheKey)
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

func routingCostForRequest(c *gin.Context, snapshot routinghotcache.CostSnapshot) *routingselector.CostSnapshot {
	cost := snapshot.Cost
	known := snapshot.Known
	if known {
		switch routingBillingMode(snapshot) {
		case "per_request":
			if snapshot.ModelPrice > 0 {
				cost = routingPositiveOrDefault(snapshot.GroupRatio, 1) * snapshot.ModelPrice
			} else {
				known = false
			}
		case "token", "":
			cost = routingTokenCostForRequest(c, snapshot)
			if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
				known = false
			}
		default:
			if snapshot.GroupRatio > 0 {
				cost = snapshot.GroupRatio
			}
		}
	}
	return &routingselector.CostSnapshot{
		Known:       known,
		Cost:        cost,
		UpdatedUnix: snapshot.UpdatedUnix,
	}
}

func routingCapacityForKey(key routinghotcache.Key) *routingselector.CapacityCooldownSnapshot {
	capacity, ok := routinghotcache.GetCapacityCooldown(key)
	if !ok {
		return nil
	}
	return &routingselector.CapacityCooldownSnapshot{
		SourceStatusCode:       capacity.SourceStatusCode,
		CooldownUntilUnixMilli: capacity.CooldownUntilUnixMilli,
		UpdatedUnixMilli:       capacity.UpdatedUnixMilli,
	}
}

func routingBillingMode(snapshot routinghotcache.CostSnapshot) string {
	mode := strings.ToLower(strings.TrimSpace(snapshot.BillingMode))
	if mode != "" {
		return mode
	}
	if snapshot.QuotaType == 1 {
		return "per_request"
	}
	return "token"
}

func routingTokenCostForRequest(c *gin.Context, snapshot routinghotcache.CostSnapshot) float64 {
	groupRatio := routingPositiveOrDefault(snapshot.GroupRatio, 1)
	if snapshot.BaseRatio <= 0 {
		return groupRatio
	}
	promptProxy := common.GetContextKeyInt(c, constant.ContextKeyRoutingPromptProxy)
	if promptProxy <= 0 {
		return groupRatio
	}
	estimatedOutput := common.GetContextKeyInt(c, constant.ContextKeyRoutingEstimatedOutput)
	if estimatedOutput <= 0 {
		estimatedOutput = int(math.Min(float64(promptProxy)*1.5, 512))
	}
	if estimatedOutput < 0 {
		estimatedOutput = 0
	}
	completionRatio := snapshot.CompletionRatio
	if completionRatio <= 0 || math.IsNaN(completionRatio) || math.IsInf(completionRatio, 0) {
		completionRatio = 1
	}
	rawCost := groupRatio * (float64(promptProxy)*snapshot.BaseRatio + float64(estimatedOutput)*snapshot.BaseRatio*completionRatio)
	return rawCost / common.QuotaPerUnit
}

func routingPositiveOrDefault(value float64, fallback float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
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
	for _, raw := range c.GetStringSlice("use_channel") {
		id, err := strconv.Atoi(strings.TrimSpace(raw))
		if err == nil && id > 0 {
			excluded[id] = struct{}{}
		}
	}
	return excluded
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

func AffinityAdmissible(channelID int) bool {
	if channelID <= 0 {
		return true
	}
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled {
		return true
	}
	if balance, ok := routinghotcache.GetBalance(channelID); ok && balance.Known && routingMarkerFresh(balance.UpdatedUnix, setting) && balance.Balance < setting.BalanceMarginUSD {
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
	filtered := filterSmartRoutingExcludedCandidates(candidates, smartRoutingExcludedChannelIDs(c), smartRoutingSwitchCount(c), setting.MaxSwitches)
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
		if !acquireRoutingHalfOpenProbe(c, ranked, modelName, group, selectorSettings.HalfOpenProbes, selectorSettings.NowUnix) {
			return nil, false
		}
		return ranked.Channel, true
	}
	return nil, false
}

func admissibleAffinityChannelLegacy(c *gin.Context, preferredID int, modelName string, usingGroup string, requestPath string) (*model.Channel, string, bool) {
	preferred, err := model.CacheGetChannel(preferredID)
	if err != nil || preferred == nil || preferred.Status != common.ChannelStatusEnabled ||
		!channelSupportsSmartRoutingRequestPath(preferred, requestPath) || !AffinityAdmissible(preferred.Id) {
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

func filterSmartRoutingExcludedCandidates(candidates []routingselector.Candidate, excluded map[int]struct{}, switchCount int, maxSwitches int) []routingselector.Candidate {
	if len(candidates) == 0 || len(excluded) == 0 {
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
		if _, found := excluded[candidate.Channel.Id]; found {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func applyRoutingHealthMarkers(candidate *routingselector.Candidate, channelID int, setting smart_routing_setting.SmartRoutingSetting) {
	if candidate == nil || channelID <= 0 {
		return
	}
	if balance, ok := routinghotcache.GetBalance(channelID); ok && balance.Known && routingMarkerFresh(balance.UpdatedUnix, setting) && balance.Balance < setting.BalanceMarginUSD {
		candidate.Breaker = &routingselector.BreakerSnapshot{
			State:       routingselector.BreakerStateOpen,
			Reason:      routingselector.BreakerReasonBalance,
			UpdatedUnix: balance.UpdatedUnix,
		}
	}
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
