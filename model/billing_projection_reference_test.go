package model

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type billingProjectionSourceCase struct {
	name             string
	kind             string
	terminalState    string
	nonterminalState string
	createSource     func(t *testing.T, referenceID int64, operationKey string, state string)
	replaySource     func(t *testing.T, referenceID int64, now time.Time)
}

func billingProjectionSourceCases() []billingProjectionSourceCase {
	return []billingProjectionSourceCase{
		{
			name: "accepted reservation", kind: BillingStatsProjectionKindAccepted,
			terminalState: AsyncBillingReservationStateTerminal, nonterminalState: AsyncBillingReservationStateReserved,
			createSource: func(t *testing.T, referenceID int64, operationKey string, state string) {
				t.Helper()
				key := operationKey
				require.NoError(t, DB.Create(&AsyncBillingReservation{
					ID: referenceID, ReservationKey: fmt.Sprintf("retention-reservation-%d", referenceID),
					ProtocolVersion: 2, Kind: AsyncBillingKindTask,
					PublicTaskID: fmt.Sprintf("retention-task-%d", referenceID), State: state,
					UserID: 1, FundingSource: TaskBillingSourceWallet,
					AcceptedProjectionKey: &key, AcceptedProjectionState: AsyncBillingAcceptedProjectionCompleted,
					CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
				}).Error)
			},
			replaySource: func(t *testing.T, referenceID int64, now time.Time) {
				t.Helper()
				require.NoError(t, ProcessAsyncBillingAcceptedProjection(context.Background(), referenceID, now))
			},
		},
		{
			name: "task terminal operation", kind: BillingStatsProjectionKindTaskTerminal,
			terminalState: TaskBillingOperationStateCompleted, nonterminalState: TaskBillingOperationStatePending,
			createSource: func(t *testing.T, referenceID int64, operationKey string, state string) {
				t.Helper()
				taskID := referenceID + 10_000
				require.NoError(t, DB.Create(&Task{
					ID: taskID, TaskID: fmt.Sprintf("retention-task-source-%d", referenceID),
					UserId: 1, ChannelId: 1, Status: TaskStatusSuccess, Progress: "100%",
				}).Error)
				require.NoError(t, DB.Create(&TaskBillingOperation{
					ID: referenceID, TaskID: taskID, OperationKey: operationKey,
					TerminalStatus: TaskStatusSuccess,
					Kind:           TaskBillingOperationKindNoop, State: state, UserID: 1, ChannelID: 1,
					BillingSource: TaskBillingSourceWallet, LogState: TaskBillingOperationLogNotRequired,
					CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
				}).Error)
			},
			replaySource: func(t *testing.T, referenceID int64, now time.Time) {
				t.Helper()
				_, err := CompleteTaskBillingOperation(context.Background(), referenceID, "retention-replay", now)
				require.NoError(t, err)
			},
		},
		{
			name: "Midjourney terminal operation", kind: BillingStatsProjectionKindMidjourneyTerminal,
			terminalState: TaskBillingOperationStateCompleted, nonterminalState: TaskBillingOperationStatePending,
			createSource: func(t *testing.T, referenceID int64, operationKey string, state string) {
				t.Helper()
				midjourneyID := int(referenceID + 20_000)
				require.NoError(t, DB.Create(&Midjourney{
					Id: midjourneyID, UserId: 1, ChannelId: 1, Status: "SUCCESS", Progress: "100%",
				}).Error)
				require.NoError(t, DB.Create(&MidjourneyBillingOperation{
					ID: referenceID, MidjourneyID: midjourneyID, OperationKey: operationKey,
					TerminalStatus: "SUCCESS",
					Kind:           TaskBillingOperationKindNoop, State: state, UserID: 1, ChannelID: 1,
					BillingSource: TaskBillingSourceWallet, LogState: TaskBillingOperationLogNotRequired,
					CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
				}).Error)
			},
			replaySource: func(t *testing.T, referenceID int64, now time.Time) {
				t.Helper()
				_, err := CompleteMidjourneyBillingOperation(context.Background(), referenceID, "retention-replay", now)
				require.NoError(t, err)
			},
		},
	}
}

func resetBillingProjectionReferenceFixtures(t *testing.T) {
	t.Helper()
	for _, table := range []string{
		"billing_stats_projections", "billing_log_projections", "async_billing_reservations",
		"task_billing_operations", "midjourney_billing_operations", "tasks", "midjourneys", "logs",
	} {
		require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
	}
}

