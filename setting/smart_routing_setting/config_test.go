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
	t.Setenv("SMART_ROUTING_HEDGE_ENABLED", "true")

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
		HedgeEnabled:       false,
	})

	assert.False(t, normalized.Enabled)
	assert.Equal(t, ModeBalanced, normalized.Mode)
	assert.False(t, normalized.AgentEnabled)
	assert.False(t, normalized.HedgeEnabled)
	assert.Equal(t, 1, normalized.TopK)
	assert.InDelta(t, 0.5, normalized.WeightAvailability, 0.000001)
	assert.InDelta(t, 0.25, normalized.WeightLatency, 0.000001)
	assert.InDelta(t, 0.25, normalized.WeightThroughput, 0.000001)
	assert.Zero(t, normalized.WeightCost)

	var after SmartRoutingSetting
	require.True(t, config.GlobalConfig.Snapshot(configName, &after))
	assert.Equal(t, before, after)
}

func TestNormalizeBoundsFailoverAndProbeBudgets(t *testing.T) {
	normalized := Normalize(SmartRoutingSetting{
		RetryTokenCapacity:       -1,
		RetryTokenRefillPerSec:   math.NaN(),
		FailoverDeadlineMs:       0,
		RetryExtraCostMultiplier: math.Inf(1),
		BackoffBaseMs5xx:         700_000,
		BackoffBaseMs429:         800_000,
		BackoffCapMs:             900_000,
		ActiveProbeHealthySec:    30,
		ActiveProbeDegradedSec:   60,
		ActiveProbeOpenSec:       90,
		ActiveProbeTimeoutMs:     0,
		ActiveProbeMaxTargets:    10_000,
		ActiveProbeConcurrency:   100,
		ActiveProbePerHost:       100,
		ActiveProbeTokenBudget:   0,
		ActiveProbeCostBudgetUSD: math.NaN(),
		HedgeMaxConcurrent:       1_000,
		HedgeMaxResponseBytes:    1,
		HedgeMaxBufferedBytes:    1 << 40,
		HedgeRatioWindowSec:      10_000,
		HedgeMaxExtraBasisPoints: 20_000,
		HedgeAuditRetentionDays:  1_000,
	})

	assert.Equal(t, 100, normalized.RetryTokenCapacity)
	assert.Equal(t, 10.0, normalized.RetryTokenRefillPerSec)
	assert.Equal(t, 120_000, normalized.FailoverDeadlineMs)
	assert.Equal(t, 2.0, normalized.RetryExtraCostMultiplier)
	assert.Equal(t, 600_000, normalized.BackoffBaseMs5xx)
	assert.Equal(t, 600_000, normalized.BackoffBaseMs429)
	assert.Equal(t, 600_000, normalized.BackoffCapMs)
	assert.Equal(t, 30, normalized.ActiveProbeHealthySec)
	assert.Equal(t, 30, normalized.ActiveProbeDegradedSec)
	assert.Equal(t, 30, normalized.ActiveProbeOpenSec)
	assert.Equal(t, 15_000, normalized.ActiveProbeTimeoutMs)
	assert.Equal(t, 4_096, normalized.ActiveProbeMaxTargets)
	assert.Equal(t, 64, normalized.ActiveProbeConcurrency)
	assert.Equal(t, 64, normalized.ActiveProbePerHost)
	assert.Equal(t, 4_096, normalized.ActiveProbeTokenBudget)
	assert.Equal(t, 0.25, normalized.ActiveProbeCostBudgetUSD)
	assert.Equal(t, 128, normalized.HedgeMaxConcurrent)
	assert.Equal(t, 4<<20, normalized.HedgeMaxResponseBytes)
	assert.Equal(t, int64(1<<30), normalized.HedgeMaxBufferedBytes)
	assert.Equal(t, 3_600, normalized.HedgeRatioWindowSec)
	assert.Equal(t, 10_000, normalized.HedgeMaxExtraBasisPoints)
	assert.Equal(t, 365, normalized.HedgeAuditRetentionDays)
}

func TestGetSettingDefaultsAndNormalizesWeights(t *testing.T) {
	ResetForTest()

	setting := GetSetting()

	assert.False(t, setting.Enabled)
	assert.False(t, setting.RequestProfileEnabled)
	assert.Equal(t, ModeObserve, setting.Mode)
	assert.Equal(t, 5, setting.Consecutive5xx)
	assert.Equal(t, 50, setting.FailureRatePct)
	assert.Equal(t, 3000, setting.FirstByteMinMs)
	assert.Equal(t, 12000, setting.FirstByteCapMs)
	assert.False(t, setting.HedgeEnabled)
	assert.Equal(t, 8, setting.HedgeMaxConcurrent)
	assert.Equal(t, 4<<20, setting.HedgeMaxResponseBytes)
	assert.Equal(t, int64(64<<20), setting.HedgeMaxBufferedBytes)
	assert.Equal(t, 60, setting.HedgeRatioWindowSec)
	assert.Equal(t, 500, setting.HedgeMaxExtraBasisPoints)
	assert.InDelta(t, 1.0, setting.WeightAvailability+setting.WeightLatency+setting.WeightThroughput+setting.WeightCost, 0.000001)
}

func TestGetSettingAppliesEnvOverridesEveryRead(t *testing.T) {
	ResetForTest()
	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", ModeBalanced)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "true")
	t.Setenv("SMART_ROUTING_HEDGE_ENABLED", "true")

	setting := GetSetting()

	assert.True(t, setting.Enabled)
	assert.Equal(t, ModeBalanced, setting.Mode)
	assert.True(t, setting.AgentEnabled)
	assert.True(t, setting.HedgeEnabled)

	t.Setenv("SMART_ROUTING_ENABLED", "false")
	t.Setenv("SMART_ROUTING_MODE", "invalid")
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "false")
	t.Setenv("SMART_ROUTING_HEDGE_ENABLED", "false")

	setting = GetSetting()
	assert.False(t, setting.Enabled)
	assert.Equal(t, ModeObserve, setting.Mode)
	assert.False(t, setting.AgentEnabled)
	assert.False(t, setting.HedgeEnabled)
}

func TestGetSettingUsesUnversionedRequestProfileEnvironmentOnly(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("SMART_ROUTING_REQUEST_PROFILE_V2_ENABLED", "true")
	t.Setenv("SMART_ROUTING_REQUEST_PROFILE_ENABLED", "")

	setting := GetSetting()
	assert.False(t, setting.RequestProfileEnabled)

	t.Setenv("SMART_ROUTING_REQUEST_PROFILE_ENABLED", "true")
	setting = GetSetting()
	assert.True(t, setting.RequestProfileEnabled)
}

func TestRequestProfileSettingExportsOnlyUnversionedKey(t *testing.T) {
	values, err := config.ConfigToMap(SmartRoutingSetting{RequestProfileEnabled: false})
	require.NoError(t, err)

	assert.Equal(t, "false", values["request_profile_enabled"])
	_, legacyPresent := values["request_profile_v2_enabled"]
	assert.False(t, legacyPresent)
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

func TestNormalizeKeepsRetryBackoffBasesWithinCap(t *testing.T) {
	normalized := Normalize(SmartRoutingSetting{
		BackoffBaseMs5xx: 500,
		BackoffBaseMs429: 1_000,
		BackoffCapMs:     100,
	})

	assert.Equal(t, 100, normalized.BackoffBaseMs5xx)
	assert.Equal(t, 100, normalized.BackoffBaseMs429)
	assert.Equal(t, 100, normalized.BackoffCapMs)
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
