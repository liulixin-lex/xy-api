package channelrouting

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictCapacityFailsClosedAndValidatesFairSharePolicy(t *testing.T) {
	request := strictCapacityRequestForTest("fail-closed", 1, StrictCapacityDemand{RPM: 1})
	_, err := NewStrictCapacityCoordinator(nil).TryReserve(context.Background(), request)
	assert.ErrorIs(t, err, ErrStrictCapacityUnavailable)

	request.PoolShares = []StrictCapacityPoolShare{
		{PoolID: 1, GuaranteedBasisPoints: 6_000, MaximumBasisPoints: 10_000},
		{PoolID: 2, GuaranteedBasisPoints: 6_000, MaximumBasisPoints: 10_000},
	}
	coordinator := NewStrictCapacityCoordinator(&strictCapacityUnavailableRedis{})
	_, err = coordinator.TryReserve(context.Background(), request)
	assert.ErrorIs(t, err, ErrStrictCapacityInvalid)

	request = strictCapacityRequestForTest("too-large", 1, StrictCapacityDemand{CostNanoUSD: strictCapacityMaximumValue + 1})
	request.Limit.CostNanoUSD = strictCapacityMaximumValue + 1
	_, err = coordinator.TryReserve(context.Background(), request)
	assert.ErrorIs(t, err, ErrStrictCapacityInvalid)

	_, err = NewStrictCapacityCoordinator(&strictCapacityStateLimitRedis{}).TryReserve(
		context.Background(),
		strictCapacityRequestForTest("state-limit", 1, StrictCapacityDemand{RPM: 1}),
	)
	assert.ErrorIs(t, err, ErrStrictCapacityStateLimit)
}

