package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingAuditExportMaxRecords      = 5_000
	RoutingAuditExportMaxRangeSeconds = int64(31 * 24 * 60 * 60)
	RoutingAuditExportMaxBytes        = 8 << 20
	routingAuditExportChunkMaxBytes   = 60 << 10
	routingAuditExportRetention       = 7 * 24 * time.Hour
)

var (
	ErrRoutingAuditExportInvalid  = errors.New("invalid channel routing audit export")
	ErrRoutingAuditExportTooLarge = errors.New("channel routing audit export exceeds the payload limit")
)

type RoutingAuditExportRequest struct {
	FromTime int64 `json:"from_time"`
	ToTime   int64 `json:"to_time"`
	Limit    int   `json:"limit"`
}

// RoutingAuditExportItem is intentionally a strict allowlist. In particular it
// excludes request/replay payloads, candidate payloads, credential identifiers,
// endpoint URLs, and upstream error text.
type RoutingAuditExportItem struct {
	AuditID                   int                               `json:"audit_id"`
	DecisionID                string                            `json:"decision_id"`
	RequestID                 string                            `json:"-"`
	PoolID                    int                               `json:"pool_id"`
	GroupName                 string                            `json:"group_name"`
	ModelName                 string                            `json:"model_name"`
	SnapshotRevision          int64                             `json:"snapshot_revision"`
	RuntimeGeneration         int64                             `json:"runtime_generation"`
	ActivationID              int64                             `json:"activation_id"`
	ActivationStage           string                            `json:"activation_stage"`
	TrafficBasisPoints        int                               `json:"traffic_basis_points"`
	Cohort                    string                            `json:"cohort,omitempty"`
	AlgorithmVersion          string                            `json:"algorithm_version"`
	RetryIndex                int                               `json:"retry_index"`
	IsStream                  bool                              `json:"is_stream"`
	ActualChannelID           int                               `json:"actual_channel_id"`
	ActualChannelGeneration   string                            `json:"actual_channel_generation,omitempty"`
	ObservedChannelID         int                               `json:"observed_channel_id"`
	ObservedChannelGeneration string                            `json:"observed_channel_generation,omitempty"`
	CandidateCount            int                               `json:"candidate_count"`
	EligibleCount             int                               `json:"eligible_count"`
	FilteredOpen              int                               `json:"filtered_open"`
	FilteredCapacity          int                               `json:"filtered_capacity"`
	BreakerBypassed           bool                              `json:"breaker_bypassed"`
	ObservedMatchesActual     bool                              `json:"observed_matches_actual"`
	DifferenceType            string                            `json:"difference_type,omitempty"`
	ActualCostKnown           bool                              `json:"actual_cost_known"`
	ActualExpectedCost        float64                           `json:"actual_expected_cost,omitempty"`
	ObservedCostKnown         bool                              `json:"observed_cost_known"`
	ObservedExpectedCost      float64                           `json:"observed_expected_cost,omitempty"`
	ExpectedCostDelta         float64                           `json:"expected_cost_delta,omitempty"`
	Replayable                bool                              `json:"replayable"`
	AttemptTimeline           *RoutingHedgeDecisionAuditSummary `json:"attempt_timeline,omitempty" gorm:"-"`
	Hedge                     *RoutingHedgeDecisionAuditSummary `json:"hedge,omitempty" gorm:"-"`
	CreatedTime               int64                             `json:"created_time"`
}

