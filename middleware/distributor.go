package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type ModelRequest struct {
	Model string `json:"model"`
	Group string `json:"group,omitempty"`
}

func Distribute() func(c *gin.Context) {
	return distribute(false)
}

func DistributeDeferred() func(c *gin.Context) {
	return distribute(true)
}

type distributorSelectionFailure struct {
	statusCode int
	message    string
	errorCode  types.ErrorCode
}

type tokenRoutingAuthorization struct {
	specificChannelID int
	hasSpecific       bool
}

func distribute(deferred bool) func(c *gin.Context) {
	return func(c *gin.Context) {
		modelRequest, shouldSelectChannel, err := getModelRequest(c)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}))
			return
		}
		setRoutingPromptCostProxy(c)
		common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
		c.Set("original_model", modelRequest.Model)
		if deferred {
			common.SetContextKey(c, constant.ContextKeyRoutingSelectionDeferred, true)
			c.Next()
			cleanupRoutingCapacityReservation(c)
			if c.Writer != nil && c.Writer.Status() < http.StatusBadRequest {
				if channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId); channelID > 0 {
					service.RecordChannelAffinity(c, channelID)
				}
			}
			return
		}

		channel, failure := selectAndSetupChannel(c, modelRequest.Model, shouldSelectChannel, true)
		if failure != nil {
			cleanupRoutingCapacityReservation(c)
			if failure.errorCode != "" {
				abortWithOpenAiMessage(c, failure.statusCode, failure.message, failure.errorCode)
			} else {
				abortWithOpenAiMessage(c, failure.statusCode, failure.message)
			}
			return
		}
		c.Next()
		cleanupRoutingCapacityReservation(c)
		if channel != nil && c.Writer != nil && c.Writer.Status() < http.StatusBadRequest {
			service.RecordChannelAffinity(c, channel.Id)
		}
	}
}

func SelectChannelForValidatedRequest(c *gin.Context, modelName string) (*model.Channel, *types.NewAPIError) {
	channel, failure := selectAndSetupChannel(c, strings.TrimSpace(modelName), true, true)
	if failure == nil {
		return channel, nil
	}
	cleanupRoutingCapacityReservation(c)
	return nil, distributorSelectionAPIError(failure)
}

func SelectChannelMetadataForValidatedRequest(c *gin.Context, modelName string) (*model.Channel, *types.NewAPIError) {
	channel, failure := selectAndSetupChannel(c, strings.TrimSpace(modelName), true, false)
	if failure == nil {
		return channel, nil
	}
	cleanupRoutingCapacityReservation(c)
	return nil, distributorSelectionAPIError(failure)
}

// AuthorizeTokenRoutingTarget validates model access and, for requests that
// produce upstream work, the final channel pinned by the token.
func AuthorizeTokenRoutingTarget(
	c *gin.Context,
	modelName string,
	finalChannelID int,
	enforceSpecificChannel bool,
) *types.NewAPIError {
	_, failure := authorizeTokenRoutingTarget(c, strings.TrimSpace(modelName), finalChannelID, enforceSpecificChannel)
	return distributorSelectionAPIError(failure)
}

func distributorSelectionAPIError(failure *distributorSelectionFailure) *types.NewAPIError {
	if failure == nil {
		return nil
	}
	errorCode := failure.errorCode
	if errorCode == "" {
		errorCode = types.ErrorCodeGetChannelFailed
	}
	return types.NewErrorWithStatusCode(
		errors.New(failure.message),
		errorCode,
		failure.statusCode,
		types.ErrOptionWithSkipRetry(),
	)
}

