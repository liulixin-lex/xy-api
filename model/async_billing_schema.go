package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrAsyncBillingSchemaNotReady = errors.New("async billing v2 schema is not ready")

const (
	asyncBillingSchemaWaitEnv     = "ASYNC_BILLING_SCHEMA_READY_WAIT_SECONDS"
	asyncBillingSchemaWaitDefault = 60

	asyncBillingManualResolutionUniqueIndex = "uidx_async_billing_manual_reservation"
	asyncBillingManualResolutionGuardIndex  = "uidx_async_billing_manual_reservation_v2_guard"
	asyncBillingManualResolutionIndexLock   = "async-billing-manual-resolution-index-v2"
	asyncBillingManualResolutionIndexWait   = 60
)

type asyncBillingSchemaRequirement struct {
	table   string
	model   any
	columns []string
	indexes map[string][]string
}

type asyncBillingManualResolutionIndexGuard struct {
	ReservationID   int64 `gorm:"uniqueIndex:uidx_async_billing_manual_reservation_v2_guard,priority:1"`
	ExpectedVersion int64 `gorm:"uniqueIndex:uidx_async_billing_manual_reservation_v2_guard,priority:2"`
}

func (asyncBillingManualResolutionIndexGuard) TableName() string {
	return "async_billing_manual_resolutions"
}