type RoutingAuditExport struct {
	ExportID      string `json:"export_id" gorm:"type:varchar(48);primaryKey"`
	OperationID   int64  `json:"operation_id" gorm:"bigint;uniqueIndex;not null"`
	ActorID       int    `json:"actor_id" gorm:"index;not null"`
	FromTime      int64  `json:"from_time" gorm:"bigint;index;not null"`
	ToTime        int64  `json:"to_time" gorm:"bigint;index;not null"`
	RecordCount   int    `json:"record_count" gorm:"not null"`
	ContentBytes  int    `json:"content_bytes" gorm:"not null"`
	ContentHash   string `json:"content_hash" gorm:"type:char(64);index;not null"`
	ChunkCount    int    `json:"chunk_count" gorm:"not null"`
	CreatedTimeMs int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
	ExpiresTimeMs int64  `json:"expires_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingAuditExport) TableName() string {
	return "routing_audit_exports"
}

type RoutingAuditExportChunk struct {
	ID           int64  `json:"-" gorm:"primaryKey"`
	ExportID     string `json:"-" gorm:"type:varchar(48);not null;uniqueIndex:idx_routing_audit_export_chunk,priority:1;index"`
	ChunkIndex   int    `json:"-" gorm:"not null;uniqueIndex:idx_routing_audit_export_chunk,priority:2"`
	ChunkCount   int    `json:"-" gorm:"not null"`
	PayloadBytes int    `json:"-" gorm:"not null"`
	PayloadHash  string `json:"-" gorm:"type:char(64);not null"`
	Payload      string `json:"-" gorm:"type:text;not null"`
}

func (RoutingAuditExportChunk) TableName() string {
	return "routing_audit_export_chunks"
}

type RoutingAuditExportResult struct {
	ExportID      string `json:"export_id"`
	RecordCount   int    `json:"record_count"`
	ContentBytes  int    `json:"content_bytes"`
	ContentHash   string `json:"content_hash"`
	CreatedTimeMs int64  `json:"created_time_ms"`
	ExpiresTimeMs int64  `json:"expires_time_ms"`
}

func CreateRoutingAuditExportContext(
	ctx context.Context,
	request RoutingAuditExportRequest,
	actorID int,
	identity RoutingOperationRequestIdentity,
) (RoutingAuditExport, RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.FromTime <= 0 || request.ToTime < request.FromTime ||
		request.ToTime-request.FromTime > RoutingAuditExportMaxRangeSeconds ||
		request.Limit < 1 || request.Limit > RoutingAuditExportMaxRecords || actorID < 0 ||
		!validRoutingOperationRequestIdentity(identity) {
		return RoutingAuditExport{}, RoutingOperation{}, false, ErrRoutingAuditExportInvalid
	}

	var export RoutingAuditExport
	var operation RoutingOperation
	created := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		existing, found, err := getRoutingOperationByRequestIdentityDB(ctx, tx, identity)
		if err != nil {
			return err
		}
		if found {
			if existing.OperationType != RoutingOperationTypeAuditExport || existing.Status != RoutingOperationStatusSucceeded {
				return ErrRoutingOperationInvalid
			}
			if err := tx.WithContext(ctx).Where("operation_id = ?", existing.ID).First(&export).Error; err != nil {
				return err
			}
			if err := validateStoredRoutingAuditExport(export); err != nil {
				return err
			}
			if _, err := loadRoutingAuditExportPayloadDBContext(ctx, tx, export); err != nil {
				return err
			}
			operation = existing
			return nil
		}

		items := make([]RoutingAuditExportItem, 0, min(request.Limit, 256))
		if err := tx.WithContext(ctx).Model(&RoutingDecisionAudit{}).
			Select([]string{
				"id AS audit_id", "decision_id", "request_id", "pool_id", "group_name", "model_name", "snapshot_revision",
				"runtime_generation", "activation_id", "activation_stage", "traffic_basis_points", "cohort",
				"algorithm_version", "retry_index", "is_stream", "actual_channel_id", "actual_channel_generation",
				"observed_channel_id", "observed_channel_generation",
				"candidate_count", "eligible_count", "filtered_open", "filtered_capacity", "breaker_bypassed",
				"observed_matches_actual", "difference_type", "actual_cost_known", "actual_expected_cost",
				"observed_cost_known", "observed_expected_cost", "expected_cost_delta", "replayable", "created_time",
			}).
			Where("created_time >= ? AND created_time <= ?", request.FromTime, request.ToTime).
			Order("created_time asc").Order("id asc").Limit(request.Limit).
			Scan(&items).Error; err != nil {
			return err
		}
		timelineReferences := make([]routingAttemptTimelineReference, len(items))
		for index := range items {
			requestKey := ""
			if items[index].RequestID != "" {
				requestKey = routingHedgeAuditHash("request", items[index].RequestID)
			}
			timelineReferences[index] = routingAttemptTimelineReference{
				DecisionID: items[index].DecisionID,
				RequestKey: requestKey,
			}
			items[index].RequestID = ""
		}
		timelineByDecision, err := getRoutingAttemptTimelinesDBContext(ctx, tx, timelineReferences)
		if err != nil {
			return err
		}
		for index := range items {
			if timeline, exists := timelineByDecision[items[index].DecisionID]; exists && timeline.AttemptCount > 0 {
				primary := timeline
				compatibilityAlias := timeline
				items[index].AttemptTimeline = &primary
				items[index].Hedge = &compatibilityAlias
			}
		}
		content, err := common.Marshal(items)
		if err != nil {
			return err
		}
		if len(content) > RoutingAuditExportMaxBytes {
			return ErrRoutingAuditExportTooLarge
		}
		digest := sha256.Sum256(content)
		exportID := "rae_" + identity.KeyHash[:32]
		chunks, err := newRoutingAuditExportChunks(exportID, content)
		if err != nil {
			return err
		}
		nowMs := time.Now().UnixMilli()
		result := RoutingAuditExportResult{
			ExportID: exportID, RecordCount: len(items), ContentBytes: len(content), ContentHash: hex.EncodeToString(digest[:]),
			CreatedTimeMs: nowMs, ExpiresTimeMs: nowMs + routingAuditExportRetention.Milliseconds(),
		}
		operation, created, err = createSucceededRoutingOperationTx(
			ctx, tx,
			RoutingOperationSpec{
				Type: RoutingOperationTypeAuditExport, EvaluationHash: identity.PayloadHash,
				SubjectType:      RoutingOperationSubjectDecisionAudit,
				ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
				ActorID: actorID, Reason: "routing decision audit export",
				RequestKeyHash: identity.KeyHash, RequestPayloadHash: identity.PayloadHash,
			},
			RoutingOperationResult{}, result, nowMs,
		)
		if err != nil {
			return err
		}
		export = RoutingAuditExport{
			ExportID: exportID, OperationID: operation.ID, ActorID: actorID,
			FromTime: request.FromTime, ToTime: request.ToTime, RecordCount: len(items),
			ContentBytes: len(content), ContentHash: result.ContentHash, ChunkCount: len(chunks),
			CreatedTimeMs: nowMs, ExpiresTimeMs: result.ExpiresTimeMs,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "export_id"}},
			DoNothing: true,
		}).Create(&export).Error; err != nil {
			return err
		}
		var stored RoutingAuditExport
		if err := tx.WithContext(ctx).Where("export_id = ?", exportID).First(&stored).Error; err != nil {
			return err
		}
		if stored.OperationID != operation.ID || stored.ActorID != actorID || stored.FromTime != request.FromTime ||
			stored.ToTime != request.ToTime || stored.ContentHash != result.ContentHash || stored.ChunkCount != len(chunks) {
			return ErrRoutingAuditExportInvalid
		}
		export = stored
		if err := validateStoredRoutingAuditExport(export); err != nil {
			return err
		}
		for start := 0; start < len(chunks); start += 10 {
			end := min(start+10, len(chunks))
			batch := chunks[start:end]
			if err := tx.WithContext(ctx).Create(&batch).Error; err != nil {
				return err
			}
		}
		loaded, err := loadRoutingAuditExportPayloadDBContext(ctx, tx, export)
		if err != nil {
			return err
		}
		if string(loaded) != string(content) {
			return ErrRoutingAuditExportInvalid
		}
		return nil
	})
	return export, operation, created, err
}

func GetRoutingAuditExportContext(ctx context.Context, exportID string) (RoutingAuditExport, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(exportID) != 36 || exportID[:4] != "rae_" {
		return RoutingAuditExport{}, nil, ErrRoutingAuditExportInvalid
	}
	var export RoutingAuditExport
	if err := DB.WithContext(ctx).Where("export_id = ?", exportID).First(&export).Error; err != nil {
		return RoutingAuditExport{}, nil, err
	}
	if err := validateStoredRoutingAuditExport(export); err != nil {
		return RoutingAuditExport{}, nil, err
	}
	payload, err := loadRoutingAuditExportPayloadDBContext(ctx, DB, export)
	if err != nil {
		return RoutingAuditExport{}, nil, err
	}
	return export, payload, nil
}

func validateStoredRoutingAuditExport(export RoutingAuditExport) error {
	if len(export.ExportID) != 36 || export.ExportID[:4] != "rae_" || export.OperationID <= 0 || export.ActorID < 0 ||
		export.FromTime <= 0 || export.ToTime < export.FromTime ||
		export.ToTime-export.FromTime > RoutingAuditExportMaxRangeSeconds ||
		export.RecordCount < 0 || export.RecordCount > RoutingAuditExportMaxRecords ||
		export.ContentBytes < 2 || export.ContentBytes > RoutingAuditExportMaxBytes || len(export.ContentHash) != sha256.Size*2 ||
		export.ChunkCount < 1 || export.ChunkCount > (RoutingAuditExportMaxBytes/routingAuditExportChunkMaxBytes)+1 ||
		export.CreatedTimeMs <= 0 || export.ExpiresTimeMs <= export.CreatedTimeMs {
		return ErrRoutingAuditExportInvalid
	}
	return nil
}

func newRoutingAuditExportChunks(exportID string, payload []byte) ([]RoutingAuditExportChunk, error) {
	if len(exportID) != 36 || len(payload) < 2 || len(payload) > RoutingAuditExportMaxBytes || !utf8.Valid(payload) {
		return nil, ErrRoutingAuditExportInvalid
	}
	parts := make([]string, 0, (len(payload)+routingAuditExportChunkMaxBytes-1)/routingAuditExportChunkMaxBytes)
	for start := 0; start < len(payload); {
		end := min(start+routingAuditExportChunkMaxBytes, len(payload))
		for end < len(payload) && !utf8.RuneStart(payload[end]) {
			end--
		}
		if end <= start {
			return nil, ErrRoutingAuditExportInvalid
		}
		parts = append(parts, string(payload[start:end]))
		start = end
	}
	chunks := make([]RoutingAuditExportChunk, len(parts))
	for index := range parts {
		digest := sha256.Sum256([]byte(parts[index]))
		chunks[index] = RoutingAuditExportChunk{
			ExportID: exportID, ChunkIndex: index, ChunkCount: len(parts), PayloadBytes: len(parts[index]),
			PayloadHash: hex.EncodeToString(digest[:]), Payload: parts[index],
		}
	}
	return chunks, nil
}

func loadRoutingAuditExportPayloadDBContext(
	ctx context.Context,
	db *gorm.DB,
	export RoutingAuditExport,
) ([]byte, error) {
	if db == nil {
		return nil, ErrRoutingAuditExportInvalid
	}
	var chunks []RoutingAuditExportChunk
	if err := db.WithContext(ctx).Where("export_id = ?", export.ExportID).
		Order("chunk_index asc").Limit(export.ChunkCount + 1).Find(&chunks).Error; err != nil {
		return nil, err
	}
	if len(chunks) != export.ChunkCount {
		return nil, ErrRoutingAuditExportInvalid
	}
	payload := make([]byte, 0, export.ContentBytes)
	for index := range chunks {
		chunk := chunks[index]
		if chunk.ExportID != export.ExportID || chunk.ChunkIndex != index || chunk.ChunkCount != export.ChunkCount ||
			chunk.PayloadBytes < 1 || chunk.PayloadBytes > routingAuditExportChunkMaxBytes ||
			chunk.PayloadBytes != len(chunk.Payload) || len(chunk.PayloadHash) != sha256.Size*2 {
			return nil, ErrRoutingAuditExportInvalid
		}
		digest := sha256.Sum256([]byte(chunk.Payload))
		if hex.EncodeToString(digest[:]) != chunk.PayloadHash {
			return nil, ErrRoutingAuditExportInvalid
		}
		payload = append(payload, chunk.Payload...)
	}
	if len(payload) != export.ContentBytes || !utf8.Valid(payload) {
		return nil, ErrRoutingAuditExportInvalid
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != export.ContentHash {
		return nil, ErrRoutingAuditExportInvalid
	}
	var items []RoutingAuditExportItem
	if common.Unmarshal(payload, &items) != nil || len(items) != export.RecordCount {
		return nil, ErrRoutingAuditExportInvalid
	}
	return payload, nil
}

func IsRoutingAuditExportNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func DeleteExpiredRoutingAuditExportsContext(ctx context.Context, cutoffMs int64, limit int) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 || limit < 1 || limit > 500 {
		return 0, ErrRoutingAuditExportInvalid
	}
	var deleted int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exports []RoutingAuditExport
		if err := tx.WithContext(ctx).Select("export_id").Where("expires_time_ms <= ?", cutoffMs).
			Order("expires_time_ms asc").Limit(limit).Find(&exports).Error; err != nil {
			return err
		}
		if len(exports) == 0 {
			return nil
		}
		exportIDs := make([]string, len(exports))
		for index := range exports {
			exportIDs[index] = exports[index].ExportID
		}
		chunks := tx.WithContext(ctx).Where("export_id IN ?", exportIDs).Delete(&RoutingAuditExportChunk{})
		if chunks.Error != nil {
			return chunks.Error
		}
		rows := tx.WithContext(ctx).Where("export_id IN ? AND expires_time_ms <= ?", exportIDs, cutoffMs).
			Delete(&RoutingAuditExport{})
		if rows.Error != nil {
			return rows.Error
		}
		deleted = chunks.RowsAffected + rows.RowsAffected
		return nil
	})
	return deleted, err
}
