package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingEndpointEvidenceMaxBatch = 500
	RoutingEndpointAggregateMaxRows = 50_000
	routingEndpointRetentionBatch   = 500
)

var (
	ErrRoutingEndpointEvidenceInvalid = errors.New("invalid channel routing endpoint evidence")
	ErrRoutingEndpointStateInvalid    = errors.New("invalid channel routing shared endpoint state")
	ErrRoutingEndpointAggregateLarge  = errors.New("channel routing endpoint evidence aggregate exceeds limit")
)

// RoutingEndpointEvidence stores one node epoch's absolute counters for an
// endpoint time bucket. Stable node identity is separate from process epoch so
// a restart cannot manufacture an additional quorum voter.
type RoutingEndpointEvidence struct {
	ID                   int64  `json:"id" gorm:"primaryKey"`
	NodeID               string `json:"node_id" gorm:"type:varchar(128);index;not null"`
	NodeKey              string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_evidence_key,priority:1"`
	NodeEpochID          string `json:"node_epoch_id" gorm:"type:varchar(128);index;not null"`
	NodeEpochKey         string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_evidence_key,priority:2"`
	QuorumEligible       bool   `json:"quorum_eligible" gorm:"not null;index"`
	EndpointHost         string `json:"endpoint_host" gorm:"type:varchar(255);not null"`
	EndpointAuthority    string `json:"endpoint_authority" gorm:"type:varchar(320);not null"`
	EndpointAuthorityKey string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_evidence_key,priority:3;index"`
	Region               string `json:"region" gorm:"type:varchar(64);index;not null"`
	RegionKey            string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_evidence_key,priority:4"`
	ResetGeneration      int64  `json:"reset_generation" gorm:"bigint;index;default:0;not null"`
	BucketTs             int64  `json:"bucket_ts" gorm:"bigint;not null;uniqueIndex:idx_routing_endpoint_evidence_key,priority:5;index"`
	RequestCount         int64  `json:"request_count" gorm:"bigint;not null"`
	ReachableCount       int64  `json:"reachable_count" gorm:"bigint;not null"`
	NetworkFailureCount  int64  `json:"network_failure_count" gorm:"bigint;not null"`
	TotalLatencyMs       int64  `json:"total_latency_ms" gorm:"bigint;not null"`
	TtftSumMs            int64  `json:"ttft_sum_ms" gorm:"bigint;not null"`
	TtftCount            int64  `json:"ttft_count" gorm:"bigint;not null"`
	UpdatedTimeMs        int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
	CreatedTimeMs        int64  `json:"created_time_ms" gorm:"bigint;not null"`
}

func (RoutingEndpointEvidence) TableName() string {
	return "routing_endpoint_evidence"
}

type RoutingEndpointEvidenceAggregate struct {
	NodeID               string `json:"node_id"`
	NodeKey              string `json:"-"`
	EndpointHost         string `json:"endpoint_host"`
	EndpointAuthority    string `json:"endpoint_authority"`
	EndpointAuthorityKey string `json:"-"`
	Region               string `json:"region"`
	RegionKey            string `json:"-"`
	ResetGeneration      int64  `json:"reset_generation"`
	RequestCount         int64  `json:"request_count"`
	ReachableCount       int64  `json:"reachable_count"`
	NetworkFailureCount  int64  `json:"network_failure_count"`
	TotalLatencyMs       int64  `json:"total_latency_ms"`
	TtftSumMs            int64  `json:"ttft_sum_ms"`
	TtftCount            int64  `json:"ttft_count"`
	EvidenceThroughMs    int64  `json:"evidence_through_ms"`
}

