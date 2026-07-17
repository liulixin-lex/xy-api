package model

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// SQLite/MySQL-safe defaults keep package-level test databases usable even
// when they are installed directly instead of through InitDB/InitLogDB.
// Production initialization still overwrites these for PostgreSQL.
var commonGroupCol = "`group`"
var commonKeyCol = "`key`"
var commonTrueVal = "1"
var commonFalseVal = "0"

var logKeyCol = "`key`"
var logGroupCol = "`group`"

const (
	routingAlphaDrainedEnv           = "ROUTING_ALPHA_DRAINED"
	routingSchemaReadyWaitSecondsEnv = "ROUTING_SCHEMA_READY_WAIT_SECONDS"
	routingSchemaReadyWaitDefault    = 60
)

var ErrRoutingRetirementAlphaDrainRequired = errors.New(
	"legacy routing alpha nodes, connector workers, and telemetry must be drained before retirement",
)

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
			if err := waitRoutingSchemaReady(DB); err != nil {
				return err
			}
			if err := waitAsyncBillingV2SchemaReadyFromEnv(); err != nil {
				return err
			}
			if err := waitBillingStatsProjectionSchemaReadyFromEnv(); err != nil {
				return err
			}
			return waitBillingLogProjectionSchemaReadyFromEnv()
		}
		if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
			//_, _ = sqlDB.Exec("ALTER TABLE channels MODIFY model_mapping TEXT;") // TODO: delete this line when most users have upgraded
		}
		common.SysLog("database migration started")
		err = migrateDB()
		return err
	} else {
		common.FatalLog(err)
	}
	return err
}

