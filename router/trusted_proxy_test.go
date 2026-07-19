package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureTrustedProxiesIgnoresForwardedHeadersByDefault(t *testing.T) {
	t.Setenv(trustedProxyCIDRsEnv, "")
	engine := gin.New()
	require.NoError(t, ConfigureTrustedProxies(engine))

	assert.False(t, engine.ForwardedByClientIP)
	assert.Nil(t, engine.RemoteIPHeaders)
	assert.Equal(t, "198.51.100.20", clientIPForTrustedProxyTest(t, engine, "198.51.100.20:1234", "203.0.113.77"))
}

func TestConfigureTrustedProxiesUsesForwardedHeadersOnlyFromConfiguredCIDR(t *testing.T) {
	t.Setenv(trustedProxyCIDRsEnv, "127.0.0.1/32")
	engine := gin.New()
	require.NoError(t, ConfigureTrustedProxies(engine))

	assert.True(t, engine.ForwardedByClientIP)
	assert.Equal(t, []string{"X-Forwarded-For", "X-Real-IP"}, engine.RemoteIPHeaders)
	assert.Equal(t, "203.0.113.77", clientIPForTrustedProxyTest(t, engine, "127.0.0.1:1234", "203.0.113.77"))
	assert.Equal(t, "198.51.100.20", clientIPForTrustedProxyTest(t, engine, "198.51.100.20:1234", "203.0.113.77"))
}

func TestConfigureTrustedProxiesRejectsUnsafeOrInvalidCIDRs(t *testing.T) {
	testCases := []struct {
		name  string
		value string
	}{
		{name: "plain IP is ambiguous", value: "127.0.0.1"},
		{name: "empty list item", value: "127.0.0.1/32,"},
		{name: "all IPv4 addresses", value: "0.0.0.0/0"},
		{name: "all IPv6 addresses", value: "::/0"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(trustedProxyCIDRsEnv, testCase.value)
			engine := gin.New()
			require.Error(t, ConfigureTrustedProxies(engine))
			assert.False(t, engine.ForwardedByClientIP)
			assert.Nil(t, engine.RemoteIPHeaders)
			assert.Equal(t, "198.51.100.20", clientIPForTrustedProxyTest(t, engine, "198.51.100.20:1234", "203.0.113.77"))
		})
	}
}

func clientIPForTrustedProxyTest(t *testing.T, engine *gin.Engine, remoteAddr, forwardedFor string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = remoteAddr
	request.Header.Set("X-Forwarded-For", forwardedFor)
	request.Header.Set("X-Real-IP", forwardedFor)
	context := gin.CreateTestContextOnly(httptest.NewRecorder(), engine)
	context.Request = request
	return context.ClientIP()
}
