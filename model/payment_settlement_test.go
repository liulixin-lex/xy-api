package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedPaymentUser(t *testing.T, id int, quota int) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       id,
		Username: fmt.Sprintf("payment_user_%d", id),
		AffCode:  fmt.Sprintf("payment_aff_%d", id),
		Quota:    quota,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}).Error)
}

func seedStripeCustomerBinding(t *testing.T, userID int, customerID string) {
	t.Helper()
	require.NoError(t, DB.Create(&PaymentCustomerBinding{
		Provider: PaymentProviderStripe, CustomerKey: customerID, UserID: userID,
	}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("stripe_customer", customerID).Error)
}

func livePaymentModeForTest() *bool {
	livemode := true
	return &livemode
}

func createTopUpPaymentOrder(t *testing.T, userID int, provider string, method string, expectedMinor int64, creditQuota int64) *PaymentOrder {
	t.Helper()
	var providerLivemode *bool
	if provider == PaymentProviderStripe {
		livemode := true
		providerLivemode = &livemode
	}
	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	require.NoError(t, CreatePaymentQuote(&PaymentQuote{
		QuoteID:             quoteID,
		UserID:              userID,
		OrderKind:           PaymentOrderKindTopUp,
		Provider:            provider,
		PaymentMethod:       method,
		ProviderLivemode:    providerLivemode,
		RequestedAmount:     10,
		CreditQuota:         creditQuota,
		ExpectedAmountMinor: expectedMinor,
		Currency:            "USD",
		PricingSnapshot:     `{"version":1}`,
		ExpiresAt:           time.Now().Add(time.Hour).Unix(),
	}))
	order, err := CreatePaymentOrderFromQuote(userID, quoteID, "request_"+quoteID)
	require.NoError(t, err)
	return order
}

func createSubscriptionPaymentOrder(t *testing.T, userID int, plan *SubscriptionPlan, expectedMinor int64) *PaymentOrder {
	t.Helper()
	livemode := true
	snapshot, err := NewSubscriptionPlanSnapshot(plan)
	require.NoError(t, err)
	snapshotJSON, err := common.Marshal(snapshot)
	require.NoError(t, err)
	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	require.NoError(t, CreatePaymentQuote(&PaymentQuote{
		QuoteID:             quoteID,
		UserID:              userID,
		OrderKind:           PaymentOrderKindSubscription,
		Provider:            PaymentProviderStripe,
		PaymentMethod:       PaymentMethodStripe,
		ProviderLivemode:    &livemode,
		RequestedAmount:     int64(plan.Id),
		ExpectedAmountMinor: expectedMinor,
		Currency:            "USD",
		ProductSnapshot:     string(snapshotJSON),
		PricingSnapshot:     `{"version":1}`,
		ExpiresAt:           time.Now().Add(time.Hour).Unix(),
	}))
	order, err := CreatePaymentOrderFromQuote(userID, quoteID, "subscription_"+quoteID)
	require.NoError(t, err)
	return order
}

func paidPaymentEvent(order *PaymentOrder, eventKey string) PaymentEventInput {
	credentialGeneration := int64(0)
	var providerLivemode *bool
	if order.Provider == PaymentProviderEpay || order.Provider == PaymentProviderStripe || order.Provider == PaymentProviderXorPay {
		credentialGeneration = 1
	}
	if order.Provider == PaymentProviderStripe {
		livemode := true
		providerLivemode = &livemode
	}
	return PaymentEventInput{
		Provider:                     order.Provider,
		ProviderCredentialGeneration: credentialGeneration,
		ProviderLivemode:             providerLivemode,
		EventKey:                     eventKey,
		EventType:                    "paid",
		TradeNo:                      order.TradeNo,
		ProviderOrderKey:             order.Provider + ":order_123",
		ProviderPaymentKey:           order.Provider + ":payment_123",
		PaidAmountMinor:              order.ExpectedAmountMinor,
		Currency:                     order.Currency,
		PaymentMethod:                order.PaymentMethod,
		Paid:                         true,
		NormalizedPayload:            `{"status":"paid"}`,
	}
}

func setPaymentOrderLivemodeForTest(t *testing.T, order *PaymentOrder, livemode bool) {
	t.Helper()
	require.NotNil(t, order)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).Update("provider_livemode", livemode).Error)
	order.ProviderLivemode = &livemode
}

func TestStripeTestPaymentRequiresExplicitSandboxEnablement(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv(setting.StripeTestModeEnabledEnv, "")
		truncateTables(t)
		seedPaymentUser(t, 970040, 0)
		order := createTopUpPaymentOrder(t, 970040, PaymentProviderStripe, PaymentMethodStripe, 1000, 5000)
		setPaymentOrderLivemodeForTest(t, order, false)
		event := paidPaymentEvent(order, "stripe-test-mode-disabled-paid")
		testMode := false
		event.ProviderLivemode = &testMode

		result, err := ProcessPaymentEvent(event)
		require.ErrorIs(t, err, ErrPaymentManualReview)
		require.NotNil(t, result)
		assert.True(t, result.ManualReview)

		var user User
		require.NoError(t, DB.First(&user, order.UserID).Error)
		assert.Zero(t, user.Quota)
		var stored PaymentEvent
		require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, event.EventKey).First(&stored).Error)
		require.NotNil(t, stored.ProviderLivemode)
		assert.False(t, *stored.ProviderLivemode)
		assert.Equal(t, PaymentEventStatusManualReview, stored.Status)
	})

	t.Run("explicit isolated sandbox", func(t *testing.T) {
		t.Setenv(setting.StripeTestModeEnabledEnv, "true")
		truncateTables(t)
		seedPaymentUser(t, 970041, 0)
		order := createTopUpPaymentOrder(t, 970041, PaymentProviderStripe, PaymentMethodStripe, 1000, 5000)
		setPaymentOrderLivemodeForTest(t, order, false)
		event := paidPaymentEvent(order, "stripe-test-mode-enabled-paid")
		testMode := false
		event.ProviderLivemode = &testMode

		_, err := ProcessPaymentEvent(event)
		require.NoError(t, err)
		var user User
		require.NoError(t, DB.First(&user, order.UserID).Error)
		assert.Equal(t, 5000, user.Quota)
	})
}

func TestStripeTestModeDisableBlocksNewCreditsButAllowsReversal(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	truncateTables(t)
	seedPaymentUser(t, 970042, 0)
	order := createTopUpPaymentOrder(t, 970042, PaymentProviderStripe, PaymentMethodStripe, 1000, 5000)
	setPaymentOrderLivemodeForTest(t, order, false)
	testMode := false
	paid := paidPaymentEvent(order, "stripe-test-mode-paid-before-disable")
	paid.ProviderLivemode = &testMode
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: &testMode,
		EventKey: "stripe-test-mode-refund-after-disable", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Zero(t, user.Quota)
	var stored PaymentOrder
	require.NoError(t, DB.First(&stored, order.ID).Error)
	assert.Equal(t, PaymentOrderStatusRefunded, stored.Status)
}

func TestProviderIdentityAuthorityConflictCommitsManualReview(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	providerOrderKey := "authority-test:provider-order-shared"
	owner := &PaymentOrder{
		TradeNo: "PO_PROVIDER_IDENTITY_OWNER", UserID: 970090,
		OrderKind: PaymentOrderKindTopUp, Provider: "authority-test", PaymentMethod: "authority-method",
		RequestID: "provider_identity_owner", ProviderOrderKey: &providerOrderKey,
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	target := &PaymentOrder{
		TradeNo: "PO_PROVIDER_IDENTITY_TARGET", UserID: 970091,
		OrderKind: PaymentOrderKindTopUp, Provider: "authority-test", PaymentMethod: "authority-method",
		RequestID:           "provider_identity_target",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(owner).Error)
	require.NoError(t, DB.Create(target).Error)
	require.NoError(t, DB.Create(&TopUp{
		PaymentOrderId: &target.ID, UserId: target.UserID, Amount: 1, Money: 1,
		TradeNo: target.TradeNo, PaymentMethod: target.PaymentMethod, PaymentProvider: target.Provider,
		CreateTime: now, Status: common.TopUpStatusPending,
	}).Error)

	result, err := ProcessPaymentEvent(PaymentEventInput{
		Provider: "authority-test", EventKey: "provider-identity-authority-conflict", EventType: "paid",
		TradeNo: target.TradeNo, ProviderOrderKey: providerOrderKey,
		PaidAmountMinor: 100, Currency: "USD", PaymentMethod: target.PaymentMethod, Paid: true,
		NormalizedPayload: `{"paid":true,"identity":"shared"}`,
	})
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	assert.Contains(t, err.Error(), ErrPaymentProviderMismatch.Error())
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)

	require.NoError(t, DB.First(target, target.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, target.Status)
	assert.Nil(t, target.ProviderOrderKey)
	require.NoError(t, DB.First(owner, owner.ID).Error)
	require.NotNil(t, owner.ProviderOrderKey)
	assert.Equal(t, providerOrderKey, *owner.ProviderOrderKey)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", "authority-test", "provider-identity-authority-conflict").First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	assert.Equal(t, target.ID, event.PaymentOrderID)
}

func TestCreatePaymentOrderFromQuoteRetryReturnsOriginalOrder(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970000, 0)
	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	require.NoError(t, CreatePaymentQuote(&PaymentQuote{
		QuoteID: quoteID, UserID: 970000, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 10,
		CreditQuota: 5000, ExpectedAmountMinor: 7300, Currency: "CNY",
		PricingSnapshot: `{"version":1}`, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))

	first, err := CreatePaymentOrderFromQuote(970000, quoteID, "payment-order-idempotent-request")
	require.NoError(t, err)
	second, err := CreatePaymentOrderFromQuote(970000, quoteID, "payment-order-idempotent-request")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.TradeNo, second.TradeNo)
}

