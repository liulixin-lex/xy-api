package smart_routing_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetSettingDefaultsAndNormalizesWeights(t *testing.T) {
	ResetForTest()

	setting := GetSetting()

	assert.False(t, setting.Enabled)
	assert.Equal(t, ModeObserve, setting.Mode)
	assert.Equal(t, 5, setting.Consecutive5xx)
	assert.Equal(t, 50, setting.FailureRatePct)
	assert.Equal(t, 3000, setting.FirstByteMinMs)
	assert.Equal(t, 12000, setting.FirstByteCapMs)
	assert.InDelta(t, 1.0, setting.WeightAvailability+setting.WeightLatency+setting.WeightThroughput+setting.WeightCost, 0.000001)
}

func TestGetSettingAppliesEnvOverridesEveryRead(t *testing.T) {
	ResetForTest()
	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", ModeBalanced)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "true")

	setting := GetSetting()

	assert.True(t, setting.Enabled)
	assert.Equal(t, ModeBalanced, setting.Mode)
	assert.True(t, setting.AgentEnabled)

	t.Setenv("SMART_ROUTING_ENABLED", "false")
	t.Setenv("SMART_ROUTING_MODE", "invalid")
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "false")

	setting = GetSetting()
	assert.False(t, setting.Enabled)
	assert.Equal(t, ModeObserve, setting.Mode)
	assert.False(t, setting.AgentEnabled)
}

func TestUpdateSettingNormalizesAndStoresValues(t *testing.T) {
	ResetForTest()

	updated := UpdateSetting(SmartRoutingSetting{
		Enabled:            true,
		Mode:               ModeBalanced,
		WeightAvailability: 2,
		WeightLatency:      1,
		WeightThroughput:   1,
		WeightCost:         0,
		TopK:               0,
	})

	assert.True(t, updated.Enabled)
	assert.Equal(t, ModeBalanced, updated.Mode)
	assert.Equal(t, 1, updated.TopK)
	assert.InDelta(t, 0.5, updated.WeightAvailability, 0.000001)
	assert.InDelta(t, 0.25, updated.WeightLatency, 0.000001)
	assert.InDelta(t, 0.25, updated.WeightThroughput, 0.000001)
	assert.Zero(t, updated.WeightCost)
	assert.Equal(t, updated, GetSetting())
}

func TestEnterpriseSLOModeUsesEnterpriseWeights(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	updated := UpdateSetting(SmartRoutingSetting{
		Enabled:            true,
		Mode:               ModeEnterpriseSLO,
		WeightAvailability: 0.45,
		WeightLatency:      0.25,
		WeightThroughput:   0.10,
		WeightCost:         0.20,
	})

	assert.InDelta(t, 0.55, updated.WeightAvailability, 0.000001)
	assert.InDelta(t, 0.30, updated.WeightLatency, 0.000001)
	assert.InDelta(t, 0.10, updated.WeightThroughput, 0.000001)
	assert.InDelta(t, 0.05, updated.WeightCost, 0.000001)
}