// AsyncBillingV2SchemaReady verifies the complete durable handoff contract.
// Routing schema readiness is intentionally separate: a routing rollout may be
// current while one of the financial receipt tables or indexes is still old.
func AsyncBillingV2SchemaReady(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("async billing database is unavailable")
	}
	requirements := []asyncBillingSchemaRequirement{
		{
			table:   "tasks",
			model:   &Task{},
			columns: []string{"DurableQuota", "DurablePrivateDataPayload", "DurablePrivateDataHash", "PrivateData"},
		},
		{
			table: "midjourneys",
			model: &Midjourney{},
			columns: []string{
				"Quota", "DurableQuota", "UpstreamTaskID", "RoutingCredentialID", "ChannelGeneration",
				"BillingSource", "BillingProtocolVersion", "AsyncBillingReservationID", "SubscriptionId",
				"TokenId", "NodeName", "BillingAuditPayload",
			},
		},
		{
			table: "async_billing_reservations",
			model: &AsyncBillingReservation{},
			columns: []string{
				"ReservationKey", "ProtocolVersion", "Kind", "PublicTaskID", "State",
				"ClientKeyHash", "ClientPayloadHash", "ClientScope", "UserID", "TokenID", "FundingSource",
				"SubscriptionID", "SubscriptionPeriodID", "SubscriptionPlanID", "SubscriptionPlanName",
				"SubscriptionTotal", "InitialQuota", "CurrentQuota", "AcceptedQuota",
				"AcceptedAttemptIndex", "TaskID", "MidjourneyID", "UpstreamTaskID", "SendAuthorizedMs",
				"CreatedTimeMs", "UpdatedTimeMs", "AcceptedTimeMs", "TerminalTimeMs", "LastError",
				"ManualReviewRequiredMs", "ManualReviewReason", "ManualReviewKind", "ReviewVersion",
				"ReviewTargetQuota", "ReviewAuditProtocol", "ReviewAuditHash", "ReviewAuditPayload",
				"ReplayProtocol", "ReplayReady", "ReplayStatusCode", "ReplayContentType", "ReplayHeadersJSON",
				"ReplayBody", "ReplayHash", "CacheSyncVersion", "CacheSyncedVersion", "CacheSyncPending",
				"CacheSyncNextRetryMs", "CacheSyncAttempts", "CacheSyncLastError",
				"AcceptedProjectionKey", "AcceptedProjectionState", "AcceptedProjectionChannelID",
				"AcceptedProjectionModelName", "AcceptedProjectionGroup", "AcceptedProjectionTaskIdentity",
				"AcceptedProjectionQuota", "AcceptedProjectionRequestDelta", "AcceptedProjectionLogProtocol",
				"AcceptedProjectionLogHash", "AcceptedProjectionLogPayload", "AcceptedProjectionCreatedMs",
				"AcceptedProjectionNextRetryMs", "AcceptedProjectionAttempts", "AcceptedProjectionLastError",
				"AcceptedProjectionUserOutcome", "AcceptedProjectionChannelOutcome", "AcceptedProjectionWarning",
			},
			indexes: map[string][]string{
				"uidx_async_billing_reservation_key":                     {"reservation_key"},
				"uidx_async_billing_public_task":                         {"public_task_id"},
				"uidx_async_billing_client_key":                          {"client_key_hash"},
				"idx_async_billing_reservations_accepted_projection_key": {"accepted_projection_key"},
			},
		},
		{
			table: "async_billing_attempts",
			model: &AsyncBillingAttempt{},
			columns: []string{
				"ReservationID", "AttemptIndex", "State", "ChannelID", "CredentialID", "ChannelVersion",
				"AuthorizedMs", "SendDeadlineMs", "ResolvedMs", "FailureCode", "IntentProtocol",
				"IntentPayloadHash", "IntentPayload",
			},
			indexes: map[string][]string{
				"uidx_async_billing_attempt": {"reservation_id", "attempt_index"},
			},
		},
		{
			table: "async_billing_manual_resolutions",
			model: &AsyncBillingManualResolution{},
			columns: []string{
				"ReservationID", "Action", "ReviewKind", "ActorUserID", "ExpectedState", "ExpectedVersion",
				"ExpectedETag", "UpstreamTaskID", "ProviderStatus", "ProviderCheckedMs", "EvidenceReference",
				"Reason", "BeforeState", "AfterState", "BeforeQuota", "AfterQuota", "QuotaDelta",
				"DecisionKeyHash", "DecisionPayloadHash", "CreatedTimeMs", "ResolvedTimeMs",
			},
			indexes: map[string][]string{
				asyncBillingManualResolutionUniqueIndex: {"reservation_id", "expected_version"},
				"uidx_async_billing_manual_decision":    {"decision_key_hash"},
			},
		},
		{
			table: "billing_log_sink_conflict_audits",
			model: &BillingLogSinkConflictAudit{},
			columns: []string{
				"OperationKey", "ProjectionID", "ExpectedPayloadHash", "ExpectedPayloadProtocol",
				"Receipts", "DistinctReceipts", "PhysicalRows", "State", "Version",
				"FirstDetectedMs", "LastDetectedMs", "LastResolvedMs", "LastResolvedBy",
				"LastResolutionReason",
			},
			indexes: map[string][]string{
				"uidx_billing_log_sink_conflict_operation": {"operation_key"},
			},
		},
		{
			table: "billing_log_sink_conflict_resolutions",
			model: &BillingLogSinkConflictResolution{},
			columns: []string{
				"ConflictAuditID", "ConflictVersion", "OperationKey", "ActorUserID", "Reason",
				"VerifiedPayloadHash", "VerifiedPayloadProtocol", "ResolvedTimeMs",
			},
			indexes: map[string][]string{
				"uidx_billing_log_sink_conflict_resolution": {"conflict_audit_id", "conflict_version"},
			},
		},
		{
			table: "task_billing_operations",
			model: &TaskBillingOperation{},
			columns: []string{
				"TaskID", "ReservationID", "OperationKey", "TerminalStatus", "Kind", "State", "UserID",
				"ChannelID", "BillingSource", "SubscriptionID", "TokenID", "PreConsumedQuota", "TargetQuota",
				"QuotaDelta", "QuotaClampOp", "QuotaClampKind", "QuotaClampOriginal", "QuotaClampValue",
				"TerminalPayloadProtocol", "TerminalPayloadHash", "TerminalPayload",
				"LeaseOwner", "LeaseUntilMs", "Attempts", "NextRetryTimeMs", "LastError", "CreatedTimeMs",
				"UpdatedTimeMs", "CompletedTimeMs", "LogState", "LogPayloadProtocol", "LogPayloadHash",
				"LogPayload", "LogAttempts", "LogLastError", "LogUpdatedTimeMs", "LogLeaseOwner",
				"LogLeaseUntilMs", "LogNextRetryMs", "UsageUserOutcome", "UsageChannelOutcome", "UsageWarning",
			},
			indexes: map[string][]string{
				"uidx_task_billing_operation_task": {"task_id"},
				"uidx_task_billing_operation_key":  {"operation_key"},
			},
		},
		{
			table: "midjourney_billing_operations",
			model: &MidjourneyBillingOperation{},
			columns: []string{
				"MidjourneyID", "ReservationID", "OperationKey", "TerminalStatus", "Kind", "State", "UserID", "ChannelID",
				"BillingSource", "SubscriptionID", "TokenID", "RefundQuota", "ModelName", "Group", "Reason",
				"TerminalPayloadProtocol", "TerminalPayloadHash", "TerminalPayload",
				"LeaseOwner", "LeaseUntilMs", "Attempts", "NextRetryTimeMs", "LastError", "CreatedTimeMs",
				"UpdatedTimeMs", "CompletedTimeMs", "LogState", "LogPayloadProtocol", "LogPayloadHash",
				"LogPayload", "LogAttempts", "LogLastError", "LogUpdatedTimeMs", "LogLeaseOwner",
				"LogLeaseUntilMs", "LogNextRetryMs", "UsageUserOutcome", "UsageChannelOutcome", "UsageWarning",
			},
			indexes: map[string][]string{
				"uidx_midjourney_billing_operation_task": {"midjourney_id"},
				"uidx_midjourney_billing_operation_key":  {"operation_key"},
			},
		},
		{
			table: "subscription_billing_periods",
			model: &SubscriptionBillingPeriod{},
			columns: []string{
				"SubscriptionID", "PeriodSequence", "UserID", "PeriodStart", "PeriodEnd", "AmountTotal",
				"AmountUsed", "ClosedTime", "CloseReason", "CreatedTime", "UpdatedTime",
			},
			indexes: map[string][]string{
				"uidx_subscription_billing_period": {"subscription_id", "period_sequence"},
			},
		},
	}
	for _, requirement := range requirements {
		if !db.Migrator().HasTable(requirement.model) {
			return false, nil
		}
		for _, column := range requirement.columns {
			if !db.Migrator().HasColumn(requirement.model, column) {
				return false, nil
			}
		}
		for name, expectedColumns := range requirement.indexes {
			ready, err := asyncBillingUniqueIndexReady(db, requirement.table, name, expectedColumns)
			if err != nil {
				return false, err
			}
			if !ready {
				return false, nil
			}
		}
	}
	statsReady, err := billingStatsProjectionSchemaReady(db)
	if err != nil || !statsReady {
		return false, err
	}
	logsReady, err := billingLogProjectionSchemaReady(db)
	if err != nil || !logsReady {
		return false, err
	}
	return true, nil
}

