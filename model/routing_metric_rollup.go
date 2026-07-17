package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingMetricRollupSchemaVersion     = 4
	RoutingMetricRollupMaxBatch          = 500
	RoutingMetricRollupDefaultQueryLimit = 1_000
	RoutingMetricRollupMaxQueryLimit     = 200_000

	routingMetricRollupLegacyUniqueIndex    = "idx_routing_metric_rollup_key"
	routingMetricRollupUniqueIndex          = "idx_routing_metric_rollup_revision_key"
	routingMetricRollupRevisionGuardIndex   = "idx_routing_metric_rollup_revision_guard"
	routingMetricRollupMigrationMaxAttempts = 20
	routingMetricRollupMigrationRetryDelay  = 50 * time.Millisecond
	routingMetricRollupSchemaPollInterval   = 50 * time.Millisecond

	routingMetricRollupRetentionBatchSize = 500
	routingMetricRollupModelNameMaxRunes  = 128
	routingMetricRollupModelNameMaxBytes  = 512
	routingMetricRollupFutureSkew         = 10 * time.Minute
	routingMetricTransactionMaxAttempts   = 4
	routingMetricMaxCounter               = int64(math.MaxInt32)
	routingMetricMaxAttemptTokens         = int64(math.MaxInt32 / 2)
	routingMetricMaxRetryAfterMs          = int64((7 * 24 * time.Hour) / time.Millisecond)
)

var (
	ErrRoutingMetricRollupBatchTooLarge      = errors.New("routing metric rollup batch exceeds limit")
	ErrRoutingMetricRollupDuplicateKey       = errors.New("routing metric rollup batch contains duplicate key")
	ErrRoutingMetricRollupInvalid            = errors.New("invalid routing metric rollup")
	ErrRoutingMetricRollupQueryTooLarge      = errors.New("routing metric rollup query exceeds limit")
	ErrRoutingMetricRollupOverflow           = errors.New("routing metric rollup counter overflow")
	ErrRoutingMetricRollupSchemaNotReady     = errors.New("routing metric rollup revision-key schema is not ready")
	ErrRoutingMetricRollupAlphaDrainRequired = errors.New("legacy routing metric writers and telemetry must be drained")
)

type RoutingMetricRollupMigrationOptions struct {
	// AlphaDrained must only be set after old writers have stopped and their
	// telemetry backlog has been fully drained.
	AlphaDrained bool
}

// RoutingMetricRollup stores mergeable counters keyed by stable routing identities.
// Percentiles are intentionally excluded because scalar percentiles are not mergeable.
type RoutingMetricRollup struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	MemberID          int    `json:"member_id" gorm:"not null;uniqueIndex:idx_routing_metric_rollup_revision_key,priority:1;index"`
	CredentialID      int    `json:"credential_id" gorm:"not null;uniqueIndex:idx_routing_metric_rollup_revision_key,priority:2;index"`
	ModelName         string `json:"model_name" gorm:"type:varchar(128);not null;index"`
	ModelKey          string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_metric_rollup_revision_key,priority:3"`
	BucketTs          int64  `json:"bucket_ts" gorm:"bigint;not null;uniqueIndex:idx_routing_metric_rollup_revision_key,priority:4;index:idx_routing_metric_rollup_bucket_ts"`
	ChannelID         int    `json:"channel_id" gorm:"not null;index"`
	ChannelGeneration string `json:"channel_generation" gorm:"type:varchar(32);index"`
	PoolID            int    `json:"pool_id" gorm:"not null;index"`
	SchemaVersion     int    `json:"schema_version" gorm:"not null"`
	// LastSnapshotRevision keeps its legacy column/API name, but schema v3 treats
	// it as the exact snapshot revision and the fifth physical rollup key.
	LastSnapshotRevision int64  `json:"last_snapshot_revision" gorm:"bigint;not null;index;uniqueIndex:idx_routing_metric_rollup_revision_key,priority:5"`
	SketchCodecVersion   int    `json:"sketch_codec_version"`
	LatencySampleCount   int64  `json:"latency_sample_count"`
	LatencySketch        []byte `json:"latency_sketch"`
	TtftSampleCount      int64  `json:"ttft_sample_count"`
	TtftSketch           []byte `json:"ttft_sketch"`

	RequestCount            int64 `json:"request_count" gorm:"not null"`
	SuccessCount            int64 `json:"success_count" gorm:"not null"`
	FailureCount            int64 `json:"failure_count" gorm:"not null"`
	UnknownCount            int64 `json:"unknown_count" gorm:"not null"`
	ReliabilityRequestCount int64 `json:"reliability_request_count" gorm:"not null"`
	ReliabilityFailureCount int64 `json:"reliability_failure_count" gorm:"not null"`
	TotalLatencyMs          int64 `json:"total_latency_ms" gorm:"not null"`
	TtftSumMs               int64 `json:"ttft_sum_ms" gorm:"not null"`
	TtftCount               int64 `json:"ttft_count" gorm:"not null"`
	OutputTokens            int64 `json:"output_tokens" gorm:"not null"`
	GenerationMs            int64 `json:"generation_ms" gorm:"not null"`
	Err4xx                  int64 `json:"err_4xx" gorm:"column:err_4xx;not null"`
	Err5xx                  int64 `json:"err_5xx" gorm:"column:err_5xx;not null"`
	Err429                  int64 `json:"err_429" gorm:"column:err_429;not null"`
	Err529                  int64 `json:"err_529" gorm:"column:err_529;not null"`
	RetryAfterCount         int64 `json:"retry_after_count" gorm:"not null"`
	RetryAfterTotalMs       int64 `json:"retry_after_total_ms" gorm:"not null"`
}

type RoutingMetricReliabilityAggregate struct {
	RequestCount int64 `json:"request_count"`
	FailureCount int64 `json:"failure_count"`
}

