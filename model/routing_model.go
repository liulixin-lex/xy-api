package model

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingUpstreamTypeNewAPI  = "newapi"
	RoutingUpstreamTypeSub2API = "sub2api"

	RoutingMetricSingleKeyIndex = -1

	RoutingCostConfidenceFull      = "full"
	RoutingCostConfidenceGroupOnly = "group_only"
	RoutingCostConfidenceUnknown   = "unknown"

	RoutingBreakerStateHealthy    = "healthy"
	RoutingBreakerStateDegraded   = "degraded"
	RoutingBreakerStateOpen       = "open"
	RoutingBreakerStateHalfOpen   = "half_open"
	RoutingBreakerSemanticVersion = 2
)

var (
	ErrCredentialSecretUnstable              = errors.New("routing credential secret is not persistent")
	ErrCredentialSecretMismatch              = errors.New("routing credential secret does not match topology metadata")
	ErrLegacyRoutingStateEligibilityMismatch = errors.New("legacy routing state eligibility does not match record")
)

// RoutingChannelBinding is retained only as the legacy table shape required
// by one-time configuration migration and irreversible connector scrubbing.
// Runtime APIs must not serialize or mutate it as an active connector.
type RoutingChannelBinding struct {
	ID               int     `json:"-" gorm:"primaryKey"`
	ChannelID        int     `json:"-" gorm:"uniqueIndex;not null"`
	UpstreamType     string  `json:"-" gorm:"type:varchar(32);not null"`
	BaseURL          string  `json:"-" gorm:"type:varchar(512);not null"`
	UpstreamGroup    string  `json:"-" gorm:"type:varchar(128);not null"`
	ServesClaudeCode bool    `json:"-"`
	EgressPolicyJSON *string `json:"-" gorm:"type:text"`
	EncCredentials   *string `json:"-" gorm:"type:text"`
	KeyVersion       int     `json:"-"`
	NewAPIUserID     *int    `json:"-"`
	Enabled          bool    `json:"-"`
	AccountKeyHash   string  `json:"-" gorm:"type:char(64);index"`
	SyncFailureCount int     `json:"-"`
	SyncBackoffUntil int64   `json:"-" gorm:"bigint"`
	LastSyncError    *string `json:"-" gorm:"type:text"`
	CreatedTime      int64   `json:"-" gorm:"bigint"`
	UpdatedTime      int64   `json:"-" gorm:"bigint"`
}

type RoutingChannelStateFence struct {
	ChannelID   int
	Generation  string
	CreatedTime int64
}

func (fence RoutingChannelStateFence) Valid() bool {
	return fence.ChannelID > 0 && fence.Generation != ""
}

func (RoutingChannelBinding) TableName() string {
	return "routing_channel_bindings"
}

