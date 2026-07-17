package controller

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

const maxChannelRoutingPolicyDraftBody = model.RoutingPolicyDraftMaxDocumentBytes + 64<<10
const maxChannelRoutingPolicySimulationBody = 4 << 10
const maxChannelRoutingIdempotencyKeyBytes = 200

var errChannelRoutingPolicyDraftBodyTooLarge = errors.New("channel routing policy draft body is too large")
var errChannelRoutingPolicyDraftNotValidated = errors.New("channel routing policy draft is not validated")

type channelRoutingPolicyDraftDetail struct {
	model.RoutingPolicyDraftSummary
	Document model.RoutingPolicyDocument `json:"document"`
}

type channelRoutingPolicyDraftList struct {
	Items      []model.RoutingPolicyDraftSummary `json:"items"`
	NextCursor int64                             `json:"next_cursor"`
	HasMore    bool                              `json:"has_more"`
}

type channelRoutingPolicyDraftBatchDeleteRequest struct {
	Items []struct {
		ID   int64  `json:"id"`
		ETag string `json:"etag"`
	} `json:"items"`
}

type channelRoutingPolicySimulationRequest struct {
	PoolID                   int
	Cursor                   int
	Limit                    int
	TargetBound              bool
	TargetStage              string
	TargetTrafficBasisPoints int
}

type channelRoutingPolicySimulationResponse struct {
	Draft     model.RoutingPolicyDraftSummary           `json:"draft"`
	Operation model.RoutingOperation                    `json:"operation"`
	Evidence  *model.RoutingPolicySimulationEvidence    `json:"evidence"`
	Result    channelrouting.HistoricalSimulationResult `json:"result"`
}

type channelRoutingPolicySimulationOperationResult struct {
	Draft                    model.RoutingPolicyDraftSummary           `json:"draft"`
	TargetBound              bool                                      `json:"target_bound"`
	TargetStage              string                                    `json:"target_stage,omitempty"`
	TargetTrafficBasisPoints int                                       `json:"target_traffic_basis_points"`
	Result                   channelrouting.HistoricalSimulationResult `json:"result"`
}

type channelRoutingPolicyPublishRequest struct {
	Activation     model.RoutingPolicyActivationSpec
	RiskAcceptance model.RoutingPolicyRiskAcceptanceSpec
}

type channelRoutingPolicyPublishResponse struct {
	Draft     model.RoutingPolicyDraftSummary  `json:"draft"`
	Published model.RoutingPolicyPublishResult `json:"published"`
	Operation model.RoutingOperation           `json:"operation"`
}

