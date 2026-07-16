package service

import (
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const routingHedgeActualTokenLimit = int64(1_000_000_000_000)

type RoutingHedgeActualCost struct {
	Known              bool
	Cost               float64
	PromptTokens       int64
	CompletionTokens   int64
	TotalTokens        int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
}

type routingHedgeNormalizedUsage struct {
	promptTokens       int
	completionTokens   int
	auditPromptTokens  int64
	cacheReadTokens    int64
	cacheWriteTokens   int64
	cacheWrite1hTokens int64
	imageInputTokens   int64
	imageOutputTokens  int64
	audioInputTokens   int64
	audioOutputTokens  int64
	claudeSemantic     bool
}

func ChannelRoutingEnterpriseHedgePolicy(
	c *gin.Context,
) (channelrouting.EnterpriseHedgePolicy, bool, error) {
	if c == nil {
		return channelrouting.EnterpriseHedgePolicy{}, false, channelrouting.ErrRoutingSessionInvalid
	}
	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if group == "auto" {
		group = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	}
	if group == "" || group == "auto" {
		return channelrouting.EnterpriseHedgePolicy{}, false, nil
	}
	sessions, err := channelRoutingSessionSet(c)
	if err != nil {
		return channelrouting.EnterpriseHedgePolicy{}, false, err
	}
	session, err := sessions.Session(group)
	if err != nil {
		return channelrouting.EnterpriseHedgePolicy{}, false, err
	}
	return session.EnterpriseHedgePolicy()
}

func ChannelRoutingHedgeCostEstimate(
	c *gin.Context,
	channelID int,
	modelName string,
	requestPath string,
	retryIndex int,
) (channelrouting.ShadowCostInput, bool, error) {
	if c == nil || channelID <= 0 {
		return channelrouting.ShadowCostInput{}, false, channelrouting.ErrRoutingSessionInvalid
	}
	promptTokens := max(common.GetContextKeyInt(c, constant.ContextKeyRoutingPromptProxy), 0)
	completionTokens := max(common.GetContextKeyInt(c, constant.ContextKeyRoutingEstimatedOutput), 0)
	profile := routingCostRequestProfile(c)
	if profile == nil {
		return channelrouting.ShadowCostInput{}, false, nil
	}
	cloned := *profile
	cloned.MaxAttempts = 1
	cloned.RetryProbability = 0
	cloned.HedgeProbability = 0
	cloned.HedgeAllowed = false
	profile = &cloned
	if !profile.KnowledgeSpecified || !profile.InputTokensKnown || !profile.MaximumCompletionKnown {
		return channelrouting.ShadowCostInput{}, false, nil
	}
	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if group == "auto" {
		group = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	}
	sessions, err := channelRoutingSessionSet(c)
	if err != nil {
		return channelrouting.ShadowCostInput{}, false, err
	}
	session, err := sessions.Session(group)
	if err != nil {
		return channelrouting.ShadowCostInput{}, false, err
	}
	requestProfile, err := routingRequestProfile(c, group, retryIndex, promptTokens, completionTokens)
	if err != nil {
		return channelrouting.ShadowCostInput{}, false, err
	}
	estimate, exists, err := session.CostEstimateForChannel(channelID, channelrouting.RequestRoutingCostInput{
		RequestPath: requestPath, ModelName: modelName,
		IsStream: common.GetContextKeyBool(c, constant.ContextKeyIsStream), RetryIndex: retryIndex,
		PromptTokenEstimate: promptTokens, CompletionTokenEstimate: completionTokens,
		CostProfile: profile,
		Profile:     requestProfile,
	})
	if err != nil || !exists {
		return channelrouting.ShadowCostInput{}, false, err
	}
	known := estimate.Known && estimate.WorstCaseKnown && estimate.EffectiveKnown &&
		estimate.Currency != "" && estimate.Unit != "" && estimate.PricingBasis != "" &&
		estimate.PricingHash != "" && estimate.PricingVersion != "" && estimate.PricingIdentity != "" &&
		estimate.ConfigurationRevision > 0 && estimate.UpstreamCostMultiplier >= 0 &&
		estimate.ObservedTime > 0 && estimate.EffectiveTime > 0 &&
		estimate.ExpiresTime >= estimate.EffectiveTime &&
		estimate.ConfidenceScore >= 0 && estimate.ConfidenceScore <= 1 &&
		estimate.FreshnessScore >= 0 && estimate.FreshnessScore <= 1 &&
		estimate.WorstCaseCost >= estimate.Cost
	return estimate, known, nil
}

func ChannelRoutingHedgeActualCost(
	c *gin.Context,
	channelID int,
	modelName string,
	requestPath string,
	retryIndex int,
	usage *dto.Usage,
) (RoutingHedgeActualCost, error) {
	if c == nil || channelID <= 0 || usage == nil {
		return RoutingHedgeActualCost{}, nil
	}
	normalized, ok := normalizeRoutingHedgeActualUsage(usage)
	if !ok {
		return RoutingHedgeActualCost{}, nil
	}

	profile := routingCostRequestProfile(c)
	if profile == nil {
		return RoutingHedgeActualCost{}, nil
	}
	actualProfile := *profile
	actualProfile.PromptTokens = int64(normalized.promptTokens)
	actualProfile.ExpectedCompletionTokens = int64(normalized.completionTokens)
	actualProfile.MaximumCompletionTokens = int64(normalized.completionTokens)
	actualProfile.CacheReadTokens = normalized.cacheReadTokens
	actualProfile.CacheWriteTokens = normalized.cacheWriteTokens
	actualProfile.CacheWriteOneHourTokens = normalized.cacheWrite1hTokens
	actualProfile.ImageInputTokens = normalized.imageInputTokens
	actualProfile.AudioInputTokens = normalized.audioInputTokens
	actualProfile.ImageOutputTokens = normalized.imageOutputTokens
	actualProfile.AudioOutputTokens = normalized.audioOutputTokens
	actualProfile.ActualUsage = &model.RoutingCostActualUsage{
		PromptTokens: int64(normalized.promptTokens), CompletionTokens: int64(normalized.completionTokens),
		CacheReadTokens: normalized.cacheReadTokens, CacheWriteTokens: normalized.cacheWriteTokens,
		CacheWriteOneHourTokens: normalized.cacheWrite1hTokens,
		ImageInputTokens:        actualProfile.ImageInputTokens, ImageOutputTokens: actualProfile.ImageOutputTokens,
		AudioInputTokens: actualProfile.AudioInputTokens, AudioOutputTokens: actualProfile.AudioOutputTokens,
		ClaudeUsageSemantic: normalized.claudeSemantic,
	}
	actualProfile.MaxAttempts = 1
	actualProfile.RetryProbability = 0
	actualProfile.HedgeProbability = 0
	actualProfile.HedgeAllowed = false
	actualProfile.KnowledgeSpecified = true
	actualProfile.InputTokensKnown = true
	actualProfile.MaximumCompletionKnown = true
	actualProfile.CacheTokensKnown = true
	actualProfile.MediaDimensionsKnown = true
	actualProfile.RequestInputKnown = true

	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if group == "auto" {
		group = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	}
	sessions, err := channelRoutingSessionSet(c)
	if err != nil {
		return RoutingHedgeActualCost{}, err
	}
	session, err := sessions.Session(group)
	if err != nil {
		return RoutingHedgeActualCost{}, err
	}
	requestProfile, err := routingRequestProfile(c, group, retryIndex, normalized.promptTokens, normalized.completionTokens)
	if err != nil {
		return RoutingHedgeActualCost{}, err
	}
	estimate, exists, err := session.CostEstimateForChannel(channelID, channelrouting.RequestRoutingCostInput{
		RequestPath: requestPath, ModelName: modelName,
		IsStream: common.GetContextKeyBool(c, constant.ContextKeyIsStream), RetryIndex: retryIndex,
		PromptTokenEstimate: normalized.promptTokens, CompletionTokenEstimate: normalized.completionTokens,
		CostProfile: &actualProfile,
		Profile:     requestProfile,
	})
	if err != nil || !exists || !estimate.Known || math.IsNaN(estimate.Cost) || math.IsInf(estimate.Cost, 0) || estimate.Cost < 0 {
		return RoutingHedgeActualCost{}, err
	}
	return RoutingHedgeActualCost{
		Known: true, Cost: estimate.Cost,
		PromptTokens: normalized.auditPromptTokens, CompletionTokens: int64(normalized.completionTokens),
		TotalTokens:     normalized.auditPromptTokens + int64(normalized.completionTokens),
		CacheReadTokens: normalized.cacheReadTokens, CacheWriteTokens: normalized.cacheWriteTokens,
		CacheWrite1hTokens: normalized.cacheWrite1hTokens,
	}, nil
}

func normalizeRoutingHedgeActualUsage(usage *dto.Usage) (routingHedgeNormalizedUsage, bool) {
	if usage == nil {
		return routingHedgeNormalizedUsage{}, false
	}
	normalized := routingHedgeNormalizedUsage{
		promptTokens: usage.PromptTokens, completionTokens: usage.CompletionTokens,
		cacheReadTokens:    int64(max(usage.PromptCacheHitTokens, usage.PromptTokensDetails.CachedTokens)),
		cacheWriteTokens:   int64(usage.ClaudeCacheCreation5mTokens),
		cacheWrite1hTokens: int64(usage.ClaudeCacheCreation1hTokens),
		imageInputTokens:   int64(usage.PromptTokensDetails.ImageTokens),
		audioInputTokens:   int64(usage.PromptTokensDetails.AudioTokens),
		imageOutputTokens:  int64(usage.CompletionTokenDetails.ImageTokens),
		audioOutputTokens:  int64(usage.CompletionTokenDetails.AudioTokens),
		claudeSemantic:     usage.UsageSemantic == "anthropic",
	}
	if normalized.promptTokens == 0 && usage.InputTokens > 0 {
		normalized.promptTokens = usage.InputTokens
	}
	if normalized.completionTokens == 0 && usage.OutputTokens > 0 {
		normalized.completionTokens = usage.OutputTokens
	}
	if usage.InputTokensDetails != nil {
		if cachedTokens := int64(usage.InputTokensDetails.CachedTokens); cachedTokens > normalized.cacheReadTokens {
			normalized.cacheReadTokens = cachedTokens
		}
		normalized.imageInputTokens = int64(usage.InputTokensDetails.ImageTokens)
		normalized.audioInputTokens = int64(usage.InputTokensDetails.AudioTokens)
	}
	if normalized.cacheWriteTokens == 0 && normalized.cacheWrite1hTokens == 0 {
		normalized.cacheWriteTokens = int64(usage.PromptTokensDetails.CachedCreationTokens)
		if usage.InputTokensDetails != nil {
			if cachedCreationTokens := int64(usage.InputTokensDetails.CachedCreationTokens); cachedCreationTokens > normalized.cacheWriteTokens {
				normalized.cacheWriteTokens = cachedCreationTokens
			}
		}
	}
	prompt := int64(normalized.promptTokens)
	completion := int64(normalized.completionTokens)
	values := []int64{
		prompt, completion, normalized.cacheReadTokens, normalized.cacheWriteTokens,
		normalized.cacheWrite1hTokens, normalized.imageInputTokens, normalized.imageOutputTokens,
		normalized.audioInputTokens, normalized.audioOutputTokens,
	}
	for _, value := range values {
		if value < 0 || value > routingHedgeActualTokenLimit {
			return routingHedgeNormalizedUsage{}, false
		}
	}
	if prompt > routingHedgeActualTokenLimit-completion ||
		normalized.imageInputTokens > prompt || normalized.audioInputTokens > prompt ||
		normalized.imageOutputTokens > completion || normalized.audioOutputTokens > completion ||
		(!normalized.claudeSemantic && (normalized.cacheReadTokens > prompt ||
			normalized.cacheWriteTokens > prompt || normalized.cacheWrite1hTokens > prompt)) {
		return routingHedgeNormalizedUsage{}, false
	}
	normalized.auditPromptTokens = prompt
	if normalized.claudeSemantic {
		for _, cacheTokens := range []int64{
			normalized.cacheReadTokens, normalized.cacheWriteTokens, normalized.cacheWrite1hTokens,
		} {
			if normalized.auditPromptTokens > routingHedgeActualTokenLimit-cacheTokens {
				return routingHedgeNormalizedUsage{}, false
			}
			normalized.auditPromptTokens += cacheTokens
		}
		if normalized.auditPromptTokens > routingHedgeActualTokenLimit-completion {
			return routingHedgeNormalizedUsage{}, false
		}
	}
	return normalized, true
}

func RoutingHedgePolicyRevision(c *gin.Context) uint64 {
	if c == nil {
		return 0
	}
	revision, _ := common.GetContextKeyType[uint64](c, constant.ContextKeyRoutingSnapshotRevision)
	return revision
}

func RoutingHedgePoolID(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return common.GetContextKeyInt(c, constant.ContextKeyRoutingPoolID)
}

func RoutingHedgeMemberID(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return common.GetContextKeyInt(c, constant.ContextKeyRoutingMemberID)
}

func RoutingHedgeCredentialID(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return common.GetContextKeyInt(c, constant.ContextKeyRoutingCredentialID)
}
