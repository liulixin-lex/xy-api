package model

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpsertPerfMetricContextPreservesAggregationContract(t *testing.T) {
	db := usePerfMetricTestDB(t)

	require.NoError(t, UpsertPerfMetric(&PerfMetric{
		ModelName:      "gpt-test",
		Group:          "default",
		BucketTs:       1_700_000_000,
		RequestCount:   2,
		SuccessCount:   1,
		TotalLatencyMs: 120,
		TtftSumMs:      40,
		TtftCount:      1,
		OutputTokens:   30,
		GenerationMs:   80,
	}))
	require.NoError(t, UpsertPerfMetricContext(context.Background(), &PerfMetric{
		ModelName:      "gpt-test",
		Group:          "default",
		BucketTs:       1_700_000_000,
		RequestCount:   3,
		SuccessCount:   2,
		TotalLatencyMs: 210,
		TtftSumMs:      60,
		TtftCount:      2,
		OutputTokens:   50,
		GenerationMs:   140,
	}))

	var metric PerfMetric
	require.NoError(t, db.Where(&PerfMetric{
		ModelName: "gpt-test",
		Group:     "default",
		BucketTs:  1_700_000_000,
	}).First(&metric).Error)
	assert.Equal(t, int64(5), metric.RequestCount)
	assert.Equal(t, int64(3), metric.SuccessCount)
	assert.Equal(t, int64(330), metric.TotalLatencyMs)
	assert.Equal(t, int64(100), metric.TtftSumMs)
	assert.Equal(t, int64(3), metric.TtftCount)
	assert.Equal(t, int64(80), metric.OutputTokens)
	assert.Equal(t, int64(220), metric.GenerationMs)
}

func TestUpsertPerfMetricContextPreservesNoopContract(t *testing.T) {
	db := usePerfMetricTestDB(t)

	require.NoError(t, UpsertPerfMetricContext(context.Background(), nil))
	require.NoError(t, UpsertPerfMetricContext(context.Background(), &PerfMetric{
		ModelName:    "gpt-noop",
		Group:        "default",
		BucketTs:     1_700_000_000,
		SuccessCount: 1,
	}))

	var count int64
	require.NoError(t, db.Model(&PerfMetric{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestDeletePerfMetricsBeforeContextUsesExclusiveCutoff(t *testing.T) {
	db := usePerfMetricTestDB(t)
	require.NoError(t, db.Create([]PerfMetric{
		{ModelName: "gpt-test", Group: "default", BucketTs: 99, RequestCount: 1},
		{ModelName: "gpt-test", Group: "default", BucketTs: 100, RequestCount: 1},
		{ModelName: "gpt-test", Group: "default", BucketTs: 101, RequestCount: 1},
	}).Error)

	require.NoError(t, DeletePerfMetricsBeforeContext(context.Background(), 100))

	var bucketTimestamps []int64
	require.NoError(t, db.Model(&PerfMetric{}).Order("bucket_ts ASC").Pluck("bucket_ts", &bucketTimestamps).Error)
	assert.Equal(t, []int64{100, 101}, bucketTimestamps)

	require.NoError(t, DeletePerfMetricsBefore(0))
	bucketTimestamps = nil
	require.NoError(t, db.Model(&PerfMetric{}).Order("bucket_ts ASC").Pluck("bucket_ts", &bucketTimestamps).Error)
	assert.Equal(t, []int64{100, 101}, bucketTimestamps)
}

func TestUpsertPerfMetricContextReturnsContextCancellation(t *testing.T) {
	db := usePerfMetricTestDB(t)
	callbackName := "test:block_perf_metric_create_until_context_done"
	started := make(chan struct{})
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		close(started)
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- UpsertPerfMetricContext(ctx, &PerfMetric{
			ModelName:    "gpt-canceled",
			Group:        "default",
			BucketTs:     1_700_000_000,
			RequestCount: 1,
		})
	}()

	<-started
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

func TestDeletePerfMetricsBeforeContextReturnsContextCancellation(t *testing.T) {
	db := usePerfMetricTestDB(t)
	callbackName := "test:block_perf_metric_delete_until_context_done"
	started := make(chan struct{})
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		close(started)
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- DeletePerfMetricsBeforeContext(ctx, 1_700_000_001)
	}()

	<-started
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

func usePerfMetricTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "perf_metric.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&PerfMetric{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})

	return db
}