// ensureAsyncBillingManualResolutionUniqueIndex serializes the legacy index
// replacement across application instances. PostgreSQL and SQLite keep the
// replacement transactional; MySQL holds a connection-scoped named lock and
// uses a shadow unique index because its DDL implicitly commits.
func ensureAsyncBillingManualResolutionUniqueIndex(db *gorm.DB) error {
	if db == nil {
		return errors.New("async billing database is unavailable")
	}
	switch common.MainDatabaseType() {
	case common.DatabaseTypeSQLite:
		return db.Connection(func(conn *gorm.DB) (resultErr error) {
			if err := conn.Exec("BEGIN IMMEDIATE").Error; err != nil {
				return fmt.Errorf("%w: acquire SQLite manual resolution index migration lock: %v",
					ErrAsyncBillingSchemaNotReady, err)
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					_ = conn.Exec("ROLLBACK").Error
					panic(recovered)
				}
				if resultErr != nil {
					if rollbackErr := conn.Exec("ROLLBACK").Error; rollbackErr != nil {
						resultErr = errors.Join(resultErr, fmt.Errorf(
							"%w: roll back SQLite manual resolution index migration: %v",
							ErrAsyncBillingSchemaNotReady, rollbackErr))
					}
					return
				}
				if commitErr := conn.Exec("COMMIT").Error; commitErr != nil {
					resultErr = fmt.Errorf("%w: commit SQLite manual resolution index migration: %v",
						ErrAsyncBillingSchemaNotReady, commitErr)
					if rollbackErr := conn.Exec("ROLLBACK").Error; rollbackErr != nil {
						resultErr = errors.Join(resultErr, fmt.Errorf(
							"%w: roll back failed SQLite manual resolution index migration: %v",
							ErrAsyncBillingSchemaNotReady, rollbackErr))
					}
				}
			}()
			return migrateAsyncBillingManualResolutionUniqueIndex(conn)
		})
	case common.DatabaseTypeMySQL:
		return db.Connection(func(conn *gorm.DB) (resultErr error) {
			var acquired sql.NullInt64
			if err := conn.Raw(`SELECT GET_LOCK(
	SHA2(CONCAT(DATABASE(), ?), 256), ?
)`, asyncBillingManualResolutionIndexLock, asyncBillingManualResolutionIndexWait).
				Row().Scan(&acquired); err != nil {
				return fmt.Errorf("%w: acquire MySQL manual resolution index migration lock: %v",
					ErrAsyncBillingSchemaNotReady, err)
			}
			if !acquired.Valid || acquired.Int64 != 1 {
				return fmt.Errorf("%w: timed out acquiring MySQL manual resolution index migration lock",
					ErrAsyncBillingSchemaNotReady)
			}
			defer func() {
				var released sql.NullInt64
				releaseErr := conn.Raw(`SELECT RELEASE_LOCK(
	SHA2(CONCAT(DATABASE(), ?), 256)
)`, asyncBillingManualResolutionIndexLock).Row().Scan(&released)
				if releaseErr == nil && (!released.Valid || released.Int64 != 1) {
					releaseErr = errors.New("lock was not owned by the migration connection")
				}
				if releaseErr != nil {
					wrapped := fmt.Errorf("%w: release MySQL manual resolution index migration lock: %v",
						ErrAsyncBillingSchemaNotReady, releaseErr)
					if resultErr == nil {
						resultErr = wrapped
					} else {
						resultErr = errors.Join(resultErr, wrapped)
					}
				}
			}()
			return migrateAsyncBillingManualResolutionUniqueIndexMySQL(conn)
		})
	case common.DatabaseTypePostgreSQL:
		return ensureAsyncBillingManualResolutionUniqueIndexPostgreSQL(
			db, time.Duration(asyncBillingManualResolutionIndexWait)*time.Second,
		)
	default:
		return fmt.Errorf("%w: unsupported main database type %q",
			ErrAsyncBillingSchemaNotReady, common.MainDatabaseType())
	}
}

