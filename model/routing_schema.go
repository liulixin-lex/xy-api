package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	routingSchemaComponent       = "channel-routing"
	routingLegacySchemaComponent = "channel-routing-v2"
	RoutingSchemaCurrentVersion  = "channel-routing-20260716.2"
	routingSchemaVersion         = RoutingSchemaCurrentVersion
	routingSchemaPollInterval    = 100 * time.Millisecond
	routingSchemaFleetLimit      = 512
)

var (
	ErrRoutingSchemaNotReady          = errors.New("channel routing schema is not ready")
	ErrRoutingSchemaFleetIncompatible = errors.New("channel routing schema cutover blocked by incompatible active instances")
)

type RoutingSchemaVersion struct {
	Component     string `json:"component" gorm:"type:varchar(64);primaryKey"`
	Version       string `json:"version" gorm:"type:varchar(128);index;not null"`
	UpdatedTimeMs int64  `json:"updated_time_ms" gorm:"bigint;not null"`
}

func (RoutingSchemaVersion) TableName() string {
	return "routing_schema_versions"
}

func preflightRoutingSchemaCutover(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if db.Dialector.Name() == "sqlite" {
		return nil
	}
	if !db.Migrator().HasTable(&SystemInstance{}) {
		return nil
	}
	nowMs, err := routingDatabaseNowMs(db)
	if err != nil {
		return err
	}
	return validateRoutingSchemaFleetForCutover(db, nowMs/1000)
}

func validateRoutingSchemaFleetForCutover(db *gorm.DB, now int64) error {
	if db == nil || db.Dialector == nil || now <= 0 {
		return ErrRoutingSchemaNotReady
	}
	var instances []SystemInstance
	if err := db.Select("node_name", "info", "last_seen_at").
		Where("last_seen_at >= ?", now-SystemInstanceStaleAfterSeconds).
		Order("node_name ASC").Limit(routingSchemaFleetLimit + 1).
		Find(&instances).Error; err != nil {
		return err
	}
	if len(instances) > routingSchemaFleetLimit {
		return fmt.Errorf(
			"%w: more than %d active instances were found; drain the fleet and retry",
			ErrRoutingSchemaFleetIncompatible, routingSchemaFleetLimit,
		)
	}

	incompatible := make([]string, 0)
	for _, instance := range instances {
		var info struct {
			Runtime struct {
				Version string `json:"version"`
			} `json:"runtime"`
			Capabilities struct {
				ChannelRoutingSchema string `json:"channel_routing_schema"`
			} `json:"capabilities"`
		}
		if err := common.UnmarshalJsonStr(instance.Info, &info); err == nil &&
			info.Capabilities.ChannelRoutingSchema == RoutingSchemaCurrentVersion {
			continue
		}

		appVersion := strings.TrimSpace(info.Runtime.Version)
		if appVersion == "" {
			appVersion = "unreported"
		}
		schemaVersion := strings.TrimSpace(info.Capabilities.ChannelRoutingSchema)
		if schemaVersion == "" {
			schemaVersion = "unreported"
		}
		incompatible = append(incompatible, fmt.Sprintf(
			"%q (app=%q, routing_schema=%q)", instance.NodeName, appVersion, schemaVersion,
		))
	}
	if len(incompatible) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: all active instances must report %q before migration; incompatible instances: %s. Stop or upgrade them, then wait at least %d seconds for stale heartbeats before retrying",
		ErrRoutingSchemaFleetIncompatible, RoutingSchemaCurrentVersion,
		strings.Join(incompatible, ", "), SystemInstanceStaleAfterSeconds,
	)
}

func routingRequiredSchemaModels() []any {
	return []any{
		&Channel{},
		&RoutingSchemaVersion{},
		&RoutingTopologyMetadata{},
		&RoutingChannelLifecycle{},
		&RoutingPool{},
		&RoutingPoolMember{},
		&RoutingCredentialRef{},
		&RoutingCredentialHealthState{},
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
		&RoutingMetricRollup{},
		&RoutingTelemetryReceipt{},
		&RoutingBreakerState{},
		&RoutingChannelHealthState{},
		&RoutingErrorBudgetState{},
		&RoutingErrorBudgetHistory{},
		&RoutingErrorBudgetCursor{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
		&TaskBillingOperation{},
		&MidjourneyBillingOperation{},
		&IdentityCacheSync{},
		&SubscriptionBillingPeriod{},
		&AsyncBillingReservation{},
		&AsyncBillingAttempt{},
		&AsyncBillingManualResolution{},
	}
}

func invalidateRoutingSchemaVersion(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return nil
	}
	return db.Where("component IN ?", []string{
		routingSchemaComponent,
		routingLegacySchemaComponent,
	}).Delete(&RoutingSchemaVersion{}).Error
}

