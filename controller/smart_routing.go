package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type routingCredentialRequest struct {
	NewAPIAccessToken *string `json:"new_api_access_token"`
	GatewayAPIKey     *string `json:"gateway_api_key"`
	Sub2APIEmail      *string `json:"sub2api_email"`
	Sub2APIPassword   *string `json:"sub2api_password"`
	Sub2APIToken      *string `json:"sub2api_token"`
	CustomCAPEM       *string `json:"custom_ca_pem"`
}

type routingBindingRequest struct {
	ChannelID                 int                      `json:"channel_id"`
	UpstreamType              string                   `json:"upstream_type"`
	BaseURL                   string                   `json:"base_url"`
	UpstreamGroup             string                   `json:"upstream_group"`
	ServesClaudeCode          bool                     `json:"serves_claude_code"`
	EgressAllowedPrivateCIDRs []string                 `json:"egress_allowed_private_cidrs"`
	NewAPIUserID              *int                     `json:"new_api_user_id"`
	Enabled                   *bool                    `json:"enabled"`
	Credentials               routingCredentialRequest `json:"credentials"`
}

type routingBindingValidationError struct {
	Field  string
	Reason string
}

func (err routingBindingValidationError) Error() string {
	return "invalid routing binding"
}

func routingBindingFieldError(field, reason string) error {
	return routingBindingValidationError{Field: field, Reason: reason}
}

type routingCredentialMasks struct {
	NewAPIAccessToken  string `json:"new_api_access_token,omitempty"`
	GatewayAPIKey      string `json:"gateway_api_key,omitempty"`
	Sub2APIEmail       string `json:"sub2api_email,omitempty"`
	Sub2APIPassword    string `json:"sub2api_password,omitempty"`
	Sub2APIToken       string `json:"sub2api_token,omitempty"`
	CustomCAConfigured bool   `json:"custom_ca_configured"`
}

type routingBindingView struct {
	ID                        int                    `json:"id"`
	ChannelID                 int                    `json:"channel_id"`
	ChannelName               string                 `json:"channel_name,omitempty"`
	ETag                      string                 `json:"etag"`
	UpstreamType              string                 `json:"upstream_type"`
	BaseURL                   string                 `json:"base_url"`
	UpstreamGroup             string                 `json:"upstream_group"`
	ServesClaudeCode          bool                   `json:"serves_claude_code"`
	EgressAllowedPrivateCIDRs []string               `json:"egress_allowed_private_cidrs"`
	NewAPIUserID              *int                   `json:"new_api_user_id,omitempty"`
	Enabled                   bool                   `json:"enabled"`
	SyncFailureCount          int                    `json:"sync_failure_count"`
	SyncBackoffUntil          int64                  `json:"sync_backoff_until"`
	LastSyncError             *string                `json:"last_sync_error,omitempty"`
	CredentialMasks           routingCredentialMasks `json:"credential_masks"`
	CredentialError           string                 `json:"credential_error,omitempty"`
	EgressPolicyError         string                 `json:"egress_policy_error,omitempty"`
	CreatedTime               int64                  `json:"created_time"`
	UpdatedTime               int64                  `json:"updated_time"`
}

const legacySmartRoutingBindingLimit = 500

func GetSmartRoutingSettings(c *gin.Context) {
	getChannelRoutingRuntimeSettings(c, true)
}

func UpdateSmartRoutingSettings(c *gin.Context) {
	updateChannelRoutingRuntimeSettings(c, true)
}

func ListSmartRoutingBindings(c *gin.Context) {
	bindingIDs := make([]int, 0, legacySmartRoutingBindingLimit+1)
	if err := model.DB.Model(&model.RoutingChannelBinding{}).
		Order("channel_id asc").
		Limit(legacySmartRoutingBindingLimit+1).
		Pluck("id", &bindingIDs).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	if len(bindingIDs) > legacySmartRoutingBindingLimit {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"success":   false,
			"code":      "legacy_result_too_large",
			"message":   "legacy routing bindings endpoint supports at most 500 records",
			"successor": "/api/channel-routing/v2/cost-bindings",
		})
		return
	}

	bindings := make([]model.RoutingChannelBinding, 0, len(bindingIDs))
	if len(bindingIDs) > 0 {
		if err := model.DB.Where("id IN ?", bindingIDs).Order("channel_id asc").Find(&bindings).Error; err != nil {
			common.ApiError(c, err)
			return
		}
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), bindings)
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_list_failed", "failed to load routing bindings", err)
		return
	}
	common.ApiSuccess(c, views)
}

