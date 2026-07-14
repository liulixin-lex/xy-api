package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const asyncBillingDecisionIdempotencyKeyMaxBytes = 200

func ListAsyncBillingManualReviews(c *gin.Context) {
	limit, limitErr := strconv.Atoi(c.Query("limit"))
	if c.Query("limit") == "" {
		limit = 50
	} else if limitErr != nil || limit <= 0 || limit > 200 {
		writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200", nil)
		return
	}
	cursor := int64(0)
	if rawCursor := c.Query("cursor"); rawCursor != "" {
		parsed, err := strconv.ParseInt(rawCursor, 10, 64)
		if err != nil || parsed < 0 {
			writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_cursor", "cursor must be a non-negative integer", err)
			return
		}
		cursor = parsed
	}
	actorUserID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if actorUserID <= 0 {
		actorUserID = c.GetInt("id")
	}
	canResolve, err := authz.CanCurrent(c.Request.Context(), actorUserID, c.GetInt("role"), authz.BillingReviewResolve)
	if err != nil {
		writeAsyncBillingReviewError(c, http.StatusInternalServerError, "authorization_check_failed", "failed to evaluate billing review permissions", err)
		return
	}
	page, err := model.ListAsyncBillingManualReviewPage(c.Request.Context(), cursor, limit, canResolve)
	if err != nil {
		writeAsyncBillingReviewModelError(c, err)
		return
	}
	count, oldestMs, err := model.AsyncBillingManualReviewStats()
	if err != nil {
		writeAsyncBillingReviewModelError(c, err)
		return
	}
	oldestAgeSeconds := int64(0)
	if oldestMs > 0 && time.Now().UnixMilli() > oldestMs {
		oldestAgeSeconds = (time.Now().UnixMilli() - oldestMs) / 1000
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, gin.H{
		"pending_count": count, "oldest_age_seconds": oldestAgeSeconds,
		"items": page.Items, "next_cursor": page.NextCursor, "has_more": page.HasMore,
		"capabilities": gin.H{"can_resolve": canResolve},
	})
}

