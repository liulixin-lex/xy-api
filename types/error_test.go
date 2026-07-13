package types

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAPIErrorHideMessagePreservesTypedCause(t *testing.T) {
	dnsErr := &net.DNSError{Name: "upstream.example.com", IsTimeout: true}

	apiErr := NewError(
		dnsErr,
		ErrorCodeDoRequestFailed,
		ErrOptionWithHideErrMsg("upstream request failed"),
	)

	assert.Equal(t, "upstream request failed", apiErr.Error())
	var actualDNSError *net.DNSError
	require.True(t, errors.As(apiErr, &actualDNSError))
	assert.Same(t, dnsErr, actualDNSError)
}

func TestNewAPIErrorSetMessageDoesNotReplaceCause(t *testing.T) {
	cause := errors.New("upstream failure")
	apiErr := NewError(cause, ErrorCodeDoRequestFailed)

	apiErr.SetMessage("public message")

	assert.Equal(t, "public message", apiErr.Error())
	assert.Equal(t, "public message", apiErr.ToOpenAIError().Message)
	assert.ErrorIs(t, apiErr, cause)
	assert.Same(t, cause, apiErr.Cause())
}

func TestNewAPIErrorResponseStatusMappingPreservesSourceStatus(t *testing.T) {
	apiErr := NewErrorWithStatusCode(
		errors.New("rate limited"),
		ErrorCodeBadResponseStatusCode,
		http.StatusTooManyRequests,
	)

	apiErr.SetResponseStatusCode(http.StatusServiceUnavailable)

	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.SourceStatusCode())
}
