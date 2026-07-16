package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatedRoutingModelNameKeepsCompactionSuffix(t *testing.T) {
	tests := []struct {
		name        string
		relayFormat types.RelayFormat
		request     dto.Request
		expected    string
	}{
		{
			name:        "compact adds suffix",
			relayFormat: types.RelayFormatOpenAIResponsesCompaction,
			request:     &dto.OpenAIResponsesCompactionRequest{Model: "gpt-test"},
			expected:    "gpt-test" + ratio_setting.CompactModelSuffix,
		},
		{
			name:        "compact suffix is idempotent",
			relayFormat: types.RelayFormatOpenAIResponsesCompaction,
			request: &dto.OpenAIResponsesCompactionRequest{
				Model: "gpt-test" + ratio_setting.CompactModelSuffix,
			},
			expected: "gpt-test" + ratio_setting.CompactModelSuffix,
		},
		{
			name:        "ordinary responses stays unchanged",
			relayFormat: types.RelayFormatOpenAIResponses,
			request:     &dto.OpenAIResponsesRequest{Model: "gpt-test"},
			expected:    "gpt-test",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := routingProfileTestContext(t, "/v1/responses", http.MethodPost)
			assert.Equal(t, test.expected, validatedRoutingModelName(ctx, test.relayFormat, test.request))
		})
	}
}

func TestCompactModelMappingSendsBaseModelUpstream(t *testing.T) {
	ctx := routingProfileTestContext(t, "/v1/responses/compact", http.MethodPost)
	ctx.Set("model_mapping", `{"gpt-test":"gpt-upstream"}`)
	request := &dto.OpenAIResponsesCompactionRequest{Model: "gpt-test"}
	modelName := validatedRoutingModelName(ctx, types.RelayFormatOpenAIResponsesCompaction, request)
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponsesCompact,
		OriginModelName: modelName,
	}

	require.NoError(t, helper.ModelMappedHelper(ctx, info, request))
	assert.Equal(t, "gpt-upstream", request.Model)
	assert.Equal(t, "gpt-test"+ratio_setting.CompactModelSuffix, info.OriginModelName)
	assert.Equal(t, "gpt-upstream", info.UpstreamModelName)
}

func TestCompactModelMappingKeepsClientModelStableAcrossRetries(t *testing.T) {
	clientModel := "gpt-client" + ratio_setting.CompactModelSuffix
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponsesCompact,
		OriginModelName: clientModel,
	}

	firstContext := routingProfileTestContext(t, "/v1/responses/compact", http.MethodPost)
	firstContext.Set("model_mapping", `{"gpt-client":"gpt-first"}`)
	firstRequest := &dto.OpenAIResponsesCompactionRequest{Model: "gpt-client"}
	require.NoError(t, helper.ModelMappedHelper(firstContext, info, firstRequest))
	assert.Equal(t, "gpt-first", firstRequest.Model)
	assert.Equal(t, clientModel, info.OriginModelName)

	secondContext := routingProfileTestContext(t, "/v1/responses/compact", http.MethodPost)
	secondContext.Set("model_mapping", `{"gpt-client":"gpt-second"}`)
	secondRequest := &dto.OpenAIResponsesCompactionRequest{Model: "gpt-client"}
	require.NoError(t, helper.ModelMappedHelper(secondContext, info, secondRequest))
	assert.Equal(t, "gpt-second", secondRequest.Model)
	assert.Equal(t, "gpt-second", info.UpstreamModelName)
	assert.Equal(t, clientModel, info.OriginModelName, "billing and logs must retain the client virtual model")
}

