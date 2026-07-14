package gemini

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type geminiTaskRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip geminiTaskRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestFetchTaskUsesStatefulClientAndPreservesCredentialHeader(t *testing.T) {
	previousFactory := geminiTaskHTTPClientFactory
	defer func() { geminiTaskHTTPClientFactory = previousFactory }()

	var capturedProxy string
	var capturedRequest *http.Request
	geminiTaskHTTPClientFactory = func(proxyURL string) (*http.Client, error) {
		capturedProxy = proxyURL
		return &http.Client{Transport: geminiTaskRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			capturedRequest = request.Clone(request.Context())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"done":false}`)),
				Request:    request,
			}, nil
		})}, nil
	}

	upstreamID := taskcommon.EncodeLocalTaskID("operations/video-operation")
	response, err := (&TaskAdaptor{}).FetchTask(
		context.Background(),
		"https://generativelanguage.googleapis.com",
		"stable-api-key",
		map[string]any{"task_id": upstreamID},
		"socks5://proxy.internal:1080",
	)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NoError(t, response.Body.Close())
	require.NotNil(t, capturedRequest)

	assert.Equal(t, "socks5://proxy.internal:1080", capturedProxy)
	assert.Equal(t, "generativelanguage.googleapis.com", capturedRequest.URL.Hostname())
	assert.Equal(t, "stable-api-key", capturedRequest.Header.Get("x-goog-api-key"))
	assert.Equal(t, "application/json", capturedRequest.Header.Get("Accept"))
}
