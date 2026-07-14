package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	billingProjectionOpsDefaultPageSize     = 50
	billingProjectionOpsMaxPageSize         = 200
	billingProjectionOpsIdempotencyMaxBytes = 200
	billingProjectionOpsSafeErrorMaxBytes   = 512
)

type billingProjectionOutcomeView struct {
	User       string `json:"user,omitempty"`
	Channel    string `json:"channel,omitempty"`
	DataExport string `json:"data_export,omitempty"`
	Log        string `json:"log,omitempty"`
}

type failedBillingProjectionView struct {
	ID               int64                        `json:"id"`
	Kind             string                       `json:"kind"`
	ReferenceID      int64                        `json:"reference_id"`
	UserID           int                          `json:"user_id,omitempty"`
	ChannelID        int                          `json:"channel_id,omitempty"`
	OperationKeyHash string                       `json:"operation_key_hash"`
	State            string                       `json:"state"`
	Disposition      string                       `json:"disposition,omitempty"`
	FailureCode      string                       `json:"failure_code"`
	Error            string                       `json:"error,omitempty"`
	Attempts         int                          `json:"attempts"`
	CreatedTimeMs    int64                        `json:"created_time_ms"`
	UpdatedTimeMs    int64                        `json:"updated_time_ms"`
	CompletedTimeMs  int64                        `json:"completed_time_ms"`
	Outcome          billingProjectionOutcomeView `json:"outcome"`
	Requeueable      bool                         `json:"requeueable"`
	ETag             string                       `json:"etag"`
}

type billingLogSinkConflictView struct {
	ID               int64  `json:"id"`
	ProjectionID     int64  `json:"projection_id"`
	OperationKeyHash string `json:"operation_key_hash"`
	State            string `json:"state"`
	Version          int64  `json:"version"`
	DistinctReceipts int64  `json:"distinct_receipts"`
	PhysicalRows     int64  `json:"physical_rows"`
	FirstDetectedMs  int64  `json:"first_detected_ms"`
	LastDetectedMs   int64  `json:"last_detected_ms"`
	ETag             string `json:"etag"`
}

func ListFailedBillingStatsProjections(c *gin.Context) {
	cursor, limit, ok := parseBillingProjectionPage(c)
	if !ok {
		return
	}
	projections, err := model.FindFailedBillingStatsProjections(c.Request.Context(), cursor, limit+1)
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	items, hasMore := trimBillingStatsProjectionPage(projections, limit)
	views := make([]failedBillingProjectionView, 0, len(items))
	for index := range items {
		projection := items[index]
		views = append(views, failedBillingProjectionView{
			ID: projection.ID, Kind: projection.Kind, ReferenceID: projection.ReferenceID,
			UserID: projection.UserID, ChannelID: projection.ChannelID,
			OperationKeyHash: billingProjectionOperationKeyHash(projection.OperationKey),
			State:            projection.State, FailureCode: projection.FailureCode,
			Error: billingProjectionSafeError(projection.LastError), Attempts: projection.Attempts,
			CreatedTimeMs: projection.CreatedTimeMs, UpdatedTimeMs: projection.UpdatedTimeMs,
			CompletedTimeMs: projection.CompletedTimeMs,
			Outcome: billingProjectionOutcomeView{
				User: projection.UserOutcome, Channel: projection.ChannelOutcome,
				DataExport: projection.DataExportOutcome,
			},
			Requeueable: projection.FailureCode != "",
			ETag: billingFailedProjectionETag(
				"stats", projection.ID, projection.UpdatedTimeMs, projection.FailureCode,
			),
		})
	}
	count, err := model.CountFailedBillingStatsProjections(c.Request.Context())
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	writeBillingProjectionPage(c, views, count, hasMore)
}

