package operation_setting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayMethodsStorageJSONIsASCIIAndRoundTripsUnicodeLabels(t *testing.T) {
	methods := []map[string]string{{
		"name": "支付宝💳", "type": "alipay", "provider": "epay", "flow": "form_post",
	}}

	stored, err := PayMethodsStorageJSON(methods)
	require.NoError(t, err)
	assert.NotContains(t, stored, "支付宝")
	assert.Contains(t, stored, `\u652f\u4ed8\u5b9d`)
	assert.Contains(t, stored, `\ud83d\udcb3`)
	for _, character := range stored {
		assert.LessOrEqual(t, character, rune(127))
	}

	parsed, err := ParsePayMethodsByJsonString(stored)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	assert.Equal(t, "支付宝💳", parsed[0]["name"])
	assert.True(t, strings.HasPrefix(parsed[0]["route_id"], "pay_"))
}

func TestParsePayMethodsRejectsUnknownFields(t *testing.T) {
	_, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","secret":"must-not-be-exposed"}]`)
	assert.Error(t, err)

	methods, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","icon":"SiAlipay","color":"#1677ff","min_topup":"10"}]`)
	require.NoError(t, err)
	require.Len(t, methods, 1)
	assert.Equal(t, "form_post", methods[0]["flow"])

	_, err = ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","min_topup":"10001"}]`)
	assert.Error(t, err)
}

func TestParsePayMethodsRejectsReservedTypesOnEpay(t *testing.T) {
	for _, methodType := range []string{"stripe", "xorpay_native", "xorpay_alipay", "waffo_pancake"} {
		_, err := ParsePayMethodsByJsonString(fmt.Sprintf(
			`[{"name":"reserved","type":%q,"provider":"epay"}]`, methodType,
		))
		assert.Error(t, err)
	}
}

func TestParsePayMethodsPreservesHistoricalCustomEpayTypes(t *testing.T) {
	methods, err := ParsePayMethodsByJsonString(`[
		{"name":"Legacy product checkout","type":"creem","provider":"epay"},
		{"name":"Legacy payment options","type":"waffo","provider":"epay"}
	]`)
	require.NoError(t, err)
	require.Len(t, methods, 2)
	assert.Equal(t, "epay", methods[0]["provider"])
	assert.Equal(t, "creem", methods[0]["type"])
	assert.Equal(t, "form_post", methods[0]["flow"])
	assert.Equal(t, "epay", methods[1]["provider"])
	assert.Equal(t, "waffo", methods[1]["type"])
	assert.Equal(t, "form_post", methods[1]["flow"])
}

func TestParsePayMethodsUsesProviderAndExactEpayTypeAsIdentity(t *testing.T) {
	assert.NotEqual(t, paymentMethodIdentityKey("epay", "alipay"), paymentMethodIdentityKey("xorpay", "alipay"))

	methods, err := ParsePayMethodsByJsonString(`[
		{"name":"lowercase","type":"alipay","provider":"epay","public_method":"alipay"},
		{"name":"case-sensitive variant","type":"Alipay","provider":"epay","public_method":"alipay"},
		{"name":"XORPay Alipay","type":"xorpay_alipay","provider":"xorpay","public_method":"alipay"}
	]`)
	require.NoError(t, err)
	require.Len(t, methods, 3)
	assert.Equal(t, "alipay", methods[0]["type"])
	assert.Equal(t, "Alipay", methods[1]["type"])
	assert.NotEqual(t, methods[0]["route_id"], methods[1]["route_id"])
	assert.NotEqual(t, methods[0]["route_id"], methods[2]["route_id"])

	_, err = ParsePayMethodsByJsonString(`[
		{"name":"first","type":"Alipay","provider":"epay"},
		{"name":"duplicate","type":"Alipay","provider":"epay"}
	]`)
	assert.ErrorContains(t, err, "duplicate payment method")
}

func TestParsePayMethodsAddsOpaqueStablePublicAliases(t *testing.T) {
	first, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"xorpay_alipay","provider":"xorpay"}]`)
	require.NoError(t, err)
	second, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"xorpay_alipay","provider":"xorpay"}]`)
	require.NoError(t, err)

	routeID := first[0]["route_id"]
	assert.Equal(t, routeID, second[0]["route_id"])
	assert.Equal(t, PublicPaymentRouteID("xorpay", "xorpay_alipay"), routeID)
	assert.NotContains(t, routeID, "xorpay")
	assert.NotContains(t, routeID, "alipay")
	assert.Equal(t, "alipay", first[0]["public_method"])
	assert.Equal(t, "qr", first[0]["channel_alias"])
}

func TestParsePayMethodsSupportsEveryCatalogManagedHostedProvider(t *testing.T) {
	methods, err := ParsePayMethodsByJsonString(`[
		{"name":"Product checkout","type":"creem","provider":"creem"},
		{"name":"Payment options","type":"waffo","provider":"waffo"}
	]`)
	require.NoError(t, err)
	require.Len(t, methods, 2)
	assert.Equal(t, "hosted_redirect", methods[0]["flow"])
	assert.Equal(t, "online_payment", methods[0]["public_method"])
	assert.Equal(t, "product_checkout", methods[0]["channel_alias"])
	assert.Equal(t, "hosted_redirect", methods[1]["flow"])
	assert.Equal(t, "online_payment", methods[1]["public_method"])
	assert.Equal(t, "payment_options", methods[1]["channel_alias"])
}

func TestParsePayMethodsRejectsLeakingOrDuplicatePublicRouteIdentifiers(t *testing.T) {
	_, err := ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","route_id":"xorpay_alipay"}]`)
	assert.ErrorContains(t, err, "invalid public payment route id")
	_, err = ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","route_id":"xor_pay_alipay"}]`)
	assert.ErrorContains(t, err, "invalid public payment route id")
	_, err = ParsePayMethodsByJsonString(`[{"name":"Alipay","type":"alipay","provider":"epay","channel_alias":"stripe_com"}]`)
	assert.ErrorContains(t, err, "invalid public payment channel alias")

	_, err = ParsePayMethodsByJsonString(`[
		{"name":"Epay Alipay","type":"alipay","provider":"epay","route_id":"alipay_primary"},
		{"name":"XORPay Alipay","type":"xorpay_alipay","provider":"xorpay","route_id":"alipay_primary"}
	]`)
	assert.ErrorContains(t, err, "duplicate public payment route id")

	methods, err := ParsePayMethodsByJsonString(`[{"name":"Prepayment","type":"custom1","provider":"epay","route_id":"prepayment"}]`)
	require.NoError(t, err)
	assert.Equal(t, "prepayment", methods[0]["route_id"])
}

func TestContainsInternalPaymentProviderNameHandlesSeparatedVariants(t *testing.T) {
	for _, value := range []string{
		"XOR Pay", "xor_pay", "x.o.r.pay", "Stripe.com", "WAF-FO checkout", "Cre em", "E Pay", "e.p.a.y",
	} {
		assert.True(t, ContainsInternalPaymentProviderName(value), value)
	}
	for _, value := range []string{"prepayment", "repayment", "online payment", "card checkout"} {
		assert.False(t, ContainsInternalPaymentProviderName(value), value)
	}
}
