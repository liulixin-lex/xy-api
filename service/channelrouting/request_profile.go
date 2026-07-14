package channelrouting

import (
	"math"
	"strings"
)

const (
	RequestProfileSchemaV1 = 1
	RequestProfileSchemaV2 = 2

	maxRequestProfileQuantity = int64(math.MaxInt32)
)

type RequestKind string

const (
	RequestKindChatCompletions  RequestKind = "chat_completions"
	RequestKindResponses        RequestKind = "responses"
	RequestKindResponsesCompact RequestKind = "responses_compaction"
	RequestKindClaudeMessages   RequestKind = "claude_messages"
	RequestKindGeminiGenerate   RequestKind = "gemini_generate"
	RequestKindImage            RequestKind = "image"
	RequestKindAudio            RequestKind = "audio"
	RequestKindEmbedding        RequestKind = "embedding"
	RequestKindRerank           RequestKind = "rerank"
	RequestKindRealtime         RequestKind = "realtime"
	RequestKindTask             RequestKind = "task"
	RequestKindMidjourney       RequestKind = "midjourney"
	RequestKindSuno             RequestKind = "suno"
	RequestKindGeminiEmbedding  RequestKind = "gemini_embedding"
	RequestKindGeminiBatchEmbed RequestKind = "gemini_batch_embedding"
)

type RequestKindMask uint64

const (
	RequestKindMaskChatCompletions RequestKindMask = 1 << iota
	RequestKindMaskResponses
	RequestKindMaskResponsesCompact
	RequestKindMaskClaudeMessages
	RequestKindMaskGeminiGenerate
	RequestKindMaskImage
	RequestKindMaskAudio
	RequestKindMaskEmbedding
	RequestKindMaskRerank
	RequestKindMaskRealtime
	RequestKindMaskTask
	RequestKindMaskMidjourney
	RequestKindMaskSuno
	RequestKindMaskGeminiEmbedding
	RequestKindMaskGeminiBatchEmbed

	requestKindMaskAll = RequestKindMaskChatCompletions |
		RequestKindMaskResponses |
		RequestKindMaskResponsesCompact |
		RequestKindMaskClaudeMessages |
		RequestKindMaskGeminiGenerate |
		RequestKindMaskImage |
		RequestKindMaskAudio |
		RequestKindMaskEmbedding |
		RequestKindMaskRerank |
		RequestKindMaskRealtime |
		RequestKindMaskTask |
		RequestKindMaskMidjourney |
		RequestKindMaskSuno |
		RequestKindMaskGeminiEmbedding |
		RequestKindMaskGeminiBatchEmbed
)

func (kind RequestKind) Mask() (RequestKindMask, bool) {
	switch kind {
	case RequestKindChatCompletions:
		return RequestKindMaskChatCompletions, true
	case RequestKindResponses:
		return RequestKindMaskResponses, true
	case RequestKindResponsesCompact:
		return RequestKindMaskResponsesCompact, true
	case RequestKindClaudeMessages:
		return RequestKindMaskClaudeMessages, true
	case RequestKindGeminiGenerate:
		return RequestKindMaskGeminiGenerate, true
	case RequestKindImage:
		return RequestKindMaskImage, true
	case RequestKindAudio:
		return RequestKindMaskAudio, true
	case RequestKindEmbedding:
		return RequestKindMaskEmbedding, true
	case RequestKindRerank:
		return RequestKindMaskRerank, true
	case RequestKindRealtime:
		return RequestKindMaskRealtime, true
	case RequestKindTask:
		return RequestKindMaskTask, true
	case RequestKindMidjourney:
		return RequestKindMaskMidjourney, true
	case RequestKindSuno:
		return RequestKindMaskSuno, true
	case RequestKindGeminiEmbedding:
		return RequestKindMaskGeminiEmbedding, true
	case RequestKindGeminiBatchEmbed:
		return RequestKindMaskGeminiBatchEmbed, true
	default:
		return 0, false
	}
}

type RequestSourceFormat string

