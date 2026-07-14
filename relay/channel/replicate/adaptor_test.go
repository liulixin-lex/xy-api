package replicate

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadFileFromFormStopsOnRequestCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	uploadStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(uploadStarted)
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-releaseUpstream:
		}
	}))
	defer server.Close()
	defer close(releaseUpstream)

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("image-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", &requestBody).WithContext(requestCtx)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:         "test-key",
			ChannelBaseUrl: server.URL,
		},
	}
	sendState := relaycommon.NewRoutingUpstreamSendState(nil)
	relaycommon.BindRoutingUpstreamSendState(c, sendState)
	result := make(chan error, 1)
	go func() {
		_, uploadErr := uploadFileFromForm(c, info, "image")
		result <- uploadErr
	}()

	select {
	case <-uploadStarted:
		assert.True(t, sendState.Sent())
	case <-time.After(time.Second):
		require.Fail(t, "replicate upload did not reach the upstream server")
	}
	cancel()

	select {
	case uploadErr := <-result:
		require.Error(t, uploadErr)
		assert.ErrorIs(t, uploadErr, context.Canceled)
	case <-time.After(time.Second):
		require.Fail(t, "replicate upload stayed blocked after request cancellation")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		require.Fail(t, "replicate upstream request did not observe cancellation")
	}
}

func TestUploadFileFromFormStopsBeforeNetworkWhenRoutingSendBoundaryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	want := errors.New("routing send rejected")

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("image-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &requestBody)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ApiKey: "test-key", ChannelBaseUrl: "http://127.0.0.1:1",
	}}
	sendState := relaycommon.NewRoutingUpstreamSendState(func() error { return want })
	relaycommon.BindRoutingUpstreamSendState(c, sendState)

	_, uploadErr := uploadFileFromForm(c, info, "image")

	require.ErrorIs(t, uploadErr, want)
	assert.False(t, sendState.Sent())
}