func authorizeTokenRoutingTarget(
	c *gin.Context,
	modelName string,
	finalChannelID int,
	enforceSpecificChannel bool,
) (tokenRoutingAuthorization, *distributorSelectionFailure) {
	authorization := tokenRoutingAuthorization{}
	if common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		value, ok := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
		if !ok {
			return authorization, &distributorSelectionFailure{
				statusCode: http.StatusForbidden,
				message:    i18n.T(c, i18n.MsgDistributorTokenNoModelAccess),
				errorCode:  types.ErrorCodeAccessDenied,
			}
		}
		tokenModelLimit, ok := value.(map[string]bool)
		if !ok {
			tokenModelLimit = map[string]bool{}
		}
		matchName := ratio_setting.FormatMatchingModelName(modelName)
		if allowed, exists := tokenModelLimit[matchName]; !exists || !allowed {
			return authorization, &distributorSelectionFailure{
				statusCode: http.StatusForbidden,
				message:    i18n.T(c, i18n.MsgDistributorTokenModelForbidden, map[string]any{"Model": modelName}),
				errorCode:  types.ErrorCodeAccessDenied,
			}
		}
	}
	if !enforceSpecificChannel {
		return authorization, nil
	}

	value, specific := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId)
	if !specific {
		return authorization, nil
	}
	rawChannelID, ok := value.(string)
	if !ok {
		return authorization, &distributorSelectionFailure{
			statusCode: http.StatusBadRequest,
			message:    i18n.T(c, i18n.MsgDistributorInvalidChannelId),
			errorCode:  types.ErrorCodeInvalidRequest,
		}
	}
	specificChannelID, err := strconv.Atoi(strings.TrimSpace(rawChannelID))
	if err != nil || specificChannelID <= 0 {
		return authorization, &distributorSelectionFailure{
			statusCode: http.StatusBadRequest,
			message:    i18n.T(c, i18n.MsgDistributorInvalidChannelId),
			errorCode:  types.ErrorCodeInvalidRequest,
		}
	}
	authorization.specificChannelID = specificChannelID
	authorization.hasSpecific = true
	if finalChannelID > 0 && finalChannelID != specificChannelID {
		return authorization, &distributorSelectionFailure{
			statusCode: http.StatusForbidden,
			message: i18n.T(c, i18n.MsgDistributorTokenChannelForbidden, map[string]any{
				"Channel": specificChannelID,
			}),
			errorCode: types.ErrorCodeAccessDenied,
		}
	}
	return authorization, nil
}

func selectAndSetupChannel(
	c *gin.Context,
	modelName string,
	shouldSelectChannel bool,
	selectCredential bool,
) (*model.Channel, *distributorSelectionFailure) {
	authorization, failure := authorizeTokenRoutingTarget(c, modelName, 0, shouldSelectChannel)
	if failure != nil {
		return nil, failure
	}
	if !shouldSelectChannel {
		return nil, nil
	}
	if modelName == "" {
		return nil, &distributorSelectionFailure{statusCode: http.StatusBadRequest, message: i18n.T(c, i18n.MsgDistributorModelNameRequired)}
	}
	if authorization.hasSpecific {
		channel, err := model.GetChannelById(authorization.specificChannelID, true)
		if err != nil {
			return nil, &distributorSelectionFailure{statusCode: http.StatusBadRequest, message: i18n.T(c, i18n.MsgDistributorInvalidChannelId)}
		}
		if channel.Status != common.ChannelStatusEnabled {
			return nil, &distributorSelectionFailure{statusCode: http.StatusForbidden, message: i18n.T(c, i18n.MsgDistributorChannelDisabled)}
		}
		if setupErr := setupSelectedChannelContext(c, channel, modelName, selectCredential); setupErr != nil {
			return nil, &distributorSelectionFailure{
				statusCode: http.StatusServiceUnavailable,
				message:    setupErr.MaskSensitiveError(),
				errorCode:  setupErr.GetErrorCode(),
			}
		}
		return channel, nil
	}

	usingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if strings.HasPrefix(c.Request.URL.Path, "/pg/chat/completions") {
		playgroundRequest := &dto.PlayGroundRequest{}
		if err := common.UnmarshalBodyReusable(c, playgroundRequest); err != nil {
			return nil, &distributorSelectionFailure{
				statusCode: http.StatusBadRequest,
				message:    i18n.T(c, i18n.MsgDistributorInvalidPlayground, map[string]any{"Error": err.Error()}),
			}
		}
		if playgroundRequest.Group != "" {
			if !service.GroupInUserUsableGroups(usingGroup, playgroundRequest.Group) && playgroundRequest.Group != usingGroup {
				return nil, &distributorSelectionFailure{statusCode: http.StatusForbidden, message: i18n.T(c, i18n.MsgDistributorGroupAccessDenied)}
			}
			usingGroup = playgroundRequest.Group
			common.SetContextKey(c, constant.ContextKeyUsingGroup, usingGroup)
		}
	}

	var channel *model.Channel
	if preferredChannelID, found := service.GetPreferredChannelByAffinity(c, modelName, usingGroup); found {
		affinityUsable := false
		affinityBypassedForCanary := false
		preferred, affinityGroup, bypassAffinity, gateErr := service.GetAdmissibleAffinityChannelWithRoutingGate(
			c,
			preferredChannelID,
			modelName,
			usingGroup,
			c.Request.URL.Path,
		)
		if gateErr != nil {
			logger.LogWarn(c, fmt.Sprintf("channel routing canary affinity gate failed: %v", gateErr))
		}
		affinityBypassedForCanary = bypassAffinity
		if preferred != nil {
			channel = preferred
			affinityUsable = true
			service.MarkChannelAffinityUsed(c, affinityGroup, preferred.Id)
			service.RecordChannelRoutingObserveSelection(&service.RetryParam{
				Ctx: c, TokenGroup: usingGroup, ModelName: modelName,
				RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0),
			}, affinityGroup, preferred, 0)
		}
		if !affinityUsable && !affinityBypassedForCanary && !service.ShouldKeepChannelAffinityOnChannelDisabled() {
			service.ClearCurrentChannelAffinityCache(c)
		}
	}

	if channel == nil {
		selected, selectGroup, err := service.CacheGetRandomSatisfiedChannel(&service.RetryParam{
			Ctx: c, ModelName: modelName, TokenGroup: usingGroup,
			RequestPath: c.Request.URL.Path, Retry: common.GetPointer(0),
		})
		if err != nil {
			showGroup := usingGroup
			if usingGroup == "auto" {
				showGroup = fmt.Sprintf("auto(%s)", selectGroup)
			}
			return nil, &distributorSelectionFailure{
				statusCode: http.StatusServiceUnavailable,
				message: i18n.T(c, i18n.MsgDistributorGetChannelFailed, map[string]any{
					"Group": showGroup, "Model": modelName, "Error": err.Error(),
				}),
				errorCode: types.ErrorCodeModelNotFound,
			}
		}
		if selected == nil {
			return nil, &distributorSelectionFailure{
				statusCode: http.StatusServiceUnavailable,
				message:    i18n.T(c, i18n.MsgDistributorNoAvailableChannel, map[string]any{"Group": usingGroup, "Model": modelName}),
				errorCode:  types.ErrorCodeModelNotFound,
			}
		}
		channel = selected
	}

	if setupErr := setupSelectedChannelContext(c, channel, modelName, selectCredential); setupErr != nil {
		return nil, &distributorSelectionFailure{
			statusCode: http.StatusServiceUnavailable,
			message:    setupErr.MaskSensitiveError(),
			errorCode:  setupErr.GetErrorCode(),
		}
	}
	return channel, nil
}

