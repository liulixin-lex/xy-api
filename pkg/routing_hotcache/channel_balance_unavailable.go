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
	return RecordChannelBalanceUnavailableForGeneration(
		channelID, "", sourceStatusCode, reason, retryAfter, baseCooldown, maxCooldown, now,
	)
}

func RecordChannelBalanceUnavailableForGeneration(
	channelID int,
	channelGeneration string,
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
	return setChannelBalanceUnavailableForGeneration(channelID, channelGeneration, snapshot)
}

func GetChannelBalanceUnavailable(channelID int) (ChannelBalanceUnavailableSnapshot, bool) {
	return GetChannelBalanceUnavailableForGeneration(channelID, "")
}

func GetChannelBalanceUnavailableForGeneration(
	channelID int,
	channelGeneration string,
) (ChannelBalanceUnavailableSnapshot, bool) {
	if channelID <= 0 {
		return ChannelBalanceUnavailableSnapshot{}, false
	}
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.channelBalanceUnavailable[ChannelLifecycleKey{
		ChannelID: channelID, ChannelGeneration: channelGeneration,
	}]
	return snapshot, ok
}

func ChannelBalanceUnavailableActive(channelID int, now time.Time) bool {
	return ChannelBalanceUnavailableActiveForGeneration(channelID, "", now)
}

func ChannelBalanceUnavailableActiveForGeneration(channelID int, channelGeneration string, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	snapshot, ok := GetChannelBalanceUnavailableForGeneration(channelID, channelGeneration)
	return ok && snapshot.CooldownUntilUnixMilli > now.UnixMilli()
}

// ClearChannelBalanceUnavailable accepts only evidence from work that began
// at or after the current marker. This prevents an older concurrent success
// from erasing a newer 402 observation.
func ClearChannelBalanceUnavailable(channelID int, evidenceStartedAt time.Time) bool {
	return ClearChannelBalanceUnavailableForGeneration(channelID, "", evidenceStartedAt)
}

func ClearChannelBalanceUnavailableForGeneration(
	channelID int,
	channelGeneration string,
	evidenceStartedAt time.Time,
) bool {
	if channelID <= 0 || evidenceStartedAt.IsZero() {
		return false
	}
	cache.Lock()
	defer cache.Unlock()
	lifecycle := ChannelLifecycleKey{ChannelID: channelID, ChannelGeneration: channelGeneration}
	snapshot, ok := cache.channelBalanceUnavailable[lifecycle]
	if !ok || evidenceStartedAt.UnixMilli() < snapshot.UpdatedUnixMilli {
		return false
	}
	delete(cache.channelBalanceUnavailable, lifecycle)
	return true
}

func SetChannelBalanceUnavailableForTest(channelID int, snapshot ChannelBalanceUnavailableSnapshot) {
	_, _ = setChannelBalanceUnavailable(channelID, snapshot)
}

func SetChannelBalanceUnavailableForGenerationForTest(
	channelID int,
	channelGeneration string,
	snapshot ChannelBalanceUnavailableSnapshot,
) {
	_, _ = setChannelBalanceUnavailableForGeneration(channelID, channelGeneration, snapshot)
}

func setChannelBalanceUnavailable(
	channelID int,
	snapshot ChannelBalanceUnavailableSnapshot,
) (ChannelBalanceUnavailableSnapshot, bool) {
	return setChannelBalanceUnavailableForGeneration(channelID, "", snapshot)
}

func setChannelBalanceUnavailableForGeneration(
	channelID int,
	channelGeneration string,
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
	lifecycle := ChannelLifecycleKey{ChannelID: channelID, ChannelGeneration: channelGeneration}
	if current, ok := cache.channelBalanceUnavailable[lifecycle]; ok {
		if current.UpdatedUnixMilli > snapshot.UpdatedUnixMilli {
			return current, true
		}
		if current.CooldownUntilUnixMilli > snapshot.CooldownUntilUnixMilli {
			snapshot.CooldownUntilUnixMilli = current.CooldownUntilUnixMilli
		}
	}
	cache.channelBalanceUnavailable[lifecycle] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(
		cache.channelBalanceUnavailable,
		cache.limits.MaxHealth,
		channelBalanceUnavailableUpdatedUnixMilli,
		channelLifecycleLess,
	))
	return snapshot, true
}