const (
	RequestSourceFormatOpenAI                    RequestSourceFormat = "openai"
	RequestSourceFormatOpenAIResponses           RequestSourceFormat = "openai_responses"
	RequestSourceFormatOpenAIResponsesCompaction RequestSourceFormat = "openai_responses_compaction"
	RequestSourceFormatClaude                    RequestSourceFormat = "claude"
	RequestSourceFormatGemini                    RequestSourceFormat = "gemini"
	RequestSourceFormatOpenAIImage               RequestSourceFormat = "openai_image"
	RequestSourceFormatOpenAIAudio               RequestSourceFormat = "openai_audio"
	RequestSourceFormatEmbedding                 RequestSourceFormat = "embedding"
	RequestSourceFormatRerank                    RequestSourceFormat = "rerank"
	RequestSourceFormatOpenAIRealtime            RequestSourceFormat = "openai_realtime"
	RequestSourceFormatTask                      RequestSourceFormat = "task"
	RequestSourceFormatMidjourney                RequestSourceFormat = "midjourney"
	RequestSourceFormatSuno                      RequestSourceFormat = "suno"
	RequestSourceFormatGeminiEmbedding           RequestSourceFormat = "gemini_embedding"
	RequestSourceFormatGeminiBatchEmbedding      RequestSourceFormat = "gemini_batch_embedding"
)

func (format RequestSourceFormat) valid() bool {
	switch format {
	case RequestSourceFormatOpenAI,
		RequestSourceFormatOpenAIResponses,
		RequestSourceFormatOpenAIResponsesCompaction,
		RequestSourceFormatClaude,
		RequestSourceFormatGemini,
		RequestSourceFormatOpenAIImage,
		RequestSourceFormatOpenAIAudio,
		RequestSourceFormatEmbedding,
		RequestSourceFormatRerank,
		RequestSourceFormatOpenAIRealtime,
		RequestSourceFormatTask,
		RequestSourceFormatMidjourney,
		RequestSourceFormatSuno,
		RequestSourceFormatGeminiEmbedding,
		RequestSourceFormatGeminiBatchEmbedding:
		return true
	default:
		return false
	}
}

func requestSourceFormatSupportsKind(format RequestSourceFormat, kind RequestKind) bool {
	switch format {
	case RequestSourceFormatOpenAI:
		return kind == RequestKindChatCompletions
	case RequestSourceFormatOpenAIResponses:
		return kind == RequestKindResponses
	case RequestSourceFormatOpenAIResponsesCompaction:
		return kind == RequestKindResponsesCompact
	case RequestSourceFormatClaude:
		return kind == RequestKindClaudeMessages
	case RequestSourceFormatGemini:
		return kind == RequestKindGeminiGenerate
	case RequestSourceFormatOpenAIImage:
		return kind == RequestKindImage
	case RequestSourceFormatOpenAIAudio:
		return kind == RequestKindAudio
	case RequestSourceFormatEmbedding:
		return kind == RequestKindEmbedding
	case RequestSourceFormatRerank:
		return kind == RequestKindRerank
	case RequestSourceFormatOpenAIRealtime:
		return kind == RequestKindRealtime
	case RequestSourceFormatTask:
		return kind == RequestKindTask
	case RequestSourceFormatMidjourney:
		return kind == RequestKindMidjourney
	case RequestSourceFormatSuno:
		return kind == RequestKindSuno
	case RequestSourceFormatGeminiEmbedding:
		return kind == RequestKindGeminiEmbedding
	case RequestSourceFormatGeminiBatchEmbedding:
		return kind == RequestKindGeminiBatchEmbed
	default:
		return false
	}
}

type RequestModalityMask uint16

const (
	RequestModalityText RequestModalityMask = 1 << iota
	RequestModalityImage
	RequestModalityAudio
	RequestModalityFile
	RequestModalityVideo

	requestModalityMaskAll = RequestModalityText |
		RequestModalityImage |
		RequestModalityAudio |
		RequestModalityFile |
		RequestModalityVideo
)

type RequestCapabilityMask uint64

const (
	RequestCapabilityTools RequestCapabilityMask = 1 << iota
	RequestCapabilityJSONSchema
	RequestCapabilityVision
	RequestCapabilityAudioInput
	RequestCapabilityAudioOutput
	RequestCapabilityImageOutput
	RequestCapabilityFileInput
	RequestCapabilityVideoInput
	RequestCapabilityVideoOutput
	RequestCapabilityRealtime
	RequestCapabilityStateful

	requestCapabilityMaskAll = RequestCapabilityTools |
		RequestCapabilityJSONSchema |
		RequestCapabilityVision |
		RequestCapabilityAudioInput |
		RequestCapabilityAudioOutput |
		RequestCapabilityImageOutput |
		RequestCapabilityFileInput |
		RequestCapabilityVideoInput |
		RequestCapabilityVideoOutput |
		RequestCapabilityRealtime |
		RequestCapabilityStateful
)

