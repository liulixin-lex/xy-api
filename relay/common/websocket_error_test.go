package common

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type websocketHandshakeBody struct {
	io.Reader
	closed bool
}

func (body *websocketHandshakeBody) Close() error {
	body.closed = true
	return nil
}

func TestNewWebSocketHandshakeErrorPreservesSourceStatusAndClosesBody(t *testing.T) {
	body := &websocketHandshakeBody{Reader: strings.NewReader(strings.Repeat("x", websocketHandshakeDrainLimit+128))}
	apiErr := NewWebSocketHandshakeError(&http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       body,
	}, errors.New("websocket: bad handshake"))

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.SourceStatusCode())
	assert.Equal(t, types.ErrorCodeBadResponseStatusCode, apiErr.GetErrorCode())
	assert.True(t, body.closed)
	assert.NotContains(t, apiErr.Error(), strings.Repeat("x", 32))
}
