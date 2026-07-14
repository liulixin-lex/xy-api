package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type MidjourneyRelayOutcome struct {
	Error            *dto.MidjourneyResponse
	StatusCode       int
	LocalError       bool
	UpstreamAccepted bool
	IdempotentReplay bool
	Cause            error
}

var midjourneyImageHTTPClientFactory = service.GetStatefulFetchHTTPClient

var (
	errMidjourneyMediaURLNotAllowed         = errors.New("Midjourney media URL is not allowed")
	errMidjourneyMediaChannelUnavailable    = errors.New("Midjourney media channel is unavailable")
	errMidjourneyMediaGenerationMismatch    = errors.New("Midjourney media channel generation mismatch")
	errMidjourneyMediaCredentialUnavailable = errors.New("Midjourney media credential is unavailable")
)

const (
	maxMidjourneyVideoBytes     int64 = 1 << 30
	midjourneySubmissionTimeout       = 60 * time.Second
)

func midjourneyLocalOutcome(description string, statusCode int, err error) MidjourneyRelayOutcome {
	if statusCode <= 0 {
		statusCode = http.StatusInternalServerError
	}
	result := ""
	if err != nil {
		result = common.LocalLogPreview(err.Error())
	}
	return MidjourneyRelayOutcome{
		Error: &dto.MidjourneyResponse{
			Code: constant.MjRequestError, Description: description, Result: result,
		},
		StatusCode: statusCode,
		LocalError: true,
		Cause:      err,
	}
}

func RelayMidjourneyImage(c *gin.Context) {
	taskId := strings.TrimSpace(c.Param("id"))
	midjourneyTask := findAccessibleMidjourneyTask(c, taskId)
	if midjourneyTask == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "midjourney_task_not_found",
		})
		return
	}
	if !authorizeMidjourneyMediaTask(c, midjourneyTask) {
		return
	}
	target, err := resolveMidjourneyMediaTarget(
		c.Request.Context(), midjourneyTask, midjourneyTask.ImageUrl,
		service.StatefulMediaNewAPIMidjourneyImage,
	)
	if err != nil {
		writeMidjourneyMediaTargetError(c, "midjourney_image", err)
		return
	}
	httpClient, err := midjourneyImageHTTPClientFactory(target.proxy)
	if err != nil || httpClient == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_image_client_unavailable"})
		return
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target.url, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_image_request_invalid"})
		return
	}
	if target.credential != "" {
		request.Header.Set("Authorization", "Bearer "+target.credential)
	}
	resp, err := service.DoStatefulFetch(httpClient, request)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_image_upstream_failed"})
		return
	}
	maxImageBytes := int64(constant.MaxFileDownloadMB) << 20
	if maxImageBytes <= 0 {
		maxImageBytes = 20 << 20
	}
	if resp.ContentLength > maxImageBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_image_too_large"})
		return
	}
	imageBody, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "read_midjourney_image_failed"})
		return
	}
	if int64(len(imageBody)) > maxImageBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_image_too_large"})
		return
	}
	contentType, err := validatedMidjourneyImageContentType(resp.Header.Get("Content-Type"), imageBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid_midjourney_image_content"})
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("Vary", "Authorization, Cookie")
	c.Data(http.StatusOK, contentType, imageBody)
	return
}

func RelayMidjourneyVideo(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	midjourneyTask := findAccessibleMidjourneyTask(c, taskID)
	if midjourneyTask == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_task_not_found"})
		return
	}
	if !authorizeMidjourneyMediaTask(c, midjourneyTask) {
		return
	}

	videoURL := strings.TrimSpace(midjourneyTask.VideoUrl)
	if rawIndex := strings.TrimSpace(c.Param("index")); rawIndex != "" {
		index, err := strconv.Atoi(rawIndex)
		if err != nil || index < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "midjourney_video_index_invalid"})
			return
		}
		var videoURLs []dto.ImgUrls
		if err := common.Unmarshal([]byte(midjourneyTask.VideoUrls), &videoURLs); err != nil || index >= len(videoURLs) {
			c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_video_not_found"})
			return
		}
		videoURL = strings.TrimSpace(videoURLs[index].Url)
	}
	if videoURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_video_not_found"})
		return
	}
	target, err := resolveMidjourneyMediaTarget(
		c.Request.Context(), midjourneyTask, videoURL,
		service.StatefulMediaNewAPIMidjourneyVideo,
	)
	if err != nil {
		writeMidjourneyMediaTargetError(c, "midjourney_video", err)
		return
	}
	httpClient, err := midjourneyImageHTTPClientFactory(target.proxy)
	if err != nil || httpClient == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_video_client_unavailable"})
		return
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target.url, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_video_request_invalid"})
		return
	}
	if target.credential != "" {
		request.Header.Set("Authorization", "Bearer "+target.credential)
	}
	if rangeHeader := strings.TrimSpace(c.GetHeader("Range")); rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	if ifRangeHeader := strings.TrimSpace(c.GetHeader("If-Range")); ifRangeHeader != "" {
		request.Header.Set("If-Range", ifRangeHeader)
	}
	response, err := service.DoStatefulFetch(httpClient, request)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_video_fetch_failed"})
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		copyMidjourneyMediaHeaders(c.Writer.Header(), response.Header, true)
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_video_upstream_failed"})
		return
	}
	if response.ContentLength > maxMidjourneyVideoBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "midjourney_video_too_large"})
		return
	}
	if err := validateMidjourneyVideoContentType(response.Header.Get("Content-Type")); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid_midjourney_video_content"})
		return
	}

	copyMidjourneyMediaHeaders(c.Writer.Header(), response.Header, false)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Vary", "Authorization, Cookie")
	c.Status(response.StatusCode)
	written, copyErr := io.Copy(c.Writer, io.LimitReader(response.Body, maxMidjourneyVideoBytes))
	if copyErr != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Midjourney video stream ended early: task=%d", midjourneyTask.Id))
		return
	}
	if written == maxMidjourneyVideoBytes {
		var extra [1]byte
		if count, _ := response.Body.Read(extra[:]); count > 0 {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Midjourney video stream exceeded proxy limit: task=%d", midjourneyTask.Id))
		}
	}
}

func findAccessibleMidjourneyTask(c *gin.Context, taskID string) *model.Midjourney {
	userID := c.GetInt("id")
	if userID <= 0 || taskID == "" {
		return nil
	}
	task, err := model.FindMidjourneyByPublicID(userID, taskID)
	if err == nil {
		return task
	}
	if c.GetInt("role") < common.RoleAdminUser {
		return nil
	}
	task, err = model.FindMidjourneyByPublicIDAnyUser(taskID)
	if err != nil {
		return nil
	}
	return task
}