type RequestQuantityState string

const (
	RequestQuantityUnknown       RequestQuantityState = "unknown"
	RequestQuantityNotApplicable RequestQuantityState = "not_applicable"
	RequestQuantityKnown         RequestQuantityState = "known"
)

type RequestQuantity struct {
	State RequestQuantityState `json:"state"`
	Value int64                `json:"value,omitempty"`
}

func UnknownRequestQuantity() RequestQuantity {
	return RequestQuantity{State: RequestQuantityUnknown}
}

func NotApplicableRequestQuantity() RequestQuantity {
	return RequestQuantity{State: RequestQuantityNotApplicable}
}

func KnownRequestQuantity(value int64) RequestQuantity {
	return RequestQuantity{State: RequestQuantityKnown, Value: value}
}

func (quantity RequestQuantity) valid() bool {
	switch quantity.State {
	case RequestQuantityUnknown, RequestQuantityNotApplicable:
		return quantity.Value == 0
	case RequestQuantityKnown:
		return quantity.Value >= 0 && quantity.Value <= maxRequestProfileQuantity
	default:
		return false
	}
}

type RequestRetrySafety string

const (
	RequestRetrySafetyUnknown     RequestRetrySafety = "unknown"
	RequestRetrySafetySafe        RequestRetrySafety = "safe"
	RequestRetrySafetyConditional RequestRetrySafety = "conditional"
	RequestRetrySafetyUnsafe      RequestRetrySafety = "unsafe"
)

func (safety RequestRetrySafety) valid() bool {
	switch safety {
	case RequestRetrySafetyUnknown, RequestRetrySafetySafe, RequestRetrySafetyConditional, RequestRetrySafetyUnsafe:
		return true
	default:
		return false
	}
}

type RequestTenantTier string

const (
	RequestTenantTierStandard   RequestTenantTier = "standard"
	RequestTenantTierPriority   RequestTenantTier = "priority"
	RequestTenantTierEnterprise RequestTenantTier = "enterprise"
)

func (tier RequestTenantTier) valid() bool {
	switch tier {
	case RequestTenantTierStandard, RequestTenantTierPriority, RequestTenantTierEnterprise:
		return true
	default:
		return false
	}
}

type RequestProfileV2Input struct {
	RequestPath              string
	GroupName                string
	ModelName                string
	RequestKind              RequestKind
	SourceFormat             RequestSourceFormat
	InputModalities          RequestModalityMask
	OutputModalities         RequestModalityMask
	RequiredCapabilities     RequestCapabilityMask
	IsStream                 bool
	RetryIndex               int
	InputTokens              RequestQuantity
	OutputTokens             RequestQuantity
	CachedTokens             RequestQuantity
	ImageUnits               RequestQuantity
	AudioMillis              RequestQuantity
	VideoMillis              RequestQuantity
	DeadlineUnixMs           int64
	NodeID                   string
	Region                   string
	RetrySafety              RequestRetrySafety
	RetryAllowed             bool
	CrossChannelRetryAllowed bool
	HedgeAllowed             bool
	IdempotencyKeyPresent    bool
	TenantTier               RequestTenantTier
	CostBudgetNanoUSD        int64
}