type RoutingCostSnapshot struct {
	ID                  int     `json:"id" gorm:"primaryKey"`
	AccountID           int     `json:"account_id,omitempty" gorm:"index"`
	ChannelID           int     `json:"channel_id" gorm:"uniqueIndex:idx_routing_cost_channel_model_key,priority:1;index"`
	ModelName           string  `json:"model_name" gorm:"type:varchar(128);index"`
	ModelKey            *string `json:"-" gorm:"type:char(64);uniqueIndex:idx_routing_cost_channel_model_key,priority:2"`
	QuotaType           int     `json:"quota_type"`
	GroupRatio          float64 `json:"group_ratio"`
	BaseRatio           float64 `json:"base_ratio"`
	CompletionRatio     float64 `json:"completion_ratio"`
	ModelPrice          float64 `json:"model_price"`
	BillingMode         string  `json:"billing_mode" gorm:"type:varchar(64)"`
	TiersJSON           *string `json:"tiers_json" gorm:"type:text"`
	ExtrasJSON          *string `json:"extras_json" gorm:"type:text"`
	Confidence          string  `json:"confidence" gorm:"type:varchar(32)"`
	SnapshotTS          int64   `json:"snapshot_ts" gorm:"bigint;index"`
	PricingVersion      string  `json:"pricing_version" gorm:"type:varchar(128)"`
	PricingHash         string  `json:"pricing_hash,omitempty" gorm:"type:char(64);index"`
	PricingJSON         *string `json:"-" gorm:"type:text"`
	UpstreamGroup       string  `json:"upstream_group,omitempty" gorm:"type:varchar(128)"`
	UpstreamModel       string  `json:"upstream_model,omitempty" gorm:"type:varchar(128)"`
	ObservedTime        int64   `json:"observed_time,omitempty" gorm:"bigint;index"`
	EffectiveTime       int64   `json:"effective_time,omitempty" gorm:"bigint;index"`
	ExpiresTime         int64   `json:"expires_time,omitempty" gorm:"bigint;index"`
	VersionConfidence   string  `json:"version_confidence,omitempty" gorm:"type:varchar(32);index"`
	ConfidenceScore     float64 `json:"confidence_score,omitempty"`
	Freshness           string  `json:"freshness,omitempty" gorm:"type:varchar(32);index"`
	FreshnessScore      float64 `json:"freshness_score,omitempty"`
	SourceSyncStatus    string  `json:"source_sync_status,omitempty" gorm:"type:varchar(32);index"`
	SourceSyncError     string  `json:"source_sync_error,omitempty" gorm:"type:text"`
	AccountSourceType   string  `json:"account_source_type,omitempty" gorm:"type:varchar(32);index"`
	AccountKeyHash      string  `json:"-" gorm:"type:char(64);index"`
	AccountMaskedID     string  `json:"account_masked_identity,omitempty" gorm:"type:varchar(256)"`
	AccountStatus       string  `json:"account_status,omitempty" gorm:"type:varchar(32);index"`
	AccountBalanceKnown bool    `json:"account_balance_known,omitempty"`
	AccountBalance      float64 `json:"account_balance,omitempty"`
	AccountBalanceAt    int64   `json:"account_balance_updated_at,omitempty" gorm:"bigint"`
	AccountSyncStatus   string  `json:"account_last_sync_status,omitempty" gorm:"type:varchar(32);index"`
	AccountSyncError    string  `json:"account_last_sync_error,omitempty" gorm:"type:text"`
}

func (RoutingCostSnapshot) TableName() string {
	return "routing_cost_snapshots"
}

type RoutingChannelMetric struct {
	ID                      int    `json:"id" gorm:"primaryKey"`
	ChannelID               int    `json:"channel_id" gorm:"uniqueIndex:idx_routing_metric_key,priority:1;index"`
	APIKeyIndex             int    `json:"api_key_index" gorm:"uniqueIndex:idx_routing_metric_key,priority:2"`
	ModelName               string `json:"model_name" gorm:"type:varchar(128);uniqueIndex:idx_routing_metric_key,priority:3"`
	Group                   string `json:"group" gorm:"column:group;type:varchar(64);uniqueIndex:idx_routing_metric_key,priority:4"`
	BucketTs                int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_routing_metric_key,priority:5;index:idx_routing_metric_bucket_ts"`
	RequestCount            int64  `json:"request_count"`
	SuccessCount            int64  `json:"success_count"`
	ReliabilityRequestCount int64  `json:"reliability_request_count" gorm:"not null;default:0"`
	ReliabilityFailureCount int64  `json:"reliability_failure_count" gorm:"not null;default:0"`
	TotalLatencyMs          int64  `json:"total_latency_ms"`
	LatencyP95Ms            int64  `json:"latency_p95_ms"`
	TtftSumMs               int64  `json:"ttft_sum_ms"`
	TtftCount               int64  `json:"ttft_count"`
	TtftP95Ms               int64  `json:"ttft_p95_ms"`
	OutputTokens            int64  `json:"output_tokens"`
	GenerationMs            int64  `json:"generation_ms"`
	Err4xx                  int64  `json:"err_4xx" gorm:"column:err_4xx"`
	Err5xx                  int64  `json:"err_5xx" gorm:"column:err_5xx"`
	Err429                  int64  `json:"err_429" gorm:"column:err_429"`
	Err529                  int64  `json:"err_529" gorm:"column:err_529;not null;default:0"`
	RetryAfterMaxMs         int64  `json:"retry_after_max_ms"`
}

func (RoutingChannelMetric) TableName() string {
	return "routing_channel_metrics"
}

func UpsertRoutingChannelMetric(metric *RoutingChannelMetric) error {
	return UpsertRoutingChannelMetricContext(context.Background(), metric)
}

