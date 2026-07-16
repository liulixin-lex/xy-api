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

	RoutingControlActionBootstrap = "bootstrap"
	RoutingControlActionReconcile = "reconcile"
	RoutingControlActionCreate    = "create"
	RoutingControlActionUpdate    = "update"
	RoutingControlActionDelete    = "delete"

	routingRuntimeSettingsStateID  = 1
	routingControlSummaryMaxBytes  = 16 << 10
	RoutingControlAuditMaxPageSize = 100
)

var (
	ErrRoutingRuntimeSettingsConflict = errors.New("channel routing runtime settings changed")
	ErrRoutingRuntimeSettingsInvalid  = errors.New("invalid channel routing runtime settings state")
	ErrRoutingControlAuditInvalid     = errors.New("invalid channel routing control audit")
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
	ID            int64  `json:"id" gorm:"primaryKey"`
	SubjectType   string `json:"subject_type" gorm:"type:varchar(32);index;not null"`
	SubjectID     int64  `json:"subject_id" gorm:"bigint;index;not null"`
	Action        string `json:"action" gorm:"type:varchar(32);index;not null"`
	ActorID       int    `json:"actor_id" gorm:"index;not null"`
	BeforeHash    string `json:"before_hash,omitempty" gorm:"type:char(64);index"`
	AfterHash     string `json:"after_hash,omitempty" gorm:"type:char(64);index"`
	SummaryJSON   string `json:"-" gorm:"type:text;not null"`
	CreatedTimeMs int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingControlAudit) TableName() string {
	return "routing_control_audits"
}

type RoutingControlAuditFilter struct {
	BeforeID    int64
	SubjectType string
	SubjectID   int64
	ActorID     int
	Limit       int
}

func RoutingRuntimeSettingsDocumentHash(document []byte) string {
	digest := sha256.Sum256(document)
	return fmt.Sprintf("%x", digest)
}

func GetRoutingRuntimeSettingsStateContext(ctx context.Context) (RoutingRuntimeSettingsState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var state RoutingRuntimeSettingsState
	err := DB.WithContext(ctx).Where("id = ?", routingRuntimeSettingsStateID).First(&state).Error
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
				if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
					SubjectType: RoutingControlSubjectRuntimeSettings,
					Action:      RoutingControlActionBootstrap, AfterHash: documentHash,
					SummaryJSON: `{"source":"existing_options"}`, CreatedTimeMs: nowMs,
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
			if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
				SubjectType: RoutingControlSubjectRuntimeSettings,
				Action:      RoutingControlActionReconcile,
				BeforeHash:  current.DocumentHash, AfterHash: documentHash,
				SummaryJSON: `{"source":"external_option_update"}`, CreatedTimeMs: nowMs,
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
		summary, err := common.Marshal(struct {
			ChangedKeys []string `json:"changed_keys"`
		}{ChangedKeys: keys})
		if err != nil {
			return err
		}
		if err := insertRoutingControlAuditTx(tx, RoutingControlAudit{
			SubjectType: RoutingControlSubjectRuntimeSettings,
			Action:      RoutingControlActionUpdate, ActorID: actorID,
			BeforeHash: current.DocumentHash, AfterHash: documentHash,
			SummaryJSON: string(summary), CreatedTimeMs: nowMs,
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
		filter.SubjectID < 0 || filter.ActorID < 0 {
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
	var audits []RoutingControlAudit
	return audits, query.Order("id desc").Limit(filter.Limit).Find(&audits).Error
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
	if tx == nil || !validRoutingControlAudit(audit) {
		return ErrRoutingControlAuditInvalid
	}
	return tx.Create(&audit).Error
}

func validRoutingRuntimeSettingsDocument(documentJSON string, documentHash string) bool {
	return documentJSON != "" && len(documentJSON) <= routingControlSummaryMaxBytes && utf8.ValidString(documentJSON) &&
		validRoutingHash(documentHash) && RoutingRuntimeSettingsDocumentHash([]byte(documentJSON)) == documentHash
}

func validRoutingControlAudit(audit RoutingControlAudit) bool {
	if audit.SubjectType != RoutingControlSubjectRuntimeSettings &&
		audit.SubjectType != RoutingControlSubjectCostBinding &&
		audit.SubjectType != RoutingControlSubjectChannelConfiguration {
		return false
	}
	switch audit.Action {
	case RoutingControlActionBootstrap, RoutingControlActionReconcile, RoutingControlActionCreate,
		RoutingControlActionUpdate, RoutingControlActionDelete:
	default:
		return false
	}
	return audit.SubjectID >= 0 && audit.ActorID >= 0 && audit.CreatedTimeMs > 0 &&
		(audit.BeforeHash == "" || validRoutingHash(audit.BeforeHash)) &&
		(audit.AfterHash == "" || validRoutingHash(audit.AfterHash)) &&
		audit.BeforeHash != audit.AfterHash && audit.SummaryJSON != "" &&
		len(audit.SummaryJSON) <= routingControlSummaryMaxBytes && utf8.ValidString(audit.SummaryJSON)
}

func nextRoutingControlRevision(current int64) (int64, error) {
	if current <= 0 || current == int64(^uint64(0)>>1) {
		return 0, ErrRoutingRuntimeSettingsInvalid
	}
	return current + 1, nil
}