func AggregateRoutingMetricReliabilityContext(
	ctx context.Context,
	poolID int,
	fromUnix int64,
	toUnix int64,
) (RoutingMetricReliabilityAggregate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 || fromUnix < 0 || toUnix <= fromUnix {
		return RoutingMetricReliabilityAggregate{}, ErrRoutingMetricRollupInvalid
	}
	var aggregate RoutingMetricReliabilityAggregate
	err := DB.WithContext(ctx).Model(&RoutingMetricRollup{}).
		Select(
			"COALESCE(SUM(reliability_request_count), 0) AS request_count, "+
				"COALESCE(SUM(reliability_failure_count), 0) AS failure_count",
		).
		Where("pool_id = ? AND bucket_ts >= ? AND bucket_ts < ?", poolID, fromUnix, toUnix).
		Scan(&aggregate).Error
	if err != nil {
		return RoutingMetricReliabilityAggregate{}, err
	}
	if aggregate.RequestCount < 0 || aggregate.FailureCount < 0 || aggregate.FailureCount > aggregate.RequestCount {
		return RoutingMetricReliabilityAggregate{}, ErrRoutingMetricRollupInvalid
	}
	return aggregate, nil
}

func (RoutingMetricRollup) TableName() string {
	return "routing_metric_rollups"
}

type routingMetricRollupRevisionGuard struct {
	MemberID             int    `gorm:"uniqueIndex:idx_routing_metric_rollup_revision_guard,priority:1"`
	CredentialID         int    `gorm:"uniqueIndex:idx_routing_metric_rollup_revision_guard,priority:2"`
	ModelKey             string `gorm:"type:char(64);uniqueIndex:idx_routing_metric_rollup_revision_guard,priority:3"`
	BucketTs             int64  `gorm:"bigint;uniqueIndex:idx_routing_metric_rollup_revision_guard,priority:4"`
	LastSnapshotRevision int64  `gorm:"bigint;uniqueIndex:idx_routing_metric_rollup_revision_guard,priority:5"`
}

func (routingMetricRollupRevisionGuard) TableName() string {
	return "routing_metric_rollups"
}

// MigrateRoutingMetricRollupRevisionKey finalizes the legacy four-column
// identity as the schema-v3 revision-isolated identity. The v3 index has a new
// physical name so concurrent masters can expand before any of them contracts
// the legacy index. Every failed DDL operation is followed by a state read;
// another master completing that phase therefore counts as success.
//
// PostgreSQL and SQLite writers compiled against the four-column ON CONFLICT
// target cannot coexist with the finalized index. The caller must first stop or
// upgrade those alpha writers and drain their telemetry queue. This helper does
// not rewrite legacy rows or claim mixed v1/v2 buckets are revision-isolated.
// The central migration path should call it after the rollout gate described
// above. Interrupted runs are safe to resume and completed runs are idempotent.
func MigrateRoutingMetricRollupRevisionKey(db *gorm.DB) error {
	return MigrateRoutingMetricRollupRevisionKeyContextWithOptions(
		context.Background(), db, RoutingMetricRollupMigrationOptions{},
	)
}

func MigrateRoutingMetricRollupRevisionKeyContext(ctx context.Context, db *gorm.DB) error {
	return MigrateRoutingMetricRollupRevisionKeyContextWithOptions(
		ctx, db, RoutingMetricRollupMigrationOptions{},
	)
}

func MigrateRoutingMetricRollupRevisionKeyWithOptions(
	db *gorm.DB,
	options RoutingMetricRollupMigrationOptions,
) error {
	return MigrateRoutingMetricRollupRevisionKeyContextWithOptions(context.Background(), db, options)
}

