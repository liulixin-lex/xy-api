package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
)

const channelRoutingRuntimeSettingsBodyMaxBytes = 64 << 10

type channelRoutingRuntimeSettingsFieldError struct {
	Field  string
	Reason string
}

func (err *channelRoutingRuntimeSettingsFieldError) Error() string {
	if err == nil {
		return model.ErrRoutingRuntimeSettingsInvalid.Error()
	}
	return fmt.Sprintf("%s: %s", err.Field, err.Reason)
}

type channelRoutingRuntimeSettingsView struct {
	Settings       smart_routing_setting.SmartRoutingSetting `json:"settings"`
	StoredSettings smart_routing_setting.SmartRoutingSetting `json:"stored_settings"`
	Revision       int64                                     `json:"revision"`
	DocumentHash   string                                    `json:"document_hash"`
	UpdatedBy      int                                       `json:"updated_by"`
	UpdatedTimeMs  int64                                     `json:"updated_time_ms"`
	ETag           string                                    `json:"etag"`
}

type channelRoutingControlAuditView struct {
	ID            int64  `json:"id"`
	SubjectType   string `json:"subject_type"`
	SubjectID     int64  `json:"subject_id"`
	Action        string `json:"action"`
	ActorID       int    `json:"actor_id"`
	BeforeHash    string `json:"before_hash,omitempty"`
	AfterHash     string `json:"after_hash,omitempty"`
	Summary       any    `json:"summary"`
	CreatedTimeMs int64  `json:"created_time_ms"`
}

func GetChannelRoutingRuntimeSettings(c *gin.Context) {
	getChannelRoutingRuntimeSettings(c, false)
}

func UpdateChannelRoutingRuntimeSettings(c *gin.Context) {
	updateChannelRoutingRuntimeSettings(c, false)
}

func getChannelRoutingRuntimeSettings(c *gin.Context, legacy bool) {
	view, err := loadChannelRoutingRuntimeSettingsView(c)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "runtime_settings_load_failed", "failed to load runtime settings", err)
		return
	}
	c.Header("ETag", view.ETag)
	if legacy {
		common.ApiSuccess(c, view.Settings)
		return
	}
	common.ApiSuccess(c, view)
}

func updateChannelRoutingRuntimeSettings(c *gin.Context, legacy bool) {
	current, err := loadChannelRoutingRuntimeSettingsView(c)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "runtime_settings_load_failed", "failed to load runtime settings", err)
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", model.ErrRoutingRuntimeSettingsConflict)
		return
	}
	revision, documentHash, err := parseChannelRoutingRuntimeSettingsETag(ifMatch)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_if_match", "invalid If-Match runtime settings tag", err)
		return
	}
	if revision != current.Revision || documentHash != current.DocumentHash || ifMatch != current.ETag {
		writeChannelRoutingRuntimeSettingsConflict(c, current)
		return
	}

	request, err := decodeChannelRoutingRuntimeSettings(c.Request.Body)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_runtime_settings", "invalid runtime settings", err)
		return
	}
	normalized := smart_routing_setting.Normalize(request)
	document, err := common.Marshal(normalized)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "runtime_settings_encode_failed", "failed to encode runtime settings", err)
		return
	}
	values, err := config.ConfigToMap(normalized)
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "runtime_settings_encode_failed", "failed to encode runtime settings", err)
		return
	}
	persisted := make(map[string]string, len(values))
	for key, value := range values {
		persisted["smart_routing_setting."+key] = value
	}
	nextHash := model.RoutingRuntimeSettingsDocumentHash(document)
	state, err := model.UpdateRoutingRuntimeSettingsContext(
		c.Request.Context(), current.Revision, current.DocumentHash,
		string(document), nextHash, persisted, c.GetInt("id"),
	)
	if errors.Is(err, model.ErrRoutingRuntimeSettingsConflict) {
		latest, latestErr := loadChannelRoutingRuntimeSettingsView(c)
		if latestErr != nil {
			writeChannelRoutingRuntimeSettingsError(c, http.StatusConflict, "runtime_settings_conflict", "runtime settings changed", err)
			return
		}
		writeChannelRoutingRuntimeSettingsConflict(c, latest)
		return
	}
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "runtime_settings_update_failed", "failed to update runtime settings", err)
		return
	}
	model.RefreshOptionsFromDatabase()
	effective := smart_routing_setting.UpdateSetting(normalized)
	syncRoutingBreakerConfigFromSetting(effective)
	view := channelRoutingRuntimeSettingsView{
		Settings: effective, StoredSettings: normalized,
		Revision: state.Revision, DocumentHash: state.DocumentHash,
		UpdatedBy: state.UpdatedBy, UpdatedTimeMs: state.UpdatedTimeMs,
	}
	view.ETag = channelRoutingRuntimeSettingsETag(state)
	c.Header("ETag", view.ETag)
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeRuntimeSettingsChanged, state.Revision, gin.H{
		"revision": state.Revision, "document_hash": state.DocumentHash, "updated_by": state.UpdatedBy,
	})
	if legacy {
		common.ApiSuccess(c, effective)
		return
	}
	common.ApiSuccess(c, view)
}

