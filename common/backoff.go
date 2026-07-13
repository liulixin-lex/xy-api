package common

import (
	"math/rand"
	"time"
)

// JitterFunc selects a delay within a backoff ceiling.
type JitterFunc func(time.Duration) time.Duration

// FullJitter returns a random delay between zero and max.
func FullJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}

	const maxDuration = time.Duration(1<<63 - 1)
	if max == maxDuration {
		return time.Duration(rand.Int63())
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}

// CappedExponentialBackoff returns a jittered exponential delay for failures.
func CappedExponentialBackoff(failures int, base, cap time.Duration, jitter JitterFunc) time.Duration {
	if failures <= 0 || base <= 0 {
		return 0
	}

	limit := cap
	if limit <= 0 {
		limit = time.Duration(1<<63 - 1)
	}

	ceiling := base
	if ceiling > limit {
		ceiling = limit
	}
	for remaining := failures - 1; remaining > 0 && ceiling < limit; remaining-- {
		if ceiling > limit/2 {
			ceiling = limit
			break
		}
		ceiling *= 2
	}

	if jitter == nil {
		jitter = FullJitter
	}
	delay := jitter(ceiling)
	if delay < 0 {
		return 0
	}
	if delay > ceiling {
		return ceiling
	}
	return delay
}