func authorizeMidjourneyMediaTask(c *gin.Context, task *model.Midjourney) bool {
	if c == nil || task == nil {
		return false
	}
	modelName := service.CovertMjpActionToModelName(task.Action)
	authorizationErr := middleware.AuthorizeTokenRoutingTarget(c, modelName, task.ChannelId, true)
	if authorizationErr == nil {
		return true
	}
	c.JSON(authorizationErr.StatusCode, gin.H{
		"error": authorizationErr.GetErrorCode(),
	})
	return false
}

type midjourneyMediaTarget struct {
	url        string
	proxy      string
	credential string
}

func resolveMidjourneyMediaTarget(
	ctx context.Context,
	task *model.Midjourney,
	rawURL string,
	kind service.StatefulMediaKind,
) (midjourneyMediaTarget, error) {
	if task == nil {
		return midjourneyMediaTarget{}, errMidjourneyMediaURLNotAllowed
	}
	var channel *model.Channel
	channel, channelErr := model.GetChannelById(task.ChannelId, true)
	baseURL := ""
	proxy := ""
	if channelErr == nil && channel != nil {
		baseURL = strings.TrimSpace(channel.GetBaseURL())
		proxy = strings.TrimSpace(channel.GetSetting().Proxy)
	}
	if task.ChannelGeneration != "" {
		if channelErr != nil || channel == nil {
			return midjourneyMediaTarget{}, errMidjourneyMediaChannelUnavailable
		}
		if task.ChannelGeneration != channel.RoutingGeneration {
			return midjourneyMediaTarget{}, errMidjourneyMediaGenerationMismatch
		}
	}

	resolution, err := service.ResolveStatefulMediaURL(baseURL, rawURL, kind)
	if err != nil || service.ValidateStatefulFetchURL(resolution.URL) != nil {
		return midjourneyMediaTarget{}, errMidjourneyMediaURLNotAllowed
	}
	target := midjourneyMediaTarget{url: resolution.URL, proxy: proxy}
	if !resolution.TrustedSameOrigin {
		return target, nil
	}
	if channelErr != nil || channel == nil {
		return midjourneyMediaTarget{}, errMidjourneyMediaChannelUnavailable
	}
	if task.ChannelGeneration == "" || task.ChannelGeneration != channel.RoutingGeneration {
		return midjourneyMediaTarget{}, errMidjourneyMediaGenerationMismatch
	}
	if task.RoutingCredentialID <= 0 {
		return midjourneyMediaTarget{}, errMidjourneyMediaCredentialUnavailable
	}
	credential, _, err := channelrouting.ResolvePersistedCredentialKey(ctx, channel, task.RoutingCredentialID)
	if err != nil || strings.TrimSpace(credential) == "" {
		return midjourneyMediaTarget{}, errMidjourneyMediaCredentialUnavailable
	}
	target.credential = credential
	return target, nil
}

func writeMidjourneyMediaTargetError(c *gin.Context, prefix string, err error) {
	statusCode := http.StatusServiceUnavailable
	errorCode := prefix + "_credential_unavailable"
	switch {
	case errors.Is(err, errMidjourneyMediaURLNotAllowed):
		statusCode = http.StatusForbidden
		errorCode = prefix + "_url_not_allowed"
	case errors.Is(err, errMidjourneyMediaGenerationMismatch):
		statusCode = http.StatusConflict
		errorCode = prefix + "_channel_generation_mismatch"
	case errors.Is(err, errMidjourneyMediaChannelUnavailable):
		errorCode = prefix + "_channel_unavailable"
	}
	c.JSON(statusCode, gin.H{"error": errorCode})
}

func validateMidjourneyVideoContentType(rawContentType string) error {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(rawContentType))
	if err != nil {
		return errors.New("invalid Midjourney video content type")
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "video/") || mediaType == "application/octet-stream" ||
		mediaType == "multipart/byteranges" {
		return nil
	}
	return errors.New("unsupported Midjourney video content type")
}

func copyMidjourneyMediaHeaders(destination, source http.Header, rangeError bool) {
	headers := []string{"Accept-Ranges", "Content-Range", "ETag", "Last-Modified"}
	if !rangeError {
		headers = append(headers, "Content-Disposition", "Content-Length", "Content-Type")
	}
	for _, key := range headers {
		for _, value := range source.Values(key) {
			destination.Add(key, value)
		}
	}
}

func validatedMidjourneyImageContentType(declared string, body []byte) (string, error) {
	mediaType := ""
	if strings.TrimSpace(declared) != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil {
			return "", errors.New("invalid Midjourney image content type")
		}
		mediaType = strings.ToLower(strings.TrimSpace(parsed))
	}
	detected := strings.ToLower(http.DetectContentType(body))
	if !isSafeMidjourneyImageMediaType(detected) {
		return "", errors.New("Midjourney response is not a supported image")
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		return detected, nil
	}
	if !isSafeMidjourneyImageMediaType(mediaType) {
		return "", errors.New("Midjourney response is not a supported image")
	}
	if mediaType != detected {
		return "", errors.New("Midjourney response body does not match its image content type")
	}
	return mediaType, nil
}

func isSafeMidjourneyImageMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return strings.HasPrefix(mediaType, "image/") && mediaType != "image/svg+xml"
}

