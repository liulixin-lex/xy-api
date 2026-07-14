package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	defer service.ReleaseAllRoutingHalfOpenProbes(c)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			writeRelayAPIErrorResponse(c, relayFormat, ws, newAPIError)
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewErrorWithStatusCode(
				err,
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}
		return
	}
	validatedModelName := validatedRoutingModelName(c, relayFormat, request)
	if validatedModelName == "" {
		newAPIError = types.NewErrorWithStatusCode(
			errors.New("model is required"),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
		return
	}
	c.Set("original_model", validatedModelName)

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}
	var (
		canaryOutcomeKnown          bool
		canaryOutcomeSuccess        bool
		canaryOutcomeClassification routingerror.Classification
	)
	defer func() {
		success := newAPIError == nil
		if canaryOutcomeKnown {
			success = canaryOutcomeSuccess
		} else if newAPIError != nil {
			canaryOutcomeClassification, _ = classifyRoutingRelayAttemptWithContext(c, newAPIError, relayInfo)
		}
		finishRoutingCanaryOutcome(c, relayInfo, canaryOutcomeClassification, success)
	}()

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)
	outputTokens := max(common.GetContextKeyInt(c, constant.ContextKeyRoutingEstimatedOutput), 0)
	if meta != nil && meta.MaxTokens > 0 {
		outputTokens = meta.MaxTokens
	}
	requestProfile, profileErr := buildRoutingRequestProfileTemplate(
		c,
		relayFormat,
		request,
		validatedModelName,
	)
	if profileErr != nil {
		newAPIError = types.NewErrorWithStatusCode(
			profileErr,
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
		return
	}
	applyValidatedRoutingTokenEstimate(
		c,
		relayFormat,
		max(tokens, 0),
		max(outputTokens, 0),
		meta != nil && meta.MaxTokens > 0,
		requestProfile,
	)
	common.SetContextKey(c, constant.ContextKeyRoutingRequestProfile, requestProfile)
	if common.GetContextKeyBool(c, constant.ContextKeyRoutingSelectionDeferred) {
		if _, selectionErr := middleware.SelectChannelForValidatedRequest(c, validatedModelName); selectionErr != nil {
			newAPIError = selectionErr
			return
		}
	}

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", relayInfo.OriginModelName))
	} else {
		newAPIError = service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
		if newAPIError != nil {
			return
		}
	}

	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()
	if relayFormat == types.RelayFormatOpenAIRealtime {
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			newAPIError = types.NewErrorWithStatusCode(
				fmt.Errorf("upgrade realtime websocket: %w", err),
				types.ErrorCodeGetChannelFailed,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
			return
		}
		relayInfo.ClientWs = ws
		defer ws.Close()
	}

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil
	attemptGuard := newRoutingAttemptGuard(c, relayInfo)
	defer attemptGuard.Complete()

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel, channelErr := getChannel(c, relayInfo, retryParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			newAPIError = channelErr
			canaryOutcomeClassification, _ = classifyRoutingRelayAttemptWithContext(c, newAPIError, relayInfo)
			break
		}

		attemptLease, attemptErr := attemptGuard.Begin(c, relayInfo)
		if attemptErr != nil {
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)
			logger.LogWarn(c, fmt.Sprintf("channel routing retry budget rejected attempt: %v", attemptErr))
			if newAPIError == nil {
				newAPIError = routingAttemptRejectionError(attemptErr)
			}
			break
		}
		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			attemptLease.Finish()
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			canaryOutcomeClassification, _ = classifyRoutingRelayAttemptWithContext(c, newAPIError, relayInfo)
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)
		if hedgeOutcome, handled := maybeExecuteRoutingHedge(
			c, relayInfo, channel, retryParam, attemptLease, bodyStorage,
		); handled {
			canaryOutcomeKnown = true
			canaryOutcomeSuccess = hedgeOutcome.success
			canaryOutcomeClassification = hedgeOutcome.classification
			if hedgeOutcome.success {
				newAPIError = nil
				relayInfo.LastError = nil
				return
			}
			newAPIError = hedgeOutcome.apiErr
			relayInfo.LastError = newAPIError
			willRetry := hedgeOutcome.willRetry
			if !hedgeOutcome.retryDecided {
				willRetry = shouldRetry(
					c, relayInfo, newAPIError, hedgeOutcome.classification,
					common.RetryTimes-retryParam.GetRetry(),
				)
			}
			if !willRetry {
				break
			}
			continue
		}
		if capacityErr := commitRoutingCapacityAttempt(c); capacityErr != nil {
			attemptLease.Finish()
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)
			newAPIError = capacityErr
			canaryOutcomeClassification, _ = classifyRoutingRelayAttemptWithContext(c, newAPIError, relayInfo)
			break
		}

		prepareRoutingRelayAttempt(relayInfo)
		sendState := bindRoutingUpstreamAttempt(c, relayInfo, channel, attemptLease)
		serialAudit, auditErr := reserveRoutingSerialAttemptAudit(c, relayInfo, channel)
		if auditErr != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing serial attempt audit unavailable: %v", auditErr))
		}
		releaseInflight := routingmetrics.BeginInflight(c, relayInfo, channel.Id)
		func() {
			defer releaseRoutingCapacityReservation(c)
			defer releaseInflight()
			defer finishRoutingCanaryAttempt(c, sendState)
			defer relaycommon.ClearRoutingUpstreamSendState(c)
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				newAPIError = relay.WssHelper(c, relayInfo)
			case types.RelayFormatClaude:
				newAPIError = relay.ClaudeHelper(c, relayInfo)
			case types.RelayFormatGemini:
				newAPIError = geminiRelayHandler(c, relayInfo)
			default:
				newAPIError = relayHandler(c, relayInfo)
			}
		}()
		attemptAPIError := newAPIError
		if attemptAPIError == nil && !sendState.Sent() {
			attemptAPIError = types.NewErrorWithStatusCode(
				errors.New("relay completed without crossing the upstream send boundary"),
				types.ErrorCodeDoRequestFailed,
				http.StatusInternalServerError,
				types.ErrOptionWithSkipRetry(),
			)
			newAPIError = attemptAPIError
		}
		if capacityFailure := service.RoutingCapacityReservationFailure(c); capacityFailure != nil {
			attemptAPIError = types.NewErrorWithStatusCode(
				capacityFailure,
				types.ErrorCodeGetChannelFailed,
				http.StatusServiceUnavailable,
				types.ErrOptionWithSkipRetry(),
			)
			newAPIError = attemptAPIError
		}
		controlAPIError := relayAttemptControlError(c, attemptAPIError, relayInfo)
		if attemptAPIError != nil && service.RoutingCapacityReservationFailure(c) != nil {
			controlAPIError = attemptAPIError
		}
		clientCommitted := sendState.Sent() && routingAttemptClientCommitted(c, relayInfo)
		if attemptLease != nil && clientCommitted {
			_ = attemptLease.MarkClientCommitted()
		}
		classificationAPIError := attemptAPIError
		if classificationAPIError == nil {
			classificationAPIError = controlAPIError
		}
		classification, attemptSuccess := classifyRoutingRelayAttemptWithContext(c, classificationAPIError, relayInfo)
		canaryOutcomeKnown = true
		canaryOutcomeSuccess = attemptSuccess
		canaryOutcomeClassification = classification
		classificationAPIError = service.NormalizeViolationFeeError(classificationAPIError)
		willRetry := false
		if controlAPIError != nil {
			willRetry = shouldRetry(
				c,
				relayInfo,
				classificationAPIError,
				classification,
				common.RetryTimes-retryParam.GetRetry(),
			)
		}
		if err := completeRoutingSerialAttemptAudit(
			c,
			relayInfo,
			channel,
			serialAudit,
			attemptSuccess,
			classificationAPIError,
			classification,
			sendState.Sent(),
			clientCommitted,
			willRetry,
		); err != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing serial attempt audit completion failed: %v", err))
		}
		attemptLease.Finish()
		if sendState.Sent() {
			recordRoutingAttemptEffects(c, relayInfo, channel.Id, attemptSuccess, classificationAPIError, classification)
			if !attemptSuccess &&
				(classification.Responsibility == routingerror.ResponsibilityProvider ||
					classification.Responsibility == routingerror.ResponsibilityNetwork) {
				service.MarkRoutingChannelFailed(c, channel.Id)
			}
		}
		service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)

		if controlAPIError == nil {
			newAPIError = nil
			relayInfo.LastError = nil
			return
		}
		newAPIError = classificationAPIError

		relayInfo.LastError = newAPIError

		if sendState.Sent() {
			processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError, classification)
		}

		if !willRetry {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		perfmetrics.RecordRelaySample(relayInfo, false, 0)
	}
}

