package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type channelRoutingCurrentPolicyResponse struct {
	Head     model.RoutingPolicyHead      `json:"head"`
	Revision *model.RoutingPolicyRevision `json:"revision,omitempty"`
	Document model.RoutingPolicyDocument  `json:"document"`
}

type channelRoutingPolicyRevisionResponse struct {
	Revision model.RoutingPolicyRevision `json:"revision"`
	Document model.RoutingPolicyDocument `json:"document"`
}

type channelRoutingPolicyRollbackResponse struct {
	Published model.RoutingPolicyPublishResult `json:"published"`
	Operation model.RoutingOperation           `json:"operation"`
}

type channelRoutingPolicyRollbackDraftResponse struct {
	SourceRevision int64                                  `json:"source_revision"`
	Draft          model.RoutingPolicyDraftSummary        `json:"draft"`
	Document       model.RoutingPolicyDocument            `json:"document"`
	Conversion     model.RoutingPolicyV2ConversionSummary `json:"conversion"`
}

type channelRoutingOperationView struct {
	model.RoutingOperation
	Result json.RawMessage `json:"result,omitempty"`
}

type channelRoutingOperationList struct {
	Items      []channelRoutingOperationPublicView `json:"items"`
	NextCursor int64                               `json:"next_cursor"`
	HasMore    bool                                `json:"has_more"`
}

type channelRoutingOperationPublicView struct {
	ID                   int64                        `json:"id"`
	SchemaVersion        int                          `json:"schema_version"`
	OperationType        string                       `json:"type"`
	SubjectType          string                       `json:"subject_type"`
	SubjectID            int64                        `json:"subject_id"`
	PoolID               int                          `json:"pool_id"`
	ExpectedRevision     int64                        `json:"expected_revision"`
	ExpectedActivationID int64                        `json:"expected_activation_id"`
	ActorID              int                          `json:"actor_id"`
	Reason               string                       `json:"reason"`
	Source               string                       `json:"source"`
	CorrelationID        string                       `json:"correlation_id"`
	ParentOperationID    int64                        `json:"parent_operation_id,omitempty"`
	RetryOfOperationID   int64                        `json:"retry_of_operation_id,omitempty"`
	RetrySequence        int                          `json:"retry_sequence"`
	Retryable            bool                         `json:"retryable"`
	Cancellable          bool                         `json:"cancellable"`
	Summary              string                       `json:"summary"`
	NeedsAttention       bool                         `json:"needs_attention"`
	RetentionCategory    string                       `json:"retention_category"`
	Status               model.RoutingOperationStatus `json:"status"`
	Attempts             int                          `json:"attempts"`
	NextRetryMs          int64                        `json:"next_retry_ms"`
	LastError            string                       `json:"last_error"`
	ResultRevision       int64                        `json:"result_revision"`
	ResultActivationID   int64                        `json:"result_activation_id"`
	TerminalActorID      int                          `json:"terminal_actor_id,omitempty"`
	CreatedTimeMs        int64                        `json:"created_time_ms"`
	UpdatedTimeMs        int64                        `json:"updated_time_ms"`
	CompletedTimeMs      int64                        `json:"completed_time_ms"`
	Result               json.RawMessage              `json:"result,omitempty"`
}

type channelRoutingSimulationOperationSummary struct {
	PoolID                   int                                            `json:"pool_id"`
	Cursor                   int                                            `json:"cursor"`
	NextCursor               int                                            `json:"next_cursor"`
	Limit                    int                                            `json:"limit"`
	ScannedSamples           int                                            `json:"scanned_samples"`
	EvaluatedSamples         int                                            `json:"evaluated_samples"`
	ActualMatchCount         int                                            `json:"actual_match_count"`
	ActualMatchRate          *float64                                       `json:"actual_match_rate,omitempty"`
	SelectionChangedCount    int                                            `json:"selection_changed_count"`
	SelectionChangeRate      *float64                                       `json:"selection_change_rate,omitempty"`
	CostKnownSamples         int                                            `json:"cost_known_samples"`
	TotalExpectedCostDelta   float64                                        `json:"total_expected_cost_delta"`
	AverageCostDelta         *float64                                       `json:"average_expected_cost_delta,omitempty"`
	SkipReasons              map[string]int                                 `json:"skip_reasons"`
	SimulatedAlgorithm       string                                         `json:"simulated_algorithm"`
	Risk                     *channelrouting.PolicySimulationRiskAssessment `json:"risk,omitempty"`
	TargetBound              bool                                           `json:"target_bound,omitempty"`
	TargetStage              string                                         `json:"target_stage,omitempty"`
	TargetTrafficBasisPoints int                                            `json:"target_traffic_basis_points,omitempty"`
}

