package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	routingV2SchemaComponent    = "channel-routing-v2"
	routingV2SchemaVersion      = "channel-routing-v2-phase5-20260712.2"
	routingV2SchemaPollInterval = 100 * time.Millisecond
)

var ErrRoutingV2SchemaNotReady = errors.New("channel routing v2 schema is not ready")

type RoutingSchemaVersion struct {
	Component     string `json:"component" gorm:"type:varchar(64);primaryKey"`
	Version       string `json:"version" gorm:"type:varchar(128);index;not null"`
	UpdatedTimeMs int64  `json:"updated_time_ms" gorm:"bigint;not null"`
}

func (RoutingSchemaVersion) TableName() string {
	return "routing_schema_versions"
}

func routingV2RequiredSchemaModels() []any {
	return []any{
		&RoutingSchemaVersion{},
		&RoutingTopologyMetadata{},
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
		&RoutingUpstreamAccount{},
		&RoutingCostSnapshotVersion{},
		&RoutingChannelBinding{},
		&RoutingCostSnapshot{},
		&RoutingChannelMetric{},
		&RoutingMetricRollup{},
		&RoutingTelemetryReceipt{},
		&RoutingBreakerState{},
		&RoutingChannelHealthState{},
		&RoutingAgentRecommendation{},
		&RoutingErrorBudgetState{},
		&RoutingErrorBudgetHistory{},
		&RoutingErrorBudgetCursor{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
	}
}

func invalidateRoutingV2SchemaVersion(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingV2SchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return nil
	}
	return db.Where("component = ?", routingV2SchemaComponent).Delete(&RoutingSchemaVersion{}).Error
}

func publishRoutingV2SchemaVersion(db *gorm.DB) error {
	ready, err := routingV2PhysicalSchemaReady(db)
	if err != nil {
		return err
	}
	if !ready {
		return ErrRoutingV2SchemaNotReady
	}
	nowMs, err := routingDatabaseNowMs(db)
	if err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "component"}},
		DoUpdates: clause.Assignments(map[string]any{
			"version": routingV2SchemaVersion, "updated_time_ms": nowMs,
		}),
	}).Create(&RoutingSchemaVersion{
		Component: routingV2SchemaComponent, Version: routingV2SchemaVersion, UpdatedTimeMs: nowMs,
	}).Error
}

func RoutingV2SchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingV2SchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return false, nil
	}
	var marker RoutingSchemaVersion
	err := db.Select("component", "version").Where("component = ?", routingV2SchemaComponent).First(&marker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.Version != routingV2SchemaVersion {
		return false, nil
	}
	return routingV2PhysicalSchemaReady(db)
}

func WaitRoutingV2SchemaReady(ctx context.Context, db *gorm.DB, maxWait time.Duration) error {
	if db == nil || db.Dialector == nil || maxWait < 0 {
		return ErrRoutingV2SchemaNotReady
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	var lastErr error
	for {
		ready, err := RoutingV2SchemaReady(db.WithContext(ctx))
		if ready {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if maxWait == 0 {
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrRoutingV2SchemaNotReady, lastErr)
			}
			return ErrRoutingV2SchemaNotReady
		}

		poll := time.NewTimer(routingV2SchemaPollInterval)
		select {
		case <-ctx.Done():
			if !poll.Stop() {
				<-poll.C
			}
			return ctx.Err()
		case <-deadline.C:
			if !poll.Stop() {
				<-poll.C
			}
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrRoutingV2SchemaNotReady, lastErr)
			}
			return ErrRoutingV2SchemaNotReady
		case <-poll.C:
		}
	}
}

func routingV2PhysicalSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingV2SchemaNotReady
	}
	for _, requiredModel := range routingV2RequiredSchemaModels() {
		ready, err := routingV2ModelSchemaReady(db, requiredModel)
		if err != nil || !ready {
			return ready, err
		}
	}
	rollupReady, err := RoutingMetricRollupRevisionKeySchemaReady(db)
	if err != nil || !rollupReady {
		return rollupReady, err
	}
	errorBudgetReady, err := RoutingErrorBudgetSchemaReady(db)
	if err != nil || !errorBudgetReady {
		return errorBudgetReady, err
	}
	operationIndexReady, err := routingV2CriticalIndexReady(
		db,
		(RoutingOperation{}).TableName(),
		routingOperationRequestKeyUniqueIndex,
		[]string{"request_key_hash"},
	)
	if err != nil || !operationIndexReady {
		return operationIndexReady, err
	}
	canaryIndexReady, err := routingV2CriticalIndexReady(
		db,
		(RoutingCanaryEvaluation{}).TableName(),
		routingCanaryEvaluationWindowUniqueIndex,
		[]string{"rollout_key", "pool_id", "window_start_ms", "window_end_ms"},
	)
	if err != nil || !canaryIndexReady {
		return canaryIndexReady, err
	}
	approvalIndexReady, err := routingV2CriticalIndexReady(
		db,
		(RoutingPolicyApproval{}).TableName(),
		routingPolicyApprovalIntentActorUniqueIndex,
		[]string{"draft_id", "draft_version", "activation_hash", "actor_id"},
	)
	if err != nil || !approvalIndexReady {
		return approvalIndexReady, err
	}
	rollbackApprovalIndexReady, err := routingV2CriticalIndexReady(
		db,
		(RoutingPolicyRollbackApproval{}).TableName(),
		routingPolicyRollbackApprovalIntentActorUniqueIndex,
		[]string{"expected_revision", "expected_activation_id", "target_revision", "activation_hash", "actor_id"},
	)
	if err != nil || !rollbackApprovalIndexReady {
		return rollbackApprovalIndexReady, err
	}
	return true, nil
}