func TestCreatePaymentOrderFromQuoteRejectsRequestIDReuseAcrossContracts(t *testing.T) {
	t.Run("different amount", func(t *testing.T) {
		truncateTables(t)
		const userID = 970033
		seedPaymentUser(t, userID, 0)
		expiresAt := time.Now().Add(time.Hour).Unix()
		firstQuoteID, err := GeneratePaymentQuoteID()
		require.NoError(t, err)
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID: firstQuoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 10,
			CreditQuota: 5000, ExpectedAmountMinor: 7300, Currency: "CNY",
			PricingSnapshot: `{"version":1}`, ExpiresAt: expiresAt,
		}))
		first, err := CreatePaymentOrderFromQuote(userID, firstQuoteID, "shared-contract-request")
		require.NoError(t, err)
		assert.Equal(t, firstQuoteID, first.QuoteID)

		secondQuoteID, err := GeneratePaymentQuoteID()
		require.NoError(t, err)
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID: secondQuoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 11,
			CreditQuota: 5500, ExpectedAmountMinor: 8030, Currency: "CNY",
			PricingSnapshot: `{"version":1}`, ExpiresAt: expiresAt,
		}))
		_, err = CreatePaymentOrderFromQuote(userID, secondQuoteID, "shared-contract-request")
		assert.ErrorIs(t, err, ErrPaymentIdempotencyConflict)
		var secondQuote PaymentQuote
		require.NoError(t, DB.Where("quote_id = ?", secondQuoteID).First(&secondQuote).Error)
		assert.Zero(t, secondQuote.ConsumedAt)
	})

	t.Run("different provider", func(t *testing.T) {
		truncateTables(t)
		const userID = 970034
		seedPaymentUser(t, userID, 0)
		expiresAt := time.Now().Add(time.Hour).Unix()
		firstQuoteID, err := GeneratePaymentQuoteID()
		require.NoError(t, err)
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID: firstQuoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 10,
			CreditQuota: 5000, ExpectedAmountMinor: 7300, Currency: "CNY",
			PricingSnapshot: `{"version":1}`, ExpiresAt: expiresAt,
		}))
		_, err = CreatePaymentOrderFromQuote(userID, firstQuoteID, "shared-provider-request")
		require.NoError(t, err)

		secondQuoteID, err := GeneratePaymentQuoteID()
		require.NoError(t, err)
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID: secondQuoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayNative, RequestedAmount: 10,
			CreditQuota: 5000, ExpectedAmountMinor: 7300, Currency: "CNY",
			PricingSnapshot: `{"version":1}`, ExpiresAt: expiresAt,
		}))
		_, err = CreatePaymentOrderFromQuote(userID, secondQuoteID, "shared-provider-request")
		assert.ErrorIs(t, err, ErrPaymentIdempotencyConflict)
	})

	t.Run("different subscription product", func(t *testing.T) {
		truncateTables(t)
		const userID = 970035
		seedPaymentUser(t, userID, 0)
		plans := []*SubscriptionPlan{
			{Id: 980035, Title: "Plan A", PriceAmount: 10, Currency: "USD", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true, TotalAmount: 1000},
			{Id: 980036, Title: "Plan B", PriceAmount: 20, Currency: "USD", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true, TotalAmount: 2000},
		}
		for _, plan := range plans {
			require.NoError(t, DB.Create(plan).Error)
		}
		expiresAt := time.Now().Add(time.Hour).Unix()
		quoteIDs := make([]string, len(plans))
		for index, plan := range plans {
			snapshot, err := NewSubscriptionPlanSnapshot(plan)
			require.NoError(t, err)
			snapshotJSON, err := common.Marshal(snapshot)
			require.NoError(t, err)
			quoteIDs[index], err = GeneratePaymentQuoteID()
			require.NoError(t, err)
			require.NoError(t, CreatePaymentQuote(&PaymentQuote{
				QuoteID: quoteIDs[index], UserID: userID, OrderKind: PaymentOrderKindSubscription,
				Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, RequestedAmount: int64(plan.Id),
				ProviderLivemode:    livePaymentModeForTest(),
				ExpectedAmountMinor: int64(plan.PriceAmount * 100), Currency: "USD",
				ProductSnapshot: string(snapshotJSON), PricingSnapshot: `{"version":1}`, ExpiresAt: expiresAt,
			}))
		}
		_, err := CreatePaymentOrderFromQuote(userID, quoteIDs[0], "shared-subscription-request")
		require.NoError(t, err)
		_, err = CreatePaymentOrderFromQuote(userID, quoteIDs[1], "shared-subscription-request")
		assert.ErrorIs(t, err, ErrPaymentIdempotencyConflict)
	})
}

func TestProcessPaymentEventCreditsTopUpExactlyOnce(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970001, 100)
	order := createTopUpPaymentOrder(t, 970001, PaymentProviderEpay, "alipay", 7300, 5000)
	event := paidPaymentEvent(order, "epay-paid-once")

	result, err := ProcessPaymentEvent(event)
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	assert.Equal(t, 5000, result.QuotaDelta)

	result, err = ProcessPaymentEvent(event)
	require.NoError(t, err)
	assert.True(t, result.Duplicate)

	var user User
	require.NoError(t, DB.First(&user, 970001).Error)
	assert.Equal(t, 5100, user.Quota)

	completed, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusFulfilled, completed.Status)
	assert.EqualValues(t, 7300, completed.PaidAmountMinor)

	var topUp TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&topUp).Error)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)

	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryCredit).Count(&ledgerCount).Error)
	assert.EqualValues(t, 1, ledgerCount)
}

func TestRevokedCredentialGenerationCannotSettleOrderInsertedAfterRevocationScan(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Model(&Option{}).
		Where(fmt.Sprintf("%s = ?", optionKeyColumn()), "EpayCredentialGeneration").Update("value", "2").Error)
	require.NoError(t, DB.Model(&Option{}).
		Where(fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).Update("value", "2").Error)

	seedPaymentUser(t, 970032, 250)
	order := createTopUpPaymentOrder(t, 970032, PaymentProviderEpay, "alipay", 7300, 5000)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("provider_credential_generation", 1).Error)
	order.ProviderCredentialGeneration = 1
	paid := paidPaymentEvent(order, "epay-revoked-generation-after-scan")
	paid.ProviderOrderKey = "epay:revoked_generation_order"
	paid.ProviderPaymentKey = "epay:revoked_generation_payment"

	result, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	var user User
	require.NoError(t, DB.First(&user, 970032).Error)
	assert.Equal(t, 250, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Equal(t, "payment_credential_generation_revoked", stored.StatusReason)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, common.TopUpStatusManualReview, projection.Status)
	var credits int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryCredit).Count(&credits).Error)
	assert.Zero(t, credits)
}

func TestRevokedStripeWebhookGenerationCannotSettleOrderInsertedAfterRevocationScan(t *testing.T) {
	truncateTables(t)
	for key, value := range map[string]string{
		"StripeWebhookCredentialGeneration":         "3",
		"StripeWebhookPreviousCredentialGeneration": "0",
		"StripeWebhookPreviousValidBefore":          "0",
		"StripeWebhookSecretPreviousExpiresAt":      "0",
		PaymentConfigurationVersionOptionKey:        "2",
	} {
		require.NoError(t, DB.Model(&Option{}).
			Where(fmt.Sprintf("%s = ?", optionKeyColumn()), key).Update("value", value).Error)
	}

	seedPaymentUser(t, 970033, 250)
	order := createTopUpPaymentOrder(t, 970033, PaymentProviderStripe, PaymentMethodStripe, 1000, 5000)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("provider_credential_generation", 2).Error)
	order.ProviderCredentialGeneration = 2
	paid := paidPaymentEvent(order, "stripe-revoked-generation-after-scan")
	paid.ProviderCredentialGeneration = 2
	paid.ProviderOrderKey = "stripe:cs_revoked_generation_order"
	paid.ProviderPaymentKey = "stripe:pi_revoked_generation_payment"

	result, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	var user User
	require.NoError(t, DB.First(&user, 970033).Error)
	assert.Equal(t, 250, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Equal(t, "payment_credential_generation_revoked", stored.StatusReason)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, common.TopUpStatusManualReview, projection.Status)
	var credits int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryCredit).Count(&credits).Error)
	assert.Zero(t, credits)
}

func TestVerifiedLatePaidTopUpRecoversFailedOrExpiredProjection(t *testing.T) {
	t.Run("local expiry", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 970037, 0)
		order := createTopUpPaymentOrder(t, 970037, PaymentProviderEpay, "alipay", 7300, 5000)
		require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
			Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
		_, err := ExpirePaymentOrderIfDue(order.UserID, order.TradeNo)
		require.NoError(t, err)

		paid := paidPaymentEvent(order, "epay-late-paid-after-local-expiry")
		paid.ProviderOrderKey = "epay:late_expiry_order"
		paid.ProviderPaymentKey = "epay:late_expiry_payment"
		_, err = ProcessPaymentEvent(paid)
		require.NoError(t, err)

		var user User
		require.NoError(t, DB.First(&user, 970037).Error)
		assert.Equal(t, 5000, user.Quota)
		stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, lookupErr)
		assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
		var projection TopUp
		require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
		assert.Equal(t, common.TopUpStatusSuccess, projection.Status)
	})

	t.Run("provider failure followed by success", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 970038, 0)
		order := createTopUpPaymentOrder(t, 970038, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		authorizationDigest := strings.Repeat("a", 64)
		require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
			"start_flow": "qr", "start_payload": "encrypted-checkout-state",
			"browser_authorization_digest":     authorizationDigest,
			"browser_authorization_payload":    "encrypted-browser-authorization",
			"browser_authorization_expires_at": time.Now().Add(time.Minute).Unix(),
			"browser_authorized_at":            time.Now().Unix(),
		}).Error)
		failed := PaymentEventInput{
			Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
			EventKey: "stripe-async-payment-failed-first", EventType: "checkout.session.async_payment_failed",
			TradeNo: order.TradeNo, ProviderOrderKey: "stripe:cs_late_success",
			Currency: "USD", PaymentMethod: PaymentMethodStripe, Failed: true, PermanentFailure: true,
			NormalizedPayload: `{"payment_status":"unpaid"}`,
		}
		_, err := ProcessPaymentEvent(failed)
		require.NoError(t, err)
		failedOrder, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, lookupErr)
		assert.Equal(t, PaymentOrderStatusFailed, failedOrder.Status)
		assert.Empty(t, failedOrder.StartFlow)
		assert.Empty(t, failedOrder.StartPayload)
		assert.Nil(t, failedOrder.BrowserAuthorizationDigest)
		assert.Empty(t, failedOrder.BrowserAuthorizationPayload)
		assert.Zero(t, failedOrder.BrowserAuthorizationExpiresAt)
		assert.Zero(t, failedOrder.BrowserAuthorizedAt)

		paid := paidPaymentEvent(order, "stripe-async-payment-succeeded-late")
		paid.ProviderOrderKey = failed.ProviderOrderKey
		paid.ProviderPaymentKey = "stripe:pi_late_success"
		paid.NormalizedPayload = `{"payment_status":"paid"}`
		_, err = ProcessPaymentEvent(paid)
		require.NoError(t, err)

		var user User
		require.NoError(t, DB.First(&user, 970038).Error)
		assert.Equal(t, 1000, user.Quota)
		stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, lookupErr)
		assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
		var projection TopUp
		require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
		assert.Equal(t, common.TopUpStatusSuccess, projection.Status)
	})
}

