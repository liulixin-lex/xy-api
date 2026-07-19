package controller

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPasswordResetURLUsesStructuredQueryEncoding(t *testing.T) {
	resetURL, err := buildPasswordResetURL(
		"https://console.example.com/base/",
		"user+alerts@example.com",
		"token&admin=true",
	)
	require.NoError(t, err)

	parsed, err := url.Parse(resetURL)
	require.NoError(t, err)
	assert.Equal(t, "https", parsed.Scheme)
	assert.Equal(t, "console.example.com", parsed.Host)
	assert.Equal(t, "/base/user/reset", parsed.Path)
	assert.Equal(t, "user+alerts@example.com", parsed.Query().Get("email"))
	assert.Equal(t, "token&admin=true", parsed.Query().Get("token"))
	assert.Len(t, parsed.Query(), 2)
}

func TestBuildPasswordResetURLRequiresTrustedTransportAndOriginShape(t *testing.T) {
	tests := []struct {
		name          string
		serverAddress string
		wantError     bool
	}{
		{name: "HTTPS", serverAddress: "https://console.example.com", wantError: false},
		{name: "localhost HTTP", serverAddress: "http://localhost:3000", wantError: false},
		{name: "loopback HTTP", serverAddress: "http://127.0.0.1:3000", wantError: false},
		{name: "external HTTP", serverAddress: "http://console.example.com", wantError: true},
		{name: "userinfo", serverAddress: "https://trusted.example@evil.example", wantError: true},
		{name: "existing query", serverAddress: "https://console.example.com?next=https://evil.example", wantError: true},
		{name: "fragment", serverAddress: "https://console.example.com/#redirect", wantError: true},
		{name: "javascript", serverAddress: "javascript:alert(1)", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildPasswordResetURL(test.serverAddress, "user@example.com", "token")
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
