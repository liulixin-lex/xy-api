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
	if audit.AlgorithmVersion != channelrouting.DecisionAlgorithmShadowV1 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmCanaryV1 &&
		audit.AlgorithmVersion != channelrouting.DecisionAlgorithmBalancedV1 {
		writeChannelRoutingReplayError(c, http.StatusUnprocessableEntity, "replay_algorithm_unsupported", "channel routing replay algorithm is not supported")
		return
	}

	if audit.AlgorithmVersion == channelrouting.DecisionAlgorithmBalancedV1 {
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
		GateVerified:      audit.AlgorithmVersion == channelrouting.DecisionAlgorithmCanaryV1,
		Result:            result,
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
	result, err := channelrouting.RunHistoricalSimulation(c.Request.Context(), request)
	if err != nil {
		if errors.Is(err, channelrouting.ErrSimulationInvalidOptions) {
			writeChannelRoutingReplayError(c, http.StatusBadRequest, "invalid_simulation_options", "invalid channel routing simulation options")
			return
		}
		writeChannelRoutingReplayError(c, http.StatusInternalServerError, "simulation_failed", "failed to simulate channel routing history")
		return
	}
	common.ApiSuccess(c, result)
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