func TestLegacyStripePreviousGenerationCannotAuthenticatePostRotationOrder(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), []string{
		"StripeWebhookCredentialGeneration", "StripeWebhookPreviousCredentialGeneration", "StripeWebhookPreviousValidBefore",
	}).Delete(&Option{}).Error)
	now := common.GetTimestamp()
	expiresAt := now + 3600
	require.NoError(t, DB.Model(&Option{}).
		Where(fmt.Sprintf("%s = ?", optionKeyColumn()), "StripeWebhookSecretPreviousExpiresAt").
		Update("value", strconv.FormatInt(expiresAt, 10)).Error)
	rotationBoundary := expiresAt - int64((24*time.Hour)/time.Second)

	var previousBeforeRotation bool
	var previousAfterRotation bool
	var currentAfterRotation bool
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		previousBeforeRotation, err = paymentCredentialGenerationAvailableTx(tx, PaymentProviderStripe, 1, rotationBoundary, now)
		if err != nil {
			return err
		}
		previousAfterRotation, err = paymentCredentialGenerationAvailableTx(tx, PaymentProviderStripe, 1, rotationBoundary+1, now)
		if err != nil {
			return err
		}
		currentAfterRotation, err = paymentCredentialGenerationAvailableTx(tx, PaymentProviderStripe, 2, rotationBoundary+1, now)
		return err
	}))
	assert.True(t, previousBeforeRotation)
	assert.False(t, previousAfterRotation)
	assert.True(t, currentAfterRotation)
}

func TestStripeSubscriptionBindsCustomerAndRejectsConflictingIdentity(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970017, 0)
	plan := &SubscriptionPlan{
		Id: 980017, Title: "Stripe customer plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true, TotalAmount: 1000,
	}
	require.NoError(t, DB.Create(plan).Error)
	firstOrder := createSubscriptionPaymentOrder(t, 970017, plan, 1000)
	firstEvent := paidPaymentEvent(firstOrder, "stripe-subscription-customer-first")
	firstEvent.CustomerID = "cus_subscription_bound"

	_, err := ProcessPaymentEvent(firstEvent)
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970017).Error)
	assert.Equal(t, "cus_subscription_bound", user.StripeCustomer)

	secondOrder := createSubscriptionPaymentOrder(t, 970017, plan, 1000)
	secondEvent := paidPaymentEvent(secondOrder, "stripe-subscription-customer-conflict")
	secondEvent.ProviderOrderKey = "stripe:order_456"
	secondEvent.ProviderPaymentKey = "stripe:payment_456"
	secondEvent.CustomerID = "cus_conflicting_identity"
	secondEvent.NormalizedPayload = `{"status":"paid","customer":"cus_conflicting_identity"}`

	_, err = ProcessPaymentEvent(secondEvent)
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	storedOrder, err := GetPaymentOrderByTradeNo(secondOrder.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusManualReview, storedOrder.Status)
	require.NoError(t, DB.First(&user, 970017).Error)
	assert.Equal(t, "cus_subscription_bound", user.StripeCustomer)
}

func TestProcessPaymentEventMovesAmountMismatchToManualReview(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970002, 100)
	order := createTopUpPaymentOrder(t, 970002, PaymentProviderEpay, "alipay", 7300, 5000)
	event := paidPaymentEvent(order, "epay-amount-mismatch")
	event.PaidAmountMinor = 7299
	event.NormalizedPayload = `{"amount":7299}`

	result, err := ProcessPaymentEvent(event)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPaymentManualReview))
	assert.True(t, result.ManualReview)

	var user User
	require.NoError(t, DB.First(&user, 970002).Error)
	assert.Equal(t, 100, user.Quota)

	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)

	var topUp TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&topUp).Error)
	assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
}

func TestReversalAfterPaidContractMismatchNeverTouchesExistingUserQuota(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970092, 500)
	order := createTopUpPaymentOrder(t, 970092, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-ungranted-paid-mismatch")
	paid.PaidAmountMinor = 999
	paid.NormalizedPayload = `{"amount_total":999}`

	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-ungranted-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.ErrorIs(t, err, ErrPaymentManualReview)

	var user User
	require.NoError(t, DB.First(&user, 970092).Error)
	assert.Equal(t, 500, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.False(t, user.PaymentFrozen)
	var debtCount int64
	require.NoError(t, DB.Model(&PaymentDebt{}).Where("payment_order_id = ?", order.ID).Count(&debtCount).Error)
	assert.Zero(t, debtCount)
	var reversalLedgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type IN ?", order.ID, []string{
			PaymentLedgerEntryRefundReversal, PaymentLedgerEntryDisputeReversal, PaymentLedgerEntryReversalRestored,
		}).Count(&reversalLedgerCount).Error)
	assert.Zero(t, reversalLedgerCount)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Zero(t, stored.ReversedQuota)
	assert.Zero(t, stored.ReversedAmountMinor)
}

func TestRepeatedRefundBeforePaidNeverCreatesAReversal(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970093, 700)
	order := createTopUpPaymentOrder(t, 970093, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	providerPaymentKey := "stripe:pi_refund_before_paid"
	for index := 1; index <= 2; index++ {
		refund := PaymentEventInput{
			Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
			EventKey: "stripe-refund-before-paid-" + strconv.Itoa(index), EventType: "charge.refunded",
			TradeNo: order.TradeNo, ProviderPaymentKey: providerPaymentKey,
			RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
			NormalizedPayload: common.GetJsonString(map[string]interface{}{"amount_refunded": 1000, "attempt": index}),
		}
		_, err := ProcessPaymentEvent(refund)
		require.ErrorIs(t, err, ErrPaymentManualReview)
	}

	var user User
	require.NoError(t, DB.First(&user, 970093).Error)
	assert.Equal(t, 700, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.False(t, user.PaymentFrozen)
	var debtCount int64
	require.NoError(t, DB.Model(&PaymentDebt{}).Where("payment_order_id = ?", order.ID).Count(&debtCount).Error)
	assert.Zero(t, debtCount)
	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
}

func TestFulfillmentConflictRollsBackAffiliateRewardBeforeManualReview(t *testing.T) {
	truncateTables(t)
	withAffiliateRewardPercents(t, 10, 10)
	insertAffiliateRewardUser(t, &User{
		Id: 970094, Username: "settlement_savepoint_inviter", AffCode: "settlement_savepoint_inviter",
		Status: common.UserStatusEnabled,
	})
	insertAffiliateRewardUser(t, &User{
		Id: 970095, Username: "settlement_savepoint_buyer", AffCode: "settlement_savepoint_buyer",
		Status: common.UserStatusEnabled, Quota: common.MaxQuota - 5,
		InviterId: 970094, InviteRewardRule: InviteRewardRuleContinuous, InviteRewardPercent: 10,
	})
	order := createTopUpPaymentOrder(t, 970095, PaymentProviderStripe, PaymentMethodStripe, 1000, 10)
	_, err := ProcessPaymentEvent(paidPaymentEvent(order, "stripe-fulfillment-savepoint-overflow"))
	require.ErrorIs(t, err, ErrPaymentManualReview)
	assert.Contains(t, err.Error(), ErrQuotaOverflow.Error())

	var inviter User
	require.NoError(t, DB.First(&inviter, 970094).Error)
	assert.Zero(t, inviter.AffQuota)
	assert.Zero(t, inviter.AffHistoryQuota)
	var buyer User
	require.NoError(t, DB.First(&buyer, 970095).Error)
	assert.Equal(t, common.MaxQuota-5, buyer.Quota)
	var rewardCount int64
	require.NoError(t, DB.Model(&AffiliateRewardRecord{}).Where("invitee_id = ?", buyer.Id).Count(&rewardCount).Error)
	assert.Zero(t, rewardCount)
	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
}

func TestReversalConflictRollsBackBuyerChangesBeforeManualReview(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970096, 0)
	seedPaymentUser(t, 970097, 0)
	order := createTopUpPaymentOrder(t, 970096, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-reversal-savepoint-paid")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	var topUp TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&topUp).Error)
	require.NoError(t, DB.Create(&AffiliateRewardRecord{
		InviterId: 970097, InviteeId: 970096, TopUpId: topUp.Id,
		RewardQuota: 100, ReversedQuota: 101, Status: AffiliateRewardStatusTransferred,
		CreatedAt: common.GetTimestamp(),
	}).Error)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-reversal-savepoint-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.ErrorIs(t, err, ErrPaymentManualReview)

	var buyer User
	require.NoError(t, DB.First(&buyer, 970096).Error)
	assert.Equal(t, 1000, buyer.Quota)
	assert.Equal(t, common.UserStatusEnabled, buyer.Status)
	assert.False(t, buyer.PaymentFrozen)
	var debtCount int64
	require.NoError(t, DB.Model(&PaymentDebt{}).Where("payment_order_id = ?", order.ID).Count(&debtCount).Error)
	assert.Zero(t, debtCount)
	var reversalLedgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type IN ?", order.ID, []string{
			PaymentLedgerEntryRefundReversal, PaymentLedgerEntryDisputeReversal, PaymentLedgerEntryAffiliateReversal,
		}).Count(&reversalLedgerCount).Error)
	assert.Zero(t, reversalLedgerCount)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Zero(t, stored.ReversedQuota)
	assert.Zero(t, stored.ReversedAmountMinor)
}