type channelRoutingOperationActionRequest struct {
	Reason string `json:"reason"`
}

func GetChannelRoutingCurrentPolicy(c *gin.Context) {
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	response := channelRoutingCurrentPolicyResponse{
		Head:     head,
		Document: model.RoutingPolicyDocument{SchemaVersion: model.RoutingPolicySchemaVersion, Pools: []model.RoutingPolicyPoolContent{}},
	}
	if head.CurrentRevision > 0 {
		document, revision, loadErr := model.LoadRoutingPolicyRevisionContext(c.Request.Context(), head.CurrentRevision)
		if loadErr != nil {
			writeChannelRoutingPolicyControlError(c, loadErr)
			return
		}
		response.Document = document
		response.Revision = &revision
	}
	c.Header("ETag", channelRoutingPolicyHeadETag(head))
	common.ApiSuccess(c, response)
}

func GetChannelRoutingPolicyRevision(c *gin.Context) {
	revisionNumber, err := parseChannelRoutingPolicyRevision(c.Param("version"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_revision", "invalid channel routing policy revision", err)
		return
	}
	document, revision, err := model.LoadRoutingPolicyRevisionContext(c.Request.Context(), revisionNumber)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	common.ApiSuccess(c, channelRoutingPolicyRevisionResponse{Revision: revision, Document: document})
}

func RollbackChannelRoutingPolicy(c *gin.Context) {
	sourceRevision, err := parseChannelRoutingPolicyRevision(c.Param("version"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_revision", "invalid channel routing policy revision", err)
		return
	}
	expectedHead, ok := requireChannelRoutingPolicyHeadIfMatch(c)
	if !ok {
		return
	}
	activation, err := decodeChannelRoutingPolicyActivation(c.Request.Body)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
		return
	}
	activation.ActorID = common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if err := activation.Validate(); err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
		return
	}
	requestIdentity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypePolicyRollback, struct {
		ExpectedRevision int64                             `json:"expected_revision"`
		SourceRevision   int64                             `json:"source_revision"`
		Activation       model.RoutingPolicyActivationSpec `json:"activation"`
	}{ExpectedRevision: expectedHead.CurrentRevision, SourceRevision: sourceRevision, Activation: activation})
	if !ok {
		return
	}
	published, operation, err := model.RollbackRoutingPolicyRevisionWithOperationRequestContext(
		c.Request.Context(), expectedHead.CurrentRevision, sourceRevision, activation, requestIdentity,
	)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	c.Header("ETag", channelRoutingPolicyHeadETag(head))
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyRolledBack, published.Revision.Revision, gin.H{
		"operation_id": operation.ID, "source_revision": sourceRevision, "revision": published.Revision.Revision,
		"activation_id": published.Activation.ID, "stage": published.Activation.Stage,
		"traffic_basis_points": published.Activation.TrafficBasisPoints,
	})
	common.ApiSuccess(c, channelRoutingPolicyRollbackResponse{Published: published, Operation: operation})
}

func CreateChannelRoutingPolicyRollbackDraft(c *gin.Context) {
	sourceRevision, err := parseChannelRoutingPolicyRevision(c.Param("version"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_revision", "invalid channel routing policy revision", err)
		return
	}
	head, ok := requireChannelRoutingPolicyHeadIfMatch(c)
	if !ok {
		return
	}
	if sourceRevision >= head.CurrentRevision {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_rollback_source", "rollback source must be older than the current policy", model.ErrRoutingPolicyInvalid)
		return
	}
	document, source, err := model.LoadRoutingPolicyRevisionContext(c.Request.Context(), sourceRevision)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	converted, conversion, err := model.ConvertRoutingPolicyDocumentToV2DBContext(
		c.Request.Context(), model.DB, document, true,
	)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	draft, err := model.CreateRoutingPolicyDraftContext(
		c.Request.Context(), head.CurrentRevision, converted,
		common.GetContextKeyInt(c, constant.ContextKeyUserId),
	)
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	summary, err := model.DecorateRoutingPolicyDraftSummaryContext(c.Request.Context(), draft.Summary())
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	c.Header("ETag", channelRoutingPolicyDraftETag(summary))
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, head.CurrentRevision, gin.H{
		"action": "rollback_draft_created", "draft_id": summary.ID,
		"source_revision": source.Revision, "source_schema_version": source.SchemaVersion,
	})
	c.JSON(http.StatusCreated, gin.H{"success": true, "message": "", "data": channelRoutingPolicyRollbackDraftResponse{
		SourceRevision: sourceRevision, Draft: summary, Document: converted, Conversion: conversion,
	}})
}

