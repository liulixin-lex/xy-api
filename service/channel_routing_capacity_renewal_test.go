package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictCapacityRenewalFailureCancelsUpstreamRequest(t *testing.T) {
	interval, timeout := strictRoutingCapacityRenewalTiming(time.Second)
	assert.Less(t, interval+timeout, time.Second, "renewal failure must be known before the lease expires")

	fake := &strictRenewalFailureRedis{}
	coordinator := channelrouting.NewStrictCapacityCoordinator(fake)
	reservation, err := coordinator.TryReserve(context.Background(), strictCapacityRenewalRequest("fake"))
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = requestWithContextForStrictRenewalTest(context.Background())
	require.NoError(t, SetRoutingStrictCapacityReservation(ctx, reservation))
	require.NoError(t, CommitRoutingCapacityReservation(ctx))

	select {
	case <-ctx.Request.Context().Done():
	case <-time.After(3 * time.Second):
		require.Fail(t, "strict capacity renewal failure did not cancel the upstream request")
	}
	cause := context.Cause(ctx.Request.Context())
	require.Error(t, cause)
	assert.ErrorIs(t, cause, channelrouting.ErrStrictCapacityUnavailable)
	assert.ErrorIs(t, RoutingCapacityReservationFailure(ctx), channelrouting.ErrStrictCapacityUnavailable)
	require.NoError(t, ReleaseRoutingCapacityReservation(ctx))
}

func TestStrictCapacityPendingReservationRenewsBeforeCommit(t *testing.T) {
	fake := newStrictRenewalObservedRedis()
	coordinator := channelrouting.NewStrictCapacityCoordinator(fake)
	reservation, err := coordinator.TryReserve(context.Background(), strictCapacityRenewalRequest("pending"))
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = requestWithContextForStrictRenewalTest(context.Background())
	require.NoError(t, SetRoutingStrictCapacityReservation(ctx, reservation))

	select {
	case <-fake.renewed:
	case <-time.After(3 * time.Second):
		require.Fail(t, "pending strict-capacity reservation was not renewed before commit")
	}
	require.NoError(t, CancelRoutingCapacityReservation(ctx))
}

func TestStrictCapacityRealRedisDisconnectCancelsUpstreamRequest(t *testing.T) {
	address := os.Getenv("ROUTING_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("ROUTING_TEST_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	require.NoError(t, client.Ping(context.Background()).Err())
	coordinator := channelrouting.NewStrictCapacityCoordinator(client)
	reservation, err := coordinator.TryReserve(context.Background(), strictCapacityRenewalRequest("real-redis"))
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(nil)
	ctx.Request = requestWithContextForStrictRenewalTest(context.Background())
	require.NoError(t, SetRoutingStrictCapacityReservation(ctx, reservation))
	require.NoError(t, CommitRoutingCapacityReservation(ctx))
	require.NoError(t, client.Close())

	select {
	case <-ctx.Request.Context().Done():
	case <-time.After(3 * time.Second):
		require.Fail(t, "redis disconnect did not cancel the strict-capacity upstream request")
	}
	assert.ErrorIs(t, context.Cause(ctx.Request.Context()), channelrouting.ErrStrictCapacityUnavailable)
}

func strictCapacityRenewalRequest(suffix string) channelrouting.StrictCapacityRequest {
	return channelrouting.StrictCapacityRequest{
		Key:    channelrouting.StrictCapacityKey{AccountID: 9001, CredentialID: 9002, Model: "renewal-" + suffix},
		PoolID: 1, PolicyRevision: 1,
		Demand: channelrouting.StrictCapacityDemand{RPM: 1, Inflight: 1},
		Limit:  channelrouting.StrictCapacityLimit{RPM: 10, Inflight: 1},
		PoolShares: []channelrouting.StrictCapacityPoolShare{
			{PoolID: 1, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000},
		},
		LeaseTTL: time.Second,
	}
}

func requestWithContextForStrictRenewalTest(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
}

type strictRenewalFailureRedis struct{}

func (*strictRenewalFailureRedis) Eval(
	_ context.Context,
	script string,
	_ []string,
	args ...interface{},
) *redis.Cmd {
	if strings.Contains(script, "strict_capacity_reserve") {
		now := time.Now().UnixMilli()
		lease, _ := args[1].(int64)
		return redis.NewCmdResult([]interface{}{int64(1), now, now + lease}, nil)
	}
	if strings.Contains(script, "current_expires") {
		return redis.NewCmdResult(nil, errors.New("redis unavailable"))
	}
	return redis.NewCmdResult(int64(1), nil)
}

type strictRenewalObservedRedis struct {
	renewed chan struct{}
}

func newStrictRenewalObservedRedis() *strictRenewalObservedRedis {
	return &strictRenewalObservedRedis{renewed: make(chan struct{}, 1)}
}

func (fake *strictRenewalObservedRedis) Eval(
	_ context.Context,
	script string,
	_ []string,
	args ...interface{},
) *redis.Cmd {
	if strings.Contains(script, "strict_capacity_reserve") {
		now := time.Now().UnixMilli()
		lease, _ := args[1].(int64)
		return redis.NewCmdResult([]interface{}{int64(1), now, now + lease}, nil)
	}
	if strings.Contains(script, "current_expires") {
		select {
		case fake.renewed <- struct{}{}:
		default:
		}
		return redis.NewCmdResult(time.Now().Add(time.Second).UnixMilli(), nil)
	}
	return redis.NewCmdResult(int64(1), nil)
}
