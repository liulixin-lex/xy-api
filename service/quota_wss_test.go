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

func TestPostWssConsumeQuotaMissingUsageRetainsReservedQuotaAndRecordsConsumption(t *testing.T) {
	truncate(t)
	seedUser(t, 9931, 100000)
	seedToken(t, 9932, 9931, "wss-missing-usage", 100000)
	seedChannel(t, 9933)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId:          9931,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 9933},
		TokenId:         9932,
		TokenKey:        "wss-missing-usage",
		UsingGroup:      "default",
		UserGroup:       "default",
		OriginModelName: "gpt-realtime",
		StartTime:       time.Now(),
		IsStream:        true,
		ForcePreConsume: true,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
	}

	require.Nil(t, PreConsumeBilling(ctx, 250, info))
	assert.Equal(t, 99750, getUserQuota(t, 9931))
	assert.Equal(t, 99750, getTokenRemainQuota(t, 9932))

	require.NotPanics(t, func() {
		PostWssConsumeQuota(ctx, info, info.OriginModelName, nil, "")
	})

	assert.False(t, info.Billing.NeedsRefund())
	assert.Equal(t, 99750, getUserQuota(t, 9931))
	assert.Equal(t, 99750, getTokenRemainQuota(t, 9932))

	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").Where("id = ?", 9931).First(&user).Error)
	assert.Equal(t, 250, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)

	var channel model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", 9933).First(&channel).Error)
	assert.Equal(t, int64(250), channel.UsedQuota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, int64(1), countLogs(t))
	assert.Equal(t, 250, log.Quota)
	assert.Zero(t, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	assert.Contains(t, log.Content, "缺少 usage")
}

func TestPostTextConsumeQuotaMissingUsageRetainsReservedQuotaAndRecordsConsumption(t *testing.T) {
	truncate(t)
	seedUser(t, 9941, 100000)
	seedToken(t, 9942, 9941, "stream-missing-usage", 100000)
	seedChannel(t, 9943)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId:          9941,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 9943},
		TokenId:         9942,
		TokenKey:        "stream-missing-usage",
		UsingGroup:      "default",
		UserGroup:       "default",
		OriginModelName: "stream-model",
		StartTime:       time.Now(),
		IsStream:        true,
		ForcePreConsume: true,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		PriceData: types.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
		},
	}
	info.SetEstimatePromptTokens(100)

	require.Nil(t, PreConsumeBilling(ctx, 250, info))
	assert.Equal(t, 99750, getUserQuota(t, 9941))
	assert.Equal(t, 99750, getTokenRemainQuota(t, 9942))

	PostTextConsumeQuota(ctx, info, nil, nil)

	assert.False(t, info.Billing.NeedsRefund())
	assert.Equal(t, 99750, getUserQuota(t, 9941))
	assert.Equal(t, 99750, getTokenRemainQuota(t, 9942))

	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").Where("id = ?", 9941).First(&user).Error)
	assert.Equal(t, 250, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)

	var channel model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", 9943).First(&channel).Error)
	assert.Equal(t, int64(250), channel.UsedQuota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, int64(1), countLogs(t))
	assert.Equal(t, 250, log.Quota)
	assert.Equal(t, 100, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	assert.Contains(t, log.Content, "按预扣额度结算")
}
