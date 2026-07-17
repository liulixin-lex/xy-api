package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingHedgeCostEstimateFailsClosedWithoutInputAndCompletionBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, known, err := ChannelRoutingHedgeCostEstimate(ctx, 1, "gpt-test", "/v1/chat/completions", 0)
	require.NoError(t, err)
	assert.False(t, known)

	common.SetContextKey(ctx, constant.ContextKeyRoutingCostProfile, &model.RoutingCostRequestProfile{
		KnowledgeSpecified: true, MaximumCompletionKnown: true,
		CacheTokensKnown: true, ImageInputTokensKnown: true, ImageOutputTokensKnown: true,
		ImageUnitsKnown: true, AudioInputTokensKnown: true, AudioOutputTokensKnown: true,
		RequestInputKnown: true,
	})
	_, known, err = ChannelRoutingHedgeCostEstimate(ctx, 1, "gpt-test", "/v1/chat/completions", 0)
	require.NoError(t, err)
	assert.False(t, known)
}

func TestNormalizeRoutingHedgeActualUsagePreservesClaudeCacheSemantics(t *testing.T) {
	usage := &dto.Usage{
		InputTokens: 100, OutputTokens: 10, UsageSemantic: "anthropic",
		InputTokensDetails:          &dto.InputTokenDetails{CachedTokens: 300},
		ClaudeCacheCreation5mTokens: 50,
		ClaudeCacheCreation1hTokens: 20,
	}

	normalized, ok := normalizeRoutingHedgeActualUsage(usage)

	require.True(t, ok)
	assert.Equal(t, 100, normalized.promptTokens)
	assert.Equal(t, 10, normalized.completionTokens)
	assert.Equal(t, int64(470), normalized.auditPromptTokens)
	assert.Equal(t, int64(300), normalized.cacheReadTokens)
	assert.Equal(t, int64(50), normalized.cacheWriteTokens)
	assert.Equal(t, int64(20), normalized.cacheWrite1hTokens)
	assert.True(t, normalized.claudeSemantic)
}

func TestNormalizeRoutingHedgeActualUsageRejectsInvalidOpenAIAndMediaCounts(t *testing.T) {
	tests := []struct {
		name  string
		usage *dto.Usage
	}{
		{
			name: "openai cache exceeds prompt",
			usage: &dto.Usage{
				PromptTokens: 100, CompletionTokens: 10,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 101},
			},
		},
		{
			name: "image output exceeds completion",
			usage: &dto.Usage{
				PromptTokens: 100, CompletionTokens: 10,
				CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 11},
			},
		},
		{
			name:  "negative count",
			usage: &dto.Usage{PromptTokens: -1, CompletionTokens: 10},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, ok := normalizeRoutingHedgeActualUsage(test.usage)
			assert.False(t, ok)
		})
	}
}