func UpsertRoutingChannelMetricContext(ctx context.Context, metric *RoutingChannelMetric) error {
	if metric == nil || metric.RequestCount == 0 {
		return nil
	}
	eligibility, err := ResolveLegacyRoutingStateEligibilityContext(ctx, metric.ChannelID, metric.APIKeyIndex)
	if err != nil {
		return err
	}
	return eligibility.UpsertRoutingChannelMetricContext(ctx, metric)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingChannelMetric(metric *RoutingChannelMetric) error {
	return eligibility.UpsertRoutingChannelMetricContext(context.Background(), metric)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingChannelMetricContext(ctx context.Context, metric *RoutingChannelMetric) error {
	return eligibility.upsertRoutingChannelMetric(DB.WithContext(ctx), metric)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingChannelMetricForChannelContext(
	ctx context.Context,
	metric *RoutingChannelMetric,
	expectedFence RoutingChannelStateFence,
) (RoutingChannelStateFence, bool, error) {
	if metric == nil || metric.RequestCount == 0 || !eligibility.Supported() {
		return RoutingChannelStateFence{}, false, nil
	}
	if metric.ChannelID != eligibility.channelID || metric.APIKeyIndex != eligibility.apiKeyIndex {
		return RoutingChannelStateFence{}, false, fmt.Errorf("%w: eligibility=(%d,%d) metric=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			metric.ChannelID, metric.APIKeyIndex,
		)
	}
	return withRoutingChannelStateWrite(ctx, metric.ChannelID, expectedFence, metric.BucketTs, func(tx *gorm.DB) error {
		return eligibility.upsertRoutingChannelMetric(tx, metric)
	})
}

func (eligibility LegacyRoutingStateEligibility) upsertRoutingChannelMetric(db *gorm.DB, metric *RoutingChannelMetric) error {
	if metric == nil || metric.RequestCount == 0 || !eligibility.Supported() {
		return nil
	}
	if metric.ChannelID != eligibility.channelID || metric.APIKeyIndex != eligibility.apiKeyIndex {
		return fmt.Errorf("%w: eligibility=(%d,%d) metric=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			metric.ChannelID, metric.APIKeyIndex,
		)
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "channel_id"},
			{Name: "api_key_index"},
			{Name: "model_name"},
			{Name: "group"},
			{Name: "bucket_ts"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"request_count":             gorm.Expr("routing_channel_metrics.request_count + ?", metric.RequestCount),
			"success_count":             gorm.Expr("routing_channel_metrics.success_count + ?", metric.SuccessCount),
			"reliability_request_count": gorm.Expr("routing_channel_metrics.reliability_request_count + ?", metric.ReliabilityRequestCount),
			"reliability_failure_count": gorm.Expr("routing_channel_metrics.reliability_failure_count + ?", metric.ReliabilityFailureCount),
			"total_latency_ms":          gorm.Expr("routing_channel_metrics.total_latency_ms + ?", metric.TotalLatencyMs),
			"latency_p95_ms":            gorm.Expr("CASE WHEN routing_channel_metrics.latency_p95_ms > ? THEN routing_channel_metrics.latency_p95_ms ELSE ? END", metric.LatencyP95Ms, metric.LatencyP95Ms),
			"ttft_sum_ms":               gorm.Expr("routing_channel_metrics.ttft_sum_ms + ?", metric.TtftSumMs),
			"ttft_count":                gorm.Expr("routing_channel_metrics.ttft_count + ?", metric.TtftCount),
			"ttft_p95_ms":               gorm.Expr("CASE WHEN routing_channel_metrics.ttft_p95_ms > ? THEN routing_channel_metrics.ttft_p95_ms ELSE ? END", metric.TtftP95Ms, metric.TtftP95Ms),
			"output_tokens":             gorm.Expr("routing_channel_metrics.output_tokens + ?", metric.OutputTokens),
			"generation_ms":             gorm.Expr("routing_channel_metrics.generation_ms + ?", metric.GenerationMs),
			"err_4xx":                   gorm.Expr("routing_channel_metrics.err_4xx + ?", metric.Err4xx),
			"err_5xx":                   gorm.Expr("routing_channel_metrics.err_5xx + ?", metric.Err5xx),
			"err_429":                   gorm.Expr("routing_channel_metrics.err_429 + ?", metric.Err429),
			"err_529":                   gorm.Expr("routing_channel_metrics.err_529 + ?", metric.Err529),
			"retry_after_max_ms":        gorm.Expr("CASE WHEN routing_channel_metrics.retry_after_max_ms > ? THEN routing_channel_metrics.retry_after_max_ms ELSE ? END", metric.RetryAfterMaxMs, metric.RetryAfterMaxMs),
		}),
	}).Create(metric).Error
}