func RelayMidjourneyNotify(c *gin.Context) *dto.MidjourneyResponse {
	var midjRequest dto.MidjourneyDto
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "bind_request_body_failed",
			Properties:  nil,
			Result:      "",
		}
	}
	if !service.ValidMidjourneyTaskReference(midjRequest.MjId) {
		return &dto.MidjourneyResponse{Code: 4, Description: "midjourney_task_identity_invalid"}
	}
	midjourneyTask, findErr := model.FindMidjourneyByUpstreamID(strings.TrimSpace(midjRequest.MjId))
	if findErr != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "midjourney_task_not_found",
			Properties:  nil,
			Result:      "",
		}
	}
	if midjourneyTask.Progress == "100%" {
		return nil
	}
	previousStatus := midjourneyTask.Status
	midjourneyTask.Progress = midjRequest.Progress
	midjourneyTask.PromptEn = midjRequest.PromptEn
	midjourneyTask.State = midjRequest.State
	midjourneyTask.SubmitTime = midjRequest.SubmitTime
	midjourneyTask.StartTime = midjRequest.StartTime
	midjourneyTask.FinishTime = midjRequest.FinishTime
	midjourneyTask.ImageUrl = midjRequest.ImageUrl
	midjourneyTask.VideoUrl = midjRequest.VideoUrl
	videoUrlsStr, marshalErr := common.Marshal(midjRequest.VideoUrls)
	if marshalErr != nil {
		return &dto.MidjourneyResponse{Code: 4, Description: "marshal_midjourney_video_urls_failed"}
	}
	midjourneyTask.VideoUrls = string(videoUrlsStr)
	midjourneyTask.Status = midjRequest.Status
	midjourneyTask.FailReason = common.SanitizeErrorMessage(midjRequest.FailReason)
	shouldRefund := (midjourneyTask.Progress != "100%" && midjourneyTask.FailReason != "") ||
		(midjourneyTask.Progress == "100%" && midjourneyTask.Status == "FAILURE")
	if shouldRefund {
		midjourneyTask.Progress = "100%"
		midjourneyTask.Status = "FAILURE"
		_, err = finalizeMidjourneyFailureAndQueueRefund(c.Request.Context(), midjourneyTask, previousStatus)
	} else {
		_, err = midjourneyTask.UpdateWithStatus(previousStatus)
	}
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "update_midjourney_task_failed",
		}
	}

	return nil
}

func coverMidjourneyTaskDto(c *gin.Context, originTask *model.Midjourney) (midjourneyTask dto.MidjourneyDto) {
	midjourneyTask.MjId = originTask.MjId
	midjourneyTask.Progress = originTask.Progress
	midjourneyTask.PromptEn = originTask.PromptEn
	midjourneyTask.State = originTask.State
	midjourneyTask.SubmitTime = originTask.SubmitTime
	midjourneyTask.StartTime = originTask.StartTime
	midjourneyTask.FinishTime = originTask.FinishTime
	midjourneyTask.ImageUrl = ""
	if originTask.ImageUrl != "" && setting.MjForwardUrlEnabled {
		midjourneyTask.ImageUrl = midjourneyMediaURL("image", originTask.MjId, -1)
		if originTask.Status != "SUCCESS" {
			midjourneyTask.ImageUrl += "?rand=" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	}
	if originTask.VideoUrl != "" {
		midjourneyTask.VideoUrl = midjourneyMediaURL("video", originTask.MjId, -1)
	}
	midjourneyTask.Status = originTask.Status
	midjourneyTask.FailReason = common.SanitizeErrorMessage(originTask.FailReason)
	midjourneyTask.Action = originTask.Action
	midjourneyTask.Description = originTask.Description
	midjourneyTask.Prompt = originTask.Prompt
	if originTask.Buttons != "" {
		var buttons []dto.ActionButton
		err := common.Unmarshal([]byte(originTask.Buttons), &buttons)
		if err == nil {
			midjourneyTask.Buttons = buttons
		}
	}
	if originTask.VideoUrls != "" {
		var videoUrls []dto.ImgUrls
		err := common.Unmarshal([]byte(originTask.VideoUrls), &videoUrls)
		if err == nil {
			for index := range videoUrls {
				if strings.TrimSpace(videoUrls[index].Url) != "" {
					videoUrls[index].Url = midjourneyMediaURL("video", originTask.MjId, index)
				}
			}
			midjourneyTask.VideoUrls = videoUrls
		}
	}
	if originTask.Properties != "" {
		var properties dto.Properties
		err := common.Unmarshal([]byte(originTask.Properties), &properties)
		if err == nil {
			midjourneyTask.Properties = &properties
		}
	}
	return
}

func midjourneyMediaURL(kind, taskID string, index int) string {
	path := "/mj/" + kind + "/" + url.PathEscape(strings.TrimSpace(taskID))
	if index >= 0 {
		path += "/" + strconv.Itoa(index)
	}
	return strings.TrimRight(system_setting.ServerAddress, "/") + path
}

func ValidateSwapFaceRequest(c *gin.Context) *dto.MidjourneyResponse {
	var swapFaceRequest dto.SwapFaceRequest
	if err := common.UnmarshalBodyReusable(c, &swapFaceRequest); err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}
	if swapFaceRequest.SourceBase64 == "" || swapFaceRequest.TargetBase64 == "" {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "sour_base64_and_target_base64_is_required")
	}
	return nil
}

func RelayMidjourneyTaskImageSeed(
	c *gin.Context,
	originTask *model.Midjourney,
	channel *model.Channel,
	apiKey string,
) MidjourneyRelayOutcome {
	if c == nil || originTask == nil || channel == nil || strings.TrimSpace(apiKey) == "" {
		return midjourneyLocalOutcome("invalid_image_seed_request", http.StatusBadRequest, nil)
	}
	requestURL := "/mj/task/" + url.PathEscape(originTask.GetUpstreamTaskID()) + "/image-seed"
	fullRequestURL := strings.TrimRight(channel.GetBaseURL(), "/") + requestURL
	midjResponseWithStatus, _, err := service.DoMidjourneyHttpRequest(
		c, 30*time.Second, fullRequestURL, apiKey, channel.GetSetting().Proxy, nil,
	)
	if err != nil {
		return MidjourneyRelayOutcome{Error: &midjResponseWithStatus.Response, StatusCode: midjResponseWithStatus.StatusCode}
	}
	midjResponse := &midjResponseWithStatus.Response
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return midjourneyLocalOutcome("marshal_response_body_failed", http.StatusInternalServerError, err)
	}
	c.Data(midjResponseWithStatus.StatusCode, "application/json", respBody)
	return MidjourneyRelayOutcome{StatusCode: midjResponseWithStatus.StatusCode, UpstreamAccepted: true}
}

