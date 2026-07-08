package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type routingCredentialRequest struct {
	NewAPIAccessToken string `json:"new_api_access_token"`
	GatewayAPIKey     string `json:"gateway_api_key"`
	Sub2APIEmail      string `json:"sub2api_email"`
	Sub2APIPassword   string `json:"sub2api_password"`
	Sub2APIToken      string `json:"sub2api_token"`
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
	updated := smart_routing_setting.UpdateSetting(request)
	values, err := config.ConfigToMap(updated)
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
	common.ApiSuccess(c, updated)
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
			view.CredentialError = err.Error()
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
		view.CredentialError = err.Error()
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
	if err := model.DB.Create(&binding).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := routingBindingViewWithStoredCredentials(binding)
	if err != nil {
		view = buildRoutingBindingView(binding, model.RoutingCredentials{})
		view.CredentialError = err.Error()
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
	if !hasRoutingCredentials(request.Credentials) {
		updated.EncCredentials = binding.EncCredentials
	} else if err := updated.SetCredentials(buildRoutingCredentials(request)); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.DB.Save(&updated).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := routingBindingViewWithStoredCredentials(updated)
	if err != nil {
		view = buildRoutingBindingView(updated, model.RoutingCredentials{})
		view.CredentialError = err.Error()
	}
	common.ApiSuccess(c, view)
}

func DeleteSmartRoutingBinding(c *gin.Context) {
	channelID, ok := parseRoutingChannelID(c)
	if !ok {
		return
	}
	if err := model.DB.Where("channel_id = ?", channelID).Delete(&model.RoutingChannelBinding{}).Error; err != nil {
		common.ApiError(c, err)
		return
	}
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
	var state model.RoutingBreakerState
	if err = model.DB.Where("id = ?", id).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "routing breaker not found"})
			return
		}
		common.ApiError(c, err)
		return
	}
	if err = model.DB.Delete(&model.RoutingBreakerState{}, id).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	routingbreaker.ResetDefaultKey(routingbreaker.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	})
	common.ApiSuccess(c, nil)
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
	return routingBindingView{
		ID:               binding.ID,
		ChannelID:        binding.ChannelID,
		UpstreamType:     binding.UpstreamType,
		BaseURL:          binding.BaseURL,
		UpstreamGroup:    binding.UpstreamGroup,
		ServesClaudeCode: binding.ServesClaudeCode,
		NewAPIUserID:     binding.NewAPIUserID,
		Enabled:          binding.Enabled,
		SyncBackoffUntil: binding.SyncBackoffUntil,
		LastSyncError:    binding.LastSyncError,
		CredentialMasks: routingCredentialMasks{
			NewAPIAccessToken: maskRoutingToken(credentials.NewAPIAccessToken),
			GatewayAPIKey:     maskRoutingToken(credentials.GatewayAPIKey),
			Sub2APIEmail:      credentials.Sub2APIEmail,
			Sub2APIPassword:   maskRoutingPassword(credentials.Sub2APIPassword),
			Sub2APIToken:      maskRoutingToken(credentials.Sub2APIToken),
		},
		CreatedTime: binding.CreatedTime,
		UpdatedTime: binding.UpdatedTime,
	}
}

func buildRoutingCredentials(request routingBindingRequest) model.RoutingCredentials {
	return model.RoutingCredentials{
		NewAPIAccessToken: strings.TrimSpace(request.Credentials.NewAPIAccessToken),
		GatewayAPIKey:     strings.TrimSpace(request.Credentials.GatewayAPIKey),
		Sub2APIEmail:      strings.TrimSpace(request.Credentials.Sub2APIEmail),
		Sub2APIPassword:   request.Credentials.Sub2APIPassword,
		Sub2APIToken:      strings.TrimSpace(request.Credentials.Sub2APIToken),
	}
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
	if strings.TrimSpace(request.UpstreamGroup) == "" {
		return errors.New("upstream_group is required")
	}
	return nil
}

func hasRoutingCredentials(credentials routingCredentialRequest) bool {
	return strings.TrimSpace(credentials.NewAPIAccessToken) != "" ||
		strings.TrimSpace(credentials.GatewayAPIKey) != "" ||
		strings.TrimSpace(credentials.Sub2APIEmail) != "" ||
		credentials.Sub2APIPassword != "" ||
		strings.TrimSpace(credentials.Sub2APIToken) != ""
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
	if c.Request.Body != nil {
		_ = common.DecodeJson(c.Request.Body, &request)
	}

	rawChannelID := strings.TrimSpace(c.Param("channelId"))
	if rawChannelID == "new" {
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
		request.ChannelID = channelID
		if err := validateRoutingBindingRequest(request, true); err != nil {
			common.ApiError(c, err)
			return nil, false
		}
		return routingBindingFromInlineRequest(c, request)
	}

	return loadRoutingBinding(c)
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