// RoutingEndpointSharedState is the durable regional consensus result. Local
// endpoint breaker state remains process-local and is merged at selection time.
type RoutingEndpointSharedState struct {
	ID                   int64  `json:"id" gorm:"primaryKey"`
	EndpointHost         string `json:"endpoint_host" gorm:"type:varchar(255);not null"`
	EndpointAuthority    string `json:"endpoint_authority" gorm:"type:varchar(320);not null"`
	EndpointAuthorityKey string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_shared_key,priority:1"`
	Region               string `json:"region" gorm:"type:varchar(64);index;not null"`
	RegionKey            string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_endpoint_shared_key,priority:2"`
	ResetGeneration      int64  `json:"reset_generation" gorm:"bigint;index;default:0;not null"`
	State                string `json:"state" gorm:"type:varchar(32);index;not null"`
	Reason               string `json:"reason" gorm:"type:varchar(64);not null"`
	EvidenceCount        int64  `json:"evidence_count" gorm:"bigint;not null"`
	NetworkFailureCount  int64  `json:"network_failure_count" gorm:"bigint;not null"`
	NodeCount            int    `json:"node_count" gorm:"not null"`
	FailureNodeCount     int    `json:"failure_node_count" gorm:"not null"`
	CooldownUntilMs      int64  `json:"cooldown_until_ms" gorm:"bigint;not null"`
	EvidenceFromMs       int64  `json:"evidence_from_ms" gorm:"bigint;not null"`
	EvidenceThroughMs    int64  `json:"evidence_through_ms" gorm:"bigint;index;not null"`
	EvaluatedAtMs        int64  `json:"evaluated_at_ms" gorm:"bigint;not null"`
	ExpiresAtMs          int64  `json:"expires_at_ms" gorm:"bigint;index;not null"`
	CreatedTimeMs        int64  `json:"created_time_ms" gorm:"bigint;not null"`
	UpdatedTimeMs        int64  `json:"updated_time_ms" gorm:"bigint;not null"`
}

func (RoutingEndpointSharedState) TableName() string {
	return "routing_endpoint_shared_states"
}

func UpsertRoutingEndpointEvidenceContext(ctx context.Context, rows []RoutingEndpointEvidence) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if len(rows) > RoutingEndpointEvidenceMaxBatch {
		return 0, ErrRoutingEndpointEvidenceInvalid
	}
	nowMs, err := routingErrorBudgetDatabaseNowMs(DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	for index := range rows {
		row := &rows[index]
		row.NodeID = strings.TrimSpace(row.NodeID)
		row.NodeEpochID = strings.TrimSpace(row.NodeEpochID)
		row.EndpointHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(row.EndpointHost)), ".")
		row.EndpointAuthority = strings.ToLower(strings.TrimSpace(row.EndpointAuthority))
		row.Region = strings.ToLower(strings.TrimSpace(row.Region))
		if !validRoutingEndpointEvidence(*row, nowMs) {
			return 0, ErrRoutingEndpointEvidenceInvalid
		}
		row.NodeKey = routingEndpointHash(row.NodeID)
		row.NodeEpochKey = routingEndpointHash(row.NodeEpochID)
		row.EndpointAuthorityKey = routingEndpointHash(row.EndpointAuthority)
		row.RegionKey = routingEndpointHash(row.Region)
		row.UpdatedTimeMs = nowMs
		if row.CreatedTimeMs <= 0 {
			row.CreatedTimeMs = nowMs
		}
	}
	rowsAffected := int64(0)
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		targets := make(map[string]RoutingBreakerResetTarget, len(rows))
		for index := range rows {
			row := rows[index]
			target := RoutingBreakerResetTarget{
				Scope: RoutingBreakerResetScopeEndpoint, EndpointHost: row.EndpointHost,
				EndpointAuthority: row.EndpointAuthority, Region: row.Region,
			}
			_, targetKey, keyErr := normalizeRoutingBreakerResetTarget(target)
			if keyErr != nil {
				return keyErr
			}
			targets[targetKey] = target
		}
		keys := make([]string, 0, len(targets))
		for key := range targets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		generations := make(map[string]int64, len(keys))
		for _, key := range keys {
			fence, lockErr := lockRoutingBreakerResetFenceTx(ctx, tx, key, nowMs)
			if lockErr != nil {
				return lockErr
			}
			generations[key] = fence.Generation
		}
		accepted := rows[:0]
		for index := range rows {
			row := rows[index]
			targetKey, keyErr := routingBreakerResetEndpointTargetKey(row.EndpointAuthority, row.Region)
			if keyErr != nil {
				return keyErr
			}
			if row.ResetGeneration < generations[targetKey] {
				continue
			}
			if row.ResetGeneration != generations[targetKey] {
				return ErrRoutingEndpointEvidenceInvalid
			}
			accepted = append(accepted, row)
		}
		if len(accepted) == 0 {
			return nil
		}
		result := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "node_key"}, {Name: "node_epoch_key"}, {Name: "endpoint_authority_key"},
				{Name: "region_key"}, {Name: "bucket_ts"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"node_id", "node_epoch_id", "quorum_eligible", "endpoint_host", "endpoint_authority", "region",
				"reset_generation", "request_count", "reachable_count", "network_failure_count", "total_latency_ms",
				"ttft_sum_ms", "ttft_count", "updated_time_ms",
			}),
		}).CreateInBatches(&accepted, RoutingEndpointEvidenceMaxBatch)
		rowsAffected = result.RowsAffected
		return result.Error
	})
	return rowsAffected, err
}

