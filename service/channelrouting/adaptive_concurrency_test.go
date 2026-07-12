package channelrouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptiveConcurrencyColdStartAdmissionAndHealthyIncrease(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(1_000, 0)}
	controller := newAdaptiveConcurrencyControllerForTest(t, clock, 8)
	target := adaptiveMemberTargetForTest(1, 10, 100, 8)

	leases := make([]*AdaptiveConcurrencyLeaseSet, 0, 4)
	for index := 0; index < 4; index++ {
		lease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{target})
		require.NoError(t, err)
		leases = append(leases, lease)
	}
	_, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{target})
	assert.ErrorIs(t, err, ErrAdaptiveConcurrencyExhausted, "cold start must use the conservative initial limit")
	for _, lease := range leases {
		require.NoError(t, lease.Release())
	}

	for index := 0; index < target.Policy.HealthySamples; index++ {
		controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
			Success: true, TTFT: 100 * time.Millisecond, ObservedAt: clock.Now(),
		})
	}
	snapshot := controller.Snapshots([]AdaptiveConcurrencyTarget{target})
	require.Len(t, snapshot, 1)
	assert.Equal(t, int64(5), snapshot[0].Limit)

	for index := 0; index < target.Policy.HealthySamples; index++ {
		controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
			Success: true, TTFT: 100 * time.Millisecond, ObservedAt: clock.Now(),
		})
	}
	snapshot = controller.Snapshots([]AdaptiveConcurrencyTarget{target})
	assert.Equal(t, int64(5), snapshot[0].Limit, "increase rate must be bounded")
	clock.Advance(time.Second)
	controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
		Success: true, TTFT: 100 * time.Millisecond, ObservedAt: clock.Now(),
	})
	snapshot = controller.Snapshots([]AdaptiveConcurrencyTarget{target})
	assert.Equal(t, int64(6), snapshot[0].Limit)
}

func TestAdaptiveConcurrencyUsesMemberAndSharedSignalScopes(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(2_000, 0)}
	controller := newAdaptiveConcurrencyControllerForTest(t, clock, 16)
	member := adaptiveMemberTargetForTest(2, 20, 200, 16)
	shared := adaptiveSharedTargetForTest(2, 300, 400, 16)
	lease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{member, shared})
	require.NoError(t, err)
	require.NoError(t, lease.Release())

	controller.Observe([]AdaptiveConcurrencyTarget{member, shared}, AdaptiveConcurrencySignal{
		StatusCode: 529, ObservedAt: clock.Now(),
	})
	snapshots := controller.Snapshots([]AdaptiveConcurrencyTarget{member, shared})
	require.Len(t, snapshots, 2)
	assert.Equal(t, int64(2), adaptiveSnapshotForScope(t, snapshots, AdaptiveConcurrencyScopeMemberModel).Limit)
	assert.Equal(t, int64(4), adaptiveSnapshotForScope(t, snapshots, AdaptiveConcurrencyScopeSharedResource).Limit,
		"provider overload is scoped to member-model, not the shared quota bucket")

	clock.Advance(time.Second)
	controller.Observe([]AdaptiveConcurrencyTarget{member, shared}, AdaptiveConcurrencySignal{
		StatusCode: 429, ObservedAt: clock.Now(),
	})
	snapshots = controller.Snapshots([]AdaptiveConcurrencyTarget{member, shared})
	assert.Equal(t, int64(1), adaptiveSnapshotForScope(t, snapshots, AdaptiveConcurrencyScopeMemberModel).Limit)
	assert.Equal(t, int64(2), adaptiveSnapshotForScope(t, snapshots, AdaptiveConcurrencyScopeSharedResource).Limit,
		"credential/account capacity overload must reduce the shared resource gate")
}

func TestAdaptiveConcurrencyTTFTSpikeAndProbeIsolation(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(3_000, 0)}
	controller := newAdaptiveConcurrencyControllerForTest(t, clock, 12)
	target := adaptiveMemberTargetForTest(3, 30, 300, 12)
	lease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{target})
	require.NoError(t, err)
	require.NoError(t, lease.Release())

	for index := 0; index < 4; index++ {
		controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
			Success: true, TTFT: 100 * time.Millisecond, ObservedAt: clock.Now(),
		})
	}
	before := controller.Snapshots([]AdaptiveConcurrencyTarget{target})[0]
	controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
		Success: true, TTFT: 500 * time.Millisecond, Probe: true, ObservedAt: clock.Now(),
	})
	afterProbe := controller.Snapshots([]AdaptiveConcurrencyTarget{target})[0]
	assert.Equal(t, before.Limit, afterProbe.Limit)
	assert.Equal(t, before.TTFTSamples, afterProbe.TTFTSamples)

	clock.Advance(time.Second)
	controller.Observe([]AdaptiveConcurrencyTarget{target}, AdaptiveConcurrencySignal{
		Success: true, TTFT: 500 * time.Millisecond, ObservedAt: clock.Now(),
	})
	afterSpike := controller.Snapshots([]AdaptiveConcurrencyTarget{target})[0]
	assert.Less(t, afterSpike.Limit, before.Limit)
	assert.InDelta(t, 100, afterSpike.BaselineTTFTMs, 0.001,
		"a spike must not immediately poison the healthy baseline")
}