func setupSelectedChannelContext(
	c *gin.Context,
	channel *model.Channel,
	modelName string,
	selectCredential bool,
) *types.NewAPIError {
	if selectCredential {
		return SetupContextForSelectedChannel(c, channel, modelName)
	}
	return SetupContextForSelectedChannelMetadata(c, channel, modelName)
}

func cleanupRoutingCapacityReservation(c *gin.Context) {
	if err := service.CancelRoutingCapacityReservation(c); err != nil {
		logger.LogError(c, fmt.Sprintf("channel routing capacity reservation cleanup failed: %v", err))
	}
}

func setRoutingPromptCostProxy(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/realtime") {
		estimate := estimateRoutingCapacityTokens(c.Request.URL.Path, nil)
		setRoutingCapacityEstimate(c, estimate)
		common.SetContextKey(c, constant.ContextKeyRoutingCostProfile, buildRoutingCostRequestProfile(
			c.Request.URL.Path, nil, c.Request.Header, estimate, 0, 0,
		))
		common.SetContextKey(c, constant.ContextKeyIsStream, true)
		return
	}
	if !strings.HasPrefix(c.Request.Header.Get("Content-Type"), "application/json") {
		estimate := estimateRoutingCapacityTokens(c.Request.URL.Path, nil)
		setRoutingCapacityEstimate(c, estimate)
		common.SetContextKey(c, constant.ContextKeyRoutingCostProfile, buildRoutingCostRequestProfile(
			c.Request.URL.Path, nil, c.Request.Header, estimate, 0, 0,
		))
		if estimate.StreamKnown {
			common.SetContextKey(c, constant.ContextKeyIsStream, estimate.Stream)
		}
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	estimate := estimateRoutingCapacityTokens(c.Request.URL.Path, body)
	promptProxy := max(len(body)/4, 1)
	common.SetContextKey(c, constant.ContextKeyRoutingPromptProxy, promptProxy)
	outputProxy := min(promptProxy+promptProxy/2, 512)
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		value := gjson.GetBytes(body, field)
		if value.Exists() && value.Num > 0 {
			outputProxy = min(outputProxy, int(value.Num))
			break
		}
	}
	common.SetContextKey(c, constant.ContextKeyRoutingEstimatedOutput, max(outputProxy, 1))
	setRoutingCapacityEstimate(c, estimate)
	common.SetContextKey(c, constant.ContextKeyRoutingCostProfile, buildRoutingCostRequestProfile(
		c.Request.URL.Path,
		body,
		c.Request.Header,
		estimate,
		promptProxy,
		max(outputProxy, 1),
	))
	if c.Query("alt") == "sse" {
		common.SetContextKey(c, constant.ContextKeyIsStream, true)
	} else if estimate.StreamKnown {
		common.SetContextKey(c, constant.ContextKeyIsStream, estimate.Stream)
	}
}