func ListChannelRoutingPolicyDrafts(c *gin.Context) {
	limit, err := parseChannelRoutingPolicyDraftLimit(c.Query("limit"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_limit", "invalid policy draft limit", err)
		return
	}
	cursor, err := parseChannelRoutingPolicyDraftCursor(c.Query("cursor"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_cursor", "invalid policy draft cursor", err)
		return
	}
	drafts, hasMore, err := model.ListRoutingPolicyDraftsContext(c.Request.Context(), cursor, limit)
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	nextCursor := int64(0)
	if hasMore && len(drafts) > 0 {
		nextCursor = drafts[len(drafts)-1].ID
	}
	common.ApiSuccess(c, channelRoutingPolicyDraftList{Items: drafts, NextCursor: nextCursor, HasMore: hasMore})
}

func GetChannelRoutingPolicyDraft(c *gin.Context) {
	id, err := parseChannelRoutingPolicyDraftID(c.Param("id"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_draft_id", "invalid policy draft id", err)
		return
	}
	draft, err := model.GetRoutingPolicyDraftContext(c.Request.Context(), id)
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	document, err := draft.Document()
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
	common.ApiSuccess(c, channelRoutingPolicyDraftDetail{RoutingPolicyDraftSummary: summary, Document: document})
}

func CreateChannelRoutingPolicyDraft(c *gin.Context) {
	baseRevision, document, err := decodeChannelRoutingPolicyDraftCreate(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_policy_draft"
		message := "invalid channel routing policy draft"
		if errors.Is(err, errChannelRoutingPolicyDraftBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "policy_draft_too_large"
			message = "channel routing policy draft is too large"
		}
		writeChannelRoutingPolicyDraftError(c, status, code, message, err)
		return
	}
	draft, err := model.CreateRoutingPolicyDraftContext(
		c.Request.Context(), baseRevision, document,
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
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, summary.BaseRevision, gin.H{
		"action": "created", "draft_id": summary.ID, "draft_version": summary.Version, "status": summary.Status,
	})
	c.JSON(http.StatusCreated, gin.H{"success": true, "message": "", "data": summary})
}

func UpdateChannelRoutingPolicyDraft(c *gin.Context) {
	id, version, etag, ok := requireChannelRoutingPolicyDraftIfMatch(c)
	if !ok {
		return
	}
	document, err := decodeChannelRoutingPolicyDraftUpdate(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_policy_draft"
		message := "invalid channel routing policy draft"
		if errors.Is(err, errChannelRoutingPolicyDraftBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "policy_draft_too_large"
			message = "channel routing policy draft is too large"
		}
		writeChannelRoutingPolicyDraftError(c, status, code, message, err)
		return
	}
	draft, err := model.UpdateRoutingPolicyDraftContext(
		c.Request.Context(), id, version, etag, document,
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
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, summary.BaseRevision, gin.H{
		"action": "updated", "draft_id": summary.ID, "draft_version": summary.Version, "status": summary.Status,
	})
	common.ApiSuccess(c, summary)
}

func ValidateChannelRoutingPolicyDraft(c *gin.Context) {
	id, version, etag, ok := requireChannelRoutingPolicyDraftIfMatch(c)
	if !ok {
		return
	}
	draft, err := model.ValidateRoutingPolicyDraftContext(
		c.Request.Context(), id, version, etag,
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
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, summary.BaseRevision, gin.H{
		"action": "validated", "draft_id": summary.ID, "draft_version": summary.Version, "status": summary.Status,
	})
	common.ApiSuccess(c, summary)
}

func SimulateChannelRoutingPolicyDraft(c *gin.Context) {
	id, version, etag, ok := requireChannelRoutingPolicyDraftIfMatch(c)
	if !ok {
		return
	}
	request, err := decodeChannelRoutingPolicySimulation(c.Request.Body)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_simulation", "invalid channel routing policy simulation", err)
		return
	}
	requestIdentity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypePolicySimulation, struct {
		DraftID                  int64  `json:"draft_id"`
		ExpectedVersion          int64  `json:"expected_version"`
		ExpectedETag             string `json:"expected_etag"`
		PoolID                   int    `json:"pool_id"`
		Cursor                   int    `json:"cursor"`
		Limit                    int    `json:"limit"`
		TargetBound              bool   `json:"target_bound"`
		TargetStage              string `json:"target_stage,omitempty"`
		TargetTrafficBasisPoints int    `json:"target_traffic_basis_points"`
	}{
		DraftID: id, ExpectedVersion: version, ExpectedETag: etag,
		PoolID: request.PoolID, Cursor: request.Cursor, Limit: request.Limit,
		TargetBound: request.TargetBound, TargetStage: request.TargetStage,
		TargetTrafficBasisPoints: request.TargetTrafficBasisPoints,
	})
	if !ok {
		return
	}
	existing, lookupErr := model.GetRoutingOperationByRequestIdentityContext(c.Request.Context(), requestIdentity)
	if lookupErr == nil {
		if existing.OperationType != model.RoutingOperationTypePolicySimulation {
			writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", model.ErrRoutingOperationIdempotencyConflict)
			return
		}
		payload, payloadErr := existing.ResultPayload()
		var stored channelRoutingPolicySimulationOperationResult
		if payloadErr != nil || common.Unmarshal(payload, &stored) != nil || stored.Draft.ID != id ||
			stored.TargetBound != request.TargetBound || stored.TargetStage != request.TargetStage ||
			stored.TargetTrafficBasisPoints != request.TargetTrafficBasisPoints {
			writeChannelRoutingPolicyControlError(c, model.ErrRoutingOperationCorrupt)
			return
		}
		evidence, evidenceErr := model.GetRoutingPolicySimulationEvidenceByOperationContext(
			c.Request.Context(), existing.ID,
		)
		if evidenceErr != nil {
			writeChannelRoutingPolicyControlError(c, model.ErrRoutingOperationCorrupt)
			return
		}
		c.Header("ETag", channelRoutingPolicyDraftETag(stored.Draft))
		common.ApiSuccess(c, channelRoutingPolicySimulationResponse{
			Draft: stored.Draft, Operation: existing, Evidence: &evidence, Result: stored.Result,
		})
		return
	}
	if errors.Is(lookupErr, model.ErrRoutingOperationIdempotencyConflict) {
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", lookupErr)
		return
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		writeChannelRoutingPolicyControlError(c, lookupErr)
		return
	}
	draft, err := model.GetRoutingPolicyDraftContext(c.Request.Context(), id)
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	if draft.Version != version || draft.ETag != etag {
		writeChannelRoutingPolicyDraftModelError(c, &model.RoutingPolicyDraftConflictError{
			DraftID: id, ExpectedVersion: version, ActualVersion: draft.Version,
			ExpectedETag: etag, ActualETag: draft.ETag, ActualStatus: draft.Status,
		})
		return
	}
	if draft.Status != model.RoutingPolicyDraftStatusValidated && draft.Status != model.RoutingPolicyDraftStatusPublished {
		writeChannelRoutingPolicyDraftError(
			c, http.StatusConflict, "policy_draft_not_validated", "channel routing policy draft must be validated before simulation",
			errChannelRoutingPolicyDraftNotValidated,
		)
		return
	}
	document, err := draft.Document()
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	baseDocument := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools:         []model.RoutingPolicyPoolContent{},
	}
	if draft.BaseRevision > 0 {
		var baseRevision model.RoutingPolicyRevision
		baseDocument, baseRevision, err = model.LoadRoutingPolicyRevisionContext(c.Request.Context(), draft.BaseRevision)
		if err != nil || baseRevision.ContentHash != draft.BaseHash {
			if err == nil {
				err = fmt.Errorf("%w: policy draft base hash mismatch", model.ErrRoutingPolicyContentCorrupt)
			}
			writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "simulation_failed", "channel routing policy simulation failed", err)
			return
		}
	}
	result, err := channelrouting.RunPolicyDocumentSimulationAgainstBase(
		c.Request.Context(), baseDocument, document, request.PoolID, request.Cursor, request.Limit,
	)
	if err != nil {
		if errors.Is(err, channelrouting.ErrSimulationInvalidOptions) {
			writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_simulation", "invalid channel routing policy simulation", err)
			return
		}
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "simulation_failed", "channel routing policy simulation failed", err)
		return
	}
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "simulation_failed", "channel routing policy simulation failed", err)
		return
	}
	actorID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	simulationSummary, err := model.DecorateRoutingPolicyDraftSummaryContext(c.Request.Context(), draft.Summary())
	if err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	operation, _, err := model.CreateSucceededRoutingOperationContext(
		c.Request.Context(),
		model.RoutingOperationSpec{
			Type: model.RoutingOperationTypePolicySimulation, EvaluationHash: requestIdentity.PayloadHash,
			SubjectType: model.RoutingOperationSubjectPolicyDraft, SubjectID: draft.ID, PoolID: request.PoolID,
			ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
			ActorID: actorID, Reason: "policy draft simulation",
			RequestKeyHash: requestIdentity.KeyHash, RequestPayloadHash: requestIdentity.PayloadHash,
		},
		model.RoutingOperationResult{},
		channelRoutingPolicySimulationOperationResult{
			Draft: simulationSummary, TargetBound: request.TargetBound,
			TargetStage:              request.TargetStage,
			TargetTrafficBasisPoints: request.TargetTrafficBasisPoints,
			Result:                   result,
		},
	)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "simulation_operation_failed", "channel routing policy simulation operation failed", err)
		return
	}
	riskState := model.RoutingPolicySimulationRiskUnknown
	if result.Risk != nil {
		switch result.Risk.State {
		case channelrouting.PolicySimulationRiskReady:
			riskState = model.RoutingPolicySimulationRiskPass
		case channelrouting.PolicySimulationRiskBlocked:
			riskState = model.RoutingPolicySimulationRiskFail
		}
	}
	evidence, err := model.CreateRoutingPolicySimulationEvidenceContext(
		c.Request.Context(), model.RoutingPolicySimulationEvidenceSpec{
			OperationID: operation.ID, Draft: simulationSummary, Head: head,
			TargetBound: request.TargetBound, TargetStage: request.TargetStage,
			TargetTrafficBasisPoints: request.TargetTrafficBasisPoints,
			RiskState:                riskState, RiskPayload: result.Risk,
		},
	)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "simulation_evidence_failed", "channel routing policy simulation evidence could not be recorded", err)
		return
	}
	summary := simulationSummary
	c.Header("ETag", channelRoutingPolicyDraftETag(summary))
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicySimulation, head.CurrentRevision, gin.H{
		"operation_id": operation.ID, "draft_id": summary.ID, "draft_version": summary.Version,
		"pool_id": result.PoolID, "evaluated_samples": result.EvaluatedSamples,
	})
	common.ApiSuccess(c, channelRoutingPolicySimulationResponse{
		Draft: summary, Operation: operation, Evidence: &evidence, Result: result,
	})
}

