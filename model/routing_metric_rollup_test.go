package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

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
	assert.False(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "latency_p95_ms"))
	assert.False(t, DB.Migrator().HasColumn(&RoutingMetricRollup{}, "ttft_p95_ms"))

	first := RoutingMetricRollup{
		MemberID: 2, CredentialID: 3, ModelName: "model-z", BucketTs: 200,
		ChannelID: 20, PoolID: 200, SchemaVersion: 2, LastSnapshotRevision: 9,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		TotalLatencyMs: 100, TtftSumMs: 20, TtftCount: 1,
		OutputTokens: 10, GenerationMs: 80, Err4xx: 1,
		RetryAfterCount: 1, RetryAfterTotalMs: 250,
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
	additive.RequestCount = 2
	additive.SuccessCount = 0
	additive.FailureCount = 2
	additive.UnknownCount = 1
	additive.ReliabilityRequestCount = 2
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
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{additive}))

	var saved RoutingMetricRollup
	require.NoError(t, DB.Where(
		"member_id = ? AND credential_id = ? AND model_name = ? AND bucket_ts = ?",
		first.MemberID, first.CredentialID, first.ModelName, first.BucketTs,
	).First(&saved).Error)
	assert.Equal(t, 2, saved.SchemaVersion)
	assert.Equal(t, int64(9), saved.LastSnapshotRevision)
	assert.Equal(t, int64(3), saved.RequestCount)
	assert.Equal(t, int64(1), saved.SuccessCount)
	assert.Equal(t, int64(2), saved.FailureCount)
	assert.Equal(t, int64(1), saved.UnknownCount)
	assert.Equal(t, int64(3), saved.ReliabilityRequestCount)
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
