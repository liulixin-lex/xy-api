package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const channelRoutingControlBodyMaxBytes = 4 << 10

type channelRoutingAuditExportResponse struct {
	Export    model.RoutingAuditExport    `json:"export"`
	Operation channelRoutingOperationView `json:"operation"`
}

func RunChannelRoutingActiveProbe(c *gin.Context) {
	if err := decodeChannelRoutingEmptyControlBody(c.Request.Body); err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_active_probe", "invalid channel routing active probe request", err)
		return
	}
	identity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypeActiveProbe, struct{}{})
	if !ok || writeExistingChannelRoutingControlOperation(c, identity, model.RoutingOperationTypeActiveProbe) {
		return
	}
	head, err := model.GetRoutingPolicyHeadContext(c.Request.Context())
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	actorID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	spec := model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeActiveProbe, EvaluationHash: identity.PayloadHash,
		SubjectType:      model.RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
		ActorID: actorID, Reason: "manual routing active probe",
		RequestKeyHash: identity.KeyHash, RequestPayloadHash: identity.PayloadHash,
	}
	operation, _, err := model.CreateRoutingOperationContext(c.Request.Context(), spec)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	status := http.StatusOK
	if operation.Status == model.RoutingOperationStatusPending || operation.Status == model.RoutingOperationStatusRunning ||
		operation.Status == model.RoutingOperationStatusRetryWait {
		status = http.StatusAccepted
	}
	c.JSON(status, gin.H{"success": true, "message": "", "data": view})
}

func CreateChannelRoutingAuditExport(c *gin.Context) {
	request, err := decodeChannelRoutingAuditExportRequest(c.Request.Body)
	if err != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_audit_export", "invalid channel routing audit export request", err)
		return
	}
	identity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypeAuditExport, request)
	if !ok {
		return
	}
	export, operation, created, err := model.CreateRoutingAuditExportContext(
		c.Request.Context(), request, common.GetContextKeyInt(c, constant.ContextKeyUserId), identity,
	)
	if err != nil {
		status := http.StatusInternalServerError
		code := "audit_export_failed"
		message := "channel routing audit export failed"
		if errors.Is(err, model.ErrRoutingAuditExportInvalid) {
			status = http.StatusBadRequest
			code = "invalid_audit_export"
			message = "invalid channel routing audit export request"
		} else if errors.Is(err, model.ErrRoutingAuditExportTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "audit_export_too_large"
			message = "channel routing audit export exceeds the size limit"
		} else if errors.Is(err, model.ErrRoutingOperationIdempotencyConflict) {
			status = http.StatusConflict
			code = "idempotency_key_conflict"
			message = "Idempotency-Key was already used for a different request"
		}
		writeChannelRoutingPolicyDraftError(c, status, code, message, err)
		return
	}
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	if created {
		publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeAuditExportReady, operation.ExpectedRevision, gin.H{
			"operation_id": operation.ID, "export_id": export.ExportID, "record_count": export.RecordCount,
		})
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"success": true, "message": "", "data": channelRoutingAuditExportResponse{
		Export: export, Operation: view,
	}})
}

func DownloadChannelRoutingAuditExport(c *gin.Context) {
	exportID, parseErr := parseChannelRoutingAuditExportID(c.Param("id"))
	if parseErr != nil {
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_audit_export_id", "invalid channel routing audit export id", parseErr)
		return
	}
	export, payload, err := model.GetRoutingAuditExportContext(c.Request.Context(), exportID)
	if err != nil {
		if model.IsRoutingAuditExportNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "audit_export_not_found", "message": "channel routing audit export not found"})
			return
		}
		writeChannelRoutingPolicyControlError(c, err)
		return
	}
	if time.Now().UnixMilli() > export.ExpiresTimeMs {
		c.JSON(http.StatusGone, gin.H{"success": false, "code": "audit_export_expired", "message": "channel routing audit export expired"})
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"channel-routing-audit-%s.json\"", export.ExportID))
	c.Header("ETag", "\""+export.ContentHash+"\"")
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func writeExistingChannelRoutingControlOperation(
	c *gin.Context,
	identity model.RoutingOperationRequestIdentity,
	expectedType string,
) bool {
	operation, err := model.GetRoutingOperationByRequestIdentityContext(c.Request.Context(), identity)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	if errors.Is(err, model.ErrRoutingOperationIdempotencyConflict) {
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", err)
		return true
	}
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return true
	}
	if operation.OperationType != expectedType {
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", model.ErrRoutingOperationIdempotencyConflict)
		return true
	}
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingPolicyControlError(c, err)
		return true
	}
	status := http.StatusOK
	if operation.Status == model.RoutingOperationStatusPending || operation.Status == model.RoutingOperationStatusRunning ||
		operation.Status == model.RoutingOperationStatusRetryWait {
		status = http.StatusAccepted
	}
	c.JSON(status, gin.H{"success": true, "message": "", "data": view})
	return true
}

func decodeChannelRoutingEmptyControlBody(body io.Reader) error {
	if body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingControlBodyMaxBytes+1))
	if err != nil || len(data) > channelRoutingControlBodyMaxBytes {
		return model.ErrRoutingOperationInvalid
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil || len(fields) != 0 {
		return model.ErrRoutingOperationInvalid
	}
	return nil
}

func decodeChannelRoutingAuditExportRequest(body io.Reader) (model.RoutingAuditExportRequest, error) {
	if body == nil {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingControlBodyMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > channelRoutingControlBodyMaxBytes {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	for key := range fields {
		if key != "from_time" && key != "to_time" && key != "limit" {
			return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
		}
	}
	request := model.RoutingAuditExportRequest{Limit: 1_000}
	from, fromOK := fields["from_time"]
	to, toOK := fields["to_time"]
	if !fromOK || !toOK || common.Unmarshal(from, &request.FromTime) != nil || common.Unmarshal(to, &request.ToTime) != nil {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	if limit, exists := fields["limit"]; exists && common.Unmarshal(limit, &request.Limit) != nil {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	if request.FromTime <= 0 || request.ToTime < request.FromTime ||
		request.ToTime-request.FromTime > model.RoutingAuditExportMaxRangeSeconds ||
		request.Limit < 1 || request.Limit > model.RoutingAuditExportMaxRecords {
		return model.RoutingAuditExportRequest{}, model.ErrRoutingAuditExportInvalid
	}
	return request, nil
}

func parseChannelRoutingAuditExportID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 36 || !strings.HasPrefix(value, "rae_") {
		return "", model.ErrRoutingAuditExportInvalid
	}
	for _, char := range value[4:] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return "", model.ErrRoutingAuditExportInvalid
		}
	}
	return value, nil
}
