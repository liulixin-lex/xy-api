package common

import (
	"sync"
	"time"
)

type InMemoryRateLimiter struct {
	store              map[string]*rateLimitWindow
	mutex              sync.Mutex
	initOnce           sync.Once
	expirationDuration time.Duration
}

type rateLimitWindow struct {
	buckets []rateLimitBucket
	total   int
}

type rateLimitBucket struct {
	timestamp int64
	count     int
}

func (l *InMemoryRateLimiter) Init(expirationDuration time.Duration) {
	l.initOnce.Do(func() {
		l.store = make(map[string]*rateLimitWindow)
		l.expirationDuration = expirationDuration
		if expirationDuration > 0 {
			go l.clearExpiredItems()
		}
	})
}

func (l *InMemoryRateLimiter) clearExpiredItems() {
	for {
		time.Sleep(l.expirationDuration)
		l.mutex.Lock()
		now := time.Now().Unix()
		for key := range l.store {
			window := l.store[key]
			size := len(window.buckets)
			if size == 0 || now-window.buckets[size-1].timestamp > int64(l.expirationDuration.Seconds()) {
				delete(l.store, key)
			}
		}
		l.mutex.Unlock()
	}
}

// Request parameter duration's unit is seconds
func (l *InMemoryRateLimiter) Request(key string, maxRequestNum int, duration int64) bool {
	// Invalid enabled limits fail closed. Besides avoiding undefined limiter
	// behaviour, this prevents negative capacities and zero-length windows from
	// reaching the storage path when a deployment environment or persisted
	// setting is malformed.
	if maxRequestNum <= 0 || duration <= 0 {
		return false
	}

	// All production callers initialize the shared limiter during middleware
	// construction. Keep Request safe for direct and concurrently-starting
	// callers as well, without racing on the map initialization.
	l.Init(RateLimitKeyExpirationDuration)

	l.mutex.Lock()
	defer l.mutex.Unlock()

	now := time.Now().Unix()

	window, ok := l.store[key]
	if !ok {
		l.store[key] = &rateLimitWindow{
			buckets: []rateLimitBucket{{timestamp: now, count: 1}},
			total:   1,
		}
		return true
	}

	// Timestamps are recorded with one-second precision, so requests sharing a
	// second can be represented by one counted bucket. This preserves the
	// intended sliding-window behaviour while making storage proportional to
	// elapsed seconds instead of a configuration-controlled request limit.
	firstActive := 0
	for firstActive < len(window.buckets) && now-window.buckets[firstActive].timestamp >= duration {
		window.total -= window.buckets[firstActive].count
		firstActive++
	}
	if firstActive == len(window.buckets) {
		window.buckets = nil
	} else if firstActive > 0 {
		window.buckets = window.buckets[firstActive:]
	}

	if window.total >= maxRequestNum {
		return false
	}

	last := len(window.buckets) - 1
	if last >= 0 && window.buckets[last].timestamp == now {
		window.buckets[last].count++
	} else {
		window.buckets = append(window.buckets, rateLimitBucket{timestamp: now, count: 1})
	}
	window.total++
	return true
}
