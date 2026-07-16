package controller

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRoutingBreakerAttemptTest(t *testing.T, enabled bool) {
	t.Helper()
	if enabled {
		t.Setenv("SMART_ROUTING_ENABLED", "true")
	} else {
		t.Setenv("SMART_ROUTING_ENABLED", "false")
	}
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = enabled
	setting.Mode = smart_routing_setting.ModeObserve
	setting.Consecutive5xx = 1
	setting.MinVolume = 1
	setting.FailureRatePct = 100
	setting.BaseCooldownSec = 1
	setting.MaxCooldownSec = 60
	setting.BackoffBaseMs429 = 1000
	smart_routing_setting.UpdateSetting(setting)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	resetRoutingBreakerConfigIdentityForTest()
	resetChannelBalanceRefreshForTest()
	runChannelBalanceRefresh = func(refresh func()) { refresh() }
	loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
		return &model.Channel{Id: channelID, ChannelInfo: model.ChannelInfo{IsMultiKey: true}}, nil
	}

	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
		resetRoutingBreakerConfigIdentityForTest()
		resetChannelBalanceRefreshForTest()
	})
}

func TestCommitRoutingCapacityAttemptFailsClosedBeforeUpstream(t *testing.T) {
	tracker, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 4,
		IdleTTL:    time.Hour,
		Shards:     1,
	})
	require.NoError(t, err)
	key := channelrouting.CapacityKey{PoolID: 1, MemberID: 2, Model: "gpt-test"}
	reservation, err := tracker.TryReserve(key, channelrouting.Demand{Inflight: 1}, channelrouting.Limit{Inflight: 1})
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NoError(t, service.SetRoutingCapacityReservation(ctx, reservation))
	require.NoError(t, reservation.Cancel())

	apiErr := commitRoutingCapacityAttempt(ctx)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Zero(t, snapshot.PendingReservations)
	assert.Zero(t, snapshot.CommittedReservations)
	require.NoError(t, service.CommitRoutingCapacityReservation(ctx))
}

func TestRoutingSendBoundaryRejectsOverlappingCanaryAttemptWithoutClaimingUpstreamSend(t *testing.T) {
	tracker, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 4,
		IdleTTL:    time.Hour,
		Shards:     1,
	})
	require.NoError(t, err)
	key := channelrouting.CapacityKey{PoolID: 1, MemberID: 2, Model: "gpt-test"}
	reservation, err := tracker.TryReserve(key, channelrouting.Demand{Inflight: 1}, channelrouting.Limit{Inflight: 1})
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	gate := channelrouting.CanaryGate{
		PoolID: 1, ActivationID: 2, PolicyRevision: 3, TrafficBasisPoints: 100,
		InCanary: true, RolloutKey: channelrouting.RolloutKey(strings.Repeat("a", 64)),
	}
	require.NoError(t, service.PrepareChannelRoutingCanarySelection(ctx, service.ChannelRoutingCanarySelection{
		Gate: gate, WindowSeconds: 60, LatenessSeconds: 5,
	}))
	require.NoError(t, service.MarkChannelRoutingCanaryAttemptStarted(ctx))
	require.NoError(t, service.SetRoutingCapacityReservation(ctx, reservation))

	apiErr := commitRoutingCapacityAttempt(ctx)
	require.Nil(t, apiErr)
	sendState := bindRoutingUpstreamAttempt(ctx, nil, nil, nil)
	assert.Error(t, relaycommon.MarkRoutingUpstreamSent(ctx))
	assert.False(t, sendState.Sent())
	releaseRoutingCapacityReservation(ctx)
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Zero(t, snapshot.PendingReservations)
	assert.Zero(t, snapshot.CommittedReservations)
	require.NoError(t, service.CommitRoutingCapacityReservation(ctx))
	require.NoError(t, service.FinishChannelRoutingCanaryAttempt(ctx))
	require.NoError(t, service.FinishChannelRoutingCanaryOutcome(ctx, false, false, false, 0, time.Now()))
}

func resetRoutingBreakerConfigIdentityForTest() {
	smartRoutingBreakerConfigMu.Lock()
	smartRoutingBreakerConfigLast = routingBreakerConfigIdentity{}
	smartRoutingBreakerConfigMu.Unlock()
}

func TestShouldRetryStopsAfterResponseWasSent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	info := &relaycommon.RelayInfo{SendResponseCount: 1}

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryStopsAfterFirstResponseTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	start := time.Now().Add(-time.Second)
	info := &relaycommon.RelayInfo{
		StartTime:         start,
		FirstResponseTime: start.Add(100 * time.Millisecond),
	}

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryStillRetriesBeforeAnyResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
	}

	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryEnforcesAttemptSafetyWhenProfileScoringIsDisabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	unsafeProfile := channelrouting.RequestProfileInput{
		RequestKind:              channelrouting.RequestKindImage,
		RetrySafety:              channelrouting.RequestRetrySafetyUnsafe,
		RetryAllowed:             false,
		CrossChannelRetryAllowed: false,
		HedgeAllowed:             false,
	}
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, unsafeProfile)
	apiErr := types.NewErrorWithStatusCode(
		errors.New("upstream failed"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}

	assert.False(t, shouldRetry(ctx, &relaycommon.RelayInfo{}, apiErr, classification, 1))
	assert.False(t, shouldRetryTaskRelay(ctx, &dto.TaskError{
		Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusBadGateway,
		Error: errors.New("task upstream failed"),
	}, classification, 1))

	safeProfile := unsafeProfile
	safeProfile.RequestKind = channelrouting.RequestKindChatCompletions
	safeProfile.RetrySafety = channelrouting.RequestRetrySafetySafe
	safeProfile.RetryAllowed = true
	safeProfile.CrossChannelRetryAllowed = true
	common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, safeProfile)
	assert.True(t, shouldRetry(ctx, &relaycommon.RelayInfo{}, apiErr, classification, 1))
}

