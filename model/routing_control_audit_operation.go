package model

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

func insertRoutingOperationTransitionAuditTx(
	tx *gorm.DB,
	before RoutingOperation,
	after RoutingOperation,
	action string,
) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingControlAudit{}) {
		return nil
	}
	beforeHash, err := routingOperationControlHash(before)
	if err != nil {
		return err
	}
	afterHash, err := routingOperationControlHash(after)
	if err != nil {
		return err
	}
	summaryJSON, err := common.Marshal(struct {
		OperationType string                 `json:"operation_type"`
		Status        RoutingOperationStatus `json:"status"`
		Summary       string                 `json:"summary"`
		Attempts      int                    `json:"attempts"`
	}{
		OperationType: after.OperationType, Status: after.Status,
		Summary: after.Summary, Attempts: after.Attempts,
	})
	if err != nil {
		return err
	}
	snapshotJSON, err := common.Marshal(struct {
		OperationID       int64                  `json:"operation_id"`
		OperationType     string                 `json:"operation_type"`
		Status            RoutingOperationStatus `json:"status"`
		Source            string                 `json:"source"`
		CorrelationID     string                 `json:"correlation_id"`
		RetrySequence     int                    `json:"retry_sequence"`
		RetentionCategory string                 `json:"retention_category"`
	}{
		OperationID: after.ID, OperationType: after.OperationType, Status: after.Status,
		Source: after.Source, CorrelationID: after.CorrelationID, RetrySequence: after.RetrySequence,
		RetentionCategory: after.RetentionCategory,
	})
	if err != nil {
		return err
	}
	changesJSON, err := common.Marshal(struct {
		Items []RoutingControlPolicyChange `json:"items"`
	}{Items: []RoutingControlPolicyChange{
		{Scope: "operation", Change: "updated", Field: "status", Before: before.Status, After: after.Status},
		{Scope: "operation", Change: "updated", Field: "attempts", Before: before.Attempts, After: after.Attempts},
		{Scope: "operation", Change: "updated", Field: "needs_attention", Before: before.NeedsAttention, After: after.NeedsAttention},
		{Scope: "operation", Change: "updated", Field: "retention_category", Before: before.RetentionCategory, After: after.RetentionCategory},
	}})
	if err != nil {
		return err
	}
	impactJSON, err := common.Marshal(struct {
		Terminal       bool `json:"terminal"`
		NeedsAttention bool `json:"needs_attention"`
		Retryable      bool `json:"retryable"`
		Cancellable    bool `json:"cancellable"`
	}{
		Terminal: routingOperationStatusTerminal(after.Status), NeedsAttention: after.NeedsAttention,
		Retryable: after.Retryable, Cancellable: after.Cancellable,
	})
	if err != nil {
		return err
	}
	relationsJSON, err := common.Marshal(struct {
		ParentOperationID  int64 `json:"parent_operation_id,omitempty"`
		RetryOfOperationID int64 `json:"retry_of_operation_id,omitempty"`
		SubjectID          int64 `json:"subject_id,omitempty"`
		PoolID             int   `json:"pool_id,omitempty"`
		Revision           int64 `json:"revision,omitempty"`
		ActivationID       int64 `json:"activation_id,omitempty"`
		OutboxID           int64 `json:"outbox_id,omitempty"`
	}{
		ParentOperationID: after.ParentOperationID, RetryOfOperationID: after.RetryOfOperationID,
		SubjectID: after.SubjectID, PoolID: after.PoolID, Revision: after.ResultRevision,
		ActivationID: after.ResultActivationID, OutboxID: after.ResultOutboxID,
	})
	if err != nil {
		return err
	}
	technicalJSON, err := common.Marshal(struct {
		EvaluationHash    string `json:"evaluation_hash"`
		ResultPayloadHash string `json:"result_payload_hash,omitempty"`
	}{EvaluationHash: after.EvaluationHash, ResultPayloadHash: after.ResultPayloadHash})
	if err != nil {
		return err
	}
	result := routingControlAuditResultForOperation(after.Status)
	actorID := after.TerminalActorID
	if actorID <= 0 {
		actorID = after.ActorID
	}
	createdTimeMs := after.CompletedTimeMs
	if createdTimeMs <= 0 {
		createdTimeMs = after.CreatedTimeMs
	}
	if createdTimeMs <= 0 {
		createdTimeMs = time.Now().UnixMilli()
	}
	return insertRoutingControlAuditTx(tx, RoutingControlAudit{
		EventType:   "operation." + string(after.Status),
		SubjectType: RoutingControlSubjectOperation, SubjectID: after.ID,
		SubjectIdentity: fmt.Sprintf("operation:%d", after.ID), SubjectName: after.Summary,
		Action: action, Source: routingControlAuditSourceForOperation(after.Source),
		Reason: after.Reason, Result: result, ActorID: actorID,
		BeforeHash: beforeHash, AfterHash: afterHash, SummaryJSON: string(summaryJSON),
		SubjectSnapshotJSON: string(snapshotJSON), ChangeSetJSON: string(changesJSON),
		ImpactJSON: string(impactJSON), RelationJSON: string(relationsJSON), TechnicalJSON: string(technicalJSON),
		ErrorMessage: after.LastError, NeedsAttention: after.NeedsAttention,
		CorrelationID: after.CorrelationID, OperationID: after.ID,
		PolicyRevision: after.ResultRevision, ActivationID: after.ResultActivationID,
		CreatedTimeMs: createdTimeMs,
	})
}

