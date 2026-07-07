package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestRechargeEpaySaturatesOversizedQuotaAndAffiliateReward(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1)
	withAffiliateRewardPercents(t, 100, 100)

	insertAffiliateRewardUser(t, &User{
		Id:       22,
		Username: "oversized-inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff22",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  23,
		Username:            "oversized-invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff23",
		InviterId:           22,
		InviteRewardRule:    InviteRewardRuleContinuous,
		InviteRewardPercent: 100,
	})
	insertAffiliateRewardTopUp(t, "epay-oversized-affiliate", 23, int64(common.MaxQuota)+1, PaymentProviderEpay)

	require.NoError(t, RechargeEpay("epay-oversized-affiliate", "wxpay", "127.0.0.1"))

	invitee := getAffiliateRewardUser(t, 23)
	assert.Equal(t, common.MaxQuota, invitee.Quota)

	inviter := getAffiliateRewardUser(t, 22)
	assert.Equal(t, common.MaxQuota, inviter.AffQuota)
	assert.Equal(t, common.MaxQuota, inviter.AffHistoryQuota)

	var record AffiliateRewardRecord
	require.NoError(t, DB.Where("invitee_id = ?", 23).First(&record).Error)
	assert.Equal(t, common.MaxQuota, record.TopUpQuota)
	assert.Equal(t, common.MaxQuota, record.RewardQuota)
}

func TestAffiliateTopUpRewardSaturatesOversizedDirectQuota(t *testing.T) {
	truncateTables(t)
	withAffiliateRewardPercents(t, 100, 100)

	insertAffiliateRewardUser(t, &User{
		Id:       24,
		Username: "direct-oversized-inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff24",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                  25,
		Username:            "direct-oversized-invitee",
		Status:              common.UserStatusEnabled,
		AffCode:             "aff25",
		InviterId:           24,
		InviteRewardRule:    InviteRewardRuleContinuous,
		InviteRewardPercent: 100,
	})
	topUp := &TopUp{
		Id:              251,
		UserId:          25,
		Amount:          int64(common.MaxQuota) + 1,
		Money:           float64(common.MaxQuota) + 1,
		TradeNo:         "direct-oversized-affiliate",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(topUp).Error)

	var reward int
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		_, reward, err = applyAffiliateTopUpRewardTx(tx, topUp, common.MaxQuota+1)
		return err
	}))

	assert.Equal(t, common.MaxQuota, reward)

	inviter := getAffiliateRewardUser(t, 24)
	assert.Equal(t, common.MaxQuota, inviter.AffQuota)
	assert.Equal(t, common.MaxQuota, inviter.AffHistoryQuota)

	var record AffiliateRewardRecord
	require.NoError(t, DB.Where("top_up_id = ?", 251).First(&record).Error)
	assert.Equal(t, common.MaxQuota, record.TopUpQuota)
	assert.Equal(t, common.MaxQuota, record.RewardQuota)
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

func TestInviteLinkBatchTopUpRewardCreatesPendingFirstAndContinuousRecords(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)

	insertAffiliateRewardUser(t, &User{
		Id:       50,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff50",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            51,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff51",
		InviterId:                     50,
		InviteLinkBatchId:             70,
		InviteFirstTopupRewardPercent: 35,
		InviteContinuousRewardPercent: 7,
		InviteBoundAt:                 1_800_000_000,
	})
	firstTopUp := &TopUp{
		Id:              701,
		UserId:          51,
		Amount:          10,
		Money:           10,
		TradeNo:         "batch-first-topup",
		PaymentProvider: PaymentProviderWaffo,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(firstTopUp).Error)

	before := common.GetTimestamp()
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, firstTopUp, 10_000)
		return err
	}))

	inviter := getAffiliateRewardUser(t, 50)
	assert.Equal(t, 0, inviter.AffQuota)
	assert.Equal(t, 0, inviter.AffHistoryQuota)

	var firstRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 701}).First(&firstRecord).Error)
	assert.Equal(t, AffiliateRewardStatusPending, firstRecord.Status)
	assert.Equal(t, InviteRewardRuleFirstTopUp, firstRecord.InviteRewardRule)
	assert.Equal(t, 35, firstRecord.InviteRewardPercent)
	assert.Equal(t, 10_000, firstRecord.TopUpQuota)
	assert.Equal(t, 3_500, firstRecord.RewardQuota)
	assert.GreaterOrEqual(t, firstRecord.AvailableAt, before+AffiliateRewardWaitSeconds)

	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", 701).Updates(map[string]interface{}{
		"status":        common.TopUpStatusSuccess,
		"complete_time": common.GetTimestamp(),
	}).Error)
	secondTopUp := &TopUp{
		Id:              702,
		UserId:          51,
		Amount:          20,
		Money:           20,
		TradeNo:         "batch-second-topup",
		PaymentProvider: PaymentProviderWaffo,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(secondTopUp).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, secondTopUp, 20_000)
		return err
	}))

	var secondRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 702}).First(&secondRecord).Error)
	assert.Equal(t, AffiliateRewardStatusPending, secondRecord.Status)
	assert.Equal(t, InviteRewardRuleContinuous, secondRecord.InviteRewardRule)
	assert.Equal(t, 7, secondRecord.InviteRewardPercent)
	assert.Equal(t, 1_400, secondRecord.RewardQuota)
}