func AggregateRoutingEndpointEvidenceContext(
	ctx context.Context,
	region string,
	bucketCutoff int64,
	updatedCutoffMs int64,
) ([]RoutingEndpointEvidenceAggregate, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	region = strings.ToLower(strings.TrimSpace(region))
	if !validRoutingEndpointText(region, 64) || bucketCutoff <= 0 || updatedCutoffMs <= 0 {
		return nil, 0, ErrRoutingEndpointEvidenceInvalid
	}
	nowMs, err := routingErrorBudgetDatabaseNowMs(DB.WithContext(ctx))
	if err != nil {
		return nil, 0, err
	}
	var rows []RoutingEndpointEvidenceAggregate
	err = DB.WithContext(ctx).Model(&RoutingEndpointEvidence{}).
		Select(
			"node_id, node_key, endpoint_host, endpoint_authority, endpoint_authority_key, region, region_key, reset_generation, "+
				"COALESCE(SUM(request_count), 0) AS request_count, "+
				"COALESCE(SUM(reachable_count), 0) AS reachable_count, "+
				"COALESCE(SUM(network_failure_count), 0) AS network_failure_count, "+
				"COALESCE(SUM(total_latency_ms), 0) AS total_latency_ms, "+
				"COALESCE(SUM(ttft_sum_ms), 0) AS ttft_sum_ms, "+
				"COALESCE(SUM(ttft_count), 0) AS ttft_count, MAX(updated_time_ms) AS evidence_through_ms",
		).
		Where("quorum_eligible = ? AND region = ? AND bucket_ts >= ? AND updated_time_ms >= ?", true, region, bucketCutoff, updatedCutoffMs).
		Group("node_id, node_key, endpoint_host, endpoint_authority, endpoint_authority_key, region, region_key, reset_generation").
		Order("endpoint_authority_key ASC, node_key ASC").
		Limit(RoutingEndpointAggregateMaxRows + 1).
		Find(&rows).Error
	if err != nil {
		return nil, nowMs, err
	}
	if len(rows) > RoutingEndpointAggregateMaxRows {
		return nil, nowMs, ErrRoutingEndpointAggregateLarge
	}
	for index := range rows {
		row := rows[index]
		if !validRoutingEndpointAggregate(row) {
			return nil, nowMs, ErrRoutingEndpointEvidenceInvalid
		}
	}
	return rows, nowMs, nil
}

func RoutingEndpointDatabaseNowMsContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return 0, ErrRoutingEndpointStateInvalid
	}
	return routingErrorBudgetDatabaseNowMs(DB.WithContext(ctx))
}

func RoutingEndpointSchemaReady() bool {
	return DB != nil && DB.Migrator().HasTable(&RoutingEndpointEvidence{}) &&
		DB.Migrator().HasTable(&RoutingEndpointSharedState{})
}

