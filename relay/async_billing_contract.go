package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

const (
	asyncBillingAcceptanceIntentContextKey = "async_billing_acceptance_intent"
	asyncBillingIdempotencyKeyMaxBytes     = 200
)

var (
	errAsyncBillingIdempotencyKeyInvalid  = errors.New("Idempotency-Key must contain 8 to 200 printable ASCII characters")
	errAsyncBillingIdempotencyKeyMismatch = errors.New("Idempotency-Key and X-Idempotency-Key must match")
)

type asyncBillingMultipartFileIdentity struct {
	FieldName   string `json:"field_name"`
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type asyncBillingMultipartFieldIdentity struct {
	FieldName string   `json:"field_name"`
	Values    []string `json:"values"`
}

func bindAsyncBillingAcceptanceIntent(c *gin.Context, intent model.AsyncBillingAcceptanceIntentSpec) {
	if c != nil {
		c.Set(asyncBillingAcceptanceIntentContextKey, intent)
	}
}

func getAsyncBillingAcceptanceIntent(c *gin.Context) (*model.AsyncBillingAcceptanceIntentSpec, bool) {
	if c == nil {
		return nil, false
	}
	value, exists := c.Get(asyncBillingAcceptanceIntentContextKey)
	if !exists {
		return nil, false
	}
	intent, ok := value.(model.AsyncBillingAcceptanceIntentSpec)
	return &intent, ok
}

func GetAsyncBillingAcceptanceIntent(c *gin.Context) (*model.AsyncBillingAcceptanceIntentSpec, bool) {
	return getAsyncBillingAcceptanceIntent(c)
}

func bindTaskAsyncBillingClientIdentity(c *gin.Context, info *relaycommon.RelayInfo) error {
	request, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return err
	}
	var multipartFields []asyncBillingMultipartFieldIdentity
	var multipartFiles []asyncBillingMultipartFileIdentity
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}
		fieldNames := make([]string, 0, len(form.Value))
		for fieldName := range form.Value {
			fieldNames = append(fieldNames, fieldName)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			multipartFields = append(multipartFields, asyncBillingMultipartFieldIdentity{
				FieldName: fieldName, Values: append([]string(nil), form.Value[fieldName]...),
			})
		}
		multipartFiles, err = hashAsyncBillingMultipartFiles(form)
		if err != nil {
			return err
		}
	}
	return bindAsyncBillingClientIdentity(c, info, model.AsyncBillingKindTask, info.Action, struct {
		Request         relaycommon.TaskSubmitReq            `json:"request"`
		MultipartFields []asyncBillingMultipartFieldIdentity `json:"multipart_fields,omitempty"`
		MultipartFiles  []asyncBillingMultipartFileIdentity  `json:"multipart_files,omitempty"`
	}{Request: request, MultipartFields: multipartFields, MultipartFiles: multipartFiles})
}

func bindMidjourneyAsyncBillingClientIdentity(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	request dto.MidjourneyRequest,
) error {
	return bindAsyncBillingClientIdentity(c, info, model.AsyncBillingKindMidjourney, request.Action, request)
}

func bindAsyncBillingClientIdentity(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	kind string,
	action string,
	payload any,
) error {
	if c == nil || c.Request == nil || info == nil || info.TaskRelayInfo == nil {
		return model.ErrAsyncBillingReservationInvariant
	}
	primaryKey := c.GetHeader("Idempotency-Key")
	legacyKey := c.GetHeader("X-Idempotency-Key")
	if primaryKey != "" && legacyKey != "" && primaryKey != legacyKey {
		return errAsyncBillingIdempotencyKeyMismatch
	}
	rawKey := primaryKey
	if rawKey == "" {
		rawKey = legacyKey
	}
	if rawKey == "" {
		return nil
	}
	key := strings.TrimSpace(rawKey)
	valid := key == rawKey && len(key) >= 8 && len(key) <= asyncBillingIdempotencyKeyMaxBytes
	for index := 0; valid && index < len(key); index++ {
		valid = key[index] >= 0x21 && key[index] <= 0x7e
	}
	if !valid {
		return errAsyncBillingIdempotencyKeyInvalid
	}
	action = strings.TrimSpace(action)
	path := c.Request.URL.Path
	queryValues, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid request query: %w", err)
	}
	canonicalQuery := queryValues.Encode()
	scopeMaterial, err := common.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		UserID        int    `json:"user_id"`
		TokenID       int    `json:"token_id"`
		Kind          string `json:"kind"`
		Action        string `json:"action"`
		Method        string `json:"method"`
		Path          string `json:"path"`
	}{
		SchemaVersion: 2, UserID: info.UserId, TokenID: info.TokenId,
		Kind: kind, Action: action, Method: c.Request.Method, Path: path,
	})
	if err != nil {
		return err
	}
	scopeDigest := sha256.Sum256(scopeMaterial)
	scope := "async-billing:v2:" + hex.EncodeToString(scopeDigest[:])
	if len(scope) > 191 || !utf8.ValidString(scope) {
		return model.ErrAsyncBillingReservationInvariant
	}
	canonical, err := common.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		UserID        int    `json:"user_id"`
		TokenID       int    `json:"token_id"`
		Kind          string `json:"kind"`
		Action        string `json:"action"`
		Method        string `json:"method"`
		Path          string `json:"path"`
		Query         string `json:"query"`
		Payload       any    `json:"payload"`
	}{
		SchemaVersion: 2, UserID: info.UserId, TokenID: info.TokenId,
		Kind: kind, Action: action, Method: c.Request.Method, Path: path,
		Query: canonicalQuery, Payload: payload,
	})
	if err != nil {
		return err
	}
	keyDigest := sha256.Sum256([]byte("async-billing-client:v2\x00" + scope + "\x00" + key))
	payloadDigest := sha256.Sum256(canonical)
	info.AsyncBillingClientKeyHash = hex.EncodeToString(keyDigest[:])
	info.AsyncBillingClientPayloadHash = hex.EncodeToString(payloadDigest[:])
	info.AsyncBillingClientScope = scope
	info.AsyncBillingClientKeyPresent = true
	c.Header("Idempotency-Key", key)
	return nil
}

