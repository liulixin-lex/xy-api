package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var commonGroupCol string
var commonKeyCol string
var commonTrueVal string
var commonFalseVal string

var logKeyCol string
var logGroupCol string

func initCol() {
	// init common column names
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		commonGroupCol = `"group"`
		commonKeyCol = `"key"`
		commonTrueVal = "true"
		commonFalseVal = "false"
	} else {
		commonGroupCol = "`group`"
		commonKeyCol = "`key`"
		commonTrueVal = "1"
		commonFalseVal = "0"
	}
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		logGroupCol = `"group"`
		logKeyCol = `"key"`
	default:
		logGroupCol = "`group`"
		logKeyCol = "`key`"
	}
}

var DB *gorm.DB

var LOG_DB *gorm.DB

func createRootAccountIfNeed() error {
	var user User
	//if user.Status != common.UserStatusEnabled {
	if err := DB.First(&user).Error; err != nil {
		common.SysLog("no user exists, create a root user for you: username is root, password is 123456")
		hashedPassword, err := common.Password2Hash("123456")
		if err != nil {
			return err
		}
		rootUser := User{
			Username:    "root",
			Password:    hashedPassword,
			Role:        common.RoleRootUser,
			Status:      common.UserStatusEnabled,
			DisplayName: "Root User",
			AccessToken: nil,
			Quota:       100000000,
		}
		DB.Create(&rootUser)
	}
	return nil
}

func CheckSetup() {
	setup := GetSetup()
	if setup == nil {
		// No setup record exists, check if we have a root user
		if RootUserExists() {
			common.SysLog("system is not initialized, but root user exists")
			// Create setup record
			newSetup := Setup{
				Version:       common.Version,
				InitializedAt: time.Now().Unix(),
			}
			err := DB.Create(&newSetup).Error
			if err != nil {
				common.SysLog("failed to create setup record: " + err.Error())
			}
			constant.Setup = true
		} else {
			common.SysLog("system is not initialized and no root user exists")
			constant.Setup = false
		}
	} else {
		// Setup record exists, system is initialized
		common.SysLog("system is already initialized at: " + time.Unix(setup.InitializedAt, 0).String())
		constant.Setup = true
	}
}

func isClickHouseDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "clickhouse://") ||
		strings.HasPrefix(dsn, "tcp://") ||
		strings.HasPrefix(dsn, "http://") ||
		strings.HasPrefix(dsn, "https://")
}

func normalizeClickHouseDSN(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "https" {
		return dsn
	}
	query := parsed.Query()
	if _, ok := query["secure"]; !ok {
		query.Set("secure", "true")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func chooseDB(envName string, isLog bool) (*gorm.DB, common.DatabaseType, error) {
	dsn := os.Getenv(envName)
	if dsn != "" {
		if isClickHouseDSN(dsn) {
			if !isLog {
				return nil, "", fmt.Errorf("%s does not support ClickHouse; use SQLite, MySQL, or PostgreSQL for the primary database and LOG_SQL_DSN for ClickHouse logs", envName)
			}
			common.SysLog("using ClickHouse as log database")
			db, err := gorm.Open(clickhouse.Open(normalizeClickHouseDSN(dsn)), &gorm.Config{
				PrepareStmt: false,
			})
			return db, common.DatabaseTypeClickHouse, err
		}
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			// Use PostgreSQL
			common.SysLog("using PostgreSQL as database")
			db, err := gorm.Open(postgres.New(postgres.Config{
				DSN:                  dsn,
				PreferSimpleProtocol: true, // disables implicit prepared statement usage
			}), &gorm.Config{
				PrepareStmt: true, // precompile SQL
			})
			return db, common.DatabaseTypePostgreSQL, err
		}
		if strings.HasPrefix(dsn, "local") {
			common.SysLog("SQL_DSN not set, using SQLite as database")
			db, err := gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{
				PrepareStmt: true, // precompile SQL
			})
			return db, common.DatabaseTypeSQLite, err
		}
		// Use MySQL
		common.SysLog("using MySQL as database")
		// check parseTime
		if !strings.Contains(dsn, "parseTime") {
			if strings.Contains(dsn, "?") {
				dsn += "&parseTime=true"
			} else {
				dsn += "?parseTime=true"
			}
		}
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			PrepareStmt: true, // precompile SQL
		})
		return db, common.DatabaseTypeMySQL, err
	}
	// Use SQLite
	common.SysLog("SQL_DSN not set, using SQLite as database")
	db, err := gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{
		PrepareStmt: true, // precompile SQL
	})
	return db, common.DatabaseTypeSQLite, err
}

func InitDB() (err error) {
	db, dbType, err := chooseDB("SQL_DSN", false)
	if err == nil {
		common.SetMainDatabaseType(dbType)
		if os.Getenv("LOG_SQL_DSN") == "" {
			common.SetLogDatabaseType(dbType)
		}
		initCol()
		if common.DebugEnabled {
			db = db.Debug()
		}
		DB = db
		// MySQL charset/collation startup check: ensure Chinese-capable charset
		if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
			if err := checkMySQLChineseSupport(DB); err != nil {
				panic(err)
			}
		}
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))

		if !common.IsMasterNode {
			return nil
		}
		if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
			//_, _ = sqlDB.Exec("ALTER TABLE channels MODIFY model_mapping TEXT;") // TODO: delete this line when most users have upgraded
		}
		common.SysLog("database migration started")
		err = migrateDBSafely()
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

const postgresMigrationAdvisoryLockKey int64 = 0x4e4150494d494752

const (
	mysqlMigrationLockWaitSeconds = 10 * 60
	mysqlMigrationLockPrefix      = "new-api:migrate:"
)

