package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseXorPayEnabledMethodsValidatesWithoutMutatingGlobalState(t *testing.T) {
	original := XorPayEnabledMethods
	t.Cleanup(func() { XorPayEnabledMethods = original })
	XorPayEnabledMethods = []string{XorPayMethodNative}

	parsed, err := ParseXorPayEnabledMethods(`[" ALIPAY ","native","alipay"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{XorPayMethodAlipay, XorPayMethodNative}, parsed)
	assert.Equal(t, []string{XorPayMethodNative}, XorPayEnabledMethods)

	_, err = ParseXorPayEnabledMethods(`["unknown"]`)
	require.Error(t, err)
	assert.Equal(t, []string{XorPayMethodNative}, XorPayEnabledMethods)
}
