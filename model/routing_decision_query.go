package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrRoutingDecisionQueryInvalid = errors.New("invalid routing decision query")

const RoutingDecisionReplayProfileMaxLimit = 50

type RoutingDecisionAuditSummary struct {
	ID                    int     `json:"id"`
	DecisionID            string  `json:"decision_id"`
	RequestID             string  `json:"request_id"`
	PoolID                int     `json:"pool_id"`
	GroupName             string  `json:"group_name"`
	ModelName             string  `json:"model_name"`
	SnapshotRevision      int64   `json:"snapshot_revision"`
	RuntimeGeneration     int64   `json:"runtime_generation"`
	ActivationID          int64   `json:"activation_id"`
	ActivationStage       string  `json:"activation_stage"`
	TrafficBasisPoints    int     `json:"traffic_basis_points"`
	Cohort                string  `json:"cohort,omitempty"`
	AlgorithmVersion      string  `json:"algorithm_version"`
	RetryIndex            int     `json:"retry_index"`
	IsStream              bool    `json:"is_stream"`
	ActualChannelID       int     `json:"actual_channel_id"`
	ObservedChannelID     int     `json:"observed_channel_id"`
	SelectedMemberID      int     `json:"selected_member_id"`
	SelectedCredentialID  int     `json:"selected_credential_id"`
	CandidateCount        int     `json:"candidate_count"`
	EligibleCount         int     `json:"eligible_count"`
	FilteredOpen          int     `json:"filtered_open"`
	FilteredCapacity      int     `json:"filtered_capacity"`
	BreakerBypassed       bool    `json:"breaker_bypassed"`
	ObservedMatchesActual bool    `json:"observed_matches_actual"`
	DifferenceType        string  `json:"difference_type,omitempty"`
	ActualCostKnown       bool    `json:"actual_cost_known"`
	ActualExpectedCost    float64 `json:"actual_expected_cost"`
	ObservedCostKnown     bool    `json:"observed_cost_known"`
	ObservedExpectedCost  float64 `json:"observed_expected_cost"`
	ExpectedCostDelta     float64 `json:"expected_cost_delta"`
	Replayable            bool    `json:"replayable"`
	CreatedTime           int64   `json:"created_time"`
}

type RoutingDecisionAuditSummaryFilter struct {
	BeforeID              int
	Limit                 int
	GroupKey              string
	ModelKey              string
	RequestKey            string
	ObservedMatchesActual *bool
	Replayable            *bool
	ActivationID          int64
	RolloutKey            string
	Cohort                string
	FromTime              int64
	ToTime                int64
}

func ListRoutingDecisionAuditSummariesContext(
	ctx context.Context,
	filter RoutingDecisionAuditSummaryFilter,
) ([]RoutingDecisionAuditSummary, bool, error) {
	if DB == nil || filter.BeforeID < 0 || filter.Limit < 1 || filter.Limit > 100 ||
		filter.ActivationID < 0 || filter.FromTime < 0 || filter.ToTime < 0 ||
		(filter.FromTime > 0 && filter.ToTime > 0 && filter.FromTime > filter.ToTime) {
		return nil, false, ErrRoutingDecisionQueryInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	query := routingDecisionAuditSummaryQuery(ctx)
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.GroupKey != "" {
		query = query.Where("group_key = ?", filter.GroupKey)
	}
	if filter.ModelKey != "" {
		query = query.Where("model_key = ?", filter.ModelKey)
	}
	if filter.RequestKey != "" {
		query = query.Where("request_key = ?", filter.RequestKey)
	}
	if filter.ObservedMatchesActual != nil {
		query = query.Where("observed_matches_actual = ?", *filter.ObservedMatchesActual)
	}
	if filter.Replayable != nil {
		query = query.Where("replayable = ?", *filter.Replayable)
	}
	if filter.ActivationID > 0 {
		query = query.Where("activation_id = ?", filter.ActivationID)
	}
	if filter.RolloutKey != "" {
		query = query.Where("rollout_key = ?", filter.RolloutKey)
	}
	if filter.Cohort != "" {
		query = query.Where("cohort = ?", filter.Cohort)
	}
	if filter.FromTime > 0 {
		query = query.Where("created_time >= ?", filter.FromTime)
	}
	if filter.ToTime > 0 {
		query = query.Where("created_time <= ?", filter.ToTime)
	}

	items := make([]RoutingDecisionAuditSummary, 0, filter.Limit+1)
	if err := query.Order("id desc").Limit(filter.Limit + 1).Scan(&items).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(items) > filter.Limit
	if hasMore {
		items = items[:filter.Limit]
	}
	return items, hasMore, nil
}

func ListLatestRoutingDecisionReplayProfilesContext(
	ctx context.Context,
	poolID int,
	limit int,
) ([]RoutingDecisionAuditSummary, error) {
	if DB == nil || poolID <= 0 || limit < 1 || limit > RoutingDecisionReplayProfileMaxLimit {
		return nil, ErrRoutingDecisionQueryInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type latestID struct {
		ID int `gorm:"column:id"`
	}
	latest := make([]latestID, 0, limit)
	if err := DB.WithContext(ctx).Model(&RoutingDecisionAudit{}).
		Select("MAX(id) AS id").
		Where("pool_id = ? AND replayable = ?", poolID, true).
		Group("model_key, model_name, is_stream").
		Order("MAX(id) DESC").
		Limit(limit).
		Scan(&latest).Error; err != nil {
		return nil, err
	}
	if len(latest) == 0 {
		return []RoutingDecisionAuditSummary{}, nil
	}
	ids := make([]int, len(latest))
	for index := range latest {
		ids[index] = latest[index].ID
	}
	rows := make([]RoutingDecisionAuditSummary, 0, len(ids))
	if err := routingDecisionAuditSummaryQuery(ctx).Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]RoutingDecisionAuditSummary, len(rows))
	for index := range rows {
		byID[rows[index].ID] = rows[index]
	}
	items := make([]RoutingDecisionAuditSummary, 0, len(ids))
	for _, id := range ids {
		if row, exists := byID[id]; exists {
			items = append(items, row)
		}
	}
	if len(items) != len(ids) {
		return nil, ErrRoutingDecisionQueryInvalid
	}
	return items, nil
}

func routingDecisionAuditSummaryQuery(ctx context.Context) *gorm.DB {
	return DB.WithContext(ctx).Model(&RoutingDecisionAudit{}).Select(
		"id", "decision_id", "request_id", "pool_id", "group_name", "model_name",
		"snapshot_revision", "runtime_generation", "activation_id", "activation_stage",
		"traffic_basis_points", "cohort", "algorithm_version", "retry_index", "is_stream",
		"actual_channel_id", "observed_channel_id", "selected_member_id", "selected_credential_id",
		"candidate_count", "eligible_count", "filtered_open", "filtered_capacity", "breaker_bypassed",
		"observed_matches_actual", "difference_type", "actual_cost_known", "actual_expected_cost",
		"observed_cost_known", "observed_expected_cost", "expected_cost_delta", "replayable", "created_time",
	)
}
