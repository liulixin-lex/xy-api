package system_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultFetchSettingFiltersResolvedDomainAddresses(t *testing.T) {
	require.True(t, defaultFetchSetting.EnableSSRFProtection)
	require.False(t, defaultFetchSetting.AllowPrivateIp)
	require.True(t, defaultFetchSetting.ApplyIPFilterForDomain)
}
