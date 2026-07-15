package middleware

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateRoutingCapacityTokensAcrossTextProtocols(t *testing.T) {
	tests := []struct {
		name            string
		path            string
		body            string
		wantOutput      int
		wantInput       bool
		wantOutputKnown bool
		wantStreamKnown bool
		wantStream      bool
	}{
		{
			name: "openai", path: "/v1/chat/completions",
			body:       `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":128,"n":2,"stream":true}`,
			wantOutput: 256, wantInput: true, wantOutputKnown: true, wantStreamKnown: true, wantStream: true,
		},
		{
			name: "responses", path: "/v1/responses",
			body:       `{"model":"gpt-test","input":"hello","max_output_tokens":256}`,
			wantOutput: 256, wantInput: true, wantOutputKnown: true,
		},
		{
			name: "claude legacy cap", path: "/v1/messages",
			body:       `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"max_tokens_to_sample":384}`,
			wantOutput: 384, wantInput: true, wantOutputKnown: true,
		},
		{
			name: "gemini", path: "/v1beta/models/gemini-test:streamGenerateContent",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":512}}`,
			wantOutput: 512, wantInput: true, wantOutputKnown: true, wantStreamKnown: true, wantStream: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(test.path, []byte(test.body))
			assert.Equal(t, len(test.body), estimate.Input.Tokens)
			assert.Equal(t, test.wantInput, estimate.Input.Known())
			assert.Equal(t, test.wantOutput, estimate.Output.Tokens)
			assert.Equal(t, test.wantOutputKnown, estimate.Output.Known())
			assert.Equal(t, test.wantStreamKnown, estimate.StreamKnown)
			assert.Equal(t, test.wantStream, estimate.Stream)
		})
	}
}

func TestBuildRoutingCostRequestProfileKeepsRequestInputTransientAndKnowledgeExplicit(t *testing.T) {
	body := []byte(`{"model":"gpt-test","max_tokens":256}`)
	profile := buildRoutingCostRequestProfile(
		"/v1/chat/completions",
		body,
		http.Header{"X-Priority": []string{"fast"}},
		routingCapacityTokenEstimate{
			Input: channelrouting.CapacityDimensionEstimate{
				State: channelrouting.CapacityDimensionBoundedKnown, Tokens: 400,
			},
			Output: channelrouting.CapacityDimensionEstimate{
				State: channelrouting.CapacityDimensionBoundedKnown, Tokens: 256,
			},
			CacheWriteTokensKnown: true,
		},
		100,
		64,
	)

	require.NotNil(t, profile)
	assert.True(t, profile.KnowledgeSpecified)
	assert.True(t, profile.InputTokensKnown)
	assert.True(t, profile.MaximumCompletionKnown)
	assert.False(t, profile.CacheTokensKnown)
	assert.False(t, profile.CacheReadTokensKnown)
	assert.True(t, profile.CacheWriteTokensKnown)
	assert.True(t, profile.MediaDimensionsKnown)
	assert.True(t, profile.RequestInputKnown)
	assert.Equal(t, int64(100), profile.PromptTokens)
	assert.Equal(t, int64(400), profile.MaximumPromptTokens)
	assert.Equal(t, int64(64), profile.ExpectedCompletionTokens)
	assert.Equal(t, int64(256), profile.MaximumCompletionTokens)
	assert.Equal(t, "fast", profile.Request.Headers["X-Priority"])
	assert.Equal(t, body, profile.Request.Body)

	unknown := buildRoutingCostRequestProfile(
		"/v1/responses",
		body,
		nil,
		routingCapacityTokenEstimate{
			Input:       channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
			Output:      channelrouting.CapacityDimensionEstimate{State: channelrouting.CapacityDimensionApplicableUnknown},
			RemoteState: true, HasMedia: true,
		},
		100,
		64,
	)
	assert.False(t, unknown.InputTokensKnown)
	assert.False(t, unknown.MaximumCompletionKnown)
	assert.False(t, unknown.CacheReadTokensKnown)
	assert.False(t, unknown.CacheWriteTokensKnown)
	assert.False(t, unknown.MediaDimensionsKnown)
}

