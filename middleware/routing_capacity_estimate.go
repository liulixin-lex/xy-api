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
	Input       channelrouting.CapacityDimensionEstimate
	Output      channelrouting.CapacityDimensionEstimate
	Stream      bool
	StreamKnown bool
	RemoteState bool
	HasMedia    bool
}

func estimateRoutingCapacityTokens(path string, body []byte) routingCapacityTokenEstimate {
	estimate := routingCapacityTokenEstimate{
		Input:  channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
		Output: channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
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
	switch {
	case strings.Contains(path, ":batchEmbedContents"):
		parsed := &dto.GeminiBatchEmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
	case strings.Contains(path, ":embedContent"):
		parsed := &dto.GeminiEmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
	case strings.HasPrefix(path, "/v1/embeddings") || strings.HasSuffix(path, "embeddings"):
		parsed := &dto.EmbeddingRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		estimate.StreamKnown = true
	case strings.HasPrefix(path, "/v1/responses/compact"):
		parsed := &dto.OpenAIResponsesCompactionRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = parsed.PreviousResponseID != ""
	case strings.HasPrefix(path, "/v1/responses"):
		parsed := &dto.OpenAIResponsesRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		inputDependsOnRemoteState = parsed.PreviousResponseID != "" || len(parsed.Conversation) > 0
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
		estimate.Stream = strings.Contains(path, ":streamGenerateContent")
		estimate.StreamKnown = true
	default:
		parsed := &dto.GeneralOpenAIRequest{}
		if common.Unmarshal(body, parsed) != nil {
			return estimate
		}
		request = parsed
		if parsed.Stream != nil {
			estimate.Stream = *parsed.Stream
			estimate.StreamKnown = true
		}
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
	if value := gjson.GetBytes(body, "n"); value.Exists() && value.Type == gjson.Number && value.Num > 0 {
		imageUnits = value.Num
	}
	requestHeaders := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 || strings.TrimSpace(key) == "" {
			continue
		}
		requestHeaders[key] = values[0]
	}
	return &model.RoutingCostRequestProfile{
		PromptTokens:             int64(max(profilePromptTokens, 0)),
		MaximumPromptTokens:      int64(max(maximumPromptTokens, 0)),
		ExpectedCompletionTokens: int64(max(profileCompletionTokens, 0)),
		MaximumCompletionTokens:  int64(max(maximumCompletionTokens, 0)),
		ImageUnits:               imageUnits,
		MaxAttempts:              1,
		KnowledgeSpecified:       true,
		InputTokensKnown:         estimate.Input.Known() && !estimate.RemoteState,
		MaximumCompletionKnown:   estimate.Output.Known(),
		CacheTokensKnown:         false,
		MediaDimensionsKnown:     !mediaRequest,
		RequestInputKnown:        len(body) > 0,
		Request: billingexpr.RequestInput{
			Headers: requestHeaders,
			Body:    append([]byte(nil), body...),
		},
	}
}
