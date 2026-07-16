package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const channelRoutingChannelConfigurationBodyMaxBytes = 16 << 10

type channelRoutingChannelConfigurationRequest struct {
	UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
	TrafficClass           string  `json:"traffic_class"`
	FailureDomainLabel     string  `json:"failure_domain_label"`
	ClearFailureDomain     bool    `json:"clear_failure_domain"`
}

type channelRoutingChannelConfigurationView struct {
	ChannelID              int     `json:"channel_id"`
	ChannelName            string  `json:"channel_name"`
	UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
	CostSource             string  `json:"cost_source"`
	CostConfirmed          bool    `json:"cost_confirmed"`
	TrafficClass           string  `json:"traffic_class"`
	FailureDomainStatus    string  `json:"failure_domain_status"`
	FailureDomainLabel     string  `json:"failure_domain_label"`
	EffectiveModelCount    int     `json:"effective_model_count"`
	CostBasisAvailable     bool    `json:"cost_basis_available"`
	Revision               int64   `json:"revision"`
	UpdatedBy              int     `json:"updated_by"`
	CreatedTime            int64   `json:"created_time"`
	UpdatedTime            int64   `json:"updated_time"`
	ETag                   string  `json:"etag"`
}

type channelRoutingChannelConfigurationList struct {
	Items    []channelRoutingChannelConfigurationView `json:"items"`
	Total    int64                                    `json:"total"`
	Page     int                                      `json:"page"`
	PageSize int                                      `json:"page_size"`
}

func ListChannelRoutingChannelConfigurations(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	costConfirmed, err := parseChannelRoutingChannelConfigurationOptionalBool(c.Query("cost_confirmed"))
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_filter", "invalid channel configuration filter", err)
		return
	}
	trafficClass := strings.ToLower(strings.TrimSpace(c.Query("traffic_class")))
	costSource := strings.ToLower(strings.TrimSpace(c.Query("cost_source")))
	configurations, total, err := model.ListRoutingChannelConfigurationsContext(
		c.Request.Context(),
		model.RoutingChannelConfigurationFilter{
			Search: strings.TrimSpace(c.Query("search")), CostConfirmed: costConfirmed,
			TrafficClass: trafficClass, CostSource: costSource,
		},
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if errors.Is(err, model.ErrRoutingChannelConfigurationInvalid) {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_filter", "invalid channel configuration filter", err)
		return
	}
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_list_failed", "failed to load channel configurations", err)
		return
	}
	views, err := channelRoutingChannelConfigurationViews(c.Request.Context(), configurations)
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_list_failed", "failed to load channel configurations", err)
		return
	}
	common.ApiSuccess(c, channelRoutingChannelConfigurationList{Items: views, Total: total, Page: page, PageSize: pageSize})
}

func GetChannelRoutingChannelConfiguration(c *gin.Context) {
	configuration, ok := loadChannelRoutingChannelConfiguration(c)
	if !ok {
		return
	}
	views, err := channelRoutingChannelConfigurationViews(c.Request.Context(), []model.RoutingChannelConfiguration{configuration})
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_load_failed", "failed to load channel configuration", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, view)
}

