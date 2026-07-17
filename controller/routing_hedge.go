package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

var (
	routingHedgeProcessLimiter     = channelrouting.DefaultHedgeProcessLimiter()
	routingHedgeProcessByteLimiter = channelrouting.DefaultHedgeByteLimiter()
	routingHedgeRatioBudget        = channelrouting.DefaultHedgeRatioBudget()
	routingHedgeExecuteText        = relay.TextHelperCapture
	routingHedgeFinalizeText       = relay.FinalizeTextResponseCapture
)

var errRoutingHedgeResponseTooLarge = errors.New("channel routing hedge response exceeded the configured buffer")

const routingHedgeLoserCleanupTimeout = 5 * time.Second

type routingHedgeOutcome struct {
	apiErr         *types.NewAPIError
	classification routingerror.Classification
	success        bool
	retryDecided   bool
	willRetry      bool
}

type routingHedgeBranch struct {
	role     channelrouting.HedgeAttemptRole
	ctx      *gin.Context
	info     *relaycommon.RelayInfo
	channel  *model.Channel
	recorder *routingHedgeResponseWriter
	cancel   context.CancelCauseFunc
	lease    *channelrouting.HedgeAttemptLease
	audit    *channelrouting.HedgeAttemptAuditReservation
	send     *relaycommon.RoutingUpstreamSendState
}

type routingHedgeBranchResult struct {
	branch         *routingHedgeBranch
	capture        *relay.TextResponseCapture
	apiErr         *types.NewAPIError
	classification routingerror.Classification
	success        bool
	cause          error
	overflow       bool
	completedAtMs  int64
}

type routingHedgeActiveCollection struct {
	results       map[channelrouting.HedgeAttemptRole]routingHedgeBranchResult
	winner        *routingHedgeBranchResult
	terminal      *routingHedgeBranchResult
	pending       <-chan routingHedgeBranchResult
	pendingBranch *routingHedgeBranch
}

type routingHedgeAuditDisposition struct {
	clientCommitted bool
	willRetry       bool
	finalAttempt    bool
	completedAtMs   int64
	finalErr        *types.NewAPIError
	classification  routingerror.Classification
}

type routingHedgeResponseWriter struct {
	header   http.Header
	buffer   bytes.Buffer
	status   int
	limit    int64
	overflow bool
}

