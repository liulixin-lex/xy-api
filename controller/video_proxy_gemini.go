package controller

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/service"
)

const maxVideoProxyMetadataResponseBytes int64 = 64 << 20

func getGeminiVideoURL(ctx context.Context, channel *model.Channel, task *model.Task, apiKey string) (string, error) {
	if channel == nil || task == nil {
		return "", fmt.Errorf("invalid channel or task")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", fmt.Errorf("api key not available for task")
	}
	if videoURL := strings.TrimSpace(task.GetUpstreamResultURL()); videoURL != "" {
		if strings.HasPrefix(strings.ToLower(videoURL), "data:") {
			return videoURL, nil
		}
		return ensureAPIKey(videoURL, apiKey)
	}

	if videoURL := extractGeminiVideoURLFromTaskData(task); videoURL != "" {
		return ensureAPIKey(videoURL, apiKey)
	}

	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	if err := validateVideoTaskMetadataBaseURL(baseURL); err != nil {
		return "", err
	}

	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channel.Type)))
	if adaptor == nil {
		return "", fmt.Errorf("gemini task adaptor not found")
	}

	proxy := channel.GetSetting().Proxy
	resp, err := adaptor.FetchTask(ctx, baseURL, apiKey, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, proxy)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return "", fmt.Errorf("fetch task failed: %w", err)
	}
	body, err := readVideoProxyMetadataResponse(resp)
	if err != nil {
		return "", err
	}

	taskInfo, parseErr := adaptor.ParseTaskResult(ctx, body)
	if parseErr == nil && taskInfo != nil && taskInfo.RemoteUrl != "" {
		return ensureAPIKey(taskInfo.RemoteUrl, apiKey)
	}

	if videoURL := extractGeminiVideoURLFromPayload(body); videoURL != "" {
		return ensureAPIKey(videoURL, apiKey)
	}

	if parseErr != nil {
		return "", fmt.Errorf("parse task result failed: %w", parseErr)
	}

	return "", fmt.Errorf("gemini video url not found")
}

func readVideoProxyMetadataResponse(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("task response is empty")
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxVideoProxyMetadataResponseBytes {
		return nil, fmt.Errorf("task response exceeds limit")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("task response returned status %d", resp.StatusCode)
	}
	if err := validateVideoProxyJSONMediaType(resp.Header.Get("Content-Type")); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVideoProxyMetadataResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read task response failed: %w", err)
	}
	if int64(len(body)) > maxVideoProxyMetadataResponseBytes {
		return nil, fmt.Errorf("task response exceeds limit")
	}
	return body, nil
}

func validateVideoProxyJSONMediaType(rawContentType string) error {
	rawContentType = strings.TrimSpace(rawContentType)
	if rawContentType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(rawContentType)
	if err != nil {
		return fmt.Errorf("invalid task response media type")
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		return nil
	}
	return fmt.Errorf("unsupported task response media type")
}

func extractGeminiVideoURLFromTaskData(task *model.Task) string {
	if task == nil || len(task.Data) == 0 {
		return ""
	}
	var payload map[string]any
	if err := common.Unmarshal(task.Data, &payload); err != nil {
		return ""
	}
	return extractGeminiVideoURLFromMap(payload)
}

func extractGeminiVideoURLFromPayload(body []byte) string {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return extractGeminiVideoURLFromMap(payload)
}

func extractGeminiVideoURLFromMap(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if uri, ok := payload["uri"].(string); ok && uri != "" {
		return uri
	}
	if resp, ok := payload["response"].(map[string]any); ok {
		if uri := extractGeminiVideoURLFromResponse(resp); uri != "" {
			return uri
		}
	}
	return ""
}

func extractGeminiVideoURLFromResponse(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	if gvr, ok := resp["generateVideoResponse"].(map[string]any); ok {
		if uri := extractGeminiVideoURLFromGeneratedSamples(gvr); uri != "" {
			return uri
		}
	}
	if videos, ok := resp["videos"].([]any); ok {
		for _, video := range videos {
			if vm, ok := video.(map[string]any); ok {
				if uri, ok := vm["uri"].(string); ok && uri != "" {
					return uri
				}
			}
		}
	}
	if uri, ok := resp["video"].(string); ok && uri != "" {
		return uri
	}
	if uri, ok := resp["uri"].(string); ok && uri != "" {
		return uri
	}
	return ""
}

func extractGeminiVideoURLFromGeneratedSamples(gvr map[string]any) string {
	if gvr == nil {
		return ""
	}
	if samples, ok := gvr["generatedSamples"].([]any); ok {
		for _, sample := range samples {
			if sm, ok := sample.(map[string]any); ok {
				if video, ok := sm["video"].(map[string]any); ok {
					if uri, ok := video["uri"].(string); ok && uri != "" {
						return uri
					}
				}
			}
		}
	}
	return ""
}