func NewRequestProfileV2(input RequestProfileV2Input) (RequestProfile, error) {
	inputTokens := input.InputTokens
	outputTokens := input.OutputTokens
	cachedTokens := input.CachedTokens
	imageUnits := input.ImageUnits
	audioMillis := input.AudioMillis
	videoMillis := input.VideoMillis
	profile := RequestProfile{
		SchemaVersion:            RequestProfileSchemaV2,
		RequestPath:              strings.TrimSpace(input.RequestPath),
		GroupName:                strings.TrimSpace(input.GroupName),
		ModelName:                strings.TrimSpace(input.ModelName),
		RequestKind:              input.RequestKind,
		SourceFormat:             input.SourceFormat,
		InputModalities:          input.InputModalities,
		OutputModalities:         input.OutputModalities,
		RequiredCapabilities:     input.RequiredCapabilities,
		IsStream:                 input.IsStream,
		RetryIndex:               input.RetryIndex,
		InputTokens:              &inputTokens,
		OutputTokens:             &outputTokens,
		CachedTokens:             &cachedTokens,
		ImageUnits:               &imageUnits,
		AudioMillis:              &audioMillis,
		VideoMillis:              &videoMillis,
		DeadlineUnixMs:           input.DeadlineUnixMs,
		NodeID:                   strings.TrimSpace(input.NodeID),
		Region:                   strings.TrimSpace(input.Region),
		RetrySafety:              input.RetrySafety,
		RetryAllowed:             input.RetryAllowed,
		CrossChannelRetryAllowed: input.CrossChannelRetryAllowed,
		HedgeAllowed:             input.HedgeAllowed,
		IdempotencyKeyPresent:    input.IdempotencyKeyPresent,
		TenantTier:               input.TenantTier,
		CostBudgetNanoUSD:        input.CostBudgetNanoUSD,
	}
	if inputTokens.State == RequestQuantityKnown {
		profile.PromptTokenEstimate = int(inputTokens.Value)
	}
	if outputTokens.State == RequestQuantityKnown {
		profile.CompletionTokenEstimate = int(outputTokens.Value)
	}
	if err := profile.Validate(); err != nil {
		return RequestProfile{}, err
	}
	return profile, nil
}

func validateRequestProfile(profile RequestProfile) error {
	if profile.GroupName == "" || profile.ModelName == "" || profile.RetryIndex < 0 ||
		profile.PromptTokenEstimate < 0 || profile.CompletionTokenEstimate < 0 ||
		!validShadowText(profile.RequestPath, 512) || !validShadowText(profile.GroupName, 64) ||
		!validShadowText(profile.ModelName, 128) {
		return ErrShadowReplayInvalid
	}
	switch profile.SchemaVersion {
	case RequestProfileSchemaV1:
		if profile.hasV2Fields() {
			return ErrShadowReplayInvalid
		}
		return nil
	case RequestProfileSchemaV2:
		return validateRequestProfileV2(profile)
	default:
		return ErrShadowReplayInvalid
	}
}

func validateRequestProfileV2(profile RequestProfile) error {
	requestKind, kindKnown := profile.RequestKind.Mask()
	if !kindKnown || requestKind&requestKindMaskAll == 0 || !profile.SourceFormat.valid() ||
		!requestSourceFormatSupportsKind(profile.SourceFormat, profile.RequestKind) ||
		profile.InputModalities&^requestModalityMaskAll != 0 || profile.OutputModalities&^requestModalityMaskAll != 0 ||
		profile.RequiredCapabilities&^requestCapabilityMaskAll != 0 || profile.InputTokens == nil ||
		profile.OutputTokens == nil || profile.CachedTokens == nil || profile.ImageUnits == nil ||
		profile.AudioMillis == nil || profile.VideoMillis == nil || !profile.InputTokens.valid() ||
		!profile.OutputTokens.valid() || !profile.CachedTokens.valid() || !profile.ImageUnits.valid() ||
		!profile.AudioMillis.valid() || !profile.VideoMillis.valid() || profile.DeadlineUnixMs < 0 ||
		!validShadowText(profile.NodeID, 64) || !validShadowText(profile.Region, 64) ||
		!profile.RetrySafety.valid() || !profile.TenantTier.valid() || profile.CostBudgetNanoUSD < 0 {
		return ErrShadowReplayInvalid
	}
	if profile.PromptTokenEstimate != requestProfileLegacyQuantity(profile.InputTokens) ||
		profile.CompletionTokenEstimate != requestProfileLegacyQuantity(profile.OutputTokens) {
		return ErrShadowReplayInvalid
	}
	if profile.HedgeAllowed && (!profile.RetryAllowed || !profile.CrossChannelRetryAllowed) {
		return ErrShadowReplayInvalid
	}
	if profile.RetrySafety == RequestRetrySafetyUnsafe &&
		(profile.RetryAllowed || profile.CrossChannelRetryAllowed || profile.HedgeAllowed) {
		return ErrShadowReplayInvalid
	}
	if profile.RetrySafety == RequestRetrySafetyConditional &&
		(profile.RetryAllowed || profile.CrossChannelRetryAllowed || profile.HedgeAllowed) &&
		!profile.IdempotencyKeyPresent {
		return ErrShadowReplayInvalid
	}
	if profile.CrossChannelRetryAllowed && !profile.RetryAllowed {
		return ErrShadowReplayInvalid
	}
	if profile.RequiredCapabilities&(RequestCapabilityRealtime|RequestCapabilityStateful) != 0 &&
		(profile.CrossChannelRetryAllowed || profile.HedgeAllowed) {
		return ErrShadowReplayInvalid
	}
	if profile.RequestKind == RequestKindRealtime && profile.RequiredCapabilities&RequestCapabilityRealtime == 0 {
		return ErrShadowReplayInvalid
	}
	if profile.RequiredCapabilities&RequestCapabilityVision != 0 && profile.InputModalities&RequestModalityImage == 0 ||
		profile.RequiredCapabilities&RequestCapabilityAudioInput != 0 && profile.InputModalities&RequestModalityAudio == 0 ||
		profile.RequiredCapabilities&RequestCapabilityAudioOutput != 0 && profile.OutputModalities&RequestModalityAudio == 0 ||
		profile.RequiredCapabilities&RequestCapabilityImageOutput != 0 && profile.OutputModalities&RequestModalityImage == 0 ||
		profile.RequiredCapabilities&RequestCapabilityFileInput != 0 && profile.InputModalities&RequestModalityFile == 0 ||
		profile.RequiredCapabilities&RequestCapabilityVideoInput != 0 && profile.InputModalities&RequestModalityVideo == 0 ||
		profile.RequiredCapabilities&RequestCapabilityVideoOutput != 0 && profile.OutputModalities&RequestModalityVideo == 0 {
		return ErrShadowReplayInvalid
	}
	return nil
}