func InitLogDB() (err error) {
	if os.Getenv("LOG_SQL_DSN") == "" {
		LOG_DB = DB
		common.SetLogDatabaseType(common.MainDatabaseType())
		initCol()
		if common.IsMasterNode {
			return waitBillingLogSchemaReady(context.Background(), LOG_DB, 0)
		}
		return waitBillingLogSchemaReadyFromEnv()
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
			return waitBillingLogSchemaReadyFromEnv()
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
	if err := preflightRoutingSchemaCutover(DB); err != nil {
		return err
	}
	if err := invalidateRoutingSchemaVersion(DB); err != nil {
		return err
	}
	// Migrate price_amount column from float/double to decimal for existing tables
	migrateSubscriptionPlanPriceAmount()
	// Migrate model_limits column from varchar to text for existing tables
	if err := migrateTokenModelLimitsToText(); err != nil {
		return err
	}
	if err := prepareRoutingCanaryEvaluationWindowUniqueIndex(DB); err != nil {
		return err
	}
	if err := prepareRoutingControlPlaneV2Schema(DB); err != nil {
		return err
	}
	if err := prepareRoutingTopologyGenerationSchema(DB); err != nil {
		return err
	}
	if err := prepareRoutingRuntimeGenerationSchema(DB); err != nil {
		return err
	}
	if err := prepareBillingLogOperationKeyColumn(DB); err != nil {
		return err
	}

	err := DB.AutoMigrate(
		&Channel{},
		&Token{},
		&User{},
		&PasskeyCredential{},
		&Option{},
		&Redemption{},
		&Ability{},
		&Log{},
		&Midjourney{},
		&MidjourneyBillingOperation{},
		&IdentityCacheSync{},
		&AsyncBillingReservation{},
		&AsyncBillingAttempt{},
		&BillingStatsProjection{},
		&BillingLogProjection{},
		&BillingLogSinkConflictAudit{},
		&BillingLogSinkConflictResolution{},
		&BillingProjectionAdminOperation{},
		&AsyncBillingManualResolution{},
		&TopUp{},
		&AffiliateRewardRecord{},
		&InviteInitialQuotaRecord{},
		&InviteLinkBatch{},
		&ReferralCapture{},
		&QuotaData{},
		&Task{},
		&TaskBillingOperation{},
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
		&SubscriptionBillingPeriod{},
		&CustomOAuthProvider{},
		&UserOAuthBinding{},
		&PerfMetric{},
		&RoutingSchemaVersion{},
		&RoutingTopologyMetadata{},
		&RoutingChannelLifecycle{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
		&RoutingDecisionAudit{},
		&RoutingDecisionReplayChunk{},
		&RoutingPolicyHead{},
		&RoutingPolicyRevision{},
		&RoutingPolicyPoolRevision{},
		&RoutingPolicyMemberRevision{},
		&RoutingPolicyActivation{},
		&RoutingPolicyDraft{},
		&RoutingPolicySimulationEvidence{},
		&RoutingPolicyRiskAcceptance{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
		&RoutingConfigOutbox{},
		&RoutingRuntimeCheckpoint{},
		&RoutingControlLease{},
		&RoutingProbeResult{},
		&RoutingEndpointEvidence{},
		&RoutingEndpointSharedState{},
		&RoutingCanaryEvaluation{},
		&RoutingOperation{},
		&RoutingBreakerResetCommand{},
		&RoutingBreakerResetFence{},
		&RoutingBreakerResetTombstone{},
		&RoutingBreakerResetOutbox{},
		&RoutingAuditExport{},
		&RoutingAuditExportChunk{},
		&RoutingHedgeAttemptAudit{},
		&RoutingRuntimeSettingsState{},
		&RoutingControlAudit{},
		&RoutingPricingVersion{},
		&RoutingCostSnapshotVersion{},
		&RoutingConfigurationEpoch{},
		&RoutingChannelConfiguration{},
		&RoutingChannelConfigurationOutbox{},
		&RoutingChannelMetric{},
		&RoutingTelemetryReceipt{},
		&RoutingBreakerState{},
		&RoutingChannelHealthState{},
		&RoutingCredentialHealthState{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
		&CasbinRule{},
		&AuthzRole{},
	)
	if err != nil {
		return err
	}
	if err := migrateRequestProfileEnabledOption(DB); err != nil {
		return err
	}
	if err := ensureAsyncBillingManualResolutionUniqueIndex(DB); err != nil {
		return err
	}
	if err := waitBillingStatsProjectionSchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := waitBillingLogProjectionSchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := WaitAsyncBillingV2SchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := EnsureChannelRoutingGenerations(DB); err != nil {
		return err
	}
	if err := EnsureChannelRoutingIdentitiesAndLifecycles(DB); err != nil {
		return err
	}
	if err := MigrateRoutingCostSnapshotGenerationSchema(DB); err != nil {
		return err
	}
	if err := EnsureRoutingPricingVersion(DB); err != nil {
		return err
	}
	if err := MigrateRoutingTopologyGenerationSchema(DB); err != nil {
		return err
	}
	if err := MigrateRoutingRuntimeGenerationSchema(DB); err != nil {
		return err
	}
	if err := RetireRoutingAgentPlaceholderSchema(DB); err != nil {
		return err
	}
	if err := migrateRoutingDedicatedSchemas(DB); err != nil {
		return err
	}
	if err := ensureRoutingOperationRequestKeyUniqueIndex(DB); err != nil {
		return err
	}
	if err := EnsureRoutingPolicyHead(); err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := ensureSubscriptionPlanTableSQLite(); err != nil {
			return err
		}
	} else {
		if err := DB.AutoMigrate(&SubscriptionPlan{}); err != nil {
			return err
		}
	}
	return publishRoutingSchemaVersion(DB)
}

func migrateDBFast() error {
	if err := preflightRoutingSchemaCutover(DB); err != nil {
		return err
	}
	if err := invalidateRoutingSchemaVersion(DB); err != nil {
		return err
	}
	if err := prepareRoutingCanaryEvaluationWindowUniqueIndex(DB); err != nil {
		return err
	}
	if err := prepareRoutingControlPlaneV2Schema(DB); err != nil {
		return err
	}
	if err := prepareRoutingTopologyGenerationSchema(DB); err != nil {
		return err
	}
	if err := prepareRoutingRuntimeGenerationSchema(DB); err != nil {
		return err
	}
	if err := prepareBillingLogOperationKeyColumn(DB); err != nil {
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
		{&MidjourneyBillingOperation{}, "MidjourneyBillingOperation"},
		{&IdentityCacheSync{}, "IdentityCacheSync"},
		{&AsyncBillingReservation{}, "AsyncBillingReservation"},
		{&AsyncBillingAttempt{}, "AsyncBillingAttempt"},
		{&BillingStatsProjection{}, "BillingStatsProjection"},
		{&BillingLogProjection{}, "BillingLogProjection"},
		{&BillingLogSinkConflictAudit{}, "BillingLogSinkConflictAudit"},
		{&BillingLogSinkConflictResolution{}, "BillingLogSinkConflictResolution"},
		{&BillingProjectionAdminOperation{}, "BillingProjectionAdminOperation"},
		{&AsyncBillingManualResolution{}, "AsyncBillingManualResolution"},
		{&TopUp{}, "TopUp"},
		{&AffiliateRewardRecord{}, "AffiliateRewardRecord"},
		{&InviteInitialQuotaRecord{}, "InviteInitialQuotaRecord"},
		{&InviteLinkBatch{}, "InviteLinkBatch"},
		{&ReferralCapture{}, "ReferralCapture"},
		{&QuotaData{}, "QuotaData"},
		{&Task{}, "Task"},
		{&TaskBillingOperation{}, "TaskBillingOperation"},
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
		{&SubscriptionBillingPeriod{}, "SubscriptionBillingPeriod"},
		{&CustomOAuthProvider{}, "CustomOAuthProvider"},
		{&UserOAuthBinding{}, "UserOAuthBinding"},
		{&PerfMetric{}, "PerfMetric"},
		{&RoutingSchemaVersion{}, "RoutingSchemaVersion"},
		{&RoutingTopologyMetadata{}, "RoutingTopologyMetadata"},
		{&RoutingChannelLifecycle{}, "RoutingChannelLifecycle"},
		{&RoutingPool{}, "RoutingPool"},
		{&RoutingPoolMember{}, "RoutingPoolMember"},
		{&RoutingCredentialRef{}, "RoutingCredentialRef"},
		{&RoutingDecisionAudit{}, "RoutingDecisionAudit"},
		{&RoutingDecisionReplayChunk{}, "RoutingDecisionReplayChunk"},
		{&RoutingPolicyHead{}, "RoutingPolicyHead"},
		{&RoutingPolicyRevision{}, "RoutingPolicyRevision"},
		{&RoutingPolicyPoolRevision{}, "RoutingPolicyPoolRevision"},
		{&RoutingPolicyMemberRevision{}, "RoutingPolicyMemberRevision"},
		{&RoutingPolicyActivation{}, "RoutingPolicyActivation"},
		{&RoutingPolicyDraft{}, "RoutingPolicyDraft"},
		{&RoutingPolicySimulationEvidence{}, "RoutingPolicySimulationEvidence"},
		{&RoutingPolicyRiskAcceptance{}, "RoutingPolicyRiskAcceptance"},
		{&RoutingPolicyApproval{}, "RoutingPolicyApproval"},
		{&RoutingPolicyRollbackApproval{}, "RoutingPolicyRollbackApproval"},
		{&RoutingConfigOutbox{}, "RoutingConfigOutbox"},
		{&RoutingRuntimeCheckpoint{}, "RoutingRuntimeCheckpoint"},
		{&RoutingControlLease{}, "RoutingControlLease"},
		{&RoutingProbeResult{}, "RoutingProbeResult"},
		{&RoutingEndpointEvidence{}, "RoutingEndpointEvidence"},
		{&RoutingEndpointSharedState{}, "RoutingEndpointSharedState"},
		{&RoutingCanaryEvaluation{}, "RoutingCanaryEvaluation"},
		{&RoutingOperation{}, "RoutingOperation"},
		{&RoutingBreakerResetCommand{}, "RoutingBreakerResetCommand"},
		{&RoutingBreakerResetFence{}, "RoutingBreakerResetFence"},
		{&RoutingBreakerResetTombstone{}, "RoutingBreakerResetTombstone"},
		{&RoutingBreakerResetOutbox{}, "RoutingBreakerResetOutbox"},
		{&RoutingAuditExport{}, "RoutingAuditExport"},
		{&RoutingAuditExportChunk{}, "RoutingAuditExportChunk"},
		{&RoutingHedgeAttemptAudit{}, "RoutingHedgeAttemptAudit"},
		{&RoutingRuntimeSettingsState{}, "RoutingRuntimeSettingsState"},
		{&RoutingControlAudit{}, "RoutingControlAudit"},
		{&RoutingPricingVersion{}, "RoutingPricingVersion"},
		{&RoutingCostSnapshotVersion{}, "RoutingCostSnapshotVersion"},
		{&RoutingConfigurationEpoch{}, "RoutingConfigurationEpoch"},
		{&RoutingChannelConfiguration{}, "RoutingChannelConfiguration"},
		{&RoutingChannelConfigurationOutbox{}, "RoutingChannelConfigurationOutbox"},
		{&RoutingChannelMetric{}, "RoutingChannelMetric"},
		{&RoutingTelemetryReceipt{}, "RoutingTelemetryReceipt"},
		{&RoutingBreakerState{}, "RoutingBreakerState"},
		{&RoutingChannelHealthState{}, "RoutingChannelHealthState"},
		{&RoutingCredentialHealthState{}, "RoutingCredentialHealthState"},
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
			if err := DB.AutoMigrate(model); err != nil {
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
	if err := migrateRequestProfileEnabledOption(DB); err != nil {
		return err
	}
	if err := ensureAsyncBillingManualResolutionUniqueIndex(DB); err != nil {
		return err
	}
	if err := waitBillingStatsProjectionSchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := waitBillingLogProjectionSchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := WaitAsyncBillingV2SchemaReady(context.Background(), DB, 0); err != nil {
		return err
	}
	if err := EnsureChannelRoutingGenerations(DB); err != nil {
		return err
	}
	if err := EnsureChannelRoutingIdentitiesAndLifecycles(DB); err != nil {
		return err
	}
	if err := MigrateRoutingCostSnapshotGenerationSchema(DB); err != nil {
		return err
	}
	if err := EnsureRoutingPricingVersion(DB); err != nil {
		return err
	}
	if err := MigrateRoutingTopologyGenerationSchema(DB); err != nil {
		return err
	}
	if err := MigrateRoutingRuntimeGenerationSchema(DB); err != nil {
		return err
	}
	if err := RetireRoutingAgentPlaceholderSchema(DB); err != nil {
		return err
	}
	if err := migrateRoutingDedicatedSchemas(DB); err != nil {
		return err
	}
	if err := ensureRoutingOperationRequestKeyUniqueIndex(DB); err != nil {
		return err
	}
	if err := EnsureRoutingPolicyHead(); err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := ensureSubscriptionPlanTableSQLite(); err != nil {
			return err
		}
	} else {
		if err := DB.AutoMigrate(&SubscriptionPlan{}); err != nil {
			return err
		}
	}
	if err := publishRoutingSchemaVersion(DB); err != nil {
		return err
	}
	common.SysLog("database migrated")
	return nil
}

func migrateRoutingDedicatedSchemas(db *gorm.DB) error {
	alphaDrained := common.GetEnvOrDefaultBool(routingAlphaDrainedEnv, false)
	// General schema preparation runs before this function, but every operation
	// that can backfill, scrub, terminate, or contract retired routing state is
	// fenced by this read-only preflight.
	if err := validateRoutingRetirementCutover(db, alphaDrained); err != nil {
		return routingMigrationError("routing retirement preflight", err)
	}
	if db.Migrator().HasTable(&Channel{}) && db.Migrator().HasTable(&RoutingChannelConfiguration{}) {
		if err := EnsureRoutingConfigurationEpoch(db); err != nil {
			return routingMigrationError("routing configuration epoch", err)
		}
		if err := MigrateRoutingChannelConfigurations(db); err != nil {
			return routingMigrationError("routing channel configurations", err)
		}
		if _, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db); err != nil {
			return routingMigrationError("retire routing upstream account connectors", err)
		}
	}
	if err := migrateRoutingOperationStateInvariants(db); err != nil {
		return routingMigrationError("routing operation invariants", err)
	}
	if err := retireRoutingCostSyncWork(db); err != nil {
		return routingMigrationError("retire routing cost sync work", err)
	}
	rollupOptions := RoutingMetricRollupMigrationOptions{AlphaDrained: alphaDrained}
	if err := MigrateRoutingMetricRollupRevisionKeyWithOptions(db, rollupOptions); err != nil {
		return routingMigrationError("routing metric rollup", err)
	}
	errorBudgetOptions := RoutingErrorBudgetMigrationOptions{AlphaDrained: alphaDrained}
	if err := MigrateRoutingErrorBudgetModelsWithOptions(db, errorBudgetOptions); err != nil {
		return routingMigrationError("routing error budget", err)
	}
	if err := MigrateRoutingPolicyApprovalIntentIndexes(db); err != nil {
		return routingMigrationError("routing policy approval indexes", err)
	}
	return nil
}

// validateRoutingRetirementCutover is deliberately read-only. The drain flag
// acknowledges a one-time cutover: once all legacy indexes and mutable
// connector/work state have converged, later restarts do not need to keep it.
func validateRoutingRetirementCutover(db *gorm.DB, alphaDrained bool) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}

	drainReasons := make([]string, 0, 10)
	legacyRollupColumns := []string{"member_id", "credential_id", "model_key", "bucket_ts"}
	revisionRollupColumns := []string{
		"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision",
	}
	columns, unique, exists, err := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
	if err != nil {
		return fmt.Errorf("inspect legacy routing metric rollup index: %w", err)
	}
	if exists {
		switch {
		case unique && routingMetricRollupIndexColumnsEqual(columns, legacyRollupColumns):
			drainReasons = append(drainReasons, "legacy routing metric rollup writers")
		case !unique || !routingMetricRollupIndexColumnsEqual(columns, revisionRollupColumns):
			return fmt.Errorf(
				"routing metric rollup index %s has unexpected definition",
				routingMetricRollupLegacyUniqueIndex,
			)
		}
	}

	columns, unique, exists, err = routingErrorBudgetIndexDefinition(
		db, (RoutingErrorBudgetState{}).TableName(), routingErrorBudgetLegacyPoolIndex,
	)
	if err != nil {
		return fmt.Errorf("inspect legacy routing error budget index: %w", err)
	}
	if exists {
		if !unique || !routingErrorBudgetIndexColumnsEqual(columns, []string{"pool_id"}) {
			return fmt.Errorf(
				"routing error budget index %s has unexpected definition",
				routingErrorBudgetLegacyPoolIndex,
			)
		}
		drainReasons = append(drainReasons, "legacy routing error budget writers")
	}

	for _, table := range []struct {
		model   any
		name    string
		columns []retiredRoutingColumnScrub
		reason  string
	}{
		{
			model: &RoutingChannelBinding{}, name: (RoutingChannelBinding{}).TableName(),
			columns: retiredRoutingChannelBindingScrubs(), reason: "active legacy routing channel bindings",
		},
		{
			model: &RoutingUpstreamAccount{}, name: (RoutingUpstreamAccount{}).TableName(),
			columns: retiredRoutingUpstreamAccountScrubs(), reason: "active legacy routing upstream accounts",
		},
		{
			model: &RoutingChannelHealthState{}, name: (RoutingChannelHealthState{}).TableName(),
			columns: retiredRoutingChannelBalanceScrubs(), reason: "legacy connector channel balance state",
		},
		{
			model: &RoutingCostSnapshot{}, name: (RoutingCostSnapshot{}).TableName(),
			columns: retiredRoutingCostSnapshotAccountScrubs(), reason: "legacy cost snapshot account state",
		},
	} {
		if !db.Migrator().HasTable(table.name) {
			continue
		}
		pending, err := routingRetirementTableNeedsScrub(db, table.model, table.name, table.columns)
		if err != nil {
			return fmt.Errorf("inspect %s retirement state: %w", table.name, err)
		}
		if pending {
			drainReasons = append(drainReasons, table.reason)
		}
	}
	if db.Migrator().HasTable(retiredRoutingUpstreamAccountHealthTable) {
		var pendingHealth struct {
			Present int `gorm:"column:present"`
		}
		if err := db.Table(retiredRoutingUpstreamAccountHealthTable).
			Select("1 AS present").Limit(1).Scan(&pendingHealth).Error; err != nil {
			return fmt.Errorf("inspect legacy routing upstream account health: %w", err)
		}
		if pendingHealth.Present == 1 {
			drainReasons = append(drainReasons, "legacy routing upstream account health")
		}
	}

	if db.Migrator().HasTable(&RoutingOperation{}) {
		var operationIDs []int64
		if err := db.Model(&RoutingOperation{}).
			Where("operation_type = ? AND status IN ?", RoutingOperationTypeCostSync, []RoutingOperationStatus{
				RoutingOperationStatusPending,
				RoutingOperationStatusRunning,
				RoutingOperationStatusRetryWait,
			}).Limit(1).Pluck("id", &operationIDs).Error; err != nil {
			return fmt.Errorf("inspect legacy routing cost sync operations: %w", err)
		}
		if len(operationIDs) != 0 {
			drainReasons = append(drainReasons, "active routing cost sync operations")
		}
	}
	if db.Migrator().HasTable(&SystemTask{}) {
		var taskIDs []int64
		if err := db.Model(&SystemTask{}).
			Where("type = ? AND status IN ?", SystemTaskTypeRoutingCostSync, []SystemTaskStatus{
				SystemTaskStatusPending,
				SystemTaskStatusRunning,
			}).Limit(1).Pluck("id", &taskIDs).Error; err != nil {
			return fmt.Errorf("inspect legacy routing cost sync tasks: %w", err)
		}
		if len(taskIDs) != 0 {
			drainReasons = append(drainReasons, "active routing cost sync tasks")
		}
	}
	if db.Migrator().HasTable(&SystemTaskLock{}) {
		var lockTypes []string
		if err := db.Model(&SystemTaskLock{}).
			Where("type = ?", SystemTaskTypeRoutingCostSync).
			Limit(1).Pluck("type", &lockTypes).Error; err != nil {
			return fmt.Errorf("inspect legacy routing cost sync locks: %w", err)
		}
		if len(lockTypes) != 0 {
			drainReasons = append(drainReasons, "routing cost sync task locks")
		}
	}

	if !alphaDrained && len(drainReasons) != 0 {
		return fmt.Errorf(
			"%w: %s",
			ErrRoutingRetirementAlphaDrainRequired,
			strings.Join(drainReasons, ", "),
		)
	}
	return nil
}

func routingRetirementTableNeedsScrub(
	db *gorm.DB,
	tableModel any,
	tableName string,
	columns []retiredRoutingColumnScrub,
) (bool, error) {
	if db == nil || db.Dialector == nil || tableModel == nil || tableName == "" {
		return false, ErrRoutingSchemaNotReady
	}
	predicates := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		if column.column == "" || column.predicate == "" || !db.Migrator().HasColumn(tableModel, column.column) {
			continue
		}
		predicates = append(predicates, "("+column.predicate+")")
		args = append(args, column.args...)
	}
	if len(predicates) == 0 {
		return false, nil
	}
	var pending struct {
		Present int `gorm:"column:present"`
	}
	if err := db.Table(tableName).Select("1 AS present").
		Where(strings.Join(predicates, " OR "), args...).Limit(1).Scan(&pending).Error; err != nil {
		return false, err
	}
	return pending.Present == 1, nil
}

