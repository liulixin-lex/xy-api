package palm

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingPalmStreamWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

func (w *failingPalmStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func (w *failingPalmStreamWriter) WriteString(_ string) (int, error) {
	w.writes++
	return 0, w.err
}

type palmFailOnMarkerWriter struct {
	gin.ResponseWriter
	marker []byte
	err    error
}

func (w *palmFailOnMarkerWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.marker) {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}

func newPalmStreamTestContext(t *testing.T, reader io.Reader) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		StartTime:   time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "palm-test"},
	}
	info.SetEstimatePromptTokens(4)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reader)}
	return ctx, recorder, resp, info
}

func TestPalmStreamReturnsUsageAndOriginalUpstreamError(t *testing.T) {
	readErr := errors.New("palm upstream read failed")
	tests := []struct {
		name      string
		reader    io.Reader
		wantCause error
	}{
		{
			name:   "malformed response",
			reader: strings.NewReader(`{"candidates":[{malformed}]}`),
		},
		{
			name: "read error",
			reader: io.MultiReader(
				strings.NewReader(`{"candidates":[{"author":"assistant","content":"partial"}]}`),
				iotest.ErrReader(readErr),
			),
			wantCause: readErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder, resp, info := newPalmStreamTestContext(t, tt.reader)

			usageAny, apiErr := (&Adaptor{}).DoResponse(ctx, resp, info)

			usage, ok := usageAny.(*dto.Usage)
			require.True(t, ok)
			assert.Equal(t, 4, usage.PromptTokens)
			require.NotNil(t, apiErr)
			if tt.wantCause != nil {
				assert.ErrorIs(t, apiErr, tt.wantCause)
			} else {
				var syntaxErr *stdjson.SyntaxError
				assert.ErrorAs(t, apiErr, &syntaxErr)
			}
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			assert.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
		})
	}
}

func TestPalmStreamStopsOnFirstDownstreamWriteError(t *testing.T) {
	body := `{"candidates":[{"author":"assistant","content":"partial"}]}`
	ctx, _, resp, info := newPalmStreamTestContext(t, strings.NewReader(body))
	writeErr := errors.New("palm downstream write failed")
	writer := &failingPalmStreamWriter{ResponseWriter: ctx.Writer, err: writeErr}
	ctx.Writer = writer

	usageAny, apiErr := (&Adaptor{}).DoResponse(ctx, resp, info)

	usage, ok := usageAny.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Equal(t, 1, writer.writes)
}

func TestPalmStreamReturnsUsageWhenDoneWriteFails(t *testing.T) {
	body := `{"candidates":[{"author":"assistant","content":"partial"}]}`
	ctx, recorder, resp, info := newPalmStreamTestContext(t, strings.NewReader(body))
	doneErr := errors.New("palm done write failed")
	ctx.Writer = &palmFailOnMarkerWriter{ResponseWriter: ctx.Writer, marker: []byte("[DONE]"), err: doneErr}

	usageAny, apiErr := (&Adaptor{}).DoResponse(ctx, resp, info)

	usage, ok := usageAny.(*dto.Usage)
	require.True(t, ok)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, doneErr)
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestPalmStreamRejectsProviderErrorAndEmptyCandidates(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantErrText string
	}{
		{
			name:        "provider error",
			body:        `{"error":{"code":400,"message":"palm request rejected","status":"INVALID_ARGUMENT"}}`,
			wantErrText: "palm request rejected",
		},
		{
			name:        "empty candidates",
			body:        `{"candidates":[],"filters":[{"reason":"SAFETY","message":"palm safety filter blocked response"}]}`,
			wantErrText: "palm safety filter blocked response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder, resp, info := newPalmStreamTestContext(t, strings.NewReader(tt.body))

			usageAny, apiErr := (&Adaptor{}).DoResponse(ctx, resp, info)

			usage, ok := usageAny.(*dto.Usage)
			require.True(t, ok)
			assert.Equal(t, 4, usage.PromptTokens)
			require.NotNil(t, apiErr)
			require.Error(t, apiErr.Cause())
			assert.Contains(t, apiErr.Cause().Error(), tt.wantErrText)
			assert.Empty(t, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			require.NotNil(t, info.StreamStatus)
			assert.True(t, info.StreamStatus.HasErrors())
		})
	}
}