func UpdateChannelRoutingChannelConfiguration(c *gin.Context) {
	expected, ok := loadChannelRoutingChannelConfiguration(c)
	if !ok {
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeChannelRoutingChannelConfigurationError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", model.ErrRoutingChannelConfigurationChanged)
		return
	}
	parsedETag, err := channelrouting.ParseChannelConfigurationETag(ifMatch)
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_if_match", "invalid If-Match channel configuration tag", err)
		return
	}
	currentETag, err := channelrouting.ChannelConfigurationETag(expected)
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_load_failed", "failed to load channel configuration", err)
		return
	}
	if parsedETag.ChannelID != expected.ChannelID || parsedETag.Revision != expected.Revision || ifMatch != currentETag {
		writeChannelRoutingChannelConfigurationConflict(c, expected.ChannelID)
		return
	}
	request, err := decodeChannelRoutingChannelConfiguration(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_channel_configuration"
		message := "invalid channel configuration"
		if errors.Is(err, errChannelRoutingChannelConfigurationBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "channel_configuration_too_large"
			message = "channel configuration request is too large"
		}
		writeChannelRoutingChannelConfigurationError(c, status, code, message, err)
		return
	}
	request.TrafficClass = strings.ToLower(strings.TrimSpace(request.TrafficClass))
	label, _, err := model.NormalizeRoutingFailureDomainLabel(request.FailureDomainLabel)
	if err != nil || request.TrafficClass != model.RoutingChannelTrafficClassAll &&
		request.TrafficClass != model.RoutingChannelTrafficClassClaudeCodeOnly ||
		math.IsNaN(request.UpstreamCostMultiplier) || math.IsInf(request.UpstreamCostMultiplier, 0) ||
		request.UpstreamCostMultiplier < 0 || request.UpstreamCostMultiplier > model.RoutingChannelUpstreamCostMultiplierMaximum {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_channel_configuration", "invalid channel configuration", model.ErrRoutingChannelConfigurationInvalid)
		return
	}
	if request.UpstreamCostMultiplier == 0 {
		request.UpstreamCostMultiplier = 0
	}
	mutation, err := model.UpdateRoutingChannelConfigurationContext(
		c.Request.Context(), expected, request.UpstreamCostMultiplier, request.TrafficClass, label,
		request.ClearFailureDomain, c.GetInt("id"),
	)
	if errors.Is(err, model.ErrRoutingChannelConfigurationChanged) {
		writeChannelRoutingChannelConfigurationConflict(c, expected.ChannelID)
		return
	}
	if errors.Is(err, model.ErrRoutingChannelConfigurationInvalid) {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_channel_configuration", "invalid channel configuration", err)
		return
	}
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_update_failed", "failed to update channel configuration", err)
		return
	}
	channelrouting.ApplyCommittedRoutingChannelConfiguration(mutation.Configuration)
	if _, publishErr := channelrouting.PublishRoutingChannelConfigurationOutboxByIDContext(c.Request.Context(), mutation.Outbox.ID); publishErr != nil {
		common.SysError("publish routing channel configuration event: " + common.SanitizeErrorMessage(publishErr.Error()))
	}
	views, err := channelRoutingChannelConfigurationViews(c.Request.Context(), []model.RoutingChannelConfiguration{mutation.Configuration})
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_load_failed", "failed to load updated channel configuration", err)
		return
	}
	view := views[0]
	c.Header("ETag", view.ETag)
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, view)
}

var errChannelRoutingChannelConfigurationBodyTooLarge = errors.New("channel routing channel configuration body is too large")

func decodeChannelRoutingChannelConfiguration(body io.Reader) (channelRoutingChannelConfigurationRequest, error) {
	if body == nil {
		return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingChannelConfigurationBodyMaxBytes+1))
	if err != nil || len(data) == 0 {
		return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
	}
	if len(data) > channelRoutingChannelConfigurationBodyMaxBytes {
		return channelRoutingChannelConfigurationRequest{}, errChannelRoutingChannelConfigurationBodyTooLarge
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil || fields == nil || len(fields) != 4 {
		return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
	}
	expectedTypes := map[string]string{
		"upstream_cost_multiplier": "number",
		"traffic_class":            "string",
		"failure_domain_label":     "string",
		"clear_failure_domain":     "boolean",
	}
	for field, expectedType := range expectedTypes {
		raw, exists := fields[field]
		if !exists || common.GetJsonType(raw) != expectedType {
			return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
		}
	}
	for field := range fields {
		if _, exists := expectedTypes[field]; !exists {
			return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
		}
	}
	var request channelRoutingChannelConfigurationRequest
	if common.Unmarshal(data, &request) != nil {
		return channelRoutingChannelConfigurationRequest{}, model.ErrRoutingChannelConfigurationInvalid
	}
	return request, nil
}