func ListChannelRoutingControlAudits(c *gin.Context) {
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > model.RoutingControlAuditMaxPageSize {
			writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_filter", "invalid control audit filter", model.ErrRoutingControlAuditInvalid)
			return
		}
		limit = parsed
	}
	beforeID, err := parseChannelRoutingAuditInt64(c.Query("before_id"))
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_filter", "invalid control audit filter", err)
		return
	}
	subjectID, err := parseChannelRoutingAuditInt64(c.Query("subject_id"))
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_filter", "invalid control audit filter", err)
		return
	}
	actorID := 0
	if raw := strings.TrimSpace(c.Query("actor_id")); raw != "" {
		actorID, err = strconv.Atoi(raw)
		if err != nil || actorID <= 0 {
			writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_filter", "invalid control audit filter", model.ErrRoutingControlAuditInvalid)
			return
		}
	}
	subjectType := strings.TrimSpace(c.Query("subject_type"))
	if subjectType != "" && subjectType != model.RoutingControlSubjectRuntimeSettings &&
		subjectType != model.RoutingControlSubjectCostBinding &&
		subjectType != model.RoutingControlSubjectChannelConfiguration {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusBadRequest, "invalid_filter", "invalid control audit filter", model.ErrRoutingControlAuditInvalid)
		return
	}
	audits, err := model.ListRoutingControlAuditsContext(c.Request.Context(), model.RoutingControlAuditFilter{
		BeforeID: beforeID, SubjectType: subjectType, SubjectID: subjectID, ActorID: actorID, Limit: limit,
	})
	if err != nil {
		writeChannelRoutingRuntimeSettingsError(c, http.StatusInternalServerError, "control_audit_list_failed", "failed to load control audits", err)
		return
	}
	views := make([]channelRoutingControlAuditView, 0, len(audits))
	for _, audit := range audits {
		var summary any
		if err := common.UnmarshalJsonStr(audit.SummaryJSON, &summary); err != nil {
			summary = map[string]any{"status": "unavailable"}
		}
		views = append(views, channelRoutingControlAuditView{
			ID: audit.ID, SubjectType: audit.SubjectType, SubjectID: audit.SubjectID,
			Action: audit.Action, ActorID: audit.ActorID,
			BeforeHash: audit.BeforeHash, AfterHash: audit.AfterHash,
			Summary: summary, CreatedTimeMs: audit.CreatedTimeMs,
		})
	}
	nextCursor := int64(0)
	if len(views) == limit {
		nextCursor = views[len(views)-1].ID
	}
	common.ApiSuccess(c, gin.H{"items": views, "next_before_id": nextCursor})
}

func loadChannelRoutingRuntimeSettingsView(c *gin.Context) (channelRoutingRuntimeSettingsView, error) {
	stored := smart_routing_setting.GetStoredSetting()
	document, err := common.Marshal(stored)
	if err != nil {
		return channelRoutingRuntimeSettingsView{}, err
	}
	hash := model.RoutingRuntimeSettingsDocumentHash(document)
	state, err := model.GetOrReconcileRoutingRuntimeSettingsStateContext(c.Request.Context(), string(document), hash)
	if err != nil {
		return channelRoutingRuntimeSettingsView{}, err
	}
	view := channelRoutingRuntimeSettingsView{
		Settings: smart_routing_setting.GetSetting(), StoredSettings: stored,
		Revision: state.Revision, DocumentHash: state.DocumentHash,
		UpdatedBy: state.UpdatedBy, UpdatedTimeMs: state.UpdatedTimeMs,
	}
	view.ETag = channelRoutingRuntimeSettingsETag(state)
	return view, nil
}