func requestProfileLegacyQuantity(quantity *RequestQuantity) int {
	if quantity == nil || quantity.State != RequestQuantityKnown {
		return 0
	}
	return int(quantity.Value)
}

func (profile RequestProfile) hasV2Fields() bool {
	return profile.RequestKind != "" || profile.SourceFormat != "" || profile.InputModalities != 0 ||
		profile.OutputModalities != 0 || profile.RequiredCapabilities != 0 || profile.InputTokens != nil ||
		profile.OutputTokens != nil || profile.CachedTokens != nil || profile.ImageUnits != nil ||
		profile.AudioMillis != nil || profile.VideoMillis != nil || profile.DeadlineUnixMs != 0 ||
		profile.NodeID != "" || profile.Region != "" || profile.RetrySafety != "" || profile.RetryAllowed ||
		profile.CrossChannelRetryAllowed || profile.HedgeAllowed || profile.IdempotencyKeyPresent ||
		profile.TenantTier != "" || profile.CostBudgetNanoUSD != 0
}

func resolveRequestProfile(
	provided *RequestProfile,
	requestPath string,
	groupName string,
	modelName string,
	isStream bool,
	retryIndex int,
	promptTokenEstimate int,
	completionTokenEstimate int,
) (RequestProfile, error) {
	if provided == nil {
		return NewRequestProfile(
			requestPath, groupName, modelName, isStream, retryIndex,
			promptTokenEstimate, completionTokenEstimate,
		)
	}
	profile := cloneRequestProfile(*provided)
	if err := profile.Validate(); err != nil || profile.RequestPath != strings.TrimSpace(requestPath) ||
		profile.GroupName != strings.TrimSpace(groupName) || profile.ModelName != strings.TrimSpace(modelName) ||
		profile.IsStream != isStream || profile.RetryIndex != retryIndex ||
		profile.PromptTokenEstimate != promptTokenEstimate ||
		profile.CompletionTokenEstimate != completionTokenEstimate {
		return RequestProfile{}, ErrShadowReplayInvalid
	}
	return profile, nil
}

func cloneRequestProfile(profile RequestProfile) RequestProfile {
	cloned := profile
	cloned.InputTokens = cloneRequestQuantity(profile.InputTokens)
	cloned.OutputTokens = cloneRequestQuantity(profile.OutputTokens)
	cloned.CachedTokens = cloneRequestQuantity(profile.CachedTokens)
	cloned.ImageUnits = cloneRequestQuantity(profile.ImageUnits)
	cloned.AudioMillis = cloneRequestQuantity(profile.AudioMillis)
	cloned.VideoMillis = cloneRequestQuantity(profile.VideoMillis)
	return cloned
}

func cloneRequestQuantity(quantity *RequestQuantity) *RequestQuantity {
	if quantity == nil {
		return nil
	}
	cloned := *quantity
	return &cloned
}
