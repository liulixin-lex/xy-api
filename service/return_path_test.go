package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
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

func TestValidatePaymentCallbackOriginRejectsPathsAndInsecureRemoteOrigins(t *testing.T) {
	require.NoError(t, ValidatePaymentCallbackOrigin("https://payments.example.com/", true))
	require.NoError(t, ValidatePaymentCallbackOrigin("http://localhost:3000", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/base", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/%2F", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("https://payments.example.com/?tenant=1", true))
	assert.Error(t, ValidatePaymentCallbackOrigin("http://payments.example.com", true))
}

func TestValidateExternalPaymentURLAllowsOnlyHTTPSOrLocalHTTP(t *testing.T) {
	require.NoError(t, ValidateExternalPaymentURL("https://payments.example.com/callback", true))
	require.NoError(t, ValidateExternalPaymentURL("http://localhost:3000/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("http://payments.example.com/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("ftp://localhost/callback", true))
	assert.Error(t, ValidateExternalPaymentURL("javascript://localhost/callback", true))
}
