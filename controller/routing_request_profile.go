package controller

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
)

func validatedRoutingModelName(c *gin.Context, relayFormat types.RelayFormat, request dto.Request) string {
	var modelName string
	switch typed := request.(type) {
	case *dto.GeneralOpenAIRequest:
		modelName = typed.Model
	case *dto.OpenAIResponsesRequest:
		modelName = typed.Model
	case *dto.OpenAIResponsesCompactionRequest:
		modelName = typed.Model
	case *dto.ClaudeRequest:
		modelName = typed.Model
	case *dto.ImageRequest:
		modelName = typed.Model
	case *dto.AudioRequest:
		modelName = typed.Model
	case *dto.EmbeddingRequest:
		modelName = typed.Model
	case *dto.RerankRequest:
		modelName = typed.Model
	case *dto.GeminiEmbeddingRequest:
		modelName = typed.Model
	case *dto.GeminiBatchEmbeddingRequest:
		if len(typed.Requests) > 0 {
			modelName = typed.Requests[0].Model
		}
	}
	if strings.TrimSpace(modelName) == "" && c != nil {
		modelName = c.GetString("original_model")
		if relayFormat == types.RelayFormatOpenAIRealtime && strings.TrimSpace(modelName) == "" {
			modelName = c.Query("model")
		}
	}
	modelName = strings.TrimSpace(modelName)
	if relayFormat == types.RelayFormatOpenAIResponsesCompaction && modelName != "" {
		modelName = ratio_setting.WithCompactModelSuffix(modelName)
	}
	return modelName
}

func applyValidatedRoutingTokenEstimate(
	c *gin.Context,
	relayFormat types.RelayFormat,
	inputTokens int,
	outputTokens int,
	outputKnown bool,
	profile channelrouting.RequestProfileV2Input,
) {
	if c == nil {
		return
	}
	inputKnown := constant.CountToken && relayFormat != types.RelayFormatOpenAIRealtime
	if profile.InputTokens.State == channelrouting.RequestQuantityNotApplicable {
		inputTokens = 0
		inputKnown = false
	} else if inputKnown {
		common.SetContextKey(c, constant.ContextKeyRoutingPromptProxy, max(inputTokens, 0))
	}
	if profile.OutputTokens.State == channelrouting.RequestQuantityNotApplicable {
		outputTokens = 0
		outputKnown = false
	}
	common.SetContextKey(c, constant.ContextKeyRoutingPromptKnown, inputKnown)
	common.SetContextKey(c, constant.ContextKeyRoutingEstimatedOutput, max(outputTokens, 0))
	common.SetContextKey(c, constant.ContextKeyRoutingOutputKnown, outputKnown)
	setValidatedRoutingCapacityDimension(
		c,
		constant.ContextKeyRoutingCapacityInput,
		constant.ContextKeyRoutingCapacityInputKnown,
		constant.ContextKeyRoutingCapacityInputState,
		profile.InputTokens,
		inputTokens,
		inputKnown,
	)
	setValidatedRoutingCapacityDimension(
		c,
		constant.ContextKeyRoutingCapacityOutput,
		constant.ContextKeyRoutingCapacityOutputKnown,
		constant.ContextKeyRoutingCapacityOutputState,
		profile.OutputTokens,
		outputTokens,
		outputKnown,
	)
}

func setValidatedRoutingCapacityDimension(
	c *gin.Context,
	valueKey constant.ContextKey,
	knownKey constant.ContextKey,
	stateKey constant.ContextKey,
	quantity channelrouting.RequestQuantity,
	tokens int,
	known bool,
) {
	state := channelrouting.CapacityDimensionApplicableUnknown
	value := 0
	switch quantity.State {
	case channelrouting.RequestQuantityNotApplicable:
		state = channelrouting.CapacityDimensionNotApplicable
	case channelrouting.RequestQuantityKnown:
		state = channelrouting.CapacityDimensionBoundedKnown
		value = int(quantity.Value)
	case channelrouting.RequestQuantityUnknown:
		if known {
			state = channelrouting.CapacityDimensionBoundedKnown
			value = max(tokens, 0)
		} else if existing, ok := common.GetContextKey(c, stateKey); ok {
			if existingState, typeOK := existing.(channelrouting.CapacityDimensionState); typeOK &&
				existingState == channelrouting.CapacityDimensionBoundedKnown {
				state = existingState
				value = max(common.GetContextKeyInt(c, valueKey), 0)
			}
		}
	}
	common.SetContextKey(c, valueKey, value)
	common.SetContextKey(c, knownKey, state == channelrouting.CapacityDimensionBoundedKnown)
	common.SetContextKey(c, stateKey, state)
}