// migrateDBSafely serializes PostgreSQL and MySQL schema changes across
// application instances. Both locks are connection-scoped, so the GORM
// migration session must use the exact dedicated connection that acquired the
// lock. SQLite remains a single-node database and uses the ordinary migration
// path.
func migrateDBSafely() (resultErr error) {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		return migrateMySQLDBSafely(DB, migrateDBOn)
	}
	if !common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return migrateDB()
	}
	return migratePostgreSQLDBSafely(DB, postgresMigrationAdvisoryLockKey, migrateDBOn)
}

func migratePostgreSQLDBSafely(db *gorm.DB, lockKey int64, migrate func(*gorm.DB) error) (resultErr error) {
	if db == nil || migrate == nil {
		return errors.New("invalid PostgreSQL database migration request")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("reserve PostgreSQL database migration connection: %w", err)
	}
	defer connection.Close()

	lockContext, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	_, err = connection.ExecContext(lockContext, "SELECT pg_advisory_lock($1)", lockKey)
	cancel()
	if err != nil {
		return fmt.Errorf("acquire PostgreSQL database migration lock: %w", err)
	}

	defer func() {
		unlockContext, unlockCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer unlockCancel()
		var released bool
		unlockErr := connection.QueryRowContext(
			unlockContext, "SELECT pg_advisory_unlock($1)", lockKey,
		).Scan(&released)
		if unlockErr == nil && !released {
			unlockErr = errors.New("PostgreSQL database migration lock was not held by the migration connection")
		}
		if unlockErr == nil {
			return
		}
		if resultErr == nil {
			resultErr = fmt.Errorf("release PostgreSQL database migration lock: %w", unlockErr)
			return
		}
		common.SysError("failed to release PostgreSQL database migration lock: " + unlockErr.Error())
	}()

	migrationDB, err := gormDBOnDedicatedConnection(db, connection)
	if err != nil {
		resultErr = fmt.Errorf("initialize PostgreSQL database migration session: %w", err)
		return resultErr
	}
	resultErr = migrate(migrationDB)
	return resultErr
}

func mysqlMigrationLockName(schema string) string {
	digest := sha256.Sum256([]byte(schema))
	return fmt.Sprintf("%s%x", mysqlMigrationLockPrefix, digest[:16])
}

func migrateMySQLDBSafely(db *gorm.DB, migrate func(*gorm.DB) error) (resultErr error) {
	if db == nil || migrate == nil {
		return errors.New("invalid MySQL database migration request")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("reserve MySQL database migration connection: %w", err)
	}
	defer connection.Close()

	schemaContext, schemaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	var schema string
	err = connection.QueryRowContext(schemaContext, "SELECT DATABASE()").Scan(&schema)
	schemaCancel()
	if err != nil {
		return fmt.Errorf("read MySQL database migration schema: %w", err)
	}
	if strings.TrimSpace(schema) == "" {
		return errors.New("MySQL database migration requires a selected schema")
	}
	lockName := mysqlMigrationLockName(schema)
	lockContext, lockCancel := context.WithTimeout(context.Background(), time.Duration(mysqlMigrationLockWaitSeconds)*time.Second)
	var acquired sql.NullInt64
	err = connection.QueryRowContext(
		lockContext, "SELECT GET_LOCK(?, ?)", lockName, mysqlMigrationLockWaitSeconds,
	).Scan(&acquired)
	lockCancel()
	if err != nil {
		return fmt.Errorf("acquire MySQL database migration lock: %w", err)
	}
	if !acquired.Valid {
		return fmt.Errorf("acquire MySQL database migration lock: server returned no result for schema %q", schema)
	}
	if acquired.Int64 != 1 {
		return fmt.Errorf("acquire MySQL database migration lock: timed out waiting for schema %q", schema)
	}

	defer func() {
		unlockContext, unlockCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer unlockCancel()
		var released sql.NullInt64
		unlockErr := connection.QueryRowContext(
			unlockContext, "SELECT RELEASE_LOCK(?)", lockName,
		).Scan(&released)
		if unlockErr == nil && (!released.Valid || released.Int64 != 1) {
			unlockErr = errors.New("MySQL database migration lock was not held by the migration connection")
		}
		if unlockErr == nil {
			return
		}
		if resultErr == nil {
			resultErr = fmt.Errorf("release MySQL database migration lock: %w", unlockErr)
			return
		}
		common.SysError("failed to release MySQL database migration lock: " + unlockErr.Error())
	}()

	migrationDB, err := gormDBOnDedicatedConnection(db, connection)
	if err != nil {
		resultErr = fmt.Errorf("initialize MySQL database migration session: %w", err)
		return resultErr
	}
	resultErr = migrate(migrationDB)
	return resultErr
}

func gormDBOnDedicatedConnection(db *gorm.DB, connection *sql.Conn) (*gorm.DB, error) {
	if db == nil || connection == nil {
		return nil, errors.New("invalid dedicated database migration connection")
	}
	// Reuse the initialized dialect, callbacks and server capability flags from
	// the source GORM handle while pinning every query to the exact connection
	// that owns the advisory/named lock. prepareMySQLMigrationDB then supplies a
	// reusable statement-cloning session for its table options.
	migrationDB := db.Session(&gorm.Session{
		Context: context.Background(),
		NewDB:   true,
	})
	if migrationDB.Error != nil {
		return nil, migrationDB.Error
	}
	migrationDB.Config.ConnPool = connection
	migrationDB.Config.PrepareStmt = false
	migrationDB.Statement.ConnPool = connection
	return migrationDB, nil
}

