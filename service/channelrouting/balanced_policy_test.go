package channelrouting

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBalancedPoolPolicyUsesProfileDefaultsAndExplicitOverrides(t *testing.T) {
	policy, err := resolveBalancedPoolPolicy(model.RoutingPolicyProfileBalanced, []byte(`{
		"availability_target": 0.995,
		"availability_floor": 0.97,
		"exploration_basis_points": 200,
		"require_known_cost": true,
		"allow_soft_failure_fallback": false
	}`))
	require.NoError(t, err)
	assert.Equal(t, 0.995, policy.AvailabilityTarget)
	assert.Equal(t, 0.97, policy.AvailabilityFloor)
	assert.Equal(t, 200, policy.ExplorationBasisPoints)
	assert.True(t, policy.RequireKnownCost)
	assert.False(t, policy.AllowSoftFailureFallback)

	settings := policy.settings(time.Unix(1_700_000_000, 0), 17, 23, true)
	assert.Equal(t, int64(17), settings.RandomSeed)
	assert.Equal(t, 23, settings.PreferredChannelID)
	assert.True(t, settings.PreferTTFT)
}

func TestResolveBalancedPoolPolicyRejectsUnsafeSLOAndBudgetCombinations(t *testing.T) {
	tests := []string{
		`{"availability_target":0.95,"availability_floor":0.96}`,
		`{"exploration_basis_points":99}`,
		`{"cost_budget":1,"require_known_cost":false}`,
		`{"max_capacity_utilization":0.7,"affinity_max_capacity_utilization":0.8}`,
	}
	for _, policyJSON := range tests {
		_, err := resolveBalancedPoolPolicy(model.RoutingPolicyProfileBalanced, []byte(policyJSON))
		assert.Error(t, err, policyJSON)
	}
}

func TestResolveBalancedPoolPolicySupportsEveryPublishedProfile(t *testing.T) {
	for _, profile := range []string{
		model.RoutingPolicyProfileBalanced,
		model.RoutingPolicyProfileReliabilityFirst,
		model.RoutingPolicyProfileCostAware,
		model.RoutingPolicyProfileEnterpriseSLO,
		model.RoutingPolicyProfileCustom,
	} {
		policy, err := resolveBalancedPoolPolicy(profile, []byte(`{}`))
		require.NoError(t, err, profile)
		assert.Greater(t, policy.AvailabilityTarget, 0.0)
		assert.Greater(t, policy.LatencyTargetMs, 0.0)
		assert.Greater(t, policy.ThroughputTarget, 0.0)
		assert.Greater(t, policy.CostTarget, 0.0)
	}
}
