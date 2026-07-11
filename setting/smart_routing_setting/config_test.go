package smart_routing_setting

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDoesNotReadEnvironmentOrPublish(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", ModeEnterpriseSLO)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "true")

	var before SmartRoutingSetting
	require.True(t, config.GlobalConfig.Snapshot(configName, &before))

	normalized := Normalize(SmartRoutingSetting{
		Enabled:            false,
		Mode:               ModeBalanced,
		WeightAvailability: 2,
		WeightLatency:      1,
		WeightThroughput:   1,
		WeightCost:         0,
		TopK:               0,
		AgentEnabled:       false,
	})

	assert.False(t, normalized.Enabled)
	assert.Equal(t, ModeBalanced, normalized.Mode)
	assert.False(t, normalized.AgentEnabled)
	assert.Equal(t, 1, normalized.TopK)
	assert.InDelta(t, 0.5, normalized.WeightAvailability, 0.000001)
	assert.InDelta(t, 0.25, normalized.WeightLatency, 0.000001)
	assert.InDelta(t, 0.25, normalized.WeightThroughput, 0.000001)
	assert.Zero(t, normalized.WeightCost)

	var after SmartRoutingSetting
	require.True(t, config.GlobalConfig.Snapshot(configName, &after))
	assert.Equal(t, before, after)
}

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

func TestUpdateSettingClampsBreakerAndRetryRanges(t *testing.T) {
	ResetForTest()

	updated := UpdateSetting(SmartRoutingSetting{
		Enabled:           true,
		Mode:              ModeBalanced,
		AvailabilityFloor: 2,
		FailureRatePct:    -10,
		BaseCooldownSec:   -1,
		MaxCooldownSec:    1,
		MaxEjectedPct:     500,
		HalfOpenProbes:    0,
		MaxSwitches:       -3,
		BackoffCapMs:      -1,
		SnapshotLiveSec:   0,
		SnapshotStaleSec:  0,
		RetentionDays:     0,
	})

	assert.Equal(t, 1.0, updated.AvailabilityFloor)
	assert.Equal(t, 50, updated.FailureRatePct)
	assert.Equal(t, 30, updated.BaseCooldownSec)
	assert.Equal(t, 30, updated.MaxCooldownSec)
	assert.Equal(t, 100, updated.MaxEjectedPct)
	assert.Equal(t, 1, updated.HalfOpenProbes)
	assert.Equal(t, 0, updated.MaxSwitches)
	assert.Equal(t, 20000, updated.BackoffCapMs)
	assert.Equal(t, 300, updated.SnapshotLiveSec)
	assert.Equal(t, 1800, updated.SnapshotStaleSec)
	assert.Equal(t, 7, updated.RetentionDays)
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

func TestSettingConcurrentReadWriteKeepsNormalizedSnapshot(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	var wait sync.WaitGroup
	var invalid atomic.Bool
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if worker%2 == 0 {
					UpdateSetting(SmartRoutingSetting{
						Enabled:            true,
						Mode:               ModeBalanced,
						WeightAvailability: 2,
						WeightLatency:      1,
						WeightThroughput:   1,
						TopK:               3,
					})
					continue
				}
				snapshot := GetSetting()
				total := snapshot.WeightAvailability + snapshot.WeightLatency +
					snapshot.WeightThroughput + snapshot.WeightCost
				if math.Abs(total-1.0) > 0.000001 {
					invalid.Store(true)
				}
			}
		}(worker)
	}
	wait.Wait()

	assert.False(t, invalid.Load())
}