func TestEstimateRoutingCapacityTokensSplitsCacheReadAndWriteKnowledge(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		body           string
		wantWriteKnown bool
	}{
		{
			name:           "recognized request without cache control",
			path:           "/v1/chat/completions",
			body:           `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`,
			wantWriteKnown: true,
		},
		{
			name: "explicit cache control remains unknown",
			path: "/v1/messages",
			body: `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			name: "escaped cache control key remains unknown",
			path: "/v1/messages",
			body: `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache\u005fcontrol":{"type":"ephemeral"}}]}]}`,
		},
		{
			name: "prompt cache key remains unknown",
			path: "/v1/responses",
			body: `{"model":"gpt-test","input":"hello","max_output_tokens":32,"prompt_cache_key":"tenant-1"}`,
		},
		{
			name: "prompt cache options remain unknown",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"prompt_cache_options":{"retention":"24h"}}`,
		},
		{
			name: "prompt cache retention remains unknown",
			path: "/v1/responses/compact",
			body: `{"model":"gpt-test","input":"hello","prompt_cache_retention":"24h"}`,
		},
		{
			name: "remote response state remains unknown",
			path: "/v1/responses",
			body: `{"model":"gpt-test","input":"continue","previous_response_id":"resp_1","max_output_tokens":32}`,
		},
		{
			name: "unknown protocol remains unknown",
			path: "/vendor/chat",
			body: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`,
		},
		{
			name: "malformed recognized request remains unknown",
			path: "/v1/chat/completions",
			body: `{"model":`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(tt.path, []byte(tt.body))
			assert.Equal(t, tt.wantWriteKnown, estimate.CacheWriteTokensKnown)
			profile := buildRoutingCostRequestProfile(tt.path, []byte(tt.body), nil, estimate, 10, 5)
			require.NotNil(t, profile)
			assert.False(t, profile.CacheTokensKnown)
			assert.False(t, profile.CacheReadTokensKnown)
			assert.Equal(t, tt.wantWriteKnown, profile.CacheWriteTokensKnown)
		})
	}
}

func TestEstimateRoutingCapacityTokensMarksUncataloguedSurchargeDimensions(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		body          string
		wantKnown     bool
		wantSurcharge bool
	}{
		{
			name: "ordinary chat", path: "/v1/chat/completions",
			body:      `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			wantKnown: true,
		},
		{
			name: "chat web search options", path: "/v1/chat/completions",
			body:      `{"model":"gpt-test","messages":[],"web_search_options":{"search_context_size":"medium"}}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "responses file search", path: "/v1/responses",
			body:      `{"model":"gpt-test","input":"hello","tools":[{"type":"file_search"}]}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "responses image generation", path: "/v1/responses",
			body:      `{"model":"gpt-test","input":"draw","tools":[{"type":"image_generation"}]}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "claude web search", path: "/v1/messages",
			body:      `{"model":"claude-test","max_tokens":32,"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "fixed image request", path: "/v1/images/generations",
			body:      `{"model":"gpt-image-1","prompt":"draw"}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "sub2api v1 alpha search", path: "/v1/alpha/search",
			body:      `{"model":"gpt-test"}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "sub2api alpha search path is sufficient", path: "/alpha/search",
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "sub2api alpha search trailing slash", path: "/alpha/search/",
			body:      `{"model":"gpt-test"}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "sub2api codex alpha search", path: "/backend-api/codex/alpha/search",
			body:      `{"model":"gpt-test"}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "sub2api alpha search behind base path", path: "/tenant/upstream/v1/alpha/search?preview=true",
			body:      `{"model":"gpt-test"}`,
			wantKnown: true, wantSurcharge: true,
		},
		{
			name: "alpha search suffix requires segment boundary", path: "/v1/not-alpha/search",
			body: `{"model":"gpt-test"}`,
		},
		{
			name: "alpha search endpoint rejects longer leaf", path: "/v1/alpha/search-preview",
			body: `{"model":"gpt-test"}`,
		},
		{
			name: "custom JSON object is not a recognized pricing envelope", path: "/vendor/chat",
			body: `{"model":"gpt-test","messages":[]}`,
		},
		{
			name: "malformed body", path: "/v1/chat/completions", body: `{"model":`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(test.path, []byte(test.body))
			assert.Equal(t, test.wantKnown, estimate.RequestPricingFeaturesKnown)
			assert.Equal(t, test.wantSurcharge, estimate.UncataloguedSurchargePossible)

			profile := buildRoutingCostRequestProfile(test.path, []byte(test.body), nil, estimate, 10, 5)
			require.NotNil(t, profile)
			assert.Equal(t, test.wantKnown, profile.RequestPricingFeaturesKnown)
			assert.Equal(t, test.wantSurcharge, profile.UncataloguedSurchargePossible)
		})
	}
}

func TestAlphaSearchRequestKeepsSub2APIEstimateUnknown(t *testing.T) {
	body := []byte(`{"model":"gpt-test"}`)
	estimate := estimateRoutingCapacityTokens("/backend-api/codex/alpha/search/", body)
	profile := buildRoutingCostRequestProfile(
		"/backend-api/codex/alpha/search/",
		body,
		nil,
		estimate,
		10,
		10,
	)
	require.NotNil(t, profile)
	require.True(t, profile.RequestPricingFeaturesKnown)
	require.True(t, profile.UncataloguedSurchargePossible)

	extras, err := common.Marshal(map[string]any{
		"platform":            "openai",
		"source_billing_mode": "token",
		"sub2api_contract":    model.RoutingCostSub2APIDisplayContractV1,
		"has_intervals":       false,
	})
	require.NoError(t, err)
	inputRate := 2.0
	outputRate := 10.0
	observed := common.GetTimestamp()
	cost, err := model.EstimateRoutingCostSnapshot(
		model.RoutingCostSnapshotVersion{
			SourceType:      model.RoutingUpstreamTypeSub2API,
			Confidence:      model.RoutingCostConfidenceDerived,
			ConfidenceScore: 0.8,
			Freshness:       model.RoutingCostFreshnessFresh,
			FreshnessScore:  1,
			ObservedTime:    observed,
			EffectiveTime:   observed,
			ExpiresTime:     observed + 3_600,
		},
		model.RoutingNormalizedPricing{
			QuotaType:            0,
			BillingMode:          "token",
			Currency:             "USD",
			InputCostPerMillion:  &inputRate,
			OutputCostPerMillion: &outputRate,
			Extras:               extras,
		},
		*profile,
		observed,
	)
	require.NoError(t, err)
	assert.False(t, cost.ExpectedKnown)
	assert.False(t, cost.WorstCaseKnown)
	assert.False(t, cost.Known)
	assert.Zero(t, cost.ExpectedCost)
	assert.Zero(t, cost.WorstCaseCost)
}

func TestEstimateRoutingCapacityTokensFailsClosedForUnknownEnvelope(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "omitted output", path: "/v1/chat/completions", body: `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`},
		{name: "responses remote state", path: "/v1/responses", body: `{"model":"gpt-test","input":"continue","previous_response_id":"resp_1","max_output_tokens":32}`},
		{name: "gemini cached content", path: "/v1beta/models/gemini-test:generateContent", body: `{"cachedContent":"cachedContents/1","generationConfig":{"maxOutputTokens":32}}`},
		{name: "media input", path: "/v1/chat/completions", body: `{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}],"max_tokens":32}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(test.path, []byte(test.body))
			if test.name == "omitted output" {
				assert.True(t, estimate.Input.Known())
				assert.False(t, estimate.Output.Known())
				return
			}
			assert.False(t, estimate.Input.Known())
		})
	}

	realtime := estimateRoutingCapacityTokens("/v1/realtime", nil)
	assert.False(t, realtime.Input.Known())
	assert.False(t, realtime.Output.Known())
	assert.True(t, realtime.StreamKnown)
	assert.True(t, realtime.Stream)
}

