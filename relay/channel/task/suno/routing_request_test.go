package suno

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSunoContinuationRequiresAndRecordsOriginTask(t *testing.T) {
	newContext := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/MUSIC", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "action", Value: constant.SunoActionMusic}}
		return c
	}

	t.Run("missing task id", func(t *testing.T) {
		info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
		taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(
			newContext(`{"continue_clip_id":"clip-1"}`), info,
		)

		require.NotNil(t, taskErr)
		assert.True(t, taskErr.LocalError)
		assert.Contains(t, taskErr.Message, "task id")
		assert.Empty(t, info.OriginTaskID)
	})

	t.Run("origin task recorded", func(t *testing.T) {
		info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
		taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(
			newContext(`{"task_id":"task-1","continue_clip_id":"clip-1"}`), info,
		)

		require.Nil(t, taskErr)
		assert.Equal(t, "task-1", info.OriginTaskID)
	})
}

func TestSunoContinuationSendsOnlyPersistedUpstreamTaskID(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/MUSIC", nil)
	ctx.Set("task_request", &dto.SunoSubmitReq{
		TaskID: "task-public", ContinueClipId: "clip-1",
	})
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{
		OriginTaskID: "task-public", OriginUpstreamTaskID: "upstream-private",
	}}

	body, err := (&TaskAdaptor{}).BuildRequestBody(ctx, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	var request dto.SunoSubmitReq
	require.NoError(t, common.Unmarshal(encoded, &request))
	assert.Equal(t, "upstream-private", request.TaskID)
	assert.NotContains(t, string(encoded), "task-public")

	info.OriginUpstreamTaskID = ""
	_, err = (&TaskAdaptor{}).BuildRequestBody(ctx, info)
	require.Error(t, err)
}
