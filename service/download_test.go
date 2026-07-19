package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type trackedReadCloser struct {
	*bytes.Reader
	closed bool
}

func (r *trackedReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestValidateWorkerTargetURL(t *testing.T) {
	tests := []struct {
		name      string
		rawURL    string
		allowHTTP bool
		wantErr   string
	}{
		{name: "HTTPS", rawURL: "https://cdn.example.com/file.png"},
		{name: "HTTP explicitly allowed", rawURL: "http://cdn.example.com/file.png", allowHTTP: true},
		{name: "HTTP blocked", rawURL: "http://cdn.example.com/file.png", wantErr: "must use HTTPS"},
		{name: "lookalike scheme", rawURL: "httpsx://cdn.example.com/file.png", wantErr: "HTTP or HTTPS"},
		{name: "relative URL", rawURL: "/file.png", wantErr: "invalid"},
		{name: "credentials", rawURL: "https://user:secret@cdn.example.com/file.png", wantErr: "invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWorkerTargetURL(test.rawURL, test.allowHTTP)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestBoundDownloadResponseRejectsDeclaredOversizeAndClosesBody(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	body := &trackedReadCloser{Reader: bytes.NewReader([]byte("data"))}
	response := &http.Response{
		Body:          body,
		ContentLength: 1024*1024 + 1,
	}

	bounded, err := boundDownloadResponse(response)
	require.Error(t, err)
	assert.Nil(t, bounded)
	assert.True(t, body.closed)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestBoundDownloadResponseLimitsChunkedBody(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	payload := bytes.Repeat([]byte("x"), 1024*1024+32)
	body := &trackedReadCloser{Reader: bytes.NewReader(payload)}
	response := &http.Response{Body: body, ContentLength: -1}

	bounded, err := boundDownloadResponse(response)
	require.NoError(t, err)
	read, err := io.ReadAll(bounded.Body)
	require.Error(t, err)
	assert.Len(t, read, 1024*1024)
	assert.Contains(t, err.Error(), "exceeds maximum allowed size")
	require.NoError(t, bounded.Body.Close())
	assert.True(t, body.closed)
}

func TestBoundDownloadResponseAllowsExactUnknownLength(t *testing.T) {
	originalLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = originalLimit })

	payload := bytes.Repeat([]byte("x"), 1024*1024)
	body := &trackedReadCloser{Reader: bytes.NewReader(payload)}
	response := &http.Response{Body: body, ContentLength: -1}

	bounded, err := boundDownloadResponse(response)
	require.NoError(t, err)
	read, err := io.ReadAll(bounded.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, read)
}

func TestMaximumDownloadBytesRejectsOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if int64(maxInt) <= maxInt64Value/bytesPerMiB {
		t.Skip("int width cannot represent an overflowing MiB value")
	}

	maxBytes, err := maximumDownloadBytes(maxInt)
	require.Error(t, err)
	assert.Zero(t, maxBytes)
	assert.Contains(t, err.Error(), "too large")
}

func TestDoWorkerRequestRejectsConfiguredEndpointCredentials(t *testing.T) {
	originalWorkerURL := system_setting.WorkerUrl
	originalAllowHTTP := system_setting.WorkerAllowHttpImageRequestEnabled
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting
	t.Cleanup(func() {
		system_setting.WorkerUrl = originalWorkerURL
		system_setting.WorkerAllowHttpImageRequestEnabled = originalAllowHTTP
		*fetchSetting = originalFetchSetting
	})

	system_setting.WorkerUrl = "http://user:secret@127.0.0.1:8080"
	system_setting.WorkerAllowHttpImageRequestEnabled = false
	fetchSetting.EnableSSRFProtection = false

	response, err := DoWorkerRequest(&WorkerRequest{URL: "https://cdn.example.com/file.png"})
	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "must not contain credentials")
}

func TestDoWorkerRequestPreservesPrivateHTTPWorkerCompatibility(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/gateway/", request.URL.Path)
		var payload WorkerRequest
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		assert.Equal(t, "https://cdn.example.com/file.png", payload.URL)
		assert.Equal(t, "worker-key", payload.Key)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer worker.Close()

	originalWorkerURL := system_setting.WorkerUrl
	originalWorkerKey := system_setting.WorkerValidKey
	originalAllowHTTP := system_setting.WorkerAllowHttpImageRequestEnabled
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting
	t.Cleanup(func() {
		system_setting.WorkerUrl = originalWorkerURL
		system_setting.WorkerValidKey = originalWorkerKey
		system_setting.WorkerAllowHttpImageRequestEnabled = originalAllowHTTP
		*fetchSetting = originalFetchSetting
	})

	system_setting.WorkerUrl = worker.URL + "/gateway"
	system_setting.WorkerValidKey = "worker-key"
	system_setting.WorkerAllowHttpImageRequestEnabled = false
	fetchSetting.EnableSSRFProtection = false

	response, err := DoWorkerRequest(&WorkerRequest{
		URL: "https://cdn.example.com/file.png",
		Key: system_setting.WorkerValidKey,
	})
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
}
