package model

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupBillingLogProjectionTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&BillingLogProjection{}, &Log{}))
	require.NoError(t, DB.Exec("DELETE FROM billing_log_projections").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Exec("DELETE FROM billing_log_projections").Error)
	})
}

func billingLogProjectionEntry(logType int, requestID string) *Log {
	return &Log{
		UserId: 9911, CreatedAt: time.Now().Unix(), Type: logType,
		Content: "durable billing projection", Username: "projection-user", TokenName: "projection-token",
		ModelName: "projection-model", Quota: 25, ChannelId: 71, TokenId: 81,
		Group: "projection-group", RequestId: requestID,
		Other: `{"billing_operation":"projection"}`,
	}
}

func createBillingLogProjectionFixture(
	t *testing.T,
	spec BillingLogProjectionSpec,
	now time.Time,
) *BillingLogProjection {
	t.Helper()
	var projection *BillingLogProjection
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		projection, _, err = CreateBillingLogProjectionTx(tx, spec, now)
		return err
	}))
	require.NotNil(t, projection)
	return projection
}

func TestBillingLogProjectionFreezesRequiredNotRequiredAndInvalidIntents(t *testing.T) {
	setupBillingLogProjectionTest(t)
	now := time.Now()
	invalidEntry := billingLogProjectionEntry(LogTypeConsume, "request-invalid")
	invalidEntry.Other = strings.Repeat("x", maxBillingLogOtherBytes+1)

	notRequired := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9911:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: 9911, Required: false, Entry: invalidEntry,
	}, now)
	assert.Equal(t, BillingLogProjectionStateCompleted, notRequired.State)
	assert.Equal(t, BillingLogProjectionDispositionNotRequired, notRequired.Disposition)
	assert.Equal(t, BillingLogProjectionOutcomeNotRequired, notRequired.Outcome)
	assert.Empty(t, notRequired.Payload)

	invalid := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9912:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: 9912, Required: true, Entry: invalidEntry,
	}, now)
	assert.Equal(t, BillingLogProjectionStateFailed, invalid.State)
	assert.Equal(t, BillingLogProjectionDispositionInvalidPayload, invalid.Disposition)
	assert.Equal(t, "invalid_frozen_payload", invalid.FailureCode)
	assert.Empty(t, invalid.Payload, "invalid content must not be retained in the audit row")
	assert.Empty(t, invalid.PayloadHash)
	failedCount, err := CountFailedBillingLogProjections(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), failedCount)
	assert.False(t, HasRecoverableBillingLogProjections(now))
}