func DeleteRoutingMetricsBefore(cutoffTs int64) (int64, error) {
	return DeleteRoutingMetricsBeforeContext(context.Background(), cutoffTs)
}

func DeleteRoutingMetricsBeforeContext(ctx context.Context, cutoffTs int64) (int64, error) {
	if cutoffTs <= 0 {
		return 0, nil
	}
	result := DB.WithContext(ctx).Where("bucket_ts < ?", cutoffTs).Delete(&RoutingChannelMetric{})
	return result.RowsAffected, result.Error
}

type RoutingBreakerState struct {
	ID                  int    `json:"id" gorm:"primaryKey"`
	ChannelID           int    `json:"channel_id" gorm:"uniqueIndex:idx_routing_breaker_key,priority:1;index"`
	APIKeyIndex         int    `json:"api_key_index" gorm:"uniqueIndex:idx_routing_breaker_key,priority:2"`
	ModelName           string `json:"model_name" gorm:"type:varchar(128);uniqueIndex:idx_routing_breaker_key,priority:3"`
	Group               string `json:"group" gorm:"column:group;type:varchar(64);uniqueIndex:idx_routing_breaker_key,priority:4"`
	SemanticVersion     int    `json:"semantic_version" gorm:"index"`
	ResetGeneration     int64  `json:"reset_generation" gorm:"bigint;index;default:0;not null"`
	State               string `json:"state" gorm:"type:varchar(32);index"`
	Reason              string `json:"reason" gorm:"type:varchar(64);index"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	Consecutive5xx      int64  `json:"consecutive_5xx" gorm:"column:consecutive_5xx"`
	EjectionCount       int64  `json:"ejection_count"`
	OpenedAt            int64  `json:"opened_at" gorm:"bigint"`
	CooldownUntil       int64  `json:"cooldown_until" gorm:"bigint;index"`
	HalfOpenInflight    int64  `json:"half_open_inflight"`
	WindowRequests      int64  `json:"window_requests"`
	WindowFailures      int64  `json:"window_failures"`
	LastProbeAt         int64  `json:"last_probe_at" gorm:"bigint"`
	UpdatedTime         int64  `json:"updated_time" gorm:"bigint;index"`
}

func (RoutingBreakerState) TableName() string {
	return "routing_breaker_states"
}

func (state *RoutingBreakerState) BeforeCreate(_ *gorm.DB) error {
	if state.UpdatedTime == 0 {
		state.UpdatedTime = common.GetTimestamp()
	}
	return nil
}

func (state *RoutingBreakerState) BeforeUpdate(_ *gorm.DB) error {
	state.UpdatedTime = common.GetTimestamp()
	return nil
}

func UpsertRoutingBreakerState(state *RoutingBreakerState) error {
	return UpsertRoutingBreakerStateContext(context.Background(), state)
}

func UpsertRoutingBreakerStateContext(ctx context.Context, state *RoutingBreakerState) error {
	if state == nil || state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" {
		return nil
	}
	eligibility, err := ResolveLegacyRoutingStateEligibilityContext(ctx, state.ChannelID, state.APIKeyIndex)
	if err != nil {
		return err
	}
	return eligibility.UpsertRoutingBreakerStateContext(ctx, state)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingBreakerState(state *RoutingBreakerState) error {
	return eligibility.UpsertRoutingBreakerStateContext(context.Background(), state)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingBreakerStateContext(ctx context.Context, state *RoutingBreakerState) error {
	return eligibility.upsertRoutingBreakerState(DB.WithContext(ctx), state)
}

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingBreakerStateForChannelContext(
	ctx context.Context,
	state *RoutingBreakerState,
	expectedFence RoutingChannelStateFence,
) (RoutingChannelStateFence, bool, error) {
	if state == nil || state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" || !eligibility.Supported() {
		return RoutingChannelStateFence{}, false, nil
	}
	if state.ChannelID != eligibility.channelID || state.APIKeyIndex != eligibility.apiKeyIndex {
		return RoutingChannelStateFence{}, false, fmt.Errorf("%w: eligibility=(%d,%d) breaker=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			state.ChannelID, state.APIKeyIndex,
		)
	}
	return withRoutingChannelStateWrite(ctx, state.ChannelID, expectedFence, state.UpdatedTime, func(tx *gorm.DB) error {
		return eligibility.upsertRoutingBreakerState(tx, state)
	})
}

func (eligibility LegacyRoutingStateEligibility) upsertRoutingBreakerState(db *gorm.DB, state *RoutingBreakerState) error {
	if state == nil || state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" || !eligibility.Supported() {
		return nil
	}
	if state.ChannelID != eligibility.channelID || state.APIKeyIndex != eligibility.apiKeyIndex {
		return fmt.Errorf("%w: eligibility=(%d,%d) breaker=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			state.ChannelID, state.APIKeyIndex,
		)
	}
	if state.ResetGeneration < 0 {
		return ErrRoutingBreakerResetInvalid
	}
	writeCtx := context.Background()
	if db.Statement != nil && db.Statement.Context != nil {
		writeCtx = db.Statement.Context
	}
	return db.Transaction(func(tx *gorm.DB) error {
		nowMs, err := routingErrorBudgetDatabaseNowMs(tx)
		if err != nil {
			return err
		}
		targetKey, err := routingBreakerResetMemberTargetKey(
			state.ChannelID, state.APIKeyIndex, state.ModelName, state.Group,
		)
		if err != nil {
			return err
		}
		fence, err := lockRoutingBreakerResetFenceTx(writeCtx, tx, targetKey, nowMs)
		if err != nil {
			return err
		}
		if state.ResetGeneration < fence.Generation {
			return nil
		}
		return eligibility.upsertRoutingBreakerStateLocked(tx, state)
	})
}

func (eligibility LegacyRoutingStateEligibility) upsertRoutingBreakerStateLocked(db *gorm.DB, state *RoutingBreakerState) error {
	state.SemanticVersion = RoutingBreakerSemanticVersion
	updateColumns := []string{
		"reset_generation",
		"state",
		"reason",
		"consecutive_failures",
		"consecutive_5xx",
		"ejection_count",
		"opened_at",
		"cooldown_until",
		"half_open_inflight",
		"window_requests",
		"window_failures",
		"last_probe_at",
		"updated_time",
		"semantic_version",
	}
	onConflict := clause.OnConflict{Columns: []clause.Column{
		{Name: "channel_id"},
		{Name: "api_key_index"},
		{Name: "model_name"},
		{Name: "group"},
	}}
	if db.Dialector.Name() == string(common.DatabaseTypeMySQL) {
		currentVersion := clause.Column{Name: "semantic_version"}
		incomingVersion := clause.Column{Name: "semantic_version"}
		currentUpdatedTime := clause.Column{Name: "updated_time"}
		incomingUpdatedTime := clause.Column{Name: "updated_time"}
		assignments := make(clause.Set, 0, len(updateColumns))
		for _, columnName := range updateColumns {
			column := clause.Column{Name: columnName}
			assignments = append(assignments, clause.Assignment{
				Column: column,
				Value: clause.Expr{
					SQL: "CASE WHEN ? IS NULL OR ? <> VALUES(?) OR ? <= VALUES(?) THEN VALUES(?) ELSE ? END",
					Vars: []any{
						currentVersion,
						currentVersion,
						incomingVersion,
						currentUpdatedTime,
						incomingUpdatedTime,
						column,
						column,
					},
				},
			})
		}
		onConflict.DoUpdates = assignments
	} else {
		onConflict.DoUpdates = clause.AssignmentColumns(updateColumns)
		currentVersion := clause.Column{Table: clause.CurrentTable, Name: "semantic_version"}
		currentUpdatedTime := clause.Column{Table: clause.CurrentTable, Name: "updated_time"}
		onConflict.Where = clause.Where{Exprs: []clause.Expression{
			clause.Or(
				clause.Eq{Column: currentVersion, Value: nil},
				clause.Neq{Column: currentVersion, Value: clause.Column{Table: "excluded", Name: "semantic_version"}},
				clause.Lte{Column: currentUpdatedTime, Value: clause.Column{Table: "excluded", Name: "updated_time"}},
			),
		}}
	}
	return db.Clauses(onConflict).Create(state).Error
}

func withRoutingChannelStateWrite(
	ctx context.Context,
	channelID int,
	expectedFence RoutingChannelStateFence,
	stateUpdatedTime int64,
	write func(*gorm.DB) error,
) (RoutingChannelStateFence, bool, error) {
	if channelID <= 0 || write == nil {
		return RoutingChannelStateFence{}, false, nil
	}
	fence := RoutingChannelStateFence{}
	stateAccepted := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		query := tx.Select("id", "routing_generation", "created_time").Where("id = ?", channelID)
		if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&channel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		fence = RoutingChannelStateFence{
			ChannelID: channel.Id, Generation: channel.RoutingGeneration, CreatedTime: channel.CreatedTime,
		}
		if !fence.Valid() {
			return nil
		}
		if expectedFence.Valid() && expectedFence != fence {
			return nil
		}
		if channel.CreatedTime > 0 && stateUpdatedTime <= channel.CreatedTime {
			return nil
		}
		if err := write(tx); err != nil {
			return err
		}
		stateAccepted = true
		return nil
	})
	return fence, stateAccepted, err
}

func RoutingChannelStateFenceMatchesContext(ctx context.Context, fence RoutingChannelStateFence) (bool, error) {
	if !fence.Valid() {
		return false, nil
	}
	var channel Channel
	err := DB.WithContext(ctx).Select("id", "routing_generation", "created_time").
		Where("id = ?", fence.ChannelID).First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return channel.Id == fence.ChannelID &&
		channel.RoutingGeneration == fence.Generation &&
		channel.CreatedTime == fence.CreatedTime, nil
}

func GetRoutingBreakerStatesForHydration(limit int) ([]RoutingBreakerState, error) {
	return GetRoutingBreakerStatesForHydrationPage(limit, 0, 0, 0)
}

func GetRoutingBreakerStatesForHydrationPage(limit int, cutoffUpdatedTime int64, beforeUpdatedTime int64, beforeID int) ([]RoutingBreakerState, error) {
	return GetRoutingBreakerStatesForHydrationPageContext(context.Background(), limit, cutoffUpdatedTime, beforeUpdatedTime, beforeID)
}

func GetRoutingBreakerStatesForHydrationPageContext(ctx context.Context, limit int, cutoffUpdatedTime int64, beforeUpdatedTime int64, beforeID int) ([]RoutingBreakerState, error) {
	if limit <= 0 {
		limit = 5000
	}
	var states []RoutingBreakerState
	query := DB.WithContext(ctx).Where("semantic_version = ? AND api_key_index = ?", RoutingBreakerSemanticVersion, RoutingMetricSingleKeyIndex)
	if cutoffUpdatedTime > 0 {
		query = query.Where("updated_time >= ?", cutoffUpdatedTime)
	}
	if beforeID > 0 {
		query = query.Where("(updated_time < ? OR (updated_time = ? AND id < ?))", beforeUpdatedTime, beforeUpdatedTime, beforeID)
	}
	err := query.Order("updated_time desc").
		Order("id desc").
		Limit(limit).
		Find(&states).Error
	return states, err
}

type RoutingChannelHealthState struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	ChannelID         int    `json:"channel_id" gorm:"uniqueIndex;not null"`
	AuthFailure       bool   `json:"auth_failure"`
	AuthFailureReason string `json:"auth_failure_reason" gorm:"type:varchar(128)"`
	AuthFailureUntil  int64  `json:"auth_failure_until" gorm:"bigint;index"`
	UpdatedTime       int64  `json:"updated_time" gorm:"bigint;index"`
}

func (RoutingChannelHealthState) TableName() string {
	return "routing_channel_health_states"
}

func (state *RoutingChannelHealthState) BeforeCreate(_ *gorm.DB) error {
	if state.UpdatedTime == 0 {
		state.UpdatedTime = common.GetTimestamp()
	}
	return nil
}

func (state *RoutingChannelHealthState) BeforeUpdate(_ *gorm.DB) error {
	state.UpdatedTime = common.GetTimestamp()
	return nil
}

func UpsertRoutingChannelAuthFailure(channelID int, marked bool, reason string, until int64) error {
	return UpsertRoutingChannelAuthFailureContext(context.Background(), channelID, marked, reason, until)
}

func UpsertRoutingChannelAuthFailureContext(ctx context.Context, channelID int, marked bool, reason string, until int64) error {
	if channelID <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := common.GetTimestamp()
	if until <= 0 {
		until = now
	}
	return upsertRoutingChannelAuthFailureDB(DB.WithContext(ctx), channelID, marked, reason, until, now)
}

func ApplyRoutingChannelProbeAuthStateContext(
	ctx context.Context,
	channelID int,
	credentialID int,
	marked bool,
	reason string,
	until int64,
) (bool, error) {
	if channelID <= 0 || credentialID <= 0 {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	applied := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "routing_generation", "key", "status", "channel_info").Where("id = ?", channelID).First(&channel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.IsMultiKey || channel.Key == "" {
			return nil
		}
		var credential RoutingCredentialRef
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND channel_id = ? AND active = ?", credentialID, channelID, true).
			First(&credential).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		fingerprint, err := RoutingCredentialFingerprint(channelID, channel.RoutingGeneration, channel.Key)
		if err != nil {
			return err
		}
		if credential.Fingerprint != fingerprint || credential.FingerprintVersion != RoutingCredentialFingerprintVersion {
			return nil
		}
		now := common.GetTimestamp()
		if !marked {
			reason = ""
			until = 0
		} else if until <= 0 {
			until = now
		}
		if err := upsertRoutingChannelAuthFailureDB(tx.WithContext(ctx), channelID, marked, reason, until, now); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func upsertRoutingChannelAuthFailureDB(
	db *gorm.DB,
	channelID int,
	marked bool,
	reason string,
	until int64,
	updatedTime int64,
) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"auth_failure":        marked,
			"auth_failure_reason": reason,
			"auth_failure_until":  until,
			"updated_time":        updatedTime,
		}),
	}).Create(&RoutingChannelHealthState{
		ChannelID:         channelID,
		AuthFailure:       marked,
		AuthFailureReason: reason,
		AuthFailureUntil:  until,
		UpdatedTime:       updatedTime,
	}).Error
}

func ClearRoutingChannelAuthFailure(channelID int, updatedTime int64) error {
	return ClearRoutingChannelAuthFailureContext(context.Background(), channelID, updatedTime)
}

func ClearRoutingChannelAuthFailureContext(ctx context.Context, channelID int, updatedTime int64) error {
	if channelID <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if updatedTime <= 0 {
		updatedTime = common.GetTimestamp()
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"auth_failure":        false,
			"auth_failure_reason": "",
			"auth_failure_until":  int64(0),
			"updated_time":        updatedTime,
		}),
	}).Create(&RoutingChannelHealthState{
		ChannelID:   channelID,
		UpdatedTime: updatedTime,
	}).Error
}

type RoutingAgentRecommendation struct {
	ID           int    `json:"id" gorm:"primaryKey"`
	Type         string `json:"type" gorm:"type:varchar(64);index"`
	TargetJSON   string `json:"target_json" gorm:"type:text"`
	ProposedJSON string `json:"proposed_json" gorm:"type:text"`
	Rationale    string `json:"rationale" gorm:"type:text"`
	Severity     string `json:"severity" gorm:"type:varchar(32);index"`
	Status       string `json:"status" gorm:"type:varchar(32);index"`
	AppliedBy    *int   `json:"applied_by"`
	CreatedTime  int64  `json:"created_time" gorm:"bigint;index"`
	UpdatedTime  int64  `json:"updated_time" gorm:"bigint;index"`
}

func (RoutingAgentRecommendation) TableName() string {
	return "routing_agent_recommendations"
}

func (recommendation *RoutingAgentRecommendation) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if recommendation.CreatedTime == 0 {
		recommendation.CreatedTime = now
	}
	if recommendation.UpdatedTime == 0 {
		recommendation.UpdatedTime = now
	}
	return nil
}

func (recommendation *RoutingAgentRecommendation) BeforeUpdate(_ *gorm.DB) error {
	recommendation.UpdatedTime = common.GetTimestamp()
	return nil
}

type RoutingJSONMap map[string]any

func (m RoutingJSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	data, err := common.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (m *RoutingJSONMap) Scan(value interface{}) error {
	if value == nil {
		*m = RoutingJSONMap{}
		return nil
	}
	switch typed := value.(type) {
	case []byte:
		return common.Unmarshal(typed, m)
	case string:
		return common.UnmarshalJsonStr(typed, m)
	default:
		return fmt.Errorf("unsupported routing JSON map value %T", value)
	}
}
