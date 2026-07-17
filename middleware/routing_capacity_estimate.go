package middleware

import (
	"math"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/tidwall/gjson"
)

type routingCapacityTokenEstimate struct {
	Input                         channelrouting.CapacityDimensionEstimate
	Output                        channelrouting.CapacityDimensionEstimate
	Stream                        bool
	StreamKnown                   bool
	RemoteState                   bool
	HasMedia                      bool
	CacheWriteTokensKnown         bool
	RequestPricingFeaturesKnown   bool
	UncataloguedSurchargePossible bool
}

type routingRequestPricingFeatureState struct {
	Known                         bool
	HasCacheControl               bool
	UncataloguedSurchargePossible bool
}

func estimateRoutingCapacityTokens(path string, body []byte) routingCapacityTokenEstimate {
	pricingFeatures := inspectRoutingRequestPricingFeatures(path, body)
	estimate := routingCapacityTokenEstimate{
		Input:                         channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
		Output:                        channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
		RequestPricingFeaturesKnown:   pricingFeatures.Known,
		UncataloguedSurchargePossible: pricingFeatures.UncataloguedSurchargePossible,
	}
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(path, "/v1/realtime") {
		estimate.Stream = true
		estimate.StreamKnown = true
		return estimate
	}
	for _, marker := range []string{
		"/v1/images/", "/v1/edits", "/v1/audio/", "/v1/video/", "/v1/videos",
		"/kling/v1/videos/", "/mj/", "/suno/", "/jimeng",
	} {
		if strings.Contains(lowerPath, marker) {
			estimate.Input.State = channelrouting.CapacityDimensionNotApplicable
			estimate.Output.State = channelrouting.CapacityDimensionNotApplicable
			return estimate
		}
	}
	if strings.HasPrefix(lowerPath, "/mj") || strings.HasSuffix(lowerPath, "/embeddings") ||
		strings.Contains(lowerPath, ":embedcontent") || strings.Contains(lowerPath, ":batchembedcontents") ||
		strings.HasPrefix(lowerPath, "/v1/rerank") || strings.HasPrefix(lowerPath, "/v1/moderations") {
		estimate.Output.State = channelrouting.CapacityDimensionNotApplicable
	}
	if len(body) == 0 {
		return estimate
	}

	var request dto.Request
	inputDependsOnRemoteState := false
	cacheEnvelopeRecognized := false
	switch {
	case strings.Contains(path, ":batchEmbedContents"):
		parsed := &dto.GeminiBatchEmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
		cacheEnvelopeRecognized = true
	case strings.Contains(path, ":embedContent"):
		parsed := &dto.GeminiEmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
		cacheEnvelopeRecognized = true
	case strings.HasPrefix(path, "/v1/embeddings") || strings.HasSuffix(path, "embeddings"):
		parsed := &dto.EmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
		cacheEnvelopeRecognized = true
	case strings.HasPrefix(path, "/v1/responses/compact"):
		parsed := &dto.OpenAIResponsesCompactionRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = parsed.PreviousResponseID != ""
		cacheEnvelopeRecognized = true
	case strings.HasPrefix(path, "/v1/responses"):
		parsed := &dto.OpenAIResponsesRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = parsed.PreviousResponseID != "" || len(parsed.Conversation) > 0
		cacheEnvelopeRecognized = true
		if parsed.Stream != nil {
			estimate.Stream = *parsed.Stream
			estimate.StreamKnown = true
		}
	case strings.HasPrefix(path, "/v1/messages"):
		parsed := &dto.ClaudeRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = len(parsed.Container) > 0
		cacheEnvelopeRecognized = true
		if parsed.Stream != nil {
			estimate.Stream = *parsed.Stream
			estimate.StreamKnown = true
		}
	case strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/"):
		parsed := &dto.GeminiChatRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = parsed.CachedContent != "" || len(parsed.Requests) > 0
		cacheEnvelopeRecognized = true
		estimate.Stream = strings.Contains(path, ":streamGenerateContent")
		estimate.StreamKnown = true
	default:
		parsed := &dto.GeneralOpenAIRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		cacheEnvelopeRecognized = strings.HasPrefix(lowerPath, "/v1/chat/completions") ||
			strings.HasPrefix(lowerPath, "/v1/completions")
		if parsed.Stream != nil {
			estimate.Stream = *parsed.Stream
			estimate.StreamKnown = true
		}
	}
	if cacheEnvelopeRecognized && !inputDependsOnRemoteState && pricingFeatures.Known && !pricingFeatures.HasCacheControl {
		estimate.CacheWriteTokensKnown = true
	}

	meta := request.GetTokenCountMeta()
	if meta == nil {
		return estimate
	}
	estimate.RemoteState = inputDependsOnRemoteState
	estimate.HasMedia = len(meta.Files) > 0
	if !inputDependsOnRemoteState && len(meta.Files) == 0 {
		estimate.Input = channelrouting.CapacityDimensionEstimate{
			State: channelrouting.CapacityDimensionBoundedKnown, Tokens: max(len(body), 1),
		}
	}
	if estimate.Output.State != channelrouting.CapacityDimensionNotApplicable && meta.MaxTokens > 0 {
		estimate.Output = channelrouting.CapacityDimensionEstimate{
			State: channelrouting.CapacityDimensionBoundedKnown, Tokens: meta.MaxTokens,
		}
	}

	switch parsed := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if parsed.N != nil {
			if *parsed.N <= 0 {
				estimate.Output.Tokens = 0
			} else if estimate.Output.Tokens > 0 {
				estimate.Output.Tokens = saturatingRoutingTokenProduct(estimate.Output.Tokens, *parsed.N)
			}
		}
	case *dto.ClaudeRequest:
		if parsed.MaxTokensToSample != nil && int(*parsed.MaxTokensToSample) > estimate.Output.Tokens {
			estimate.Output = channelrouting.CapacityDimensionEstimate{
				State: channelrouting.CapacityDimensionBoundedKnown, Tokens: int(*parsed.MaxTokensToSample),
			}
		}
	}
	return estimate
}

