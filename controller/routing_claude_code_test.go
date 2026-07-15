package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyRoutingRequestTrafficMatchesSub2APIClaudeCodeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	legacyUserID := "user_" + strings.Repeat("a", 64) +
		"_account__session_12345678-1234-1234-1234-123456789abc"
	validMetadata := []byte(`{"user_id":"` + legacyUserID + `"}`)
	validSystem := "You are Claude Code, Anthropic's official CLI for Claude."

	newRequest := func() *dto.ClaudeRequest {
		return &dto.ClaudeRequest{
			Model:    "claude-sonnet-4",
			System:   []dto.ClaudeMediaMessage{{Type: "text", Text: &validSystem}},
			Metadata: validMetadata,
		}
	}
	newContext := func(path string) *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, path, nil)
		ctx.Request.Header.Set("User-Agent", "claude-cli/2.1.79")
		ctx.Request.Header.Set("X-App", "claude-code")
		ctx.Request.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
		ctx.Request.Header.Set("anthropic-version", "2023-06-01")
		return ctx
	}

	tests := []struct {
		name       string
		path       string
		format     types.RelayFormat
		mutateCtx  func(*gin.Context)
		mutateBody func(*dto.ClaudeRequest)
		want       channelrouting.RequestTrafficClass
	}{
		{name: "strict messages request", path: "/v1/messages", format: types.RelayFormatClaude, want: channelrouting.RequestTrafficClassClaudeCode},
		{name: "billing attribution block", path: "/v1/messages", format: types.RelayFormatClaude, mutateBody: func(request *dto.ClaudeRequest) {
			billing := "x-anthropic-billing-header: cc_entrypoint=claude-vscode"
			request.System = []dto.ClaudeMediaMessage{{Type: "text", Text: &billing}}
			request.Metadata = []byte(`{"user_id":"{\"device_id\":\"device\",\"session_id\":\"session\"}"}`)
		}, want: channelrouting.RequestTrafficClassClaudeCode},
		{name: "count tokens UA fast path", path: "/v1/messages/count_tokens", format: types.RelayFormatClaude, mutateBody: func(request *dto.ClaudeRequest) {
			request.System = nil
			request.Metadata = nil
		}, want: channelrouting.RequestTrafficClassClaudeCode},
		{name: "haiku probe UA fast path", path: "/v1/messages", format: types.RelayFormatClaude, mutateBody: func(request *dto.ClaudeRequest) {
			one := uint(1)
			request.Model = "claude-3-haiku"
			request.MaxTokens = &one
			request.System = nil
			request.Metadata = nil
		}, mutateCtx: func(ctx *gin.Context) {
			ctx.Request.Header.Del("X-App")
			ctx.Request.Header.Del("anthropic-beta")
			ctx.Request.Header.Del("anthropic-version")
		}, want: channelrouting.RequestTrafficClassClaudeCode},
		{name: "non Claude endpoint stays standard", path: "/v1/chat/completions", format: types.RelayFormatOpenAI, want: channelrouting.RequestTrafficClassStandard},
		{name: "ordinary Claude client", path: "/v1/messages", format: types.RelayFormatClaude, mutateCtx: func(ctx *gin.Context) {
			ctx.Request.Header.Set("User-Agent", "curl/8.0")
		}, want: channelrouting.RequestTrafficClassStandard},
		{name: "matching UA with missing header is explicit unknown", path: "/v1/messages", format: types.RelayFormatClaude, mutateCtx: func(ctx *gin.Context) {
			ctx.Request.Header.Del("X-App")
		}, want: channelrouting.RequestTrafficClassUnknown},
		{name: "matching UA with invalid metadata is explicit unknown", path: "/v1/messages", format: types.RelayFormatClaude, mutateBody: func(request *dto.ClaudeRequest) {
			request.Metadata = []byte(`{"user_id":"not-a-claude-code-id"}`)
		}, want: channelrouting.RequestTrafficClassUnknown},
		{name: "matching UA with unrelated system is explicit unknown", path: "/v1/messages", format: types.RelayFormatClaude, mutateBody: func(request *dto.ClaudeRequest) {
			text := "You are a general assistant."
			request.System = []dto.ClaudeMediaMessage{{Type: "text", Text: &text}}
		}, want: channelrouting.RequestTrafficClassUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := newContext(test.path)
			request := newRequest()
			if test.mutateCtx != nil {
				test.mutateCtx(ctx)
			}
			if test.mutateBody != nil {
				test.mutateBody(request)
			}
			assert.Equal(t, test.want, classifyRoutingRequestTraffic(ctx, test.format, request))
		})
	}
}

func TestBuildRoutingRequestProfileCarriesClaudeCodeTrafficClass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx.Request.Header.Set("User-Agent", "claude-cli/2.1.79")
	ctx.Request.Header.Set("X-App", "claude-code")
	ctx.Request.Header.Set("anthropic-beta", "beta")
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	prompt := "You are Claude Code, Anthropic's official CLI for Claude."
	request := &dto.ClaudeRequest{
		Model:    "claude-sonnet-4",
		System:   []dto.ClaudeMediaMessage{{Type: "text", Text: &prompt}},
		Metadata: []byte(`{"user_id":"user_` + strings.Repeat("b", 64) + `_account__session_12345678-1234-1234-1234-123456789abc"}`),
	}

	profile, err := buildRoutingRequestProfileTemplate(ctx, types.RelayFormatClaude, request, request.Model)
	require.NoError(t, err)
	assert.Equal(t, channelrouting.RequestTrafficClassClaudeCode, profile.TrafficClass)
}
