package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type httpStreamBillingSpy struct {
	preConsumedQuota int
	settledQuotas    []int
}

func (s *httpStreamBillingSpy) Settle(actualQuota int) error {
	s.settledQuotas = append(s.settledQuotas, actualQuota)
	return nil
}

func (s *httpStreamBillingSpy) Refund(_ *gin.Context) {}

func (s *httpStreamBillingSpy) NeedsRefund() bool { return len(s.settledQuotas) == 0 }

func (s *httpStreamBillingSpy) GetPreConsumedQuota() int { return s.preConsumedQuota }

func (s *httpStreamBillingSpy) Reserve(_ int) error { return nil }

func TestFinalizeHTTPStreamErrorSettlesOnlyCommittedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}))
	mainDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = mainDB })

	logConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = logConsumeEnabled })

	tests := []struct {
		name           string
		committed      bool
		streamFailure  bool
		usage          any
		wantSettled    []int
		wantHandled    bool
		wantRefundable bool
	}{
		{
			name:        "committed partial usage settles actual quota",
			committed:   true,
			usage:       &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			wantSettled: []int{5},
		},
		{
			name:        "committed missing usage retains reserved quota",
			committed:   true,
			wantSettled: []int{10},
		},
		{
			name:          "committed stream failure with empty usage retains reserved quota",
			committed:     true,
			streamFailure: true,
			usage:         &dto.Usage{},
			wantSettled:   []int{10},
			wantHandled:   true,
		},
		{
			name:           "pre-commit error remains refundable",
			usage:          &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			wantRefundable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tt.committed {
				_, err := ctx.Writer.Write([]byte("data"))
				require.NoError(t, err)
			}
			billing := &httpStreamBillingSpy{preConsumedQuota: 10}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{},
				Billing:     billing,
				IsStream:    true,
				StartTime:   time.Now(),
				UserQuota:   1_000_000,
				PriceData: types.PriceData{
					ModelRatio:      1,
					CompletionRatio: 1,
					GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
				},
			}
			if tt.streamFailure {
				info.StreamStatus = relaycommon.NewStreamStatus()
				info.StreamStatus.RecordError("malformed trailing chunk")
			}

			handled := false
			if tt.streamFailure {
				handled = finalizeCommittedHTTPStreamFailure(ctx, info, tt.usage, false)
			} else {
				finalizeHTTPStreamError(ctx, info, tt.usage, false)
			}

			assert.Equal(t, tt.wantSettled, billing.settledQuotas)
			assert.Equal(t, tt.wantHandled, handled)
			assert.Equal(t, tt.wantRefundable, billing.NeedsRefund())
		})
	}
}