func buildRoutingRequestProfileTemplate(
	c *gin.Context,
	relayFormat types.RelayFormat,
	request dto.Request,
	modelName string,
) (channelrouting.RequestProfileV2Input, error) {
	modelName = strings.TrimSpace(modelName)
	if c == nil || c.Request == nil || request == nil || modelName == "" {
		return channelrouting.RequestProfileV2Input{}, errors.New("channel routing request profile is incomplete")
	}

	input := channelrouting.RequestProfileV2Input{
		RequestPath:      c.Request.URL.Path,
		GroupName:        common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		ModelName:        modelName,
		InputModalities:  channelrouting.RequestModalityText,
		OutputModalities: channelrouting.RequestModalityText,
		IsStream:         request.IsStream(c),
		InputTokens:      channelrouting.UnknownRequestQuantity(),
		OutputTokens:     channelrouting.UnknownRequestQuantity(),
		CachedTokens:     channelrouting.UnknownRequestQuantity(),
		ImageUnits:       channelrouting.NotApplicableRequestQuantity(),
		AudioMillis:      channelrouting.NotApplicableRequestQuantity(),
		VideoMillis:      channelrouting.NotApplicableRequestQuantity(),
		Region:           channelrouting.RoutingRegion(),
		TenantTier:       channelrouting.RequestTenantTierStandard,
		TrafficClass:     channelrouting.RequestTrafficClassStandard,
	}
	if input.GroupName == "" {
		input.GroupName = "default"
	}
	if deadline, ok := c.Request.Context().Deadline(); ok {
		input.DeadlineUnixMs = deadline.UnixMilli()
	}
	input.IdempotencyKeyPresent = strings.TrimSpace(c.GetHeader("Idempotency-Key")) != "" ||
		strings.TrimSpace(c.GetHeader("X-Idempotency-Key")) != ""

	switch relayFormat {
	case types.RelayFormatOpenAI:
		input.RequestKind = channelrouting.RequestKindChatCompletions
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAI
		openAIRequest, ok := request.(*dto.GeneralOpenAIRequest)
		if !ok {
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid OpenAI routing request")
		}
		if len(openAIRequest.Tools) > 0 || routingJSONPresent(openAIRequest.Functions) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityTools
		}
		if openAIRequest.ResponseFormat != nil && (openAIRequest.ResponseFormat.Type == "json_schema" || routingJSONPresent(openAIRequest.ResponseFormat.JsonSchema)) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityJSONSchema
		}
		for messageIndex := range openAIRequest.Messages {
			for _, media := range openAIRequest.Messages[messageIndex].ParseContent() {
				applyOpenAIMediaProfile(&input, media.Type)
			}
		}
		for _, modality := range routingStringList(openAIRequest.Modalities) {
			applyRoutingOutputModality(&input, modality)
		}
		if lo.FromPtrOr(openAIRequest.ReturnImages, false) {
			input.OutputModalities |= channelrouting.RequestModalityImage
			input.RequiredCapabilities |= channelrouting.RequestCapabilityImageOutput
		}
	case types.RelayFormatOpenAIResponses:
		input.RequestKind = channelrouting.RequestKindResponses
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAIResponses
		responsesRequest, ok := request.(*dto.OpenAIResponsesRequest)
		if !ok {
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid Responses routing request")
		}
		if routingJSONPresent(responsesRequest.Tools) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityTools
		}
		if routingJSONPathEquals(responsesRequest.Text, "format.type", "json_schema") ||
			routingJSONPathEquals(responsesRequest.Text, "type", "json_schema") {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityJSONSchema
		}
		for _, media := range responsesRequest.ParseInput() {
			switch media.Type {
			case "input_image":
				input.InputModalities |= channelrouting.RequestModalityImage
				input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
			case "input_file":
				input.InputModalities |= channelrouting.RequestModalityFile
				input.RequiredCapabilities |= channelrouting.RequestCapabilityFileInput
			}
		}
		if responsesRequest.PreviousResponseID != "" || routingJSONPresent(responsesRequest.Conversation) ||
			routingJSONPresent(responsesRequest.ContextManagement) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
		}
	case types.RelayFormatOpenAIResponsesCompaction:
		input.RequestKind = channelrouting.RequestKindResponsesCompact
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAIResponsesCompaction
		compactionRequest, ok := request.(*dto.OpenAIResponsesCompactionRequest)
		if !ok {
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid Responses compaction routing request")
		}
		if compactionRequest.PreviousResponseID != "" {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
		}
	case types.RelayFormatClaude:
		input.RequestKind = channelrouting.RequestKindClaudeMessages
		input.SourceFormat = channelrouting.RequestSourceFormatClaude
		claudeRequest, ok := request.(*dto.ClaudeRequest)
		if !ok {
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid Claude routing request")
		}
		input.TrafficClass = classifyRoutingRequestTraffic(c, relayFormat, claudeRequest)
		if len(claudeRequest.GetTools()) > 0 || routingJSONPresent(claudeRequest.McpServers) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityTools
		}
		if routingJSONPathEquals(claudeRequest.OutputFormat, "type", "json_schema") ||
			routingJSONPathEquals(claudeRequest.OutputConfig, "format.type", "json_schema") {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityJSONSchema
		}
		for messageIndex := range claudeRequest.Messages {
			media, _ := claudeRequest.Messages[messageIndex].ParseContent()
			for _, item := range media {
				if item.Type == "image" {
					input.InputModalities |= channelrouting.RequestModalityImage
					input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
				}
			}
		}
		if routingJSONPresent(claudeRequest.Container) || routingJSONPresent(claudeRequest.ContextManagement) {
			input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
		}
	case types.RelayFormatGemini:
		switch geminiRequest := request.(type) {
		case *dto.GeminiChatRequest:
			input.RequestKind = channelrouting.RequestKindGeminiGenerate
			input.SourceFormat = channelrouting.RequestSourceFormatGemini
			applyGeminiRoutingProfile(&input, geminiRequest)
		case *dto.GeminiEmbeddingRequest:
			input.RequestKind = channelrouting.RequestKindGeminiEmbedding
			input.SourceFormat = channelrouting.RequestSourceFormatGeminiEmbedding
			input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
			input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
		case *dto.GeminiBatchEmbeddingRequest:
			input.RequestKind = channelrouting.RequestKindGeminiBatchEmbed
			input.SourceFormat = channelrouting.RequestSourceFormatGeminiBatchEmbedding
			input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
			input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
		default:
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid Gemini routing request")
		}
	case types.RelayFormatOpenAIImage:
		input.RequestKind = channelrouting.RequestKindImage
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAIImage
		input.OutputModalities = channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityImageOutput
		input.InputTokens = channelrouting.NotApplicableRequestQuantity()
		input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
		input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
		input.AudioMillis = channelrouting.NotApplicableRequestQuantity()
		input.VideoMillis = channelrouting.NotApplicableRequestQuantity()
		imageRequest, ok := request.(*dto.ImageRequest)
		if !ok {
			return channelrouting.RequestProfileV2Input{}, errors.New("invalid image routing request")
		}
		input.ImageUnits = channelrouting.KnownRequestQuantity(int64(lo.FromPtrOr(imageRequest.N, uint(1))))
		if relayconstant.Path2RelayMode(c.Request.URL.Path) == relayconstant.RelayModeImagesEdits ||
			routingJSONPresent(imageRequest.Image) || routingJSONPresent(imageRequest.Images) || routingJSONPresent(imageRequest.Mask) {
			input.InputModalities |= channelrouting.RequestModalityImage
			input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
		}
	case types.RelayFormatOpenAIAudio:
		input.RequestKind = channelrouting.RequestKindAudio
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAIAudio
		input.InputTokens = channelrouting.NotApplicableRequestQuantity()
		input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
		input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
		input.AudioMillis = channelrouting.UnknownRequestQuantity()
		if strings.Contains(c.Request.URL.Path, "/speech") {
			input.OutputModalities = channelrouting.RequestModalityAudio
			input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioOutput
		} else {
			input.InputModalities = channelrouting.RequestModalityAudio | channelrouting.RequestModalityFile
			input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioInput | channelrouting.RequestCapabilityFileInput
		}
	case types.RelayFormatEmbedding:
		input.RequestKind = channelrouting.RequestKindEmbedding
		input.SourceFormat = channelrouting.RequestSourceFormatEmbedding
		input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
		input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
	case types.RelayFormatRerank:
		input.RequestKind = channelrouting.RequestKindRerank
		input.SourceFormat = channelrouting.RequestSourceFormatRerank
		input.OutputTokens = channelrouting.NotApplicableRequestQuantity()
		input.CachedTokens = channelrouting.NotApplicableRequestQuantity()
	case types.RelayFormatOpenAIRealtime:
		input.RequestKind = channelrouting.RequestKindRealtime
		input.SourceFormat = channelrouting.RequestSourceFormatOpenAIRealtime
		input.IsStream = true
		input.RequiredCapabilities = channelrouting.RequestCapabilityRealtime | channelrouting.RequestCapabilityStateful
		input.AudioMillis = channelrouting.UnknownRequestQuantity()
	default:
		return channelrouting.RequestProfileV2Input{}, errors.New("unsupported channel routing request format")
	}

	input.RetrySafety = channelrouting.RequestRetrySafetySafe
	input.RetryAllowed = true
	input.CrossChannelRetryAllowed = true
	input.HedgeAllowed = input.RequestKind == channelrouting.RequestKindChatCompletions ||
		input.RequestKind == channelrouting.RequestKindResponses ||
		input.RequestKind == channelrouting.RequestKindClaudeMessages ||
		input.RequestKind == channelrouting.RequestKindGeminiGenerate
	if input.RequiredCapabilities&channelrouting.RequestCapabilityStateful != 0 {
		input.RetrySafety = channelrouting.RequestRetrySafetyConditional
		input.RetryAllowed = input.IdempotencyKeyPresent
		input.CrossChannelRetryAllowed = false
		input.HedgeAllowed = false
	}
	if input.RequestKind == channelrouting.RequestKindImage {
		input.RetrySafety = channelrouting.RequestRetrySafetyUnsafe
		input.RetryAllowed = false
		input.CrossChannelRetryAllowed = false
		input.HedgeAllowed = false
	}
	if input.RequestKind == channelrouting.RequestKindRealtime {
		input.RetrySafety = channelrouting.RequestRetrySafetyUnsafe
		input.RetryAllowed = false
		input.CrossChannelRetryAllowed = false
		input.HedgeAllowed = false
	}
	return input, nil
}

