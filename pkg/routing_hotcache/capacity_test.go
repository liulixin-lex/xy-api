package routinghotcache

import (
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordCapacityCooldownCapsKeepsLaterDeadlineAndExpiresOnPrune(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	key := Key{ChannelID: 11, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	now := time.UnixMilli(100_000)

	_, recorded := RecordCapacityCooldown(key, http.StatusTooManyRequests, 50*time.Second, time.Second, 5*time.Second, now)
	require.True(t, recorded)
	_, recorded = RecordCapacityCooldown(key, http.StatusTooManyRequests, time.Second, time.Second, 5*time.Second, now.Add(time.Second))
	require.True(t, recorded)

	snapshot, ok := GetCapacityCooldown(key)
	require.True(t, ok)
	assert.Equal(t, int64(105_000), snapshot.CooldownUntilUnixMilli)
	assert.Equal(t, int64(100_000), snapshot.UpdatedUnixMilli)
	assert.True(t, CapacityCooldownActive(key, time.UnixMilli(104_999)))
	assert.False(t, CapacityCooldownActive(key, time.UnixMilli(105_000)))
	assert.Zero(t, Prune(104, 1))
	_, ok = GetCapacityCooldown(key)
	assert.True(t, ok)
	assert.Equal(t, 1, Prune(105, 0))
	_, ok = GetCapacityCooldown(key)
	assert.False(t, ok)
}

func TestRecordCapacityCooldownUsesCapacityStatusOrValidRetryAfter(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}
	now := time.UnixMilli(100_000)
	for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests, 529} {
		ClearCapacityCooldown(key)
		_, recorded := RecordCapacityCooldown(key, status, 0, 1500*time.Millisecond, 5*time.Second, now)
		assert.True(t, recorded)
		snapshot, ok := GetCapacityCooldown(key)
		require.True(t, ok)
		assert.Equal(t, int64(101_500), snapshot.CooldownUntilUnixMilli)
	}

	ClearCapacityCooldown(key)
	_, recorded := RecordCapacityCooldown(key, http.StatusServiceUnavailable, 2*time.Second, time.Second, 5*time.Second, now)
	assert.True(t, recorded)
	ClearCapacityCooldown(key)
	_, recorded = RecordCapacityCooldown(key, http.StatusBadRequest, 0, time.Second, 5*time.Second, now)
	assert.False(t, recorded)
}

func TestCapacityCooldownRespectsHardLimitStatsAndStableEviction(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 1, MaxCosts: 1, MaxBreakers: 1, MaxHealth: 1, MaxCapacityCooldowns: 2}
	cache.Unlock()

	for _, channelID := range []int{3, 1, 2} {
		SetCapacityCooldownForTest(Key{ChannelID: channelID, APIKeyIndex: -1, Model: "same", Group: "default"}, CapacityCooldownSnapshot{
			CooldownUntilUnixMilli: 200_000,
			UpdatedUnixMilli:       100_000,
		})
	}

	assert.Equal(t, 2, RuntimeStats().CapacityCooldowns)
	_, one := GetCapacityCooldown(Key{ChannelID: 1, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, two := GetCapacityCooldown(Key{ChannelID: 2, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, three := GetCapacityCooldown(Key{ChannelID: 3, APIKeyIndex: -1, Model: "same", Group: "default"})
	assert.False(t, one)
	assert.True(t, two)
	assert.True(t, three)
}

func TestClearChannelAndResetClearCapacityCooldowns(t *testing.T) {
	ResetForTest()
	first := Key{ChannelID: 1, APIKeyIndex: -1, Model: "a", Group: "default"}
	second := Key{ChannelID: 2, APIKeyIndex: -1, Model: "b", Group: "default"}
	SetCapacityCooldownForTest(first, CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000})
	SetCapacityCooldownForTest(second, CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000})

	ClearChannel(1)
	_, ok := GetCapacityCooldown(first)
	assert.False(t, ok)
	_, ok = GetCapacityCooldown(second)
	assert.True(t, ok)

	ResetForTest()
	assert.Equal(t, Stats{}, RuntimeStats())
}
