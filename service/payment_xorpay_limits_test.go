package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
)

func configureXorPayWebhookLimitTest(t *testing.T) {
	t.Helper()
	previousAid, previousSecret := setting.XorPayAid, setting.XorPayAppSecret
	previousGeneration := setting.XorPayCredentialGeneration
	t.Cleanup(func() {
		setting.XorPayAid = previousAid
		setting.XorPayAppSecret = previousSecret
		setting.XorPayCredentialGeneration = previousGeneration
	})
	setting.XorPayAid = "limit_test_aid"
	setting.XorPayAppSecret = "limit_test_secret"
	setting.XorPayCredentialGeneration = 1
}

func TestDecodeXorPayResponseRejectsOversizedPayload(t *testing.T) {
	var response xorPayQueryResponse
	err := decodeXorPayResponse(
		strings.NewReader(strings.Repeat(" ", xorPayMaxResponseBytes)+"{}"),
		&response,
	)

	require.EqualError(t, err, "xorpay response exceeds size limit")
}

func TestDecodeXorPayResponseAcceptsExactSizeLimit(t *testing.T) {
	prefix := `{"status":"new"}`
	payload := prefix + strings.Repeat(" ", xorPayMaxResponseBytes-len(prefix))
	var response xorPayQueryResponse

	err := decodeXorPayResponse(strings.NewReader(payload), &response)

	require.NoError(t, err)
	require.Equal(t, "new", response.Status)
}

func TestXorPayWebhookRejectsExcessiveParameterCount(t *testing.T) {
	configureXorPayWebhookLimitTest(t)

	form := make(url.Values, xorPayMaxWebhookParameters+1)
	for index := 0; index <= xorPayMaxWebhookParameters; index++ {
		form.Set("field_"+strings.Repeat("x", index), "value")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, err := (&xorPayProvider{}).VerifyWebhook(request)

	require.EqualError(t, err, "xorpay callback contains too many parameters")
}

func TestXorPayWebhookRejectsChunkedOversizedBody(t *testing.T) {
	configureXorPayWebhookLimitTest(t)
	body := "field=" + strings.Repeat("x", xorPayMaxWebhookBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", io.NopCloser(strings.NewReader(body)))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, err := (&xorPayProvider{}).VerifyWebhook(request)

	require.ErrorContains(t, err, "request body too large")
}

func TestXorPayWebhookRejectsDuplicateValues(t *testing.T) {
	configureXorPayWebhookLimitTest(t)
	request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", strings.NewReader("aoid=one&aoid=two"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, err := (&xorPayProvider{}).VerifyWebhook(request)

	require.EqualError(t, err, "invalid xorpay parameter: aoid")
}