func TestConflictAfterFulfillmentPreservesProjectionAndAllowsLaterRefund(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970098, 0)
	order := createTopUpPaymentOrder(t, 970098, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-before-conflicting-replay")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	conflictingPaid := paidPaymentEvent(order, "stripe-conflicting-paid-after-fulfillment")
	conflictingPaid.PaidAmountMinor = 999
	conflictingPaid.NormalizedPayload = `{"amount_total":999}`
	_, err = ProcessPaymentEvent(conflictingPaid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, common.TopUpStatusSuccess, projection.Status)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-valid-refund-after-conflict", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970098).Error)
	assert.Zero(t, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusRefunded, stored.Status)
	require.NoError(t, DB.First(&projection, projection.Id).Error)
	assert.Equal(t, PaymentOrderStatusRefunded, projection.Status)
}

func TestInvalidStripePaidContractDoesNotBindCustomer(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PaymentEventInput)
	}{
		{name: "wrong amount", mutate: func(event *PaymentEventInput) { event.PaidAmountMinor-- }},
		{name: "wrong currency", mutate: func(event *PaymentEventInput) { event.Currency = "EUR" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			userID := 970040
			if test.name == "wrong currency" {
				userID = 970041
			}
			seedPaymentUser(t, userID, 0)
			order := createTopUpPaymentOrder(t, userID, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
			event := paidPaymentEvent(order, "stripe-invalid-contract-"+strings.ReplaceAll(test.name, " ", "-"))
			event.CustomerID = "cus_invalid_contract_" + strconv.Itoa(userID)
			test.mutate(&event)
			event.NormalizedPayload = common.GetJsonString(map[string]interface{}{
				"amount": event.PaidAmountMinor, "currency": event.Currency, "customer": event.CustomerID,
			})

			_, err := ProcessPaymentEvent(event)
			require.ErrorIs(t, err, ErrPaymentManualReview)
			var user User
			require.NoError(t, DB.First(&user, userID).Error)
			assert.Empty(t, user.StripeCustomer)
			var bindings int64
			require.NoError(t, DB.Model(&PaymentCustomerBinding{}).
				Where("provider = ? AND user_id = ?", PaymentProviderStripe, userID).Count(&bindings).Error)
			assert.Zero(t, bindings)
		})
	}
}

func TestInvalidVerifiedEventCanBeDurablyRetainedForManualReview(t *testing.T) {
	truncateTables(t)
	input := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-invalid-paid-event", EventType: "checkout.session.completed",
		TradeNo: "PO_INVALID_PAID", Paid: true, PaidAmountMinor: 0, Currency: "USD",
		NormalizedPayload: `{"amount_total":0}`,
	}
	_, err := ProcessPaymentEvent(input)
	require.ErrorIs(t, err, ErrPaymentEventInvalid)
	require.NoError(t, RecordPaymentEventManualReview(input, err.Error()))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	assert.Contains(t, event.LastError, "invalid normalized payment event")
}

func TestFullRefundCreatesDebtAndFreezesWhenQuotaWasSpent(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970003, 0)
	order := createTopUpPaymentOrder(t, 970003, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-for-refund")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970003).Update("quota", 200).Error)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey:            "stripe-full-refund",
		EventType:           "charge.refunded",
		ProviderPaymentKey:  paid.ProviderPaymentKey,
		RefundedAmountMinor: 1000,
		Currency:            "USD",
		Refunded:            true,
		NormalizedPayload:   `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, 970003).Error)
	assert.Zero(t, user.Quota)
	assert.Equal(t, common.UserStatusDisabled, user.Status)

	var debt PaymentDebt
	require.NoError(t, DB.Where("payment_order_id = ? AND user_id = ?", order.ID, 970003).First(&debt).Error)
	assert.EqualValues(t, 800, debt.OutstandingQuota)
	assert.Equal(t, PaymentDebtStatusOpen, debt.Status)

	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusDebt, stored.Status)
	assert.EqualValues(t, 1000, stored.ReversedQuota)
}

func TestWonDisputeRestoresQuotaAndReleasesPaymentFreeze(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970004, 0)
	order := createTopUpPaymentOrder(t, 970004, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-for-dispute")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970004).Update("quota", 200).Error)

	dispute := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey:            "stripe-dispute-created",
		EventType:           "charge.dispute.created",
		ProviderPaymentKey:  paid.ProviderPaymentKey,
		DisputedAmountMinor: 1000,
		Currency:            "USD",
		Disputed:            true,
		NormalizedPayload:   `{"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(dispute)
	require.NoError(t, err)

	won := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey:           "stripe-dispute-won",
		EventType:          "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey,
		Currency:           "USD",
		DisputeResolved:    true,
		DisputeWon:         true,
		NormalizedPayload:  `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(won)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, 970004).Error)
	assert.Equal(t, 200, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)

	var debt PaymentDebt
	require.NoError(t, DB.Where("payment_order_id = ? AND user_id = ?", order.ID, 970004).First(&debt).Error)
	assert.Zero(t, debt.OutstandingQuota)
	assert.Equal(t, PaymentDebtStatusResolved, debt.Status)

	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
	assert.Zero(t, stored.ReversedQuota)
}

func TestCanonicalSubscriptionOrderReservesAndFulfillsSnapshot(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970005, 0)
	seedStripeCustomerBinding(t, 970005, "cus_canonical_subscription")
	allowWallet := true
	plan := &SubscriptionPlan{
		Id:                  970105,
		Title:               "Canonical plan",
		PriceAmount:         9.99,
		Currency:            "USD",
		DurationUnit:        SubscriptionDurationMonth,
		DurationValue:       1,
		Enabled:             true,
		AllowWalletOverflow: &allowWallet,
		MaxPurchasePerUser:  1,
		TotalAmount:         50_000,
		QuotaResetPeriod:    SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	snapshot, err := NewSubscriptionPlanSnapshot(plan)
	require.NoError(t, err)
	snapshotJSON, err := common.Marshal(snapshot)
	require.NoError(t, err)

	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	require.NoError(t, CreatePaymentQuote(&PaymentQuote{
		QuoteID:             quoteID,
		UserID:              970005,
		OrderKind:           PaymentOrderKindSubscription,
		Provider:            PaymentProviderStripe,
		PaymentMethod:       PaymentMethodStripe,
		ProviderLivemode:    livePaymentModeForTest(),
		RequestedAmount:     int64(plan.Id),
		ExpectedAmountMinor: 999,
		Currency:            "USD",
		ProductSnapshot:     string(snapshotJSON),
		PricingSnapshot:     `{"version":1}`,
		ExpiresAt:           time.Now().Add(time.Hour).Unix(),
	}))
	order, err := CreatePaymentOrderFromQuote(970005, quoteID, "subscription_payment_one")
	require.NoError(t, err)

	secondQuoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	require.NoError(t, CreatePaymentQuote(&PaymentQuote{
		QuoteID: secondQuoteID, UserID: 970005, OrderKind: PaymentOrderKindSubscription,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, RequestedAmount: int64(plan.Id),
		ProviderLivemode:    livePaymentModeForTest(),
		ExpectedAmountMinor: 999, Currency: "USD", ProductSnapshot: string(snapshotJSON),
		PricingSnapshot: `{"version":1}`, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	_, err = CreatePaymentOrderFromQuote(970005, secondQuoteID, "subscription_payment_two")
	assert.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)

	paid := paidPaymentEvent(order, "stripe-subscription-paid")
	_, err = ProcessPaymentEvent(paid)
	require.NoError(t, err)

	var subscription UserSubscription
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&subscription).Error)
	assert.Equal(t, plan.Id, subscription.PlanId)
	assert.EqualValues(t, plan.TotalAmount, subscription.AmountTotal)
	assert.Equal(t, "active", subscription.Status)

	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
}

func TestSubscriptionDisputeThenRefundConvergesToUsageDebtOnce(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970013, 0)
	allowWallet := true
	plan := &SubscriptionPlan{
		Id: 970113, Title: "Reversal plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		AllowWalletOverflow: &allowWallet, TotalAmount: 10_000, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 970013, plan, 1000)
	paid := paidPaymentEvent(order, "stripe-subscription-dispute-paid")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	var subscription UserSubscription
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&subscription).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"amount_used": 400, "amount_used_total": 400,
	}).Error)

	dispute := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-subscription-dispute", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, DisputedAmountMinor: 1000, Currency: "USD", Disputed: true,
		NormalizedPayload: `{"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(dispute)
	require.NoError(t, err)
	var debt PaymentDebt
	require.NoError(t, DB.Where("payment_order_id = ? AND debt_kind = ?", order.ID, PaymentDebtKindBuyer).First(&debt).Error)
	assert.EqualValues(t, 1000, debt.OutstandingAmountMinor)
	assert.Zero(t, debt.OutstandingQuota)
	require.NoError(t, DB.First(&subscription, subscription.Id).Error)
	assert.Equal(t, "active", subscription.Status)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-subscription-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)
	require.NoError(t, DB.First(&debt, debt.ID).Error)
	assert.Zero(t, debt.OutstandingAmountMinor)
	assert.EqualValues(t, 400, debt.OutstandingQuota)
	assert.Zero(t, debt.ResolvedAt)
	assert.Equal(t, PaymentDebtStatusOpen, debt.Status)
	require.NoError(t, DB.First(&subscription, subscription.Id).Error)
	assert.Equal(t, "cancelled", subscription.Status)

	lost := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-subscription-dispute-lost", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, Currency: "USD", DisputeResolved: true,
		NormalizedPayload: `{"status":"lost"}`,
	}
	_, err = ProcessPaymentEvent(lost)
	require.NoError(t, err)
	require.NoError(t, DB.First(&debt, debt.ID).Error)
	assert.EqualValues(t, 400, debt.OutstandingQuota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusDebt, stored.Status)
	var legacyOrder SubscriptionOrder
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&legacyOrder).Error)
	assert.Equal(t, PaymentOrderStatusDebt, legacyOrder.Status)
}