type asyncBillingManualResolutionRequest struct {
	Action            string `json:"action"`
	ExpectedVersion   int64  `json:"expected_version"`
	UpstreamTaskID    string `json:"upstream_task_id"`
	ProviderStatus    string `json:"provider_status"`
	ProviderCheckedMs int64  `json:"provider_checked_ms"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}

type asyncBillingManualResolutionResponse struct {
	ReservationID int64  `json:"reservation_id"`
	State         string `json:"state"`
	ReviewVersion int64  `json:"review_version"`
	ETag          string `json:"etag"`
	CurrentQuota  int    `json:"current_quota"`
	Resolution    struct {
		ID             int64  `json:"id"`
		Action         string `json:"action"`
		ReviewKind     string `json:"review_kind"`
		BeforeState    string `json:"before_state"`
		AfterState     string `json:"after_state"`
		BeforeQuota    int    `json:"before_quota"`
		AfterQuota     int    `json:"after_quota"`
		QuotaDelta     int    `json:"quota_delta"`
		ResolvedTimeMs int64  `json:"resolved_time_ms"`
	} `json:"resolution"`
}

func ResolveAsyncBillingManualReview(c *gin.Context) {
	reservationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || reservationID <= 0 {
		writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_reservation_id", "invalid reservation id", err)
		return
	}
	var request asyncBillingManualResolutionRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_resolution_request", "invalid resolution request", err)
		return
	}
	request.Action = strings.TrimSpace(request.Action)
	expectedETag := strings.TrimSpace(c.GetHeader("If-Match"))
	if expectedETag == "" {
		writeAsyncBillingReviewError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", nil)
		return
	}
	actorUserID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if actorUserID <= 0 {
		actorUserID = c.GetInt("id")
	}
	keyHash, payloadHash, identityErr := bindAsyncBillingDecisionIdentity(
		c, reservationID, actorUserID, expectedETag, request,
	)
	if identityErr != nil {
		writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_idempotency_key", identityErr.Error(), identityErr)
		return
	}
	result, err := model.ResolveAsyncBillingManualReview(c.Request.Context(), model.AsyncBillingManualDecisionSpec{
		ReservationID: reservationID, Action: request.Action, ActorUserID: actorUserID,
		ExpectedVersion: request.ExpectedVersion, ExpectedETag: expectedETag,
		UpstreamTaskID: request.UpstreamTaskID, ProviderStatus: request.ProviderStatus,
		ProviderCheckedMs: request.ProviderCheckedMs, EvidenceReference: request.EvidenceReference,
		Reason: request.Reason, DecisionKeyHash: keyHash, DecisionPayloadHash: payloadHash,
	}, time.Now())
	if err != nil {
		writeAsyncBillingReviewModelError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response := asyncBillingManualResolutionResponse{
		ReservationID: result.Reservation.ID,
		State:         result.Reservation.State,
		ReviewVersion: result.Reservation.ReviewVersion,
		ETag:          model.AsyncBillingManualReviewETag(result.Reservation.ID, result.Reservation.ReviewVersion),
		CurrentQuota:  result.Reservation.CurrentQuota,
	}
	response.Resolution.ID = result.Resolution.ID
	response.Resolution.Action = result.Resolution.Action
	response.Resolution.ReviewKind = result.Resolution.ReviewKind
	response.Resolution.BeforeState = result.Resolution.BeforeState
	response.Resolution.AfterState = result.Resolution.AfterState
	response.Resolution.BeforeQuota = result.Resolution.BeforeQuota
	response.Resolution.AfterQuota = result.Resolution.AfterQuota
	response.Resolution.QuotaDelta = result.Resolution.QuotaDelta
	response.Resolution.ResolvedTimeMs = result.Resolution.ResolvedTimeMs
	common.ApiSuccess(c, response)
}

func bindAsyncBillingDecisionIdentity(
	c *gin.Context,
	reservationID int64,
	actorUserID int,
	expectedETag string,
	request asyncBillingManualResolutionRequest,
) (string, string, error) {
	primaryKey := c.GetHeader("Idempotency-Key")
	legacyKey := c.GetHeader("X-Idempotency-Key")
	if primaryKey != "" && legacyKey != "" && primaryKey != legacyKey {
		return "", "", errors.New("Idempotency-Key and X-Idempotency-Key must match")
	}
	rawKey := primaryKey
	if rawKey == "" {
		rawKey = legacyKey
	}
	key := strings.TrimSpace(rawKey)
	valid := key == rawKey && len(key) >= 8 && len(key) <= asyncBillingDecisionIdempotencyKeyMaxBytes
	for index := 0; valid && index < len(key); index++ {
		valid = key[index] >= 0x21 && key[index] <= 0x7e
	}
	if !valid {
		return "", "", errors.New("Idempotency-Key must contain 8 to 200 printable ASCII characters")
	}
	canonical, err := common.Marshal(struct {
		SchemaVersion int                                 `json:"schema_version"`
		ReservationID int64                               `json:"reservation_id"`
		ActorUserID   int                                 `json:"actor_user_id"`
		ExpectedETag  string                              `json:"expected_etag"`
		Request       asyncBillingManualResolutionRequest `json:"request"`
	}{
		SchemaVersion: 1, ReservationID: reservationID, ActorUserID: actorUserID,
		ExpectedETag: expectedETag, Request: request,
	})
	if err != nil {
		return "", "", err
	}
	keyDigest := sha256.Sum256([]byte("async-billing-decision:v1\x00" + strconv.Itoa(actorUserID) +
		"\x00" + strconv.FormatInt(reservationID, 10) + "\x00" + key))
	payloadDigest := sha256.Sum256(canonical)
	c.Header("Idempotency-Key", key)
	return hex.EncodeToString(keyDigest[:]), hex.EncodeToString(payloadDigest[:]), nil
}

func writeAsyncBillingReviewModelError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrAsyncBillingManualDecisionInvalid):
		writeAsyncBillingReviewError(c, http.StatusBadRequest, "invalid_manual_decision", "invalid billing review decision", err)
	case errors.Is(err, model.ErrAsyncBillingManualDecisionPrecondition):
		writeAsyncBillingReviewError(c, http.StatusPreconditionFailed, "review_precondition_failed", "billing review changed; refresh before deciding", err)
	case errors.Is(err, model.ErrAsyncBillingManualDecisionConflict),
		errors.Is(err, model.ErrAsyncBillingIdempotencyConflict):
		writeAsyncBillingReviewError(c, http.StatusConflict, "decision_conflict", "a different decision is already recorded", err)
	case errors.Is(err, model.ErrAsyncBillingManualDecisionBlocked):
		writeAsyncBillingReviewError(c, http.StatusUnprocessableEntity, "decision_blocked", "billing review evidence is incomplete", err)
	case errors.Is(err, model.ErrAsyncBillingInsufficientQuota),
		errors.Is(err, model.ErrAsyncBillingSubscriptionExhausted):
		writeAsyncBillingReviewError(c, http.StatusConflict, "insufficient_quota", "additional quota is unavailable", err)
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeAsyncBillingReviewError(c, http.StatusNotFound, "review_not_found", "billing review was not found", err)
	default:
		writeAsyncBillingReviewError(c, http.StatusInternalServerError, "billing_review_failed", "billing review operation failed", err)
	}
}

func writeAsyncBillingReviewError(c *gin.Context, status int, code string, message string, err error) {
	if err != nil {
		common.SysError("async billing review error: " + common.SanitizeErrorMessage(err.Error()))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}
