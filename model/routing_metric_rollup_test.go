package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
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
	prepareLegacyRoutingMetricRollupIndex(t, DB)
	legacyRows := []RoutingMetricRollup{
		{
			MemberID: 91, CredentialID: 901, ModelName: "legacy-v1",
			ModelKey: routingMetricRollupModelKey("legacy-v1"), BucketTs: 91,
			ChannelID: 901, PoolID: 9001, SchemaVersion: 1, LastSnapshotRevision: 5,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		},
		{
			MemberID: 92, CredentialID: 902, ModelName: "legacy-v2",
			ModelKey: routingMetricRollupModelKey("legacy-v2"), BucketTs: 92,
			ChannelID: 902, PoolID: 9002, SchemaVersion: 2, LastSnapshotRevision: 6,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		},
	}
	require.NoError(t, DB.Create(&legacyRows).Error)
	require.NoError(t, MigrateRoutingMetricRollupRevisionKeyWithOptions(DB, RoutingMetricRollupMigrationOptions{
		AlphaDrained: true,
	}))
	require.NoError(t, MigrateRoutingMetricRollupRevisionKey(DB))
	columns, unique, exists, err := routingMetricRollupIndexDefinition(DB, routingMetricRollupUniqueIndex)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, unique)
	assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"}, columns)
	_, _, exists, err = routingMetricRollupIndexDefinition(DB, routingMetricRollupLegacyUniqueIndex)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.False(t, DB.Migrator().HasIndex(&routingMetricRollupRevisionGuard{}, routingMetricRollupRevisionGuardIndex))
	for _, legacy := range legacyRows {
		var preserved RoutingMetricRollup
		require.NoError(t, DB.First(&preserved, legacy.ID).Error)
		assert.Equal(t, legacy.SchemaVersion, preserved.SchemaVersion)
		assert.Equal(t, legacy.LastSnapshotRevision, preserved.LastSnapshotRevision)
		assert.Equal(t, legacy.RequestCount, preserved.RequestCount)
		assert.Equal(t, legacy.ReliabilityRequestCount, preserved.ReliabilityRequestCount)
	}

	for _, legacy := range legacyRows {
		incoming := legacy
		incoming.ID = 0
		incoming.SchemaVersion = RoutingMetricRollupSchemaVersion
		incoming.RequestCount = 1
		incoming.SuccessCount = 1
		incoming.ReliabilityRequestCount = 1
		require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{incoming}))

		var preserved RoutingMetricRollup
		require.NoError(t, DB.First(&preserved, legacy.ID).Error)
		assert.Equal(t, legacy.SchemaVersion, preserved.SchemaVersion)
		assert.Equal(t, legacy.LastSnapshotRevision, preserved.LastSnapshotRevision)
		assert.Equal(t, int64(2), preserved.RequestCount)

		isolated := incoming
		isolated.LastSnapshotRevision++
		require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{isolated}))
		history, historyErr := GetRoutingMetricRollupsContext(
			context.Background(), legacy.MemberID, legacy.CredentialID, legacy.ModelName, legacy.BucketTs, legacy.BucketTs,
		)
		require.NoError(t, historyErr)
		require.Len(t, history, 2)
		assert.Equal(t, legacy.LastSnapshotRevision, history[0].LastSnapshotRevision)
		assert.Equal(t, legacy.LastSnapshotRevision+1, history[1].LastSnapshotRevision)
		exact, exactErr := GetRoutingMetricRollupsForSnapshotRevisionContext(
			context.Background(), legacy.MemberID, legacy.CredentialID, legacy.ModelName,
			legacy.LastSnapshotRevision, legacy.BucketTs, legacy.BucketTs,
		)
		require.NoError(t, exactErr)
		assert.Empty(t, exact)
		exact, exactErr = GetRoutingMetricRollupsForSnapshotRevisionContext(
			context.Background(), legacy.MemberID, legacy.CredentialID, legacy.ModelName,
			legacy.LastSnapshotRevision+1, legacy.BucketTs, legacy.BucketTs,
		)
		require.NoError(t, exactErr)
		require.Len(t, exact, 1)
		assert.Equal(t, RoutingMetricRollupSchemaVersion, exact[0].SchemaVersion)
	}
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&RoutingMetricRollup{}).Error)

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
		ChannelID: 20, PoolID: 200, SchemaVersion: RoutingMetricRollupSchemaVersion, LastSnapshotRevision: 9,
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
	additive.SchemaVersion = RoutingMetricRollupSchemaVersion
	additive.LastSnapshotRevision = first.LastSnapshotRevision
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
		"member_id = ? AND credential_id = ? AND model_name = ? AND bucket_ts = ? AND last_snapshot_revision = ?",
		first.MemberID, first.CredentialID, first.ModelName, first.BucketTs, first.LastSnapshotRevision,
	).First(&saved).Error)
	assert.Equal(t, RoutingMetricRollupSchemaVersion, saved.SchemaVersion)
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

	revisionTen := RoutingMetricRollup{
		MemberID: first.MemberID, CredentialID: first.CredentialID,
		ModelName: first.ModelName, BucketTs: first.BucketTs,
		ChannelID: first.ChannelID, PoolID: first.PoolID,
		SchemaVersion: RoutingMetricRollupSchemaVersion, LastSnapshotRevision: 10,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{revisionTen}))
	window, err := GetRoutingMetricRollupsContext(context.Background(), 2, 3, "model-z", 100, 250)
	require.NoError(t, err)
	require.Len(t, window, 2)
	assert.Equal(t, saved.ID, window[0].ID)
	assert.Equal(t, int64(9), window[0].LastSnapshotRevision)
	assert.Equal(t, int64(10), window[1].LastSnapshotRevision)
	exactRevision, err := GetRoutingMetricRollupsForSnapshotRevisionContext(
		context.Background(), 2, 3, "model-z", 9, 100, 250,
	)
	require.NoError(t, err)
	require.Len(t, exactRevision, 1)
	assert.Equal(t, saved.ID, exactRevision[0].ID)
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
	distinctRevisions, err := normalizeRoutingMetricRollups([]RoutingMetricRollup{first, revisionTen})
	require.NoError(t, err)
	require.Len(t, distinctRevisions, 2)
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
	invalid = first
	invalid.LastSnapshotRevision = 0
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