func ListFailedBillingLogProjections(c *gin.Context) {
	cursor, limit, ok := parseBillingProjectionPage(c)
	if !ok {
		return
	}
	projections, err := model.FindFailedBillingLogProjections(c.Request.Context(), cursor, limit+1)
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	items, hasMore := trimBillingLogProjectionPage(projections, limit)
	views := make([]failedBillingProjectionView, 0, len(items))
	for index := range items {
		projection := items[index]
		requeueable := projection.Disposition == model.BillingLogProjectionDispositionPending &&
			projection.FailureCode != model.BillingLogProjectionFailureInvalidPayload &&
			projection.FailureCode != model.BillingLogProjectionFailureSinkReceiptConflict &&
			projection.FailureCode != model.BillingLogProjectionFailureSinkReceiptConflictLate
		views = append(views, failedBillingProjectionView{
			ID: projection.ID, Kind: projection.Kind, ReferenceID: projection.ReferenceID,
			OperationKeyHash: billingProjectionOperationKeyHash(projection.OperationKey),
			State:            projection.State, Disposition: projection.Disposition,
			FailureCode: projection.FailureCode,
			Error:       billingProjectionSafeError(projection.LastError), Attempts: projection.Attempts,
			CreatedTimeMs: projection.CreatedTimeMs, UpdatedTimeMs: projection.UpdatedTimeMs,
			CompletedTimeMs: projection.CompletedTimeMs,
			Outcome:         billingProjectionOutcomeView{Log: projection.Outcome},
			Requeueable:     requeueable,
			ETag: billingFailedProjectionETag(
				"log", projection.ID, projection.UpdatedTimeMs, projection.FailureCode,
			),
		})
	}
	count, err := model.CountFailedBillingLogProjections(c.Request.Context())
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	writeBillingProjectionPage(c, views, count, hasMore)
}

func ListOpenBillingLogSinkConflicts(c *gin.Context) {
	cursor, limit, ok := parseBillingProjectionPage(c)
	if !ok {
		return
	}
	conflicts, err := model.FindOpenBillingLogSinkConflicts(c.Request.Context(), cursor, limit+1)
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	hasMore := len(conflicts) > limit
	if hasMore {
		conflicts = conflicts[:limit]
	}
	views := make([]billingLogSinkConflictView, 0, len(conflicts))
	for index := range conflicts {
		conflict := conflicts[index]
		views = append(views, billingLogSinkConflictView{
			ID: conflict.ID, ProjectionID: conflict.ProjectionID,
			OperationKeyHash: billingProjectionOperationKeyHash(conflict.OperationKey),
			State:            conflict.State, Version: conflict.Version,
			DistinctReceipts: conflict.DistinctReceipts, PhysicalRows: conflict.PhysicalRows,
			FirstDetectedMs: conflict.FirstDetectedMs, LastDetectedMs: conflict.LastDetectedMs,
			ETag: model.BillingLogSinkConflictETag(conflict.ID, conflict.Version),
		})
	}
	count, err := model.CountOpenBillingLogSinkConflicts(c.Request.Context())
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	writeBillingProjectionPage(c, views, count, hasMore)
}

type billingProjectionRequeueRequest struct {
	ExpectedFailureCode string `json:"expected_failure_code"`
}

func RequeueFailedBillingStatsProjection(c *gin.Context) {
	requeueFailedBillingProjection(c, "stats")
}

func RequeueFailedBillingLogProjection(c *gin.Context) {
	requeueFailedBillingProjection(c, "log")
}

func requeueFailedBillingProjection(c *gin.Context, projectionType string) {
	projectionID, ok := parseBillingProjectionTargetID(c)
	if !ok {
		return
	}
	var request billingProjectionRequeueRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_request", "invalid requeue request", err)
		return
	}
	request.ExpectedFailureCode = strings.TrimSpace(request.ExpectedFailureCode)
	if request.ExpectedFailureCode == "" || len(request.ExpectedFailureCode) > 64 ||
		!utf8.ValidString(request.ExpectedFailureCode) || strings.ContainsAny(request.ExpectedFailureCode, "\r\n\x00") {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_failure_code", "expected_failure_code is invalid", nil)
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeBillingProjectionOpsHTTPError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", nil)
		return
	}
	expectedRevision, err := parseBillingFailedProjectionETag(
		ifMatch, projectionType, projectionID, request.ExpectedFailureCode,
	)
	if err != nil {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_if_match", "If-Match is invalid", err)
		return
	}
	actorUserID := c.GetInt("id")
	keyHash, requestHash, err := bindBillingProjectionAdminIdentity(c, actorUserID, struct {
		Action              string `json:"action"`
		ProjectionID        int64  `json:"projection_id"`
		ExpectedRevision    int64  `json:"expected_revision"`
		ExpectedFailureCode string `json:"expected_failure_code"`
	}{projectionType + "_requeue", projectionID, expectedRevision, request.ExpectedFailureCode})
	if err != nil {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_idempotency_key", err.Error(), err)
		return
	}
	spec := model.BillingProjectionAdminOperationSpec{
		TargetID: projectionID, ActorUserID: actorUserID,
		ExpectedRevision: expectedRevision, ExpectedFailureCode: request.ExpectedFailureCode,
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
	}
	var result model.BillingProjectionAdminOperationResult
	if projectionType == "stats" {
		result, err = model.RequeueFailedBillingStatsProjectionAdmin(c.Request.Context(), spec, time.Now())
	} else {
		result, err = model.RequeueFailedBillingLogProjectionAdmin(c.Request.Context(), spec, time.Now())
	}
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	writeBillingProjectionOperationResult(c, result)
}

type billingLogSinkConflictResolutionRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

func ResolveAndRequeueBillingLogSinkConflict(c *gin.Context) {
	conflictID, ok := parseBillingProjectionTargetID(c)
	if !ok {
		return
	}
	var request billingLogSinkConflictResolutionRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_request", "invalid conflict resolution request", err)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.ExpectedVersion <= 0 || request.Reason == "" || len(request.Reason) > 1024 ||
		!utf8.ValidString(request.Reason) || strings.ContainsAny(request.Reason, "\r\n\x00") {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_resolution", "expected_version and reason are required", nil)
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeBillingProjectionOpsHTTPError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", nil)
		return
	}
	if ifMatch != model.BillingLogSinkConflictETag(conflictID, request.ExpectedVersion) {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_if_match", "If-Match is invalid", nil)
		return
	}
	actorUserID := c.GetInt("id")
	keyHash, requestHash, err := bindBillingProjectionAdminIdentity(c, actorUserID, struct {
		Action          string `json:"action"`
		ConflictID      int64  `json:"conflict_id"`
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}{"conflict_resolve", conflictID, request.ExpectedVersion, request.Reason})
	if err != nil {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_idempotency_key", err.Error(), err)
		return
	}
	result, err := model.ResolveAndRequeueBillingLogSinkConflictAdmin(
		c.Request.Context(), model.BillingProjectionAdminOperationSpec{
			TargetID: conflictID, ActorUserID: actorUserID, ExpectedRevision: request.ExpectedVersion,
			Reason: request.Reason, IdempotencyKeyHash: keyHash, RequestHash: requestHash,
		}, time.Now(),
	)
	if err != nil {
		writeBillingProjectionOpsError(c, err)
		return
	}
	writeBillingProjectionOperationResult(c, result)
}

func parseBillingProjectionPage(c *gin.Context) (int64, int, bool) {
	limit := billingProjectionOpsDefaultPageSize
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > billingProjectionOpsMaxPageSize {
			writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200", err)
			return 0, 0, false
		}
		limit = parsed
	}
	cursor := int64(0)
	if raw := c.Query("cursor"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_cursor", "cursor must be a non-negative integer", err)
			return 0, 0, false
		}
		cursor = parsed
	}
	return cursor, limit, true
}

func trimBillingStatsProjectionPage(
	items []model.BillingStatsProjection,
	limit int,
) ([]model.BillingStatsProjection, bool) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore
}

func trimBillingLogProjectionPage(
	items []model.BillingLogProjection,
	limit int,
) ([]model.BillingLogProjection, bool) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore
}

func writeBillingProjectionPage(c *gin.Context, items any, count int64, hasMore bool) {
	nextCursor := int64(0)
	if hasMore {
		switch typed := items.(type) {
		case []failedBillingProjectionView:
			nextCursor = typed[len(typed)-1].ID
		case []billingLogSinkConflictView:
			nextCursor = typed[len(typed)-1].ID
		}
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, gin.H{
		"items": items, "count": count, "has_more": hasMore, "next_cursor": nextCursor,
	})
}

func billingProjectionOperationKeyHash(operationKey string) string {
	if operationKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(operationKey))
	return hex.EncodeToString(digest[:])
}

func billingProjectionSafeError(message string) string {
	message = common.SanitizeErrorMessage(message)
	if len(message) <= billingProjectionOpsSafeErrorMaxBytes {
		return message
	}
	end := billingProjectionOpsSafeErrorMaxBytes
	for end > 0 && !utf8.ValidString(message[:end]) {
		end--
	}
	return strings.TrimSpace(message[:end])
}

