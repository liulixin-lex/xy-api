package aws

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimeTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAWSResponseStream struct {
	events <-chan bedrockruntimeTypes.ResponseStream
	err    error
}

func (s *fakeAWSResponseStream) Events() <-chan bedrockruntimeTypes.ResponseStream {
	return s.events
}

func (s *fakeAWSResponseStream) Err() error {
	return s.err
}

type awsFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

func (w *awsFailOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newFakeAWSResponseStream(events ...bedrockruntimeTypes.ResponseStream) *fakeAWSResponseStream {
	ch := make(chan bedrockruntimeTypes.ResponseStream, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeAWSResponseStream{events: ch}
}

func awsResponseChunk(data string) bedrockruntimeTypes.ResponseStream {
	return &bedrockruntimeTypes.ResponseStreamMemberChunk{
		Value: bedrockruntimeTypes.PayloadPart{Bytes: []byte(data)},
	}
}

func TestDoAwsClientRequest_AppliesRuntimeHeaderOverrideToAnthropicBeta(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName:           "claude-3-5-sonnet-20240620",
		IsStream:                  false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"anthropic-beta": "computer-use-2025-01-24",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:            "access-key|secret-key|us-east-1",
			UpstreamModelName: "claude-3-5-sonnet-20240620",
		},
	}

	requestBody := bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":128}`)
	adaptor := &Adaptor{}

	_, err := doAwsClientRequest(ctx, info, adaptor, requestBody)
	require.NoError(t, err)

	awsReq, ok := adaptor.AwsReq.(*bedrockruntime.InvokeModelInput)
	require.True(t, ok)

	var payload map[string]any
	require.NoError(t, common.Unmarshal(awsReq.Body, &payload))

	anthropicBeta, exists := payload["anthropic_beta"]
	require.True(t, exists)

	values, ok := anthropicBeta.([]any)
	require.True(t, ok)
	require.Equal(t, []any{"computer-use-2025-01-24"}, values)
}

func TestConsumeAWSResponseStreamReturnsPartialUsageOnChunkError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"},
	}
	stream := newFakeAWSResponseStream(
		awsResponseChunk(`{"type":"message_start","message":{"id":"msg_aws","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`),
		awsResponseChunk(`{malformed`),
	)

	apiErr, usage := consumeAWSResponseStream(ctx, info, stream, func() {})

	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "invalid character")
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestConsumeAWSResponseStreamReturnsUsageWhenFinalizationWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writeErr := errors.New("aws finalization write failed")
	ctx.Writer = &awsFailOnMarkerWriter{ResponseWriter: ctx.Writer, marker: []byte("[DONE]"), err: writeErr}
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"},
	}
	stream := newFakeAWSResponseStream(
		awsResponseChunk(`{"type":"message_start","message":{"id":"msg_aws","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`),
	)

	apiErr, usage := consumeAWSResponseStream(ctx, info, stream, func() {})

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestConsumeAWSResponseStreamReturnsPartialUsageOnStreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"},
	}
	streamErr := errors.New("aws event stream failed")
	stream := newFakeAWSResponseStream(
		awsResponseChunk(`{"type":"message_start","message":{"id":"msg_aws","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`),
	)
	stream.err = streamErr

	apiErr, usage := consumeAWSResponseStream(ctx, info, stream, func() {})

	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, streamErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}
