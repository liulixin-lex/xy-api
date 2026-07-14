package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

const (
	midjourneyCustomIDMaxBytes      = 4 << 10
	midjourneyCustomIDMaxParts      = 16
	midjourneyTaskReferenceMaxBytes = 512
	midjourneySimpleChangeMaxBytes  = 1 << 10
	midjourneyResponseMaxBytes      = 4 << 20
)

func CovertMjpActionToModelName(mjAction string) string {
	modelName := "mj_" + strings.ToLower(mjAction)
	if mjAction == constant.MjActionSwapFace {
		modelName = "swap_face"
	}
	return modelName
}

func GetMjRequestModel(relayMode int, midjRequest *dto.MidjourneyRequest) (string, *dto.MidjourneyResponse, bool) {
	if midjRequest == nil {
		return "", MidjourneyErrorWrapper(constant.MjRequestError, "invalid_request"), false
	}
	prepared, mjErr := PrepareMidjourneyRequest(relayMode, *midjRequest)
	if mjErr != nil {
		return "", mjErr, false
	}
	if prepared == nil {
		return "", nil, true
	}
	*midjRequest = prepared.Request
	return prepared.ModelName, nil, true
}

type PreparedMidjourneyRequest struct {
	RelayMode     int
	Request       dto.MidjourneyRequest
	ModelName     string
	TaskReference string
	Stateful      bool
	ConsumesQuota bool
}

func PrepareMidjourneyRequest(relayMode int, request dto.MidjourneyRequest) (*PreparedMidjourneyRequest, *dto.MidjourneyResponse) {
	prepared := &PreparedMidjourneyRequest{
		RelayMode: relayMode, Request: request, ConsumesQuota: true,
	}

	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch,
		relayconstant.RelayModeMidjourneyTaskFetchByCondition,
		relayconstant.RelayModeMidjourneyNotify,
		relayconstant.RelayModeMidjourneyTaskImageSeed:
		return nil, nil
	case relayconstant.RelayModeMidjourneyAction:
		if mjErr := CoverPlusActionToNormalAction(&prepared.Request); mjErr != nil {
			return nil, mjErr
		}
		prepared.RelayMode = relayconstant.RelayModeMidjourneyChange
		prepared.Stateful = true
	case relayconstant.RelayModeMidjourneyImagine:
		if strings.TrimSpace(prepared.Request.Prompt) == "" {
			return nil, MidjourneyErrorWrapper(constant.MjRequestError, "prompt_is_required")
		}
		prepared.Request.Action = constant.MjActionImagine
	case relayconstant.RelayModeMidjourneyDescribe:
		prepared.Request.Action = constant.MjActionDescribe
	case relayconstant.RelayModeMidjourneyEdits:
		prepared.Request.Action = constant.MjActionEdits
	case relayconstant.RelayModeMidjourneyShorten:
		prepared.Request.Action = constant.MjActionShorten
	case relayconstant.RelayModeMidjourneyBlend:
		prepared.Request.Action = constant.MjActionBlend
	case relayconstant.RelayModeMidjourneyUpload:
		prepared.Request.Action = constant.MjActionUpload
	case relayconstant.RelayModeMidjourneyVideo:
		prepared.Request.Action = constant.MjActionVideo
		prepared.Stateful = true
	case relayconstant.RelayModeMidjourneyChange:
		prepared.Stateful = true
	case relayconstant.RelayModeMidjourneySimpleChange:
		params := ConvertSimpleChangeParams(prepared.Request.Content)
		if params == nil {
			return nil, MidjourneyErrorWrapper(constant.MjRequestError, "content_parse_failed")
		}
		prepared.Request.TaskId = params.TaskId
		prepared.Request.Action = params.Action
		prepared.Request.Index = params.Index
		prepared.Stateful = true
	case relayconstant.RelayModeMidjourneyModal:
		prepared.Request.Action = constant.MjActionModal
		prepared.Stateful = true
	case relayconstant.RelayModeSwapFace:
		prepared.Request.Action = constant.MjActionSwapFace
	default:
		return nil, MidjourneyErrorWrapper(constant.MjRequestError, "unknown_relay_action")
	}

	if prepared.Stateful {
		prepared.TaskReference = strings.TrimSpace(prepared.Request.TaskId)
		if !validMidjourneyTaskReference(prepared.TaskReference) {
			return nil, MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
		}
		prepared.Request.TaskId = prepared.TaskReference
		if strings.TrimSpace(prepared.Request.Action) == "" {
			return nil, MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
		}
		if prepared.RelayMode == relayconstant.RelayModeMidjourneyChange && prepared.Request.Index <= 0 {
			return nil, MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
		}
	}
	if prepared.Request.Action == constant.MjActionInPaint || prepared.Request.Action == constant.MjActionCustomZoom {
		prepared.ConsumesQuota = false
	}
	prepared.ModelName = CovertMjpActionToModelName(prepared.Request.Action)
	return prepared, nil
}

