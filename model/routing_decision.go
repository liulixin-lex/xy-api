package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"unicode/utf8"

	"gorm.io/gorm/clause"
)

const (
	RoutingDecisionAuditMaxBatch      = 500
	routingDecisionRetentionBatchSize = 500
)

var ErrRoutingDecisionAuditInvalid = errors.New("invalid routing decision audit")

type RoutingDecisionAudit struct {
	ID                    int    `json:"id" gorm:"primaryKey"`
	DecisionID            string `json:"decision_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	RequestID             string `json:"request_id" gorm:"type:varchar(64);index"`
	RequestKey            string `json:"-" gorm:"type:char(64);index"`
	PoolID                int    `json:"pool_id" gorm:"index"`
	GroupName             string `json:"group_name" gorm:"type:varchar(64);index"`
	GroupKey              string `json:"-" gorm:"type:char(64);index"`
	ModelName             string `json:"model_name" gorm:"type:varchar(128);index"`
	ModelKey              string `json:"-" gorm:"type:char(64);index"`
	SnapshotRevision      int64  `json:"snapshot_revision" gorm:"bigint;index"`
	AlgorithmVersion      string `json:"algorithm_version" gorm:"type:varchar(64);index"`
	RetryIndex            int    `json:"retry_index"`
	IsStream              bool   `json:"is_stream"`
	ActualChannelID       int    `json:"actual_channel_id" gorm:"index"`
	ObservedChannelID     int    `json:"observed_channel_id" gorm:"index"`
	CandidateCount        int    `json:"candidate_count"`
	EligibleCount         int    `json:"eligible_count"`
	FilteredOpen          int    `json:"filtered_open"`
	FilteredCapacity      int    `json:"filtered_capacity"`
	BreakerBypassed       bool   `json:"breaker_bypassed"`
	ObservedMatchesActual bool   `json:"observed_matches_actual" gorm:"index"`
	CandidatesJSON        string `json:"-" gorm:"type:text"`
	CreatedTime           int64  `json:"created_time" gorm:"bigint;index"`
}

func (RoutingDecisionAudit) TableName() string {
	return "routing_decision_audits"
}

func CreateRoutingDecisionAuditsContext(ctx context.Context, audits []RoutingDecisionAudit) error {
	if len(audits) == 0 {
		return nil
	}
	if len(audits) > RoutingDecisionAuditMaxBatch {
		return errors.New("routing decision audit batch exceeds limit")
	}
	normalized := append([]RoutingDecisionAudit(nil), audits...)
	for index := range normalized {
		audit := &normalized[index]
		if !validRoutingDecisionAudit(audit) {
			return ErrRoutingDecisionAuditInvalid
		}
		audit.GroupKey = RoutingDecisionGroupKey(audit.GroupName)
		audit.ModelKey = RoutingDecisionModelKey(audit.ModelName)
		audit.RequestKey = RoutingDecisionRequestKey(audit.RequestID)
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "decision_id"}},
		DoNothing: true,
	}).CreateInBatches(normalized, RoutingDecisionAuditMaxBatch).Error
}

func RoutingDecisionGroupKey(groupName string) string {
	return routingDecisionTextKey(groupName)
}

func RoutingDecisionModelKey(modelName string) string {
	return routingDecisionTextKey(modelName)
}

func RoutingDecisionRequestKey(requestID string) string {
	return routingDecisionTextKey(requestID)
}

func DeleteRoutingDecisionAuditsBeforeContext(ctx context.Context, cutoff int64) (int64, error) {
	if cutoff <= 0 {
		return 0, nil
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []int
		if err := DB.WithContext(ctx).Model(&RoutingDecisionAudit{}).
			Where("created_time < ?", cutoff).
			Order("created_time asc").
			Order("id asc").
			Limit(routingDecisionRetentionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		result := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&RoutingDecisionAudit{})
		total += result.RowsAffected
		if result.Error != nil {
			return total, result.Error
		}
		if len(ids) < routingDecisionRetentionBatchSize {
			return total, nil
		}
	}
}

func validRoutingDecisionAudit(audit *RoutingDecisionAudit) bool {
	if audit == nil || audit.DecisionID == "" || audit.GroupName == "" || audit.ModelName == "" || audit.PoolID <= 0 {
		return false
	}
	for _, field := range []struct {
		value    string
		maxRunes int
	}{
		{value: audit.DecisionID, maxRunes: 64},
		{value: audit.RequestID, maxRunes: 64},
		{value: audit.GroupName, maxRunes: 64},
		{value: audit.ModelName, maxRunes: 128},
		{value: audit.AlgorithmVersion, maxRunes: 64},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.maxRunes {
			return false
		}
	}
	return utf8.ValidString(audit.CandidatesJSON) && len(audit.CandidatesJSON) <= 60<<10
}

func routingDecisionTextKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
