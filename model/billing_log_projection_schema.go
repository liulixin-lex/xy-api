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
	billingLogProjectionTableName         = "billing_log_projections"
	billingLogProjectionOperationIndex    = "uidx_billing_log_projection_operation"
	billingLogProjectionSourceIndex       = "uidx_billing_log_projection_source"
	billingLogProjectionSchemaWaitEnv     = "BILLING_LOG_PROJECTION_SCHEMA_READY_WAIT_SECONDS"
	billingLogProjectionSchemaWaitDefault = 60
)

func billingLogProjectionSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("billing log projection database is unavailable")
	}
	requiredColumns := []string{
		"OperationKey", "ProtocolVersion", "Kind", "ReferenceID", "Required", "Disposition",
		"PayloadProtocol", "PayloadHash", "Payload", "State", "LeaseOwner", "LeaseUntilMs",
		"Attempts", "NextRetryMs", "LastError", "FailureCode", "Outcome",
		"CreatedTimeMs", "UpdatedTimeMs", "CompletedTimeMs",
	}
	for _, column := range requiredColumns {
		if !db.Migrator().HasColumn(&BillingLogProjection{}, column) {
			return false, nil
		}
	}

	var notNullCount int64
	var uniqueIndexCount int64
	switch common.MainDatabaseType() {
	case common.DatabaseTypeSQLite:
		if err := db.Raw(`SELECT count(*) FROM pragma_table_info('billing_log_projections')
WHERE name = 'operation_key' AND "notnull" = 1`).Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pragma_index_list('billing_log_projections')
WHERE name = ? AND "unique" = 1`, billingLogProjectionOperationIndex).
			Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypeMySQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
	AND column_name = 'operation_key' AND is_nullable = 'NO'`, billingLogProjectionTableName).
			Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(DISTINCT index_name) FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ?
	AND index_name = ? AND non_unique = 0`, billingLogProjectionTableName, billingLogProjectionOperationIndex).
			Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	case common.DatabaseTypePostgreSQL:
		if err := db.Raw(`SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ?
	AND column_name = 'operation_key' AND is_nullable = 'NO'`, billingLogProjectionTableName).
			Scan(&notNullCount).Error; err != nil {
			return false, err
		}
		if err := db.Raw(`SELECT count(*) FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = ?
	AND indexname = ? AND upper(indexdef) LIKE 'CREATE UNIQUE INDEX%'`,
			billingLogProjectionTableName, billingLogProjectionOperationIndex).Scan(&uniqueIndexCount).Error; err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported main database type %q", common.MainDatabaseType())
	}
	indexReady, err := billingUniqueReceiptIndexReady(
		db, common.MainDatabaseType(), billingLogProjectionTableName,
		billingLogProjectionOperationIndex, "operation_key",
	)
	if err != nil {
		return false, err
	}
	sourceIndexReady, err := asyncBillingUniqueIndexReady(
		db, billingLogProjectionTableName, billingLogProjectionSourceIndex,
		[]string{"kind", "reference_id"},
	)
	if err != nil {
		return false, err
	}
	return notNullCount == 1 && uniqueIndexCount == 1 && indexReady && sourceIndexReady, nil
}

func waitBillingLogProjectionSchemaReady(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout < 0 {
		return errors.New("billing log projection schema wait timeout must be non-negative")
	}
	deadline := time.Now().Add(timeout)
	for {
		ready, err := billingLogProjectionSchemaReady(db)
		if err == nil && ready {
			return nil
		}
		if timeout == 0 || !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("billing log projection schema readiness check failed: %w", err)
			}
			return errors.New("billing log projection schema is not ready")
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

func waitBillingLogProjectionSchemaReadyFromEnv() error {
	waitSeconds := common.GetEnvOrDefault(
		billingLogProjectionSchemaWaitEnv,
		billingLogProjectionSchemaWaitDefault,
	)
	if waitSeconds < 0 {
		return fmt.Errorf("%s must be non-negative", billingLogProjectionSchemaWaitEnv)
	}
	if err := waitBillingLogProjectionSchemaReady(
		context.Background(), DB, time.Duration(waitSeconds)*time.Second,
	); err != nil {
		return fmt.Errorf("wait for billing log projection schema: %w", err)
	}
	return nil
}
