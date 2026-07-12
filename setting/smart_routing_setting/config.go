package smart_routing_setting

import (
	"math"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

const (
	ModeObserve       = "observe"
	ModeShadow        = "shadow"
	ModeBalanced      = "balanced"
	ModeEnterpriseSLO = "enterprise_slo"
)

const configName = "smart_routing_setting"

const (
	enterpriseWeightAvailability = 0.55
	enterpriseWeightLatency      = 0.30
	enterpriseWeightThroughput   = 0.10
	enterpriseWeightCost         = 0.05
)

type SmartRoutingSetting struct {
	Enabled                  bool    `json:"enabled"`
	Mode                     string  `json:"mode"`
	WeightAvailability       float64 `json:"weight_availability"`
	WeightLatency            float64 `json:"weight_latency"`
	WeightThroughput         float64 `json:"weight_throughput"`
	WeightCost               float64 `json:"weight_cost"`
	AvailabilityFloor        float64 `json:"availability_floor"`
	MinVolume                int     `json:"min_volume"`
	TopK                     int     `json:"top_k"`
	Consecutive5xx           int     `json:"consecutive_5xx"`
	FailureRatePct           int     `json:"failure_rate_pct"`
	BaseCooldownSec          int     `json:"base_cooldown_sec"`
	MaxCooldownSec           int     `json:"max_cooldown_sec"`
	MaxEjectedPct            int     `json:"max_ejected_pct"`
	HalfOpenProbes           int     `json:"half_open_probes"`
	MaxSwitches              int     `json:"max_switches"`
	RetryTokenCapacity       int     `json:"retry_token_capacity"`
	RetryTokenRefillPerSec   float64 `json:"retry_token_refill_per_sec"`
	FailoverDeadlineMs       int     `json:"failover_deadline_ms"`
	RetryExtraCostMultiplier float64 `json:"retry_extra_cost_multiplier"`
	BackoffBaseMs5xx         int     `json:"backoff_base_ms_5xx"`
	BackoffBaseMs429         int     `json:"backoff_base_ms_429"`
	BackoffCapMs             int     `json:"backoff_cap_ms"`
	FirstByteFailoverEnabled bool    `json:"first_byte_failover_enabled"`
	FirstByteMinMs           int     `json:"first_byte_min_ms"`
	FirstByteCapMs           int     `json:"first_byte_cap_ms"`
	FirstByteP95Multiplier   float64 `json:"first_byte_p95_multiplier"`
	SnapshotLiveSec          int     `json:"snapshot_live_sec"`
	SnapshotStaleSec         int     `json:"snapshot_stale_sec"`
	BalanceMarginUSD         float64 `json:"balance_margin_usd"`
	SyncIntervalMin          int     `json:"sync_interval_min"`
	HotcacheRefreshSec       int     `json:"hotcache_refresh_sec"`
	MetricBucketSec          int     `json:"metric_bucket_sec"`
	FlushIntervalMin         int     `json:"flush_interval_min"`
	RetentionDays            int     `json:"retention_days"`
	ActiveProbeEnabled       bool    `json:"active_probe_enabled"`
	ActiveProbeHealthySec    int     `json:"active_probe_healthy_sec"`
	ActiveProbeDegradedSec   int     `json:"active_probe_degraded_sec"`
	ActiveProbeOpenSec       int     `json:"active_probe_open_sec"`
	ActiveProbeTimeoutMs     int     `json:"active_probe_timeout_ms"`
	ActiveProbeMaxTargets    int     `json:"active_probe_max_targets"`
	ActiveProbeConcurrency   int     `json:"active_probe_concurrency"`
	ActiveProbePerHost       int     `json:"active_probe_per_host"`
	ActiveProbeTokenBudget   int     `json:"active_probe_token_budget"`
	ActiveProbeCostBudgetUSD float64 `json:"active_probe_cost_budget_usd"`
	AgentEnabled             bool    `json:"agent_enabled"`
	AgentAutoApply           bool    `json:"agent_auto_apply"`
	AgentModel               string  `json:"agent_model"`
}

var defaultSmartRoutingSetting = SmartRoutingSetting{
	Enabled:                  false,
	Mode:                     ModeObserve,
	WeightAvailability:       0.45,
	WeightLatency:            0.25,
	WeightThroughput:         0.10,
	WeightCost:               0.20,
	AvailabilityFloor:        0.95,
	MinVolume:                50,
	TopK:                     3,
	Consecutive5xx:           5,
	FailureRatePct:           50,
	BaseCooldownSec:          30,
	MaxCooldownSec:           300,
	MaxEjectedPct:            50,
	HalfOpenProbes:           1,
	MaxSwitches:              2,
	RetryTokenCapacity:       100,
	RetryTokenRefillPerSec:   10,
	FailoverDeadlineMs:       120_000,
	RetryExtraCostMultiplier: 2,
	BackoffBaseMs5xx:         50,
	BackoffBaseMs429:         1000,
	BackoffCapMs:             20000,
	FirstByteFailoverEnabled: true,
	FirstByteMinMs:           3000,
	FirstByteCapMs:           12000,
	FirstByteP95Multiplier:   2.0,
	SnapshotLiveSec:          300,
	SnapshotStaleSec:         1800,
	BalanceMarginUSD:         1.0,
	SyncIntervalMin:          5,
	HotcacheRefreshSec:       3,
	MetricBucketSec:          60,
	FlushIntervalMin:         1,
	RetentionDays:            7,
	ActiveProbeEnabled:       false,
	ActiveProbeHealthySec:    900,
	ActiveProbeDegradedSec:   120,
	ActiveProbeOpenSec:       30,
	ActiveProbeTimeoutMs:     15_000,
	ActiveProbeMaxTargets:    128,
	ActiveProbeConcurrency:   4,
	ActiveProbePerHost:       1,
	ActiveProbeTokenBudget:   4_096,
	ActiveProbeCostBudgetUSD: 0.25,
	AgentEnabled:             false,
	AgentAutoApply:           false,
	AgentModel:               "claude-opus-4-8",
}

var smartRoutingSetting = defaultSmartRoutingSetting

func init() {
	config.GlobalConfig.Register(configName, &smartRoutingSetting)
}

func GetSetting() SmartRoutingSetting {
	setting := defaultSmartRoutingSetting
	// Snapshot is a shallow copy; SmartRoutingSetting contains scalar fields only.
	if !config.GlobalConfig.Snapshot(configName, &setting) {
		setting = defaultSmartRoutingSetting
	}
	applyEnvOverrides(&setting)
	normalize(&setting)
	return setting
}

// Normalize returns a normalized copy without reading environment overrides
// or publishing it to the global config snapshot.
func Normalize(setting SmartRoutingSetting) SmartRoutingSetting {
	normalize(&setting)
	return setting
}

func UpdateSetting(setting SmartRoutingSetting) SmartRoutingSetting {
	config.GlobalConfig.Replace(configName, Normalize(setting))
	return GetSetting()
}

func Enabled() bool {
	return GetSetting().Enabled
}

func Mode() string {
	return GetSetting().Mode
}

func ResetForTest() {
	config.GlobalConfig.Replace(configName, defaultSmartRoutingSetting)
}

func applyEnvOverrides(setting *SmartRoutingSetting) {
	if value, ok := os.LookupEnv("SMART_ROUTING_ENABLED"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			setting.Enabled = parsed
		}
	}
	if value, ok := os.LookupEnv("SMART_ROUTING_MODE"); ok {
		setting.Mode = value
	}
	if value, ok := os.LookupEnv("SMART_ROUTING_AGENT_ENABLED"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			setting.AgentEnabled = parsed
		}
	}
}