func TestStrictCapacityRealRedisEnforcesFairShareTokenBucketsAndLifecycle(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	coordinator := NewStrictCapacityCoordinator(client)

	firstRequest := strictCapacityRequestForTest(
		"fair-share-borrow",
		1,
		StrictCapacityDemand{RPM: 5, Inflight: 1},
	)
	beforeReserve := time.Now().UnixMilli()
	first, err := coordinator.TryReserve(context.Background(), firstRequest)
	require.NoError(t, err)
	assert.Equal(t, CapacityModeRedisStrict, first.Admission().Mode)
	assert.GreaterOrEqual(t, first.Admission().LeaseExpiresMs, beforeReserve+firstRequest.LeaseTTL.Milliseconds())
	require.NoError(t, first.Commit(context.Background()))

	borrowed, err := coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"fair-share-borrow", 1, StrictCapacityDemand{RPM: 1},
	))
	require.NoError(t, err, "a pool may borrow another idle pool's unused guarantee")
	require.NoError(t, borrowed.Commit(context.Background()))
	require.NoError(t, borrowed.Release(context.Background()))

	second, err := coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"fair-share-borrow", 2, StrictCapacityDemand{RPM: 4, Inflight: 1},
	))
	require.NoError(t, err)
	require.NoError(t, second.Commit(context.Background()))
	require.NoError(t, first.Release(context.Background()))
	require.NoError(t, second.Release(context.Background()))
	assert.ErrorIs(t, first.Cancel(context.Background()), ErrStrictCapacityTransition)

	activeRequest := strictCapacityRequestForTest(
		"fair-share-active",
		2,
		StrictCapacityDemand{RPM: 1},
	)
	active, err := coordinator.TryReserve(context.Background(), activeRequest)
	require.NoError(t, err)
	require.NoError(t, active.Commit(context.Background()))
	require.NoError(t, active.Release(context.Background()))
	_, err = coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"fair-share-active", 1, StrictCapacityDemand{RPM: 6},
	))
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "an active pool's remaining guarantee must stay protected")
	protected, err := coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"fair-share-active", 1, StrictCapacityDemand{RPM: 5},
	))
	require.NoError(t, err)
	require.NoError(t, protected.Cancel(context.Background()))

	inflightActiveRequest := strictCapacityRequestForTest(
		"fair-share-active-inflight",
		2,
		StrictCapacityDemand{Inflight: 1},
	)
	inflightActiveRequest.Limit = StrictCapacityLimit{Inflight: 2}
	inflightActive, err := coordinator.TryReserve(context.Background(), inflightActiveRequest)
	require.NoError(t, err)
	require.NoError(t, inflightActive.Commit(context.Background()))
	require.NoError(t, inflightActive.Release(context.Background()))
	inflightBorrow := strictCapacityRequestForTest(
		"fair-share-active-inflight",
		1,
		StrictCapacityDemand{Inflight: 2},
	)
	inflightBorrow.Limit = StrictCapacityLimit{Inflight: 2}
	_, err = coordinator.TryReserve(context.Background(), inflightBorrow)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "a recently active pool keeps its inflight guarantee after releasing to zero")
	inflightBorrow.Demand.Inflight = 1
	oneInflight, err := coordinator.TryReserve(context.Background(), inflightBorrow)
	require.NoError(t, err)
	require.NoError(t, oneInflight.Cancel(context.Background()))
	inflightAgain, err := coordinator.TryReserve(context.Background(), inflightActiveRequest)
	require.NoError(t, err, "the protected pool must not be starved on its next request")
	require.NoError(t, inflightAgain.Cancel(context.Background()))

	borrowAll := strictCapacityRequestForTest(
		"fair-share-reactivation",
		1,
		StrictCapacityDemand{Inflight: 2},
	)
	borrowAll.Limit = StrictCapacityLimit{Inflight: 2}
	borrowedInflight, err := coordinator.TryReserve(context.Background(), borrowAll)
	require.NoError(t, err)
	require.NoError(t, borrowedInflight.Commit(context.Background()))
	reactivate := strictCapacityRequestForTest(
		"fair-share-reactivation",
		2,
		StrictCapacityDemand{Inflight: 1},
	)
	reactivate.Limit = StrictCapacityLimit{Inflight: 2}
	_, err = coordinator.TryReserve(context.Background(), reactivate)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted)
	require.NoError(t, borrowedInflight.Release(context.Background()))
	_, err = coordinator.TryReserve(context.Background(), borrowAll)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "a denied request must reactivate its pool before capacity returns")
	reactivated, err := coordinator.TryReserve(context.Background(), reactivate)
	require.NoError(t, err, "the reactivated pool must receive the next guaranteed slot")
	require.NoError(t, reactivated.Cancel(context.Background()))

	maximumRequest := strictCapacityRequestForTest(
		"fair-share-maximum",
		1,
		StrictCapacityDemand{RPM: 6},
	)
	maximumRequest.PoolShares = []StrictCapacityPoolShare{
		{PoolID: 1, GuaranteedBasisPoints: 0, MaximumBasisPoints: 5_000},
		{PoolID: 2, GuaranteedBasisPoints: 0, MaximumBasisPoints: 10_000},
	}
	_, err = coordinator.TryReserve(context.Background(), maximumRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "a pool maximum applies even when every other pool is idle")
	maximumRequest.Demand.RPM = 5
	maximum, err := coordinator.TryReserve(context.Background(), maximumRequest)
	require.NoError(t, err)
	require.NoError(t, maximum.Cancel(context.Background()))

	pending, err := coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"cancel-refund", 1, StrictCapacityDemand{RPM: 5, Inflight: 1},
	))
	require.NoError(t, err)
	require.NoError(t, pending.Cancel(context.Background()))
	retry, err := coordinator.TryReserve(context.Background(), strictCapacityRequestForTest(
		"cancel-refund", 1, StrictCapacityDemand{RPM: 5, Inflight: 1},
	))
	require.NoError(t, err)
	require.NoError(t, retry.Cancel(context.Background()))

	refillRequest := strictCapacityRequestForTest(
		"token-refill",
		1,
		StrictCapacityDemand{RPM: 60},
	)
	refillRequest.Limit.RPM = 60
	refillRequest.PoolShares = []StrictCapacityPoolShare{
		{PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000},
	}
	consumed, err := coordinator.TryReserve(context.Background(), refillRequest)
	require.NoError(t, err)
	require.NoError(t, consumed.Commit(context.Background()))
	require.NoError(t, consumed.Release(context.Background()))
	refillRequest.Demand.RPM = 1
	_, err = coordinator.TryReserve(context.Background(), refillRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted)
	time.Sleep(1100 * time.Millisecond)
	refilled, err := coordinator.TryReserve(context.Background(), refillRequest)
	require.NoError(t, err, "the continuous token bucket must refill without waiting for a minute boundary")
	require.NoError(t, refilled.Cancel(context.Background()))

	expiringRequest := strictCapacityRequestForTest(
		"lease-recovery", 1, StrictCapacityDemand{Inflight: 1},
	)
	expiringRequest.LeaseTTL = time.Second
	expiringRequest.Limit = StrictCapacityLimit{Inflight: 1}
	expiring, err := coordinator.TryReserve(context.Background(), expiringRequest)
	require.NoError(t, err)
	time.Sleep(400 * time.Millisecond)
	previousExpiry := expiring.Admission().LeaseExpiresMs
	assert.ErrorIs(t, expiring.Renew(context.Background(), 2*time.Second), ErrStrictCapacityTransition)
	require.NoError(t, expiring.Renew(context.Background(), time.Second))
	assert.Greater(t, expiring.Admission().LeaseExpiresMs, previousExpiry)
	time.Sleep(900 * time.Millisecond)
	otherRequest := strictCapacityRequestForTest("lease-recovery", 2, StrictCapacityDemand{Inflight: 1})
	otherRequest.Limit = StrictCapacityLimit{Inflight: 1}
	otherRequest.LeaseTTL = time.Second
	_, err = coordinator.TryReserve(context.Background(), otherRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "a renewed lease must retain its global inflight slot")
	time.Sleep(1200 * time.Millisecond)
	recovered, err := coordinator.TryReserve(context.Background(), otherRequest)
	require.NoError(t, err, "the next atomic admission must reclaim an expired lease")
	require.NoError(t, recovered.Cancel(context.Background()))
	assert.ErrorIs(t, expiring.Commit(context.Background()), ErrStrictCapacityLost)

	stats := coordinator.Stats()
	assert.Positive(t, stats.Allowed)
	assert.Positive(t, stats.Denied)
	assert.Equal(t, int64(1), stats.Renewed)
	assert.Positive(t, stats.TransitionErr)
}

