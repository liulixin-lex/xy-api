package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingDecisionAuditMaxBatch          = 500
	RoutingDecisionReplayChunkMaxBytes    = 48 << 10
	RoutingDecisionReplayMaxChunks        = 256
	RoutingDecisionReplayMaxBytes         = RoutingDecisionReplayChunkMaxBytes * RoutingDecisionReplayMaxChunks
	RoutingDecisionExclusionMaxReasons    = 32
	RoutingDecisionExclusionMaxBytes      = 8 << 10
	RoutingDecisionAlgorithmCanaryV1      = "channel-routing-canary-v1"
	RoutingDecisionAlgorithmCanaryV2      = "channel-routing-canary-v2"
	RoutingDecisionAlgorithmBalancedV1    = "channel-routing-balanced-v1"
	RoutingDecisionAlgorithmBalancedV2    = "channel-routing-balanced-v2"
	RoutingDecisionCohortControl          = "control"
	RoutingDecisionCohortCanary           = "canary"
	RoutingDecisionReservationLocalSoft   = "local_soft"
	RoutingDecisionReservationRedisStrict = "redis_strict"
	RoutingDecisionReservationRedisBlock  = "redis_block"
	routingDecisionRetentionBatchSize     = 500
	routingDecisionDBBatchMaxBytes        = 1 << 20
	routingDecisionDBRowOverheadBytes     = 2 << 10
	routingDecisionSQLiteBindBudget       = 900
	routingDecisionDefaultBindBudget      = 60_000
)

type RoutingDecisionExclusionCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type RoutingDecisionExclusionSummary struct {
	ExcludedCount int                             `json:"excluded_count"`
	Reasons       []RoutingDecisionExclusionCount `json:"reasons"`
}

var (
	ErrRoutingDecisionAuditInvalid    = errors.New("invalid routing decision audit")
	ErrRoutingDecisionReplayIntegrity = errors.Join(
		ErrRoutingDecisionAuditInvalid,
		errors.New("routing decision replay integrity mismatch"),
	)
)