func inspectRoutingRequestPricingFeatures(path string, body []byte) routingRequestPricingFeatureState {
	state := routingRequestPricingFeatureState{}
	normalizedPath := strings.ToLower(path)
	if separator := strings.IndexAny(normalizedPath, "?#"); separator >= 0 {
		normalizedPath = normalizedPath[:separator]
	}
	normalizedPath = strings.TrimRight(normalizedPath, "/")
	recognizedProtocolEnvelope := normalizedPath == "/v1/chat/completions" ||
		normalizedPath == "/pg/chat/completions" ||
		normalizedPath == "/v1/completions" ||
		normalizedPath == "/v1/embeddings" ||
		normalizedPath == "/v1/messages" ||
		normalizedPath == "/v1/responses" ||
		normalizedPath == "/v1/responses/compact" ||
		normalizedPath == "/v1/moderations" ||
		normalizedPath == "/v1/rerank" ||
		normalizedPath == "/v1/audio/speech" ||
		(strings.HasPrefix(normalizedPath, "/v1/engines/") && strings.HasSuffix(normalizedPath, "/embeddings")) ||
		((strings.HasPrefix(normalizedPath, "/v1beta/models/") ||
			strings.HasPrefix(normalizedPath, "/v1/models/")) &&
			(strings.HasSuffix(normalizedPath, ":generatecontent") ||
				strings.HasSuffix(normalizedPath, ":streamgeneratecontent") ||
				strings.HasSuffix(normalizedPath, ":embedcontent") ||
				strings.HasSuffix(normalizedPath, ":batchembedcontents")))
	if strings.HasSuffix(normalizedPath, "/alpha/search") {
		state.Known = true
		state.UncataloguedSurchargePossible = true
	}
	if normalizedPath == "/v1/edits" || normalizedPath == "/v1/images/generations" ||
		normalizedPath == "/v1/images/edits" {
		state.Known = true
		state.UncataloguedSurchargePossible = true
	}
	if !state.Known && !recognizedProtocolEnvelope {
		return state
	}
	if len(body) == 0 {
		return state
	}
	var payload map[string]any
	if common.Unmarshal(body, &payload) != nil || payload == nil {
		return state
	}
	if recognizedProtocolEnvelope {
		state.Known = true
	}
	for _, key := range []string{"web_search_options", "web_search"} {
		if value, exists := payload[key]; exists && value != nil {
			state.UncataloguedSurchargePossible = true
		}
	}
	if tools, ok := payload["tools"].([]any); ok {
		for _, value := range tools {
			tool, objectOK := value.(map[string]any)
			if !objectOK {
				continue
			}
			toolType, _ := tool["type"].(string)
			toolType = strings.ToLower(strings.TrimSpace(toolType))
			if strings.Contains(toolType, "web_search") || toolType == "file_search" ||
				toolType == "image_generation" {
				state.UncataloguedSurchargePossible = true
			}
		}
	}
	stack := []any{payload}
	const maxJSONNodes = 16_384
	visited := 0
	for len(stack) > 0 {
		visited++
		if visited > maxJSONNodes {
			return routingRequestPricingFeatureState{}
		}
		last := len(stack) - 1
		value := stack[last]
		stack = stack[:last]
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalizedKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				if normalizedKey == "cachecontrol" ||
					normalizedKey == "promptcachekey" ||
					normalizedKey == "promptcacheoptions" ||
					normalizedKey == "promptcacheretention" {
					state.HasCacheControl = true
				}
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, typed...)
		}
	}
	return state
}

