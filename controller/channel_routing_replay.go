package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxChannelRoutingSimulationBody = 16 << 10

type channelRoutingReplayView struct {
	DecisionID        string `json:"decision_id"`
	PoolID            int    `json:"pool_id"`
	SnapshotRevision  int64  `json:"snapshot_revision"`
	RuntimeGeneration int64  `json:"runtime_generation"`
	AlgorithmVersion  string `json:"algorithm_version"`
	ActualChannelID   int    `json:"actual_channel_id"`
	StoredChannelID   int    `json:"stored_channel_id"`
	ReplayedChannelID int    `json:"replayed_channel_id"`
	DifferenceType    string `json:"difference_type"`
	AuditVerified     bool   `json:"audit_verified"`
	GateVerified      bool   `json:"gate_verified"`
	Result            any    `json:"result"`
}

type channelRoutingHistoricalSimulationResponse struct {
	Operation channelRoutingOperationView `json:"operation"`
	Result    any                         `json:"result"`
}

func ReplayChannelRoutingDecision(c *gin.Context) {
	decisionID := strings.TrimSpace(c.Param("id"))
	if decisionID == "" || !utf8.ValidString(decisionID) || utf8.RuneCountInString(decisionID) > 64 {
		writeChannelRoutingReplayError(c, http.StatusBadRequest, "invalid_decision_id", "invalid channel routing decision id")
		return
	}

	var audit model.RoutingDecisionAudit
	if err := model.DB.WithContext(c.Request.Context()).Where("decision_id = ?", decisionID).First(&audit).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeChannelRoutingReplayError(c, http.StatusNotFound, "decision_not_found", "channel routing decision not found")
			return
		}
		writeChannelRoutingReplayError(c, http.StatusInternalServerError, "decision_load_failed", "failed to load channel routing decision")
		return
	}
	if !audit.Replayable {
		writeChannelRoutingReplayError(c, http.StatusUnprocessableEntity, "decision_not_replayable", "channel routing decision is not replayable")
		return
	}
	if audit.AlgorithmVersion != channelrouting.DecisionAlgorithmShadow &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmCanary &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmBalanced &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmShadowV1 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmShadowV2 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmCanaryV1 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmCanaryV2 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmBalancedV1 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmBalancedV2 {
		writeChannelRoutingReplayError(c, http.StatusUnprocessableEntity, "replay_algorithm_unsupported", "channel routing replay algorithm is not supported")
		return
	}

	if audit.AlgorithmVersion == channelrouting.DecisionAlgorithmBalanced ||
		audit.AlgorithmVersion == channelrouting.DecisionAlgorithmBalancedV1 ||
		audit.AlgorithmVersion == channelrouting.DecisionAlgorithmBalancedV2 {
		result, err := channelrouting.ReplayBalancedDecisionAudit(audit)
		if err != nil {
			if errors.Is(err, channelrouting.ErrBalancedReplayHash) || errors.Is(err, channelrouting.ErrBalancedReplayInvalid) {
				writeChannelRoutingReplayError(c, http.StatusConflict, "replay_integrity_failed", "channel routing decision audit failed integrity verification")
				return
			}
			writeChannelRoutingReplayError(c, http.StatusInternalServerError, "replay_failed", "failed to replay channel routing decision")
			return
		}
		differenceType := "active_unavailable"
		if result.SelectedChannelID > 0 {
			differenceType = "active_selected"
		}
		common.ApiSuccess(c, channelRoutingReplayView{
			DecisionID: audit.DecisionID, PoolID: audit.PoolID, SnapshotRevision: audit.SnapshotRevision,
			RuntimeGeneration: audit.RuntimeGeneration, AlgorithmVersion: audit.AlgorithmVersion,
			ActualChannelID: audit.ActualChannelID, StoredChannelID: audit.ObservedChannelID,
			ReplayedChannelID: result.SelectedChannelID, DifferenceType: differenceType,
			AuditVerified: true, GateVerified: true, Result: result,
		})
		return
	}

	result, err := channelrouting.ReplayDecisionAudit(audit)
	if err != nil {
		switch {
		case errors.Is(err, channelrouting.ErrShadowReplayAlgorithm):
			writeChannelRoutingReplayError(c, http.StatusUnprocessableEntity, "replay_algorithm_unsupported", "channel routing replay algorithm is not supported")
		case errors.Is(err, channelrouting.ErrShadowReplayHash),
			errors.Is(err, channelrouting.ErrShadowReplayAudit),
			errors.Is(err, channelrouting.ErrShadowReplayInvalid):
			writeChannelRoutingReplayError(c, http.StatusConflict, "replay_integrity_failed", "channel routing decision audit failed integrity verification")
		default:
			writeChannelRoutingReplayError(c, http.StatusInternalServerError, "replay_failed", "failed to replay channel routing decision")
		}
		return
	}

	common.ApiSuccess(c, channelRoutingReplayView{
		DecisionID:        audit.DecisionID,
		PoolID:            audit.PoolID,
		SnapshotRevision:  audit.SnapshotRevision,
		RuntimeGeneration: audit.RuntimeGeneration,
		AlgorithmVersion:  audit.AlgorithmVersion,
		ActualChannelID:   audit.ActualChannelID,
		StoredChannelID:   audit.ObservedChannelID,
		ReplayedChannelID: result.SelectedChannelID,
		DifferenceType:    channelrouting.ClassifyShadowDifference(audit.ActualChannelID, result),
		AuditVerified:     true,
		GateVerified: audit.AlgorithmVersion == channelrouting.DecisionAlgorithmCanary ||
			audit.AlgorithmVersion == channelrouting.DecisionAlgorithmCanaryV1 ||
			audit.AlgorithmVersion == channelrouting.DecisionAlgorithmCanaryV2,
		Result: result,
	})
}

