package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
)

func configureStripeAccountConfirmationTest(t *testing.T, handler http.HandlerFunc) *stripePaymentProvider {
	t.Helper()
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	originalBackend := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0),
	}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, originalBackend) })

	originalSecret := setting.StripeApiSecret
	originalCredentialAccountID := setting.StripeCredentialAccountId
	originalConnectedAccountID := setting.StripeAccountId
	originalPriceID := setting.StripePriceId
	originalCurrency := setting.StripeCurrency
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalWebhookGeneration := setting.StripeWebhookCredentialGeneration
	originalFingerprint := setting.StripeConfigurationVerifiedFingerprint
	originalVerifiedAt := setting.StripeConfigurationVerifiedAt
	t.Cleanup(func() {
		setting.StripeApiSecret = originalSecret
		setting.StripeCredentialAccountId = originalCredentialAccountID
		setting.StripeAccountId = originalConnectedAccountID
		setting.StripePriceId = originalPriceID
		setting.StripeCurrency = originalCurrency
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalWebhookGeneration
		setting.StripeConfigurationVerifiedFingerprint = originalFingerprint
		setting.StripeConfigurationVerifiedAt = originalVerifiedAt
	})

	setting.StripeApiSecret = "sk_test_account_confirmation"
	setting.StripeCredentialAccountId = "acct_platformconfirmation"
	setting.StripeAccountId = "acct_connectedconfirmation"
	setting.StripePriceId = "price_confirmation"
	setting.StripeCurrency = "USD"
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 2
	setting.StripeConfigurationVerifiedFingerprint = StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode, setting.StripeCheckoutAllowedHosts,
	)
	setting.StripeConfigurationVerifiedAt = time.Now().Unix()
	return &stripePaymentProvider{}
}

func confirmedStripeEvent() *NormalizedPaymentEvent {
	livemode := false
	return &NormalizedPaymentEvent{
		Provider: model.PaymentProviderStripe, Paid: true, ProviderLivemode: &livemode,
		TradeNo: "PO_STRIPE_ACCOUNT_BOUND", ProviderOrderKey: "stripe:cs_test_account_bound",
		ProviderPaymentKey: "stripe:pi_account_bound", CustomerID: "cus_account_bound",
		PaidAmountMinor: 800, Currency: "USD",
	}
}

func stripeCheckoutConfirmationPayload(overrides map[string]string) string {
	values := map[string]string{
		"id":                  "cs_test_account_bound",
		"livemode":            "false",
		"client_reference_id": "PO_STRIPE_ACCOUNT_BOUND",
		"metadata_trade_no":   "PO_STRIPE_ACCOUNT_BOUND",
		"mode":                "payment",
		"status":              "complete",
		"payment_status":      "paid",
		"amount_total":        "800",
		"currency":            "usd",
		"payment_intent":      "pi_account_bound",
		"customer":            "cus_account_bound",
		"url":                 "https://checkout.stripe.com/c/pay/account-bound",
		"expires_at":          fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()),
	}
	for key, value := range overrides {
		values[key] = value
	}
	return fmt.Sprintf(`{
		"id":%q,"object":"checkout.session","livemode":%s,
		"client_reference_id":%q,"metadata":{"trade_no":%q},
		"mode":%q,"status":%q,"payment_status":%q,"amount_total":%s,"currency":%q,
		"payment_intent":%q,"customer":%q,"url":%q,"expires_at":%s
	}`,
		values["id"], values["livemode"], values["client_reference_id"], values["metadata_trade_no"],
		values["mode"], values["status"], values["payment_status"], values["amount_total"], values["currency"],
		values["payment_intent"], values["customer"], values["url"], values["expires_at"],
	)
}