func createCompletedBillingProjectionPair(
	t *testing.T,
	kind string,
	referenceID int64,
	operationKey string,
	now time.Time,
) {
	t.Helper()
	requestDelta := 0
	if kind == BillingStatsProjectionKindAccepted {
		requestDelta = 1
	}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		stats, _, err := CreateBillingStatsProjectionTx(tx, BillingStatsProjectionSpec{
			OperationKey: operationKey, Kind: kind, ReferenceID: referenceID,
			UserID: 1, ChannelID: 1, QuotaDelta: 1, RequestDelta: requestDelta,
		}, now)
		if err != nil {
			return err
		}
		if err := tx.Model(&BillingStatsProjection{}).Where("id = ?", stats.ID).Updates(map[string]any{
			"state": BillingStatsProjectionStateCompleted, "completed_time_ms": now.UnixMilli(),
		}).Error; err != nil {
			return err
		}
		_, _, err = CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
			OperationKey: operationKey, Kind: kind, ReferenceID: referenceID, Required: false,
		}, now)
		return err
	}))
}

func TestBillingProjectionRetentionKeepsEvidenceUntilSourceReceiptIsRemoved(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(
		&AsyncBillingReservation{}, &Task{}, &Midjourney{}, &TaskBillingOperation{}, &MidjourneyBillingOperation{},
		&BillingStatsProjection{}, &BillingLogProjection{}, &Log{},
	))
	t.Cleanup(func() { resetBillingProjectionReferenceFixtures(t) })

	for index, test := range billingProjectionSourceCases() {
		t.Run(test.name, func(t *testing.T) {
			resetBillingProjectionReferenceFixtures(t)
			now := time.Unix(1_800_600_000+int64(index), 0)
			referenceID := int64(20_000 + index)
			operationKey := fmt.Sprintf("retention:%d:terminal:v1", referenceID)
			test.createSource(t, referenceID, operationKey, test.terminalState)
			createCompletedBillingProjectionPair(t, test.kind, referenceID, operationKey, now)

			cleanupAt := now.Add(billingStatsProjectionCompletedRetention + time.Hour)
			statsDeleted, err := CleanupExpiredBillingStatsProjections(context.Background(), cleanupAt, 100)
			require.NoError(t, err)
			logsDeleted, err := CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 100)
			require.NoError(t, err)
			assert.Zero(t, statsDeleted)
			assert.Zero(t, logsDeleted)

			switch test.kind {
			case BillingStatsProjectionKindAccepted:
				require.NoError(t, DB.Delete(&AsyncBillingReservation{}, referenceID).Error)
			case BillingStatsProjectionKindTaskTerminal:
				require.NoError(t, DB.Delete(&TaskBillingOperation{}, referenceID).Error)
			case BillingStatsProjectionKindMidjourneyTerminal:
				require.NoError(t, DB.Delete(&MidjourneyBillingOperation{}, referenceID).Error)
			}
			statsDeleted, err = CleanupExpiredBillingStatsProjections(context.Background(), cleanupAt, 100)
			require.NoError(t, err)
			logsDeleted, err = CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 100)
			require.NoError(t, err)
			assert.Equal(t, int64(1), statsDeleted)
			assert.Equal(t, int64(1), logsDeleted)
		})
	}
}

func TestBillingProjectionRetentionProtectsNonterminalAndMismatchedSources(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(
		&AsyncBillingReservation{}, &Task{}, &Midjourney{}, &TaskBillingOperation{}, &MidjourneyBillingOperation{},
		&BillingStatsProjection{}, &BillingLogProjection{}, &Log{},
	))
	t.Cleanup(func() { resetBillingProjectionReferenceFixtures(t) })

	variants := []struct {
		name  string
		state func(billingProjectionSourceCase) string
		key   func(string) string
	}{
		{name: "nonterminal", state: func(test billingProjectionSourceCase) string { return test.nonterminalState }, key: func(key string) string { return key }},
		{name: "identity mismatch", state: func(test billingProjectionSourceCase) string { return test.terminalState }, key: func(key string) string { return key + ":mismatch" }},
	}
	caseIndex := 0
	for _, source := range billingProjectionSourceCases() {
		for _, variant := range variants {
			t.Run(source.name+"/"+variant.name, func(t *testing.T) {
				resetBillingProjectionReferenceFixtures(t)
				now := time.Unix(1_800_700_000+int64(caseIndex), 0)
				caseIndex++
				referenceID := int64(30_000 + caseIndex)
				operationKey := fmt.Sprintf("retention:%d:protected:v1", referenceID)
				source.createSource(t, referenceID, variant.key(operationKey), variant.state(source))
				createCompletedBillingProjectionPair(t, source.kind, referenceID, operationKey, now)

				cleanupAt := now.Add(billingStatsProjectionCompletedRetention + time.Hour)
				statsDeleted, err := CleanupExpiredBillingStatsProjections(context.Background(), cleanupAt, 100)
				require.NoError(t, err)
				logsDeleted, err := CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 100)
				require.NoError(t, err)
				assert.Zero(t, statsDeleted)
				assert.Zero(t, logsDeleted)
			})
		}
	}
}