func decodeChannelRoutingRuntimeSettings(body io.Reader) (smart_routing_setting.SmartRoutingSetting, error) {
	if body == nil {
		return smart_routing_setting.SmartRoutingSetting{}, model.ErrRoutingRuntimeSettingsInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, channelRoutingRuntimeSettingsBodyMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > channelRoutingRuntimeSettingsBodyMaxBytes {
		return smart_routing_setting.SmartRoutingSetting{}, model.ErrRoutingRuntimeSettingsInvalid
	}
	var values map[string]json.RawMessage
	if err := common.Unmarshal(data, &values); err != nil || values == nil {
		return smart_routing_setting.SmartRoutingSetting{}, model.ErrRoutingRuntimeSettingsInvalid
	}

	settingType := reflect.TypeOf(smart_routing_setting.SmartRoutingSetting{})
	knownFields := make(map[string]struct{}, settingType.NumField())
	for index := 0; index < settingType.NumField(); index++ {
		field := settingType.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonName != "" && jsonName != "-" {
			knownFields[jsonName] = struct{}{}
		}
	}
	unknownFields := make([]string, 0)
	for field := range values {
		if _, ok := knownFields[field]; !ok {
			unknownFields = append(unknownFields, field)
		}
	}
	if len(unknownFields) > 0 {
		sort.Strings(unknownFields)
		return smart_routing_setting.SmartRoutingSetting{}, &channelRoutingRuntimeSettingsFieldError{
			Field: unknownFields[0], Reason: "unknown_field",
		}
	}

	request := smart_routing_setting.SmartRoutingSetting{}
	requestValue := reflect.ValueOf(&request).Elem()
	for index := 0; index < settingType.NumField(); index++ {
		field := settingType.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		raw, ok := values[jsonName]
		if !ok {
			return smart_routing_setting.SmartRoutingSetting{}, &channelRoutingRuntimeSettingsFieldError{
				Field: jsonName, Reason: "required",
			}
		}
		expectedType := "number"
		switch field.Type.Kind() {
		case reflect.Bool:
			expectedType = "boolean"
		case reflect.String:
			expectedType = "string"
		}
		if common.GetJsonType(raw) != expectedType {
			return smart_routing_setting.SmartRoutingSetting{}, &channelRoutingRuntimeSettingsFieldError{
				Field: jsonName, Reason: "expected_" + expectedType,
			}
		}
		decoded := reflect.New(field.Type)
		if err := common.Unmarshal(raw, decoded.Interface()); err != nil {
			reason := "invalid_value"
			if field.Type.Kind() >= reflect.Int && field.Type.Kind() <= reflect.Int64 {
				reason = "expected_integer"
			}
			return smart_routing_setting.SmartRoutingSetting{}, &channelRoutingRuntimeSettingsFieldError{
				Field: jsonName, Reason: reason,
			}
		}
		if field.Type.Kind() == reflect.Float32 || field.Type.Kind() == reflect.Float64 {
			value := decoded.Elem().Float()
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return smart_routing_setting.SmartRoutingSetting{}, &channelRoutingRuntimeSettingsFieldError{
					Field: jsonName, Reason: "must_be_finite",
				}
			}
		}
		requestValue.Field(index).Set(decoded.Elem())
	}
	return request, nil
}

func channelRoutingRuntimeSettingsETag(state model.RoutingRuntimeSettingsState) string {
	return fmt.Sprintf("\"crs.%d.%s\"", state.Revision, state.DocumentHash)
}

func parseChannelRoutingRuntimeSettingsETag(value string) (int64, string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 128 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, "", model.ErrRoutingRuntimeSettingsInvalid
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) != 3 || parts[0] != "crs" {
		return 0, "", model.ErrRoutingRuntimeSettingsInvalid
	}
	revision, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || revision <= 0 || len(parts[2]) != 64 {
		return 0, "", model.ErrRoutingRuntimeSettingsInvalid
	}
	for _, char := range parts[2] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return 0, "", model.ErrRoutingRuntimeSettingsInvalid
		}
	}
	return revision, parts[2], nil
}

func writeChannelRoutingRuntimeSettingsConflict(c *gin.Context, current channelRoutingRuntimeSettingsView) {
	c.Header("ETag", current.ETag)
	c.JSON(http.StatusConflict, gin.H{
		"success": false, "code": "runtime_settings_conflict", "message": "runtime settings changed",
		"conflict": gin.H{"current": current, "current_etag": current.ETag},
	})
}

func writeChannelRoutingRuntimeSettingsError(c *gin.Context, status int, code string, message string, err error) {
	if status >= http.StatusInternalServerError && err != nil {
		common.SysError(message + ": " + common.SanitizeErrorMessage(err.Error()))
	}
	payload := gin.H{"success": false, "code": code, "message": message}
	var fieldErr *channelRoutingRuntimeSettingsFieldError
	if errors.As(err, &fieldErr) && fieldErr.Field != "" {
		payload["field"] = fieldErr.Field
		payload["reason"] = fieldErr.Reason
		payload["field_errors"] = gin.H{fieldErr.Field: fieldErr.Reason}
	}
	c.JSON(status, payload)
}

func parseChannelRoutingAuditInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, model.ErrRoutingControlAuditInvalid
	}
	return value, nil
}