func CoverPlusActionToNormalAction(midjRequest *dto.MidjourneyRequest) *dto.MidjourneyResponse {
	// "customId": "MJ::JOB::upsample::2::3dbbd469-36af-4a0f-8f02-df6c579e7011"
	if midjRequest == nil {
		return MidjourneyErrorWrapper(constant.MjRequestError, "invalid_request")
	}
	customId := strings.TrimSpace(midjRequest.CustomId)
	if customId == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_is_required")
	}
	if len(customId) > midjourneyCustomIDMaxBytes || !utf8.ValidString(customId) || strings.ContainsAny(customId, "\r\n\x00") {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_invalid")
	}
	splits := strings.Split(customId, "::")
	if len(splits) < 3 || len(splits) > midjourneyCustomIDMaxParts || splits[0] != "MJ" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_invalid")
	}
	for _, part := range splits {
		if len(part) > midjourneyTaskReferenceMaxBytes {
			return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_invalid")
		}
	}
	var action string
	actionIndex := 1
	if len(splits) > 2 && splits[1] == "JOB" {
		actionIndex = 2
	}
	action = strings.TrimSpace(splits[actionIndex])

	if action == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}
	actionLower := strings.ToLower(action)
	indexAt := actionIndex + 1
	if strings.Contains(actionLower, "upsample") {
		if indexAt >= len(splits) {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
		}
		index, err := strconv.Atoi(splits[indexAt])
		if err != nil || index < 1 || index > 4 {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		midjRequest.Index = index
		midjRequest.Action = constant.MjActionUpscale
	} else if strings.Contains(actionLower, "variation") {
		midjRequest.Index = 1
		if actionLower == "variation" {
			if indexAt >= len(splits) {
				return MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
			}
			index, err := strconv.Atoi(splits[indexAt])
			if err != nil || index < 1 || index > 4 {
				return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
			}
			midjRequest.Index = index
			midjRequest.Action = constant.MjActionVariation
		} else if actionLower == "low_variation" {
			midjRequest.Action = constant.MjActionLowVariation
		} else if actionLower == "high_variation" {
			midjRequest.Action = constant.MjActionHighVariation
		} else {
			return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
		}
	} else if strings.Contains(actionLower, "pan") {
		midjRequest.Action = constant.MjActionPan
		midjRequest.Index = 1
	} else if strings.Contains(actionLower, "reroll") {
		midjRequest.Action = constant.MjActionReRoll
		midjRequest.Index = 1
	} else if actionLower == "outpaint" {
		midjRequest.Action = constant.MjActionZoom
		midjRequest.Index = 1
	} else if actionLower == "customzoom" {
		midjRequest.Action = constant.MjActionCustomZoom
		midjRequest.Index = 1
	} else if actionLower == "inpaint" {
		midjRequest.Action = constant.MjActionInPaint
		midjRequest.Index = 1
	} else {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}

	taskReference := strings.TrimSpace(midjRequest.TaskId)
	if taskReference == "" {
		taskReference = strings.TrimSpace(splits[len(splits)-1])
	}
	if !validMidjourneyTaskReference(taskReference) {
		return MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
	}
	midjRequest.TaskId = taskReference
	return nil
}

