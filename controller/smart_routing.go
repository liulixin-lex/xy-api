package controller

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type routingCredentialRequest struct {
	NewAPIAccessToken *string `json:"new_api_access_token"`
	GatewayAPIKey     *string `json:"gateway_api_key"`
	Sub2APIEmail      *string `json:"sub2api_email"`
	Sub2APIPassword   *string `json:"sub2api_password"`
	Sub2APIToken      *string `json:"sub2api_token"`
}

type routingBindingRequest struct {
	ChannelID        int                      `json:"channel_id"`
	UpstreamType     string                   `json:"upstream_type"`
	BaseURL          string                   `json:"base_url"`
	UpstreamGroup    string                   `json:"upstream_group"`
	ServesClaudeCode bool                     `json:"serves_claude_code"`
	NewAPIUserID     *int                     `json:"new_api_user_id"`
	Enabled          *bool                    `json:"enabled"`
	Credentials      routingCredentialRequest `json:"credentials"`
}

type routingCredentialMasks struct {
	NewAPIAccessToken string `json:"new_api_access_token,omitempty"`
	GatewayAPIKey     string `json:"gateway_api_key,omitempty"`
	Sub2APIEmail      string `json:"sub2api_email,omitempty"`
	Sub2APIPassword   string `json:"sub2api_password,omitempty"`
	Sub2APIToken      string `json:"sub2api_token,omitempty"`
}

type routingBindingView struct {
	ID               int                    `json:"id"`
	ChannelID        int                    `json:"channel_id"`
	UpstreamType     string                 `json:"upstream_type"`
	BaseURL          string                 `json:"base_url"`
	UpstreamGroup    string                 `json:"upstream_group"`
	ServesClaudeCode bool                   `json:"serves_claude_code"`
	NewAPIUserID     *int                   `json:"new_api_user_id,omitempty"`
	Enabled          bool                   `json:"enabled"`
	SyncFailureCount int                    `json:"sync_failure_count"`
	SyncBackoffUntil int64                  `json:"sync_backoff_until"`
	LastSyncError    *string                `json:"last_sync_error,omitempty"`
	CredentialMasks  routingCredentialMasks `json:"credential_masks"`
	CredentialError  string                 `json:"credential_error,omitempty"`
	CreatedTime      int64                  `json:"created_time"`
	UpdatedTime      int64                  `json:"updated_time"`
}

func GetSmartRoutingSettings(c *gin.Context) {
	common.ApiSuccess(c, smart_routing_setting.GetSetting())
}

func UpdateSmartRoutingSettings(c *gin.Context) {
	var request smart_routing_setting.SmartRoutingSetting
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "invalid smart routing settings")
		return
	}
	normalized := smart_routing_setting.Normalize(request)
	values, err := config.ConfigToMap(normalized)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	persisted := make(map[string]string, len(values))
	for key, value := range values {
		persisted["smart_routing_setting."+key] = value
	}
	if err = model.UpdateOptionsBulk(persisted); err != nil {
		common.ApiError(c, err)
		return
	}
	effective := smart_routing_setting.GetSetting()
	syncRoutingBreakerConfigFromSetting(effective)
	common.ApiSuccess(c, effective)
}

func ListSmartRoutingBindings(c *gin.Context) {
	var bindings []model.RoutingChannelBinding
	if err := model.DB.Order("channel_id asc").Find(&bindings).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	views := make([]routingBindingView, 0, len(bindings))
	for _, binding := range bindings {
		view, err := routingBindingViewWithStoredCredentials(binding)
		if err != nil {
			view = buildRoutingBindingView(binding, model.RoutingCredentials{})
			view.CredentialError = common.SanitizeErrorMessage(err.Error())
		}
		views = append(views, view)
	}
	common.ApiSuccess(c, views)
}