func routingV2ModelSchemaReady(db *gorm.DB, value any) (bool, error) {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(value); err != nil {
		return false, err
	}
	if statement.Schema == nil || !db.Migrator().HasTable(value) {
		return false, nil
	}
	for _, field := range statement.Schema.Fields {
		if field.DBName == "" || field.IgnoreMigration {
			continue
		}
		if !db.Migrator().HasColumn(value, field.DBName) {
			return false, nil
		}
	}
	for indexName := range statement.Schema.ParseIndexes() {
		if !db.Migrator().HasIndex(value, indexName) {
			return false, nil
		}
	}
	return true, nil
}

func routingV2CriticalIndexReady(
	db *gorm.DB,
	tableName string,
	indexName string,
	expectedColumns []string,
) (bool, error) {
	columns, unique, exists, err := routingV2CriticalIndexDefinition(db, tableName, indexName)
	if err != nil {
		return false, err
	}
	return exists && unique && routingV2IndexColumnsEqual(columns, expectedColumns), nil
}

func routingV2CriticalIndexDefinition(
	db *gorm.DB,
	tableName string,
	indexName string,
) ([]string, bool, bool, error) {
	allowed := tableName == (RoutingOperation{}).TableName() && indexName == routingOperationRequestKeyUniqueIndex ||
		tableName == (RoutingCanaryEvaluation{}).TableName() && indexName == routingCanaryEvaluationWindowUniqueIndex ||
		tableName == (RoutingPolicyApproval{}).TableName() && indexName == routingPolicyApprovalIntentActorUniqueIndex ||
		tableName == (RoutingPolicyRollbackApproval{}).TableName() &&
			indexName == routingPolicyRollbackApprovalIntentActorUniqueIndex
	if db == nil || db.Dialector == nil || !allowed {
		return nil, false, false, ErrRoutingV2SchemaNotReady
	}

	switch db.Dialector.Name() {
	case "sqlite":
		var indexes []struct {
			Name   string `gorm:"column:name"`
			Unique int    `gorm:"column:unique"`
		}
		if err := db.Raw("PRAGMA index_list('" + tableName + "')").Scan(&indexes).Error; err != nil {
			return nil, false, false, err
		}
		for _, index := range indexes {
			if index.Name != indexName {
				continue
			}
			var columns []struct {
				Sequence int    `gorm:"column:seqno"`
				Name     string `gorm:"column:name"`
			}
			if err := db.Raw("PRAGMA index_info('" + indexName + "')").Scan(&columns).Error; err != nil {
				return nil, false, false, err
			}
			sort.Slice(columns, func(left int, right int) bool {
				return columns[left].Sequence < columns[right].Sequence
			})
			names := make([]string, 0, len(columns))
			for _, column := range columns {
				names = append(names, column.Name)
			}
			return names, index.Unique == 1, true, nil
		}
		return nil, false, false, nil
	case "mysql":
		var rows []struct {
			ColumnName string `gorm:"column:column_name"`
			NonUnique  int    `gorm:"column:non_unique"`
		}
		if err := db.Raw(
			"SELECT COLUMN_NAME AS column_name, NON_UNIQUE AS non_unique "+
				"FROM information_schema.STATISTICS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? "+
				"ORDER BY SEQ_IN_INDEX",
			tableName, indexName,
		).Scan(&rows).Error; err != nil {
			return nil, false, false, err
		}
		if len(rows) == 0 {
			return nil, false, false, nil
		}
		names := make([]string, 0, len(rows))
		unique := true
		for _, row := range rows {
			names = append(names, row.ColumnName)
			unique = unique && row.NonUnique == 0
		}
		return names, unique, true, nil
	case "postgres":
		var rows []struct {
			ColumnName string `gorm:"column:column_name"`
			IsUnique   bool   `gorm:"column:is_unique"`
		}
		if err := db.Raw(
			"SELECT pg_get_indexdef(index_meta.indexrelid, position.number, TRUE) AS column_name, "+
				"index_meta.indisunique AS is_unique "+
				"FROM pg_catalog.pg_index AS index_meta "+
				"JOIN pg_catalog.pg_class AS index_table ON index_table.oid = index_meta.indexrelid "+
				"JOIN pg_catalog.pg_class AS data_table ON data_table.oid = index_meta.indrelid "+
				"JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = data_table.relnamespace "+
				"JOIN LATERAL generate_series(1, index_meta.indnatts::integer) AS position(number) ON TRUE "+
				"WHERE namespace.nspname = current_schema() AND data_table.relname = ? AND index_table.relname = ? "+
				"ORDER BY position.number",
			tableName, indexName,
		).Scan(&rows).Error; err != nil {
			return nil, false, false, err
		}
		if len(rows) == 0 {
			return nil, false, false, nil
		}
		names := make([]string, 0, len(rows))
		unique := true
		for _, row := range rows {
			names = append(names, row.ColumnName)
			unique = unique && row.IsUnique
		}
		return names, unique, true, nil
	default:
		return nil, false, false, fmt.Errorf("unsupported routing schema database %q", db.Dialector.Name())
	}
}

func routingV2IndexColumnsEqual(actual []string, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		actualColumn := strings.Trim(strings.TrimSpace(actual[index]), "`\"")
		if !strings.EqualFold(actualColumn, expected[index]) {
			return false
		}
	}
	return true
}