func ensureAsyncBillingManualResolutionUniqueIndexPostgreSQL(db *gorm.DB, maxWait time.Duration) error {
	if db == nil || maxWait <= 0 {
		return fmt.Errorf("%w: invalid PostgreSQL manual resolution index migration timeout",
			ErrAsyncBillingSchemaNotReady)
	}
	baseCtx := context.Background()
	if db.Statement != nil && db.Statement.Context != nil {
		baseCtx = db.Statement.Context
	}
	ctx, cancel := context.WithTimeout(baseCtx, maxWait)
	defer cancel()
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(
	hashtext(current_database() || ':' || current_schema()), hashtext(?)
)`, asyncBillingManualResolutionIndexLock).Error; err != nil {
			return fmt.Errorf("%w: acquire PostgreSQL manual resolution index migration lock: %v",
				ErrAsyncBillingSchemaNotReady, err)
		}
		return migrateAsyncBillingManualResolutionUniqueIndex(tx)
	})
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w: PostgreSQL manual resolution index migration timed out or was canceled: %v",
			ErrAsyncBillingSchemaNotReady, ctxErr)
	}
	if errors.Is(err, ErrAsyncBillingSchemaNotReady) {
		return err
	}
	return fmt.Errorf("%w: PostgreSQL manual resolution index migration: %v",
		ErrAsyncBillingSchemaNotReady, err)
}

// migrateAsyncBillingManualResolutionUniqueIndexMySQL builds the target
// definition under a shadow name before removing the legacy index. Renaming
// the shadow keeps the uniqueness invariant intact even if the process stops
// between MySQL's implicitly committed DDL statements.
func migrateAsyncBillingManualResolutionUniqueIndexMySQL(db *gorm.DB) error {
	db = db.Session(&gorm.Session{NewDB: true})
	if !db.Migrator().HasTable(&AsyncBillingManualResolution{}) {
		return ErrAsyncBillingSchemaNotReady
	}
	targetReady, err := asyncBillingUniqueIndexReady(
		db,
		"async_billing_manual_resolutions",
		asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	if err != nil {
		return fmt.Errorf("%w: inspect MySQL manual resolution index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	if targetReady {
		return dropAsyncBillingManualResolutionMySQLGuardIndex(db)
	}

	guardReady, err := asyncBillingUniqueIndexReady(
		db,
		"async_billing_manual_resolutions",
		asyncBillingManualResolutionGuardIndex,
		[]string{"reservation_id", "expected_version"},
	)
	if err != nil {
		return fmt.Errorf("%w: inspect MySQL manual resolution guard index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	if db.Migrator().HasIndex(
		&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
	) {
		if !guardReady {
			return fmt.Errorf("%w: unexpected definition for %s",
				ErrAsyncBillingSchemaNotReady, asyncBillingManualResolutionGuardIndex)
		}
	} else {
		if err := db.Migrator().CreateIndex(
			&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
		); err != nil {
			return fmt.Errorf("%w: create MySQL manual resolution guard index: %v",
				ErrAsyncBillingSchemaNotReady, err)
		}
		guardReady, err = asyncBillingUniqueIndexReady(
			db,
			"async_billing_manual_resolutions",
			asyncBillingManualResolutionGuardIndex,
			[]string{"reservation_id", "expected_version"},
		)
		if err != nil {
			return fmt.Errorf("%w: inspect created MySQL manual resolution guard index: %v",
				ErrAsyncBillingSchemaNotReady, err)
		}
		if !guardReady {
			return fmt.Errorf("%w: created MySQL manual resolution guard index has an unexpected definition",
				ErrAsyncBillingSchemaNotReady)
		}
	}

	// Re-read the target after the potentially long shadow-index build. A
	// concurrently finishing AutoMigrate may already have installed it.
	targetReady, err = asyncBillingUniqueIndexReady(
		db,
		"async_billing_manual_resolutions",
		asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	if err != nil {
		return fmt.Errorf("%w: re-inspect MySQL manual resolution index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	if targetReady {
		return dropAsyncBillingManualResolutionMySQLGuardIndex(db)
	}
	if db.Migrator().HasIndex(&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex) {
		legacyReady, inspectErr := asyncBillingUniqueIndexReady(
			db,
			"async_billing_manual_resolutions",
			asyncBillingManualResolutionUniqueIndex,
			[]string{"reservation_id"},
		)
		if inspectErr != nil {
			return fmt.Errorf("%w: inspect legacy MySQL manual resolution index: %v",
				ErrAsyncBillingSchemaNotReady, inspectErr)
		}
		if !legacyReady {
			return fmt.Errorf("%w: unexpected definition for %s",
				ErrAsyncBillingSchemaNotReady, asyncBillingManualResolutionUniqueIndex)
		}
		if err := db.Migrator().DropIndex(
			&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
		); err != nil {
			return fmt.Errorf("%w: drop legacy MySQL manual resolution index: %v",
				ErrAsyncBillingSchemaNotReady, err)
		}
	}
	if err := db.Migrator().RenameIndex(
		&asyncBillingManualResolutionIndexGuard{},
		asyncBillingManualResolutionGuardIndex,
		asyncBillingManualResolutionUniqueIndex,
	); err != nil {
		return fmt.Errorf("%w: promote MySQL manual resolution guard index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	targetReady, err = asyncBillingUniqueIndexReady(
		db,
		"async_billing_manual_resolutions",
		asyncBillingManualResolutionUniqueIndex,
		[]string{"reservation_id", "expected_version"},
	)
	if err != nil {
		return fmt.Errorf("%w: inspect promoted MySQL manual resolution index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	if !targetReady {
		return fmt.Errorf("%w: promoted MySQL manual resolution index has an unexpected definition",
			ErrAsyncBillingSchemaNotReady)
	}
	return nil
}

func dropAsyncBillingManualResolutionMySQLGuardIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(
		&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
	) {
		return nil
	}
	guardReady, err := asyncBillingUniqueIndexReady(
		db,
		"async_billing_manual_resolutions",
		asyncBillingManualResolutionGuardIndex,
		[]string{"reservation_id", "expected_version"},
	)
	if err != nil {
		return fmt.Errorf("%w: inspect leftover MySQL manual resolution guard index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	if !guardReady {
		return fmt.Errorf("%w: unexpected definition for %s",
			ErrAsyncBillingSchemaNotReady, asyncBillingManualResolutionGuardIndex)
	}
	if err := db.Migrator().DropIndex(
		&asyncBillingManualResolutionIndexGuard{}, asyncBillingManualResolutionGuardIndex,
	); err != nil {
		return fmt.Errorf("%w: drop leftover MySQL manual resolution guard index: %v",
			ErrAsyncBillingSchemaNotReady, err)
	}
	return nil
}

// migrateAsyncBillingManualResolutionUniqueIndex upgrades the v0.1.11
// reservation-only receipt key while the database-specific migration lock is
// held. Unknown definitions fail closed because they may encode constraints
// from a newer binary.
func migrateAsyncBillingManualResolutionUniqueIndex(db *gorm.DB) error {
	db = db.Session(&gorm.Session{NewDB: true})
	if !db.Migrator().HasTable(&AsyncBillingManualResolution{}) {
		return ErrAsyncBillingSchemaNotReady
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		targetReady, err := asyncBillingUniqueIndexReady(
			db,
			"async_billing_manual_resolutions",
			asyncBillingManualResolutionUniqueIndex,
			[]string{"reservation_id", "expected_version"},
		)
		if err != nil {
			lastErr = fmt.Errorf("inspect async billing manual resolution index: %w", err)
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if targetReady {
			return nil
		}
		hasNamedIndex := db.Migrator().HasIndex(
			&AsyncBillingManualResolution{},
			asyncBillingManualResolutionUniqueIndex,
		)
		if hasNamedIndex {
			legacyReady, inspectErr := asyncBillingUniqueIndexReady(
				db,
				"async_billing_manual_resolutions",
				asyncBillingManualResolutionUniqueIndex,
				[]string{"reservation_id"},
			)
			if inspectErr != nil {
				lastErr = fmt.Errorf("inspect legacy async billing manual resolution index: %w", inspectErr)
				time.Sleep(25 * time.Millisecond)
				continue
			}
			if !legacyReady {
				lastErr = fmt.Errorf("unexpected definition for %s", asyncBillingManualResolutionUniqueIndex)
				time.Sleep(25 * time.Millisecond)
				continue
			}
			if err := db.Migrator().DropIndex(
				&AsyncBillingManualResolution{},
				asyncBillingManualResolutionUniqueIndex,
			); err != nil {
				lastErr = fmt.Errorf("drop legacy async billing manual resolution index: %w", err)
				time.Sleep(25 * time.Millisecond)
				continue
			}
		}
		if err := db.Migrator().CreateIndex(
			&AsyncBillingManualResolution{},
			asyncBillingManualResolutionUniqueIndex,
		); err != nil {
			lastErr = fmt.Errorf("create async billing manual resolution index: %w", err)
			time.Sleep(25 * time.Millisecond)
			continue
		}
		createdReady, inspectErr := asyncBillingUniqueIndexReady(
			db,
			"async_billing_manual_resolutions",
			asyncBillingManualResolutionUniqueIndex,
			[]string{"reservation_id", "expected_version"},
		)
		if inspectErr != nil {
			lastErr = fmt.Errorf("inspect created async billing manual resolution index: %w", inspectErr)
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if createdReady {
			return nil
		}
		lastErr = fmt.Errorf("created async billing manual resolution index has an unexpected definition")
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("%w: manual resolution index migration did not converge: %v",
			ErrAsyncBillingSchemaNotReady, lastErr)
	}
	return fmt.Errorf("%w: manual resolution index migration did not converge", ErrAsyncBillingSchemaNotReady)
}

func asyncBillingUniqueIndexReady(db *gorm.DB, tableName, indexName string, expectedColumns []string) (bool, error) {
	if db == nil || tableName == "" || indexName == "" || len(expectedColumns) == 0 {
		return false, errors.New("async billing index requirement is invalid")
	}
	var columns []struct {
		Name         string `gorm:"column:name"`
		PrefixLength *int   `gorm:"column:prefix_length"`
	}
	switch common.MainDatabaseType() {
	case common.DatabaseTypeSQLite:
		var count int64
		if err := db.Raw(fmt.Sprintf(`SELECT count(*) FROM pragma_index_list('%s')
WHERE name = ? AND "unique" = 1 AND partial = 0`, tableName), indexName).Scan(&count).Error; err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
		if err := db.Raw(fmt.Sprintf("SELECT name FROM pragma_index_info('%s') ORDER BY seqno", indexName)).
			Scan(&columns).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypeMySQL:
		if err := db.Raw(`SELECT column_name AS name, sub_part AS prefix_length FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? AND non_unique = 0
ORDER BY seq_in_index`, tableName, indexName).Scan(&columns).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypePostgreSQL:
		if err := db.Raw(`SELECT attribute.attname AS name
FROM pg_catalog.pg_class AS table_class
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = table_class.relnamespace
JOIN pg_catalog.pg_index AS index_meta ON index_meta.indrelid = table_class.oid
JOIN pg_catalog.pg_class AS index_class ON index_class.oid = index_meta.indexrelid
JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = table_class.oid
	AND attribute.attnum = ANY(index_meta.indkey::smallint[])
WHERE namespace.nspname = current_schema() AND table_class.relname = ?
	AND index_class.relname = ? AND index_meta.indisunique = TRUE
	AND index_meta.indisvalid = TRUE AND index_meta.indisready = TRUE
	AND index_meta.indpred IS NULL AND index_meta.indexprs IS NULL AND index_meta.indnatts = ?
ORDER BY array_position(index_meta.indkey::smallint[], attribute.attnum)`,
			tableName, indexName, len(expectedColumns)).Scan(&columns).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported main database type %q", common.MainDatabaseType())
	}
	actualColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		if column.PrefixLength != nil {
			return false, nil
		}
		actualColumns = append(actualColumns, column.Name)
	}
	return slices.Equal(actualColumns, expectedColumns), nil
}

func WaitAsyncBillingV2SchemaReady(ctx context.Context, db *gorm.DB, maxWait time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxWait < 0 {
		return errors.New("async billing schema wait timeout must be non-negative")
	}
	if db == nil {
		return errors.New("async billing database is unavailable")
	}
	deadline := time.Now().Add(maxWait)
	for {
		ready, err := AsyncBillingV2SchemaReady(db.WithContext(ctx))
		if err == nil && ready {
			return nil
		}
		if maxWait == 0 || !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("async billing schema readiness check failed: %w", err)
			}
			return ErrAsyncBillingSchemaNotReady
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitAsyncBillingV2SchemaReadyFromEnv() error {
	waitSeconds := common.GetEnvOrDefault(asyncBillingSchemaWaitEnv, asyncBillingSchemaWaitDefault)
	if waitSeconds < 0 {
		return fmt.Errorf("%s must be non-negative", asyncBillingSchemaWaitEnv)
	}
	return WaitAsyncBillingV2SchemaReady(
		context.Background(), DB, time.Duration(waitSeconds)*time.Second,
	)
}