func TestStrictCapacityRealRedisCarriesGlobalDebtAcrossPolicyEpochs(t *testing.T) {
	tests := []struct {
		name      string
		oldDemand StrictCapacityDemand
		newDenied StrictCapacityDemand
		newAllow  StrictCapacityDemand
		limit     StrictCapacityLimit
	}{
		{name: "rpm", oldDemand: StrictCapacityDemand{RPM: 7}, newDenied: StrictCapacityDemand{RPM: 4}, newAllow: StrictCapacityDemand{RPM: 3}, limit: StrictCapacityLimit{RPM: 10}},
		{name: "input tpm", oldDemand: StrictCapacityDemand{InputTPM: 7}, newDenied: StrictCapacityDemand{InputTPM: 4}, newAllow: StrictCapacityDemand{InputTPM: 3}, limit: StrictCapacityLimit{InputTPM: 10}},
		{name: "output tpm", oldDemand: StrictCapacityDemand{OutputTPM: 7}, newDenied: StrictCapacityDemand{OutputTPM: 4}, newAllow: StrictCapacityDemand{OutputTPM: 3}, limit: StrictCapacityLimit{OutputTPM: 10}},
		{name: "total tpm", oldDemand: StrictCapacityDemand{TotalTPM: 7}, newDenied: StrictCapacityDemand{TotalTPM: 4}, newAllow: StrictCapacityDemand{TotalTPM: 3}, limit: StrictCapacityLimit{TotalTPM: 10}},
		{name: "cost", oldDemand: StrictCapacityDemand{CostNanoUSD: 7}, newDenied: StrictCapacityDemand{CostNanoUSD: 4}, newAllow: StrictCapacityDemand{CostNanoUSD: 3}, limit: StrictCapacityLimit{CostNanoUSD: 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := routingRedisIntegrationClient(t)
			coordinator := NewStrictCapacityCoordinator(client)
			oldRequest := strictCapacityEpochRequestForTest("epoch-debt-"+test.name, 1, test.oldDemand, test.limit)
			oldReservation, err := coordinator.TryReserve(context.Background(), oldRequest)
			require.NoError(t, err)
			require.NoError(t, oldReservation.Commit(context.Background()))
			require.NoError(t, oldReservation.Release(context.Background()))

			newRequest := strictCapacityEpochRequestForTest("epoch-debt-"+test.name, 2, test.newDenied, test.limit)
			_, err = coordinator.TryReserve(context.Background(), newRequest)
			assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "a new epoch must inherit physical rate debt")

			newRequest.Demand = test.newAllow
			allowed, err := coordinator.TryReserve(context.Background(), newRequest)
			require.NoError(t, err)
			require.NoError(t, allowed.Cancel(context.Background()))

			_, err = coordinator.TryReserve(context.Background(), oldRequest)
			assert.ErrorIs(t, err, ErrStrictCapacityConflict, "an inactive epoch cannot admit new work")
			fields, err := client.HKeys(context.Background(), oldReservation.capacityKey).Result()
			require.NoError(t, err)
			for _, field := range fields {
				assert.NotContains(t, field, "e:"+oldReservation.Admission().CapacityEpoch+":")
			}
		})
	}
}

