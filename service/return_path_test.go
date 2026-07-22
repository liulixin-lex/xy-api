package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentReturnURLSupportsDefaultAndClassicThemes(t *testing.T) {
	originalTheme := common.GetTheme()
	originalAddress := system_setting.ServerAddress
	t.Cleanup(func() {
		common.SetTheme(originalTheme)
		system_setting.ServerAddress = originalAddress
	})
	system_setting.ServerAddress = "https://example.com/"

	common.SetTheme("default")
	assert.Equal(t,
		"https://example.com/wallet?payment_result=pending&trade_no=PO123",
		PaymentReturnURL("/console/topup?payment_result=pending&trade_no=PO123"),
	)

	common.SetTheme("classic")
	assert.Equal(t,
		"https://example.com/console/topup?payment_result=pending&trade_no=PO123",
		PaymentReturnURL("/console/topup?payment_result=pending&trade_no=PO123"),
	)
}

func TestFirstPartyPaymentReturnURLIsThemeIndependent(t *testing.T) {
	originalTheme := common.GetTheme()
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		common.SetTheme(originalTheme)
		operation_setting.CustomCallbackAddress = originalCallbackAddress
	})
	operation_setting.CustomCallbackAddress = "https://payments.example.com/"

	for _, theme := range []string{"default", "classic"} {
		common.SetTheme(theme)
		paymentURL, err := firstPartyPaymentReturnURL("PO/return path")
		require.NoError(t, err)
		assert.Equal(t, "https://payments.example.com/payment/PO%2Freturn%20path", paymentURL)
	}
}

func TestValidatePaymentCallbackOriginRejectsPathsAndInsecureRemoteOrigins(t *testing.T) {
	require.NoError(t, ValidatePaymentCallbackOrigin("https://payments.example.com/", true))
	require.NoError(t, ValidatePaymentCallbackOrigin("https://8.8.8.8", true))
	require.NoError(t, ValidatePaymentCallbackOrigin("https://[2606:4700:4700::1111]", true))
	require.NoError(t, ValidatePaymentCallbackOrigin("http://localhost:3000", true))
	require.NoError(t, ValidatePaymentCallbackOrigin("http://api.localhost:3000", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/base", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/%2F", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/?tenant=1", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("http://payments.example.com", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("http://localhost:3000", false))

	for _, raw := range []string{
		"https://localhost",
		"https://localhost.",
		"https://api.localhost",
		"https://127.0.0.1",
		"https://127.0.0.2",
		"https://10.0.0.1",
		"https://100.64.0.1",
		"https://169.254.169.254",
		"https://192.0.2.1",
		"https://224.0.0.1",
		"https://[::1]",
		"https://[fc00::1]",
		"https://[fe80::1]",
		"https://[2001:db8::1]",
		"https://[::ffff:127.0.0.1]",
		"https://2130706433",
		"https://0x7f000001",
		"https://017700000001",
		"https://127.1",
		"https://0177.0.0.1",
		"https://0x7f.0.0.1",
	} {
		assert.Error(t, ValidatePaymentCallbackOrigin(raw, true), raw)
	}
}

func TestValidateExternalPaymentURLAllowsOnlyHTTPSOrLocalHTTP(t *testing.T) {
	require.NoError(t, ValidateExternalPaymentURL("https://payments.example.com/callback", true))
	require.NoError(t, ValidateExternalPaymentURL("https://[2606:4700:4700::1111]:443/callback", true))
	require.NoError(t, ValidateExternalPaymentURL("http://localhost:3000/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("http://payments.example.com/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("ftp://localhost/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("javascript://localhost/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("https://example.com;script-src/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("https://exa_mple.com/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("https://payments.example.com:70000/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("https://user@payments.example.com/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("https://payments.example.com/callback#fragment", true))
}
