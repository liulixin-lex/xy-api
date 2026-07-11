package common

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
