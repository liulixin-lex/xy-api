package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingAdaptiveConcurrencyLeaseFollowsCapacityReservationLifecycle(t *testing.T) {
	previous := channelRoutingCanaryRuntime
	manager, err := newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	channelRoutingCanaryRuntime = manager
	t.Cleanup(func() { channelRoutingCanaryRuntime = previous })

	policy := model.DefaultRoutingCanaryPolicy()
	policy.Capacity.Inflight = 8
	identity := channelrouting.Identity{SnapshotRevision: 7, PoolID: 3, MemberID: 31, CredentialID: 301}
	lease, err := reserveRoutingAdaptiveConcurrency(
		7, policy, identity, 101, "gpt-test", "upstream-gpt", nil,
	)
	require.NoError(t, err)
	reservation, err := manager.capacity.TryReserve(
		channelrouting.CapacityKey{PolicyRevision: 7, PoolID: 3, MemberID: 31, Model: "gpt-test"},
		channelrouting.Demand{Inflight: 1},
		channelrouting.Limit{Inflight: 8},
	)
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	require.NoError(t, SetRoutingCapacityReservation(ctx, reservation))
	require.NoError(t, AttachRoutingAdaptiveConcurrency(ctx, lease))

	targets := RoutingAdaptiveConcurrencyTargets(ctx)
	require.Len(t, targets, 2)
	for _, snapshot := range manager.adaptive.Snapshots(targets) {
		assert.Equal(t, int64(1), snapshot.Inflight)
	}
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	for _, snapshot := range manager.adaptive.Snapshots(targets) {
		assert.Zero(t, snapshot.Inflight)
	}
}

func TestRoutingAdaptiveConcurrencyOutcomeUpdatesCorrectScopes(t *testing.T) {
	previous := channelRoutingCanaryRuntime
	manager, err := newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	channelRoutingCanaryRuntime = manager
	t.Cleanup(func() { channelRoutingCanaryRuntime = previous })

	policy := model.DefaultRoutingCanaryPolicy()
	policy.Capacity.Inflight = 16
	identity := channelrouting.Identity{SnapshotRevision: 8, PoolID: 4, MemberID: 41, CredentialID: 401}
	lease, err := reserveRoutingAdaptiveConcurrency(
		8, policy, identity, 102, "gpt-test", "upstream-gpt", nil,
	)
	require.NoError(t, err)
	targets := lease.Targets()
	require.NoError(t, lease.Release())
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set("routing_adaptive_concurrency", targets)

	ObserveRoutingAdaptiveConcurrency(ctx, channelrouting.AdaptiveConcurrencySignal{
		StatusCode: 529, ObservedAt: time.Now(),
	})
	snapshots := manager.adaptive.Snapshots(targets)
	assert.Equal(t, int64(2), routingAdaptiveLimitForScope(t, snapshots, channelrouting.AdaptiveConcurrencyScopeMemberModel))
	assert.Equal(t, int64(4), routingAdaptiveLimitForScope(t, snapshots, channelrouting.AdaptiveConcurrencyScopeSharedResource))

	ObserveRoutingAdaptiveConcurrency(ctx, channelrouting.AdaptiveConcurrencySignal{
		StatusCode: 429, ObservedAt: time.Now().Add(time.Second),
	})
	snapshots = manager.adaptive.Snapshots(targets)
	assert.Equal(t, int64(1), routingAdaptiveLimitForScope(t, snapshots, channelrouting.AdaptiveConcurrencyScopeMemberModel))
	assert.Equal(t, int64(2), routingAdaptiveLimitForScope(t, snapshots, channelrouting.AdaptiveConcurrencyScopeSharedResource))
}

func TestRoutingAdaptiveConcurrencyStrictAccountSharesPhysicalScopeAcrossChannels(t *testing.T) {
	manager, err := newChannelRoutingCanaryRuntimeManager(nil)
	require.NoError(t, err)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Capacity.Inflight = 12
	strictRequest := &channelrouting.StrictCapacityRequest{
		Key:   channelrouting.StrictCapacityKey{AccountID: 9001, CredentialID: 77, Model: "upstream-gpt"},
		Limit: channelrouting.StrictCapacityLimit{Inflight: 20},
	}

	first, err := manager.tryAcquireAdaptiveConcurrency(
		9, policy,
		channelrouting.Identity{SnapshotRevision: 9, PoolID: 5, MemberID: 51, CredentialID: 501},
		101, "gpt-test", "mapped-one", strictRequest,
	)
	require.NoError(t, err)
	second, err := manager.tryAcquireAdaptiveConcurrency(
		9, policy,
		channelrouting.Identity{SnapshotRevision: 9, PoolID: 5, MemberID: 52, CredentialID: 502},
		202, "gpt-test", "mapped-two", strictRequest,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, first.Release())
		require.NoError(t, second.Release())
	})

	var firstShared, secondShared channelrouting.AdaptiveConcurrencyKey
	for _, target := range first.Targets() {
		if target.Key.Scope == channelrouting.AdaptiveConcurrencyScopeSharedResource {
			firstShared = target.Key
		}
	}
	for _, target := range second.Targets() {
		if target.Key.Scope == channelrouting.AdaptiveConcurrencyScopeSharedResource {
			secondShared = target.Key
		}
	}
	assert.Equal(t, firstShared, secondShared)
	assert.Zero(t, firstShared.ChannelID)
	assert.Equal(t, 9001, firstShared.AccountID)
	assert.Equal(t, 77, firstShared.CredentialID)
	assert.Equal(t, "upstream-gpt", firstShared.Model)
	assert.Equal(t, 3, manager.adaptive.Stats().Entries)
	snapshots := manager.adaptive.Snapshots([]channelrouting.AdaptiveConcurrencyTarget{{
		Key: firstShared, Policy: channelrouting.DefaultAdaptiveConcurrencyPolicy(20),
	}})
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(2), snapshots[0].Inflight)
}

func routingAdaptiveLimitForScope(
	t *testing.T,
	snapshots []channelrouting.AdaptiveConcurrencySnapshot,
	scope string,
) int64 {
	t.Helper()
	for _, snapshot := range snapshots {
		if snapshot.Key.Scope == scope {
			return snapshot.Limit
		}
	}
	require.Fail(t, "adaptive concurrency scope was not found", scope)
	return 0
}