func routingOperationInitialAuditState(operation RoutingOperation) RoutingOperation {
	initial := operation
	initial.Status = RoutingOperationStatusPending
	initial.Attempts = 0
	initial.NextRetryMs = 0
	initial.LastError = ""
	initial.ResultRevision = 0
	initial.ResultActivationID = 0
	initial.ResultOutboxID = 0
	initial.ResultPayloadJSON = ""
	initial.ResultPayloadHash = ""
	initial.TerminalActorID = 0
	initial.NeedsAttention = false
	initial.CompletedTimeMs = 0
	initial.UpdatedTimeMs = initial.CreatedTimeMs
	return initial
}

func routingOperationControlHash(operation RoutingOperation) (string, error) {
	payload, err := common.Marshal(struct {
		ID                 int64                  `json:"id"`
		OperationType      string                 `json:"operation_type"`
		SubjectType        string                 `json:"subject_type"`
		SubjectID          int64                  `json:"subject_id"`
		Status             RoutingOperationStatus `json:"status"`
		Attempts           int                    `json:"attempts"`
		NextRetryMs        int64                  `json:"next_retry_ms"`
		NeedsAttention     bool                   `json:"needs_attention"`
		RetentionCategory  string                 `json:"retention_category"`
		RetrySequence      int                    `json:"retry_sequence"`
		ParentOperationID  int64                  `json:"parent_operation_id"`
		RetryOfOperationID int64                  `json:"retry_of_operation_id"`
		ResultRevision     int64                  `json:"result_revision"`
		ResultActivationID int64                  `json:"result_activation_id"`
		ResultOutboxID     int64                  `json:"result_outbox_id"`
		ResultPayloadHash  string                 `json:"result_payload_hash"`
		TerminalActorID    int                    `json:"terminal_actor_id"`
		CompletedTimeMs    int64                  `json:"completed_time_ms"`
	}{
		ID: operation.ID, OperationType: operation.OperationType,
		SubjectType: operation.SubjectType, SubjectID: operation.SubjectID,
		Status: operation.Status, Attempts: operation.Attempts, NextRetryMs: operation.NextRetryMs,
		NeedsAttention: operation.NeedsAttention, RetentionCategory: operation.RetentionCategory,
		RetrySequence: operation.RetrySequence, ParentOperationID: operation.ParentOperationID,
		RetryOfOperationID: operation.RetryOfOperationID, ResultRevision: operation.ResultRevision,
		ResultActivationID: operation.ResultActivationID, ResultOutboxID: operation.ResultOutboxID,
		ResultPayloadHash: operation.ResultPayloadHash, TerminalActorID: operation.TerminalActorID,
		CompletedTimeMs: operation.CompletedTimeMs,
	})
	if err != nil {
		return "", err
	}
	return routingPolicyHash(payload), nil
}

func routingControlAuditSourceForOperation(source string) string {
	switch source {
	case RoutingOperationSourceAdmin:
		return RoutingControlAuditSourceAdmin
	case RoutingOperationSourceMigration:
		return RoutingControlAuditSourceMigration
	case RoutingOperationSourceSystem, RoutingOperationSourceScheduler, RoutingOperationSourceRecovery:
		return RoutingControlAuditSourceSystem
	default:
		return RoutingControlAuditSourceSystem
	}
}

func routingControlAuditResultForOperation(status RoutingOperationStatus) string {
	switch status {
	case RoutingOperationStatusSucceeded:
		return RoutingControlAuditResultSucceeded
	case RoutingOperationStatusPartial:
		return RoutingControlAuditResultPartial
	case RoutingOperationStatusFailed, RoutingOperationStatusCancelled, RoutingOperationStatusSuperseded:
		return RoutingControlAuditResultFailed
	default:
		return RoutingControlAuditResultRejected
	}
}

func routingOperationStatusTerminal(status RoutingOperationStatus) bool {
	switch status {
	case RoutingOperationStatusSucceeded, RoutingOperationStatusPartial, RoutingOperationStatusFailed,
		RoutingOperationStatusCancelled, RoutingOperationStatusSuperseded:
		return true
	default:
		return false
	}
}