func GetSmartRoutingBinding(c *gin.Context) {
	binding, ok := loadRoutingBinding(c)
	if !ok {
		return
	}
	view, err := routingBindingViewWithStoredCredentials(*binding)
	if err != nil {
		view = buildRoutingBindingView(*binding, model.RoutingCredentials{})
		view.CredentialError = common.SanitizeErrorMessage(err.Error())
	}
	common.ApiSuccess(c, view)
}

func CreateSmartRoutingBinding(c *gin.Context) {
	var request routingBindingRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "invalid routing binding")
		return
	}
	if err := validateRoutingBindingRequest(request, true); err != nil {
		common.ApiError(c, err)
		return
	}
	binding := routingBindingFromRequest(request)
	if hasRoutingCredentials(request.Credentials) {
		if err := binding.SetCredentials(buildRoutingCredentials(request)); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	requestContext := c.Request.Context()
	if err := smartRoutingRuntimeStateMu.LockContext(requestContext); err != nil {
		common.ApiError(c, err)
		return
	}
	err := model.DB.WithContext(requestContext).Create(&binding).Error
	smartRoutingRuntimeStateMu.Unlock()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := routingBindingViewWithStoredCredentials(binding)
	if err != nil {
		view = buildRoutingBindingView(binding, model.RoutingCredentials{})
		view.CredentialError = common.SanitizeErrorMessage(err.Error())
	}
	common.ApiSuccess(c, view)
}

func UpdateSmartRoutingBinding(c *gin.Context) {
	binding, ok := loadRoutingBinding(c)
	if !ok {
		return
	}
	var request routingBindingRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "invalid routing binding")
		return
	}
	request.ChannelID = binding.ChannelID
	if err := validateRoutingBindingRequest(request, false); err != nil {
		common.ApiError(c, err)
		return
	}
	updated := routingBindingFromRequest(request)
	updated.ID = binding.ID
	updated.ChannelID = binding.ChannelID
	updated.CreatedTime = binding.CreatedTime
	updated.EncCredentials = binding.EncCredentials
	updated.KeyVersion = binding.KeyVersion
	oldSub2APIAuthKey := newRoutingSub2APIAuthKey(*binding, model.RoutingCredentials{})
	retireOldSub2APIAuth := binding.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		updated.UpstreamType != model.RoutingUpstreamTypeSub2API
	if hasRoutingCredentials(request.Credentials) {
		existingCredentials, err := binding.GetCredentials()
		if err != nil {
			common.ApiError(c, err)
			return
		}
		updatedCredentials := buildRoutingCredentials(request, existingCredentials)
		if err := updated.SetCredentials(updatedCredentials); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	newSub2APIAuthKey := newRoutingSub2APIAuthKey(updated, model.RoutingCredentials{})
	authKeyChanged := oldSub2APIAuthKey != newSub2APIAuthKey
	if binding.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		updated.UpstreamType == model.RoutingUpstreamTypeSub2API && authKeyChanged {
		retireOldSub2APIAuth = true
	}
	activateNewSub2APIAuth := updated.UpstreamType == model.RoutingUpstreamTypeSub2API &&
		(binding.UpstreamType != model.RoutingUpstreamTypeSub2API || authKeyChanged)
	requestContext := c.Request.Context()
	if err := smartRoutingRuntimeStateMu.LockContext(requestContext); err != nil {
		common.ApiError(c, err)
		return
	}
	activationFence := routingSub2APIJWTActivationFence{}
	if activateNewSub2APIAuth {
		var err error
		activationFence, err = prepareRoutingSub2APIJWTActivation(requestContext, updated)
		if err != nil {
			smartRoutingRuntimeStateMu.Unlock()
			common.ApiError(c, err)
			return
		}
	}
	err := model.UpdateRoutingChannelBindingAndInvalidateCostContext(requestContext, *binding, &updated)
	if err == nil {
		routinghotcache.ClearCostChannel(updated.ChannelID)
		if retireOldSub2APIAuth {
			invalidateRoutingSub2APIJWT(requestContext, *binding)
		}
		if activateNewSub2APIAuth {
			activateRoutingSub2APIJWT(requestContext, activationFence)
		}
	}
	smartRoutingRuntimeStateMu.Unlock()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := routingBindingViewWithStoredCredentials(updated)
	if err != nil {
		view = buildRoutingBindingView(updated, model.RoutingCredentials{})
		view.CredentialError = common.SanitizeErrorMessage(err.Error())
	}
	common.ApiSuccess(c, view)
}

