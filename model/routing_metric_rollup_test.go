package model

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingMetricRollupSQLiteContract(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingMetricRollupContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingMetricRollupExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			runRoutingMetricRollupContract(t, db, test.dbType)
		})
	}
}

func runRoutingMetricRollupContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()

	withRoutingTestDB(t, db, dbType)
	require.NoError(t, DB.AutoMigrate(&RoutingMetricRollup{}))
	require.NoError(t, DB.AutoMigrate(&RoutingMetricRollup{}))
	assert.True(t, DB.Migrator().HasTable(&RoutingMetricRollup{}))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "UnknownCount"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "RetryAfterTotalMs"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "ModelKey"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "SketchCodecVersion"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "LatencySketch"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "TtftSketch"))
	assert.False(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "latency_p95_ms"))
	assert.False(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "ttft_p95_ms"))

	first := RoutingMetricRollup{
		MemberID: 2, CredentialID: 3, ModelName: "model-z", BucketTs: 200,
		ChannelID: 20, PoolID: 200, SchemaVersion: 2, LastSnapshotRevision: 9,
		RequestCount: 2, SuccessCount: 1, FailureCount: 1, ReliabilityRequestCount: 2,
		TotalLatencyMs: 100, TtftSumMs: 20, TtftCount: 1,
		OutputTokens: 10, GenerationMs: 80, Err4xx: 1,
		RetryAfterCount: 1, RetryAfterTotalMs: 250,
		SketchCodecVersion: routingdistribution.CodecVersion,
		LatencySampleCount: 1, LatencySketch: routingMetricDurationSketch(t, 100),
		TtftSampleCount: 1, TtftSketch: routingMetricDurationSketch(t, 20),
	}
	second := RoutingMetricRollup{
		MemberID: 1, CredentialID: 2, ModelName: "model-b", BucketTs: 300,
		ChannelID: 10, PoolID: 100, LastSnapshotRevision: 4,
		RequestCount: 1, FailureCount: 1, UnknownCount: 1,
		ReliabilityRequestCount: 1, ReliabilityFailureCount: 1,
		TotalLatencyMs: 500, Err5xx: 1,
	}
	keyless := RoutingMetricRollup{
		MemberID: 3, CredentialID: 0, ModelName: strings.Repeat("\U0001F600", 128), BucketTs: 400,
		ChannelID: 30, PoolID: 300, LastSnapshotRevision: 5, RequestCount: 1, SuccessCount: 1,
	}
	require.Len(t, []byte(keyless.ModelName), 512)
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{first, second, keyless}))
	caseVariants := []RoutingMetricRollup{
		{MemberID: 7, CredentialID: 8, ModelName: "Model-X", BucketTs: 500, ChannelID: 70, PoolID: 700, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: 7, CredentialID: 8, ModelName: "model-x", BucketTs: 500, ChannelID: 70, PoolID: 700, LastSnapshotRevision: 1, RequestCount: 2},
	}
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), caseVariants))
	upperWindow, err := GetRoutingMetricRollupsContext(context.Background(), 7, 8, "Model-X", 500, 500)
	require.NoError(t, err)
	require.Len(t, upperWindow, 1)
	assert.Equal(t, int64(1), upperWindow[0].RequestCount)
	lowerWindow, err := GetRoutingMetricRollupsContext(context.Background(), 7, 8, "model-x", 500, 500)
	require.NoError(t, err)
	require.Len(t, lowerWindow, 1)
	assert.Equal(t, int64(2), lowerWindow[0].RequestCount)

	additive := first
	additive.SchemaVersion = 1
	additive.LastSnapshotRevision = 7
	additive.RequestCount = 10
	additive.SuccessCount = 0
	additive.FailureCount = 9
	additive.UnknownCount = 1
	additive.ReliabilityRequestCount = 10
	additive.ReliabilityFailureCount = 1
	additive.TotalLatencyMs = 300
	additive.TtftSumMs = 40
	additive.TtftCount = 2
	additive.OutputTokens = 30
	additive.GenerationMs = 120
	additive.Err4xx = 0
	additive.Err5xx = 2
	additive.Err429 = 3
	additive.Err529 = 4
	additive.RetryAfterCount = 2
	additive.RetryAfterTotalMs = 500
	additive.SketchCodecVersion = routingdistribution.CodecVersion
	additive.LatencySampleCount = 2
	additive.LatencySketch = routingMetricDurationSketch(t, 150, 150)
	additive.TtftSampleCount = 2
	additive.TtftSketch = routingMetricDurationSketch(t, 30, 40)
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{additive}))

	var saved RoutingMetricRollup
	require.NoError(t, DB.Where(
		"member_id = ? AND credential_id = ? AND model_name = ? AND bucket_ts = ?",
		first.MemberID, first.CredentialID, first.ModelName, first.BucketTs,
	).First(&saved).Error)
	assert.Equal(t, 2, saved.SchemaVersion)
	assert.Equal(t, int64(9), saved.LastSnapshotRevision)
	assert.Equal(t, int64(12), saved.RequestCount)
	assert.Equal(t, int64(1), saved.SuccessCount)
	assert.Equal(t, int64(10), saved.FailureCount)
	assert.Equal(t, int64(1), saved.UnknownCount)
	assert.Equal(t, int64(12), saved.ReliabilityRequestCount)
	assert.Equal(t, int64(1), saved.ReliabilityFailureCount)
	assert.Equal(t, int64(400), saved.TotalLatencyMs)
	assert.Equal(t, int64(60), saved.TtftSumMs)
	assert.Equal(t, int64(3), saved.TtftCount)
	assert.Equal(t, int64(40), saved.OutputTokens)
	assert.Equal(t, int64(200), saved.GenerationMs)
	assert.Equal(t, int64(1), saved.Err4xx)
	assert.Equal(t, int64(2), saved.Err5xx)
	assert.Equal(t, int64(3), saved.Err429)
	assert.Equal(t, int64(4), saved.Err529)
	assert.Equal(t, int64(3), saved.RetryAfterCount)
	assert.Equal(t, int64(750), saved.RetryAfterTotalMs)
	assert.Equal(t, routingdistribution.CodecVersion, saved.SketchCodecVersion)
	assert.Equal(t, int64(3), saved.LatencySampleCount)
	assert.Equal(t, int64(3), saved.TtftSampleCount)
	latencySketch, err := routingdistribution.Decode(saved.LatencySketch, saved.SketchCodecVersion)
	require.NoError(t, err)
	assert.Equal(t, int64(3), latencySketch.Count())
	latencyP95, err := latencySketch.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, latencyP95.Known)
	assert.InDelta(t, routingMetricDurationQuantile(t, 0.95, 100, 150, 150), latencyP95.ValueMilliseconds, 0.000001)
	ttftSketch, err := routingdistribution.Decode(saved.TtftSketch, saved.SketchCodecVersion)
	require.NoError(t, err)
	assert.Equal(t, int64(3), ttftSketch.Count())
	ttftP95, err := ttftSketch.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, ttftP95.Known)
	assert.InDelta(t, routingMetricDurationQuantile(t, 0.95, 20, 30, 40), ttftP95.ValueMilliseconds, 0.000001)

	window, err := GetRoutingMetricRollupsContext(context.Background(), 2, 3, "model-z", 100, 250)
	require.NoError(t, err)
	require.Len(t, window, 1)
	assert.Equal(t, saved.ID, window[0].ID)
	keylessWindow, err := GetRoutingMetricRollupsContext(context.Background(), 3, 0, keyless.ModelName, 0, 500)
	require.NoError(t, err)
	require.Len(t, keylessWindow, 1)
	assert.Zero(t, keylessWindow[0].CredentialID)

	stable, err := GetRoutingMetricRollupsSinceContext(context.Background(), 0, 2)
	require.NoError(t, err)
	require.Len(t, stable, 2)
	assert.Equal(t, 1, stable[0].MemberID)
	assert.Equal(t, 2, stable[1].MemberID)
	_, err = GetRoutingMetricRollupsSinceContext(context.Background(), 0, RoutingMetricRollupMaxQueryLimit+1)
	assert.ErrorIs(t, err, ErrRoutingMetricRollupQueryTooLarge)

	tooLarge := make([]RoutingMetricRollup, RoutingMetricRollupMaxBatch+1)
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), tooLarge), ErrRoutingMetricRollupBatchTooLarge)
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{first, first}), ErrRoutingMetricRollupDuplicateKey)
	invalid := first
	invalid.RequestCount = -1
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{invalid}), ErrRoutingMetricRollupInvalid)
	invalid = first
	invalid.CredentialID = -1
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{invalid}), ErrRoutingMetricRollupInvalid)
	invalid = first
	invalid.ModelName = strings.Repeat("x", 129)
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{invalid}), ErrRoutingMetricRollupInvalid)
	invalid = first
	invalid.LatencySketch = []byte("not-a-sketch")
	assert.ErrorIs(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{invalid}), ErrRoutingMetricRollupInvalid)

	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&RoutingMetricRollup{}).Error)
	old := make([]RoutingMetricRollup, 501)
	for index := range old {
		old[index] = RoutingMetricRollup{
			MemberID: 99, CredentialID: 999, ModelName: "retention", BucketTs: int64(1_000 + index),
			ChannelID: 9, PoolID: 90, LastSnapshotRevision: 1, RequestCount: 1,
		}
	}
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), old[:RoutingMetricRollupMaxBatch]))
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), old[RoutingMetricRollupMaxBatch:]))
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{{
		MemberID: 99, CredentialID: 999, ModelName: "retention", BucketTs: 100_000,
		ChannelID: 9, PoolID: 90, LastSnapshotRevision: 2, RequestCount: 1,
	}}))

	deleted, err := DeleteRoutingMetricRollupsBeforeContext(context.Background(), 50_000)
	require.NoError(t, err)
	assert.Equal(t, int64(501), deleted)
	var retained []RoutingMetricRollup
	require.NoError(t, DB.Order("bucket_ts asc").Find(&retained).Error)
	require.Len(t, retained, 1)
	assert.Equal(t, int64(100_000), retained[0].BucketTs)
}

