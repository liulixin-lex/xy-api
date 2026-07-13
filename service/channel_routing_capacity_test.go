package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingCapacityReservationContextCommitsAndReleasesExactlyOnce(t *testing.T) {
	tracker, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 8,
		IdleTTL:    time.Hour,
		Shards:     2,
	})
	require.NoError(t, err)
	key := channelrouting.CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-test"}
	reservation, err := tracker.TryReserve(
		key,
		channelrouting.Demand{RPM: 1, InputTPM: 100, OutputTPM: 50, Inflight: 1},
		channelrouting.Limit{RPM: 10, InputTPM: 1_000, OutputTPM: 500, Inflight: 1},
	)
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, SetRoutingCapacityReservation(ctx, reservation))
	require.NoError(t, CommitRoutingCapacityReservation(ctx))
	require.NoError(t, CommitRoutingCapacityReservation(ctx))
	snapshot, ok := tracker.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, int64(1), snapshot.CommittedReservations)
	assert.Equal(t, int64(1), snapshot.Committed.Inflight)
	assert.Equal(t, int64(1), snapshot.Committed.RPM)

	require.NoError(t, ReleaseRoutingCapacityReservation(ctx))
	require.NoError(t, ReleaseRoutingCapacityReservation(ctx))
	snapshot, ok = tracker.Snapshot(key)
	require.True(t, ok)
	assert.Zero(t, snapshot.CommittedReservations)
	assert.Zero(t, snapshot.Committed.Inflight)
	assert.Equal(t, int64(1), snapshot.Committed.RPM, "rate debt must survive request completion")
}

func TestRoutingCapacityReservationContextCancelsReplacedPendingReservation(t *testing.T) {
	tracker, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: 8,
		IdleTTL:    time.Hour,
		Shards:     2,
	})
	require.NoError(t, err)
	firstKey := channelrouting.CapacityKey{PoolID: 1, MemberID: 11, Model: "gpt-test"}
	secondKey := channelrouting.CapacityKey{PoolID: 1, MemberID: 12, Model: "gpt-test"}
	first, err := tracker.TryReserve(firstKey, channelrouting.Demand{Inflight: 1}, channelrouting.Limit{Inflight: 1})
	require.NoError(t, err)
	second, err := tracker.TryReserve(secondKey, channelrouting.Demand{Inflight: 1}, channelrouting.Limit{Inflight: 1})
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, SetRoutingCapacityReservation(ctx, first))
	require.NoError(t, SetRoutingCapacityReservation(ctx, second))
	firstSnapshot, ok := tracker.Snapshot(firstKey)
	require.True(t, ok)
	assert.Zero(t, firstSnapshot.PendingReservations)
	secondSnapshot, ok := tracker.Snapshot(secondKey)
	require.True(t, ok)
	assert.Equal(t, int64(1), secondSnapshot.PendingReservations)

	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	secondSnapshot, ok = tracker.Snapshot(secondKey)
	require.True(t, ok)
	assert.Zero(t, secondSnapshot.PendingReservations)
}

func TestRoutingCapacityReservationContextIsNoopWithoutReservation(t *testing.T) {
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, CommitRoutingCapacityReservation(ctx))
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
	require.NoError(t, ReleaseRoutingCapacityReservation(ctx))
}
