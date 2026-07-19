package common

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryRateLimiterRejectsInvalidConfiguration(t *testing.T) {
	limiter := InMemoryRateLimiter{}

	assert.False(t, limiter.Request("zero-limit", 0, 60))
	assert.False(t, limiter.Request("negative-limit", -1, 60))
	assert.False(t, limiter.Request("zero-duration", 1, 0))
	assert.False(t, limiter.Request("negative-duration", 1, -1))
	assert.Nil(t, limiter.store, "invalid configuration must not initialize or allocate limiter storage")
}

func TestInMemoryRateLimiterDoesNotAllocateFromConfiguredLimit(t *testing.T) {
	limiter := InMemoryRateLimiter{}
	limiter.Init(0)

	require.True(t, limiter.Request("large-limit", math.MaxInt, 60))

	window := limiter.store["large-limit"]
	require.NotNil(t, window)
	assert.Equal(t, 1, window.total)
	require.Len(t, window.buckets, 1)
	assert.Equal(t, 1, window.buckets[0].count)
}

func TestInMemoryRateLimiterPreservesSlidingWindowLimit(t *testing.T) {
	limiter := InMemoryRateLimiter{}
	limiter.Init(0)

	require.True(t, limiter.Request("user", 2, 60))
	require.True(t, limiter.Request("user", 2, 60))
	assert.False(t, limiter.Request("user", 2, 60))

	window := limiter.store["user"]
	require.NotNil(t, window)
	for index := range window.buckets {
		window.buckets[index].timestamp = time.Now().Unix() - 60
	}

	assert.True(t, limiter.Request("user", 2, 60))
	assert.Equal(t, 1, window.total)
	require.Len(t, window.buckets, 1)
	assert.Equal(t, 1, window.buckets[0].count)
}

func TestInMemoryRateLimiterConcurrentLazyInitialization(t *testing.T) {
	const requestCount = 8
	limiter := InMemoryRateLimiter{}
	start := make(chan struct{})
	results := make(chan bool, requestCount)
	var waitGroup sync.WaitGroup

	for range requestCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			results <- limiter.Request("shared-user", requestCount, 60)
		}()
	}

	close(start)
	waitGroup.Wait()
	close(results)
	for allowed := range results {
		assert.True(t, allowed)
	}

	window := limiter.store["shared-user"]
	require.NotNil(t, window)
	assert.Equal(t, requestCount, window.total)
}
