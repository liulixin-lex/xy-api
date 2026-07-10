package routinghotcache

import (
	"net/http"
	"time"
)

type CapacityCooldownSnapshot struct {
	SourceStatusCode       int
	Reason                 string
	RetryAfterMs           int64
	CooldownUntilUnixMilli int64
	UpdatedUnixMilli       int64
}

func RecordCapacityCooldown(
	key Key,
	sourceStatusCode int,
	retryAfter time.Duration,
	baseCooldown time.Duration,
	maxCooldown time.Duration,
	now time.Time,
) (CapacityCooldownSnapshot, bool) {
	capacityStatus := sourceStatusCode == http.StatusPaymentRequired ||
		sourceStatusCode == http.StatusTooManyRequests || sourceStatusCode == 529
	if !capacityStatus && retryAfter <= 0 {
		return CapacityCooldownSnapshot{}, false
	}
	cooldown := retryAfter
	if cooldown <= 0 {
		cooldown = baseCooldown
	}
	if cooldown <= 0 || maxCooldown <= 0 {
		return CapacityCooldownSnapshot{}, false
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}
	retryAfterMs := retryAfter.Milliseconds()
	if retryAfterMs < 0 {
		retryAfterMs = 0
	}
	snapshot := CapacityCooldownSnapshot{
		SourceStatusCode:       sourceStatusCode,
		Reason:                 "capacity_cooldown",
		RetryAfterMs:           retryAfterMs,
		CooldownUntilUnixMilli: now.Add(cooldown).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	}
	return setCapacityCooldown(key, snapshot)
}

func GetCapacityCooldown(key Key) (CapacityCooldownSnapshot, bool) {
	cache.RLock()
	defer cache.RUnlock()
	snapshot, ok := cache.capacityCooldowns[key]
	return snapshot, ok
}

func CapacityCooldownActive(key Key, now time.Time) bool {
	snapshot, ok := GetCapacityCooldown(key)
	return ok && snapshot.CooldownUntilUnixMilli > now.UnixMilli()
}

func ClearCapacityCooldown(key Key) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.capacityCooldowns, key)
}

func SetCapacityCooldownForTest(key Key, snapshot CapacityCooldownSnapshot) {
	_, _ = setCapacityCooldown(key, snapshot)
}

func setCapacityCooldown(key Key, snapshot CapacityCooldownSnapshot) (CapacityCooldownSnapshot, bool) {
	if key.ChannelID <= 0 || key.Model == "" || key.Group == "" || snapshot.CooldownUntilUnixMilli <= 0 {
		return CapacityCooldownSnapshot{}, false
	}
	cache.Lock()
	defer cache.Unlock()
	if current, ok := cache.capacityCooldowns[key]; ok && current.CooldownUntilUnixMilli >= snapshot.CooldownUntilUnixMilli {
		return current, true
	}
	cache.capacityCooldowns[key] = snapshot
	cache.limits = normalizedLimits(cache.limits)
	cache.evictions += int64(trimBoundedMap(cache.capacityCooldowns, cache.limits.MaxCapacityCooldowns, capacityUpdatedUnixMilli, keyLess))
	return snapshot, true
}
