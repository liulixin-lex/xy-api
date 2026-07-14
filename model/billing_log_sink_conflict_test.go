package model

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBillingLogSinkConflictQuarantineResolutionAndReopen(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-log-conflict.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&BillingLogProjection{}, &BillingLogSinkConflictAudit{}, &BillingLogSinkConflictResolution{},
		&TaskBillingOperation{}, &Log{},
	))
	previousDB := DB
	previousLogDB := LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	DB = db
	LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
	})

	now := time.Unix(1_800_500_000, 0)
	operationKey := "task:conflict:terminal:v1"
	entry := &Log{
		UserId: 41, CreatedAt: now.Unix(), Type: LogTypeConsume, Content: "terminal billing",
		Username: "conflict-user", TokenName: "conflict-token", ModelName: "conflict-model",
		Quota: 25, ChannelId: 71, TokenId: 81, Group: "conflict-group", RequestId: "conflict-request",
	}
	var projection *BillingLogProjection
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		projection, _, createErr = CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
			OperationKey: operationKey, Kind: BillingLogProjectionKindTaskTerminal,
			ReferenceID: 9001, Required: true, Entry: entry,
		}, now)
		return createErr
	}))
	require.NoError(t, db.Model(&BillingLogProjection{}).Where("id = ?", projection.ID).Updates(map[string]any{
		"state": BillingLogProjectionStateCompleted, "outcome": BillingLogProjectionOutcomeWritten,
		"completed_time_ms": now.UnixMilli(),
	}).Error)

	conflict := BillingLogSinkConflict{
		OperationKey:     operationKey,
		Receipts:         fmt.Sprintf("v1:%s,v1:%064x", projection.PayloadHash, 2),
		DistinctReceipts: 2,
		PhysicalRows:     3,
	}
	require.NoError(t, QuarantineBillingLogSinkConflicts(context.Background(), []BillingLogSinkConflict{conflict}, now))
	var audit BillingLogSinkConflictAudit
	require.NoError(t, db.Where("operation_key = ?", operationKey).First(&audit).Error)
	assert.Equal(t, BillingLogSinkConflictStateOpen, audit.State)
	assert.Equal(t, int64(1), audit.Version)
	assert.Equal(t, fmt.Sprintf("\"billing-log-sink-conflict-%d-v1\"", audit.ID),
		BillingLogSinkConflictETag(audit.ID, audit.Version))
	assert.Equal(t, projection.ID, audit.ProjectionID)
	assert.Equal(t, projection.PayloadHash, audit.ExpectedPayloadHash)
	require.NoError(t, db.First(projection, projection.ID).Error)
	assert.Equal(t, BillingLogProjectionStateFailed, projection.State)
	assert.Equal(t, BillingLogProjectionFailureSinkReceiptConflictLate, projection.FailureCode)
	assert.ErrorIs(t, RequeueFailedBillingLogProjection(
		context.Background(), projection.ID, projection.FailureCode, now.Add(time.Second),
	), ErrBillingLogProjectionInvalid)
	require.NoError(t, QuarantineBillingLogSinkConflicts(
		context.Background(), []BillingLogSinkConflict{conflict}, now.Add(500*time.Millisecond),
	))
	require.NoError(t, db.First(&audit, audit.ID).Error)
	assert.Equal(t, int64(1), audit.Version, "an unchanged periodic scan must preserve the operator ETag")
	require.NoError(t, db.First(projection, projection.ID).Error)
	assert.Equal(t, BillingLogProjectionFailureSinkReceiptConflictLate, projection.FailureCode)

	conflict.PhysicalRows = 4
	require.NoError(t, QuarantineBillingLogSinkConflicts(
		context.Background(), []BillingLogSinkConflict{conflict}, now.Add(time.Second),
	))
	require.NoError(t, db.First(&audit, audit.ID).Error)
	assert.Equal(t, int64(2), audit.Version)
	assert.Equal(t, int64(4), audit.PhysicalRows)

	key := operationKey
	require.NoError(t, db.Create(&Log{
		UserId: 41, CreatedAt: now.Unix(), Type: LogTypeConsume, RequestId: "verified-conflict-request",
		BillingOperationKey: &key, BillingPayloadHash: projection.PayloadHash,
		BillingPayloadProtocol: projection.PayloadProtocol,
	}).Error)
	require.NoError(t, ResolveAndRequeueBillingLogSinkConflict(
		context.Background(), audit.ID, audit.Version, 99, "ClickHouse raw receipts were repaired and verified.",
		now.Add(2*time.Second),
	))
	require.NoError(t, ResolveAndRequeueBillingLogSinkConflict(
		context.Background(), audit.ID, audit.Version, 99, "ClickHouse raw receipts were repaired and verified.",
		now.Add(2*time.Second),
	), "a lost acknowledgement must replay the same immutable resolution")
	assert.ErrorIs(t, ResolveAndRequeueBillingLogSinkConflict(
		context.Background(), audit.ID, audit.Version, 99, "A different operator decision.",
		now.Add(2*time.Second),
	), ErrBillingLogSinkConflictPrecondition)
	require.NoError(t, db.First(&audit, audit.ID).Error)
	assert.Equal(t, BillingLogSinkConflictStateResolved, audit.State)
	assert.Equal(t, 99, audit.LastResolvedBy)
	require.NoError(t, db.First(projection, projection.ID).Error)
	assert.Equal(t, BillingLogProjectionStatePending, projection.State)
	assert.Empty(t, projection.FailureCode)
	var resolutionCount int64
	require.NoError(t, db.Model(&BillingLogSinkConflictResolution{}).Count(&resolutionCount).Error)
	assert.Equal(t, int64(1), resolutionCount)

	require.NoError(t, QuarantineBillingLogSinkConflicts(
		context.Background(), []BillingLogSinkConflict{conflict}, now.Add(3*time.Second),
	))
	require.NoError(t, db.First(&audit, audit.ID).Error)
	assert.Equal(t, BillingLogSinkConflictStateOpen, audit.State)
	assert.Equal(t, int64(3), audit.Version)
	require.NoError(t, db.First(projection, projection.ID).Error)
	assert.Equal(t, BillingLogProjectionStateFailed, projection.State)
	assert.Equal(t, BillingLogProjectionFailureSinkReceiptConflict, projection.FailureCode)
	require.NoError(t, db.Model(&BillingLogSinkConflictResolution{}).Count(&resolutionCount).Error)
	assert.Equal(t, int64(1), resolutionCount, "reopening must not erase immutable resolution history")

	require.NoError(t, db.Where("billing_operation_key = ?", operationKey).Delete(&Log{}).Error)
	cleanupAt := now.Add(billingStatsProjectionFailedRetention + time.Hour)
	deleted, err := CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted, "an open sink conflict must protect its projection after raw-log TTL")
	require.NoError(t, db.Model(&BillingLogSinkConflictAudit{}).Where("id = ?", audit.ID).
		Update("state", BillingLogSinkConflictStateResolved).Error)
	deleted, err = CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestBillingLogSinkConflictExternalDatabaseCompatibility(t *testing.T) {
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
			models := []any{
				&BillingLogProjection{}, &BillingLogSinkConflictAudit{},
				&BillingLogSinkConflictResolution{}, &Log{},
			}
			for _, candidate := range models {
				if db.Migrator().HasTable(candidate) {
					t.Skip("refusing to use a non-empty external conflict audit database")
				}
			}
			previousDB := DB
			previousLogDB := LOG_DB
			previousMainType := common.MainDatabaseType()
			previousLogType := common.LogDatabaseType()
			DB = db
			LOG_DB = db
			common.SetDatabaseTypes(test.dbType, test.dbType)
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(
					&BillingLogSinkConflictResolution{}, &BillingLogSinkConflictAudit{},
					&BillingLogProjection{}, &Log{},
				)
				DB = previousDB
				LOG_DB = previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
			})
			require.NoError(t, db.AutoMigrate(models...))
			require.NoError(t, db.AutoMigrate(models...), "conflict audit migration must be idempotent")

			now := time.Unix(1_800_500_100, 0)
			operationKey := "task:external-conflict:terminal:v1"
			var projection *BillingLogProjection
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var createErr error
				projection, _, createErr = CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
					OperationKey: operationKey, Kind: BillingLogProjectionKindTaskTerminal,
					ReferenceID: 9002, Required: true,
					Entry: &Log{
						UserId: 42, CreatedAt: now.Unix(), Type: LogTypeConsume, Content: "external conflict",
						Username: "external-user", ModelName: "external-model", Quota: 7,
						ChannelId: 72, RequestId: "external-conflict-request",
					},
				}, now)
				return createErr
			}))
			require.NoError(t, db.Model(&BillingLogProjection{}).Where("id = ?", projection.ID).Updates(map[string]any{
				"state": BillingLogProjectionStateCompleted, "outcome": BillingLogProjectionOutcomeWritten,
				"completed_time_ms": now.UnixMilli(),
			}).Error)
			conflict := BillingLogSinkConflict{
				OperationKey:     operationKey,
				Receipts:         fmt.Sprintf("v1:%s,v1:%064x", projection.PayloadHash, 9),
				DistinctReceipts: 2, PhysicalRows: 2,
			}
			require.NoError(t, QuarantineBillingLogSinkConflicts(
				context.Background(), []BillingLogSinkConflict{conflict}, now,
			))
			var audit BillingLogSinkConflictAudit
			require.NoError(t, db.Where("operation_key = ?", operationKey).First(&audit).Error)
			assert.Equal(t, BillingLogSinkConflictStateOpen, audit.State)

			key := operationKey
			require.NoError(t, db.Create(&Log{
				UserId: 42, CreatedAt: now.Unix(), Type: LogTypeConsume, RequestId: "external-verified",
				BillingOperationKey: &key, BillingPayloadHash: projection.PayloadHash,
				BillingPayloadProtocol: projection.PayloadProtocol,
			}).Error)
			require.NoError(t, ResolveAndRequeueBillingLogSinkConflict(
				context.Background(), audit.ID, audit.Version, 100,
				"External database conflict receipt was repaired and verified.", now.Add(time.Second),
			))
			require.NoError(t, db.First(projection, projection.ID).Error)
			assert.Equal(t, BillingLogProjectionStatePending, projection.State)
		})
	}
}
