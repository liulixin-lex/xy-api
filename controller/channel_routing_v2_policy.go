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

	"github.com/gin-gonic/gin"
)

const maxChannelRoutingPolicyDraftBody = model.RoutingPolicyDraftMaxDocumentBytes + 64<<10

var errChannelRoutingPolicyDraftBodyTooLarge = errors.New("channel routing policy draft body is too large")

type channelRoutingPolicyDraftDetail struct {
	model.RoutingPolicyDraftSummary
	Document model.RoutingPolicyDocument `json:"document"`
}

type channelRoutingPolicyDraftList struct {
	Items      []model.RoutingPolicyDraftSummary `json:"items"`
	NextCursor int64                             `json:"next_cursor"`
	HasMore    bool                              `json:"has_more"`
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
	summary := draft.Summary()
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
	summary := draft.Summary()
	c.Header("ETag", channelRoutingPolicyDraftETag(summary))
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
	summary := draft.Summary()
	c.Header("ETag", channelRoutingPolicyDraftETag(summary))
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
	summary := draft.Summary()
	c.Header("ETag", channelRoutingPolicyDraftETag(summary))
	common.ApiSuccess(c, summary)
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