func stripeCheckoutContractOrder() *model.PaymentOrder {
	livemode := false
	providerOrderKey := "stripe:cs_test_account_bound"
	return &model.PaymentOrder{
		TradeNo: "PO_STRIPE_ACCOUNT_BOUND", Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, ProviderOrderKey: &providerOrderKey,
		ProviderLivemode: &livemode, ExpectedAmountMinor: 800, Currency: "USD",
		Status: model.PaymentOrderStatusPending, ExpiresAt: time.Now().Add(40 * time.Minute).Unix(),
	}
}

func TestStripePaidCheckoutConfirmationBindsConfiguredAccount(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		assert.True(t, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer sk_test_account_confirmation"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(nil)))
	})

	require.NoError(t, provider.confirmCheckoutSessionAuthority(t.Context(), confirmedStripeEvent()))
}

func TestStripePaidCheckoutConfirmationSurvivesCatalogDisable(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(nil)))
	})
	setting.StripePriceId = ""
	setting.StripeCurrency = "EUR"
	setting.StripeConfigurationVerifiedFingerprint = ""
	setting.StripeConfigurationVerifiedAt = 0

	require.NoError(t, provider.confirmCheckoutSessionAuthority(t.Context(), confirmedStripeEvent()))
}

func TestStripeFailedCheckoutWebhookRequiresConfiguredAccountAuthority(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		wantError bool
	}{
		{
			name: "matching account session",
			overrides: map[string]string{
				"status": "complete", "payment_status": "unpaid", "payment_intent": "", "customer": "",
			},
		},
		{
			name: "different account session contract",
			overrides: map[string]string{
				"client_reference_id": "PO_OTHER_ACCOUNT", "metadata_trade_no": "PO_OTHER_ACCOUNT",
				"status": "complete", "payment_status": "unpaid", "payment_intent": "", "customer": "",
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
				assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(test.overrides)))
			})
			livemode := false
			event := &NormalizedPaymentEvent{
				Provider: model.PaymentProviderStripe, EventType: string(stripe.EventTypeCheckoutSessionAsyncPaymentFailed),
				TradeNo: "PO_STRIPE_ACCOUNT_BOUND", ProviderOrderKey: "stripe:cs_test_account_bound",
				ProviderLivemode: &livemode, PaidAmountMinor: 800, Currency: "USD", Failed: true,
				VerifiedPayload: []byte(`{"verified":true}`),
			}
			err := provider.ValidateVerifiedWebhook(t.Context(), event)
			if test.wantError {
				assert.ErrorIs(t, err, errStripeCheckoutConfirmation)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestStripeSignedTestPaymentIsRetainedAfterTestModeIsDisabled(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(nil)))
	})
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	originalWebhookSecret := setting.StripeWebhookSecret
	setting.StripeWebhookSecret = "whsec_test_mode_disabled"
	t.Cleanup(func() { setting.StripeWebhookSecret = originalWebhookSecret })

	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.User{}, &model.TopUp{}, &model.PaymentOrder{}, &model.PaymentEvent{},
		&model.PaymentLedgerEntry{},
	))
	const userID = 995900
	const tradeNo = "PO_STRIPE_ACCOUNT_BOUND"
	model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentEvent{})
	model.DB.Where("trade_no = ?", tradeNo).Delete(&model.TopUp{})
	model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentOrder{})
	model.DB.Unscoped().Delete(&model.User{}, userID)
	t.Cleanup(func() {
		model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentEvent{})
		model.DB.Where("trade_no = ?", tradeNo).Delete(&model.TopUp{})
		model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentOrder{})
		model.DB.Unscoped().Delete(&model.User{}, userID)
	})
	user := &model.User{
		Id: userID, Username: "stripe_test_mode_disabled", AffCode: "stripe_test_mode_disabled", Quota: 321,
	}
	require.NoError(t, model.DB.Create(user).Error)
	now := time.Now().Unix()
	providerOrderKey := "stripe:cs_test_account_bound"
	livemode := false
	order := &model.PaymentOrder{
		TradeNo: tradeNo, UserID: userID, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderStripe, PaymentMethod: model.PaymentMethodStripe,
		RequestID: "stripe_test_mode_disabled", ExpectedAmountMinor: 800, Currency: "USD",
		RequestedAmount: 1, CreditQuota: 500, Status: model.PaymentOrderStatusPending,
		ProviderOrderKey: &providerOrderKey, ProviderLivemode: &livemode,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	projection := &model.TopUp{
		PaymentOrderId: &order.ID, UserId: userID, Amount: order.CreditQuota,
		Money: 8, TradeNo: order.TradeNo, PaymentMethod: model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending,
		CreateTime: now,
	}
	require.NoError(t, model.DB.Create(projection).Error)

	payload := []byte(fmt.Sprintf(`{
		"id":"evt_test_mode_disabled","object":"event","api_version":%q,
		"account":"acct_connectedconfirmation","created":%d,"livemode":false,
		"type":"checkout.session.completed",
		"data":{"object":%s}
	}`, stripe.APIVersion, now, stripeCheckoutConfirmationPayload(nil)))
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload,
		Secret:  setting.StripeWebhookSecret,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	require.True(t, event.Paid)
	require.NoError(t, provider.ValidateVerifiedWebhook(t.Context(), event))

	result, err := ProcessNormalizedPaymentEvent(event)
	require.ErrorIs(t, err, model.ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	storedOrder, err := model.GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusManualReview, storedOrder.Status)
	var storedProjection model.TopUp
	require.NoError(t, model.DB.First(&storedProjection, projection.Id).Error)
	assert.Equal(t, common.TopUpStatusManualReview, storedProjection.Status)
	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, userID).Error)
	assert.Equal(t, 321, storedUser.Quota)
	var ledgerCount int64
	require.NoError(t, model.DB.Model(&model.PaymentLedgerEntry{}).Where("user_id = ?", userID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
}

func TestStripeCreateUsesServerAuthoritativeOrderExpiry(t *testing.T) {
	var sentExpiresAt int64
	var checkoutRequests int
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/prices/price_confirmation":
			_, _ = w.Write([]byte(`{"id":"price_confirmation","object":"price","active":true,"currency":"usd","product":"prod_confirmation"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/checkout/sessions":
			require.NoError(t, request.ParseForm())
			checkoutRequests++
			assert.Equal(t, "payment:PO_STRIPE_EXPIRY_MARGIN", request.Header.Get("Idempotency-Key"))
			assert.Equal(t, string(stripe.CheckoutSessionModePayment), request.Form.Get("mode"))
			assert.Empty(t, request.Form.Get("subscription_data"))
			var err error
			sentExpiresAt, err = strconv.ParseInt(request.Form.Get("expires_at"), 10, 64)
			require.NoError(t, err)
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"id":"cs_test_expiry_margin","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/expiry-margin","expires_at":%d}`,
				sentExpiresAt,
			)))
		default:
			http.NotFound(w, request)
		}
	})

	originalCallbackAddress := operation_setting.CustomCallbackAddress
	operation_setting.CustomCallbackAddress = "https://api.example.com"
	t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallbackAddress })

	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	const userID = 995901
	model.DB.Unscoped().Delete(&model.User{}, userID)
	t.Cleanup(func() { model.DB.Unscoped().Delete(&model.User{}, userID) })
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: "stripe_expiry_margin", AffCode: "stripe_expiry_margin",
	}).Error)

	livemode := false
	orderExpiresAt := time.Now().Add(40 * time.Minute).Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_STRIPE_EXPIRY_MARGIN", UserID: userID, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderStripe, PaymentMethod: model.PaymentMethodStripe,
		ExpectedAmountMinor: 800, Currency: "USD", ProviderLivemode: &livemode,
		ExpiresAt: orderExpiresAt,
	}
	start, err := provider.Create(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, sentExpiresAt, start.ExpiresAt)
	assert.Equal(t, orderExpiresAt, sentExpiresAt)

	// An ambiguous network result is retried with the same Checkout parameters
	// and idempotency key, so Stripe returns the original Session instead of
	// creating a second charge attempt.
	retried, err := provider.Create(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, start.ProviderOrderKey, retried.ProviderOrderKey)
	assert.Equal(t, start.URL, retried.URL)
	assert.Equal(t, 2, checkoutRequests)
}

