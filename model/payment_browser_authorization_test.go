package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentBrowserAuthorizationIsOneUseAndEncrypted(t *testing.T) {
	truncateTables(t)
	t.Setenv("PAYMENT_SECRET_KEY", "payment-browser-authorization-key-at-least-32-bytes")
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_JSAPI_AUTHORIZATION", UserID: 979301, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayJSAPI,
		RequestID: "jsapi-authorization", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	state := "authorization_state_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	_, err := BeginPaymentBrowserAuthorization(order.UserID, order.TradeNo, state)
	require.NoError(t, err)

	stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	require.NotNil(t, stored.BrowserAuthorizationDigest)
	assert.NotContains(t, *stored.BrowserAuthorizationDigest, state)
	assert.Empty(t, stored.BrowserAuthorizationPayload)

	openID := "oXorPayUser_0123456789"
	completed, err := CompletePaymentBrowserAuthorization(state, openID)
	require.NoError(t, err)
	assert.Nil(t, completed.BrowserAuthorizationDigest)
	assert.NotEmpty(t, completed.BrowserAuthorizationPayload)
	assert.NotContains(t, completed.BrowserAuthorizationPayload, openID)
	decrypted, err := PaymentOrderBrowserAuthorization(completed)
	require.NoError(t, err)
	assert.Equal(t, openID, decrypted)

	// Once an OpenID has been accepted, another authorization start must not
	// clear or replace it while the worker is preparing the provider request.
	_, err = BeginPaymentBrowserAuthorization(order.UserID, order.TradeNo,
		"replacement_authorization_state_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	assert.ErrorIs(t, err, ErrPaymentBrowserAuthorizationInvalid)
	stored, err = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, completed.BrowserAuthorizationPayload, stored.BrowserAuthorizationPayload)

	_, err = CompletePaymentBrowserAuthorization(state, openID)
	assert.ErrorIs(t, err, ErrPaymentBrowserAuthorizationInvalid)
	_, err = DecryptPaymentOrderBrowserAuthorization("PO_OTHER_ORDER", completed.BrowserAuthorizationPayload)
	assert.Error(t, err)

	// The OpenID is needed only for provider creation. Committing the encrypted
	// browser start snapshot must remove it immediately from durable storage.
	require.NoError(t, SavePaymentOrderStartWithProviderIdentity(
		order.TradeNo, "jsapi", `{"flow":"jsapi"}`, now+300, "xorpay:AOID_JSAPI", "",
	))
	stored, err = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Nil(t, stored.BrowserAuthorizationDigest)
	assert.Empty(t, stored.BrowserAuthorizationPayload)
	assert.Zero(t, stored.BrowserAuthorizationExpiresAt)
	assert.Zero(t, stored.BrowserAuthorizedAt)
}

func TestPaymentBrowserAuthorizationRejectsExpiredOrder(t *testing.T) {
	truncateTables(t)
	t.Setenv("PAYMENT_SECRET_KEY", "payment-browser-expiry-key-at-least-32-bytes")
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_JSAPI_EXPIRED", UserID: 979302, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayJSAPI,
		RequestID: "jsapi-expired", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: PaymentOrderStatusPending,
		ExpiresAt: now - 1, CreatedAt: now - 60, UpdatedAt: now - 60, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	_, err := BeginPaymentBrowserAuthorization(order.UserID, order.TradeNo,
		"expired_authorization_state_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	assert.ErrorIs(t, err, ErrPaymentBrowserAuthorizationInvalid)
}
