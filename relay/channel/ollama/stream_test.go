package ollama

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

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingOllamaStreamWriter struct {
	gin.ResponseWriter
	err    error
	writes int
}

func (w *failingOllamaStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func (w *failingOllamaStreamWriter) WriteString(_ string) (int, error) {
	w.writes++
	return 0, w.err
}

func newOllamaStreamTestContext(t *testing.T, reader io.Reader) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		StartTime:   time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama-test"},
	}
	info.SetEstimatePromptTokens(4)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reader)}
	return ctx, recorder, resp, info
}

func TestOllamaStreamHandlerReturnsPartialUsageAndFirstUpstreamError(t *testing.T) {
	validChunk := `{"model":"llama-test","created_at":"2026-07-11T00:00:00Z","message":{"role":"assistant","content":"partial"},"done":false}` + "\n"
	scannerErr := errors.New("ollama upstream read failed")
	tests := []struct {
		name      string
		reader    io.Reader
		wantCause error
	}{
		{
			name:   "malformed chunk after partial response",
			reader: strings.NewReader(validChunk + "{malformed\n"),
		},
		{
			name:      "scanner error after partial response",
			reader:    io.MultiReader(strings.NewReader(validChunk), iotest.ErrReader(scannerErr)),
			wantCause: scannerErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, recorder, resp, info := newOllamaStreamTestContext(t, tt.reader)

			usage, apiErr := ollamaStreamHandler(ctx, info, resp)

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
			assert.NotContains(t, recorder.Body.String(), `"usage":{"prompt_tokens"`)
		})
	}
}

func TestOllamaStreamHandlerStopsOnFirstDownstreamWriteError(t *testing.T) {
	body := strings.Repeat(`{"model":"llama-test","message":{"role":"assistant","content":"partial"},"done":false}`+"\n", 3)
	ctx, _, resp, info := newOllamaStreamTestContext(t, strings.NewReader(body))
	writeErr := errors.New("ollama downstream write failed")
	writer := &failingOllamaStreamWriter{ResponseWriter: ctx.Writer, err: writeErr}
	ctx.Writer = writer

	usage, apiErr := ollamaStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	assert.ErrorIs(t, apiErr, writeErr)
	assert.Equal(t, 1, writer.writes)
}

func TestOllamaStreamHandlerStopsOnProviderErrorFrame(t *testing.T) {
	validChunk := `{"model":"llama-test","created_at":"2026-07-11T00:00:00Z","message":{"role":"assistant","content":"partial"},"done":false}` + "\n"
	errorChunk := `{"error":"ollama model runner failed"}` + "\n"
	lateChunk := `{"model":"llama-test","message":{"role":"assistant","content":"late response"},"done":false}` + "\n"
	doneChunk := `{"model":"llama-test","done":true,"done_reason":"stop","prompt_eval_count":4,"eval_count":8}` + "\n"
	ctx, recorder, resp, info := newOllamaStreamTestContext(t, strings.NewReader(validChunk+errorChunk+lateChunk+doneChunk))

	usage, apiErr := ollamaStreamHandler(ctx, info, resp)

	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	require.NotNil(t, apiErr)
	require.Error(t, apiErr.Cause())
	assert.Contains(t, apiErr.Cause().Error(), "ollama model runner failed")
	assert.Contains(t, recorder.Body.String(), "partial")
	assert.NotContains(t, recorder.Body.String(), "late response")
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
	assert.NotContains(t, recorder.Body.String(), `"usage":{"prompt_tokens"`)
}

func TestOllamaChatHandlerNonStreamToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "compact json per-line parse path",
			raw:  `{"model":"llama3.1","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"Paris","days":0}}}]},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":7}`,
		},
		{
			name: "pretty json fallback parse path",
			raw: `{
  "model": "llama3.1",
  "created_at": "2026-05-27T12:00:00Z",
  "message": {
    "role": "assistant",
    "content": "",
    "tool_calls": [
      {
        "function": {
          "name": "get_weather",
          "arguments": {
            "city": "Paris",
            "days": 0
          }
        }
      }
    ]
  },
  "done": true,
  "done_reason": "stop",
  "prompt_eval_count": 5,
  "eval_count": 7
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.raw)),
			}

			usage, apiErr := ollamaChatHandler(c, &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "fallback-model"},
			}, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 12, usage.TotalTokens)

			var out dto.OpenAITextResponse
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
			require.Len(t, out.Choices, 1)
			assert.Equal(t, constant.FinishReasonToolCalls, out.Choices[0].FinishReason)

			var toolCalls []dto.ToolCallResponse
			require.NoError(t, common.Unmarshal(out.Choices[0].Message.ToolCalls, &toolCalls))
			require.Len(t, toolCalls, 1)
			assert.NotEmpty(t, toolCalls[0].ID)
			assert.Equal(t, "function", toolCalls[0].Type)
			assert.Equal(t, "get_weather", toolCalls[0].Function.Name)
			assert.Nil(t, toolCalls[0].Index)

			var args map[string]any
			require.NoError(t, common.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args))
			assert.Equal(t, "Paris", args["city"])
			assert.Equal(t, float64(0), args["days"])
		})
	}
}