type routingMetricRollupLegacyIndex struct {
	MemberID     int    `gorm:"uniqueIndex:idx_routing_metric_rollup_key,priority:1"`
	CredentialID int    `gorm:"uniqueIndex:idx_routing_metric_rollup_key,priority:2"`
	ModelKey     string `gorm:"type:char(64);uniqueIndex:idx_routing_metric_rollup_key,priority:3"`
	BucketTs     int64  `gorm:"bigint;uniqueIndex:idx_routing_metric_rollup_key,priority:4"`
}

func (routingMetricRollupLegacyIndex) TableName() string {
	return "routing_metric_rollups"
}

type routingMetricRollupV3LegacyNameIndex struct {
	MemberID             int    `gorm:"uniqueIndex:idx_routing_metric_rollup_key,priority:1"`
	CredentialID         int    `gorm:"uniqueIndex:idx_routing_metric_rollup_key,priority:2"`
	ModelKey             string `gorm:"type:char(64);uniqueIndex:idx_routing_metric_rollup_key,priority:3"`
	BucketTs             int64  `gorm:"bigint;uniqueIndex:idx_routing_metric_rollup_key,priority:4"`
	LastSnapshotRevision int64  `gorm:"bigint;uniqueIndex:idx_routing_metric_rollup_key,priority:5"`
}

func (routingMetricRollupV3LegacyNameIndex) TableName() string {
	return "routing_metric_rollups"
}

func prepareLegacyRoutingMetricRollupIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&RoutingMetricRollup{}))
	if db.Migrator().HasIndex(&routingMetricRollupRevisionGuard{}, routingMetricRollupRevisionGuardIndex) {
		require.NoError(t, db.Migrator().DropIndex(&routingMetricRollupRevisionGuard{}, routingMetricRollupRevisionGuardIndex))
	}
	if db.Migrator().HasIndex(&RoutingMetricRollup{}, routingMetricRollupUniqueIndex) {
		require.NoError(t, db.Migrator().DropIndex(&RoutingMetricRollup{}, routingMetricRollupUniqueIndex))
	}
	if db.Migrator().HasIndex(&routingMetricRollupLegacyIndex{}, routingMetricRollupLegacyUniqueIndex) {
		require.NoError(t, db.Migrator().DropIndex(&routingMetricRollupLegacyIndex{}, routingMetricRollupLegacyUniqueIndex))
	}
	require.NoError(t, db.Migrator().CreateIndex(&routingMetricRollupLegacyIndex{}, routingMetricRollupLegacyUniqueIndex))
	columns, unique, exists, err := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, unique)
	assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts"}, columns)
}