func TestBillingProjectionRetentionBatchDrainsBeyondProtectedHead(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(
		&AsyncBillingReservation{}, &BillingStatsProjection{}, &BillingLogProjection{}, &Log{},
	))
	t.Cleanup(func() { resetBillingProjectionReferenceFixtures(t) })
	resetBillingProjectionReferenceFixtures(t)

	now := time.Unix(1_800_750_000, 0)
	oldCompletedMs := now.Add(-billingStatsProjectionCompletedRetention - time.Hour).UnixMilli()
	protectedKey := "async:retention-batch:protected:v1"
	key := protectedKey
	require.NoError(t, DB.Create(&AsyncBillingReservation{
		ID: 70_001, ReservationKey: "retention-batch-reservation", ProtocolVersion: 2,
		Kind: AsyncBillingKindTask, PublicTaskID: "retention-batch-task",
		State: AsyncBillingReservationStateReserved, UserID: 1, FundingSource: TaskBillingSourceWallet,
		AcceptedProjectionKey: &key, AcceptedProjectionState: AsyncBillingAcceptedProjectionCompleted,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}).Error)
	createCompletedBillingProjectionPair(t, BillingStatsProjectionKindAccepted, 70_001, protectedKey, now)
	require.NoError(t, DB.Model(&BillingStatsProjection{}).Where("operation_key = ?", protectedKey).
		Update("completed_time_ms", oldCompletedMs).Error)
	require.NoError(t, DB.Model(&BillingLogProjection{}).Where("operation_key = ?", protectedKey).
		Update("completed_time_ms", oldCompletedMs).Error)

	const orphanCount = billingStatsProjectionCleanupMaxBatch + 1
	statsRows := make([]BillingStatsProjection, 0, orphanCount)
	logRows := make([]BillingLogProjection, 0, orphanCount)
	for index := 0; index < orphanCount; index++ {
		operationKey := fmt.Sprintf("async:retention-batch:%d:v1", index)
		referenceID := int64(80_000 + index)
		statsRows = append(statsRows, BillingStatsProjection{
			OperationKey: operationKey, ProtocolVersion: BillingStatsProjectionProtocol,
			Kind: BillingStatsProjectionKindAccepted, ReferenceID: referenceID,
			UserID: 1, ChannelID: 1, QuotaDelta: 1, RequestDelta: 1,
			State:         BillingStatsProjectionStateCompleted,
			CreatedTimeMs: oldCompletedMs, UpdatedTimeMs: oldCompletedMs, CompletedTimeMs: oldCompletedMs,
		})
		logRows = append(logRows, BillingLogProjection{
			OperationKey: operationKey, ProtocolVersion: BillingLogProjectionProtocol,
			Kind: BillingLogProjectionKindAccepted, ReferenceID: referenceID,
			Required: false, Disposition: BillingLogProjectionDispositionNotRequired,
			State: BillingLogProjectionStateCompleted, Outcome: BillingLogProjectionOutcomeNotRequired,
			CreatedTimeMs: oldCompletedMs, UpdatedTimeMs: oldCompletedMs, CompletedTimeMs: oldCompletedMs,
		})
	}
	require.NoError(t, DB.CreateInBatches(&statsRows, 100).Error)
	require.NoError(t, DB.CreateInBatches(&logRows, 100).Error)

	statsDeleted, nextStatsID, statsMore, err := CleanupExpiredBillingStatsProjectionsPage(
		context.Background(), now, 0, billingStatsProjectionCleanupMaxBatch,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(billingStatsProjectionCleanupMaxBatch-1), statsDeleted)
	assert.True(t, statsMore)
	assert.Positive(t, nextStatsID)
	secondStatsDeleted, nextStatsID, statsMore, err := CleanupExpiredBillingStatsProjectionsPage(
		context.Background(), now, nextStatsID, billingStatsProjectionCleanupMaxBatch,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), secondStatsDeleted)
	assert.Zero(t, nextStatsID)
	assert.False(t, statsMore)
	assert.Equal(t, int64(orphanCount), statsDeleted+secondStatsDeleted)

	logsDeleted, nextLogsID, logsMore, err := CleanupExpiredBillingLogProjectionsPage(
		context.Background(), now, 0, billingStatsProjectionCleanupMaxBatch,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(billingStatsProjectionCleanupMaxBatch-1), logsDeleted)
	assert.True(t, logsMore)
	assert.Positive(t, nextLogsID)
	secondLogsDeleted, nextLogsID, logsMore, err := CleanupExpiredBillingLogProjectionsPage(
		context.Background(), now, nextLogsID, billingStatsProjectionCleanupMaxBatch,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), secondLogsDeleted)
	assert.Zero(t, nextLogsID)
	assert.False(t, logsMore)
	assert.Equal(t, int64(orphanCount), logsDeleted+secondLogsDeleted)

	var protectedStats int64
	var protectedLogs int64
	require.NoError(t, DB.Model(&BillingStatsProjection{}).Where("operation_key = ?", protectedKey).
		Count(&protectedStats).Error)
	require.NoError(t, DB.Model(&BillingLogProjection{}).Where("operation_key = ?", protectedKey).
		Count(&protectedLogs).Error)
	assert.Equal(t, int64(1), protectedStats)
	assert.Equal(t, int64(1), protectedLogs)
}

