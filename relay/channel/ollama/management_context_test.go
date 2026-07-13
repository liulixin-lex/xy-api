package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaManagementRequestsStopOnContextCancel(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, string) error
	}{
		{
			name: "fetch models",
			run: func(ctx context.Context, baseURL string) error {
				_, err := FetchOllamaModels(ctx, baseURL, "test-key")
				return err
			},
		},
		{
			name: "pull model",
			run: func(ctx context.Context, baseURL string) error {
				return PullOllamaModel(ctx, baseURL, "test-key", "model")
			},
		},
		{
			name: "stream pull model",
			run: func(ctx context.Context, baseURL string) error {
				return PullOllamaModelStream(ctx, baseURL, "test-key", "model", nil)
			},
		},
		{
			name: "delete model",
			run: func(ctx context.Context, baseURL string) error {
				return DeleteOllamaModel(ctx, baseURL, "test-key", "model")
			},
		},
		{
			name: "fetch version",
			run: func(ctx context.Context, baseURL string) error {
				_, err := FetchOllamaVersion(ctx, baseURL, "test-key")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			releaseUpstream := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				close(requestStarted)
				<-releaseUpstream
			}))
			defer server.Close()
			defer close(releaseUpstream)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- test.run(ctx, server.URL)
			}()

			select {
			case <-requestStarted:
			case <-time.After(time.Second):
				require.Fail(t, "Ollama management request did not reach upstream")
			}
			cancel()

			select {
			case err := <-result:
				require.Error(t, err)
				assert.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				require.Fail(t, "Ollama management request stayed blocked after cancellation")
			}
		})
	}
}
