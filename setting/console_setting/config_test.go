package console_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApiInfoActionVisibilityDefaultsToShown(t *testing.T) {
	assert.True(t, defaultConsoleSetting.ApiInfoTestLatencyEnabled)
	assert.True(t, defaultConsoleSetting.ApiInfoExternalSpeedTestEnabled)
	assert.True(t, defaultConsoleSetting.ApiInfoOpenNewTabEnabled)
}

func TestApiInfoActionVisibilityCanBeLoadedFromConfig(t *testing.T) {
	settings := defaultConsoleSetting

	require.NoError(t, config.UpdateConfigFromMap(&settings, map[string]string{
		"api_info_test_latency_enabled":        "false",
		"api_info_external_speed_test_enabled": "true",
		"api_info_open_new_tab_enabled":        "false",
	}))

	assert.False(t, settings.ApiInfoTestLatencyEnabled)
	assert.True(t, settings.ApiInfoExternalSpeedTestEnabled)
	assert.False(t, settings.ApiInfoOpenNewTabEnabled)
}