func PublishChannelRoutingPolicyDraft(c *gin.Context) {
	id, version, etag, ok := requireChannelRoutingPolicyDraftIfMatch(c)
	if !ok {
		return
	}
	publishRequest, err := decodeChannelRoutingPolicyPublish(c.Request.Body)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
		return
	}
	activation := publishRequest.Activation
	activation.ActorID = common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if err := activation.Validate(); err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_activation", "invalid channel routing policy activation", err)
		return
	}
	requestIdentity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypePolicyPublish, struct {
		DraftID         int64                                 `json:"draft_id"`
		ExpectedVersion int64                                 `json:"expected_version"`
		ExpectedETag    string                                `json:"expected_etag"`
		Activation      model.RoutingPolicyActivationSpec     `json:"activation"`
		RiskAcceptance  model.RoutingPolicyRiskAcceptanceSpec `json:"risk_acceptance"`
	}{DraftID: id, ExpectedVersion: version, ExpectedETag: etag, Activation: activation, RiskAcceptance: publishRequest.RiskAcceptance})
	if !ok {
		return
	}
	draft, published, operation, err := model.PublishRoutingPolicyDraftWithOperationRequestAndRiskContext(
		c.Request.Context(), id, version, etag, activation, requestIdentity, publishRequest.RiskAcceptance,
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
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyPublished, published.Revision.Revision, gin.H{
		"operation_id": operation.ID, "draft_id": summary.ID, "revision": published.Revision.Revision,
		"activation_id": published.Activation.ID, "stage": published.Activation.Stage,
		"traffic_basis_points": published.Activation.TrafficBasisPoints,
	})
	common.ApiSuccess(c, channelRoutingPolicyPublishResponse{
		Draft: summary, Published: published, Operation: operation,
	})
}