func TestStripeCheckoutExpiryEnforcesStripeWindow(t *testing.T) {
	const now = int64(1_900_000_000)
	minimum := now + int64((30*time.Minute+stripeCheckoutSessionExpirySafetyGap)/time.Second)
	maximum := now + int64(stripeCheckoutSessionMaxLifetime/time.Second)

	expiresAt, err := stripeCheckoutExpiresAt(minimum, now)
	require.NoError(t, err)
	assert.Equal(t, minimum, expiresAt)
	expiresAt, err = stripeCheckoutExpiresAt(maximum, now)
	require.NoError(t, err)
	assert.Equal(t, maximum, expiresAt)
	_, err = stripeCheckoutExpiresAt(minimum-1, now)
	assert.ErrorIs(t, err, errStripeCheckoutWindowTooShort)
	_, err = stripeCheckoutExpiresAt(maximum+1, now)
	assert.ErrorIs(t, err, errStripeCheckoutWindowTooLong)
}

func TestStripePaidCheckoutConfirmationRejectsCrossAccountSession(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","param":"id"}}`))
	})

	err := provider.confirmCheckoutSessionAuthority(t.Context(), confirmedStripeEvent())
	assert.ErrorIs(t, err, errStripeCheckoutConfirmation)
}

func TestStripePaidCheckoutConfirmationRejectsAuthorityMismatches(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{name: "session", overrides: map[string]string{"id": "cs_test_other"}},
		{name: "client reference", overrides: map[string]string{"client_reference_id": "PO_OTHER", "metadata_trade_no": "PO_OTHER"}},
		{name: "metadata conflict", overrides: map[string]string{"metadata_trade_no": "PO_OTHER"}},
		{name: "mode", overrides: map[string]string{"mode": "subscription"}},
		{name: "payment status", overrides: map[string]string{"payment_status": "unpaid"}},
		{name: "amount", overrides: map[string]string{"amount_total": "801"}},
		{name: "currency", overrides: map[string]string{"currency": "eur"}},
		{name: "livemode", overrides: map[string]string{"livemode": "true"}},
		{name: "payment intent", overrides: map[string]string{"payment_intent": "pi_other"}},
		{name: "customer", overrides: map[string]string{"customer": "cus_other"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(test.overrides)))
			})
			err := provider.confirmCheckoutSessionAuthority(t.Context(), confirmedStripeEvent())
			assert.ErrorIs(t, err, errStripeCheckoutConfirmation)
		})
	}
}

