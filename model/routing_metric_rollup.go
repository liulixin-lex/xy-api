package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingMetricRollupSchemaVersion     = 1
	RoutingMetricRollupMaxBatch          = 500
	RoutingMetricRollupDefaultQueryLimit = 1_000
	RoutingMetricRollupMaxQueryLimit     = 200_000

	routingMetricRollupRetentionBatchSize = 500
	routingMetricRollupModelNameMaxRunes  = 128
	routingMetricRollupModelNameMaxBytes  = 512
)

var (
	ErrRoutingMetricRollupBatchTooLarge = errors.New("routing metric rollup batch exceeds limit")
	ErrRoutingMetricRollupDuplicateKey  = errors.New("routing metric rollup batch contains duplicate key")
	ErrRoutingMetricRollupInvalid       = errors.New("invalid routing metric rollup")
	ErrRoutingMetricRollupQueryTooLarge = errors.New("routing metric rollup query exceeds limit")
)

// RoutingMetricRollup stores mergeable counters keyed by stable routing identities.
// Percentiles are intentionally excluded because scalar percentiles are not mergeable.
type RoutingMetricRollup struct {
	ID                   int    `json:"id" gorm:"primaryKey"`
	MemberID             int    `json:"member_id" gorm:"not null;uniqueIndex:idx_routing_metric_rollup_key,priority:1;index"`
	CredentialID         int    `json:"credential_id" gorm:"not null;uniqueIndex:idx_routing_metric_rollup_key,priority:2;index"`
	ModelName            string `json:"model_name" gorm:"type:varchar(128);not null;index"`
	ModelKey             string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_metric_rollup_key,priority:3"`
	BucketTs             int64  `json:"bucket_ts" gorm:"bigint;not null;uniqueIndex:idx_routing_metric_rollup_key,priority:4;index:idx_routing_metric_rollup_bucket_ts"`
	ChannelID            int    `json:"channel_id" gorm:"not null;index"`
	PoolID               int    `json:"pool_id" gorm:"not null;index"`
	SchemaVersion        int    `json:"schema_version" gorm:"not null"`
	LastSnapshotRevision int64  `json:"last_snapshot_revision" gorm:"bigint;not null;index"`

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

func (RoutingMetricRollup) TableName() string {
	return "routing_metric_rollups"
}

func UpsertRoutingMetricRollups(rollups []RoutingMetricRollup) error {
	return UpsertRoutingMetricRollupsContext(context.Background(), rollups)
}

func UpsertRoutingMetricRollupsContext(ctx context.Context, rollups []RoutingMetricRollup) error {
	if len(rollups) == 0 {
		return nil
	}
	if len(rollups) > RoutingMetricRollupMaxBatch {
		return ErrRoutingMetricRollupBatchTooLarge
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	normalized := append([]RoutingMetricRollup(nil), rollups...)
	type rollupKey struct {
		memberID     int
		credentialID int
		modelName    string
		bucketTs     int64
	}
	seen := make(map[rollupKey]struct{}, len(normalized))
	for index := range normalized {
		rollup := &normalized[index]
		rollup.ModelKey = routingMetricRollupModelKey(rollup.ModelName)
		if rollup.SchemaVersion == 0 {
			rollup.SchemaVersion = RoutingMetricRollupSchemaVersion
		}
		if err := validateRoutingMetricRollup(rollup); err != nil {
			return fmt.Errorf("%w at index %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		key := rollupKey{
			memberID:     rollup.MemberID,
			credentialID: rollup.CredentialID,
			modelName:    rollup.ModelName,
			bucketTs:     rollup.BucketTs,
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w at index %d", ErrRoutingMetricRollupDuplicateKey, index)
		}
		seen[key] = struct{}{}
	}

	db := DB.WithContext(ctx)
	incomingColumn := func(column string) string {
		if db.Dialector.Name() == "mysql" {
			return "VALUES(" + column + ")"
		}
		return "excluded." + column
	}
	currentColumn := func(column string) string {
		return "routing_metric_rollups." + column
	}

	updates := clause.Set{
		{Column: clause.Column{Name: "channel_id"}, Value: gorm.Expr(incomingColumn("channel_id"))},
		{Column: clause.Column{Name: "pool_id"}, Value: gorm.Expr(incomingColumn("pool_id"))},
		{
			Column: clause.Column{Name: "schema_version"},
			Value: gorm.Expr(
				"CASE WHEN " + currentColumn("schema_version") + " > " + incomingColumn("schema_version") + " THEN " + currentColumn("schema_version") + " ELSE " + incomingColumn("schema_version") + " END",
			),
		},
		{
			Column: clause.Column{Name: "last_snapshot_revision"},
			Value: gorm.Expr(
				"CASE WHEN " + currentColumn("last_snapshot_revision") + " > " + incomingColumn("last_snapshot_revision") + " THEN " + currentColumn("last_snapshot_revision") + " ELSE " + incomingColumn("last_snapshot_revision") + " END",
			),
		},
	}
	for _, column := range []string{
		"request_count",
		"success_count",
		"failure_count",
		"unknown_count",
		"reliability_request_count",
		"reliability_failure_count",
		"total_latency_ms",
		"ttft_sum_ms",
		"ttft_count",
		"output_tokens",
		"generation_ms",
		"err_4xx",
		"err_5xx",
		"err_429",
		"err_529",
		"retry_after_count",
		"retry_after_total_ms",
	} {
		updates = append(updates, clause.Assignment{
			Column: clause.Column{Name: column},
			Value:  gorm.Expr(currentColumn(column) + " + " + incomingColumn(column)),
		})
	}

	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "member_id"},
			{Name: "credential_id"},
			{Name: "model_key"},
			{Name: "bucket_ts"},
		},
		DoUpdates: updates,
	}).CreateInBatches(&normalized, RoutingMetricRollupMaxBatch).Error
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
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
	if rollup.BucketTs < 0 || rollup.SchemaVersion <= 0 || rollup.LastSnapshotRevision < 0 {
		return errors.New("bucket, schema version, or snapshot revision is invalid")
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
