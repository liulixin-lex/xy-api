package routinghotcache

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelBalanceUnavailableUsesFencedRecovery(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	now := time.UnixMilli(100_000)

	snapshot, recorded := RecordChannelBalanceUnavailable(
		31,
		http.StatusPaymentRequired,
		ChannelBalanceUnavailableReason,
		0,
		5*time.Second,
		30*time.Second,
		now,
	)
	require.True(t, recorded)
	assert.Equal(t, int64(105_000), snapshot.CooldownUntilUnixMilli)
	assert.True(t, ChannelBalanceUnavailableActive(31, now.Add(4*time.Second)))

	assert.False(t, ClearChannelBalanceUnavailable(31, now.Add(-time.Millisecond)))
	assert.True(t, ChannelBalanceUnavailableActive(31, now.Add(4*time.Second)))
	assert.True(t, ClearChannelBalanceUnavailable(31, now))
	_, found := GetChannelBalanceUnavailable(31)
	assert.False(t, found)
}

func TestChannelBalanceUnavailableExpiresAndPrunesIndependently(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	now := time.UnixMilli(200_000)

	_, recorded := RecordChannelBalanceUnavailable(
		41,
		http.StatusPaymentRequired,
		"provider reported depleted balance",
		2*time.Second,
		time.Second,
		10*time.Second,
		now,
	)
	require.True(t, recorded)
	assert.False(t, ChannelBalanceUnavailableActive(41, now.Add(2*time.Second)))
	assert.Equal(t, 1, Prune(202, 0))
}

func TestChannelBalanceUnavailableCoversChannelLifecycleAndLimit(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 1, MaxBreakers: 1, MaxHealth: 2, MaxCapacityCooldowns: 1}
	cache.Unlock()

	for _, channelID := range []int{3, 1, 2} {
		SetChannelBalanceUnavailableForTest(channelID, ChannelBalanceUnavailableSnapshot{
			SourceStatusCode:       http.StatusPaymentRequired,
			Reason:                 ChannelBalanceUnavailableReason,
			CooldownUntilUnixMilli: 200_000,
			UpdatedUnixMilli:       100_000,
		})
	}
	assert.Equal(t, 2, RuntimeStats().ChannelBalanceUnavailable)
	_, oldestTie := GetChannelBalanceUnavailable(1)
	assert.False(t, oldestTie)

	ClearChannel(2)
	_, found := GetChannelBalanceUnavailable(2)
	assert.False(t, found)
	_, found = GetChannelBalanceUnavailable(3)
	assert.True(t, found)

	ResetForTest()
	assert.Equal(t, Stats{}, RuntimeStats())
}
