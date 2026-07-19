package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const (
	workerRequestTimeout   = 2 * time.Minute
	defaultDownloadLimitMB = 64
	bytesPerMiB            = int64(1024 * 1024)
	maxInt64Value          = int64(^uint64(0) >> 1)
)

type boundedDownloadBody struct {
	body      io.ReadCloser
	remaining int64
	limit     int64
	exceeded  bool
}

var workerServiceClientCache PinnedServiceClientCache

// WorkerRequest Worker请求的数据结构
type WorkerRequest struct {
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// DoWorkerRequest 通过Worker发送请求
func DoWorkerRequest(req *WorkerRequest) (*http.Response, error) {
	if !system_setting.EnableWorker() {
		return nil, fmt.Errorf("worker not enabled")
	}
	if req == nil {
		return nil, fmt.Errorf("worker request is required")
	}
	if err := validateWorkerTargetURL(req.URL, system_setting.WorkerAllowHttpImageRequestEnabled); err != nil {
		return nil, err
	}

	// SSRF防护：验证请求URL
	fetchSetting := system_setting.GetFetchSetting()
	if err := common.ValidateURLWithFetchSetting(req.URL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain); err != nil {
		return nil, fmt.Errorf("request reject: %v", err)
	}

	workerURL, workerClient, err := workerServiceClientCache.Get(system_setting.WorkerUrl, workerRequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("invalid worker URL: %w", err)
	}
	if !strings.HasSuffix(workerURL.Path, "/") {
		workerURL.Path += "/"
		workerURL.RawPath = ""
	}

	// 序列化worker请求数据
	workerPayload, err := common.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal worker payload: %v", err)
	}

	httpRequest, err := http.NewRequest(http.MethodPost, workerURL.String(), bytes.NewReader(workerPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create worker request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "*/*")
	// The client pins the operator-configured scheme, host, and port, rejects
	// redirects, and resolves the configured host immediately before dialing.
	// codeql[go/request-forgery]
	return workerClient.Do(httpRequest)
}

func DoDownloadRequest(originUrl string, reason ...string) (*http.Response, error) {
	var response *http.Response
	var err error
	if system_setting.EnableWorker() {
		common.SysLog(fmt.Sprintf("downloading file from worker: %s, reason: %s", common.MaskSensitiveInfo(originUrl), strings.Join(reason, ", ")))
		req := &WorkerRequest{
			URL: originUrl,
			Key: system_setting.WorkerValidKey,
		}
		response, err = DoWorkerRequest(req)
	} else {
		// SSRF防护：验证请求URL（非Worker模式）
		if err := ValidateSSRFProtectedFetchURL(originUrl); err != nil {
			return nil, fmt.Errorf("request reject: %v", err)
		}

		common.SysLog(fmt.Sprintf("downloading from origin: %s, reason: %s", common.MaskSensitiveInfo(originUrl), strings.Join(reason, ", ")))
		// The URL is checked under the explicit operator fetch policy above. When
		// protection is enabled, the dialer repeats host, port, and resolved-IP
		// checks immediately before connecting; the disable switch is retained only
		// for the documented v0.1.6 administrator compatibility contract.
		// codeql[go/request-forgery]
		response, err = GetSSRFProtectedHTTPClient().Get(originUrl)
	}
	if err != nil {
		return nil, err
	}
	return boundDownloadResponse(response)
}

func validateWorkerTargetURL(rawURL string, allowHTTP bool) error {
	parsedURL, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsedURL.Hostname() == "" || parsedURL.User != nil {
		return fmt.Errorf("invalid worker target URL")
	}
	switch parsedURL.Scheme {
	case "https":
		return nil
	case "http":
		if allowHTTP {
			return nil
		}
		return fmt.Errorf("worker target URL must use HTTPS")
	default:
		return fmt.Errorf("worker target URL must use HTTP or HTTPS")
	}
}

func boundDownloadResponse(response *http.Response) (*http.Response, error) {
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("download returned an empty response")
	}
	maxBytes, err := maximumDownloadBytes(constant.MaxFileDownloadMB)
	if err != nil {
		response.Body.Close()
		return nil, err
	}
	if response.ContentLength > maxBytes {
		response.Body.Close()
		return nil, fmt.Errorf("download size %d exceeds maximum allowed size of %d bytes", response.ContentLength, maxBytes)
	}
	response.Body = &boundedDownloadBody{
		body:      response.Body,
		remaining: maxBytes,
		limit:     maxBytes,
	}
	return response, nil
}

func maximumDownloadBytes(maxMB int) (int64, error) {
	if maxMB <= 0 {
		maxMB = defaultDownloadLimitMB
	}
	if int64(maxMB) > maxInt64Value/bytesPerMiB {
		return 0, fmt.Errorf("maximum download size is too large")
	}
	return int64(maxMB) * bytesPerMiB, nil
}

func (body *boundedDownloadBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if body.exceeded {
		return 0, fmt.Errorf("download exceeds maximum allowed size of %d bytes", body.limit)
	}
	if body.remaining > 0 {
		readLimit := len(buffer)
		if int64(readLimit) > body.remaining {
			readLimit = int(body.remaining)
		}
		read, err := body.body.Read(buffer[:readLimit])
		body.remaining -= int64(read)
		return read, err
	}

	var probe [1]byte
	read, err := body.body.Read(probe[:])
	if read > 0 {
		body.exceeded = true
		return 0, fmt.Errorf("download exceeds maximum allowed size of %d bytes", body.limit)
	}
	return 0, err
}

func (body *boundedDownloadBody) Close() error {
	return body.body.Close()
}
