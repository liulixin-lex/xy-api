package model

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type legacyAsyncBillingManualResolutionIndex struct {
	ReservationID int64 `gorm:"uniqueIndex:uidx_async_billing_manual_reservation"`
}

func TestWaitAsyncBillingV2SchemaReadyRejectsNilDatabase(t *testing.T) {
	assert.NotPanics(t, func() {
		err := WaitAsyncBillingV2SchemaReady(context.Background(), nil, 0)
		assert.EqualError(t, err, "async billing database is unavailable")
	})
}

func (legacyAsyncBillingManualResolutionIndex) TableName() string {
	return "async_billing_manual_resolutions"
}

type unexpectedAsyncBillingManualResolutionIndex struct {
	ReservationID   int64  `gorm:"uniqueIndex:uidx_async_billing_manual_reservation,priority:1"`
	ExpectedVersion int64  `gorm:"uniqueIndex:uidx_async_billing_manual_reservation,priority:2"`
	Action          string `gorm:"uniqueIndex:uidx_async_billing_manual_reservation,priority:3"`
}

func (unexpectedAsyncBillingManualResolutionIndex) TableName() string {
	return "async_billing_manual_resolutions"
}

func runAsyncBillingManualResolutionConcurrentMigrators(t *testing.T, migratorDBs []*gorm.DB) {
	t.Helper()

	start := make(chan struct{})
	errorsByMigrator := make(chan error, len(migratorDBs))
	var wait sync.WaitGroup
	for _, migratorDB := range migratorDBs {
		wait.Add(1)
		go func(db *gorm.DB) {
			defer wait.Done()
			<-start
			errorsByMigrator <- ensureAsyncBillingManualResolutionUniqueIndex(db)
		}(migratorDB)
	}
	close(start)
	wait.Wait()
	close(errorsByMigrator)
	for migrationErr := range errorsByMigrator {
		require.NoError(t, migrationErr)
	}
}