type RoutingDecisionAudit struct {
	ID                              int                          `json:"id" gorm:"primaryKey"`
	DecisionID                      string                       `json:"decision_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	RequestID                       string                       `json:"request_id" gorm:"type:varchar(64);index"`
	RequestKey                      string                       `json:"-" gorm:"type:char(64);index"`
	PoolID                          int                          `json:"pool_id" gorm:"index"`
	GroupName                       string                       `json:"group_name" gorm:"type:varchar(64);index"`
	GroupKey                        string                       `json:"-" gorm:"type:char(64);index"`
	ModelName                       string                       `json:"model_name" gorm:"type:varchar(128);index"`
	ModelKey                        string                       `json:"-" gorm:"type:char(64);index"`
	SnapshotRevision                int64                        `json:"snapshot_revision" gorm:"bigint;index"`
	RuntimeGeneration               int64                        `json:"runtime_generation" gorm:"bigint;index"`
	ActivationID                    int64                        `json:"activation_id" gorm:"bigint;index"`
	ActivationStage                 string                       `json:"activation_stage" gorm:"type:varchar(16);index"`
	TrafficBasisPoints              int                          `json:"traffic_basis_points"`
	CanaryBucket                    int                          `json:"canary_bucket"`
	RolloutKey                      string                       `json:"rollout_key" gorm:"type:char(64);index"`
	Cohort                          string                       `json:"cohort" gorm:"type:varchar(16);index"`
	PolicyHash                      string                       `json:"policy_hash" gorm:"type:char(64);index"`
	SnapshotHash                    string                       `json:"snapshot_hash" gorm:"type:char(64);index"`
	ProfileHash                     string                       `json:"profile_hash" gorm:"type:char(64);index"`
	AlgorithmVersion                string                       `json:"algorithm_version" gorm:"type:varchar(64);index"`
	Seed                            int64                        `json:"seed" gorm:"bigint"`
	RetryIndex                      int                          `json:"retry_index"`
	IsStream                        bool                         `json:"is_stream"`
	ActualChannelID                 int                          `json:"actual_channel_id" gorm:"index"`
	ObservedChannelID               int                          `json:"observed_channel_id" gorm:"index"`
	SelectedMemberID                int                          `json:"selected_member_id" gorm:"index"`
	SelectedCredentialID            int                          `json:"selected_credential_id" gorm:"index"`
	ReservationMode                 string                       `json:"reservation_mode" gorm:"type:varchar(16);index"`
	ReservationRPM                  int64                        `json:"reservation_rpm" gorm:"bigint"`
	ReservationInputTPM             int64                        `json:"reservation_input_tpm" gorm:"bigint"`
	ReservationOutputTPM            int64                        `json:"reservation_output_tpm" gorm:"bigint"`
	ReservationInflight             int64                        `json:"reservation_inflight" gorm:"bigint"`
	ReservationLimitRPM             int64                        `json:"reservation_limit_rpm" gorm:"bigint"`
	ReservationLimitInputTPM        int64                        `json:"reservation_limit_input_tpm" gorm:"bigint"`
	ReservationLimitOutputTPM       int64                        `json:"reservation_limit_output_tpm" gorm:"bigint"`
	ReservationLimitInflight        int64                        `json:"reservation_limit_inflight" gorm:"bigint"`
	ReservationAccountID            int                          `json:"reservation_account_id" gorm:"index"`
	ReservationResourceCredentialID int                          `json:"reservation_resource_credential_id" gorm:"index"`
	ReservationResourceModel        string                       `json:"reservation_resource_model" gorm:"type:varchar(128);index"`
	ReservationTotalTPM             int64                        `json:"reservation_total_tpm" gorm:"bigint"`
	ReservationCostNanoUSD          int64                        `json:"reservation_cost_nano_usd" gorm:"bigint"`
	ReservationLimitTotalTPM        int64                        `json:"reservation_limit_total_tpm" gorm:"bigint"`
	ReservationLimitCostNanoUSD     int64                        `json:"reservation_limit_cost_nano_usd" gorm:"bigint"`
	ReservationLeaseExpiresMs       int64                        `json:"reservation_lease_expires_ms" gorm:"bigint"`
	ReservationPoolSharesJSON       string                       `json:"-" gorm:"type:text"`
	CandidateCount                  int                          `json:"candidate_count"`
	EligibleCount                   int                          `json:"eligible_count"`
	FilteredOpen                    int                          `json:"filtered_open"`
	FilteredCapacity                int                          `json:"filtered_capacity"`
	BreakerBypassed                 bool                         `json:"breaker_bypassed"`
	ObservedMatchesActual           bool                         `json:"observed_matches_actual" gorm:"index"`
	DifferenceType                  string                       `json:"difference_type" gorm:"type:varchar(64);index"`
	ActualCostKnown                 bool                         `json:"actual_cost_known"`
	ActualExpectedCost              float64                      `json:"actual_expected_cost"`
	ObservedCostKnown               bool                         `json:"observed_cost_known"`
	ObservedExpectedCost            float64                      `json:"observed_expected_cost"`
	ExpectedCostDelta               float64                      `json:"expected_cost_delta"`
	ActualCostEstimateJSON          string                       `json:"-" gorm:"type:text"`
	ObservedCostEstimateJSON        string                       `json:"-" gorm:"type:text"`
	Replayable                      bool                         `json:"replayable" gorm:"index"`
	RequestProfileJSON              string                       `json:"-" gorm:"type:text"`
	ReplayInputJSON                 string                       `json:"-" gorm:"type:text"`
	ReplayInputHash                 string                       `json:"-" gorm:"type:char(64)"`
	ReplayInputBytes                int                          `json:"-"`
	ReplayChunkCount                int                          `json:"-"`
	ReplayChunks                    []RoutingDecisionReplayChunk `json:"-" gorm:"-"`
	CandidatesJSON                  string                       `json:"-" gorm:"type:text"`
	ExclusionSummaryJSON            string                       `json:"-" gorm:"type:text"`
	CreatedTime                     int64                        `json:"created_time" gorm:"bigint;index"`
}

func (RoutingDecisionAudit) TableName() string {
	return "routing_decision_audits"
}

type RoutingDecisionReplayChunk struct {
	ID           int64  `json:"id" gorm:"primaryKey"`
	DecisionID   string `json:"decision_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_routing_decision_replay_chunk,priority:1;index"`
	ChunkIndex   int    `json:"chunk_index" gorm:"not null;uniqueIndex:idx_routing_decision_replay_chunk,priority:2"`
	ChunkCount   int    `json:"chunk_count" gorm:"not null"`
	PayloadBytes int    `json:"payload_bytes" gorm:"not null"`
	PayloadHash  string `json:"payload_hash" gorm:"type:char(64);not null"`
	Payload      string `json:"-" gorm:"type:text;not null"`
	CreatedTime  int64  `json:"created_time" gorm:"bigint;index;not null"`
}

func (RoutingDecisionReplayChunk) TableName() string {
	return "routing_decision_replay_chunks"
}

func NewRoutingDecisionReplayChunks(
	decisionID string,
	payload []byte,
	createdTime int64,
) (string, []RoutingDecisionReplayChunk, error) {
	if decisionID == "" || len(decisionID) > 64 || createdTime <= 0 || len(payload) == 0 ||
		len(payload) > RoutingDecisionReplayMaxBytes || !utf8.Valid(payload) {
		return "", nil, ErrRoutingDecisionAuditInvalid
	}
	var decoded any
	if err := common.Unmarshal(payload, &decoded); err != nil || decoded == nil {
		return "", nil, ErrRoutingDecisionAuditInvalid
	}

	payloads := make([]string, 0, (len(payload)+RoutingDecisionReplayChunkMaxBytes-1)/RoutingDecisionReplayChunkMaxBytes)
	for start := 0; start < len(payload); {
		end := min(start+RoutingDecisionReplayChunkMaxBytes, len(payload))
		for end < len(payload) && !utf8.RuneStart(payload[end]) {
			end--
		}
		if end <= start {
			return "", nil, ErrRoutingDecisionAuditInvalid
		}
		payloads = append(payloads, string(payload[start:end]))
		start = end
	}
	if len(payloads) == 0 || len(payloads) > RoutingDecisionReplayMaxChunks {
		return "", nil, ErrRoutingDecisionAuditInvalid
	}

	chunks := make([]RoutingDecisionReplayChunk, len(payloads))
	for index := range payloads {
		chunkHash := sha256.Sum256([]byte(payloads[index]))
		chunks[index] = RoutingDecisionReplayChunk{
			DecisionID:   decisionID,
			ChunkIndex:   index,
			ChunkCount:   len(payloads),
			PayloadBytes: len(payloads[index]),
			PayloadHash:  hex.EncodeToString(chunkHash[:]),
			Payload:      payloads[index],
			CreatedTime:  createdTime,
		}
	}
	totalHash := sha256.Sum256(payload)
	return hex.EncodeToString(totalHash[:]), chunks, nil
}

func LoadRoutingDecisionReplayInputContext(ctx context.Context, audit RoutingDecisionAudit) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !audit.Replayable {
		return "", ErrRoutingDecisionAuditInvalid
	}
	if audit.ReplayChunkCount == 0 {
		if validRoutingDecisionReplayPayload(audit, audit.ReplayInputJSON) {
			return audit.ReplayInputJSON, nil
		}
		return "", ErrRoutingDecisionReplayIntegrity
	}
	if DB == nil || audit.ReplayInputJSON != "" || audit.ReplayChunkCount < 1 ||
		audit.ReplayChunkCount > RoutingDecisionReplayMaxChunks {
		return "", ErrRoutingDecisionAuditInvalid
	}
	var chunks []RoutingDecisionReplayChunk
	if err := DB.WithContext(ctx).
		Where("decision_id = ?", audit.DecisionID).
		Order("chunk_index asc").
		Limit(audit.ReplayChunkCount + 1).
		Find(&chunks).Error; err != nil {
		return "", err
	}
	payload, err := joinRoutingDecisionReplayChunks(audit, chunks)
	if err != nil {
		return "", err
	}
	return payload, nil
}

func CreateRoutingDecisionAuditsContext(ctx context.Context, audits []RoutingDecisionAudit) error {
	if len(audits) == 0 {
		return nil
	}
	if len(audits) > RoutingDecisionAuditMaxBatch {
		return errors.New("routing decision audit batch exceeds limit")
	}
	normalized := append([]RoutingDecisionAudit(nil), audits...)
	chunks := make([]RoutingDecisionReplayChunk, 0)
	for index := range normalized {
		audit := &normalized[index]
		audit.ReplayChunks = append([]RoutingDecisionReplayChunk(nil), audit.ReplayChunks...)
		if !validRoutingDecisionAudit(audit) {
			return ErrRoutingDecisionAuditInvalid
		}
		audit.GroupKey = RoutingDecisionGroupKey(audit.GroupName)
		audit.ModelKey = RoutingDecisionModelKey(audit.ModelName)
		audit.RequestKey = RoutingDecisionRequestKey(audit.RequestID)
		chunks = append(chunks, audit.ReplayChunks...)
		audit.ReplayChunks = nil
	}
	batches, err := splitRoutingDecisionAuditDBBatches(normalized)
	if err != nil {
		return err
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index := range batches {
			if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "decision_id"}},
				DoNothing: true,
			}).Create(&batches[index]).Error; err != nil {
				return err
			}
		}
		chunkBatchRows := routingDecisionDBBatchRowLimit(&RoutingDecisionReplayChunk{})
		for start := 0; start < len(chunks); {
			end := start
			batchBytes := 0
			for end < len(chunks) {
				rowBytes := routingDecisionReplayChunkApproxBytes(chunks[end])
				if rowBytes > routingDecisionDBBatchMaxBytes {
					return ErrRoutingDecisionAuditInvalid
				}
				if end > start && (end-start >= chunkBatchRows || batchBytes > routingDecisionDBBatchMaxBytes-rowBytes) {
					break
				}
				batchBytes += rowBytes
				end++
			}
			batch := chunks[start:end]
			if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "decision_id"}, {Name: "chunk_index"}},
				DoNothing: true,
			}).Create(&batch).Error; err != nil {
				return err
			}
			start = end
		}
		return nil
	})
}

func splitRoutingDecisionAuditDBBatches(audits []RoutingDecisionAudit) ([][]RoutingDecisionAudit, error) {
	batches := make([][]RoutingDecisionAudit, 0, 1)
	batchStart := 0
	batchBytes := 0
	batchRows := routingDecisionDBBatchRowLimit(&RoutingDecisionAudit{})
	for index := range audits {
		rowBytes := routingDecisionAuditApproxBytes(audits[index])
		if rowBytes > routingDecisionDBBatchMaxBytes {
			return nil, ErrRoutingDecisionAuditInvalid
		}
		if index > batchStart && (index-batchStart >= batchRows || batchBytes+rowBytes > routingDecisionDBBatchMaxBytes) {
			batches = append(batches, audits[batchStart:index])
			batchStart = index
			batchBytes = 0
		}
		batchBytes += rowBytes
	}
	if batchStart < len(audits) {
		batches = append(batches, audits[batchStart:])
	}
	return batches, nil
}

func routingDecisionDBBatchRowLimit(value any) int {
	bindBudget := routingDecisionDefaultBindBudget
	if DB != nil && DB.Dialector != nil && DB.Dialector.Name() == "sqlite" {
		bindBudget = routingDecisionSQLiteBindBudget
	}
	if DB == nil {
		return 1
	}
	statement := &gorm.Statement{DB: DB}
	if err := statement.Parse(value); err != nil || statement.Schema == nil {
		return 1
	}
	columns := 0
	for _, field := range statement.Schema.Fields {
		if field.Creatable && field.DBName != "" {
			columns++
		}
	}
	if columns < 1 {
		return 1
	}
	return max(bindBudget/columns, 1)
}

func routingDecisionAuditApproxBytes(audit RoutingDecisionAudit) int {
	return routingDecisionDBRowOverheadBytes +
		len(audit.DecisionID) + len(audit.RequestID) + len(audit.RequestKey) +
		len(audit.GroupName) + len(audit.GroupKey) + len(audit.ModelName) + len(audit.ModelKey) +
		len(audit.PolicyHash) + len(audit.SnapshotHash) + len(audit.ProfileHash) +
		len(audit.AlgorithmVersion) + len(audit.DifferenceType) + len(audit.ActivationStage) +
		len(audit.RolloutKey) + len(audit.Cohort) + len(audit.ReservationMode) +
		len(audit.ReservationResourceModel) +
		len(audit.ReservationPoolSharesJSON) + len(audit.ActualCostEstimateJSON) +
		len(audit.ObservedCostEstimateJSON) + len(audit.RequestProfileJSON) + len(audit.ReplayInputJSON) +
		len(audit.CandidatesJSON) + len(audit.ExclusionSummaryJSON)
}

func routingDecisionReplayChunkApproxBytes(chunk RoutingDecisionReplayChunk) int {
	return routingDecisionDBRowOverheadBytes + len(chunk.DecisionID) + len(chunk.PayloadHash) + len(chunk.Payload)
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
		deleted := int64(0)
		if err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("decision_id IN (?)", tx.Model(&RoutingDecisionAudit{}).Select("decision_id").Where("id IN ?", ids)).
				Delete(&RoutingDecisionReplayChunk{}).Error; err != nil {
				return err
			}
			result := tx.Where("id IN ?", ids).Delete(&RoutingDecisionAudit{})
			deleted = result.RowsAffected
			return result.Error
		}); err != nil {
			return total + deleted, err
		}
		total += deleted
		if len(ids) < routingDecisionRetentionBatchSize {
			return total, nil
		}
	}
}

func validRoutingDecisionAudit(audit *RoutingDecisionAudit) bool {
	if audit == nil || audit.DecisionID == "" || audit.GroupName == "" || audit.ModelName == "" ||
		audit.PoolID <= 0 || audit.SnapshotRevision <= 0 || audit.RetryIndex < 0 {
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
		{value: audit.DifferenceType, maxRunes: 64},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.maxRunes {
			return false
		}
	}
	if !utf8.ValidString(audit.CandidatesJSON) || len(audit.CandidatesJSON) > 60<<10 ||
		!utf8.ValidString(audit.ReservationPoolSharesJSON) || len(audit.ReservationPoolSharesJSON) > 8<<10 ||
		!validRoutingDecisionCanaryMetadata(audit) || !validRoutingDecisionExclusionSummary(audit.ExclusionSummaryJSON) ||
		!validRoutingDecisionCost(audit.ActualCostKnown, audit.ActualExpectedCost) ||
		!validRoutingDecisionCost(audit.ObservedCostKnown, audit.ObservedExpectedCost) ||
		!validOptionalRoutingDecisionJSON(audit.ActualCostEstimateJSON, 32<<10) ||
		!validOptionalRoutingDecisionJSON(audit.ObservedCostEstimateJSON, 32<<10) ||
		math.IsNaN(audit.ExpectedCostDelta) || math.IsInf(audit.ExpectedCostDelta, 0) {
		return false
	}
	if !audit.Replayable {
		return audit.RequestProfileJSON == "" && audit.ReplayInputJSON == "" && audit.ReplayInputHash == "" &&
			audit.ReplayInputBytes == 0 && audit.ReplayChunkCount == 0 && len(audit.ReplayChunks) == 0
	}
	if audit.RuntimeGeneration <= 0 || !validRoutingDecisionHash(audit.PolicyHash) ||
		!validRoutingDecisionHash(audit.SnapshotHash) || !validRoutingDecisionHash(audit.ProfileHash) ||
		!validRoutingDecisionHash(audit.ReplayInputHash) || audit.ReplayInputBytes < 1 ||
		audit.ReplayInputBytes > RoutingDecisionReplayMaxBytes || audit.DifferenceType == "" ||
		!validRoutingDecisionJSON(audit.RequestProfileJSON) {
		return false
	}
	profileHash := sha256.Sum256([]byte(audit.RequestProfileJSON))
	if audit.ProfileHash != hex.EncodeToString(profileHash[:]) {
		return false
	}
	if audit.ReplayChunkCount == 0 {
		return len(audit.ReplayChunks) == 0 && validRoutingDecisionReplayPayload(*audit, audit.ReplayInputJSON)
	}
	if audit.ReplayInputJSON != "" || audit.ReplayChunkCount < 1 ||
		audit.ReplayChunkCount > RoutingDecisionReplayMaxChunks || len(audit.ReplayChunks) != audit.ReplayChunkCount {
		return false
	}
	_, err := joinRoutingDecisionReplayChunks(*audit, audit.ReplayChunks)
	return err == nil
}

func validRoutingDecisionCanaryMetadata(audit *RoutingDecisionAudit) bool {
	if audit == nil {
		return false
	}
	hasCanaryMetadata := audit.ActivationID != 0 || audit.ActivationStage != "" || audit.TrafficBasisPoints != 0 ||
		audit.CanaryBucket != 0 || audit.RolloutKey != "" || audit.Cohort != ""
	hasSelectedIdentity := audit.SelectedMemberID != 0 || audit.SelectedCredentialID != 0
	hasReservation := audit.ReservationMode != "" || audit.ReservationRPM != 0 || audit.ReservationInputTPM != 0 ||
		audit.ReservationOutputTPM != 0 || audit.ReservationInflight != 0 || audit.ReservationLimitRPM != 0 ||
		audit.ReservationLimitInputTPM != 0 || audit.ReservationLimitOutputTPM != 0 || audit.ReservationLimitInflight != 0 ||
		audit.ReservationAccountID != 0 || audit.ReservationResourceCredentialID != 0 || audit.ReservationTotalTPM != 0 ||
		audit.ReservationResourceModel != "" ||
		audit.ReservationCostNanoUSD != 0 || audit.ReservationLimitTotalTPM != 0 ||
		audit.ReservationLimitCostNanoUSD != 0 || audit.ReservationLeaseExpiresMs != 0 ||
		audit.ReservationPoolSharesJSON != ""
	if audit.AlgorithmVersion == RoutingDecisionAlgorithmBalancedV1 ||
		audit.AlgorithmVersion == RoutingDecisionAlgorithmBalancedV2 {
		if audit.ActivationID <= 0 || audit.ActivationStage != RoutingDeploymentStageActive ||
			audit.TrafficBasisPoints != 0 || audit.CanaryBucket != 0 || audit.RolloutKey != "" || audit.Cohort != "" ||
			audit.SelectedMemberID < 0 || audit.SelectedCredentialID < 0 ||
			(audit.SelectedCredentialID > 0 && audit.SelectedMemberID == 0) || !audit.Replayable {
			return false
		}
		if audit.ObservedChannelID <= 0 {
			return audit.ActualChannelID == 0 && !hasSelectedIdentity && !hasReservation
		}
		return audit.ActualChannelID == audit.ObservedChannelID && audit.SelectedMemberID > 0 &&
			validRoutingDecisionReservation(audit)
	}
	if !hasCanaryMetadata {
		return !hasSelectedIdentity && !hasReservation
	}
	if (audit.AlgorithmVersion != RoutingDecisionAlgorithmCanaryV1 &&
		audit.AlgorithmVersion != RoutingDecisionAlgorithmCanaryV2) || audit.ActivationID <= 0 ||
		audit.ActivationStage != RoutingDeploymentStageCanary ||
		audit.TrafficBasisPoints < RoutingPolicyCanaryMinBasisPoints ||
		audit.TrafficBasisPoints > RoutingPolicyCanaryMaxBasisPoints || audit.CanaryBucket < 0 ||
		audit.CanaryBucket >= 10_000 || !validRoutingDecisionHash(audit.RolloutKey) ||
		audit.RolloutKey != strings.ToLower(audit.RolloutKey) || audit.SelectedMemberID < 0 ||
		audit.SelectedCredentialID < 0 || (audit.SelectedCredentialID > 0 && audit.SelectedMemberID == 0) {
		return false
	}
	inCanary := audit.CanaryBucket < audit.TrafficBasisPoints
	if (inCanary && audit.Cohort != RoutingDecisionCohortCanary) ||
		(!inCanary && audit.Cohort != RoutingDecisionCohortControl) {
		return false
	}
	if audit.ObservedChannelID > 0 {
		if audit.SelectedMemberID <= 0 || audit.ActualChannelID != audit.ObservedChannelID {
			return false
		}
	} else if hasSelectedIdentity || hasReservation || audit.ActualChannelID != 0 {
		return false
	}
	if audit.Cohort == RoutingDecisionCohortControl {
		return !audit.Replayable && !hasReservation
	}
	if !audit.Replayable {
		return false
	}
	if audit.ObservedChannelID == 0 {
		return !hasReservation
	}
	return validRoutingDecisionReservation(audit)
}

func validRoutingDecisionReservation(audit *RoutingDecisionAudit) bool {
	if audit == nil {
		return false
	}
	demand := [4]int64{audit.ReservationRPM, audit.ReservationInputTPM, audit.ReservationOutputTPM, audit.ReservationInflight}
	limit := [4]int64{audit.ReservationLimitRPM, audit.ReservationLimitInputTPM, audit.ReservationLimitOutputTPM, audit.ReservationLimitInflight}
	demandKnown := false
	limitKnown := false
	for index := range demand {
		if demand[index] < 0 || limit[index] < 0 || (demand[index] > 0 && limit[index] <= 0) || demand[index] > limit[index] {
			return false
		}
		demandKnown = demandKnown || demand[index] > 0
		limitKnown = limitKnown || limit[index] > 0
	}
	if !demandKnown || !limitKnown {
		return false
	}
	switch audit.ReservationMode {
	case RoutingDecisionReservationLocalSoft:
		return audit.ReservationAccountID == 0 && audit.ReservationResourceCredentialID == 0 &&
			audit.ReservationResourceModel == "" &&
			audit.ReservationTotalTPM == 0 && audit.ReservationCostNanoUSD == 0 &&
			audit.ReservationLimitTotalTPM == 0 && audit.ReservationLimitCostNanoUSD == 0 &&
			audit.ReservationLeaseExpiresMs == 0 && audit.ReservationPoolSharesJSON == ""
	case RoutingDecisionReservationRedisStrict, RoutingDecisionReservationRedisBlock:
		if (audit.ReservationAccountID <= 0 && audit.ReservationResourceCredentialID <= 0) ||
			audit.ReservationResourceModel == "" || !utf8.ValidString(audit.ReservationResourceModel) ||
			len([]rune(audit.ReservationResourceModel)) > 128 ||
			audit.ReservationTotalTPM < 0 || audit.ReservationCostNanoUSD < 0 ||
			audit.ReservationLimitTotalTPM <= 0 || audit.ReservationLimitCostNanoUSD < 0 ||
			audit.ReservationTotalTPM > audit.ReservationLimitTotalTPM ||
			audit.ReservationCostNanoUSD > audit.ReservationLimitCostNanoUSD ||
			audit.ReservationLeaseExpiresMs <= 0 || audit.ReservationPoolSharesJSON == "" {
			return false
		}
		if audit.ReservationInputTPM > math.MaxInt64-audit.ReservationOutputTPM ||
			audit.ReservationTotalTPM != audit.ReservationInputTPM+audit.ReservationOutputTPM {
			return false
		}
		return validRoutingDecisionPoolShares(audit.PoolID, audit.ReservationPoolSharesJSON)
	default:
		return false
	}
}

func validRoutingDecisionPoolShares(poolID int, value string) bool {
	type poolShare struct {
		PoolID                int `json:"pool_id"`
		GuaranteedBasisPoints int `json:"guaranteed_basis_points"`
		MaximumBasisPoints    int `json:"maximum_basis_points"`
	}
	var shares []poolShare
	if common.UnmarshalJsonStr(value, &shares) != nil || len(shares) == 0 || len(shares) > 128 {
		return false
	}
	found := false
	guaranteedTotal := 0
	for index, share := range shares {
		if share.PoolID <= 0 || share.GuaranteedBasisPoints < 0 || share.MaximumBasisPoints < 1 ||
			share.MaximumBasisPoints > 10_000 || share.GuaranteedBasisPoints > share.MaximumBasisPoints ||
			(index > 0 && shares[index-1].PoolID >= share.PoolID) {
			return false
		}
		guaranteedTotal += share.GuaranteedBasisPoints
		if guaranteedTotal > 10_000 {
			return false
		}
		found = found || share.PoolID == poolID
	}
	return found
}

func validRoutingDecisionExclusionSummary(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || len(value) > RoutingDecisionExclusionMaxBytes {
		return false
	}
	var summary RoutingDecisionExclusionSummary
	if common.UnmarshalJsonStr(value, &summary) != nil || summary.ExcludedCount < 0 ||
		len(summary.Reasons) > RoutingDecisionExclusionMaxReasons {
		return false
	}
	total := 0
	previous := ""
	for index := range summary.Reasons {
		item := summary.Reasons[index]
		if item.Count <= 0 || item.Reason == "" || !utf8.ValidString(item.Reason) ||
			utf8.RuneCountInString(item.Reason) > 128 || (index > 0 && item.Reason <= previous) {
			return false
		}
		total += item.Count
		previous = item.Reason
	}
	return total == summary.ExcludedCount && (summary.ExcludedCount == 0) == (len(summary.Reasons) == 0)
}

func joinRoutingDecisionReplayChunks(audit RoutingDecisionAudit, chunks []RoutingDecisionReplayChunk) (string, error) {
	if len(chunks) != audit.ReplayChunkCount || audit.ReplayChunkCount < 1 ||
		audit.ReplayChunkCount > RoutingDecisionReplayMaxChunks {
		return "", ErrRoutingDecisionReplayIntegrity
	}
	var payload strings.Builder
	payload.Grow(audit.ReplayInputBytes)
	for index := range chunks {
		chunk := chunks[index]
		chunkHash := sha256.Sum256([]byte(chunk.Payload))
		if chunk.DecisionID != audit.DecisionID || chunk.ChunkIndex != index ||
			chunk.ChunkCount != audit.ReplayChunkCount || chunk.PayloadBytes != len(chunk.Payload) ||
			chunk.PayloadBytes < 1 || chunk.PayloadBytes > RoutingDecisionReplayChunkMaxBytes ||
			chunk.PayloadHash != hex.EncodeToString(chunkHash[:]) || !utf8.ValidString(chunk.Payload) {
			return "", ErrRoutingDecisionReplayIntegrity
		}
		payload.WriteString(chunk.Payload)
		if payload.Len() > RoutingDecisionReplayMaxBytes {
			return "", ErrRoutingDecisionReplayIntegrity
		}
	}
	joined := payload.String()
	if !validRoutingDecisionReplayPayload(audit, joined) {
		return "", ErrRoutingDecisionReplayIntegrity
	}
	return joined, nil
}

func validRoutingDecisionReplayPayload(audit RoutingDecisionAudit, payload string) bool {
	if len(payload) != audit.ReplayInputBytes || len(payload) < 1 ||
		len(payload) > RoutingDecisionReplayMaxBytes || !utf8.ValidString(payload) {
		return false
	}
	payloadHash := sha256.Sum256([]byte(payload))
	if audit.ReplayInputHash != hex.EncodeToString(payloadHash[:]) {
		return false
	}
	var replayIdentity struct {
		SnapshotHash string `json:"snapshot_hash"`
		PolicyHash   string `json:"policy_hash"`
	}
	if err := common.UnmarshalJsonStr(payload, &replayIdentity); err != nil {
		return false
	}
	return replayIdentity.SnapshotHash == audit.SnapshotHash && replayIdentity.PolicyHash == audit.PolicyHash
}

func validRoutingDecisionCost(known bool, value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return false
	}
	return known || value == 0
}

func validRoutingDecisionHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRoutingDecisionJSON(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > 60<<10 {
		return false
	}
	var decoded any
	return common.UnmarshalJsonStr(value, &decoded) == nil && decoded != nil
}

func validOptionalRoutingDecisionJSON(value string, maxBytes int) bool {
	if value == "" {
		return true
	}
	if maxBytes < 1 || !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	var decoded any
	return common.UnmarshalJsonStr(value, &decoded) == nil && decoded != nil
}

func routingDecisionTextKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
