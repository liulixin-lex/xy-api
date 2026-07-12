package channelrouting

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnterpriseHedgePolicyRequiresExplicitEnterpriseFailClosedConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		policy  string
		wantErr error
	}{
		{name: "implicit disabled", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{}`},
		{name: "explicit enabled", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"enabled":true,"delay_ms":250,"max_extra_cost_multiplier":1.5,
			"max_response_bytes":1048576,"scope":"distinct_endpoint_or_account","cross_region":false}}
		}`},
		{name: "non enterprise", profile: model.RoutingPolicyProfileBalanced, policy: `{
			"enterprise":{"hedge":{"enabled":true}}
		}`, wantErr: ErrHedgePolicyInvalid},
		{name: "missing enabled", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"delay_ms":250}}
		}`, wantErr: ErrHedgePolicyInvalid},
		{name: "unknown field", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"enabled":true,"streaming":true}}
		}`, wantErr: ErrHedgePolicyInvalid},
		{name: "cross region", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"enabled":true,"cross_region":true}}
		}`, wantErr: ErrHedgePolicyInvalid},
		{name: "same endpoint scope", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"enabled":true,"scope":"any"}}
		}`, wantErr: ErrHedgePolicyInvalid},
		{name: "unbounded response", profile: model.RoutingPolicyProfileEnterpriseSLO, policy: `{
			"enterprise":{"hedge":{"enabled":true,"max_response_bytes":67108865}}
		}`, wantErr: ErrHedgePolicyInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolveEnterprisePoolPolicy(test.profile, json.RawMessage(test.policy))
			if test.wantErr != nil {
				assert.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			if test.name == "implicit disabled" {
				assert.False(t, policy.Hedge.Enabled)
				assert.False(t, policy.Hedge.Explicit)
				return
			}
			assert.True(t, policy.Hedge.Enabled)
			assert.True(t, policy.Hedge.Explicit)
			assert.Equal(t, 250*time.Millisecond, policy.Hedge.Delay)
			assert.Equal(t, 1.5, policy.Hedge.MaxExtraCostMultiplier)
			assert.Equal(t, int64(1<<20), policy.Hedge.MaxResponseBytes)
			assert.False(t, policy.Hedge.CrossRegion)
		})
	}
}

func TestHedgeCoordinatorEnforcesDistinctEndpointBudgetAndSingleWinner(t *testing.T) {
	policy := defaultEnterpriseHedgePolicy()
	policy.Enabled = true
	policy.Explicit = true
	policy.MaxExtraCostMultiplier = 1.25
	coordinator, err := NewHedgeCoordinator(policy, 4)
	require.NoError(t, err)

	primary, err := coordinator.BeginPrimary()
	require.NoError(t, err)
	_, err = coordinator.BeginSecondary(4, false)
	assert.ErrorIs(t, err, ErrHedgeTargetNotDistinct)
	_, err = coordinator.BeginSecondary(6, true)
	assert.ErrorIs(t, err, ErrHedgeCostBudgetExceeded)
	secondary, err := coordinator.BeginSecondary(5, true)
	require.NoError(t, err)

	assert.True(t, secondary.TryWin())
	assert.False(t, primary.TryWin())
	assert.True(t, secondary.TryWin(), "the selected winner remains stable")
	primary.Finish()
	secondary.Finish()

	snapshot := coordinator.Snapshot()
	assert.Equal(t, HedgeAttemptSecondary, snapshot.Winner)
	assert.True(t, snapshot.PrimaryFinished)
	assert.True(t, snapshot.SecondaryFinished)
}

func TestHedgeLimiterNeverExceedsConfiguredProcessConcurrency(t *testing.T) {
	limiter := &HedgeLimiter{}
	const limit = 3
	const contenders = 64
	start := make(chan struct{})
	release := make(chan struct{})
	var wait sync.WaitGroup
	var attempted sync.WaitGroup
	var admittedMu sync.Mutex
	admitted := make([]*HedgeSlot, 0, limit)
	wait.Add(contenders)
	attempted.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			<-start
			slot := limiter.TryAcquire(limit)
			attempted.Done()
			if slot == nil {
				return
			}
			admittedMu.Lock()
			admitted = append(admitted, slot)
			admittedMu.Unlock()
			<-release
			slot.Release()
		}()
	}
	close(start)
	attempted.Wait()
	assert.Equal(t, int64(limit), limiter.Stats().Active)
	close(release)
	wait.Wait()

	admittedMu.Lock()
	assert.Len(t, admitted, limit)
	admittedMu.Unlock()
	assert.Equal(t, int64(0), limiter.Stats().Active)
	assert.Equal(t, int64(contenders-limit), limiter.Stats().Denied)
}

func TestHedgeByteLimiterReservesWorstCaseBuffersUntilExplicitRelease(t *testing.T) {
	limiter := &HedgeByteLimiter{}
	first := limiter.TryAcquire(6, 10)
	require.NotNil(t, first)
	assert.Nil(t, limiter.TryAcquire(5, 10))
	stats := limiter.Stats(10)
	assert.Equal(t, int64(6), stats.ActiveBytes)
	assert.Equal(t, int64(6), stats.PeakBytes)
	assert.Equal(t, int64(1), stats.Denied)

	first.Release()
	first.Release()
	second := limiter.TryAcquire(10, 10)
	require.NotNil(t, second)
	assert.Equal(t, int64(10), limiter.Stats(10).PeakBytes)
	second.Release()
	assert.Zero(t, limiter.Stats(10).ActiveBytes)
}

func TestHedgeRatioBudgetBoundsLongRunExtraRequestsAndPoolState(t *testing.T) {
	budget := NewHedgeRatioBudget(2)
	now := time.Unix(1_000, 0)
	const maxExtraBasisPoints = 500
	window := time.Minute
	allowed := 0
	for index := 0; index < 100; index++ {
		require.True(t, budget.ObservePrimary(7, now, window))
		if budget.AllowSecondary(7, now, window, maxExtraBasisPoints) {
			allowed++
		}
	}
	assert.Equal(t, 5, allowed)
	stats := budget.Stats(window, maxExtraBasisPoints)
	assert.Equal(t, int64(100), stats.PrimaryRequests)
	assert.Equal(t, int64(5), stats.ExtraAllowed)
	assert.Equal(t, int64(95), stats.ExtraDenied)
	assert.Equal(t, maxExtraBasisPoints, stats.MaxExtraBasisPoints)

	require.True(t, budget.ObservePrimary(8, now, window))
	require.True(t, budget.ObservePrimary(9, now, window))
	stats = budget.Stats(window, maxExtraBasisPoints)
	assert.Equal(t, 2, stats.Pools)
	assert.Equal(t, int64(1), stats.Evictions)

	require.True(t, budget.ObservePrimary(10, now.Add(3*window), window))
	stats = budget.Stats(window, maxExtraBasisPoints)
	assert.Equal(t, 2, stats.Pools)
	assert.Equal(t, int64(2), stats.Evictions)
}
