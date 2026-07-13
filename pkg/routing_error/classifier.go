package routingerror

import (
	"errors"
	"net"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
)

type Responsibility string
type Scope string
type Retryability string
type HealthEffect string
type CapacityEffect string
type Component string
type Operation string
type Signal string

const (
	ResponsibilityCaller     Responsibility = "caller"
	ResponsibilityGateway    Responsibility = "gateway"
	ResponsibilityConfig     Responsibility = "config"
	ResponsibilityCredential Responsibility = "credential"
	ResponsibilityCapacity   Responsibility = "capacity"
	ResponsibilityProvider   Responsibility = "provider"
	ResponsibilityNetwork    Responsibility = "network"

	ScopeRequest    Scope = "request"
	ScopeModel      Scope = "model"
	ScopeCredential Scope = "credential"
	ScopeEndpoint   Scope = "endpoint"
	ScopePoolMember Scope = "pool_member"
	ScopeChannel    Scope = "channel"

	RetryNever               Retryability = "never"
	RetryBeforeCommit        Retryability = "before_commit"
	RetryIdempotencyRequired Retryability = "idempotency_required"

	HealthIgnore  HealthEffect = "ignore"
	HealthDegrade HealthEffect = "degrade"
	HealthOpen    HealthEffect = "open"

	CapacityNone     CapacityEffect = "none"
	CapacityReduce   CapacityEffect = "reduce"
	CapacityCooldown CapacityEffect = "cooldown"

	ComponentServing       Component = "serving"
	ComponentCostConnector Component = "cost_connector"

	OperationRelay      Operation = "relay"
	OperationTaskSubmit Operation = "task_submit"
	OperationTaskPoll   Operation = "task_poll"
	OperationProbe      Operation = "probe"
	OperationSync       Operation = "sync"

	SignalNone             Signal = ""
	SignalContentSafety    Signal = "content_safety"
	SignalFirstByteTimeout Signal = "first_byte_timeout"
	SignalStreamCorruption Signal = "stream_corruption"
	SignalClientGone       Signal = "client_gone"
)

type Context struct {
	Component Component
	Operation Operation
	Signal    Signal
	// BeforeCommit is set only when the caller has verified that no response
	// bytes were committed to the downstream client.
	BeforeCommit bool
}

type Classification struct {
	Responsibility Responsibility
	Scope          Scope
	Retryability   Retryability
	HealthEffect   HealthEffect
	CapacityEffect CapacityEffect
	Component      Component
	Rule           string
}

func ClassifyAPIError(apiErr *types.NewAPIError, ctx Context) Classification {
	ctx = normalizeContext(ctx)
	if ctx.Signal == SignalClientGone {
		return result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "client_gone")
	}
	if ctx.Signal == SignalContentSafety {
		return result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "content_safety")
	}
	if ctx.Signal == SignalStreamCorruption {
		retryability := RetryNever
		if ctx.BeforeCommit {
			retryability = RetryBeforeCommit
		}
		return result(ctx, ResponsibilityProvider, ScopePoolMember, retryability, HealthDegrade, CapacityNone, "stream_corruption")
	}
	if apiErr == nil {
		return result(ctx, "", ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "success")
	}

	finish := func(classification Classification) Classification {
		return withRetryAfterCapacity(apiErr, classification)
	}

	if ctx.Signal == SignalFirstByteTimeout || apiErr.GetErrorCode() == types.ErrorCodeFirstByteTimeout {
		return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "first_byte_timeout"))
	}
	if callerCode(apiErr.GetErrorCode()) {
		return finish(result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, callerRule(apiErr.GetErrorCode())))
	}
	if gatewayCode(apiErr.GetErrorCode()) {
		return finish(result(ctx, ResponsibilityGateway, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "gateway_code"))
	}
	if modelConfigCode(apiErr.GetErrorCode()) {
		return finish(result(ctx, ResponsibilityConfig, ScopeModel, RetryBeforeCommit, HealthOpen, CapacityNone, "model_config"))
	}
	if credentialCode(apiErr.GetErrorCode()) {
		return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "credential_code"))
	}

	var networkError net.Error
	if errors.As(apiErr.Cause(), &networkError) || apiErr.GetErrorCode() == types.ErrorCodeDoRequestFailed {
		return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "network_cause"))
	}

	statusCode := apiErr.SourceStatusCode()
	if ctx.Component == ComponentCostConnector && (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) {
		return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "cost_connector_credential"))
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "serving_credential_status"))
	}
	if statusCode == http.StatusPaymentRequired || statusCode == http.StatusTooManyRequests || statusCode == 529 {
		return finish(result(ctx, ResponsibilityCapacity, ScopePoolMember, RetryBeforeCommit, HealthIgnore, CapacityCooldown, "capacity_status"))
	}
	if statusCode == http.StatusGatewayTimeout {
		return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "gateway_timeout"))
	}
	if statusCode >= 500 && statusCode <= 599 {
		return finish(result(ctx, ResponsibilityProvider, ScopePoolMember, RetryBeforeCommit, HealthDegrade, CapacityNone, "provider_5xx"))
	}
	if statusCode >= 400 && statusCode <= 499 {
		return finish(result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "caller_status"))
	}
	if responseFailureCode(apiErr.GetErrorCode()) {
		return finish(result(ctx, ResponsibilityProvider, ScopePoolMember, RetryBeforeCommit, HealthDegrade, CapacityNone, "provider_response"))
	}
	return finish(result(ctx, ResponsibilityGateway, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "conservative_gateway_fallback"))
}

