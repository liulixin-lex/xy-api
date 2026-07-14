package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	billingStatsProjectionTableName         = "billing_stats_projections"
	billingStatsProjectionOperationIndex    = "uidx_billing_stats_projection_operation"
	billingStatsProjectionSourceIndex       = "uidx_billing_stats_projection_source"
	billingStatsProjectionSchemaWaitEnv     = "BILLING_STATS_PROJECTION_SCHEMA_READY_WAIT_SECONDS"
	billingStatsProjectionSchemaWaitDefault = 60
)

func billingStatsProjectionSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("billing stats projection database is unavailable")
	}
	requiredColumns := []string{
		"OperationKey", "ProtocolVersion", "Kind", "ReferenceID", "UserID", "ChannelID",
		"QuotaDelta", "RequestDelta", "DataExportRequired", "DataExportUsername", "DataExportModelName",
		"DataExportCreatedAt", "DataExportTokenUsed", "DataExportUseGroup", "DataExportTokenID",
		"DataExportNodeName", "DataExportOutcome", "State",
		"LeaseOwner", "LeaseUntilMs", "Attempts", "NextRetryMs", "LastError", "FailureCode",
		"UserOutcome", "ChannelOutcome", "CreatedTimeMs", "UpdatedTimeMs", "CompletedTimeMs",
	}
	for _, column := range requiredColumns {
		if !db.Migrator().HasColumn(&BillingStatsProjection{}, column) {
			return false, nil
		}
	}

	var notNullCount int64
	var uniqueIndexCount int64
	switch common.MainDatabaseType() {
	case common.DatabaseTypeSQLite:
		if err := db.Raw(`SELECT count(*) FROM pragma_table_info('billing_stats_projections')
WHERE name = 'operation_key' AND "notnull" = 1`).Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pragma_index_list('billing_stats_projections')
WHERE name = ? AND "unique" = 1`, billingStatsProjectionOperationIndex).
			Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypeMySQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
	AND column_name = 'operation_key' AND is_nullable = 'NO'`, billingStatsProjectionTableName).
			Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(DISTINCT index_name) FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ?
	AND index_name = ? AND non_unique = 0`, billingStatsProjectionTableName, billingStatsProjectionOperationIndex).
			Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypePostgreSQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ?
	AND column_name = 'operation_key' AND is_nullable = 'NO'`, billingStatsProjectionTableName).
			Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = ?
	AND indexname = ? AND upper(indexdef) LIKE 'CREATE UNIQUE INDEX%'`,
			billingStatsProjectionTableName, billingStatsProjectionOperationIndex).Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported main database type %q", common.MainDatabaseType())
	}
	indexReady, err := billingUniqueReceiptIndexReady(
		db, common.MainDatabaseType(), billingStatsProjectionTableName,
		billingStatsProjectionOperationIndex, "operation_key",
	)
	if err != nil {
		return false, err
	}
	sourceIndexReady, err := asyncBillingUniqueIndexReady(
		db, billingStatsProjectionTableName, billingStatsProjectionSourceIndex,
		[]string{"kind", "reference_id"},
	)
	if err != nil {
		return false, err
	}
	return notNullCount == 1 && uniqueIndexCount == 1 && indexReady && sourceIndexReady, nil
}

func waitBillingStatsProjectionSchemaReady(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout < 0 {
		return errors.New("billing stats projection schema wait timeout must be non-negative")
	}
	deadline := time.Now().Add(timeout)
	for {
		ready, err := billingStatsProjectionSchemaReady(db)
		if err == nil && ready {
			return nil
		}
		if timeout == 0 || !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("billing stats projection schema readiness check failed: %w", err)
			}
			return errors.New("billing stats projection schema is not ready")
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

func waitBillingStatsProjectionSchemaReadyFromEnv() error {
	waitSeconds := common.GetEnvOrDefault(
		billingStatsProjectionSchemaWaitEnv,
		billingStatsProjectionSchemaWaitDefault,
	)
	if waitSeconds < 0 {
		return fmt.Errorf("%s must be non-negative", billingStatsProjectionSchemaWaitEnv)
	}
	if err := waitBillingStatsProjectionSchemaReady(
		context.Background(), DB, time.Duration(waitSeconds)*time.Second,
	); err != nil {
		return fmt.Errorf("wait for billing stats projection schema: %w", err)
	}
	return nil
}