func routingMetricDurationSketch(t *testing.T, values ...int64) []byte {
	t.Helper()
	sketch := routingdistribution.New()
	for _, value := range values {
		_, err := sketch.Add(value)
		require.NoError(t, err)
	}
	data, err := sketch.Marshal()
	require.NoError(t, err)
	return data
}

func routingMetricDurationQuantile(t *testing.T, quantile float64, values ...int64) float64 {
	t.Helper()
	sketch, err := routingdistribution.DecodeDurationSketch(routingMetricDurationSketch(t, values...), routingdistribution.SketchCodecVersion)
	require.NoError(t, err)
	result, err := sketch.Quantile(quantile)
	require.NoError(t, err)
	require.True(t, result.Known)
	return result.ValueMilliseconds
}

func TestNormalizeRoutingMetricRollupsSortsUniqueLockKeys(t *testing.T) {
	now := time.Unix(10_000, 0)
	rollups := []RoutingMetricRollup{
		{MemberID: 2, CredentialID: 2, ModelName: "model-b", BucketTs: 20, ChannelID: 2, PoolID: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 2, ModelName: "model-b", BucketTs: 30, ChannelID: 1, PoolID: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-z", BucketTs: 30, ChannelID: 1, PoolID: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-a", BucketTs: 40, ChannelID: 1, PoolID: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-a", BucketTs: 10, ChannelID: 1, PoolID: 1, RequestCount: 1},
	}

	normalized, err := normalizeRoutingMetricRollupsAt(rollups, now)
	require.NoError(t, err)
	require.Len(t, normalized, len(rollups))
	for index := 1; index < len(normalized); index++ {
		left := normalized[index-1]
		right := normalized[index]
		assert.True(t,
			left.MemberID < right.MemberID ||
				(left.MemberID == right.MemberID && left.CredentialID < right.CredentialID) ||
				(left.MemberID == right.MemberID && left.CredentialID == right.CredentialID && left.ModelKey < right.ModelKey) ||
				(left.MemberID == right.MemberID && left.CredentialID == right.CredentialID && left.ModelKey == right.ModelKey && left.BucketTs < right.BucketTs),
		)
	}
	modelABuckets := make([]int64, 0, 2)
	for index := range normalized {
		if normalized[index].MemberID == 1 && normalized[index].CredentialID == 1 && normalized[index].ModelName == "model-a" {
			modelABuckets = append(modelABuckets, normalized[index].BucketTs)
		}
	}
	assert.Equal(t, []int64{10, 40}, modelABuckets)
}

func TestNormalizeRoutingMetricRollupsRejectsFutureBucketAndAcceptsHistory(t *testing.T) {
	now := time.Unix(100_000, 0)
	rollup := RoutingMetricRollup{
		MemberID: 1, CredentialID: 1, ModelName: "model-time", BucketTs: 1,
		ChannelID: 1, PoolID: 1, RequestCount: 1,
	}
	_, err := normalizeRoutingMetricRollupsAt([]RoutingMetricRollup{rollup}, now)
	require.NoError(t, err)

	rollup.BucketTs = now.Unix() + int64(routingMetricRollupFutureSkew/time.Second) + 1
	_, err = normalizeRoutingMetricRollupsAt([]RoutingMetricRollup{rollup}, now)
	assert.ErrorIs(t, err, ErrRoutingMetricRollupInvalid)
}

func TestNormalizeRoutingMetricRollupsRejectsUnknownSchemaAndImpossibleCounters(t *testing.T) {
	base := RoutingMetricRollup{
		MemberID: 1, CredentialID: 1, ModelName: "model-invariants", BucketTs: 1,
		ChannelID: 1, PoolID: 1, SchemaVersion: RoutingMetricRollupSchemaVersion,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}
	tests := []struct {
		name   string
		mutate func(*RoutingMetricRollup)
	}{
		{name: "future schema", mutate: func(rollup *RoutingMetricRollup) { rollup.SchemaVersion = 999 }},
		{name: "request count exceeds absolute limit", mutate: func(rollup *RoutingMetricRollup) {
			rollup.RequestCount = routingMetricMaxCounter + 1
		}},
		{name: "success exceeds requests", mutate: func(rollup *RoutingMetricRollup) { rollup.SuccessCount = 2 }},
		{name: "failure exceeds requests", mutate: func(rollup *RoutingMetricRollup) { rollup.FailureCount = 2 }},
		{name: "success and failure exceed requests", mutate: func(rollup *RoutingMetricRollup) { rollup.FailureCount = 1 }},
		{name: "unknown exceeds failures", mutate: func(rollup *RoutingMetricRollup) { rollup.UnknownCount = 1 }},
		{name: "reliability failures exceed reliability requests", mutate: func(rollup *RoutingMetricRollup) {
			rollup.ReliabilityRequestCount = 0
			rollup.ReliabilityFailureCount = 1
		}},
		{name: "ttft count exceeds requests", mutate: func(rollup *RoutingMetricRollup) { rollup.TtftCount = 2 }},
		{name: "error counters exceed requests", mutate: func(rollup *RoutingMetricRollup) {
			rollup.Err4xx = 1
			rollup.Err5xx = 1
		}},
		{name: "retry after count exceeds requests", mutate: func(rollup *RoutingMetricRollup) { rollup.RetryAfterCount = 2 }},
		{name: "latency total exceeds bound", mutate: func(rollup *RoutingMetricRollup) {
			rollup.TotalLatencyMs = routingdistribution.MaxDurationMilliseconds + 1
		}},
		{name: "ttft total exceeds bound", mutate: func(rollup *RoutingMetricRollup) {
			rollup.TtftCount = 1
			rollup.TtftSumMs = routingdistribution.MaxDurationMilliseconds + 1
		}},
		{name: "generation total exceeds bound", mutate: func(rollup *RoutingMetricRollup) {
			rollup.GenerationMs = routingdistribution.MaxDurationMilliseconds + 1
		}},
		{name: "output tokens exceed bound", mutate: func(rollup *RoutingMetricRollup) {
			rollup.OutputTokens = routingMetricMaxAttemptTokens + 1
		}},
		{name: "retry after total exceeds bound", mutate: func(rollup *RoutingMetricRollup) {
			rollup.RetryAfterCount = 1
			rollup.RetryAfterTotalMs = routingMetricMaxRetryAfterMs + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rollup := base
			test.mutate(&rollup)
			_, err := normalizeRoutingMetricRollups([]RoutingMetricRollup{rollup})
			assert.ErrorIs(t, err, ErrRoutingMetricRollupInvalid)
		})
	}

	legacy := base
	legacy.SchemaVersion = 1
	_, err := normalizeRoutingMetricRollups([]RoutingMetricRollup{legacy})
	require.NoError(t, err)
}

func TestRoutingMetricTransactionRetryClassification(t *testing.T) {
	tests := []struct {
		message   string
		retryable bool
	}{
		{message: "Error 1213 (40001): Deadlock found when trying to get lock", retryable: true},
		{message: "ERROR: deadlock detected (SQLSTATE 40P01)", retryable: true},
		{message: "ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)", retryable: true},
		{message: "database is locked (5) (SQLITE_BUSY)", retryable: true},
		{message: "duplicate key value violates unique constraint", retryable: false},
	}
	for _, test := range tests {
		assert.Equal(t, test.retryable, isRetryableRoutingMetricTransactionError(errors.New(test.message)), test.message)
	}
	assert.False(t, isRetryableRoutingMetricTransactionError(nil))
}

func TestRoutingMetricTransactionRetriesOnlyTransientConflicts(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)

	attempts := 0
	err := runRoutingMetricTransactionWithRetry(context.Background(), func(*gorm.DB) error {
		attempts++
		if attempts < 3 {
			return errors.New("ERROR: could not serialize access (SQLSTATE 40001)")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, attempts)

	attempts = 0
	err = runRoutingMetricTransactionWithRetry(context.Background(), func(*gorm.DB) error {
		attempts++
		return errors.New("invalid payload")
	})
	require.EqualError(t, err, "invalid payload")
	assert.Equal(t, 1, attempts)
}