func ListChannelRoutingOperations(c *gin.Context) {
	limit, err := parseChannelRoutingPolicyDraftLimit(c.Query("limit"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_limit", "invalid channel routing operation limit", err)
		return
	}
	cursor, err := parseChannelRoutingPolicyDraftCursor(c.Query("cursor"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_cursor", "invalid channel routing operation cursor", err)
		return
	}
	filter := model.RoutingOperationFilter{
		OperationType: strings.TrimSpace(c.Query("type")),
		Status:        model.RoutingOperationStatus(strings.TrimSpace(c.Query("status"))),
		Source:        strings.TrimSpace(c.Query("source")),
		CorrelationID: strings.TrimSpace(c.Query("correlation_id")),
		Retention:     strings.TrimSpace(c.Query("retention_category")),
		BeforeID:      cursor,
		Limit:         limit,
	}
	if raw, exists := c.GetQuery("needs_attention"); exists {
		value, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_filter", "invalid channel routing operation filter", parseErr)
			return
		}
		filter.NeedsAttention = &value
	}
	operations, hasMore, err := model.ListRoutingOperationsContext(c.Request.Context(), filter)
	if err != nil {
		if errors.Is(err, model.ErrRoutingOperationInvalid) {
			writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_filter", "invalid channel routing operation filter", err)
			return
		}
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	items := make([]channelRoutingOperationPublicView, len(operations))
	for index := range operations {
		items[index], err = channelRoutingOperationPublicViewFromModel(operations[index], false)
		if err != nil {
			writeChannelRoutingPolicyControlError(c, err)
			return
		}
	}
	nextCursor := int64(0)
	if hasMore && len(operations) > 0 {
		nextCursor = operations[len(operations)-1].ID
	}
	common.ApiSuccess(c, channelRoutingOperationList{Items: items, NextCursor: nextCursor, HasMore: hasMore})
}

func GetChannelRoutingOperation(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_id", "invalid channel routing operation id", model.ErrRoutingOperationInvalid)
		return
	}
	operation, err := model.GetRoutingOperationContext(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "operation_not_found", "channel routing operation not found", err)
			return
		}
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	view, err := channelRoutingOperationPublicViewFromModel(operation, true)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

func GetChannelRoutingOperationTechnical(c *gin.Context) {
	id, ok := parseChannelRoutingOperationID(c)
	if !ok {
		return
	}
	operation, err := model.GetRoutingOperationContext(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "operation_not_found", "channel routing operation not found", err)
		return
	}
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	technical, err := operation.TechnicalPayload()
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"id": operation.ID, "schema_version": operation.SchemaVersion,
		"type": operation.OperationType, "technical": technical,
	})
}

func RetryChannelRoutingOperation(c *gin.Context) {
	id, ok := parseChannelRoutingOperationID(c)
	if !ok {
		return
	}
	reason, ok := decodeChannelRoutingOperationAction(c)
	if !ok {
		return
	}
	operation, created, err := model.RetryTerminalRoutingOperationContext(
		c.Request.Context(), id, common.GetContextKeyInt(c, constant.ContextKeyUserId), reason,
	)
	if err != nil {
		writeChannelRoutingOperationActionError(c, err, "operation_not_retryable", "channel routing operation cannot be retried")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	view, err := channelRoutingOperationPublicViewFromModel(operation, true)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeOperationChanged, operation.ExpectedRevision, gin.H{
		"action": "retried", "operation_id": operation.ID, "retry_of_operation_id": operation.RetryOfOperationID,
		"correlation_id": operation.CorrelationID, "status": operation.Status,
	})
	c.JSON(status, gin.H{"success": true, "message": "", "data": gin.H{
		"operation": view, "created": created,
	}})
}

