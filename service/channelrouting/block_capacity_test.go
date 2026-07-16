package channelrouting

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictCapacityRedisKeysUseUnversionedNamespace(t *testing.T) {
	stateKey, leasesKey := strictCapacityRedisKeys(StrictCapacityKey{
		AccountID: 17, CredentialID: 29, Model: "gpt-test",
	})

	assert.True(t, strings.HasPrefix(stateKey, "channel-routing:capacity:{"))
	assert.True(t, strings.HasSuffix(stateKey, "}:state"))
	assert.True(t, strings.HasPrefix(leasesKey, "channel-routing:capacity:{"))
	assert.True(t, strings.HasSuffix(leasesKey, "}:leases"))
	assert.NotContains(t, stateKey, ":v2:")
	assert.NotContains(t, leasesKey, ":v2:")
}

func TestRedisBlockCapacityReusesNodeLeaseAndRefundsPendingAdmission(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(5_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	request := blockCapacityRequestForTest(1)

	reservations := make([]*StrictCapacityReservation, 0, 5)
	for index := 0; index < 5; index++ {
		reservation, err := coordinator.TryReserve(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, CapacityModeRedisBlock, reservation.Admission().Mode)
		assert.True(t, reservation.Admission().BlockLease)
		reservations = append(reservations, reservation)
	}
	assert.Equal(t, 1, fake.calls("reserve"), "requests inside the node block must not visit Redis")
	for _, reservation := range reservations {
		require.NoError(t, reservation.Cancel(context.Background()))
	}
	assert.Equal(t, 1, fake.calls("cancel"), "a fully unused pending block must be atomically refunded")
	stats := coordinator.Stats()
	assert.Equal(t, int64(5), stats.BlockAllowed)
	assert.Equal(t, int64(1), stats.BlockLeases)
	assert.Equal(t, int64(5), stats.Canceled)
}

func TestRedisBlockCapacityUsesLeasedRemainderDuringRedisFailure(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(6_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	request := blockCapacityRequestForTest(1)

	first, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, first.Commit(context.Background()))
	fake.setUnavailable(true)
	second, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err, "an unexpired leased block remains usable while Redis is unavailable")
	require.NoError(t, second.Commit(context.Background()))
	assert.Equal(t, 1, fake.calls("reserve"))
	assert.Equal(t, 1, fake.calls("commit"), "only the shared block transition touches Redis")
	require.NoError(t, first.Release(context.Background()))
	require.NoError(t, second.Release(context.Background()))
}

