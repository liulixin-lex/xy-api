package model

import (
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
	upgradeDB := openDB(t, upgradeDSN)
	logDB := openDB(t, logDSN)
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
	require.NoError(t, migrateDBOn(cleanDB))
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
	require.NoError(t, migrateDBOn(upgradeDB))
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
	require.NoError(t, migrateDBOn(upgradeDB), "repeated migration must be idempotent")

	LOG_DB = logDB
	require.NoError(t, migrateLOGDB())
	assert.Equal(t, "Dynamic", rowFormat(t, logDB, "logs"))
	require.NoError(t, logDB.Raw(
		"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'logs' AND INDEX_NAME = 'index_username_model_name' AND SUB_PART IS NOT NULL",
	).Scan(&prefixedIndexes).Error)
	assert.Zero(t, prefixedIndexes)

	if gbk8DSN := os.Getenv("MYSQL57_TEST_GBK8_DSN"); gbk8DSN != "" {
		gbk8DB := openDB(t, gbk8DSN)
		requireEmptySchema(t, gbk8DB)
		DB = gbk8DB
		require.NoError(t, migrateDBOn(gbk8DB))
		assert.Equal(t, "Dynamic", rowFormat(t, gbk8DB, "casbin_rule"))
		require.NoError(t, gbk8DB.Raw(
			"SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ? AND INDEX_NAME <> 'PRIMARY' AND SUB_PART IS NOT NULL",
			longIndexTables,
		).Scan(&prefixedIndexes).Error)
		assert.Zero(t, prefixedIndexes)
	}
}
