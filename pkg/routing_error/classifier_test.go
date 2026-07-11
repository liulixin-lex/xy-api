package routingerror

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyAPIErrorMatrix(t *testing.T) {
	tests := []struct {
		name string
		err  *types.NewAPIError
		ctx  Context
		want Classification
	}{
		{
			name: "caller bad request",
			err:  types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest),
			want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "caller_code"},
		},
		{
			name: "user quota",
			err:  types.NewErrorWithStatusCode(errors.New("quota"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden),
			want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "caller_quota"},
		},
		{
			name: "content safety before generic forbidden",
			err:  types.NewErrorWithStatusCode(errors.New("blocked"), types.ErrorCodePromptBlocked, http.StatusForbidden),
			want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "content_safety"},
		},
		{
			name: "serving credential",
			err:  types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized),
			want: Classification{Responsibility: ResponsibilityCredential, Scope: ScopeCredential, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "serving_credential_status"},
		},
		{
			name: "mapped capacity uses source status",
			err:  mappedStatusError(t, http.StatusTooManyRequests, http.StatusServiceUnavailable),
			want: Classification{Responsibility: ResponsibilityCapacity, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthIgnore, CapacityEffect: CapacityCooldown, Component: ComponentServing, Rule: "capacity_status"},
		},
		{
			name: "provider overload 529",
			err:  types.NewErrorWithStatusCode(errors.New("overloaded"), types.ErrorCodeBadResponseStatusCode, 529),
			want: Classification{Responsibility: ResponsibilityCapacity, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthIgnore, CapacityEffect: CapacityCooldown, Component: ComponentServing, Rule: "capacity_status"},
		},
		{
			name: "provider bad gateway",
			err:  types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
			want: Classification{Responsibility: ResponsibilityProvider, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "provider_5xx"},
		},
		{
			name: "first byte timeout",
			err:  types.NewErrorWithStatusCode(errors.New("timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout),
			want: Classification{Responsibility: ResponsibilityNetwork, Scope: ScopeEndpoint, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "first_byte_timeout"},
		},
		{
			name: "typed dns failure",
			err:  types.NewError(&net.DNSError{Name: "upstream.example.com"}, types.ErrorCodeDoRequestFailed),
			want: Classification{Responsibility: ResponsibilityNetwork, Scope: ScopeEndpoint, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "network_cause"},
		},
		{
			name: "channel model mapping",
			err:  types.NewErrorWithStatusCode(errors.New("mapping"), types.ErrorCodeChannelModelMappedError, http.StatusBadRequest),
			want: Classification{Responsibility: ResponsibilityConfig, Scope: ScopeModel, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "model_config"},
		},
		{
			name: "local gateway error",
			err:  types.NewError(errors.New("db failed"), types.ErrorCodeQueryDataError),
			want: Classification{Responsibility: ResponsibilityGateway, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "gateway_code"},
		},
		{
			name: "cost connector credential does not affect serving",
			err:  types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized),
			ctx:  Context{Component: ComponentCostConnector, Operation: OperationSync},
			want: Classification{Responsibility: ResponsibilityCredential, Scope: ScopeCredential, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentCostConnector, Rule: "cost_connector_credential"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyAPIError(tt.err, tt.ctx))
		})
	}
}

func TestClassifyTaskErrorRequiresIdempotencyForUpstreamRetry(t *testing.T) {
	taskErr := &dto.TaskError{
		Code:       string(types.ErrorCodeBadResponseStatusCode),
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limited"),
	}

	classification := ClassifyTaskError(taskErr, Context{Component: ComponentServing, Operation: OperationTaskSubmit})

	assert.Equal(t, RetryIdempotencyRequired, classification.Retryability)
	assert.Equal(t, CapacityCooldown, classification.CapacityEffect)
}

func TestClassifyTaskLocalErrorNeverAffectsRoutingHealth(t *testing.T) {
	taskErr := &dto.TaskError{
		Code:       string(types.ErrorCodeInsufficientUserQuota),
		StatusCode: http.StatusForbidden,
		LocalError: true,
		Error:      errors.New("quota"),
	}

	classification := ClassifyTaskError(taskErr, Context{Component: ComponentServing, Operation: OperationTaskSubmit})

	assert.Equal(t, ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, RetryNever, classification.Retryability)
	assert.Equal(t, HealthIgnore, classification.HealthEffect)
}

func TestClassifyProviderFailureWithRetryAfterHasReliabilityAndCapacityEffects(t *testing.T) {
	apiErr := types.NewErrorWithStatusCode(errors.New("temporarily unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	apiErr.Metadata = metadata

	classification := ClassifyAPIError(apiErr, Context{Component: ComponentServing, Operation: OperationRelay})

	assert.Equal(t, ResponsibilityProvider, classification.Responsibility)
	assert.Equal(t, HealthDegrade, classification.HealthEffect)
	assert.Equal(t, CapacityCooldown, classification.CapacityEffect)
}

func TestClassifyExplicitStreamSignalsWithoutAPIError(t *testing.T) {
	corrupted := ClassifyAPIError(nil, Context{Signal: SignalStreamCorruption})
	preCommitCorruption := ClassifyAPIError(nil, Context{Signal: SignalStreamCorruption, BeforeCommit: true})
	clientGone := ClassifyAPIError(nil, Context{Signal: SignalClientGone})

	assert.Equal(t, ResponsibilityProvider, corrupted.Responsibility)
	assert.Equal(t, HealthDegrade, corrupted.HealthEffect)
	assert.Equal(t, RetryNever, corrupted.Retryability)
	assert.Equal(t, RetryBeforeCommit, preCommitCorruption.Retryability)
	assert.Equal(t, ResponsibilityCaller, clientGone.Responsibility)
	assert.Equal(t, HealthIgnore, clientGone.HealthEffect)
}

func mappedStatusError(t *testing.T, sourceStatusCode, responseStatusCode int) *types.NewAPIError {
	t.Helper()
	apiErr := types.NewErrorWithStatusCode(errors.New("mapped status"), types.ErrorCodeBadResponseStatusCode, sourceStatusCode)
	apiErr.SetResponseStatusCode(responseStatusCode)
	require.Equal(t, sourceStatusCode, apiErr.SourceStatusCode())
	return apiErr
}
