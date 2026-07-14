package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskSubmissionResponseWriterDefersClientCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	buffered := newTaskSubmissionResponseWriter(ctx.Writer, 1024)

	buffered.Header().Set("Content-Type", "application/json")
	buffered.WriteHeader(http.StatusAccepted)
	_, err := buffered.Write([]byte(`{"id":"task_public"}`))
	require.NoError(t, err)
	assert.False(t, ctx.Writer.Written())
	assert.Equal(t, 0, recorder.Body.Len())

	require.NoError(t, buffered.CommitTo(ctx.Writer))
	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"id":"task_public"}`, recorder.Body.String())
}

func TestTaskSubmissionResponseWriterEnforcesHardLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	buffered := newTaskSubmissionResponseWriter(ctx.Writer, 4)

	_, err := buffered.Write([]byte("12345"))
	require.ErrorIs(t, err, errTaskSubmissionResponseTooLarge)
	assert.False(t, ctx.Writer.Written())
	require.ErrorIs(t, buffered.CommitTo(ctx.Writer), errTaskSubmissionResponseTooLarge)
}

func TestTaskSubmissionResponseWriterKeepsReplayBelowDatabasePacketLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	buffered := newTaskSubmissionResponseWriter(ctx.Writer, maxTaskSubmissionResponseBytes)
	boundary := strings.Repeat("x", 3<<20)

	written, err := buffered.WriteString(boundary)
	require.NoError(t, err)
	assert.Equal(t, len(boundary), written)
	_, err = buffered.WriteString("x")
	assert.ErrorIs(t, err, errTaskSubmissionResponseTooLarge)
	assert.False(t, ctx.Writer.Written())
}

func TestTaskSubmissionReplaySnapshotReturnsUsableFallbackWhenHeadersCannotBeFrozen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	buffered := newTaskSubmissionResponseWriter(ctx.Writer, 1024)
	buffered.Header().Set("Content-Type", "application/json")
	buffered.Header().Set("X-Oversized", strings.Repeat("x", 17<<10))
	buffered.WriteHeader(http.StatusAccepted)
	_, err := buffered.Write([]byte(`{"id":"task_public"}`))
	require.NoError(t, err)

	snapshot, err := TaskSubmissionReplaySnapshot(
		&TaskSubmitResult{Platform: constant.TaskPlatformSuno, response: buffered}, "task_public", "suno-test",
	)
	require.Error(t, err)
	assert.Equal(t, http.StatusOK, snapshot.StatusCode)
	assert.Equal(t, "application/json", snapshot.ContentType)
	assert.Empty(t, snapshot.HeadersJSON)
	assert.JSONEq(t, `{"code":"success","message":"","data":"task_public"}`, string(snapshot.Body))
}

func TestTaskSubmissionReplaySnapshotRejectsOverflowAndUnwrittenResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	overflow := newTaskSubmissionResponseWriter(ctx.Writer, 4)
	_, writeErr := overflow.Write([]byte("12345"))
	require.ErrorIs(t, writeErr, errTaskSubmissionResponseTooLarge)
	snapshot, err := TaskSubmissionReplaySnapshot(
		&TaskSubmitResult{response: overflow}, "task_overflow", "video-model",
	)
	require.ErrorIs(t, err, errTaskSubmissionResponseTooLarge)
	assert.Equal(t, http.StatusOK, snapshot.StatusCode)
	assert.Contains(t, string(snapshot.Body), "task_overflow")
	assert.NotEqual(t, overflow.body.Bytes(), snapshot.Body)

	unwritten := newTaskSubmissionResponseWriter(ctx.Writer, 1024)
	snapshot, err = TaskSubmissionReplaySnapshot(
		&TaskSubmitResult{response: unwritten}, "task_unwritten", "video-model",
	)
	require.Error(t, err)
	assert.Equal(t, http.StatusOK, snapshot.StatusCode)
	assert.Contains(t, string(snapshot.Body), "task_unwritten")
}

func TestTaskSubmissionReplaySnapshotStripsWriterOwnedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	buffered := newTaskSubmissionResponseWriter(ctx.Writer, 1024)
	buffered.Header().Set("Connection", "X-Hop")
	buffered.Header().Set("Content-Encoding", "gzip")
	buffered.Header().Set("Content-Length", "999")
	buffered.Header().Set("Content-Type", "application/json; charset=utf-8")
	buffered.Header().Set("X-Hop", "drop-me")
	buffered.Header().Set("X-Trace", "trace-1")
	buffered.WriteHeader(http.StatusAccepted)
	_, err := buffered.Write([]byte(`{"code":"success","data":"task_public"}`))
	require.NoError(t, err)

	snapshot, err := TaskSubmissionReplaySnapshot(
		&TaskSubmitResult{Platform: constant.TaskPlatformSuno, response: buffered}, "task_public", "suno-test",
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, snapshot.StatusCode)
	assert.JSONEq(t, `{"X-Trace":["trace-1"]}`, snapshot.HeadersJSON)
	assert.NotContains(t, snapshot.HeadersJSON, "Content-")
	assert.NotContains(t, snapshot.HeadersJSON, "X-Hop")
}

func TestTaskSubmissionReplaySnapshotFallsBackWhenPublicIdentityIsMissingOrWrong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		platform constant.TaskPlatform
		body     string
	}{
		{name: "empty", platform: constant.TaskPlatformSuno, body: `{}`},
		{name: "suno wrong id", platform: constant.TaskPlatformSuno, body: `{"code":"success","data":"task_other"}`},
		{name: "video missing task id", platform: constant.TaskPlatform("sora"), body: `{"id":"task_public"}`},
		{name: "video wrong id", platform: constant.TaskPlatform("sora"), body: `{"id":"task_other","task_id":"task_public"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			buffered := newTaskSubmissionResponseWriter(ctx.Writer, 1024)
			buffered.Header().Set("Content-Type", "application/json")
			buffered.WriteHeader(http.StatusOK)
			_, writeErr := buffered.Write([]byte(test.body))
			require.NoError(t, writeErr)

			snapshot, err := TaskSubmissionReplaySnapshot(
				&TaskSubmitResult{Platform: test.platform, response: buffered}, "task_public", "video-model",
			)
			require.Error(t, err)
			assert.Equal(t, http.StatusOK, snapshot.StatusCode)
			assert.Contains(t, string(snapshot.Body), "task_public")
			assert.NotContains(t, string(snapshot.Body), "task_other")
		})
	}
}