func TestRefundedExpiredSubscriptionStillCreatesCumulativeUsageDebt(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970014, 0)
	plan := &SubscriptionPlan{
		Id: 970114, Title: "Expired reversal plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationDay, DurationValue: 1, Enabled: true,
		TotalAmount: 10_000, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 970014, plan, 1000)
	paid := paidPaymentEvent(order, "stripe-expired-subscription-paid")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	var subscription UserSubscription
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&subscription).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"amount_used": 0, "amount_used_total": 300, "status": "expired", "end_time": time.Now().Add(-time.Hour).Unix(),
	}).Error)
	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-expired-subscription-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)
	var debt PaymentDebt
	require.NoError(t, DB.Where("payment_order_id = ? AND debt_kind = ?", order.ID, PaymentDebtKindBuyer).First(&debt).Error)
	assert.EqualValues(t, 300, debt.OutstandingQuota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusDebt, stored.Status)
}

func TestLegacyResetSubscriptionRefundRequiresManualReview(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970015, 0)
	plan := &SubscriptionPlan{
		Id: 970115, Title: "Legacy reset plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 10_000, QuotaResetPeriod: SubscriptionResetDaily,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 970015, plan, 1000)
	paid := paidPaymentEvent(order, "stripe-legacy-reset-paid")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	var subscription UserSubscription
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&subscription).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"amount_used": 20, "amount_used_total": 0, "usage_accounting_version": 0,
		"last_reset_time": subscription.StartTime + 10,
	}).Error)
	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-legacy-reset-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000}`,
	}
	result, err := ProcessPaymentEvent(refund)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.True(t, result.ManualReview)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	var debtCount int64
	require.NoError(t, DB.Model(&PaymentDebt{}).Where("payment_order_id = ?", order.ID).Count(&debtCount).Error)
	assert.Zero(t, debtCount)
}

func TestSubscriptionRefundRecomputesStrongestRemainingGroup(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970016, 0)
	plan := &SubscriptionPlan{
		Id: 970116, Title: "High tier plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 10_000, QuotaResetPeriod: SubscriptionResetNever, UpgradeGroup: "svip",
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 970016, plan, 1000)
	paid := paidPaymentEvent(order, "stripe-high-tier-paid")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&UserSubscription{
		UserId: 970016, PlanId: 970216, AmountTotal: 1000, StartTime: now - 60, EndTime: now + 3600,
		Status: "active", Source: "admin", UpgradeGroup: "vip", PrevUserGroup: "default",
		UsageAccountingVersion: 1,
	}).Error)
	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-high-tier-refund", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000}`,
	}
	result, err := ProcessPaymentEvent(refund)
	require.NoError(t, err)
	assert.Equal(t, "vip", result.GroupCacheValue)
	var user User
	require.NoError(t, DB.First(&user, 970016).Error)
	assert.Equal(t, "vip", user.Group)
}

func TestOutOfOrderReversalIsPersistedBeforeWebhookAcknowledgement(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970006, 0)
	order := createTopUpPaymentOrder(t, 970006, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-refund-before-paid", EventType: "charge.refunded",
		TradeNo: order.TradeNo, ProviderPaymentKey: "stripe:payment_123", RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	result, err := ProcessPaymentEvent(refund)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.True(t, result.ManualReview)

	var refundEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, refund.EventKey).First(&refundEvent).Error)
	assert.Equal(t, PaymentEventStatusManualReview, refundEvent.Status)
	assert.Zero(t, refundEvent.PaymentOrderID)

	paid := paidPaymentEvent(order, "stripe-paid-after-refund")
	_, err = ProcessPaymentEvent(paid)
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970006).Error)
	assert.Zero(t, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusRefunded, stored.Status)
	require.NoError(t, DB.First(&refundEvent, refundEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, refundEvent.Status)
	assert.Equal(t, order.ID, refundEvent.PaymentOrderID)
}

func TestReversalProviderPaymentIdentityCannotBeRedirectedByTradeMetadata(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970043, 0)
	seedPaymentUser(t, 970044, 0)
	orderA := createTopUpPaymentOrder(t, 970043, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	orderB := createTopUpPaymentOrder(t, 970044, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paidA := paidPaymentEvent(orderA, "stripe-paid-reversal-authority-a")
	paidA.ProviderOrderKey = "stripe:cs_reversal_authority_a"
	paidA.ProviderPaymentKey = "stripe:pi_reversal_authority_a"
	paidB := paidPaymentEvent(orderB, "stripe-paid-reversal-authority-b")
	paidB.ProviderOrderKey = "stripe:cs_reversal_authority_b"
	paidB.ProviderPaymentKey = "stripe:pi_reversal_authority_b"
	_, err := ProcessPaymentEvent(paidA)
	require.NoError(t, err)
	_, err = ProcessPaymentEvent(paidB)
	require.NoError(t, err)

	result, err := ProcessPaymentEvent(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-refund-conflicting-trade-metadata", EventType: "charge.refunded",
		TradeNo: orderB.TradeNo, ProviderPaymentKey: paidA.ProviderPaymentKey,
		RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000,"metadata_trade":"order-b"}`,
	})
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result.Order)
	assert.Equal(t, orderA.ID, result.Order.ID)

	storedA, lookupErr := GetPaymentOrderByTradeNo(orderA.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, storedA.Status)
	assert.Zero(t, storedA.RefundedAmountMinor)
	storedB, lookupErr := GetPaymentOrderByTradeNo(orderB.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, storedB.Status)
	assert.Zero(t, storedB.RefundedAmountMinor)
	for _, userID := range []int{970043, 970044} {
		var user User
		require.NoError(t, DB.First(&user, userID).Error)
		assert.Equal(t, 1000, user.Quota)
	}
	var storedEvent PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", "stripe-refund-conflicting-trade-metadata").First(&storedEvent).Error)
	assert.Equal(t, orderA.ID, storedEvent.PaymentOrderID)
	assert.Equal(t, PaymentEventStatusManualReview, storedEvent.Status)
}

func TestIncompatibleStripeFinancialEventKeepsPaymentIdentityAuthority(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970046, 0)
	seedPaymentUser(t, 970047, 0)
	orderA := createTopUpPaymentOrder(t, 970046, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	orderB := createTopUpPaymentOrder(t, 970047, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paidA := paidPaymentEvent(orderA, "stripe-paid-incompatible-financial-a")
	paidA.ProviderOrderKey = "stripe:cs_incompatible_financial_a"
	paidA.ProviderPaymentKey = "stripe:pi_incompatible_financial_a"
	paidB := paidPaymentEvent(orderB, "stripe-paid-incompatible-financial-b")
	paidB.ProviderOrderKey = "stripe:cs_incompatible_financial_b"
	paidB.ProviderPaymentKey = "stripe:pi_incompatible_financial_b"
	_, err := ProcessPaymentEvent(paidA)
	require.NoError(t, err)
	_, err = ProcessPaymentEvent(paidB)
	require.NoError(t, err)

	result, err := ProcessPaymentEvent(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-incompatible-financial-conflicting-trade", EventType: "charge.refunded",
		TradeNo: orderB.TradeNo, ProviderPaymentKey: paidA.ProviderPaymentKey,
		ProviderState: "api_version_manual_review", ManualReview: true,
		NormalizedPayload: `{"automatic_action":"blocked_incompatible_api_version"}`,
	})
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result.Order)
	assert.Equal(t, orderA.ID, result.Order.ID)

	storedA, lookupErr := GetPaymentOrderByTradeNo(orderA.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, storedA.Status)
	storedB, lookupErr := GetPaymentOrderByTradeNo(orderB.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, storedB.Status)
	var storedEvent PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", "stripe-incompatible-financial-conflicting-trade").First(&storedEvent).Error)
	assert.Equal(t, orderA.ID, storedEvent.PaymentOrderID)
	assert.Equal(t, PaymentEventStatusManualReview, storedEvent.Status)
}

func TestAffiliateRewardIsReversedAndRestoredWithBuyerDispute(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970007, 0)
	seedPaymentUser(t, 970008, 50)
	order := createTopUpPaymentOrder(t, 970007, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-affiliate")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	var topUp TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&topUp).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970008).Updates(map[string]interface{}{
		"aff_quota": 0, "aff_history": 200,
	}).Error)
	require.NoError(t, DB.Create(&AffiliateRewardRecord{
		InviterId: 970008, InviteeId: 970007, TopUpId: topUp.Id, RewardQuota: 200,
		TransferredQuota: 200, Status: AffiliateRewardStatusTransferred, CreatedAt: common.GetTimestamp(),
	}).Error)

	dispute := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-affiliate-dispute", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, DisputedAmountMinor: 1000, Currency: "USD",
		Disputed: true, NormalizedPayload: `{"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(dispute)
	require.NoError(t, err)
	var inviter User
	require.NoError(t, DB.First(&inviter, 970008).Error)
	assert.Zero(t, inviter.Quota)
	assert.Zero(t, inviter.AffHistoryQuota)
	assert.True(t, inviter.PaymentFrozen)
	var affiliateDebt PaymentDebt
	require.NoError(t, DB.Where("payment_order_id = ? AND user_id = ? AND debt_kind = ?", order.ID, 970008, PaymentDebtKindAffiliate).First(&affiliateDebt).Error)
	assert.EqualValues(t, 150, affiliateDebt.OutstandingQuota)

	won := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-affiliate-dispute-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, Currency: "USD", DisputeResolved: true, DisputeWon: true,
		NormalizedPayload: `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(won)
	require.NoError(t, err)
	require.NoError(t, DB.First(&inviter, 970008).Error)
	assert.Equal(t, 50, inviter.Quota)
	assert.Equal(t, 200, inviter.AffHistoryQuota)
	assert.False(t, inviter.PaymentFrozen)
	var reward AffiliateRewardRecord
	require.NoError(t, DB.Where("top_up_id = ?", topUp.Id).First(&reward).Error)
	assert.Zero(t, reward.ReversedQuota)
	assert.Equal(t, AffiliateRewardStatusTransferred, reward.Status)
}

func TestResolvedDebtDoesNotReenableExplicitlyDisabledUser(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970009, 0)
	order := createTopUpPaymentOrder(t, 970009, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-admin-disable")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970009).Update("quota", 0).Error)
	dispute := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-dispute-admin-disable", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, DisputedAmountMinor: 1000, Currency: "USD", Disputed: true,
		NormalizedPayload: `{"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(dispute)
	require.NoError(t, err)
	// An administrator explicitly disables the user after the payment freeze.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970009).Updates(map[string]interface{}{
		"status": common.UserStatusDisabled, "payment_frozen": false,
	}).Error)
	won := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-dispute-admin-disable-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, Currency: "USD", DisputeResolved: true, DisputeWon: true,
		NormalizedPayload: `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(won)
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970009).Error)
	assert.Equal(t, common.UserStatusDisabled, user.Status)
	assert.False(t, user.PaymentFrozen)
}

func TestPaidEventReplaysEarlierUnmatchedRefund(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970010, 0)
	order := createTopUpPaymentOrder(t, 970010, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	providerPaymentKey := "stripe:payment_123"
	require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-unmatched-refund", EventType: "charge.refunded",
		ProviderPaymentKey: providerPaymentKey, RefundedAmountMinor: 1000, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":1000}`,
	}, "payment intent was not bound yet"))

	paid := paidPaymentEvent(order, "stripe-paid-replay-refund")
	paid.ProviderPaymentKey = providerPaymentKey
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, 970010).Error)
	assert.Zero(t, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusRefunded, stored.Status)
	var replayed PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", "stripe-unmatched-refund").First(&replayed).Error)
	assert.Equal(t, PaymentEventStatusProcessed, replayed.Status)
	assert.Equal(t, order.ID, replayed.PaymentOrderID)
}

