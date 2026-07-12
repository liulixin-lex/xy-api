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

	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingMetricRollupSchemaVersion     = 2
	RoutingMetricRollupMaxBatch          = 500
	RoutingMetricRollupDefaultQueryLimit = 1_000
	RoutingMetricRollupMaxQueryLimit     = 200_000

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
	ErrRoutingMetricRollupBatchTooLarge = errors.New("routing metric rollup batch exceeds limit")
	ErrRoutingMetricRollupDuplicateKey  = errors.New("routing metric rollup batch contains duplicate key")
	ErrRoutingMetricRollupInvalid       = errors.New("invalid routing metric rollup")
	ErrRoutingMetricRollupQueryTooLarge = errors.New("routing metric rollup query exceeds limit")
	ErrRoutingMetricRollupOverflow      = errors.New("routing metric rollup counter overflow")
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

func (RoutingMetricRollup) TableName() string {
	return "routing_metric_rollups"
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
		return applyRoutingMetricRollupsTx(ctx, tx, normalized)
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
		memberID     int
		credentialID int
		modelKey     string
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
			return nil, fmt.Errorf("%w at index %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		if err := validateRoutingMetricRollupIngressTime(rollup, now); err != nil {
			return nil, fmt.Errorf("%w at index %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		key := rollupKey{
			memberID:     rollup.MemberID,
			credentialID: rollup.CredentialID,
			modelKey:     rollup.ModelKey,
			bucketTs:     rollup.BucketTs,
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
		return left.BucketTs < right.BucketTs
	})
}

func applyRoutingMetricRollupsTx(ctx context.Context, tx *gorm.DB, rollups []RoutingMetricRollup) error {
	sortRoutingMetricRollupsByKey(rollups)
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
			PoolID:               rollup.PoolID,
			SchemaVersion:        1,
			LastSnapshotRevision: 0,
		})
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "member_id"},
			{Name: "credential_id"},
			{Name: "model_key"},
			{Name: "bucket_ts"},
		},
		DoNothing: true,
	}).CreateInBatches(&placeholders, RoutingMetricRollupMaxBatch).Error; err != nil {
		return err
	}

	for index := range rollups {
		incoming := &rollups[index]
		var current RoutingMetricRollup
		if err := lockForUpdate(tx.WithContext(ctx)).Where(
			"member_id = ? AND credential_id = ? AND model_key = ? AND bucket_ts = ?",
			incoming.MemberID, incoming.CredentialID, incoming.ModelKey, incoming.BucketTs,
		).First(&current).Error; err != nil {
			return err
		}
		if err := mergeRoutingMetricRollup(&current, incoming); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Model(&RoutingMetricRollup{}).Where("id = ?", current.ID).Updates(map[string]any{
			"channel_id":                current.ChannelID,
			"pool_id":                   current.PoolID,
			"schema_version":            current.SchemaVersion,
			"last_snapshot_revision":    current.LastSnapshotRevision,
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
			return err
		}
	}
	return nil
}

func mergeRoutingMetricRollup(current *RoutingMetricRollup, incoming *RoutingMetricRollup) error {
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
	current.SchemaVersion = max(current.SchemaVersion, incoming.SchemaVersion)
	current.LastSnapshotRevision = max(current.LastSnapshotRevision, incoming.LastSnapshotRevision)
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
	if rollup.BucketTs < 0 ||
		(rollup.SchemaVersion != 1 && rollup.SchemaVersion != RoutingMetricRollupSchemaVersion) ||
		rollup.LastSnapshotRevision < 0 {
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
