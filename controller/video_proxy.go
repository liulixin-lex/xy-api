package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const (
	videoProxyTimeout                = 60 * time.Second
	maxVideoProxyResponseBytes int64 = 1 << 30
	maxInlineVideoBytes        int64 = 64 << 20
)

var (
	errVideoProxyCredentialIdentityMissing = errors.New("video task credential identity is missing")
	errVideoProxyCredentialUnavailable     = errors.New("video task credential is unavailable")
	videoProxyHTTPClientFactory            = service.GetStatefulFetchHTTPClient
)

// videoProxyError returns a standardized OpenAI-style error response.
func videoProxyError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

func VideoProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	userID := c.GetInt("id")
	task, exists, err := model.GetByTaskId(userID, taskID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to query task %s: %s", taskID, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to query task")
		return
	}
	if !exists || task == nil {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Task not found")
		return
	}

	if task.Status != model.TaskStatusSuccess {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Task is not completed yet, current status: %s", task.Status))
		return
	}
	if authorizationErr := middleware.AuthorizeTokenRoutingTarget(
		c, task.GetOriginModelName(), task.ChannelId, true,
	); authorizationErr != nil {
		videoProxyError(
			c,
			authorizationErr.StatusCode,
			string(authorizationErr.GetErrorCode()),
			authorizationErr.MaskSensitiveError(),
		)
		return
	}

	// Stateful result access intentionally loads the current channel from the
	// database instead of relying on a routing snapshot that may have advanced.
	channel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to get channel for video task %s", taskID))
		videoProxyError(c, http.StatusServiceUnavailable, "task_credential_unavailable", "Task credential is currently unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), videoProxyTimeout)
	defer cancel()

	channelBaseURL := strings.TrimSpace(channel.GetBaseURL())
	baseURL := channelBaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	var (
		videoURL           string
		credential         string
		credentialNeeded   bool
		trustedNewAPIMedia bool
	)
	requestHeaders := make(http.Header)

	switch channel.Type {
	case constant.ChannelTypeGemini:
		credentialNeeded = true
	case constant.ChannelTypeVertexAi:
		credentialNeeded = true
	case constant.ChannelTypeOpenAI, constant.ChannelTypeSora:
		credentialNeeded = true
	default:
		videoURL = strings.TrimSpace(task.GetUpstreamResultURL())
		if strings.HasPrefix(videoURL, "data:") {
			if err := writeVideoDataURL(c, videoURL); err != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to decode bounded video data URL for task %s", taskID))
				if !c.Writer.Written() {
					videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
				}
			}
			return
		}
		if videoURL == "" {
			// Preserve the historical credential-identity error boundary for tasks
			// that never stored any provider media location.
			credentialNeeded = true
		} else {
			resolution, resolveErr := service.ResolveStatefulMediaURL(
				channelBaseURL, videoURL, service.StatefulMediaNewAPIVideo,
			)
			if resolveErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Rejected relative video URL for task %s", taskID))
				videoProxyError(c, http.StatusForbidden, "server_error", "Video URL is not allowed by the outbound policy")
				return
			}
			videoURL = resolution.URL
			trustedNewAPIMedia = resolution.TrustedSameOrigin
			credentialNeeded = trustedNewAPIMedia
		}
	}

	if credentialNeeded {
		if trustedNewAPIMedia && task.PrivateData.RoutingCredentialID <= 0 {
			err = errVideoProxyCredentialIdentityMissing
		} else {
			credential, err = resolveVideoProxyCredential(c.Request.Context(), channel, task.PrivateData)
		}
		if err != nil {
			if errors.Is(err, errVideoProxyCredentialIdentityMissing) {
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("Historical video task %s has no stable credential identity", taskID))
				videoProxyError(c, http.StatusConflict, "task_credential_identity_missing", "Historical task has no stable credential identity")
				return
			}
			logger.LogError(c.Request.Context(), fmt.Sprintf("Stable credential is unavailable for video task %s", taskID))
			videoProxyError(c, http.StatusServiceUnavailable, "task_credential_unavailable", "Task credential is currently unavailable")
			return
		}
	}

	switch channel.Type {
	case constant.ChannelTypeGemini:
		videoURL, err = getGeminiVideoURL(ctx, channel, task, credential)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Gemini video URL for task %s", taskID))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Gemini video URL")
			return
		}
		requestHeaders.Set("x-goog-api-key", credential)
	case constant.ChannelTypeVertexAi:
		videoURL, err = getVertexVideoURL(ctx, channel, task, credential)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Vertex video URL for task %s", taskID))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Vertex video URL")
			return
		}
	case constant.ChannelTypeOpenAI, constant.ChannelTypeSora:
		upstreamTaskID := strings.TrimSpace(task.GetUpstreamTaskID())
		if upstreamTaskID == "" {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Upstream task identity is empty for video task %s", taskID))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve upstream video identity")
			return
		}
		videoURL = fmt.Sprintf("%s/v1/videos/%s/content", strings.TrimRight(baseURL, "/"), url.PathEscape(upstreamTaskID))
		requestHeaders.Set("Authorization", "Bearer "+credential)
	default:
		if trustedNewAPIMedia {
			requestHeaders.Set("Authorization", "Bearer "+credential)
		}
	}

	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video URL is empty for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}

	if strings.HasPrefix(videoURL, "data:") {
		if err := writeVideoDataURL(c, videoURL); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to decode bounded video data URL for task %s", taskID))
			if !c.Writer.Written() {
				videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
			}
		}
		return
	}

	proxy := strings.TrimSpace(channel.GetSetting().Proxy)
	parsedVideoURL, err := parseAndValidateVideoProxyURL(videoURL)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Outbound policy blocked the video URL for task %s", taskID))
		videoProxyError(c, http.StatusForbidden, "server_error", "Video URL is not allowed by the outbound policy")
		return
	}
	if len(requestHeaders) > 0 && !strings.EqualFold(parsedVideoURL.Scheme, "https") {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Plaintext credential transport was blocked for video task %s", taskID))
		videoProxyError(c, http.StatusForbidden, "server_error", "Credential-bearing video URLs must use HTTPS")
		return
	}

	client, err := videoProxyHTTPClientFactory(proxy)
	if err != nil || client == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create bounded video client for task %s", taskID))
		videoProxyError(c, http.StatusServiceUnavailable, "server_error", "Failed to create video proxy client")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedVideoURL.String(), nil)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create video request for task %s", taskID))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy request")
		return
	}
	for key, values := range requestHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if rangeHeader := strings.TrimSpace(c.GetHeader("Range")); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	if ifRangeHeader := strings.TrimSpace(c.GetHeader("If-Range")); ifRangeHeader != "" {
		req.Header.Set("If-Range", ifRangeHeader)
	}

	resp, err := service.DoStatefulFetch(client, req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to fetch video content for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		copyVideoProxyResponseHeaders(c.Writer.Header(), resp.Header, true)
		c.Writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Upstream returned status %d for video task %s", resp.StatusCode, taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error",
			fmt.Sprintf("Upstream service returned status %d", resp.StatusCode))
		return
	}
	if resp.ContentLength > maxVideoProxyResponseBytes {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video response exceeded the configured limit for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Video content exceeds the proxy limit")
		return
	}
	if err := validateVideoProxyResponseMediaType(resp.Header.Get("Content-Type")); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video response media type was rejected for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Upstream returned an unsupported video media type")
		return
	}

	copyVideoProxyResponseHeaders(c.Writer.Header(), resp.Header, false)
	c.Writer.Header().Set("Cache-Control", "private, max-age=86400")
	c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
	c.Writer.WriteHeader(resp.StatusCode)
	written, copyErr := io.Copy(c.Writer, io.LimitReader(resp.Body, maxVideoProxyResponseBytes))
	if copyErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video stream ended early for task %s", taskID))
		return
	}
	if written == maxVideoProxyResponseBytes {
		var extra [1]byte
		if count, _ := resp.Body.Read(extra[:]); count > 0 {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Video stream was truncated at the configured limit for task %s", taskID))
		}
	}
}