func DeleteSmartRoutingBinding(c *gin.Context) {
	channelID, ok := parseRoutingChannelID(c)
	if !ok {
		return
	}
	requestContext := c.Request.Context()
	if err := smartRoutingRuntimeStateMu.LockContext(requestContext); err != nil {
		common.ApiError(c, err)
		return
	}
	defer smartRoutingRuntimeStateMu.Unlock()
	deletedBinding := model.RoutingChannelBinding{ChannelID: channelID}
	if err := model.DB.WithContext(requestContext).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", channelID).Take(&deletedBinding).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.RoutingChannelBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.RoutingCostSnapshot{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.RoutingBreakerState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.RoutingChannelMetric{}).Error; err != nil {
			return err
		}
		return tx.Where("channel_id = ?", channelID).Delete(&model.RoutingChannelHealthState{}).Error
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	invalidateRoutingSub2APIJWT(requestContext, deletedBinding)
	routingmetrics.ClearChannel(channelID)
	routingbreaker.ClearDefaultChannelWithCache(channelID, routinghotcache.ClearChannel)
	common.ApiSuccess(c, nil)
}

func TestSmartRoutingBinding(c *gin.Context) {
	binding, ok := routingBindingForAction(c)
	if !ok {
		return
	}
	payload, err := fetchRoutingPricingPayload(c.Request.Context(), *binding)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"channel_id":       binding.ChannelID,
		"upstream_type":    binding.UpstreamType,
		"credential_ready": binding.EncCredentials != nil && *binding.EncCredentials != "",
		"groups":           routingPricingGroups(payload),
		"model_count":      len(payload.Data),
		"pricing_version":  payload.PricingVersion,
	})
}

func LoadSmartRoutingBindingGroups(c *gin.Context) {
	binding, ok := routingBindingForAction(c)
	if !ok {
		return
	}
	payload, err := fetchRoutingPricingPayload(c.Request.Context(), *binding)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"channel_id":      binding.ChannelID,
		"upstream_type":   binding.UpstreamType,
		"upstream_group":  binding.UpstreamGroup,
		"groups":          routingPricingGroups(payload),
		"requires_sync":   false,
		"sync_task_type":  model.SystemTaskTypeRoutingCostSync,
		"serves_claude":   binding.ServesClaudeCode,
		"credential_test": binding.EncCredentials != nil && *binding.EncCredentials != "",
		"model_count":     len(payload.Data),
		"pricing_version": payload.PricingVersion,
	})
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
	var lastSyncError *string
	if binding.LastSyncError != nil {
		message := common.SanitizeErrorMessage(*binding.LastSyncError, routingCredentialSecrets(credentials)...)
		if message == "" {
			message = "routing cost sync failed"
		}
		lastSyncError = &message
	}
	return routingBindingView{
		ID:               binding.ID,
		ChannelID:        binding.ChannelID,
		UpstreamType:     binding.UpstreamType,
		BaseURL:          binding.BaseURL,
		UpstreamGroup:    binding.UpstreamGroup,
		ServesClaudeCode: binding.ServesClaudeCode,
		NewAPIUserID:     binding.NewAPIUserID,
		Enabled:          binding.Enabled,
		SyncFailureCount: binding.SyncFailureCount,
		SyncBackoffUntil: binding.SyncBackoffUntil,
		LastSyncError:    lastSyncError,
		CredentialMasks: routingCredentialMasks{
			NewAPIAccessToken: maskRoutingToken(credentials.NewAPIAccessToken),
			GatewayAPIKey:     maskRoutingToken(credentials.GatewayAPIKey),
			Sub2APIEmail:      maskRoutingEmail(credentials.Sub2APIEmail),
			Sub2APIPassword:   maskRoutingPassword(credentials.Sub2APIPassword),
			Sub2APIToken:      maskRoutingToken(credentials.Sub2APIToken),
		},
		CreatedTime: binding.CreatedTime,
		UpdatedTime: binding.UpdatedTime,
	}
}