func normalize(setting *SmartRoutingSetting) {
	if !isValidMode(setting.Mode) {
		setting.Mode = ModeObserve
	}
	if setting.Mode == ModeEnterpriseSLO {
		setting.WeightAvailability = enterpriseWeightAvailability
		setting.WeightLatency = enterpriseWeightLatency
		setting.WeightThroughput = enterpriseWeightThroughput
		setting.WeightCost = enterpriseWeightCost
	}
	total := setting.WeightAvailability + setting.WeightLatency + setting.WeightThroughput + setting.WeightCost
	if total <= 0 {
		setting.WeightAvailability = defaultSmartRoutingSetting.WeightAvailability
		setting.WeightLatency = defaultSmartRoutingSetting.WeightLatency
		setting.WeightThroughput = defaultSmartRoutingSetting.WeightThroughput
		setting.WeightCost = defaultSmartRoutingSetting.WeightCost
		total = 1
	}
	setting.WeightAvailability /= total
	setting.WeightLatency /= total
	setting.WeightThroughput /= total
	setting.WeightCost /= total

	if setting.TopK < 1 {
		setting.TopK = 1
	}
	if setting.MinVolume < 0 {
		setting.MinVolume = 0
	}
	if setting.AvailabilityFloor < 0 {
		setting.AvailabilityFloor = 0
	}
	if setting.AvailabilityFloor > 1 {
		setting.AvailabilityFloor = 1
	}
	if setting.Consecutive5xx < 1 {
		setting.Consecutive5xx = defaultSmartRoutingSetting.Consecutive5xx
	}
	if setting.FailureRatePct <= 0 || setting.FailureRatePct > 100 {
		setting.FailureRatePct = defaultSmartRoutingSetting.FailureRatePct
	}
	if setting.BaseCooldownSec < 1 {
		setting.BaseCooldownSec = defaultSmartRoutingSetting.BaseCooldownSec
	}
	if setting.MaxCooldownSec < setting.BaseCooldownSec {
		setting.MaxCooldownSec = setting.BaseCooldownSec
	}
	if setting.MaxEjectedPct < 0 {
		setting.MaxEjectedPct = 0
	}
	if setting.MaxEjectedPct > 100 {
		setting.MaxEjectedPct = 100
	}
	if setting.HalfOpenProbes < 1 {
		setting.HalfOpenProbes = defaultSmartRoutingSetting.HalfOpenProbes
	}
	if setting.MaxSwitches < 0 {
		setting.MaxSwitches = 0
	}
	if setting.RetryTokenCapacity < 1 {
		setting.RetryTokenCapacity = defaultSmartRoutingSetting.RetryTokenCapacity
	}
	if setting.RetryTokenCapacity > 1_000_000 {
		setting.RetryTokenCapacity = 1_000_000
	}
	if setting.RetryTokenRefillPerSec <= 0 || math.IsNaN(setting.RetryTokenRefillPerSec) || math.IsInf(setting.RetryTokenRefillPerSec, 0) {
		setting.RetryTokenRefillPerSec = defaultSmartRoutingSetting.RetryTokenRefillPerSec
	}
	if setting.RetryTokenRefillPerSec > 1_000_000 {
		setting.RetryTokenRefillPerSec = 1_000_000
	}
	if setting.FailoverDeadlineMs < 1 {
		setting.FailoverDeadlineMs = defaultSmartRoutingSetting.FailoverDeadlineMs
	}
	if setting.FailoverDeadlineMs > 600_000 {
		setting.FailoverDeadlineMs = 600_000
	}
	if setting.RetryExtraCostMultiplier <= 0 || math.IsNaN(setting.RetryExtraCostMultiplier) || math.IsInf(setting.RetryExtraCostMultiplier, 0) {
		setting.RetryExtraCostMultiplier = defaultSmartRoutingSetting.RetryExtraCostMultiplier
	}
	if setting.RetryExtraCostMultiplier > 16 {
		setting.RetryExtraCostMultiplier = 16
	}
	if setting.BackoffBaseMs5xx < 1 {
		setting.BackoffBaseMs5xx = defaultSmartRoutingSetting.BackoffBaseMs5xx
	}
	if setting.BackoffBaseMs429 < 1 {
		setting.BackoffBaseMs429 = defaultSmartRoutingSetting.BackoffBaseMs429
	}
	if setting.BackoffCapMs < 1 {
		setting.BackoffCapMs = defaultSmartRoutingSetting.BackoffCapMs
	}
	if setting.FirstByteMinMs < 1 {
		setting.FirstByteMinMs = defaultSmartRoutingSetting.FirstByteMinMs
	}
	if setting.FirstByteCapMs < setting.FirstByteMinMs {
		setting.FirstByteCapMs = setting.FirstByteMinMs
	}
	if setting.FirstByteP95Multiplier <= 0 {
		setting.FirstByteP95Multiplier = defaultSmartRoutingSetting.FirstByteP95Multiplier
	}
	if setting.SnapshotLiveSec < 1 {
		setting.SnapshotLiveSec = defaultSmartRoutingSetting.SnapshotLiveSec
	}
	if setting.SnapshotStaleSec < 1 {
		setting.SnapshotStaleSec = defaultSmartRoutingSetting.SnapshotStaleSec
	}
	if setting.SyncIntervalMin < 1 {
		setting.SyncIntervalMin = 1
	}
	if setting.HotcacheRefreshSec < 1 {
		setting.HotcacheRefreshSec = 1
	}
	if setting.MetricBucketSec < 1 {
		setting.MetricBucketSec = defaultSmartRoutingSetting.MetricBucketSec
	}
	if setting.FlushIntervalMin < 1 {
		setting.FlushIntervalMin = 1
	}
	if setting.RetentionDays < 1 {
		setting.RetentionDays = defaultSmartRoutingSetting.RetentionDays
	}
	if setting.ActiveProbeHealthySec < 1 {
		setting.ActiveProbeHealthySec = defaultSmartRoutingSetting.ActiveProbeHealthySec
	}
	if setting.ActiveProbeHealthySec > 86_400 {
		setting.ActiveProbeHealthySec = 86_400
	}
	if setting.ActiveProbeDegradedSec < 1 {
		setting.ActiveProbeDegradedSec = defaultSmartRoutingSetting.ActiveProbeDegradedSec
	}
	if setting.ActiveProbeDegradedSec > 86_400 {
		setting.ActiveProbeDegradedSec = 86_400
	}
	if setting.ActiveProbeOpenSec < 1 {
		setting.ActiveProbeOpenSec = defaultSmartRoutingSetting.ActiveProbeOpenSec
	}
	if setting.ActiveProbeOpenSec > 86_400 {
		setting.ActiveProbeOpenSec = 86_400
	}
	if setting.ActiveProbeDegradedSec > setting.ActiveProbeHealthySec {
		setting.ActiveProbeDegradedSec = setting.ActiveProbeHealthySec
	}
	if setting.ActiveProbeOpenSec > setting.ActiveProbeDegradedSec {
		setting.ActiveProbeOpenSec = setting.ActiveProbeDegradedSec
	}
	if setting.ActiveProbeTimeoutMs < 1 {
		setting.ActiveProbeTimeoutMs = defaultSmartRoutingSetting.ActiveProbeTimeoutMs
	}
	if setting.ActiveProbeTimeoutMs > 120_000 {
		setting.ActiveProbeTimeoutMs = 120_000
	}
	if setting.ActiveProbeMaxTargets < 1 {
		setting.ActiveProbeMaxTargets = defaultSmartRoutingSetting.ActiveProbeMaxTargets
	}
	if setting.ActiveProbeMaxTargets > 4_096 {
		setting.ActiveProbeMaxTargets = 4_096
	}
	if setting.ActiveProbeConcurrency < 1 {
		setting.ActiveProbeConcurrency = defaultSmartRoutingSetting.ActiveProbeConcurrency
	}
	if setting.ActiveProbeConcurrency > 64 {
		setting.ActiveProbeConcurrency = 64
	}
	if setting.ActiveProbePerHost < 1 {
		setting.ActiveProbePerHost = defaultSmartRoutingSetting.ActiveProbePerHost
	}
	if setting.ActiveProbePerHost > setting.ActiveProbeConcurrency {
		setting.ActiveProbePerHost = setting.ActiveProbeConcurrency
	}
	if setting.ActiveProbeTokenBudget < 1 {
		setting.ActiveProbeTokenBudget = defaultSmartRoutingSetting.ActiveProbeTokenBudget
	}
	if setting.ActiveProbeTokenBudget > 1_000_000_000 {
		setting.ActiveProbeTokenBudget = 1_000_000_000
	}
	if setting.ActiveProbeCostBudgetUSD <= 0 || math.IsNaN(setting.ActiveProbeCostBudgetUSD) || math.IsInf(setting.ActiveProbeCostBudgetUSD, 0) {
		setting.ActiveProbeCostBudgetUSD = defaultSmartRoutingSetting.ActiveProbeCostBudgetUSD
	}
	if setting.ActiveProbeCostBudgetUSD > 1_000 {
		setting.ActiveProbeCostBudgetUSD = 1_000
	}
}

func isValidMode(mode string) bool {
	switch mode {
	case ModeObserve, ModeShadow, ModeBalanced, ModeEnterpriseSLO:
		return true
	default:
		return false
	}
}