func resolveVideoProxyCredential(ctx context.Context, channel *model.Channel, privateData model.TaskPrivateData) (string, error) {
	if channel == nil {
		return "", errVideoProxyCredentialUnavailable
	}
	credentialID := privateData.RoutingCredentialID
	if credentialID > 0 {
		credential, _, err := channelrouting.ResolvePersistedCredentialKey(ctx, channel, credentialID)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errVideoProxyCredentialUnavailable, err)
		}
		if strings.TrimSpace(credential) == "" {
			return "", errVideoProxyCredentialUnavailable
		}
		return credential, nil
	}
	if credential, ok := privateData.HistoricalPlaintextCredential(); ok {
		return credential, nil
	}
	if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
		return "", errVideoProxyCredentialIdentityMissing
	}
	if strings.TrimSpace(channel.Key) == "" {
		return "", errVideoProxyCredentialUnavailable
	}
	return channel.Key, nil
}

func parseAndValidateVideoProxyURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, errors.New("invalid absolute video URL")
	}
	if parsed.User != nil {
		return nil, errors.New("video URL userinfo is not allowed")
	}
	parsed.Fragment = ""
	if err := service.ValidateStatefulFetchURL(parsed.String()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateVideoProxyResponseMediaType(rawContentType string) error {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(rawContentType))
	if err != nil {
		return errors.New("invalid video response media type")
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "video/") || mediaType == "application/octet-stream" ||
		mediaType == "multipart/byteranges" {
		return nil
	}
	return errors.New("unsupported video response media type")
}

func copyVideoProxyResponseHeaders(destination, source http.Header, rangeError bool) {
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

func writeVideoDataURL(c *gin.Context, dataURL string) error {
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid data url")
	}

	header := parts[0]
	payload := parts[1]
	if !strings.HasPrefix(header, "data:") {
		return fmt.Errorf("unsupported data url")
	}

	metadata := strings.TrimPrefix(header, "data:")
	separator := strings.LastIndex(metadata, ";")
	if separator < 0 || !strings.EqualFold(metadata[separator+1:], "base64") {
		return fmt.Errorf("unsupported data url")
	}
	mediaType := metadata[:separator]
	if mediaType == "" {
		mediaType = "video/mp4"
	}
	mimeType, _, err := mime.ParseMediaType(mediaType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mimeType), "video/") {
		return fmt.Errorf("unsupported video media type")
	}
	maxEncodedBytes := int64(base64.StdEncoding.EncodedLen(int(maxInlineVideoBytes)))
	if int64(len(payload)) > maxEncodedBytes+2 {
		return fmt.Errorf("inline video exceeds limit")
	}

	videoBytes, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		videoBytes, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return err
		}
	}
	if int64(len(videoBytes)) > maxInlineVideoBytes {
		return fmt.Errorf("inline video exceeds limit")
	}

	c.Writer.Header().Set("Content-Type", mimeType)
	c.Writer.Header().Set("Cache-Control", "private, max-age=86400")
	c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
	c.Writer.WriteHeader(http.StatusOK)
	_, err = c.Writer.Write(videoBytes)
	return err
}
