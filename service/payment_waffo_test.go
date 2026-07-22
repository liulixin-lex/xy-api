package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaffoCreateAndRecoverStartResolverPreservesConfiguredDeepLinkAndWebFallback(t *testing.T) {
	originalSchemes := setting.WaffoAppRedirectSchemes
	originalHosts := setting.WaffoWebRedirectHosts
	t.Cleanup(func() {
		setting.WaffoAppRedirectSchemes = originalSchemes
		setting.WaffoWebRedirectHosts = originalHosts
	})
	setting.WaffoAppRedirectSchemes = "weixin"
	setting.WaffoWebRedirectHosts = ""

	const deeplink = "weixin://wap/pay?prepayid=wx123#provider-state"
	const webURL = "https://cashier.waffo.com/checkout/order_123?fallback=1"
	action := `{"actionType":"DEEPLINK","deeplinkUrl":"` + deeplink + `","webUrl":"` + webURL + `"}`

	flow, paymentURL, err := resolveWaffoPaymentStart(action)
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowAppRedirect, flow)
	assert.Equal(t, deeplink, paymentURL)

	setting.WaffoAppRedirectSchemes = ""
	flow, paymentURL, err = resolveWaffoPaymentStart(action)
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowHostedRedirect, flow)
	assert.Equal(t, webURL, paymentURL)

	_, _, err = resolveWaffoPaymentStart(
		`{"actionType":"DEEPLINK","deeplinkUrl":"weixin://wap/pay?prepayid=wx123"}`,
	)
	assert.Error(t, err)
}

func TestPublicPaymentCheckoutKeepsWaffoAppDeepLinkServerSide(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "waffo-public-checkout-key-0123456789abcdef")
	const tradeNo = "PO_WAFFO_APP_PUBLIC"
	const deeplink = "weixin://wap/pay?prepayid=wx123#provider-state"
	payload, err := common.Marshal(PaymentStart{
		Flow: PaymentFlowAppRedirect, TradeNo: tradeNo, URL: deeplink, ExpiresAt: 4_102_444_800,
	})
	require.NoError(t, err)
	encrypted, err := model.EncryptPaymentOrderStartPayload(tradeNo, string(payload))
	require.NoError(t, err)

	checkout, err := PublicPaymentCheckout(&model.PaymentOrder{
		TradeNo: tradeNo, Provider: model.PaymentProviderWaffo,
		PaymentMethod: model.PaymentMethodWaffo, StartPayload: encrypted,
		Status: model.PaymentOrderStatusPending, ExpiresAt: 4_102_444_800,
	})
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowHostedRedirect, checkout.Flow)
	assert.Equal(t, "/api/user/payment/orders/"+tradeNo+"/continue", checkout.ContinueURL)

	publicJSON, err := common.Marshal(checkout)
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(string(publicJSON)), "weixin")
	assert.NotContains(t, string(publicJSON), "prepayid")
}

func TestPublicPaymentCheckoutHidesTerminalInstructions(t *testing.T) {
	checkout, err := PublicPaymentCheckout(&model.PaymentOrder{
		TradeNo: "PO_TERMINAL_PUBLIC", Status: model.PaymentOrderStatusFailed,
		StartFlow: PaymentFlowQR, StartPayload: "stale-encrypted-provider-instructions",
		ExpiresAt: 4_102_444_800,
	})
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowPending, checkout.Flow)
	assert.Empty(t, checkout.QRContent)
	assert.Empty(t, checkout.ContinueURL)
	assert.Nil(t, checkout.JSAPI)
}