func DeleteChannelRoutingPolicyDraft(c *gin.Context) {
	id, version, etag, ok := requireChannelRoutingPolicyDraftIfMatch(c)
	if !ok {
		return
	}
	if err := model.DeleteRoutingPolicyDraftContext(c.Request.Context(), id, version, etag); err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, 0, gin.H{
		"action": "deleted", "draft_ids": []int64{id},
	})
	common.ApiSuccess(c, gin.H{"deleted_ids": []int64{id}})
}

func DeleteChannelRoutingPolicyDrafts(c *gin.Context) {
	if c.Request.Body == nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_draft_delete", "invalid policy draft delete request", model.ErrRoutingPolicyDraftInvalid)
		return
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, (64<<10)+1))
	if err != nil || len(data) == 0 || len(data) > 64<<10 {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_draft_delete", "invalid policy draft delete request", model.ErrRoutingPolicyDraftInvalid)
		return
	}
	var request channelRoutingPolicyDraftBatchDeleteRequest
	if common.Unmarshal(data, &request) != nil || len(request.Items) == 0 ||
		len(request.Items) > model.RoutingPolicyDraftMaxDeleteBatch {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_draft_delete", "invalid policy draft delete request", model.ErrRoutingPolicyDraftInvalid)
		return
	}
	specs := make([]model.RoutingPolicyDraftDeleteSpec, len(request.Items))
	deletedIDs := make([]int64, len(request.Items))
	for index := range request.Items {
		item := request.Items[index]
		etagID, version, etag, parseErr := parseChannelRoutingPolicyDraftETag(item.ETag)
		if parseErr != nil || item.ID <= 0 || etagID != item.ID {
			writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_draft_delete", "invalid policy draft delete request", model.ErrRoutingPolicyDraftInvalid)
			return
		}
		specs[index] = model.RoutingPolicyDraftDeleteSpec{
			ID: item.ID, ExpectedVersion: version, ExpectedETag: etag,
		}
		deletedIDs[index] = item.ID
	}
	if err := model.DeleteRoutingPolicyDraftsContext(c.Request.Context(), specs); err != nil {
		writeChannelRoutingPolicyDraftModelError(c, err)
		return
	}
	sort.Slice(deletedIDs, func(left int, right int) bool { return deletedIDs[left] < deletedIDs[right] })
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicyDraftChanged, 0, gin.H{
		"action": "deleted", "draft_ids": deletedIDs,
	})
	common.ApiSuccess(c, gin.H{"deleted_ids": deletedIDs})
}