func writeRelayAPIErrorResponse(
	c *gin.Context,
	relayFormat types.RelayFormat,
	ws *websocket.Conn,
	apiErr *types.NewAPIError,
) {
	if c == nil || apiErr == nil {
		return
	}
	if c.Writer.Written() && (relayFormat != types.RelayFormatOpenAIRealtime || ws == nil) {
		return
	}
	switch relayFormat {
	case types.RelayFormatOpenAIRealtime:
		if ws != nil {
			helper.WssError(c, ws, apiErr.ToOpenAIError())
			return
		}
		c.JSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
	case types.RelayFormatClaude:
		c.JSON(apiErr.StatusCode, gin.H{
			"type":  "error",
			"error": apiErr.ToClaudeError(),
		})
	default:
		c.JSON(apiErr.StatusCode, gin.H{
			"error": apiErr.ToOpenAIError(),
		})
	}
}

func recordRoutingAttemptEffects(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channelID int,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
) {
	if info == nil || info.OriginModelName == "" {
		return
	}
	routingmetrics.RecordClassifiedAttempt(c, info, channelID, success, apiErr, classification)
	statusCode := 0
	if apiErr != nil {
		statusCode = apiErr.SourceStatusCode()
	}
	ttft := time.Duration(0)
	attemptStart := info.RoutingAttemptStartTime()
	if !attemptStart.IsZero() && info.FirstResponseTime.After(attemptStart) {
		ttft = info.FirstResponseTime.Sub(attemptStart)
	}
	service.ObserveRoutingAdaptiveConcurrency(c, channelrouting.AdaptiveConcurrencySignal{
		Success:          success,
		StatusCode:       statusCode,
		TTFT:             ttft,
		FirstByteTimeout: relayAttemptStreamSignal(info) == routingerror.SignalFirstByteTimeout,
		ObservedAt:       time.Now(),
	})
	if err := service.ObserveRoutingSlowStartProbe(
		c,
		success,
		!success && (classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen),
	); err != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing slow-start probe outcome failed: %v", err))
	}
	if !smart_routing_setting.Enabled() {
		return
	}
	setting := smart_routing_setting.GetSetting()
	syncRoutingBreakerConfigFromSetting(setting)
	credentialID := common.GetContextKeyInt(c, constant.ContextKeyRoutingCredentialID)
	upstreamAccountID := common.GetContextKeyInt(c, constant.ContextKeyRoutingUpstreamAccountID)
	if info.ChannelMeta != nil {
		if credentialID <= 0 {
			credentialID = info.ChannelMeta.RoutingCredentialID
		}
		if upstreamAccountID <= 0 {
			upstreamAccountID = info.ChannelMeta.RoutingUpstreamAccountID
		}
	}
	now := time.Now()
	if success {
		if credentialID > 0 {
			channelrouting.ClearCredentialAuthFailure(credentialID, channelID, now)
			channelrouting.ClearCredentialCapacityCooldown(credentialID, channelID, now)
		}
		if upstreamAccountID > 0 {
			channelrouting.ClearUpstreamAccountUnavailable(upstreamAccountID, now)
		}
	} else {
		switch {
		case classification.Responsibility == routingerror.ResponsibilityCredential && credentialID > 0:
			channelrouting.RecordCredentialAuthFailure(
				credentialID, channelID, classification.Rule, time.Time{}, now,
			)
		case statusCode == http.StatusPaymentRequired && upstreamAccountID > 0:
			channelrouting.RecordUpstreamAccountUnavailable(
				upstreamAccountID, statusCode, classification.Rule, time.Time{}, now,
			)
		case classification.Responsibility == routingerror.ResponsibilityCapacity &&
			classification.CapacityEffect == routingerror.CapacityCooldown && credentialID > 0:
			maxCooldown := time.Duration(setting.MaxCooldownSec) * time.Second
			if maxCooldown <= 0 {
				maxCooldown = routingbreaker.DefaultConfig().MaxCooldown
			}
			baseCooldown := time.Duration(setting.BackoffBaseMs429) * time.Millisecond
			if baseCooldown <= 0 {
				baseCooldown = time.Second
			}
			cooldown := retryAfterFromAPIError(apiErr, maxCooldown)
			if cooldown <= 0 {
				cooldown = baseCooldown
			}
			if maxCooldown > 0 && cooldown > maxCooldown {
				cooldown = maxCooldown
			}
			channelrouting.RecordCredentialCapacityCooldown(
				credentialID, channelID, statusCode, now.Add(cooldown), now,
			)
		}
	}
	group := info.UsingGroup
	if group == "" {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	if group == "" {
		group = "default"
	}
	endpointAuthority := common.GetContextKeyString(c, constant.ContextKeyRoutingEndpointAuthority)
	region := common.GetContextKeyString(c, constant.ContextKeyRoutingRegion)
	if info.ChannelMeta != nil {
		if endpointAuthority == "" {
			endpointAuthority = info.ChannelMeta.RoutingEndpointAuthority
		}
		if region == "" {
			region = info.ChannelMeta.RoutingRegion
		}
	}
	endpointKey := routingbreaker.NewEndpointKey(endpointAuthority, region)
	endpointKnown := endpointKey.EndpointAuthority != "" && endpointKey.Region != ""
	if endpointKnown {
		switch {
		case !success && classification.Responsibility == routingerror.ResponsibilityNetwork &&
			classification.Scope == routingerror.ScopeEndpoint &&
			(classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen):
			recordRoutingBreakerFailure(c, endpointKey, routingbreaker.FailureNetwork)
		case success || classification.Responsibility == routingerror.ResponsibilityProvider ||
			classification.Responsibility == routingerror.ResponsibilityCapacity ||
			classification.Responsibility == routingerror.ResponsibilityCredential:
			recordRoutingBreakerSuccess(c, endpointKey)
		}
	}
	if info.CurrentAttemptIsMultiKey(c) {
		return
	}
	key := routingbreaker.Key{
		ChannelID:   channelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       info.OriginModelName,
		Group:       group,
	}

	if success {
		recordRoutingBreakerSuccess(c, key)
		return
	}
	if classification.CapacityEffect == routingerror.CapacityCooldown {
		maxCooldown := time.Duration(setting.MaxCooldownSec) * time.Second
		if maxCooldown <= 0 {
			maxCooldown = routingbreaker.DefaultConfig().MaxCooldown
		}
		baseCooldown := time.Duration(setting.BackoffBaseMs429) * time.Millisecond
		if baseCooldown <= 0 {
			baseCooldown = time.Second
		}
		retryAfter := retryAfterFromAPIError(apiErr, maxCooldown)
		routinghotcache.RecordCapacityCooldown(key.HotcacheKey(), apiErr.SourceStatusCode(), retryAfter, baseCooldown, maxCooldown, time.Now())
	}
	switch classification.Responsibility {
	case routingerror.ResponsibilityProvider:
		if classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen {
			recordRoutingBreakerFailure(c, key, routingbreaker.FailureProvider5xx)
		}
	}
}

func recordRoutingBreakerSuccess(c *gin.Context, key routingbreaker.Key) {
	previous := routingBreakerState(key)
	current := routingbreaker.RecordReliabilitySuccess(key)
	publishRoutingBreakerTransition(c, key, previous, current)
}

func recordRoutingBreakerFailure(c *gin.Context, key routingbreaker.Key, kind routingbreaker.FailureKind) {
	previous := routingBreakerState(key)
	current := routingbreaker.RecordReliabilityFailure(key, kind)
	publishRoutingBreakerTransition(c, key, previous, current)
}

func routingBreakerState(key routingbreaker.Key) routingbreaker.State {
	snapshot, ok := routinghotcache.GetBreaker(key.HotcacheKey())
	if !ok || snapshot.State == "" {
		return routingbreaker.StateHealthy
	}
	return routingbreaker.State(snapshot.State)
}

func publishRoutingBreakerTransition(
	c *gin.Context,
	key routingbreaker.Key,
	previous routingbreaker.State,
	current routingbreaker.Snapshot,
) {
	eventType := ""
	if previous != routingbreaker.StateOpen && current.State == routingbreaker.StateOpen {
		eventType = channelrouting.RoutingEventTypeBreakerOpened
	} else if previous != routingbreaker.StateHealthy && current.State == routingbreaker.StateHealthy {
		eventType = channelrouting.RoutingEventTypeBreakerRecovered
	}
	if eventType == "" {
		return
	}
	revision := service.RoutingHedgePolicyRevision(c)
	_, err := channelrouting.PublishRoutingEvent(eventType, revision, map[string]any{
		"scope":              string(key.Scope),
		"channel_id":         key.ChannelID,
		"model_name":         key.Model,
		"group_name":         key.Group,
		"endpoint_authority": key.EndpointAuthority,
		"region":             key.Region,
		"previous_state":     string(previous),
		"current_state":      string(current.State),
		"reason":             current.Reason,
		"cooldown_until_ms":  current.CooldownUntil.UnixMilli(),
	})
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing breaker transition event failed: %v", err))
	}
}