func TestBuildRoutingRequestProfileTemplateCapturesOpenAICapabilities(t *testing.T) {
	ctx := routingProfileTestContext(t, "/v1/chat/completions", http.MethodPost)
	request := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{{
			Role: "user",
			Content: []any{
				map[string]any{"type": dto.ContentTypeText, "text": "hello"},
				map[string]any{"type": dto.ContentTypeImageURL, "image_url": map[string]any{"url": "data:image/png;base64,AA"}},
				map[string]any{"type": dto.ContentTypeInputAudio, "input_audio": map[string]any{"data": "AA", "format": "wav"}},
				map[string]any{"type": dto.ContentTypeFile, "file": map[string]any{"file_id": "file-test"}},
				map[string]any{"type": dto.ContentTypeVideoUrl, "video_url": "https://example.test/video.mp4"},
			},
		}},
		Tools: []dto.ToolCallRequest{{Type: "function"}},
		ResponseFormat: &dto.ResponseFormat{
			Type:       "json_schema",
			JsonSchema: mustMarshalRoutingProfileJSON(t, dto.FormatJsonSchema{Name: "answer"}),
		},
		Modalities: mustMarshalRoutingProfileJSON(t, []string{"text", "audio"}),
	}

	input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAI, request, request.Model)
	require.NoError(t, err)
	assert.Equal(t, channelrouting.RequestKindChatCompletions, input.RequestKind)
	assert.Equal(t, channelrouting.RequestSourceFormatOpenAI, input.SourceFormat)
	assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage|
		channelrouting.RequestModalityAudio|channelrouting.RequestModalityFile|channelrouting.RequestModalityVideo,
		input.InputModalities)
	assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityAudio, input.OutputModalities)
	assert.Equal(t, channelrouting.RequestCapabilityTools|channelrouting.RequestCapabilityJSONSchema|
		channelrouting.RequestCapabilityVision|channelrouting.RequestCapabilityAudioInput|
		channelrouting.RequestCapabilityAudioOutput|channelrouting.RequestCapabilityFileInput|
		channelrouting.RequestCapabilityVideoInput, input.RequiredCapabilities)
	assert.True(t, input.RetryAllowed)
	assert.True(t, input.CrossChannelRetryAllowed)
	assert.True(t, input.HedgeAllowed)
	assert.Equal(t, channelrouting.RequestRetrySafetySafe, input.RetrySafety)
}

func TestBuildRoutingRequestProfileTemplateCapturesResponsesState(t *testing.T) {
	ctx := routingProfileTestContext(t, "/v1/responses", http.MethodPost)
	ctx.Request.Header.Set("Idempotency-Key", "request-key")
	request := &dto.OpenAIResponsesRequest{
		Model:              "gpt-test",
		Input:              mustMarshalRoutingProfileJSON(t, []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA"}, map[string]any{"type": "input_file", "file_url": "data:application/pdf;base64,AA"}}}}),
		Conversation:       mustMarshalRoutingProfileJSON(t, "conversation-test"),
		PreviousResponseID: "response-test",
		Text:               mustMarshalRoutingProfileJSON(t, map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer"}}),
		Tools:              mustMarshalRoutingProfileJSON(t, []any{map[string]any{"type": "function", "name": "lookup"}}),
	}

	input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAIResponses, request, request.Model)
	require.NoError(t, err)
	assert.Equal(t, channelrouting.RequestKindResponses, input.RequestKind)
	assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage|channelrouting.RequestModalityFile, input.InputModalities)
	assert.Equal(t, channelrouting.RequestCapabilityTools|channelrouting.RequestCapabilityJSONSchema|
		channelrouting.RequestCapabilityVision|channelrouting.RequestCapabilityFileInput|
		channelrouting.RequestCapabilityStateful, input.RequiredCapabilities)
	assert.Equal(t, channelrouting.RequestRetrySafetyConditional, input.RetrySafety)
	assert.True(t, input.IdempotencyKeyPresent)
	assert.True(t, input.RetryAllowed)
	assert.False(t, input.CrossChannelRetryAllowed)
	assert.False(t, input.HedgeAllowed)
}