func GetSmartRoutingBinding(c *gin.Context) {
	binding, ok := loadRoutingBinding(c)
	if !ok {
		return
	}
	views, err := channelRoutingCostBindingViews(c.Request.Context(), []model.RoutingChannelBinding{*binding})
	if err != nil {
		writeChannelRoutingCostBindingError(c, http.StatusInternalServerError, "cost_binding_load_failed", "failed to load routing binding", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	common.ApiSuccess(c, view)
}

func CreateSmartRoutingBinding(c *gin.Context) {
	CreateChannelRoutingCostBinding(c)
}

func UpdateSmartRoutingBinding(c *gin.Context) {
	UpdateChannelRoutingCostBinding(c)
}

func DeleteSmartRoutingBinding(c *gin.Context) {
	DeleteChannelRoutingCostBinding(c)
}

func createRoutingBindingContext(ctx context.Context, request routingBindingRequest, actorID int) (model.RoutingChannelBinding, error) {
	binding, err := routingBindingFromRequest(request)
	if err != nil {
		return model.RoutingChannelBinding{}, err
	}
	if hasRoutingCredentials(request.Credentials) {
		if err := binding.SetCredentials(buildRoutingCredentials(request)); err != nil {
			return model.RoutingChannelBinding{}, err
		}
	}
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return model.RoutingChannelBinding{}, err
	}
	defer smartRoutingRuntimeStateMu.Unlock()
	if err := model.CreateRoutingChannelBindingWithAuditContext(ctx, &binding, actorID); err != nil {
		return model.RoutingChannelBinding{}, err
	}
	return binding, nil
}

func updateRoutingBindingContext(
	ctx context.Context,
	expected model.RoutingChannelBinding,
	request routingBindingRequest,
	actorID int,
) (model.RoutingChannelBinding, error) {
	updated, err := routingBindingFromRequest(request)
	if err != nil {
		return model.RoutingChannelBinding{}, err
	}
	updated.ID = expected.ID
	updated.ChannelID = expected.ChannelID
	updated.CreatedTime = expected.CreatedTime
	updated.EncCredentials = expected.EncCredentials
	updated.KeyVersion = expected.KeyVersion
	oldSub2APIAuthKey := newRoutingSub2APIAuthKey(expected, model.RoutingCredentials{})
	retireOldSub2APIAuth := expected.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		updated.UpstreamType != model.RoutingUpstreamTypeSub2API
	providerChanged := expected.UpstreamType != updated.UpstreamType
	if providerChanged || hasRoutingCredentials(request.Credentials) {
		credentials, err := routingBindingCredentialsForUpdate(expected, updated, request)
		if err != nil {
			return model.RoutingChannelBinding{}, err
		}
		if err := updated.SetCredentials(credentials); err != nil {
			return model.RoutingChannelBinding{}, err
		}
	}
	newSub2APIAuthKey := newRoutingSub2APIAuthKey(updated, model.RoutingCredentials{})
	authKeyChanged := oldSub2APIAuthKey != newSub2APIAuthKey
	if expected.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		updated.UpstreamType == model.RoutingUpstreamTypeSub2API && authKeyChanged {
		retireOldSub2APIAuth = true
	}
	activateNewSub2APIAuth := updated.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		(expected.UpstreamType != model.RoutingUpstreamTypeSub2API || authKeyChanged)
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return model.RoutingChannelBinding{}, err
	}
	defer smartRoutingRuntimeStateMu.Unlock()
	activationFence := routingSub2APIJWTActivationFence{}
	if activateNewSub2APIAuth {
		var err error
		activationFence, err = prepareRoutingSub2APIJWTActivation(ctx, updated)
		if err != nil {
			return model.RoutingChannelBinding{}, err
		}
	}
	if err := model.UpdateRoutingChannelBindingAndInvalidateCostWithAuditContext(ctx, expected, &updated, actorID); err != nil {
		return model.RoutingChannelBinding{}, err
	}
	routinghotcache.ClearCostChannel(updated.ChannelID)
	if retireOldSub2APIAuth {
		invalidateRoutingSub2APIJWT(ctx, expected)
	}
	if activateNewSub2APIAuth {
		activateRoutingSub2APIJWT(ctx, activationFence)
	}
	return updated, nil
}