func buildTaskRoutingRequestProfileTemplate(
	c *gin.Context,
	info *relaycommon.RelayInfo,
) (channelrouting.RequestProfileV2Input, error) {
	if c == nil || c.Request == nil || info == nil || strings.TrimSpace(info.OriginModelName) == "" {
		return channelrouting.RequestProfileV2Input{}, errors.New("channel routing task profile is incomplete")
	}
	input := channelrouting.RequestProfileV2Input{
		RequestPath:          c.Request.URL.Path,
		GroupName:            common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		ModelName:            strings.TrimSpace(info.OriginModelName),
		InputModalities:      channelrouting.RequestModalityText,
		OutputModalities:     channelrouting.RequestModalityVideo,
		RequiredCapabilities: channelrouting.RequestCapabilityVideoOutput,
		InputTokens:          channelrouting.NotApplicableRequestQuantity(),
		OutputTokens:         channelrouting.NotApplicableRequestQuantity(),
		CachedTokens:         channelrouting.NotApplicableRequestQuantity(),
		ImageUnits:           channelrouting.NotApplicableRequestQuantity(),
		AudioMillis:          channelrouting.NotApplicableRequestQuantity(),
		VideoMillis:          channelrouting.UnknownRequestQuantity(),
		Region:               channelrouting.RoutingRegion(),
		RetrySafety:          channelrouting.RequestRetrySafetyUnsafe,
		TenantTier:           channelrouting.RequestTenantTierStandard,
	}
	if input.GroupName == "" {
		input.GroupName = "default"
	}
	if deadline, ok := c.Request.Context().Deadline(); ok {
		input.DeadlineUnixMs = deadline.UnixMilli()
	}
	input.IdempotencyKeyPresent = strings.TrimSpace(c.GetHeader("Idempotency-Key")) != "" ||
		strings.TrimSpace(c.GetHeader("X-Idempotency-Key")) != ""

	if constant.TaskPlatform(c.GetString("platform")) == constant.TaskPlatformSuno {
		input.RequestKind = channelrouting.RequestKindSuno
		input.SourceFormat = channelrouting.RequestSourceFormatSuno
		input.VideoMillis = channelrouting.NotApplicableRequestQuantity()
		if info.Action == constant.SunoActionLyrics {
			input.OutputModalities = channelrouting.RequestModalityText
			input.RequiredCapabilities = 0
		} else {
			input.OutputModalities = channelrouting.RequestModalityAudio
			input.RequiredCapabilities = channelrouting.RequestCapabilityAudioOutput
			input.AudioMillis = channelrouting.UnknownRequestQuantity()
		}
		if request, ok := c.Get("task_request"); ok {
			if sunoRequest, typeOK := request.(*dto.SunoSubmitReq); typeOK && sunoRequest != nil &&
				(strings.TrimSpace(sunoRequest.TaskID) != "" || strings.TrimSpace(sunoRequest.ContinueClipId) != "") {
				input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
			}
		}
		return input, nil
	}

	input.RequestKind = channelrouting.RequestKindTask
	input.SourceFormat = channelrouting.RequestSourceFormatTask
	request, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return channelrouting.RequestProfileV2Input{}, err
	}
	if request.HasImage() || strings.TrimSpace(request.InputReference) != "" {
		input.InputModalities |= channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
	}
	seconds := request.Duration
	if seconds == 0 && strings.TrimSpace(request.Seconds) != "" {
		seconds, _ = strconv.Atoi(request.Seconds)
	}
	if seconds > 0 {
		input.VideoMillis = channelrouting.KnownRequestQuantity(int64(seconds) * int64(time.Second/time.Millisecond))
	}
	if info.Action == constant.TaskActionRemix || strings.Contains(c.Request.URL.Path, "/remix") {
		input.InputModalities |= channelrouting.RequestModalityVideo
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVideoInput | channelrouting.RequestCapabilityStateful
	}
	return input, nil
}

