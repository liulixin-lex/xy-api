package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withQuotaPerUnitForAffiliateTest(t *testing.T, quotaPerUnit float64) {
	t.Helper()
	original := common.QuotaPerUnit
	common.QuotaPerUnit = quotaPerUnit
	t.Cleanup(func() {
		common.QuotaPerUnit = original
	})
}

func withAffiliateRewardPercents(t *testing.T, continuousPercent int, firstTopupPercent int) {
	t.Helper()
	paymentSetting := operation_setting.GetPaymentSetting()
	originalContinuousPercent := paymentSetting.AffiliateContinuousPercent
	originalFirstTopupPercent := paymentSetting.AffiliateFirstTopupPercent
	paymentSetting.AffiliateContinuousPercent = continuousPercent
	paymentSetting.AffiliateFirstTopupPercent = firstTopupPercent
	t.Cleanup(func() {
		paymentSetting.AffiliateContinuousPercent = originalContinuousPercent
		paymentSetting.AffiliateFirstTopupPercent = originalFirstTopupPercent
	})
}

func insertAffiliateRewardUser(t *testing.T, user *User) {
	t.Helper()
	require.NoError(t, DB.Create(user).Error)
}

func insertAffiliateRewardTopUp(t *testing.T, tradeNo string, userId int, amount int64, paymentProvider string) {
	t.Helper()
	require.NoError(t, DB.Create(&TopUp{
		UserId:          userId,
		Amount:          amount,
		Money:           float64(amount),
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}).Error)
}

func getAffiliateRewardUser(t *testing.T, id int) User {
	t.Helper()
	var user User
	require.NoError(t, DB.First(&user, id).Error)
	return user
}

func TestRechargeWaffoAppliesContinuousAffiliateRewardOnce(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)
	withAffiliateRewardPercents(t, 5, 30)

	insertAffiliateRewardUser(t, &User{
		Id:       1,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff1",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  2,
		Username:            "invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff2",
		InviterId:           1,
		InviteRewardRule:    InviteRewardRuleContinuous,
		InviteRewardPercent: 5,
	})
	insertAffiliateRewardTopUp(t, "continuous-affiliate", 2, 20, PaymentProviderWaffo)

	require.NoError(t, RechargeWaffo("continuous-affiliate", "127.0.0.1"))

	inviter := getAffiliateRewardUser(t, 1)
	assert.Equal(t, 1000, inviter.AffQuota)
	assert.Equal(t, 1000, inviter.AffHistoryQuota)

	invitee := getAffiliateRewardUser(t, 2)
	assert.Equal(t, 20000, invitee.Quota)

	require.NoError(t, RechargeWaffo("continuous-affiliate", "127.0.0.1"))
	inviter = getAffiliateRewardUser(t, 1)
	assert.Equal(t, 1000, inviter.AffQuota)
	assert.Equal(t, 1000, inviter.AffHistoryQuota)

	var records []AffiliateRewardRecord
	require.NoError(t, DB.Where("invitee_id = ?", 2).Find(&records).Error)
	require.Len(t, records, 1)
	assert.Equal(t, 1, records[0].InviterId)
	assert.Equal(t, InviteRewardRuleContinuous, records[0].InviteRewardRule)
	assert.Equal(t, 5, records[0].InviteRewardPercent)
	assert.Equal(t, 20000, records[0].TopUpQuota)
	assert.Equal(t, 1000, records[0].RewardQuota)
}

func TestManualCompleteTopUpAppliesFirstTopUpAffiliateRewardSnapshotOnlyOnce(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)
	withAffiliateRewardPercents(t, 5, 12)

	insertAffiliateRewardUser(t, &User{
		Id:       10,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff10",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  11,
		Username:            "invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff11",
		InviterId:           10,
		InviteRewardRule:    InviteRewardRuleFirstTopUp,
		InviteRewardPercent: 30,
	})
	insertAffiliateRewardTopUp(t, "first-topup-affiliate-1", 11, 20, PaymentProviderWaffo)
	insertAffiliateRewardTopUp(t, "first-topup-affiliate-2", 11, 30, PaymentProviderWaffo)

	require.NoError(t, ManualCompleteTopUp("first-topup-affiliate-1", "127.0.0.1"))

	inviter := getAffiliateRewardUser(t, 10)
	assert.Equal(t, 6000, inviter.AffQuota)
	assert.Equal(t, 6000, inviter.AffHistoryQuota)

	require.NoError(t, ManualCompleteTopUp("first-topup-affiliate-2", "127.0.0.1"))
	inviter = getAffiliateRewardUser(t, 10)
	assert.Equal(t, 6000, inviter.AffQuota)
	assert.Equal(t, 6000, inviter.AffHistoryQuota)

	var records []AffiliateRewardRecord
	require.NoError(t, DB.Where("invitee_id = ?", 11).Find(&records).Error)
	require.Len(t, records, 1)
	assert.Equal(t, InviteRewardRuleFirstTopUp, records[0].InviteRewardRule)
	assert.Equal(t, 30, records[0].InviteRewardPercent)
	assert.Equal(t, 20000, records[0].TopUpQuota)
	assert.Equal(t, 6000, records[0].RewardQuota)
}