func routingBreakerConfigFromSetting(setting smart_routing_setting.SmartRoutingSetting) routingbreaker.Config {
	failureRate := float64(setting.FailureRatePct) / 100
	if failureRate <= 0 || failureRate > 1 {
		failureRate = routingbreaker.DefaultConfig().FailureRateThreshold
	}
	minSamples := setting.MinVolume
	if minSamples <= 0 {
		minSamples = routingbreaker.DefaultConfig().FailureRateMinSamples
	}
	windowSize := minSamples * 2
	if windowSize < routingbreaker.DefaultConfig().WindowSize {
		windowSize = routingbreaker.DefaultConfig().WindowSize
	}
	consecutive5xx := setting.Consecutive5xx
	if consecutive5xx <= 0 {
		consecutive5xx = routingbreaker.DefaultConfig().Consecutive5xxThreshold
	}
	degradedFailures := consecutive5xx / 2
	if degradedFailures < 1 {
		degradedFailures = 1
	}
	return routingbreaker.Config{
		Consecutive5xxThreshold:      consecutive5xx,
		FailureRateThreshold:         failureRate,
		FailureRateMinSamples:        minSamples,
		WindowSize:                   windowSize,
		BaseCooldown:                 time.Duration(setting.BaseCooldownSec) * time.Second,
		MaxCooldown:                  time.Duration(setting.MaxCooldownSec) * time.Second,
		DegradedConsecutiveFailures:  degradedFailures,
		DegradedFailureRateThreshold: failureRate / 2,
		DegradedMinSamples:           minSamples,
	}
}

const maxRetryAfterDuration = time.Duration(1<<63 - 1)

func retryAfterFromAPIError(apiErr *types.NewAPIError, capDuration time.Duration) time.Duration {
	if apiErr == nil || len(apiErr.Metadata) == 0 {
		return 0
	}
	var metadata struct {
		RetryAfterMS      int64   `json:"retry_after_ms"`
		RetryAfterSeconds float64 `json:"retry_after_seconds"`
		RetryAfter        string  `json:"retry_after"`
	}
	if err := common.Unmarshal(apiErr.Metadata, &metadata); err != nil {
		return 0
	}
	if metadata.RetryAfterMS > 0 {
		return cappedMillisecondsDuration(metadata.RetryAfterMS, capDuration)
	}
	if metadata.RetryAfterSeconds > 0 && !math.IsNaN(metadata.RetryAfterSeconds) && !math.IsInf(metadata.RetryAfterSeconds, 0) {
		return cappedSecondsDuration(metadata.RetryAfterSeconds, capDuration)
	}
	value := strings.TrimSpace(metadata.RetryAfter)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return 0
		}
		return cappedSecondsDuration(seconds, capDuration)
	}
	if deadline, err := http.ParseTime(value); err == nil {
		delay := time.Until(deadline)
		if delay > 0 {
			if capDuration > 0 && delay > capDuration {
				return capDuration
			}
			return delay
		}
	}
	return 0
}

func cappedMillisecondsDuration(milliseconds int64, capDuration time.Duration) time.Duration {
	if milliseconds <= 0 {
		return 0
	}
	if capDuration > 0 {
		capMilliseconds := capDuration.Milliseconds()
		if capMilliseconds <= 0 || milliseconds >= capMilliseconds {
			return capDuration
		}
	}
	maxMilliseconds := int64(maxRetryAfterDuration / time.Millisecond)
	if milliseconds > maxMilliseconds {
		if capDuration > 0 {
			return capDuration
		}
		return maxRetryAfterDuration
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func cappedSecondsDuration(seconds float64, capDuration time.Duration) time.Duration {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	if capDuration > 0 && seconds >= capDuration.Seconds() {
		return capDuration
	}
	maxSeconds := float64(maxRetryAfterDuration) / float64(time.Second)
	if seconds > maxSeconds {
		if capDuration > 0 {
			return capDuration
		}
		return maxRetryAfterDuration
	}
	return time.Duration(seconds * float64(time.Second))
}

func prepareRoutingRelayAttempt(info *relaycommon.RelayInfo) {
	info.ResetStreamAttemptState()
}

func commitRoutingCapacityAttempt(c *gin.Context) *types.NewAPIError {
	if err := service.CommitRoutingCapacityReservation(c); err != nil {
		cancelRoutingCapacityReservation(c)
		return types.NewErrorWithStatusCode(
			fmt.Errorf("channel routing capacity reservation commit failed: %w", err),
			types.ErrorCodeGetChannelFailed,
			http.StatusServiceUnavailable,
			types.ErrOptionWithSkipRetry(),
		)
	}
	return nil
}

func bindRoutingUpstreamAttempt(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channel *model.Channel,
	attemptLease *channelrouting.AttemptLease,
) *relaycommon.RoutingUpstreamSendState {
	state := relaycommon.NewRoutingUpstreamSendState(func() error {
		var persistContext context.Context
		var cancel context.CancelFunc
		reservationAuthorized := false
		if info != nil && info.TaskRelayInfo != nil && info.AsyncBillingReservationID > 0 {
			if channel == nil || channel.Id <= 0 {
				return model.ErrAsyncBillingReservationInvariant
			}
			acceptanceIntent, ok := relay.GetAsyncBillingAcceptanceIntent(c)
			if !ok || acceptanceIntent == nil {
				return model.ErrAsyncBillingReservationInvariant
			}
			persistContext = context.Background()
			if c != nil && c.Request != nil {
				persistContext = context.WithoutCancel(c.Request.Context())
			}
			persistContext, cancel = context.WithTimeout(persistContext, 5*time.Second)
			defer cancel()
			if _, err := model.AuthorizeAsyncBillingAttempt(persistContext, info.AsyncBillingReservationID, model.AsyncBillingAttemptSpec{
				AttemptIndex:     info.RetryIndex,
				ChannelID:        channel.Id,
				CredentialID:     common.GetContextKeyInt(c, constant.ContextKeyRoutingCredentialID),
				ChannelVersion:   channel.RoutingGeneration,
				SendDeadlineMs:   info.AsyncBillingSendDeadlineMs,
				AcceptanceIntent: acceptanceIntent,
			}, time.Now()); err != nil {
				return err
			}
			reservationAuthorized = true
		}
		rejectAuthorized := func(cause error) error {
			if !reservationAuthorized {
				return cause
			}
			rejectErr := model.RejectAsyncBillingAttempt(
				persistContext, info.AsyncBillingReservationID, info.RetryIndex,
				"local_send_gate_failed", time.Now(),
			)
			return errors.Join(cause, rejectErr)
		}
		if attemptLease != nil {
			if err := attemptLease.MarkSent(); err != nil {
				return rejectAuthorized(err)
			}
		}
		if err := service.MarkChannelRoutingCanaryAttemptStarted(c); err != nil {
			if attemptLease != nil {
				attemptLease.Finish()
			}
			return rejectAuthorized(err)
		}
		return nil
	})
	relaycommon.BindRoutingUpstreamSendState(c, state)
	return state
}

func finishRoutingCanaryOutcome(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	classification routingerror.Classification,
	success bool,
) {
	include := success || classification.Responsibility != routingerror.ResponsibilityCaller
	clientTTFTMilliseconds := int64(0)
	if success && info != nil && !info.StartTime.IsZero() && info.FirstResponseTime.After(info.StartTime) {
		clientTTFTMilliseconds = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
		if clientTTFTMilliseconds < 0 {
			clientTTFTMilliseconds = 0
		}
	}
	if err := service.FinishChannelRoutingCanaryOutcome(
		c, include, success, false, clientTTFTMilliseconds, time.Now(),
	); err != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing canary outcome dropped: %v", err))
	}
}

func finishRoutingCanaryAttempt(c *gin.Context, sendState *relaycommon.RoutingUpstreamSendState) {
	if sendState == nil || !sendState.Sent() {
		return
	}
	if err := service.FinishChannelRoutingCanaryAttempt(c); err != nil {
		logger.LogError(c, fmt.Sprintf("channel routing canary attempt release failed: %v", err))
	}
}

func cancelRoutingCapacityReservation(c *gin.Context) {
	if err := service.CancelRoutingCapacityReservation(c); err != nil {
		logger.LogError(c, fmt.Sprintf("channel routing capacity reservation cancel failed: %v", err))
	}
}

func releaseRoutingCapacityReservation(c *gin.Context) {
	if err := service.ReleaseRoutingCapacityReservation(c); err != nil {
		logger.LogError(c, fmt.Sprintf("channel routing capacity reservation release failed: %v", err))
	}
}

func submitRoutingTaskAttempt(c *gin.Context, info *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError) {
	prepareRoutingRelayAttempt(info)
	return relay.RelayTaskSubmit(c, info)
}

func relayAttemptControlError(c *gin.Context, apiErr *types.NewAPIError, info *relaycommon.RelayInfo) *types.NewAPIError {
	isRealtime := info != nil && info.RelayFormat == types.RelayFormatOpenAIRealtime
	isStream := info != nil && (info.IsStream || info.StreamStatus != nil || isRealtime)
	clientCommitted := false
	if isRealtime {
		clientCommitted = info.ReceivedResponseCount > 0 || info.HasSendResponse()
	} else if isStream {
		clientCommitted = info.HTTPStreamClientCommitted(c)
	}
	return relayAttemptControlErrorWithClientCommit(apiErr, info, clientCommitted)
}

func relayAttemptControlErrorWithClientCommit(
	apiErr *types.NewAPIError,
	info *relaycommon.RelayInfo,
	clientCommitted bool,
) *types.NewAPIError {
	if clientCommitted {
		return nil
	}
	if apiErr != nil {
		return apiErr
	}

	signal := relayAttemptStreamSignal(info)
	if signal != routingerror.SignalFirstByteTimeout && signal != routingerror.SignalStreamCorruption {
		return nil
	}

	if signal == routingerror.SignalFirstByteTimeout {
		return types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout)
	}
	cause := errors.New("upstream stream failed before client commit")
	if info != nil && info.StreamStatus != nil && info.StreamStatus.EndError != nil {
		cause = fmt.Errorf("upstream stream failed before client commit: %w", info.StreamStatus.EndError)
	}
	return types.NewErrorWithStatusCode(
		cause,
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
		types.ErrOptionWithHideErrMsg("upstream stream failed before client commit"),
	)
}