func buildMidjourneyRoutingRequestProfileTemplate(
	c *gin.Context,
	modelName string,
	relayMode int,
) (channelrouting.RequestProfileV2Input, error) {
	modelName = strings.TrimSpace(modelName)
	if c == nil || c.Request == nil || modelName == "" {
		return channelrouting.RequestProfileV2Input{}, errors.New("channel routing Midjourney profile is incomplete")
	}
	input := channelrouting.RequestProfileV2Input{
		RequestPath:          c.Request.URL.Path,
		GroupName:            common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		ModelName:            modelName,
		RequestKind:          channelrouting.RequestKindMidjourney,
		SourceFormat:         channelrouting.RequestSourceFormatMidjourney,
		InputModalities:      channelrouting.RequestModalityText,
		OutputModalities:     channelrouting.RequestModalityImage,
		RequiredCapabilities: channelrouting.RequestCapabilityImageOutput,
		InputTokens:          channelrouting.NotApplicableRequestQuantity(),
		OutputTokens:         channelrouting.NotApplicableRequestQuantity(),
		CachedTokens:         channelrouting.NotApplicableRequestQuantity(),
		ImageUnits:           channelrouting.UnknownRequestQuantity(),
		AudioMillis:          channelrouting.NotApplicableRequestQuantity(),
		VideoMillis:          channelrouting.NotApplicableRequestQuantity(),
		Region:               channelrouting.RoutingRegion(),
		RetrySafety:          channelrouting.RequestRetrySafetyUnsafe,
		TenantTier:           channelrouting.RequestTenantTierStandard,
	}
	if input.GroupName == "" {
		input.GroupName = "default"
	}
	if deadline, ok := c.Request.Context().Deadline(); ok {
		input.DeadlineUnixMs = deadline.UnixMilli()
	}
	input.IdempotencyKeyPresent = strings.TrimSpace(c.GetHeader("Idempotency-Key")) != "" ||
		strings.TrimSpace(c.GetHeader("X-Idempotency-Key")) != ""
	switch relayMode {
	case relayconstant.RelayModeMidjourneyDescribe:
		input.InputModalities |= channelrouting.RequestModalityImage
		input.OutputModalities = channelrouting.RequestModalityText
		input.RequiredCapabilities = channelrouting.RequestCapabilityVision
	case relayconstant.RelayModeMidjourneyShorten:
		input.OutputModalities = channelrouting.RequestModalityText
		input.RequiredCapabilities = 0
	case relayconstant.RelayModeMidjourneyBlend,
		relayconstant.RelayModeMidjourneyEdits,
		relayconstant.RelayModeSwapFace,
		relayconstant.RelayModeMidjourneyUpload:
		input.InputModalities |= channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
	case relayconstant.RelayModeMidjourneyVideo:
		input.OutputModalities = channelrouting.RequestModalityVideo
		input.RequiredCapabilities = channelrouting.RequestCapabilityVideoOutput | channelrouting.RequestCapabilityStateful
		input.VideoMillis = channelrouting.UnknownRequestQuantity()
	case relayconstant.RelayModeMidjourneyAction,
		relayconstant.RelayModeMidjourneyChange,
		relayconstant.RelayModeMidjourneySimpleChange,
		relayconstant.RelayModeMidjourneyModal,
		relayconstant.RelayModeMidjourneyTaskImageSeed:
		input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
	}
	return input, nil
}