func ConvertSimpleChangeParams(content string) *dto.MidjourneyRequest {
	if len(content) == 0 || len(content) > midjourneySimpleChangeMaxBytes || !utf8.ValidString(content) ||
		strings.ContainsAny(content, "\r\n\x00") {
		return nil
	}
	split := strings.Fields(content)
	if len(split) != 2 {
		return nil
	}

	action := strings.ToLower(split[1])
	if !validMidjourneyTaskReference(split[0]) || action == "" {
		return nil
	}
	changeParams := &dto.MidjourneyRequest{TaskId: split[0]}

	if action == "r" {
		changeParams.Action = constant.MjActionReRoll
		changeParams.Index = 1
		return changeParams
	}
	if len(action) != 2 {
		return nil
	}
	if action[0] == 'u' {
		changeParams.Action = "UPSCALE"
	} else if action[0] == 'v' {
		changeParams.Action = "VARIATION"
	} else {
		return nil
	}

	index, err := strconv.Atoi(action[1:])
	if err != nil || index < 1 || index > 4 {
		return nil
	}
	changeParams.Index = index
	return changeParams
}

func validMidjourneyTaskReference(taskReference string) bool {
	return taskReference != "" && len(taskReference) <= midjourneyTaskReferenceMaxBytes &&
		utf8.ValidString(taskReference) && !strings.ContainsAny(taskReference, "\r\n\x00")
}

func ValidMidjourneyTaskReference(taskReference string) bool {
	return validMidjourneyTaskReference(strings.TrimSpace(taskReference))
}

// BuildMidjourneyRequestBody preserves provider-specific extension fields while
// replacing gateway task identities with the origin task's upstream identity.
func BuildMidjourneyRequestBody(
	c *gin.Context,
	prepared *PreparedMidjourneyRequest,
	upstreamTaskID string,
) ([]byte, error) {
	if c == nil || c.Request == nil || prepared == nil {
		return nil, errors.New("invalid midjourney request")
	}
	if c.Request.Method == http.MethodGet {
		return nil, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("midjourney request body must be an object")
	}
	if !setting.MjAccountFilterEnabled {
		delete(payload, "accountFilter")
	}
	if !setting.MjNotifyEnabled {
		delete(payload, "notifyHook")
	}

	request := prepared.Request
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if prepared.Stateful {
		if !validMidjourneyTaskReference(upstreamTaskID) {
			return nil, errors.New("upstream midjourney task identity is unavailable")
		}
		request.TaskId = upstreamTaskID
		if request.CustomId != "" {
			parts := strings.Split(request.CustomId, "::")
			for index := range parts {
				if strings.TrimSpace(parts[index]) == prepared.TaskReference {
					parts[index] = upstreamTaskID
				}
			}
			request.CustomId = strings.Join(parts, "::")
		}
		if prepared.RelayMode == relayconstant.RelayModeMidjourneySimpleChange {
			switch request.Action {
			case constant.MjActionUpscale:
				request.Content = upstreamTaskID + " u" + strconv.Itoa(request.Index)
			case constant.MjActionVariation:
				request.Content = upstreamTaskID + " v" + strconv.Itoa(request.Index)
			case constant.MjActionReRoll:
				request.Content = upstreamTaskID + " r"
			default:
				return nil, errors.New("unsupported simple-change action")
			}
		}
	}

	payload["action"] = request.Action
	if request.TaskId != "" {
		payload["taskId"] = request.TaskId
	}
	if request.Index > 0 {
		payload["index"] = request.Index
	}
	if request.CustomId != "" {
		payload["customId"] = request.CustomId
	}
	if request.Content != "" {
		payload["content"] = request.Content
	}
	if request.Prompt != "" {
		payload["prompt"] = request.Prompt
	}
	if setting.MjModeClearEnabled {
		if prompt, ok := payload["prompt"].(string); ok {
			prompt = strings.ReplaceAll(prompt, "--fast", "")
			prompt = strings.ReplaceAll(prompt, "--relax", "")
			payload["prompt"] = strings.ReplaceAll(prompt, "--turbo", "")
		}
	}
	return common.Marshal(payload)
}