func UpsertRoutingEndpointSharedStatesContext(ctx context.Context, states []RoutingEndpointSharedState) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(states) == 0 {
		return nil
	}
	if len(states) > RoutingEndpointEvidenceMaxBatch {
		return ErrRoutingEndpointStateInvalid
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sort.Slice(states, func(left int, right int) bool {
			if states[left].EndpointAuthority != states[right].EndpointAuthority {
				return states[left].EndpointAuthority < states[right].EndpointAuthority
			}
			return states[left].Region < states[right].Region
		})
		for index := range states {
			state := &states[index]
			state.EndpointHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(state.EndpointHost)), ".")
			state.EndpointAuthority = strings.ToLower(strings.TrimSpace(state.EndpointAuthority))
			state.Region = strings.ToLower(strings.TrimSpace(state.Region))
			state.EndpointAuthorityKey = routingEndpointHash(state.EndpointAuthority)
			state.RegionKey = routingEndpointHash(state.Region)
			if !validRoutingEndpointSharedState(*state) {
				return ErrRoutingEndpointStateInvalid
			}
			nowMs, err := routingErrorBudgetDatabaseNowMs(tx.WithContext(ctx))
			if err != nil {
				return err
			}
			targetKey, err := routingBreakerResetEndpointTargetKey(state.EndpointAuthority, state.Region)
			if err != nil {
				return err
			}
			fence, err := lockRoutingBreakerResetFenceTx(ctx, tx, targetKey, nowMs)
			if err != nil {
				return err
			}
			if state.ResetGeneration < fence.Generation {
				continue
			}
			if state.ResetGeneration != fence.Generation {
				return ErrRoutingEndpointStateInvalid
			}
			created := *state
			if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "endpoint_authority_key"}, {Name: "region_key"}},
				DoNothing: true,
			}).Create(&created).Error; err != nil {
				return err
			}
			result := tx.WithContext(ctx).Model(&RoutingEndpointSharedState{}).
				Where("endpoint_authority_key = ? AND region_key = ? AND (reset_generation < ? OR (reset_generation = ? AND (evidence_through_ms < ? OR (evidence_through_ms = ? AND evaluated_at_ms <= ?))))",
					state.EndpointAuthorityKey, state.RegionKey, state.ResetGeneration, state.ResetGeneration,
					state.EvidenceThroughMs, state.EvidenceThroughMs, state.EvaluatedAtMs).
				Select(
					"endpoint_host", "endpoint_authority", "region", "reset_generation", "state", "reason", "evidence_count",
					"network_failure_count", "node_count", "failure_node_count", "cooldown_until_ms",
					"evidence_from_ms", "evidence_through_ms", "evaluated_at_ms", "expires_at_ms", "updated_time_ms",
				).
				Updates(state)
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})
}

func ListFreshRoutingEndpointSharedStatesContext(ctx context.Context, region string) ([]RoutingEndpointSharedState, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	region = strings.ToLower(strings.TrimSpace(region))
	if region != "" && !validRoutingEndpointText(region, 64) {
		return nil, 0, ErrRoutingEndpointStateInvalid
	}
	nowMs, err := routingErrorBudgetDatabaseNowMs(DB.WithContext(ctx))
	if err != nil {
		return nil, 0, err
	}
	query := DB.WithContext(ctx).Model(&RoutingEndpointSharedState{}).Where("expires_at_ms > ?", nowMs)
	if region != "" {
		query = query.Where("region = ?", region)
	}
	var states []RoutingEndpointSharedState
	err = query.Order("region ASC, endpoint_authority ASC").Limit(RoutingEndpointAggregateMaxRows + 1).Find(&states).Error
	if err != nil {
		return nil, nowMs, err
	}
	if len(states) > RoutingEndpointAggregateMaxRows {
		return nil, nowMs, ErrRoutingEndpointAggregateLarge
	}
	return states, nowMs, nil
}

