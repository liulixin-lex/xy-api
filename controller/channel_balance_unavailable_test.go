package controller

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelBalanceRefreshClearsOnlyOnPositiveSupportedResult(t *testing.T) {
	tests := []struct {
		name          string
		multiKey      bool
		balance       float64
		refreshErr    error
		wantRefreshes int
		wantBlocked   bool
	}{
		{name: "positive balance recovers", balance: 12.5, wantRefreshes: 1},
		{name: "zero balance stays blocked", balance: 0, wantRefreshes: 1, wantBlocked: true},
		{name: "refresh failure stays blocked", refreshErr: errors.New("upstream unavailable"), wantRefreshes: 1, wantBlocked: true},
		{name: "multi key skips unsupported query", multiKey: true, wantBlocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routinghotcache.ResetForTest()
			resetChannelBalanceRefreshForTest()
			t.Cleanup(func() {
				routinghotcache.ResetForTest()
				resetChannelBalanceRefreshForTest()
			})

			channel := &model.Channel{
				Id: 91, Balance: 99,
				ChannelInfo: model.ChannelInfo{IsMultiKey: tt.multiKey},
			}
			loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
				assert.Equal(t, 91, channelID)
				return channel, nil
			}
			refreshes := 0
			updateChannelBalanceForRefresh = func(got *model.Channel) (float64, error) {
				refreshes++
				assert.Same(t, channel, got)
				return tt.balance, tt.refreshErr
			}
			runChannelBalanceRefresh = func(refresh func()) { refresh() }

			now := time.Now().Add(-time.Second)
			recordChannelBalanceAttemptEffect(
				91,
				false,
				http.StatusPaymentRequired,
				types.NewErrorWithStatusCode(errors.New("payment required"), types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired),
				routingerror.Classification{
					Responsibility: routingerror.ResponsibilityCapacity,
					Scope:          routingerror.ScopeChannel,
					CapacityEffect: routingerror.CapacityCooldown,
					Rule:           routinghotcache.ChannelBalanceUnavailableReason,
				},
				time.Time{},
				now,
			)

			assert.Equal(t, tt.wantRefreshes, refreshes)
			assert.Equal(t, 99.0, channel.Balance, "the 402 marker itself must not fabricate a balance")
			_, blocked := routinghotcache.GetChannelBalanceUnavailable(91)
			assert.Equal(t, tt.wantBlocked, blocked)
		})
	}
}

func TestChannelBalanceRefreshDeduplicatesAndUsesNewEvidenceFence(t *testing.T) {
	routinghotcache.ResetForTest()
	resetChannelBalanceRefreshForTest()
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		resetChannelBalanceRefreshForTest()
	})

	var queued func()
	runs := 0
	runChannelBalanceRefresh = func(refresh func()) {
		runs++
		queued = refresh
	}
	loadChannelForBalanceRefresh = func(channelID int) (*model.Channel, error) {
		return &model.Channel{Id: channelID}, nil
	}
	updateChannelBalanceForRefresh = func(*model.Channel) (float64, error) { return 5, nil }
	classification := routingerror.Classification{
		Responsibility: routingerror.ResponsibilityCapacity,
		Scope:          routingerror.ScopeChannel,
		CapacityEffect: routingerror.CapacityCooldown,
		Rule:           routinghotcache.ChannelBalanceUnavailableReason,
	}
	apiErr := types.NewErrorWithStatusCode(
		errors.New("payment required"), types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired,
	)

	recordChannelBalanceAttemptEffect(92, false, http.StatusPaymentRequired, apiErr, classification, time.Time{}, time.Now().Add(-2*time.Second))
	recordChannelBalanceAttemptEffect(92, false, http.StatusPaymentRequired, apiErr, classification, time.Time{}, time.Now().Add(-time.Second))
	assert.Equal(t, 1, runs)
	require.NotNil(t, queued)
	queued()
	_, blocked := routinghotcache.GetChannelBalanceUnavailable(92)
	assert.False(t, blocked)
}

func TestSuccessfulAttemptCannotClearNewerChannelBalanceSignal(t *testing.T) {
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	now := time.Now()
	routinghotcache.SetChannelBalanceUnavailableForTest(93, routinghotcache.ChannelBalanceUnavailableSnapshot{
		SourceStatusCode:       http.StatusPaymentRequired,
		Reason:                 routinghotcache.ChannelBalanceUnavailableReason,
		CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	})

	recordChannelBalanceAttemptEffect(93, true, http.StatusOK, nil, routingerror.Classification{}, now.Add(-time.Millisecond), now)
	_, blocked := routinghotcache.GetChannelBalanceUnavailable(93)
	assert.True(t, blocked)

	recordChannelBalanceAttemptEffect(93, true, http.StatusOK, nil, routingerror.Classification{}, now, now.Add(time.Millisecond))
	_, blocked = routinghotcache.GetChannelBalanceUnavailable(93)
	assert.False(t, blocked)
}