func setRoutingCapacityEstimate(c *gin.Context, estimate routingCapacityTokenEstimate) {
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityInput, estimate.Input.Tokens)
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityInputKnown, estimate.Input.Known())
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityInputState, estimate.Input.State)
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityOutput, estimate.Output.Tokens)
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityOutputKnown, estimate.Output.Known())
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityOutputState, estimate.Output.State)
}

// channelSupportsRequestPath reports whether a channel can serve the request path.
// Only Advanced Custom (type 58) channels are path-checked; all other channel types
// always pass. A type-58 channel is usable only when one of its routes matches.
func channelSupportsRequestPath(channel *model.Channel, requestPath string) bool {
	if channel == nil {
		return false
	}
	if channel.Type != constant.ChannelTypeAdvancedCustom {
		return true
	}
	config := channel.GetOtherSettings().AdvancedCustom
	return config != nil && config.SupportsPath(requestPath)
}

// getModelFromRequest 从请求中读取模型信息
// 根据 Content-Type 自动处理：
// - application/json
// - application/x-www-form-urlencoded
// - multipart/form-data
func getModelFromRequest(c *gin.Context) (*ModelRequest, error) {
	if strings.HasPrefix(c.Request.Header.Get("Content-Type"), "application/json") {
		modelRequest, err := getModelFromJSONBody(c)
		if err != nil {
			return nil, errors.New(i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}))
		}
		return modelRequest, nil
	}

	var modelRequest ModelRequest
	err := common.UnmarshalBodyReusable(c, &modelRequest)
	if err != nil {
		return nil, errors.New(i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}))
	}
	return &modelRequest, nil
}

func getModelFromJSONBody(c *gin.Context) (*ModelRequest, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	requestBody, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	if !gjson.ValidBytes(requestBody) {
		return nil, errors.New("invalid JSON request body")
	}

	values := gjson.GetManyBytes(requestBody, "model", "group")
	model, err := getJSONStringValue(values[0], "model")
	if err != nil {
		return nil, err
	}
	group, err := getJSONStringValue(values[1], "group")
	if err != nil {
		return nil, err
	}

	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr != nil {
		return nil, seekErr
	}
	c.Request.Body = io.NopCloser(storage)

	return &ModelRequest{
		Model: model,
		Group: group,
	}, nil
}

func getJSONStringValue(result gjson.Result, field string) (string, error) {
	if !result.Exists() || result.Type == gjson.Null {
		return "", nil
	}
	if result.Type != gjson.String {
		return "", fmt.Errorf("field %s must be a string", field)
	}
	return result.String(), nil
}