func InitLogDB() (err error) {
	if os.Getenv("LOG_SQL_DSN") == "" {
		LOG_DB = DB
		common.SetLogDatabaseType(common.MainDatabaseType())
		initCol()
		return
	}
	db, dbType, err := chooseDB("LOG_SQL_DSN", true)
	if err == nil {
		common.SetLogDatabaseType(dbType)
		initCol()
		if common.DebugEnabled {
			db = db.Debug()
		}
		LOG_DB = db
		// If log DB is MySQL, also ensure Chinese-capable charset
		if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
			if err := checkMySQLChineseSupport(LOG_DB); err != nil {
				panic(err)
			}
		}
		sqlDB, err := LOG_DB.DB()
		if err != nil {
			return err
		}
		sqlDB.SetMaxIdleConns(common.GetEnvOrDefault("SQL_MAX_IDLE_CONNS", 100))
		sqlDB.SetMaxOpenConns(common.GetEnvOrDefault("SQL_MAX_OPEN_CONNS", 1000))
		sqlDB.SetConnMaxLifetime(time.Second * time.Duration(common.GetEnvOrDefault("SQL_MAX_LIFETIME", 60)))

		if !common.IsMasterNode {
			return nil
		}
		common.SysLog("database migration started")
		err = migrateLOGDB()
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

func migrateDB() error {
	return migrateDBOn(DB)
}

func migrateDBOn(db *gorm.DB) error {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		var err error
		db, err = prepareMySQLMigrationDB(db, 700, []string{
			"subscription_orders",
			"payment_orders",
			"payment_events",
			"stripe_legacy_subscriptions",
			"stripe_legacy_invoices",
		})
		if err != nil {
			return err
		}
	}
	// Migrate price_amount column from float/double to decimal for existing tables
	migrateSubscriptionPlanPriceAmountOn(db)
	if err := ensurePaymentProjectionColumnsSQLiteOn(db); err != nil {
		return err
	}
	// Migrate model_limits column from varchar to text for existing tables
	if err := migrateTokenModelLimitsToTextOn(db); err != nil {
		return err
	}
	if err := ensureUserPaymentFrozenColumnOn(db); err != nil {
		return err
	}

	err := db.AutoMigrate(
		&Channel{},
		&Token{},
		&User{},
		&PasskeyCredential{},
		&Option{},
		&Redemption{},
		&Ability{},
		&Log{},
		&Midjourney{},
		&TopUp{},
		&PaymentQuote{},
		&PaymentUserGuard{},
		&PaymentOrder{},
		&PaymentTask{},
		&PaymentLimitPolicy{},
		&PaymentLimitBucket{},
		&PaymentLimitReservation{},
		&PaymentEvent{},
		&PaymentLedgerEntry{},
		&PaymentDebt{},
		&PaymentCustomerBinding{},
		&PaymentCustomerBindingRetirement{},
		&PaymentOperationsAudit{},
		&PaymentConfigurationAudit{},
		&StripeLegacySubscription{},
		&StripeLegacyInvoice{},
		&BillingReservation{},
		&QuotaLedgerEntry{},
		&BillingReservationAdminResolution{},
		&AffiliateRewardRecord{},
		&InviteInitialQuotaRecord{},
		&InviteLinkBatch{},
		&ReferralCapture{},
		&QuotaData{},
		&Task{},
		&Model{},
		&Vendor{},
		&PrefillGroup{},
		&Setup{},
		&TwoFA{},
		&TwoFABackupCode{},
		&Checkin{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&SubscriptionPreConsumeRecord{},
		&CustomOAuthProvider{},
		&UserOAuthBinding{},
		&PerfMetric{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
		&CasbinRule{},
		&AuthzRole{},
	)
	if err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := ensureSubscriptionPlanTableSQLiteOn(db); err != nil {
			return err
		}
	} else {
		if err := db.AutoMigrate(&SubscriptionPlan{}); err != nil {
			return err
		}
	}
	return nil
}

// prepareMySQLMigrationDB verifies the server's real InnoDB key capacity before
// any schema mutation, upgrades only existing tables that receive new long
// indexes, and makes every newly created table explicitly DYNAMIC. MySQL 5.7.8
// defaults to COMPACT even though Barracuda and large prefixes are available;
// relying on an implicit server default would therefore break clean utf8mb4
// installations.
func prepareMySQLMigrationDB(db *gorm.DB, requiredIndexCharacters int64, existingTables []string) (*gorm.DB, error) {
	if db == nil {
		return nil, errors.New("invalid MySQL migration capability request")
	}
	if db.Dialector.Name() != "mysql" {
		return db, nil
	}
	if requiredIndexCharacters <= 0 {
		return nil, errors.New("invalid MySQL migration index requirement")
	}
	if err := checkMySQLLongIndexCapability(db, requiredIndexCharacters); err != nil {
		return nil, err
	}
	if err := ensureMySQLLongIndexRowFormatOn(db, existingTables); err != nil {
		return nil, err
	}
	// Set returns a chain handle whose statement is intentionally mutable. Wrap
	// it in a reusable session so each later migration/helper call clones the
	// statement (including table_options) instead of leaving a prior Table(...)
	// selection attached to every subsequent AutoMigrate model.
	return db.Set("gorm:table_options", "ENGINE=InnoDB ROW_FORMAT=DYNAMIC").Session(&gorm.Session{}), nil
}

func checkMySQLLongIndexCapability(db *gorm.DB, requiredIndexCharacters int64) error {
	var charsetMaxBytes int64
	if err := db.Raw(`
		SELECT CHARACTER_SETS.MAXLEN
		FROM information_schema.SCHEMATA
		JOIN information_schema.CHARACTER_SETS
			ON CHARACTER_SETS.CHARACTER_SET_NAME = SCHEMATA.DEFAULT_CHARACTER_SET_NAME
		WHERE SCHEMATA.SCHEMA_NAME = DATABASE()
	`).Row().Scan(&charsetMaxBytes); err != nil {
		return fmt.Errorf("read MySQL schema character width: %w", err)
	}
	if charsetMaxBytes <= 0 || requiredIndexCharacters > math.MaxInt64/charsetMaxBytes {
		return errors.New("invalid MySQL schema character width")
	}
	requiredIndexBytes := requiredIndexCharacters * charsetMaxBytes
	var pageSize int64
	if err := db.Raw("SELECT @@innodb_page_size").Row().Scan(&pageSize); err != nil {
		return fmt.Errorf("read MySQL InnoDB page size: %w", err)
	}
	maxIndexBytes := int64(768)
	if pageSize >= 16*1024 {
		maxIndexBytes = 3072
	} else if pageSize >= 8*1024 {
		maxIndexBytes = 1536
	}
	if requiredIndexBytes > maxIndexBytes {
		return fmt.Errorf(
			"MySQL InnoDB page size %d supports at most %d index bytes, but this schema requires %d characters at %d bytes each (%d bytes)",
			pageSize, maxIndexBytes, requiredIndexCharacters, charsetMaxBytes, requiredIndexBytes,
		)
	}
	type mysqlVariable struct {
		Name  string `gorm:"column:Variable_name"`
		Value string `gorm:"column:Value"`
	}
	for _, variable := range []struct {
		query    string
		name     string
		accepted func(string) bool
		hint     string
	}{
		{
			query: "SHOW VARIABLES LIKE 'innodb_large_prefix'", name: "innodb_large_prefix",
			accepted: func(value string) bool { return value == "1" || strings.EqualFold(value, "ON") },
			hint:     "ON",
		},
		{
			query: "SHOW VARIABLES LIKE 'innodb_file_format'", name: "innodb_file_format",
			accepted: func(value string) bool { return strings.EqualFold(value, "Barracuda") },
			hint:     "Barracuda",
		},
	} {
		var value mysqlVariable
		result := db.Raw(variable.query).Scan(&value)
		if result.Error != nil {
			return fmt.Errorf("read MySQL %s: %w", variable.name, result.Error)
		}
		// MySQL 8 removed these switches because their capable values are
		// permanent. MySQL 5.7 exposes them and must report the safe value.
		if result.RowsAffected > 0 && !variable.accepted(strings.TrimSpace(value.Value)) {
			return fmt.Errorf("MySQL %s must be %s for DYNAMIC utf8mb4 indexes", variable.name, variable.hint)
		}
	}
	return nil
}

// ensureMySQLLongIndexRowFormatOn upgrades only existing tables that receive
// new indexed identifiers longer than their current row format can support.
// The v0.1.6 subscription_orders table is the important upgrade case; the
// other main-database entries make an interrupted first migration resumable.
func ensureMySQLLongIndexRowFormatOn(db *gorm.DB, tables []string) error {
	type tableStorage struct {
		Engine    string `gorm:"column:ENGINE"`
		RowFormat string `gorm:"column:ROW_FORMAT"`
	}
	for _, table := range tables {
		var storage tableStorage
		if err := db.Raw(
			"SELECT ENGINE, ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'",
			table,
		).Scan(&storage).Error; err != nil {
			return fmt.Errorf("inspect MySQL row format for %s: %w", table, err)
		}
		if storage.Engine == "" {
			continue
		}
		if !strings.EqualFold(storage.Engine, "InnoDB") {
			return fmt.Errorf("MySQL table %s uses %s; payment migrations require InnoDB transactions", table, storage.Engine)
		}
		if strings.EqualFold(storage.RowFormat, "Dynamic") || strings.EqualFold(storage.RowFormat, "Compressed") {
			continue
		}
		quotedTable := "`" + strings.ReplaceAll(table, "`", "``") + "`"
		if err := db.Exec("ALTER TABLE " + quotedTable + " ROW_FORMAT=DYNAMIC").Error; err != nil {
			return fmt.Errorf(
				"upgrade MySQL table %s to ROW_FORMAT=DYNAMIC for utf8mb4 indexed identifiers: %w",
				table, err,
			)
		}
		var verified tableStorage
		if err := db.Raw(
			"SELECT ENGINE, ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'",
			table,
		).Scan(&verified).Error; err != nil {
			return fmt.Errorf("verify MySQL row format for %s: %w", table, err)
		}
		if !strings.EqualFold(verified.Engine, "InnoDB") || !strings.EqualFold(verified.RowFormat, "Dynamic") {
			return fmt.Errorf("MySQL table %s did not retain required InnoDB DYNAMIC row format", table)
		}
	}
	return nil
}

func ensureUserPaymentFrozenColumnOn(db *gorm.DB) error {
	if !db.Migrator().HasTable(&User{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&User{}, "PaymentFrozen") {
		var addColumnSQL string
		switch {
		case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
			addColumnSQL = `ALTER TABLE "users" ADD COLUMN "payment_frozen" boolean`
		case common.UsingMainDatabase(common.DatabaseTypeMySQL):
			addColumnSQL = "ALTER TABLE `users` ADD COLUMN `payment_frozen` boolean NULL"
		case common.UsingMainDatabase(common.DatabaseTypeSQLite):
			addColumnSQL = "ALTER TABLE `users` ADD COLUMN `payment_frozen` numeric NOT NULL DEFAULT 0"
		default:
			return nil
		}
		if err := db.Exec(addColumnSQL).Error; err != nil {
			return fmt.Errorf("add users.payment_frozen compatibility column: %w", err)
		}
	}
	result := db.Table("users").Where("payment_frozen IS NULL").Update("payment_frozen", false)
	if result.Error != nil {
		return fmt.Errorf("backfill users.payment_frozen: %w", result.Error)
	}
	return nil
}

func migrateDBFast() error {
	migrationDB := DB
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		var err error
		migrationDB, err = prepareMySQLMigrationDB(DB, 700, []string{
			"subscription_orders",
			"payment_orders",
			"payment_events",
			"stripe_legacy_subscriptions",
			"stripe_legacy_invoices",
		})
		if err != nil {
			return err
		}
	}
	if err := ensurePaymentProjectionColumnsSQLite(); err != nil {
		return err
	}

	var wg sync.WaitGroup

	migrations := []struct {
		model interface{}
		name  string
	}{
		{&Channel{}, "Channel"},
		{&Token{}, "Token"},
		{&User{}, "User"},
		{&PasskeyCredential{}, "PasskeyCredential"},
		{&Option{}, "Option"},
		{&Redemption{}, "Redemption"},
		{&Ability{}, "Ability"},
		{&Log{}, "Log"},
		{&Midjourney{}, "Midjourney"},
		{&TopUp{}, "TopUp"},
		{&PaymentQuote{}, "PaymentQuote"},
		{&PaymentUserGuard{}, "PaymentUserGuard"},
		{&PaymentOrder{}, "PaymentOrder"},
		{&PaymentTask{}, "PaymentTask"},
		{&PaymentLimitPolicy{}, "PaymentLimitPolicy"},
		{&PaymentLimitBucket{}, "PaymentLimitBucket"},
		{&PaymentLimitReservation{}, "PaymentLimitReservation"},
		{&PaymentEvent{}, "PaymentEvent"},
		{&PaymentLedgerEntry{}, "PaymentLedgerEntry"},
		{&PaymentDebt{}, "PaymentDebt"},
		{&PaymentCustomerBinding{}, "PaymentCustomerBinding"},
		{&PaymentCustomerBindingRetirement{}, "PaymentCustomerBindingRetirement"},
		{&PaymentOperationsAudit{}, "PaymentOperationsAudit"},
		{&PaymentConfigurationAudit{}, "PaymentConfigurationAudit"},
		{&StripeLegacySubscription{}, "StripeLegacySubscription"},
		{&StripeLegacyInvoice{}, "StripeLegacyInvoice"},
		{&BillingReservation{}, "BillingReservation"},
		{&QuotaLedgerEntry{}, "QuotaLedgerEntry"},
		{&BillingReservationAdminResolution{}, "BillingReservationAdminResolution"},
		{&AffiliateRewardRecord{}, "AffiliateRewardRecord"},
		{&InviteInitialQuotaRecord{}, "InviteInitialQuotaRecord"},
		{&InviteLinkBatch{}, "InviteLinkBatch"},
		{&ReferralCapture{}, "ReferralCapture"},
		{&QuotaData{}, "QuotaData"},
		{&Task{}, "Task"},
		{&Model{}, "Model"},
		{&Vendor{}, "Vendor"},
		{&PrefillGroup{}, "PrefillGroup"},
		{&Setup{}, "Setup"},
		{&TwoFA{}, "TwoFA"},
		{&TwoFABackupCode{}, "TwoFABackupCode"},
		{&Checkin{}, "Checkin"},
		{&SubscriptionOrder{}, "SubscriptionOrder"},
		{&UserSubscription{}, "UserSubscription"},
		{&SubscriptionPreConsumeRecord{}, "SubscriptionPreConsumeRecord"},
		{&CustomOAuthProvider{}, "CustomOAuthProvider"},
		{&UserOAuthBinding{}, "UserOAuthBinding"},
		{&PerfMetric{}, "PerfMetric"},
		{&SystemInstance{}, "SystemInstance"},
		{&SystemTask{}, "SystemTask"},
		{&SystemTaskLock{}, "SystemTaskLock"},
	}
	// 动态计算migration数量，确保errChan缓冲区足够大
	errChan := make(chan error, len(migrations))

	for _, m := range migrations {
		wg.Add(1)
		go func(model interface{}, name string) {
			defer wg.Done()
			if err := migrationDB.AutoMigrate(model); err != nil {
				errChan <- fmt.Errorf("failed to migrate %s: %v", name, err)
			}
		}(m.model, m.name)
	}

	// Wait for all migrations to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := ensureSubscriptionPlanTableSQLite(); err != nil {
			return err
		}
	} else {
		if err := migrationDB.AutoMigrate(&SubscriptionPlan{}); err != nil {
			return err
		}
	}
	common.SysLog("database migrated")
	return nil
}