func TestInviteLinkBatchTopUpRewardStacksMultipleActivityRules(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)

	insertAffiliateRewardUser(t, &User{
		Id:       56,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff56",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            57,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff57",
		InviterId:                     56,
		InviteLinkBatchId:             90,
		InviteFirstTopupRewardPercent: 30,
		InviteContinuousRewardPercent: 5,
		InviteRewardRulesSnapshot: InviteRewardActivities{
			{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 20},
			{ActivityDetail: "VIP first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 10},
			{ActivityDetail: "Ongoing partner", Type: InviteRewardRuleContinuous, Percent: 5},
			{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
		},
		InviteBoundAt: 1_800_000_000,
	})
	require.NoError(t, DB.Create(&[]TopUp{
		{Id: 721, UserId: 57, Amount: 10, Money: 10, TradeNo: "stacked-first", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
		{Id: 722, UserId: 57, Amount: 20, Money: 20, TradeNo: "stacked-continuous", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
	}).Error)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 721, UserId: 57}, 10_000)
		return err
	}))
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", 721).Updates(map[string]interface{}{
		"status":        common.TopUpStatusSuccess,
		"complete_time": common.GetTimestamp(),
	}).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 722, UserId: 57}, 20_000)
		return err
	}))

	var firstRecords []AffiliateRewardRecord
	require.NoError(t, DB.Where("top_up_id = ?", 721).Order("id asc").Find(&firstRecords).Error)
	require.Len(t, firstRecords, 2)
	assert.Equal(t, "Launch first top-up", firstRecords[0].ActivityDetail)
	assert.Equal(t, InviteRewardRuleFirstTopUp, firstRecords[0].InviteRewardRule)
	assert.Equal(t, 20, firstRecords[0].InviteRewardPercent)
	assert.Equal(t, 2_000, firstRecords[0].RewardQuota)
	assert.Equal(t, "VIP first top-up", firstRecords[1].ActivityDetail)
	assert.Equal(t, InviteRewardRuleFirstTopUp, firstRecords[1].InviteRewardRule)
	assert.Equal(t, 10, firstRecords[1].InviteRewardPercent)
	assert.Equal(t, 1_000, firstRecords[1].RewardQuota)

	var continuousRecords []AffiliateRewardRecord
	require.NoError(t, DB.Where("top_up_id = ?", 722).Find(&continuousRecords).Error)
	require.Len(t, continuousRecords, 1)
	assert.Equal(t, "Ongoing partner", continuousRecords[0].ActivityDetail)
	assert.Equal(t, InviteRewardRuleContinuous, continuousRecords[0].InviteRewardRule)
	assert.Equal(t, 5, continuousRecords[0].InviteRewardPercent)
	assert.Equal(t, 1_000, continuousRecords[0].RewardQuota)

	var initialRewardRecords int64
	require.NoError(t, DB.Model(&AffiliateRewardRecord{}).Where("invite_reward_rule = ?", InviteRewardRuleInitialQuota).Count(&initialRewardRecords).Error)
	assert.Equal(t, int64(0), initialRewardRecords)
}