func hashAsyncBillingMultipartFiles(form *multipart.Form) ([]asyncBillingMultipartFileIdentity, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}
	limitMB := constant.MaxRequestBodyMB
	if limitMB <= 0 {
		limitMB = 128
	}
	maxBytes := int64(limitMB) << 20
	fieldNames := make([]string, 0, len(form.File))
	for fieldName := range form.File {
		fieldNames = append(fieldNames, fieldName)
	}
	sort.Strings(fieldNames)
	identities := make([]asyncBillingMultipartFileIdentity, 0)
	var totalBytes int64
	for _, fieldName := range fieldNames {
		for index, header := range form.File[fieldName] {
			if header == nil || header.Size < 0 || header.Size > maxBytes || totalBytes > maxBytes-header.Size {
				return nil, common.ErrRequestBodyTooLarge
			}
			file, err := header.Open()
			if err != nil {
				return nil, err
			}
			hasher := sha256.New()
			written, copyErr := io.Copy(hasher, io.LimitReader(file, header.Size+1))
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return nil, errors.Join(copyErr, closeErr)
			}
			if written != header.Size {
				return nil, model.ErrAsyncBillingReservationInvariant
			}
			totalBytes += written
			identities = append(identities, asyncBillingMultipartFileIdentity{
				FieldName: fieldName, Index: index, Filename: header.Filename,
				ContentType: header.Header.Get("Content-Type"), Size: written,
				SHA256: hex.EncodeToString(hasher.Sum(nil)),
			})
		}
	}
	return identities, nil
}

func buildAsyncBillingAcceptanceIntent(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	task *model.Task,
	midjourney *model.Midjourney,
) (model.AsyncBillingAcceptanceIntentSpec, error) {
	if c == nil || c.Request == nil || info == nil || (task == nil) == (midjourney == nil) {
		return model.AsyncBillingAcceptanceIntentSpec{}, model.ErrAsyncBillingReservationInvariant
	}
	ratioKeys := make([]string, 0)
	for key := range info.PriceData.OtherRatios() {
		ratioKeys = append(ratioKeys, key)
	}
	sort.Strings(ratioKeys)
	content := fmt.Sprintf("操作 %s", info.Action)
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		content += "，按次计费"
	} else {
		parts := make([]string, 0, len(ratioKeys))
		for _, key := range ratioKeys {
			value := info.PriceData.OtherRatios()[key]
			if value != 1 {
				parts = append(parts, fmt.Sprintf("%s: %.2f", key, value))
			}
		}
		if len(parts) > 0 {
			content += ", 计算参数：" + strings.Join(parts, ", ")
		}
	}
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = info.RequestId
	}
	clientIP := ""
	if info.UserSetting.RecordIpLog {
		clientIP = c.ClientIP()
	}
	adminInfo := map[string]any{"use_channel": c.GetStringSlice("use_channel")}
	if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		adminInfo["is_multi_key"] = true
		adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	}
	if poolID := common.GetContextKeyInt(c, constant.ContextKeyRoutingPoolID); poolID > 0 {
		adminInfo["routing_pool_id"] = poolID
		adminInfo["routing_member_id"] = common.GetContextKeyInt(c, constant.ContextKeyRoutingMemberID)
		if revision, ok := common.GetContextKeyType[uint64](c, constant.ContextKeyRoutingSnapshotRevision); ok {
			adminInfo["routing_snapshot_revision"] = revision
		}
		if accountID := common.GetContextKeyInt(c, constant.ContextKeyRoutingUpstreamAccountID); accountID > 0 {
			adminInfo["routing_upstream_account_id"] = accountID
		}
	}
	var userGroupRatio *float64
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		value := info.PriceData.GroupRatioInfo.GroupSpecialRatio
		userGroupRatio = &value
	}
	group := info.UsingGroup
	if task != nil {
		group = task.Group
	}
	if midjourney != nil {
		group = midjourney.Group
	}
	audit := model.AsyncBillingAcceptedAuditSnapshot{
		RequestID: requestID, RequestPath: c.Request.URL.Path, ClientIP: clientIP,
		Action: info.Action, Content: content, OriginModelName: info.OriginModelName,
		UpstreamModelName: info.UpstreamModelName, IsModelMapped: info.IsModelMapped,
		Group: group, NodeName: common.NodeName, ModelPrice: info.PriceData.ModelPrice,
		ModelRatio: info.PriceData.ModelRatio, GroupRatio: info.PriceData.GroupRatioInfo.GroupRatio,
		UserGroupRatio: userGroupRatio, OtherRatios: info.PriceData.OtherRatios(),
		QuotaClamp: info.QuotaClamp, AdminInfo: adminInfo, DataExportEnabled: common.DataExportEnabled,
	}
	return model.AsyncBillingAcceptanceIntentSpec{Task: task, Midjourney: midjourney, Audit: audit}, nil
}