func TestEstimateRoutingCapacityTokensTreatsEmbeddingsAsKnownZeroOutput(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "openai", path: "/v1/embeddings", body: `{"model":"text-embedding-3-small","input":"hello"}`},
		{name: "gemini", path: "/v1beta/models/text-embedding-004:embedContent", body: `{"content":{"parts":[{"text":"hello"}]}}`},
		{name: "gemini batch", path: "/v1beta/models/text-embedding-004:batchEmbedContents", body: `{"requests":[{"content":{"parts":[{"text":"hello"}]}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(test.path, []byte(test.body))
			assert.True(t, estimate.Input.Known())
			assert.Equal(t, len(test.body), estimate.Input.Tokens)
			assert.True(t, estimate.Output.Known())
			assert.Zero(t, estimate.Output.Tokens)
			assert.True(t, estimate.StreamKnown)
			assert.False(t, estimate.Stream)
		})
	}
}

func TestEstimateRoutingCapacityTokensMarksMediaProtocolsNotApplicable(t *testing.T) {
	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/v1/audio/transcriptions",
		"/v1/audio/speech",
		"/v1/videos",
		"/kling/v1/videos/text2video",
		"/mj/submit/imagine",
		"/suno/submit/music",
	} {
		t.Run(path, func(t *testing.T) {
			estimate := estimateRoutingCapacityTokens(path, nil)
			assert.Equal(t, channelrouting.CapacityDimensionNotApplicable, estimate.Input.State)
			assert.Equal(t, channelrouting.CapacityDimensionNotApplicable, estimate.Output.State)
			assert.Zero(t, estimate.Input.Tokens)
			assert.Zero(t, estimate.Output.Tokens)
		})
	}
}
