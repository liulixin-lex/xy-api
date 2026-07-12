package common

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoRoutingObservationTracksMonotonicTokensAndAttemptEnd(t *testing.T) {
	attemptStart := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	info := &RelayInfo{StartTime: attemptStart}

	info.ObserveRoutingOutputTokensAt(0, attemptStart.Add(500*time.Millisecond))
	assert.Zero(t, info.RoutingOutputTokens())
	assert.Equal(t, attemptStart.Add(500*time.Millisecond), info.RoutingAttemptEndTime())

	info.ObserveRoutingOutputTokensAt(12, attemptStart.Add(1200*time.Millisecond))
	info.ObserveRoutingOutputTokensAt(8, attemptStart.Add(2*time.Second))
	info.ObserveRoutingOutputTokensAt(12, attemptStart.Add(1500*time.Millisecond))
	info.ObserveRoutingOutputTokensAt(15, attemptStart.Add(1400*time.Millisecond))
	info.ObserveRoutingOutputTokensAt(-1, attemptStart.Add(3*time.Second))

	assert.Equal(t, int64(15), info.RoutingOutputTokens())
	assert.Equal(t, attemptStart.Add(1500*time.Millisecond), info.RoutingAttemptEndTime())
}

func TestRelayInfoRoutingObservationIsAttemptScoped(t *testing.T) {
	logicalStart := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	info := &RelayInfo{StartTime: logicalStart}
	info.ObserveRoutingOutputTokensAt(12, logicalStart.Add(time.Second))

	info.ResetStreamAttemptState()
	attemptStart := info.RoutingAttemptStartTime()
	assert.Zero(t, info.RoutingOutputTokens())
	assert.True(t, info.RoutingAttemptEndTime().IsZero())

	info.ObserveRoutingOutputTokensAt(7, attemptStart.Add(700*time.Millisecond))
	assert.Equal(t, int64(7), info.RoutingOutputTokens())
	assert.Equal(t, attemptStart.Add(700*time.Millisecond), info.RoutingAttemptEndTime())

	info.ResetStreamAttemptState()
	assert.Zero(t, info.RoutingOutputTokens())
	assert.True(t, info.RoutingAttemptEndTime().IsZero())
}

func TestRelayInfoRoutingAttemptUsageIsNormalizedAndResetPerAttempt(t *testing.T) {
	info := &RelayInfo{}
	info.ObserveRoutingAttemptUsage(&dto.Usage{
		PromptTokens: 100, CompletionTokens: 25, PromptCacheHitTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 15, CachedCreationTokens: 7, ImageTokens: 3, AudioTokens: 4,
		},
		CompletionTokenDetails:      dto.OutputTokenDetails{ImageTokens: 2, AudioTokens: 1},
		ClaudeCacheCreation1hTokens: 5,
		UsageSemantic:               "anthropic",
	})

	usage, known := info.RoutingAttemptUsageSnapshot()
	require.True(t, known)
	assert.Equal(t, int64(100), usage.PromptTokens)
	assert.Equal(t, int64(25), usage.CompletionTokens)
	assert.Equal(t, int64(20), usage.CacheReadTokens)
	assert.Zero(t, usage.CacheWriteTokens, "one-hour cache creation remains a distinct billing dimension")
	assert.Equal(t, int64(5), usage.CacheWrite1hTokens)
	assert.Equal(t, int64(3), usage.ImageInputTokens)
	assert.Equal(t, int64(2), usage.ImageOutputTokens)
	assert.True(t, usage.ClaudeSemantic)

	info.ResetStreamAttemptState()
	_, known = info.RoutingAttemptUsageSnapshot()
	assert.False(t, known)

	info.ObserveRoutingRealtimeUsage(&dto.RealtimeUsage{
		InputTokens: 12, OutputTokens: 8,
		InputTokenDetails:  dto.InputTokenDetails{CachedTokens: 4, AudioTokens: 3},
		OutputTokenDetails: dto.OutputTokenDetails{AudioTokens: 2},
	})
	usage, known = info.RoutingAttemptUsageSnapshot()
	require.True(t, known)
	assert.Equal(t, int64(12), usage.PromptTokens)
	assert.Equal(t, int64(8), usage.CompletionTokens)
	assert.Equal(t, int64(4), usage.CacheReadTokens)
	assert.Equal(t, int64(3), usage.AudioInputTokens)
	assert.Equal(t, int64(2), usage.AudioOutputTokens)
}