func relayAttemptStreamSignal(info *relaycommon.RelayInfo) routingerror.Signal {
	if info == nil || info.StreamStatus == nil {
		return routingerror.SignalNone
	}
	status := info.StreamStatus
	if status.EndReason == relaycommon.StreamEndReasonClientGone {
		return routingerror.SignalClientGone
	}
	if status.EndReason == relaycommon.StreamEndReasonHandlerStop || status.HasErrors() {
		return routingerror.SignalStreamCorruption
	}
	switch status.EndReason {
	case relaycommon.StreamEndReasonFirstByteTimeout:
		return routingerror.SignalFirstByteTimeout
	case relaycommon.StreamEndReasonTimeout,
		relaycommon.StreamEndReasonScannerErr,
		relaycommon.StreamEndReasonPanic,
		relaycommon.StreamEndReasonPingFail:
		return routingerror.SignalStreamCorruption
	default:
		return routingerror.SignalNone
	}
}

func classifyRoutingRelayAttempt(apiErr *types.NewAPIError, info *relaycommon.RelayInfo) (routingerror.Classification, bool) {
	return classifyRoutingRelayAttemptWithContext(nil, apiErr, info)
}

func classifyRoutingRelayAttemptWithContext(c *gin.Context, apiErr *types.NewAPIError, info *relaycommon.RelayInfo) (routingerror.Classification, bool) {
	beforeClientCommit := c != nil && info != nil && info.HTTPStreamFailedBeforeCommit(c)
	return classifyRoutingRelayAttemptAtCommitBoundary(apiErr, info, beforeClientCommit)
}

func classifyRoutingRelayAttemptAtCommitBoundary(
	apiErr *types.NewAPIError,
	info *relaycommon.RelayInfo,
	beforeClientCommit bool,
) (routingerror.Classification, bool) {
	ctx := routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay}
	success := apiErr == nil
	if apiErr != nil && service.HasCSAMViolationMarker(apiErr) {
		ctx.Signal = routingerror.SignalContentSafety
	} else {
		ctx.Signal = relayAttemptStreamSignal(info)
	}
	ctx.BeforeCommit = ctx.Signal == routingerror.SignalStreamCorruption && beforeClientCommit
	if ctx.Signal != routingerror.SignalNone {
		success = false
	}
	return routingerror.ClassifyAPIError(apiErr, ctx), success
}

func taskErrorToAPIError(taskErr *dto.TaskError) *types.NewAPIError {
	if taskErr == nil {
		return nil
	}
	if taskErr.Code == "" {
		taskErr.Code = string(types.ErrorCodeBadResponseStatusCode)
	}
	if taskErr.StatusCode == 0 {
		taskErr.StatusCode = http.StatusInternalServerError
	}
	if taskErr.Message == "" {
		taskErr.Message = "task relay failed"
	}
	if taskErr.Error == nil {
		taskErr.Error = errors.New(taskErr.Message)
	}
	apiErr := types.NewErrorWithStatusCode(taskErr.Error, types.ErrorCode(taskErr.Code), taskErr.StatusCode)
	if taskErr.RetryAfterMs > 0 {
		metadata, err := common.Marshal(map[string]int64{"retry_after_ms": taskErr.RetryAfterMs})
		if err == nil {
			apiErr.Metadata = metadata
		}
	}
	return apiErr
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
	service.MarkRoutingTargetTried(
		c,
		channelId,
		common.GetContextKeyInt(c, constant.ContextKeyRoutingCredentialID),
		common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey),
	)
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		return &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}, nil
	}
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	if err != nil {
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
	if newAPIError != nil {
		cancelRoutingCapacityReservation(c)
		service.ReleaseRoutingHalfOpenProbe(c, channel.Id, info.OriginModelName, selectGroup)
		return nil, newAPIError
	}
	return channel, nil
}

func getTaskRetryChannel(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	retryParam *service.RetryParam,
) (*model.Channel, *types.NewAPIError) {
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)
	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
	if err != nil {
		return nil, types.NewError(
			fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()),
			types.ErrorCodeGetChannelFailed,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if channel == nil {
		return nil, types.NewError(
			fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName),
			types.ErrorCodeGetChannelFailed,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if setupErr := middleware.SetupContextForSelectedChannelMetadata(c, channel, info.OriginModelName); setupErr != nil {
		cancelRoutingCapacityReservation(c)
		service.ReleaseRoutingHalfOpenProbe(c, channel.Id, info.OriginModelName, selectGroup)
		return nil, setupErr
	}
	return channel, nil
}

func shouldRetry(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
	retryTimes int,
) bool {
	if apiErr == nil {
		return false
	}
	if policy, ok := service.ChannelRoutingRequestAttemptPolicy(c); ok &&
		(!policy.RetryAllowed || !policy.CrossChannelRetryAllowed) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if relayInfo != nil {
		isRealtime := relayInfo.RelayFormat == types.RelayFormatOpenAIRealtime
		isHTTPStream := relayInfo.IsStream || relayInfo.StreamStatus != nil
		if (isRealtime || !isHTTPStream) &&
			(relayInfo.SendResponseCount > 0 || relayInfo.HasSendResponse() || relayInfo.ReceivedResponseCount > 0) {
			return false
		}
	}
	if c != nil && c.Writer != nil && c.Writer.Written() {
		return false
	}
	if types.IsSkipRetryError(apiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if c != nil {
		if _, ok := c.Get("specific_channel_id"); ok {
			return false
		}
	}
	if classification.Retryability != routingerror.RetryBeforeCommit {
		return false
	}
	if operation_setting.IsAlwaysSkipRetryCode(apiErr.GetErrorCode()) {
		return false
	}
	statusCode := apiErr.SourceStatusCode()
	if statusCode < 100 || statusCode > 599 {
		return true
	}
	return operation_setting.ShouldRetryByStatusCode(statusCode)
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, classification routingerror.Classification) {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if service.ShouldDisableChannel(err, classification) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
		}
		other["error_type"] = err.GetErrorType()
		other["error_code"] = err.GetErrorCode()
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = c.GetString("channel_name")
		other["channel_type"] = c.GetInt("channel_type")
		adminInfo := make(map[string]interface{})
		adminInfo["use_channel"] = c.GetStringSlice("use_channel")
		isMultiKey := common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
		if isMultiKey {
			adminInfo["is_multi_key"] = true
			adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		}
		service.AppendChannelAffinityAdminInfo(c, adminInfo)
		other["admin_info"] = adminInfo
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}

}

