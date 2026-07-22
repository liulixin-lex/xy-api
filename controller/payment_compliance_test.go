package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionBalancePayHidesComplianceConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})
	paymentSetting.ComplianceConfirmed = false
	paymentSetting.ComplianceTermsVersion = ""

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/subscription/balance/pay",
		strings.NewReader(`{"plan_id":1,"request_id":"compliance_contract"}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")

	SubscriptionRequestBalancePay(context)
	assert.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]interface{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, false, payload["success"])
	assert.Equal(t, "payment_temporarily_unavailable", payload["code"])
	assert.Equal(t, "payment_temporarily_unavailable", payload["message"])
	assert.NotContains(t, recorder.Body.String(), "compliance")
	assert.NotContains(t, recorder.Body.String(), "compliance terms")
	assert.NotContains(t, recorder.Body.String(), "合规")
}

func TestConfirmPaymentComplianceUsesStableAdministratorErrors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		accessToken bool
		status      int
		code        string
	}{
		{
			name:        "dashboard session required",
			body:        `{"confirmed":true}`,
			accessToken: true,
			status:      http.StatusForbidden,
			code:        "payment_settings_auth_required",
		},
		{
			name:   "confirmation required",
			body:   `{"confirmed":false}`,
			status: http.StatusBadRequest,
			code:   "payment_settings_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(
				http.MethodPost,
				"/api/option/payment/compliance",
				strings.NewReader(test.body),
			)
			context.Request.Header.Set("Content-Type", "application/json")
			context.Set("use_access_token", test.accessToken)

			ConfirmPaymentCompliance(context)

			assert.Equal(t, test.status, recorder.Code)
			var payload map[string]interface{}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Equal(t, false, payload["success"])
			assert.Equal(t, test.code, payload["code"])
			assert.NotContains(t, recorder.Body.String(), "dashboard session")
			assert.NotContains(t, recorder.Body.String(), "合规")
		})
	}
}
