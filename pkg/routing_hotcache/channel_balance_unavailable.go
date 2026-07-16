package routinghotcache

import (
	"net/http"
	"strings"
	"time"
)

const ChannelBalanceUnavailableReason = "channel_balance_unavailable"

type ChannelBalanceUnavailableSnapshot struct {
	SourceStatusCode       int
	Reason                 string
	CooldownUntilUnixMilli int64
	UpdatedUnixMilli       int64
}

func RecordChannelBalanceUnavailable(
	channelID int,
	sourceStatusCode int,
	reason string,
	retryAfter time.Duration,
	baseCooldown time.Duration,
	maxCooldown time.Duration,
	now time.Time,
) (ChannelBalanceUnavailableSnapshot, bool) {
	if channelID <= 0 || sourceStatusCode != http.StatusPaymentRequired {
		return ChannelBalanceUnavailableSnapshot{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	cooldown := retryAfter
	if cooldown <= 0 {
		cooldown = baseCooldown
	}
	if cooldown <= 0 || maxCooldown <= 0 {
		return ChannelBalanceUnavailableSnapshot{}, false
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = ChannelBalanceUnavailableReason
	}
	snapshot := ChannelBalanceUnavailableSnapshot{
		SourceStatusCode:       sourceStatusCode,
		Reason:                 reason,
		CooldownUntilUnixMilli: now.Add(cooldown).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	}
	return setChannelBalanceUnavailable(channelID, snapshot)
}

func GetChannelBalanceUnavailable(channelID int) (ChannelBalanceUnavailableSnapshot, bool) {
	if channelID <= 0 {
		return ChannelBalanceUnavailableSnapshot{}, false
	}
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.channelBalanceUnavailable[channelID]
	return snapshot, ok
}

func ChannelBalanceUnavailableActive(channelID int, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	snapshot, ok := GetChannelBalanceUnavailable(channelID)
	return ok && snapshot.CooldownUntilUnixMilli > now.UnixMilli()
}

// ClearChannelBalanceUnavailable accepts only evidence from work that began
// at or after the current marker. This prevents an older concurrent success
// from erasing a newer 402 observation.
func ClearChannelBalanceUnavailable(channelID int, evidenceStartedAt time.Time) bool {
	if channelID <= 0 || evidenceStartedAt.IsZero() {
		return false
	}
	cache.Lock()
	defer cache.Unlock()
	snapshot, ok := cache.channelBalanceUnavailable[channelID]
	if !ok || evidenceStartedAt.UnixMilli() < snapshot.UpdatedUnixMilli {
		return false
	}
	delete(cache.channelBalanceUnavailable, channelID)
	return true
}

func SetChannelBalanceUnavailableForTest(channelID int, snapshot ChannelBalanceUnavailableSnapshot) {
	_, _ = setChannelBalanceUnavailable(channelID, snapshot)
}

func setChannelBalanceUnavailable(
	channelID int,
	snapshot ChannelBalanceUnavailableSnapshot,
) (ChannelBalanceUnavailableSnapshot, bool) {
	if channelID <= 0 || snapshot.SourceStatusCode != http.StatusPaymentRequired ||
		snapshot.CooldownUntilUnixMilli <= snapshot.UpdatedUnixMilli || snapshot.UpdatedUnixMilli <= 0 {
		return ChannelBalanceUnavailableSnapshot{}, false
	}
	if strings.TrimSpace(snapshot.Reason) == "" {
		snapshot.Reason = ChannelBalanceUnavailableReason
	}
	cache.Lock()
	defer cache.Unlock()
	if current, ok := cache.channelBalanceUnavailable[channelID]; ok {
		if current.UpdatedUnixMilli > snapshot.UpdatedUnixMilli {
			return current, true
		}
		if current.CooldownUntilUnixMilli > snapshot.CooldownUntilUnixMilli {
			snapshot.CooldownUntilUnixMilli = current.CooldownUntilUnixMilli
		}
	}
	cache.channelBalanceUnavailable[channelID] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(
		cache.channelBalanceUnavailable,
		cache.limits.MaxHealth,
		channelBalanceUnavailableUpdatedUnixMilli,
		intLess,
	))
	return snapshot, true
}