func openAsyncBillingSchemaExternalTestDB(t *testing.T, dbType common.DatabaseType, dsn string) *gorm.DB {
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
		t.Fatalf("unsupported async billing schema test database type %q", dbType)
	}
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestAsyncBillingV2SchemaReadyFailsClosedForMissingTableColumnOrUniqueIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-schema.db"), &gorm.Config{})
	require.NoError(t, err)
	previousType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(previousType) })

	ready, err := AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, db.AutoMigrate(
		&Task{}, &Midjourney{},
		&AsyncBillingReservation{}, &AsyncBillingAttempt{}, &AsyncBillingManualResolution{},
		&TaskBillingOperation{}, &MidjourneyBillingOperation{}, &SubscriptionBillingPeriod{},
		&BillingStatsProjection{}, &BillingLogProjection{},
		&BillingLogSinkConflictAudit{}, &BillingLogSinkConflictResolution{},
	))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)

	require.NoError(t, db.Migrator().DropIndex(
		&BillingLogSinkConflictAudit{}, "uidx_billing_log_sink_conflict_operation",
	))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, db.Migrator().CreateIndex(
		&BillingLogSinkConflictAudit{}, "uidx_billing_log_sink_conflict_operation",
	))

	require.NoError(t, db.Migrator().DropIndex(
		&BillingLogSinkConflictResolution{}, "uidx_billing_log_sink_conflict_resolution",
	))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, db.Migrator().CreateIndex(
		&BillingLogSinkConflictResolution{}, "uidx_billing_log_sink_conflict_resolution",
	))

	require.NoError(t, db.Migrator().DropIndex(
		&AsyncBillingReservation{}, "idx_async_billing_reservations_accepted_projection_key",
	))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, db.Migrator().CreateIndex(
		&AsyncBillingReservation{}, "idx_async_billing_reservations_accepted_projection_key",
	))

	require.NoError(t, db.Migrator().DropIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_task"))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, db.Migrator().CreateIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_task"))

	require.NoError(t, db.Migrator().DropIndex(
		&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_task",
	))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, db.Migrator().CreateIndex(
		&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_task",
	))

	require.NoError(t, db.Migrator().DropIndex(&AsyncBillingAttempt{}, "uidx_async_billing_attempt"))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, db.Migrator().CreateIndex(&AsyncBillingAttempt{}, "uidx_async_billing_attempt"))
	require.NoError(t, db.Migrator().DropColumn(&AsyncBillingAttempt{}, "send_deadline_ms"))
	ready, err = AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestAsyncBillingV2SchemaReadyCoversLateOperationRuntimeFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-operation-schema.db"), &gorm.Config{})
	require.NoError(t, err)
	previousType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
	require.NoError(t, db.AutoMigrate(
		&Task{}, &Midjourney{}, &AsyncBillingReservation{}, &AsyncBillingAttempt{},
		&AsyncBillingManualResolution{}, &TaskBillingOperation{}, &MidjourneyBillingOperation{},
		&SubscriptionBillingPeriod{}, &BillingStatsProjection{}, &BillingLogProjection{},
		&BillingLogSinkConflictAudit{}, &BillingLogSinkConflictResolution{},
	))
	require.NoError(t, db.Migrator().DropColumn(&TaskBillingOperation{}, "log_lease_owner"))
	ready, err := AsyncBillingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestEnsureAsyncBillingManualResolutionUniqueIndexUpgradesOnlyLegacyDefinition(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-manual-index.db"), &gorm.Config{})
	require.NoError(t, err)
	previousType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(previousType) })

	require.NoError(t, db.AutoMigrate(&AsyncBillingManualResolution{}))
	require.NoError(t, db.Migrator().DropIndex(
		&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
	))
	require.NoError(t, db.Migrator().CreateIndex(
		&legacyAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
	))
	legacyReady, err := asyncBillingUniqueIndexReady(
		db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id"},
	)
	require.NoError(t, err)
	require.True(t, legacyReady)

	require.NoError(t, ensureAsyncBillingManualResolutionUniqueIndex(db))
	require.NoError(t, ensureAsyncBillingManualResolutionUniqueIndex(db), "upgrade must be restart-safe")
	targetReady, err := asyncBillingUniqueIndexReady(
		db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	require.NoError(t, err)
	assert.True(t, targetReady)

	require.NoError(t, db.Migrator().DropIndex(
		&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
	))
	require.NoError(t, db.Migrator().CreateIndex(
		&unexpectedAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
	))
	err = ensureAsyncBillingManualResolutionUniqueIndex(db)
	assert.ErrorIs(t, err, ErrAsyncBillingSchemaNotReady)
	targetReady, readErr := asyncBillingUniqueIndexReady(
		db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	require.NoError(t, readErr)
	assert.False(t, targetReady)
}

func TestEnsureAsyncBillingManualResolutionUniqueIndexConvergesAcrossConcurrentMigrators(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "async-manual-index-concurrent.db") + "?_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	previousType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
	require.NoError(t, db.AutoMigrate(&AsyncBillingManualResolution{}))
	require.NoError(t, db.Migrator().DropIndex(
		&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
	))
	require.NoError(t, db.Migrator().CreateIndex(
		&legacyAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
	))

	const migrators = 4
	migratorDBs := make([]*gorm.DB, 0, migrators)
	for index := 0; index < migrators; index++ {
		migratorDB, openErr := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, openErr)
		sqlDB, sqlErr := migratorDB.DB()
		require.NoError(t, sqlErr)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		migratorDBs = append(migratorDBs, migratorDB)
	}
	runAsyncBillingManualResolutionConcurrentMigrators(t, migratorDBs)
	ready, err := asyncBillingUniqueIndexReady(
		db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestEnsureAsyncBillingManualResolutionUniqueIndexConvergesAcrossExternalMigrators(t *testing.T) {
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
			db := openAsyncBillingSchemaExternalTestDB(t, test.dbType, dsn)
			if db.Migrator().HasTable(&AsyncBillingManualResolution{}) {
				t.Skip("refusing to run against external database because async billing manual resolutions already exists")
			}
			t.Cleanup(func() { _ = db.Migrator().DropTable(&AsyncBillingManualResolution{}) })

			previousType := common.MainDatabaseType()
			common.SetMainDatabaseType(test.dbType)
			t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
			require.NoError(t, db.AutoMigrate(&AsyncBillingManualResolution{}))
			require.NoError(t, db.Migrator().DropIndex(
				&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
			))
			require.NoError(t, db.Migrator().CreateIndex(
				&legacyAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
			))

			const migrators = 4
			migratorDBs := make([]*gorm.DB, 0, migrators)
			for index := 0; index < migrators; index++ {
				migratorDB := openAsyncBillingSchemaExternalTestDB(t, test.dbType, dsn)
				require.True(t, migratorDB.Migrator().HasTable(&AsyncBillingManualResolution{}))
				migratorDBs = append(migratorDBs, migratorDB)
			}
			runAsyncBillingManualResolutionConcurrentMigrators(t, migratorDBs)

			ready, err := asyncBillingUniqueIndexReady(
				db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
				[]string{"reservation_id", "expected_version"},
			)
			require.NoError(t, err)
			assert.True(t, ready)
			if test.dbType == common.DatabaseTypeMySQL {
				assert.False(t, db.Migrator().HasIndex(
					&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
				))
				require.NoError(t, db.Migrator().RenameIndex(
					&AsyncBillingManualResolution{},
					asyncBillingManualResolutionUniqueIndex,
					asyncBillingManualResolutionGuardIndex,
				))
				require.NoError(t, ensureAsyncBillingManualResolutionUniqueIndex(db))
				assert.True(t, db.Migrator().HasIndex(
					&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
				))
				assert.False(t, db.Migrator().HasIndex(
					&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
				))
				require.NoError(t, db.Migrator().CreateIndex(
					&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
				))
				require.NoError(t, ensureAsyncBillingManualResolutionUniqueIndex(db))
				assert.False(t, db.Migrator().HasIndex(
					&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
				))
			}
		})
	}
}

func TestEnsureAsyncBillingManualResolutionUniqueIndexPostgreSQLTimesOutWhenLockIsHeld(t *testing.T) {
	dsn := os.Getenv("ROUTING_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ROUTING_TEST_POSTGRES_DSN is not set")
	}
	lockDB := openAsyncBillingSchemaExternalTestDB(t, common.DatabaseTypePostgreSQL, dsn)
	contenderDB := openAsyncBillingSchemaExternalTestDB(t, common.DatabaseTypePostgreSQL, dsn)

	lockTx := lockDB.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() { _ = lockTx.Rollback().Error })
	require.NoError(t, lockTx.Exec(`SELECT pg_advisory_xact_lock(
	hashtext(current_database() || ':' || current_schema()), hashtext(?)
)`, asyncBillingManualResolutionIndexLock).Error)

	err := ensureAsyncBillingManualResolutionUniqueIndexPostgreSQL(contenderDB, 100*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAsyncBillingSchemaNotReady)
	assert.Contains(t, err.Error(), "timed out or was canceled")
}
