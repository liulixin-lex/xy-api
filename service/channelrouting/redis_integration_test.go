package channelrouting

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingConfigRealRedisBroadcastsToIndependentNodeCursors(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	db := routingRedisIntegrationDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
		&model.RoutingRuntimeCheckpoint{},
	))
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	published, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		testRoutingConfigPolicyDocument(),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1},
	)
	require.NoError(t, err)

	loadRoutingConfigRedis = func() routingConfigRedis { return client }
	refreshCalls := 0
	refreshRoutingConfigSnapshot = func(ctx context.Context) (SnapshotView, error) {
		refreshCalls++
		head, headErr := model.GetRoutingPolicyHeadContext(ctx)
		if headErr != nil {
			return SnapshotView{}, headErr
		}
		return SnapshotView{
			Revision:   uint64(head.CurrentRevision),
			PolicyHash: head.CurrentHash,
		}, nil
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) {
		return SnapshotMetadata{}, false
	}
	t.Cleanup(ResetRoutingConfigStreamForTest)

	ok, err := PublishRoutingConfigOutboxOnceContext(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	states := make([]*routingConfigStreamState, 2)
	for node := 0; node < 2; node++ {
		states[node] = newRoutingConfigStreamState()
		routingConfigState = states[node]
		require.NoError(t, initializeRoutingConfigCursorContext(context.Background(), client))
		assert.NotEqual(t, "0-0", RoutingConfigStreamRuntimeStats().Cursor)
	}
	assert.Equal(t, 2, refreshCalls)

	second, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(),
		published.Revision.Revision,
		testRoutingConfigPolicyDocument(),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1},
	)
	require.NoError(t, err)
	ok, err = PublishRoutingConfigOutboxOnceContext(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	for node := 0; node < 2; node++ {
		routingConfigState = states[node]
		processed, consumeErr := ConsumeRoutingConfigOnceContext(context.Background())
		require.NoError(t, consumeErr)
		assert.Equal(t, 1, processed)
		assert.Equal(t, second.Revision.Revision, RoutingConfigStreamRuntimeStats().LastAppliedRevision)
	}
	assert.Equal(t, 4, refreshCalls)
}

func TestRoutingTelemetryRealRedisCommitBeforeAckRedeliveryIsIdempotent(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	db := routingRedisIntegrationDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)

	envelope, err := newRoutingTelemetryEnvelope(
		[]model.RoutingMetricRollup{testRoutingTelemetryRollup()},
		time.Now(),
	)
	require.NoError(t, err)
	_, err = client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: routingTelemetryStream,
		Values: map[string]interface{}{"payload": string(envelope.payload)},
	}).Result()
	require.NoError(t, err)
	require.NoError(t, ensureRoutingTelemetryConsumerGroup(context.Background(), client))

	streams, err := client.XReadGroup(context.Background(), &redis.XReadGroupArgs{
		Group:    routingTelemetryConsumerGroup,
		Consumer: "crashed-node",
		Streams:  []string{routingTelemetryStream, ">"},
		Count:    1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Messages, 1)
	message := streams[0].Messages[0]

	applied, permanent, err := applyRoutingTelemetryMessageContext(context.Background(), message)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.False(t, permanent)

	claimed, err := client.XClaim(context.Background(), &redis.XClaimArgs{
		Stream:   routingTelemetryStream,
		Group:    routingTelemetryConsumerGroup,
		Consumer: "recovery-node",
		MinIdle:  0,
		Messages: []string{message.ID},
	}).Result()
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	applied, permanent, err = applyRoutingTelemetryMessageContext(context.Background(), claimed[0])
	require.NoError(t, err)
	assert.True(t, applied)
	assert.False(t, permanent)
	_, err = client.XAck(
		context.Background(), routingTelemetryStream, routingTelemetryConsumerGroup, message.ID,
	).Result()
	require.NoError(t, err)

	var rollup model.RoutingMetricRollup
	require.NoError(t, db.First(&rollup).Error)
	assert.Equal(t, int64(1), rollup.RequestCount)
	assert.Equal(t, int64(1), countRoutingTelemetryReceipts(t, db))
	assert.Equal(t, int64(1), RoutingTelemetryTransportRuntimeStats().DuplicateConsumed)
}

func TestRoutingTelemetryRealRedisReportsPendingAndUndeliveredBacklog(t *testing.T) {
	client := routingRedisIntegrationClient(t)
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)
	require.NoError(t, ensureRoutingTelemetryConsumerGroup(context.Background(), client))
	baseID := time.Now().Add(-time.Second).UnixMilli()
	for index := 0; index < 3; index++ {
		_, err := client.XAdd(context.Background(), &redis.XAddArgs{
			Stream: routingTelemetryStream,
			ID:     fmt.Sprintf("%d-0", baseID+int64(index)),
			Values: map[string]interface{}{"payload": "{}"},
		}).Result()
		require.NoError(t, err)
	}

	refreshRoutingTelemetryPipelineStatsContext(context.Background(), client)
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.True(t, stats.PipelineAvailable)
	assert.True(t, stats.PipelineLagAvailable)
	assert.Zero(t, stats.PipelinePending)
	assert.Equal(t, int64(3), stats.PipelineUndelivered)
	assert.Equal(t, int64(3), stats.PipelineBacklog)

	streams, err := client.XReadGroup(context.Background(), &redis.XReadGroupArgs{
		Group: routingTelemetryConsumerGroup, Consumer: "lag-test",
		Streams: []string{routingTelemetryStream, ">"}, Count: 1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Messages, 1)

	refreshRoutingTelemetryPipelineStatsContext(context.Background(), client)
	stats = RoutingTelemetryTransportRuntimeStats()
	assert.Equal(t, int64(1), stats.PipelinePending)
	assert.Equal(t, int64(2), stats.PipelineUndelivered)
	assert.Equal(t, int64(3), stats.PipelineBacklog)
	assert.Positive(t, stats.PipelineOldestMessageAgeMs)
}

func routingRedisIntegrationClient(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("ROUTING_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("ROUTING_TEST_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	require.NoError(t, client.Ping(context.Background()).Err())
	require.NoError(t, client.FlushDB(context.Background()).Err())
	t.Cleanup(func() {
		_ = client.FlushDB(context.Background()).Err()
		_ = client.Close()
	})
	return client
}

func routingRedisIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	withSnapshotTestDB(t, db)
	common.RedisEnabled = true
	t.Cleanup(func() { common.RedisEnabled = false })
	return db
}
