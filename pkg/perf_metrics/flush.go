package perfmetrics

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

func flushLoop() {
	for {
		interval := perf_metrics_setting.GetFlushIntervalMinutes()
		time.Sleep(time.Duration(interval) * time.Minute)
		setting := perf_metrics_setting.GetSetting()
		runMaintenanceWith(setting, bucketStart(time.Now().Unix()), model.UpsertPerfMetric)
	}
}

func flushCompletedBuckets() {
	flushCompletedBucketsWith(bucketStart(time.Now().Unix()), model.UpsertPerfMetric)
}

func runMaintenanceWith(setting perf_metrics_setting.PerfMetricsSetting, currentBucket int64, persist func(*model.PerfMetric) error) {
	flushCompletedBucketsWith(currentBucket, persist)
	cleanupStaleMetrics(setting.RetentionDays)
}

type drainedBucket struct {
	key      bucketKey
	counters counters
}

func flushCompletedBucketsWith(currentBucket int64, persist func(*model.PerfMetric) error) {
	drainedBuckets := drainCompletedBuckets(currentBucket)
	for _, drained := range drainedBuckets {
		k := drained.key
		value := drained.counters
		err := persist(&model.PerfMetric{
			ModelName:      k.model,
			Group:          k.group,
			BucketTs:       k.bucketTs,
			RequestCount:   value.requestCount,
			SuccessCount:   value.successCount,
			TotalLatencyMs: value.totalLatencyMs,
			TtftSumMs:      value.ttftSumMs,
			TtftCount:      value.ttftCount,
			OutputTokens:   value.outputTokens,
			GenerationMs:   value.generationMs,
		})
		if err == nil {
			continue
		}
		recordCounters(k, value)
		common.SysError(fmt.Sprintf("failed to flush perf metric bucket model=%s group=%s bucket=%d: %s", k.model, k.group, k.bucketTs, err.Error()))
	}
}

func drainCompletedBuckets(currentBucket int64) []drainedBucket {
	drainedBuckets := make([]drainedBucket, 0)
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs >= currentBucket {
			return true
		}

		bucket := value.(*bucket)
		bucket.mu.Lock()
		bucket.draining = true
		drained := bucket.counters
		deleted := hotBuckets.CompareAndDelete(key, value)
		bucket.mu.Unlock()
		if !deleted {
			return true
		}
		bucketCount.Add(-1)
		if drained.requestCount > 0 {
			drainedBuckets = append(drainedBuckets, drainedBucket{key: k, counters: drained})
		}
		return true
	})
	return drainedBuckets
}

func cleanupStaleMetrics(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	if err := model.DeletePerfMetricsBefore(cutoff); err != nil {
		common.SysError("failed to cleanup expired perf metrics: " + err.Error())
	}
}
