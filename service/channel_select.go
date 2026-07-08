package service

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
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
	if shouldActivateSmartRouting(smartSetting) {
		channel, selectGroup, handled, err := cacheGetSmartSatisfiedChannel(param, smartSetting)
		if err != nil {
			return channel, selectGroup, err
		}
		if handled && channel != nil {
			return channel, selectGroup, nil
		}
	} else if shouldObserveSmartRouting(smartSetting) {
		recordSmartRoutingDecision(param, smartSetting)
	}
	return cacheGetRandomSatisfiedChannelLegacy(param)
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
	return setting.Mode == smart_routing_setting.ModeBalanced || setting.Mode == smart_routing_setting.ModeEnterpriseSLO
}

func shouldObserveSmartRouting(setting smart_routing_setting.SmartRoutingSetting) bool {
	if !setting.Enabled {
		return false
	}
	return setting.Mode == smart_routing_setting.ModeObserve || setting.Mode == smart_routing_setting.ModeShadow
}

func recordSmartRoutingDecision(param *RetryParam, setting smart_routing_setting.SmartRoutingSetting) {
	if param == nil {
		return
	}
	if param.TokenGroup == "auto" {
		userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
		for _, group := range GetUserAutoGroup(userGroup) {
			if channel, _ := selectSmartChannelForGroup(param, group, setting); channel != nil {
				return
			}
		}
		return
	}
	_, _ = selectSmartChannelForGroup(param, param.TokenGroup, setting)
}

func cacheGetSmartSatisfiedChannel(param *RetryParam, smartSetting smart_routing_setting.SmartRoutingSetting) (*model.Channel, string, bool, error) {
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	if param.TokenGroup != "auto" {
		channel, err := selectSmartChannelForGroup(param, param.TokenGroup, smartSetting)
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

		channel, err := selectSmartChannelForGroup(param, autoGroup, smartSetting)
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

func selectSmartChannelForGroup(param *RetryParam, group string, setting smart_routing_setting.SmartRoutingSetting) (*model.Channel, error) {
	candidates, err := smartRoutingCandidatesForGroup(param, group)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}

	filtered := filterSmartRoutingExcludedCandidates(candidates, smartRoutingExcludedChannelIDs(param.Ctx), smartRoutingSwitchCount(param.Ctx), setting.MaxSwitches)
	if len(filtered) == 0 {
		return nil, nil
	}

	decision := routingselector.SelectRankedFromCandidates(filtered, routingSelectorSettings(setting))
	if param.Ctx != nil {
		common.SetContextKey(param.Ctx, constant.ContextKeyRoutingLastDecision, decision)
	}
	if decision.Selected == nil || decision.Selected.Channel == nil {
		return nil, nil
	}
	return decision.Selected.Channel, nil
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
				return candidates, nil
			}
		}
	}

	channels, err := model.GetRankedSatisfiedChannels(group, param.ModelName, param.RequestPath)
	if err != nil || len(channels) == 0 {
		return nil, err
	}
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
		if metric, ok := routinghotcache.GetMetric(cacheKey); ok {
			candidate.Metric = &routingselector.MetricSnapshot{
				RequestCount: metric.RequestCount,
				SuccessCount: metric.SuccessCount,
				P95LatencyMs: metric.P95LatencyMs,
				TPS:          metric.TPS,
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
		if cost, ok := routinghotcache.GetCost(cacheKey.CostKey()); ok {
			candidate.Cost = &routingselector.CostSnapshot{
				Known:       cost.Known,
				Cost:        cost.Cost,
				UpdatedUnix: cost.UpdatedUnix,
			}
		}
		if breaker, ok := routinghotcache.GetBreaker(cacheKey); ok {
			candidate.Breaker = &routingselector.BreakerSnapshot{
				State:       breaker.State,
				Reason:      breaker.Reason,
				UpdatedUnix: breaker.UpdatedUnix,
			}
		}
		applyRoutingHealthMarkers(&candidate, channel.Id, smart_routing_setting.GetSetting())
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

func smartRoutingMemoKey(group string, modelName string, requestPath string) string {
	return group + "\x00" + modelName + "\x00" + requestPath
}

func routingSelectorSettings(setting smart_routing_setting.SmartRoutingSetting) routingselector.Settings {
	return routingselector.Settings{
		WeightAvailability: setting.WeightAvailability,
		WeightLatency:      setting.WeightLatency,
		WeightThroughput:   setting.WeightThroughput,
		WeightCost:         setting.WeightCost,
		AvailabilityFloor:  setting.AvailabilityFloor,
		MinVolume:          setting.MinVolume,
		TopK:               setting.TopK,
		MaxEjectedPct:      setting.MaxEjectedPct,
		SnapshotStaleSec:   setting.SnapshotStaleSec,
		NowUnix:            common.GetTimestamp(),
		RandomSeed:         time.Now().UnixNano(),
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
	if authFailure, ok := routinghotcache.GetAuthFailure(channelID); ok && authFailure.Marked && routingMarkerFresh(authFailure.UpdatedUnix, setting) {
		return false
	}
	if balance, ok := routinghotcache.GetBalance(channelID); ok && balance.Known && routingMarkerFresh(balance.UpdatedUnix, setting) && balance.Balance < setting.BalanceMarginUSD {
		return false
	}
	return true
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
	if authFailure, ok := routinghotcache.GetAuthFailure(channelID); ok && authFailure.Marked && routingMarkerFresh(authFailure.UpdatedUnix, setting) {
		candidate.Breaker = &routingselector.BreakerSnapshot{
			State:       routingselector.BreakerStateOpen,
			Reason:      routingselector.BreakerReasonAuthFail,
			UpdatedUnix: authFailure.UpdatedUnix,
		}
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