func TestStripePaidCheckoutConfirmationDoesNotQueryInventoryOnlyEvents(t *testing.T) {
	requested := false
	provider := configureStripeAccountConfirmationTest(t, func(http.ResponseWriter, *http.Request) {
		requested = true
	})
	event := confirmedStripeEvent()
	event.Paid = false

	require.NoError(t, provider.confirmCheckoutSessionAuthority(t.Context(), event))
	assert.False(t, requested)
}

func TestStripeQueryConfirmsCanonicalCheckoutContract(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(nil)))
	})

	event, err := provider.Query(t.Context(), stripeCheckoutContractOrder())
	require.NoError(t, err)
	assert.True(t, event.Paid)
	assert.False(t, event.Expired)
	assert.Equal(t, "stripe:pi_account_bound", event.ProviderPaymentKey)
	assert.Equal(t, "cus_account_bound", event.CustomerID)
	assert.Contains(t, event.NormalizedPayload, `"mode":"payment"`)
	assert.NotEmpty(t, event.EventKey)
}

func TestStripeQueryRejectsCheckoutContractMismatches(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}))
	tests := []struct {
		name        string
		overrides   map[string]string
		mutateOrder func(*model.PaymentOrder)
	}{
		{name: "recurring mode", overrides: map[string]string{"mode": "subscription"}},
		{name: "client reference", overrides: map[string]string{"client_reference_id": "PO_OTHER", "metadata_trade_no": "PO_OTHER"}},
		{name: "metadata conflict", overrides: map[string]string{"metadata_trade_no": "PO_OTHER"}},
		{name: "amount", overrides: map[string]string{"amount_total": "801"}},
		{name: "currency", overrides: map[string]string{"currency": "eur"}},
		{name: "livemode", overrides: map[string]string{"livemode": "true"}},
		{name: "unknown status", overrides: map[string]string{"status": "unexpected"}},
		{name: "paid open session", overrides: map[string]string{"status": "open"}},
		{name: "missing payment intent", overrides: map[string]string{"payment_intent": ""}},
		{name: "missing customer", overrides: map[string]string{"customer": ""}},
		{name: "bound payment intent", mutateOrder: func(order *model.PaymentOrder) {
			providerPaymentKey := "stripe:pi_other"
			order.ProviderPaymentKey = &providerPaymentKey
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(test.overrides)))
			})
			order := stripeCheckoutContractOrder()
			if test.mutateOrder != nil {
				test.mutateOrder(order)
			}
			_, err := provider.Query(t.Context(), order)
			assert.ErrorIs(t, err, model.ErrPaymentManualReview)
		})
	}
}

