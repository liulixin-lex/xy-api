package hailuo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTaskResultCancelsFileLookup(t *testing.T) {
	service.InitHttpClient()
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
		_, err := (&TaskAdaptor{baseURL: server.URL, apiKey: "test-key"}).ParseTaskResult(
			ctx,
			[]byte(`{"task_id":"task-1","status":"Success","file_id":"file-1","base_resp":{"status_code":0}}`),
		)
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		require.Fail(t, "Hailuo file lookup did not reach upstream")
	}
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "Hailuo file lookup stayed blocked after cancellation")
	}
}
