package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreWssConsumeQuotaReservesCumulativeUsageWithoutDoubleCharge(t *testing.T) {
	truncate(t)
	seedUser(t, 9921, 100000)
	seedToken(t, 9922, 9921, "wss-token", 100000)
	seedChannel(t, 9923)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId:          9921,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 9923},
		TokenId:         9922,
		TokenKey:        "wss-token",
		UsingGroup:      "default",
		UserGroup:       "default",
		OriginModelName: "gpt-4o-mini-realtime-preview",
		StartTime:       time.Now(),
		IsStream:        true,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		PriceData: types.PriceData{
			ModelRatio:     37.5,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
	}
	require.Nil(t, PreConsumeBilling(ctx, 0, info))
	usage := &dto.RealtimeUsage{
		TotalTokens: 1000,
		InputTokens: 1000,
		InputTokenDetails: dto.InputTokenDetails{
			TextTokens: 1000,
		},
	}

	require.NoError(t, PreWssConsumeQuota(ctx, info, usage))
	assert.Equal(t, 37500, info.Billing.GetPreConsumedQuota())
	assert.Equal(t, 62500, getUserQuota(t, 9921))
	assert.Equal(t, 62500, getTokenRemainQuota(t, 9922))

	PostWssConsumeQuota(ctx, info, info.OriginModelName, usage, "")

	assert.Equal(t, 62500, getUserQuota(t, 9921))
	assert.Equal(t, 62500, getTokenRemainQuota(t, 9922))
	var logCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}