func maybeExecuteRoutingHedge(
	c *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	primaryChannel *model.Channel,
	retryParam *service.RetryParam,
	attemptLease *channelrouting.AttemptLease,
	bodyStorage common.BodyStorage,
) (routingHedgeOutcome, bool) {
	if attemptLease == nil || bodyStorage == nil {
		return routingHedgeOutcome{}, false
	}
	setting := smart_routing_setting.GetSetting()
	policy, active, policyErr := service.ChannelRoutingEnterpriseHedgePolicy(c)
	if policyErr != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing hedge policy unavailable: %v", policyErr))
		return routingHedgeOutcome{}, false
	}
	if !active || !routingHedgeEligible(c, baseInfo, primaryChannel, retryParam, setting, policy) {
		return routingHedgeOutcome{}, false
	}
	primaryCost, costKnown, costErr := service.ChannelRoutingHedgeCostEstimate(
		c, primaryChannel.Id, baseInfo.OriginModelName, retryParam.RequestPath, retryParam.GetRetry(),
	)
	if costErr != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing hedge primary cost unavailable: %v", costErr))
		return routingHedgeOutcome{}, false
	}
	if !costKnown {
		return routingHedgeOutcome{}, false
	}
	coordinator, err := channelrouting.NewHedgeCoordinator(policy, primaryCost.WorstCaseCost)
	if err != nil {
		return routingHedgeOutcome{}, false
	}
	primaryHedgeLease, err := coordinator.BeginPrimary()
	if err != nil {
		return routingHedgeOutcome{}, false
	}
	slot := routingHedgeProcessLimiter.TryAcquire(setting.HedgeMaxConcurrent)
	if slot == nil {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	defer func() {
		if slot != nil {
			slot.Release()
		}
	}()

	responseLimit := policy.MaxResponseBytes
	if globalLimit := int64(setting.HedgeMaxResponseBytes); globalLimit < responseLimit {
		responseLimit = globalLimit
	}
	if bodyStorage.Size() <= 0 || bodyStorage.Size() > responseLimit {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	bufferSlot := routingHedgeProcessByteLimiter.TryAcquire(responseLimit*2, setting.HedgeMaxBufferedBytes)
	if bufferSlot == nil {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	defer func() {
		if bufferSlot != nil {
			bufferSlot.Release()
		}
	}()
	poolID := service.RoutingHedgePoolID(c)
	ratioWindow := time.Duration(setting.HedgeRatioWindowSec) * time.Second
	if !routingHedgeRatioBudget.ObservePrimary(poolID, time.Now(), ratioWindow) {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	body, err := bodyStorage.Bytes()
	if err != nil {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	primary, err := newRoutingHedgeBranch(
		c, baseInfo, primaryChannel, channelrouting.HedgeAttemptPrimary,
		primaryHedgeLease, body, responseLimit, false,
	)
	if err != nil {
		primaryHedgeLease.Finish()
		return routingHedgeOutcome{}, false
	}
	defer primary.cancel(nil)
	primary.audit, err = startRoutingHedgeBranchAudit(primary, primaryCost)
	if err != nil {
		primaryHedgeLease.Finish()
		logger.LogWarn(c, fmt.Sprintf("channel routing hedge audit unavailable; using serial relay: %v", err))
		return routingHedgeOutcome{}, false
	}
	if capacityErr := commitRoutingCapacityAttempt(primary.ctx); capacityErr != nil {
		primaryHedgeLease.Finish()
		attemptLease.Finish()
		classification, _ := classifyRoutingRelayAttemptWithContext(primary.ctx, capacityErr, primary.info)
		return finishRoutingHedgeFailure(
			c, baseInfo, retryParam, capacityErr, classification,
			func(willRetry bool) {
				completeRoutingHedgeAdmissionFailure(primary, capacityErr, willRetry, !willRetry)
			},
		), true
	}
	prepareRoutingRelayAttempt(primary.info)
	primary.send = bindRoutingUpstreamAttempt(primary.ctx, primary.info, primary.channel, attemptLease)
	defer attemptLease.Finish()
	defer common.SetContextKey(c, constant.ContextKeyRoutingCapacityFailure, nil)

	primaryResults := make(chan routingHedgeBranchResult, 1)
	go func() {
		primaryResults <- executeRoutingHedgeBranch(primary)
	}()
	if result, finished := waitRoutingHedgePrimaryOrDelay(primaryResults, policy.Delay); finished {
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	}

	secondary, secondaryCost, secondaryErr := prepareRoutingHedgeSecondary(
		c, baseInfo, primaryChannel, retryParam, coordinator, primaryCost, body, responseLimit,
	)
	if secondaryErr != nil || secondary == nil {
		if secondaryErr != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing hedge secondary unavailable: %v", secondaryErr))
		}
		result := <-primaryResults
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	}
	defer secondary.cancel(nil)

	select {
	case result := <-primaryResults:
		abortRoutingHedgeBranchBeforeSend(secondary)
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	default:
	}
	if !routingHedgeRatioBudget.AllowSecondary(
		poolID, time.Now(), ratioWindow, setting.HedgeMaxExtraBasisPoints,
	) {
		abortRoutingHedgeBranchBeforeSend(secondary)
		result := <-primaryResults
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	}
	secondary.audit, err = startRoutingHedgeBranchAudit(secondary, secondaryCost)
	if err != nil {
		abortRoutingHedgeBranchBeforeSend(secondary)
		logger.LogWarn(c, fmt.Sprintf("channel routing hedge secondary audit unavailable: %v", err))
		result := <-primaryResults
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	}
	if capacityErr := commitRoutingCapacityAttempt(secondary.ctx); capacityErr != nil {
		completeRoutingHedgeAdmissionFailure(secondary, capacityErr, false, false)
		abortRoutingHedgeBranchBeforeSend(secondary)
		result := <-primaryResults
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	}
	prepareRoutingRelayAttempt(secondary.info)
	secondary.send = bindRoutingUpstreamAttempt(secondary.ctx, secondary.info, secondary.channel, nil)

	select {
	case result := <-primaryResults:
		releaseRoutingCapacityReservation(secondary.ctx)
		finishRoutingCanaryAttempt(secondary.ctx, secondary.send)
		relaycommon.ClearRoutingUpstreamSendState(secondary.ctx)
		completeRoutingHedgeBranchAsLost(secondary, time.Now().UnixMilli())
		secondary.lease.Finish()
		return finishSingleRoutingHedgeResult(c, baseInfo, retryParam, attemptLease, result)
	default:
	}
	addUsedChannel(secondary.ctx, secondary.channel.Id)
	mergeRoutingHedgeUsedChannels(c, secondary.ctx)
	service.MergeRoutingTargetExclusions(c, secondary.ctx)
	secondaryResults := make(chan routingHedgeBranchResult, 1)
	go func() {
		secondaryResults <- executeRoutingHedgeBranch(secondary)
	}()

	collection := collectRoutingHedgeActiveResults(primaryResults, secondaryResults, primary, secondary)
	if collection.winner != nil && collection.pending != nil && collection.pendingBranch != nil {
		winner := *collection.winner
		promoteRoutingHedgeRelayInfo(baseInfo, winner.branch)
		finishRoutingHedgeLoserAsync(
			collection.pending, collection.pendingBranch, &winner, slot, bufferSlot, routingHedgeLoserCleanupTimeout,
		)
		slot = nil
		bufferSlot = nil
		return finishRoutingHedgeWinnerResult(c, baseInfo, retryParam, attemptLease, winner), true
	}
	if collection.terminal != nil && collection.pending != nil && collection.pendingBranch != nil {
		terminal := *collection.terminal
		promoteRoutingHedgeRelayInfo(baseInfo, terminal.branch)
		recordRoutingHedgeAttemptEffects(terminal, nil)
		mergeRoutingHedgeTargetExclusions(c, terminal.branch)
		finishRoutingHedgeLoserAsync(
			collection.pending, collection.pendingBranch, nil, slot, bufferSlot, routingHedgeLoserCleanupTimeout,
		)
		slot = nil
		bufferSlot = nil
		apiErr := terminal.apiErr
		if apiErr == nil {
			apiErr = routingHedgeInternalError("safety rejection", errors.New("empty safety rejection"))
		}
		return finishRoutingHedgeFailure(
			c, baseInfo, retryParam, apiErr, terminal.classification,
			func(willRetry bool) {
				disposition := routingHedgeAuditDisposition{
					willRetry: willRetry, finalAttempt: !willRetry, completedAtMs: time.Now().UnixMilli(),
				}
				if err := completeRoutingHedgeResultAudit(terminal, &terminal, disposition); err != nil {
					logger.LogError(c, fmt.Sprintf("channel routing hedge safety audit failed: %v", err))
				}
			},
		), true
	}
	results, winner := collection.results, collection.winner
	primaryResult := results[channelrouting.HedgeAttemptPrimary]
	secondaryResult := results[channelrouting.HedgeAttemptSecondary]
	promoteRoutingHedgeRelayInfo(baseInfo, primaryResult.branch)
	recordRoutingHedgeAttemptEffects(primaryResult, winner)
	recordRoutingHedgeAttemptEffects(secondaryResult, winner)
	mergeRoutingHedgeTargetExclusions(c, primaryResult.branch)
	mergeRoutingHedgeTargetExclusions(c, secondaryResult.branch)
	if winner != nil {
		loser := primaryResult
		if winner.branch == primaryResult.branch {
			loser = secondaryResult
		}
		if err := completeRoutingHedgeResultAudit(loser, winner, routingHedgeAuditDisposition{}); err != nil {
			logger.LogError(c, fmt.Sprintf("channel routing hedge loser audit failed: %v", err))
		}
		return finishRoutingHedgeWinnerResult(c, baseInfo, retryParam, attemptLease, *winner), true
	}
	failure := primaryResult
	if failure.apiErr == nil || errors.Is(failure.cause, channelrouting.ErrHedgeLost) {
		failure = secondaryResult
	}
	apiErr := failure.apiErr
	if apiErr == nil {
		apiErr = routingHedgeInternalError("both attempts failed without a relay error", errors.New("empty hedge failure"))
	}
	return finishRoutingHedgeFailure(
		c, baseInfo, retryParam, apiErr, failure.classification,
		func(willRetry bool) {
			disposition := routingHedgeAuditDisposition{
				willRetry: willRetry, finalAttempt: !willRetry, completedAtMs: time.Now().UnixMilli(),
			}
			if err := completeRoutingHedgeResultAudits(
				primaryResult, secondaryResult, &failure, disposition,
			); err != nil {
				logger.LogError(c, fmt.Sprintf("channel routing hedge result audit failed: %v", err))
			}
		},
	), true
}

func routingHedgeEligible(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channel *model.Channel,
	retryParam *service.RetryParam,
	setting smart_routing_setting.SmartRoutingSetting,
	policy channelrouting.EnterpriseHedgePolicy,
) bool {
	if requestPolicy, ok := service.ChannelRoutingRequestAttemptPolicy(c); ok && !requestPolicy.HedgeAllowed {
		return false
	}
	if c == nil || c.Request == nil || info == nil || channel == nil || retryParam == nil ||
		!smart_routing_setting.ResolveEffectiveMode(setting).AllowsEnterpriseFeatures() ||
		!setting.HedgeEnabled || !policy.Enabled || !policy.Explicit || policy.CrossRegion ||
		policy.Scope != channelrouting.EnterpriseHedgeScopeDistinctTarget || retryParam.GetRetry() != 0 ||
		info.RelayFormat != types.RelayFormatOpenAI || info.IsStream ||
		(info.RelayMode != relayconstant.RelayModeChatCompletions && info.RelayMode != relayconstant.RelayModeCompletions) ||
		channel.Type != constant.ChannelTypeOpenAI ||
		common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) ||
		common.GetContextKeyString(c, constant.ContextKeyRoutingDecisionID) == "" ||
		!service.HasRoutingStrictCapacityReservation(c) || routingHedgeHasHalfOpenProbe(c) ||
		!strings.HasPrefix(strings.ToLower(c.Request.Header.Get("Content-Type")), "application/json") ||
		strings.Contains(strings.ToLower(c.Request.Header.Get("Content-Type")), "multipart/") {
		return false
	}
	if _, pinned := c.Get(string(constant.ContextKeyTokenSpecificChannelId)); pinned {
		return false
	}
	channelSetting, _ := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || channelSetting.PassThroughBodyEnabled ||
		service.ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, info.OriginModelName) ||
		len(common.GetContextKeyStringMap(c, constant.ContextKeyChannelParamOverride)) > 0 {
		return false
	}
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	return ok && routingHedgeReplaySafeTextRequest(request, info.RelayMode)
}

func routingHedgeReplaySafeTextRequest(request *dto.GeneralOpenAIRequest, relayMode int) bool {
	if request == nil || (request.Stream != nil && *request.Stream) ||
		(request.N != nil && *request.N != 1) || request.ResponseFormat != nil ||
		request.ToolChoice != nil || len(request.Tools) > 0 || request.ParallelTooCalls != nil ||
		request.WebSearchOptions != nil || request.ReturnImages != nil || request.ReturnRelatedQuestions != nil ||
		request.Prefix != nil || request.Suffix != nil || request.Input != nil ||
		request.PromptCacheKey != "" || request.PromptCacheRetention != nil ||
		request.Size != "" || request.Instruction != "" || request.Dimensions != nil {
		return false
	}
	rawFields := [][]byte{
		request.Functions, request.FunctionCall, request.EncodingFormat, request.Modalities, request.Audio,
		request.ServiceTier, request.SafetyIdentifier, request.Store, request.LogitBias, request.Metadata,
		request.Prediction, request.ExtraBody, request.SearchParameters, request.Usage, request.Reasoning,
		request.User,
		request.VlHighResolutionImages, request.EnableThinking, request.ChatTemplateKwargs, request.EnableSearch,
		request.Think, request.WebSearch, request.THINKING, request.SearchDomainFilter, request.SearchRecencyFilter,
		request.SearchMode, request.ReasoningSplit,
	}
	for _, raw := range rawFields {
		if !routingHedgeRawFieldEmpty(raw) {
			return false
		}
	}
	switch relayMode {
	case relayconstant.RelayModeChatCompletions:
		if request.Prompt != nil || len(request.Messages) == 0 {
			return false
		}
		for index := range request.Messages {
			if !routingHedgeReplaySafeMessage(request.Messages[index]) {
				return false
			}
		}
		return true
	case relayconstant.RelayModeCompletions:
		return len(request.Messages) == 0 && routingHedgeReplaySafePrompt(request.Prompt)
	default:
		return false
	}
}

func routingHedgeReplaySafeMessage(message dto.Message) bool {
	switch strings.ToLower(strings.TrimSpace(message.Role)) {
	case "system", "developer", "user", "assistant":
	default:
		return false
	}
	if message.ToolCallId != "" ||
		!routingHedgeRawFieldEmpty(message.ToolCalls) || message.Prefix != nil {
		return false
	}
	switch content := message.Content.(type) {
	case string:
		return true
	case []any:
		if len(content) == 0 {
			return false
		}
		for _, item := range content {
			switch typed := item.(type) {
			case dto.MediaContent:
				if !routingHedgeReplaySafeMediaContent(typed) {
					return false
				}
			case map[string]any:
				if len(typed) != 2 || typed["type"] != dto.ContentTypeText {
					return false
				}
				if _, ok := typed["text"].(string); !ok {
					return false
				}
			default:
				return false
			}
		}
		return true
	case []dto.MediaContent:
		if len(content) == 0 {
			return false
		}
		for _, item := range content {
			if !routingHedgeReplaySafeMediaContent(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func routingHedgeReplaySafeMediaContent(content dto.MediaContent) bool {
	return content.Type == dto.ContentTypeText && content.ImageUrl == nil && content.InputAudio == nil &&
		content.File == nil && content.VideoUrl == nil && routingHedgeRawFieldEmpty(content.CacheControl)
}

func routingHedgeReplaySafePrompt(prompt any) bool {
	switch value := prompt.(type) {
	case string:
		return true
	case []string:
		return len(value) > 0
	case []any:
		if len(value) == 0 {
			return false
		}
		for _, item := range value {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func routingHedgeRawFieldEmpty(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func routingHedgeHasHalfOpenProbe(c *gin.Context) bool {
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](c, constant.ContextKeyRoutingHalfOpenProbes)
	return ok && len(probes) > 0
}

func newRoutingHedgeBranch(
	source *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	channel *model.Channel,
	role channelrouting.HedgeAttemptRole,
	lease *channelrouting.HedgeAttemptLease,
	body []byte,
	responseLimit int64,
	secondary bool,
) (*routingHedgeBranch, error) {
	if source == nil || source.Request == nil || baseInfo == nil || channel == nil || lease == nil || responseLimit < 1 {
		return nil, channelrouting.ErrHedgePolicyInvalid
	}
	recorder := newRoutingHedgeResponseWriter(responseLimit)
	created, _ := gin.CreateTestContext(recorder)
	branchContext := source.Copy()
	branchContext.Writer = created.Writer
	requestContext, cancel := context.WithCancelCause(source.Request.Context())
	request := source.Request.Clone(requestContext)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	branchContext.Request = request
	branchContext.Set(common.KeyBodyStorage, nil)
	branchContext.Set(common.KeyRequestBody, nil)
	branchContext.Set("use_channel", append([]string(nil), source.GetStringSlice("use_channel")...))
	for _, key := range []constant.ContextKey{
		constant.ContextKeyChannelParamOverride, constant.ContextKeyChannelHeaderOverride,
	} {
		if overrides := common.GetContextKeyStringMap(source, key); len(overrides) > 0 {
			cloned, copyErr := common.DeepCopy(&overrides)
			if copyErr != nil {
				cancel(copyErr)
				return nil, copyErr
			}
			common.SetContextKey(branchContext, key, *cloned)
		}
	}
	common.SetContextKey(branchContext, constant.ContextKeyRoutingCapacityFailure, nil)
	common.SetContextKey(branchContext, constant.ContextKeyRoutingHalfOpenProbes, nil)
	common.SetContextKey(branchContext, constant.ContextKeyRoutingHalfOpenProbeOwners, nil)
	common.SetContextKey(branchContext, constant.ContextKeyRoutingHalfOpenLeases, nil)
	if secondary {
		common.SetContextKey(branchContext, constant.ContextKeyRoutingCapacityReserve, nil)
		common.SetContextKey(branchContext, constant.ContextKeyRoutingAdaptiveConcurrency, nil)
		common.SetContextKey(branchContext, constant.ContextKeyRoutingSelectedIdentity, nil)

		primaryAuthority := strings.TrimSpace(common.GetContextKeyString(source, constant.ContextKeyRoutingEndpointAuthority))
		primaryRegion := strings.TrimSpace(common.GetContextKeyString(source, constant.ContextKeyRoutingRegion))
		retainedEndpoints := make(map[string]channelrouting.RequestEndpointIdentity)
		if excludedEndpoints, ok := common.GetContextKeyType[map[string]channelrouting.RequestEndpointIdentity](
			source, constant.ContextKeyRoutingExcludedEndpoints,
		); ok {
			for key, identity := range excludedEndpoints {
				if strings.EqualFold(strings.TrimSpace(identity.EndpointAuthority), primaryAuthority) &&
					strings.EqualFold(strings.TrimSpace(identity.Region), primaryRegion) {
					continue
				}
				retainedEndpoints[key] = identity
			}
		}
		common.SetContextKey(branchContext, constant.ContextKeyRoutingExcludedEndpoints, retainedEndpoints)

		primaryFailureDomain := strings.ToLower(strings.TrimSpace(
			common.GetContextKeyString(source, constant.ContextKeyRoutingFailureDomainHash),
		))
		retainedFailureDomains := make(map[string]struct{})
		if excludedFailureDomains, ok := common.GetContextKeyType[map[string]struct{}](
			source, constant.ContextKeyRoutingExcludedDomains,
		); ok {
			for hash := range excludedFailureDomains {
				normalized := strings.ToLower(strings.TrimSpace(hash))
				if normalized == "" || normalized == primaryFailureDomain {
					continue
				}
				retainedFailureDomains[normalized] = struct{}{}
			}
		}
		common.SetContextKey(branchContext, constant.ContextKeyRoutingExcludedDomains, retainedFailureDomains)
	}
	info, err := cloneRoutingHedgeRelayInfo(baseInfo)
	if err != nil {
		cancel(err)
		return nil, err
	}
	if secondary {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}
	return &routingHedgeBranch{
		role: role, ctx: branchContext, info: info, channel: channel,
		recorder: recorder, cancel: cancel, lease: lease,
	}, nil
}

func cloneRoutingHedgeRelayInfo(base *relaycommon.RelayInfo) (*relaycommon.RelayInfo, error) {
	if base == nil {
		return nil, channelrouting.ErrHedgePolicyInvalid
	}
	clone := *base
	request, ok := base.Request.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, channelrouting.ErrHedgePolicyInvalid
	}
	requestCopy, err := common.DeepCopy(request)
	if err != nil {
		return nil, err
	}
	priceCopy, err := common.DeepCopy(&base.PriceData)
	if err != nil {
		return nil, err
	}
	clone.Request = requestCopy
	clone.PriceData = *priceCopy
	clone.ChannelMeta = nil
	clone.StreamStatus = nil
	clone.ClientWs = nil
	clone.TargetWs = nil
	clone.ClaudeConvertInfo = nil
	clone.RerankerInfo = nil
	clone.ResponsesUsageInfo = nil
	clone.TaskRelayInfo = nil
	clone.LastError = nil
	clone.RequestHeaders = cloneRoutingHedgeStringMap(base.RequestHeaders)
	clone.RequestConversionChain = append([]types.RelayFormat(nil), base.RequestConversionChain...)
	clone.ParamOverrideAudit = append([]string(nil), base.ParamOverrideAudit...)
	if base.TieredBillingSnapshot != nil {
		snapshot := *base.TieredBillingSnapshot
		clone.TieredBillingSnapshot = &snapshot
	}
	if base.BillingRequestInput != nil {
		input := *base.BillingRequestInput
		input.Body = append([]byte(nil), base.BillingRequestInput.Body...)
		input.Headers = cloneRoutingHedgeStringMap(base.BillingRequestInput.Headers)
		clone.BillingRequestInput = &input
	}
	if base.RuntimeHeadersOverride != nil {
		runtimeHeaders, copyErr := common.DeepCopy(&base.RuntimeHeadersOverride)
		if copyErr != nil {
			return nil, copyErr
		}
		clone.RuntimeHeadersOverride = *runtimeHeaders
	}
	return &clone, nil
}

func cloneRoutingHedgeStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func prepareRoutingHedgeSecondary(
	source *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	primaryChannel *model.Channel,
	retryParam *service.RetryParam,
	coordinator *channelrouting.HedgeCoordinator,
	primaryCost channelrouting.ShadowCostInput,
	body []byte,
	responseLimit int64,
) (*routingHedgeBranch, channelrouting.ShadowCostInput, error) {
	placeholderLease := &channelrouting.HedgeAttemptLease{}
	secondary, err := newRoutingHedgeBranch(
		source, baseInfo, primaryChannel, channelrouting.HedgeAttemptSecondary,
		placeholderLease, body, responseLimit, true,
	)
	if err != nil {
		return nil, channelrouting.ShadowCostInput{}, err
	}
	secondaryParam := &service.RetryParam{
		Ctx: secondary.ctx, TokenGroup: retryParam.TokenGroup, ModelName: retryParam.ModelName,
		RequestPath: retryParam.RequestPath, Retry: common.GetPointer(retryParam.GetRetry()),
	}
	selected, apiErr := getChannel(secondary.ctx, secondary.info, secondaryParam)
	if apiErr != nil {
		abortRoutingHedgeBranchBeforeSend(secondary)
		return nil, channelrouting.ShadowCostInput{}, apiErr
	}
	secondary.channel = selected
	if !routingHedgeSecondaryChannelEligible(source, secondary.ctx, baseInfo, selected) {
		abortRoutingHedgeBranchBeforeSend(secondary)
		return nil, channelrouting.ShadowCostInput{}, channelrouting.ErrHedgeTargetNotDistinct
	}
	secondaryCost, costKnown, err := service.ChannelRoutingHedgeCostEstimate(
		secondary.ctx, selected.Id, baseInfo.OriginModelName, retryParam.RequestPath, retryParam.GetRetry(),
	)
	if err != nil || !costKnown {
		abortRoutingHedgeBranchBeforeSend(secondary)
		if err == nil {
			err = channelrouting.ErrHedgeCostBudgetExceeded
		}
		return nil, channelrouting.ShadowCostInput{}, err
	}
	targetDistinct := routingHedgeTargetsHaveDistinctFailureDomain(source, secondary.ctx, primaryCost, secondaryCost)
	lease, err := coordinator.BeginSecondary(secondaryCost.WorstCaseCost, targetDistinct)
	if err != nil {
		abortRoutingHedgeBranchBeforeSend(secondary)
		return nil, channelrouting.ShadowCostInput{}, err
	}
	secondary.lease = lease
	return secondary, secondaryCost, nil
}

func routingHedgeSecondaryChannelEligible(
	primary *gin.Context,
	secondary *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	channel *model.Channel,
) bool {
	if primary == nil || secondary == nil || baseInfo == nil || channel == nil ||
		channel.Type != constant.ChannelTypeOpenAI ||
		channel.Id == common.GetContextKeyInt(primary, constant.ContextKeyChannelId) ||
		common.GetContextKeyBool(secondary, constant.ContextKeyChannelIsMultiKey) ||
		!service.HasRoutingStrictCapacityReservation(secondary) || routingHedgeHasHalfOpenProbe(secondary) ||
		common.GetContextKeyInt(primary, constant.ContextKeyRoutingPoolID) !=
			common.GetContextKeyInt(secondary, constant.ContextKeyRoutingPoolID) {
		return false
	}
	primaryRevision, _ := common.GetContextKeyType[uint64](primary, constant.ContextKeyRoutingSnapshotRevision)
	secondaryRevision, _ := common.GetContextKeyType[uint64](secondary, constant.ContextKeyRoutingSnapshotRevision)
	if primaryRevision == 0 || primaryRevision != secondaryRevision ||
		common.GetContextKeyString(primary, constant.ContextKeyRoutingRegion) !=
			common.GetContextKeyString(secondary, constant.ContextKeyRoutingRegion) {
		return false
	}
	primaryAuthority := common.GetContextKeyString(primary, constant.ContextKeyRoutingEndpointAuthority)
	secondaryAuthority := common.GetContextKeyString(secondary, constant.ContextKeyRoutingEndpointAuthority)
	primaryCredentialID := service.RoutingHedgeCredentialID(primary)
	secondaryCredentialID := service.RoutingHedgeCredentialID(secondary)
	if primaryAuthority == "" || secondaryAuthority == "" ||
		primaryCredentialID <= 0 || secondaryCredentialID <= 0 || primaryCredentialID == secondaryCredentialID {
		return false
	}
	settings, _ := common.GetContextKeyType[dto.ChannelSettings](secondary, constant.ContextKeyChannelSetting)
	return !model_setting.GetGlobalSettings().PassThroughRequestEnabled && !settings.PassThroughBodyEnabled &&
		!service.ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, baseInfo.OriginModelName) &&
		len(common.GetContextKeyStringMap(secondary, constant.ContextKeyChannelParamOverride)) == 0
}

func routingHedgeTargetsHaveDistinctFailureDomain(
	primary *gin.Context,
	secondary *gin.Context,
	primaryCost channelrouting.ShadowCostInput,
	secondaryCost channelrouting.ShadowCostInput,
) bool {
	if primary == nil || secondary == nil {
		return false
	}
	primaryAuthority := common.GetContextKeyString(primary, constant.ContextKeyRoutingEndpointAuthority)
	secondaryAuthority := common.GetContextKeyString(secondary, constant.ContextKeyRoutingEndpointAuthority)
	primaryCredentialID := service.RoutingHedgeCredentialID(primary)
	secondaryCredentialID := service.RoutingHedgeCredentialID(secondary)
	if primaryCredentialID <= 0 || secondaryCredentialID <= 0 || primaryCredentialID == secondaryCredentialID {
		return false
	}
	if primaryAuthority != "" && secondaryAuthority != "" && primaryAuthority != secondaryAuthority {
		return true
	}
	return primaryCost.FailureDomainHash != "" && secondaryCost.FailureDomainHash != "" &&
		primaryCost.FailureDomainHash != secondaryCost.FailureDomainHash
}

func executeRoutingHedgeBranch(branch *routingHedgeBranch) routingHedgeBranchResult {
	defer branch.lease.Finish()
	defer common.CleanupBodyStorage(branch.ctx)
	releaseInflight := routingmetrics.BeginInflight(branch.ctx, branch.info, branch.channel.Id)
	defer releaseInflight()
	defer releaseRoutingCapacityReservation(branch.ctx)
	defer finishRoutingCanaryAttempt(branch.ctx, branch.send)
	defer relaycommon.ClearRoutingUpstreamSendState(branch.ctx)
	defer service.ReleaseRoutingHalfOpenProbe(
		branch.ctx, branch.channel.Id, branch.info.OriginModelName, branch.info.UsingGroup,
	)
	capture, apiErr := routingHedgeExecuteText(branch.ctx, branch.info)
	overflow := branch.recorder.Overflowed()
	if overflow {
		apiErr = types.NewErrorWithStatusCode(
			errRoutingHedgeResponseTooLarge, types.ErrorCodeBadResponse,
			http.StatusBadGateway, types.ErrOptionWithSkipRetry(),
		)
	}
	if capacityFailure := service.RoutingCapacityReservationFailure(branch.ctx); capacityFailure != nil {
		apiErr = types.NewErrorWithStatusCode(
			capacityFailure, types.ErrorCodeGetChannelFailed,
			http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		)
	}
	requestCause := context.Cause(branch.ctx.Request.Context())
	if errors.Is(requestCause, channelrouting.ErrStrictCapacityUnavailable) {
		apiErr = types.NewErrorWithStatusCode(
			requestCause, types.ErrorCodeGetChannelFailed,
			http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		)
	}
	// The branch writer is an internal buffer. Nothing is client-committed until
	// commitRoutingHedgeWinner copies the selected response to the real writer.
	controlErr := relayAttemptControlErrorWithClientCommit(apiErr, branch.info, false)
	if controlErr == nil && !routingHedgeBranchSent(branch) {
		controlErr = routingHedgeInternalError(
			"upstream send boundary",
			errors.New("hedge branch completed without crossing the upstream send boundary"),
		)
	}
	classificationErr := apiErr
	if classificationErr == nil {
		classificationErr = controlErr
	}
	classification, success := classifyRoutingRelayAttemptAtCommitBoundary(classificationErr, branch.info, true)
	return routingHedgeBranchResult{
		branch: branch, capture: capture, apiErr: controlErr,
		classification: classification, success: success && controlErr == nil,
		cause: requestCause, overflow: overflow,
		completedAtMs: time.Now().UnixMilli(),
	}
}

func waitRoutingHedgePrimaryOrDelay(
	primary <-chan routingHedgeBranchResult,
	delay time.Duration,
) (routingHedgeBranchResult, bool) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case result := <-primary:
		return result, true
	case <-timer.C:
		return routingHedgeBranchResult{}, false
	}
}

func collectRoutingHedgeActiveResults(
	primaryResults <-chan routingHedgeBranchResult,
	secondaryResults <-chan routingHedgeBranchResult,
	primary *routingHedgeBranch,
	secondary *routingHedgeBranch,
) routingHedgeActiveCollection {
	results := make(map[channelrouting.HedgeAttemptRole]routingHedgeBranchResult, 2)
	var winner *routingHedgeBranchResult
	for len(results) < 2 {
		var result routingHedgeBranchResult
		select {
		case result = <-primaryResults:
		case result = <-secondaryResults:
		}
		results[result.branch.role] = result
		var otherResults <-chan routingHedgeBranchResult
		var otherBranch *routingHedgeBranch
		if result.branch.role == channelrouting.HedgeAttemptPrimary {
			otherResults = secondaryResults
			otherBranch = secondary
		} else {
			otherResults = primaryResults
			otherBranch = primary
		}
		if routingHedgeIsSafetyRejection(result) {
			otherBranch.cancel(channelrouting.ErrHedgeLost)
			terminal := result
			return routingHedgeActiveCollection{
				results: results, terminal: &terminal, pending: otherResults, pendingBranch: otherBranch,
			}
		}
		if winner == nil && result.success {
			select {
			case ready := <-otherResults:
				results[ready.branch.role] = ready
				if routingHedgeIsSafetyRejection(ready) {
					terminal := ready
					return routingHedgeActiveCollection{results: results, terminal: &terminal}
				}
			default:
			}
			if !result.branch.lease.TryWin() {
				continue
			}
			winnerCopy := result
			winner = &winnerCopy
			if len(results) == 2 {
				return routingHedgeActiveCollection{results: results, winner: winner}
			}
			otherBranch.cancel(channelrouting.ErrHedgeLost)
			return routingHedgeActiveCollection{
				results: results, winner: winner, pending: otherResults, pendingBranch: otherBranch,
			}
		}
	}
	return routingHedgeActiveCollection{results: results, winner: winner}
}

func routingHedgeIsSafetyRejection(result routingHedgeBranchResult) bool {
	return !result.success && result.classification.Responsibility == routingerror.ResponsibilityCaller &&
		result.classification.Retryability == routingerror.RetryNever &&
		result.classification.Rule == "content_safety"
}

func finishRoutingHedgeLoserAsync(
	result <-chan routingHedgeBranchResult,
	loser *routingHedgeBranch,
	winner *routingHedgeBranchResult,
	slot *channelrouting.HedgeSlot,
	bufferSlot *channelrouting.HedgeByteSlot,
	timeout time.Duration,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if slot != nil {
			defer slot.Release()
		}
		if bufferSlot != nil {
			defer bufferSlot.Release()
		}
		if timeout <= 0 {
			timeout = routingHedgeLoserCleanupTimeout
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		var loserResult routingHedgeBranchResult
		select {
		case loserResult = <-result:
		case <-timer.C:
			loser.cancel(channelrouting.ErrHedgeLost)
			logger.LogWarn(loser.ctx, "channel routing hedge loser ignored cancellation; resources remain reserved until exit")
			loserResult = <-result
		}
		if err := completeRoutingHedgeResultAudit(loserResult, winner, routingHedgeAuditDisposition{}); err != nil {
			logger.LogError(loserResult.branch.ctx, fmt.Sprintf("channel routing hedge loser audit failed: %v", err))
		}
		recordRoutingHedgeAttemptEffects(loserResult, winner)
	}()
	return done
}

func finishSingleRoutingHedgeResult(
	c *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	retryParam *service.RetryParam,
	attemptLease *channelrouting.AttemptLease,
	result routingHedgeBranchResult,
) (routingHedgeOutcome, bool) {
	promoteRoutingHedgeRelayInfo(baseInfo, result.branch)
	if result.success && result.branch.lease.TryWin() {
		return finishRoutingHedgeWinnerResult(c, baseInfo, retryParam, attemptLease, result), true
	}
	recordRoutingHedgeAttemptEffects(result, nil)
	mergeRoutingHedgeTargetExclusions(c, result.branch)
	apiErr := result.apiErr
	if apiErr == nil {
		apiErr = routingHedgeInternalError("primary attempt failed without a relay error", errors.New("empty primary hedge failure"))
	}
	return finishRoutingHedgeFailure(
		c, baseInfo, retryParam, apiErr, result.classification,
		func(willRetry bool) {
			disposition := routingHedgeAuditDisposition{
				willRetry: willRetry, finalAttempt: !willRetry, completedAtMs: time.Now().UnixMilli(),
			}
			if err := completeRoutingHedgeResultAudit(result, &result, disposition); err != nil {
				logger.LogError(c, fmt.Sprintf("channel routing hedge result audit failed: %v", err))
			}
		},
	), true
}

func finishRoutingHedgeWinnerResult(
	c *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	retryParam *service.RetryParam,
	attemptLease *channelrouting.AttemptLease,
	result routingHedgeBranchResult,
) routingHedgeOutcome {
	commitErr := commitRoutingHedgeWinner(c, baseInfo, attemptLease, result)
	clientCommitted := c != nil && c.Writer != nil && c.Writer.Written()
	recordRoutingHedgeAttemptEffects(result, &result)
	if commitErr != nil {
		classification, _ := classifyRoutingRelayAttemptWithContext(c, commitErr, baseInfo)
		return finishRoutingHedgeFailure(
			c, baseInfo, retryParam, commitErr, classification,
			func(willRetry bool) {
				disposition := routingHedgeAuditDisposition{
					clientCommitted: clientCommitted,
					willRetry:       willRetry,
					finalAttempt:    !willRetry,
					completedAtMs:   time.Now().UnixMilli(),
					finalErr:        commitErr,
					classification:  classification,
				}
				if err := completeRoutingHedgeResultAudit(result, &result, disposition); err != nil {
					logger.LogError(c, fmt.Sprintf("channel routing hedge winner audit failed: %v", err))
				}
			},
		)
	}
	disposition := routingHedgeAuditDisposition{
		clientCommitted: clientCommitted,
		finalAttempt:    true,
		completedAtMs:   time.Now().UnixMilli(),
	}
	if err := completeRoutingHedgeResultAudit(result, &result, disposition); err != nil {
		logger.LogError(c, fmt.Sprintf("channel routing hedge winner audit failed: %v", err))
	}
	return routingHedgeOutcome{success: true}
}

func finishRoutingHedgeFailure(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	retryParam *service.RetryParam,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
	finalizeAudit func(bool),
) routingHedgeOutcome {
	apiErr = service.NormalizeViolationFeeError(apiErr)
	remaining := 0
	if retryParam != nil {
		remaining = common.RetryTimes - retryParam.GetRetry()
	}
	willRetry := shouldRetry(c, info, apiErr, classification, remaining)
	if finalizeAudit != nil {
		finalizeAudit(willRetry)
	}
	return routingHedgeOutcome{
		apiErr: apiErr, classification: classification,
		retryDecided: true, willRetry: willRetry,
	}
}

func startRoutingHedgeBranchAudit(
	branch *routingHedgeBranch,
	cost channelrouting.ShadowCostInput,
) (*channelrouting.HedgeAttemptAuditReservation, error) {
	role := model.RoutingHedgeAttemptRolePrimary
	if branch.role == channelrouting.HedgeAttemptSecondary {
		role = model.RoutingHedgeAttemptRoleSecondary
	}
	stableNodeID, stableNodeKnown := channelrouting.StableNodeID()
	algorithmVersion := common.GetContextKeyString(branch.ctx, constant.ContextKeyRoutingAlgorithmVersion)
	if algorithmVersion == "" {
		algorithmVersion = channelrouting.DecisionAlgorithmBalanced
	}
	return channelrouting.ReserveUpstreamAttemptAudit(model.RoutingHedgeAttemptStartSpec{
		DecisionID:       common.GetContextKeyString(branch.ctx, constant.ContextKeyRoutingDecisionID),
		RequestID:        branch.info.RequestId,
		NodeEpochID:      channelrouting.NodeEpochID(),
		StableNodeID:     stableNodeID,
		StableNodeKnown:  stableNodeKnown,
		PolicyRevision:   service.RoutingHedgePolicyRevision(branch.ctx),
		AlgorithmVersion: algorithmVersion,
		PoolID:           service.RoutingHedgePoolID(branch.ctx),
		MemberID:         service.RoutingHedgeMemberID(branch.ctx),
		ChannelID:        branch.channel.Id,
		CredentialID:     service.RoutingHedgeCredentialID(branch.ctx),
		ModelName:        branch.info.OriginModelName,
		ExecutionMode:    model.RoutingAttemptExecutionHedge,
		AttemptIndex:     branch.info.RetryIndex, Role: role,
		EndpointAuthority: common.GetContextKeyString(branch.ctx, constant.ContextKeyRoutingEndpointAuthority),
		Region:            common.GetContextKeyString(branch.ctx, constant.ContextKeyRoutingRegion),
		StartedTimeMs:     time.Now().UnixMilli(),
		Cost:              routingAttemptAuditCostSpec(cost, true),
	})
}

func completeRoutingHedgeAdmissionFailure(
	branch *routingHedgeBranch,
	apiErr *types.NewAPIError,
	willRetry bool,
	finalAttempt bool,
) {
	if branch == nil || branch.audit == nil {
		return
	}
	classification, _ := classifyRoutingRelayAttemptWithContext(branch.ctx, apiErr, branch.info)
	err := branch.audit.Complete(model.RoutingHedgeAttemptCompleteSpec{
		Result:       model.RoutingHedgeAttemptResultInternalError,
		WillRetry:    willRetry,
		FinalAttempt: finalAttempt,
		HTTPStatus:   sourceRoutingHedgeStatus(apiErr), ErrorClassification: classification.Rule,
		ErrorResponsibility: string(classification.Responsibility),
		ErrorRetryability:   string(classification.Retryability), ErrorCode: routingHedgeErrorCode(apiErr),
		CompletedTimeMs: time.Now().UnixMilli(),
	})
	if err != nil {
		logger.LogError(branch.ctx, fmt.Sprintf("channel routing hedge admission audit completion failed: %v", err))
	}
}

func completeRoutingHedgeBranchAsLost(branch *routingHedgeBranch, completedAtMs int64) {
	if branch == nil || branch.audit == nil {
		return
	}
	err := branch.audit.Complete(model.RoutingHedgeAttemptCompleteSpec{
		Result: model.RoutingHedgeAttemptResultHedgeLost, UpstreamSent: routingHedgeBranchSent(branch),
		CompletedTimeMs: completedAtMs,
	})
	if err != nil {
		logger.LogError(branch.ctx, fmt.Sprintf("channel routing hedge neutral audit completion failed: %v", err))
	}
}

func completeRoutingHedgeResultAudits(
	primary routingHedgeBranchResult,
	secondary routingHedgeBranchResult,
	selected *routingHedgeBranchResult,
	disposition routingHedgeAuditDisposition,
) error {
	return errors.Join(
		completeRoutingHedgeResultAudit(primary, selected, disposition),
		completeRoutingHedgeResultAudit(secondary, selected, disposition),
	)
}

func completeRoutingHedgeResultAudit(
	result routingHedgeBranchResult,
	selected *routingHedgeBranchResult,
	disposition routingHedgeAuditDisposition,
) error {
	if result.branch == nil || result.branch.audit == nil {
		return model.ErrRoutingHedgeAttemptInvalid
	}
	isSelected := selected != nil && selected.branch == result.branch
	isWinner := isSelected && result.success && disposition.finalErr == nil
	completedAtMs := result.completedAtMs
	if isSelected && disposition.completedAtMs > 0 {
		completedAtMs = disposition.completedAtMs
	}
	completion := model.RoutingHedgeAttemptCompleteSpec{
		Winner: isWinner, UpstreamSent: routingHedgeBranchSent(result.branch),
		ClientCommitted: isSelected && disposition.clientCommitted,
		WillRetry:       isSelected && disposition.willRetry,
		FinalAttempt:    isSelected && disposition.finalAttempt,
		HTTPStatus:      result.branch.recorder.StatusCode(), CompletedTimeMs: completedAtMs,
	}
	if firstByte := result.branch.info.FirstResponseTime; !firstByte.IsZero() {
		completion.FirstByteTimeMs = firstByte.UnixMilli()
	}
	switch {
	case isSelected && disposition.finalErr != nil:
		completion.Result = model.RoutingHedgeAttemptResultInternalError
		completion.HTTPStatus = sourceRoutingHedgeStatus(disposition.finalErr)
		completion.ErrorClassification = disposition.classification.Rule
		completion.ErrorResponsibility = string(disposition.classification.Responsibility)
		completion.ErrorRetryability = string(disposition.classification.Retryability)
		completion.ErrorCode = routingHedgeErrorCode(disposition.finalErr)
	case isWinner:
		completion.Result = model.RoutingHedgeAttemptResultSuccess
	case result.success || errors.Is(result.cause, channelrouting.ErrHedgeLost):
		completion.Result = model.RoutingHedgeAttemptResultHedgeLost
	case errors.Is(result.cause, context.Canceled) || errors.Is(result.cause, context.DeadlineExceeded):
		completion.Result = model.RoutingHedgeAttemptResultClientCanceled
	case result.overflow:
		completion.Result = model.RoutingHedgeAttemptResultResponseTooLarge
	default:
		completion.Result = model.RoutingHedgeAttemptResultUpstreamError
	}
	if disposition.finalErr == nil && !result.success && !errors.Is(result.cause, channelrouting.ErrHedgeLost) {
		completion.HTTPStatus = sourceRoutingHedgeStatus(result.apiErr)
		completion.ErrorClassification = result.classification.Rule
		completion.ErrorResponsibility = string(result.classification.Responsibility)
		completion.ErrorRetryability = string(result.classification.Retryability)
		completion.ErrorCode = routingHedgeErrorCode(result.apiErr)
	}
	if result.capture != nil && result.capture.Usage != nil {
		actual, err := service.ChannelRoutingHedgeActualCost(
			result.branch.ctx,
			result.branch.channel.Id,
			result.branch.info.OriginModelName,
			result.branch.info.RequestURLPath,
			result.branch.info.RetryIndex,
			result.capture.Usage,
		)
		if err != nil {
			logger.LogWarn(result.branch.ctx, fmt.Sprintf("channel routing hedge actual cost unavailable: %v", err))
		} else if actual.Known {
			completion.ActualCostKnown = true
			completion.ActualCost = actual.Cost
			completion.ActualPromptTokens = actual.PromptTokens
			completion.ActualCompletionTokens = actual.CompletionTokens
			completion.ActualTotalTokens = actual.TotalTokens
			completion.ActualCacheReadTokens = actual.CacheReadTokens
			completion.ActualCacheWriteTokens = actual.CacheWriteTokens
			completion.ActualCacheWrite1hTokens = actual.CacheWrite1hTokens
		}
	}
	return result.branch.audit.Complete(completion)
}

func recordRoutingHedgeAttemptEffects(
	result routingHedgeBranchResult,
	winner *routingHedgeBranchResult,
) {
	if result.branch == nil || errors.Is(result.cause, channelrouting.ErrHedgeLost) ||
		!routingHedgeBranchSent(result.branch) ||
		(result.success && (winner == nil || winner.branch != result.branch)) {
		return
	}
	apiErr := service.NormalizeViolationFeeError(result.apiErr)
	recordRoutingAttemptEffects(
		result.branch.ctx, result.branch.info, result.branch.channel.Id,
		result.success, apiErr, result.classification,
	)
	if result.success || apiErr == nil {
		return
	}
	service.MarkRoutingTargetFailure(result.branch.ctx, result.branch.channel.Id, result.classification.Scope)
	channel := result.branch.channel
	processChannelError(
		result.branch.ctx,
		*types.NewChannelError(
			channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
			common.GetContextKeyString(result.branch.ctx, constant.ContextKeyChannelKey), channel.GetAutoBan(),
		),
		apiErr,
		result.classification,
	)
}

func routingHedgeBranchSent(branch *routingHedgeBranch) bool {
	return branch != nil && branch.send != nil && branch.send.Sent()
}

func commitRoutingHedgeWinner(
	c *gin.Context,
	baseInfo *relaycommon.RelayInfo,
	attemptLease *channelrouting.AttemptLease,
	result routingHedgeBranchResult,
) *types.NewAPIError {
	if result.branch == nil || result.capture == nil || result.branch.recorder.Overflowed() {
		return routingHedgeInternalError("winner response capture", errRoutingHedgeResponseTooLarge)
	}
	var transitionErr error
	if attemptLease != nil {
		transitionErr = attemptLease.MarkClientCommitted()
	}
	routingHedgePromoteContext(c, result.branch.ctx)
	commitErr := result.branch.recorder.CommitTo(c.Writer)
	finalizeErr := routingHedgeFinalizeText(result.branch.ctx, result.branch.info, result.capture)
	promoteRoutingHedgeRelayInfo(baseInfo, result.branch)
	if transitionErr != nil {
		return routingHedgeInternalError("winner client commit transition", transitionErr)
	}
	if commitErr != nil {
		return routingHedgeInternalError("winner response commit", commitErr)
	}
	if finalizeErr != nil {
		return routingHedgeInternalError("winner billing finalization", finalizeErr)
	}
	return nil
}

func promoteRoutingHedgeRelayInfo(baseInfo *relaycommon.RelayInfo, branch *routingHedgeBranch) {
	if baseInfo == nil || branch == nil || branch.info == nil {
		return
	}
	*baseInfo = *branch.info
}

func routingHedgePromoteContext(target *gin.Context, source *gin.Context) {
	keys := []constant.ContextKey{
		constant.ContextKeyChannelId, constant.ContextKeyChannelName, constant.ContextKeyChannelCreateTime,
		constant.ContextKeyChannelBaseUrl, constant.ContextKeyChannelType, constant.ContextKeyChannelSetting,
		constant.ContextKeyChannelOtherSetting, constant.ContextKeyChannelParamOverride,
		constant.ContextKeyChannelHeaderOverride, constant.ContextKeyChannelOrganization,
		constant.ContextKeyChannelAutoBan, constant.ContextKeyChannelModelMapping,
		constant.ContextKeyChannelStatusCodeMapping, constant.ContextKeyChannelIsMultiKey,
		constant.ContextKeyChannelMultiKeyIndex, constant.ContextKeyChannelKey,
		constant.ContextKeyRoutingSnapshotRevision, constant.ContextKeyRoutingGeneration,
		constant.ContextKeyRoutingPoolID,
		constant.ContextKeyRoutingMemberID, constant.ContextKeyRoutingCredentialID,
		constant.ContextKeyRoutingFailureDomainHash,
		constant.ContextKeyRoutingEndpointHost, constant.ContextKeyRoutingEndpointAuthority,
		constant.ContextKeyRoutingRegion, constant.ContextKeyRoutingAdaptiveConcurrency,
		constant.ContextKeySystemPromptOverride,
	}
	for _, key := range keys {
		if value, exists := common.GetContextKey(source, key); exists {
			common.SetContextKey(target, key, value)
		}
	}
	for _, key := range []string{"api_version", "region", "plugin", "bot_id"} {
		if value, exists := source.Get(key); exists {
			target.Set(key, value)
		}
	}
	mergeRoutingHedgeUsedChannels(target, source)
	service.MergeRoutingTargetExclusions(target, source)
}

func mergeRoutingHedgeTargetExclusions(target *gin.Context, branch *routingHedgeBranch) {
	if branch == nil {
		return
	}
	service.MergeRoutingTargetExclusions(target, branch.ctx)
}

func mergeRoutingHedgeUsedChannels(target *gin.Context, source *gin.Context) {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, values := range [][]string{target.GetStringSlice("use_channel"), source.GetStringSlice("use_channel")} {
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	target.Set("use_channel", merged)
}

func abortRoutingHedgeBranchBeforeSend(branch *routingHedgeBranch) {
	if branch == nil {
		return
	}
	branch.cancel(channelrouting.ErrHedgeLost)
	cancelRoutingCapacityReservation(branch.ctx)
	service.ReleaseAllRoutingHalfOpenProbes(branch.ctx)
	if branch.lease != nil {
		branch.lease.Finish()
	}
}

func sourceRoutingHedgeStatus(apiErr *types.NewAPIError) int {
	if apiErr == nil {
		return 0
	}
	status := apiErr.SourceStatusCode()
	if status < 100 || status > 599 {
		status = apiErr.StatusCode
	}
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func routingHedgeErrorCode(apiErr *types.NewAPIError) string {
	if apiErr == nil {
		return ""
	}
	return string(apiErr.GetErrorCode())
}

func routingHedgeInternalError(operation string, err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		fmt.Errorf("channel routing hedge %s failed: %w", operation, err),
		types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithHideErrMsg("channel routing hedge is temporarily unavailable"),
	)
}

func newRoutingHedgeResponseWriter(limit int64) *routingHedgeResponseWriter {
	return &routingHedgeResponseWriter{header: make(http.Header), limit: limit}
}

func (writer *routingHedgeResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *routingHedgeResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}

func (writer *routingHedgeResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if writer.overflow || int64(writer.buffer.Len())+int64(len(data)) > writer.limit {
		writer.overflow = true
		return 0, errRoutingHedgeResponseTooLarge
	}
	return writer.buffer.Write(data)
}

func (writer *routingHedgeResponseWriter) Flush() {}

func (writer *routingHedgeResponseWriter) StatusCode() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func (writer *routingHedgeResponseWriter) Overflowed() bool {
	return writer == nil || writer.overflow
}

func (writer *routingHedgeResponseWriter) CommitTo(target gin.ResponseWriter) error {
	if writer == nil || target == nil || writer.overflow {
		return errRoutingHedgeResponseTooLarge
	}
	for key, values := range writer.header {
		target.Header()[key] = append([]string(nil), values...)
	}
	target.WriteHeader(writer.StatusCode())
	written, err := target.Write(writer.buffer.Bytes())
	if err == nil && written != writer.buffer.Len() {
		err = io.ErrShortWrite
	}
	return err
}