func getVertexVideoURL(ctx context.Context, channel *model.Channel, task *model.Task, apiKey string) (string, error) {
	if channel == nil || task == nil {
		return "", fmt.Errorf("invalid channel or task")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", fmt.Errorf("vertex key not available for task")
	}
	if videoURL := strings.TrimSpace(task.GetUpstreamResultURL()); videoURL != "" {
		return videoURL, nil
	}
	if url := extractVertexVideoURLFromTaskData(task); url != "" {
		return url, nil
	}

	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	if err := validateVideoTaskMetadataBaseURL(baseURL); err != nil {
		return "", err
	}

	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channel.Type)))
	if adaptor == nil {
		return "", fmt.Errorf("vertex task adaptor not found")
	}

	resp, err := adaptor.FetchTask(ctx, baseURL, apiKey, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, channel.GetSetting().Proxy)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return "", fmt.Errorf("fetch task failed: %w", err)
	}
	body, err := readVideoProxyMetadataResponse(resp)
	if err != nil {
		return "", err
	}

	taskInfo, parseErr := adaptor.ParseTaskResult(ctx, body)
	if parseErr == nil && taskInfo != nil && strings.TrimSpace(taskInfo.Url) != "" {
		return taskInfo.Url, nil
	}
	if url := extractVertexVideoURLFromPayload(body); url != "" {
		return url, nil
	}
	if parseErr != nil {
		return "", fmt.Errorf("parse task result failed: %w", parseErr)
	}
	return "", fmt.Errorf("vertex video url not found")
}

func extractVertexVideoURLFromTaskData(task *model.Task) string {
	if task == nil || len(task.Data) == 0 {
		return ""
	}
	return extractVertexVideoURLFromPayload(task.Data)
}

func extractVertexVideoURLFromPayload(body []byte) string {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return ""
	}
	resp, ok := payload["response"].(map[string]any)
	if !ok || resp == nil {
		return ""
	}

	if videos, ok := resp["videos"].([]any); ok && len(videos) > 0 {
		if video, ok := videos[0].(map[string]any); ok && video != nil {
			if b64, _ := video["bytesBase64Encoded"].(string); strings.TrimSpace(b64) != "" {
				mime, _ := video["mimeType"].(string)
				enc, _ := video["encoding"].(string)
				return buildVideoDataURL(mime, enc, b64)
			}
		}
	}
	if b64, _ := resp["bytesBase64Encoded"].(string); strings.TrimSpace(b64) != "" {
		enc, _ := resp["encoding"].(string)
		return buildVideoDataURL("", enc, b64)
	}
	if video, _ := resp["video"].(string); strings.TrimSpace(video) != "" {
		if strings.HasPrefix(video, "data:") || strings.HasPrefix(video, "http://") || strings.HasPrefix(video, "https://") {
			return video
		}
		// Polling intentionally truncates stored inline payloads. A truncated
		// value is not valid media and must trigger the bounded realtime fetch.
		if strings.HasSuffix(strings.TrimSpace(video), "...") {
			return ""
		}
		enc, _ := resp["encoding"].(string)
		return buildVideoDataURL("", enc, video)
	}
	return ""
}

func buildVideoDataURL(mimeType string, encoding string, base64Data string) string {
	mime := strings.TrimSpace(mimeType)
	if mime == "" {
		enc := strings.TrimSpace(encoding)
		if enc == "" {
			enc = "mp4"
		}
		if strings.Contains(enc, "/") {
			mime = enc
		} else {
			mime = "video/" + enc
		}
	}
	return "data:" + mime + ";base64," + base64Data
}

func validateVideoTaskMetadataBaseURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" || !parsed.IsAbs() || parsed.User != nil {
		return fmt.Errorf("invalid video task metadata base URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("video task metadata base URL must use HTTPS")
	}
	parsed.Fragment = ""
	if err := service.ValidateStatefulFetchURL(parsed.String()); err != nil {
		return fmt.Errorf("video task metadata base URL is not allowed")
	}
	return nil
}

func ensureAPIKey(uri, key string) (string, error) {
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("video URL or API key is empty")
	}
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("invalid Gemini video URL")
	}
	if parsed.User != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("Gemini video URL must use secure transport")
	}
	query := parsed.Query()
	// Authentication is sent through x-goog-api-key. Removing a stale query key
	// keeps credentials out of request URLs and net/http error strings.
	query.Del("key")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