func RelayMidjourney(c *gin.Context) {
	defer service.ReleaseAllRoutingHalfOpenProbes(c)

	relayMode := c.GetInt("relay_mode")
	if relayMode == relayconstant.RelayModeUnknown {
		relayMode = relayconstant.Path2RelayModeMidjourney(c.Request.URL.Path)
		c.Set("relay_mode", relayMode)
	}
	switch relayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		writeMidjourneyOutcome(c, relay.MidjourneyRelayOutcome{
			Error: relay.RelayMidjourneyNotify(c), StatusCode: http.StatusBadRequest, LocalError: true,
		})
		return
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		if authorizationErr := authorizeMidjourneyFetchModels(c, relayMode); authorizationErr != nil {
			writeMidjourneyAPIError(c, authorizationErr)
			return
		}
		writeMidjourneyOutcome(c, relay.MidjourneyRelayOutcome{
			Error: relay.RelayMidjourneyTask(c, relayMode), StatusCode: http.StatusBadRequest, LocalError: true,
		})
		return
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		originTask, channel, apiErr := prepareMidjourneyOriginChannel(
			c, c.Param("id"), "", true,
		)
		if apiErr != nil {
			writeMidjourneyAPIError(c, apiErr)
			return
		}
		modelName := service.CovertMjpActionToModelName(originTask.Action)
		c.Set("original_model", modelName)
		relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)
		if err != nil {
			writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
				err, types.ErrorCodeGenRelayInfoFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry(),
			))
			return
		}
		relayInfo.OriginModelName = modelName
		relayInfo.InitChannelMeta(c)
		outcome := executeMidjourneyRoutingAttempt(c, relayInfo, channel, func() relay.MidjourneyRelayOutcome {
			return relay.RelayMidjourneyTaskImageSeed(
				c, originTask, channel, common.GetContextKeyString(c, constant.ContextKeyChannelKey),
			)
		})
		writeMidjourneyOutcome(c, outcome)
		return
	}

	var request dto.MidjourneyRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
			err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
		))
		return
	}
	prepared, mjErr := service.PrepareMidjourneyRequest(relayMode, request)
	if mjErr != nil || prepared == nil {
		writeMidjourneyOutcome(c, relay.MidjourneyRelayOutcome{
			Error: mjErr, StatusCode: http.StatusBadRequest, LocalError: true,
		})
		return
	}
	c.Set("original_model", prepared.ModelName)
	requestProfile, profileErr := buildMidjourneyRoutingRequestProfileTemplate(c, prepared.ModelName, prepared.RelayMode)
	if profileErr != nil {
		writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
			profileErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
		))
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingRequestProfile, requestProfile)

	var (
		originTask *model.Midjourney
		channel    *model.Channel
	)
	if prepared.Stateful {
		var apiErr *types.NewAPIError
		originTask, channel, apiErr = prepareMidjourneyOriginChannel(
			c, prepared.TaskReference, prepared.ModelName,
			prepared.RelayMode != relayconstant.RelayModeMidjourneyModal,
		)
		if apiErr != nil {
			writeMidjourneyAPIError(c, apiErr)
			return
		}
	} else if common.GetContextKeyBool(c, constant.ContextKeyRoutingSelectionDeferred) {
		selected, selectionErr := middleware.SelectChannelForValidatedRequest(c, prepared.ModelName)
		if selectionErr != nil {
			writeMidjourneyAPIError(c, selectionErr)
			return
		}
		channel = selected
	} else {
		channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
		selected, err := model.GetChannelById(channelID, true)
		if err != nil {
			writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
				err, types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
			))
			return
		}
		channel = selected
	}
	if channel == nil {
		writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
			errors.New("selected Midjourney channel is unavailable"),
			types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		))
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)
	if err != nil {
		writeMidjourneyAPIError(c, types.NewErrorWithStatusCode(
			err, types.ErrorCodeGenRelayInfoFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry(),
		))
		return
	}
	relayInfo.RelayMode = prepared.RelayMode
	relayInfo.OriginModelName = prepared.ModelName
	if replayOutcome, handled := relay.ReplayPreparedMidjourneySubmit(c, relayInfo, prepared, originTask); handled {
		writeMidjourneyOutcome(c, replayOutcome)
		return
	}
	outcome := executeMidjourneyRoutingAttempt(c, relayInfo, channel, func() relay.MidjourneyRelayOutcome {
		return relay.RelayPreparedMidjourneySubmit(c, relayInfo, prepared, originTask, channel)
	})
	writeMidjourneyOutcome(c, outcome)
}

func authorizeMidjourneyFetchModels(c *gin.Context, relayMode int) *types.NewAPIError {
	if c == nil || !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return nil
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if userID <= 0 {
		userID = c.GetInt("id")
	}
	var tasks []*model.Midjourney
	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch:
		task, err := model.FindMidjourneyByPublicID(userID, strings.TrimSpace(c.Param("id")))
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, model.ErrMidjourneyIdentityInvalid) {
			return nil
		}
		if err != nil {
			return types.NewErrorWithStatusCode(
				errors.New("load Midjourney task authorization failed"),
				types.ErrorCodeGetChannelFailed,
				http.StatusInternalServerError,
				types.ErrOptionWithSkipRetry(),
			)
		}
		tasks = []*model.Midjourney{task}
	case relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		var condition struct {
			IDs []string `json:"ids"`
		}
		if err := common.UnmarshalBodyReusable(c, &condition); err != nil || len(condition.IDs) > 1_000 {
			return nil
		}
		loaded, err := model.FindMidjourneysByPublicIDs(userID, condition.IDs)
		if errors.Is(err, model.ErrMidjourneyIdentityInvalid) || errors.Is(err, model.ErrMidjourneyIdentityAmbiguous) {
			return nil
		}
		if err != nil {
			return types.NewErrorWithStatusCode(
				errors.New("load Midjourney task authorization failed"),
				types.ErrorCodeGetChannelFailed,
				http.StatusInternalServerError,
				types.ErrOptionWithSkipRetry(),
			)
		}
		tasks = loaded
	default:
		return nil
	}
	for _, task := range tasks {
		if task == nil {
			continue
		}
		modelName := service.CovertMjpActionToModelName(task.Action)
		if authorizationErr := middleware.AuthorizeTokenRoutingTarget(c, modelName, 0, false); authorizationErr != nil {
			return authorizationErr
		}
	}
	return nil
}

func prepareMidjourneyOriginChannel(
	c *gin.Context,
	publicTaskID string,
	modelName string,
	requireSuccess bool,
) (*model.Midjourney, *model.Channel, *types.NewAPIError) {
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if userID <= 0 {
		userID = c.GetInt("id")
	}
	originTask, err := model.FindMidjourneyByPublicID(userID, strings.TrimSpace(publicTaskID))
	if err != nil {
		statusCode := http.StatusInternalServerError
		message := "load Midjourney task failed"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			statusCode = http.StatusNotFound
			message = "Midjourney task not found"
		} else {
			logger.LogError(c, "load Midjourney origin task: "+err.Error())
		}
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New(message), types.ErrorCodeInvalidRequest, statusCode, types.ErrOptionWithSkipRetry(),
		)
	}
	if !service.StatefulRoutingGroupAllowed(
		common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
		common.GetContextKeyString(c, constant.ContextKeyUserGroup),
		originTask.Group,
	) {
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New("task_routing_group_forbidden"),
			types.ErrorCodeAccessDenied,
			http.StatusForbidden,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if requireSuccess && setting.MjActionCheckSuccessEnabled && originTask.Status != "SUCCESS" {
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New("origin Midjourney task is not successful"),
			types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry(),
		)
	}
	channel, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		logger.LogError(c, "load Midjourney origin channel: "+err.Error())
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New("origin Midjourney channel is unavailable"),
			types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		)
	}
	if channel.Status != common.ChannelStatusEnabled {
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New("origin Midjourney channel is disabled"),
			types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		)
	}
	if originTask.ChannelGeneration != "" && originTask.ChannelGeneration != channel.RoutingGeneration {
		return nil, nil, types.NewErrorWithStatusCode(
			errors.New("origin Midjourney channel generation no longer exists"),
			types.ErrorCodeChannelNoAvailableKey, http.StatusConflict, types.ErrOptionWithSkipRetry(),
		)
	}
	if modelName == "" {
		modelName = service.CovertMjpActionToModelName(originTask.Action)
	}
	if authorizationErr := middleware.AuthorizeTokenRoutingTarget(c, modelName, channel.Id, true); authorizationErr != nil {
		return nil, nil, authorizationErr
	}
	c.Set("original_model", modelName)
	requestProfile, profileErr := buildMidjourneyRoutingRequestProfileTemplate(
		c, modelName, relayconstant.Path2RelayModeMidjourney(c.Request.URL.Path),
	)
	if profileErr != nil {
		return nil, nil, types.NewErrorWithStatusCode(
			profileErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
		)
	}
	common.SetContextKey(c, constant.ContextKeyRoutingRequestProfile, requestProfile)
	routingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if routingGroup == "auto" {
		if originTask.Group != "" {
			routingGroup = originTask.Group
		} else if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			routingGroup = selectedGroup
		}
	}
	if routingGroup == "" {
		routingGroup = "default"
	}
	if common.GetContextKeyString(c, constant.ContextKeyUsingGroup) == "auto" {
		common.SetContextKey(c, constant.ContextKeyAutoGroup, routingGroup)
	}
	pinned, active, reserveErr := service.ReservePinnedChannelRoutingAttempt(
		&service.RetryParam{
			Ctx: c, TokenGroup: routingGroup,
			ModelName: modelName, RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0),
		},
		routingGroup,
		channel.Id,
		originTask.RoutingCredentialID,
	)
	if reserveErr != nil {
		return nil, nil, types.NewErrorWithStatusCode(
			reserveErr, types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
		)
	}
	if active {
		if pinned == nil || pinned.Id != channel.Id {
			cancelMidjourneyPinnedRoutingAdmission(c, channel.Id)
			return nil, nil, types.NewErrorWithStatusCode(
				service.ErrPinnedRoutingIdentityUnavailable,
				types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(),
			)
		}
		channel = pinned
	}
	if setupErr := middleware.SetupContextForSelectedChannelMetadata(c, channel, modelName); setupErr != nil {
		cancelMidjourneyPinnedRoutingAdmission(c, channel.Id)
		return nil, nil, setupErr
	}
	if credentialErr := middleware.CommitTaskChannelCredential(c, channel, originTask.RoutingCredentialID); credentialErr != nil {
		cancelMidjourneyPinnedRoutingAdmission(c, channel.Id)
		return nil, nil, credentialErr
	}
	return originTask, channel, nil
}