func TestAdaptiveConcurrencySharesLimitsAcrossPolicyRevisions(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(4_000, 0)}
	controller := newAdaptiveConcurrencyControllerForTest(t, clock, 1)
	oldTarget := adaptiveMemberTargetForTest(4, 40, 400, 1)
	oldLease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{oldTarget})
	require.NoError(t, err)

	newTarget := oldTarget
	newTarget.Key.PolicyRevision++
	_, err = controller.TryAcquire([]AdaptiveConcurrencyTarget{newTarget})
	assert.ErrorIs(t, err, ErrAdaptiveConcurrencyExhausted)

	require.NoError(t, oldLease.Release())
	newLease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{newTarget})
	require.NoError(t, err)
	require.NoError(t, newLease.Release())

	changedTarget := newTarget
	changedTarget.Key.PolicyRevision++
	changedTarget.Policy = DefaultAdaptiveConcurrencyPolicy(2)
	changedLease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{changedTarget})
	require.NoError(t, err, "a drained resource must adopt the new revision policy without waiting for TTL eviction")
	require.NoError(t, changedLease.Release())
	snapshots := controller.Snapshots([]AdaptiveConcurrencyTarget{changedTarget})
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(2), snapshots[0].Limit)
	assert.Equal(t, 1, controller.Stats().Entries)

	_, err = controller.TryAcquire([]AdaptiveConcurrencyTarget{oldTarget})
	assert.ErrorIs(t, err, ErrAdaptiveConcurrencyConflict,
		"a drained stale revision must not restore an older adaptive policy")

	newerSamePolicy := changedTarget
	newerSamePolicy.Key.PolicyRevision++
	newerLease, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{newerSamePolicy})
	require.NoError(t, err)
	require.NoError(t, newerLease.Release())
	staleSamePolicy, err := controller.TryAcquire([]AdaptiveConcurrencyTarget{changedTarget})
	require.NoError(t, err, "stale revisions with the same policy may share the current controller")
	require.NoError(t, staleSamePolicy.Release())
}

func BenchmarkAdaptiveConcurrencyTryAcquireObserve(b *testing.B) {
	controller, err := NewAdaptiveConcurrencyController(AdaptiveConcurrencyConfig{
		MaxEntries: 64, IdleTTL: time.Hour, Shards: 8,
	})
	require.NoError(b, err)
	targets := []AdaptiveConcurrencyTarget{
		{
			Key: AdaptiveConcurrencyKey{
				PolicyRevision: 1, Scope: AdaptiveConcurrencyScopeMemberModel,
				PoolID: 1, MemberID: 1, Model: "gpt-test",
			},
			Policy: DefaultAdaptiveConcurrencyPolicy(128),
		},
		{
			Key: AdaptiveConcurrencyKey{
				PolicyRevision: 1, Scope: AdaptiveConcurrencyScopeSharedResource,
				AccountID: 1, CredentialID: 1, Model: "upstream-gpt",
			},
			Policy: DefaultAdaptiveConcurrencyPolicy(128),
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		lease, acquireErr := controller.TryAcquire(targets)
		if acquireErr != nil {
			b.Fatal(acquireErr)
		}
		if releaseErr := lease.Release(); releaseErr != nil {
			b.Fatal(releaseErr)
		}
		controller.Observe(targets, AdaptiveConcurrencySignal{Success: true})
	}
}

func newAdaptiveConcurrencyControllerForTest(
	t *testing.T,
	clock Clock,
	maxEntries int,
) *AdaptiveConcurrencyController {
	t.Helper()
	controller, err := NewAdaptiveConcurrencyController(AdaptiveConcurrencyConfig{
		MaxEntries: maxEntries, IdleTTL: time.Minute, Shards: 4, Clock: clock,
	})
	require.NoError(t, err)
	return controller
}

func adaptiveMemberTargetForTest(
	revision uint64,
	poolID int,
	memberID int,
	maximum int64,
) AdaptiveConcurrencyTarget {
	return AdaptiveConcurrencyTarget{
		Key: AdaptiveConcurrencyKey{
			PolicyRevision: revision, Scope: AdaptiveConcurrencyScopeMemberModel,
			PoolID: poolID, MemberID: memberID, Model: "gpt-test",
		},
		Policy: DefaultAdaptiveConcurrencyPolicy(maximum),
	}
}

func adaptiveSharedTargetForTest(
	revision uint64,
	channelID int,
	credentialID int,
	maximum int64,
) AdaptiveConcurrencyTarget {
	return AdaptiveConcurrencyTarget{
		Key: AdaptiveConcurrencyKey{
			PolicyRevision: revision, Scope: AdaptiveConcurrencyScopeSharedResource,
			ChannelID: channelID, CredentialID: credentialID, Model: "upstream-gpt",
		},
		Policy: DefaultAdaptiveConcurrencyPolicy(maximum),
	}
}

func adaptiveSnapshotForScope(
	t *testing.T,
	snapshots []AdaptiveConcurrencySnapshot,
	scope string,
) AdaptiveConcurrencySnapshot {
	t.Helper()
	for _, snapshot := range snapshots {
		if snapshot.Key.Scope == scope {
			return snapshot
		}
	}
	require.Fail(t, "adaptive concurrency scope was not found", scope)
	return AdaptiveConcurrencySnapshot{}
}