func ClassifyTaskError(taskErr *dto.TaskError, ctx Context) Classification {
	if ctx.Operation == "" {
		ctx.Operation = OperationTaskSubmit
	}
	ctx = normalizeContext(ctx)
	if taskErr == nil {
		return Classification{Component: ctx.Component, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone}
	}

	code := types.ErrorCode(taskErr.Code)
	if code == "" {
		code = types.ErrorCodeBadResponseStatusCode
	}
	cause := taskErr.Error
	if cause == nil {
		cause = errors.New(taskErr.Message)
	}
	apiErr := types.NewErrorWithStatusCode(cause, code, taskErr.StatusCode)
	if taskErr.RetryAfterMs > 0 {
		metadata, _ := common.Marshal(map[string]int64{"retry_after_ms": taskErr.RetryAfterMs})
		apiErr.Metadata = metadata
	}

	classification := ClassifyAPIError(apiErr, ctx)
	if taskErr.LocalError {
		classification.Retryability = RetryNever
		classification.HealthEffect = HealthIgnore
		classification.CapacityEffect = CapacityNone
		classification.Rule = "task_local_" + classification.Rule
		return classification
	}
	if ctx.Operation == OperationTaskSubmit && classification.Retryability == RetryBeforeCommit {
		classification.Retryability = RetryIdempotencyRequired
	}
	return classification
}

func normalizeContext(ctx Context) Context {
	if ctx.Component == "" {
		ctx.Component = ComponentServing
	}
	if ctx.Operation == "" {
		ctx.Operation = OperationRelay
	}
	return ctx
}

func result(
	ctx Context,
	responsibility Responsibility,
	scope Scope,
	retryability Retryability,
	healthEffect HealthEffect,
	capacityEffect CapacityEffect,
	rule string,
) Classification {
	return Classification{
		Responsibility: responsibility,
		Scope:          scope,
		Retryability:   retryability,
		HealthEffect:   healthEffect,
		CapacityEffect: capacityEffect,
		Component:      ctx.Component,
		Rule:           rule,
	}
}

func withRetryAfterCapacity(apiErr *types.NewAPIError, classification Classification) Classification {
	if hasRetryAfterMetadata(apiErr) {
		classification.CapacityEffect = CapacityCooldown
	}
	return classification
}

func hasRetryAfterMetadata(apiErr *types.NewAPIError) bool {
	if apiErr == nil || len(apiErr.Metadata) == 0 {
		return false
	}
	metadata := struct {
		RetryAfterMs int64 `json:"retry_after_ms"`
	}{}
	if err := common.Unmarshal(apiErr.Metadata, &metadata); err != nil {
		return false
	}
	return metadata.RetryAfterMs > 0
}

func callerCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeInvalidRequest,
		types.ErrorCodeReadRequestBodyFailed,
		types.ErrorCodeBadRequestBody,
		types.ErrorCodeAccessDenied,
		types.ErrorCodeSensitiveWordsDetected,
		types.ErrorCodePromptBlocked,
		types.ErrorCodeViolationFeeGrokCSAM,
		types.ErrorCodeInsufficientUserQuota:
		return true
	default:
		return false
	}
}

func callerRule(code types.ErrorCode) string {
	switch code {
	case types.ErrorCodeInsufficientUserQuota:
		return "caller_quota"
	case types.ErrorCodeSensitiveWordsDetected,
		types.ErrorCodePromptBlocked,
		types.ErrorCodeViolationFeeGrokCSAM:
		return "content_safety"
	default:
		return "caller_code"
	}
}

func gatewayCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeCountTokenFailed,
		types.ErrorCodeModelPriceError,
		types.ErrorCodeInvalidApiType,
		types.ErrorCodeJsonMarshalFailed,
		types.ErrorCodeGetChannelFailed,
		types.ErrorCodeGenRelayInfoFailed,
		types.ErrorCodeConvertRequestFailed,
		types.ErrorCodeQueryDataError,
		types.ErrorCodeUpdateDataError,
		types.ErrorCodePreConsumeTokenQuotaFailed:
		return true
	default:
		return false
	}
}

func modelConfigCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeChannelModelMappedError,
		types.ErrorCodeChannelParamOverrideInvalid,
		types.ErrorCodeChannelHeaderOverrideInvalid,
		types.ErrorCodeModelNotFound:
		return true
	default:
		return false
	}
}

func credentialCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeChannelInvalidKey,
		types.ErrorCodeChannelNoAvailableKey:
		return true
	default:
		return false
	}
}

func responseFailureCode(code types.ErrorCode) bool {
	switch code {
	case types.ErrorCodeReadResponseBodyFailed,
		types.ErrorCodeBadResponse,
		types.ErrorCodeBadResponseBody,
		types.ErrorCodeEmptyResponse,
		types.ErrorCodeAwsInvokeError,
		types.ErrorCodeChannelAwsClientError,
		types.ErrorCodeChannelResponseTimeExceeded:
		return true
	default:
		return false
	}
}