func TestStripeRecoverStartRequiresOpenUnpaidCanonicalSession(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(map[string]string{
			"status": "open", "payment_status": "unpaid",
		})))
	})

	order := stripeCheckoutContractOrder()
	start, err := provider.RecoverStart(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowHostedRedirect, start.Flow)
	assert.Equal(t, "https://checkout.stripe.com/c/pay/account-bound", start.URL)
	assert.Equal(t, order.ExpiresAt, start.ExpiresAt)
}

func TestStripeRecoverStartReturnsContractMismatchWithoutMutatingOrder(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}))
	const tradeNo = "PO_STRIPE_RECOVER_CONTRACT_MISMATCH"
	model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentOrder{})
	t.Cleanup(func() { model.DB.Where("trade_no = ?", tradeNo).Delete(&model.PaymentOrder{}) })

	order := stripeCheckoutContractOrder()
	order.TradeNo = tradeNo
	order.RequestID = "stripe_recover_contract_mismatch"
	order.CreatedAt = time.Now().Unix()
	order.UpdatedAt = order.CreatedAt
	order.Version = 1
	require.NoError(t, model.DB.Create(order).Error)

	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(map[string]string{
			"client_reference_id": tradeNo,
			"metadata_trade_no":   tradeNo,
			"mode":                "subscription",
			"status":              "open",
			"payment_status":      "unpaid",
		})))
	})

	_, err := provider.RecoverStart(t.Context(), order)
	require.ErrorIs(t, err, model.ErrPaymentManualReview)
	stored, err := model.GetPaymentOrderByTradeNo(tradeNo)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusPending, stored.Status)
	assert.Empty(t, stored.StatusReason)
	assert.Empty(t, stored.StartPayload)
}

func TestStripeRecoverStartRejectsRecurringAndClosedSessions(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{name: "recurring", overrides: map[string]string{"mode": "subscription", "status": "open", "payment_status": "unpaid"}},
		{name: "complete", overrides: map[string]string{"status": "complete", "payment_status": "unpaid"}},
		{name: "expired", overrides: map[string]string{"status": "expired", "payment_status": "unpaid"}},
		{name: "past expiry", overrides: map[string]string{"status": "open", "payment_status": "unpaid", "expires_at": "1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(test.overrides)))
			})
			_, err := provider.RecoverStart(t.Context(), stripeCheckoutContractOrder())
			assert.Error(t, err)
		})
	}
}