func TestBuildRoutingRequestProfileTemplateCapturesClaudeAndGeminiMedia(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/v1/messages", http.MethodPost)
		request := &dto.ClaudeRequest{
			Model: "claude-test",
			Messages: []dto.ClaudeMessage{{
				Role: "user",
				Content: []any{map[string]any{
					"type":   "image",
					"source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AA"},
				}},
			}},
			Tools:        []any{map[string]any{"name": "lookup"}},
			OutputFormat: mustMarshalRoutingProfileJSON(t, map[string]any{"type": "json_schema"}),
			Container:    mustMarshalRoutingProfileJSON(t, "container-test"),
		}

		input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatClaude, request, request.Model)
		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestKindClaudeMessages, input.RequestKind)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage, input.InputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityTools|channelrouting.RequestCapabilityJSONSchema|
			channelrouting.RequestCapabilityVision|channelrouting.RequestCapabilityStateful, input.RequiredCapabilities)
		assert.False(t, input.CrossChannelRetryAllowed)
	})

	t.Run("gemini", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/v1beta/models/gemini-test:generateContent", http.MethodPost)
		request := &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{{Parts: []dto.GeminiPart{
				{Text: "hello"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "AA"}},
				{InlineData: &dto.GeminiInlineData{MimeType: "audio/wav", Data: "AA"}},
				{InlineData: &dto.GeminiInlineData{MimeType: "video/mp4", Data: "AA"}},
				{FileData: &dto.GeminiFileData{MimeType: "application/pdf", FileUri: "gs://bucket/file"}},
			}}},
			Tools: mustMarshalRoutingProfileJSON(t, []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "lookup"}}}}),
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ResponseJsonSchema: mustMarshalRoutingProfileJSON(t, map[string]any{"type": "object"}),
				ResponseModalities: []string{"TEXT", "AUDIO", "IMAGE"},
			},
		}

		input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatGemini, request, "gemini-test")
		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestKindGeminiGenerate, input.RequestKind)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage|
			channelrouting.RequestModalityAudio|channelrouting.RequestModalityFile|channelrouting.RequestModalityVideo,
			input.InputModalities)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityAudio|channelrouting.RequestModalityImage, input.OutputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityTools|channelrouting.RequestCapabilityJSONSchema|
			channelrouting.RequestCapabilityVision|channelrouting.RequestCapabilityAudioInput|
			channelrouting.RequestCapabilityAudioOutput|channelrouting.RequestCapabilityImageOutput|
			channelrouting.RequestCapabilityFileInput|channelrouting.RequestCapabilityVideoInput,
			input.RequiredCapabilities)
	})
}

func TestBuildRoutingRequestProfileTemplateUsesConservativeRealtimeProfileWithoutSessionUpdate(t *testing.T) {
	ctx := routingProfileTestContext(t, "/v1/realtime", http.MethodGet)
	input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAIRealtime, &dto.BaseRequest{}, "gpt-realtime")
	require.NoError(t, err)
	assert.Equal(t, channelrouting.RequestKindRealtime, input.RequestKind)
	assert.Equal(t, channelrouting.RequestCapabilityRealtime|channelrouting.RequestCapabilityStateful, input.RequiredCapabilities)
	assert.Equal(t, channelrouting.RequestRetrySafetyUnsafe, input.RetrySafety)
	assert.False(t, input.RetryAllowed)
	assert.False(t, input.CrossChannelRetryAllowed)
	assert.False(t, input.HedgeAllowed)
}

func TestBuildRoutingRequestProfileTemplateCarriesDeadlineAndIdempotency(t *testing.T) {
	ctx := routingProfileTestContext(t, "/v1/images/generations", http.MethodPost)
	deadline := time.Now().Add(5 * time.Second).Round(time.Millisecond)
	requestContext, cancel := context.WithDeadline(ctx.Request.Context(), deadline)
	t.Cleanup(cancel)
	ctx.Request = ctx.Request.WithContext(requestContext)
	ctx.Request.Header.Set("X-Idempotency-Key", "image-key")
	request := &dto.ImageRequest{Model: "image-test", N: common.GetPointer(uint(2))}

	input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAIImage, request, request.Model)
	require.NoError(t, err)
	assert.Equal(t, deadline.UnixMilli(), input.DeadlineUnixMs)
	assert.True(t, input.IdempotencyKeyPresent)
	assert.Equal(t, channelrouting.RequestCapabilityImageOutput, input.RequiredCapabilities)
	assert.Equal(t, channelrouting.KnownRequestQuantity(2), input.ImageUnits)
	assert.Equal(t, channelrouting.RequestModalityImage, input.OutputModalities)
}

func TestBuildRoutingRequestProfileTemplateMarksImageEditsAsVision(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/v1/images/edits", http.MethodPost)
		request := &dto.ImageRequest{Model: "image-test"}

		input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAIImage, request, request.Model)
		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage, input.InputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityImageOutput|channelrouting.RequestCapabilityVision, input.RequiredCapabilities)
	})

	t.Run("multipart", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "image-test"))
		require.NoError(t, writer.WriteField("prompt", "edit this"))
		part, err := writer.CreateFormFile("image", "input.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		ctx := routingProfileTestContext(t, "/v1/images/edits", http.MethodPost)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
		request, err := helper.GetAndValidateRequest(ctx, types.RelayFormatOpenAIImage)
		require.NoError(t, err)

		input, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatOpenAIImage, request, "image-test")
		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage, input.InputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityImageOutput|channelrouting.RequestCapabilityVision, input.RequiredCapabilities)
	})
}

