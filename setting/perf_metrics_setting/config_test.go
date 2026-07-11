package perf_metrics_setting

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSettingNormalizesRuntimeValues(t *testing.T) {
	t.Cleanup(func() {
		replacePerfMetricsSetting(t, productDefaultPerfMetricsSetting())
	})

	tests := []struct {
		name                  string
		input                 PerfMetricsSetting
		expected              PerfMetricsSetting
		expectedBucketSeconds int64
	}{
		{
			name: "invalid values use safe defaults",
			input: PerfMetricsSetting{
				Enabled:       false,
				FlushInterval: -3,
				BucketTime:    "day",
				RetentionDays: -7,
			},
			expected: PerfMetricsSetting{
				Enabled:       false,
				FlushInterval: 1,
				BucketTime:    "hour",
				RetentionDays: 0,
			},
			expectedBucketSeconds: 3600,
		},
		{
			name: "zero retention keeps permanent retention semantics",
			input: PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 5,
				BucketTime:    "minute",
				RetentionDays: 0,
			},
			expected: PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 5,
				BucketTime:    "minute",
				RetentionDays: 0,
			},
			expectedBucketSeconds: 60,
		},
		{
			name: "valid five minute bucket is preserved",
			input: PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 9,
				BucketTime:    "5min",
				RetentionDays: 30,
			},
			expected: PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 9,
				BucketTime:    "5min",
				RetentionDays: 30,
			},
			expectedBucketSeconds: 300,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			replacePerfMetricsSetting(t, test.input)

			assert.Equal(t, test.expected, GetSetting())
			assert.Equal(t, test.expectedBucketSeconds, GetBucketSeconds())
			assert.Equal(t, test.expected.FlushInterval, GetFlushIntervalMinutes())
		})
	}
}

func TestConcurrentReadsReturnCompleteOldOrNewSnapshot(t *testing.T) {
	t.Cleanup(func() {
		replacePerfMetricsSetting(t, productDefaultPerfMetricsSetting())
	})

	oldSetting := PerfMetricsSetting{
		Enabled:       false,
		FlushInterval: 2,
		BucketTime:    "minute",
		RetentionDays: 11,
	}
	newSetting := PerfMetricsSetting{
		Enabled:       true,
		FlushInterval: 7,
		BucketTime:    "5min",
		RetentionDays: 23,
	}
	replacePerfMetricsSetting(t, oldSetting)

	const (
		rounds  = 32
		readers = 4
	)
	type readResult struct {
		setting       PerfMetricsSetting
		bucketSeconds int64
		flushInterval int
	}

	for round := 0; round < rounds; round++ {
		replacement := oldSetting
		if round%2 == 0 {
			replacement = newSetting
		}

		start := make(chan struct{})
		replaced := make(chan bool, 1)
		results := make(chan readResult, readers)
		var wait sync.WaitGroup
		wait.Add(readers + 1)

		go func() {
			defer wait.Done()
			<-start
			replaced <- config.GlobalConfig.Replace("perf_metrics_setting", replacement)
		}()
		for reader := 0; reader < readers; reader++ {
			go func() {
				defer wait.Done()
				<-start
				results <- readResult{
					setting:       GetSetting(),
					bucketSeconds: GetBucketSeconds(),
					flushInterval: GetFlushIntervalMinutes(),
				}
			}()
		}

		close(start)
		wait.Wait()
		close(results)
		require.True(t, <-replaced)

		for result := range results {
			assert.Truef(t, result.setting == oldSetting || result.setting == newSetting,
				"read a mixed setting snapshot: %+v", result.setting)
			assert.Contains(t, []int64{60, 300}, result.bucketSeconds)
			assert.Contains(t, []int{2, 7}, result.flushInterval)
		}
	}
}

func TestUpdateSettingNormalizesAndResetRestoresDefaults(t *testing.T) {
	t.Cleanup(ResetForTest)

	updated := UpdateSetting(PerfMetricsSetting{
		Enabled:       false,
		FlushInterval: 0,
		BucketTime:    "invalid",
		RetentionDays: -1,
	})

	expected := PerfMetricsSetting{
		Enabled:       false,
		FlushInterval: 1,
		BucketTime:    "hour",
		RetentionDays: 0,
	}
	assert.Equal(t, expected, updated)
	assert.Equal(t, expected, GetSetting())

	ResetForTest()
	assert.Equal(t, productDefaultPerfMetricsSetting(), GetSetting())
}

func replacePerfMetricsSetting(t *testing.T, setting PerfMetricsSetting) {
	t.Helper()
	require.True(t, config.GlobalConfig.Replace("perf_metrics_setting", setting))
}

func productDefaultPerfMetricsSetting() PerfMetricsSetting {
	return PerfMetricsSetting{
		Enabled:       true,
		FlushInterval: 5,
		BucketTime:    "hour",
		RetentionDays: 0,
	}
}
