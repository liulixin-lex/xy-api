package model

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	routingErrorBudgetHistoryMaxPerRevision = 2_048
	routingErrorBudgetStateMaxPerPool       = 64
	routingErrorBudgetRetentionBatch        = 500
	routingErrorBudgetLegacyPoolIndex       = "idx_routing_error_budget_states_pool_id"
	routingErrorBudgetStateUniqueIndex      = "idx_routing_error_budget_state_key"
	routingErrorBudgetHistoryUniqueIndex    = "idx_routing_error_budget_history_event"
	routingErrorBudgetMigrationMaxAttempts  = 20
	routingErrorBudgetMigrationRetryDelay   = 50 * time.Millisecond
	routingErrorBudgetSchemaPollInterval    = 50 * time.Millisecond
)

type RoutingErrorBudgetMigrationOptions struct {
	// AlphaDrained must only be set after old routing writers have stopped.
	// Their pending telemetry must be drained before the pool-only identity is
	// removed, otherwise a legacy writer can recreate incompatible state.
	AlphaDrained bool
}

// RoutingErrorBudgetHistory is an immutable record of a state transition. It
// deliberately contains only bounded SLO fields and never request-level data.
type RoutingErrorBudgetHistory struct {
	ID                 int64   `json:"id" gorm:"primaryKey"`
	PoolID             int     `json:"pool_id" gorm:"not null;index;uniqueIndex:idx_routing_error_budget_history_event,priority:1"`
	PolicyRevision     int64   `json:"policy_revision" gorm:"bigint;not null;index;uniqueIndex:idx_routing_error_budget_history_event,priority:2"`
	PreviousStatus     string  `json:"previous_status,omitempty" gorm:"type:varchar(32);not null"`
	PreviousReason     string  `json:"previous_reason,omitempty" gorm:"type:varchar(64);not null"`
	Status             string  `json:"status" gorm:"type:varchar(32);index;not null"`
	Reason             string  `json:"reason" gorm:"type:varchar(64);index;not null"`
	AvailabilityTarget float64 `json:"availability_target" gorm:"not null"`
	EvaluationJSON     string  `json:"-" gorm:"type:text;not null"`
	LeaseFencingToken  int64   `json:"lease_fencing_token" gorm:"bigint;not null"`
	FirstObservedAtMs  int64   `json:"first_observed_at_ms" gorm:"bigint;not null"`
	EvaluatedAtMs      int64   `json:"evaluated_at_ms" gorm:"bigint;not null;index;uniqueIndex:idx_routing_error_budget_history_event,priority:3"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingErrorBudgetHistory) TableName() string {
	return "routing_error_budget_histories"
}

// RoutingErrorBudgetTransition is the bounded handoff for an outbox publisher.
// HistoryID is stable and can be used as the event idempotency identity.
type RoutingErrorBudgetTransition struct {
	HistoryID          int64   `json:"history_id"`
	PoolID             int     `json:"pool_id"`
	PolicyRevision     int64   `json:"policy_revision"`
	PreviousStatus     string  `json:"previous_status,omitempty"`
	PreviousReason     string  `json:"previous_reason,omitempty"`
	Status             string  `json:"status"`
	Reason             string  `json:"reason"`
	AvailabilityTarget float64 `json:"availability_target"`
	FirstObservedAtMs  int64   `json:"first_observed_at_ms"`
	EvaluatedAtMs      int64   `json:"evaluated_at_ms"`
}

func createRoutingErrorBudgetHistoryTx(
	ctx context.Context,
	tx *gorm.DB,
	state RoutingErrorBudgetState,
	previousStatus string,
	previousReason string,
	createdTime int64,
) (RoutingErrorBudgetTransition, error) {
	if err := ensureRoutingErrorBudgetPublisherBacklogTx(ctx, tx); err != nil {
		return RoutingErrorBudgetTransition{}, err
	}
	history := RoutingErrorBudgetHistory{
		PoolID: state.PoolID, PolicyRevision: state.PolicyRevision,
		PreviousStatus: previousStatus, PreviousReason: previousReason,
		Status: state.Status, Reason: state.Reason, AvailabilityTarget: state.AvailabilityTarget,
		EvaluationJSON: state.EvaluationJSON, LeaseFencingToken: state.LeaseFencingToken,
		FirstObservedAtMs: state.FirstObservedAtMs, EvaluatedAtMs: state.LastEvaluatedAtMs,
		CreatedTime: createdTime,
	}
	if err := validateRoutingErrorBudgetHistory(&history); err != nil {
		return RoutingErrorBudgetTransition{}, err
	}
	if err := tx.WithContext(ctx).Create(&history).Error; err != nil {
		return RoutingErrorBudgetTransition{}, err
	}
	if err := pruneRoutingErrorBudgetHistoryTx(ctx, tx, state.PoolID, state.PolicyRevision); err != nil {
		return RoutingErrorBudgetTransition{}, err
	}
	return routingErrorBudgetTransitionFromHistory(history), nil
}

func pruneRoutingErrorBudgetHistoryTx(
	ctx context.Context,
	tx *gorm.DB,
	poolID int,
	policyRevision int64,
) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetHistory{}).
		Where("pool_id = ? AND policy_revision = ?", poolID, policyRevision).Count(&count).Error; err != nil {
		return err
	}
	excess := count - routingErrorBudgetHistoryMaxPerRevision
	for excess > 0 {
		publishedThrough, err := routingErrorBudgetPublishedThroughTx(ctx, tx)
		if err != nil {
			return err
		}
		if publishedThrough <= 0 {
			return nil
		}
		limit := min(excess, int64(routingErrorBudgetRetentionBatch))
		var ids []int64
		if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetHistory{}).
			Where("pool_id = ? AND policy_revision = ? AND id <= ?", poolID, policyRevision, publishedThrough).
			Order("evaluated_at_ms ASC").Order("id ASC").Limit(int(limit)).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingErrorBudgetHistory{}).Error; err != nil {
			return err
		}
		excess -= int64(len(ids))
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func pruneRoutingErrorBudgetStatesTx(ctx context.Context, tx *gorm.DB, poolID int, retainID int64) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetState{}).Where("pool_id = ?", poolID).Count(&count).Error; err != nil {
		return err
	}
	excess := count - routingErrorBudgetStateMaxPerPool
	for excess > 0 {
		limit := min(excess, int64(routingErrorBudgetRetentionBatch))
		var ids []int64
		if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetState{}).
			Where("pool_id = ? AND id <> ?", poolID, retainID).
			Order("policy_revision ASC").Order("id ASC").Limit(int(limit)).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingErrorBudgetState{}).Error; err != nil {
			return err
		}
		excess -= int64(len(ids))
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func ListRoutingErrorBudgetHistoryContext(
	ctx context.Context,
	poolID int,
	policyRevision int64,
	beforeID int64,
	limit int,
) ([]RoutingErrorBudgetHistory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 || policyRevision <= 0 || beforeID < 0 {
		return nil, ErrRoutingErrorBudgetStateInvalid
	}
	if limit < 1 {
		limit = 100
	}
	if limit > routingErrorBudgetRetentionBatch {
		limit = routingErrorBudgetRetentionBatch
	}
	query := DB.WithContext(ctx).Where(
		"pool_id = ? AND policy_revision = ?", poolID, policyRevision,
	)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var history []RoutingErrorBudgetHistory
	err := query.Order("id DESC").Limit(limit).Find(&history).Error
	return history, err
}

// ListRoutingErrorBudgetTransitionsAfterContext exposes history as a durable,
// ordered event source. A central publisher can checkpoint the last HistoryID
// and safely retry without coupling state persistence to an in-memory event.
func ListRoutingErrorBudgetTransitionsAfterContext(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]RoutingErrorBudgetTransition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if afterID < 0 {
		return nil, ErrRoutingErrorBudgetStateInvalid
	}
	if limit < 1 {
		limit = 100
	}
	if limit > routingErrorBudgetRetentionBatch {
		limit = routingErrorBudgetRetentionBatch
	}
	var history []RoutingErrorBudgetHistory
	if err := DB.WithContext(ctx).Where("id > ?", afterID).
		Order("id ASC").Limit(limit).Find(&history).Error; err != nil {
		return nil, err
	}
	transitions := make([]RoutingErrorBudgetTransition, 0, len(history))
	for _, item := range history {
		transitions = append(transitions, routingErrorBudgetTransitionFromHistory(item))
	}
	return transitions, nil
}

func DeleteRoutingErrorBudgetHistoryBeforeContext(ctx context.Context, cutoffMs int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 {
		return 0, ErrRoutingErrorBudgetStateInvalid
	}
	publishedThrough, err := routingErrorBudgetPublishedThroughTx(ctx, DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	if publishedThrough <= 0 {
		return 0, nil
	}
	deleted := int64(0)
	for {
		var ids []int64
		if err := DB.WithContext(ctx).Model(&RoutingErrorBudgetHistory{}).
			Where("evaluated_at_ms < ? AND id <= ?", cutoffMs, publishedThrough).Order("id ASC").
			Limit(routingErrorBudgetRetentionBatch).Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		result := DB.WithContext(ctx).Where("id IN ? AND evaluated_at_ms < ? AND id <= ?", ids, cutoffMs, publishedThrough).
			Delete(&RoutingErrorBudgetHistory{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
		if len(ids) < routingErrorBudgetRetentionBatch {
			return deleted, nil
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
	}
}

// MigrateRoutingErrorBudgetModels upgrades the legacy pool-only unique state
// index to the PoolID+PolicyRevision identity and creates transition history.
// Legacy alpha writers must be stopped before the contract phase is allowed.
func MigrateRoutingErrorBudgetModels(db *gorm.DB) error {
	return MigrateRoutingErrorBudgetModelsContextWithOptions(
		context.Background(), db, RoutingErrorBudgetMigrationOptions{},
	)
}

func MigrateRoutingErrorBudgetModelsWithOptions(
	db *gorm.DB,
	options RoutingErrorBudgetMigrationOptions,
) error {
	return MigrateRoutingErrorBudgetModelsContextWithOptions(context.Background(), db, options)
}

func MigrateRoutingErrorBudgetModelsContextWithOptions(
	ctx context.Context,
	db *gorm.DB,
	options RoutingErrorBudgetMigrationOptions,
) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingErrorBudgetStateInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db = db.WithContext(ctx)

	legacyColumns, legacyUnique, legacyExists, err := routingErrorBudgetIndexDefinition(
		db, (RoutingErrorBudgetState{}).TableName(), routingErrorBudgetLegacyPoolIndex,
	)
	if err != nil {
		return fmt.Errorf("inspect legacy routing error budget index: %w", err)
	}
	if legacyExists {
		if !legacyUnique || !routingErrorBudgetIndexColumnsEqual(legacyColumns, []string{"pool_id"}) {
			return fmt.Errorf("routing error budget index %s has unexpected definition", routingErrorBudgetLegacyPoolIndex)
		}
		if !options.AlphaDrained {
			return ErrRoutingErrorBudgetAlphaDrainRequired
		}
	}

	var lastErr error
	expanded := false
	for attempt := 0; attempt < routingErrorBudgetMigrationMaxAttempts; attempt++ {
		migrationErr := db.AutoMigrate(&RoutingErrorBudgetState{}, &RoutingErrorBudgetHistory{}, &RoutingErrorBudgetCursor{})
		ready, readErr := routingErrorBudgetExpandedSchemaReady(db)
		if ready {
			expanded = true
			break
		}
		switch {
		case readErr != nil:
			lastErr = readErr
		case migrationErr != nil:
			lastErr = migrationErr
		default:
			lastErr = ErrRoutingErrorBudgetSchemaNotReady
		}
		if migrationErr != nil && !isRetryableRoutingMetricMigrationError(migrationErr) && readErr == nil {
			return fmt.Errorf("migrate routing error budget models: %w", migrationErr)
		}
		if err := waitRoutingErrorBudgetMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if !expanded {
		return fmt.Errorf("migrate routing error budget models: %w", lastErr)
	}

	legacyRemoved := false
	for attempt := 0; attempt < routingErrorBudgetMigrationMaxAttempts; attempt++ {
		columns, unique, exists, readErr := routingErrorBudgetIndexDefinition(
			db, (RoutingErrorBudgetState{}).TableName(), routingErrorBudgetLegacyPoolIndex,
		)
		if readErr == nil && !exists {
			legacyRemoved = true
			break
		}
		if readErr == nil && (!unique || !routingErrorBudgetIndexColumnsEqual(columns, []string{"pool_id"})) {
			return fmt.Errorf("routing error budget index %s has unexpected definition", routingErrorBudgetLegacyPoolIndex)
		}
		if readErr == nil && !options.AlphaDrained {
			return ErrRoutingErrorBudgetAlphaDrainRequired
		}

		migrationErr := readErr
		if readErr == nil {
			migrationErr = db.Migrator().DropIndex(&RoutingErrorBudgetState{}, routingErrorBudgetLegacyPoolIndex)
		}
		_, _, exists, readErr = routingErrorBudgetIndexDefinition(
			db, (RoutingErrorBudgetState{}).TableName(), routingErrorBudgetLegacyPoolIndex,
		)
		if readErr == nil && !exists {
			legacyRemoved = true
			break
		}
		switch {
		case readErr != nil:
			lastErr = readErr
		case migrationErr != nil:
			lastErr = migrationErr
		default:
			lastErr = ErrRoutingErrorBudgetSchemaNotReady
		}
		if migrationErr != nil && !isRetryableRoutingMetricMigrationError(migrationErr) && readErr == nil {
			return fmt.Errorf("drop legacy routing error budget index: %w", migrationErr)
		}
		if err := waitRoutingErrorBudgetMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if !legacyRemoved {
		return fmt.Errorf("drop legacy routing error budget index: %w", lastErr)
	}

	for attempt := 0; attempt < routingErrorBudgetMigrationMaxAttempts; attempt++ {
		ready, readErr := RoutingErrorBudgetSchemaReady(db)
		if ready {
			return nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = ErrRoutingErrorBudgetSchemaNotReady
		}
		if err := waitRoutingErrorBudgetMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("verify routing error budget schema: %w", lastErr)
}

func RoutingErrorBudgetSchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingErrorBudgetStateInvalid
	}
	ready, err := routingErrorBudgetExpandedSchemaReady(db)
	if err != nil || !ready {
		return ready, err
	}
	columns, unique, exists, err := routingErrorBudgetIndexDefinition(
		db, (RoutingErrorBudgetState{}).TableName(), routingErrorBudgetLegacyPoolIndex,
	)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if unique && routingErrorBudgetIndexColumnsEqual(columns, []string{"pool_id"}) {
		return false, nil
	}
	return false, fmt.Errorf("routing error budget index %s has unexpected definition", routingErrorBudgetLegacyPoolIndex)
}

func WaitRoutingErrorBudgetSchemaReady(ctx context.Context, db *gorm.DB, maxWait time.Duration) error {
	if db == nil || db.Dialector == nil || maxWait < 0 {
		return ErrRoutingErrorBudgetStateInvalid
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
		ready, err := RoutingErrorBudgetSchemaReady(db.WithContext(ctx))
		if ready {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if maxWait == 0 {
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrRoutingErrorBudgetSchemaNotReady, lastErr)
			}
			return ErrRoutingErrorBudgetSchemaNotReady
		}

		poll := time.NewTimer(routingErrorBudgetSchemaPollInterval)
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
				return fmt.Errorf("%w: %v", ErrRoutingErrorBudgetSchemaNotReady, lastErr)
			}
			return ErrRoutingErrorBudgetSchemaNotReady
		case <-poll.C:
		}
	}
}

func routingErrorBudgetExpandedSchemaReady(db *gorm.DB) (bool, error) {
	stateTable := (RoutingErrorBudgetState{}).TableName()
	historyTable := (RoutingErrorBudgetHistory{}).TableName()
	for _, table := range []struct {
		model   any
		columns []string
	}{
		{model: &RoutingErrorBudgetState{}, columns: []string{
			"id", "pool_id", "policy_revision", "availability_target", "status", "reason",
			"evaluation_json", "lease_fencing_token", "first_observed_at_ms", "last_evaluated_at_ms",
			"last_changed_at_ms", "created_time", "updated_time",
		}},
		{model: &RoutingErrorBudgetHistory{}, columns: []string{
			"id", "pool_id", "policy_revision", "previous_status", "previous_reason", "status", "reason",
			"availability_target", "evaluation_json", "lease_fencing_token", "first_observed_at_ms",
			"evaluated_at_ms", "created_time",
		}},
		{model: &RoutingErrorBudgetCursor{}, columns: []string{
			"cursor_name", "policy_revision", "position_id", "lease_name", "lease_fencing_token", "updated_time_ms",
		}},
	} {
		if !db.Migrator().HasTable(table.model) {
			return false, nil
		}
		columnTypes, err := db.Migrator().ColumnTypes(table.model)
		if err != nil {
			return false, err
		}
		present := make(map[string]struct{}, len(columnTypes))
		for _, columnType := range columnTypes {
			present[strings.ToLower(columnType.Name())] = struct{}{}
		}
		for _, column := range table.columns {
			if _, exists := present[column]; !exists {
				return false, nil
			}
		}
	}

	columns, unique, exists, err := routingErrorBudgetIndexDefinition(
		db, stateTable, routingErrorBudgetStateUniqueIndex,
	)
	if err != nil {
		return false, err
	}
	if !exists || !unique || !routingErrorBudgetIndexColumnsEqual(columns, []string{"pool_id", "policy_revision"}) {
		return false, nil
	}
	columns, unique, exists, err = routingErrorBudgetIndexDefinition(
		db, historyTable, routingErrorBudgetHistoryUniqueIndex,
	)
	if err != nil {
		return false, err
	}
	return exists && unique && routingErrorBudgetIndexColumnsEqual(
		columns, []string{"pool_id", "policy_revision", "evaluated_at_ms"},
	), nil
}

func routingErrorBudgetIndexDefinition(
	db *gorm.DB,
	tableName string,
	indexName string,
) ([]string, bool, bool, error) {
	allowed := map[string]map[string]struct{}{
		(RoutingErrorBudgetState{}).TableName(): {
			routingErrorBudgetLegacyPoolIndex:  {},
			routingErrorBudgetStateUniqueIndex: {},
		},
		(RoutingErrorBudgetHistory{}).TableName(): {
			routingErrorBudgetHistoryUniqueIndex: {},
		},
	}
	indexes, tableAllowed := allowed[tableName]
	if !tableAllowed {
		return nil, false, false, ErrRoutingErrorBudgetStateInvalid
	}
	if _, indexAllowed := indexes[indexName]; !indexAllowed {
		return nil, false, false, ErrRoutingErrorBudgetStateInvalid
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
		return nil, false, false, fmt.Errorf("unsupported routing error budget database %q", db.Dialector.Name())
	}
}

func routingErrorBudgetIndexColumnsEqual(actual []string, expected []string) bool {
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

func waitRoutingErrorBudgetMigrationRetry(ctx context.Context, attempt int) error {
	if attempt >= routingErrorBudgetMigrationMaxAttempts-1 {
		return nil
	}
	delay := time.Duration(attempt+1) * routingErrorBudgetMigrationRetryDelay
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validateRoutingErrorBudgetHistory(history *RoutingErrorBudgetHistory) error {
	if history == nil || history.PoolID <= 0 || history.PolicyRevision <= 0 ||
		(history.PreviousStatus != "" && !validRoutingErrorBudgetText(history.PreviousStatus, 32)) ||
		(history.PreviousReason != "" && !validRoutingErrorBudgetText(history.PreviousReason, 64)) ||
		!validRoutingErrorBudgetText(history.Status, 32) || !validRoutingErrorBudgetText(history.Reason, 64) ||
		math.IsNaN(history.AvailabilityTarget) || math.IsInf(history.AvailabilityTarget, 0) ||
		history.AvailabilityTarget <= 0 || history.AvailabilityTarget >= 1 ||
		len(history.EvaluationJSON) == 0 || len(history.EvaluationJSON) > routingErrorBudgetPayloadMaxBytes ||
		history.LeaseFencingToken <= 0 || history.FirstObservedAtMs <= 0 ||
		history.EvaluatedAtMs < history.FirstObservedAtMs || history.CreatedTime <= 0 {
		return ErrRoutingErrorBudgetStateInvalid
	}
	return nil
}

func routingErrorBudgetTransitionFromHistory(history RoutingErrorBudgetHistory) RoutingErrorBudgetTransition {
	return RoutingErrorBudgetTransition{
		HistoryID: history.ID, PoolID: history.PoolID, PolicyRevision: history.PolicyRevision,
		PreviousStatus: history.PreviousStatus, PreviousReason: history.PreviousReason,
		Status: history.Status, Reason: history.Reason, AvailabilityTarget: history.AvailabilityTarget,
		FirstObservedAtMs: history.FirstObservedAtMs, EvaluatedAtMs: history.EvaluatedAtMs,
	}
}