func migrateLOGDB() error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return migrateClickHouseLogDB()
	}
	return migrateLOGDBSafelyOn(LOG_DB, common.LogDatabaseType(), migrateLOGDBOn)
}

func migrateLOGDBSafelyOn(db *gorm.DB, databaseType common.DatabaseType, migrate func(*gorm.DB) error) error {
	switch databaseType {
	case common.DatabaseTypeMySQL:
		return migrateMySQLDBSafely(db, migrate)
	case common.DatabaseTypePostgreSQL:
		return migratePostgreSQLDBSafely(db, postgresMigrationAdvisoryLockKey, migrate)
	default:
		return migrate(db)
	}
}

func migrateLOGDBOn(db *gorm.DB) error {
	if db == nil {
		return errors.New("invalid log database migration request")
	}
	migrationDB := db
	if db.Dialector.Name() == "mysql" {
		var err error
		migrationDB, err = prepareMySQLMigrationDB(db, 382, nil)
		if err != nil {
			return err
		}
	}
	return migrationDB.AutoMigrate(&Log{})
}

func migrateClickHouseLogDB() error {
	ttlDays := clickHouseLogTTLDays()
	if err := LOG_DB.Exec(clickHouseLogCreateTableSQL(ttlDays)).Error; err != nil {
		return err
	}
	return syncClickHouseLogTTL(ttlDays)
}

