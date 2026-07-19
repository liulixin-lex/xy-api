package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func configureEpayWebhookLimitTest(t *testing.T) {
	t.Helper()
	previousID, previousKey := operation_setting.EpayId, operation_setting.EpayKey
	previousGeneration := operation_setting.EpayCredentialGeneration
	t.Cleanup(func() {
		operation_setting.EpayId = previousID
		operation_setting.EpayKey = previousKey
		operation_setting.EpayCredentialGeneration = previousGeneration
	})
	operation_setting.EpayId = "limit_test_merchant"
	operation_setting.EpayKey = "limit_test_key"
	operation_setting.EpayCredentialGeneration = 1
}

func TestEpayWebhookRejectsExcessiveParameterCount(t *testing.T) {
	configureEpayWebhookLimitTest(t)

	query := make(url.Values, epayMaxWebhookParameters+1)
	for index := 0; index <= epayMaxWebhookParameters; index++ {
		query.Set("field_"+strconv.Itoa(index), "value")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/payment/epay/notify?"+query.Encode(), nil)

	_, err := (&epayPaymentProvider{}).VerifyWebhook(request)

	require.EqualError(t, err, "epay callback contains too many parameters")
}

func TestEpayWebhookRejectsChunkedOversizedBody(t *testing.T) {
	configureEpayWebhookLimitTest(t)
	body := "field=" + strings.Repeat("x", epayMaxWebhookBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", io.NopCloser(strings.NewReader(body)))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, err := (&epayPaymentProvider{}).VerifyWebhook(request)

	require.ErrorContains(t, err, "request body too large")
}

func TestEpayWebhookRejectsDuplicateValues(t *testing.T) {
	configureEpayWebhookLimitTest(t)
	request := httptest.NewRequest(http.MethodGet, "/api/payment/epay/notify?pid=one&pid=two", nil)

	_, err := (&epayPaymentProvider{}).VerifyWebhook(request)

	require.EqualError(t, err, "invalid epay parameter: pid")
}
