package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetResponseBodyWithContextStopsOnCancel(t *testing.T) {
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
		_, err := GetResponseBodyWithContext(ctx, http.MethodGet, server.URL, &model.Channel{}, http.Header{})
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		require.Fail(t, "model discovery request did not reach upstream")
	}
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "model discovery request stayed blocked after cancellation")
	}
}