func waitRoutingSchemaReady(db *gorm.DB) error {
	waitSeconds := common.GetEnvOrDefault(routingSchemaReadyWaitSecondsEnv, routingSchemaReadyWaitDefault)
	if waitSeconds < 0 {
		return fmt.Errorf("%s must be non-negative", routingSchemaReadyWaitSecondsEnv)
	}
	if err := WaitRoutingSchemaReady(context.Background(), db, time.Duration(waitSeconds)*time.Second); err != nil {
		return fmt.Errorf("wait for channel routing schema version %s: %w", routingSchemaVersion, err)
	}
	return nil
}

func routingMigrationError(component string, err error) error {
	if errors.Is(err, ErrRoutingRetirementAlphaDrainRequired) ||
		errors.Is(err, ErrRoutingMetricRollupAlphaDrainRequired) ||
		errors.Is(err, ErrRoutingErrorBudgetAlphaDrainRequired) {
		return fmt.Errorf(
			"migrate %s: %w; stop all legacy routing alpha nodes and connector workers, drain legacy Redis telemetry stream routing:v2:telemetry, then set %s=true",
			component, err, routingAlphaDrainedEnv,
		)
	}
	return fmt.Errorf("migrate %s: %w", component, err)
}

func migrateLOGDB() error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return migrateClickHouseLogDB()
	}
	if err := prepareBillingLogOperationKeyColumn(LOG_DB); err != nil {
		return err
	}
	if err := LOG_DB.AutoMigrate(&Log{}); err != nil {
		return err
	}
	ready, err := billingLogSchemaReady(LOG_DB)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("billing log schema migration completed without the required unique receipt index")
	}
	return nil
}

