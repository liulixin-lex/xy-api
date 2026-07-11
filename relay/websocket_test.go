package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wssResponseAdaptor struct {
	channel.Adaptor
	usage       any
	responseErr *types.NewAPIError
}

func (a *wssResponseAdaptor) Init(_ *relaycommon.RelayInfo) {}

func (a *wssResponseAdaptor) DoRequest(_ *gin.Context, _ *relaycommon.RelayInfo, _ io.Reader) (any, error) {
	return nil, nil
}

func (a *wssResponseAdaptor) DoResponse(_ *gin.Context, _ *http.Response, _ *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	return a.usage, a.responseErr
}

type wssBillingSpy struct {
	settledQuotas []int
	refunded      bool
}

func (s *wssBillingSpy) Settle(actualQuota int) error {
	s.settledQuotas = append(s.settledQuotas, actualQuota)
	return nil
}

func (s *wssBillingSpy) Refund(_ *gin.Context) {
	s.refunded = true
}

func (s *wssBillingSpy) NeedsRefund() bool {
	return len(s.settledQuotas) == 0 && !s.refunded
}

func (s *wssBillingSpy) GetPreConsumedQuota() int {
	return 10
}

func (s *wssBillingSpy) Reserve(_ int) error {
	return nil
}

func TestWssHelperSettlesOnlyCommittedRealtimeErrors(t *testing.T) {
	logConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() {
		common.LogConsumeEnabled = logConsumeEnabled
	})

	responseErr := types.NewError(errors.New("realtime handler failed"), types.ErrorCodeBadResponse)
	tests := []struct {
		name                  string
		receivedResponseCount int
		wantSettledQuotas     []int
		wantNeedsRefund       bool
	}{
		{
			name:                  "committed response settles returned usage once",
			receivedResponseCount: 1,
			wantSettledQuotas:     []int{0},
			wantNeedsRefund:       false,
		},
		{
			name:              "pre-commit error remains refundable",
			wantNeedsRefund:   true,
			wantSettledQuotas: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
			billing := &wssBillingSpy{}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "gpt-realtime",
				},
				StartTime:             time.Now(),
				ReceivedResponseCount: tt.receivedResponseCount,
				Billing:               billing,
				IsStream:              true,
			}
			adaptor := &wssResponseAdaptor{
				usage:       &dto.RealtimeUsage{},
				responseErr: responseErr,
			}

			gotErr := wssHelperWithAdaptor(ctx, info, adaptor)

			require.Same(t, responseErr, gotErr)
			assert.Equal(t, tt.wantSettledQuotas, billing.settledQuotas)
			assert.Equal(t, tt.wantNeedsRefund, billing.NeedsRefund())
			assert.False(t, billing.refunded)
		})
	}
}