func getModelRequest(c *gin.Context) (*ModelRequest, bool, error) {
	var modelRequest ModelRequest
	shouldSelectChannel := true
	var err error
	if strings.Contains(c.Request.URL.Path, "/mj/") {
		relayMode := relayconstant.Path2RelayModeMidjourney(c.Request.URL.Path)
		if relayMode == relayconstant.RelayModeMidjourneyTaskFetch ||
			relayMode == relayconstant.RelayModeMidjourneyTaskFetchByCondition ||
			relayMode == relayconstant.RelayModeMidjourneyNotify ||
			relayMode == relayconstant.RelayModeMidjourneyTaskImageSeed {
			shouldSelectChannel = false
		} else {
			midjourneyRequest := dto.MidjourneyRequest{}
			err = common.UnmarshalBodyReusable(c, &midjourneyRequest)
			if err != nil {
				return nil, false, errors.New(i18n.T(c, i18n.MsgDistributorInvalidMidjourney, map[string]any{"Error": err.Error()}))
			}
			midjourneyModel, mjErr, success := service.GetMjRequestModel(relayMode, &midjourneyRequest)
			if mjErr != nil {
				return nil, false, fmt.Errorf("%s", mjErr.Description)
			}
			if midjourneyModel == "" {
				if !success {
					return nil, false, fmt.Errorf("%s", i18n.T(c, i18n.MsgDistributorInvalidParseModel))
				} else {
					// task fetch, task fetch by condition, notify
					shouldSelectChannel = false
				}
			}
			modelRequest.Model = midjourneyModel
		}
		c.Set("relay_mode", relayMode)
	} else if strings.Contains(c.Request.URL.Path, "/suno/") {
		relayMode := relayconstant.Path2RelaySuno(c.Request.Method, c.Request.URL.Path)
		if relayMode == relayconstant.RelayModeSunoFetch ||
			relayMode == relayconstant.RelayModeSunoFetchByID {
			shouldSelectChannel = false
		} else {
			modelName := service.CoverTaskActionToModelName(constant.TaskPlatformSuno, c.Param("action"))
			modelRequest.Model = modelName
		}
		c.Set("platform", string(constant.TaskPlatformSuno))
		c.Set("relay_mode", relayMode)
	} else if strings.Contains(c.Request.URL.Path, "/v1/videos/") && strings.HasSuffix(c.Request.URL.Path, "/remix") {
		relayMode := relayconstant.RelayModeVideoSubmit
		c.Set("relay_mode", relayMode)
		shouldSelectChannel = false
	} else if strings.Contains(c.Request.URL.Path, "/v1/videos") {
		//curl https://api.openai.com/v1/videos \
		//  -H "Authorization: Bearer $OPENAI_API_KEY" \
		//  -F "model=sora-2" \
		//  -F "prompt=A calico cat playing a piano on stage"
		//	-F input_reference="@image.jpg"
		relayMode := relayconstant.RelayModeUnknown
		if c.Request.Method == http.MethodPost {
			relayMode = relayconstant.RelayModeVideoSubmit
			req, err := getModelFromRequest(c)
			if err != nil {
				return nil, false, err
			}
			if req != nil {
				modelRequest.Model = req.Model
			}
		} else if c.Request.Method == http.MethodGet {
			relayMode = relayconstant.RelayModeVideoFetchByID
			shouldSelectChannel = false
			modelRequest.Model = getTaskOriginModelName(c)
		}
		c.Set("relay_mode", relayMode)
	} else if strings.Contains(c.Request.URL.Path, "/v1/video/generations") {
		relayMode := relayconstant.RelayModeUnknown
		if c.Request.Method == http.MethodPost {
			req, err := getModelFromRequest(c)
			if err != nil {
				return nil, false, err
			}
			modelRequest.Model = req.Model
			relayMode = relayconstant.RelayModeVideoSubmit
		} else if c.Request.Method == http.MethodGet {
			relayMode = relayconstant.RelayModeVideoFetchByID
			shouldSelectChannel = false
			modelRequest.Model = getTaskOriginModelName(c)
		}
		if _, ok := c.Get("relay_mode"); !ok {
			c.Set("relay_mode", relayMode)
		}
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") || strings.HasPrefix(c.Request.URL.Path, "/v1/models/") {
		// Gemini API 路径处理: /v1beta/models/gemini-2.0-flash:generateContent
		relayMode := relayconstant.RelayModeGemini
		modelName := extractModelNameFromGeminiPath(c.Request.URL.Path)
		if modelName != "" {
			modelRequest.Model = modelName
		}
		c.Set("relay_mode", relayMode)
	} else if !strings.HasPrefix(c.Request.URL.Path, "/v1/audio/transcriptions") && !strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
		req, err := getModelFromRequest(c)
		if err != nil {
			return nil, false, err
		}
		modelRequest.Model = req.Model
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/realtime") {
		//wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview-2024-10-01
		modelRequest.Model = c.Query("model")
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/moderations") {
		if modelRequest.Model == "" {
			modelRequest.Model = "text-moderation-stable"
		}
	}
	if strings.HasSuffix(c.Request.URL.Path, "embeddings") {
		if modelRequest.Model == "" {
			modelRequest.Model = c.Param("model")
		}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/images/generations") {
		modelRequest.Model = common.GetStringIfEmpty(modelRequest.Model, "dall-e")
	} else if strings.HasPrefix(c.Request.URL.Path, "/v1/images/edits") {
		//modelRequest.Model = common.GetStringIfEmpty(c.PostForm("model"), "gpt-image-1")
		contentType := c.ContentType()
		if slices.Contains([]string{gin.MIMEPOSTForm, gin.MIMEMultipartPOSTForm}, contentType) {
			req, err := getModelFromRequest(c)
			if err == nil && req.Model != "" {
				modelRequest.Model = req.Model
			}
		}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/audio") {
		relayMode := relayconstant.RelayModeAudioSpeech
		if strings.HasPrefix(c.Request.URL.Path, "/v1/audio/speech") {

			modelRequest.Model = common.GetStringIfEmpty(modelRequest.Model, "tts-1")
		} else if strings.HasPrefix(c.Request.URL.Path, "/v1/audio/translations") {
			// 先尝试从请求读取
			if req, err := getModelFromRequest(c); err == nil && req.Model != "" {
				modelRequest.Model = req.Model
			}
			modelRequest.Model = common.GetStringIfEmpty(modelRequest.Model, "whisper-1")
			relayMode = relayconstant.RelayModeAudioTranslation
		} else if strings.HasPrefix(c.Request.URL.Path, "/v1/audio/transcriptions") {
			// 先尝试从请求读取
			if req, err := getModelFromRequest(c); err == nil && req.Model != "" {
				modelRequest.Model = req.Model
			}
			modelRequest.Model = common.GetStringIfEmpty(modelRequest.Model, "whisper-1")
			relayMode = relayconstant.RelayModeAudioTranscription
		}
		c.Set("relay_mode", relayMode)
	}
	if strings.HasPrefix(c.Request.URL.Path, "/pg/chat/completions") {
		// playground chat completions
		req, err := getModelFromRequest(c)
		if err != nil {
			return nil, false, err
		}
		modelRequest.Model = req.Model
		modelRequest.Group = req.Group
		common.SetContextKey(c, constant.ContextKeyTokenGroup, modelRequest.Group)
	}

	if strings.HasPrefix(c.Request.URL.Path, "/v1/responses/compact") && modelRequest.Model != "" {
		modelRequest.Model = ratio_setting.WithCompactModelSuffix(modelRequest.Model)
	}
	return &modelRequest, shouldSelectChannel, nil
}