func TestRoutingMetricRollupMigrationAcceptsExistingFiveColumnIndex(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	prepareLegacyRoutingMetricRollupIndex(t, db)
	require.NoError(t, db.Migrator().DropIndex(&routingMetricRollupLegacyIndex{}, routingMetricRollupLegacyUniqueIndex))
	require.NoError(t, db.Migrator().CreateIndex(&routingMetricRollupV3LegacyNameIndex{}, routingMetricRollupLegacyUniqueIndex))

	require.NoError(t, MigrateRoutingMetricRollupRevisionKey(db))
	ready, err := RoutingMetricRollupRevisionKeySchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)
	columns, unique, exists, err := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
	require.NoError(t, err)
	if exists {
		assert.True(t, unique)
		assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"}, columns)
	}
	columns, unique, exists, err = routingMetricRollupIndexDefinition(db, routingMetricRollupUniqueIndex)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, unique)
	assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"}, columns)
}

func TestRoutingMetricRollupMigrationResumesFromEveryPhase(t *testing.T) {
	tests := []struct {
		name         string
		expand       bool
		contract     bool
		initialReady bool
	}{
		{name: "legacy only"},
		{name: "expanded before legacy drop", expand: true},
		{name: "contracted before final verification", expand: true, contract: true, initialReady: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			prepareLegacyRoutingMetricRollupIndex(t, db)
			if test.expand {
				require.NoError(t, db.Migrator().CreateIndex(&RoutingMetricRollup{}, routingMetricRollupUniqueIndex))
			}
			if test.contract {
				require.NoError(t, db.Migrator().DropIndex(&RoutingMetricRollup{}, routingMetricRollupLegacyUniqueIndex))
			}

			ready, err := RoutingMetricRollupRevisionKeySchemaReady(db)
			require.NoError(t, err)
			assert.Equal(t, test.initialReady, ready)
			if test.initialReady {
				require.NoError(t, WaitRoutingMetricRollupRevisionKeySchemaReady(context.Background(), db, 0))
			} else {
				assert.ErrorIs(t, WaitRoutingMetricRollupRevisionKeySchemaReady(context.Background(), db, 0), ErrRoutingMetricRollupSchemaNotReady)
			}

			if !test.contract {
				assert.ErrorIs(t, MigrateRoutingMetricRollupRevisionKey(db), ErrRoutingMetricRollupAlphaDrainRequired)
				columns, unique, exists, indexErr := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
				require.NoError(t, indexErr)
				assert.True(t, exists)
				assert.True(t, unique)
				assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts"}, columns)
				columns, unique, exists, indexErr = routingMetricRollupIndexDefinition(db, routingMetricRollupUniqueIndex)
				require.NoError(t, indexErr)
				assert.True(t, exists)
				assert.True(t, unique)
				assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"}, columns)
			}
			require.NoError(t, MigrateRoutingMetricRollupRevisionKeyWithOptions(db, RoutingMetricRollupMigrationOptions{
				AlphaDrained: true,
			}))
			require.NoError(t, MigrateRoutingMetricRollupRevisionKey(db))
			ready, err = RoutingMetricRollupRevisionKeySchemaReady(db)
			require.NoError(t, err)
			assert.True(t, ready)
			require.NoError(t, WaitRoutingMetricRollupRevisionKeySchemaReady(context.Background(), db, 0))
		})
	}
}

func TestRoutingMetricRollupMigrationHonorsCanceledContext(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	prepareLegacyRoutingMetricRollupIndex(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, MigrateRoutingMetricRollupRevisionKeyContext(ctx, db), context.Canceled)
	assert.ErrorIs(t, WaitRoutingMetricRollupRevisionKeySchemaReady(ctx, db, time.Second), context.Canceled)
}

func TestRoutingMetricRollupConcurrentMigrationSQLite(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "clean install"
		if legacy {
			name = "legacy finalize"
		}
		t.Run(name, func(t *testing.T) {
			dsn := fmt.Sprintf(
				"file:%s?_busy_timeout=5000&_journal_mode=WAL",
				filepath.ToSlash(filepath.Join(t.TempDir(), "routing-rollup.db")),
			)
			first := openRoutingMetricRollupSQLiteConnection(t, dsn)
			second := openRoutingMetricRollupSQLiteConnection(t, dsn)
			runRoutingMetricRollupConcurrentMigrationContract(t, first, second, legacy)
		})
	}
}

