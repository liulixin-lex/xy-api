package service

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapAsViolationFeePreservesCauseAndSourceStatus(t *testing.T) {
	cause := &net.DNSError{Name: CSAMViolationMarker, Err: "blocked"}
	apiErr := types.NewErrorWithStatusCode(cause, types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	apiErr.SetResponseStatusCode(http.StatusBadRequest)
	apiErr.SetMessage("Failed check: SAFETY_CHECK_TYPE: blocked content")
	metadata, err := common.Marshal(map[string]string{"provider": "grok"})
	require.NoError(t, err)
	apiErr.Metadata = metadata

	normalized := NormalizeViolationFeeError(apiErr)

	var gotCause *net.DNSError
	require.True(t, errors.As(normalized, &gotCause))
	assert.Same(t, cause, gotCause)
	assert.Equal(t, http.StatusForbidden, normalized.SourceStatusCode())
	assert.Equal(t, http.StatusBadRequest, normalized.StatusCode)
	assert.Equal(t, types.ErrorCodeViolationFeeGrokCSAM, normalized.GetErrorCode())
	assert.Equal(t, apiErr.Error(), normalized.Error())
	assert.Equal(t, apiErr.Metadata, normalized.Metadata)
}

func TestNormalizeViolationFeeErrorPreservesStableCodeEvidenceWithoutMarker(t *testing.T) {
	stableCode := types.ErrorCode("violation_fee.provider.policy")
	cause := &net.DNSError{Name: "upstream.example", Err: "policy rejected"}
	apiErr := types.NewErrorWithStatusCode(cause, stableCode, http.StatusForbidden)
	apiErr.SetResponseStatusCode(http.StatusBadRequest)
	apiErr.SetMessage("stable violation response")
	metadata, err := common.Marshal(map[string]string{"policy": "provider"})
	require.NoError(t, err)
	apiErr.Metadata = metadata

	normalized := NormalizeViolationFeeError(apiErr)

	var gotCause *net.DNSError
	require.True(t, errors.As(normalized, &gotCause))
	assert.Same(t, cause, gotCause)
	assert.Equal(t, stableCode, normalized.GetErrorCode())
	assert.Equal(t, http.StatusForbidden, normalized.SourceStatusCode())
	assert.Equal(t, http.StatusBadRequest, normalized.StatusCode)
	assert.Equal(t, apiErr.Error(), normalized.Error())
	assert.Equal(t, apiErr.Metadata, normalized.Metadata)
	assert.True(t, types.IsSkipRetryError(normalized))
}