func TestRelayAttemptControlErrorFirstByteTimeoutUsesActualClientCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatOpenAI,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	apiErr := relayAttemptControlError(ctx, nil, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeFirstByteTimeout, apiErr.GetErrorCode())

	info.SendResponseCount = 1
	assert.NotNil(t, relayAttemptControlError(ctx, nil, info), "pre-incremented send count is not a client commit")
	_, err := ctx.Writer.Write([]byte("committed"))
	require.NoError(t, err)
	assert.Nil(t, relayAttemptControlError(ctx, nil, info))
}

func TestRelayAttemptControlErrorKeepsCommittedRealtimeErrorAsClassificationEvidenceOnly(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cause := errors.New("failed to count realtime tokens")
	attemptErr := types.NewError(cause, types.ErrorCodeCountTokenFailed)
	committedInfo := &relaycommon.RelayInfo{
		RelayFormat:           types.RelayFormatOpenAIRealtime,
		ReceivedResponseCount: 1,
		StreamStatus:          relaycommon.NewStreamStatus(),
	}
	committedInfo.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)

	controlErr := relayAttemptControlError(ctx, attemptErr, committedInfo)
	classification, success := classifyRoutingRelayAttempt(attemptErr, committedInfo)

	assert.Nil(t, controlErr)
	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityGateway, classification.Responsibility)
	assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
	assert.ErrorIs(t, attemptErr, cause)

	preCommitInfo := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIRealtime}
	assert.Same(t, attemptErr, relayAttemptControlError(ctx, attemptErr, preCommitInfo))
	_, err := ctx.Writer.Write([]byte("non-stream response"))
	require.NoError(t, err)
	assert.Same(t, attemptErr, relayAttemptControlError(ctx, attemptErr, &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}))
}

func TestShouldRetryAllowsHTTPStreamRetryUntilWriterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1))

	info.SendResponseCount = 1
	info.ReceivedResponseCount = 1
	info.StartTime = time.Now().Add(-time.Second)
	info.FirstResponseTime = time.Now()
	assert.True(t, shouldRetry(ctx, info, apiErr, classification, 1), "attempt counters are not a client commit")
}

func TestShouldRetryBlocksRealtimeFirstByteTimeoutAfterWebSocketUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	_, err := ctx.Writer.Write([]byte("websocket upgrade complete"))
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatOpenAIRealtime,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryBlocksFirstByteTimeoutAfterHTTPWriterWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	_, err := ctx.Writer.Write([]byte("response already started"))
	require.NoError(t, err)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	info := &relaycommon.RelayInfo{
		RelayFormat:  types.RelayFormatOpenAI,
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)

	classification := routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}
	assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
}

func TestShouldRetryUsesClassificationBeforeStatusOverlay(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{}
	caller := types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest)
	timeout := types.NewErrorWithStatusCode(errors.New("timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout)

	assert.False(t, shouldRetry(ctx, info, caller, routingerror.Classification{Retryability: routingerror.RetryNever}, 1))
	assert.True(t, shouldRetry(ctx, info, timeout, routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}, 1))
}

func TestCanaryFaultInjectionMatrixRetriesOnlyBeforeClientCommit(t *testing.T) {
	tests := []struct {
		name           string
		apiErr         *types.NewAPIError
		responsibility routingerror.Responsibility
		scope          routingerror.Scope
	}{
		{
			name: "dns", apiErr: types.NewError(
				&net.DNSError{Name: "upstream.example.com", IsTemporary: true}, types.ErrorCodeDoRequestFailed,
			),
			responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint,
		},
		{
			name: "tls", apiErr: types.NewError(
				x509.UnknownAuthorityError{Cert: &x509.Certificate{}}, types.ErrorCodeDoRequestFailed,
			),
			responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint,
		},
		{name: "401", apiErr: types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, 401), responsibility: routingerror.ResponsibilityCredential, scope: routingerror.ScopeCredential},
		{name: "403", apiErr: types.NewErrorWithStatusCode(errors.New("forbidden"), types.ErrorCodeBadResponseStatusCode, 403), responsibility: routingerror.ResponsibilityCredential, scope: routingerror.ScopeCredential},
		{name: "402", apiErr: types.NewErrorWithStatusCode(errors.New("payment required"), types.ErrorCodeBadResponseStatusCode, 402), responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopeChannel},
		{name: "429", apiErr: types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, 429), responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember},
		{name: "529", apiErr: types.NewErrorWithStatusCode(errors.New("overloaded"), types.ErrorCodeBadResponseStatusCode, 529), responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember},
		{name: "5xx", apiErr: types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, 500), responsibility: routingerror.ResponsibilityProvider, scope: routingerror.ScopePoolMember},
		{name: "first byte timeout", apiErr: types.NewErrorWithStatusCode(errors.New("first byte timeout"), types.ErrorCodeFirstByteTimeout, 504), responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{}
			classification, success := classifyRoutingRelayAttemptWithContext(ctx, test.apiErr, info)
			assert.False(t, success)
			assert.Equal(t, test.responsibility, classification.Responsibility)
			assert.Equal(t, test.scope, classification.Scope)
			assert.Equal(t, routingerror.RetryBeforeCommit, classification.Retryability)
			assert.True(t, shouldRetry(ctx, info, test.apiErr, classification, 1))
		})
	}

	t.Run("stream failure after client commit", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		_, err := ctx.Writer.Write([]byte("committed"))
		require.NoError(t, err)
		info := &relaycommon.RelayInfo{IsStream: true, StreamStatus: relaycommon.NewStreamStatus()}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("late stream failure"))

		apiErr := relayAttemptControlError(ctx, nil, info)
		assert.Nil(t, apiErr)
		classification, success := classifyRoutingRelayAttemptWithContext(ctx, apiErr, info)
		assert.False(t, success)
		assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
		assert.Equal(t, routingerror.RetryNever, classification.Retryability)
		assert.False(t, shouldRetry(ctx, info, apiErr, classification, 1))
	})
}