func TestStrictCapacityRealRedisAllowsOldStreamsToFinishWithoutDoublingInflight(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	coordinator := NewStrictCapacityCoordinator(client)
	oldRequest := strictCapacityEpochRequestForTest(
		"epoch-inflight", 1, StrictCapacityDemand{Inflight: 1}, StrictCapacityLimit{Inflight: 2},
	)
	oldRequest.LeaseTTL = 5 * time.Minute
	oldReservation, err := coordinator.TryReserve(context.Background(), oldRequest)
	require.NoError(t, err)
	require.NoError(t, oldReservation.Commit(context.Background()))
	oldTTL, err := client.PTTL(context.Background(), oldReservation.capacityKey).Result()
	require.NoError(t, err)

	newRequest := strictCapacityEpochRequestForTest(
		"epoch-inflight", 2, StrictCapacityDemand{Inflight: 1}, StrictCapacityLimit{Inflight: 2},
	)
	newRequest.LeaseTTL = time.Second
	newReservation, err := coordinator.TryReserve(context.Background(), newRequest)
	require.NoError(t, err, "a new revision must not conflict with the old epoch configuration")
	require.NotEqual(t, oldReservation.Admission().CapacityEpoch, newReservation.Admission().CapacityEpoch)
	transitionTTL, err := client.PTTL(context.Background(), oldReservation.capacityKey).Result()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, transitionTTL, oldTTL-2*time.Second, "a shorter new lease must not truncate old stream state")
	require.NoError(t, newReservation.Commit(context.Background()))
	require.NoError(t, oldReservation.Renew(context.Background(), 5*time.Minute), "an existing old stream may finish naturally")

	_, err = coordinator.TryReserve(context.Background(), newRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityExhausted, "old and new epochs share the physical inflight ceiling")
	_, err = coordinator.TryReserve(context.Background(), oldRequest)
	assert.ErrorIs(t, err, ErrStrictCapacityConflict, "the old epoch cannot admit another request")

	require.NoError(t, oldReservation.Release(context.Background()))
	replacement, err := coordinator.TryReserve(context.Background(), newRequest)
	require.NoError(t, err)
	require.NoError(t, replacement.Cancel(context.Background()))
	require.NoError(t, newReservation.Release(context.Background()))

	fields, err := client.HKeys(context.Background(), oldReservation.capacityKey).Result()
	require.NoError(t, err)
	for _, field := range fields {
		assert.NotContains(t, field, "e:"+oldReservation.Admission().CapacityEpoch+":")
	}
	inflight, err := client.HGet(context.Background(), oldReservation.capacityKey, "t:inflight").Int64()
	require.NoError(t, err)
	assert.Zero(t, inflight)
	assert.Equal(t, int64(0), client.ZCard(context.Background(), oldReservation.leaseKey).Val())
}

