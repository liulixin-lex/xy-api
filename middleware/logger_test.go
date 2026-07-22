package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeAccessLogPathRemovesWeChatAuthorizationSecrets(t *testing.T) {
	const callbackPath = "/api/payment/wechat/authorize/callback"
	logged := sanitizeAccessLogPath(callbackPath +
		"?state=authorization_state_0123456789&openid=oSensitiveOpenID_0123456789")
	assert.Equal(t, callbackPath, logged)
	assert.NotContains(t, logged, "state=")
	assert.NotContains(t, logged, "openid=")
	assert.Equal(t, "/api/payment/orders/example?state=public",
		sanitizeAccessLogPath("/api/payment/orders/example?state=public"))
}

func TestSanitizeAccessLogPathRemovesPaymentCallbackSecrets(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{
			path: "/api/payment/epay/notify?pid=merchant&sign=epay_signature",
			want: "/api/payment/epay/notify",
		},
		{
			path: "/api/user/epay/notify?sign=legacy_topup_signature",
			want: "/api/user/epay/notify",
		},
		{
			path: "/api/subscription/epay/notify?sign=legacy_subscription_signature",
			want: "/api/subscription/epay/notify",
		},
		{
			path: "/api/subscription/epay/return?sign=subscription_return_signature",
			want: "/api/subscription/epay/return",
		},
		{
			path: "/api/xorpay/notify?sign=xorpay_signature",
			want: "/api/xorpay/notify",
		},
		{
			path: "/api/stripe/webhook?token=unexpected_secret",
			want: "/api/stripe/webhook",
		},
		{
			path: "/api/creem/webhook?token=unexpected_secret",
			want: "/api/creem/webhook",
		},
		{
			path: "/api/waffo/webhook?signature=waffo_signature",
			want: "/api/waffo/webhook",
		},
		{
			path: "/api/waffo-pancake/webhook/prod?signature=pancake_signature",
			want: "/api/waffo-pancake/webhook/prod",
		},
	}

	for _, test := range tests {
		assert.Equal(t, test.want, sanitizeAccessLogPath(test.path))
		assert.NotContains(t, sanitizeAccessLogPath(test.path), "signature")
		assert.NotContains(t, sanitizeAccessLogPath(test.path), "token")
	}
}
