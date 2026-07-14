package vertex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	vertexcore "github.com/QuantumNous/new-api/relay/channel/vertex"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type vertexTaskRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip vertexTaskRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestFetchTaskUsesStatefulClientWithPinnedCredentialToken(t *testing.T) {
	previousClientFactory := vertexTaskHTTPClientFactory
	previousTokenFactory := vertexTaskAccessToken
	defer func() {
		vertexTaskHTTPClientFactory = previousClientFactory
		vertexTaskAccessToken = previousTokenFactory
	}()

	var capturedProxy string
	var capturedRequest *http.Request
	vertexTaskAccessToken = func(ctx context.Context, credentials vertexcore.Credentials, proxyURL string) (string, error) {
		assert.Equal(t, "project-a", credentials.ProjectID)
		assert.Equal(t, "http://proxy.internal:3128", proxyURL)
		return "stable-access-token", nil
	}
	vertexTaskHTTPClientFactory = func(proxyURL string) (*http.Client, error) {
		capturedProxy = proxyURL
		return &http.Client{Transport: vertexTaskRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			capturedRequest = request.Clone(request.Context())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"done":false}`)),
				Request:    request,
			}, nil
		})}, nil
	}

	operationName := "projects/project-a/locations/us-central1/publishers/google/models/veo-3.0-generate-001/operations/op-a"
	response, err := (&TaskAdaptor{}).FetchTask(
		context.Background(),
		"https://us-central1-aiplatform.googleapis.com",
		`{"project_id":"project-a","client_email":"svc@example.com","private_key":"unused"}`,
		map[string]any{"task_id": taskcommon.EncodeLocalTaskID(operationName)},
		"http://proxy.internal:3128",
	)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NoError(t, response.Body.Close())
	require.NotNil(t, capturedRequest)

	assert.Equal(t, "http://proxy.internal:3128", capturedProxy)
	assert.Equal(t, "Bearer stable-access-token", capturedRequest.Header.Get("Authorization"))
	assert.Equal(t, "project-a", capturedRequest.Header.Get("x-goog-user-project"))
	assert.Contains(t, capturedRequest.URL.Path, ":fetchPredictOperation")
}
