package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type channelRoutingBreakerResetRequest struct {
	Scope             string `json:"scope"`
	PoolID            int    `json:"pool_id"`
	MemberID          int    `json:"member_id"`
	ModelName         string `json:"model_name"`
	EndpointAuthority string `json:"endpoint_authority"`
	Region            string `json:"region"`
	Reason            string `json:"reason"`
}

type channelRoutingBreakerResetResponse struct {
	Operation channelRoutingOperationView     `json:"operation"`
	Target    model.RoutingBreakerResetTarget `json:"target"`
}

func ResetChannelRoutingBreaker(c *gin.Context) {
	request, err := decodeChannelRoutingBreakerResetRequest(c.Request.Body)
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	identity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypeBreakerReset, request)
	if !ok {
		return
	}
	existing, lookupErr := model.GetRoutingOperationByRequestIdentityContext(c.Request.Context(), identity)
	if lookupErr == nil {
		if existing.OperationType != model.RoutingOperationTypeBreakerReset {
			writeChannelRoutingBreakerResetError(c, model.ErrRoutingOperationIdempotencyConflict)
			return
		}
		command, err := model.GetRoutingBreakerResetCommandByOperationContext(c.Request.Context(), existing.ID)
		if err != nil {
			writeChannelRoutingBreakerResetError(c, err)
			return
		}
		writeChannelRoutingBreakerResetResponse(c, existing, command.Target())
		return
	}
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		writeChannelRoutingBreakerResetError(c, lookupErr)
		return
	}
	var resolved channelrouting.BreakerResetResolvedTarget
	switch request.Scope {
	case model.RoutingBreakerResetScopeMember:
		resolved, err = channelrouting.ResolveMemberBreakerResetTarget(request.PoolID, request.MemberID, request.ModelName)
	case model.RoutingBreakerResetScopeEndpoint:
		resolved, err = channelrouting.ResolveEndpointBreakerResetTarget(request.EndpointAuthority, request.Region)
	default:
		err = model.ErrRoutingBreakerResetInvalid
	}
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	subjectType := model.RoutingOperationSubjectEndpointBreaker
	subjectID := int64(0)
	poolID := 0
	if resolved.Target.Scope == model.RoutingBreakerResetScopeMember {
		subjectType = model.RoutingOperationSubjectMemberBreaker
		subjectID = int64(resolved.Target.MemberID)
		poolID = resolved.Target.PoolID
	}
	operation, _, err := model.CreateRoutingBreakerResetOperationContext(
		c.Request.Context(),
		model.RoutingOperationSpec{
			Type: model.RoutingOperationTypeBreakerReset, EvaluationHash: identity.PayloadHash,
			SubjectType: subjectType, SubjectID: subjectID, PoolID: poolID,
			ExpectedRevision: resolved.ExpectedRevision, ExpectedActivationID: resolved.ExpectedActivationID,
			ActorID: common.GetContextKeyInt(c, constant.ContextKeyUserId), Reason: request.Reason,
			RequestKeyHash: identity.KeyHash, RequestPayloadHash: identity.PayloadHash,
		},
		resolved.Target,
	)
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	writeChannelRoutingBreakerResetResponse(c, operation, resolved.Target)
}

func writeChannelRoutingBreakerResetResponse(
	c *gin.Context,
	operation model.RoutingOperation,
	target model.RoutingBreakerResetTarget,
) {
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	response := channelRoutingBreakerResetResponse{Operation: view, Target: target}
	if operation.Status == model.RoutingOperationStatusPending || operation.Status == model.RoutingOperationStatusRunning ||
		operation.Status == model.RoutingOperationStatusRetryWait {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "message": "", "data": response})
		return
	}
	common.ApiSuccess(c, response)
}

func decodeChannelRoutingBreakerResetRequest(body io.Reader) (channelRoutingBreakerResetRequest, error) {
	if body == nil {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingControlBodyMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > channelRoutingControlBodyMaxBytes {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	for key := range fields {
		switch key {
		case "scope", "pool_id", "member_id", "model_name", "endpoint_authority", "region", "reason":
		default:
			return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
		}
	}
	var request channelRoutingBreakerResetRequest
	if common.Unmarshal(data, &request) != nil {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	request.ModelName = strings.TrimSpace(request.ModelName)
	request.EndpointAuthority = strings.TrimSpace(request.EndpointAuthority)
	request.Region = strings.ToLower(strings.TrimSpace(request.Region))
	request.Reason = strings.TrimSpace(request.Reason)
	if !utf8.ValidString(request.Reason) || utf8.RuneCountInString(request.Reason) > 512 {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	if request.Reason == "" {
		request.Reason = "manual breaker reset"
	}
	member := request.Scope == model.RoutingBreakerResetScopeMember && request.PoolID > 0 && request.MemberID > 0 &&
		request.ModelName != "" && request.EndpointAuthority == "" && request.Region == ""
	endpoint := request.Scope == model.RoutingBreakerResetScopeEndpoint && request.PoolID == 0 && request.MemberID == 0 &&
		request.ModelName == "" && request.EndpointAuthority != "" && request.Region != ""
	if !member && !endpoint {
		return channelRoutingBreakerResetRequest{}, model.ErrRoutingBreakerResetInvalid
	}
	return request, nil
}

func writeChannelRoutingBreakerResetError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, channelrouting.ErrBreakerResetTargetNotFound):
		writeChannelRoutingPolicyDraftError(c, http.StatusNotFound, "breaker_target_not_found", "channel routing breaker target not found", err)
	case errors.Is(err, channelrouting.ErrBreakerResetSnapshotUnavailable):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "routing_snapshot_unavailable", "channel routing snapshot is unavailable", err)
	case errors.Is(err, model.ErrRoutingOperationIdempotencyConflict):
		writeChannelRoutingPolicyDraftError(c, http.StatusConflict, "idempotency_key_conflict", "Idempotency-Key was already used for a different request", err)
	case errors.Is(err, model.ErrRoutingBreakerResetInvalid), errors.Is(err, model.ErrRoutingOperationInvalid):
		writeChannelRoutingPolicyDraftError(c, http.StatusBadRequest, "invalid_breaker_reset", "invalid channel routing breaker reset request", err)
	default:
		writeChannelRoutingPolicyDraftError(c, http.StatusInternalServerError, "breaker_reset_failed", "channel routing breaker reset failed", err)
	}
}