func requireChannelRoutingOperationIdempotency(
	c *gin.Context,
	operationType string,
	payload any,
) (model.RoutingOperationRequestIdentity, bool) {
	if c == nil {
		return model.RoutingOperationRequestIdentity{}, false
	}
	rawKey := c.GetHeader("Idempotency-Key")
	key := strings.TrimSpace(rawKey)
	valid := key == rawKey && len(key) >= 8 && len(key) <= maxChannelRoutingIdempotencyKeyBytes
	for index := 0; valid && index < len(key); index++ {
		valid = key[index] >= 0x21 && key[index] <= 0x7e
	}
	if !valid {
		writeChannelRoutingPolicyDraftError(
			c, http.StatusBadRequest, "invalid_idempotency_key",
			"Idempotency-Key must contain 8 to 200 printable ASCII characters", model.ErrRoutingOperationInvalid,
		)
		return model.RoutingOperationRequestIdentity{}, false
	}
	actorID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	canonical, err := common.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		OperationType string `json:"operation_type"`
		ActorID       int    `json:"actor_id"`
		Payload       any    `json:"payload"`
	}{SchemaVersion: 1, OperationType: operationType, ActorID: actorID, Payload: payload})
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "idempotency_failed", "failed to bind Idempotency-Key", err)
		return model.RoutingOperationRequestIdentity{}, false
	}
	keyMaterial := fmt.Sprintf("channel-routing-operation:v1\x00%s\x00%d\x00%s", operationType, actorID, key)
	keyDigest := sha256.Sum256([]byte(keyMaterial))
	payloadDigest := sha256.Sum256(canonical)
	c.Header("Idempotency-Key", key)
	return model.RoutingOperationRequestIdentity{
		KeyHash: fmt.Sprintf("%x", keyDigest[:]), PayloadHash: fmt.Sprintf("%x", payloadDigest[:]),
	}, true
}