func deleteRoutingBindingContext(ctx context.Context, expected model.RoutingChannelBinding, actorID int) error {
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return err
	}
	defer smartRoutingRuntimeStateMu.Unlock()
	if err := model.DeleteRoutingChannelBindingAndInvalidateCostWithAuditContext(ctx, expected, actorID); err != nil {
		return err
	}
	invalidateRoutingSub2APIJWT(ctx, expected)
	routinghotcache.ClearCostChannel(expected.ChannelID)
	return nil
}

func TestSmartRoutingBinding(c *gin.Context) {
	TestChannelRoutingCostBinding(c)
}

func LoadSmartRoutingBindingGroups(c *gin.Context) {
	LoadChannelRoutingCostBindingGroups(c)
}

func ListSmartRoutingMetrics(c *gin.Context) {
	limit := parseRoutingLimit(c, 100)
	var metrics []model.RoutingChannelMetric
	if err := model.DB.Order("bucket_ts desc").Limit(limit).Find(&metrics).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, metrics)
}

func ListSmartRoutingSnapshots(c *gin.Context) {
	limit := parseRoutingLimit(c, 100)
	var snapshots []model.RoutingCostSnapshot
	if err := model.DB.Order("snapshot_ts desc").Limit(limit).Find(&snapshots).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, snapshots)
}

func ListSmartRoutingBreakers(c *gin.Context) {
	limit := parseRoutingLimit(c, 100)
	var states []model.RoutingBreakerState
	if err := model.DB.Order("updated_time desc").Limit(limit).Find(&states).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, states)
}

