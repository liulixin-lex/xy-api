package coze

import (
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingCozeStreamWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

func (w *failingCozeStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func (w *failingCozeStreamWriter) WriteString(_ string) (int, error) {
	w.writes++
	return 0, w.err
}

func newCozeStreamTestContext(t *testing.T, reader io.Reader) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		StartTime:   time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "coze-test"},
	}
	info.SetEstimatePromptTokens(4)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reader)}
	return ctx, recorder, resp, info
}

func TestCozeChatStreamHandlerReturnsPartialUsageAndFirstUpstreamError(t *testing.T) {
	validEvent := "event: conversation.message.delta\n" +
		"data: {\"content\":\"partial\"}\n\n"
	scannerErr := errors.New("coze upstream read failed")
	tests := []struct {
		name          string
		reader        io.Reader
		wantCause     error
		forbiddenText string
	}{
		{
			name:   "malformed event after partial response",
			reader: strings.NewReader(validEvent + "event: conversation.message.delta\ndata: {malformed\n\n"),
		},
		{
			name: "scanner error discards incomplete trailing event",
			reader: io.MultiReader(
				strings.NewReader(validEvent+"event: conversation.message.delta\ndata: {\"content\":\"late\"}\n"),
				iotest.ErrReader(scannerErr),
			),
			wantCause:     scannerErr,
			forbiddenText: "late",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder, resp, info := newCozeStreamTestContext(t, tt.reader)

			usage, apiErr := cozeChatStreamHandler(ctx, info, resp)

			require.NotNil(t, usage)
			assert.Equal(t, 4, usage.PromptTokens)
			assert.Positive(t, usage.CompletionTokens)
			require.NotNil(t, apiErr)
			if tt.wantCause != nil {
				assert.ErrorIs(t, apiErr, tt.wantCause)
			} else {
				var syntaxErr *stdjson.SyntaxError
				assert.ErrorAs(t, apiErr, &syntaxErr)
			}
			assert.Contains(t, recorder.Body.String(), "partial")
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			assert.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
			if tt.forbiddenText != "" {
				assert.NotContains(t, recorder.Body.String(), tt.forbiddenText)
			}
		})
	}
}

func TestCozeChatStreamHandlerStopsOnFirstDownstreamWriteError(t *testing.T) {
	body := strings.Repeat("event: conversation.message.delta\ndata: {\"content\":\"partial\"}\n\n", 3)
	ctx, _, resp, info := newCozeStreamTestContext(t, strings.NewReader(body))
	writeErr := errors.New("coze downstream write failed")
	writer := &failingCozeStreamWriter{ResponseWriter: ctx.Writer, err: writeErr}
	ctx.Writer = writer

	usage, apiErr := cozeChatStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Equal(t, 1, writer.writes)
}