func BuildAsyncBillingFinalAudit(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	task *model.Task,
) (model.AsyncBillingAcceptedAuditSnapshot, error) {
	intent, err := buildAsyncBillingAcceptanceIntent(c, info, task, nil)
	if err != nil {
		return model.AsyncBillingAcceptedAuditSnapshot{}, err
	}
	intent.Audit.AdminInfo["billing_context_phase"] = "post_submit_final"
	return intent.Audit, nil
}

func asyncBillingReservationKey(info *relaycommon.RelayInfo, kind string) string {
	if info != nil && info.AsyncBillingClientKeyPresent {
		return "client:" + info.AsyncBillingClientKeyHash
	}
	if info == nil {
		return ""
	}
	return kind + ":" + info.RequestId
}

func asyncBillingIdempotencyTaskError(err error) *dto.TaskError {
	status := http.StatusConflict
	code := "idempotency_request_in_progress"
	switch {
	case errors.Is(err, model.ErrAsyncBillingIdempotencyConflict):
		code = "idempotency_key_conflict"
	case errors.Is(err, model.ErrAsyncBillingRequestReleased):
		code = "idempotency_key_released"
	case errors.Is(err, model.ErrAsyncBillingReplayUnavailable):
		status = http.StatusServiceUnavailable
		code = "idempotency_replay_unavailable"
	}
	return &dto.TaskError{Code: code, Message: err.Error(), StatusCode: status, LocalError: true, Error: err}
}

func asyncBillingIdempotentReplay(
	reservation *model.AsyncBillingReservation,
) (*model.AsyncBillingReplayResponse, error) {
	if reservation == nil {
		return nil, model.ErrAsyncBillingRequestInProgress
	}
	switch reservation.State {
	case model.AsyncBillingReservationStateReleased:
		return nil, model.ErrAsyncBillingRequestReleased
	case model.AsyncBillingReservationStateAccepted, model.AsyncBillingReservationStateTerminal:
		if !reservation.ReplayReady {
			return nil, model.ErrAsyncBillingRequestInProgress
		}
		return model.GetAsyncBillingReplayResponse(reservation)
	default:
		return nil, model.ErrAsyncBillingRequestInProgress
	}
}

func parseAsyncBillingReplayHeaders(raw string) (http.Header, error) {
	headers, _, err := model.NormalizeAsyncBillingReplayHeaders(raw)
	if err != nil {
		return nil, model.ErrAsyncBillingReplayUnavailable
	}
	return headers, nil
}

func safeAsyncBillingReplayHeaders(source http.Header) (string, error) {
	connectionHeaders := make(map[string]struct{})
	for _, value := range source.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if canonical := http.CanonicalHeaderKey(strings.TrimSpace(token)); canonical != "" {
				connectionHeaders[canonical] = struct{}{}
			}
		}
	}
	headers := make(http.Header)
	for key, values := range source {
		canonical := http.CanonicalHeaderKey(key)
		switch canonical {
		case "Authorization", "Connection", "Content-Encoding", "Content-Length", "Content-Type", "Cookie",
			"Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Set-Cookie",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		if _, hopByHop := connectionHeaders[canonical]; hopByHop {
			continue
		}
		headers[canonical] = append([]string(nil), values...)
	}
	if len(headers) == 0 {
		return "", nil
	}
	encoded, err := common.Marshal(headers)
	if err != nil || len(encoded) > 16<<10 {
		return "", model.ErrAsyncBillingReservationInvariant
	}
	_, normalized, err := model.NormalizeAsyncBillingReplayHeaders(string(encoded))
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func commitAsyncBillingReplay(c *gin.Context, reservation *model.AsyncBillingReservation) error {
	if c == nil {
		return model.ErrAsyncBillingReplayUnavailable
	}
	replay, err := model.GetAsyncBillingReplayResponse(reservation)
	if err != nil {
		return err
	}
	headers, err := parseAsyncBillingReplayHeaders(replay.HeadersJSON)
	if err != nil {
		return err
	}
	for key, values := range headers {
		c.Writer.Header()[key] = append([]string(nil), values...)
	}
	c.Data(replay.StatusCode, replay.ContentType, replay.Body)
	return nil
}