func TestCurrentAttemptIsMultiKeyPrefersContextOverStaleChannelMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		context   bool
		channel   bool
		wantMulti bool
	}{
		{name: "context multi key overrides stale single key metadata", context: true, channel: false, wantMulti: true},
		{name: "context single key overrides stale multi key metadata", context: false, channel: true, wantMulti: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, test.context)
			info := &RelayInfo{ChannelMeta: &ChannelMeta{ChannelIsMultiKey: test.channel}}

			assert.Equal(t, test.wantMulti, info.CurrentAttemptIsMultiKey(ctx))
		})
	}

	assert.True(t, (&RelayInfo{ChannelMeta: &ChannelMeta{ChannelIsMultiKey: true}}).CurrentAttemptIsMultiKey(nil))
	assert.False(t, (*RelayInfo)(nil).CurrentAttemptIsMultiKey(nil))
}

func TestInitChannelMetaCopiesStableRoutingIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 41)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(17))
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 7)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 11)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 13)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointHost, "api.example.test")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://api.example.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-east-1")

	info := &RelayInfo{}
	info.InitChannelMeta(ctx)

	require.NotNil(t, info.ChannelMeta)
	assert.Equal(t, uint64(17), info.ChannelMeta.RoutingSnapshotRevision)
	assert.Equal(t, 7, info.ChannelMeta.RoutingPoolID)
	assert.Equal(t, 11, info.ChannelMeta.RoutingMemberID)
	assert.Equal(t, 13, info.ChannelMeta.RoutingCredentialID)
	assert.Equal(t, "api.example.test", info.ChannelMeta.RoutingEndpointHost)
	assert.Equal(t, "https://api.example.test:443", info.ChannelMeta.RoutingEndpointAuthority)
	assert.Equal(t, "us-east-1", info.ChannelMeta.RoutingRegion)
}

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoHTTPStreamCommitState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                string
		writeResponse       bool
		configure           func(*StreamStatus)
		wantCommitted       bool
		wantFailure         bool
		wantFailedPreCommit bool
	}{
		{
			name:          "written stream response is committed",
			writeResponse: true,
			configure: func(status *StreamStatus) {
				status.SetEndReason(StreamEndReasonScannerErr, errors.New("late corruption"))
			},
			wantCommitted: true,
			wantFailure:   true,
		},
		{
			name: "scanner failure before write remains uncommitted",
			configure: func(status *StreamStatus) {
				status.SetEndReason(StreamEndReasonScannerErr, errors.New("early corruption"))
			},
			wantFailure:         true,
			wantFailedPreCommit: true,
		},
		{
			name: "soft error before write remains uncommitted",
			configure: func(status *StreamStatus) {
				status.RecordError("malformed first chunk")
			},
			wantFailure:         true,
			wantFailedPreCommit: true,
		},
		{
			name: "client disconnect is not provider pre-commit failure",
			configure: func(status *StreamStatus) {
				status.SetEndReason(StreamEndReasonClientGone, errors.New("client closed"))
			},
			wantFailure: true,
		},
		{
			name: "normal completion is not a failure",
			configure: func(status *StreamStatus) {
				status.SetEndReason(StreamEndReasonDone, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tt.writeResponse {
				_, err := ctx.Writer.Write([]byte("data"))
				require.NoError(t, err)
			}
			status := NewStreamStatus()
			tt.configure(status)
			info := &RelayInfo{IsStream: true, StreamStatus: status}

			assert.Equal(t, tt.wantCommitted, info.HTTPStreamClientCommitted(ctx))
			assert.Equal(t, tt.wantFailure, info.HTTPStreamHasFailure())
			assert.Equal(t, tt.wantFailedPreCommit, info.HTTPStreamFailedBeforeCommit(ctx))
		})
	}
}