func TestBuildTaskRoutingRequestProfileCapturesMediaStateAndRetrySafety(t *testing.T) {
	t.Run("image to video", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/v1/videos", http.MethodPost)
		ctx.Set("task_request", relaycommon.TaskSubmitReq{
			Model: "video-test", Prompt: "animate", Images: []string{"https://example.test/input.png"}, Duration: 8,
		})
		info := &relaycommon.RelayInfo{
			OriginModelName: "video-test",
			TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: constant.TaskActionGenerate},
		}

		input, err := buildTaskRoutingRequestProfileTemplate(ctx, info)

		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestKindTask, input.RequestKind)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityImage, input.InputModalities)
		assert.Equal(t, channelrouting.RequestModalityVideo, input.OutputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityVision|channelrouting.RequestCapabilityVideoOutput, input.RequiredCapabilities)
		assert.Equal(t, channelrouting.KnownRequestQuantity(8_000), input.VideoMillis)
		assert.Equal(t, channelrouting.RequestRetrySafetyUnsafe, input.RetrySafety)
		assert.False(t, input.RetryAllowed)
		assert.False(t, input.CrossChannelRetryAllowed)
		assert.False(t, input.HedgeAllowed)
	})

	t.Run("video remix", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/v1/videos/task-1/remix", http.MethodPost)
		ctx.Set("task_request", relaycommon.TaskSubmitReq{Model: "video-test", Prompt: "remix"})
		info := &relaycommon.RelayInfo{
			OriginModelName: "video-test",
			TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: constant.TaskActionRemix},
		}

		input, err := buildTaskRoutingRequestProfileTemplate(ctx, info)

		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestModalityText|channelrouting.RequestModalityVideo, input.InputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityVideoOutput|channelrouting.RequestCapabilityVideoInput|
			channelrouting.RequestCapabilityStateful, input.RequiredCapabilities)
	})

	t.Run("Suno continuation", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/suno/submit/MUSIC", http.MethodPost)
		ctx.Set("platform", string(constant.TaskPlatformSuno))
		ctx.Set("task_request", &dto.SunoSubmitReq{TaskID: "task-1", ContinueClipId: "clip-1"})
		info := &relaycommon.RelayInfo{
			OriginModelName: "suno_music",
			TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: constant.SunoActionMusic},
		}

		input, err := buildTaskRoutingRequestProfileTemplate(ctx, info)

		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestKindSuno, input.RequestKind)
		assert.Equal(t, channelrouting.RequestModalityAudio, input.OutputModalities)
		assert.Equal(t, channelrouting.RequestCapabilityAudioOutput|channelrouting.RequestCapabilityStateful, input.RequiredCapabilities)
		assert.Equal(t, channelrouting.RequestRetrySafetyUnsafe, input.RetrySafety)
		assert.False(t, input.RetryAllowed)
		assert.False(t, input.CrossChannelRetryAllowed)
	})

	t.Run("Suno lyrics", func(t *testing.T) {
		ctx := routingProfileTestContext(t, "/suno/submit/LYRICS", http.MethodPost)
		ctx.Set("platform", string(constant.TaskPlatformSuno))
		ctx.Set("task_request", &dto.SunoSubmitReq{Prompt: "lyrics"})
		info := &relaycommon.RelayInfo{
			OriginModelName: "suno_lyrics",
			TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: constant.SunoActionLyrics},
		}

		input, err := buildTaskRoutingRequestProfileTemplate(ctx, info)

		require.NoError(t, err)
		assert.Equal(t, channelrouting.RequestModalityText, input.OutputModalities)
		assert.Zero(t, input.RequiredCapabilities)
	})
}

