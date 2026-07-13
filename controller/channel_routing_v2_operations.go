package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

type channelRoutingPolicyRollbackApprovalList struct {
	Items                []model.RoutingPolicyRollbackApproval `json:"items"`
	Groups               []channelRoutingPolicyApprovalGroup   `json:"groups"`
	Required             int                                   `json:"required"`
	RequiresApproval     bool                                  `json:"requires_approval"`
	Count                int                                   `json:"count"`
	Quorum               bool                                  `json:"quorum"`
	ExpectedRevision     int64                                 `json:"expected_revision"`
	TargetRevision       int64                                 `json:"target_revision"`
	TargetActivationHash string                                `json:"target_activation_hash,omitempty"`
}

type channelRoutingOperationView struct {
	model.RoutingOperation
	Result json.RawMessage `json:"result,omitempty"`
}

type channelRoutingOperationList struct {
	Items      []channelRoutingOperationView `json:"items"`
	NextCursor int64                         `json:"next_cursor"`
	HasMore    bool                          `json:"has_more"`
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

func ListChannelRoutingPolicyRollbackApprovals(c *gin.Context) {
	targetRevision, err := parseChannelRoutingPolicyRevision(c.Param("version"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_revision", "invalid channel routing policy revision", err)
		return
	}
	target, err := parseChannelRoutingPolicyApprovalTarget(c)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
		return
	}
	requiresApproval := false
	if target.Present {
		document, _, loadErr := model.LoadRoutingPolicyRevisionContext(c.Request.Context(), targetRevision)
		if loadErr != nil {
			writeChannelRoutingPolicyControlError(c, loadErr)
			return
		}
		requiresApproval, err = model.RoutingPolicyDeploymentRequiresApproval(document, target.Activation)
		if err != nil {
			writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
			return
		}
	}
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	items, err := model.ListRoutingPolicyRollbackApprovalsContext(c.Request.Context(), head, targetRevision)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	executorID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	groupsByHash := make(map[string]*channelRoutingPolicyApprovalGroup, len(items))
	for index := range items {
		approval := items[index]
		if approval.ActorID == executorID {
			continue
		}
		group := groupsByHash[approval.ActivationHash]
		if group == nil {
			group = &channelRoutingPolicyApprovalGroup{
				ActivationHash: approval.ActivationHash, ActivationStage: approval.ActivationStage,
				ActivationTrafficBasisPoints: approval.ActivationTrafficBasisPoints,
				ActivationReasonHash:         approval.ActivationReasonHash,
			}
			groupsByHash[approval.ActivationHash] = group
		}
		group.Count++
	}
	groups := make([]channelRoutingPolicyApprovalGroup, 0, len(groupsByHash))
	for _, group := range groupsByHash {
		group.Quorum = group.Count >= model.RoutingPolicyRequiredApprovals
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ActivationHash < groups[j].ActivationHash })
	targetHash := target.Hash
	count := 0
	quorum := false
	if targetHash != "" {
		if group := groupsByHash[targetHash]; group != nil {
			count = group.Count
			quorum = group.Quorum
		}
	} else {
		for index := range groups {
			count = max(count, groups[index].Count)
			quorum = quorum || groups[index].Quorum
		}
	}
	c.Header("ETag", channelRoutingPolicyHeadETag(head))
	common.ApiSuccess(c, channelRoutingPolicyRollbackApprovalList{
		Items: items, Groups: groups, Required: model.RoutingPolicyRequiredApprovals,
		RequiresApproval: requiresApproval, Count: count, Quorum: quorum, ExpectedRevision: head.CurrentRevision,
		TargetRevision: targetRevision, TargetActivationHash: targetHash,
	})
}

func ApproveChannelRoutingPolicyRollback(c *gin.Context) {
	targetRevision, err := parseChannelRoutingPolicyRevision(c.Param("version"))
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
	approval, created, err := model.CreateRoutingPolicyRollbackApprovalContext(
		c.Request.Context(), expectedHead, targetRevision, activation,
		common.GetContextKeyInt(c, constant.ContextKeyUserId),
	)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.Header("ETag", channelRoutingPolicyHeadETag(expectedHead))
	c.JSON(status, gin.H{"success": true, "message": "", "data": gin.H{
		"approval": approval, "created": created,
	}})
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
		BeforeID:      cursor,
		Limit:         limit,
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
	items := make([]channelRoutingOperationView, len(operations))
	for index := range operations {
		items[index] = channelRoutingOperationView{RoutingOperation: operations[index]}
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
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	common.ApiSuccess(c, view)
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

func channelRoutingOperationViewFromModel(operation model.RoutingOperation) (channelRoutingOperationView, error) {
	view := channelRoutingOperationView{RoutingOperation: operation}
	payload, err := operation.ResultPayload()
	if err != nil {
		return channelRoutingOperationView{}, err
	}
	if len(payload) == 0 {
		if operation.OperationType == model.RoutingOperationTypeCostSync && operation.SystemTaskID != "" {
			executionState := string(operation.Status)
			if operation.Status == model.RoutingOperationStatusPending {
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
	case errors.Is(err, model.ErrRoutingPolicyApprovalRequired):
		writeChannelRoutingPolicyDraftError(c, http.StatusPreconditionFailed, "policy_approval_required", "two distinct approvals are required before rollback", err)
	case errors.Is(err, model.ErrRoutingPolicyApprovalInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_approval_invalid", "channel routing policy rollback approval is invalid", err)
	case errors.Is(err, model.ErrRoutingPolicyReferenceInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_reference_invalid", "channel routing policy references changed", err)
	case errors.Is(err, model.ErrRoutingPolicyInvalid), errors.Is(err, model.ErrRoutingOperationInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_operation", "invalid channel routing policy operation", err)
	default:
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "policy_operation_failed", "channel routing policy operation failed", err)
	}
}
