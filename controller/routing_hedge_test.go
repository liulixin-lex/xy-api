package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingHedgeReplaySafeRequestExclusionMatrix(t *testing.T) {
	stream := true
	nTwo := 2
	base := &dto.GeneralOpenAIRequest{
		Model:    "gpt-test",
		Messages: []dto.Message{{Role: "user", Content: "hello"}},
	}
	tests := []struct {
		name   string
		mutate func(*dto.GeneralOpenAIRequest)
		want   bool
	}{
		{name: "plain chat", want: true},
		{name: "text parts", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.Messages[0].Content = []any{map[string]any{"type": dto.ContentTypeText, "text": "hello"}}
		}, want: true},
		{name: "stream", mutate: func(request *dto.GeneralOpenAIRequest) { request.Stream = &stream }},
		{name: "multiple choices", mutate: func(request *dto.GeneralOpenAIRequest) { request.N = &nTwo }},
		{name: "tools", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.Tools = []dto.ToolCallRequest{{Type: "function"}}
		}},
		{name: "tool history", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.Messages[0].ToolCallId = "call-1"
		}},
		{name: "image", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.Messages[0].Content = []any{map[string]any{"type": dto.ContentTypeImageURL, "image_url": "https://example.test/image.png"}}
		}},
		{name: "stateful store", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.Store = []byte("true")
		}},
		{name: "pass through extension", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.ExtraBody = []byte(`{"side_effect":true}`)
		}},
		{name: "web search", mutate: func(request *dto.GeneralOpenAIRequest) {
			request.WebSearchOptions = &dto.WebSearchOptions{SearchContextSize: "medium"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := common.DeepCopy(base)
			require.NoError(t, err)
			if test.mutate != nil {
				test.mutate(request)
			}
			assert.Equal(t, test.want, routingHedgeReplaySafeTextRequest(request, relayconstant.RelayModeChatCompletions))
		})
	}

	assert.True(t, routingHedgeReplaySafeTextRequest(
		&dto.GeneralOpenAIRequest{Model: "gpt-test", Prompt: "hello"},
		relayconstant.RelayModeCompletions,
	))
	assert.False(t, routingHedgeReplaySafeTextRequest(
		&dto.GeneralOpenAIRequest{Model: "gpt-test", Prompt: map[string]any{"unsafe": true}},
		relayconstant.RelayModeCompletions,
	))
}

func TestRoutingHedgePrimarySuccessBeforeDelayDoesNotTriggerSecondary(t *testing.T) {
	results := make(chan routingHedgeBranchResult, 1)
	primary := &routingHedgeBranch{role: channelrouting.HedgeAttemptPrimary}
	results <- routingHedgeBranchResult{branch: primary, success: true}

	result, finished := waitRoutingHedgePrimaryOrDelay(results, time.Second)

	assert.True(t, finished)
	assert.Same(t, primary, result.branch)
}

