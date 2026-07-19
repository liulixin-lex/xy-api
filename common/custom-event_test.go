package common

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errCustomEventWrite = errors.New("custom event write failed")

type failingCustomEventWriter struct {
	header http.Header
}

func (w *failingCustomEventWriter) Header() http.Header {
	return w.header
}

func (w *failingCustomEventWriter) Write([]byte) (int, error) {
	return 0, errCustomEventWrite
}

func (w *failingCustomEventWriter) WriteHeader(int) {}

func TestCustomEventRenderPreservesHeadersAndPayload(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Cache-Control", "private")

	err := (CustomEvent{Data: "data: hello"}).Render(recorder)

	require.NoError(t, err)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "private", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "data: hello\n\n", recorder.Body.String())
}

func TestCustomEventRenderAcceptsNonStringData(t *testing.T) {
	recorder := httptest.NewRecorder()

	err := (CustomEvent{Data: 42}).Render(recorder)

	require.NoError(t, err)
	assert.Equal(t, "42", recorder.Body.String())
	assert.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
}

func TestCustomEventRenderPropagatesWriteFailure(t *testing.T) {
	writer := &failingCustomEventWriter{header: make(http.Header)}

	err := (CustomEvent{Data: "data: hello"}).Render(writer)

	require.ErrorIs(t, err, errCustomEventWrite)
}
