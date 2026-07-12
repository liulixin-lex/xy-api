package controller

import (
	"context"
	"crypto/x509"
	"errors"
	"math"
	"net"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingProbeExecutionClassifiesOperationalFailures(t *testing.T) {
	tests := []struct {
		name           string
		apiErr         *types.NewAPIError
		statusCode     int
		responsibility routingerror.Responsibility
		scope          routingerror.Scope
		health         routingerror.HealthEffect
		capacity       routingerror.CapacityEffect
		local          bool
	}{
		{
			name: "dns", apiErr: types.NewError(&net.DNSError{Err: "no such host", Name: "missing.example"}, types.ErrorCodeDoRequestFailed),
			responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone,
		},
		{
			name: "tls", apiErr: types.NewError(x509.UnknownAuthorityError{Cert: &x509.Certificate{}}, types.ErrorCodeDoRequestFailed),
			responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone,
		},
		{
			name: "timeout", apiErr: types.NewError(context.DeadlineExceeded, types.ErrorCodeDoRequestFailed),
			responsibility: routingerror.ResponsibilityNetwork, scope: routingerror.ScopeEndpoint, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone,
		},
		{
			name: "credential", apiErr: types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponse, http.StatusUnauthorized), statusCode: http.StatusUnauthorized,
			responsibility: routingerror.ResponsibilityCredential, scope: routingerror.ScopeCredential, health: routingerror.HealthOpen, capacity: routingerror.CapacityNone,
		},
		{
			name: "payment", apiErr: types.NewErrorWithStatusCode(errors.New("payment required"), types.ErrorCodeBadResponse, http.StatusPaymentRequired), statusCode: http.StatusPaymentRequired,
			responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown,
		},
		{
			name: "rate limit", apiErr: types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponse, http.StatusTooManyRequests), statusCode: http.StatusTooManyRequests,
			responsibility: routingerror.ResponsibilityCapacity, scope: routingerror.ScopePoolMember, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown,
		},
		{
			name: "provider", apiErr: types.NewErrorWithStatusCode(errors.New("unavailable"), types.ErrorCodeBadResponse, http.StatusServiceUnavailable), statusCode: http.StatusServiceUnavailable,
			responsibility: routingerror.ResponsibilityProvider, scope: routingerror.ScopePoolMember, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone,
		},
		{
			name: "local setup", apiErr: types.NewError(errors.New("missing channel"), types.ErrorCodeGetChannelFailed),
			responsibility: routingerror.ResponsibilityGateway, scope: routingerror.ScopeRequest, health: routingerror.HealthIgnore, capacity: routingerror.CapacityNone, local: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := channelRoutingProbeExecution(test.apiErr, test.statusCode, nil, 0, 0, false)
			assert.Equal(t, test.responsibility, result.Classification.Responsibility)
			assert.Equal(t, test.scope, result.Classification.Scope)
			assert.Equal(t, test.health, result.Classification.HealthEffect)
			assert.Equal(t, test.capacity, result.Classification.CapacityEffect)
			assert.Equal(t, test.local, result.LocalError)
			require.Error(t, result.Err)
		})
	}
}

func TestChannelRoutingProbeExecutionReportsUsageWithoutProductionSettlement(t *testing.T) {
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	result := channelRoutingProbeExecution(nil, http.StatusOK, &dto.Usage{PromptTokens: 3, CompletionTokens: 5}, 500_000, 0, false)
	assert.NoError(t, result.Err)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Equal(t, int64(3), result.PromptTokens)
	assert.Equal(t, int64(5), result.CompletionTokens)
	assert.Equal(t, int64(1_000_000_000), result.CostNanoUSD)
	assert.False(t, result.LocalError)
	assert.Equal(t, routingerror.HealthIgnore, result.Classification.HealthEffect)

	retryAfter := channelRoutingProbeExecution(
		types.NewErrorWithStatusCode(errors.New("temporarily unavailable"), types.ErrorCodeBadResponse, http.StatusServiceUnavailable),
		http.StatusServiceUnavailable,
		nil,
		0,
		2_500,
		false,
	)
	assert.Equal(t, int64(2_500), retryAfter.RetryAfterMs)
	assert.Equal(t, routingerror.CapacityCooldown, retryAfter.Classification.CapacityEffect)

	assert.Equal(t, int64(math.MaxInt64), channelTestQuotaNanoUSD(math.MaxInt))
}

func TestApplyChannelTestMaxOutputTokensKeepsProbeRequestBounded(t *testing.T) {
	general := &dto.GeneralOpenAIRequest{}
	applyChannelTestMaxOutputTokens(general, 16)
	require.NotNil(t, general.MaxTokens)
	assert.Equal(t, uint(16), *general.MaxTokens)

	general.MaxCompletionTokens = common.GetPointer(uint(128))
	applyChannelTestMaxOutputTokens(general, 8)
	require.NotNil(t, general.MaxCompletionTokens)
	assert.Equal(t, uint(8), *general.MaxCompletionTokens)

	responses := &dto.OpenAIResponsesRequest{}
	applyChannelTestMaxOutputTokens(responses, 12)
	require.NotNil(t, responses.MaxOutputTokens)
	assert.Equal(t, uint(12), *responses.MaxOutputTokens)
}

func TestChannelRoutingActiveProbeRejectsStaleTargetBeforeDatabaseLookup(t *testing.T) {
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(channelrouting.ResetSnapshotForTest)

	result := executeChannelRoutingActiveProbe(context.Background(), channelrouting.ActiveProbeTarget{
		SnapshotRevision: 1, PoolID: 1, MemberID: 1, ChannelID: 1,
		GroupName: "default", ModelName: "gpt-test",
	})

	require.Error(t, result.Err)
	assert.True(t, result.LocalError)
	assert.Equal(t, routingerror.ResponsibilityGateway, result.Classification.Responsibility)
}

func TestActiveProbeChannelCopyDoesNotAdvanceProductionMultiKeyState(t *testing.T) {
	original := &model.Channel{
		Key: "disabled-key\nenabled-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true, MultiKeySize: 2, MultiKeyPollingIndex: 1, MultiKeyMode: constant.MultiKeyModePolling,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled, 1: common.ChannelStatusEnabled},
		},
	}

	probeChannel, err := activeProbeChannelCopy(original)
	require.NoError(t, err)
	require.NotSame(t, original, probeChannel)
	assert.Equal(t, "enabled-key", probeChannel.Key)
	assert.True(t, probeChannel.ChannelInfo.IsMultiKey)
	assert.Equal(t, constant.MultiKeyModeRandom, probeChannel.ChannelInfo.MultiKeyMode)
	assert.Equal(t, 1, original.ChannelInfo.MultiKeyPollingIndex)
	assert.Equal(t, constant.MultiKeyModePolling, original.ChannelInfo.MultiKeyMode)

	key, index, apiErr := probeChannel.GetNextEnabledKey()
	require.Nil(t, apiErr)
	assert.Equal(t, "enabled-key", key)
	assert.Zero(t, index)
	assert.Equal(t, 1, original.ChannelInfo.MultiKeyPollingIndex)
}