func requireChannelRoutingPolicyDraftIfMatch(c *gin.Context) (int64, int64, string, bool) {
	id, err := parseChannelRoutingPolicyDraftID(c.Param("id"))
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_draft_id", "invalid policy draft id", err)
		return 0, 0, "", false
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeChannelRoutingPolicyDraftError(
			c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", model.ErrRoutingPolicyDraftConflict,
		)
		return 0, 0, "", false
	}
	matchedID, version, etag, err := parseChannelRoutingPolicyDraftETag(ifMatch)
	if err != nil || matchedID != id {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_if_match", "invalid If-Match policy draft tag", err)
		return 0, 0, "", false
	}
	return id, version, etag, true
}

func decodeChannelRoutingPolicyDraftCreate(body io.Reader) (int64, model.RoutingPolicyDocument, error) {
	fields, err := decodeChannelRoutingPolicyDraftFields(body)
	if err != nil {
		return 0, model.RoutingPolicyDocument{}, err
	}
	for key := range fields {
		if key != "base_revision" && key != "document" {
			return 0, model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
		}
	}
	baseRaw, baseExists := fields["base_revision"]
	documentRaw, documentExists := fields["document"]
	if !baseExists || !documentExists || isNullChannelRoutingJSON(baseRaw) || isNullChannelRoutingJSON(documentRaw) {
		return 0, model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
	}
	var baseRevision int64
	var document model.RoutingPolicyDocument
	if common.Unmarshal(baseRaw, &baseRevision) != nil || baseRevision < 0 || common.Unmarshal(documentRaw, &document) != nil {
		return 0, model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
	}
	return baseRevision, document, nil
}

func decodeChannelRoutingPolicyDraftUpdate(body io.Reader) (model.RoutingPolicyDocument, error) {
	fields, err := decodeChannelRoutingPolicyDraftFields(body)
	if err != nil {
		return model.RoutingPolicyDocument{}, err
	}
	if len(fields) != 1 {
		return model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
	}
	documentRaw, exists := fields["document"]
	if !exists || isNullChannelRoutingJSON(documentRaw) {
		return model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
	}
	var document model.RoutingPolicyDocument
	if common.Unmarshal(documentRaw, &document) != nil {
		return model.RoutingPolicyDocument{}, model.ErrRoutingPolicyDraftInvalid
	}
	return document, nil
}

func decodeChannelRoutingPolicyDraftFields(body io.Reader) (map[string]json.RawMessage, error) {
	if body == nil {
		return nil, model.ErrRoutingPolicyDraftInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, maxChannelRoutingPolicyDraftBody+1))
	if err != nil || len(data) == 0 {
		return nil, model.ErrRoutingPolicyDraftInvalid
	}
	if len(data) > maxChannelRoutingPolicyDraftBody {
		return nil, errChannelRoutingPolicyDraftBodyTooLarge
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, model.ErrRoutingPolicyDraftInvalid
	}
	return fields, nil
}

