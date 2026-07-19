package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v86"
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
	setting.StripeConfigurationVerifiedFingerprint = StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
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
		"payment_status":      "paid",
		"amount_total":        "800",
		"currency":            "usd",
		"payment_intent":      "pi_account_bound",
		"customer":            "cus_account_bound",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return fmt.Sprintf(`{
		"id":%q,"object":"checkout.session","livemode":%s,
		"client_reference_id":%q,"metadata":{"trade_no":%q},
		"mode":%q,"payment_status":%q,"amount_total":%s,"currency":%q,
		"payment_intent":%q,"customer":%q
	}`,
		values["id"], values["livemode"], values["client_reference_id"], values["metadata_trade_no"],
		values["mode"], values["payment_status"], values["amount_total"], values["currency"],
		values["payment_intent"], values["customer"],
	)
}

func TestStripePaidCheckoutConfirmationBindsConfiguredAccount(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/checkout/sessions/cs_test_account_bound", request.URL.Path)
		assert.Equal(t, setting.StripeAccountId, request.Header.Get("Stripe-Account"))
		assert.True(t, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer sk_test_account_confirmation"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stripeCheckoutConfirmationPayload(nil)))
	})

	require.NoError(t, provider.confirmPaidCheckoutSession(t.Context(), confirmedStripeEvent()))
}

func TestStripePaidCheckoutConfirmationRejectsCrossAccountSession(t *testing.T) {
	provider := configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","param":"id"}}`))
	})

	err := provider.confirmPaidCheckoutSession(t.Context(), confirmedStripeEvent())
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
			err := provider.confirmPaidCheckoutSession(t.Context(), confirmedStripeEvent())
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

	require.NoError(t, provider.confirmPaidCheckoutSession(t.Context(), event))
	assert.False(t, requested)
}