func loadChannelRoutingChannelConfiguration(c *gin.Context) (model.RoutingChannelConfiguration, bool) {
	channelID, err := strconv.Atoi(strings.TrimSpace(c.Param("channelId")))
	if err != nil || channelID <= 0 {
		writeChannelRoutingChannelConfigurationError(c, http.StatusBadRequest, "invalid_channel_id", "invalid channel id", model.ErrRoutingChannelConfigurationInvalid)
		return model.RoutingChannelConfiguration{}, false
	}
	configuration, err := model.GetRoutingChannelConfigurationContext(c.Request.Context(), channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeChannelRoutingChannelConfigurationError(c, http.StatusNotFound, "channel_configuration_not_found", "channel configuration not found", err)
		return model.RoutingChannelConfiguration{}, false
	}
	if err != nil {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_load_failed", "failed to load channel configuration", err)
		return model.RoutingChannelConfiguration{}, false
	}
	if !model.ValidRoutingChannelConfiguration(configuration) {
		writeChannelRoutingChannelConfigurationError(c, http.StatusInternalServerError, "channel_configuration_corrupt", "channel configuration is unavailable", model.ErrRoutingChannelConfigurationInvalid)
		return model.RoutingChannelConfiguration{}, false
	}
	return configuration, true
}

func channelRoutingChannelConfigurationViews(
	ctx context.Context,
	configurations []model.RoutingChannelConfiguration,
) ([]channelRoutingChannelConfigurationView, error) {
	channelIDs := make([]int, 0, len(configurations))
	for index := range configurations {
		if !model.ValidRoutingChannelConfiguration(configurations[index]) {
			return nil, model.ErrRoutingChannelConfigurationInvalid
		}
		channelIDs = append(channelIDs, configurations[index].ChannelID)
	}
	channels := make(map[int]model.Channel, len(channelIDs))
	if len(channelIDs) > 0 {
		var rows []model.Channel
		if err := model.DB.WithContext(ctx).Select("id", "name", "models").Where("id IN ?", channelIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for index := range rows {
			channels[rows[index].Id] = rows[index]
		}
	}
	views := make([]channelRoutingChannelConfigurationView, 0, len(configurations))
	for index := range configurations {
		configuration := configurations[index]
		channel, exists := channels[configuration.ChannelID]
		if !exists {
			return nil, gorm.ErrRecordNotFound
		}
		etag, err := channelrouting.ChannelConfigurationETag(configuration)
		if err != nil {
			return nil, err
		}
		modelCount, costBasisAvailable := channelrouting.ChannelConfigurationCostBasisSummary(channel.GetModels())
		views = append(views, channelRoutingChannelConfigurationView{
			ChannelID: configuration.ChannelID, ChannelName: channel.Name,
			UpstreamCostMultiplier: configuration.UpstreamCostMultiplier,
			CostSource:             configuration.CostSource, CostConfirmed: configuration.CostConfirmed,
			TrafficClass: configuration.TrafficClass, FailureDomainStatus: configuration.FailureDomainStatus,
			FailureDomainLabel: configuration.FailureDomainLabel, EffectiveModelCount: modelCount,
			CostBasisAvailable: costBasisAvailable, Revision: configuration.Revision,
			UpdatedBy: configuration.UpdatedBy, CreatedTime: configuration.CreatedTime,
			UpdatedTime: configuration.UpdatedTime, ETag: etag,
		})
	}
	return views, nil
}

func writeChannelRoutingChannelConfigurationConflict(c *gin.Context, channelID int) {
	current, err := model.GetRoutingChannelConfigurationContext(c.Request.Context(), channelID)
	if err == nil {
		views, viewErr := channelRoutingChannelConfigurationViews(c.Request.Context(), []model.RoutingChannelConfiguration{current})
		if viewErr == nil {
			view := views[0]
			c.Header("ETag", view.ETag)
			c.JSON(http.StatusConflict, gin.H{
				"success": false, "code": "channel_configuration_conflict", "message": "channel configuration changed",
				"conflict": gin.H{"current": view, "current_etag": view.ETag},
			})
			return
		}
	}
	c.JSON(http.StatusConflict, gin.H{
		"success": false, "code": "channel_configuration_conflict", "message": "channel configuration changed",
		"conflict": gin.H{"current": nil, "current_etag": ""},
	})
}

func writeChannelRoutingChannelConfigurationError(c *gin.Context, status int, code string, message string, err error) {
	if status >= http.StatusInternalServerError && err != nil {
		common.SysError(message + ": " + common.SanitizeErrorMessage(err.Error()))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}

func parseChannelRoutingChannelConfigurationOptionalBool(raw string) (*bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil || value != "true" && value != "false" {
		return nil, model.ErrRoutingChannelConfigurationInvalid
	}
	return &parsed, nil
}
