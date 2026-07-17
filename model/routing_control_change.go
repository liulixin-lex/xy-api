package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingControlSubjectRuntimeSettings      = "runtime_settings"
	RoutingControlSubjectCostBinding          = "cost_binding"
	RoutingControlSubjectChannelConfiguration = "channel_configuration"
	RoutingControlSubjectChannelLifecycle     = "channel_lifecycle"
	RoutingControlSubjectPolicyDraft          = "policy_draft"
	RoutingControlSubjectPolicyRevision       = "policy_revision"
	RoutingControlSubjectPolicyActivation     = "policy_activation"
	RoutingControlSubjectPolicyRiskAcceptance = "policy_risk_acceptance"
	RoutingControlSubjectOperation            = "operation"
	RoutingControlSubjectPricing              = "pricing"

	RoutingControlActionBootstrap  = "bootstrap"
	RoutingControlActionReconcile  = "reconcile"
	RoutingControlActionCreate     = "create"
	RoutingControlActionUpdate     = "update"
	RoutingControlActionDelete     = "delete"
	RoutingControlActionValidate   = "validate"
	RoutingControlActionPublish    = "publish"
	RoutingControlActionRollback   = "rollback"
	RoutingControlActionRiskAccept = "risk_accept"
	RoutingControlActionRotate     = "rotate"
	RoutingControlActionRetire     = "retire"
	RoutingControlActionRetry      = "retry"
	RoutingControlActionCancel     = "cancel"

	RoutingControlAuditSourceSystem    = "system"
	RoutingControlAuditSourceAdmin     = "admin"
	RoutingControlAuditSourceMigration = "migration"
	RoutingControlAuditSourceReconcile = "reconciler"

	RoutingControlAuditResultSucceeded = "succeeded"
	RoutingControlAuditResultPartial   = "partially_succeeded"
	RoutingControlAuditResultFailed    = "failed"
	RoutingControlAuditResultRejected  = "rejected"

	routingRuntimeSettingsStateID    = 1
	routingControlSummaryMaxBytes    = 16 << 10
	RoutingControlAuditMaxPageSize   = 100
	RoutingControlAuditSchemaVersion = 2
)

var (
	ErrRoutingRuntimeSettingsConflict = errors.New("channel routing runtime settings changed")
	ErrRoutingRuntimeSettingsInvalid  = errors.New("invalid channel routing runtime settings state")
	ErrRoutingControlAuditInvalid     = errors.New("invalid channel routing control audit")
	ErrRoutingControlAuditImmutable   = errors.New("channel routing control audit is immutable")
)

type RoutingRuntimeSettingsState struct {
	ID            int    `json:"-" gorm:"primaryKey"`
	Revision      int64  `json:"revision" gorm:"bigint;not null"`
	DocumentHash  string `json:"document_hash" gorm:"type:char(64);index;not null"`
	DocumentJSON  string `json:"-" gorm:"type:text;not null"`
	UpdatedBy     int    `json:"updated_by" gorm:"index;not null"`
	UpdatedTimeMs int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingRuntimeSettingsState) TableName() string {
	return "routing_runtime_settings_state"
}

