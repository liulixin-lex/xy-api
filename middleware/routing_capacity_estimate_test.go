package middleware

import (
	"net/http"
	"testing"

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
		},
		100,
		64,
	)

	require.NotNil(t, profile)
	assert.True(t, profile.KnowledgeSpecified)
	assert.True(t, profile.InputTokensKnown)
	assert.True(t, profile.MaximumCompletionKnown)
	assert.False(t, profile.CacheTokensKnown)
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
	assert.False(t, unknown.MediaDimensionsKnown)
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