func TestBuildMidjourneyRoutingRequestProfileUsesOperationSemantics(t *testing.T) {
	tests := []struct {
		name         string
		mode         int
		input        channelrouting.RequestModalityMask
		output       channelrouting.RequestModalityMask
		capabilities channelrouting.RequestCapabilityMask
	}{
		{
			name: "describe", mode: relayconstant.RelayModeMidjourneyDescribe,
			input:  channelrouting.RequestModalityText | channelrouting.RequestModalityImage,
			output: channelrouting.RequestModalityText, capabilities: channelrouting.RequestCapabilityVision,
		},
		{
			name: "shorten", mode: relayconstant.RelayModeMidjourneyShorten,
			input: channelrouting.RequestModalityText, output: channelrouting.RequestModalityText,
		},
		{
			name: "swap face", mode: relayconstant.RelayModeSwapFace,
			input:        channelrouting.RequestModalityText | channelrouting.RequestModalityImage,
			output:       channelrouting.RequestModalityImage,
			capabilities: channelrouting.RequestCapabilityImageOutput | channelrouting.RequestCapabilityVision,
		},
		{
			name: "video", mode: relayconstant.RelayModeMidjourneyVideo,
			input: channelrouting.RequestModalityText, output: channelrouting.RequestModalityVideo,
			capabilities: channelrouting.RequestCapabilityVideoOutput | channelrouting.RequestCapabilityStateful,
		},
		{
			name: "simple change", mode: relayconstant.RelayModeMidjourneySimpleChange,
			input: channelrouting.RequestModalityText, output: channelrouting.RequestModalityImage,
			capabilities: channelrouting.RequestCapabilityImageOutput | channelrouting.RequestCapabilityStateful,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := routingProfileTestContext(t, "/mj/submit/"+strings.ReplaceAll(test.name, " ", "-"), http.MethodPost)

			input, err := buildMidjourneyRoutingRequestProfileTemplate(ctx, "mj-test", test.mode)

			require.NoError(t, err)
			assert.Equal(t, test.input, input.InputModalities)
			assert.Equal(t, test.output, input.OutputModalities)
			assert.Equal(t, test.capabilities, input.RequiredCapabilities)
			assert.Equal(t, channelrouting.RequestRetrySafetyUnsafe, input.RetrySafety)
			assert.False(t, input.RetryAllowed)
			assert.False(t, input.CrossChannelRetryAllowed)
			assert.False(t, input.HedgeAllowed)
		})
	}
}

func TestApplyValidatedRoutingTokenEstimatePreservesUnknownSemantics(t *testing.T) {
	previousCountToken := constant.CountToken
	t.Cleanup(func() { constant.CountToken = previousCountToken })
	profile := channelrouting.RequestProfileInput{
		InputTokens:  channelrouting.UnknownRequestQuantity(),
		OutputTokens: channelrouting.UnknownRequestQuantity(),
	}

	t.Run("disabled token counting keeps conservative bound", func(t *testing.T) {
		constant.CountToken = false
		ctx := routingProfileTestContext(t, "/v1/chat/completions", http.MethodPost)
		common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 123)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInput, 456)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputKnown, true)
		common.SetContextKey(ctx, constant.ContextKeyRoutingCapacityInputState, channelrouting.CapacityDimensionBoundedKnown)

		applyValidatedRoutingTokenEstimate(ctx, types.RelayFormatOpenAI, 0, 0, false, profile)

		assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingPromptKnown))
		assert.Equal(t, 123, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPromptProxy))
		assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityInputKnown))
		assert.Equal(t, 456, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCapacityInput))
	})

	t.Run("realtime zero remains unknown", func(t *testing.T) {
		constant.CountToken = true
		ctx := routingProfileTestContext(t, "/v1/realtime", http.MethodGet)

		applyValidatedRoutingTokenEstimate(ctx, types.RelayFormatOpenAIRealtime, 0, 0, false, profile)

		assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingPromptKnown))
		assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityInputKnown))
		state, exists := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityInputState)
		require.True(t, exists)
		assert.Equal(t, channelrouting.CapacityDimensionApplicableUnknown, state)
	})
}

func TestRelayValidationRunsBeforeDeferredChannelSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.DistributeDeferred(), func(c *gin.Context) {
		Relay(c, types.RelayFormatOpenAI)
		_, selected := common.GetContextKey(c, constant.ContextKeyChannelId)
		assert.False(t, selected)
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":2147483648}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "max_tokens")
}

func TestTaskValidationRunsBeforeDeferredCredentialSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/videos", middleware.DistributeDeferred(), func(c *gin.Context) {
		RelayTask(c)
		_, selected := common.GetContextKey(c, constant.ContextKeyChannelId)
		assert.False(t, selected)
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/videos",
		strings.NewReader(`{"model":"sora-test","prompt":"hello","duration":3601}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "invalid_seconds")
}

func routingProfileTestContext(t *testing.T, path string, method string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(method, path, nil)
	return ctx
}

func mustMarshalRoutingProfileJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}