func TestLegacyFirstTopUpAffiliateRewardKeepsTenPercent(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)
	withAffiliateRewardPercents(t, 5, 30)

	insertAffiliateRewardUser(t, &User{
		Id:       12,
		Username: "legacy-inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff12",
	})
	insertAffiliateRewardUser(t, &User{
		Id:               13,
		Username:         "legacy-invitee",
		Status:           common.UserStatusEnabled,
		AffCode:          "aff13",
		InviterId:        12,
		InviteRewardRule: "first_topup_10",
	})
	insertAffiliateRewardTopUp(t, "legacy-first-topup-affiliate", 13, 20, PaymentProviderWaffo)

	require.NoError(t, ManualCompleteTopUp("legacy-first-topup-affiliate", "127.0.0.1"))

	inviter := getAffiliateRewardUser(t, 12)
	assert.Equal(t, 2000, inviter.AffQuota)
	assert.Equal(t, 2000, inviter.AffHistoryQuota)

	var record AffiliateRewardRecord
	require.NoError(t, DB.Where("invitee_id = ?", 13).First(&record).Error)
	assert.Equal(t, InviteRewardRuleFirstTopUp, record.InviteRewardRule)
	assert.Equal(t, 10, record.InviteRewardPercent)
	assert.Equal(t, 2000, record.RewardQuota)
}

func TestRechargeEpayAppliesContinuousAffiliateRewardOnce(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)
	withAffiliateRewardPercents(t, 5, 30)

	insertAffiliateRewardUser(t, &User{
		Id:       20,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff20",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  21,
		Username:            "invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff21",
		InviterId:           20,
		InviteRewardRule:    InviteRewardRuleContinuous,
		InviteRewardPercent: 5,
	})
	insertAffiliateRewardTopUp(t, "epay-continuous-affiliate", 21, 20, PaymentProviderEpay)

	require.NoError(t, RechargeEpay("epay-continuous-affiliate", "wxpay", "127.0.0.1"))

	inviter := getAffiliateRewardUser(t, 20)
	assert.Equal(t, 1000, inviter.AffQuota)
	assert.Equal(t, 1000, inviter.AffHistoryQuota)

	invitee := getAffiliateRewardUser(t, 21)
	assert.Equal(t, 20000, invitee.Quota)

	topUp := GetTopUpByTradeNo("epay-continuous-affiliate")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	assert.Equal(t, "wxpay", topUp.PaymentMethod)

	require.NoError(t, RechargeEpay("epay-continuous-affiliate", "alipay", "127.0.0.1"))
	inviter = getAffiliateRewardUser(t, 20)
	assert.Equal(t, 1000, inviter.AffQuota)
	assert.Equal(t, 1000, inviter.AffHistoryQuota)
}

func TestFirstTopUpAffiliateRewardIgnoresSubscriptionTopUpRecords(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)
	withAffiliateRewardPercents(t, 5, 30)

	insertAffiliateRewardUser(t, &User{
		Id:       30,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff30",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  31,
		Username:            "invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff31",
		InviterId:           30,
		InviteRewardRule:    InviteRewardRuleFirstTopUp,
		InviteRewardPercent: 30,
	})
	require.NoError(t, DB.Create(&TopUp{
		UserId:          31,
		Amount:          0,
		Money:           9.99,
		TradeNo:         "subscription-history-record",
		PaymentMethod:   PaymentProviderEpay,
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Unix(),
		CompleteTime:    time.Now().Unix(),
	}).Error)
	insertAffiliateRewardTopUp(t, "first-topup-after-subscription", 31, 20, PaymentProviderWaffo)

	require.NoError(t, RechargeWaffo("first-topup-after-subscription", "127.0.0.1"))

	inviter := getAffiliateRewardUser(t, 30)
	assert.Equal(t, 6000, inviter.AffQuota)
	assert.Equal(t, 6000, inviter.AffHistoryQuota)
}