type RoutingControlAudit struct {
	ID                    int64  `json:"id" gorm:"primaryKey"`
	SchemaVersion         int    `json:"schema_version" gorm:"index"`
	EventType             string `json:"event_type" gorm:"type:varchar(96);index"`
	SubjectType           string `json:"subject_type" gorm:"type:varchar(32);index;not null"`
	SubjectID             int64  `json:"subject_id" gorm:"bigint;index;not null"`
	SubjectIdentity       string `json:"subject_identity" gorm:"type:varchar(128);index"`
	SubjectGeneration     string `json:"subject_generation,omitempty" gorm:"type:varchar(32);index"`
	SubjectName           string `json:"subject_name" gorm:"type:varchar(256)"`
	Action                string `json:"action" gorm:"type:varchar(32);index;not null"`
	Source                string `json:"source" gorm:"type:varchar(32);index"`
	Reason                string `json:"reason,omitempty" gorm:"type:varchar(512)"`
	Result                string `json:"result" gorm:"type:varchar(32);index"`
	ActorID               int    `json:"actor_id" gorm:"index;not null"`
	ActorName             string `json:"actor_name" gorm:"type:varchar(128)"`
	ActorRole             int    `json:"actor_role"`
	BeforeHash            string `json:"before_hash,omitempty" gorm:"type:char(64);index"`
	AfterHash             string `json:"after_hash,omitempty" gorm:"type:char(64);index"`
	SummaryJSON           string `json:"-" gorm:"type:text;not null"`
	SubjectSnapshotJSON   string `json:"-" gorm:"type:text"`
	ChangeSetJSON         string `json:"-" gorm:"type:text"`
	ImpactJSON            string `json:"-" gorm:"type:text"`
	RecommendationJSON    string `json:"-" gorm:"type:text"`
	RelationJSON          string `json:"-" gorm:"type:text"`
	TechnicalJSON         string `json:"-" gorm:"type:text"`
	ErrorCode             string `json:"error_code,omitempty" gorm:"type:varchar(64);index"`
	ErrorMessage          string `json:"error_message,omitempty" gorm:"type:text"`
	NeedsAttention        bool   `json:"needs_attention" gorm:"index"`
	CorrelationID         string `json:"correlation_id,omitempty" gorm:"type:varchar(64);index"`
	OperationID           int64  `json:"operation_id,omitempty" gorm:"bigint;index"`
	DraftID               int64  `json:"draft_id,omitempty" gorm:"bigint;index"`
	SimulationOperationID int64  `json:"simulation_operation_id,omitempty" gorm:"bigint;index"`
	PolicyRevision        int64  `json:"policy_revision,omitempty" gorm:"bigint;index"`
	ActivationID          int64  `json:"activation_id,omitempty" gorm:"bigint;index"`
	RollbackOfRevision    int64  `json:"rollback_of_revision,omitempty" gorm:"bigint;index"`
	TopologyEpoch         int64  `json:"topology_epoch,omitempty" gorm:"bigint;index"`
	PricingEpoch          int64  `json:"pricing_epoch,omitempty" gorm:"bigint;index"`
	CreatedTimeMs         int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingControlAudit) TableName() string {
	return "routing_control_audits"
}

type RoutingControlAuditFilter struct {
	BeforeID       int64
	SubjectType    string
	SubjectID      int64
	ActorID        int
	Source         string
	Result         string
	CorrelationID  string
	NeedsAttention *bool
	Limit          int
}

func RoutingRuntimeSettingsDocumentHash(document []byte) string {
	digest := sha256.Sum256(document)
	return fmt.Sprintf("%x", digest)
}

func GetRoutingRuntimeSettingsStateContext(ctx context.Context) (RoutingRuntimeSettingsState, error) {
	return GetRoutingRuntimeSettingsStateDBContext(ctx, DB)
}

func GetRoutingRuntimeSettingsStateDBContext(
	ctx context.Context,
	db *gorm.DB,
) (RoutingRuntimeSettingsState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Dialector == nil {
		return RoutingRuntimeSettingsState{}, ErrRoutingRuntimeSettingsInvalid
	}
	var state RoutingRuntimeSettingsState
	err := db.WithContext(ctx).Where("id = ?", routingRuntimeSettingsStateID).First(&state).Error
	return state, err
}