func TestRoutingHedgeSecondaryWinCancelsBlockedPrimaryBodyRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	headersSent := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(headersSent)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	coordinator := newRoutingHedgeCoordinatorForTest(t)
	primaryLease, err := coordinator.BeginPrimary()
	require.NoError(t, err)
	secondaryLease, err := coordinator.BeginSecondary(1, true)
	require.NoError(t, err)
	primaryContext, primaryCancel := context.WithCancelCause(context.Background())
	secondaryContext, secondaryCancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() {
		primaryCancel(nil)
		secondaryCancel(nil)
	})
	primary := &routingHedgeBranch{
		role: channelrouting.HedgeAttemptPrimary, lease: primaryLease, cancel: primaryCancel,
	}
	secondary := &routingHedgeBranch{
		role: channelrouting.HedgeAttemptSecondary, lease: secondaryLease, cancel: secondaryCancel,
	}
	primaryResults := make(chan routingHedgeBranchResult, 1)
	secondaryResults := make(chan routingHedgeBranchResult, 1)
	go func() {
		request, requestErr := http.NewRequestWithContext(primaryContext, http.MethodGet, server.URL, nil)
		if requestErr != nil {
			primaryResults <- routingHedgeBranchResult{branch: primary, apiErr: types.NewError(requestErr, types.ErrorCodeDoRequestFailed)}
			return
		}
		response, requestErr := server.Client().Do(request)
		if requestErr == nil {
			_, requestErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		primaryResults <- routingHedgeBranchResult{
			branch: primary, apiErr: types.NewError(requestErr, types.ErrorCodeDoRequestFailed),
			cause: context.Cause(primaryContext),
		}
	}()
	select {
	case <-headersSent:
	case <-time.After(time.Second):
		require.FailNow(t, "primary request did not reach the blocked body read")
	}
	secondaryResults <- routingHedgeBranchResult{branch: secondary, success: true}

	collection := collectRoutingHedgeActiveResults(primaryResults, secondaryResults, primary, secondary)

	require.NotNil(t, collection.winner)
	assert.Equal(t, channelrouting.HedgeAttemptSecondary, collection.winner.branch.role)
	assert.ErrorIs(t, context.Cause(primaryContext), channelrouting.ErrHedgeLost)
	select {
	case loser := <-collection.pending:
		assert.ErrorIs(t, loser.cause, channelrouting.ErrHedgeLost)
	case <-time.After(time.Second):
		require.FailNow(t, "canceled primary attempt did not exit")
	}
	assert.NoError(t, context.Cause(secondaryContext))
}

func TestRoutingHedgeBothAttemptsFailWithoutSelectingWinner(t *testing.T) {
	coordinator := newRoutingHedgeCoordinatorForTest(t)
	primaryLease, err := coordinator.BeginPrimary()
	require.NoError(t, err)
	secondaryLease, err := coordinator.BeginSecondary(1, true)
	require.NoError(t, err)
	_, primaryCancel := context.WithCancelCause(context.Background())
	_, secondaryCancel := context.WithCancelCause(context.Background())
	primary := &routingHedgeBranch{role: channelrouting.HedgeAttemptPrimary, lease: primaryLease, cancel: primaryCancel}
	secondary := &routingHedgeBranch{role: channelrouting.HedgeAttemptSecondary, lease: secondaryLease, cancel: secondaryCancel}
	primaryResults := make(chan routingHedgeBranchResult, 1)
	secondaryResults := make(chan routingHedgeBranchResult, 1)
	primaryResults <- routingHedgeBranchResult{branch: primary, apiErr: types.NewError(errors.New("primary"), types.ErrorCodeDoRequestFailed)}
	secondaryResults <- routingHedgeBranchResult{branch: secondary, apiErr: types.NewError(errors.New("secondary"), types.ErrorCodeDoRequestFailed)}

	collection := collectRoutingHedgeActiveResults(primaryResults, secondaryResults, primary, secondary)

	assert.Nil(t, collection.winner)
	assert.Len(t, collection.results, 2)
	assert.Empty(t, coordinator.Snapshot().Winner)
}