// 修复 #4834: GET /v1/video/generations/:task_id && /v1/video/:task_id 此前不解析 model，
// 当 token 启用「可用模型限制」时，下游 modelLimitEnable 校验会因
// modelRequest.Model 为空而误报 "This token has no access to model"。
// 从已存储的任务记录中回填 OriginModelName 即可让校验走在正确的模型上。
func getTaskOriginModelName(c *gin.Context) string {
	if !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return ""
	}

	taskId := c.Param("task_id")
	if taskId == "" {
		// jimeng adapter
		taskId = c.GetString("task_id")
	}
	if taskId == "" {
		return ""
	}

	userId := c.GetInt("id")
	if task, exist, err := model.GetByTaskId(userId, taskId); err == nil && exist && task != nil {
		return task.GetOriginModelName()
	}
	return ""
}

func SetupContextForSelectedChannel(c *gin.Context, channel *model.Channel, modelName string) *types.NewAPIError {
	if setupErr := SetupContextForSelectedChannelMetadata(c, channel, modelName); setupErr != nil {
		return setupErr
	}
	return CommitSelectedChannelCredential(c, channel)
}

func SetupContextForSelectedChannelMetadata(c *gin.Context, channel *model.Channel, modelName string) *types.NewAPIError {
	c.Set("original_model", modelName) // for retry
	common.SetContextKey(c, constant.ContextKeyChannelKey, "")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(c, constant.ContextKeyRoutingSnapshotRevision, uint64(0))
	common.SetContextKey(c, constant.ContextKeyRoutingPoolID, 0)
	common.SetContextKey(c, constant.ContextKeyRoutingMemberID, 0)
	common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, 0)
	common.SetContextKey(c, constant.ContextKeyRoutingFailureDomainHash, "")
	if channel == nil {
		return types.NewError(errors.New("channel is nil"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if _, blocked := channelrouting.ChannelBalanceRuntimeBlocked(channel.Id, time.Now()); blocked {
		return types.NewErrorWithStatusCode(
			errors.New(channelrouting.ExclusionReasonChannelBalance),
			types.ErrorCodeGetChannelFailed,
			http.StatusServiceUnavailable,
			types.ErrOptionWithSkipRetry(),
		)
	}
	trafficAdmissible, trafficErr := service.ChannelRoutingTrafficAdmissible(c, channel.Id)
	if trafficErr != nil {
		return types.NewErrorWithStatusCode(
			trafficErr,
			types.ErrorCodeGetChannelFailed,
			http.StatusServiceUnavailable,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if !trafficAdmissible {
		return types.NewErrorWithStatusCode(
			service.ErrRoutingChannelTrafficRestricted,
			types.ErrorCodeAccessDenied,
			http.StatusForbidden,
			types.ErrOptionWithSkipRetry(),
		)
	}
	common.SetContextKey(c, constant.ContextKeyChannelId, channel.Id)
	common.SetContextKey(c, constant.ContextKeyChannelName, channel.Name)
	common.SetContextKey(c, constant.ContextKeyChannelType, channel.Type)
	common.SetContextKey(c, constant.ContextKeyChannelCreateTime, channel.CreatedTime)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, channel.GetSetting())
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, channel.GetOtherSettings())
	paramOverride := channel.GetParamOverride()
	headerOverride := channel.GetHeaderOverride()
	if mergedParam, applied := service.ApplyChannelAffinityOverrideTemplate(c, paramOverride); applied {
		paramOverride = mergedParam
	}
	common.SetContextKey(c, constant.ContextKeyChannelParamOverride, paramOverride)
	common.SetContextKey(c, constant.ContextKeyChannelHeaderOverride, headerOverride)
	if nil != channel.OpenAIOrganization && *channel.OpenAIOrganization != "" {
		common.SetContextKey(c, constant.ContextKeyChannelOrganization, *channel.OpenAIOrganization)
	}
	common.SetContextKey(c, constant.ContextKeyChannelAutoBan, channel.GetAutoBan())
	common.SetContextKey(c, constant.ContextKeyChannelModelMapping, channel.GetModelMapping())
	common.SetContextKey(c, constant.ContextKeyChannelStatusCodeMapping, channel.GetStatusCodeMapping())

	routingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if routingGroup == "auto" {
		if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			routingGroup = selectedGroup
		}
	}
	selectedIdentity, selected := service.GetSelectedRoutingIdentity(c, channel.Id)
	if selected {
		common.SetContextKey(c, constant.ContextKeyRoutingSnapshotRevision, selectedIdentity.SnapshotRevision)
		common.SetContextKey(c, constant.ContextKeyRoutingPoolID, selectedIdentity.PoolID)
		common.SetContextKey(c, constant.ContextKeyRoutingMemberID, selectedIdentity.MemberID)
	}
	failureDomainHash := selectedIdentity.FailureDomainHash
	if failureDomainHash == "" {
		if identity, ok := channelrouting.ResolveIdentity(routingGroup, channel.Id, ""); ok {
			failureDomainHash = identity.FailureDomainHash
		}
	}
	common.SetContextKey(c, constant.ContextKeyRoutingFailureDomainHash, failureDomainHash)
	channelBaseURL := channel.GetBaseURL()
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, channelBaseURL)
	common.SetContextKey(c, constant.ContextKeyRoutingEndpointHost, channelrouting.EndpointHost(channelBaseURL, channel.Id))
	common.SetContextKey(c, constant.ContextKeyRoutingEndpointAuthority, channelrouting.EndpointAuthority(channelBaseURL, channel.Id))
	common.SetContextKey(c, constant.ContextKeyRoutingRegion, channelrouting.RoutingRegion())

	common.SetContextKey(c, constant.ContextKeySystemPromptOverride, false)

	// TODO: api_version统一
	switch channel.Type {
	case constant.ChannelTypeAzure:
		c.Set("api_version", channel.Other)
	case constant.ChannelTypeVertexAi:
		c.Set("region", channel.Other)
	case constant.ChannelTypeXunfei:
		c.Set("api_version", channel.Other)
	case constant.ChannelTypeGemini:
		c.Set("api_version", channel.Other)
	case constant.ChannelTypeAli:
		c.Set("plugin", channel.Other)
	case constant.ChannelCloudflare:
		c.Set("api_version", channel.Other)
	case constant.ChannelTypeMokaAI:
		c.Set("api_version", channel.Other)
	case constant.ChannelTypeCoze:
		c.Set("bot_id", channel.Other)
	}
	return nil
}