func GetOrReconcileRoutingRuntimeSettingsStateContext(
	ctx context.Context,
	documentJSON string,
	documentHash string,
) (RoutingRuntimeSettingsState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingRuntimeSettingsDocument(documentJSON, documentHash) {
		return RoutingRuntimeSettingsState{}, ErrRoutingRuntimeSettingsInvalid
	}
	var stored RoutingRuntimeSettingsState
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := loadRoutingRuntimeSettingsStateForUpdate(tx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			nowMs := time.Now().UnixMilli()
			candidate := RoutingRuntimeSettingsState{
				ID: routingRuntimeSettingsStateID, Revision: 1,
				DocumentHash: documentHash, DocumentJSON: documentJSON,
				UpdatedTimeMs: nowMs,
			}
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if created.Error != nil {
				return created.Error
			}
			current, err = loadRoutingRuntimeSettingsStateForUpdate(tx)
			if err != nil {
				return err
			}
			if created.RowsAffected == 1 {
				auditDocuments, _, auditErr := routingRuntimeSettingsAuditDocuments("", documentJSON, candidate.Revision)
				if auditErr != nil {
					return auditErr
				}
				if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
					SubjectType: RoutingControlSubjectRuntimeSettings, SubjectIdentity: "runtime-settings",
					Action: RoutingControlActionBootstrap, AfterHash: documentHash,
					SummaryJSON: `{"source":"existing_options"}`, SubjectSnapshotJSON: auditDocuments.SubjectSnapshot,
					ChangeSetJSON: auditDocuments.ChangeSet, ImpactJSON: auditDocuments.Impact,
					TechnicalJSON: auditDocuments.Technical, CreatedTimeMs: nowMs,
				}); err != nil {
					return err
				}
			}
		} else if err != nil {
			return err
		}
		if current.DocumentHash != documentHash || current.DocumentJSON != documentJSON {
			nowMs := time.Now().UnixMilli()
			nextRevision, err := nextRoutingControlRevision(current.Revision)
			if err != nil {
				return err
			}
			result := tx.Model(&RoutingRuntimeSettingsState{}).
				Where("id = ? AND revision = ? AND document_hash = ?", current.ID, current.Revision, current.DocumentHash).
				Updates(map[string]any{
					"revision": nextRevision, "document_hash": documentHash, "document_json": documentJSON,
					"updated_by": 0, "updated_time_ms": nowMs,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRoutingRuntimeSettingsConflict
			}
			auditDocuments, _, auditErr := routingRuntimeSettingsAuditDocuments(
				current.DocumentJSON, documentJSON, nextRevision,
			)
			if auditErr != nil {
				return auditErr
			}
			if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
				SubjectType: RoutingControlSubjectRuntimeSettings, SubjectIdentity: "runtime-settings",
				Action: RoutingControlActionReconcile, BeforeHash: current.DocumentHash, AfterHash: documentHash,
				SummaryJSON: `{"source":"external_option_update"}`, SubjectSnapshotJSON: auditDocuments.SubjectSnapshot,
				ChangeSetJSON: auditDocuments.ChangeSet, ImpactJSON: auditDocuments.Impact,
				TechnicalJSON: auditDocuments.Technical, CreatedTimeMs: nowMs,
			}); err != nil {
				return err
			}
			current.Revision = nextRevision
			current.DocumentHash = documentHash
			current.DocumentJSON = documentJSON
			current.UpdatedBy = 0
			current.UpdatedTimeMs = nowMs
		}
		stored = current
		return nil
	})
	return stored, err
}

func UpdateRoutingRuntimeSettingsContext(
	ctx context.Context,
	expectedRevision int64,
	expectedHash string,
	documentJSON string,
	documentHash string,
	values map[string]string,
	actorID int,
) (RoutingRuntimeSettingsState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expectedRevision <= 0 || !validRoutingHash(expectedHash) || actorID <= 0 || len(values) == 0 ||
		!validRoutingRuntimeSettingsDocument(documentJSON, documentHash) {
		return RoutingRuntimeSettingsState{}, ErrRoutingRuntimeSettingsInvalid
	}
	var stored RoutingRuntimeSettingsState
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := loadRoutingRuntimeSettingsStateForUpdate(tx)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.DocumentHash != expectedHash {
			return ErrRoutingRuntimeSettingsConflict
		}
		nextRevision, err := nextRoutingControlRevision(current.Revision)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			option := Option{Key: key, Value: values[key]}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&option).Error; err != nil {
				return err
			}
		}
		nowMs := time.Now().UnixMilli()
		result := tx.Model(&RoutingRuntimeSettingsState{}).
			Where("id = ? AND revision = ? AND document_hash = ?", current.ID, current.Revision, current.DocumentHash).
			Updates(map[string]any{
				"revision": nextRevision, "document_hash": documentHash, "document_json": documentJSON,
				"updated_by": actorID, "updated_time_ms": nowMs,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingRuntimeSettingsConflict
		}
		auditDocuments, changedKeys, err := routingRuntimeSettingsAuditDocuments(
			current.DocumentJSON, documentJSON, nextRevision,
		)
		if err != nil {
			return err
		}
		summary, err := common.Marshal(struct {
			ChangedKeys []string `json:"changed_keys"`
		}{ChangedKeys: changedKeys})
		if err != nil {
			return err
		}
		if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
			SubjectType: RoutingControlSubjectRuntimeSettings, SubjectIdentity: "runtime-settings",
			Action: RoutingControlActionUpdate, ActorID: actorID,
			BeforeHash: current.DocumentHash, AfterHash: documentHash,
			SummaryJSON: string(summary), SubjectSnapshotJSON: auditDocuments.SubjectSnapshot,
			ChangeSetJSON: auditDocuments.ChangeSet, ImpactJSON: auditDocuments.Impact,
			TechnicalJSON: auditDocuments.Technical, CreatedTimeMs: nowMs,
		}); err != nil {
			return err
		}
		stored = RoutingRuntimeSettingsState{
			ID: current.ID, Revision: nextRevision,
			DocumentHash: documentHash, DocumentJSON: documentJSON,
			UpdatedBy: actorID, UpdatedTimeMs: nowMs,
		}
		return nil
	})
	return stored, err
}