func TestUnmatchedReversalReplayRollsBackClaimOnTransientFailure(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970039, 0)
	order := createTopUpPaymentOrder(t, 970039, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-before-replay-failure")
	paid.ProviderPaymentKey = "stripe:pi_replay_transient_failure"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-unmatched-refund-transient-failure", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 1000,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":1000}`,
	}
	require.NoError(t, RecordPaymentEventManualReview(refund, "payment intent replay is pending"))

	forcedErr := errors.New("forced transient payment order lookup failure")
	callbackName := "test:payment_reversal_replay_transient_failure"
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "payment_orders" {
			tx.AddError(forcedErr)
		}
	}))
	callbackRegistered := true
	t.Cleanup(func() {
		if callbackRegistered {
			_ = DB.Callback().Query().Remove(callbackName)
		}
	})
	err = replayUnmatchedPaymentReversals(PaymentProviderStripe, paid.ProviderPaymentKey)
	assert.ErrorIs(t, err, forcedErr)
	require.NoError(t, DB.Callback().Query().Remove(callbackName))
	callbackRegistered = false

	var storedEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", refund.Provider, refund.EventKey).First(&storedEvent).Error)
	assert.Equal(t, PaymentEventStatusManualReview, storedEvent.Status)
	assert.Zero(t, storedEvent.PaymentOrderID)

	require.NoError(t, replayUnmatchedPaymentReversals(PaymentProviderStripe, paid.ProviderPaymentKey))
	require.NoError(t, DB.First(&storedEvent, storedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, storedEvent.Status)
	assert.Equal(t, order.ID, storedEvent.PaymentOrderID)
	var user User
	require.NoError(t, DB.First(&user, 970039).Error)
	assert.Zero(t, user.Quota)
}

func TestStaleCumulativeRefundIsIdempotentNoOp(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970011, 0)
	order := createTopUpPaymentOrder(t, 970011, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-stale-refund")
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	firstRefund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "stripe-refund-800", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 800, Currency: "USD", Refunded: true,
		NormalizedPayload: `{"amount_refunded":800}`,
	}
	_, err = ProcessPaymentEvent(firstRefund)
	require.NoError(t, err)
	staleRefund := firstRefund
	staleRefund.EventKey = "stripe-refund-stale-500"
	staleRefund.RefundedAmountMinor = 500
	staleRefund.NormalizedPayload = `{"amount_refunded":500}`
	_, err = ProcessPaymentEvent(staleRefund)
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970011).Error)
	assert.Equal(t, 200, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 800, stored.RefundedAmountMinor)
	assert.EqualValues(t, 800, stored.ReversedQuota)
}

func TestRefundAndDisputeReverseIndependentAmountsWithoutOverCredit(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970036, 0)
	order := createTopUpPaymentOrder(t, 970036, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-refund-plus-dispute")
	paid.ProviderPaymentKey = "stripe:pi_refund_plus_dispute"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	refund := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-partial-refund-before-dispute", EventType: "charge.refunded",
		ProviderPaymentKey: paid.ProviderPaymentKey, RefundedAmountMinor: 200,
		Currency: "USD", Refunded: true, NormalizedPayload: `{"amount_refunded":200}`,
	}
	_, err = ProcessPaymentEvent(refund)
	require.NoError(t, err)

	dispute := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-remaining-amount-disputed", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_refund_plus_dispute",
		ProviderCreatedAt: 100, ProviderState: "needs_response", DisputedAmountMinor: 800,
		Currency: "USD", Disputed: true, NormalizedPayload: `{"amount":800,"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(dispute)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, 970036).Error)
	assert.Zero(t, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 200, stored.RefundedAmountMinor)
	assert.EqualValues(t, 800, stored.DisputedAmountMinor)
	assert.EqualValues(t, 1000, stored.ReversedAmountMinor)
	assert.EqualValues(t, 1000, stored.ReversedQuota)

	won := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-remaining-amount-dispute-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: dispute.ProviderResourceKey,
		ProviderCreatedAt: 101, ProviderState: "won", Currency: "USD",
		DisputeResolved: true, DisputeWon: true, NormalizedPayload: `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(won)
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, 970036).Error)
	assert.Equal(t, 800, user.Quota)
	stored, lookupErr = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 200, stored.RefundedAmountMinor)
	assert.Zero(t, stored.DisputedAmountMinor)
	assert.EqualValues(t, 200, stored.ReversedAmountMinor)
	assert.EqualValues(t, 200, stored.ReversedQuota)
	assert.Equal(t, PaymentOrderStatusRefundPending, stored.Status)
}

func TestMultipleStripeDisputesAggregateByResource(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970045, 0)
	order := createTopUpPaymentOrder(t, 970045, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-multiple-disputes")
	paid.ProviderPaymentKey = "stripe:pi_multiple_disputes"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	disputeA := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-multiple-disputes-a-created", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_multiple_a",
		ProviderCreatedAt: 100, ProviderState: "needs_response", DisputedAmountMinor: 600,
		Currency: "USD", Disputed: true, NormalizedPayload: `{"amount":600,"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(disputeA)
	require.NoError(t, err)
	disputeB := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-multiple-disputes-b-created", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_multiple_b",
		ProviderCreatedAt: 101, ProviderState: "needs_response", DisputedAmountMinor: 400,
		Currency: "USD", Disputed: true, NormalizedPayload: `{"amount":400,"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(disputeB)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Zero(t, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 1000, stored.DisputedAmountMinor)
	assert.EqualValues(t, 1000, stored.ReversedAmountMinor)

	_, err = ProcessPaymentEvent(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-multiple-disputes-b-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: disputeB.ProviderResourceKey,
		ProviderCreatedAt: 102, ProviderState: "won", DisputedAmountMinor: 400,
		Currency: "USD", DisputeResolved: true, DisputeWon: true, NormalizedPayload: `{"amount":400,"status":"won"}`,
	})
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Equal(t, 400, user.Quota)
	stored, lookupErr = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 600, stored.DisputedAmountMinor)
	assert.EqualValues(t, 600, stored.ReversedAmountMinor)
	assert.Equal(t, PaymentOrderStatusDisputed, stored.Status)

	_, err = ProcessPaymentEvent(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-multiple-disputes-a-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: disputeA.ProviderResourceKey,
		ProviderCreatedAt: 103, ProviderState: "won", DisputedAmountMinor: 600,
		Currency: "USD", DisputeResolved: true, DisputeWon: true, NormalizedPayload: `{"amount":600,"status":"won"}`,
	})
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Equal(t, 1000, user.Quota)
	stored, lookupErr = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Zero(t, stored.DisputedAmountMinor)
	assert.Zero(t, stored.ReversedAmountMinor)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
}

func TestAdminCanAuditablyFulfillManualPaymentOrder(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970012, 0)
	order := createTopUpPaymentOrder(t, 970012, PaymentProviderEpay, "alipay", 7300, 5000)
	mismatch := paidPaymentEvent(order, "epay-manual-resolution")
	mismatch.PaidAmountMinor = 7299
	mismatch.NormalizedPayload = `{"amount":7299}`
	_, err := ProcessPaymentEvent(mismatch)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	manualOrder, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)

	result, err := ResolveManualPaymentOrderByAdmin(order.TradeNo, manualOrder.Version, 1, "192.0.2.1", "verified against the provider dashboard")
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	var user User
	require.NoError(t, DB.First(&user, 970012).Error)
	assert.Equal(t, 5000, user.Quota)
	resolved, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, resolved.Status)
	var topUp TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&topUp).Error)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	var fulfillAudit PaymentOperationsAudit
	require.NoError(t, DB.Where("action = ? AND payment_order_id = ?", PaymentOperationsActionAdminFulfill, order.ID).
		First(&fulfillAudit).Error)
	assert.Equal(t, "192.0.2.1", fulfillAudit.ActorIP)
	assert.Equal(t, "verified against the provider dashboard", fulfillAudit.Reason)
}

func TestDelayedDisputeCreatedCannotReverseNewerClosedState(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970013, 0)
	order := createTopUpPaymentOrder(t, 970013, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-before-out-of-order-dispute")
	paid.ProviderOrderKey = "stripe:cs_out_of_order"
	paid.ProviderPaymentKey = "stripe:pi_out_of_order"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970013).Update("quota", 200).Error)

	closed := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "evt_dispute_closed_first", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_out_of_order",
		ProviderCreatedAt: 200, ProviderState: "won", Currency: "USD", DisputeResolved: true, DisputeWon: true,
		NormalizedPayload: `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(closed)
	require.NoError(t, err)

	delayedCreated := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "evt_dispute_created_late", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_out_of_order",
		ProviderCreatedAt: 100, ProviderState: "needs_response", DisputedAmountMinor: 1000, Currency: "USD", Disputed: true,
		NormalizedPayload: `{"status":"needs_response"}`,
	}
	result, err := ProcessPaymentEvent(delayedCreated)
	require.NoError(t, err)
	require.NotNil(t, result.Order)

	var user User
	require.NoError(t, DB.First(&user, 970013).Error)
	assert.Equal(t, 200, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.False(t, user.PaymentFrozen)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
	assert.Zero(t, stored.DisputedAmountMinor)
	var debtCount int64
	require.NoError(t, DB.Model(&PaymentDebt{}).Where("payment_order_id = ?", order.ID).Count(&debtCount).Error)
	assert.Zero(t, debtCount)
}

func TestStaleSameTimestampDisputeDoesNotReenterAggregate(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970048, 0)
	order := createTopUpPaymentOrder(t, 970048, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-before-same-timestamp-stale-dispute")
	paid.ProviderPaymentKey = "stripe:pi_same_timestamp_stale"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)

	closedA := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-same-timestamp-a-won", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_same_timestamp_a",
		ProviderCreatedAt: 100, ProviderState: "won", DisputedAmountMinor: 600,
		Currency: "USD", DisputeResolved: true, DisputeWon: true, NormalizedPayload: `{"amount":600,"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(closedA)
	require.NoError(t, err)
	staleCreatedA := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-same-timestamp-a-created-late", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: closedA.ProviderResourceKey,
		ProviderCreatedAt: 100, ProviderState: "needs_response", DisputedAmountMinor: 600,
		Currency: "USD", Disputed: true, NormalizedPayload: `{"amount":600,"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(staleCreatedA)
	require.NoError(t, err)
	var staleEvent PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", staleCreatedA.EventKey).First(&staleEvent).Error)
	assert.Equal(t, PaymentEventStatusDismissed, staleEvent.Status)

	disputeB := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		EventKey: "stripe-same-timestamp-b-created", EventType: "charge.dispute.created",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_same_timestamp_b",
		ProviderCreatedAt: 101, ProviderState: "needs_response", DisputedAmountMinor: 400,
		Currency: "USD", Disputed: true, NormalizedPayload: `{"amount":400,"status":"needs_response"}`,
	}
	_, err = ProcessPaymentEvent(disputeB)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Equal(t, 600, user.Quota)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.EqualValues(t, 400, stored.DisputedAmountMinor)
	assert.EqualValues(t, 400, stored.ReversedAmountMinor)
}

