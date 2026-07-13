package ali

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAsyncTaskWaitStopsDuringInitialDelayOnRequestCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestCtx, cancel := context.WithCancel(context.Background())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(requestCtx)
	cancel()

	type result struct {
		response *AliResponse
		body     []byte
		err      error
	}
	resultChan := make(chan result, 1)
	go func() {
		response, body, err := asyncTaskWait(c, &relaycommon.RelayInfo{}, "task-1")
		resultChan <- result{response: response, body: body, err: err}
	}()

	select {
	case got := <-resultChan:
		require.Error(t, got.err)
		assert.ErrorIs(t, got.err, context.Canceled)
		assert.Nil(t, got.response)
		assert.Nil(t, got.body)
	case <-time.After(time.Second):
		require.Fail(t, "Ali async task wait stayed blocked after request cancellation")
	}
}