func publishRoutingSchemaVersion(db *gorm.DB) error {
	ready, err := routingPhysicalSchemaReady(db)
	if err != nil {
		return err
	}
	if !ready {
		return ErrRoutingSchemaNotReady
	}
	nowMs, err := routingDatabaseNowMs(db)
	if err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "component"}},
		DoUpdates: clause.Assignments(map[string]any{
			"version": routingSchemaVersion, "updated_time_ms": nowMs,
		}),
	}).Create(&RoutingSchemaVersion{
		Component: routingSchemaComponent, Version: routingSchemaVersion, UpdatedTimeMs: nowMs,
	}).Error
}

func RoutingSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingSchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return false, nil
	}
	var marker RoutingSchemaVersion
	err := db.Select("component", "version").Where("component = ?", routingSchemaComponent).First(&marker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.Version != routingSchemaVersion {
		return false, nil
	}
	return routingPhysicalSchemaReady(db)
}

func WaitRoutingSchemaReady(ctx context.Context, db *gorm.DB, maxWait time.Duration) error {
	if db == nil || db.Dialector == nil || maxWait < 0 {
		return ErrRoutingSchemaNotReady
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
		ready, err := RoutingSchemaReady(db.WithContext(ctx))
		if ready {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if maxWait == 0 {
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrRoutingSchemaNotReady, lastErr)
			}
			return ErrRoutingSchemaNotReady
		}

		poll := time.NewTimer(routingSchemaPollInterval)
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
				return fmt.Errorf("%w: %v", ErrRoutingSchemaNotReady, lastErr)
			}
			return ErrRoutingSchemaNotReady
		case <-poll.C:
		}
	}
}

func routingPhysicalSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingSchemaNotReady
	}
	for _, requiredModel := range routingRequiredSchemaModels() {
		ready, err := routingModelSchemaReady(db, requiredModel)
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
	operationIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingOperation{}).TableName(),
		routingOperationRequestKeyUniqueIndex,
		[]string{"request_key_hash"},
	)
	if err != nil || !operationIndexReady {
		return operationIndexReady, err
	}
	memberGenerationIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingPoolMember{}).TableName(),
		routingPoolMemberGenerationUniqueIndex,
		[]string{"pool_id", "channel_generation"},
	)
	if err != nil || !memberGenerationIndexReady {
		return memberGenerationIndexReady, err
	}
	credentialGenerationIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingCredentialRef{}).TableName(),
		routingCredentialGenerationUniqueIndex,
		[]string{"channel_generation", "fingerprint"},
	)
	if err != nil || !credentialGenerationIndexReady {
		return credentialGenerationIndexReady, err
	}
	canaryIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingCanaryEvaluation{}).TableName(),
		routingCanaryEvaluationWindowUniqueIndex,
		[]string{"rollout_key", "pool_id", "window_start_ms", "window_end_ms"},
	)
	if err != nil || !canaryIndexReady {
		return canaryIndexReady, err
	}
	approvalIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingPolicyApproval{}).TableName(),
		routingPolicyApprovalIntentActorUniqueIndex,
		[]string{"draft_id", "draft_version", "activation_hash", "actor_id"},
	)
	if err != nil || !approvalIndexReady {
		return approvalIndexReady, err
	}
	rollbackApprovalIndexReady, err := routingCriticalIndexReady(
		db,
		(RoutingPolicyRollbackApproval{}).TableName(),
		routingPolicyRollbackApprovalIntentActorUniqueIndex,
		[]string{"expected_revision", "expected_activation_id", "target_revision", "activation_hash", "actor_id"},
	)
	if err != nil || !rollbackApprovalIndexReady {
		return rollbackApprovalIndexReady, err
	}
	return routingControlPlaneV2PreparedDataReady(db)
}

func routingModelSchemaReady(db *gorm.DB, value any) (bool, error) {
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

func routingCriticalIndexReady(
	db *gorm.DB,
	tableName string,
	indexName string,
	expectedColumns []string,
) (bool, error) {
	columns, unique, exists, err := routingCriticalIndexDefinition(db, tableName, indexName)
	if err != nil {
		return false, err
	}
	return exists && unique && routingIndexColumnsEqual(columns, expectedColumns), nil
}

func routingCriticalIndexDefinition(
	db *gorm.DB,
	tableName string,
	indexName string,
) ([]string, bool, bool, error) {
	allowed := tableName == (RoutingOperation{}).TableName() && indexName == routingOperationRequestKeyUniqueIndex ||
		tableName == (RoutingPoolMember{}).TableName() && indexName == routingPoolMemberGenerationUniqueIndex ||
		tableName == (RoutingCredentialRef{}).TableName() && indexName == routingCredentialGenerationUniqueIndex ||
		tableName == (RoutingCanaryEvaluation{}).TableName() && indexName == routingCanaryEvaluationWindowUniqueIndex ||
		tableName == (RoutingPolicyApproval{}).TableName() && indexName == routingPolicyApprovalIntentActorUniqueIndex ||
		tableName == (RoutingPolicyRollbackApproval{}).TableName() &&
			indexName == routingPolicyRollbackApprovalIntentActorUniqueIndex
	if db == nil || db.Dialector == nil || !allowed {
		return nil, false, false, ErrRoutingSchemaNotReady
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

func routingIndexColumnsEqual(actual []string, expected []string) bool {
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
