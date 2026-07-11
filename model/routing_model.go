package model

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"runtime"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingUpstreamTypeNewAPI  = "newapi"
	RoutingUpstreamTypeSub2API = "sub2api"

	RoutingCredentialKeyVersion = 1
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
	ErrCredentialKeyMismatch                 = errors.New("routing credential key mismatch")
	ErrLegacyRoutingStateEligibilityMismatch = errors.New("legacy routing state eligibility does not match record")
	ErrRoutingBindingChanged                 = errors.New("routing channel binding changed during sync")
)

type RoutingCredentials struct {
	NewAPIAccessToken string `json:"-"`
	GatewayAPIKey     string `json:"-"`
	Sub2APIEmail      string `json:"-"`
	Sub2APIPassword   string `json:"-"`
	Sub2APIToken      string `json:"-"`
}

type routingCredentialsEnvelope struct {
	NewAPIAccessToken string `json:"new_api_access_token,omitempty"`
	GatewayAPIKey     string `json:"gateway_api_key,omitempty"`
	Sub2APIEmail      string `json:"sub2api_email,omitempty"`
	Sub2APIPassword   string `json:"sub2api_password,omitempty"`
	Sub2APIToken      string `json:"sub2api_token,omitempty"`
}

type RoutingChannelBinding struct {
	ID               int     `json:"id" gorm:"primaryKey"`
	ChannelID        int     `json:"channel_id" gorm:"uniqueIndex;not null"`
	UpstreamType     string  `json:"upstream_type" gorm:"type:varchar(32);not null"`
	BaseURL          string  `json:"base_url" gorm:"type:varchar(512);not null"`
	UpstreamGroup    string  `json:"upstream_group" gorm:"type:varchar(128);not null"`
	ServesClaudeCode bool    `json:"serves_claude_code"`
	EncCredentials   *string `json:"-" gorm:"type:text"`
	KeyVersion       int     `json:"key_version"`
	NewAPIUserID     *int    `json:"new_api_user_id"`
	Enabled          bool    `json:"enabled"`
	SyncFailureCount int     `json:"sync_failure_count"`
	SyncBackoffUntil int64   `json:"sync_backoff_until" gorm:"bigint"`
	LastSyncError    *string `json:"last_sync_error" gorm:"type:text"`
	CreatedTime      int64   `json:"created_time" gorm:"bigint"`
	UpdatedTime      int64   `json:"updated_time" gorm:"bigint"`
}

func (RoutingChannelBinding) TableName() string {
	return "routing_channel_bindings"
}

func (binding *RoutingChannelBinding) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if binding.CreatedTime == 0 {
		binding.CreatedTime = now
	}
	if binding.UpdatedTime == 0 {
		binding.UpdatedTime = now
	}
	return nil
}

func (binding *RoutingChannelBinding) BeforeUpdate(_ *gorm.DB) error {
	binding.UpdatedTime = common.GetTimestamp()
	return nil
}

func (binding *RoutingChannelBinding) SetCredentials(credentials RoutingCredentials) error {
	if !common.CryptoSecretIsPersistent() {
		return ErrCredentialSecretUnstable
	}
	data, err := common.Marshal(routingCredentialsEnvelope{
		NewAPIAccessToken: credentials.NewAPIAccessToken,
		GatewayAPIKey:     credentials.GatewayAPIKey,
		Sub2APIEmail:      credentials.Sub2APIEmail,
		Sub2APIPassword:   credentials.Sub2APIPassword,
		Sub2APIToken:      credentials.Sub2APIToken,
	})
	if err != nil {
		return err
	}
	encrypted, err := common.EncryptAESGCMString(string(data))
	if err != nil {
		return err
	}
	binding.EncCredentials = &encrypted
	binding.KeyVersion = RoutingCredentialKeyVersion
	return nil
}