func TestInviteLinkBatchTopUpRewardUsesContinuousRulesForFirstTopUpWhenNoFirstRules(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)

	insertAffiliateRewardUser(t, &User{
		Id:       58,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff58",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            59,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff59",
		InviterId:                     58,
		InviteLinkBatchId:             91,
		InviteFirstTopupRewardPercent: 5,
		InviteContinuousRewardPercent: 5,
		InviteRewardRulesSnapshot: InviteRewardActivities{
			{ActivityDetail: "Ongoing only", Type: InviteRewardRuleContinuous, Percent: 5},
		},
		InviteBoundAt: 1_800_000_000,
	})
	require.NoError(t, DB.Create(&[]TopUp{
		{Id: 723, UserId: 59, Amount: 10, Money: 10, TradeNo: "continuous-only-first", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
		{Id: 724, UserId: 59, Amount: 20, Money: 20, TradeNo: "continuous-only-second", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
	}).Error)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 723, UserId: 59}, 10_000)
		return err
	}))
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", 723).Updates(map[string]interface{}{
		"status":        common.TopUpStatusSuccess,
		"complete_time": common.GetTimestamp(),
	}).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 724, UserId: 59}, 20_000)
		return err
	}))

	var firstRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 723}).First(&firstRecord).Error)
	assert.Equal(t, InviteRewardRuleContinuous, firstRecord.InviteRewardRule)
	assert.Equal(t, 5, firstRecord.InviteRewardPercent)
	assert.Equal(t, 500, firstRecord.RewardQuota)

	var secondRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 724}).First(&secondRecord).Error)
	assert.Equal(t, InviteRewardRuleContinuous, secondRecord.InviteRewardRule)
	assert.Equal(t, 5, secondRecord.InviteRewardPercent)
	assert.Equal(t, 1_000, secondRecord.RewardQuota)
}

func TestInviteLinkBatchTopUpRewardDoesNotCreateContinuousRewardWhenOnlyFirstRulesExist(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)

	insertAffiliateRewardUser(t, &User{
		Id:       60,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff60",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            61,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff61",
		InviterId:                     60,
		InviteLinkBatchId:             92,
		InviteFirstTopupRewardPercent: 30,
		InviteContinuousRewardPercent: 0,
		InviteRewardRulesSnapshot: InviteRewardActivities{
			{ActivityDetail: "First only", Type: InviteRewardRuleFirstTopUp, Percent: 30},
		},
		InviteBoundAt: 1_800_000_000,
	})
	require.NoError(t, DB.Create(&[]TopUp{
		{Id: 725, UserId: 61, Amount: 10, Money: 10, TradeNo: "first-only-first", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
		{Id: 726, UserId: 61, Amount: 20, Money: 20, TradeNo: "first-only-second", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
	}).Error)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 725, UserId: 61}, 10_000)
		return err
	}))
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", 725).Updates(map[string]interface{}{
		"status":        common.TopUpStatusSuccess,
		"complete_time": common.GetTimestamp(),
	}).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, &TopUp{Id: 726, UserId: 61}, 20_000)
		return err
	}))

	var firstRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 725}).First(&firstRecord).Error)
	assert.Equal(t, InviteRewardRuleFirstTopUp, firstRecord.InviteRewardRule)
	assert.Equal(t, 30, firstRecord.InviteRewardPercent)
	assert.Equal(t, 3_000, firstRecord.RewardQuota)

	var secondCount int64
	require.NoError(t, DB.Model(&AffiliateRewardRecord{}).Where("top_up_id = ?", 726).Count(&secondCount).Error)
	assert.Equal(t, int64(0), secondCount)
}

func TestInviteLinkBatchTopUpRewardUsesCompletionTimeForAvailability(t *testing.T) {
	truncateTables(t)

	insertAffiliateRewardUser(t, &User{
		Id:       52,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff52",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            53,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff53",
		InviterId:                     52,
		InviteLinkBatchId:             72,
		InviteFirstTopupRewardPercent: 35,
		InviteContinuousRewardPercent: 7,
		InviteBoundAt:                 1_800_000_000,
	})

	completedAt := int64(1_900_000_000)
	topUp := &TopUp{
		Id:           703,
		UserId:       53,
		Amount:       10,
		Status:       common.TopUpStatusPending,
		CompleteTime: completedAt,
	}
	require.NoError(t, DB.Create(topUp).Error)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, topUp, 10_000)
		return err
	}))

	var record AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 703}).First(&record).Error)
	assert.Equal(t, completedAt+AffiliateRewardWaitSeconds, record.AvailableAt)
}

