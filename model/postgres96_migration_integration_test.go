package model

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgreSQL96MigrationLockContracts(t *testing.T) {
	mainDSN := os.Getenv("POSTGRES96_TEST_LOCK_DSN")
	logDSN := os.Getenv("POSTGRES96_TEST_LOG_DSN")
	if mainDSN == "" || logDSN == "" {
		t.Skip("dedicated PostgreSQL 9.6 migration lock databases are not configured")
	}
	openDB := func(t *testing.T, dsn string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{PrepareStmt: false})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return db
	}

	mainDB := openDB(t, mainDSN)
	mainPeerDB := openDB(t, mainDSN)
	logDB := openDB(t, logDSN)
	logPeerDB := openDB(t, logDSN)
	requireDedicatedEmptyPaymentSchema(t, mainDB, common.DatabaseTypePostgreSQL)
	requireDedicatedEmptyPaymentSchema(t, logDB, common.DatabaseTypePostgreSQL)

	assertPostgreSQLMigrationLockContract(t, mainDB, mainPeerDB, func(migrate func(*gorm.DB) error) error {
		return migratePostgreSQLDBSafely(mainDB, postgresMigrationAdvisoryLockKey, migrate)
	})
	assertPostgreSQLMigrationLockContract(t, logDB, logPeerDB, func(migrate func(*gorm.DB) error) error {
		return migrateLOGDBSafelyOn(logDB, common.DatabaseTypePostgreSQL, migrate)
	})

	previousLogDB := LOG_DB
	previousLogType := common.LogDatabaseType()
	LOG_DB = logDB
	common.SetLogDatabaseType(common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
		initCol()
	})

	logFixture := v020LogFixture{
		Id: 7201, UserId: 101, CreatedAt: 1711000500, Type: LogTypeTopup,
		Content: "v0.2.0 preserved log", Username: "v020-user", ModelName: "payment",
		Quota: 125000, ChannelId: 9, TokenId: 301, Group: "premium", Ip: "192.0.2.11",
		RequestId: "req-v020-pg-log", UpstreamRequestId: "upstream-v020-pg-log", Other: `{"source":"v0.2.0"}`,
	}
	require.NoError(t, logDB.AutoMigrate(&v020LogFixture{}))
	require.NoError(t, logDB.Create(&logFixture).Error)
	require.NoError(t, migrateLOGDB())
	require.NoError(t, migrateLOGDB(), "repeated independent log migration must be idempotent")
	var storedLog v020LogFixture
	require.NoError(t, logDB.First(&storedLog, logFixture.Id).Error)
	assert.Equal(t, logFixture, storedLog)
}

func assertPostgreSQLMigrationLockContract(
	t *testing.T,
	db *gorm.DB,
	peerDB *gorm.DB,
	run func(func(*gorm.DB) error) error,
) {
	t.Helper()
	migrationReady := make(chan error, 1)
	migrationRelease := make(chan struct{}, 1)
	migrationDone := make(chan error, 1)
	t.Cleanup(func() {
		select {
		case migrationRelease <- struct{}{}:
		default:
		}
	})
	go func() {
		err := run(func(migrationDB *gorm.DB) error {
			var backendPID int64
			var ownsAdvisoryLock bool
			if err := migrationDB.Raw(`
				SELECT pg_backend_pid(), EXISTS (
					SELECT 1
					FROM pg_locks
					WHERE pid = pg_backend_pid()
						AND locktype = 'advisory'
						AND granted
				)
			`).Row().Scan(&backendPID, &ownsAdvisoryLock); err != nil {
				migrationReady <- err
				return err
			}
			if backendPID == 0 || !ownsAdvisoryLock {
				err := errors.New("PostgreSQL migration callback does not own the advisory lock connection")
				migrationReady <- err
				return err
			}
			migrationReady <- nil
			<-migrationRelease
			return nil
		})
		select {
		case migrationReady <- err:
		default:
		}
		migrationDone <- err
	}()
	require.NoError(t, <-migrationReady)

	peerSQLDB, err := peerDB.DB()
	require.NoError(t, err)
	peerConnection, err := peerSQLDB.Conn(context.Background())
	require.NoError(t, err)
	defer peerConnection.Close()
	var peerAcquired bool
	require.NoError(t, peerConnection.QueryRowContext(
		context.Background(), "SELECT pg_try_advisory_lock($1)", postgresMigrationAdvisoryLockKey,
	).Scan(&peerAcquired))
	assert.False(t, peerAcquired)

	migrationRelease <- struct{}{}
	require.NoError(t, <-migrationDone)
	assertPostgreSQLPeerCanAcquireAndReleaseMigrationLock(t, peerConnection)

	sentinel := errors.New("deterministic migration callback failure")
	require.ErrorIs(t, run(func(*gorm.DB) error { return sentinel }), sentinel)
	assertPostgreSQLPeerCanAcquireAndReleaseMigrationLock(t, peerConnection)
}

func assertPostgreSQLPeerCanAcquireAndReleaseMigrationLock(t *testing.T, connection *sql.Conn) {
	t.Helper()
	var acquired bool
	require.NoError(t, connection.QueryRowContext(
		context.Background(), "SELECT pg_try_advisory_lock($1)", postgresMigrationAdvisoryLockKey,
	).Scan(&acquired))
	require.True(t, acquired)
	var released bool
	require.NoError(t, connection.QueryRowContext(
		context.Background(), "SELECT pg_advisory_unlock($1)", postgresMigrationAdvisoryLockKey,
	).Scan(&released))
	assert.True(t, released)
}
