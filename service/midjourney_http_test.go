package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMidjourneyHTTPTestContext(method string, url string, body string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, url, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("Accept", "application/json")
	return ctx
}

func TestDoMidjourneyHttpRequestDistinguishesSafeLocalFailureFromUnknownUpstreamState(t *testing.T) {
	originalClient := httpClient
	httpClient = http.DefaultClient
	t.Cleanup(func() { httpClient = originalClient })

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"result":"task-1"}`))
	}))
	defer server.Close()

	ctx := newMidjourneyHTTPTestContext(http.MethodPost, server.URL, `{`)
	_, _, err := DoMidjourneyHttpRequest(ctx, time.Second, server.URL)
	require.Error(t, err)
	assert.False(t, IsMidjourneyUpstreamStateUnknown(err))
	assert.EqualValues(t, 0, requests.Load())

	brokenResponseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer brokenResponseServer.Close()
	ctx = newMidjourneyHTTPTestContext(http.MethodPost, brokenResponseServer.URL, `{"prompt":"test"}`)
	_, _, err = DoMidjourneyHttpRequest(ctx, time.Second, brokenResponseServer.URL)
	require.Error(t, err)
	assert.True(t, IsMidjourneyUpstreamStateUnknown(err))
}

func TestDoMidjourneyHttpRequestPreservesUploadAcceptanceForBilling(t *testing.T) {
	originalClient := httpClient
	httpClient = http.DefaultClient
	t.Cleanup(func() { httpClient = originalClient })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"description":"ok","result":["https://cdn.example/one.png","https://cdn.example/two.png"]}`))
	}))
	defer server.Close()

	ctx := newMidjourneyHTTPTestContext(http.MethodPost, server.URL, `{"base64Array":["data:image/png;base64,AA=="]}`)
	response, _, err := DoMidjourneyHttpRequest(ctx, time.Second, server.URL)
	require.NoError(t, err)
	assert.Equal(t, 1, response.Response.Code)
	assert.Equal(t, "https://cdn.example/one.png", response.Response.Result)
}