func TestBillingProjectionRetentionExternalDatabaseCompatibility(t *testing.T) {
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
			for _, table := range []any{
				&AsyncBillingReservation{}, &BillingStatsProjection{}, &BillingLogProjection{}, &Log{},
			} {
				if db.Migrator().HasTable(table) {
					t.Skip("refusing to use a non-empty external projection retention database")
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
					&BillingLogProjection{}, &BillingStatsProjection{}, &Log{}, &AsyncBillingReservation{},
				)
				DB = previousDB
				LOG_DB = previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
			})
			require.NoError(t, db.AutoMigrate(
				&AsyncBillingReservation{}, &BillingStatsProjection{}, &BillingLogProjection{}, &Log{},
			))

			now := time.Unix(1_800_800_000, 0)
			cleanupAt := now.Add(billingStatsProjectionFailedRetention + time.Hour)
			orphanKey := "async:external-retention:orphan:v1"
			createCompletedBillingProjectionPair(t, BillingStatsProjectionKindAccepted, 71_001, orphanKey, now)
			statsDeleted, nextStatsID, statsMore, err := CleanupExpiredBillingStatsProjectionsPage(
				context.Background(), cleanupAt, 0, 500,
			)
			require.NoError(t, err)
			logsDeleted, nextLogsID, logsMore, err := CleanupExpiredBillingLogProjectionsPage(
				context.Background(), cleanupAt, 0, 500,
			)
			require.NoError(t, err)
			assert.Equal(t, int64(1), statsDeleted)
			assert.Equal(t, int64(1), logsDeleted)
			assert.Zero(t, nextStatsID)
			assert.Zero(t, nextLogsID)
			assert.False(t, statsMore)
			assert.False(t, logsMore)

			protectedKey := "async:external-retention:protected:v1"
			key := protectedKey
			require.NoError(t, db.Create(&AsyncBillingReservation{
				ID: 71_002, ReservationKey: "external-retention-reservation",
				ProtocolVersion: 2, Kind: AsyncBillingKindTask, PublicTaskID: "external-retention-task",
				State: AsyncBillingReservationStateReserved, UserID: 1, FundingSource: TaskBillingSourceWallet,
				AcceptedProjectionKey: &key, AcceptedProjectionState: AsyncBillingAcceptedProjectionCompleted,
				CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
			}).Error)
			createCompletedBillingProjectionPair(t, BillingStatsProjectionKindAccepted, 71_002, protectedKey, now)
			statsDeleted, err = CleanupExpiredBillingStatsProjections(context.Background(), cleanupAt, 500)
			require.NoError(t, err)
			logsDeleted, err = CleanupExpiredBillingLogProjections(context.Background(), cleanupAt, 500)
			require.NoError(t, err)
			assert.Zero(t, statsDeleted)
			assert.Zero(t, logsDeleted)
		})
	}
}