func TestTaskErrorToAPIErrorPreservesOriginalCodeCauseAndRetryAfter(t *testing.T) {
	dnsErr := &net.DNSError{Name: "upstream.example.com"}
	taskErr := &dto.TaskError{
		Code:         string(types.ErrorCodeDoRequestFailed),
		Message:      "dial failed",
		StatusCode:   http.StatusBadGateway,
		RetryAfterMs: 2500,
		Error:        dnsErr,
	}

	apiErr := taskErrorToAPIError(taskErr)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeDoRequestFailed, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusBadGateway, apiErr.SourceStatusCode())
	var extracted *net.DNSError
	require.ErrorAs(t, apiErr, &extracted)
	assert.Same(t, dnsErr, extracted)
	assert.Equal(t, 2500*time.Millisecond, retryAfterFromAPIError(apiErr, time.Minute))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	respondTaskError(ctx, taskErr)
	assert.Equal(t, "3", recorder.Header().Get("Retry-After"))
}

func TestTaskErrorToAPIErrorNormalizesEmptyTaskErrorFacts(t *testing.T) {
	taskErr := &dto.TaskError{}

	apiErr := taskErrorToAPIError(taskErr)

	require.NotNil(t, apiErr)
	require.Equal(t, string(types.ErrorCodeBadResponseStatusCode), taskErr.Code)
	require.Equal(t, http.StatusInternalServerError, taskErr.StatusCode)
	require.Equal(t, "task relay failed", taskErr.Message)
	require.Error(t, taskErr.Error)
	assert.Equal(t, "task relay failed", taskErr.Error.Error())
	assert.Equal(t, types.ErrorCodeBadResponseStatusCode, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusInternalServerError, apiErr.SourceStatusCode())
	assert.ErrorIs(t, apiErr, taskErr.Error)

	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationTaskSubmit,
	})
	assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
	assert.Equal(t, routingerror.RetryIdempotencyRequired, classification.Retryability)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	respondTaskError(ctx, taskErr)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"bad_response_status_code"`)
	assert.Contains(t, recorder.Body.String(), `"message":"task relay failed"`)
}

func TestTaskSubmitUpstreamFailuresRequireIdempotencyAndDoNotRetry(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	ctx.Request.Header.Set("Idempotency-Key", "incoming-key-is-not-stably-forwarded")
	tests := []*dto.TaskError{
		{Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusTooManyRequests, Error: errors.New("rate limited")},
		{Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusBadGateway, Error: errors.New("bad gateway")},
		{Code: string(types.ErrorCodeDoRequestFailed), StatusCode: http.StatusBadGateway, Error: &net.DNSError{Name: "upstream.example.com"}},
	}

	for _, taskErr := range tests {
		classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})
		assert.Equal(t, routingerror.RetryIdempotencyRequired, classification.Retryability)
		assert.False(t, shouldRetryTaskRelay(ctx, taskErr, classification, 2))
	}
}

func TestTaskLocalErrorDoesNotAffectReliabilityBreakerOrCapacity(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 72)
	taskErr := &dto.TaskError{Code: string(types.ErrorCodeModelPriceError), StatusCode: http.StatusBadRequest, LocalError: true, Error: errors.New("price missing")}
	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})

	recordRoutingAttemptEffects(ctx, info, 72, false, taskErrorToAPIError(taskErr), classification)

	snapshots := routingmetrics.Snapshots()
	require.Len(t, snapshots, 1)
	assert.Zero(t, snapshots[0].ReliabilityRequestCount)
	assert.Zero(t, snapshots[0].ReliabilityFailureCount)
	assert.Empty(t, routingbreaker.DirtySnapshots())
	_, ok := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 72, APIKeyIndex: -1, Model: "gpt-test", Group: "vip"})
	assert.False(t, ok)
}

func TestClassifyRoutingRelayAttemptDistinguishesStreamCorruptionAndClientGone(t *testing.T) {
	corrupted := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	corrupted.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("scanner failed"))
	classification, success := classifyRoutingRelayAttempt(nil, corrupted)
	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
	assert.Equal(t, routingerror.HealthDegrade, classification.HealthEffect)

	clientGone := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	clientGone.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, context.Canceled)
	classification, success = classifyRoutingRelayAttempt(nil, clientGone)
	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
}

func TestRelayAttemptControlErrorConvertsOnlyPreCommitStreamFailure(t *testing.T) {
	tests := []struct {
		name               string
		configure          func(*relaycommon.RelayInfo)
		writerCommitted    bool
		wantErr            bool
		wantResponsibility routingerror.Responsibility
	}{
		{
			name: "scanner corruption before commit",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("scanner failed"))
			},
			wantErr:            true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
		{
			name: "handler stop before commit",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, errors.New("handler failed"))
			},
			wantErr:            true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
		{
			name: "soft corruption before commit",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				info.StreamStatus.RecordError("malformed first chunk")
			},
			wantErr:            true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
		{
			name: "malformed chunk before first-byte timeout",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				info.StreamStatus.RecordError("malformed first chunk")
			},
			wantErr:            true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
		{
			name: "normal completion",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
			},
		},
		{
			name: "send count without writer commit",
			configure: func(info *relaycommon.RelayInfo) {
				info.SendResponseCount = 1
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("write failed after counter increment"))
			},
			wantErr:            true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
		{
			name: "corruption after writer commit without counters",
			configure: func(info *relaycommon.RelayInfo) {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("late scanner failure"))
			},
			writerCommitted:    true,
			wantResponsibility: routingerror.ResponsibilityProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			if tt.writerCommitted {
				_, err := ctx.Writer.Write([]byte("committed"))
				require.NoError(t, err)
			}
			info := &relaycommon.RelayInfo{
				RelayFormat:  types.RelayFormatOpenAI,
				StreamStatus: relaycommon.NewStreamStatus(),
			}
			tt.configure(info)

			apiErr := relayAttemptControlError(ctx, nil, info)
			if tt.wantErr {
				require.NotNil(t, apiErr)
				assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			} else {
				assert.Nil(t, apiErr)
			}

			classification, success := classifyRoutingRelayAttemptWithContext(ctx, apiErr, info)
			if tt.wantResponsibility == "" {
				assert.True(t, success)
				assert.Equal(t, "success", classification.Rule)
				return
			}
			assert.False(t, success)
			assert.Equal(t, tt.wantResponsibility, classification.Responsibility)
			wantRetryability := routingerror.RetryBeforeCommit
			if tt.writerCommitted {
				wantRetryability = routingerror.RetryNever
			}
			assert.Equal(t, wantRetryability, classification.Retryability)
			assert.Equal(t, !tt.writerCommitted, shouldRetry(ctx, info, apiErr, classification, 1))
		})
	}
}

func TestRelayAttemptControlErrorPreservesStreamEndCause(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cause := &net.DNSError{Name: "stream.example.com"}
	info := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, cause)

	apiErr := relayAttemptControlError(ctx, nil, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, "upstream stream failed before client commit", apiErr.Error())
	assert.ErrorIs(t, apiErr, cause)
}

func TestRealtimeStreamOutcomesDoNotPolluteClientGoneOrCountCorruptionAsSuccess(t *testing.T) {
	t.Run("client gone is excluded from reliability", func(t *testing.T) {
		configureRoutingBreakerAttemptTest(t, true)
		ctx, info := singleKeyRoutingAttemptFixture(t, 73)
		info.RelayFormat = types.RelayFormatOpenAIRealtime
		info.StreamStatus = relaycommon.NewStreamStatus()
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, context.Canceled)

		apiErr := relayAttemptControlError(ctx, nil, info)
		classification, success := classifyRoutingRelayAttempt(apiErr, info)
		recordRoutingAttemptEffects(ctx, info, 73, success, apiErr, classification)

		assert.Nil(t, apiErr)
		assert.False(t, success)
		assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
		assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
		metrics := routingmetrics.Snapshots()
		require.Len(t, metrics, 1)
		assert.Zero(t, metrics[0].SuccessCount)
		assert.Zero(t, metrics[0].ReliabilityRequestCount)
		assert.Zero(t, metrics[0].ReliabilityFailureCount)
		assert.Empty(t, routingbreaker.DirtySnapshots())
	})

	t.Run("upstream corruption is a reliability failure", func(t *testing.T) {
		configureRoutingBreakerAttemptTest(t, true)
		ctx, info := singleKeyRoutingAttemptFixture(t, 74)
		info.RelayFormat = types.RelayFormatOpenAIRealtime
		info.StreamStatus = relaycommon.NewStreamStatus()
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("upstream websocket parse failed"))

		apiErr := relayAttemptControlError(ctx, nil, info)
		classification, success := classifyRoutingRelayAttempt(apiErr, info)
		recordRoutingAttemptEffects(ctx, info, 74, success, apiErr, classification)

		require.NotNil(t, apiErr)
		assert.False(t, success)
		assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
		assert.Equal(t, routingerror.HealthDegrade, classification.HealthEffect)
		metrics := routingmetrics.Snapshots()
		require.Len(t, metrics, 1)
		assert.Zero(t, metrics[0].SuccessCount)
		assert.Equal(t, int64(1), metrics[0].ReliabilityRequestCount)
		assert.Equal(t, int64(1), metrics[0].ReliabilityFailureCount)
		breakers := routingbreaker.DirtySnapshots()
		require.Len(t, breakers, 1)
		assert.Equal(t, routingbreaker.StateOpen, breakers[0].State)
	})
}

func TestClassifyRoutingRelayAttemptMarksCSAMAsContentSafetyBeforeNormalization(t *testing.T) {
	apiErr := types.NewErrorWithStatusCode(errors.New(service.CSAMViolationMarker), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
	apiErr.SetResponseStatusCode(http.StatusBadRequest)

	classification, success := classifyRoutingRelayAttempt(apiErr, nil)
	normalized := service.NormalizeViolationFeeError(apiErr)

	assert.False(t, success)
	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.RetryNever, classification.Retryability)
	assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
	assert.Equal(t, routingerror.CapacityNone, classification.CapacityEffect)
	assert.Equal(t, types.ErrorCodeViolationFeeGrokCSAM, normalized.GetErrorCode())
}

func TestPrepareRoutingRelayAttemptIsolatesStreamStatusPerAttempt(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responsibility routingerror.Responsibility
		scope          routingerror.Scope
		health         routingerror.HealthEffect
		capacity       routingerror.CapacityEffect
	}{
		{name: "capacity", statusCode: http.StatusTooManyRequests, responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown},
		{name: "provider", statusCode: http.StatusBadGateway, responsibility: routingerror.ResponsibilityProvider, scope: routingerror.ScopePoolMember, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone},
		{name: "credential", statusCode: http.StatusUnauthorized, responsibility: routingerror.ResponsibilityCredential, scope: routingerror.ScopeCredential, health: routingerror.HealthOpen, capacity: routingerror.CapacityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now().Add(-time.Second)
			info := &relaycommon.RelayInfo{
				StartTime:             start,
				FirstResponseTime:     time.Now(),
				SendResponseCount:     2,
				ReceivedResponseCount: 3,
				StreamStatus:          relaycommon.NewStreamStatus(),
			}
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, errors.New("previous attempt timed out"))

			prepareRoutingRelayAttempt(info)
			assert.Nil(t, info.StreamStatus)
			assert.Zero(t, info.SendResponseCount)
			assert.Zero(t, info.ReceivedResponseCount)
			assert.False(t, info.HasSendResponse())
			info.SetFirstResponseTime()
			assert.True(t, info.HasSendResponse())
			apiErr := types.NewErrorWithStatusCode(errors.New("new attempt failed"), types.ErrorCodeBadResponseStatusCode, tt.statusCode)
			classification, success := classifyRoutingRelayAttempt(apiErr, info)

			assert.False(t, success)
			assert.Equal(t, tt.responsibility, classification.Responsibility)
			assert.Equal(t, tt.scope, classification.Scope)
			assert.Equal(t, tt.health, classification.HealthEffect)
			assert.Equal(t, tt.capacity, classification.CapacityEffect)
		})
	}
}

func TestPrepareRoutingRelayAttemptRestoresRequestFormatBaseline(t *testing.T) {
	info := &relaycommon.RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		IsStream:               false,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	prepareRoutingRelayAttempt(info)

	info.IsStream = true
	info.AppendRequestConversion(types.RelayFormatClaude)
	info.FinalRequestRelayFormat = types.RelayFormatClaude
	prepareRoutingRelayAttempt(info)

	assert.False(t, info.IsStream)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI}, info.RequestConversionChain)
	assert.Empty(t, info.FinalRequestRelayFormat)
}

func TestSubmitRoutingTaskAttemptResetsTelemetryBeforeEverySubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/invalid-task-platform", strings.NewReader(`{}`))
	requestStart := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	info := &relaycommon.RelayInfo{
		StartTime:         requestStart,
		FirstResponseTime: requestStart.Add(time.Second),
		IsStream:          true,
		TaskRelayInfo:     &relaycommon.TaskRelayInfo{},
	}
	info.ObserveRoutingOutputTokensAt(11, requestStart.Add(2*time.Second))

	result, taskErr := submitRoutingTaskAttempt(ctx, info)

	assert.Nil(t, result)
	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.True(t, info.FirstResponseTime.IsZero())
	assert.True(t, info.RoutingAttemptEndTime().IsZero())
	assert.Zero(t, info.RoutingOutputTokens())

	attemptStart := info.RoutingAttemptStartTime()
	info.FirstResponseTime = attemptStart.Add(100 * time.Millisecond)
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.SendResponseCount = 2
	info.ReceivedResponseCount = 3
	info.ObserveRoutingOutputTokensAt(7, attemptStart.Add(time.Second))

	result, taskErr = submitRoutingTaskAttempt(ctx, info)

	assert.Nil(t, result)
	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError)
	assert.True(t, info.FirstResponseTime.IsZero())
	assert.Nil(t, info.StreamStatus)
	assert.Zero(t, info.SendResponseCount)
	assert.Zero(t, info.ReceivedResponseCount)
	assert.True(t, info.RoutingAttemptEndTime().IsZero())
	assert.Zero(t, info.RoutingOutputTokens())
}

func TestClassifyRoutingTaskErrorKeepsContentSafety403OutOfAutoDisable(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	originalRanges := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: http.StatusForbidden, End: http.StatusForbidden}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = originalEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = originalRanges
	})

	taskErr := &dto.TaskError{Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusForbidden, Error: errors.New(service.CSAMViolationMarker)}
	apiErr := taskErrorToAPIError(taskErr)
	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationTaskSubmit,
		Signal:    routingerror.SignalContentSafety,
	})

	assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
	assert.Equal(t, routingerror.ScopeRequest, classification.Scope)
	assert.Equal(t, routingerror.RetryNever, classification.Retryability)
	assert.False(t, service.ShouldDisableChannel(apiErr, classification))
}

func TestRecordRoutingTaskAttemptCapturesMetricsAndBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	configureRoutingBreakerAttemptTest(t, true)
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	routingSetting.Consecutive5xx = 1
	routingSetting.BaseCooldownSec = 1
	routingSetting.MaxCooldownSec = 1
	smart_routing_setting.UpdateSetting(routingSetting)
	routingmetrics.ResetForTest()
	resetRoutingBreakerConfigIdentityForTest()
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
	})

	start := time.Now().Add(-1500 * time.Millisecond)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "mj-test",
		StartTime:       start,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelIsMultiKey: false,
		},
	}
	taskErr := &dto.TaskError{
		Code:       string(types.ErrorCodeBadResponse),
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream failed"),
	}
	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})
	apiErr := taskErrorToAPIError(taskErr)

	recordRoutingAttemptEffects(ctx, info, 31, false, apiErr, classification)
	assert.Equal(t, types.ErrorCodeBadResponse, apiErr.GetErrorCode())

	metrics := routingmetrics.Snapshots()
	require.Len(t, metrics, 1)
	assert.Equal(t, 31, metrics[0].ChannelID)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, metrics[0].APIKeyIndex)
	assert.Equal(t, "mj-test", metrics[0].ModelName)
	assert.Equal(t, "vip", metrics[0].Group)
	assert.Equal(t, int64(1), metrics[0].RequestCount)
	assert.Equal(t, int64(0), metrics[0].SuccessCount)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 1)
	assert.Equal(t, 31, breakers[0].Key.ChannelID)
	assert.Equal(t, routingbreaker.StateOpen, breakers[0].State)
	assert.Equal(t, "5xx", breakers[0].Reason)
}

func TestRecordRoutingTaskAttemptWritesStableMultiKeyWithoutLegacyState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "selected-key")
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 9)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 91)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 911)
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(3))
	configureRoutingBreakerAttemptTest(t, true)
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	routingSetting.Consecutive5xx = 1
	smart_routing_setting.UpdateSetting(routingSetting)
	routingmetrics.ResetForTest()
	t.Cleanup(routingmetrics.ResetForTest)

	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "mj-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelIsMultiKey: false},
	}
	taskErr := &dto.TaskError{StatusCode: http.StatusBadGateway, Error: errors.New("upstream failed")}
	classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})

	recordRoutingAttemptEffects(ctx, info, 37, false, taskErrorToAPIError(taskErr), classification)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Equal(t, routingmetrics.Stats{}, routingmetrics.RuntimeStats())
	stable := routingmetrics.StableSnapshots()
	require.Len(t, stable, 1)
	assert.Equal(t, 91, stable[0].PoolMemberID)
	assert.Equal(t, 911, stable[0].CredentialID)
	assert.Equal(t, int64(1), stable[0].FailureCount)
	assert.Empty(t, routingbreaker.DirtySnapshots())
	assert.Equal(t, routingbreaker.Stats{}, routingbreaker.RuntimeStats())
}

func singleKeyRoutingAttemptFixture(t *testing.T, channelID int) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	routingmetrics.ResetForTest()
	t.Cleanup(routingmetrics.ResetForTest)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointHost, "api.example.test")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://api.example.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-east-1")
	return ctx, &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID, ChannelIsMultiKey: false,
			RoutingEndpointHost: "api.example.test", RoutingEndpointAuthority: "https://api.example.test:443",
			RoutingRegion: "us-east-1",
		},
	}
}

func TestRecordRoutingAttemptEffectsMatrix(t *testing.T) {
	tests := []struct {
		name              string
		sourceStatus      int
		responseStatus    int
		retryAfterMs      int64
		responsibility    routingerror.Responsibility
		scope             routingerror.Scope
		health            routingerror.HealthEffect
		capacity          routingerror.CapacityEffect
		wantReliability   bool
		wantCapacity      bool
		wantChannelSignal bool
		wantBreakerReason string
		wantEndpointState string
	}{
		{name: "402 is channel capacity only", sourceStatus: 402, responseStatus: 402, responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopeChannel, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantChannelSignal: true, wantEndpointState: string(routingbreaker.StateHealthy)},
		{name: "mapped 429 is capacity only", sourceStatus: 429, responseStatus: 503, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true, wantEndpointState: string(routingbreaker.StateHealthy)},
		{name: "529 is capacity only", sourceStatus: 529, responseStatus: 529, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true, wantEndpointState: string(routingbreaker.StateHealthy)},
		{name: "502 is reliability only", sourceStatus: 502, responseStatus: 502, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone, wantReliability: true, wantBreakerReason: "5xx", wantEndpointState: string(routingbreaker.StateHealthy)},
		{name: "503 retry after has both effects", sourceStatus: 503, responseStatus: 503, retryAfterMs: 2500, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityCooldown, wantReliability: true, wantCapacity: true, wantBreakerReason: "5xx", wantEndpointState: string(routingbreaker.StateHealthy)},
		{name: "network endpoint does not poison member", sourceStatus: 504, responseStatus: 504, responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone, wantEndpointState: string(routingbreaker.StateOpen)},
		{name: "caller 400 has neither effect", sourceStatus: 400, responseStatus: 400, responsibility: routingerror.ResponsibilityCaller, health: routingerror.HealthIgnore, capacity: routingerror.CapacityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureRoutingBreakerAttemptTest(t, true)
			ctx, info := singleKeyRoutingAttemptFixture(t, 71)
			apiErr := types.NewErrorWithStatusCode(errors.New("failed"), types.ErrorCodeBadResponseStatusCode, tt.sourceStatus)
			apiErr.SetResponseStatusCode(tt.responseStatus)
			if tt.retryAfterMs > 0 {
				metadata, err := common.Marshal(map[string]int64{"retry_after_ms": tt.retryAfterMs})
				require.NoError(t, err)
				apiErr.Metadata = metadata
			}
			classification := routingerror.Classification{
				Responsibility: tt.responsibility,
				Scope:          tt.scope,
				HealthEffect:   tt.health,
				CapacityEffect: tt.capacity,
				Component:      routingerror.ComponentServing,
			}

			recordRoutingAttemptEffects(ctx, info, 71, false, apiErr, classification)

			key := routinghotcache.Key{ChannelID: 71, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
			capacity, hasCapacity := routinghotcache.GetCapacityCooldown(key)
			assert.Equal(t, tt.wantCapacity, hasCapacity)
			if hasCapacity {
				assert.Equal(t, tt.sourceStatus, capacity.SourceStatusCode)
				assert.Greater(t, capacity.CooldownUntilUnixMilli, capacity.UpdatedUnixMilli)
				if tt.retryAfterMs > 0 {
					assert.Equal(t, tt.retryAfterMs, capacity.RetryAfterMs)
				}
			}
			_, hasChannelSignal := routinghotcache.GetChannelBalanceUnavailable(71)
			assert.Equal(t, tt.wantChannelSignal, hasChannelSignal)
			breaker, hasBreaker := routinghotcache.GetBreaker(key)
			assert.Equal(t, tt.wantReliability, hasBreaker)
			if tt.wantBreakerReason != "" && hasBreaker {
				assert.Equal(t, tt.wantBreakerReason, breaker.Reason)
			}
			endpoint, hasEndpoint := routinghotcache.GetBreaker(
				routingbreaker.NewEndpointKey("https://api.example.test:443", "us-east-1").HotcacheKey(),
			)
			assert.Equal(t, tt.wantEndpointState != "", hasEndpoint)
			if hasEndpoint {
				assert.Equal(t, tt.wantEndpointState, endpoint.State)
			}
			metrics := routingmetrics.Snapshots()
			require.Len(t, metrics, 1)
			if tt.wantReliability {
				assert.Equal(t, int64(1), metrics[0].ReliabilityRequestCount)
				assert.Equal(t, int64(1), metrics[0].ReliabilityFailureCount)
			} else {
				assert.Zero(t, metrics[0].ReliabilityRequestCount)
				assert.Zero(t, metrics[0].ReliabilityFailureCount)
			}
		})
	}
}

func TestRecordRoutingAttemptEffectsSuccessOnlyRecordsReliability(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 72)
	classification := routingerror.ClassifyAPIError(nil, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 72, true, nil, classification)

	key := routinghotcache.Key{ChannelID: 72, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	_, hasCapacity := routinghotcache.GetCapacityCooldown(key)
	assert.False(t, hasCapacity)
	breaker, hasBreaker := routinghotcache.GetBreaker(key)
	require.True(t, hasBreaker)
	assert.Equal(t, string(routingbreaker.StateHealthy), breaker.State)
	metrics := routingmetrics.Snapshots()
	require.Len(t, metrics, 1)
	assert.Equal(t, int64(1), metrics[0].SuccessCount)
	assert.Equal(t, int64(1), metrics[0].ReliabilityRequestCount)
	assert.Zero(t, metrics[0].ReliabilityFailureCount)
}

func TestRecordRoutingAttemptEffectsDoesNothingWhenDisabled(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, false)
	ctx, info := singleKeyRoutingAttemptFixture(t, 30)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 30, false, apiErr, classification)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Empty(t, routingbreaker.DirtySnapshots())
	key := routinghotcache.Key{ChannelID: 30, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	_, hasBreaker := routinghotcache.GetBreaker(key)
	_, hasCapacity := routinghotcache.GetCapacityCooldown(key)
	assert.False(t, hasBreaker)
	assert.False(t, hasCapacity)
}

func TestRecordRoutingAttemptEffectsKeepsChannel402SignalWhenDisabledAndMultiKey(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, false)
	ctx, info := singleKeyRoutingAttemptFixture(t, 35)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 2)
	apiErr := types.NewErrorWithStatusCode(
		errors.New("payment required"), types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired,
	)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationRelay,
	})

	recordRoutingAttemptEffects(ctx, info, 35, false, apiErr, classification)

	signal, found := routinghotcache.GetChannelBalanceUnavailable(35)
	require.True(t, found)
	assert.Equal(t, http.StatusPaymentRequired, signal.SourceStatusCode)
	assert.Equal(t, routinghotcache.ChannelBalanceUnavailableReason, signal.Reason)
	assert.Greater(t, signal.CooldownUntilUnixMilli, signal.UpdatedUnixMilli)
	_, aggregate := routinghotcache.GetCapacityCooldown(routinghotcache.Key{
		ChannelID: 35, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip",
	})
	assert.False(t, aggregate)
}

func TestRecordRoutingAttemptEffectsIgnoresCurrentMultiKeyWithStaleSingleKeyMeta(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 34)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 2)
	apiErr := types.NewErrorWithStatusCode(errors.New("temporarily unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	apiErr.Metadata = metadata
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 34, false, apiErr, classification)

	assert.Empty(t, routingmetrics.Snapshots())
	assert.Equal(t, routingmetrics.Stats{}, routingmetrics.RuntimeStats())
	assert.Empty(t, routingbreaker.DirtySnapshots())
	assert.Equal(t, routingbreaker.Stats{}, routingbreaker.RuntimeStats())
	_, aggregate := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, perKey := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 34, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	_, aggregateCapacity := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, perKeyCapacity := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 34, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	assert.False(t, aggregate)
	assert.False(t, perKey)
	assert.False(t, aggregateCapacity)
	assert.False(t, perKeyCapacity)
}

func TestRecordRoutingAttemptEffectsUsesOnlyMinusOneForCurrentSingleKeyWithStaleMultiKeyMeta(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	ctx, info := singleKeyRoutingAttemptFixture(t, 36)
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 36, ChannelIsMultiKey: true, ChannelMultiKeyIndex: 2}
	apiErr := types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 36, false, apiErr, classification)

	breakers := routingbreaker.DirtySnapshots()
	require.Len(t, breakers, 1)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, breakers[0].Key.APIKeyIndex)
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 36, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cached.State)
	_, perKey := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 36, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"})
	assert.False(t, perKey)
}

func TestRecordRoutingAttemptEffectsUsesSmartRoutingBreakerSettings(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		Consecutive5xx:     2,
		FailureRatePct:     90,
		BaseCooldownSec:    7,
		MaxCooldownSec:     7,
		MetricBucketSec:    60,
		FlushIntervalMin:   1,
		SyncIntervalMin:    1,
		HotcacheRefreshSec: 1,
	})
	resetRoutingBreakerConfigIdentityForTest()
	ctx, info := singleKeyRoutingAttemptFixture(t, 33)
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay})

	recordRoutingAttemptEffects(ctx, info, 33, false, apiErr, classification)
	recordRoutingAttemptEffects(ctx, info, 33, false, apiErr, classification)

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 33, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, string(routingbreaker.StateOpen), cached.State)
}

func TestRecordRoutingAttemptEffectsPublishesBreakerOpenAndRecoveryEvents(t *testing.T) {
	configureRoutingBreakerAttemptTest(t, true)
	channelrouting.ResetRoutingEventsForTest()
	t.Cleanup(channelrouting.ResetRoutingEventsForTest)
	ctx, info := singleKeyRoutingAttemptFixture(t, 34)
	apiErr := types.NewErrorWithStatusCode(
		errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway,
	)
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
		Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay,
	})

	recordRoutingAttemptEffects(ctx, info, 34, false, apiErr, classification)
	key := routingbreaker.Key{
		ChannelID: 34, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "vip",
	}
	routingbreaker.ResetDefaultForTest(routingBreakerConfigFromSetting(smart_routing_setting.GetSetting()))
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key: key, State: routingbreaker.StateHalfOpen, UpdatedAt: time.Now(),
	}})
	recordRoutingAttemptEffects(ctx, info, 34, true, nil, routingerror.Classification{})

	events := channelrouting.RecentRoutingEvents(
		10, channelrouting.RoutingEventTypeBreakerOpened, channelrouting.RoutingEventTypeBreakerRecovered,
	)
	require.Len(t, events, 2)
	assert.Equal(t, channelrouting.RoutingEventTypeBreakerRecovered, events[0].Type)
	assert.Equal(t, channelrouting.RoutingEventTypeBreakerOpened, events[1].Type)
	assert.Contains(t, string(events[0].PayloadJSON), `"channel_id":34`)
	assert.Contains(t, string(events[0].PayloadJSON), `"current_state":"healthy"`)
}

func TestRelayInvalidRequestReleasesReservedHalfOpenProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 5,
		BaseCooldown:            time.Second,
		MaxCooldown:             time.Minute,
		Now: func() time.Time {
			return now
		},
	})
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	key := routingbreaker.Key{ChannelID: 35, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       key,
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: now,
	}})
	_, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(key, 1)
	require.True(t, acquired)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(ctx, constant.ContextKeyRoutingHalfOpenProbes, map[routingbreaker.Key]struct{}{key: {}})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	Relay(ctx, types.RelayFormatOpenAI)

	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 35, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Zero(t, cached.HalfOpenInflight)
}