func TestLaterWonDisputeRestoresEarlierLostState(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970014, 0)
	order := createTopUpPaymentOrder(t, 970014, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-paid-before-late-win")
	paid.ProviderOrderKey = "stripe:cs_late_win"
	paid.ProviderPaymentKey = "stripe:pi_late_win"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970014).Update("quota", 200).Error)

	lost := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "evt_dispute_lost", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_late_win",
		ProviderCreatedAt: 100, ProviderState: "lost", DisputedAmountMinor: 1000, Currency: "USD",
		Disputed: true, DisputeResolved: true, NormalizedPayload: `{"status":"lost"}`,
	}
	_, err = ProcessPaymentEvent(lost)
	require.NoError(t, err)

	won := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: "evt_dispute_late_win", EventType: "charge.dispute.closed",
		ProviderPaymentKey: paid.ProviderPaymentKey, ProviderResourceKey: "stripe:dp_late_win",
		ProviderCreatedAt: 200, ProviderState: "won", Currency: "USD", DisputeResolved: true, DisputeWon: true,
		NormalizedPayload: `{"status":"won"}`,
	}
	_, err = ProcessPaymentEvent(won)
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.First(&user, 970014).Error)
	assert.Equal(t, 200, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.False(t, user.PaymentFrozen)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
	assert.Zero(t, stored.DisputedAmountMinor)
}

func TestResolvingMultiplePaymentDebtsReleasesSingleUserFreeze(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970015, 0)
	seedStripeCustomerBinding(t, 970015, "cus_multi_debt")
	orders := []*PaymentOrder{
		createTopUpPaymentOrder(t, 970015, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000),
		createTopUpPaymentOrder(t, 970015, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000),
	}
	for index, order := range orders {
		paid := paidPaymentEvent(order, fmt.Sprintf("stripe-paid-multi-debt-%d", index))
		paid.ProviderOrderKey = fmt.Sprintf("stripe:cs_multi_debt_%d", index)
		paid.ProviderPaymentKey = fmt.Sprintf("stripe:pi_multi_debt_%d", index)
		_, err := ProcessPaymentEvent(paid)
		require.NoError(t, err)
	}
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970015).Update("quota", 0).Error)

	for index, order := range orders {
		dispute := PaymentEventInput{
			Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(), EventKey: fmt.Sprintf("stripe-multi-debt-dispute-%d", index), EventType: "charge.dispute.created",
			ProviderPaymentKey: fmt.Sprintf("stripe:pi_multi_debt_%d", index), DisputedAmountMinor: 1000,
			Currency: "USD", Disputed: true, NormalizedPayload: `{"status":"needs_response"}`,
		}
		_, err := ProcessPaymentEvent(dispute)
		require.NoError(t, err)
		_ = order
	}

	var debts []PaymentDebt
	require.NoError(t, DB.Where("user_id = ? AND status = ?", 970015, PaymentDebtStatusOpen).Order("id asc").Find(&debts).Error)
	require.Len(t, debts, 2)
	assert.Equal(t, common.UserStatusEnabled, debts[0].PreviousUserStatus)
	assert.Equal(t, common.UserStatusEnabled, debts[1].PreviousUserStatus)

	_, err := ResolvePaymentDebtByAdmin(PaymentDebtResolutionInput{
		DebtID: debts[0].ID, AdminID: 1, ActorIP: "192.0.2.1",
		ExpectedOutstandingQuota: debts[0].OutstandingQuota, ExpectedOutstandingAmount: debts[0].OutstandingAmountMinor,
		Resolution: "waived", Note: "verified first debt resolution",
	})
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 970015).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)

	_, err = ResolvePaymentDebtByAdmin(PaymentDebtResolutionInput{
		DebtID: debts[1].ID, AdminID: 1, ActorIP: "192.0.2.1",
		ExpectedOutstandingQuota: debts[1].OutstandingQuota, ExpectedOutstandingAmount: debts[1].OutstandingAmountMinor,
		Resolution: "waived", Note: "verified second debt resolution",
	})
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, 970015).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	var debtAudits []PaymentOperationsAudit
	require.NoError(t, DB.Where("action = ? AND subject_id IN ?", PaymentOperationsActionDebtResolve,
		[]int64{debts[0].ID, debts[1].ID}).Order("subject_id asc").Find(&debtAudits).Error)
	require.Len(t, debtAudits, 2)
	for index, audit := range debtAudits {
		assert.Equal(t, "192.0.2.1", audit.ActorIP)
		assert.EqualValues(t, debts[index].ID, audit.SubjectID)
		assert.EqualValues(t, debts[index].PaymentOrderID, audit.PaymentOrderID)
	}
}