func cancelMidjourneyPinnedRoutingAdmission(c *gin.Context, channelID int) {
	cancelRoutingCapacityReservation(c)
	service.ReleaseRoutingHalfOpenProbe(c, channelID, "", "")
}

func executeMidjourneyRoutingAttempt(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	channel *model.Channel,
	run func() relay.MidjourneyRelayOutcome,
) relay.MidjourneyRelayOutcome {
	attemptGuard := newRoutingAttemptGuard(c, relayInfo)
	defer attemptGuard.Complete()

	attemptLease, err := attemptGuard.Begin(c, relayInfo)
	if err != nil {
		cancelRoutingCapacityReservation(c)
		return relay.MidjourneyRelayOutcome{
			Error:      &dto.MidjourneyResponse{Code: constant.MjRequestError, Description: "routing_attempt_rejected"},
			StatusCode: routingAttemptRejectionError(err).StatusCode, LocalError: true, Cause: err,
		}
	}
	if capacityErr := commitRoutingCapacityAttempt(c); capacityErr != nil {
		attemptLease.Finish()
		return relay.MidjourneyRelayOutcome{
			Error:      &dto.MidjourneyResponse{Code: constant.MjRequestError, Description: "routing_capacity_commit_failed"},
			StatusCode: capacityErr.StatusCode, LocalError: true, Cause: capacityErr.Err,
		}
	}
	addUsedChannel(c, channel.Id)
	prepareRoutingRelayAttempt(relayInfo)
	sendState := bindRoutingUpstreamAttempt(c, relayInfo, channel, attemptLease)
	serialAudit, auditErr := reserveRoutingSerialAttemptAudit(c, relayInfo, channel)
	if auditErr != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing Midjourney attempt audit unavailable: %v", auditErr))
	}
	releaseInflight := routingmetrics.BeginInflight(c, relayInfo, channel.Id)
	var outcome relay.MidjourneyRelayOutcome
	func() {
		defer releaseRoutingCapacityReservation(c)
		defer releaseInflight()
		defer finishRoutingCanaryAttempt(c, sendState)
		defer relaycommon.ClearRoutingUpstreamSendState(c)
		outcome = run()
	}()
	if outcome.IdempotentReplay {
		if serialAudit != nil {
			if err := serialAudit.Discard(); err != nil {
				logger.LogWarn(c, fmt.Sprintf("discard Midjourney replay attempt audit failed: %v", err))
			}
		}
		if attemptLease != nil {
			attemptLease.Finish()
		}
		service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)
		return outcome
	}
	if outcome.Error == nil && !sendState.Sent() {
		outcome = relay.MidjourneyRelayOutcome{
			Error:      &dto.MidjourneyResponse{Code: constant.MjRequestError, Description: "routing_upstream_not_sent"},
			StatusCode: http.StatusInternalServerError, LocalError: true,
			Cause: errors.New("Midjourney relay completed without crossing the upstream send boundary"),
		}
	}

	apiErr := midjourneyOutcomeAPIError(outcome)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
		Component:    routingerror.ComponentServing,
		Operation:    routingerror.OperationTaskSubmit,
		BeforeCommit: !sendState.Sent(),
	})
	if outcome.LocalError {
		responsibility := routingerror.ResponsibilityGateway
		if outcome.StatusCode >= http.StatusBadRequest && outcome.StatusCode < http.StatusInternalServerError {
			responsibility = routingerror.ResponsibilityCaller
		}
		classification = routingerror.Classification{
			Responsibility: responsibility,
			Scope:          routingerror.ScopeRequest,
			Retryability:   routingerror.RetryNever,
			HealthEffect:   routingerror.HealthIgnore,
			CapacityEffect: routingerror.CapacityNone,
			Component:      routingerror.ComponentServing,
			Rule:           "midjourney_local_failure",
		}
	}
	success := outcome.Error == nil
	clientCommitted := sendState.Sent()
	if attemptLease != nil && clientCommitted {
		_ = attemptLease.MarkClientCommitted()
	}
	if err := completeRoutingSerialAttemptAudit(
		c, relayInfo, channel, serialAudit, success, apiErr, classification,
		sendState.Sent(), clientCommitted, false,
	); err != nil {
		logger.LogWarn(c, fmt.Sprintf("channel routing Midjourney attempt audit completion failed: %v", err))
	}
	attemptLease.Finish()
	if sendState.Sent() {
		recordRoutingAttemptEffects(c, relayInfo, channel.Id, success, apiErr, classification)
		if !success && (classification.Responsibility == routingerror.ResponsibilityProvider ||
			classification.Responsibility == routingerror.ResponsibilityNetwork) {
			service.MarkRoutingChannelFailed(c, channel.Id)
		}
	}
	service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, relayInfo.UsingGroup)
	finishRoutingCanaryOutcome(c, relayInfo, classification, success)
	if !success && sendState.Sent() && !outcome.LocalError {
		processChannelError(c,
			*types.NewChannelError(
				channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
				common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan(),
			),
			apiErr,
			classification,
		)
	}
	return outcome
}

func midjourneyOutcomeAPIError(outcome relay.MidjourneyRelayOutcome) *types.NewAPIError {
	if outcome.Error == nil {
		return nil
	}
	statusCode := outcome.StatusCode
	if statusCode <= 0 {
		statusCode = http.StatusBadGateway
	}
	cause := outcome.Cause
	if cause == nil {
		cause = errors.New(strings.TrimSpace(outcome.Error.Description + " " + outcome.Error.Result))
	}
	code := types.ErrorCodeBadResponseStatusCode
	if outcome.LocalError {
		code = types.ErrorCodeInvalidRequest
		if statusCode >= http.StatusInternalServerError {
			code = types.ErrorCodeDoRequestFailed
		}
	}
	return types.NewErrorWithStatusCode(cause, code, statusCode, types.ErrOptionWithSkipRetry())
}

func writeMidjourneyOutcome(c *gin.Context, outcome relay.MidjourneyRelayOutcome) {
	if outcome.Error == nil || (c.Writer != nil && c.Writer.Written()) {
		return
	}
	apiErr := midjourneyOutcomeAPIError(outcome)
	message := strings.TrimSpace(outcome.Error.Description + " " + outcome.Error.Result)
	if outcome.LocalError || message == "" {
		message = apiErr.MaskSensitiveError()
	}
	message = common.MessageWithRequestId(message, c.GetString(common.RequestIdKey))
	code := outcome.Error.Code
	if code == 0 {
		code = constant.MjRequestError
	}
	c.JSON(apiErr.StatusCode, gin.H{
		"description": message, "type": "upstream_error", "code": code,
	})
	logger.LogError(c, fmt.Sprintf(
		"Midjourney relay error (channel #%d, status %d, code %d): %s",
		common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		apiErr.StatusCode,
		code,
		common.LocalLogPreview(message),
	))
}