func RelayMidjourneyTask(c *gin.Context, relayMode int) *dto.MidjourneyResponse {
	userId := c.GetInt("id")
	var err error
	var respBody []byte
	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch:
		taskId := c.Param("id")
		originTask, findErr := model.FindMidjourneyByPublicID(userId, strings.TrimSpace(taskId))
		if findErr != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "task_no_found",
			}
		}
		midjourneyTask := coverMidjourneyTaskDto(c, originTask)
		respBody, err = common.Marshal(midjourneyTask)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	case relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		var condition = struct {
			IDs []string `json:"ids"`
		}{}
		err = common.UnmarshalBodyReusable(c, &condition)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "do_request_failed",
			}
		}
		if len(condition.IDs) > 1_000 {
			return &dto.MidjourneyResponse{Code: 4, Description: "too_many_task_ids"}
		}
		for _, taskID := range condition.IDs {
			if !service.ValidMidjourneyTaskReference(taskID) {
				return &dto.MidjourneyResponse{Code: 4, Description: "task_id_invalid"}
			}
		}
		var tasks []dto.MidjourneyDto
		if len(condition.IDs) != 0 {
			originTasks, findErr := model.FindMidjourneysByPublicIDs(userId, condition.IDs)
			if findErr != nil {
				return &dto.MidjourneyResponse{Code: 4, Description: "task_identity_ambiguous"}
			}
			for _, originTask := range originTasks {
				midjourneyTask := coverMidjourneyTaskDto(c, originTask)
				tasks = append(tasks, midjourneyTask)
			}
		}
		if tasks == nil {
			tasks = make([]dto.MidjourneyDto, 0)
		}
		respBody, err = common.Marshal(tasks)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "application/json")

	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	return nil
}

// ReplayPreparedMidjourneySubmit resolves an existing client-idempotent
// request before routing capacity, attempt, canary, and upstream-send state are
// acquired. A miss leaves creation to RelayPreparedMidjourneySubmit.
func ReplayPreparedMidjourneySubmit(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	prepared *service.PreparedMidjourneyRequest,
	originTask *model.Midjourney,
) (MidjourneyRelayOutcome, bool) {
	if c == nil || c.Request == nil || relayInfo == nil || prepared == nil ||
		prepared.RelayMode == relayconstant.RelayModeMidjourneyUpload {
		return MidjourneyRelayOutcome{}, false
	}
	if !relayInfo.AsyncBillingV2Decided {
		relayInfo.AsyncBillingV2Decided = true
		relayInfo.AsyncBillingV2Enabled = service.AsyncBillingV2FleetReady(c.Request.Context())
	}
	if !relayInfo.AsyncBillingV2Enabled {
		return MidjourneyRelayOutcome{}, false
	}
	identityRequest := prepared.Request
	if originTask != nil {
		identityRequest.Prompt = originTask.Prompt
	}
	if err := bindMidjourneyAsyncBillingClientIdentity(c, relayInfo, identityRequest); err != nil {
		return midjourneyLocalOutcome("invalid_idempotency_key", http.StatusBadRequest, err), true
	}
	if !relayInfo.AsyncBillingClientKeyPresent {
		return MidjourneyRelayOutcome{}, false
	}
	reservation, err := model.GetAsyncBillingReservationByClientIdentity(
		c.Request.Context(), relayInfo.AsyncBillingClientKeyHash,
		relayInfo.AsyncBillingClientPayloadHash, relayInfo.AsyncBillingClientScope,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return MidjourneyRelayOutcome{}, false
	}
	if err != nil {
		status := http.StatusInternalServerError
		code := "idempotency_lookup_failed"
		if errors.Is(err, model.ErrAsyncBillingIdempotencyConflict) {
			status = http.StatusConflict
			code = "idempotency_key_conflict"
		}
		return midjourneyLocalOutcome(code, status, err), true
	}
	_, replayErr := asyncBillingIdempotentReplay(reservation)
	if replayErr == nil {
		if err := commitAsyncBillingReplay(c, reservation); err != nil {
			return midjourneyLocalOutcome("idempotency_replay_unavailable", http.StatusServiceUnavailable, err), true
		}
		return MidjourneyRelayOutcome{
			StatusCode: reservation.ReplayStatusCode, UpstreamAccepted: true, IdempotentReplay: true,
		}, true
	}
	if errors.Is(replayErr, model.ErrAsyncBillingRequestReleased) {
		return midjourneyLocalOutcome("idempotency_key_released", http.StatusConflict, model.ErrAsyncBillingRequestReleased), true
	}
	if !errors.Is(replayErr, model.ErrAsyncBillingRequestInProgress) {
		return midjourneyLocalOutcome("idempotency_replay_unavailable", http.StatusServiceUnavailable, replayErr), true
	}
	return midjourneyLocalOutcome("idempotency_request_in_progress", http.StatusConflict, model.ErrAsyncBillingRequestInProgress), true
}

