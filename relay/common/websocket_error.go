package common

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/types"
)

const websocketHandshakeDrainLimit = 4 << 10

// NewWebSocketHandshakeError preserves the upstream HTTP status returned when
// a WebSocket upgrade is rejected. The response body is drained only within a
// small bound and is never included in the error because it may contain
// provider diagnostics that are not safe to expose.
func NewWebSocketHandshakeError(response *http.Response, dialErr error) *types.NewAPIError {
	if response == nil {
		return types.NewError(fmt.Errorf("websocket handshake failed: %w", dialErr), types.ErrorCodeDoRequestFailed)
	}
	statusCode := response.StatusCode
	if statusCode <= 0 {
		statusCode = http.StatusBadGateway
	}
	if response.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, websocketHandshakeDrainLimit))
		_ = response.Body.Close()
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("websocket handshake failed with upstream status %d: %w", statusCode, dialErr),
		types.ErrorCodeBadResponseStatusCode,
		statusCode,
	)
}