func TestRedisBlockCapacityRefillsAtLowWaterInsteadOfEveryRequest(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(7_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	request := blockCapacityRequestForTest(1)

	reservations := make([]*StrictCapacityReservation, 0, 9)
	for index := 0; index < 9; index++ {
		reservation, err := coordinator.TryReserve(context.Background(), request)
		require.NoError(t, err)
		reservations = append(reservations, reservation)
	}
	assert.Equal(t, 2, fake.calls("reserve"), "the next request after low water acquires one refill block")
	assert.Equal(t, int64(1), coordinator.Stats().BlockRefills)
	for _, reservation := range reservations {
		require.NoError(t, reservation.Cancel(context.Background()))
	}
}

func TestRedisBlockCapacityHonorsLeaseTTLWhenRenewalFails(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(8_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	request := blockCapacityRequestForTest(1)
	request.LeaseTTL = time.Second

	reservation, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, reservation.Commit(context.Background()))
	fake.setUnavailable(true)
	clock.Advance(600 * time.Millisecond)
	require.NoError(t, reservation.Renew(context.Background(), time.Second),
		"Redis failure must not revoke a block before its published TTL")
	clock.Advance(500 * time.Millisecond)
	assert.ErrorIs(t, reservation.Renew(context.Background(), time.Second), ErrStrictCapacityLost)
}

func TestRedisBlockCapacityFencesOldRevisionAdmissionsButLetsExistingLeaseFinish(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(9_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	oldRequest := blockCapacityRequestForTest(1)
	oldReservation, err := coordinator.TryReserve(context.Background(), oldRequest)
	require.NoError(t, err)

	newRequest := blockCapacityRequestForTest(2)
	newReservation, err := coordinator.TryReserve(context.Background(), newRequest)
	require.NoError(t, err)
	_, err = coordinator.TryReserve(context.Background(), oldRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityConflict)
	require.NoError(t, oldReservation.Commit(context.Background()),
		"work admitted before the epoch switch may still finish within its original lease")
	require.NoError(t, oldReservation.Release(context.Background()))
	require.NoError(t, newReservation.Cancel(context.Background()))
	assert.Positive(t, coordinator.Stats().BlockFenced)
}

func TestRedisBlockCapacityEvictionKeepsActiveEpochsBounded(t *testing.T) {
	clock := &routingTestClock{now: time.Unix(9_500, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now

	for index := 1; index <= strictCapacityBlockMaximumEntries; index++ {
		request := blockCapacityRequestForTest(1)
		request.Key.CredentialID = index
		_, err := coordinator.blocks.entry(request)
		require.NoError(t, err)
	}
	assert.Len(t, coordinator.blocks.entries, strictCapacityBlockMaximumEntries)
	assert.Len(t, coordinator.blocks.activeEpochs, strictCapacityBlockMaximumEntries)

	for index := 1; index <= 32; index++ {
		request := blockCapacityRequestForTest(1)
		request.Key.CredentialID = strictCapacityBlockMaximumEntries + index
		_, err := coordinator.blocks.entry(request)
		assert.ErrorIs(t, err, ErrStrictCapacityStateLimit)
	}
	assert.Len(t, coordinator.blocks.activeEpochs, strictCapacityBlockMaximumEntries,
		"failed admissions must not leave orphan active epochs")

	clock.Advance(strictCapacityBlockEntryIdleTTL + time.Second)
	request := blockCapacityRequestForTest(2)
	request.Key.CredentialID = strictCapacityBlockMaximumEntries + 100
	_, err := coordinator.blocks.entry(request)
	require.NoError(t, err)
	assert.Len(t, coordinator.blocks.entries, 1)
	assert.Len(t, coordinator.blocks.activeEpochs, 1)
	assert.Equal(t, 1, coordinator.blocks.entryCounts[request.Key])
}

func TestStrictCapacityRealRedisBlockLeaseAvoidsPerRequestRoundTrips(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	var evalCalls atomic.Int64
	coordinator := NewStrictCapacityCoordinator(&countingBlockCapacityRedis{client: client, calls: &evalCalls})
	request := blockCapacityRequestForTest(1)
	request.Key.Model = "real-redis-block-" + time.Now().Format("150405.000000000")
	request.Limit.Inflight = 20

	first, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err)
	second, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, first.block)
	global := first.block.block.global
	t.Cleanup(func() {
		_ = client.Del(context.Background(), global.capacityKey, global.leaseKey).Err()
	})
	assert.Equal(t, int64(1), evalCalls.Load(), "two local admissions share one Redis block reservation")
	assert.Equal(t, int64(1), client.ZCard(context.Background(), global.leaseKey).Val())

	require.NoError(t, first.Commit(context.Background()))
	require.NoError(t, second.Commit(context.Background()))
	assert.Equal(t, int64(2), evalCalls.Load(), "only the first local send commits the shared Redis block")
	require.NoError(t, first.Release(context.Background()))
	require.NoError(t, second.Release(context.Background()))
}

func TestStrictCapacityRealRedisBlockRevisionFencesNewOldAdmissions(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	coordinator := NewStrictCapacityCoordinator(client)
	oldRequest := blockCapacityRequestForTest(1)
	oldRequest.Key.Model = "real-redis-block-revision-" + time.Now().Format("150405.000000000")
	oldReservation, err := coordinator.TryReserve(context.Background(), oldRequest)
	require.NoError(t, err)
	oldGlobal := oldReservation.block.block.global
	t.Cleanup(func() { _ = client.Del(context.Background(), oldGlobal.capacityKey, oldGlobal.leaseKey).Err() })

	newRequest := oldRequest
	newRequest.PolicyRevision = 2
	newReservation, err := coordinator.TryReserve(context.Background(), newRequest)
	require.NoError(t, err)
	newGlobal := newReservation.block.block.global
	t.Cleanup(func() { _ = client.Del(context.Background(), newGlobal.capacityKey, newGlobal.leaseKey).Err() })
	_, err = coordinator.TryReserve(context.Background(), oldRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityConflict)
	require.NoError(t, oldReservation.Commit(context.Background()))
	require.NoError(t, oldReservation.Release(context.Background()))
	require.NoError(t, newReservation.Cancel(context.Background()))
}

func BenchmarkRedisBlockCapacityLocalLease(b *testing.B) {
	clock := &routingTestClock{now: time.Unix(12_000, 0)}
	fake := &blockCapacityRedis{now: clock.Now}
	coordinator := NewStrictCapacityCoordinator(fake)
	coordinator.blocks.now = clock.Now
	request := blockCapacityRequestForTest(1)
	request.Demand = StrictCapacityDemand{Inflight: 1}
	request.Limit = StrictCapacityLimit{Inflight: 10_000}

	warm, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(b, err)
	require.NoError(b, warm.Commit(context.Background()))
	require.NoError(b, warm.Release(context.Background()))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		reservation, reserveErr := coordinator.TryReserve(context.Background(), request)
		if reserveErr != nil {
			b.Fatal(reserveErr)
		}
		if commitErr := reservation.Commit(context.Background()); commitErr != nil {
			b.Fatal(commitErr)
		}
		if releaseErr := reservation.Release(context.Background()); releaseErr != nil {
			b.Fatal(releaseErr)
		}
	}
}

func blockCapacityRequestForTest(revision uint64) StrictCapacityRequest {
	return StrictCapacityRequest{
		Mode:   CapacityModeRedisBlock,
		Key:    StrictCapacityKey{CredentialID: 91, Model: "gpt-block"},
		PoolID: 1, PolicyRevision: revision,
		Demand: StrictCapacityDemand{
			RPM: 1, InputTPM: 10, OutputTPM: 5, TotalTPM: 15, Inflight: 1,
		},
		Limit: StrictCapacityLimit{
			RPM: 1_000, InputTPM: 100_000, OutputTPM: 50_000, TotalTPM: 150_000, Inflight: 100,
		},
		PoolShares: []StrictCapacityPoolShare{{
			PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000,
		}},
		LeaseTTL: time.Minute,
	}
}

type blockCapacityRedis struct {
	mu          sync.Mutex
	now         func() time.Time
	unavailable bool
	counts      map[string]int
}

type countingBlockCapacityRedis struct {
	client *redis.Client
	calls  *atomic.Int64
}

func (counter *countingBlockCapacityRedis) Eval(
	ctx context.Context,
	script string,
	keys []string,
	args ...interface{},
) *redis.Cmd {
	counter.calls.Add(1)
	return counter.client.Eval(ctx, script, keys, args...)
}

func (fake *blockCapacityRedis) Eval(
	_ context.Context,
	script string,
	_ []string,
	args ...interface{},
) *redis.Cmd {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.counts == nil {
		fake.counts = make(map[string]int)
	}
	kind := "unknown"
	for _, candidate := range []string{"reserve", "commit", "cancel", "release", "renew"} {
		if strings.Contains(script, "strict_capacity_"+candidate) {
			kind = candidate
			break
		}
	}
	fake.counts[kind]++
	if fake.unavailable {
		return redis.NewCmdResult(nil, errors.New("redis unavailable"))
	}
	now := fake.now().UnixMilli()
	if kind == "reserve" {
		leaseTTL, _ := args[1].(int64)
		return redis.NewCmdResult([]interface{}{int64(1), now, now + leaseTTL}, nil)
	}
	if kind == "renew" {
		leaseTTL, _ := args[1].(int64)
		return redis.NewCmdResult(now+leaseTTL, nil)
	}
	return redis.NewCmdResult(int64(1), nil)
}

func (fake *blockCapacityRedis) calls(kind string) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.counts[kind]
}

func (fake *blockCapacityRedis) setUnavailable(unavailable bool) {
	fake.mu.Lock()
	fake.unavailable = unavailable
	fake.mu.Unlock()
}