func SimulateChannelRoutingGroup(c *gin.Context) {
	poolID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || poolID <= 0 {
		writeChannelRoutingReplayError(c, http.StatusBadRequest, "invalid_group_id", "invalid channel routing group id")
		return
	}
	request, err := decodeChannelRoutingSimulationRequest(c.Request.Body)
	if err != nil {
		writeChannelRoutingReplayError(c, http.StatusBadRequest, "invalid_simulation_request", "invalid channel routing simulation request")
		return
	}
	request.PoolID = poolID
	if err := channelrouting.ValidateHistoricalSimulationOptions(request); err != nil {
		writeChannelRoutingReplayError(c, http.StatusBadRequest, "invalid_simulation_request", "invalid channel routing simulation request")
		return
	}
	identity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypeHistoricalSimulation, struct {
		PoolID   int                                        `json:"pool_id"`
		Cursor   int                                        `json:"cursor"`
		Limit    int                                        `json:"limit"`
		Selector channelrouting.SimulationSelectorOverrides `json:"selector"`
	}{PoolID: poolID, Cursor: request.Cursor, Limit: request.Limit, Selector: request.Selector})
	if !ok {
		return
	}
	existing, lookupErr := model.GetRoutingOperationByRequestIdentityContext(c.Request.Context(), identity)
	if lookupErr == nil {
		if existing.OperationType != model.RoutingOperationTypeHistoricalSimulation {
			writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", model.ErrRoutingOperationIdempotencyConflict)
			return
		}
		view, viewErr := channelRoutingOperationViewFromModel(existing)
		if viewErr != nil {
			writeChannelRoutingPolicyControlError(c, viewErr)
			return
		}
		common.ApiSuccess(c, channelRoutingHistoricalSimulationResponse{Operation: view, Result: view.Result})
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
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	spec := model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeHistoricalSimulation, EvaluationHash: identity.PayloadHash,
		SubjectType: model.RoutingOperationSubjectRoutingPool, SubjectID: int64(poolID), PoolID: poolID,
		ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
		ActorID: common.GetContextKeyInt(c, constant.ContextKeyUserId), Reason: "historical routing simulation",
		RequestKeyHash: identity.KeyHash, RequestPayloadHash: identity.PayloadHash,
	}
	result, err := channelrouting.RunHistoricalSimulation(c.Request.Context(), request)
	if err != nil {
		message := common.SanitizeErrorMessage(err.Error())
		if message == "" {
			message = "historical simulation failed"
		}
		operation, _, persistErr := model.CreateFailedRoutingOperationContext(c.Request.Context(), spec, errors.New(message))
		if persistErr != nil {
			writeChannelRoutingPolicyControlError(c, persistErr)
			return
		}
		if errors.Is(err, channelrouting.ErrSimulationInvalidOptions) {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false, "code": "invalid_simulation_options", "message": "invalid channel routing simulation options",
				"operation": operation,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "simulation_failed", "message": "failed to simulate channel routing history",
			"operation": operation,
		})
		return
	}
	operation, _, err := model.CreateSucceededRoutingOperationContext(
		c.Request.Context(), spec, model.RoutingOperationResult{}, result,
	)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypePolicySimulation, head.CurrentRevision, gin.H{
		"operation_id": operation.ID, "pool_id": poolID, "evaluated_samples": result.EvaluatedSamples,
	})
	common.ApiSuccess(c, channelRoutingHistoricalSimulationResponse{Operation: view, Result: result})
}

func decodeChannelRoutingSimulationRequest(body io.Reader) (channelrouting.HistoricalSimulationOptions, error) {
	request := channelrouting.HistoricalSimulationOptions{Limit: channelrouting.DefaultSimulationLimit}
	data, err := io.ReadAll(io.LimitReader(body, maxChannelRoutingSimulationBody+1))
	if err != nil || len(data) == 0 || len(data) > maxChannelRoutingSimulationBody {
		return request, channelrouting.ErrSimulationInvalidOptions
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil || fields == nil {
		return request, channelrouting.ErrSimulationInvalidOptions
	}
	for key := range fields {
		if key != "cursor" && key != "limit" && key != "selector" {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
	}
	if raw, exists := fields["cursor"]; exists {
		if isNullChannelRoutingJSON(raw) || common.Unmarshal(raw, &request.Cursor) != nil || request.Cursor < 0 {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
	}
	if raw, exists := fields["limit"]; exists {
		if isNullChannelRoutingJSON(raw) || common.Unmarshal(raw, &request.Limit) != nil ||
			request.Limit < 1 || request.Limit > channelrouting.MaxSimulationLimit {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
	}
	if raw, exists := fields["selector"]; exists {
		if isNullChannelRoutingJSON(raw) {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
		var selectorFields map[string]json.RawMessage
		if err := common.Unmarshal(raw, &selectorFields); err != nil || selectorFields == nil {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
		for key := range selectorFields {
			switch key {
			case "weight_availability", "weight_latency", "weight_throughput", "weight_cost",
				"availability_floor", "min_volume", "top_k", "max_ejected_pct", "half_open_probes",
				"snapshot_stale_sec":
			default:
				return request, channelrouting.ErrSimulationInvalidOptions
			}
			if isNullChannelRoutingJSON(selectorFields[key]) {
				return request, channelrouting.ErrSimulationInvalidOptions
			}
		}
		if err := common.Unmarshal(raw, &request.Selector); err != nil {
			return request, channelrouting.ErrSimulationInvalidOptions
		}
	}
	return request, nil
}

func isNullChannelRoutingJSON(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func writeChannelRoutingReplayError(c *gin.Context, status int, code string, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": message,
	})
}
