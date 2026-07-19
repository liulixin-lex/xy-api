package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifiedEpayCallbacksReviewUnsafeLegacyTopUpAndSettleSafeSubscription(t *testing.T) {
	hadSubscriptionPlanTable := model.DB.Migrator().HasTable(&model.SubscriptionPlan{})
	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.PaymentOrder{}, &model.PaymentEvent{}, &model.PaymentLedgerEntry{},
		&model.PaymentDebt{}, &model.PaymentCustomerBinding{}, &model.AffiliateRewardRecord{},
		&model.SubscriptionPlan{}, &model.SubscriptionOrder{},
	))
	originalID, originalKey := operation_setting.EpayId, operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalPreviousValidBefore := operation_setting.EpayPreviousValidBefore
	originalPreviousExpiresAt := operation_setting.EpayPreviousExpiresAt
	operation_setting.EpayId = "legacy_upgrade_merchant"
	operation_setting.EpayKey = "legacy_upgrade_signing_secret"
	operation_setting.EpayCredentialGeneration = 1
	operation_setting.EpayPreviousCredentialGeneration = 0
	operation_setting.EpayPreviousValidBefore = 0
	operation_setting.EpayPreviousExpiresAt = 0
	t.Cleanup(func() {
		operation_setting.EpayId, operation_setting.EpayKey = originalID, originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore = originalPreviousValidBefore
		operation_setting.EpayPreviousExpiresAt = originalPreviousExpiresAt
		model.DB.Where("trade_no IN ?", []string{"LEGACY_SIGNED_TOPUP", "LEGACY_SIGNED_SUBSCRIPTION"}).Delete(&model.TopUp{})
		model.DB.Where("trade_no = ?", "LEGACY_SIGNED_SUBSCRIPTION").Delete(&model.SubscriptionOrder{})
		model.DB.Where("trade_no IN ?", []string{"LEGACY_SIGNED_TOPUP", "LEGACY_SIGNED_SUBSCRIPTION"}).Delete(&model.PaymentOrder{})
		model.DB.Where("trade_no IN ?", []string{"LEGACY_SIGNED_TOPUP", "LEGACY_SIGNED_SUBSCRIPTION"}).Delete(&model.PaymentEvent{})
		model.DB.Where("user_id IN ?", []int{998821, 998822}).Delete(&model.UserSubscription{})
		model.DB.Where("id IN ?", []int{998821, 998822}).Delete(&model.User{})
		model.DB.Where("id = ?", 998831).Delete(&model.SubscriptionPlan{})
		if !hadSubscriptionPlanTable {
			_ = model.DB.Migrator().DropTable(&model.SubscriptionPlan{})
		}
	})
	for key, value := range map[string]string{
		model.PaymentConfigurationVersionOptionKey: "1", "EpayCredentialGeneration": "1",
		"EpayPreviousCredentialGeneration": "0", "EpayPreviousValidBefore": "0", "EpayPreviousExpiresAt": "0",
	} {
		var option model.Option
		require.NoError(t, model.DB.Where("`key` = ?", key).
			Assign(model.Option{Value: value}).FirstOrCreate(&option, model.Option{Key: key}).Error)
	}
	for _, userID := range []int{998821, 998822} {
		require.NoError(t, model.DB.Create(&model.User{
			Id: userID, Username: fmt.Sprintf("legacy_signed_user_%d", userID),
			AffCode: fmt.Sprintf("legacy_signed_aff_%d", userID), Status: common.UserStatusEnabled,
		}).Error)
	}
	now := common.GetTimestamp()
	require.NoError(t, model.DB.Create(&model.TopUp{
		UserId: 998821, Amount: 2, Money: 2.5, TradeNo: "LEGACY_SIGNED_TOPUP",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: now - 100,
	}).Error)
	plan := &model.SubscriptionPlan{
		Id: 998831, Title: "Legacy signed plan", PriceAmount: 12.34, Currency: "USD",
		DurationUnit: model.SubscriptionDurationDay, DurationValue: 30, Enabled: true,
		TotalAmount: 4321, QuotaResetPeriod: model.SubscriptionResetNever,
	}
	require.NoError(t, model.DB.Create(plan).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		UpdateColumns(map[string]interface{}{"created_at": now - 300, "updated_at": now - 200}).Error)
	require.NoError(t, model.DB.Create(&model.SubscriptionOrder{
		UserId: 998822, PlanId: plan.Id, Money: 12.34, TradeNo: "LEGACY_SIGNED_SUBSCRIPTION",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: now - 100,
	}).Error)

	provider, err := GetPaymentProvider(model.PaymentProviderEpay)
	require.NoError(t, err)
	for _, payment := range []struct {
		tradeNo string
		money   string
		kind    string
	}{
		{tradeNo: "LEGACY_SIGNED_TOPUP", money: "2.50", kind: model.PaymentOrderKindTopUp},
		{tradeNo: "LEGACY_SIGNED_SUBSCRIPTION", money: "12.34", kind: model.PaymentOrderKindSubscription},
	} {
		params := map[string]string{
			"pid": operation_setting.EpayId, "trade_no": "gateway_" + payment.tradeNo,
			"out_trade_no": payment.tradeNo, "type": "alipay", "name": "legacy upgrade",
			"money": payment.money, "trade_status": epay.StatusTradeSuccess,
		}
		epay.GenerateParams(params, operation_setting.EpayKey)
		form := url.Values{}
		for key, value := range params {
			form.Set(key, value)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		event, err := provider.VerifyWebhook(request)
		require.NoError(t, err)
		assert.True(t, event.Paid)
		assert.Equal(t, payment.tradeNo, event.TradeNo)
		assert.Equal(t, "alipay", event.PaymentMethod)

		settlement, err := ProcessNormalizedPaymentEvent(event)
		if payment.kind == model.PaymentOrderKindTopUp {
			require.ErrorIs(t, err, model.ErrPaymentManualReview)
			require.NotNil(t, settlement)
			assert.True(t, settlement.ManualReview)
			assert.Nil(t, settlement.Order)
			continue
		}
		require.NoError(t, err)
		require.NotNil(t, settlement.Order)
		assert.Equal(t, payment.kind, settlement.Order.OrderKind)
		assert.Equal(t, model.PaymentOrderStatusFulfilled, settlement.Order.Status)
	}

	var upgradedTopUp model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", "LEGACY_SIGNED_TOPUP").First(&upgradedTopUp).Error)
	assert.Empty(t, upgradedTopUp.PaymentProvider)
	assert.Nil(t, upgradedTopUp.PaymentOrderId)
	var topUpUser model.User
	require.NoError(t, model.DB.First(&topUpUser, 998821).Error)
	assert.Zero(t, topUpUser.Quota)
	var topUpEvent model.PaymentEvent
	require.NoError(t, model.DB.Where("provider = ? AND trade_no = ?", model.PaymentProviderEpay,
		"LEGACY_SIGNED_TOPUP").First(&topUpEvent).Error)
	assert.Equal(t, model.PaymentEventStatusManualReview, topUpEvent.Status)
	assert.Contains(t, topUpEvent.LastError, "legacy top-up quota snapshot is unavailable")
	var upgradedSubscription model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", "LEGACY_SIGNED_SUBSCRIPTION").First(&upgradedSubscription).Error)
	assert.Equal(t, model.PaymentProviderEpay, upgradedSubscription.PaymentProvider)
	assert.EqualValues(t, 1234, upgradedSubscription.ExpectedAmountMinor)
	assert.Equal(t, "CNY", upgradedSubscription.PaymentCurrency)
	assert.NotEmpty(t, upgradedSubscription.PlanSnapshot)
	require.NotNil(t, upgradedSubscription.PaymentOrderId)
	var entitlement model.UserSubscription
	require.NoError(t, model.DB.Where("payment_order_id = ?", *upgradedSubscription.PaymentOrderId).First(&entitlement).Error)
	assert.EqualValues(t, plan.TotalAmount, entitlement.AmountTotal)
}
