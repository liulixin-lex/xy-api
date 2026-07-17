package model

import (
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	routingAgentRetirementComponent = "channel-routing-agent-retirement"
	routingAgentRetirementDataPhase = "data-scrubbed"
	routingAgentRetirementComplete  = "complete"
	routingAgentTaskType            = "routing_agent"
	routingAgentRecommendationTable = "routing_agent_recommendations"
)

var retiredRoutingAgentOptionKeys = []string{
	"smart_routing_setting.agent_enabled",
	"smart_routing_setting.agent_auto_apply",
	"smart_routing_setting.agent_model",
}

func RetireRoutingAgentPlaceholderSchema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingSchemaVersion{}) {
		return ErrRoutingSchemaNotReady
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&Option{}) {
			if err := tx.Where("key IN ?", retiredRoutingAgentOptionKeys).Delete(&Option{}).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&SystemTask{}) {
			if err := tx.Where("type = ?", routingAgentTaskType).Delete(&SystemTask{}).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&SystemTaskLock{}) {
			if err := tx.Where("type = ?", routingAgentTaskType).Delete(&SystemTaskLock{}).Error; err != nil {
				return err
			}
		}
		if err := retireRoutingAgentRuntimeSettingsTx(tx); err != nil {
			return err
		}
		return markRoutingAgentRetirementPhaseTx(tx, routingAgentRetirementDataPhase)
	}); err != nil {
		return err
	}
	if db.Migrator().HasTable(routingAgentRecommendationTable) {
		if err := db.Migrator().DropTable(routingAgentRecommendationTable); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(routingAgentRecommendationTable) {
		return ErrRoutingSchemaNotReady
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return markRoutingAgentRetirementPhaseTx(tx, routingAgentRetirementComplete)
	})
}

func retireRoutingAgentRuntimeSettingsTx(tx *gorm.DB) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingRuntimeSettingsState{}) {
		return nil
	}
	var current RoutingRuntimeSettingsState
	err := tx.Where("id = ?", routingRuntimeSettingsStateID).First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var document map[string]any
	if common.UnmarshalJsonStr(current.DocumentJSON, &document) != nil || document == nil {
		return ErrRoutingRuntimeSettingsInvalid
	}
	changedKeys := make([]string, 0, 3)
	for _, key := range []string{"agent_enabled", "agent_auto_apply", "agent_model"} {
		if _, exists := document[key]; exists {
			delete(document, key)
			changedKeys = append(changedKeys, key)
		}
	}
	if len(changedKeys) == 0 {
		return nil
	}
	if current.Revision <= 0 || current.Revision == math.MaxInt64 {
		return ErrRoutingRuntimeSettingsInvalid
	}
	encoded, err := common.Marshal(document)
	if err != nil {
		return err
	}
	nextRevision := current.Revision + 1
	nextHash := RoutingRuntimeSettingsDocumentHash(encoded)
	nowMs := time.Now().UnixMilli()
	result := tx.Model(&RoutingRuntimeSettingsState{}).
		Where("id = ? AND revision = ? AND document_hash = ?", current.ID, current.Revision, current.DocumentHash).
		Updates(map[string]any{
			"revision": nextRevision, "document_hash": nextHash, "document_json": string(encoded),
			"updated_by": 0, "updated_time_ms": nowMs,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingRuntimeSettingsConflict
	}
	if !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	auditDocuments, _, err := routingRuntimeSettingsAuditDocuments(current.DocumentJSON, string(encoded), nextRevision)
	if err != nil {
		return err
	}
	summaryJSON, err := common.Marshal(struct {
		Source      string   `json:"source"`
		ChangedKeys []string `json:"changed_keys"`
	}{Source: "routing_agent_retirement_migration", ChangedKeys: changedKeys})
	if err != nil {
		return err
	}
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		EventType:   "runtime_settings.routing_agent_retired",
		SubjectType: RoutingControlSubjectRuntimeSettings, SubjectIdentity: "runtime-settings",
		Action: RoutingControlActionReconcile, Source: RoutingControlAuditSourceMigration,
		Result: RoutingControlAuditResultSucceeded, BeforeHash: current.DocumentHash, AfterHash: nextHash,
		SummaryJSON: string(summaryJSON), SubjectSnapshotJSON: auditDocuments.SubjectSnapshot,
		ChangeSetJSON: auditDocuments.ChangeSet, ImpactJSON: auditDocuments.Impact,
		TechnicalJSON: auditDocuments.Technical, CreatedTimeMs: nowMs,
	})
}

func markRoutingAgentRetirementPhaseTx(tx *gorm.DB, phase string) error {
	if tx == nil || (phase != routingAgentRetirementDataPhase && phase != routingAgentRetirementComplete) {
		return ErrRoutingSchemaNotReady
	}
	nowMs, err := routingDatabaseNowMs(tx)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "component"}},
		DoUpdates: clause.Assignments(map[string]any{
			"version": phase, "updated_time_ms": nowMs,
		}),
	}).Create(&RoutingSchemaVersion{
		Component: routingAgentRetirementComponent, Version: phase, UpdatedTimeMs: nowMs,
	}).Error
}