func MigrateRoutingMetricRollupRevisionKeyContextWithOptions(
	ctx context.Context,
	db *gorm.DB,
	options RoutingMetricRollupMigrationOptions,
) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingMetricRollupInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db = db.WithContext(ctx)
	desiredColumns := []string{
		"member_id",
		"credential_id",
		"model_key",
		"bucket_ts",
		"last_snapshot_revision",
	}
	legacyColumns := desiredColumns[:len(desiredColumns)-1]

	var lastErr error
	var (
		legacyIndexColumns []string
		legacyIndexUnique  bool
		legacyIndexExists  bool
		err                error
	)
	for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
		legacyIndexColumns, legacyIndexUnique, legacyIndexExists, err = routingMetricRollupIndexDefinition(
			db, routingMetricRollupLegacyUniqueIndex,
		)
		if err == nil {
			break
		}
		lastErr = err
		if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if err != nil {
		return fmt.Errorf("inspect legacy routing metric rollup index: %w", lastErr)
	}
	legacyIndexIsPreRevision := legacyIndexExists && legacyIndexUnique &&
		routingMetricRollupIndexColumnsEqual(legacyIndexColumns, legacyColumns)
	if legacyIndexExists && !legacyIndexIsPreRevision &&
		(!legacyIndexUnique || !routingMetricRollupIndexColumnsEqual(legacyIndexColumns, desiredColumns)) {
		return fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupLegacyUniqueIndex)
	}

	columnsReady := false
	for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
		columnsReady, err = routingMetricRollupRevisionKeyColumnsReady(db)
		if err == nil {
			break
		}
		lastErr = err
		if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if err != nil {
		return fmt.Errorf("inspect routing metric rollup columns: %w", lastErr)
	}
	if legacyIndexIsPreRevision && !columnsReady && !options.AlphaDrained {
		return ErrRoutingMetricRollupAlphaDrainRequired
	}
	if !legacyIndexIsPreRevision || !columnsReady {
		columnsReady = false
		for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
			migrationErr := db.AutoMigrate(&RoutingMetricRollup{})
			var readErr error
			columnsReady, readErr = routingMetricRollupRevisionKeyColumnsReady(db)
			if columnsReady {
				break
			}
			if readErr != nil {
				lastErr = readErr
			} else if migrationErr != nil {
				lastErr = migrationErr
			} else {
				lastErr = ErrRoutingMetricRollupSchemaNotReady
			}
			if migrationErr != nil && !isRetryableRoutingMetricMigrationError(migrationErr) && readErr == nil {
				return fmt.Errorf("migrate routing metric rollup columns: %w", migrationErr)
			}
			if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
				return err
			}
		}
	}
	if !columnsReady {
		return fmt.Errorf("migrate routing metric rollup columns: %w", lastErr)
	}

	indexReady := false
	for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
		columns, unique, exists, readErr := routingMetricRollupIndexDefinition(db, routingMetricRollupUniqueIndex)
		if readErr == nil && exists && unique && routingMetricRollupIndexColumnsEqual(columns, desiredColumns) {
			indexReady = true
			break
		}
		if readErr == nil && exists {
			return fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupUniqueIndex)
		}

		migrationErr := readErr
		if readErr == nil {
			migrationErr = db.Migrator().CreateIndex(&RoutingMetricRollup{}, routingMetricRollupUniqueIndex)
		}
		columns, unique, exists, readErr = routingMetricRollupIndexDefinition(db, routingMetricRollupUniqueIndex)
		if readErr == nil && exists && unique && routingMetricRollupIndexColumnsEqual(columns, desiredColumns) {
			indexReady = true
			break
		}
		if readErr == nil && exists {
			return fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupUniqueIndex)
		}
		if readErr != nil {
			lastErr = readErr
		} else if migrationErr != nil {
			lastErr = migrationErr
		} else {
			lastErr = ErrRoutingMetricRollupSchemaNotReady
		}
		if migrationErr != nil && !isRetryableRoutingMetricMigrationError(migrationErr) && readErr == nil {
			return fmt.Errorf("create revision-isolated routing metric rollup index: %w", migrationErr)
		}
		if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if !indexReady {
		return fmt.Errorf("create revision-isolated routing metric rollup index: %w", lastErr)
	}
	columns, unique, exists, err := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
	if err != nil {
		return err
	}
	if exists && unique && routingMetricRollupIndexColumnsEqual(columns, legacyColumns) && !options.AlphaDrained {
		return ErrRoutingMetricRollupAlphaDrainRequired
	}

	legacyRemoved := false
	for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
		columns, unique, exists, readErr := routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
		if readErr == nil && (!exists || unique && routingMetricRollupIndexColumnsEqual(columns, desiredColumns)) {
			legacyRemoved = true
			break
		}
		if readErr == nil && (!unique || !routingMetricRollupIndexColumnsEqual(columns, legacyColumns)) {
			return fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupLegacyUniqueIndex)
		}

		migrationErr := readErr
		if readErr == nil {
			migrationErr = db.Migrator().DropIndex(&RoutingMetricRollup{}, routingMetricRollupLegacyUniqueIndex)
		}
		columns, unique, exists, readErr = routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
		if readErr == nil && (!exists || unique && routingMetricRollupIndexColumnsEqual(columns, desiredColumns)) {
			legacyRemoved = true
			break
		}
		if readErr == nil && exists && (!unique || !routingMetricRollupIndexColumnsEqual(columns, legacyColumns)) {
			return fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupLegacyUniqueIndex)
		}
		if readErr != nil {
			lastErr = readErr
		} else if migrationErr != nil {
			lastErr = migrationErr
		} else {
			lastErr = ErrRoutingMetricRollupSchemaNotReady
		}
		if migrationErr != nil && !isRetryableRoutingMetricMigrationError(migrationErr) && readErr == nil {
			return fmt.Errorf("drop legacy routing metric rollup index: %w", migrationErr)
		}
		if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if !legacyRemoved {
		return fmt.Errorf("drop legacy routing metric rollup index: %w", lastErr)
	}

	for attempt := 0; attempt < routingMetricRollupMigrationMaxAttempts; attempt++ {
		ready, readErr := RoutingMetricRollupRevisionKeySchemaReady(db)
		if ready {
			return nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = ErrRoutingMetricRollupSchemaNotReady
		}
		if err := waitRoutingMetricRollupMigrationRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("verify routing metric rollup revision-key schema: %w", lastErr)
}

// RoutingMetricRollupRevisionKeySchemaReady reports whether v3 writers can use
// the exact five-column conflict target. It fails closed while a legacy
// four-column index is still present.
func RoutingMetricRollupRevisionKeySchemaReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingMetricRollupInvalid
	}
	columnsReady, err := routingMetricRollupRevisionKeyColumnsReady(db)
	if err != nil || !columnsReady {
		return false, err
	}
	desiredColumns := []string{
		"member_id",
		"credential_id",
		"model_key",
		"bucket_ts",
		"last_snapshot_revision",
	}
	columns, unique, exists, err := routingMetricRollupIndexDefinition(db, routingMetricRollupUniqueIndex)
	if err != nil {
		return false, err
	}
	if !exists || !unique || !routingMetricRollupIndexColumnsEqual(columns, desiredColumns) {
		return false, nil
	}
	columns, unique, exists, err = routingMetricRollupIndexDefinition(db, routingMetricRollupLegacyUniqueIndex)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if unique && routingMetricRollupIndexColumnsEqual(columns, desiredColumns) {
		return true, nil
	}
	legacyColumns := desiredColumns[:len(desiredColumns)-1]
	if unique && routingMetricRollupIndexColumnsEqual(columns, legacyColumns) {
		return false, nil
	}
	return false, fmt.Errorf("routing metric rollup index %s has unexpected definition", routingMetricRollupLegacyUniqueIndex)
}

// WaitRoutingMetricRollupRevisionKeySchemaReady gives non-migrating nodes a
// bounded startup gate while the elected/default master converges the schema.
func WaitRoutingMetricRollupRevisionKeySchemaReady(
	ctx context.Context,
	db *gorm.DB,
	maxWait time.Duration,
) error {
	if db == nil || db.Dialector == nil || maxWait < 0 {
		return ErrRoutingMetricRollupInvalid
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
		ready, err := RoutingMetricRollupRevisionKeySchemaReady(db.WithContext(ctx))
		if ready {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if maxWait == 0 {
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrRoutingMetricRollupSchemaNotReady, lastErr)
			}
			return ErrRoutingMetricRollupSchemaNotReady
		}

		poll := time.NewTimer(routingMetricRollupSchemaPollInterval)
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
				return fmt.Errorf("%w: %v", ErrRoutingMetricRollupSchemaNotReady, lastErr)
			}
			return ErrRoutingMetricRollupSchemaNotReady
		case <-poll.C:
		}
	}
}

func routingMetricRollupRevisionKeyColumnsReady(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(&RoutingMetricRollup{}) {
		return false, nil
	}
	columnTypes, err := db.Migrator().ColumnTypes(&RoutingMetricRollup{})
	if err != nil {
		return false, err
	}
	columns := make(map[string]struct{}, len(columnTypes))
	for _, columnType := range columnTypes {
		columns[strings.ToLower(columnType.Name())] = struct{}{}
	}
	for _, column := range []string{"member_id", "credential_id", "model_key", "bucket_ts", "last_snapshot_revision"} {
		if _, exists := columns[column]; !exists {
			return false, nil
		}
	}
	return true, nil
}