func billingFailedProjectionETag(projectionType string, id, updatedTimeMs int64, failureCode string) string {
	digest := sha256.Sum256([]byte(failureCode))
	return fmt.Sprintf("\"billing-%s-projection.%d.%d.%s\"",
		projectionType, id, updatedTimeMs, hex.EncodeToString(digest[:]))
}

func parseBillingFailedProjectionETag(
	value string,
	projectionType string,
	id int64,
	failureCode string,
) (int64, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("strong ETag is required")
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) != 4 || parts[0] != "billing-"+projectionType+"-projection" {
		return 0, errors.New("projection ETag type does not match")
	}
	parsedID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || parsedID != id {
		return 0, errors.New("projection ETag target does not match")
	}
	updatedTimeMs, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || updatedTimeMs <= 0 {
		return 0, errors.New("projection ETag revision is invalid")
	}
	digest := sha256.Sum256([]byte(failureCode))
	if parts[3] != hex.EncodeToString(digest[:]) {
		return 0, errors.New("projection ETag failure code does not match")
	}
	return updatedTimeMs, nil
}

func bindBillingProjectionAdminIdentity(c *gin.Context, actorUserID int, request any) (string, string, error) {
	rawKey := c.GetHeader("Idempotency-Key")
	key := strings.TrimSpace(rawKey)
	valid := actorUserID > 0 && key == rawKey && len(key) >= 8 && len(key) <= billingProjectionOpsIdempotencyMaxBytes
	for index := 0; valid && index < len(key); index++ {
		valid = key[index] >= 0x21 && key[index] <= 0x7e
	}
	if !valid {
		return "", "", errors.New("Idempotency-Key must contain 8 to 200 printable ASCII characters")
	}
	canonical, err := common.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
		ActorUserID   int `json:"actor_user_id"`
		Request       any `json:"request"`
	}{SchemaVersion: 1, ActorUserID: actorUserID, Request: request})
	if err != nil {
		return "", "", err
	}
	keyDigest := sha256.Sum256([]byte("billing-projection-admin:v1\x00" + strconv.Itoa(actorUserID) + "\x00" + key))
	requestDigest := sha256.Sum256(canonical)
	c.Header("Idempotency-Key", key)
	return hex.EncodeToString(keyDigest[:]), hex.EncodeToString(requestDigest[:]), nil
}

func parseBillingProjectionTargetID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_target_id", "target id is invalid", err)
		return 0, false
	}
	return id, true
}

func writeBillingProjectionOperationResult(c *gin.Context, result model.BillingProjectionAdminOperationResult) {
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, gin.H{
		"operation_id":      result.Operation.ID,
		"action":            result.Operation.Action,
		"target_id":         result.Operation.TargetID,
		"state":             result.Operation.State,
		"outcome":           result.Operation.Outcome,
		"completed_time_ms": result.Operation.CompletedTimeMs,
		"replayed":          result.Replayed,
	})
}

func writeBillingProjectionOpsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrBillingProjectionAdminInvalid):
		writeBillingProjectionOpsHTTPError(c, http.StatusBadRequest, "invalid_operation", "billing projection operation is invalid", err)
	case errors.Is(err, model.ErrBillingProjectionAdminIdempotencyConflict):
		writeBillingProjectionOpsHTTPError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", err)
	case errors.Is(err, model.ErrBillingProjectionAdminPrecondition):
		writeBillingProjectionOpsHTTPError(c, http.StatusPreconditionFailed, "precondition_failed", "billing projection state changed; refresh before retrying", err)
	case errors.Is(err, model.ErrBillingProjectionAdminNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		writeBillingProjectionOpsHTTPError(c, http.StatusNotFound, "target_not_found", "billing projection target was not found", err)
	case errors.Is(err, model.ErrBillingProjectionAdminNotRequeueable):
		writeBillingProjectionOpsHTTPError(c, http.StatusUnprocessableEntity, "not_requeueable", "billing projection requires a different remediation workflow", err)
	default:
		writeBillingProjectionOpsHTTPError(c, http.StatusInternalServerError, "operation_failed", "billing projection operation failed", err)
	}
}

func writeBillingProjectionOpsHTTPError(c *gin.Context, status int, code string, message string, err error) {
	if err != nil {
		common.SysError("billing projection operations: " + common.SanitizeErrorMessage(err.Error()))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}