func ResetSmartRoutingBreaker(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid breaker id")
		return
	}
	legacyRequest := c.GetHeader("Idempotency-Key") == ""
	var state model.RoutingBreakerState
	var resolved channelrouting.BreakerResetResolvedTarget
	if legacyRequest {
		if err = model.DB.WithContext(c.Request.Context()).Where("id = ?", id).First(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				operation, command, replayErr := model.GetLatestLegacyRoutingBreakerResetContext(c.Request.Context(), id)
				if replayErr != nil {
					if errors.Is(replayErr, gorm.ErrRecordNotFound) {
						c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "routing breaker not found"})
						return
					}
					writeChannelRoutingBreakerResetError(c, replayErr)
					return
				}
				key := legacyBreakerResetIdempotencyKey(id, command.LegacyGeneration, command.TargetKey)
				if key == "" {
					writeChannelRoutingBreakerResetError(c, model.ErrRoutingBreakerResetInvalid)
					return
				}
				view, viewErr := channelRoutingOperationViewFromModel(operation)
				if viewErr != nil {
					writeChannelRoutingBreakerResetError(c, viewErr)
					return
				}
				c.Header("Idempotency-Key", key)
				common.ApiSuccess(c, view)
				return
			}
			common.ApiError(c, err)
			return
		}
		if state.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
			writeChannelRoutingBreakerResetError(c, model.ErrRoutingBreakerResetInvalid)
			return
		}
		resolved, err = channelrouting.ResolveLegacyMemberBreakerResetTarget(
			state.ChannelID, state.ModelName, state.Group,
		)
		if err != nil {
			writeChannelRoutingBreakerResetError(c, err)
			return
		}
		targetKey, targetErr := model.RoutingBreakerResetTargetKey(resolved.Target)
		if targetErr != nil {
			writeChannelRoutingBreakerResetError(c, targetErr)
			return
		}
		key := legacyBreakerResetIdempotencyKey(id, state.ResetGeneration, targetKey)
		if key == "" {
			writeChannelRoutingBreakerResetError(c, model.ErrRoutingBreakerResetInvalid)
			return
		}
		c.Request.Header.Set("Idempotency-Key", key)
	}
	identity, ok := requireChannelRoutingOperationIdempotency(c, model.RoutingOperationTypeBreakerReset, struct {
		LegacyBreakerID int `json:"legacy_breaker_id"`
	}{LegacyBreakerID: id})
	if !ok {
		return
	}
	operation, lookupErr := model.GetRoutingOperationByRequestIdentityContext(c.Request.Context(), identity)
	if lookupErr == nil {
		if operation.OperationType != model.RoutingOperationTypeBreakerReset {
			writeChannelRoutingBreakerResetError(c, model.ErrRoutingOperationIdempotencyConflict)
			return
		}
		view, viewErr := channelRoutingOperationViewFromModel(operation)
		if viewErr != nil {
			writeChannelRoutingBreakerResetError(c, viewErr)
			return
		}
		common.ApiSuccess(c, view)
		return
	}
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		writeChannelRoutingBreakerResetError(c, lookupErr)
		return
	}
	if !legacyRequest {
		if err = model.DB.WithContext(c.Request.Context()).Where("id = ?", id).First(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "routing breaker not found"})
				return
			}
			common.ApiError(c, err)
			return
		}
		if state.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
			writeChannelRoutingBreakerResetError(c, model.ErrRoutingBreakerResetInvalid)
			return
		}
		resolved, err = channelrouting.ResolveLegacyMemberBreakerResetTarget(
			state.ChannelID, state.ModelName, state.Group,
		)
		if err != nil {
			writeChannelRoutingBreakerResetError(c, err)
			return
		}
	}
	spec := model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeBreakerReset, EvaluationHash: identity.PayloadHash,
		SubjectType: model.RoutingOperationSubjectMemberBreaker,
		SubjectID:   int64(resolved.Target.MemberID), PoolID: resolved.Target.PoolID,
		ExpectedRevision: resolved.ExpectedRevision, ExpectedActivationID: resolved.ExpectedActivationID,
		ActorID: common.GetContextKeyInt(c, constant.ContextKeyUserId), Reason: "legacy manual breaker reset",
		RequestKeyHash: identity.KeyHash, RequestPayloadHash: identity.PayloadHash,
	}
	if legacyRequest {
		operation, _, err = model.CreateLegacyRoutingBreakerResetOperationContext(
			c.Request.Context(), spec, resolved.Target, id, state.ResetGeneration,
		)
	} else {
		operation, _, err = model.CreateRoutingBreakerResetOperationContext(
			c.Request.Context(), spec, resolved.Target,
		)
	}
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	view, err := channelRoutingOperationViewFromModel(operation)
	if err != nil {
		writeChannelRoutingBreakerResetError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "message": "", "data": view})
}

func legacyBreakerResetIdempotencyKey(breakerID int, generation int64, targetKey string) string {
	if breakerID <= 0 || generation < 0 || len(targetKey) != 64 {
		return ""
	}
	return "legacy-breaker-reset-" + strconv.Itoa(breakerID) + "-" +
		strconv.FormatInt(generation, 10) + "-" + targetKey
}

func EnqueueSmartRoutingSync(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeRoutingCostSync, nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"task":    task,
		"created": created,
	})
}

func ListSmartRoutingAgentRecommendations(c *gin.Context) {
	limit := parseRoutingLimit(c, 100)
	var recommendations []model.RoutingAgentRecommendation
	if err := model.DB.Order("id desc").Limit(limit).Find(&recommendations).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, recommendations)
}

func ApproveSmartRoutingAgentRecommendation(c *gin.Context) {
	updateRoutingAgentRecommendationStatus(c, "approved")
}

func RejectSmartRoutingAgentRecommendation(c *gin.Context) {
	updateRoutingAgentRecommendationStatus(c, "rejected")
}

