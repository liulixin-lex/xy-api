package channelrouting

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttemptCoordinatorEnforcesSerialCommitBoundaryAndHedgingOff(t *testing.T) {
	now := time.Unix(1_000, 0)
	coordinator := NewAttemptCoordinator(AttemptPolicy{
		MaxAttempts:          3,
		Deadline:             now.Add(time.Minute),
		ExtraCostBudgetUnits: 20,
		RetryTokenCapacity:   10,
		RetryTokenRefill:     1,
		RetryTokens:          NewRetryTokenBudget(8, time.Minute),
		Now:                  func() time.Time { return now },
	})

	first, err := coordinator.BeginAttempt(AttemptInput{PoolID: 7, EstimatedCostUnits: 10})
	require.NoError(t, err)
	require.NoError(t, first.MarkSent())
	_, err = coordinator.BeginAttempt(AttemptInput{PoolID: 7, EstimatedCostUnits: 10})
	assert.ErrorIs(t, err, ErrAttemptAlreadyInFlight)
	first.Finish()

	second, err := coordinator.BeginAttempt(AttemptInput{PoolID: 7, EstimatedCostUnits: 10})
	require.NoError(t, err)
	require.NoError(t, second.MarkSent())
	require.NoError(t, second.MarkClientCommitted())
	second.Finish()

	_, err = coordinator.BeginAttempt(AttemptInput{PoolID: 7, EstimatedCostUnits: 10})
	assert.ErrorIs(t, err, ErrAttemptClientCommitted)
	_, err = coordinator.BeginAttempt(AttemptInput{PoolID: 7, EstimatedCostUnits: 10, Hedge: true})
	assert.ErrorIs(t, err, ErrAttemptHedgingDisabled)

	snapshot := coordinator.Snapshot()
	assert.Equal(t, AttemptStateClientCommitted, snapshot.State)
	assert.Equal(t, 2, snapshot.AttemptsStarted)
	assert.Equal(t, int64(10), snapshot.RetryCostUsedUnits)
	assert.True(t, snapshot.ClientCommitted)
	assert.False(t, snapshot.InFlight)
}

func TestAttemptCoordinatorEnforcesCountDeadlineCostAndPoolBudget(t *testing.T) {
	now := time.Unix(2_000, 0)
	tokens := NewRetryTokenBudget(8, time.Minute)
	coordinator := NewAttemptCoordinator(AttemptPolicy{
		MaxAttempts:          3,
		Deadline:             now.Add(time.Minute),
		ExtraCostBudgetUnits: 5,
		RetryTokenCapacity:   1,
		RetryTokenRefill:     0.1,
		RetryTokens:          tokens,
		Now:                  func() time.Time { return now },
	})

	first, err := coordinator.BeginAttempt(AttemptInput{EstimatedCostUnits: 5})
	require.NoError(t, err)
	first.Finish()

	_, err = coordinator.BeginAttempt(AttemptInput{EstimatedCostUnits: 5})
	assert.ErrorIs(t, err, ErrAttemptInvalidPool)
	_, err = coordinator.BeginAttempt(AttemptInput{PoolID: 1, EstimatedCostUnits: 6})
	assert.ErrorIs(t, err, ErrAttemptCostBudgetExceeded)

	second, err := coordinator.BeginAttempt(AttemptInput{PoolID: 1, EstimatedCostUnits: 5})
	require.NoError(t, err)
	second.Finish()
	_, err = coordinator.BeginAttempt(AttemptInput{PoolID: 1, EstimatedCostUnits: 1})
	assert.ErrorIs(t, err, ErrAttemptCostBudgetExceeded)

	deadlineCoordinator := NewAttemptCoordinator(AttemptPolicy{
		MaxAttempts:          2,
		Deadline:             now,
		ExtraCostBudgetUnits: 1,
		RetryTokenCapacity:   1,
		RetryTokenRefill:     1,
		RetryTokens:          tokens,
		Now:                  func() time.Time { return now },
	})
	_, err = deadlineCoordinator.BeginAttempt(AttemptInput{})
	assert.ErrorIs(t, err, ErrAttemptDeadlineExceeded)

	countCoordinator := NewAttemptCoordinator(AttemptPolicy{
		MaxAttempts:          1,
		Deadline:             now.Add(time.Minute),
		ExtraCostBudgetUnits: 1,
		RetryTokenCapacity:   1,
		RetryTokenRefill:     1,
		RetryTokens:          tokens,
		Now:                  func() time.Time { return now },
	})
	lease, err := countCoordinator.BeginAttempt(AttemptInput{})
	require.NoError(t, err)
	lease.Finish()
	_, err = countCoordinator.BeginAttempt(AttemptInput{PoolID: 1})
	assert.ErrorIs(t, err, ErrAttemptLimitExceeded)
}

func TestAttemptCoordinatorAllowsOnlyOneConcurrentAttempt(t *testing.T) {
	now := time.Unix(3_000, 0)
	coordinator := NewAttemptCoordinator(AttemptPolicy{
		MaxAttempts:          2,
		Deadline:             now.Add(time.Minute),
		ExtraCostBudgetUnits: 1,
		RetryTokenCapacity:   1,
		RetryTokenRefill:     1,
		RetryTokens:          NewRetryTokenBudget(8, time.Minute),
		Now:                  func() time.Time { return now },
	})

	first, err := coordinator.BeginAttempt(AttemptInput{})
	require.NoError(t, err)
	defer first.Finish()

	const contenders = 16
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	wait.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			<-start
			_, err := coordinator.BeginAttempt(AttemptInput{PoolID: 1})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	rejected := 0
	for err := range results {
		if assert.ErrorIs(t, err, ErrAttemptAlreadyInFlight) {
			rejected++
		}
	}
	assert.Equal(t, contenders, rejected)
	assert.Equal(t, 1, coordinator.Snapshot().AttemptsStarted)
}

func TestRetryTokenBudgetIsBoundedAndRefills(t *testing.T) {
	now := time.Unix(4_000, 0)
	budget := NewRetryTokenBudget(2, 10*time.Second)

	assert.True(t, budget.Allow(1, now, 2, 1))
	assert.True(t, budget.Allow(1, now, 2, 1))
	assert.False(t, budget.Allow(1, now, 2, 1))
	assert.True(t, budget.Allow(1, now.Add(time.Second), 2, 1))
	assert.True(t, budget.Allow(2, now.Add(time.Second), 2, 1))
	assert.True(t, budget.Allow(3, now.Add(time.Second), 2, 1))

	stats := budget.Stats()
	assert.Equal(t, 2, stats.Pools)
	assert.Equal(t, int64(5), stats.Allowed)
	assert.Equal(t, int64(1), stats.Denied)
	assert.Equal(t, int64(1), stats.Evictions)

	assert.True(t, budget.Allow(4, now.Add(20*time.Second), 2, 1))
	stats = budget.Stats()
	assert.Equal(t, 1, stats.Pools)
	assert.Equal(t, int64(3), stats.Evictions)
}

func TestAttemptDeadlineUsesEarlierRequestDeadline(t *testing.T) {
	now := time.Unix(5_000, 0)
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
	defer cancel()

	assert.Equal(t, now.Add(5*time.Second), AttemptDeadline(ctx, now, time.Minute))
	assert.Equal(t, now.Add(time.Minute), AttemptDeadline(context.Background(), now, time.Minute))
	assert.True(t, AttemptDeadline(context.Background(), now, 0).IsZero())
	assert.Equal(t, int64(20), AttemptExtraCostBudget(10, 2))
	assert.Equal(t, int64(1), AttemptExtraCostBudget(0, 1))
}
