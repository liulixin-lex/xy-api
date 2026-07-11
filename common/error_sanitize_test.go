package common

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeErrorMessageRemovesSecretsControlsAndUnsafeLength(t *testing.T) {
	knownSecret := "known-secret-long"
	message := string([]byte{0xff, 0xfe}) +
		" Authorization: Bearer auth-secret\r\n" +
		"Bearer bearer-secret api_key=key-secret password: pw-secret " +
		"Cookie: session=cookie-secret; other=second-secret\n" +
		"url=https://api.example.com/path?access_token=query-secret ip=169.254.169.254 " +
		knownSecret + "\t" + strings.Repeat("尾", SafeErrorMaxRunes+100)

	got := SanitizeErrorMessage(message, "known-secret", knownSecret)

	require.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, utf8.RuneCountInString(got), SafeErrorMaxRunes)
	for _, secret := range []string{
		"auth-secret",
		"bearer-secret",
		"key-secret",
		"pw-secret",
		"cookie-secret",
		"second-secret",
		"query-secret",
		"known-secret",
		knownSecret,
	} {
		assert.NotContains(t, got, secret)
	}
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\n")
	assert.NotContains(t, got, "\t")
	assert.Contains(t, got, "***")
}

func TestSanitizeErrorMessageRedactsGenericCredentialLabels(t *testing.T) {
	tests := []string{
		`authorization="Basic abc123"`,
		`access_token: token-value`,
		`refresh-token=refresh-value`,
		`api key: key-value`,
		`password='password-value'`,
		`passwd: passwd-value`,
		`pwd=pwd-value`,
		`cookie: session-value`,
		`secret=secret-value`,
	}

	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			got := SanitizeErrorMessage(message)
			assert.Contains(t, got, "***")
			assert.NotContains(t, got, "value")
			assert.NotContains(t, got, "abc123")
		})
	}
}

func TestSanitizeErrorMessageUsesLongestKnownSecretFirst(t *testing.T) {
	got := SanitizeErrorMessage("failed with secret-long and secret", "secret", "secret-long")

	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "long")
}

func TestSanitizeErrorMessageMasksURLsAndIPs(t *testing.T) {
	got := SanitizeErrorMessage("request https://api.example.com/v1/data?token=abc failed at 203.0.113.10")

	assert.NotContains(t, got, "api.example.com")
	assert.NotContains(t, got, "/v1/data")
	assert.NotContains(t, got, "203.0.113.10")
	assert.NotContains(t, got, "abc")
}