func waitRoutingMetricRollupMigrationRetry(ctx context.Context, attempt int) error {
	if attempt >= routingMetricRollupMigrationMaxAttempts-1 {
		return nil
	}
	delay := time.Duration(attempt+1) * routingMetricRollupMigrationRetryDelay
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableRoutingMetricMigrationError(err error) bool {
	if err == nil {
		return false
	}
	if isRetryableRoutingMetricTransactionError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"already exists",
		"duplicate column",
		"duplicate key name",
		"error 1050",
		"error 1060",
		"error 1061",
		"sqlstate 42p07",
		"sqlstate 42701",
		"no such index",
		"does not exist",
		"can't drop",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func routingMetricRollupIndexDefinition(db *gorm.DB, indexName string) ([]string, bool, bool, error) {
	switch db.Dialector.Name() {
	case "sqlite":
		var indexes []struct {
			Name   string `gorm:"column:name"`
			Unique int    `gorm:"column:unique"`
		}
		if err := db.Raw("PRAGMA index_list('routing_metric_rollups')").Scan(&indexes).Error; err != nil {
			return nil, false, false, err
		}
		for _, index := range indexes {
			if index.Name != indexName {
				continue
			}
			if indexName != routingMetricRollupLegacyUniqueIndex && indexName != routingMetricRollupUniqueIndex && indexName != routingMetricRollupRevisionGuardIndex {
				return nil, false, false, errors.New("unsupported routing metric rollup index")
			}
			var columns []struct {
				Sequence int    `gorm:"column:seqno"`
				Name     string `gorm:"column:name"`
			}
			query := "PRAGMA index_info('" + indexName + "')"
			if err := db.Raw(query).Scan(&columns).Error; err != nil {
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
			"routing_metric_rollups", indexName,
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
			"SELECT indexed_column.attname AS column_name, "+
				"index_meta.indisunique AS is_unique "+
				"FROM pg_catalog.pg_index AS index_meta "+
				"JOIN pg_catalog.pg_class AS index_table ON index_table.oid = index_meta.indexrelid "+
				"JOIN pg_catalog.pg_class AS data_table ON data_table.oid = index_meta.indrelid "+
				"JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = data_table.relnamespace "+
				"JOIN LATERAL unnest(index_meta.indkey::smallint[]) WITH ORDINALITY "+
				"AS indexed_key(attribute_number, position) ON TRUE "+
				"JOIN pg_catalog.pg_attribute AS indexed_column "+
				"ON indexed_column.attrelid = data_table.oid "+
				"AND indexed_column.attnum = indexed_key.attribute_number "+
				"WHERE namespace.nspname = current_schema() AND data_table.relname = ? AND index_table.relname = ? "+
				"ORDER BY indexed_key.position",
			"routing_metric_rollups", indexName,
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
		return nil, false, false, fmt.Errorf("unsupported routing metric rollup database %q", db.Dialector.Name())
	}
}

func routingMetricRollupIndexColumnsEqual(actual []string, expected []string) bool {
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

func UpsertRoutingMetricRollups(rollups []RoutingMetricRollup) error {
	return UpsertRoutingMetricRollupsContext(context.Background(), rollups)
}

func UpsertRoutingMetricRollupsContext(ctx context.Context, rollups []RoutingMetricRollup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeRoutingMetricRollups(rollups)
	if err != nil || len(normalized) == 0 {
		return err
	}

	err = runRoutingMetricTransactionWithRetry(ctx, func(tx *gorm.DB) error {
		_, applyErr := applyRoutingMetricRollupsTx(ctx, tx, normalized)
		return applyErr
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func runRoutingMetricTransactionWithRetry(ctx context.Context, operation func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < routingMetricTransactionMaxAttempts; attempt++ {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		err = DB.WithContext(ctx).Transaction(operation)
		if err == nil || !isRetryableRoutingMetricTransactionError(err) {
			return err
		}
		if attempt == routingMetricTransactionMaxAttempts-1 {
			break
		}
		delay := time.Duration(attempt+1) * 10 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func isRetryableRoutingMetricTransactionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"deadlock found when trying to get lock",
		"deadlock detected",
		"sqlstate 40p01",
		"sqlstate 40001",
		"could not serialize access",
		"serialization failure",
		"lock wait timeout exceeded",
		"error 1205",
		"error 1213",
		"database is locked",
		"database table is locked",
		"sqlite_busy",
		"sqlite_locked",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func normalizeRoutingMetricRollups(rollups []RoutingMetricRollup) ([]RoutingMetricRollup, error) {
	return normalizeRoutingMetricRollupsAt(rollups, time.Now())
}

func normalizeRoutingMetricRollupsAt(rollups []RoutingMetricRollup, now time.Time) ([]RoutingMetricRollup, error) {
	if len(rollups) == 0 {
		return nil, nil
	}
	if len(rollups) > RoutingMetricRollupMaxBatch {
		return nil, ErrRoutingMetricRollupBatchTooLarge
	}
	normalized := append([]RoutingMetricRollup(nil), rollups...)
	type rollupKey struct {
		memberID         int
		credentialID     int
		modelKey         string
		bucketTs         int64
		snapshotRevision int64
	}
	seen := make(map[rollupKey]struct{}, len(normalized))
	for index := range normalized {
		rollup := &normalized[index]
		rollup.ModelKey = routingMetricRollupModelKey(rollup.ModelName)
		if rollup.SchemaVersion == 0 {
			rollup.SchemaVersion = RoutingMetricRollupSchemaVersion
		}
		if err := validateRoutingMetricRollup(rollup); err != nil {
			return nil, fmt.Errorf("%w at index %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		if err := validateRoutingMetricRollupIngressTime(rollup, now); err != nil {
			return nil, fmt.Errorf("%w at index %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		key := rollupKey{
			memberID:         rollup.MemberID,
			credentialID:     rollup.CredentialID,
			modelKey:         rollup.ModelKey,
			bucketTs:         rollup.BucketTs,
			snapshotRevision: rollup.LastSnapshotRevision,
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w at index %d", ErrRoutingMetricRollupDuplicateKey, index)
		}
		seen[key] = struct{}{}
	}
	sortRoutingMetricRollupsByKey(normalized)
	return normalized, nil
}

func sortRoutingMetricRollupsByKey(rollups []RoutingMetricRollup) {
	sort.Slice(rollups, func(leftIndex int, rightIndex int) bool {
		left := rollups[leftIndex]
		right := rollups[rightIndex]
		if left.MemberID != right.MemberID {
			return left.MemberID < right.MemberID
		}
		if left.CredentialID != right.CredentialID {
			return left.CredentialID < right.CredentialID
		}
		if left.ModelKey != right.ModelKey {
			return left.ModelKey < right.ModelKey
		}
		if left.BucketTs != right.BucketTs {
			return left.BucketTs < right.BucketTs
		}
		return left.LastSnapshotRevision < right.LastSnapshotRevision
	})
}

func applyRoutingMetricRollupsTx(ctx context.Context, tx *gorm.DB, rollups []RoutingMetricRollup) (int, error) {
	sortRoutingMetricRollupsByKey(rollups)
	accepted, err := filterActiveRoutingMetricRollupsTx(ctx, tx, rollups)
	if err != nil || len(accepted) == 0 {
		return 0, err
	}
	rollups = accepted
	placeholders := make([]RoutingMetricRollup, 0, len(rollups))
	for index := range rollups {
		rollup := rollups[index]
		placeholders = append(placeholders, RoutingMetricRollup{
			MemberID:             rollup.MemberID,
			CredentialID:         rollup.CredentialID,
			ModelName:            rollup.ModelName,
			ModelKey:             rollup.ModelKey,
			BucketTs:             rollup.BucketTs,
			ChannelID:            rollup.ChannelID,
			ChannelGeneration:    rollup.ChannelGeneration,
			PoolID:               rollup.PoolID,
			SchemaVersion:        rollup.SchemaVersion,
			LastSnapshotRevision: rollup.LastSnapshotRevision,
		})
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "member_id"},
			{Name: "credential_id"},
			{Name: "model_key"},
			{Name: "bucket_ts"},
			{Name: "last_snapshot_revision"},
		},
		DoNothing: true,
	}).CreateInBatches(&placeholders, RoutingMetricRollupMaxBatch).Error; err != nil {
		return 0, err
	}

	for index := range rollups {
		incoming := &rollups[index]
		var current RoutingMetricRollup
		if err := lockForUpdate(tx.WithContext(ctx)).Where(
			"member_id = ? AND credential_id = ? AND model_key = ? AND bucket_ts = ? AND last_snapshot_revision = ?",
			incoming.MemberID, incoming.CredentialID, incoming.ModelKey, incoming.BucketTs,
			incoming.LastSnapshotRevision,
		).First(&current).Error; err != nil {
			return 0, err
		}
		if err := mergeRoutingMetricRollup(&current, incoming); err != nil {
			return 0, err
		}
		if err := tx.WithContext(ctx).Model(&RoutingMetricRollup{}).Where("id = ?", current.ID).Updates(map[string]any{
			"channel_id":                current.ChannelID,
			"channel_generation":        current.ChannelGeneration,
			"pool_id":                   current.PoolID,
			"schema_version":            current.SchemaVersion,
			"sketch_codec_version":      current.SketchCodecVersion,
			"latency_sample_count":      current.LatencySampleCount,
			"latency_sketch":            current.LatencySketch,
			"ttft_sample_count":         current.TtftSampleCount,
			"ttft_sketch":               current.TtftSketch,
			"request_count":             current.RequestCount,
			"success_count":             current.SuccessCount,
			"failure_count":             current.FailureCount,
			"unknown_count":             current.UnknownCount,
			"reliability_request_count": current.ReliabilityRequestCount,
			"reliability_failure_count": current.ReliabilityFailureCount,
			"total_latency_ms":          current.TotalLatencyMs,
			"ttft_sum_ms":               current.TtftSumMs,
			"ttft_count":                current.TtftCount,
			"output_tokens":             current.OutputTokens,
			"generation_ms":             current.GenerationMs,
			"err_4xx":                   current.Err4xx,
			"err_5xx":                   current.Err5xx,
			"err_429":                   current.Err429,
			"err_529":                   current.Err529,
			"retry_after_count":         current.RetryAfterCount,
			"retry_after_total_ms":      current.RetryAfterTotalMs,
		}).Error; err != nil {
			return 0, err
		}
	}
	return len(rollups), nil
}

func filterActiveRoutingMetricRollupsTx(
	ctx context.Context,
	tx *gorm.DB,
	rollups []RoutingMetricRollup,
) ([]RoutingMetricRollup, error) {
	if !routingGenerationFencingAvailable(tx) {
		return rollups, nil
	}
	memberIDs := make([]int, 0, len(rollups))
	seenMembers := make(map[int]struct{}, len(rollups))
	credentialIDs := make([]int, 0, len(rollups))
	seenCredentials := make(map[int]struct{}, len(rollups))
	for index := range rollups {
		rollup := rollups[index]
		if _, exists := seenMembers[rollup.MemberID]; !exists {
			seenMembers[rollup.MemberID] = struct{}{}
			memberIDs = append(memberIDs, rollup.MemberID)
		}
		if rollup.CredentialID > 0 {
			if _, exists := seenCredentials[rollup.CredentialID]; !exists {
				seenCredentials[rollup.CredentialID] = struct{}{}
				credentialIDs = append(credentialIDs, rollup.CredentialID)
			}
		}
	}
	sort.Ints(memberIDs)
	sort.Ints(credentialIDs)

	var candidateMembers []RoutingPoolMember
	if err := tx.WithContext(ctx).Select("id", "channel_id").Where("id IN ?", memberIDs).
		Order("channel_id asc").Find(&candidateMembers).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]int, 0, len(candidateMembers))
	seenChannels := make(map[int]struct{}, len(candidateMembers))
	for index := range candidateMembers {
		channelID := candidateMembers[index].ChannelID
		if _, exists := seenChannels[channelID]; !exists {
			seenChannels[channelID] = struct{}{}
			channelIDs = append(channelIDs, channelID)
		}
	}
	sort.Ints(channelIDs)

	var channels []Channel
	if len(channelIDs) > 0 {
		query := tx.WithContext(ctx).Select("id", "routing_generation").Where("id IN ?", channelIDs).Order("id asc")
		if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&channels).Error; err != nil {
			return nil, err
		}
	}
	channelsByID := make(map[int]Channel, len(channels))
	for index := range channels {
		channelsByID[channels[index].Id] = channels[index]
	}

	var members []RoutingPoolMember
	if err := tx.WithContext(ctx).
		Where("id IN ? AND active = ?", memberIDs, true).Order("id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	membersByID := make(map[int]RoutingPoolMember, len(members))
	for index := range members {
		membersByID[members[index].ID] = members[index]
	}

	credentialsByID := make(map[int]RoutingCredentialRef, len(credentialIDs))
	if len(credentialIDs) > 0 {
		var credentials []RoutingCredentialRef
		if err := tx.WithContext(ctx).
			Where("id IN ? AND active = ?", credentialIDs, true).Order("id asc").Find(&credentials).Error; err != nil {
			return nil, err
		}
		for index := range credentials {
			credentialsByID[credentials[index].ID] = credentials[index]
		}
	}

	accepted := make([]RoutingMetricRollup, 0, len(rollups))
	for index := range rollups {
		rollup := rollups[index]
		member, memberActive := membersByID[rollup.MemberID]
		channel, channelActive := channelsByID[rollup.ChannelID]
		if !memberActive || !channelActive || member.PoolID != rollup.PoolID ||
			member.ChannelID != rollup.ChannelID || !validRoutingIdentity(member.ChannelGeneration) ||
			channel.RoutingGeneration != member.ChannelGeneration ||
			(rollup.ChannelGeneration != "" && rollup.ChannelGeneration != member.ChannelGeneration) {
			continue
		}
		if rollup.CredentialID > 0 {
			credential, credentialActive := credentialsByID[rollup.CredentialID]
			if !credentialActive || credential.ChannelID != rollup.ChannelID ||
				credential.ChannelGeneration != member.ChannelGeneration {
				continue
			}
		}
		rollup.ChannelGeneration = member.ChannelGeneration
		accepted = append(accepted, rollup)
	}
	return accepted, nil
}

func mergeRoutingMetricRollup(current *RoutingMetricRollup, incoming *RoutingMetricRollup) error {
	if current.LastSnapshotRevision != incoming.LastSnapshotRevision {
		return fmt.Errorf("%w: snapshot revision mismatch", ErrRoutingMetricRollupInvalid)
	}
	if current.ChannelGeneration == "" {
		current.ChannelGeneration = incoming.ChannelGeneration
	} else if incoming.ChannelGeneration != current.ChannelGeneration {
		return fmt.Errorf("%w: channel generation mismatch", ErrRoutingMetricRollupInvalid)
	}
	codecVersion := current.SketchCodecVersion
	if codecVersion == 0 {
		codecVersion = incoming.SketchCodecVersion
	} else if incoming.SketchCodecVersion != 0 && incoming.SketchCodecVersion != codecVersion {
		return fmt.Errorf("%w: sketch codec version mismatch", ErrRoutingMetricRollupInvalid)
	}

	latencySketch, latencyCount, err := mergeRoutingDurationSketch(
		current.LatencySketch, current.LatencySampleCount, current.SketchCodecVersion,
		incoming.LatencySketch, incoming.LatencySampleCount, incoming.SketchCodecVersion,
	)
	if err != nil {
		return fmt.Errorf("%w: latency sketch: %v", ErrRoutingMetricRollupInvalid, err)
	}
	ttftSketch, ttftCount, err := mergeRoutingDurationSketch(
		current.TtftSketch, current.TtftSampleCount, current.SketchCodecVersion,
		incoming.TtftSketch, incoming.TtftSampleCount, incoming.SketchCodecVersion,
	)
	if err != nil {
		return fmt.Errorf("%w: ttft sketch: %v", ErrRoutingMetricRollupInvalid, err)
	}

	targets := []*int64{
		&current.RequestCount,
		&current.SuccessCount,
		&current.FailureCount,
		&current.UnknownCount,
		&current.ReliabilityRequestCount,
		&current.ReliabilityFailureCount,
		&current.TotalLatencyMs,
		&current.TtftSumMs,
		&current.TtftCount,
		&current.OutputTokens,
		&current.GenerationMs,
		&current.Err4xx,
		&current.Err5xx,
		&current.Err429,
		&current.Err529,
		&current.RetryAfterCount,
		&current.RetryAfterTotalMs,
	}
	deltas := []int64{
		incoming.RequestCount,
		incoming.SuccessCount,
		incoming.FailureCount,
		incoming.UnknownCount,
		incoming.ReliabilityRequestCount,
		incoming.ReliabilityFailureCount,
		incoming.TotalLatencyMs,
		incoming.TtftSumMs,
		incoming.TtftCount,
		incoming.OutputTokens,
		incoming.GenerationMs,
		incoming.Err4xx,
		incoming.Err5xx,
		incoming.Err429,
		incoming.Err529,
		incoming.RetryAfterCount,
		incoming.RetryAfterTotalMs,
	}
	for index, delta := range deltas {
		if *targets[index] < 0 || delta < 0 || *targets[index] > math.MaxInt64-delta {
			return ErrRoutingMetricRollupOverflow
		}
		*targets[index] += delta
	}

	current.ChannelID = incoming.ChannelID
	current.PoolID = incoming.PoolID
	if current.SchemaVersion < RoutingMetricRollupSchemaVersion || incoming.SchemaVersion < RoutingMetricRollupSchemaVersion {
		if current.SchemaVersion >= RoutingMetricRollupSchemaVersion {
			current.SchemaVersion = incoming.SchemaVersion
		} else if incoming.SchemaVersion < RoutingMetricRollupSchemaVersion {
			current.SchemaVersion = max(current.SchemaVersion, incoming.SchemaVersion)
		}
	} else {
		current.SchemaVersion = RoutingMetricRollupSchemaVersion
	}
	current.SketchCodecVersion = codecVersion
	current.LatencySampleCount = latencyCount
	current.LatencySketch = latencySketch
	current.TtftSampleCount = ttftCount
	current.TtftSketch = ttftSketch
	if err := validateRoutingMetricRollup(current); err != nil {
		return fmt.Errorf("%w: merged rollup: %v", ErrRoutingMetricRollupInvalid, err)
	}
	return nil
}

func mergeRoutingDurationSketch(
	currentData []byte,
	currentCount int64,
	currentVersion int,
	incomingData []byte,
	incomingCount int64,
	incomingVersion int,
) ([]byte, int64, error) {
	if currentCount == 0 && incomingCount == 0 {
		return nil, 0, nil
	}
	var merged *routingdistribution.DurationSketch
	if currentCount > 0 {
		decoded, err := routingdistribution.Decode(currentData, currentVersion)
		if err != nil || decoded.Count() != currentCount {
			return nil, 0, ErrRoutingMetricRollupInvalid
		}
		merged = decoded
	}
	if incomingCount > 0 {
		decoded, err := routingdistribution.Decode(incomingData, incomingVersion)
		if err != nil || decoded.Count() != incomingCount {
			return nil, 0, ErrRoutingMetricRollupInvalid
		}
		if merged == nil {
			merged = decoded
		} else if err := merged.Merge(decoded); err != nil {
			return nil, 0, err
		}
	}
	data, err := merged.Marshal()
	if err != nil {
		return nil, 0, err
	}
	return data, merged.Count(), nil
}

func GetRoutingMetricRollupsContext(
	ctx context.Context,
	memberID int,
	credentialID int,
	modelName string,
	startTs int64,
	endTs int64,
) ([]RoutingMetricRollup, error) {
	if memberID <= 0 || credentialID < 0 || !validRoutingMetricRollupModelName(modelName) || startTs < 0 || endTs < startTs {
		return nil, ErrRoutingMetricRollupInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var rollups []RoutingMetricRollup
	err := DB.WithContext(ctx).
		Where("member_id = ? AND credential_id = ? AND model_key = ? AND bucket_ts >= ? AND bucket_ts <= ?",
			memberID, credentialID, routingMetricRollupModelKey(modelName), startTs, endTs).
		Order("bucket_ts asc").
		Order("last_snapshot_revision asc").
		Order("id asc").
		Limit(RoutingMetricRollupMaxQueryLimit).
		Find(&rollups).Error
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return rollups, err
}

// GetRoutingMetricRollupsForSnapshotRevisionContext returns only schema-v3
// buckets whose physical key carries the requested exact snapshot revision.
// Legacy v1/v2 rows remain available through GetRoutingMetricRollupsContext but
// are intentionally excluded from revision-isolated reads.
func GetRoutingMetricRollupsForSnapshotRevisionContext(
	ctx context.Context,
	memberID int,
	credentialID int,
	modelName string,
	snapshotRevision int64,
	startTs int64,
	endTs int64,
) ([]RoutingMetricRollup, error) {
	if memberID <= 0 || credentialID < 0 || !validRoutingMetricRollupModelName(modelName) ||
		snapshotRevision <= 0 || startTs < 0 || endTs < startTs {
		return nil, ErrRoutingMetricRollupInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var rollups []RoutingMetricRollup
	err := DB.WithContext(ctx).
		Where(
			"member_id = ? AND credential_id = ? AND model_key = ? AND last_snapshot_revision = ? "+
				"AND schema_version = ? AND bucket_ts >= ? AND bucket_ts <= ?",
			memberID, credentialID, routingMetricRollupModelKey(modelName), snapshotRevision,
			RoutingMetricRollupSchemaVersion, startTs, endTs,
		).
		Order("bucket_ts asc").
		Order("id asc").
		Limit(RoutingMetricRollupMaxQueryLimit).
		Find(&rollups).Error
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return rollups, err
}

func GetRoutingMetricRollupsSinceContext(ctx context.Context, cutoffTs int64, limit int) ([]RoutingMetricRollup, error) {
	if cutoffTs < 0 {
		return nil, ErrRoutingMetricRollupInvalid
	}
	if limit <= 0 {
		limit = RoutingMetricRollupDefaultQueryLimit
	}
	if limit > RoutingMetricRollupMaxQueryLimit {
		return nil, ErrRoutingMetricRollupQueryTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var rollups []RoutingMetricRollup
	err := DB.WithContext(ctx).
		Where("bucket_ts >= ?", cutoffTs).
		Order("member_id asc").
		Order("credential_id asc").
		Order("model_name asc").
		Order("bucket_ts asc").
		Order("last_snapshot_revision asc").
		Order("id asc").
		Limit(limit).
		Find(&rollups).Error
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return rollups, err
}

func DeleteRoutingMetricRollupsBefore(cutoffTs int64) (int64, error) {
	return DeleteRoutingMetricRollupsBeforeContext(context.Background(), cutoffTs)
}

func DeleteRoutingMetricRollupsBeforeContext(ctx context.Context, cutoffTs int64) (int64, error) {
	if cutoffTs <= 0 {
		return 0, nil
	}

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []int
		if err := DB.WithContext(ctx).Model(&RoutingMetricRollup{}).
			Where("bucket_ts < ?", cutoffTs).
			Order("bucket_ts asc").
			Order("id asc").
			Limit(routingMetricRollupRetentionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}

		result := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingMetricRollup{})
		total += result.RowsAffected
		if result.Error != nil {
			return total, result.Error
		}
		if len(ids) < routingMetricRollupRetentionBatchSize {
			return total, nil
		}
	}
}

func validateRoutingMetricRollup(rollup *RoutingMetricRollup) error {
	if rollup.MemberID <= 0 || rollup.CredentialID < 0 || rollup.ChannelID <= 0 || rollup.PoolID <= 0 {
		return errors.New("member, channel, and pool IDs must be positive and credential ID must be non-negative")
	}
	if !validRoutingMetricRollupModelName(rollup.ModelName) {
		return errors.New("model name is empty, invalid, or too long")
	}
	if rollup.ModelKey != routingMetricRollupModelKey(rollup.ModelName) {
		return errors.New("model key does not match model name")
	}
	if rollup.BucketTs < 0 || rollup.SchemaVersion < 1 || rollup.SchemaVersion > RoutingMetricRollupSchemaVersion ||
		rollup.LastSnapshotRevision < 0 {
		return errors.New("bucket, schema version, or snapshot revision is invalid")
	}
	if rollup.SchemaVersion == RoutingMetricRollupSchemaVersion && rollup.LastSnapshotRevision == 0 {
		return errors.New("current schema requires an exact positive snapshot revision")
	}
	if rollup.ChannelGeneration != "" && !validRoutingIdentity(rollup.ChannelGeneration) {
		return errors.New("channel generation is invalid")
	}
	for _, counter := range []int64{
		rollup.RequestCount,
		rollup.SuccessCount,
		rollup.FailureCount,
		rollup.UnknownCount,
		rollup.ReliabilityRequestCount,
		rollup.ReliabilityFailureCount,
		rollup.TotalLatencyMs,
		rollup.TtftSumMs,
		rollup.TtftCount,
		rollup.OutputTokens,
		rollup.GenerationMs,
		rollup.Err4xx,
		rollup.Err5xx,
		rollup.Err429,
		rollup.Err529,
		rollup.RetryAfterCount,
		rollup.RetryAfterTotalMs,
	} {
		if counter < 0 {
			return errors.New("additive counters must be non-negative")
		}
	}
	for _, counter := range []int64{
		rollup.RequestCount,
		rollup.SuccessCount,
		rollup.FailureCount,
		rollup.UnknownCount,
		rollup.ReliabilityRequestCount,
		rollup.ReliabilityFailureCount,
		rollup.TtftCount,
		rollup.Err4xx,
		rollup.Err5xx,
		rollup.Err429,
		rollup.Err529,
		rollup.RetryAfterCount,
		rollup.LatencySampleCount,
		rollup.TtftSampleCount,
	} {
		if counter > routingMetricMaxCounter {
			return errors.New("metric counters exceed the supported per-bucket limit")
		}
	}
	if rollup.SuccessCount > rollup.RequestCount || rollup.FailureCount > rollup.RequestCount ||
		rollup.SuccessCount > rollup.RequestCount-rollup.FailureCount ||
		rollup.UnknownCount > rollup.FailureCount ||
		rollup.ReliabilityRequestCount > rollup.RequestCount ||
		rollup.ReliabilityFailureCount > rollup.ReliabilityRequestCount ||
		rollup.TtftCount > rollup.RequestCount || rollup.RetryAfterCount > rollup.RequestCount {
		return errors.New("metric counters violate request classification invariants")
	}
	errorCount := int64(0)
	for _, count := range []int64{rollup.Err4xx, rollup.Err5xx, rollup.Err429, rollup.Err529} {
		if count > rollup.RequestCount-errorCount {
			return errors.New("error counters exceed request count")
		}
		errorCount += count
	}
	if !routingMetricTotalWithinBound(rollup.TotalLatencyMs, rollup.RequestCount, routingdistribution.MaxDurationMilliseconds) ||
		!routingMetricTotalWithinBound(rollup.TtftSumMs, rollup.TtftCount, routingdistribution.MaxDurationMilliseconds) ||
		!routingMetricTotalWithinBound(rollup.GenerationMs, rollup.RequestCount, routingdistribution.MaxDurationMilliseconds) ||
		!routingMetricTotalWithinBound(rollup.OutputTokens, rollup.RequestCount, routingMetricMaxAttemptTokens) ||
		!routingMetricTotalWithinBound(rollup.RetryAfterTotalMs, rollup.RetryAfterCount, routingMetricMaxRetryAfterMs) {
		return errors.New("metric totals exceed their bounded counter products")
	}
	if rollup.LatencySampleCount > rollup.RequestCount || rollup.TtftSampleCount > rollup.TtftCount {
		return errors.New("distribution sample counts exceed metric counters")
	}
	if err := validateRoutingMetricSketch(rollup.LatencySketch, rollup.LatencySampleCount, rollup.SketchCodecVersion); err != nil {
		return fmt.Errorf("latency sketch: %w", err)
	}
	if err := validateRoutingMetricSketch(rollup.TtftSketch, rollup.TtftSampleCount, rollup.SketchCodecVersion); err != nil {
		return fmt.Errorf("ttft sketch: %w", err)
	}
	if rollup.LatencySampleCount == 0 && rollup.TtftSampleCount == 0 && rollup.SketchCodecVersion != 0 {
		return errors.New("sketch codec version is set without distribution samples")
	}
	return nil
}

func routingMetricTotalWithinBound(total int64, count int64, perItemMax int64) bool {
	if total < 0 || count < 0 || perItemMax <= 0 {
		return false
	}
	if count == 0 {
		return total == 0
	}
	if count > math.MaxInt64/perItemMax {
		return true
	}
	return total <= count*perItemMax
}

func validateRoutingMetricRollupIngressTime(rollup *RoutingMetricRollup, now time.Time) error {
	nowUnix := now.Unix()
	maxFutureBucketTs := int64(math.MaxInt64)
	futureSkewSeconds := int64(routingMetricRollupFutureSkew / time.Second)
	if nowUnix <= math.MaxInt64-futureSkewSeconds {
		maxFutureBucketTs = nowUnix + futureSkewSeconds
	}
	if rollup.BucketTs > maxFutureBucketTs {
		return errors.New("bucket timestamp is too far in the future")
	}
	return nil
}

func validateRoutingMetricSketch(data []byte, sampleCount int64, codecVersion int) error {
	if sampleCount < 0 {
		return errors.New("sample count must be non-negative")
	}
	if sampleCount == 0 {
		if len(data) != 0 {
			return errors.New("sketch payload is set without samples")
		}
		return nil
	}
	if codecVersion <= 0 || len(data) == 0 {
		return errors.New("sketch codec and payload are required")
	}
	sketch, err := routingdistribution.Decode(data, codecVersion)
	if err != nil {
		return err
	}
	if sketch.Count() != sampleCount {
		return errors.New("sketch count does not match sample count")
	}
	return nil
}

func validRoutingMetricRollupModelName(modelName string) bool {
	return modelName != "" &&
		utf8.ValidString(modelName) &&
		utf8.RuneCountInString(modelName) <= routingMetricRollupModelNameMaxRunes &&
		len(modelName) <= routingMetricRollupModelNameMaxBytes
}

func routingMetricRollupModelKey(modelName string) string {
	sum := sha256.Sum256([]byte(modelName))
	return hex.EncodeToString(sum[:])
}

func RoutingMetricRollupModelKey(modelName string) string {
	return routingMetricRollupModelKey(modelName)
}