func TestBillingLogProjectionReceiptClaimDeliveryAndAckReplay(t *testing.T) {
	setupBillingLogProjectionTest(t)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = false
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })
	now := time.Now()
	spec := BillingLogProjectionSpec{
		OperationKey: "task:9913:terminal:v1", Kind: BillingLogProjectionKindTaskTerminal,
		ReferenceID: 9913, Required: true,
		Entry: billingLogProjectionEntry(LogTypeRefund, "request-real-9913"),
	}
	projection := createBillingLogProjectionFixture(t, spec, now)
	assert.Equal(t, BillingLogProjectionStatePending, projection.State)
	assert.Equal(t, BillingLogProjectionDispositionPending, projection.Disposition)
	require.NotEmpty(t, projection.Payload)

	var replay *BillingLogProjection
	var created bool
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		replay, created, err = CreateBillingLogProjectionTx(tx, spec, now.Add(time.Second))
		return err
	}))
	assert.False(t, created)
	assert.Equal(t, projection.ID, replay.ID)
	conflict := spec
	conflict.Entry = billingLogProjectionEntry(LogTypeRefund, "different-request")
	err := DB.Transaction(func(tx *gorm.DB) error {
		_, _, createErr := CreateBillingLogProjectionTx(tx, conflict, now)
		return createErr
	})
	assert.ErrorIs(t, err, ErrBillingLogProjectionConflict)
	sourceConflict := spec
	sourceConflict.OperationKey = "task:9913:terminal:alternate:v1"
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, _, createErr := CreateBillingLogProjectionTx(tx, sourceConflict, now)
		return createErr
	})
	assert.ErrorIs(t, err, ErrBillingLogProjectionConflict)

	claimed, won, err := ClaimBillingLogProjection(context.Background(), projection.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	assert.Equal(t, 1, claimed.Attempts)
	_, won, err = ClaimBillingLogProjection(context.Background(), projection.ID, "worker-b", now, time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	completed, err := DeliverClaimedBillingLogProjection(context.Background(), projection.ID, "worker-a")
	require.NoError(t, err)
	assert.Equal(t, BillingLogProjectionStateCompleted, completed.State)
	assert.Equal(t, BillingLogProjectionOutcomeWritten, completed.Outcome)

	// Main-db acknowledgement replay must observe completion without writing a
	// second external receipt.
	_, err = DeliverClaimedBillingLogProjection(context.Background(), projection.ID, "worker-a")
	require.NoError(t, err)
	var logCount int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", spec.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
	var stored Log
	require.NoError(t, LOG_DB.Where("billing_operation_key = ?", spec.OperationKey).First(&stored).Error)
	assert.Equal(t, "request-real-9913", stored.RequestId)
	assert.Equal(t, LogTypeRefund, stored.Type, "refund delivery is independent of LogConsumeEnabled")
}

func TestBillingLogProjectionExternalAckLossRecoversExactlyOnce(t *testing.T) {
	setupBillingLogProjectionTest(t)
	now := time.Now()
	spec := BillingLogProjectionSpec{
		OperationKey: "task:9914:terminal:v1", Kind: BillingLogProjectionKindTaskTerminal,
		ReferenceID: 9914, Required: true,
		Entry: billingLogProjectionEntry(LogTypeConsume, "request-real-9914"),
	}
	projection := createBillingLogProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingLogProjection(context.Background(), projection.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	// Simulate LOG_DB commit followed by process death before the main-db ack.
	require.NoError(t, writeFrozenBillingLog(
		context.Background(), projection.OperationKey, projection.Payload,
		projection.PayloadHash, projection.PayloadProtocol,
	))
	_, won, err = ClaimBillingLogProjection(context.Background(), projection.ID, "worker-b", now.Add(59*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	_, won, err = ClaimBillingLogProjection(context.Background(), projection.ID, "worker-b", now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := DeliverClaimedBillingLogProjection(context.Background(), projection.ID, "worker-b")
	require.NoError(t, err)
	assert.Equal(t, BillingLogProjectionStateCompleted, completed.State)
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", spec.OperationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestBillingLogProjectionConcurrentClaimHasOneWinner(t *testing.T) {
	setupBillingLogProjectionTest(t)
	now := time.Now()
	projection := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9915:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: 9915, Required: true,
		Entry: billingLogProjectionEntry(LogTypeConsume, "request-real-9915"),
	}, now)
	const workers = 12
	var wait sync.WaitGroup
	winnerCh := make(chan string, workers)
	errCh := make(chan error, workers)
	for index := 0; index < workers; index++ {
		owner := "worker-" + string(rune('a'+index))
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, won, err := ClaimBillingLogProjection(context.Background(), projection.ID, owner, now, time.Minute)
			if err != nil {
				errCh <- err
				return
			}
			if won {
				winnerCh <- owner
			}
		}()
	}
	wait.Wait()
	close(winnerCh)
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	winners := make([]string, 0, 1)
	for owner := range winnerCh {
		winners = append(winners, owner)
	}
	require.Len(t, winners, 1)
}

func TestBillingLogProjectionSchemaReadinessRequiresUniqueReceipt(t *testing.T) {
	setupBillingLogProjectionTest(t)
	ready, err := billingLogProjectionSchemaReady(DB)
	require.NoError(t, err)
	assert.True(t, ready)
	require.NoError(t, waitBillingLogProjectionSchemaReady(context.Background(), DB, 0))
	require.NoError(t, DB.Migrator().DropIndex(&BillingLogProjection{}, billingLogProjectionSourceIndex))
	ready, err = billingLogProjectionSchemaReady(DB)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, DB.Migrator().CreateIndex(&BillingLogProjection{}, billingLogProjectionSourceIndex))

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/log-projection-schema.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&BillingLogProjection{}))
	require.NoError(t, db.Migrator().DropIndex(&BillingLogProjection{}, billingLogProjectionOperationIndex))
	require.NoError(t, db.Exec(`CREATE INDEX idx_billing_log_projection_operation_nonunique
		ON billing_log_projections(operation_key)`).Error)
	ready, err = billingLogProjectionSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestBillingLogProjectionRetentionKeepsSourceAndSinkEvidence(t *testing.T) {
	setupBillingLogProjectionTest(t)
	require.NoError(t, DB.AutoMigrate(&AsyncBillingReservation{}))
	require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error) })
	now := time.Now()
	reservation := &AsyncBillingReservation{
		ReservationKey: "log-retention-reservation", ProtocolVersion: 2, Kind: AsyncBillingKindTask,
		PublicTaskID: "log-retention-task", State: AsyncBillingReservationStateTerminal,
		UserID: 9911, FundingSource: "wallet", CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, DB.Create(reservation).Error)
	projection := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9916:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: reservation.ID, Required: true,
		Entry: billingLogProjectionEntry(LogTypeConsume, "request-real-9916"),
	}, now)
	oldCompletedMs := now.Add(-billingStatsProjectionCompletedRetention - time.Hour).UnixMilli()
	require.NoError(t, DB.Model(&BillingLogProjection{}).Where("id = ?", projection.ID).Updates(map[string]any{
		"state": BillingLogProjectionStateCompleted, "outcome": BillingLogProjectionOutcomeWritten,
		"completed_time_ms": oldCompletedMs,
	}).Error)
	deleted, err := CleanupExpiredBillingLogProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	require.NoError(t, DB.Delete(&AsyncBillingReservation{}, reservation.ID).Error)

	key := projection.OperationKey
	require.NoError(t, DB.Create(&Log{
		CreatedAt: now.Unix(), BillingOperationKey: &key,
		BillingPayloadHash: projection.PayloadHash, BillingPayloadProtocol: projection.PayloadProtocol,
	}).Error)
	deleted, err = CleanupExpiredBillingLogProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	require.NoError(t, DB.Where("billing_operation_key = ?", key).Delete(&Log{}).Error)
	deleted, err = CleanupExpiredBillingLogProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestBillingLogProjectionRetentionCursorSkipsReferencedHead(t *testing.T) {
	setupBillingLogProjectionTest(t)
	require.NoError(t, DB.AutoMigrate(&AsyncBillingReservation{}))
	require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error) })
	now := time.Now()
	reservation := &AsyncBillingReservation{
		ReservationKey: "log-cursor-reservation", ProtocolVersion: 2, Kind: AsyncBillingKindTask,
		PublicTaskID: "log-cursor-task", State: AsyncBillingReservationStateTerminal,
		UserID: 9911, FundingSource: "wallet", CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, DB.Create(reservation).Error)
	referenced := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9922:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: reservation.ID, Required: false,
	}, now)
	orphan := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
		OperationKey: "async:9923:accepted:v1", Kind: BillingLogProjectionKindAccepted,
		ReferenceID: reservation.ID + 999, Required: false,
	}, now)
	oldCompletedMs := now.Add(-billingStatsProjectionCompletedRetention - time.Hour).UnixMilli()
	require.NoError(t, DB.Model(&BillingLogProjection{}).Where("id IN ?", []int64{referenced.ID, orphan.ID}).
		Update("completed_time_ms", oldCompletedMs).Error)

	deleted, nextID, hasMore, err := CleanupExpiredBillingLogProjectionsPage(context.Background(), now, 0, 1)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.True(t, hasMore)
	assert.Equal(t, referenced.ID, nextID)
	deleted, nextID, hasMore, err = CleanupExpiredBillingLogProjectionsPage(context.Background(), now, nextID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Zero(t, nextID)
	assert.False(t, hasMore)
	_, err = GetBillingLogProjection(context.Background(), orphan.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = GetBillingLogProjection(context.Background(), referenced.ID)
	require.NoError(t, err)
}

func TestBillingLogProjectionExternalDatabaseCompatibility(t *testing.T) {
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
			if db.Migrator().HasTable(&BillingLogProjection{}) || db.Migrator().HasTable(&Log{}) {
				t.Skip("refusing to use a non-empty external log projection database")
			}
			previousDB := DB
			previousLogDB := LOG_DB
			previousMainType := common.MainDatabaseType()
			previousLogType := common.LogDatabaseType()
			DB = db
			LOG_DB = db
			common.SetDatabaseTypes(test.dbType, test.dbType)
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&BillingLogProjection{}, &Log{})
				DB = previousDB
				LOG_DB = previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
			})
			require.NoError(t, db.AutoMigrate(&Log{}, &BillingLogProjection{}))
			require.NoError(t, db.AutoMigrate(&Log{}, &BillingLogProjection{}),
				"billing log projection migration must be idempotent")
			ready, err := billingLogProjectionSchemaReady(db)
			require.NoError(t, err)
			require.True(t, ready)

			now := time.Now()
			if test.dbType == common.DatabaseTypeMySQL {
				rrSpec := BillingLogProjectionSpec{
					OperationKey: "async:mysql-rr:log:v1", Kind: BillingLogProjectionKindAccepted,
					ReferenceID: 9916, Required: true,
					Entry: billingLogProjectionEntry(LogTypeConsume, "request-mysql-rr-log"),
				}
				contender := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
				require.NoError(t, contender.Error)
				var snapshotCount int64
				require.NoError(t, contender.Model(&BillingLogProjection{}).
					Where("operation_key = ?", rrSpec.OperationKey).Count(&snapshotCount).Error)
				require.Zero(t, snapshotCount)

				winner := createBillingLogProjectionFixture(t, rrSpec, now)
				replay, created, replayErr := CreateBillingLogProjectionTx(contender, rrSpec, now.Add(time.Second))
				require.NoError(t, replayErr)
				assert.False(t, created)
				assert.Equal(t, winner.ID, replay.ID)
				require.NoError(t, contender.Rollback().Error)
			}
			projection := createBillingLogProjectionFixture(t, BillingLogProjectionSpec{
				OperationKey: "async:external:log:v1", Kind: BillingLogProjectionKindAccepted,
				ReferenceID: 9917, Required: true,
				Entry: billingLogProjectionEntry(LogTypeConsume, "request-external-log"),
			}, now)
			const workers = 8
			var wait sync.WaitGroup
			winnerCh := make(chan string, workers)
			errCh := make(chan error, workers)
			for index := 0; index < workers; index++ {
				owner := "external-worker-" + string(rune('a'+index))
				wait.Add(1)
				go func() {
					defer wait.Done()
					_, won, claimErr := ClaimBillingLogProjection(
						context.Background(), projection.ID, owner, now, time.Minute,
					)
					if claimErr != nil {
						errCh <- claimErr
						return
					}
					if won {
						winnerCh <- owner
					}
				}()
			}
			wait.Wait()
			close(winnerCh)
			close(errCh)
			for claimErr := range errCh {
				require.NoError(t, claimErr)
			}
			winners := make([]string, 0, 1)
			for owner := range winnerCh {
				winners = append(winners, owner)
			}
			require.Len(t, winners, 1)
			_, err = DeliverClaimedBillingLogProjection(context.Background(), projection.ID, winners[0])
			require.NoError(t, err)
			_, err = DeliverClaimedBillingLogProjection(context.Background(), projection.ID, winners[0])
			require.NoError(t, err)
			var count int64
			require.NoError(t, db.Model(&Log{}).
				Where("billing_operation_key = ?", projection.OperationKey).Count(&count).Error)
			assert.Equal(t, int64(1), count)
		})
	}
}
