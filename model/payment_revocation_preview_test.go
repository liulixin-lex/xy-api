package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewPaymentCredentialRevocationCountsOnlyAffectedUnfinishedWork(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	orders := []PaymentOrder{
		{TradeNo: "PO_PREVIEW_MATCH", UserID: 979201, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "preview-match", ProviderCredentialGeneration: 2,
			ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusPending, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_PREVIEW_AMBIGUOUS", UserID: 979202, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "preview-ambiguous", ProviderCredentialGeneration: 0,
			ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusProcessing, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_PREVIEW_COMPLETE", UserID: 979203, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "preview-complete", ProviderCredentialGeneration: 2,
			ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusFulfilled, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_PREVIEW_OTHER_GENERATION", UserID: 979204, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "preview-other", ProviderCredentialGeneration: 3,
			ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusPending, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_PREVIEW_ALREADY_OPEN", UserID: 979207, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "preview-already-open", ProviderCredentialGeneration: 2,
			ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusManualReview, CreatedAt: now - 10, UpdatedAt: now,
			CredentialIncident: true, CredentialIncidentGeneration: 2, CredentialIncidentState: PaymentCredentialIncidentOpen},
	}
	require.NoError(t, DB.Create(&orders).Error)
	require.NoError(t, DB.Create(&TopUp{
		UserId: 979205, Amount: 1, Money: 1, TradeNo: "PREVIEW_LEGACY_TOPUP", PaymentMethod: "alipay",
		PaymentProvider: PaymentProviderEpay, CreateTime: now - 10, Status: common.TopUpStatusPending,
	}).Error)
	require.NoError(t, DB.Create(&SubscriptionOrder{
		UserId: 979206, PlanId: 1, Money: 1, TradeNo: "PREVIEW_LEGACY_SUB", PaymentMethod: "alipay",
		PaymentProvider: PaymentProviderEpay, CreateTime: now - 10, Status: common.TopUpStatusPending,
	}).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderEpay, EventKey: "preview-unmatched-economic", EventType: "paid",
		ProviderCredentialGeneration: 2, PaymentOrderID: 0, Paid: true, PaidAmountMinor: 100,
		Currency: "CNY", Status: PaymentEventStatusManualReview, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderEpay, EventKey: "preview-unmatched-non-economic", EventType: "ignored",
		ProviderCredentialGeneration: 2, PaymentOrderID: 0, Status: PaymentEventStatusManualReview,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	impact, err := PreviewPaymentCredentialRevocation(
		PaymentProviderEpay, PaymentCredentialRevocationModePrevious, []int64{2, 2}, false, now,
	)
	require.NoError(t, err)
	assert.Equal(t, &PaymentCredentialRevocationImpact{
		Provider: PaymentProviderEpay, Mode: PaymentCredentialRevocationModePrevious,
		CanonicalAffectedOrders: 3, CanonicalUnfinishedOrders: 2,
		LegacyPendingTopUps: 1, LegacyPendingSubscriptions: 1,
		UnmatchedEconomicEvents: 1, TotalAffectedOrders: 5, TotalUnfinishedOrders: 4,
	}, impact)
}

func TestPreviewStripeEmergencyRevocationMirrorsLinkedAndActiveOrderScope(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	orders := []PaymentOrder{
		{TradeNo: "PO_STRIPE_PREVIEW_ACTIVE", UserID: 979211, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "stripe-preview-active", ProviderCredentialGeneration: 9,
			ExpectedAmountMinor: 100, Currency: "USD", Status: PaymentOrderStatusPending, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_STRIPE_PREVIEW_CURRENT", UserID: 979212, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "stripe-preview-current", ProviderCredentialGeneration: 5,
			ExpectedAmountMinor: 100, Currency: "USD", Status: PaymentOrderStatusFulfilled, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_STRIPE_PREVIEW_PREVIOUS", UserID: 979213, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "stripe-preview-previous", ProviderCredentialGeneration: 4,
			ExpectedAmountMinor: 100, Currency: "USD", Status: PaymentOrderStatusFulfilled, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_STRIPE_PREVIEW_LINKED", UserID: 979214, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "stripe-preview-linked", ProviderCredentialGeneration: 9,
			ExpectedAmountMinor: 100, Currency: "USD", Status: PaymentOrderStatusFulfilled, CreatedAt: now - 10, UpdatedAt: now},
		{TradeNo: "PO_STRIPE_PREVIEW_ALREADY_OPEN", UserID: 979215, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "stripe-preview-already-open", ProviderCredentialGeneration: 5,
			ExpectedAmountMinor: 100, Currency: "USD", Status: PaymentOrderStatusFulfilled, CreatedAt: now - 10, UpdatedAt: now,
			CredentialIncident: true, CredentialIncidentGeneration: 5, CredentialIncidentState: PaymentCredentialIncidentOpen},
	}
	require.NoError(t, DB.Create(&orders).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "stripe-preview-linked-event", EventType: "checkout.session.completed",
		ProviderCredentialGeneration: 5, PaymentOrderID: orders[3].ID, Paid: true, PaidAmountMinor: 100,
		Currency: "USD", Status: PaymentEventStatusProcessed, CreatedAt: now, UpdatedAt: now,
	}).Error)

	impact, err := PreviewPaymentCredentialRevocation(
		PaymentProviderStripe, PaymentCredentialRevocationModeAllActive, []int64{5, 4}, true, now,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(4), impact.CanonicalAffectedOrders)
	assert.Equal(t, int64(1), impact.CanonicalUnfinishedOrders)
	assert.Equal(t, int64(4), impact.TotalAffectedOrders)
	assert.Equal(t, int64(1), impact.TotalUnfinishedOrders)
}
