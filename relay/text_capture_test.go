package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettleOrCaptureTextUsageDefersBillingForHedgeWinner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	capture := &TextResponseCapture{}
	ctx.Set(textResponseCaptureContextKey, capture)
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	settleOrCaptureTextUsage(ctx, &relaycommon.RelayInfo{}, usage, false)

	require.Same(t, usage, capture.Usage)
	assert.False(t, capture.ContainsAudio)
}

func TestFinalizeTextResponseCaptureRejectsMissingUsage(t *testing.T) {
	assert.ErrorIs(t, FinalizeTextResponseCapture(nil, nil, nil), ErrTextResponseCaptureMissing)
}