func writeMidjourneyAPIError(c *gin.Context, apiErr *types.NewAPIError) {
	if c == nil || apiErr == nil || (c.Writer != nil && c.Writer.Written()) {
		return
	}
	message := apiErr.MaskSensitiveError()
	c.JSON(apiErr.StatusCode, gin.H{
		"description": message, "type": "upstream_error", "code": constant.MjRequestError,
	})
	logger.LogError(c, fmt.Sprintf(
		"Midjourney relay error (channel #%d, status %d): %s",
		common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		apiErr.StatusCode,
		common.LocalLogPreview(message),
	))
}
func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	defer service.ReleaseAllRoutingHalfOpenProbes(c)

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}
	if taskErr := relay.ValidateTaskRequestForRouting(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}
	if relayInfo.OriginTaskID != "" && relayInfo.LockedChannel == nil {
		if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
			respondTaskError(c, taskErr)
			return
		}
	}
	if relayInfo.OriginModelName == "" {
		if request, requestErr := relaycommon.GetTaskRequest(c); requestErr == nil {
			relayInfo.OriginModelName = strings.TrimSpace(request.Model)
		}
	}
	if relayInfo.OriginModelName == "" {
		respondTaskError(c, service.TaskErrorWrapperLocal(errors.New("model is required"), "missing_model", http.StatusBadRequest))
		return
	}
	if lockedChannel, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedChannel != nil {
		if authorizationErr := middleware.AuthorizeTokenRoutingTarget(
			c, relayInfo.OriginModelName, lockedChannel.Id, true,
		); authorizationErr != nil {
			respondTaskError(c, service.TaskErrorWrapperLocal(
				authorizationErr.Err,
				string(authorizationErr.GetErrorCode()),
				authorizationErr.StatusCode,
			))
			return
		}
	}
	c.Set("original_model", relayInfo.OriginModelName)
	requestProfile, profileErr := buildTaskRoutingRequestProfileTemplate(c, relayInfo)
	if profileErr != nil {
		respondTaskError(c, service.TaskErrorWrapperLocal(profileErr, "invalid_request", http.StatusBadRequest))
		return
	}
	common.SetContextKey(c, constant.ContextKeyRoutingRequestProfile, requestProfile)

	var initialChannel *model.Channel
	if lockedChannel, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedChannel != nil {
		initialChannel = lockedChannel
	} else if common.GetContextKeyBool(c, constant.ContextKeyRoutingSelectionDeferred) {
		selected, selectionErr := middleware.SelectChannelMetadataForValidatedRequest(c, relayInfo.OriginModelName)
		if selectionErr != nil {
			respondTaskError(c, service.TaskErrorWrapperLocal(selectionErr.Err, "get_channel_failed", selectionErr.StatusCode))
			return
		}
		initialChannel = selected
	} else if channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId); channelID > 0 {
		selected, channelErr := model.GetChannelById(channelID, true)
		if channelErr != nil {
			respondTaskError(c, service.TaskErrorWrapperLocal(channelErr, "get_channel_failed", http.StatusServiceUnavailable))
			return
		}
		initialChannel = selected
		if setupErr := middleware.SetupContextForSelectedChannelMetadata(c, initialChannel, relayInfo.OriginModelName); setupErr != nil {
			respondTaskError(c, service.TaskErrorWrapperLocal(setupErr.Err, "setup_channel_failed", http.StatusServiceUnavailable))
			return
		}
	}

	var result *relay.TaskSubmitResult
	var taskErr *dto.TaskError
	idempotentTaskReplay := false
	var (
		canaryOutcomeKnown          bool
		canaryOutcomeSuccess        bool
		canaryOutcomeClassification routingerror.Classification
	)
	defer func() {
		if !canaryOutcomeKnown && taskErr != nil {
			canaryOutcomeClassification = routingerror.ClassifyTaskError(taskErr, routingerror.Context{
				Component: routingerror.ComponentServing,
				Operation: routingerror.OperationTaskSubmit,
			})
		}
		success := taskErr == nil
		if canaryOutcomeKnown {
			success = canaryOutcomeSuccess
		}
		finishRoutingCanaryOutcome(c, relayInfo, canaryOutcomeClassification, success)
	}()
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}
	attemptGuard := newRoutingAttemptGuard(c, relayInfo)
	defer attemptGuard.Complete()

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		relayInfo.RetryIndex = retryParam.GetRetry()
		var channel *model.Channel
		attemptRoutingGroup := relayInfo.UsingGroup
		lockedChannel, stateful := relayInfo.LockedChannel.(*model.Channel)
		stateful = stateful && lockedChannel != nil

		if retryParam.GetRetry() == 0 && initialChannel != nil {
			channel = initialChannel
		} else if stateful {
			channel = lockedChannel
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getTaskRetryChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}
		if stateful {
			if relayInfo.LockedRoutingGroup != "" {
				attemptRoutingGroup = relayInfo.LockedRoutingGroup
			}
			pinnedChannel, active, reserveErr := service.ReservePinnedChannelRoutingAttempt(
				retryParam,
				attemptRoutingGroup,
				lockedChannel.Id,
				relayInfo.LockedRoutingCredentialID,
			)
			if reserveErr != nil {
				statusCode := http.StatusServiceUnavailable
				code := "task_routing_identity_unavailable"
				if errors.Is(reserveErr, channelrouting.ErrCapacityExhausted) ||
					errors.Is(reserveErr, channelrouting.ErrStrictCapacityExhausted) ||
					errors.Is(reserveErr, channelrouting.ErrAdaptiveConcurrencyExhausted) {
					statusCode = http.StatusTooManyRequests
					code = "task_routing_capacity_exhausted"
				}
				taskErr = service.TaskErrorWrapperLocal(reserveErr, code, statusCode)
				if statusCode == http.StatusTooManyRequests {
					taskErr.RetryAfterMs = time.Second.Milliseconds()
				}
				break
			}
			if active {
				if pinnedChannel == nil || pinnedChannel.Id != lockedChannel.Id {
					taskErr = service.TaskErrorWrapperLocal(
						service.ErrPinnedRoutingIdentityUnavailable,
						"task_routing_identity_unavailable",
						http.StatusServiceUnavailable,
					)
					break
				}
				channel = pinnedChannel
			}
			if setupErr := middleware.SetupContextForSelectedChannelMetadata(c, channel, relayInfo.OriginModelName); setupErr != nil {
				cancelRoutingCapacityReservation(c)
				service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
				taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusServiceUnavailable)
				break
			}
		}

		prepared, prepareErr := relay.PrepareTaskAttempt(c, relayInfo)
		if prepareErr != nil {
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			taskErr = prepareErr
			break
		}
		replayResult, replayErr := prepared.IdempotentReplayResult(c)
		if replayErr != nil {
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			taskErr = service.TaskErrorWrapperLocal(replayErr, "idempotency_replay_unavailable", http.StatusServiceUnavailable)
			break
		}
		if replayResult != nil {
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			result = replayResult
			idempotentTaskReplay = true
			break
		}

		attemptLease, attemptErr := attemptGuard.Begin(c, relayInfo)
		if attemptErr != nil {
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			logger.LogWarn(c, fmt.Sprintf("channel routing retry budget rejected task attempt: %v", attemptErr))
			if taskErr == nil {
				apiErr := routingAttemptRejectionError(attemptErr)
				taskErr = service.TaskErrorWrapperLocal(apiErr.Err, "routing_attempt_rejected", apiErr.StatusCode)
			}
			break
		}
		if credentialErr := middleware.CommitSelectedChannelCredential(c, channel); credentialErr != nil {
			attemptLease.Finish()
			cancelRoutingCapacityReservation(c)
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			taskErr = service.TaskErrorWrapperLocal(credentialErr.Err, "select_channel_credential_failed", http.StatusServiceUnavailable)
			break
		}
		addUsedChannel(c, channel.Id)
		if capacityErr := commitRoutingCapacityAttempt(c); capacityErr != nil {
			attemptLease.Finish()
			service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
			taskErr = service.TaskErrorWrapperLocal(capacityErr.Err, "routing_capacity_commit_failed", http.StatusServiceUnavailable)
			break
		}

		prepareRoutingRelayAttempt(relayInfo)
		sendState := bindRoutingUpstreamAttempt(c, relayInfo, channel, attemptLease)
		serialAudit, auditErr := reserveRoutingSerialAttemptAudit(c, relayInfo, channel)
		if auditErr != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing serial task attempt audit unavailable: %v", auditErr))
		}
		releaseInflight := routingmetrics.BeginInflight(c, relayInfo, channel.Id)
		func() {
			defer releaseRoutingCapacityReservation(c)
			defer releaseInflight()
			defer finishRoutingCanaryAttempt(c, sendState)
			defer relaycommon.ClearRoutingUpstreamSendState(c)
			result, taskErr = relay.RelayPreparedTaskSubmit(c, relayInfo, prepared)
		}()
		if taskErr == nil && !sendState.Sent() {
			taskErr = service.TaskErrorWrapperLocal(
				errors.New("task relay completed without crossing the upstream send boundary"),
				"routing_upstream_not_sent",
				http.StatusInternalServerError,
			)
		}
		taskAPIError := taskErrorToAPIError(taskErr)
		classificationContext := routingerror.Context{
			Component: routingerror.ComponentServing,
			Operation: routingerror.OperationTaskSubmit,
		}
		if service.HasCSAMViolationMarker(taskAPIError) {
			classificationContext.Signal = routingerror.SignalContentSafety
		}
		classification := routingerror.ClassifyTaskError(taskErr, classificationContext)
		clientCommitted := sendState.Sent() && (taskErr == nil || classification.Retryability == routingerror.RetryIdempotencyRequired)
		if attemptLease != nil && clientCommitted {
			_ = attemptLease.MarkClientCommitted()
		}
		willRetry := false
		if taskErr != nil {
			willRetry = shouldRetryTaskRelay(
				c,
				taskErr,
				classification,
				common.RetryTimes-retryParam.GetRetry(),
			)
		}
		if err := completeRoutingSerialAttemptAudit(
			c,
			relayInfo,
			channel,
			serialAudit,
			taskErr == nil,
			taskAPIError,
			classification,
			sendState.Sent(),
			clientCommitted,
			willRetry,
		); err != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing serial task attempt audit completion failed: %v", err))
		}
		attemptLease.Finish()
		canaryOutcomeKnown = true
		canaryOutcomeSuccess = taskErr == nil
		canaryOutcomeClassification = classification
		if sendState.Sent() {
			recordRoutingAttemptEffects(c, relayInfo, channel.Id, taskErr == nil, taskAPIError, classification)
			if taskErr != nil &&
				(classification.Responsibility == routingerror.ResponsibilityProvider ||
					classification.Responsibility == routingerror.ResponsibilityNetwork) {
				service.MarkRoutingChannelFailed(c, channel.Id)
			}
		}
		service.ReleaseRoutingHalfOpenProbe(c, channel.Id, relayInfo.OriginModelName, attemptRoutingGroup)
		if taskErr == nil {
			break
		}

		if sendState.Sent() && !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				taskAPIError,
				classification)
		}

		if !willRetry {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// Persist the accepted upstream task before committing the buffered client
	// response. V2 performs task creation, final submit quota adjustment, accepted
	// identity fencing, and projection freezing in one main-database transaction.
	if taskErr == nil && !idempotentTaskReplay {
		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		task.PrivateData.BillingProtocolVersion = model.TaskBillingLegacyProtocolVersion
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.NodeName = common.NodeName
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios(),
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = service.SanitizeTaskData(result.TaskData)
		task.Action = relayInfo.Action
		replaySpec, replaySnapshotErr := relay.TaskSubmissionReplaySnapshot(
			result, relayInfo.PublicTaskID, relayInfo.OriginModelName,
		)
		var finalAudit model.AsyncBillingAcceptedAuditSnapshot
		if relayInfo.AsyncBillingReservationID > 0 {
			var auditErr error
			finalAudit, auditErr = relay.BuildAsyncBillingFinalAudit(c, relayInfo, task)
			if auditErr != nil {
				reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, reviewErr := model.MarkAsyncBillingAcceptedHandoffManualReviewFromAttempt(
					reviewContext, relayInfo.AsyncBillingReservationID, result.Quota, result.UpstreamTaskID,
					replaySpec, "freeze_final_task_billing_audit_failed: "+auditErr.Error(), time.Now(),
				)
				reviewCancel()
				if reviewErr == nil {
					relayInfo.AsyncBillingManualReviewMarked = true
				}
				taskErr = service.TaskErrorWrapperLocal(errors.Join(auditErr, replaySnapshotErr, reviewErr),
					"persist_task_failed", http.StatusInternalServerError)
			}
		}
		if relayInfo.AsyncBillingReservationID > 0 && taskErr == nil {
			persistContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
			if replaySnapshotErr != nil {
				cancel()
				reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, reviewErr := model.MarkAsyncBillingAcceptedHandoffManualReview(
					reviewContext, relayInfo.AsyncBillingReservationID, result.Quota, finalAudit,
					result.UpstreamTaskID, replaySpec,
					"freeze_task_response_failed: "+replaySnapshotErr.Error(), time.Now(),
				)
				reviewCancel()
				if reviewErr == nil {
					relayInfo.AsyncBillingManualReviewMarked = true
				}
				taskErr = service.TaskErrorWrapperLocal(
					errors.Join(replaySnapshotErr, reviewErr), "freeze_task_response_failed", http.StatusInternalServerError,
				)
			} else {
				_, acceptErr := model.AcceptAsyncTaskReservation(
					persistContext,
					relayInfo.AsyncBillingReservationID,
					relayInfo.RetryIndex,
					task,
					result.UpstreamTaskID,
					result.Quota,
					finalAudit,
					replaySpec,
					time.Now(),
				)
				cancel()
				if acceptErr != nil {
					common.SysError("persist accepted task reservation error: " + common.SanitizeErrorMessage(acceptErr.Error()))
					reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
					var reviewErr error
					if errors.Is(acceptErr, model.ErrAsyncBillingInsufficientQuota) ||
						errors.Is(acceptErr, model.ErrAsyncBillingSubscriptionExhausted) {
						_, reviewErr = model.MarkAsyncBillingAcceptanceOverageManualReviewWithReplay(
							reviewContext, relayInfo.AsyncBillingReservationID, result.Quota,
							finalAudit, result.UpstreamTaskID, replaySpec, time.Now(),
						)
					} else {
						_, reviewErr = model.MarkAsyncBillingAcceptedHandoffManualReview(
							reviewContext, relayInfo.AsyncBillingReservationID, result.Quota,
							finalAudit, result.UpstreamTaskID, replaySpec,
							"accepted_task_handoff_failed: "+acceptErr.Error(), time.Now(),
						)
					}
					reviewCancel()
					if reviewErr == nil {
						relayInfo.AsyncBillingManualReviewMarked = true
					}
					taskErr = service.TaskErrorWrapperLocal(
						errors.Join(acceptErr, reviewErr), "persist_task_failed", http.StatusInternalServerError,
					)
				}
			}
		} else if relayInfo.AsyncBillingReservationID == 0 {
			if insertErr := task.Insert(); insertErr != nil {
				common.SysError("insert accepted task error: " + common.SanitizeErrorMessage(insertErr.Error()))
				taskErr = service.TaskErrorWrapperLocal(insertErr, "persist_task_failed", http.StatusInternalServerError)
			}
		}
	}

	if taskErr == nil && !idempotentTaskReplay && relayInfo.AsyncBillingReservationID == 0 {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
			taskErr = service.TaskErrorWrapperLocal(settleErr, "settle_billing_failed", http.StatusInternalServerError)
		}
	}
	if taskErr == nil {
		if !idempotentTaskReplay && relayInfo.AsyncBillingReservationID == 0 {
			service.LogTaskConsumption(c, relayInfo)
		}
		if commitErr := result.CommitResponse(c); commitErr != nil {
			// The upstream task and charge are already durable. Never refund or
			// append a second response after a client transport failure.
			logger.LogWarn(c, fmt.Sprintf("commit accepted task response failed: %v", commitErr))
		}
	}

	if taskErr != nil {
		if relayInfo.AsyncBillingReservationID > 0 && !relayInfo.AsyncBillingManualReviewMarked {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, releaseErr := model.ReleaseAsyncBillingReservation(
				cleanupContext, relayInfo.AsyncBillingReservationID, time.Now(),
			)
			if errors.Is(releaseErr, model.ErrAsyncBillingReservationAmbiguous) {
				_, reviewErr := model.MarkAsyncBillingReservationManualReview(
					cleanupContext,
					relayInfo.AsyncBillingReservationID,
					"task_submit_outcome_ambiguous: "+taskErr.Message,
					"",
					time.Now(),
				)
				if reviewErr != nil {
					logger.LogError(c, "mark task billing reservation for manual review: "+reviewErr.Error())
				}
			} else if releaseErr != nil && !errors.Is(releaseErr, model.ErrAsyncBillingReservationAccepted) {
				logger.LogError(c, "release task billing reservation: "+releaseErr.Error())
			}
			cancel()
		}
		respondTaskError(c, taskErr)
	}
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *dto.TaskError) {
	if taskErr == nil {
		return
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	if taskErr.RetryAfterMs > 0 {
		seconds := (taskErr.RetryAfterMs + time.Second.Milliseconds() - 1) / time.Second.Milliseconds()
		c.Header("Retry-After", strconv.FormatInt(max(seconds, 1), 10))
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, taskErr *dto.TaskError, classification routingerror.Classification, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if policy, ok := service.ChannelRoutingRequestAttemptPolicy(c); ok &&
		(!policy.RetryAllowed || !policy.CrossChannelRetryAllowed) {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	return classification.Retryability == routingerror.RetryBeforeCommit
}