func (binding *RoutingChannelBinding) GetCredentials() (RoutingCredentials, error) {
	if binding.EncCredentials == nil || *binding.EncCredentials == "" {
		return RoutingCredentials{}, nil
	}
	if binding.KeyVersion != RoutingCredentialKeyVersion {
		return RoutingCredentials{}, ErrCredentialKeyMismatch
	}
	plaintext, err := common.DecryptAESGCMString(*binding.EncCredentials)
	if err != nil {
		return RoutingCredentials{}, fmt.Errorf("%w: %v", ErrCredentialKeyMismatch, err)
	}
	var envelope routingCredentialsEnvelope
	if err = common.UnmarshalJsonStr(plaintext, &envelope); err != nil {
		return RoutingCredentials{}, fmt.Errorf("%w: %v", ErrCredentialKeyMismatch, err)
	}
	return RoutingCredentials{
		NewAPIAccessToken: envelope.NewAPIAccessToken,
		GatewayAPIKey:     envelope.GatewayAPIKey,
		Sub2APIEmail:      envelope.Sub2APIEmail,
		Sub2APIPassword:   envelope.Sub2APIPassword,
		Sub2APIToken:      envelope.Sub2APIToken,
	}, nil
}

type RoutingCostSnapshot struct {
	ID              int     `json:"id" gorm:"primaryKey"`
	ChannelID       int     `json:"channel_id" gorm:"uniqueIndex:idx_routing_cost_channel_model,priority:1;index"`
	ModelName       string  `json:"model_name" gorm:"type:varchar(128);uniqueIndex:idx_routing_cost_channel_model,priority:2;index"`
	QuotaType       int     `json:"quota_type"`
	GroupRatio      float64 `json:"group_ratio"`
	BaseRatio       float64 `json:"base_ratio"`
	CompletionRatio float64 `json:"completion_ratio"`
	ModelPrice      float64 `json:"model_price"`
	BillingMode     string  `json:"billing_mode" gorm:"type:varchar(32)"`
	TiersJSON       *string `json:"tiers_json" gorm:"type:text"`
	ExtrasJSON      *string `json:"extras_json" gorm:"type:text"`
	Confidence      string  `json:"confidence" gorm:"type:varchar(32)"`
	SnapshotTS      int64   `json:"snapshot_ts" gorm:"bigint;index"`
	PricingVersion  string  `json:"pricing_version" gorm:"type:varchar(128)"`
}

func (RoutingCostSnapshot) TableName() string {
	return "routing_cost_snapshots"
}

func UpsertRoutingCostSnapshot(snapshot *RoutingCostSnapshot) error {
	return UpsertRoutingCostSnapshotContext(context.Background(), snapshot)
}

func UpsertRoutingCostSnapshotContext(ctx context.Context, snapshot *RoutingCostSnapshot) error {
	if snapshot == nil || snapshot.ChannelID <= 0 || snapshot.ModelName == "" {
		return nil
	}
	return upsertRoutingCostSnapshot(DB.WithContext(ctx), snapshot)
}

func upsertRoutingCostSnapshot(db *gorm.DB, snapshot *RoutingCostSnapshot) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "channel_id"},
			{Name: "model_name"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"quota_type":       snapshot.QuotaType,
			"group_ratio":      snapshot.GroupRatio,
			"base_ratio":       snapshot.BaseRatio,
			"completion_ratio": snapshot.CompletionRatio,
			"model_price":      snapshot.ModelPrice,
			"billing_mode":     snapshot.BillingMode,
			"tiers_json":       snapshot.TiersJSON,
			"extras_json":      snapshot.ExtrasJSON,
			"confidence":       snapshot.Confidence,
			"snapshot_ts":      snapshot.SnapshotTS,
			"pricing_version":  snapshot.PricingVersion,
		}),
	}).Create(snapshot).Error
}

