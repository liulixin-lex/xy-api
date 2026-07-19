package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTopUpInfoHidesConfiguredXorPayMethodsThatAreDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMidjourneyControllerBillingDB(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	originalAid := setting.XorPayAid
	originalSecret := setting.XorPayAppSecret
	originalCurrency := setting.XorPayCurrency
	originalEnabledMethods := setting.XorPayEnabledMethods
	originalPayMethods := operation_setting.PayMethods
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	paymentSetting := operation_setting.GetPaymentSetting()
	originalComplianceConfirmed := paymentSetting.ComplianceConfirmed
	originalComplianceVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		setting.XorPayAid = originalAid
		setting.XorPayAppSecret = originalSecret
		setting.XorPayCurrency = originalCurrency
		setting.XorPayEnabledMethods = originalEnabledMethods
		operation_setting.PayMethods = originalPayMethods
		operation_setting.CustomCallbackAddress = originalCallbackAddress
		paymentSetting.ComplianceConfirmed = originalComplianceConfirmed
		paymentSetting.ComplianceTermsVersion = originalComplianceVersion
	})

	setting.XorPayAid = "xorpay_test_aid"
	setting.XorPayAppSecret = "xorpay_test_secret"
	setting.XorPayCurrency = "CNY"
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodNative}
	operation_setting.CustomCallbackAddress = "https://api.example.com"
	operation_setting.PayMethods = []map[string]string{
		{"name": "XORPay WeChat", "type": model.PaymentMethodXorPayNative, "provider": model.PaymentProviderXorPay},
		{"name": "XORPay Alipay", "type": model.PaymentMethodXorPayAlipay, "provider": model.PaymentProviderXorPay},
	}
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/user/topup", nil)
	GetTopUpInfo(context)

	require.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), model.PaymentMethodXorPayNative)
	assert.NotContains(t, recorder.Body.String(), model.PaymentMethodXorPayAlipay)
}