func applyOpenAIMediaProfile(input *channelrouting.RequestProfileV2Input, mediaType string) {
	if input == nil {
		return
	}
	switch mediaType {
	case dto.ContentTypeImageURL:
		input.InputModalities |= channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
	case dto.ContentTypeInputAudio:
		input.InputModalities |= channelrouting.RequestModalityAudio
		input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioInput
	case dto.ContentTypeFile:
		input.InputModalities |= channelrouting.RequestModalityFile
		input.RequiredCapabilities |= channelrouting.RequestCapabilityFileInput
	case dto.ContentTypeVideoUrl:
		input.InputModalities |= channelrouting.RequestModalityVideo
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVideoInput
	}
}

func applyGeminiRoutingProfile(input *channelrouting.RequestProfileV2Input, request *dto.GeminiChatRequest) {
	if input == nil || request == nil {
		return
	}
	if routingJSONPresent(request.Tools) {
		input.RequiredCapabilities |= channelrouting.RequestCapabilityTools
	}
	if strings.TrimSpace(request.CachedContent) != "" {
		input.RequiredCapabilities |= channelrouting.RequestCapabilityStateful
	}
	if request.GenerationConfig.ResponseSchema != nil || routingJSONPresent(request.GenerationConfig.ResponseJsonSchema) ||
		strings.EqualFold(request.GenerationConfig.ResponseMimeType, "application/json") {
		input.RequiredCapabilities |= channelrouting.RequestCapabilityJSONSchema
	}
	contents := append([]dto.GeminiChatContent(nil), request.Contents...)
	for requestIndex := range request.Requests {
		contents = append(contents, request.Requests[requestIndex].Contents...)
	}
	for _, content := range contents {
		for _, part := range content.Parts {
			mimeType := ""
			if part.InlineData != nil {
				mimeType = part.InlineData.MimeType
			} else if part.FileData != nil {
				mimeType = part.FileData.MimeType
			}
			switch {
			case strings.HasPrefix(mimeType, "image/"):
				input.InputModalities |= channelrouting.RequestModalityImage
				input.RequiredCapabilities |= channelrouting.RequestCapabilityVision
			case strings.HasPrefix(mimeType, "audio/"):
				input.InputModalities |= channelrouting.RequestModalityAudio
				input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioInput
			case strings.HasPrefix(mimeType, "video/"):
				input.InputModalities |= channelrouting.RequestModalityVideo
				input.RequiredCapabilities |= channelrouting.RequestCapabilityVideoInput
			case mimeType != "":
				input.InputModalities |= channelrouting.RequestModalityFile
				input.RequiredCapabilities |= channelrouting.RequestCapabilityFileInput
			}
		}
	}
	for _, modality := range request.GenerationConfig.ResponseModalities {
		applyRoutingOutputModality(input, modality)
	}
	if routingJSONPresent(request.GenerationConfig.SpeechConfig) {
		input.OutputModalities |= channelrouting.RequestModalityAudio
		input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioOutput
	}
	if routingJSONPresent(request.GenerationConfig.ImageConfig) {
		input.OutputModalities |= channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityImageOutput
	}
}

func applyRoutingOutputModality(input *channelrouting.RequestProfileV2Input, modality string) {
	if input == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(modality)) {
	case "text":
		input.OutputModalities |= channelrouting.RequestModalityText
	case "audio":
		input.OutputModalities |= channelrouting.RequestModalityAudio
		input.RequiredCapabilities |= channelrouting.RequestCapabilityAudioOutput
	case "image":
		input.OutputModalities |= channelrouting.RequestModalityImage
		input.RequiredCapabilities |= channelrouting.RequestCapabilityImageOutput
	case "video":
		input.OutputModalities |= channelrouting.RequestModalityVideo
		input.RequiredCapabilities |= channelrouting.RequestCapabilityVideoOutput
	}
}

func routingStringList(raw []byte) []string {
	if !routingJSONPresent(raw) {
		return nil
	}
	var values []string
	if err := common.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func routingJSONPathEquals(raw []byte, path string, expected string) bool {
	if !routingJSONPresent(raw) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(gjson.GetBytes(raw, path).String()), expected)
}

func routingJSONPresent(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) ||
		bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte(`""`)) {
		return false
	}
	return true
}