func buildRoutingCredentials(request routingBindingRequest, base ...model.RoutingCredentials) model.RoutingCredentials {
	credentials := model.RoutingCredentials{}
	if len(base) > 0 {
		credentials = base[0]
	}
	if request.Credentials.NewAPIAccessToken != nil {
		credentials.NewAPIAccessToken = strings.TrimSpace(*request.Credentials.NewAPIAccessToken)
	}
	if request.Credentials.GatewayAPIKey != nil {
		credentials.GatewayAPIKey = strings.TrimSpace(*request.Credentials.GatewayAPIKey)
	}
	if request.Credentials.Sub2APIEmail != nil {
		credentials.Sub2APIEmail = strings.TrimSpace(*request.Credentials.Sub2APIEmail)
	}
	if request.Credentials.Sub2APIPassword != nil {
		credentials.Sub2APIPassword = *request.Credentials.Sub2APIPassword
	}
	if request.Credentials.Sub2APIToken != nil {
		credentials.Sub2APIToken = strings.TrimSpace(*request.Credentials.Sub2APIToken)
	}
	return credentials
}

func routingBindingViewWithStoredCredentials(binding model.RoutingChannelBinding) (routingBindingView, error) {
	credentials, err := binding.GetCredentials()
	if err != nil {
		return routingBindingView{}, err
	}
	return buildRoutingBindingView(binding, credentials), nil
}

func routingBindingFromRequest(request routingBindingRequest) model.RoutingChannelBinding {
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
	return model.RoutingChannelBinding{
		ChannelID:        request.ChannelID,
		UpstreamType:     strings.TrimSpace(request.UpstreamType),
		BaseURL:          strings.TrimSpace(request.BaseURL),
		UpstreamGroup:    strings.TrimSpace(request.UpstreamGroup),
		ServesClaudeCode: servesClaudeCode,
		NewAPIUserID:     newAPIUserID,
		Enabled:          enabled,
	}
}

func validateRoutingBindingRequest(request routingBindingRequest, requireChannelID bool) error {
	if requireChannelID && request.ChannelID <= 0 {
		return errors.New("channel_id is required")
	}
	switch request.UpstreamType {
	case model.RoutingUpstreamTypeNewAPI, model.RoutingUpstreamTypeSub2API:
	default:
		return errors.New("invalid upstream_type")
	}
	if strings.TrimSpace(request.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	if err := validateRoutingBaseURL(request.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(request.UpstreamGroup) == "" {
		return errors.New("upstream_group is required")
	}
	return nil
}

func hasRoutingCredentials(credentials routingCredentialRequest) bool {
	return credentials.NewAPIAccessToken != nil ||
		credentials.GatewayAPIKey != nil ||
		credentials.Sub2APIEmail != nil ||
		credentials.Sub2APIPassword != nil ||
		credentials.Sub2APIToken != nil
}

func validateRoutingBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid base_url")
	}
	if parsed.Scheme != "https" {
		return errors.New("base_url must use https")
	}
	if parsed.User != nil {
		return errors.New("base_url must not contain credentials")
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "key") ||
			strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "password") ||
			strings.Contains(normalized, "authorization") {
			return errors.New("base_url must not contain sensitive query parameters")
		}
	}
	if err = service.ValidateRoutingCostURL(parsed.String()); err != nil {
		return err
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
	binding := routingBindingFromRequest(request)
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