func clickHouseLogTTLDays() int {
	ttlDays := common.GetEnvOrDefault("LOG_SQL_CLICKHOUSE_TTL_DAYS", 0)
	if ttlDays < 0 {
		return 0
	}
	return ttlDays
}

func clickHouseLogTTLExpression(ttlDays int) string {
	if ttlDays <= 0 {
		return ""
	}
	return fmt.Sprintf("toDateTime(created_at) + INTERVAL %d DAY DELETE", ttlDays)
}

func clickHouseLogTTLClause(ttlDays int) string {
	expression := clickHouseLogTTLExpression(ttlDays)
	if expression == "" {
		return ""
	}
	return "\nTTL " + expression
}

func clickHouseLogCreateTableSQL(ttlDays int) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS logs (
	id Int64 DEFAULT 0,
	user_id Int32 DEFAULT 0,
	created_at Int64 DEFAULT 0,
	type Int32 DEFAULT 0,
	content String DEFAULT '',
	username String DEFAULT '',
	token_name String DEFAULT '',
	model_name String DEFAULT '',
	quota Int32 DEFAULT 0,
	prompt_tokens Int32 DEFAULT 0,
	completion_tokens Int32 DEFAULT 0,
	use_time Int32 DEFAULT 0,
	is_stream UInt8 DEFAULT 0,
	channel_id Int32 DEFAULT 0,
	token_id Int32 DEFAULT 0,
	`+"`group`"+` String DEFAULT '',
	ip String DEFAULT '',
	request_id String DEFAULT '',
	upstream_request_id String DEFAULT '',
	other String DEFAULT ''
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, request_id)%s`, clickHouseLogTTLClause(ttlDays))
}

func syncClickHouseLogTTL(ttlDays int) error {
	expression := clickHouseLogTTLExpression(ttlDays)
	if expression != "" {
		return LOG_DB.Exec("ALTER TABLE logs MODIFY TTL " + expression).Error
	}

	hasTTL, err := clickHouseLogTableHasTTL()
	if err != nil {
		return err
	}
	if !hasTTL {
		return nil
	}
	return LOG_DB.Exec("ALTER TABLE logs REMOVE TTL").Error
}

func clickHouseLogTableHasTTL() (bool, error) {
	var createTableSQL string
	if err := LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&createTableSQL).Error; err != nil {
		return false, err
	}
	return clickHouseCreateTableHasTTL(createTableSQL), nil
}

func clickHouseCreateTableHasTTL(createTableSQL string) bool {
	upperSQL := strings.ToUpper(createTableSQL)
	return strings.Contains(upperSQL, "\nTTL ") || strings.Contains(upperSQL, " TTL ")
}

type sqliteColumnDef struct {
	Name string
	DDL  string
}

func ensurePaymentProjectionColumnsSQLite() error {
	return ensurePaymentProjectionColumnsSQLiteOn(DB)
}

func ensurePaymentProjectionColumnsSQLiteOn(db *gorm.DB) error {
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return nil
	}
	columns := []struct {
		table string
		name  string
		ddl   string
	}{
		{table: "top_ups", name: "provider_order_key", ddl: "`provider_order_key` varchar(320)"},
		{table: "payment_orders", name: "browser_authorization_digest", ddl: "`browser_authorization_digest` varchar(64)"},
		{table: "subscription_orders", name: "provider_order_key", ddl: "`provider_order_key` varchar(320)"},
		{table: "user_subscriptions", name: "payment_order_id", ddl: "`payment_order_id` bigint"},
	}
	for _, column := range columns {
		if !db.Migrator().HasTable(column.table) || db.Migrator().HasColumn(column.table, column.name) {
			continue
		}
		if err := db.Exec("ALTER TABLE `" + column.table + "` ADD COLUMN " + column.ddl).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensureSubscriptionPlanTableSQLite() error {
	return ensureSubscriptionPlanTableSQLiteOn(DB)
}

func ensureSubscriptionPlanTableSQLiteOn(db *gorm.DB) error {
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return nil
	}
	tableName := "subscription_plans"
	if !db.Migrator().HasTable(tableName) {
		createSQL := `CREATE TABLE ` + "`" + tableName + "`" + ` (
` + "`id`" + ` integer,
` + "`title`" + ` varchar(128) NOT NULL,
` + "`subtitle`" + ` varchar(255) DEFAULT '',
` + "`price_amount`" + ` decimal(10,6) NOT NULL,
` + "`currency`" + ` varchar(8) NOT NULL DEFAULT 'USD',
` + "`duration_unit`" + ` varchar(16) NOT NULL DEFAULT 'month',
` + "`duration_value`" + ` integer NOT NULL DEFAULT 1,
` + "`custom_seconds`" + ` bigint NOT NULL DEFAULT 0,
` + "`enabled`" + ` numeric DEFAULT 1,
` + "`sort_order`" + ` integer DEFAULT 0,
` + "`allow_balance_pay`" + ` numeric DEFAULT 1,
` + "`allow_wallet_overflow`" + ` numeric DEFAULT 1,
` + "`stripe_price_id`" + ` varchar(128) DEFAULT '',
` + "`creem_product_id`" + ` varchar(128) DEFAULT '',
` + "`waffo_pancake_product_id`" + ` varchar(128) DEFAULT '',
` + "`max_purchase_per_user`" + ` integer DEFAULT 0,
` + "`upgrade_group`" + ` varchar(64) DEFAULT '',
` + "`downgrade_group`" + ` varchar(64) DEFAULT '',
` + "`total_amount`" + ` bigint NOT NULL DEFAULT 0,
` + "`quota_reset_period`" + ` varchar(16) DEFAULT 'never',
` + "`quota_reset_custom_seconds`" + ` bigint DEFAULT 0,
` + "`created_at`" + ` bigint,
` + "`updated_at`" + ` bigint,
PRIMARY KEY (` + "`id`" + `)
)`
		return db.Exec(createSQL).Error
	}
	var cols []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw("PRAGMA table_info(`" + tableName + "`)").Scan(&cols).Error; err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		existing[c.Name] = struct{}{}
	}
	required := []sqliteColumnDef{
		{Name: "title", DDL: "`title` varchar(128) NOT NULL"},
		{Name: "subtitle", DDL: "`subtitle` varchar(255) DEFAULT ''"},
		{Name: "price_amount", DDL: "`price_amount` decimal(10,6) NOT NULL"},
		{Name: "currency", DDL: "`currency` varchar(8) NOT NULL DEFAULT 'USD'"},
		{Name: "duration_unit", DDL: "`duration_unit` varchar(16) NOT NULL DEFAULT 'month'"},
		{Name: "duration_value", DDL: "`duration_value` integer NOT NULL DEFAULT 1"},
		{Name: "custom_seconds", DDL: "`custom_seconds` bigint NOT NULL DEFAULT 0"},
		{Name: "enabled", DDL: "`enabled` numeric DEFAULT 1"},
		{Name: "sort_order", DDL: "`sort_order` integer DEFAULT 0"},
		{Name: "allow_balance_pay", DDL: "`allow_balance_pay` numeric DEFAULT 1"},
		{Name: "allow_wallet_overflow", DDL: "`allow_wallet_overflow` numeric DEFAULT 1"},
		{Name: "stripe_price_id", DDL: "`stripe_price_id` varchar(128) DEFAULT ''"},
		{Name: "creem_product_id", DDL: "`creem_product_id` varchar(128) DEFAULT ''"},
		{Name: "waffo_pancake_product_id", DDL: "`waffo_pancake_product_id` varchar(128) DEFAULT ''"},
		{Name: "max_purchase_per_user", DDL: "`max_purchase_per_user` integer DEFAULT 0"},
		{Name: "upgrade_group", DDL: "`upgrade_group` varchar(64) DEFAULT ''"},
		{Name: "downgrade_group", DDL: "`downgrade_group` varchar(64) DEFAULT ''"},
		{Name: "total_amount", DDL: "`total_amount` bigint NOT NULL DEFAULT 0"},
		{Name: "quota_reset_period", DDL: "`quota_reset_period` varchar(16) DEFAULT 'never'"},
		{Name: "quota_reset_custom_seconds", DDL: "`quota_reset_custom_seconds` bigint DEFAULT 0"},
		{Name: "created_at", DDL: "`created_at` bigint"},
		{Name: "updated_at", DDL: "`updated_at` bigint"},
	}
	for _, col := range required {
		if _, ok := existing[col.Name]; ok {
			continue
		}
		if err := db.Exec("ALTER TABLE `" + tableName + "` ADD COLUMN " + col.DDL).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateTokenModelLimitsToText migrates model_limits column from varchar(1024) to text
// This is safe to run multiple times - it checks the column type first
func migrateTokenModelLimitsToText() error {
	return migrateTokenModelLimitsToTextOn(DB)
}

func migrateTokenModelLimitsToTextOn(db *gorm.DB) error {
	// SQLite uses type affinity, so TEXT and VARCHAR are effectively the same — no migration needed
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return nil
	}

	tableName := "tokens"
	columnName := "model_limits"

	if !db.Migrator().HasTable(tableName) {
		return nil
	}

	if !db.Migrator().HasColumn(&Token{}, columnName) {
		return nil
	}

	var alterSQL string
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		var dataType string
		if err := db.Raw(`SELECT data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			tableName, columnName).Scan(&dataType).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
		} else if dataType == "text" {
			return nil
		}
		alterSQL = fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE text`, tableName, columnName)
	} else if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		var columnType string
		if err := db.Raw(`SELECT COLUMN_TYPE FROM information_schema.columns
				WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			tableName, columnName).Scan(&columnType).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
		} else if strings.ToLower(columnType) == "text" {
			return nil
		}
		alterSQL = fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s text", tableName, columnName)
	} else {
		return nil
	}

	if alterSQL != "" {
		if err := db.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("failed to migrate %s.%s to text: %w", tableName, columnName, err)
		}
		common.SysLog(fmt.Sprintf("Successfully migrated %s.%s to text", tableName, columnName))
	}
	return nil
}