func TestRoutingMetricRollupConcurrentMigrationExternalCompatibility(t *testing.T) {
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
			first := openRoutingExternalTestDB(t, test.dbType, dsn)
			second := openRoutingMetricRollupExternalConnection(t, test.dbType, dsn)
			t.Run("clean install", func(t *testing.T) {
				runRoutingMetricRollupConcurrentMigrationContract(t, first, second, false)
			})
			require.NoError(t, first.Migrator().DropTable(&RoutingMetricRollup{}))
			t.Run("legacy finalize", func(t *testing.T) {
				runRoutingMetricRollupConcurrentMigrationContract(t, first, second, true)
			})
		})
	}
}

func runRoutingMetricRollupConcurrentMigrationContract(t *testing.T, first *gorm.DB, second *gorm.DB, legacy bool) {
	t.Helper()
	if legacy {
		prepareLegacyRoutingMetricRollupIndex(t, first)
	}
	ready, err := RoutingMetricRollupRevisionKeySchemaReady(second)
	require.NoError(t, err)
	assert.False(t, ready)

	start := make(chan struct{})
	errorsByConnection := make(chan error, 2)
	var workers sync.WaitGroup
	for _, db := range []*gorm.DB{first, second} {
		workers.Add(1)
		go func(connection *gorm.DB) {
			defer workers.Done()
			<-start
			options := RoutingMetricRollupMigrationOptions{}
			if legacy {
				options.AlphaDrained = true
			}
			errorsByConnection <- MigrateRoutingMetricRollupRevisionKeyWithOptions(
				connection,
				options,
			)
		}(db)
	}
	close(start)
	workers.Wait()
	close(errorsByConnection)
	for err := range errorsByConnection {
		require.NoError(t, err)
	}

	for _, db := range []*gorm.DB{first, second} {
		ready, err = RoutingMetricRollupRevisionKeySchemaReady(db)
		require.NoError(t, err)
		assert.True(t, ready)
		require.NoError(t, WaitRoutingMetricRollupRevisionKeySchemaReady(context.Background(), db, 0))
		require.NoError(t, MigrateRoutingMetricRollupRevisionKey(db))
	}
	columns, unique, exists, err := routingMetricRollupIndexDefinition(first, routingMetricRollupUniqueIndex)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, unique)
	assert.Equal(t, []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"}, columns)
	_, _, exists, err = routingMetricRollupIndexDefinition(first, routingMetricRollupLegacyUniqueIndex)
	require.NoError(t, err)
	assert.False(t, exists)
}

func openRoutingMetricRollupSQLiteConnection(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func openRoutingMetricRollupExternalConnection(t *testing.T, dbType common.DatabaseType, dsn string) *gorm.DB {
	t.Helper()
	var (
		db  *gorm.DB
		err error
	)
	switch dbType {
	case common.DatabaseTypeMySQL:
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	case common.DatabaseTypePostgreSQL:
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	default:
		t.Fatalf("unsupported external routing test database type %q", dbType)
	}
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
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
		{MemberID: 2, CredentialID: 2, ModelName: "model-b", BucketTs: 20, ChannelID: 2, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 2, ModelName: "model-b", BucketTs: 30, ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-z", BucketTs: 30, ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-a", BucketTs: 40, ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-a", BucketTs: 10, ChannelID: 1, PoolID: 1, LastSnapshotRevision: 2, RequestCount: 1},
		{MemberID: 1, CredentialID: 1, ModelName: "model-a", BucketTs: 10, ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1},
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
				(left.MemberID == right.MemberID && left.CredentialID == right.CredentialID && left.ModelKey == right.ModelKey && left.BucketTs < right.BucketTs) ||
				(left.MemberID == right.MemberID && left.CredentialID == right.CredentialID && left.ModelKey == right.ModelKey && left.BucketTs == right.BucketTs && left.LastSnapshotRevision < right.LastSnapshotRevision),
		)
	}
	modelAKeys := make([][2]int64, 0, 3)
	for index := range normalized {
		if normalized[index].MemberID == 1 && normalized[index].CredentialID == 1 && normalized[index].ModelName == "model-a" {
			modelAKeys = append(modelAKeys, [2]int64{normalized[index].BucketTs, normalized[index].LastSnapshotRevision})
		}
	}
	assert.Equal(t, [][2]int64{{10, 1}, {10, 2}, {40, 1}}, modelAKeys)
}

func TestNormalizeRoutingMetricRollupsRejectsFutureBucketAndAcceptsHistory(t *testing.T) {
	now := time.Unix(100_000, 0)
	rollup := RoutingMetricRollup{
		MemberID: 1, CredentialID: 1, ModelName: "model-time", BucketTs: 1,
		ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1,
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
		LastSnapshotRevision: 1, RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
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
	legacy.SchemaVersion = 2
	_, err = normalizeRoutingMetricRollups([]RoutingMetricRollup{legacy})
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