func migrateClickHouseLogDB() error {
	ttlDays := clickHouseLogTTLDays()
	if err := LOG_DB.Exec(clickHouseLogCreateTableSQL(ttlDays)).Error; err != nil {
		return err
	}
	for _, statement := range clickHouseBillingLogColumnMigrations() {
		if err := LOG_DB.Exec(statement).Error; err != nil {
			return err
		}
	}
	if err := LOG_DB.Exec(clickHouseVisibleLogsViewSQL()).Error; err != nil {
		return err
	}
	if err := syncClickHouseLogTTL(ttlDays); err != nil {
		return err
	}
	ready, err := billingLogSchemaReady(LOG_DB)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("ClickHouse billing log schema migration did not become ready")
	}
	return nil
}

func waitBillingLogSchemaReadyFromEnv() error {
	waitSeconds := common.GetEnvOrDefault(billingLogSchemaReadyWaitEnv, billingLogSchemaReadyWaitDefault)
	if waitSeconds < 0 {
		return fmt.Errorf("%s must be non-negative", billingLogSchemaReadyWaitEnv)
	}
	if err := waitBillingLogSchemaReady(context.Background(), LOG_DB, time.Duration(waitSeconds)*time.Second); err != nil {
		return fmt.Errorf("wait for billing log sink schema: %w", err)
	}
	return nil
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
	billing_operation_key Nullable(String) DEFAULT NULL,
	billing_payload_hash String DEFAULT '',
	billing_payload_protocol UInt16 DEFAULT 0,
	billing_sink_written_at Int64 DEFAULT 0,
	other String DEFAULT ''
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, request_id)%s`, clickHouseLogTTLClause(ttlDays))
}

func clickHouseBillingLogColumnMigrations() []string {
	return []string{
		"ALTER TABLE logs ADD COLUMN IF NOT EXISTS billing_operation_key Nullable(String) DEFAULT NULL AFTER upstream_request_id",
		"ALTER TABLE logs ADD COLUMN IF NOT EXISTS billing_payload_hash String DEFAULT '' AFTER billing_operation_key",
		"ALTER TABLE logs ADD COLUMN IF NOT EXISTS billing_payload_protocol UInt16 DEFAULT 0 AFTER billing_payload_hash",
		"ALTER TABLE logs ADD COLUMN IF NOT EXISTS billing_sink_written_at Int64 DEFAULT 0 AFTER billing_payload_protocol",
	}
}

func clickHouseVisibleLogsViewSQL() string {
	return fmt.Sprintf(`CREATE OR REPLACE VIEW %s AS