func CancelChannelRoutingOperation(c *gin.Context) {
	id, ok := parseChannelRoutingOperationID(c)
	if !ok {
		return
	}
	reason, ok := decodeChannelRoutingOperationAction(c)
	if !ok {
		return
	}
	operation, err := model.CancelRoutingOperationContext(
		c.Request.Context(), id, common.GetContextKeyInt(c, constant.ContextKeyUserId), reason,
	)
	if err != nil {
		writeChannelRoutingOperationActionError(c, err, "operation_not_cancellable", "channel routing operation cannot be cancelled")
		return
	}
	view, err := channelRoutingOperationPublicViewFromModel(operation, true)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeOperationChanged, operation.ExpectedRevision, gin.H{
		"action": "cancelled", "operation_id": operation.ID,
		"correlation_id": operation.CorrelationID, "status": operation.Status,
	})
	common.ApiSuccess(c, view)
}

func parseChannelRoutingOperationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_id", "invalid channel routing operation id", model.ErrRoutingOperationInvalid)
		return 0, false
	}
	return id, true
}

func decodeChannelRoutingOperationAction(c *gin.Context) (string, bool) {
	if c.Request.Body == nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_action", "operation reason is required", model.ErrRoutingOperationInvalid)
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 4_097))
	if err != nil || len(data) == 0 || len(data) > 4_096 {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_action", "operation reason is required", model.ErrRoutingOperationInvalid)
		return "", false
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || len(fields) != 1 {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_action", "operation reason is required", model.ErrRoutingOperationInvalid)
		return "", false
	}
	raw, exists := fields["reason"]
	var request channelRoutingOperationActionRequest
	if !exists || common.Unmarshal(raw, &request.Reason) != nil || strings.TrimSpace(request.Reason) == "" {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_operation_action", "operation reason is required", model.ErrRoutingOperationInvalid)
		return "", false
	}
	return request.Reason, true
}

func writeChannelRoutingOperationActionError(c *gin.Context, err error, code string, message string) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "operation_not_found", "channel routing operation not found", err)
		return
	}
	if errors.Is(err, model.ErrRoutingOperationInvalid) || errors.Is(err, model.ErrRoutingOperationClaimLost) {
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, code, message, err)
		return
	}
	writeChannelRoutingPolicyControlError(c, err)
}

func requireChannelRoutingPolicyHeadIfMatch(c *gin.Context) (model.RoutingPolicyHead, bool) {
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeChannelRoutingPolicyDraftError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", model.ErrRoutingPolicyRevisionConflict)
		return model.RoutingPolicyHead{}, false
	}
	expected, err := parseChannelRoutingPolicyHeadETag(ifMatch)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_if_match", "invalid If-Match policy head tag", err)
		return model.RoutingPolicyHead{}, false
	}
	actual, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return model.RoutingPolicyHead{}, false
	}
	if actual.CurrentRevision != expected.CurrentRevision || actual.CurrentActivationID != expected.CurrentActivationID ||
		actual.CurrentHash != expected.CurrentHash {
		c.Header("ETag", channelRoutingPolicyHeadETag(actual))
		c.JSON(http.StatusConflict, gin.H{
			"success": false, "code": "policy_head_conflict", "message": "channel routing policy head changed",
			"head": actual,
		})
		return model.RoutingPolicyHead{}, false
	}
	return actual, true
}

func channelRoutingPolicyHeadETag(head model.RoutingPolicyHead) string {
	hash := head.CurrentHash
	if hash == "" {
		hash = strings.Repeat("0", 64)
	}
	return fmt.Sprintf("\"crh.%d.%d.%s\"", head.CurrentRevision, head.CurrentActivationID, hash)
}

func parseChannelRoutingPolicyHeadETag(value string) (model.RoutingPolicyHead, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) != 4 || parts[0] != "crh" || len(parts[3]) != 64 {
		return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
	}
	revision, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || revision < 0 {
		return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
	}
	activationID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || activationID < 0 {
		return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
	}
	for _, char := range parts[3] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
		}
	}
	hash := parts[3]
	if revision == 0 {
		if hash != strings.Repeat("0", 64) || activationID != 0 {
			return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
		}
		hash = ""
	} else if hash == strings.Repeat("0", 64) || activationID <= 0 {
		return model.RoutingPolicyHead{}, model.ErrRoutingPolicyInvalid
	}
	return model.RoutingPolicyHead{
		CurrentRevision: revision, CurrentActivationID: activationID, CurrentHash: hash,
	}, nil
}

func parseChannelRoutingPolicyRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || revision <= 0 {
		return 0, model.ErrRoutingPolicyInvalid
	}
	return revision, nil
}

