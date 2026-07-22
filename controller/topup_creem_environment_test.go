package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func creemEnvironmentTestEvent(checkoutMode, orderMode, productMode, customerMode string) *CreemWebhookEvent {
	event := &CreemWebhookEvent{Id: "evt_creem_environment", EventType: "checkout.completed"}
	event.Object.Id = "checkout_creem_environment"
	event.Object.RequestId = "PO_CREEM_ENVIRONMENT"
	event.Object.Mode = checkoutMode
	event.Object.Order.Id = "order_creem_environment"
	event.Object.Order.Status = "paid"
	event.Object.Order.AmountPaid = 1000
	event.Object.Order.Currency = "USD"
	event.Object.Order.Mode = orderMode
	event.Object.Product.Id = "product_creem_environment"
	event.Object.Product.Mode = productMode
	event.Object.Customer.Id = "customer_creem_environment"
	event.Object.Customer.Mode = customerMode
	return event
}

func TestNormalizedCreemWebhookEventRequiresConsistentEnvironment(t *testing.T) {
	for _, test := range []struct {
		name         string
		checkoutMode string
		orderMode    string
		productMode  string
		customerMode string
		wantLive     bool
		wantErr      bool
	}{
		{name: "production", checkoutMode: "local", orderMode: "local", productMode: "sandbox", customerMode: "test", wantLive: true},
		{name: "test", checkoutMode: "test", orderMode: "test", productMode: "test", customerMode: "test"},
		{name: "test aliases agree", checkoutMode: "test", orderMode: "sandbox", productMode: "local", customerMode: "local"},
		{name: "sandbox aliases agree", checkoutMode: "sandbox", orderMode: "test", productMode: "", customerMode: ""},
		{name: "missing checkout mode", orderMode: "local", productMode: "local", customerMode: "local", wantErr: true},
		{name: "missing order mode", checkoutMode: "local", productMode: "local", customerMode: "local", wantErr: true},
		{name: "api-only prod is rejected", checkoutMode: "prod", orderMode: "prod", wantErr: true},
		{name: "unknown mode", checkoutMode: "live", orderMode: "live", wantErr: true},
		{name: "live and test mismatch", checkoutMode: "local", orderMode: "test", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, err := normalizedCreemWebhookEvent(creemEnvironmentTestEvent(
				test.checkoutMode, test.orderMode, test.productMode, test.customerMode,
			))
			require.NotNil(t, event)
			if test.wantErr {
				require.Error(t, err)
				assert.True(t, event.ManualReview)
				assert.False(t, event.Paid)
				assert.Nil(t, event.ProviderLivemode)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, event.ProviderLivemode)
			assert.Equal(t, test.wantLive, *event.ProviderLivemode)
			assert.True(t, event.Paid)
			assert.False(t, event.ManualReview)
		})
	}
}

func TestInvalidCreemEnvironmentIsPersistedAndCanonicalOrderIsNotSettled(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentOrder{}, &model.PaymentEvent{}, &model.TopUp{}, &model.SubscriptionOrder{},
	))
	livemode := true
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_CREEM_ENVIRONMENT", UserID: 990201, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderCreem, PaymentMethod: model.PaymentMethodCreem,
		ProviderLivemode: &livemode, RequestID: "request_creem_environment",
		ExpectedAmountMinor: 1000, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: model.PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	normalized, normalizeErr := normalizedCreemWebhookEvent(
		creemEnvironmentTestEvent("local", "", "local", "local"),
	)
	require.Error(t, normalizeErr)
	handled, err := processCanonicalRetainedPaymentEvent(normalized)
	require.True(t, handled)
	require.NoError(t, err)

	require.NoError(t, db.First(order, order.ID).Error)
	assert.Equal(t, model.PaymentOrderStatusManualReview, order.Status)
	assert.Zero(t, order.SettledAt)
	assert.Zero(t, order.PaidAmountMinor)
	var inbox model.PaymentEvent
	require.NoError(t, db.Where("provider = ? AND event_key = ?", model.PaymentProviderCreem, normalized.EventKey).First(&inbox).Error)
	assert.Equal(t, model.PaymentEventStatusManualReview, inbox.Status)
	assert.False(t, inbox.Paid)
}
