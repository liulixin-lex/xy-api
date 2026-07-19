package openai

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type realtimeBillingStub struct {
	reserved    int
	reserveTo   int
	reserveErr  error
	refundCalls int
}

func (s *realtimeBillingStub) Settle(int) error { return nil }

func (s *realtimeBillingStub) Refund(*gin.Context) { s.refundCalls++ }

func (s *realtimeBillingStub) NeedsRefund() bool { return true }

func (s *realtimeBillingStub) GetPreConsumedQuota() int { return s.reserved }

func (s *realtimeBillingStub) Reserve(targetQuota int) error {
	s.reserveTo = targetQuota
	return s.reserveErr
}

func TestPreConsumeUsagePropagatesRealtimeReserveFailure(t *testing.T) {
	reserveErr := errors.New("reserve rejected")
	billing := &realtimeBillingStub{reserved: 40, reserveErr: reserveErr}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-4o",
		UsingGroup:      "default",
		UserGroup:       "default",
		Billing:         billing,
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	usage := &dto.RealtimeUsage{TotalTokens: 1_000, InputTokens: 1_000}
	usage.InputTokenDetails.TextTokens = 1_000
	total := &dto.RealtimeUsage{}

	err := preConsumeUsage(ctx, info, usage, total)
	require.ErrorIs(t, err, reserveErr)
	assert.Greater(t, billing.reserveTo, billing.reserved)
	assert.Equal(t, usage.TotalTokens, total.TotalTokens)
	assert.Equal(t, usage.InputTokens, total.InputTokens)
	assert.Zero(t, billing.refundCalls)
}
