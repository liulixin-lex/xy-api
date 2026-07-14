package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBillingProjectionAdminOperationProvidesDurableIdempotencyAndCAS(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-projection-admin.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&BillingStatsProjection{}, &BillingProjectionAdminOperation{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	now := time.Unix(1_800_700_000, 0)
	failed := BillingStatsProjection{
		OperationKey: "async:admin:accepted:v1", ProtocolVersion: BillingStatsProjectionProtocol,
		Kind: BillingStatsProjectionKindAccepted, ReferenceID: 7001, UserID: 11, ChannelID: 21,
		QuotaDelta: 100, RequestDelta: 1, State: BillingStatsProjectionStateFailed,
		FailureCode: "retry_exhausted", UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		CreatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, db.Create(&failed).Error)
	spec := BillingProjectionAdminOperationSpec{
		TargetID: failed.ID, ActorUserID: 99, ExpectedRevision: failed.UpdatedTimeMs,
		ExpectedFailureCode: failed.FailureCode,
		IdempotencyKeyHash:  strings.Repeat("a", 64), RequestHash: strings.Repeat("b", 64),
	}

	first, err := RequeueFailedBillingStatsProjectionAdmin(context.Background(), spec, now.Add(time.Second))
	require.NoError(t, err)
	assert.False(t, first.Replayed)
	assert.Equal(t, BillingProjectionAdminOutcomeSucceeded, first.Operation.Outcome)
	require.NoError(t, db.First(&failed, failed.ID).Error)
	assert.Equal(t, BillingStatsProjectionStatePending, failed.State)

	replayed, err := RequeueFailedBillingStatsProjectionAdmin(context.Background(), spec, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, first.Operation.ID, replayed.Operation.ID)

	conflicting := spec
	conflicting.RequestHash = strings.Repeat("c", 64)
	_, err = RequeueFailedBillingStatsProjectionAdmin(context.Background(), conflicting, now.Add(3*time.Second))
	assert.ErrorIs(t, err, ErrBillingProjectionAdminIdempotencyConflict)

	stale := BillingStatsProjection{
		OperationKey: "async:admin:accepted:v2", ProtocolVersion: BillingStatsProjectionProtocol,
		Kind: BillingStatsProjectionKindAccepted, ReferenceID: 7002, UserID: 12, ChannelID: 22,
		QuotaDelta: 100, RequestDelta: 1, State: BillingStatsProjectionStateFailed,
		FailureCode: "retry_exhausted", UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		CreatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, db.Create(&stale).Error)
	staleSpec := BillingProjectionAdminOperationSpec{
		TargetID: stale.ID, ActorUserID: 99, ExpectedRevision: stale.UpdatedTimeMs - 1,
		ExpectedFailureCode: stale.FailureCode,
		IdempotencyKeyHash:  strings.Repeat("d", 64), RequestHash: strings.Repeat("e", 64),
	}
	_, err = RequeueFailedBillingStatsProjectionAdmin(context.Background(), staleSpec, now.Add(time.Second))
	assert.ErrorIs(t, err, ErrBillingProjectionAdminPrecondition)
	_, err = RequeueFailedBillingStatsProjectionAdmin(context.Background(), staleSpec, now.Add(2*time.Second))
	assert.ErrorIs(t, err, ErrBillingProjectionAdminPrecondition)
	require.NoError(t, db.First(&stale, stale.ID).Error)
	assert.Equal(t, BillingStatsProjectionStateFailed, stale.State)
}

func TestBillingProjectionAdminOperationExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql57", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres96", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			models := []any{&BillingStatsProjection{}, &BillingProjectionAdminOperation{}}
			for _, candidate := range models {
				if db.Migrator().HasTable(candidate) {
					t.Skip("refusing to use a non-empty external billing projection admin database")
				}
			}
			previousDB := DB
			DB = db
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&BillingProjectionAdminOperation{}, &BillingStatsProjection{})
				DB = previousDB
			})
			require.NoError(t, db.AutoMigrate(models...))
			require.NoError(t, db.AutoMigrate(models...), "admin operation migration must be idempotent")

			now := time.Unix(1_800_850_000, 0)
			projection := BillingStatsProjection{
				OperationKey: "async:external-admin:accepted:v1", ProtocolVersion: BillingStatsProjectionProtocol,
				Kind: BillingStatsProjectionKindAccepted, ReferenceID: 8501, UserID: 1, ChannelID: 1,
				QuotaDelta: 1, RequestDelta: 1, State: BillingStatsProjectionStateFailed,
				FailureCode: "retry_exhausted", CreatedTimeMs: now.UnixMilli(),
				UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
			}
			require.NoError(t, db.Create(&projection).Error)
			result, err := RequeueFailedBillingStatsProjectionAdmin(
				context.Background(), BillingProjectionAdminOperationSpec{
					TargetID: projection.ID, ActorUserID: 1, ExpectedRevision: projection.UpdatedTimeMs,
					ExpectedFailureCode: projection.FailureCode,
					IdempotencyKeyHash:  strings.Repeat("a", 64), RequestHash: strings.Repeat("b", 64),
				}, now.Add(time.Second),
			)
			require.NoError(t, err)
			assert.Equal(t, BillingProjectionAdminOutcomeSucceeded, result.Operation.Outcome)
		})
	}
}

func TestBillingProjectionAdminOperationCleanupUsesPersistentCursor(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-projection-admin-cleanup.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&BillingProjectionAdminOperation{}))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	now := time.Unix(1_800_800_000, 0)
	rows := make([]BillingProjectionAdminOperation, 0, 3)
	for index := 0; index < 3; index++ {
		rows = append(rows, BillingProjectionAdminOperation{
			IdempotencyKeyHash: strings.Repeat(string(rune('a'+index)), 64),
			RequestHash:        strings.Repeat(string(rune('d'+index)), 64),
			Action:             BillingProjectionAdminActionStatsRequeue, TargetID: int64(index + 1), ActorUserID: 99,
			ExpectedRevision: 1, ExpectedFailureCode: "retry_exhausted",
			State: BillingProjectionAdminStateCompleted, Outcome: BillingProjectionAdminOutcomeSucceeded,
			CreatedTimeMs: now.Add(-billingProjectionAdminRetention - time.Hour).UnixMilli(),
			UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		})
	}
	require.NoError(t, db.Create(&rows).Error)

	deleted, cursor, hasMore, err := CleanupExpiredBillingProjectionAdminOperationsPage(
		context.Background(), now, 0, 2,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	assert.True(t, hasMore)
	assert.Greater(t, cursor, int64(0))
	deleted, cursor, hasMore, err = CleanupExpiredBillingProjectionAdminOperationsPage(
		context.Background(), now, cursor, 2,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.False(t, hasMore)
	assert.Zero(t, cursor)
}