func saturatingRoutingTokenProduct(left int, right int) int {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left > math.MaxInt/right {
		return math.MaxInt
	}
	return left * right
}

func buildRoutingCostRequestProfile(
	path string,
	body []byte,
	headers http.Header,
	estimate routingCapacityTokenEstimate,
	promptTokens int,
	expectedCompletionTokens int,
) *model.RoutingCostRequestProfile {
	profilePromptTokens := promptTokens
	if estimate.Input.State == channelrouting.CapacityDimensionNotApplicable {
		profilePromptTokens = 0
	}
	profileCompletionTokens := expectedCompletionTokens
	if estimate.Output.State == channelrouting.CapacityDimensionNotApplicable {
		profileCompletionTokens = 0
	}
	maximumCompletionTokens := expectedCompletionTokens
	if estimate.Output.Known() {
		maximumCompletionTokens = estimate.Output.Tokens
	}
	maximumPromptTokens := promptTokens
	if estimate.Input.Known() {
		maximumPromptTokens = estimate.Input.Tokens
	}
	mediaRequest := estimate.HasMedia
	lowerPath := strings.ToLower(path)
	for _, marker := range []string{"/images", "/audio", "/realtime", "/videos"} {
		if strings.Contains(lowerPath, marker) {
			mediaRequest = true
			break
		}
	}
	imageUnits := 0.0
	imageUnitsKnown := !mediaRequest
	if value := gjson.GetBytes(body, "n"); value.Exists() && value.Type == gjson.Number && value.Num >= 0 {
		imageUnits = value.Num
		imageUnitsKnown = true
	}
	requestHeaders := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 || strings.TrimSpace(key) == "" {
			continue
		}
		requestHeaders[key] = values[0]
	}
	return &model.RoutingCostRequestProfile{
		PromptTokens:                  int64(max(profilePromptTokens, 0)),
		MaximumPromptTokens:           int64(max(maximumPromptTokens, 0)),
		ExpectedCompletionTokens:      int64(max(profileCompletionTokens, 0)),
		MaximumCompletionTokens:       int64(max(maximumCompletionTokens, 0)),
		ImageUnits:                    imageUnits,
		MaxAttempts:                   1,
		KnowledgeSpecified:            true,
		InputTokensKnown:              estimate.Input.Known() && !estimate.RemoteState,
		MaximumCompletionKnown:        estimate.Output.Known(),
		CacheTokensKnown:              false,
		CacheReadTokensKnown:          false,
		CacheWriteTokensKnown:         estimate.CacheWriteTokensKnown,
		CacheWriteOneHourTokensKnown:  estimate.CacheWriteTokensKnown,
		ImageInputTokensKnown:         !mediaRequest,
		ImageOutputTokensKnown:        !mediaRequest,
		ImageUnitsKnown:               imageUnitsKnown,
		AudioInputTokensKnown:         !mediaRequest,
		AudioOutputTokensKnown:        !mediaRequest,
		AudioDurationKnown:            !mediaRequest,
		VideoDurationKnown:            !mediaRequest,
		RequestInputKnown:             len(body) > 0,
		RequestPricingFeaturesKnown:   estimate.RequestPricingFeaturesKnown,
		UncataloguedSurchargePossible: estimate.UncataloguedSurchargePossible,
		Request: billingexpr.RequestInput{
			Headers: requestHeaders,
			Body:    append([]byte(nil), body...),
		},
	}
}
