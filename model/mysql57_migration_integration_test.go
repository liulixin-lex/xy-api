package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMySQL57CompactMigrationCompatibility(t *testing.T) {
	cleanDSN := os.Getenv("MYSQL57_TEST_CLEAN_DSN")
	upgradeDSN := os.Getenv("MYSQL57_TEST_UPGRADE_DSN")
	logDSN := os.Getenv("MYSQL57_TEST_LOG_DSN")
	if cleanDSN == "" || upgradeDSN == "" || logDSN == "" {
		t.Skip("dedicated MySQL 5.7 migration databases are not configured")
	}

	openDB := func(t *testing.T, dsn string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{PrepareStmt: false})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	requireEmptySchema := func(t *testing.T, db *gorm.DB) {
		t.Helper()
		var count int64
		require.NoError(t, db.Raw(
			"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'",
		).Scan(&count).Error)
		require.Zero(t, count, "integration DSN must point to a dedicated empty schema")
	}
	rowFormat := func(t *testing.T, db *gorm.DB, table string) string {
		t.Helper()
		var format string
		require.NoError(t, db.Raw(
			"SELECT ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
			table,
		).Scan(&format).Error)
		require.NotEmpty(t, format)
		return format
	}

	cleanDB := openDB(t, cleanDSN)
	cleanPeerDB := openDB(t, cleanDSN)
	upgradeDB := openDB(t, upgradeDSN)
	logDB := openDB(t, logDSN)
	logPeerDB := openDB(t, logDSN)
	requireEmptySchema(t, cleanDB)
	requireEmptySchema(t, upgradeDB)
	requireEmptySchema(t, logDB)

	previousDB := DB
	previousLogDB := LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		initCol()
	})
	common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	initCol()

	// MySQL named locks are session-scoped. Prove the main and independent log
	// migration paths both run on the lock-owning connection, exclude a peer,
	// and release the lock after success and failure.
	assertMySQLMigrationLockContract(t, cleanDB, cleanPeerDB, func(migrate func(*gorm.DB) error) error {
		return migrateMySQLDBSafely(cleanDB, migrate)
	})
	assertMySQLMigrationLockContract(t, logDB, logPeerDB, func(migrate func(*gorm.DB) error) error {
		return migrateLOGDBSafelyOn(logDB, common.DatabaseTypeMySQL, migrate)
	})

	// The capability gate must fail before creating any business table when a
	// legal MySQL 5.7 switch is changed to an unsafe value.
	require.NoError(t, cleanDB.Exec("SET GLOBAL innodb_large_prefix=OFF").Error)
	t.Cleanup(func() { _ = cleanDB.Exec("SET GLOBAL innodb_large_prefix=ON").Error })
	_, err := prepareMySQLMigrationDB(cleanDB, 700, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "innodb_large_prefix")
	requireEmptySchema(t, cleanDB)
	require.NoError(t, cleanDB.Exec("SET GLOBAL innodb_large_prefix=ON").Error)

	DB = cleanDB
	require.NoError(t, migrateMySQLDBSafely(cleanDB, migrateDBOn))
	longIndexTables := []string{
		"casbin_rule",
		"passkey_credentials",
		"logs",
		"subscription_orders",
		"payment_events",
		"user_oauth_bindings",
		"payment_orders",
		"stripe_legacy_invoices",
		"stripe_legacy_subscriptions",
		"top_ups",
		"perf_metrics",
	}
	for _, table := range longIndexTables {
		assert.Equal(t, "Dynamic", rowFormat(t, cleanDB, table), table)
	}
	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "payment_orders", name: "provider_order_key"},
		{table: "payment_orders", name: "provider_payment_key"},
		{table: "payment_events", name: "provider_order_key"},
		{table: "payment_events", name: "provider_payment_key"},
		{table: "payment_events", name: "provider_resource_key"},
	} {
		var length int64
		require.NoError(t, cleanDB.Raw(
			"SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
			column.table, column.name,
		).Scan(&length).Error)
		assert.EqualValues(t, PaymentProviderAuthorityKeyMaxLength, length, column.table+"."+column.name)
	}
	var prefixedIndexes int64
	require.NoError(t, cleanDB.Raw(
		"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ? AND INDEX_NAME <> 'PRIMARY' AND SUB_PART IS NOT NULL",
		longIndexTables,
	).Scan(&prefixedIndexes).Error)
	assert.Zero(t, prefixedIndexes)

	// A v0.1.6-compatible utf8/COMPACT subscription table can exist with its
	// 255-character trade number. v0.2 must preserve its row while converting
	// the table before adding the new 255/320-character provider indexes.
	require.NoError(t, upgradeDB.Exec(`
		CREATE TABLE subscription_orders (
			id bigint NOT NULL AUTO_INCREMENT,
			user_id bigint DEFAULT NULL,
			plan_id bigint DEFAULT NULL,
			money double DEFAULT NULL,
			trade_no varchar(255) NOT NULL,
			payment_method varchar(50) DEFAULT '',
			payment_provider varchar(50) DEFAULT '',
			status longtext,
			create_time bigint DEFAULT 0,
			complete_time bigint DEFAULT 0,
			provider_payload longtext,
			PRIMARY KEY (id),
			UNIQUE KEY uni_subscription_orders_trade_no (trade_no),
			KEY idx_subscription_orders_trade_no (trade_no)
		) ENGINE=InnoDB ROW_FORMAT=COMPACT DEFAULT CHARSET=utf8
	`).Error)
	require.NoError(t, upgradeDB.Exec(`
		INSERT INTO subscription_orders
			(user_id, plan_id, money, trade_no, payment_method, payment_provider, status, create_time)
		VALUES (42, 7, 12.5, 'MYSQL57_COMPACT_SAMPLE', 'alipay', 'epay', 'pending', 1700000000)
	`).Error)
	DB = upgradeDB
	require.NoError(t, migrateMySQLDBSafely(upgradeDB, migrateDBOn))
	assert.Equal(t, "Dynamic", rowFormat(t, upgradeDB, "subscription_orders"))
	var tradeNo string
	require.NoError(t, upgradeDB.Raw(
		"SELECT trade_no FROM subscription_orders WHERE user_id = 42",
	).Scan(&tradeNo).Error)
	assert.Equal(t, "MYSQL57_COMPACT_SAMPLE", tradeNo)
	var providerKeyLength int64
	require.NoError(t, upgradeDB.Raw(
		"SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription_orders' AND COLUMN_NAME = 'provider_order_key'",
	).Scan(&providerKeyLength).Error)
	assert.EqualValues(t, 320, providerKeyLength)
	require.NoError(t, upgradeDB.Raw(
		"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'subscription_orders' AND COLUMN_NAME IN ('trade_no', 'provider_order_id', 'provider_order_key') AND SUB_PART IS NOT NULL",
	).Scan(&prefixedIndexes).Error)
	assert.Zero(t, prefixedIndexes)
	require.NoError(t, migrateMySQLDBSafely(upgradeDB, migrateDBOn), "repeated migration must be idempotent")

	LOG_DB = logDB
	logFixtureDB, err := prepareMySQLMigrationDB(logDB, 382, nil)
	require.NoError(t, err)
	logFixture := v020LogFixture{
		Id: 7101, UserId: 101, CreatedAt: 1711000500, Type: LogTypeTopup,
		Content: "v0.2.0 preserved log", Username: "v020-user", ModelName: "payment",
		Quota: 125000, ChannelId: 9, TokenId: 301, Group: "premium", Ip: "192.0.2.10",
		RequestId: "req-v020-log", UpstreamRequestId: "upstream-v020-log", Other: `{"source":"v0.2.0"}`,
	}
	require.NoError(t, logFixtureDB.AutoMigrate(&v020LogFixture{}))
	require.NoError(t, logFixtureDB.Create(&logFixture).Error)
	require.NoError(t, migrateLOGDB())
	require.NoError(t, migrateLOGDB(), "repeated independent log migration must be idempotent")
	var storedLog v020LogFixture
	require.NoError(t, logDB.First(&storedLog, logFixture.Id).Error)
	assert.Equal(t, logFixture, storedLog)
	assert.Equal(t, "Dynamic", rowFormat(t, logDB, "logs"))
	require.NoError(t, logDB.Raw(
		"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'logs' AND INDEX_NAME = 'index_username_model_name' AND SUB_PART IS NOT NULL",
	).Scan(&prefixedIndexes).Error)
	assert.Zero(t, prefixedIndexes)

	if gbk8DSN := os.Getenv("MYSQL57_TEST_GBK8_DSN"); gbk8DSN != "" {
		gbk8DB := openDB(t, gbk8DSN)
		requireEmptySchema(t, gbk8DB)
		DB = gbk8DB
		require.NoError(t, migrateMySQLDBSafely(gbk8DB, migrateDBOn))
		assert.Equal(t, "Dynamic", rowFormat(t, gbk8DB, "casbin_rule"))
		require.NoError(t, gbk8DB.Raw(
			"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ? AND INDEX_NAME <> 'PRIMARY' AND SUB_PART IS NOT NULL",
			longIndexTables,
		).Scan(&prefixedIndexes).Error)
		assert.Zero(t, prefixedIndexes)
	}
}