func TestStrictCapacityRealRedisBoundsRenewalLifetimeAndReclaimsExpiredLease(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	coordinator := NewStrictCapacityCoordinator(client)
	request := strictCapacityEpochRequestForTest(
		"epoch-max-lifetime", 1, StrictCapacityDemand{Inflight: 1}, StrictCapacityLimit{Inflight: 1},
	)
	request.LeaseTTL = time.Second
	reservation, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, reservation.Commit(context.Background()))

	prefix := "r:" + reservation.token + ":"
	maxExpires := time.Now().Add(1200 * time.Millisecond).UnixMilli()
	require.NoError(t, client.HSet(context.Background(), reservation.capacityKey, prefix+"max_expires", maxExpires).Err())
	time.Sleep(400 * time.Millisecond)
	require.NoError(t, reservation.Renew(context.Background(), time.Second))
	assert.LessOrEqual(t, reservation.Admission().LeaseExpiresMs, maxExpires)
	time.Sleep(900 * time.Millisecond)
	assert.ErrorIs(t, reservation.Renew(context.Background(), time.Second), ErrStrictCapacityLost)

	recovered, err := coordinator.TryReserve(context.Background(), request)
	require.NoError(t, err, "the next admission must reclaim the expired bounded lease")
	require.NoError(t, recovered.Cancel(context.Background()))
}

func TestStrictCapacityRealRedisBoundsLeaseState(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	coordinator := NewStrictCapacityCoordinator(client)
	request := StrictCapacityRequest{
		Key:            StrictCapacityKey{AccountID: 41, CredentialID: 42, Model: "bounded-state"},
		PoolID:         1,
		PolicyRevision: 1,
		Demand:         StrictCapacityDemand{Inflight: 1},
		Limit:          StrictCapacityLimit{Inflight: strictCapacityMaximumLeases + 1},
		PoolShares: []StrictCapacityPoolShare{
			{PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000},
		},
		LeaseTTL: 30 * time.Second,
	}
	reservations := make([]*StrictCapacityReservation, 0, strictCapacityMaximumLeases)
	for range strictCapacityMaximumLeases {
		reservation, err := coordinator.TryReserve(context.Background(), request)
		require.NoError(t, err)
		reservations = append(reservations, reservation)
	}
	_, err := coordinator.TryReserve(context.Background(), request)
	assert.ErrorIs(t, err, ErrStrictCapacityStateLimit)
	for _, reservation := range reservations {
		require.NoError(t, reservation.Cancel(context.Background()))
	}
}

func strictCapacityRequestForTest(
	modelName string,
	poolID int,
	demand StrictCapacityDemand,
) StrictCapacityRequest {
	return StrictCapacityRequest{
		Key:    StrictCapacityKey{AccountID: 11, CredentialID: 21, Model: modelName},
		PoolID: poolID, PolicyRevision: 1, Demand: demand,
		Limit: StrictCapacityLimit{
			RPM: 10, InputTPM: 1_000, OutputTPM: 1_000, TotalTPM: 2_000,
			Inflight: 2, CostNanoUSD: 1_000_000_000,
		},
		PoolShares: []StrictCapacityPoolShare{
			{PoolID: 1, GuaranteedBasisPoints: 5_000, MaximumBasisPoints: 10_000},
			{PoolID: 2, GuaranteedBasisPoints: 5_000, MaximumBasisPoints: 10_000},
		},
		LeaseTTL: 30 * time.Second,
	}
}

func strictCapacityEpochRequestForTest(
	modelName string,
	revision uint64,
	demand StrictCapacityDemand,
	limit StrictCapacityLimit,
) StrictCapacityRequest {
	return StrictCapacityRequest{
		Key:    StrictCapacityKey{AccountID: 11, CredentialID: 21, Model: modelName},
		PoolID: 1, PolicyRevision: revision, Demand: demand, Limit: limit,
		PoolShares: []StrictCapacityPoolShare{{
			PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000,
		}},
		LeaseTTL: time.Second,
	}
}

type strictCapacityUnavailableRedis struct{}

func (*strictCapacityUnavailableRedis) Eval(context.Context, string, []string, ...interface{}) *redis.Cmd {
	return redis.NewCmdResult(nil, context.DeadlineExceeded)
}

type strictCapacityStateLimitRedis struct{}

func (*strictCapacityStateLimitRedis) Eval(context.Context, string, []string, ...interface{}) *redis.Cmd {
	return redis.NewCmdResult([]interface{}{int64(4)}, nil)
}
