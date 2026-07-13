package perf_metrics_setting

import "github.com/QuantumNous/new-api/setting/config"

const configName = "perf_metrics_setting"

type PerfMetricsSetting struct {
	Enabled       bool   `json:"enabled"`
	FlushInterval int    `json:"flush_interval"`
	BucketTime    string `json:"bucket_time"`
	RetentionDays int    `json:"retention_days"`
}

var defaultPerfMetricsSetting = PerfMetricsSetting{
	Enabled:       true,
	FlushInterval: 5,
	BucketTime:    "hour",
	RetentionDays: 0,
}

var perfMetricsSetting = defaultPerfMetricsSetting

func init() {
	config.GlobalConfig.Register(configName, &perfMetricsSetting)
}

func GetSetting() PerfMetricsSetting {
	setting := defaultPerfMetricsSetting
	// Snapshot is a shallow copy; PerfMetricsSetting contains scalar fields only.
	if !config.GlobalConfig.Snapshot(configName, &setting) {
		setting = defaultPerfMetricsSetting
	}
	return Normalize(setting)
}

func UpdateSetting(setting PerfMetricsSetting) PerfMetricsSetting {
	setting = Normalize(setting)
	config.GlobalConfig.Replace(configName, setting)
	return GetSetting()
}

func ResetForTest() {
	config.GlobalConfig.Replace(configName, defaultPerfMetricsSetting)
}

func GetBucketSeconds() int64 {
	setting := GetSetting()
	switch setting.BucketTime {
	case "minute":
		return 60
	case "5min":
		return 300
	case "hour":
		return 3600
	default:
		return 3600
	}
}

func GetFlushIntervalMinutes() int {
	setting := GetSetting()
	return setting.FlushInterval
}

// Normalize returns a safe runtime representation of a performance metrics setting.
func Normalize(setting PerfMetricsSetting) PerfMetricsSetting {
	switch setting.BucketTime {
	case "minute", "5min", "hour":
	default:
		setting.BucketTime = defaultPerfMetricsSetting.BucketTime
	}
	if setting.FlushInterval < 1 {
		setting.FlushInterval = 1
	} else if setting.FlushInterval > 1440 {
		setting.FlushInterval = 1440
	}
	if setting.RetentionDays < 0 {
		setting.RetentionDays = 0
	}
	return setting
}