func TestPaymentEventEntryPointsNormalizeIdentityBeforeIdempotencyAndBinding(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970020, 0)
	order := createTopUpPaymentOrder(t, 970020, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	input := paidPaymentEvent(order, " stripe-normalized-event ")
	input.Provider = " Stripe "
	input.EventType = " checkout.session.completed "
	input.TradeNo = " " + order.TradeNo + " "
	input.ProviderOrderKey = " stripe:cs_normalized "
	input.ProviderPaymentKey = " stripe:pi_normalized "
	input.ProviderResourceKey = " stripe:resource_normalized "
	input.ProviderState = " paid "
	input.CustomerID = " cus_normalized "
	input.Currency = " usd "
	input.PaymentMethod = " STRIPE "

	first, err := ProcessPaymentEvent(input)
	require.NoError(t, err)
	require.NotNil(t, first.Order)
	assert.False(t, first.Duplicate)

	canonical := input
	canonical.Provider = PaymentProviderStripe
	canonical.EventKey = "stripe-normalized-event"
	canonical.EventType = "checkout.session.completed"
	canonical.TradeNo = order.TradeNo
	canonical.ProviderOrderKey = "stripe:cs_normalized"
	canonical.ProviderPaymentKey = "stripe:pi_normalized"
	canonical.ProviderResourceKey = "stripe:resource_normalized"
	canonical.ProviderState = "paid"
	canonical.CustomerID = "cus_normalized"
	canonical.Currency = "USD"
	canonical.PaymentMethod = PaymentMethodStripe
	duplicate, err := ProcessPaymentEvent(canonical)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)

	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, canonical.EventKey).First(&event).Error)
	assert.Equal(t, canonical.EventType, event.EventType)
	assert.Equal(t, canonical.TradeNo, event.TradeNo)
	assert.Equal(t, canonical.ProviderOrderKey, event.ProviderOrderKey)
	assert.Equal(t, canonical.ProviderPaymentKey, event.ProviderPaymentKey)
	assert.Equal(t, canonical.ProviderResourceKey, event.ProviderResourceKey)
	assert.Equal(t, canonical.ProviderState, event.ProviderState)
	assert.Equal(t, canonical.Currency, event.Currency)
	assert.Equal(t, canonical.PaymentMethod, event.PaymentMethod)
	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND event_key = ?", PaymentProviderStripe, canonical.EventKey).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)

	var user User
	require.NoError(t, DB.First(&user, 970020).Error)
	assert.Equal(t, "cus_normalized", user.StripeCustomer)

	manual := PaymentEventInput{
		Provider: " Stripe ", EventKey: " manual-normalized-event ", EventType: " unknown.event ",
		TradeNo: " missing-order ", ProviderPaymentKey: " stripe:pi_manual ", Currency: " usd ", PaymentMethod: " STRIPE ",
		NormalizedPayload: `{"manual":true}`,
	}
	require.NoError(t, RecordPaymentEventManualReview(manual, "verified event requires manual review"))
	manual.Provider = PaymentProviderStripe
	manual.EventKey = "manual-normalized-event"
	manual.EventType = "unknown.event"
	manual.TradeNo = "missing-order"
	manual.ProviderPaymentKey = "stripe:pi_manual"
	manual.Currency = "USD"
	manual.PaymentMethod = PaymentMethodStripe
	require.NoError(t, RecordPaymentEventManualReview(manual, "verified event requires manual review"))
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND event_key = ?", PaymentProviderStripe, manual.EventKey).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

func TestPaymentEventIdentityPreservesCaseSensitiveEpayMethod(t *testing.T) {
	input := PaymentEventInput{
		Provider: PaymentProviderEpay, PaymentMethod: " CustomQR ", Currency: " cny ",
	}
	input.normalizeIdentity()
	assert.Equal(t, "CustomQR", input.PaymentMethod)
	assert.Equal(t, "CNY", input.Currency)

	stripeInput := PaymentEventInput{
		Provider: " Stripe ", PaymentMethod: " STRIPE ", Currency: " usd ",
	}
	stripeInput.normalizeIdentity()
	assert.Equal(t, PaymentMethodStripe, stripeInput.PaymentMethod)
	assert.Equal(t, "USD", stripeInput.Currency)
}

func TestStripeCustomerBindingRejectsCustomerOwnedByAnotherUser(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970021, 0)
	seedPaymentUser(t, 970022, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 970022).Update("stripe_customer", "cus_shared_owner").Error)
	order := createTopUpPaymentOrder(t, 970021, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	input := paidPaymentEvent(order, "stripe-customer-cross-user")
	input.CustomerID = "cus_shared_owner"

	result, err := ProcessPaymentEvent(input)
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Equal(t, "stripe_customer_identity_mismatch", stored.StatusReason)
	var target User
	require.NoError(t, DB.First(&target, 970021).Error)
	assert.Empty(t, target.StripeCustomer)
	assert.Zero(t, target.Quota)
}

func TestStripeCustomerBindingRejectsValueLongerThanUserColumn(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970023, 0)
	order := createTopUpPaymentOrder(t, 970023, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	input := paidPaymentEvent(order, "stripe-customer-too-long")
	input.CustomerID = "cus_" + strings.Repeat("x", 61)
	require.Len(t, input.CustomerID, 65)

	result, err := ProcessPaymentEvent(input)
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.Equal(t, "stripe_customer_identity_mismatch", stored.StatusReason)
	var user User
	require.NoError(t, DB.First(&user, 970023).Error)
	assert.Empty(t, user.StripeCustomer)
	assert.Zero(t, user.Quota)
}

func TestStripeCustomerOwnershipBindingIsIdempotentForSameUser(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970024, 0)
	order := createTopUpPaymentOrder(t, 970024, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	first := paidPaymentEvent(order, "stripe-customer-binding-first")
	first.CustomerID = "cus_binding_idempotent"
	_, err := ProcessPaymentEvent(first)
	require.NoError(t, err)

	second := first
	second.EventKey = "stripe-customer-binding-second"
	second.NormalizedPayload = `{"event":"second"}`
	_, err = ProcessPaymentEvent(second)
	require.NoError(t, err)

	var bindings []PaymentCustomerBinding
	require.NoError(t, DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, 970024).Find(&bindings).Error)
	require.Len(t, bindings, 1)
	assert.Equal(t, "cus_binding_idempotent", bindings[0].CustomerKey)
	var user User
	require.NoError(t, DB.First(&user, 970024).Error)
	assert.Equal(t, "cus_binding_idempotent", user.StripeCustomer)
}

func TestStripeCustomerOwnershipTableRejectsConflictWithoutLegacyUserValue(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970025, 0)
	seedPaymentUser(t, 970026, 0)
	require.NoError(t, DB.Create(&PaymentCustomerBinding{
		Provider: PaymentProviderStripe, CustomerKey: "cus_binding_owner", UserID: 970026,
	}).Error)
	order := createTopUpPaymentOrder(t, 970025, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	input := paidPaymentEvent(order, "stripe-customer-binding-conflict")
	input.CustomerID = "cus_binding_owner"

	result, err := ProcessPaymentEvent(input)
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, lookupErr)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	var binding PaymentCustomerBinding
	require.NoError(t, DB.Where("provider = ? AND customer_key = ?", PaymentProviderStripe, input.CustomerID).First(&binding).Error)
	assert.Equal(t, 970026, binding.UserID)
}

func TestStripeCustomerOwnershipTableRejectsSecondCustomerForSameUser(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970027, 0)
	require.NoError(t, DB.Create(&PaymentCustomerBinding{
		Provider: PaymentProviderStripe, CustomerKey: "cus_binding_first", UserID: 970027,
	}).Error)
	order := createTopUpPaymentOrder(t, 970027, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	input := paidPaymentEvent(order, "stripe-customer-binding-second-customer")
	input.CustomerID = "cus_binding_second"

	result, err := ProcessPaymentEvent(input)
	assert.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	var bindings []PaymentCustomerBinding
	require.NoError(t, DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, 970027).Find(&bindings).Error)
	require.Len(t, bindings, 1)
	assert.Equal(t, "cus_binding_first", bindings[0].CustomerKey)
}

func TestStripeCustomerForCheckoutBackfillsOnlyUnambiguousOwnership(t *testing.T) {
	t.Run("unique legacy binding", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 970028, 0)
		require.NoError(t, DB.Model(&User{}).Where("id = ?", 970028).Update("stripe_customer", "cus_checkout_unique").Error)

		customer, err := StripeCustomerForCheckout(970028, "cus_checkout_unique")
		require.NoError(t, err)
		assert.Equal(t, "cus_checkout_unique", customer)
		var binding PaymentCustomerBinding
		require.NoError(t, DB.Where("provider = ? AND customer_key = ?", PaymentProviderStripe, customer).First(&binding).Error)
		assert.Equal(t, 970028, binding.UserID)
	})

	t.Run("duplicate legacy customer", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 970029, 0)
		seedPaymentUser(t, 970030, 0)
		require.NoError(t, DB.Model(&User{}).Where("id IN ?", []int{970029, 970030}).
			Update("stripe_customer", "cus_checkout_duplicated").Error)

		customer, err := StripeCustomerForCheckout(970029, "cus_checkout_duplicated")
		assert.Empty(t, customer)
		assert.ErrorIs(t, err, ErrPaymentManualReview)
		var bindings int64
		require.NoError(t, DB.Model(&PaymentCustomerBinding{}).
			Where("provider = ? AND customer_key = ?", PaymentProviderStripe, "cus_checkout_duplicated").Count(&bindings).Error)
		assert.Zero(t, bindings)
	})

	t.Run("authoritative binding mismatch", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 970031, 0)
		require.NoError(t, DB.Model(&User{}).Where("id = ?", 970031).Update("stripe_customer", "cus_checkout_legacy").Error)
		require.NoError(t, DB.Create(&PaymentCustomerBinding{
			Provider: PaymentProviderStripe, CustomerKey: "cus_checkout_authoritative", UserID: 970031,
		}).Error)

		customer, err := StripeCustomerForCheckout(970031, "cus_checkout_legacy")
		assert.Empty(t, customer)
		assert.ErrorIs(t, err, ErrPaymentManualReview)
	})
}

func TestMarkPaymentOrderFailedSynchronizesSubscriptionProjection(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 970016, 0)
	allowWallet := true
	plan := &SubscriptionPlan{
		Id: 970116, Title: "Failed external checkout", PriceAmount: 9.99, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		AllowWalletOverflow: &allowWallet, MaxPurchasePerUser: 1, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 970016, plan, 999)
	require.NoError(t, MarkPaymentOrderFailed(order.TradeNo, "provider rejected configuration"))

	stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusFailed, stored.Status)
	var projection SubscriptionOrder
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, PaymentOrderStatusFailed, projection.Status)
	usedSlots, err := CountUserSubscriptionPurchasesByPlan(970016, plan.Id)
	require.NoError(t, err)
	assert.Zero(t, usedSlots)
}