func assertMySQLMigrationLockContract(
	t *testing.T,
	db *gorm.DB,
	peerDB *gorm.DB,
	run func(func(*gorm.DB) error) error,
) {
	t.Helper()
	var schema string
	require.NoError(t, db.Raw("SELECT DATABASE()").Scan(&schema).Error)
	require.NotEmpty(t, schema)
	lockName := mysqlMigrationLockName(schema)
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
			var connectionID int64
			if err := migrationDB.Raw("SELECT CONNECTION_ID()").Row().Scan(&connectionID); err != nil {
				migrationReady <- err
				return err
			}
			var ownerConnectionID sql.NullInt64
			if err := migrationDB.Raw("SELECT IS_USED_LOCK(?)", lockName).Row().Scan(&ownerConnectionID); err != nil {
				migrationReady <- err
				return err
			}
			if !ownerConnectionID.Valid || ownerConnectionID.Int64 != connectionID {
				err := fmt.Errorf(
					"MySQL migration ran on connection %d while lock owner was %v",
					connectionID, ownerConnectionID,
				)
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
	var peerAcquired sql.NullInt64
	require.NoError(t, peerConnection.QueryRowContext(
		context.Background(), "SELECT GET_LOCK(?, 0)", lockName,
	).Scan(&peerAcquired))
	require.True(t, peerAcquired.Valid)
	assert.Zero(t, peerAcquired.Int64)

	migrationRelease <- struct{}{}
	require.NoError(t, <-migrationDone)
	assertMySQLPeerCanAcquireAndReleaseMigrationLock(t, peerConnection, lockName)

	sentinel := errors.New("deterministic migration callback failure")
	require.ErrorIs(t, run(func(*gorm.DB) error { return sentinel }), sentinel)
	assertMySQLPeerCanAcquireAndReleaseMigrationLock(t, peerConnection, lockName)
}

func assertMySQLPeerCanAcquireAndReleaseMigrationLock(t *testing.T, connection *sql.Conn, lockName string) {
	t.Helper()
	var acquired sql.NullInt64
	require.NoError(t, connection.QueryRowContext(
		context.Background(), "SELECT GET_LOCK(?, 0)", lockName,
	).Scan(&acquired))
	require.True(t, acquired.Valid)
	assert.Equal(t, int64(1), acquired.Int64)
	var released sql.NullInt64
	require.NoError(t, connection.QueryRowContext(
		context.Background(), "SELECT RELEASE_LOCK(?)", lockName,
	).Scan(&released))
	require.True(t, released.Valid)
	assert.Equal(t, int64(1), released.Int64)
}