func DoMidjourneyHttpRequest(
	c *gin.Context,
	timeout time.Duration,
	fullRequestURL string,
	apiKey string,
	proxyURL string,
	requestBody []byte,
) (*dto.MidjourneyResponseWithStatusCode, []byte, error) {
	var nullBytes []byte
	if c == nil || c.Request == nil || timeout <= 0 || strings.TrimSpace(fullRequestURL) == "" {
		err := errors.New("invalid midjourney request")
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "invalid_request", http.StatusInternalServerError), nullBytes, err
	}
	requestContext, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	var body io.Reader
	if len(requestBody) > 0 {
		body = bytes.NewReader(requestBody)
	}
	req, err := http.NewRequestWithContext(requestContext, c.Request.Method, fullRequestURL, body)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "create_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" && len(requestBody) > 0 {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))
	apiKey = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(apiKey), "Bearer "))
	if apiKey != "" {
		req.Header.Set("mj-api-secret", apiKey)
	}
	httpClient, err := GetHttpClientWithProxy(proxyURL)
	if err != nil || httpClient == nil {
		if err == nil {
			err = errors.New("midjourney HTTP client is unavailable")
		}
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "create_http_client_failed", http.StatusInternalServerError), nullBytes, err
	}
	if err := requestContext.Err(); err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "request_cancelled", http.StatusRequestTimeout), nullBytes, err
	}
	if err := relaycommon.MarkRoutingUpstreamSent(c); err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "routing_attempt_commit_failed", http.StatusServiceUnavailable), nullBytes, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		common.SysLog("do Midjourney request failed: " + common.SanitizeErrorMessage(err.Error(), apiKey))
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "do_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	if resp == nil || resp.Body == nil {
		err = errors.New("midjourney upstream returned an empty response")
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "empty_response", http.StatusBadGateway), nullBytes, err
	}
	defer CloseResponseBodyGracefully(resp)
	statusCode := resp.StatusCode
	responseError := func(code int, description string, responseStatus int) *dto.MidjourneyResponseWithStatusCode {
		wrapped := MidjourneyErrorWithStatusCodeWrapper(code, description, responseStatus)
		wrapped.UpstreamResponseReceived = true
		wrapped.UpstreamStatusCode = statusCode
		return wrapped
	}
	//if statusCode != 200  {
	//	return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "bad_response_status_code", statusCode), nullBytes, nil
	//}
	var midjResponse dto.MidjourneyResponse
	var midjourneyUploadsResponse dto.MidjourneyUploadResponse
	if resp.ContentLength > midjourneyResponseMaxBytes {
		err = errors.New("midjourney response body is too large")
		return responseError(constant.MjErrorUnknown, "response_body_too_large", http.StatusBadGateway), nullBytes, err
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, midjourneyResponseMaxBytes+1))
	if err != nil {
		return responseError(constant.MjErrorUnknown, "read_response_body_failed", statusCode), nullBytes, err
	}
	if len(responseBody) > midjourneyResponseMaxBytes {
		err = errors.New("midjourney response body is too large")
		return responseError(constant.MjErrorUnknown, "response_body_too_large", http.StatusBadGateway), nullBytes, err
	}
	logger.LogDebug(c, "midjourney response status=%d body_bytes=%d", statusCode, len(responseBody))
	if len(responseBody) == 0 {
		return responseError(constant.MjErrorUnknown, "empty_response_body", statusCode), responseBody, nil
	} else {
		err = common.Unmarshal(responseBody, &midjResponse)
		if err != nil {
			err2 := common.Unmarshal(responseBody, &midjourneyUploadsResponse)
			if err2 != nil {
				return responseError(constant.MjErrorUnknown, "unmarshal_response_body_failed", statusCode), responseBody, err
			}
		}
	}
	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode, UpstreamStatusCode: statusCode,
		Response: midjResponse, UpstreamResponseReceived: true,
	}, responseBody, nil
}