func CompleteRoutingCostSyncContext(ctx context.Context, expected RoutingChannelBinding, snapshots []RoutingCostSnapshot) error {
	if expected.ID <= 0 || expected.ChannelID <= 0 {
		return ErrRoutingBindingChanged
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := currentRoutingBindingForSync(tx, expected)
		if err != nil {
			return err
		}

		for i := range snapshots {
			if err := ctx.Err(); err != nil {
				return err
			}
			if snapshots[i].ChannelID != expected.ChannelID {
				return fmt.Errorf("routing cost snapshot channel does not match binding")
			}
			if err := upsertRoutingCostSnapshot(tx, &snapshots[i]); err != nil {
				return err
			}
		}

		result := tx.Model(&RoutingChannelBinding{}).Where("id = ?", expected.ID).Updates(map[string]any{
			"last_sync_error":    nil,
			"sync_failure_count": 0,
			"sync_backoff_until": 0,
			"updated_time":       nextRoutingBindingUpdatedTime(current.UpdatedTime),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var verified RoutingChannelBinding
			if err := tx.Where("id = ?", expected.ID).First(&verified).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRoutingBindingChanged
				}
				return err
			}
			if !routingBindingSyncSourceEqual(verified, expected) || !verified.Enabled {
				return ErrRoutingBindingChanged
			}
		}
		return nil
	})
}

func UpdateRoutingCostSyncFailureContext(
	ctx context.Context,
	expected RoutingChannelBinding,
	failureCount int,
	backoffUntil int64,
	message string,
) error {
	query := routingBindingSyncSourceQuery(DB.WithContext(ctx).Model(&RoutingChannelBinding{}), expected)
	result := query.Updates(map[string]any{
		"last_sync_error":    &message,
		"sync_failure_count": failureCount,
		"sync_backoff_until": backoffUntil,
		"updated_time":       nextRoutingBindingUpdatedTime(expected.UpdatedTime),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingBindingChanged
	}
	return nil
}

func UpdateRoutingChannelBindingAndInvalidateCostContext(ctx context.Context, expected RoutingChannelBinding, updated *RoutingChannelBinding) error {
	if updated == nil || expected.ID <= 0 || expected.ChannelID <= 0 ||
		updated.ID != expected.ID || updated.ChannelID != expected.ChannelID {
		return ErrRoutingBindingChanged
	}
	updated.UpdatedTime = nextRoutingBindingUpdatedTime(expected.UpdatedTime)
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := routingBindingSyncSourceQuery(tx.Model(&RoutingChannelBinding{}), expected).Updates(map[string]any{
			"upstream_type":      updated.UpstreamType,
			"base_url":           updated.BaseURL,
			"upstream_group":     updated.UpstreamGroup,
			"serves_claude_code": updated.ServesClaudeCode,
			"enc_credentials":    updated.EncCredentials,
			"key_version":        updated.KeyVersion,
			"new_api_user_id":    updated.NewAPIUserID,
			"enabled":            updated.Enabled,
			"sync_failure_count": 0,
			"sync_backoff_until": 0,
			"last_sync_error":    nil,
			"updated_time":       updated.UpdatedTime,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingBindingChanged
		}
		if err := tx.Where("channel_id = ?", updated.ChannelID).Delete(&RoutingCostSnapshot{}).Error; err != nil {
			return err
		}
		return tx.Model(&RoutingChannelHealthState{}).
			Where("channel_id = ?", updated.ChannelID).
			Updates(map[string]any{
				"balance_known":        false,
				"balance":              0,
				"balance_updated_time": 0,
			}).Error
	})
}

func routingBindingSyncSourceQuery(query *gorm.DB, expected RoutingChannelBinding) *gorm.DB {
	query = query.Where(
		"id = ? AND channel_id = ? AND upstream_type = ? AND base_url = ? AND upstream_group = ? AND serves_claude_code = ? AND enabled = ? AND key_version = ? AND updated_time = ? AND sync_backoff_until = ?",
		expected.ID,
		expected.ChannelID,
		expected.UpstreamType,
		expected.BaseURL,
		expected.UpstreamGroup,
		expected.ServesClaudeCode,
		expected.Enabled,
		expected.KeyVersion,
		expected.UpdatedTime,
		expected.SyncBackoffUntil,
	)
	if expected.SyncFailureCount == 0 {
		query = query.Where("(sync_failure_count = ? OR sync_failure_count IS NULL)", 0)
	} else {
		query = query.Where("sync_failure_count = ?", expected.SyncFailureCount)
	}
	if expected.EncCredentials == nil {
		query = query.Where("enc_credentials IS NULL")
	} else {
		query = query.Where("enc_credentials = ?", *expected.EncCredentials)
	}
	if expected.NewAPIUserID == nil {
		return query.Where("new_api_user_id IS NULL")
	}
	return query.Where("new_api_user_id = ?", *expected.NewAPIUserID)
}