func ListRoutingControlAuditsContext(ctx context.Context, filter RoutingControlAuditFilter) ([]RoutingControlAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if filter.Limit < 1 || filter.Limit > RoutingControlAuditMaxPageSize || filter.BeforeID < 0 ||
		filter.SubjectID < 0 || filter.ActorID < 0 ||
		(filter.SubjectType != "" && !validRoutingControlAuditSubjectType(filter.SubjectType)) ||
		(filter.Source != "" && !validRoutingControlAuditSource(filter.Source)) ||
		(filter.Result != "" && !validRoutingControlAuditResult(filter.Result)) ||
		(filter.CorrelationID != "" && !validRoutingOperationCorrelationID(filter.CorrelationID)) {
		return nil, ErrRoutingControlAuditInvalid
	}
	query := DB.WithContext(ctx).Model(&RoutingControlAudit{})
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.SubjectType != "" {
		query = query.Where("subject_type = ?", filter.SubjectType)
	}
	if filter.SubjectID > 0 {
		query = query.Where("subject_id = ?", filter.SubjectID)
	}
	if filter.ActorID > 0 {
		query = query.Where("actor_id = ?", filter.ActorID)
	}
	if filter.Source != "" {
		query = query.Where("source = ?", filter.Source)
	}
	if filter.Result != "" {
		query = query.Where("result = ?", filter.Result)
	}
	if filter.CorrelationID != "" {
		query = query.Where("correlation_id = ?", filter.CorrelationID)
	}
	if filter.NeedsAttention != nil {
		query = query.Where("needs_attention = ?", *filter.NeedsAttention)
	}
	var audits []RoutingControlAudit
	return audits, query.Order("id desc").Limit(filter.Limit).Find(&audits).Error
}

func GetRoutingControlAuditContext(ctx context.Context, id int64) (RoutingControlAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 {
		return RoutingControlAudit{}, ErrRoutingControlAuditInvalid
	}
	var audit RoutingControlAudit
	err := DB.WithContext(ctx).Where("id = ?", id).First(&audit).Error
	return audit, err
}

func IsRoutingControlAuditNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func loadRoutingRuntimeSettingsStateForUpdate(tx *gorm.DB) (RoutingRuntimeSettingsState, error) {
	query := tx
	if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var state RoutingRuntimeSettingsState
	err := query.Where("id = ?", routingRuntimeSettingsStateID).First(&state).Error
	return state, err
}

func insertRoutingControlAuditTx(tx *gorm.DB, audit RoutingControlAudit) error {
	if tx == nil || tx.Dialector == nil {
		return ErrRoutingControlAuditInvalid
	}
	return tx.Create(&audit).Error
}

func validRoutingRuntimeSettingsDocument(documentJSON string, documentHash string) bool {
	return documentJSON != "" && len(documentJSON) <= routingControlSummaryMaxBytes && utf8.ValidString(documentJSON) &&
		validRoutingHash(documentHash) && RoutingRuntimeSettingsDocumentHash([]byte(documentJSON)) == documentHash
}

func nextRoutingControlRevision(current int64) (int64, error) {
	if current <= 0 || current == int64(^uint64(0)>>1) {
		return 0, ErrRoutingRuntimeSettingsInvalid
	}
	return current + 1, nil
}