func RelayPreparedMidjourneySubmit(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	prepared *service.PreparedMidjourneyRequest,
	originTask *model.Midjourney,
	channel *model.Channel,
) (outcome MidjourneyRelayOutcome) {
	if c == nil || c.Request == nil || relayInfo == nil || prepared == nil || channel == nil {
		return midjourneyLocalOutcome("invalid_midjourney_submit", http.StatusBadRequest, nil)
	}
	if prepared.Stateful && originTask == nil {
		return midjourneyLocalOutcome("task_not_found", http.StatusNotFound, nil)
	}
	if prepared.RelayMode == relayconstant.RelayModeSwapFace {
		if validationErr := ValidateSwapFaceRequest(c); validationErr != nil {
			return MidjourneyRelayOutcome{Error: validationErr, StatusCode: http.StatusBadRequest, LocalError: true}
		}
	}

	request := *prepared
	request.Request = prepared.Request
	upstreamTaskID := ""
	if originTask != nil {
		upstreamTaskID = originTask.GetUpstreamTaskID()
		request.Request.Prompt = originTask.Prompt
	}
	requestBody, err := service.BuildMidjourneyRequestBody(c, &request, upstreamTaskID)
	if err != nil {
		return midjourneyLocalOutcome("build_request_body_failed", http.StatusBadRequest, err)
	}

	credentialID := common.GetContextKeyInt(c, constant.ContextKeyRoutingCredentialID)
	apiKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if strings.TrimSpace(apiKey) == "" {
		return midjourneyLocalOutcome("channel_credential_unavailable", http.StatusServiceUnavailable, nil)
	}
	if credentialID <= 0 {
		resolvedID, resolveErr := channelrouting.ResolvePersistedCredentialID(c.Request.Context(), channel, apiKey)
		if resolveErr == nil {
			credentialID = resolvedID
			common.SetContextKey(c, constant.ContextKeyRoutingCredentialID, credentialID)
		} else if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
			return midjourneyLocalOutcome("channel_credential_identity_unavailable", http.StatusServiceUnavailable, resolveErr)
		}
	}

	relayInfo.OriginModelName = request.ModelName
	relayInfo.Action = request.Request.Action
	relayInfo.InitChannelMeta(c)
	priceData, err := helper.ModelPriceHelperPerCall(c, relayInfo)
	if err != nil {
		return midjourneyLocalOutcome("model_price_error", http.StatusBadRequest, err)
	}
	priceData.QuotaToPreConsume = priceData.Quota
	relayInfo.PriceData = priceData

	persistTask := request.RelayMode != relayconstant.RelayModeMidjourneyUpload
	if !relayInfo.AsyncBillingV2Decided {
		relayInfo.AsyncBillingV2Decided = true
		relayInfo.AsyncBillingV2Enabled = persistTask && service.AsyncBillingV2FleetReady(c.Request.Context())
	}
	useDurableReservation := relayInfo.AsyncBillingV2Enabled
	if useDurableReservation {
		if identityErr := bindMidjourneyAsyncBillingClientIdentity(c, relayInfo, request.Request); identityErr != nil {
			return midjourneyLocalOutcome("invalid_idempotency_key", http.StatusBadRequest, identityErr)
		}
	}
	completed := false
	refundViaSession := true
	defer func() {
		if !completed && refundViaSession && relayInfo.Billing != nil && relayInfo.Billing.NeedsRefund() {
			relayInfo.Billing.Refund(c)
		}
	}()

	var task *model.Midjourney
	if persistTask {
		publicTaskID := model.GenerateTaskID()
		if !strings.HasPrefix(publicTaskID, "task_") || len(publicTaskID) <= len("task_") {
			return midjourneyLocalOutcome("generate_public_task_id_failed", http.StatusInternalServerError, nil)
		}
		prompt := request.Request.Prompt
		if request.RelayMode == relayconstant.RelayModeSwapFace {
			prompt = "InsightFace"
		}
		quota := 0
		if request.ConsumesQuota {
			quota = priceData.Quota
		}
		taskGroup := relayInfo.UsingGroup
		if taskGroup == "auto" {
			if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
				taskGroup = selectedGroup
			}
		}
		billingProtocolVersion := model.TaskBillingLegacyProtocolVersion
		if useDurableReservation {
			billingProtocolVersion = model.TaskBillingProtocolVersion
		}
		task = &model.Midjourney{
			UserId:                 relayInfo.UserId,
			Code:                   0,
			Action:                 request.Request.Action,
			MjId:                   publicTaskID,
			RoutingCredentialID:    credentialID,
			ChannelGeneration:      channel.RoutingGeneration,
			Prompt:                 prompt,
			SubmitTime:             time.Now().UnixMilli(),
			Status:                 "NOT_START",
			Progress:               "0%",
			ChannelId:              channel.Id,
			Quota:                  quota,
			Group:                  taskGroup,
			BillingSource:          relayInfo.BillingSource,
			BillingProtocolVersion: billingProtocolVersion,
			SubscriptionId:         relayInfo.SubscriptionId,
			TokenId:                relayInfo.TokenId,
			NodeName:               common.NodeName,
		}
		if useDurableReservation {
			reservation, created, reserveErr := model.CreateAsyncBillingReservation(c.Request.Context(), model.AsyncBillingReservationSpec{
				ReservationKey:    asyncBillingReservationKey(relayInfo, model.AsyncBillingKindMidjourney),
				ProtocolVersion:   model.TaskBillingProtocolVersion,
				Kind:              model.AsyncBillingKindMidjourney,
				PublicTaskID:      task.MjId,
				UserID:            relayInfo.UserId,
				TokenID:           relayInfo.TokenId,
				IsPlayground:      relayInfo.IsPlayground,
				BillingPreference: relayInfo.UserSetting.BillingPreference,
				Quota:             quota,
				ClientKeyHash:     relayInfo.AsyncBillingClientKeyHash,
				ClientPayloadHash: relayInfo.AsyncBillingClientPayloadHash,
				ClientScope:       relayInfo.AsyncBillingClientScope,
			}, time.Now())
			if reserveErr != nil {
				return midjourneyLocalOutcome("pre_consume_quota_failed", http.StatusForbidden, reserveErr)
			}
			relayInfo.AsyncBillingReservationID = reservation.ID
			task.MjId = reservation.PublicTaskID
			relayInfo.PublicTaskID = reservation.PublicTaskID
			relayInfo.FinalPreConsumedQuota = reservation.CurrentQuota
			relayInfo.BillingSource = reservation.FundingSource
			relayInfo.SubscriptionId = reservation.SubscriptionID
			relayInfo.SubscriptionPreConsumed = int64(reservation.CurrentQuota)
			relayInfo.SubscriptionPlanId = reservation.SubscriptionPlanID
			relayInfo.SubscriptionPlanTitle = reservation.SubscriptionPlanName
			relayInfo.SubscriptionAmountTotal = reservation.SubscriptionTotal
			refundViaSession = false
			if relayInfo.AsyncBillingClientKeyPresent && !created {
				_, replayErr := asyncBillingIdempotentReplay(reservation)
				if replayErr == nil {
					if replayErr := commitAsyncBillingReplay(c, reservation); replayErr != nil {
						return midjourneyLocalOutcome("idempotency_replay_unavailable", http.StatusServiceUnavailable, replayErr)
					}
					completed = true
					return MidjourneyRelayOutcome{
						StatusCode: reservation.ReplayStatusCode, UpstreamAccepted: true, IdempotentReplay: true,
					}
				}
				if errors.Is(replayErr, model.ErrAsyncBillingRequestReleased) {
					return midjourneyLocalOutcome("idempotency_key_released", http.StatusConflict, model.ErrAsyncBillingRequestReleased)
				}
				if !errors.Is(replayErr, model.ErrAsyncBillingRequestInProgress) {
					return midjourneyLocalOutcome("idempotency_replay_unavailable", http.StatusServiceUnavailable, replayErr)
				}
				return midjourneyLocalOutcome("idempotency_request_in_progress", http.StatusConflict, model.ErrAsyncBillingRequestInProgress)
			}
		} else {
			if request.ConsumesQuota && !priceData.FreeModel {
				relayInfo.ForcePreConsume = true
				if apiErr := service.PreConsumeBilling(c, priceData.Quota, relayInfo); apiErr != nil {
					return midjourneyLocalOutcome("pre_consume_quota_failed", apiErr.StatusCode, apiErr.Err)
				}
				task.BillingSource = relayInfo.BillingSource
				task.SubscriptionId = relayInfo.SubscriptionId
			}
			if err := task.Insert(); err != nil {
				return midjourneyLocalOutcome("insert_midjourney_task_failed", http.StatusInternalServerError, err)
			}
			refundViaSession = false
		}
		if useDurableReservation {
			intent, intentErr := buildAsyncBillingAcceptanceIntent(c, relayInfo, nil, task)
			if intentErr != nil {
				return midjourneyLocalOutcome("freeze_midjourney_acceptance_intent_failed", http.StatusInternalServerError, intentErr)
			}
			bindAsyncBillingAcceptanceIntent(c, intent)
		}
	} else if request.ConsumesQuota && !priceData.FreeModel {
		relayInfo.ForcePreConsume = true
		if apiErr := service.PreConsumeBilling(c, priceData.Quota, relayInfo); apiErr != nil {
			return midjourneyLocalOutcome("pre_consume_quota_failed", apiErr.StatusCode, apiErr.Err)
		}
	}

	requestURL := getMjRequestPath(c.Request.URL.String())
	fullRequestURL := strings.TrimRight(channel.GetBaseURL(), "/") + requestURL
	if useDurableReservation {
		deadline := time.Now().Add(midjourneySubmissionTimeout)
		if requestDeadline, ok := c.Request.Context().Deadline(); ok && requestDeadline.Before(deadline) {
			deadline = requestDeadline
		}
		relayInfo.AsyncBillingSendDeadlineMs = deadline.UnixMilli()
	}
	midjourneyResponse, responseBody, requestErr := service.DoMidjourneyHttpRequest(
		c,
		midjourneySubmissionTimeout,
		fullRequestURL,
		apiKey,
		channel.GetSetting().Proxy,
		requestBody,
	)
	if requestErr != nil {
		persistErr := error(nil)
		if useDurableReservation {
			if midjourneyUpstreamSent(c) && (!midjourneyResponse.UpstreamResponseReceived ||
				classifyAsyncSubmissionHTTPStatus(midjourneyResponse.UpstreamStatusCode) != asyncSubmissionOutcomeRejected) {
				persistErr = markMidjourneyReservationManualReview(
					c, relayInfo, "midjourney_transport_outcome_ambiguous: "+requestErr.Error(), "",
				)
			} else if midjourneyUpstreamSent(c) {
				persistErr = rejectAndReleaseMidjourneyReservation(
					c, relayInfo, fmt.Sprintf("midjourney_rejected_status_%d", midjourneyResponse.StatusCode),
				)
			} else {
				persistErr = releaseMidjourneyReservation(c, relayInfo)
			}
		} else if task != nil {
			refundDurable, updateErr := failMidjourneySubmission(c.Request.Context(), task, midjourneyResponse.Response.Code, midjourneyResponse.Response.Description)
			if updateErr != nil {
				logger.LogError(c, "mark Midjourney submit failure: "+updateErr.Error())
			} else if refundDurable {
				refundViaSession = false
			}
		}
		return MidjourneyRelayOutcome{
			Error: &midjourneyResponse.Response, StatusCode: midjourneyResponse.StatusCode,
			LocalError: !midjourneyUpstreamSent(c), Cause: errors.Join(requestErr, persistErr),
		}
	}
	if request.RelayMode == relayconstant.RelayModeMidjourneyUpload {
		var uploadResponse dto.MidjourneyUploadResponse
		if err := common.Unmarshal(responseBody, &uploadResponse); err != nil {
			return midjourneyLocalOutcome("unmarshal_upload_response_failed", http.StatusBadGateway, err)
		}
		if midjourneyResponse.StatusCode < http.StatusOK || midjourneyResponse.StatusCode >= http.StatusMultipleChoices || uploadResponse.Code != 1 {
			return MidjourneyRelayOutcome{
				Error:      &dto.MidjourneyResponse{Code: uploadResponse.Code, Description: uploadResponse.Description},
				StatusCode: midjourneyApplicationStatus(uploadResponse.Code, midjourneyResponse.StatusCode),
			}
		}
		if request.ConsumesQuota {
			if err := service.SettleBilling(c, relayInfo, priceData.Quota); err != nil {
				return midjourneyLocalOutcome("settle_billing_failed", http.StatusInternalServerError, err)
			}
			service.LogTaskConsumption(c, relayInfo)
		}
		completed = true
		c.Data(midjourneyResponse.StatusCode, "application/json", responseBody)
		return MidjourneyRelayOutcome{StatusCode: midjourneyResponse.StatusCode, UpstreamAccepted: true}
	}

	response := midjourneyResponse.Response
	accepted := midjourneyResponse.StatusCode >= http.StatusOK &&
		midjourneyResponse.StatusCode < http.StatusMultipleChoices &&
		(response.Code == 1 || response.Code == 21 || response.Code == 22) &&
		strings.TrimSpace(response.Result) != ""
	if !accepted {
		persistErr := error(nil)
		if useDurableReservation {
			httpOutcome := classifyAsyncSubmissionHTTPStatus(midjourneyResponse.UpstreamStatusCode)
			explicitApplicationRejection := httpOutcome == asyncSubmissionOutcomeAccepted && response.Code != 0 &&
				response.Code != 1 && response.Code != 21 && response.Code != 22
			if httpOutcome == asyncSubmissionOutcomeRejected || explicitApplicationRejection {
				persistErr = rejectAndReleaseMidjourneyReservation(
					c, relayInfo, fmt.Sprintf("midjourney_rejected_status_%d_code_%d", midjourneyResponse.UpstreamStatusCode, response.Code),
				)
			} else {
				persistErr = markMidjourneyReservationManualReview(
					c, relayInfo, fmt.Sprintf("midjourney_outcome_ambiguous_status_%d_code_%d", midjourneyResponse.UpstreamStatusCode, response.Code), "",
				)
			}
		} else if task != nil {
			refundDurable, updateErr := failMidjourneySubmission(c.Request.Context(), task, response.Code, response.Description)
			if updateErr != nil {
				logger.LogError(c, "mark Midjourney application failure: "+updateErr.Error())
			} else if refundDurable {
				refundViaSession = false
			}
		}
		if response.Code == 3 && channel.GetAutoBan() && common.AutomaticDisableChannelEnabled {
			model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "No available account instance")
		}
		return MidjourneyRelayOutcome{
			Error: &response, StatusCode: midjourneyApplicationStatus(response.Code, midjourneyResponse.StatusCode), Cause: persistErr,
		}
	}

	status := ""
	progress := "0%"
	imageURL := ""
	startTime := int64(0)
	finishTime := int64(0)
	if response.Code == 21 {
		if properties, ok := response.Properties.(map[string]interface{}); ok {
			imageURL, _ = properties["imageUrl"].(string)
			status, _ = properties["status"].(string)
			status = strings.ToUpper(strings.TrimSpace(status))
			if status == "SUCCESS" || status == "FAILURE" {
				progress = "100%"
				startTime = time.Now().UnixMilli()
				finishTime = startTime
			}
		}
	}
	if request.Request.Action == constant.MjActionUpload {
		status = "SUCCESS"
		progress = "100%"
	}
	upstreamTaskID = strings.TrimSpace(response.Result)
	if !service.ValidMidjourneyTaskReference(upstreamTaskID) {
		var updateErr error
		if useDurableReservation {
			updateErr = markMidjourneyReservationManualReview(
				c, relayInfo, "upstream_task_identity_invalid", upstreamTaskID,
			)
		} else {
			refundDurable, legacyErr := failMidjourneySubmission(c.Request.Context(), task, constant.MjErrorUnknown, "upstream_task_identity_invalid")
			updateErr = legacyErr
			if updateErr == nil && refundDurable {
				refundViaSession = false
			}
		}
		return midjourneyLocalOutcome("upstream_task_identity_invalid", http.StatusBadGateway, updateErr)
	}
	task.Code = response.Code
	task.UpstreamTaskID = upstreamTaskID
	task.Description = response.Description
	task.Progress = progress
	task.ImageUrl = imageURL
	task.StartTime = startTime
	task.FinishTime = finishTime
	if status != "" {
		task.Status = status
	}
	if task.Status == "FAILURE" {
		task.FailReason = common.SanitizeErrorMessage(response.Description)
	}
	clientResponse := response
	if clientResponse.Code == 22 || (clientResponse.Code == 21 &&
		request.Request.Action != constant.MjActionInPaint &&
		request.Request.Action != constant.MjActionCustomZoom) {
		clientResponse.Code = 1
	}
	clientResponse.Result = task.MjId
	responseBody, replaySpec, replaySnapshotErr := midjourneyAcceptedReplaySnapshot(
		clientResponse, midjourneyResponse.StatusCode, task.MjId,
	)
	if replaySnapshotErr != nil && !useDurableReservation {
		return midjourneyLocalOutcome("marshal_response_body_failed", http.StatusInternalServerError, replaySnapshotErr)
	}
	if useDurableReservation {
		finalIntent, finalAuditErr := buildAsyncBillingAcceptanceIntent(c, relayInfo, nil, task)
		if finalAuditErr != nil {
			reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, reviewErr := model.MarkAsyncBillingAcceptedHandoffManualReviewFromAttempt(
				reviewContext, relayInfo.AsyncBillingReservationID, task.Quota, upstreamTaskID,
				replaySpec, "freeze_final_midjourney_billing_audit_failed: "+finalAuditErr.Error(), time.Now(),
			)
			reviewCancel()
			if reviewErr == nil {
				relayInfo.AsyncBillingManualReviewMarked = true
			}
			return midjourneyLocalOutcome(
				"persist_midjourney_task_failed", http.StatusInternalServerError, errors.Join(finalAuditErr, reviewErr),
			)
		}
		finalAudit := finalIntent.Audit
		if finalAudit.AdminInfo == nil {
			finalAudit.AdminInfo = map[string]any{}
		}
		finalAudit.AdminInfo["billing_context_phase"] = "post_submit_final"
		if replaySnapshotErr != nil {
			reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, reviewErr := model.MarkAsyncBillingAcceptedHandoffManualReview(
				reviewContext, relayInfo.AsyncBillingReservationID, task.Quota, finalAudit,
				upstreamTaskID, replaySpec,
				"freeze_midjourney_response_failed: "+replaySnapshotErr.Error(), time.Now(),
			)
			reviewCancel()
			if reviewErr == nil {
				relayInfo.AsyncBillingManualReviewMarked = true
			}
			return midjourneyLocalOutcome(
				"persist_midjourney_task_failed", http.StatusInternalServerError,
				errors.Join(replaySnapshotErr, reviewErr),
			)
		}
		persistContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
		_, acceptErr := model.AcceptAsyncMidjourneyReservation(
			persistContext,
			relayInfo.AsyncBillingReservationID,
			relayInfo.RetryIndex,
			task,
			upstreamTaskID,
			task.Quota,
			finalAudit,
			replaySpec,
			time.Now(),
		)
		cancel()
		if acceptErr != nil {
			reviewContext, reviewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, reviewErr := model.MarkAsyncBillingAcceptedHandoffManualReview(
				reviewContext, relayInfo.AsyncBillingReservationID, task.Quota, finalAudit,
				upstreamTaskID, replaySpec, "accepted_midjourney_handoff_failed: "+acceptErr.Error(), time.Now(),
			)
			reviewCancel()
			if reviewErr == nil {
				relayInfo.AsyncBillingManualReviewMarked = true
			}
			return midjourneyLocalOutcome(
				"persist_midjourney_task_failed", http.StatusInternalServerError, errors.Join(acceptErr, reviewErr),
			)
		}
	} else {
		updates := map[string]any{
			"code": task.Code, "upstream_task_id": task.UpstreamTaskID,
			"description": task.Description, "progress": task.Progress,
			"image_url": task.ImageUrl, "start_time": task.StartTime, "finish_time": task.FinishTime,
			"fail_reason": task.FailReason,
		}
		if status != "" {
			updates["status"] = status
		}
		if err := model.UpdateMidjourneyByID(task.Id, updates); err != nil {
			logger.LogError(c, "persist Midjourney upstream identity: "+err.Error())
			refundDurable, updateErr := failMidjourneySubmission(c.Request.Context(), task, constant.MjErrorUnknown, "update_midjourney_task_failed")
			if updateErr == nil && refundDurable {
				refundViaSession = false
			}
			return midjourneyLocalOutcome("update_midjourney_task_failed", http.StatusInternalServerError, errors.Join(err, updateErr))
		}
	}

	if request.ConsumesQuota && !useDurableReservation {
		if err := service.SettleBilling(c, relayInfo, priceData.Quota); err != nil {
			return midjourneyLocalOutcome("settle_billing_failed", http.StatusInternalServerError, err)
		}
		service.LogTaskConsumption(c, relayInfo)
	}
	completed = true
	c.Data(midjourneyResponse.StatusCode, "application/json", responseBody)
	return MidjourneyRelayOutcome{
		StatusCode: midjourneyResponse.StatusCode, UpstreamAccepted: true,
	}
}