func buildRoutingBindingView(binding model.RoutingChannelBinding, credentials model.RoutingCredentials) routingBindingView {
	credentials = credentials.ForUpstream(binding.UpstreamType)
	var lastSyncError *string
	if binding.LastSyncError != nil {
		message := common.SanitizeErrorMessage(*binding.LastSyncError, routingCredentialSecrets(credentials)...)
		if message == "" {
			message = "routing cost sync failed"
		}
		lastSyncError = &message
	}
	egressCIDRs, egressErr := binding.GetEgressAllowedPrivateCIDRs()
	view := routingBindingView{
		ID:                        binding.ID,
		ChannelID:                 binding.ChannelID,
		ETag:                      channelRoutingCostBindingETag(binding),
		UpstreamType:              binding.UpstreamType,
		BaseURL:                   binding.BaseURL,
		UpstreamGroup:             binding.UpstreamGroup,
		ServesClaudeCode:          binding.ServesClaudeCode,
		EgressAllowedPrivateCIDRs: egressCIDRs,
		NewAPIUserID:              binding.NewAPIUserID,
		Enabled:                   binding.Enabled,
		SyncFailureCount:          binding.SyncFailureCount,
		SyncBackoffUntil:          binding.SyncBackoffUntil,
		LastSyncError:             lastSyncError,
		CredentialMasks: routingCredentialMasks{
			NewAPIAccessToken:  maskRoutingToken(credentials.NewAPIAccessToken),
			GatewayAPIKey:      maskRoutingToken(credentials.GatewayAPIKey),
			Sub2APIEmail:       maskRoutingEmail(credentials.Sub2APIEmail),
			Sub2APIPassword:    maskRoutingPassword(credentials.Sub2APIPassword),
			Sub2APIToken:       maskRoutingToken(credentials.Sub2APIToken),
			CustomCAConfigured: strings.TrimSpace(credentials.CustomCAPEM) != "",
		},
		CreatedTime: binding.CreatedTime,
		UpdatedTime: binding.UpdatedTime,
	}
	if egressErr != nil {
		view.EgressPolicyError = common.SanitizeErrorMessage(egressErr.Error())
	}
	return view
}

func buildRoutingCredentials(request routingBindingRequest, base ...model.RoutingCredentials) model.RoutingCredentials {
	credentials := model.RoutingCredentials{}
	if len(base) > 0 {
		credentials = base[0].ForUpstream(request.UpstreamType)
	}
	if request.UpstreamType == model.RoutingUpstreamTypeNewAPI && request.Credentials.NewAPIAccessToken != nil {
		credentials.NewAPIAccessToken = strings.TrimSpace(*request.Credentials.NewAPIAccessToken)
	}
	if request.Credentials.GatewayAPIKey != nil {
		credentials.GatewayAPIKey = strings.TrimSpace(*request.Credentials.GatewayAPIKey)
	}
	if request.UpstreamType == model.RoutingUpstreamTypeSub2API && request.Credentials.Sub2APIEmail != nil {
		credentials.Sub2APIEmail = strings.TrimSpace(*request.Credentials.Sub2APIEmail)
	}
	if request.UpstreamType == model.RoutingUpstreamTypeSub2API && request.Credentials.Sub2APIPassword != nil {
		credentials.Sub2APIPassword = *request.Credentials.Sub2APIPassword
	}
	if request.UpstreamType == model.RoutingUpstreamTypeSub2API && request.Credentials.Sub2APIToken != nil {
		credentials.Sub2APIToken = strings.TrimSpace(*request.Credentials.Sub2APIToken)
	}
	if request.Credentials.CustomCAPEM != nil {
		credentials.CustomCAPEM = strings.TrimSpace(*request.Credentials.CustomCAPEM)
	}
	return credentials.ForUpstream(request.UpstreamType)
}

func routingBindingCredentialsForUpdate(
	expected model.RoutingChannelBinding,
	updated model.RoutingChannelBinding,
	request routingBindingRequest,
) (model.RoutingCredentials, error) {
	replacement := buildRoutingCredentials(request)
	if expected.UpstreamType != updated.UpstreamType {
		return replacement, nil
	}
	existing, err := expected.GetCredentials()
	if err == nil {
		return buildRoutingCredentials(request, existing), nil
	}
	if replacement.ReadyForUpstream(updated.UpstreamType) {
		return replacement, nil
	}
	return model.RoutingCredentials{}, err
}