func TestRoutingHedgeWinnerCommitsOneCapturedSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	clientContext, _ := gin.CreateTestContext(recorder)
	clientContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	branchRecorder := newRoutingHedgeResponseWriter(1 << 20)
	branchRecorder.Header().Set("Content-Type", "application/json")
	branchRecorder.WriteHeader(http.StatusOK)
	_, err := branchRecorder.Write([]byte(`{"id":"winner"}`))
	require.NoError(t, err)
	branchContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	branchContext.Request = clientContext.Request.Clone(context.Background())
	branchInfo := &relaycommon.RelayInfo{
		Request:     &dto.GeneralOpenAIRequest{Model: "gpt-test"},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 2},
	}
	branch := &routingHedgeBranch{ctx: branchContext, info: branchInfo, recorder: branchRecorder}
	baseInfo := &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Model: "gpt-test"}}
	var settlements atomic.Int64
	previousFinalize := routingHedgeFinalizeText
	routingHedgeFinalizeText = func(*gin.Context, *relaycommon.RelayInfo, *relay.TextResponseCapture) error {
		settlements.Add(1)
		return nil
	}
	t.Cleanup(func() { routingHedgeFinalizeText = previousFinalize })

	apiErr := commitRoutingHedgeWinner(clientContext, baseInfo, nil, routingHedgeBranchResult{
		branch: branch, capture: &relay.TextResponseCapture{Usage: &dto.Usage{TotalTokens: 3}}, success: true,
	})

	assert.Nil(t, apiErr)
	assert.Equal(t, int64(1), settlements.Load())
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"id":"winner"}`, recorder.Body.String())
	assert.Equal(t, 2, baseInfo.ChannelId)
}

func TestRoutingHedgePartialClientWriteStaysCommittedAndSettled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	clientContext, _ := gin.CreateTestContext(recorder)
	clientContext.Writer = &routingHedgePartialResponseWriter{ResponseWriter: clientContext.Writer, limit: 5}
	clientContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	branchRecorder := newRoutingHedgeResponseWriter(1 << 20)
	branchRecorder.WriteHeader(http.StatusOK)
	_, err := branchRecorder.Write([]byte(`{"id":"winner"}`))
	require.NoError(t, err)
	branchContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	branchContext.Request = clientContext.Request.Clone(context.Background())
	billing := &routingHedgeBillingSpy{}
	branchInfo := &relaycommon.RelayInfo{
		Request: &dto.GeneralOpenAIRequest{Model: "gpt-test"}, Billing: billing,
	}
	branch := &routingHedgeBranch{ctx: branchContext, info: branchInfo, recorder: branchRecorder}
	baseInfo := &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{Model: "gpt-test"}, Billing: billing}
	coordinator := channelrouting.NewAttemptCoordinator(channelrouting.AttemptPolicy{
		MaxAttempts: 2, Deadline: time.Now().Add(time.Minute), ExtraCostBudgetUnits: 2,
		RetryTokenCapacity: 2, RetryTokenRefill: 1,
	})
	lease, err := coordinator.BeginAttempt(channelrouting.AttemptInput{PoolID: 1, EstimatedCostUnits: 1})
	require.NoError(t, err)
	require.NoError(t, lease.MarkSent())
	previousFinalize := routingHedgeFinalizeText
	routingHedgeFinalizeText = func(_ *gin.Context, info *relaycommon.RelayInfo, _ *relay.TextResponseCapture) error {
		return info.Billing.Settle(1)
	}
	t.Cleanup(func() { routingHedgeFinalizeText = previousFinalize })

	apiErr := commitRoutingHedgeWinner(clientContext, baseInfo, lease, routingHedgeBranchResult{
		branch: branch, capture: &relay.TextResponseCapture{Usage: &dto.Usage{TotalTokens: 3}}, success: true,
	})

	require.NotNil(t, apiErr)
	assert.True(t, clientContext.Writer.Written())
	assert.Equal(t, `{"id"`, recorder.Body.String())
	assert.True(t, billing.settled.Load())
	committedBody := recorder.Body.String()
	writeRelayAPIErrorResponse(clientContext, types.RelayFormatOpenAI, nil, apiErr)
	assert.Equal(t, committedBody, recorder.Body.String(), "a committed writer must not receive a JSON error suffix")
	billing.Refund(clientContext)
	assert.Zero(t, billing.refunds.Load())
	lease.Finish()
	_, err = coordinator.BeginAttempt(channelrouting.AttemptInput{PoolID: 1, EstimatedCostUnits: 1})
	assert.ErrorIs(t, err, channelrouting.ErrAttemptClientCommitted)
}

func TestRoutingHedgeLoserKeepsProcessBudgetsUntilBranchActuallyExits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := &channelrouting.HedgeLimiter{}
	byteLimiter := &channelrouting.HedgeByteLimiter{}
	slot := limiter.TryAcquire(1)
	require.NotNil(t, slot)
	bufferSlot := byteLimiter.TryAcquire(8, 8)
	require.NotNil(t, bufferSlot)
	branchContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestContext, cancel := context.WithCancelCause(context.Background())
	branchContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	loser := &routingHedgeBranch{
		role: channelrouting.HedgeAttemptPrimary, ctx: branchContext,
		info: &relaycommon.RelayInfo{OriginModelName: "gpt-test"}, channel: &model.Channel{Id: 1},
		cancel: cancel, lease: &channelrouting.HedgeAttemptLease{}, recorder: newRoutingHedgeResponseWriter(8),
	}
	results := make(chan routingHedgeBranchResult, 1)
	done := finishRoutingHedgeLoserAsync(results, loser, nil, slot, bufferSlot, time.Nanosecond)

	select {
	case <-requestContext.Done():
	case <-time.After(time.Second):
		require.FailNow(t, "loser cleanup did not cancel the branch")
	}
	assert.Equal(t, int64(1), limiter.Stats(1).Active)
	assert.Equal(t, int64(8), byteLimiter.Stats(8).ActiveBytes)
	results <- routingHedgeBranchResult{
		branch: loser, cause: channelrouting.ErrHedgeLost, completedAtMs: time.Now().UnixMilli(),
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "loser cleanup did not finish after branch exit")
	}
	assert.Zero(t, limiter.Stats(1).Active)
	assert.Zero(t, byteLimiter.Stats(8).ActiveBytes)
}

func TestRoutingHedgeBufferedSafetyRejectionTakesPrecedenceOverSuccess(t *testing.T) {
	for _, safetyRole := range []channelrouting.HedgeAttemptRole{
		channelrouting.HedgeAttemptPrimary,
		channelrouting.HedgeAttemptSecondary,
	} {
		t.Run(string(safetyRole), func(t *testing.T) {
			coordinator := newRoutingHedgeCoordinatorForTest(t)
			primaryLease, err := coordinator.BeginPrimary()
			require.NoError(t, err)
			secondaryLease, err := coordinator.BeginSecondary(1, true)
			require.NoError(t, err)
			_, primaryCancel := context.WithCancelCause(context.Background())
			_, secondaryCancel := context.WithCancelCause(context.Background())
			primary := &routingHedgeBranch{
				role: channelrouting.HedgeAttemptPrimary, lease: primaryLease, cancel: primaryCancel,
			}
			secondary := &routingHedgeBranch{
				role: channelrouting.HedgeAttemptSecondary, lease: secondaryLease, cancel: secondaryCancel,
			}
			primaryResult := routingHedgeBranchResult{branch: primary, success: true}
			secondaryResult := routingHedgeBranchResult{branch: secondary, success: true}
			safety := routingHedgeBranchResult{
				classification: routingerror.Classification{
					Responsibility: routingerror.ResponsibilityCaller,
					Retryability:   routingerror.RetryNever,
					Rule:           "content_safety",
				},
			}
			if safetyRole == channelrouting.HedgeAttemptPrimary {
				safety.branch = primary
				primaryResult = safety
			} else {
				safety.branch = secondary
				secondaryResult = safety
			}
			primaryResults := make(chan routingHedgeBranchResult, 1)
			secondaryResults := make(chan routingHedgeBranchResult, 1)
			primaryResults <- primaryResult
			secondaryResults <- secondaryResult

			collection := collectRoutingHedgeActiveResults(primaryResults, secondaryResults, primary, secondary)

			assert.Nil(t, collection.winner)
			require.NotNil(t, collection.terminal)
			assert.Equal(t, safetyRole, collection.terminal.branch.role)
			assert.Empty(t, coordinator.Snapshot().Winner)
			if collection.pending != nil {
				<-collection.pending
			}
		})
	}
}

func TestRoutingHedgeRequiresDistinctEndpointOrUpstreamAccountFailureDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name                string
		primaryAuthority    string
		secondaryAuthority  string
		primaryAccount      string
		secondaryAccount    string
		primaryCredential   int
		secondaryCredential int
		want                bool
	}{
		{
			name: "different endpoint", primaryAuthority: "https://api-a.example.test:443",
			secondaryAuthority: "https://api-b.example.test:443", primaryAccount: strings.Repeat("a", 64),
			secondaryAccount: strings.Repeat("a", 64), primaryCredential: 11, secondaryCredential: 12, want: true,
		},
		{
			name: "different upstream account", primaryAuthority: "https://api.example.test:443",
			secondaryAuthority: "https://api.example.test:443", primaryAccount: strings.Repeat("a", 64),
			secondaryAccount: strings.Repeat("b", 64), primaryCredential: 11, secondaryCredential: 12, want: true,
		},
		{
			name: "same endpoint and account", primaryAuthority: "https://api.example.test:443",
			secondaryAuthority: "https://api.example.test:443", primaryAccount: strings.Repeat("a", 64),
			secondaryAccount: strings.Repeat("a", 64), primaryCredential: 11, secondaryCredential: 12,
		},
		{
			name: "same credential", primaryAuthority: "https://api-a.example.test:443",
			secondaryAuthority: "https://api-b.example.test:443", primaryAccount: strings.Repeat("a", 64),
			secondaryAccount: strings.Repeat("a", 64), primaryCredential: 11, secondaryCredential: 11,
		},
		{
			name: "unknown endpoint", secondaryAuthority: "https://api-b.example.test:443",
			primaryAccount: strings.Repeat("a", 64), secondaryAccount: strings.Repeat("b", 64),
			primaryCredential: 11, secondaryCredential: 12,
		},
		{
			name: "unknown account", primaryAuthority: "https://api-a.example.test:443",
			secondaryAuthority: "https://api-b.example.test:443", secondaryAccount: strings.Repeat("b", 64),
			primaryCredential: 11, secondaryCredential: 12,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary, _ := gin.CreateTestContext(httptest.NewRecorder())
			secondary, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(primary, constant.ContextKeyRoutingEndpointAuthority, test.primaryAuthority)
			common.SetContextKey(secondary, constant.ContextKeyRoutingEndpointAuthority, test.secondaryAuthority)
			common.SetContextKey(primary, constant.ContextKeyRoutingCredentialID, test.primaryCredential)
			common.SetContextKey(secondary, constant.ContextKeyRoutingCredentialID, test.secondaryCredential)

			actual := routingHedgeTargetsHaveDistinctFailureDomain(
				primary,
				secondary,
				channelrouting.ShadowCostInput{AccountKeyHash: test.primaryAccount},
				channelrouting.ShadowCostInput{AccountKeyHash: test.secondaryAccount},
			)

			assert.Equal(t, test.want, actual)
		})
	}
}

func TestRoutingHedgeResponseWriterEnforcesHardBound(t *testing.T) {
	writer := newRoutingHedgeResponseWriter(4)
	_, err := writer.Write([]byte("1234"))
	require.NoError(t, err)
	_, err = writer.Write([]byte("5"))
	assert.ErrorIs(t, err, errRoutingHedgeResponseTooLarge)
	assert.True(t, writer.Overflowed())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	assert.ErrorIs(t, writer.CommitTo(ctx.Writer), errRoutingHedgeResponseTooLarge)
}

func TestRoutingHedgeBufferedStreamFailureRemainsRetryableBeforeClientCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := newRoutingHedgeResponseWriter(1 << 20)
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		IsStream:    true,
	}
	branch := &routingHedgeBranch{
		ctx: ctx, info: info, channel: &model.Channel{Id: 1}, recorder: recorder,
		lease: &channelrouting.HedgeAttemptLease{},
	}
	previousExecute := routingHedgeExecuteText
	routingHedgeExecuteText = func(ctx *gin.Context, info *relaycommon.RelayInfo) (*relay.TextResponseCapture, *types.NewAPIError) {
		_, err := ctx.Writer.Write([]byte("data: partial\n\n"))
		require.NoError(t, err)
		info.StreamStatus = relaycommon.NewStreamStatus()
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("scanner failed"))
		return nil, nil
	}
	t.Cleanup(func() { routingHedgeExecuteText = previousExecute })

	result := executeRoutingHedgeBranch(branch)

	assert.True(t, ctx.Writer.Written(), "the hedge buffer records an internal write")
	require.NotNil(t, result.apiErr)
	assert.False(t, result.success)
	assert.Equal(t, http.StatusBadGateway, result.apiErr.StatusCode)
	assert.Equal(t, routingerror.RetryBeforeCommit, result.classification.Retryability)
}

func TestRoutingHedgeBranchClonesRequestConversionAndOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	source, _ := gin.CreateTestContext(httptest.NewRecorder())
	source.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(source, constant.ContextKeyChannelParamOverride, map[string]any{"temperature": 0.5})
	base := &relaycommon.RelayInfo{
		Request:                &dto.GeneralOpenAIRequest{Model: "gpt-test", Messages: []dto.Message{{Role: "user", Content: "original"}}},
		RuntimeHeadersOverride: map[string]any{"X-Test": "original"},
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	lease := &channelrouting.HedgeAttemptLease{}
	branch, err := newRoutingHedgeBranch(
		source, base, &model.Channel{Id: 1}, channelrouting.HedgeAttemptPrimary,
		lease, []byte(`{"model":"gpt-test"}`), 1<<20, false,
	)
	require.NoError(t, err)
	t.Cleanup(func() { branch.cancel(nil) })

	branchRequest := branch.info.Request.(*dto.GeneralOpenAIRequest)
	branchRequest.Messages[0].Content = "changed"
	branch.info.RuntimeHeadersOverride["X-Test"] = "changed"
	branchOverrides := common.GetContextKeyStringMap(branch.ctx, constant.ContextKeyChannelParamOverride)
	branchOverrides["temperature"] = 1.0
	branch.info.RequestConversionChain[0] = types.RelayFormatClaude

	assert.Equal(t, "original", base.Request.(*dto.GeneralOpenAIRequest).Messages[0].Content)
	assert.Equal(t, "original", base.RuntimeHeadersOverride["X-Test"])
	assert.Equal(t, 0.5, common.GetContextKeyStringMap(source, constant.ContextKeyChannelParamOverride)["temperature"])
	assert.Equal(t, types.RelayFormatOpenAI, base.RequestConversionChain[0])
}

func newRoutingHedgeCoordinatorForTest(t *testing.T) *channelrouting.HedgeCoordinator {
	t.Helper()
	coordinator, err := channelrouting.NewHedgeCoordinator(channelrouting.EnterpriseHedgePolicy{
		Enabled: true, Explicit: true, Delay: 25 * time.Millisecond,
		MaxExtraCostMultiplier: 1, MaxResponseBytes: 1 << 20,
		Scope: channelrouting.EnterpriseHedgeScopeDistinctTarget,
	}, 1)
	require.NoError(t, err)
	return coordinator
}

type routingHedgePartialResponseWriter struct {
	gin.ResponseWriter
	limit int
}

func (writer *routingHedgePartialResponseWriter) Write(data []byte) (int, error) {
	limit := min(writer.limit, len(data))
	written, _ := writer.ResponseWriter.Write(data[:limit])
	return written, io.ErrShortWrite
}

type routingHedgeBillingSpy struct {
	settled atomic.Bool
	refunds atomic.Int64
}

func (billing *routingHedgeBillingSpy) Settle(int) error {
	billing.settled.Store(true)
	return nil
}

func (billing *routingHedgeBillingSpy) Refund(*gin.Context) {
	if !billing.settled.Load() {
		billing.refunds.Add(1)
	}
}

func (billing *routingHedgeBillingSpy) NeedsRefund() bool {
	return !billing.settled.Load()
}

func (billing *routingHedgeBillingSpy) GetPreConsumedQuota() int {
	return 1
}

func (billing *routingHedgeBillingSpy) Reserve(int) error {
	return nil
}