SELECT *
FROM logs
WHERE billing_operation_key IS NULL
UNION ALL
SELECT *
FROM
(
	SELECT *
	FROM logs
	WHERE billing_operation_key IS NOT NULL
		AND billing_operation_key IN
		(
			SELECT billing_operation_key
			FROM logs
			WHERE billing_operation_key IS NOT NULL
			GROUP BY billing_operation_key
			HAVING uniqExact(tuple(billing_payload_protocol, billing_payload_hash)) = 1
		)
	ORDER BY created_at ASC, request_id ASC
	LIMIT 1 BY billing_operation_key
)`, clickHouseVisibleLogsView)
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

func ensureSubscriptionPlanTableSQLite() error {
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return nil
	}
	tableName := "subscription_plans"
	if !DB.Migrator().HasTable(tableName) {
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
		return DB.Exec(createSQL).Error
	}
	var cols []struct {
		Name string `gorm:"column:name"`
	}
	if err := DB.Raw("PRAGMA table_info(`" + tableName + "`)").Scan(&cols).Error; err != nil {
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
		if err := DB.Exec("ALTER TABLE `" + tableName + "` ADD COLUMN " + col.DDL).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateTokenModelLimitsToText migrates model_limits column from varchar(1024) to text
// This is safe to run multiple times - it checks the column type first
func migrateTokenModelLimitsToText() error {
	// SQLite uses type affinity, so TEXT and VARCHAR are effectively the same — no migration needed
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return nil
	}

	tableName := "tokens"
	columnName := "model_limits"

	if !DB.Migrator().HasTable(tableName) {
		return nil
	}

	if !DB.Migrator().HasColumn(&Token{}, columnName) {
		return nil
	}

	var alterSQL string
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		var dataType string
		if err := DB.Raw(`SELECT data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			tableName, columnName).Scan(&dataType).Error; err != nil {
			common.SysLog(fmt.Sprintf("Warning: failed to query metadata for %s.%s: %v", tableName, columnName, err))
		} else if dataType == "text" {
			return nil
		}
		alterSQL = fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s TYPE text`, tableName, columnName)
	} else if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		var columnType string
		if err := DB.Raw(`SELECT COLUMN_TYPE FROM information_schema.columns
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
		if err := DB.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("failed to migrate %s.%s to text: %w", tableName, columnName, err)
		}
		common.SysLog(fmt.Sprintf("Successfully migrated %s.%s to text", tableName, columnName))
	}
	return nil
}

// migrateSubscriptionPlanPriceAmount migrates price_amount column from float/double to decimal(10,6)
// This is safe to run multiple times - it checks the column type first
func migrateSubscriptionPlanPriceAmount() {
	// SQLite doesn't support ALTER COLUMN, and its type affinity handles this automatically
	// Skip early to avoid GORM parsing the existing table DDL which may cause issues
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return
	}

	tableName := "subscription_plans"
	columnName := "price_amount"

	// Check if table exists first
	if !DB.Migrator().HasTable(tableName) {
		return
	}

	// Check if column exists
	if !DB.Migrator().HasColumn(&SubscriptionPlan{}, columnName) {
		return
	}

	var alterSQL string
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		// PostgreSQL: Check if already decimal/numeric
		var dataType string
		if err := DB.Raw(`SELECT data_type FROM information_schema.columns
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
		if err := DB.Raw(`SELECT COLUMN_TYPE FROM information_schema.columns
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
		if err := DB.Exec(alterSQL).Error; err != nil {
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