func midjourneyAcceptedReplaySnapshot(
	response dto.MidjourneyResponse,
	statusCode int,
	publicTaskID string,
) ([]byte, model.AsyncBillingReplaySpec, error) {
	body, snapshotErr := common.Marshal(response)
	if snapshotErr == nil && len(body) > int(maxTaskSubmissionResponseBytes) {
		snapshotErr = fmt.Errorf("midjourney response exceeds %d bytes", maxTaskSubmissionResponseBytes)
	}
	if strings.TrimSpace(publicTaskID) == "" || strings.TrimSpace(response.Result) != strings.TrimSpace(publicTaskID) {
		snapshotErr = errors.Join(snapshotErr, errors.New("midjourney response does not match the public task identity"))
	}
	if snapshotErr == nil {
		var frozen dto.MidjourneyResponse
		if err := common.Unmarshal(body, &frozen); err != nil || strings.TrimSpace(frozen.Result) != strings.TrimSpace(publicTaskID) {
			snapshotErr = errors.Join(snapshotErr, errors.New("midjourney replay body does not preserve the public task identity"), err)
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		snapshotErr = errors.Join(snapshotErr, fmt.Errorf("midjourney accepted response status is invalid: %d", statusCode))
	}
	if snapshotErr == nil {
		return body, model.AsyncBillingReplaySpec{
			StatusCode: statusCode, ContentType: "application/json", Body: body,
		}, nil
	}
	fallback := dto.MidjourneyResponse{
		Code: 1, Description: "accepted task requires durable reconciliation", Result: publicTaskID,
	}
	fallbackBody, fallbackErr := common.Marshal(fallback)
	if fallbackErr != nil {
		return nil, model.AsyncBillingReplaySpec{}, errors.Join(snapshotErr, fallbackErr)
	}
	return fallbackBody, model.AsyncBillingReplaySpec{
		StatusCode: http.StatusOK, ContentType: "application/json", Body: fallbackBody,
	}, snapshotErr
}

func releaseMidjourneyReservation(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil || info.AsyncBillingReservationID <= 0 {
		return nil
	}
	persistContext := context.Background()
	if c != nil && c.Request != nil {
		persistContext = context.WithoutCancel(c.Request.Context())
	}
	persistContext, cancel := context.WithTimeout(persistContext, 5*time.Second)
	defer cancel()
	_, err := model.ReleaseAsyncBillingReservation(persistContext, info.AsyncBillingReservationID, time.Now())
	return err
}

func rejectAndReleaseMidjourneyReservation(c *gin.Context, info *relaycommon.RelayInfo, reason string) error {
	if info == nil || info.AsyncBillingReservationID <= 0 {
		return nil
	}
	persistContext := context.Background()
	if c != nil && c.Request != nil {
		persistContext = context.WithoutCancel(c.Request.Context())
	}
	persistContext, cancel := context.WithTimeout(persistContext, 5*time.Second)
	defer cancel()
	if err := model.RejectAsyncBillingAttempt(
		persistContext, info.AsyncBillingReservationID, info.RetryIndex, reason, time.Now(),
	); err != nil {
		return err
	}
	_, err := model.ReleaseAsyncBillingReservation(persistContext, info.AsyncBillingReservationID, time.Now())
	return err
}

func markMidjourneyReservationManualReview(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	reason string,
	upstreamTaskID string,
) error {
	if info == nil || info.AsyncBillingReservationID <= 0 {
		return nil
	}
	persistContext := context.Background()
	if c != nil && c.Request != nil {
		persistContext = context.WithoutCancel(c.Request.Context())
	}
	persistContext, cancel := context.WithTimeout(persistContext, 5*time.Second)
	defer cancel()
	_, err := model.MarkAsyncBillingReservationManualReview(
		persistContext, info.AsyncBillingReservationID, reason, upstreamTaskID, time.Now(),
	)
	if err == nil {
		info.AsyncBillingManualReviewMarked = true
	}
	return err
}

func failMidjourneySubmission(ctx context.Context, task *model.Midjourney, code int, description string) (bool, error) {
	if task == nil {
		return false, nil
	}
	if strings.TrimSpace(description) == "" {
		description = "midjourney_submit_failed"
	}
	description = common.SanitizeErrorMessage(description)
	previousStatus := task.Status
	task.Code = code
	task.Description = description
	task.FailReason = description
	task.Status = "FAILURE"
	task.Progress = "100%"
	finalization, err := finalizeMidjourneyFailureAndQueueRefund(ctx, task, previousStatus)
	if err != nil {
		return false, err
	}
	if finalization.Operation.ID == 0 || finalization.Operation.Kind != model.TaskBillingOperationKindRefund {
		return false, nil
	}
	return true, nil
}

func finalizeMidjourneyFailureAndQueueRefund(
	ctx context.Context,
	task *model.Midjourney,
	fromStatus string,
) (*model.MidjourneyFailureFinalization, error) {
	finalization, err := service.FinalizeMidjourneyFailure(ctx, task, fromStatus)
	if err != nil {
		return nil, err
	}
	if finalization.Operation.ID == 0 || finalization.Operation.Kind != model.TaskBillingOperationKindRefund {
		return finalization, nil
	}
	owner := "midjourney-relay:" + common.GetUUID()
	if _, processErr := service.ProcessMidjourneyBillingOperation(ctx, finalization.Operation.ID, owner, time.Minute); processErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("deferred Midjourney refund remains durable: operation=%s error=%s",
			finalization.Operation.OperationKey, common.SanitizeErrorMessage(processErr.Error())))
	}
	return finalization, nil
}

func midjourneyApplicationStatus(code int, upstreamStatus int) int {
	if upstreamStatus < http.StatusOK || upstreamStatus >= http.StatusMultipleChoices {
		return upstreamStatus
	}
	switch code {
	case 23:
		return http.StatusTooManyRequests
	case 24:
		return http.StatusBadRequest
	case 3:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func midjourneyUpstreamSent(c *gin.Context) bool {
	state := relaycommon.RoutingUpstreamSendStateFromContext(c)
	return state != nil && state.Sent()
}

type taskChangeParams struct {
	ID     string
	Action string
	Index  int
}

func getMjRequestPath(path string) string {
	requestURL := path
	if strings.Contains(requestURL, "/mj-") {
		urls := strings.Split(requestURL, "/mj/")
		if len(urls) < 2 {
			return requestURL
		}
		requestURL = "/mj/" + urls[1]
	}
	return requestURL
}
