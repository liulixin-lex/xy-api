package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const channelRoutingCostBindingBodyMaxBytes = 128 << 10

var errChannelRoutingCostBindingInvalid = errors.New("invalid channel routing cost binding")
var errChannelRoutingCostBindingBodyTooLarge = errors.New("channel routing cost binding body is too large")

type channelRoutingCostBindingList struct {
	Items    []routingBindingView `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

func ListChannelRoutingCostBindings(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	search := strings.TrimSpace(c.Query("search"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 256 {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_filter", "invalid cost binding filter", errChannelRoutingCostBindingInvalid)
		return
	}
	upstreamType := strings.ToLower(strings.TrimSpace(c.Query("upstream_type")))
	if upstreamType != "" && upstreamType != model.RoutingUpstreamTypeNewAPI && upstreamType != model.RoutingUpstreamTypeSub2API {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_filter", "invalid cost binding filter", errChannelRoutingCostBindingInvalid)
		return
	}
	enabled, err := parseOptionalChannelRoutingBool(c.Query("enabled"))
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_filter", "invalid cost binding filter", err)
		return
	}
	channelID, err := parseOptionalChannelRoutingInt(c.Query("channel_id"))
	if err != nil || channelID != nil && *channelID <= 0 {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_filter", "invalid cost binding filter", errChannelRoutingCostBindingInvalid)
		return
	}
	bindings, total, err := model.ListRoutingChannelBindingsContext(
		c.Request.Context(),
		model.RoutingChannelBindingFilter{
			Search: search, UpstreamType: upstreamType, Enabled: enabled, ChannelID: channelID,
		},
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_list_failed", "failed to load cost bindings", err)
		return
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), bindings)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_list_failed", "failed to load cost bindings", err)
		return
	}
	common.ApiSuccess(c, channelRoutingCostBindingList{
		Items: views,
		Total: total, Page: page, PageSize: pageSize,
	})
}

func GetChannelRoutingCostBinding(c *gin.Context) {
	binding, ok := loadChannelRoutingCostBinding(c)
	if !ok {
		return
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), []model.RoutingChannelBinding{binding})
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_load_failed", "failed to load cost binding", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	common.ApiSuccess(c, view)
}

func CreateChannelRoutingCostBinding(c *gin.Context) {
	request, _, err := decodeChannelRoutingCostBindingRequest(c.Request.Body, false)
	if err != nil {
		writeChannelRoutingCostBindingDecodeError(c, err)
		return
	}
	request = normalizeChannelRoutingCostBindingRequest(request)
	if err = validateChannelRoutingCostBindingRequest(request, true); err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "invalid cost binding", err)
		return
	}
	channelExists, err := channelRoutingCostBindingChannelExists(c.Request.Context(), request.ChannelID)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_create_failed", "failed to verify channel", err)
		return
	}
	if !channelExists {
		writeChannelRoutingCostBindingError(c, http.StatusNotFound, "channel_not_found", "channel not found", gorm.ErrRecordNotFound)
		return
	}
	if _, err = model.GetRoutingChannelBindingContext(c.Request.Context(), request.ChannelID); err == nil {
		writeChannelRoutingCostBindingError(c, http.StatusConflict, "cost_binding_exists", "cost binding already exists", model.ErrRoutingBindingChanged)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_create_failed", "failed to create cost binding", err)
		return
	}
	binding, err := createRoutingBindingContext(c.Request.Context(), request, c.GetInt("id"))
	if err != nil {
		if _, lookupErr := model.GetRoutingChannelBindingContext(c.Request.Context(), request.ChannelID); lookupErr == nil {
			writeChannelRoutingCostBindingError(c, http.StatusConflict, "cost_binding_exists", "cost binding already exists", model.ErrRoutingBindingChanged)
			return
		}
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_create_failed", "failed to create cost binding", err)
		return
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), []model.RoutingChannelBinding{binding})
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_create_failed", "failed to load created cost binding", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeCostBindingChanged, 0, gin.H{
		"action": "created", "channel_id": binding.ChannelID,
	})
	c.JSON(http.StatusCreated, gin.H{"success": true, "message": "", "data": view})
}

func UpdateChannelRoutingCostBinding(c *gin.Context) {
	expected, ok := loadChannelRoutingCostBinding(c)
	if !ok {
		return
	}
	if !requireChannelRoutingCostBindingIfMatch(c, expected) {
		return
	}
	request, _, err := decodeChannelRoutingCostBindingRequest(c.Request.Body, false)
	if err != nil {
		writeChannelRoutingCostBindingDecodeError(c, err)
		return
	}
	request = normalizeChannelRoutingCostBindingRequest(request)
	if request.ChannelID != 0 && request.ChannelID != expected.ChannelID {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "cost binding channel does not match the route", errChannelRoutingCostBindingInvalid)
		return
	}
	request.ChannelID = expected.ChannelID
	if err = validateChannelRoutingCostBindingRequest(request, false); err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "invalid cost binding", err)
		return
	}
	updated, err := updateRoutingBindingContext(c.Request.Context(), expected, request, c.GetInt("id"))
	if errors.Is(err, model.ErrRoutingBindingChanged) {
		writeChannelRoutingCostBindingConflict(c, expected.ChannelID)
		return
	}
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_update_failed", "failed to update cost binding", err)
		return
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), []model.RoutingChannelBinding{updated})
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_update_failed", "failed to load updated cost binding", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeCostBindingChanged, 0, gin.H{
		"action": "updated", "channel_id": updated.ChannelID,
	})
	common.ApiSuccess(c, view)
}

func DeleteChannelRoutingCostBinding(c *gin.Context) {
	expected, ok := loadChannelRoutingCostBinding(c)
	if !ok {
		return
	}
	if !requireChannelRoutingCostBindingIfMatch(c, expected) {
		return
	}
	if err := deleteRoutingBindingContext(c.Request.Context(), expected, c.GetInt("id")); errors.Is(err, model.ErrRoutingBindingChanged) {
		writeChannelRoutingCostBindingConflict(c, expected.ChannelID)
		return
	} else if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_delete_failed", "failed to delete cost binding", err)
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeCostBindingChanged, 0, gin.H{
		"action": "deleted", "channel_id": expected.ChannelID,
	})
	common.ApiSuccess(c, gin.H{"channel_id": expected.ChannelID})
}

func TestChannelRoutingCostBinding(c *gin.Context) {
	binding, ok := channelRoutingCostBindingForAction(c)
	if !ok {
		return
	}
	payload, err := fetchRoutingPricingPayload(c.Request.Context(), binding)
	if err != nil {
		writeChannelRoutingCostBindingActionError(c, binding, "cost_binding_test_failed", "cost binding test failed", err)
		return
	}
	credentials, _ := binding.GetCredentials()
	groups, groupsTotal := routingPricingGroups(payload)
	common.ApiSuccess(c, gin.H{
		"channel_id": binding.ChannelID, "upstream_type": binding.UpstreamType,
		"credential_ready": credentials.ReadyForUpstream(binding.UpstreamType),
		"groups":           groups, "groups_total": groupsTotal,
		"groups_truncated": groupsTotal > len(groups), "model_count": len(payload.Data),
		"pricing_version": payload.PricingVersion,
	})
}

func LoadChannelRoutingCostBindingGroups(c *gin.Context) {
	binding, ok := channelRoutingCostBindingForAction(c)
	if !ok {
		return
	}
	payload, err := fetchRoutingPricingPayload(c.Request.Context(), binding)
	if err != nil {
		writeChannelRoutingCostBindingActionError(c, binding, "cost_binding_groups_failed", "failed to load upstream groups", err)
		return
	}
	credentials, _ := binding.GetCredentials()
	groups, groupsTotal := routingPricingGroups(payload)
	common.ApiSuccess(c, gin.H{
		"channel_id": binding.ChannelID, "upstream_type": binding.UpstreamType,
		"upstream_group": binding.UpstreamGroup, "groups": groups,
		"groups_total": groupsTotal, "groups_truncated": groupsTotal > len(groups),
		"requires_sync": false, "sync_task_type": model.SystemTaskTypeRoutingCostSync,
		"serves_claude":   binding.ServesClaudeCode,
		"credential_test": credentials.ReadyForUpstream(binding.UpstreamType),
		"model_count":     len(payload.Data), "pricing_version": payload.PricingVersion,
	})
}

func channelRoutingCostBindingETag(binding model.RoutingChannelBinding) string {
	payload, _ := common.Marshal(struct {
		ID               int     `json:"id"`
		ChannelID        int     `json:"channel_id"`
		UpstreamType     string  `json:"upstream_type"`
		BaseURL          string  `json:"base_url"`
		UpstreamGroup    string  `json:"upstream_group"`
		ServesClaudeCode bool    `json:"serves_claude_code"`
		EgressPolicyJSON *string `json:"egress_policy_json"`
		EncCredentials   *string `json:"enc_credentials"`
		KeyVersion       int     `json:"key_version"`
		NewAPIUserID     *int    `json:"new_api_user_id"`
		Enabled          bool    `json:"enabled"`
	}{
		ID: binding.ID, ChannelID: binding.ChannelID, UpstreamType: binding.UpstreamType,
		BaseURL: binding.BaseURL, UpstreamGroup: binding.UpstreamGroup,
		ServesClaudeCode: binding.ServesClaudeCode, EgressPolicyJSON: binding.EgressPolicyJSON,
		EncCredentials: binding.EncCredentials,
		KeyVersion:     binding.KeyVersion, NewAPIUserID: binding.NewAPIUserID,
		Enabled: binding.Enabled,
	})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("\"crb.%d.1.%x\"", binding.ID, digest)
}

func parseChannelRoutingCostBindingETag(value string) error {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 256 || value[0] != '"' || value[len(value)-1] != '"' {
		return errChannelRoutingCostBindingInvalid
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) != 4 || parts[0] != "crb" {
		return errChannelRoutingCostBindingInvalid
	}
	if id, err := strconv.Atoi(parts[1]); err != nil || id <= 0 {
		return errChannelRoutingCostBindingInvalid
	}
	if version, err := strconv.Atoi(parts[2]); err != nil || version != 1 {
		return errChannelRoutingCostBindingInvalid
	}
	if len(parts[3]) != 64 {
		return errChannelRoutingCostBindingInvalid
	}
	for _, char := range parts[3] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return errChannelRoutingCostBindingInvalid
		}
	}
	return nil
}

func requireChannelRoutingCostBindingIfMatch(c *gin.Context, expected model.RoutingChannelBinding) bool {
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeChannelRoutingCostBindingError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", model.ErrRoutingBindingChanged)
		return false
	}
	if err := parseChannelRoutingCostBindingETag(ifMatch); err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_if_match", "invalid If-Match cost binding tag", err)
		return false
	}
	if ifMatch != channelRoutingCostBindingETag(expected) {
		writeChannelRoutingCostBindingConflict(c, expected.ChannelID)
		return false
	}
	return true
}

func loadChannelRoutingCostBinding(c *gin.Context) (model.RoutingChannelBinding, bool) {
	channelID, err := parseChannelRoutingCostBindingChannelID(c.Param("channelId"))
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_channel_id", "invalid channel id", errChannelRoutingCostBindingInvalid)
		return model.RoutingChannelBinding{}, false
	}
	binding, err := model.GetRoutingChannelBindingContext(c.Request.Context(), channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeChannelRoutingCostBindingError(c, http.StatusNotFound, "cost_binding_not_found", "cost binding not found", err)
		return model.RoutingChannelBinding{}, false
	}
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_load_failed", "failed to load cost binding", err)
		return model.RoutingChannelBinding{}, false
	}
	return binding, true
}

func channelRoutingCostBindingViews(ctx context.Context, bindings []model.RoutingChannelBinding) ([]routingBindingView, error) {
	channelIDs := make([]int, 0, len(bindings))
	for _, binding := range bindings {
		channelIDs = append(channelIDs, binding.ChannelID)
	}
	names := make(map[int]string, len(channelIDs))
	if len(channelIDs) > 0 {
		var channels []model.Channel
		if err := model.DB.WithContext(ctx).Select("id", "name").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return nil, fmt.Errorf("load cost binding channel names: %w", err)
		}
		for _, channel := range channels {
			names[channel.Id] = channel.Name
		}
	}
	views := make([]routingBindingView, 0, len(bindings))
	for _, binding := range bindings {
		view, err := routingBindingViewWithStoredCredentials(binding)
		if err != nil {
			view = buildRoutingBindingView(binding, model.RoutingCredentials{})
			view.CredentialError = "Stored credentials are unavailable. Re-enter them to repair this binding."
			if binding.LastSyncError != nil {
				message := "routing cost sync failed"
				view.LastSyncError = &message
			}
		}
		view.ChannelName = names[binding.ChannelID]
		views = append(views, view)
	}
	return views, nil
}

func decodeChannelRoutingCostBindingRequest(
	body io.Reader,
	optional bool,
) (routingBindingRequest, map[string]json.RawMessage, error) {
	if body == nil {
		if optional {
			return routingBindingRequest{}, map[string]json.RawMessage{}, nil
		}
		return routingBindingRequest{}, nil, routingBindingFieldError("", "invalid_json")
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingCostBindingBodyMaxBytes+1))
	if err != nil {
		return routingBindingRequest{}, nil, routingBindingFieldError("", "invalid_json")
	}
	if len(data) == 0 {
		if optional {
			return routingBindingRequest{}, map[string]json.RawMessage{}, nil
		}
		return routingBindingRequest{}, nil, routingBindingFieldError("", "invalid_json")
	}
	if len(data) > channelRoutingCostBindingBodyMaxBytes {
		return routingBindingRequest{}, nil, errChannelRoutingCostBindingBodyTooLarge
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil {
		return routingBindingRequest{}, nil, routingBindingFieldError("", "invalid_json")
	}
	allowed := map[string]struct{}{
		"channel_id": {}, "upstream_type": {}, "base_url": {}, "upstream_group": {},
		"serves_claude_code": {}, "egress_allowed_private_cidrs": {},
		"new_api_user_id": {}, "enabled": {}, "credentials": {},
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return routingBindingRequest{}, nil, routingBindingFieldError("", "unknown_field")
		}
	}
	if raw, ok := fields["credentials"]; ok && string(raw) != "null" {
		var credentialFields map[string]json.RawMessage
		if common.Unmarshal(raw, &credentialFields) != nil || credentialFields == nil {
			return routingBindingRequest{}, nil, routingBindingFieldError("credentials", "invalid_type")
		}
		allowedCredentials := map[string]struct{}{
			"new_api_access_token": {}, "gateway_api_key": {}, "sub2api_email": {},
			"sub2api_password": {}, "sub2api_token": {}, "custom_ca_pem": {},
		}
		for key := range credentialFields {
			if _, ok := allowedCredentials[key]; !ok {
				return routingBindingRequest{}, nil, routingBindingFieldError("credentials", "unknown_field")
			}
		}
	}
	var request routingBindingRequest
	if common.Unmarshal(data, &request) != nil {
		return routingBindingRequest{}, nil, routingBindingFieldError("", "invalid_type")
	}
	return request, fields, nil
}

func normalizeChannelRoutingCostBindingRequest(request routingBindingRequest) routingBindingRequest {
	request.UpstreamType = strings.ToLower(strings.TrimSpace(request.UpstreamType))
	request.BaseURL = strings.TrimSpace(request.BaseURL)
	request.UpstreamGroup = strings.TrimSpace(request.UpstreamGroup)
	if normalized, err := service.NormalizeRoutingCostEgressCIDRs(request.EgressAllowedPrivateCIDRs); err == nil {
		request.EgressAllowedPrivateCIDRs = normalized
	}
	return request
}

func validateChannelRoutingCostBindingRequest(request routingBindingRequest, requireChannelID bool) error {
	if err := validateRoutingBindingRequest(request, requireChannelID); err != nil {
		return err
	}
	if !utf8.ValidString(request.BaseURL) {
		return routingBindingFieldError("base_url", "invalid")
	}
	if utf8.RuneCountInString(request.BaseURL) > 512 {
		return routingBindingFieldError("base_url", "too_long")
	}
	if !utf8.ValidString(request.UpstreamGroup) {
		return routingBindingFieldError("upstream_group", "invalid")
	}
	if utf8.RuneCountInString(request.UpstreamGroup) > 128 {
		return routingBindingFieldError("upstream_group", "too_long")
	}
	if request.NewAPIUserID != nil && *request.NewAPIUserID <= 0 {
		return routingBindingFieldError("new_api_user_id", "invalid")
	}
	credentialLimits := []struct {
		name  string
		value *string
		limit int
	}{
		{"new_api_access_token", request.Credentials.NewAPIAccessToken, 4_096},
		{"gateway_api_key", request.Credentials.GatewayAPIKey, 4_096},
		{"sub2api_email", request.Credentials.Sub2APIEmail, 320},
		{"sub2api_password", request.Credentials.Sub2APIPassword, 4_096},
		{"sub2api_token", request.Credentials.Sub2APIToken, 4_096},
		{"custom_ca_pem", request.Credentials.CustomCAPEM, 96 << 10},
	}
	for _, field := range credentialLimits {
		if field.value == nil {
			continue
		}
		if !utf8.ValidString(*field.value) {
			return routingBindingFieldError(field.name, "invalid")
		}
		if utf8.RuneCountInString(*field.value) > field.limit {
			return routingBindingFieldError(field.name, "too_long")
		}
	}
	return nil
}

func channelRoutingCostBindingChannelExists(ctx context.Context, channelID int) (bool, error) {
	if channelID <= 0 {
		return false, nil
	}
	var count int64
	err := model.DB.WithContext(ctx).Model(&model.Channel{}).
		Where("id = ?", channelID).Count(&count).Error
	return count == 1, err
}

func channelRoutingCostBindingForAction(c *gin.Context) (model.RoutingChannelBinding, bool) {
	request, fields, err := decodeChannelRoutingCostBindingRequest(c.Request.Body, true)
	if err != nil {
		writeChannelRoutingCostBindingDecodeError(c, err)
		return model.RoutingChannelBinding{}, false
	}
	rawChannelID := strings.TrimSpace(c.Param("channelId"))
	inline := rawChannelID == "new" || len(fields) > 0
	if !inline {
		return loadChannelRoutingCostBinding(c)
	}
	if !requireChannelRoutingSensitiveWriteCurrent(c) {
		return model.RoutingChannelBinding{}, false
	}
	request = normalizeChannelRoutingCostBindingRequest(request)
	var existing model.RoutingChannelBinding
	if rawChannelID != "new" {
		channelID, parseErr := parseChannelRoutingCostBindingChannelID(rawChannelID)
		if parseErr != nil {
			writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_channel_id", "invalid channel id", errChannelRoutingCostBindingInvalid)
			return model.RoutingChannelBinding{}, false
		}
		existing, err = model.GetRoutingChannelBindingContext(c.Request.Context(), channelID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeChannelRoutingCostBindingError(c, http.StatusNotFound, "cost_binding_not_found", "cost binding not found", err)
			} else {
				writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_load_failed", "failed to load cost binding", err)
			}
			return model.RoutingChannelBinding{}, false
		}
		if request.ChannelID != 0 && request.ChannelID != channelID {
			writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "cost binding channel does not match the route", errChannelRoutingCostBindingInvalid)
			return model.RoutingChannelBinding{}, false
		}
		request.ChannelID = channelID
	}
	if err := validateChannelRoutingCostBindingRequest(request, true); err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "invalid cost binding", err)
		return model.RoutingChannelBinding{}, false
	}
	channelExists, channelErr := channelRoutingCostBindingChannelExists(c.Request.Context(), request.ChannelID)
	if channelErr != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_load_failed", "failed to verify channel", channelErr)
		return model.RoutingChannelBinding{}, false
	}
	if !channelExists {
		writeChannelRoutingCostBindingError(c, http.StatusNotFound, "channel_not_found", "channel not found", gorm.ErrRecordNotFound)
		return model.RoutingChannelBinding{}, false
	}
	candidate, err := routingBindingFromRequest(request)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "invalid cost binding", err)
		return model.RoutingChannelBinding{}, false
	}
	if existing.ID > 0 {
		candidate.EncCredentials = existing.EncCredentials
		candidate.KeyVersion = existing.KeyVersion
		providerChanged := existing.UpstreamType != candidate.UpstreamType
		if providerChanged || hasRoutingCredentials(request.Credentials) {
			credentials, credentialErr := routingBindingCredentialsForUpdate(existing, candidate, request)
			if credentialErr != nil {
				writeChannelRoutingCostBindingError(c, http.StatusConflict, "cost_binding_credentials_unavailable", "stored cost binding credentials are unavailable", credentialErr)
				return model.RoutingChannelBinding{}, false
			}
			if credentialErr = candidate.SetCredentials(credentials); credentialErr != nil {
				writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_credentials_failed", "failed to prepare cost binding credentials", credentialErr)
				return model.RoutingChannelBinding{}, false
			}
		}
	} else if hasRoutingCredentials(request.Credentials) {
		if err := candidate.SetCredentials(buildRoutingCredentials(request)); err != nil {
			writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_credentials_failed", "failed to prepare cost binding credentials", err)
			return model.RoutingChannelBinding{}, false
		}
	}
	return candidate, true
}

func parseChannelRoutingCostBindingChannelID(raw string) (int, error) {
	channelID, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || channelID <= 0 {
		return 0, errChannelRoutingCostBindingInvalid
	}
	return channelID, nil
}

func requireChannelRoutingSensitiveWriteCurrent(c *gin.Context) bool {
	allowed, err := authz.CanCurrent(
		c.Request.Context(), c.GetInt("id"), c.GetInt("role"), authz.ChannelRoutingSensitiveWrite,
	)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "authorization_failed", "failed to verify cost binding permission", err)
		return false
	}
	if !allowed {
		writeChannelRoutingCostBindingError(c, http.StatusForbidden, "insufficient_privilege", "insufficient privilege", nil)
		return false
	}
	return true
}

func writeChannelRoutingCostBindingConflict(c *gin.Context, channelID int) {
	current, err := model.GetRoutingChannelBindingContext(c.Request.Context(), channelID)
	if err == nil {
		views, viewErr := channelRoutingCostBindingViews(c.Request.Context(), []model.RoutingChannelBinding{current})
		if viewErr != nil {
			writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_conflict_failed", "failed to load current cost binding", viewErr)
			return
		}
		view := views[0]
		c.Header("ETag", view.ETag)
		c.JSON(http.StatusConflict, gin.H{
			"success": false, "code": "cost_binding_conflict", "message": "cost binding changed",
			"conflict": gin.H{"current": view, "current_etag": view.ETag},
		})
		return
	}
	c.JSON(http.StatusConflict, gin.H{
		"success": false, "code": "cost_binding_conflict", "message": "cost binding changed",
		"conflict": gin.H{"current": nil},
	})
}

func writeChannelRoutingCostBindingDecodeError(c *gin.Context, err error) {
	if errors.Is(err, errChannelRoutingCostBindingBodyTooLarge) {
		writeChannelRoutingCostBindingError(c, http.StatusRequestEntityTooLarge, "cost_binding_too_large", "cost binding request is too large", err)
		return
	}
	writeChannelRoutingCostBindingError(c, http.StatusBadRequest, "invalid_cost_binding", "invalid cost binding", err)
}

func writeChannelRoutingCostBindingActionError(
	c *gin.Context,
	binding model.RoutingChannelBinding,
	code string,
	message string,
	err error,
) {
	credentials, _ := binding.GetCredentials()
	safe := common.SanitizeErrorMessage(err.Error(), routingCredentialSecrets(credentials)...)
	if safe == "" {
		safe = message
	}
	c.JSON(http.StatusBadGateway, gin.H{
		"success": false, "code": code, "message": message, "detail": safe,
	})
}

func writeChannelRoutingCostBindingError(
	c *gin.Context,
	status int,
	code string,
	message string,
	err error,
) {
	if status >= http.StatusInternalServerError && err != nil {
		common.SysError(message + ": " + common.SanitizeErrorMessage(err.Error()))
	}
	payload := gin.H{"success": false, "code": code, "message": message}
	var validationErr routingBindingValidationError
	if errors.As(err, &validationErr) {
		if validationErr.Field != "" {
			payload["field"] = validationErr.Field
		}
		if validationErr.Reason != "" {
			payload["reason"] = validationErr.Reason
		}
	}
	c.JSON(status, payload)
}