func TestInviteLinkBatchFirstTopUpRewardIsClaimedOnlyOnceBeforeTopUpStatusChanges(t *testing.T) {
	truncateTables(t)
	insertAffiliateRewardUser(t, &User{
		Id:       54,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff54",
	})
	insertAffiliateRewardUser(t, &User{
		Id:                            55,
		Username:                      "invitee",
		Status:                        common.UserStatusEnabled,
		AffCode:                       "aff55",
		InviterId:                     54,
		InviteLinkBatchId:             88,
		InviteFirstTopupRewardPercent: 35,
		InviteContinuousRewardPercent: 7,
		InviteBoundAt:                 1_800_000_000,
	})
	require.NoError(t, DB.Create(&[]TopUp{
		{Id: 711, UserId: 55, Amount: 10, Money: 10, TradeNo: "batch-first-claim-1", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
		{Id: 712, UserId: 55, Amount: 20, Money: 20, TradeNo: "batch-first-claim-2", PaymentProvider: PaymentProviderWaffo, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()},
	}).Error)

	firstTopUp := &TopUp{Id: 711, UserId: 55}
	secondTopUp := &TopUp{Id: 712, UserId: 55}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, firstTopUp, 10_000)
		return err
	}))
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := applyAffiliateTopUpRewardTx(tx, secondTopUp, 20_000)
		return err
	}))

	var firstRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 711}).First(&firstRecord).Error)
	assert.Equal(t, InviteRewardRuleFirstTopUp, firstRecord.InviteRewardRule)
	assert.Equal(t, 3_500, firstRecord.RewardQuota)

	var secondRecord AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 712}).First(&secondRecord).Error)
	assert.Equal(t, InviteRewardRuleContinuous, secondRecord.InviteRewardRule)
	assert.Equal(t, 1_400, secondRecord.RewardQuota)
}

func TestTransferAffQuotaSettlesAvailableRewardsBeforeTransfer(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 1)
	now := common.GetTimestamp()

	insertAffiliateRewardUser(t, &User{
		Id:       80,
		Username: "inviter",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff80",
	})
	insertAffiliateRewardUser(t, &User{
		Id:        81,
		Username:  "invitee",
		Status:    common.UserStatusEnabled,
		AffCode:   "aff81",
		InviterId: 80,
	})
	require.NoError(t, DB.Create(&[]AffiliateRewardRecord{
		{
			InviterId:           80,
			InviteeId:           81,
			TopUpId:             801,
			InviteLinkBatchId:   1,
			InviteRewardRule:    InviteRewardRuleFirstTopUp,
			InviteRewardPercent: 30,
			TopUpQuota:          1_000,
			RewardQuota:         300,
			Status:              AffiliateRewardStatusPending,
			AvailableAt:         now + 60,
			CreatedAt:           now,
		},
		{
			InviterId:           80,
			InviteeId:           81,
			TopUpId:             802,
			InviteLinkBatchId:   1,
			InviteRewardRule:    InviteRewardRuleContinuous,
			InviteRewardPercent: 10,
			TopUpQuota:          2_000,
			RewardQuota:         200,
			Status:              AffiliateRewardStatusPending,
			AvailableAt:         now - 1,
			CreatedAt:           now,
		},
	}).Error)

	inviter := getAffiliateRewardUser(t, 80)
	require.NoError(t, inviter.TransferAffQuotaToQuota(200))

	inviter = getAffiliateRewardUser(t, 80)
	assert.Equal(t, 0, inviter.AffQuota)
	assert.Equal(t, 200, inviter.AffHistoryQuota)
	assert.Equal(t, 200, inviter.Quota)

	var future AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 801}).First(&future).Error)
	assert.Equal(t, AffiliateRewardStatusPending, future.Status)
	assert.Equal(t, 0, future.TransferredQuota)

	var available AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 802}).First(&available).Error)
	assert.Equal(t, AffiliateRewardStatusTransferred, available.Status)
	assert.Equal(t, 200, available.TransferredQuota)
	assert.NotZero(t, available.TransferredAt)

	err := inviter.TransferAffQuotaToQuota(1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "邀请额度不足")
}

func TestTransferAffQuotaMarksLegacyEmptyStatusRewardsAsTransferred(t *testing.T) {
	truncateTables(t)
	withQuotaPerUnitForAffiliateTest(t, 300)
	insertAffiliateRewardUser(t, &User{
		Id:              90,
		Username:        "inviter",
		Status:          common.UserStatusEnabled,
		AffCode:         "aff90",
		AffQuota:        300,
		AffHistoryQuota: 300,
	})
	insertAffiliateRewardUser(t, &User{
		Id:        91,
		Username:  "invitee",
		Status:    common.UserStatusEnabled,
		AffCode:   "aff91",
		InviterId: 90,
	})
	require.NoError(t, DB.Create(&AffiliateRewardRecord{
		InviterId:   90,
		InviteeId:   91,
		TopUpId:     901,
		RewardQuota: 300,
		Status:      "",
		CreatedAt:   common.GetTimestamp(),
	}).Error)

	inviter := getAffiliateRewardUser(t, 90)
	err := inviter.TransferAffQuotaToQuota(300)

	require.NoError(t, err)
	var record AffiliateRewardRecord
	require.NoError(t, DB.Where(&AffiliateRewardRecord{TopUpId: 901}).First(&record).Error)
	assert.Equal(t, AffiliateRewardStatusTransferred, record.Status)
	assert.Equal(t, 300, record.TransferredQuota)
}