func nextRoutingBindingUpdatedTime(previous int64) int64 {
	now := common.GetTimestamp()
	if now > previous {
		return now
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if previous < maxInt64 {
		return previous + 1
	}
	return previous
}

func routingBindingSyncSourceEqual(current RoutingChannelBinding, expected RoutingChannelBinding) bool {
	if current.ID != expected.ID ||
		current.ChannelID != expected.ChannelID ||
		current.UpstreamType != expected.UpstreamType ||
		current.BaseURL != expected.BaseURL ||
		current.UpstreamGroup != expected.UpstreamGroup ||
		current.ServesClaudeCode != expected.ServesClaudeCode ||
		current.Enabled != expected.Enabled ||
		current.KeyVersion != expected.KeyVersion ||
		current.UpdatedTime != expected.UpdatedTime {
		return false
	}
	if (current.EncCredentials == nil) != (expected.EncCredentials == nil) ||
		(current.NewAPIUserID == nil) != (expected.NewAPIUserID == nil) {
		return false
	}
	if current.EncCredentials != nil && *current.EncCredentials != *expected.EncCredentials {
		return false
	}
	return current.NewAPIUserID == nil || *current.NewAPIUserID == *expected.NewAPIUserID
}

func currentRoutingBindingForSync(tx *gorm.DB, expected RoutingChannelBinding) (RoutingChannelBinding, error) {
	var current RoutingChannelBinding
	query := tx
	if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Where("id = ?", expected.ID).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingChannelBinding{}, ErrRoutingBindingChanged
		}
		return RoutingChannelBinding{}, err
	}
	if !routingBindingSyncSourceEqual(current, expected) || !current.Enabled {
		return RoutingChannelBinding{}, ErrRoutingBindingChanged
	}
	return current, nil
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

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingChannelMetricForBindingContext(ctx context.Context, metric *RoutingChannelMetric, expectedBindingID int) (int, bool, error) {
	if metric == nil || metric.RequestCount == 0 || !eligibility.Supported() {
		return 0, false, nil
	}
	if metric.ChannelID != eligibility.channelID || metric.APIKeyIndex != eligibility.apiKeyIndex {
		return 0, false, fmt.Errorf("%w: eligibility=(%d,%d) metric=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			metric.ChannelID, metric.APIKeyIndex,
		)
	}
	return withRoutingBindingStateWrite(ctx, metric.ChannelID, expectedBindingID, metric.BucketTs, func(tx *gorm.DB) error {
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

func (eligibility LegacyRoutingStateEligibility) UpsertRoutingBreakerStateForBindingContext(ctx context.Context, state *RoutingBreakerState, expectedBindingID int) (int, bool, error) {
	if state == nil || state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" || !eligibility.Supported() {
		return 0, false, nil
	}
	if state.ChannelID != eligibility.channelID || state.APIKeyIndex != eligibility.apiKeyIndex {
		return 0, false, fmt.Errorf("%w: eligibility=(%d,%d) breaker=(%d,%d)",
			ErrLegacyRoutingStateEligibilityMismatch,
			eligibility.channelID, eligibility.apiKeyIndex,
			state.ChannelID, state.APIKeyIndex,
		)
	}
	return withRoutingBindingStateWrite(ctx, state.ChannelID, expectedBindingID, state.UpdatedTime, func(tx *gorm.DB) error {
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
	state.SemanticVersion = RoutingBreakerSemanticVersion
	updateColumns := []string{
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

func withRoutingBindingStateWrite(ctx context.Context, channelID int, expectedBindingID int, stateUpdatedTime int64, write func(*gorm.DB) error) (int, bool, error) {
	if channelID <= 0 || write == nil {
		return 0, false, nil
	}
	bindingID := 0
	stateAccepted := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var binding RoutingChannelBinding
		query := tx.Select("id", "created_time").Where("channel_id = ?", channelID)
		if expectedBindingID > 0 {
			query = query.Where("id = ?", expectedBindingID)
		}
		if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		bindingID = binding.ID
		if binding.CreatedTime > 0 && stateUpdatedTime <= binding.CreatedTime {
			return nil
		}
		if err := write(tx); err != nil {
			return err
		}
		stateAccepted = true
		return nil
	})
	return bindingID, stateAccepted, err
}

func RoutingChannelBindingMatchesContext(ctx context.Context, channelID int, bindingID int) (bool, error) {
	if channelID <= 0 || bindingID <= 0 {
		return false, nil
	}
	var count int64
	err := DB.WithContext(ctx).Model(&RoutingChannelBinding{}).
		Where("channel_id = ? AND id = ?", channelID, bindingID).
		Count(&count).Error
	return count == 1, err
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
	ID                 int     `json:"id" gorm:"primaryKey"`
	ChannelID          int     `json:"channel_id" gorm:"uniqueIndex;not null"`
	AuthFailure        bool    `json:"auth_failure"`
	AuthFailureReason  string  `json:"auth_failure_reason" gorm:"type:varchar(128)"`
	AuthFailureUntil   int64   `json:"auth_failure_until" gorm:"bigint;index"`
	BalanceKnown       bool    `json:"balance_known"`
	Balance            float64 `json:"balance"`
	BalanceUpdatedTime int64   `json:"balance_updated_time" gorm:"bigint"`
	UpdatedTime        int64   `json:"updated_time" gorm:"bigint;index"`
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
	if channelID <= 0 {
		return nil
	}
	now := common.GetTimestamp()
	if until <= 0 {
		until = now
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"auth_failure":        marked,
			"auth_failure_reason": reason,
			"auth_failure_until":  until,
			"updated_time":        now,
		}),
	}).Create(&RoutingChannelHealthState{
		ChannelID:         channelID,
		AuthFailure:       marked,
		AuthFailureReason: reason,
		AuthFailureUntil:  until,
		UpdatedTime:       now,
	}).Error
}

func ClearRoutingChannelAuthFailure(channelID int, updatedTime int64) error {
	if channelID <= 0 {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = common.GetTimestamp()
	}
	return DB.Clauses(clause.OnConflict{
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

func UpsertRoutingChannelBalance(channelID int, balance float64, updatedTime int64) error {
	return UpsertRoutingChannelBalanceContext(context.Background(), channelID, balance, updatedTime)
}

func UpsertRoutingChannelBalanceContext(ctx context.Context, channelID int, balance float64, updatedTime int64) error {
	if channelID <= 0 {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = common.GetTimestamp()
	}
	db := DB.WithContext(ctx)
	return retryRoutingSQLiteBalanceWrite(ctx, db, func() error {
		_, err := upsertRoutingChannelBalance(db, channelID, balance, updatedTime)
		return err
	})
}

func UpdateRoutingChannelBalanceForBindingContext(ctx context.Context, expected RoutingChannelBinding, balance float64, updatedTime int64) (bool, error) {
	if expected.ID <= 0 || expected.ChannelID <= 0 {
		return false, ErrRoutingBindingChanged
	}
	if updatedTime <= 0 {
		updatedTime = common.GetTimestamp()
	}
	db := DB.WithContext(ctx)
	applied := false
	err := retryRoutingSQLiteBalanceWrite(ctx, db, func() error {
		applied = false
		return db.Transaction(func(tx *gorm.DB) error {
			if _, err := currentRoutingBindingForSync(tx, expected); err != nil {
				return err
			}
			if tx.Dialector.Name() == string(common.DatabaseTypeMySQL) {
				var current RoutingChannelHealthState
				err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Select("balance_updated_time").
					Where("channel_id = ?", expected.ChannelID).
					First(&current).Error
				if err == nil && current.BalanceUpdatedTime >= updatedTime {
					return nil
				}
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}

			var err error
			applied, err = upsertRoutingChannelBalance(tx, expected.ChannelID, balance, updatedTime)
			return err
		})
	})
	return applied, err
}

func upsertRoutingChannelBalance(db *gorm.DB, channelID int, balance float64, updatedTime int64) (bool, error) {
	candidate := RoutingChannelHealthState{
		ChannelID:          channelID,
		BalanceKnown:       true,
		Balance:            balance,
		BalanceUpdatedTime: updatedTime,
		UpdatedTime:        updatedTime,
	}

	onConflict := clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}}}
	if db.Dialector.Name() == string(common.DatabaseTypeMySQL) {
		onConflict.DoUpdates = clause.Set{
			{
				Column: clause.Column{Name: "balance_known"},
				Value: clause.Expr{
					SQL: "CASE WHEN ? < VALUES(?) THEN VALUES(?) ELSE ? END",
					Vars: []any{
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_known"},
						clause.Column{Name: "balance_known"},
					},
				},
			},
			{
				Column: clause.Column{Name: "balance"},
				Value: clause.Expr{
					SQL: "CASE WHEN ? < VALUES(?) THEN VALUES(?) ELSE ? END",
					Vars: []any{
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance"},
						clause.Column{Name: "balance"},
					},
				},
			},
			{
				Column: clause.Column{Name: "updated_time"},
				Value: clause.Expr{
					SQL: "CASE WHEN ? < VALUES(?) THEN CASE WHEN ? > VALUES(?) THEN ? ELSE VALUES(?) END ELSE ? END",
					Vars: []any{
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "updated_time"},
						clause.Column{Name: "updated_time"},
						clause.Column{Name: "updated_time"},
						clause.Column{Name: "updated_time"},
						clause.Column{Name: "updated_time"},
					},
				},
			},
			{
				Column: clause.Column{Name: "balance_updated_time"},
				Value: clause.Expr{
					SQL: "CASE WHEN ? < VALUES(?) THEN VALUES(?) ELSE ? END",
					Vars: []any{
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
						clause.Column{Name: "balance_updated_time"},
					},
				},
			},
		}
	} else {
		onConflict.DoUpdates = clause.Set{
			{Column: clause.Column{Name: "balance_known"}, Value: clause.Column{Table: "excluded", Name: "balance_known"}},
			{Column: clause.Column{Name: "balance"}, Value: clause.Column{Table: "excluded", Name: "balance"}},
			{
				Column: clause.Column{Name: "updated_time"},
				Value: clause.Expr{
					SQL: "CASE WHEN ? > ? THEN ? ELSE ? END",
					Vars: []any{
						clause.Column{Table: clause.CurrentTable, Name: "updated_time"},
						clause.Column{Table: "excluded", Name: "updated_time"},
						clause.Column{Table: clause.CurrentTable, Name: "updated_time"},
						clause.Column{Table: "excluded", Name: "updated_time"},
					},
				},
			},
			{Column: clause.Column{Name: "balance_updated_time"}, Value: clause.Column{Table: "excluded", Name: "balance_updated_time"}},
		}
		onConflict.Where = clause.Where{Exprs: []clause.Expression{
			clause.Lt{
				Column: clause.Column{Table: clause.CurrentTable, Name: "balance_updated_time"},
				Value:  clause.Column{Table: "excluded", Name: "balance_updated_time"},
			},
		}}
	}

	result := db.Clauses(onConflict).Create(&candidate)
	return result.RowsAffected > 0, result.Error
}

func retryRoutingSQLiteBalanceWrite(ctx context.Context, db *gorm.DB, write func() error) error {
	if db.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		return write()
	}

	const maxAttempts = 16
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		err = write()
		if err == nil || !routingSQLiteBusyOrLocked(err) {
			return err
		}
		runtime.Gosched()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func routingSQLiteBusyOrLocked(err error) bool {
	var sqliteErr interface{ Code() int }
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == 5 || code == 6
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
