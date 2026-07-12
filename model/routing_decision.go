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
	RoutingDecisionAuditMaxBatch       = 500
	RoutingDecisionReplayChunkMaxBytes = 48 << 10
	RoutingDecisionReplayMaxChunks     = 256
	RoutingDecisionReplayMaxBytes      = RoutingDecisionReplayChunkMaxBytes * RoutingDecisionReplayMaxChunks
	routingDecisionRetentionBatchSize  = 500
	routingDecisionDBBatchMaxBytes     = 1 << 20
	routingDecisionDBRowOverheadBytes  = 2 << 10
)

var (
	ErrRoutingDecisionAuditInvalid    = errors.New("invalid routing decision audit")
	ErrRoutingDecisionReplayIntegrity = errors.Join(
		ErrRoutingDecisionAuditInvalid,
		errors.New("routing decision replay integrity mismatch"),
	)
)

type RoutingDecisionAudit struct {
	ID                    int                          `json:"id" gorm:"primaryKey"`
	DecisionID            string                       `json:"decision_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	RequestID             string                       `json:"request_id" gorm:"type:varchar(64);index"`
	RequestKey            string                       `json:"-" gorm:"type:char(64);index"`
	PoolID                int                          `json:"pool_id" gorm:"index"`
	GroupName             string                       `json:"group_name" gorm:"type:varchar(64);index"`
	GroupKey              string                       `json:"-" gorm:"type:char(64);index"`
	ModelName             string                       `json:"model_name" gorm:"type:varchar(128);index"`
	ModelKey              string                       `json:"-" gorm:"type:char(64);index"`
	SnapshotRevision      int64                        `json:"snapshot_revision" gorm:"bigint;index"`
	RuntimeGeneration     int64                        `json:"runtime_generation" gorm:"bigint;index"`
	PolicyHash            string                       `json:"policy_hash" gorm:"type:char(64);index"`
	SnapshotHash          string                       `json:"snapshot_hash" gorm:"type:char(64);index"`
	ProfileHash           string                       `json:"profile_hash" gorm:"type:char(64);index"`
	AlgorithmVersion      string                       `json:"algorithm_version" gorm:"type:varchar(64);index"`
	Seed                  int64                        `json:"seed" gorm:"bigint"`
	RetryIndex            int                          `json:"retry_index"`
	IsStream              bool                         `json:"is_stream"`
	ActualChannelID       int                          `json:"actual_channel_id" gorm:"index"`
	ObservedChannelID     int                          `json:"observed_channel_id" gorm:"index"`
	CandidateCount        int                          `json:"candidate_count"`
	EligibleCount         int                          `json:"eligible_count"`
	FilteredOpen          int                          `json:"filtered_open"`
	FilteredCapacity      int                          `json:"filtered_capacity"`
	BreakerBypassed       bool                         `json:"breaker_bypassed"`
	ObservedMatchesActual bool                         `json:"observed_matches_actual" gorm:"index"`
	DifferenceType        string                       `json:"difference_type" gorm:"type:varchar(64);index"`
	ActualCostKnown       bool                         `json:"actual_cost_known"`
	ActualExpectedCost    float64                      `json:"actual_expected_cost"`
	ObservedCostKnown     bool                         `json:"observed_cost_known"`
	ObservedExpectedCost  float64                      `json:"observed_expected_cost"`
	ExpectedCostDelta     float64                      `json:"expected_cost_delta"`
	Replayable            bool                         `json:"replayable" gorm:"index"`
	RequestProfileJSON    string                       `json:"-" gorm:"type:text"`
	ReplayInputJSON       string                       `json:"-" gorm:"type:text"`
	ReplayInputHash       string                       `json:"-" gorm:"type:char(64)"`
	ReplayInputBytes      int                          `json:"-"`
	ReplayChunkCount      int                          `json:"-"`
	ReplayChunks          []RoutingDecisionReplayChunk `json:"-" gorm:"-"`
	CandidatesJSON        string                       `json:"-" gorm:"type:text"`
	CreatedTime           int64                        `json:"created_time" gorm:"bigint;index"`
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
		for start := 0; start < len(chunks); {
			end := start
			batchBytes := 0
			for end < len(chunks) {
				rowBytes := routingDecisionReplayChunkApproxBytes(chunks[end])
				if rowBytes > routingDecisionDBBatchMaxBytes {
					return ErrRoutingDecisionAuditInvalid
				}
				if end > start && batchBytes > routingDecisionDBBatchMaxBytes-rowBytes {
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
	for index := range audits {
		rowBytes := routingDecisionAuditApproxBytes(audits[index])
		if rowBytes > routingDecisionDBBatchMaxBytes {
			return nil, ErrRoutingDecisionAuditInvalid
		}
		if index > batchStart && batchBytes+rowBytes > routingDecisionDBBatchMaxBytes {
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

func routingDecisionAuditApproxBytes(audit RoutingDecisionAudit) int {
	return routingDecisionDBRowOverheadBytes +
		len(audit.DecisionID) + len(audit.RequestID) + len(audit.RequestKey) +
		len(audit.GroupName) + len(audit.GroupKey) + len(audit.ModelName) + len(audit.ModelKey) +
		len(audit.PolicyHash) + len(audit.SnapshotHash) + len(audit.ProfileHash) +
		len(audit.AlgorithmVersion) + len(audit.DifferenceType) +
		len(audit.RequestProfileJSON) + len(audit.ReplayInputJSON) + len(audit.CandidatesJSON)
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
		!validRoutingDecisionCost(audit.ActualCostKnown, audit.ActualExpectedCost) ||
		!validRoutingDecisionCost(audit.ObservedCostKnown, audit.ObservedExpectedCost) ||
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

func routingDecisionTextKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