// migrateSubscriptionPlanPriceAmount migrates price_amount column from float/double to decimal(10,6)
// This is safe to run multiple times - it checks the column type first
func migrateSubscriptionPlanPriceAmount() {
	migrateSubscriptionPlanPriceAmountOn(DB)
}

func migrateSubscriptionPlanPriceAmountOn(db *gorm.DB) {
	// SQLite doesn't support ALTER COLUMN, and its type affinity handles this automatically
	// Skip early to avoid GORM parsing the existing table DDL which may cause issues
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return
	}

	tableName := "subscription_plans"
	columnName := "price_amount"

	// Check if table exists first
	if !db.Migrator().HasTable(tableName) {
		return
	}

	// Check if column exists
	if !db.Migrator().HasColumn(&SubscriptionPlan{}, columnName) {
		return
	}

	var alterSQL string
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		// PostgreSQL: Check if already decimal/numeric
		var dataType string
		if err := db.Raw(`SELECT data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			tableName, columnName).Scan(&dataType).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
		} else if dataType == "numeric" {
			return // Already decimal/numeric
		}
		alterSQL = fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE decimal(10,6) USING %s::decimal(10,6)`,
			tableName, columnName, columnName)
	} else if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// MySQL: Check if already decimal
		var columnType string
		if err := db.Raw(`SELECT COLUMN_TYPE FROM information_schema.columns
				WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			tableName, columnName).Scan(&columnType).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
		} else if strings.HasPrefix(strings.ToLower(columnType), "decimal") {
			return // Already decimal
		}
		alterSQL = fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s decimal(10,6) NOT NULL DEFAULT 0",
			tableName, columnName)
	} else {
		return
	}

	if alterSQL != "" {
		if err := db.Exec(alterSQL).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to migrate %s.%s to decimal: %v", tableName, columnName, err))
		} else {
			common.SysLog(fmt.Sprintf("Successfully migrated %s.%s to decimal(10,6)", tableName, columnName))
		}
	}
}

func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	return err
}

func CloseDB() error {
	if LOG_DB != DB {
		err := closeDB(LOG_DB)
		if err != nil {
			return err
		}
	}
	return closeDB(DB)
}

// checkMySQLChineseSupport ensures the MySQL connection and current schema
// default charset/collation can store Chinese characters. It allows common
// Chinese-capable charsets (utf8mb4, utf8, gbk, big5, gb18030) and panics otherwise.
func checkMySQLChineseSupport(db *gorm.DB) error {
	// 仅检测：当前库默认字符集/排序规则 + 各表的排序规则（隐含字符集）

	// Read current schema defaults
	var schemaCharset, schemaCollation string
	err := db.Raw("SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = DATABASE()").Row().Scan(&schemaCharset, &schemaCollation)
	if err != nil {
		return fmt.Errorf("读取当前库默认字符集/排序规则失败 / Failed to read schema default charset/collation: %v", err)
	}

	toLower := func(s string) string { return strings.ToLower(s) }
	// Allowed charsets that can store Chinese text
	allowedCharsets := map[string]string{
		"utf8mb4": "utf8mb4_",
		"utf8":    "utf8_",
		"gbk":     "gbk_",
		"big5":    "big5_",
		"gb18030": "gb18030_",
	}
	isChineseCapable := func(cs, cl string) bool {
		csLower := toLower(cs)
		clLower := toLower(cl)
		if prefix, ok := allowedCharsets[csLower]; ok {
			if clLower == "" {
				return true
			}
			return strings.HasPrefix(clLower, prefix)
		}
		// 如果仅提供了排序规则，尝试按排序规则前缀判断
		for _, prefix := range allowedCharsets {
			if strings.HasPrefix(clLower, prefix) {
				return true
			}
		}
		return false
	}

	// 1) 当前库默认值必须支持中文
	if !isChineseCapable(schemaCharset, schemaCollation) {
		return fmt.Errorf("当前库默认字符集/排序规则不支持中文：schema(%s/%s)。请将库设置为 utf8mb4/utf8/gbk/big5/gb18030 / Schema default charset/collation is not Chinese-capable: schema(%s/%s). Please set to utf8mb4/utf8/gbk/big5/gb18030",
			schemaCharset, schemaCollation, schemaCharset, schemaCollation)
	}

	// 2) 所有物理表的排序规则（隐含字符集）必须支持中文
	type tableInfo struct {
		Name      string
		Collation *string
	}
	var tables []tableInfo
	if err := db.Raw("SELECT TABLE_NAME, TABLE_COLLATION FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'").Scan(&tables).Error; err != nil {
		return fmt.Errorf("读取表排序规则失败 / Failed to read table collations: %v", err)
	}

	var badTables []string
	for _, t := range tables {
		// NULL 或空表示继承库默认设置，已在上面校验库默认，视为通过
		if t.Collation == nil || *t.Collation == "" {
			continue
		}
		cl := *t.Collation
		// 仅凭排序规则判断是否中文可用
		ok := false
		lower := strings.ToLower(cl)
		for _, prefix := range allowedCharsets {
			if strings.HasPrefix(lower, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			badTables = append(badTables, fmt.Sprintf("%s(%s)", t.Name, cl))
		}
	}

	if len(badTables) > 0 {
		// 限制输出数量以避免日志过长
		maxShow := 20
		shown := badTables
		if len(shown) > maxShow {
			shown = shown[:maxShow]
		}
		return fmt.Errorf(
			"存在不支持中文的表，请修复其排序规则/字符集。示例（最多展示 %d 项）：%v / Found tables not Chinese-capable. Please fix their collation/charset. Examples (showing up to %d): %v",
			maxShow, shown, maxShow, shown,
		)
	}
	return nil
}

var (
	lastPingTime time.Time
	pingMutex    sync.Mutex
)

func PingDB() error {
	pingMutex.Lock()
	defer pingMutex.Unlock()

	if time.Since(lastPingTime) < time.Second*10 {
		return nil
	}

	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("Error getting sql.DB from GORM: %v", err)
		return err
	}

	err = sqlDB.Ping()
	if err != nil {
		log.Printf("Error pinging DB: %v", err)
		return err
	}

	lastPingTime = time.Now()
	common.SysLog("Database pinged successfully")
	return nil
}