func CommitSelectedChannelCredential(c *gin.Context, channel *model.Channel) *types.NewAPIError {
	common.SetContextKey(c, constant.ContextKeyChannelKey, "")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, 0)
	if channel == nil {
		service.ClearSelectedRoutingIdentity(c)
		return types.NewError(errors.New("channel is nil"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	selected, planned := service.GetSelectedRoutingIdentity(c, channel.Id)
	key := ""
	index := model.RoutingMetricSingleKeyIndex
	var newAPIError *types.NewAPIError
	if planned && selected.CredentialID > 0 {
		var resolved bool
		key, index, resolved = channelrouting.ResolveCredentialKey(channel, selected.CredentialID)
		if !resolved {
			newAPIError = types.NewError(
				errors.New("selected routing credential is no longer available"),
				types.ErrorCodeChannelNoAvailableKey,
			)
		}
	} else if planned && selected.CredentialID == 0 && strings.TrimSpace(channel.Key) == "" {
		key = ""
	} else {
		key, index, newAPIError = channel.GetNextEnabledKey()
	}
	if newAPIError != nil {
		common.SetContextKey(c, constant.ContextKeyRoutingSnapshotRevision, uint64(0))
		common.SetContextKey(c, constant.ContextKeyRoutingPoolID, 0)
		common.SetContextKey(c, constant.ContextKeyRoutingMemberID, 0)
		service.ClearSelectedRoutingIdentity(c)
		return newAPIError
	}
	isMultiKey := channel.ChannelInfo.IsMultiKey
	if !isMultiKey {
		index = model.RoutingMetricSingleKeyIndex
	}
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, isMultiKey)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, index)
	common.SetContextKey(c, constant.ContextKeyChannelKey, key)

	routingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if routingGroup == "auto" {
		if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			routingGroup = selectedGroup
		}
	}
	if planned && selected.CredentialID == 0 && key != "" {
		if identity, resolved := channelrouting.ResolveIdentity(routingGroup, channel.Id, key); resolved &&
			identity.PoolID == selected.PoolID && identity.MemberID == selected.MemberID {
			selected.CredentialID = identity.CredentialID
		}
	}
	if planned {
		common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, selected.CredentialID)
		service.ClearSelectedRoutingIdentity(c)
	} else if smart_routing_setting.Enabled() {
		if identity, ok := channelrouting.ResolveIdentity(routingGroup, channel.Id, key); ok {
			common.SetContextKey(c, constant.ContextKeyRoutingSnapshotRevision, identity.SnapshotRevision)
			common.SetContextKey(c, constant.ContextKeyRoutingPoolID, identity.PoolID)
			common.SetContextKey(c, constant.ContextKeyRoutingMemberID, identity.MemberID)
			common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, identity.CredentialID)
		}
	}
	return nil
}