func channelRoutingOperationPublicViewFromModel(
	operation model.RoutingOperation,
	includeResult bool,
) (channelRoutingOperationPublicView, error) {
	view := channelRoutingOperationPublicView{
		ID: operation.ID, SchemaVersion: operation.SchemaVersion, OperationType: operation.OperationType,
		SubjectType: operation.SubjectType, SubjectID: operation.SubjectID, PoolID: operation.PoolID,
		ExpectedRevision: operation.ExpectedRevision, ExpectedActivationID: operation.ExpectedActivationID,
		ActorID: operation.ActorID, Reason: operation.Reason, Source: operation.Source,
		CorrelationID: operation.CorrelationID, ParentOperationID: operation.ParentOperationID,
		RetryOfOperationID: operation.RetryOfOperationID, RetrySequence: operation.RetrySequence,
		Retryable: operation.Retryable, Cancellable: operation.Cancellable, Summary: operation.Summary,
		NeedsAttention: operation.NeedsAttention, RetentionCategory: operation.RetentionCategory,
		Status: operation.Status, Attempts: operation.Attempts, NextRetryMs: operation.NextRetryMs,
		LastError: operation.LastError, ResultRevision: operation.ResultRevision,
		ResultActivationID: operation.ResultActivationID, TerminalActorID: operation.TerminalActorID,
		CreatedTimeMs: operation.CreatedTimeMs, UpdatedTimeMs: operation.UpdatedTimeMs,
		CompletedTimeMs: operation.CompletedTimeMs,
	}
	if !includeResult {
		return view, nil
	}
	result, err := channelRoutingOperationPublicResult(operation)
	if err != nil {
		return channelRoutingOperationPublicView{}, err
	}
	view.Result = result
	return view, nil
}

func channelRoutingOperationPublicResult(operation model.RoutingOperation) (json.RawMessage, error) {
	payload, err := operation.ResultPayload()
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		if operation.OperationType != model.RoutingOperationTypeCostSync || operation.SystemTaskID == "" {
			return nil, nil
		}
		executionState := string(operation.Status)
		if operation.Status == model.RoutingOperationStatusPending || operation.Status == model.RoutingOperationStatusRetryWait {
			executionState = "accepted"
		}
		return marshalChannelRoutingOperationPublicResult(struct {
			TaskStatus     string `json:"task_status"`
			ExecutionState string `json:"execution_state"`
		}{TaskStatus: string(operation.Status), ExecutionState: executionState})
	}

	switch operation.OperationType {
	case model.RoutingOperationTypeActiveProbe:
		var result channelrouting.ActiveProbeOperationResult
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(result)
	case model.RoutingOperationTypeAuditExport:
		var result model.RoutingAuditExportResult
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(struct {
			ExportID      string `json:"export_id"`
			RecordCount   int    `json:"record_count"`
			ContentBytes  int    `json:"content_bytes"`
			CreatedTimeMs int64  `json:"created_time_ms"`
			ExpiresTimeMs int64  `json:"expires_time_ms"`
		}{
			ExportID: result.ExportID, RecordCount: result.RecordCount, ContentBytes: result.ContentBytes,
			CreatedTimeMs: result.CreatedTimeMs, ExpiresTimeMs: result.ExpiresTimeMs,
		})
	case model.RoutingOperationTypeBreakerReset:
		var result struct {
			Scope      string `json:"scope"`
			Generation int64  `json:"generation"`
		}
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(result)
	case model.RoutingOperationTypeHistoricalSimulation:
		var result channelrouting.HistoricalSimulationResult
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(channelRoutingSimulationSummary(result))
	case model.RoutingOperationTypePolicySimulation:
		var result channelRoutingPolicySimulationOperationResult
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		summary := channelRoutingSimulationSummary(result.Result)
		summary.TargetBound = result.TargetBound
		summary.TargetStage = result.TargetStage
		summary.TargetTrafficBasisPoints = result.TargetTrafficBasisPoints
		return marshalChannelRoutingOperationPublicResult(summary)
	case model.RoutingOperationTypePolicyPublish:
		var result struct {
			DraftID      int64 `json:"draft_id"`
			DraftVersion int64 `json:"draft_version"`
		}
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(result)
	case model.RoutingOperationTypePolicyRollback:
		var result struct {
			SourceRevision int64 `json:"source_revision"`
		}
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(result)
	case model.RoutingOperationTypeCostSync:
		var result struct {
			TaskStatus     string `json:"task_status,omitempty"`
			ExecutionState string `json:"execution_state,omitempty"`
		}
		if common.Unmarshal(payload, &result) != nil {
			return nil, model.ErrRoutingOperationInvalid
		}
		return marshalChannelRoutingOperationPublicResult(result)
	default:
		return nil, nil
	}
}

