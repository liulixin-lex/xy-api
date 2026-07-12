package model

import (
	"context"
	"errors"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingProbeTypeServing = "serving"

	RoutingProbeOutcomeSuccess    = "success"
	RoutingProbeOutcomeFailure    = "failure"
	RoutingProbeOutcomeTimeout    = "timeout"
	RoutingProbeOutcomeCanceled   = "canceled"
	RoutingProbeOutcomeLocalError = "local_error"

	routingProbeRetentionBatch = 500
)

var (
	ErrRoutingProbeResultInvalid  = errors.New("invalid channel routing probe result")
	ErrRoutingProbeResultConflict = errors.New("channel routing probe result id conflict")
)

type RoutingProbeResult struct {
	ID                 int    `json:"id" gorm:"primaryKey"`
	ProbeID            string `json:"probe_id" gorm:"type:char(64);uniqueIndex;not null"`
	TargetKey          string `json:"target_key" gorm:"type:char(64);index;not null"`
	ProbeType          string `json:"probe_type" gorm:"type:varchar(32);index;not null"`
	SnapshotRevision   int64  `json:"snapshot_revision" gorm:"bigint;index;not null"`
	PoolID             int    `json:"pool_id" gorm:"index;not null"`
	MemberID           int    `json:"member_id" gorm:"index;not null"`
	ChannelID          int    `json:"channel_id" gorm:"index;not null"`
	CredentialID       int    `json:"credential_id" gorm:"index;not null"`
	GroupName          string `json:"group_name" gorm:"type:varchar(64);index;not null"`
	ModelName          string `json:"model_name" gorm:"type:varchar(128);index;not null"`
	EndpointHost       string `json:"endpoint_host" gorm:"type:varchar(255);index;not null"`
	BreakerState       string `json:"breaker_state" gorm:"type:varchar(32);index;not null"`
	Outcome            string `json:"outcome" gorm:"type:varchar(32);index;not null"`
	Responsibility     string `json:"responsibility" gorm:"type:varchar(32);index;not null"`
	Scope              string `json:"scope" gorm:"type:varchar(32);index;not null"`
	Retryability       string `json:"retryability" gorm:"type:varchar(32);index;not null"`
	HealthEffect       string `json:"health_effect" gorm:"type:varchar(32);index;not null"`
	CapacityEffect     string `json:"capacity_effect" gorm:"type:varchar(32);index;not null"`
	ClassificationRule string `json:"classification_rule" gorm:"type:varchar(64);index;not null"`
	StatusCode         int    `json:"status_code" gorm:"index;not null"`
	ErrorCode          string `json:"error_code" gorm:"type:varchar(128);index;not null"`
	ErrorMessage       string `json:"error_message" gorm:"type:text;not null"`
	PromptTokens       int64  `json:"prompt_tokens" gorm:"bigint;not null"`
	CompletionTokens   int64  `json:"completion_tokens" gorm:"bigint;not null"`
	CostNanoUSD        int64  `json:"cost_nano_usd" gorm:"bigint;not null"`
	LatencyMs          int64  `json:"latency_ms" gorm:"bigint;not null"`
	StartedTimeMs      int64  `json:"started_time_ms" gorm:"bigint;index;not null"`
	FinishedTimeMs     int64  `json:"finished_time_ms" gorm:"bigint;index;not null"`
	LeaseFencingToken  int64  `json:"lease_fencing_token" gorm:"bigint;not null"`
	NodeEpochID        string `json:"node_epoch_id" gorm:"type:varchar(128);index;not null"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingProbeResult) TableName() string {
	return "routing_probe_results"
}

type RoutingProbeResultFilter struct {
	PoolID    int
	ChannelID int
	Outcome   string
	BeforeID  int
	Limit     int
}

func CreateRoutingProbeResultContext(
	ctx context.Context,
	lease RoutingControlLease,
	result RoutingProbeResult,
) (RoutingProbeResult, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRoutingProbeResult(lease, result); err != nil {
		return RoutingProbeResult{}, false, err
	}

	created := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentLease RoutingControlLease
		if err := lockForUpdate(tx.WithContext(ctx)).Where("lease_name = ?", lease.LeaseName).First(&currentLease).Error; err != nil {
			return err
		}
		if currentLease.HolderID != lease.HolderID || currentLease.LeaseToken != lease.LeaseToken ||
			currentLease.FencingToken != lease.FencingToken || currentLease.LeaseUntilMs < result.FinishedTimeMs {
			return ErrRoutingControlLeaseLost
		}

		create := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "probe_id"}},
			DoNothing: true,
		}).Create(&result)
		if create.Error != nil {
			return create.Error
		}
		if create.RowsAffected == 1 {
			created = true
			return nil
		}

		var existing RoutingProbeResult
		if err := tx.WithContext(ctx).Where("probe_id = ?", result.ProbeID).First(&existing).Error; err != nil {
			return err
		}
		if existing.TargetKey != result.TargetKey || existing.LeaseFencingToken != result.LeaseFencingToken ||
			existing.NodeEpochID != result.NodeEpochID {
			return ErrRoutingProbeResultConflict
		}
		result = existing
		return nil
	})
	return result, created, err
}

func ListRoutingProbeResultsContext(ctx context.Context, filter RoutingProbeResultFilter) ([]RoutingProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if filter.PoolID < 0 || filter.ChannelID < 0 || filter.BeforeID < 0 ||
		(filter.Outcome != "" && !validRoutingProbeOutcome(filter.Outcome)) {
		return nil, ErrRoutingProbeResultInvalid
	}
	limit := filter.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := DB.WithContext(ctx).Model(&RoutingProbeResult{})
	if filter.PoolID > 0 {
		query = query.Where("pool_id = ?", filter.PoolID)
	}
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.Outcome != "" {
		query = query.Where("outcome = ?", filter.Outcome)
	}
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	var results []RoutingProbeResult
	err := query.Order("id DESC").Limit(limit).Find(&results).Error
	return results, err
}

func DeleteRoutingProbeResultsBeforeContext(ctx context.Context, cutoffMs int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 {
		return 0, nil
	}
	deleted := int64(0)
	for {
		var ids []int
		if err := DB.WithContext(ctx).Model(&RoutingProbeResult{}).
			Where("finished_time_ms < ?", cutoffMs).
			Order("id ASC").Limit(routingProbeRetentionBatch).
			Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		result := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingProbeResult{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
		if len(ids) < routingProbeRetentionBatch {
			return deleted, nil
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
	}
}

func validateRoutingProbeResult(lease RoutingControlLease, result RoutingProbeResult) error {
	if !validRoutingControlLeaseText(lease.LeaseName, 64) || !validRoutingControlLeaseText(lease.HolderID, 128) ||
		len(lease.LeaseToken) != 32 || lease.FencingToken <= 0 || result.LeaseFencingToken != lease.FencingToken ||
		result.NodeEpochID != lease.HolderID || len(result.ProbeID) != 64 || len(result.TargetKey) != 64 ||
		result.ProbeType != RoutingProbeTypeServing || result.SnapshotRevision <= 0 || result.PoolID <= 0 ||
		result.MemberID <= 0 || result.ChannelID <= 0 || result.CredentialID < 0 ||
		!validRoutingProbeText(result.GroupName, 64, false) || !validRoutingProbeText(result.ModelName, 128, false) ||
		!validRoutingProbeText(result.EndpointHost, 255, false) || !validRoutingProbeText(result.BreakerState, 32, true) ||
		!validRoutingProbeOutcome(result.Outcome) || !validRoutingProbeText(result.Responsibility, 32, true) ||
		!validRoutingProbeText(result.Scope, 32, true) || !validRoutingProbeText(result.Retryability, 32, true) ||
		!validRoutingProbeText(result.HealthEffect, 32, true) || !validRoutingProbeText(result.CapacityEffect, 32, true) ||
		!validRoutingProbeText(result.ClassificationRule, 64, true) || !validRoutingProbeText(result.ErrorCode, 128, true) ||
		!validRoutingProbeText(result.ErrorMessage, 1_024, true) || result.StatusCode < 0 || result.StatusCode > 599 ||
		result.PromptTokens < 0 || result.CompletionTokens < 0 || result.CostNanoUSD < 0 || result.LatencyMs < 0 ||
		result.StartedTimeMs <= 0 || result.FinishedTimeMs < result.StartedTimeMs || result.CreatedTime <= 0 {
		return ErrRoutingProbeResultInvalid
	}
	return nil
}

func validRoutingProbeOutcome(outcome string) bool {
	switch outcome {
	case RoutingProbeOutcomeSuccess, RoutingProbeOutcomeFailure, RoutingProbeOutcomeTimeout,
		RoutingProbeOutcomeCanceled, RoutingProbeOutcomeLocalError:
		return true
	default:
		return false
	}
}

func validRoutingProbeText(value string, maxRunes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return allowEmpty || value != ""
}