func routingBindingViewWithStoredCredentials(binding model.RoutingChannelBinding) (routingBindingView, error) {
	credentials, err := binding.GetCredentials()
	if err != nil {
		return routingBindingView{}, err
	}
	return buildRoutingBindingView(binding, credentials), nil
}

func routingBindingFromRequest(request routingBindingRequest) (model.RoutingChannelBinding, error) {
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	servesClaudeCode := request.ServesClaudeCode
	if request.UpstreamType != model.RoutingUpstreamTypeSub2API {
		servesClaudeCode = false
	}
	newAPIUserID := request.NewAPIUserID
	if request.UpstreamType != model.RoutingUpstreamTypeNewAPI {
		newAPIUserID = nil
	}
	binding := model.RoutingChannelBinding{
		ChannelID:        request.ChannelID,
		UpstreamType:     strings.TrimSpace(request.UpstreamType),
		BaseURL:          strings.TrimSpace(request.BaseURL),
		UpstreamGroup:    strings.TrimSpace(request.UpstreamGroup),
		ServesClaudeCode: servesClaudeCode,
		NewAPIUserID:     newAPIUserID,
		Enabled:          enabled,
	}
	normalizedCIDRs, err := service.NormalizeRoutingCostEgressCIDRs(request.EgressAllowedPrivateCIDRs)
	if err != nil {
		return model.RoutingChannelBinding{}, err
	}
	if err := binding.SetEgressAllowedPrivateCIDRs(normalizedCIDRs); err != nil {
		return model.RoutingChannelBinding{}, err
	}
	return binding, nil
}

func validateRoutingBindingRequest(request routingBindingRequest, requireChannelID bool) error {
	if requireChannelID && request.ChannelID <= 0 {
		return routingBindingFieldError("channel_id", "required")
	}
	switch request.UpstreamType {
	case model.RoutingUpstreamTypeNewAPI, model.RoutingUpstreamTypeSub2API:
	default:
		return routingBindingFieldError("upstream_type", "unsupported")
	}
	if strings.TrimSpace(request.BaseURL) == "" {
		return routingBindingFieldError("base_url", "required")
	}
	normalizedCIDRs, err := service.NormalizeRoutingCostEgressCIDRs(request.EgressAllowedPrivateCIDRs)
	if err != nil {
		return routingBindingFieldError("egress_allowed_private_cidrs", "invalid")
	}
	if err := validateRoutingBaseURL(request.BaseURL, normalizedCIDRs); err != nil {
		return err
	}
	customCAPEM := ""
	if request.Credentials.CustomCAPEM != nil {
		customCAPEM = *request.Credentials.CustomCAPEM
	}
	if _, err := service.WithRoutingCostEgressPolicy(context.Background(), normalizedCIDRs, customCAPEM); err != nil {
		return routingBindingFieldError("custom_ca_pem", "invalid")
	}
	if strings.TrimSpace(request.UpstreamGroup) == "" {
		return routingBindingFieldError("upstream_group", "required")
	}
	return nil
}

func hasRoutingCredentials(credentials routingCredentialRequest) bool {
	return credentials.NewAPIAccessToken != nil ||
		credentials.GatewayAPIKey != nil ||
		credentials.Sub2APIEmail != nil ||
		credentials.Sub2APIPassword != nil ||
		credentials.Sub2APIToken != nil ||
		credentials.CustomCAPEM != nil
}

func validateRoutingBaseURL(value string, allowedPrivateCIDRs []string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return routingBindingFieldError("base_url", "invalid")
	}
	if parsed.Scheme != "https" {
		return routingBindingFieldError("base_url", "insecure_scheme")
	}
	if parsed.User != nil {
		return routingBindingFieldError("base_url", "credentials_not_allowed")
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "key") ||
			strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "password") ||
			strings.Contains(normalized, "authorization") {
			return routingBindingFieldError("base_url", "sensitive_query_not_allowed")
		}
	}
	if err = service.ValidateRoutingCostURLWithEgressPolicy(parsed.String(), allowedPrivateCIDRs); err != nil {
		return routingBindingFieldError("base_url", "unsafe_target")
	}
	return nil
}

func maskRoutingToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return "****" + value[len(value)-4:]
}

func maskRoutingPassword(value string) string {
	if value == "" {
		return ""
	}
	return "********"
}

func maskRoutingEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.SplitN(value, "@", 2)
	if len(parts) != 2 {
		return maskRoutingToken(value)
	}
	local := parts[0]
	if len(local) <= 2 {
		return strings.Repeat("*", len(local)) + "@" + parts[1]
	}
	return local[:1] + strings.Repeat("*", len(local)-2) + local[len(local)-1:] + "@" + parts[1]
}

func parseRoutingChannelID(c *gin.Context) (int, bool) {
	channelID, err := strconv.Atoi(c.Param("channelId"))
	if err != nil || channelID <= 0 {
		common.ApiErrorMsg(c, "invalid channel id")
		return 0, false
	}
	return channelID, true
}

func loadRoutingBinding(c *gin.Context) (*model.RoutingChannelBinding, bool) {
	channelID, ok := parseRoutingChannelID(c)
	if !ok {
		return nil, false
	}
	var binding model.RoutingChannelBinding
	err := model.DB.Where("channel_id = ?", channelID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "routing binding not found"})
		return nil, false
	}
	if err != nil {
		common.ApiError(c, err)
		return nil, false
	}
	return &binding, true
}

func routingBindingForAction(c *gin.Context) (*model.RoutingChannelBinding, bool) {
	var request routingBindingRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := common.DecodeJson(c.Request.Body, &request); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid routing binding"})
			return nil, false
		}
	}

	rawChannelID := strings.TrimSpace(c.Param("channelId"))
	if rawChannelID == "new" {
		if !requireRoutingSensitiveWriteForInline(c) {
			return nil, false
		}
		if err := validateRoutingBindingRequest(request, true); err != nil {
			common.ApiError(c, err)
			return nil, false
		}
		return routingBindingFromInlineRequest(c, request)
	}

	channelID, ok := parseRoutingChannelID(c)
	if !ok {
		return nil, false
	}
	if request.ChannelID == 0 {
		request.ChannelID = channelID
	}
	if hasInlineRoutingBindingRequest(request) {
		if !requireRoutingSensitiveWriteForInline(c) {
			return nil, false
		}
		request.ChannelID = channelID
		if err := validateRoutingBindingRequest(request, true); err != nil {
			common.ApiError(c, err)
			return nil, false
		}
		return routingBindingFromInlineRequest(c, request)
	}

	return loadRoutingBinding(c)
}

func requireRoutingSensitiveWriteForInline(c *gin.Context) bool {
	if authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelSensitiveWrite) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "insufficient privilege"})
	return false
}

func hasInlineRoutingBindingRequest(request routingBindingRequest) bool {
	return request.UpstreamType != "" ||
		request.BaseURL != "" ||
		request.UpstreamGroup != "" ||
		request.NewAPIUserID != nil ||
		hasRoutingCredentials(request.Credentials)
}

func routingBindingFromInlineRequest(c *gin.Context, request routingBindingRequest) (*model.RoutingChannelBinding, bool) {
	binding, err := routingBindingFromRequest(request)
	if err != nil {
		common.ApiError(c, err)
		return nil, false
	}
	if hasRoutingCredentials(request.Credentials) {
		if err := binding.SetCredentials(buildRoutingCredentials(request)); err != nil {
			common.ApiError(c, err)
			return nil, false
		}
	}
	return &binding, true
}

func parseRoutingLimit(c *gin.Context, defaultLimit int) int {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil || limit <= 0 {
		return defaultLimit
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func updateRoutingAgentRecommendationStatus(c *gin.Context, status string) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid recommendation id")
		return
	}
	result := model.DB.Model(&model.RoutingAgentRecommendation{}).Where("id = ?", id).Update("status", status)
	if result.Error != nil {
		common.ApiError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "recommendation not found"})
		return
	}
	common.ApiSuccess(c, nil)
}
