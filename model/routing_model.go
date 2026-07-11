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
	if snapshot == nil || snapshot.ChannelID <= 0 || snapshot.ModelName == "" {
		return nil
	}
	return DB.Clauses(clause.OnConflict{
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
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
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
	updates := map[string]interface{}{
		"semantic_version":     state.SemanticVersion,
		"state":                state.State,
		"reason":               state.Reason,
		"consecutive_failures": state.ConsecutiveFailures,
		"consecutive_5xx":      state.Consecutive5xx,
		"ejection_count":       state.EjectionCount,
		"opened_at":            state.OpenedAt,
		"cooldown_until":       state.CooldownUntil,
		"half_open_inflight":   state.HalfOpenInflight,
		"window_requests":      state.WindowRequests,
		"window_failures":      state.WindowFailures,
		"last_probe_at":        state.LastProbeAt,
		"updated_time":         state.UpdatedTime,
	}
	db := DB.WithContext(ctx)
	breakerKeyWhere := func() *gorm.DB {
		return db.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
			state.ChannelID, state.APIKeyIndex, state.ModelName, state.Group)
	}
	versionWhere := "(semantic_version IS NULL OR semantic_version <> ? OR updated_time <= ?)"
	result := breakerKeyWhere().Where(versionWhere, RoutingBreakerSemanticVersion, state.UpdatedTime).Model(&RoutingBreakerState{}).UpdateColumns(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	createErr := db.Create(state).Error
	if createErr == nil {
		return nil
	}
	// A concurrent writer may have inserted the row after our conditional update.
	return breakerKeyWhere().Where(versionWhere, RoutingBreakerSemanticVersion, state.UpdatedTime).Model(&RoutingBreakerState{}).UpdateColumns(updates).Error
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
	if channelID <= 0 {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = common.GetTimestamp()
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"balance_known":        true,
			"balance":              balance,
			"balance_updated_time": updatedTime,
			"updated_time":         updatedTime,
		}),
	}).Create(&RoutingChannelHealthState{
		ChannelID:          channelID,
		BalanceKnown:       true,
		Balance:            balance,
		BalanceUpdatedTime: updatedTime,
		UpdatedTime:        updatedTime,
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