// CommitTaskChannelCredential binds a stateful continuation to the exact
// credential used by its origin task. Historical single-key rows may omit the
// stable ID; multi-key rows fail closed because an array position is not a
// durable credential identity.
func CommitTaskChannelCredential(c *gin.Context, channel *model.Channel, credentialID int) *types.NewAPIError {
	if c == nil || channel == nil || channel.Id <= 0 {
		return types.NewError(errors.New("channel is nil"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	common.SetContextKey(c, constant.ContextKeyChannelKey, "")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, 0)
	key := ""
	index := model.RoutingMetricSingleKeyIndex
	if credentialID > 0 {
		requestContext := c.Request.Context()
		resolvedKey, resolvedIndex, err := channelrouting.ResolvePersistedCredentialKey(requestContext, channel, credentialID)
		if err != nil {
			return types.NewError(err, types.ErrorCodeChannelNoAvailableKey, types.ErrOptionWithSkipRetry())
		}
		key = resolvedKey
		index = resolvedIndex
	} else {
		keys := channel.GetKeys()
		if channel.ChannelInfo.IsMultiKey || len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
			return types.NewError(
				channelrouting.ErrPersistedCredentialUnavailable,
				types.ErrorCodeChannelNoAvailableKey,
				types.ErrOptionWithSkipRetry(),
			)
		}
		key = keys[0]
		if resolvedID, err := channelrouting.ResolvePersistedCredentialID(c.Request.Context(), channel, key); err == nil {
			credentialID = resolvedID
		}
	}

	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, channel.ChannelInfo.IsMultiKey)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, index)
	common.SetContextKey(c, constant.ContextKeyChannelKey, key)
	common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, credentialID)

	routingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if routingGroup == "auto" {
		if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			routingGroup = selectedGroup
		}
	}
	if identity, ok := channelrouting.ResolveIdentity(routingGroup, channel.Id, key); ok {
		if credentialID > 0 && identity.CredentialID > 0 && identity.CredentialID != credentialID {
			return types.NewError(
				channelrouting.ErrPersistedCredentialUnavailable,
				types.ErrorCodeChannelNoAvailableKey,
				types.ErrOptionWithSkipRetry(),
			)
		}
		common.SetContextKey(c, constant.ContextKeyRoutingSnapshotRevision, identity.SnapshotRevision)
		common.SetContextKey(c, constant.ContextKeyRoutingPoolID, identity.PoolID)
		common.SetContextKey(c, constant.ContextKeyRoutingMemberID, identity.MemberID)
		if credentialID == 0 {
			credentialID = identity.CredentialID
			common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, credentialID)
		}
	}
	return nil
}

// extractModelNameFromGeminiPath 从 Gemini API URL 路径中提取模型名
// 输入格式: /v1beta/models/gemini-2.0-flash:generateContent
// 输出: gemini-2.0-flash
func extractModelNameFromGeminiPath(path string) string {
	// 查找 "/models/" 的位置
	modelsPrefix := "/models/"
	modelsIndex := strings.Index(path, modelsPrefix)
	if modelsIndex == -1 {
		return ""
	}

	// 从 "/models/" 之后开始提取
	startIndex := modelsIndex + len(modelsPrefix)
	if startIndex >= len(path) {
		return ""
	}

	// 查找 ":" 的位置，模型名在 ":" 之前
	colonIndex := strings.Index(path[startIndex:], ":")
	if colonIndex == -1 {
		// 如果没有找到 ":"，返回从 "/models/" 到路径结尾的部分
		return path[startIndex:]
	}

	// 返回模型名部分
	return path[startIndex : startIndex+colonIndex]
}