func DeleteRoutingEndpointHistoryBeforeContext(ctx context.Context, cutoffMs int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 {
		return 0, nil
	}
	deleted := int64(0)
	for {
		var ids []int64
		if err := DB.WithContext(ctx).Model(&RoutingEndpointEvidence{}).
			Where("updated_time_ms < ?", cutoffMs).Order("id ASC").Limit(routingEndpointRetentionBatch).
			Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			break
		}
		result := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingEndpointEvidence{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
		if len(ids) < routingEndpointRetentionBatch {
			break
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
	}
	result := DB.WithContext(ctx).Where("expires_at_ms < ?", cutoffMs).Delete(&RoutingEndpointSharedState{})
	return deleted + result.RowsAffected, result.Error
}

func validRoutingEndpointEvidence(row RoutingEndpointEvidence, nowMs int64) bool {
	if !validRoutingEndpointText(row.NodeID, 128) || !validRoutingEndpointText(row.NodeEpochID, 128) ||
		!validRoutingEndpointText(row.EndpointHost, 255) || !validRoutingEndpointText(row.EndpointAuthority, 320) ||
		!validRoutingEndpointText(row.Region, 64) || row.ResetGeneration < 0 || row.BucketTs <= 0 || row.BucketTs > nowMs/1000+600 ||
		row.RequestCount < 0 || row.ReachableCount < 0 || row.NetworkFailureCount < 0 ||
		row.ReachableCount > row.RequestCount || row.NetworkFailureCount > row.RequestCount-row.ReachableCount ||
		row.TotalLatencyMs < 0 ||
		row.TtftSumMs < 0 || row.TtftCount < 0 || row.TtftCount > row.RequestCount {
		return false
	}
	return row.RequestCount > 0
}

func validRoutingEndpointAggregate(row RoutingEndpointEvidenceAggregate) bool {
	return validRoutingEndpointText(row.NodeID, 128) && len(row.NodeKey) == 64 &&
		validRoutingEndpointText(row.EndpointHost, 255) && validRoutingEndpointText(row.EndpointAuthority, 320) &&
		len(row.EndpointAuthorityKey) == 64 && validRoutingEndpointText(row.Region, 64) && len(row.RegionKey) == 64 &&
		row.ResetGeneration >= 0 && row.RequestCount > 0 && row.ReachableCount >= 0 && row.NetworkFailureCount >= 0 &&
		row.ReachableCount <= row.RequestCount && row.NetworkFailureCount <= row.RequestCount-row.ReachableCount &&
		row.TotalLatencyMs >= 0 &&
		row.TtftSumMs >= 0 && row.TtftCount >= 0 && row.TtftCount <= row.RequestCount && row.EvidenceThroughMs > 0
}

func validRoutingEndpointSharedState(state RoutingEndpointSharedState) bool {
	if !validRoutingEndpointText(state.EndpointHost, 255) || !validRoutingEndpointText(state.EndpointAuthority, 320) ||
		len(state.EndpointAuthorityKey) != 64 || !validRoutingEndpointText(state.Region, 64) || len(state.RegionKey) != 64 ||
		!validRoutingEndpointText(state.State, 32) || !validRoutingEndpointTextAllowEmpty(state.Reason, 64) ||
		state.ResetGeneration < 0 || state.EvidenceCount < 0 || state.NetworkFailureCount < 0 || state.NetworkFailureCount > state.EvidenceCount ||
		state.NodeCount < 0 || state.FailureNodeCount < 0 || state.FailureNodeCount > state.NodeCount ||
		state.CooldownUntilMs < 0 || state.EvidenceFromMs <= 0 || state.EvidenceThroughMs < state.EvidenceFromMs ||
		state.EvaluatedAtMs < state.EvidenceThroughMs || state.ExpiresAtMs <= state.EvaluatedAtMs ||
		state.CreatedTimeMs <= 0 || state.UpdatedTimeMs < state.CreatedTimeMs {
		return false
	}
	switch state.State {
	case RoutingBreakerStateHealthy, RoutingBreakerStateDegraded, RoutingBreakerStateOpen, RoutingBreakerStateHalfOpen:
		return true
	default:
		return false
	}
}

func validRoutingEndpointText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func validRoutingEndpointTextAllowEmpty(value string, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func routingEndpointHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