func decodeChannelRoutingPolicySimulation(body io.Reader) (channelRoutingPolicySimulationRequest, error) {
	if body == nil {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	data, err := io.ReadAll(io.LimitReader(body, maxChannelRoutingPolicySimulationBody+1))
	if err != nil || len(data) == 0 || len(data) > maxChannelRoutingPolicySimulationBody {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	for key := range fields {
		if key != "pool_id" && key != "cursor" && key != "limit" &&
			key != "target_stage" && key != "target_traffic_basis_points" {
			return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
		}
	}
	poolRaw, exists := fields["pool_id"]
	if !exists || isNullChannelRoutingJSON(poolRaw) {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	request := channelRoutingPolicySimulationRequest{Limit: channelrouting.DefaultSimulationLimit}
	if common.Unmarshal(poolRaw, &request.PoolID) != nil || request.PoolID <= 0 {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	if cursorRaw, exists := fields["cursor"]; exists {
		if isNullChannelRoutingJSON(cursorRaw) || common.Unmarshal(cursorRaw, &request.Cursor) != nil || request.Cursor < 0 {
			return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
		}
	}
	if limitRaw, exists := fields["limit"]; exists {
		if isNullChannelRoutingJSON(limitRaw) || common.Unmarshal(limitRaw, &request.Limit) != nil ||
			request.Limit < 1 || request.Limit > channelrouting.MaxSimulationLimit {
			return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
		}
	}
	targetStageRaw, targetStageExists := fields["target_stage"]
	targetTrafficRaw, targetTrafficExists := fields["target_traffic_basis_points"]
	if targetStageExists != targetTrafficExists {
		return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
	}
	if targetStageExists {
		if isNullChannelRoutingJSON(targetStageRaw) || isNullChannelRoutingJSON(targetTrafficRaw) ||
			common.Unmarshal(targetStageRaw, &request.TargetStage) != nil ||
			common.Unmarshal(targetTrafficRaw, &request.TargetTrafficBasisPoints) != nil {
			return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
		}
		target := model.RoutingPolicyActivationSpec{
			Stage: request.TargetStage, TrafficBasisPoints: request.TargetTrafficBasisPoints,
			ActorID: 0, Reason: "simulation target",
		}
		if target.Validate() != nil {
			return channelRoutingPolicySimulationRequest{}, channelrouting.ErrSimulationInvalidOptions
		}
		request.TargetBound = true
	}
	return request, nil
}

func decodeChannelRoutingPolicyPublish(body io.Reader) (channelRoutingPolicyPublishRequest, error) {
	if body == nil {
		return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, maxChannelRoutingPolicySimulationBody+1))
	if err != nil || len(data) == 0 || len(data) > maxChannelRoutingPolicySimulationBody {
		return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
	}
	for key := range fields {
		if key != "stage" && key != "traffic_basis_points" && key != "reason" &&
			key != "accept_simulation_risk" && key != "risk_acceptance_reason" {
			return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
		}
	}
	activation, err := decodeChannelRoutingPolicyActivationFields(fields)
	if err != nil {
		return channelRoutingPolicyPublishRequest{}, err
	}
	request := channelRoutingPolicyPublishRequest{Activation: activation}
	if acceptedRaw, exists := fields["accept_simulation_risk"]; exists {
		if isNullChannelRoutingJSON(acceptedRaw) || common.Unmarshal(acceptedRaw, &request.RiskAcceptance.Accepted) != nil {
			return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
		}
	}
	if reasonRaw, exists := fields["risk_acceptance_reason"]; exists {
		if isNullChannelRoutingJSON(reasonRaw) || common.Unmarshal(reasonRaw, &request.RiskAcceptance.Reason) != nil {
			return channelRoutingPolicyPublishRequest{}, model.ErrRoutingPolicyInvalid
		}
	}
	return request, nil
}

func decodeChannelRoutingPolicyActivation(body io.Reader) (model.RoutingPolicyActivationSpec, error) {
	if body == nil {
		return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, maxChannelRoutingPolicySimulationBody+1))
	if err != nil || len(data) == 0 || len(data) > maxChannelRoutingPolicySimulationBody {
		return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
	}
	for key := range fields {
		if key != "stage" && key != "traffic_basis_points" && key != "reason" {
			return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
		}
	}
	return decodeChannelRoutingPolicyActivationFields(fields)
}

func decodeChannelRoutingPolicyActivationFields(
	fields map[string]json.RawMessage,
) (model.RoutingPolicyActivationSpec, error) {
	stageRaw, stageExists := fields["stage"]
	reasonRaw, reasonExists := fields["reason"]
	if !stageExists || !reasonExists || isNullChannelRoutingJSON(stageRaw) || isNullChannelRoutingJSON(reasonRaw) {
		return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
	}
	var activation model.RoutingPolicyActivationSpec
	if common.Unmarshal(stageRaw, &activation.Stage) != nil || common.Unmarshal(reasonRaw, &activation.Reason) != nil {
		return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
	}
	if trafficRaw, exists := fields["traffic_basis_points"]; exists {
		if isNullChannelRoutingJSON(trafficRaw) || common.Unmarshal(trafficRaw, &activation.TrafficBasisPoints) != nil {
			return model.RoutingPolicyActivationSpec{}, model.ErrRoutingPolicyInvalid
		}
	}
	return activation, nil
}

func channelRoutingPolicyDraftETag(draft model.RoutingPolicyDraftSummary) string {
	return fmt.Sprintf("\"crd.%d.%d.%s\"", draft.ID, draft.Version, draft.ETag)
}

func parseChannelRoutingPolicyDraftETag(value string) (int64, int64, string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 256 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, 0, "", model.ErrRoutingPolicyDraftInvalid
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) != 4 || parts[0] != "crd" {
		return 0, 0, "", model.ErrRoutingPolicyDraftInvalid
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, "", model.ErrRoutingPolicyDraftInvalid
	}
	version, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || version <= 0 || len(parts[3]) != 64 {
		return 0, 0, "", model.ErrRoutingPolicyDraftInvalid
	}
	for _, char := range parts[3] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return 0, 0, "", model.ErrRoutingPolicyDraftInvalid
		}
	}
	return id, version, parts[3], nil
}