func channelRoutingSimulationSummary(
	result channelrouting.HistoricalSimulationResult,
) channelRoutingSimulationOperationSummary {
	return channelRoutingSimulationOperationSummary{
		PoolID: result.PoolID, Cursor: result.Cursor, NextCursor: result.NextCursor, Limit: result.Limit,
		ScannedSamples: result.ScannedSamples, EvaluatedSamples: result.EvaluatedSamples,
		ActualMatchCount: result.ActualMatchCount, ActualMatchRate: result.ActualMatchRate,
		SelectionChangedCount: result.SelectionChangedCount, SelectionChangeRate: result.SelectionChangeRate,
		CostKnownSamples: result.CostKnownSamples, TotalExpectedCostDelta: result.TotalExpectedCostDelta,
		AverageCostDelta: result.AverageCostDelta, SkipReasons: result.SkipReasons,
		SimulatedAlgorithm: result.SimulatedAlgorithm, Risk: result.Risk,
	}
}

func marshalChannelRoutingOperationPublicResult(value any) (json.RawMessage, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, model.ErrRoutingOperationInvalid
	}
	return json.RawMessage(encoded), nil
}

func channelRoutingOperationViewFromModel(operation model.RoutingOperation) (channelRoutingOperationView, error) {
	view := channelRoutingOperationView{RoutingOperation: operation}
	payload, err := operation.ResultPayload()
	if err != nil {
		return channelRoutingOperationView{}, err
	}
	if len(payload) == 0 {
		if operation.OperationType == model.RoutingOperationTypeCostSync && operation.SystemTaskID != "" {
			executionState := string(operation.Status)
			if operation.Status == model.RoutingOperationStatusPending || operation.Status == model.RoutingOperationStatusRetryWait {
				executionState = "accepted"
			}
			encoded, err := common.Marshal(map[string]string{
				"system_task_id":   operation.SystemTaskID,
				"system_task_type": model.SystemTaskTypeRoutingCostSync,
				"task_status":      string(operation.Status),
				"execution_state":  executionState,
			})
			if err != nil {
				return channelRoutingOperationView{}, model.ErrRoutingOperationInvalid
			}
			view.Result = json.RawMessage(encoded)
		}
		return view, nil
	}
	var result json.RawMessage
	if common.Unmarshal(payload, &result) != nil || len(result) == 0 || string(result) == "null" {
		return channelRoutingOperationView{}, model.ErrRoutingOperationInvalid
	}
	view.Result = result
	return view, nil
}

func appendChannelRoutingOperationCreatedResult(view *channelRoutingOperationView, created bool) error {
	if view == nil || len(view.Result) == 0 {
		return nil
	}
	var result map[string]json.RawMessage
	if common.Unmarshal(view.Result, &result) != nil || result == nil {
		return nil
	}
	createdPayload, err := common.Marshal(created)
	if err != nil {
		return model.ErrRoutingOperationInvalid
	}
	result["created"] = json.RawMessage(createdPayload)
	encoded, err := common.Marshal(result)
	if err != nil {
		return model.ErrRoutingOperationInvalid
	}
	view.Result = json.RawMessage(encoded)
	return nil
}

func writeChannelRoutingPolicyControlError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrRoutingPolicyRevisionNotFound):
		writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "policy_revision_not_found", "channel routing policy revision not found", err)
	case errors.Is(err, model.ErrRoutingPolicyRevisionConflict):
		var conflict *model.RoutingPolicyRevisionConflictError
		if errors.As(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{
				"success": false, "code": "policy_revision_conflict", "message": "channel routing policy revision changed",
				"conflict": conflict,
			})
			return
		}
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_revision_conflict", "channel routing policy revision changed", err)
	case errors.Is(err, model.ErrRoutingOperationIdempotencyConflict):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", err)
	case errors.Is(err, model.ErrRoutingPolicyReferenceInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_reference_invalid", "channel routing policy references changed", err)
	case errors.Is(err, model.ErrRoutingPolicyLegacyRollback):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "legacy_policy_requires_conversion_draft", "legacy policy history must be converted into a v2 draft before rollback", err)
	case errors.Is(err, model.ErrRoutingPolicyInvalid), errors.Is(err, model.ErrRoutingOperationInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_operation", "invalid channel routing policy operation", err)
	default:
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "policy_operation_failed", "channel routing policy operation failed", err)
	}
}
