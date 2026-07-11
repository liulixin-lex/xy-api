package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFullJitterHandlesNonPositiveMaximum(t *testing.T) {
	assert.Equal(t, time.Duration(0), FullJitter(0))
	assert.Equal(t, time.Duration(0), FullJitter(-time.Second))
}

func TestFullJitterReturnsValueWithinMaximum(t *testing.T) {
	const maximum = 25 * time.Millisecond

	got := FullJitter(maximum)

	assert.GreaterOrEqual(t, got, time.Duration(0))
	assert.LessOrEqual(t, got, maximum)
}

func TestCappedExponentialBackoffRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		base     time.Duration
	}{
		{name: "zero failures", failures: 0, base: time.Second},
		{name: "negative failures", failures: -1, base: time.Second},
		{name: "zero base", failures: 1, base: 0},
		{name: "negative base", failures: 1, base: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CappedExponentialBackoff(tt.failures, tt.base, time.Minute, func(max time.Duration) time.Duration {
				return max
			})

			assert.Equal(t, time.Duration(0), got)
		})
	}
}

func TestCappedExponentialBackoffComputesBoundedCeiling(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	maxFailures := int(^uint(0) >> 1)
	tests := []struct {
		name     string
		failures int
		base     time.Duration
		cap      time.Duration
		want     time.Duration
	}{
		{name: "first failure", failures: 1, base: 100 * time.Millisecond, want: 100 * time.Millisecond},
		{name: "second failure", failures: 2, base: 100 * time.Millisecond, want: 200 * time.Millisecond},
		{name: "third failure", failures: 3, base: 100 * time.Millisecond, want: 400 * time.Millisecond},
		{name: "cap below base", failures: 1, base: 100 * time.Millisecond, cap: 50 * time.Millisecond, want: 50 * time.Millisecond},
		{name: "cap between powers", failures: 3, base: 100 * time.Millisecond, cap: 250 * time.Millisecond, want: 250 * time.Millisecond},
		{name: "negative cap is unbounded", failures: 2, base: 100 * time.Millisecond, cap: -time.Second, want: 200 * time.Millisecond},
		{name: "doubling saturates before overflow", failures: 2, base: maxDuration/2 + 1, want: maxDuration},
		{name: "huge failure count remains bounded", failures: maxFailures, base: time.Nanosecond, want: maxDuration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CappedExponentialBackoff(tt.failures, tt.base, tt.cap, func(max time.Duration) time.Duration {
				return max
			})

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCappedExponentialBackoffClampsInjectedJitter(t *testing.T) {
	const ceiling = 400 * time.Millisecond
	tests := []struct {
		name   string
		jitter JitterFunc
		want   time.Duration
	}{
		{
			name: "negative jitter",
			jitter: func(time.Duration) time.Duration {
				return -time.Second
			},
			want: 0,
		},
		{
			name: "oversized jitter",
			jitter: func(time.Duration) time.Duration {
				return time.Second
			},
			want: ceiling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CappedExponentialBackoff(3, 100*time.Millisecond, 0, tt.jitter)

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCappedExponentialBackoffUsesFullJitterByDefault(t *testing.T) {
	const ceiling = 400 * time.Millisecond

	got := CappedExponentialBackoff(3, 100*time.Millisecond, 0, nil)

	assert.GreaterOrEqual(t, got, time.Duration(0))
	assert.LessOrEqual(t, got, ceiling)
}