func parseChannelRoutingPolicyDraftID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, model.ErrRoutingPolicyDraftInvalid
	}
	return id, nil
}

func parseChannelRoutingPolicyDraftCursor(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || cursor <= 0 {
		return 0, model.ErrRoutingPolicyDraftInvalid
	}
	return cursor, nil
}

func parseChannelRoutingPolicyDraftLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > model.RoutingPolicyDraftMaxPageSize {
		return 0, model.ErrRoutingPolicyDraftInvalid
	}
	return limit, nil
}

func writeChannelRoutingPolicyDraftModelError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrRoutingPolicyDraftNotFound):
		writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "policy_draft_not_found", "channel routing policy draft not found", err)
	case errors.Is(err, model.ErrRoutingPolicyDraftConflict):
		var conflict *model.RoutingPolicyDraftConflictError
		if errors.As(err, &conflict) {
			actual := model.RoutingPolicyDraftSummary{ID: conflict.DraftID, Version: conflict.ActualVersion, ETag: conflict.ActualETag}
			c.Header("ETag", channelRoutingPolicyDraftETag(actual))
			c.JSON(http.StatusConflict, gin.H{
				"success": false, "code": "policy_draft_conflict", "message": "channel routing policy draft changed",
				"conflict": conflict,
			})
			return
		}
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_draft_conflict", "channel routing policy draft changed", err)
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
	case errors.Is(err, model.ErrRoutingPolicyDraftImmutable):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_draft_immutable", "published channel routing policy draft is immutable", err)
	case errors.Is(err, model.ErrRoutingPolicyReferenceInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "policy_reference_invalid", "channel routing policy references changed", err)
	case errors.Is(err, model.ErrRoutingPolicySimulationRiskAcceptanceRequired):
		writeChannelRoutingPolicyDraftError(c, http.StatusPreconditionFailed, "policy_simulation_risk_acceptance_required", "the latest matching simulation found a known failure; explicit risk acceptance is required", err)
	case errors.Is(err, model.ErrRoutingPolicySimulationRiskAcceptanceInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_simulation_risk_acceptance", "policy simulation risk acceptance is invalid or no longer matches the draft", err)
	case errors.Is(err, model.ErrRoutingOperationIdempotencyConflict):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", err)
	case errors.Is(err, model.ErrRoutingPolicyDraftInvalid), errors.Is(err, model.ErrRoutingPolicyInvalid),
		errors.Is(err, model.ErrRoutingPolicyPoolIdentity), errors.Is(err, model.ErrRoutingPolicyMemberIdentity):
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_policy_draft", "invalid channel routing policy draft", err)
	default:
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "policy_draft_failed", "channel routing policy draft operation failed", err)
	}
}

func writeChannelRoutingPolicyDraftError(c *gin.Context, status int, code string, message string, _ error) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}
