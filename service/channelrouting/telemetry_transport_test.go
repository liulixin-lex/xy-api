package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingTelemetryAmbiguousPublishFallsBackAndStreamRedeliveryIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)

	stub := &routingTelemetryRedisStub{xAddErr: errors.New("ambiguous redis timeout")}
	withRoutingTelemetryRedis(t, stub)
	envelope, err := newRoutingTelemetryEnvelope([]model.RoutingMetricRollup{testRoutingTelemetryRollup()}, time.UnixMilli(1_000))
	require.NoError(t, err)

	require.NoError(t, deliverRoutingTelemetryEnvelopeContext(context.Background(), envelope))
	var saved model.RoutingMetricRollup
	require.NoError(t, db.First(&saved).Error)
	assert.Equal(t, int64(1), saved.RequestCount)
	assert.Equal(t, int64(1), countRoutingTelemetryReceipts(t, db))

	applied, permanent, err := applyRoutingTelemetryMessageContext(context.Background(), redis.XMessage{
		ID:     "1-0",
		Values: map[string]interface{}{"payload": string(envelope.payload)},
	})
	require.NoError(t, err)
	assert.True(t, applied)
	assert.False(t, permanent)
	require.NoError(t, db.First(&saved).Error)
	assert.Equal(t, int64(1), saved.RequestCount, "the stream retry must be absorbed by the receipt")
	assert.Equal(t, int64(1), countRoutingTelemetryReceipts(t, db))
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.Equal(t, int64(1), stats.PublishFailures)
	assert.Equal(t, int64(1), stats.FallbackApplied)
	assert.Equal(t, int64(1), stats.DuplicateConsumed)
}

func TestRoutingTelemetryCommitBeforeAckFailureRedeliversWithoutDoubleCount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)

	envelope, err := newRoutingTelemetryEnvelope([]model.RoutingMetricRollup{testRoutingTelemetryRollup()}, time.UnixMilli(1_000))
	require.NoError(t, err)
	stub := &routingTelemetryRedisStub{
		readMessages: []redis.XMessage{{ID: "1-0", Values: map[string]interface{}{"payload": string(envelope.payload)}}},
		ackErrors:    []error{errors.New("ack connection lost"), nil},
	}
	withRoutingTelemetryRedis(t, stub)

	processed, err := ConsumeRoutingTelemetryOnceContext(context.Background())
	require.ErrorContains(t, err, "ack connection lost")
	assert.Zero(t, processed, "the message is not complete until ACK succeeds")
	processed, err = ConsumeRoutingTelemetryOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)

	var saved model.RoutingMetricRollup
	require.NoError(t, db.First(&saved).Error)
	assert.Equal(t, int64(1), saved.RequestCount)
	assert.Equal(t, int64(1), countRoutingTelemetryReceipts(t, db))
	assert.Equal(t, 2, stub.ackCalls)
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.Equal(t, int64(1), stats.AckFailures)
	assert.Equal(t, int64(1), stats.DuplicateConsumed)
}

func TestRoutingTelemetryPermanentlyInvalidStreamMessageIsRejectedAndAcked(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)

	stub := &routingTelemetryRedisStub{
		readMessages: []redis.XMessage{{ID: "1-0", Values: map[string]interface{}{"payload": "not-json"}}},
	}
	withRoutingTelemetryRedis(t, stub)

	processed, err := ConsumeRoutingTelemetryOnceContext(context.Background())
	require.NoError(t, err)
	assert.Zero(t, processed)
	assert.Equal(t, 1, stub.ackCalls)
	assert.Equal(t, int64(1), RoutingTelemetryTransportRuntimeStats().Rejected)
	assert.Zero(t, countRoutingTelemetryReceipts(t, db))
}

func TestRoutingTelemetryPipelineStatsExposePendingAndOldestIdle(t *testing.T) {
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)
	nowMs := time.Now().UnixMilli()
	stub := &routingTelemetryRedisStub{pending: []redis.XPendingExt{{
		ID: fmt.Sprintf("%d-0", nowMs-10_000), Consumer: "node-a", Idle: 12 * time.Second, RetryCount: 1,
	}}, groupLag: 3, lastDeliveredID: fmt.Sprintf("%d-0", nowMs-5_000), undeliveredMessages: []redis.XMessage{{
		ID: fmt.Sprintf("%d-0", nowMs-4_000),
	}}}
	withRoutingTelemetryRedis(t, stub)

	processed, err := ConsumeRoutingTelemetryOnceContext(context.Background())
	require.NoError(t, err)
	assert.Zero(t, processed)
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.True(t, stats.PipelineAvailable)
	assert.True(t, stats.PipelineLagAvailable)
	assert.Equal(t, int64(1), stats.PipelinePending)
	assert.Equal(t, int64(3), stats.PipelineUndelivered)
	assert.Equal(t, int64(4), stats.PipelineBacklog)
	assert.Equal(t, int64((12*time.Second)/time.Millisecond), stats.PipelineOldestIdleMs)
	assert.GreaterOrEqual(t, stats.PipelineOldestMessageAgeMs, int64(9_000))
	assert.Equal(t, stub.lastDeliveredID, stats.PipelineLastDeliveredID)
	assert.Positive(t, stats.PipelineCheckedAtMs)
}

func TestRoutingTelemetryPipelineStatsMarkRedisUnavailable(t *testing.T) {
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)
	withRoutingTelemetryRedis(t, nil)

	_, err := ConsumeRoutingTelemetryOnceContext(context.Background())
	assert.ErrorIs(t, err, ErrRoutingTelemetryUnavailable)
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.False(t, stats.PipelineAvailable)
	assert.Contains(t, stats.PipelineLastError, "unavailable")
}

func TestRoutingTelemetryUsesUnversionedStreamAndConsumerGroup(t *testing.T) {
	ResetRoutingTelemetryTransportForTest()
	t.Cleanup(ResetRoutingTelemetryTransportForTest)
	stub := &routingTelemetryRedisStub{}
	withRoutingTelemetryRedis(t, stub)

	envelope, err := newRoutingTelemetryEnvelope([]model.RoutingMetricRollup{testRoutingTelemetryRollup()}, time.Now())
	require.NoError(t, err)
	require.NoError(t, deliverRoutingTelemetryEnvelopeContext(context.Background(), envelope))
	_, err = ConsumeRoutingTelemetryOnceContext(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "channel-routing:telemetry", stub.addStream)
	assert.Equal(t, "channel-routing:telemetry", stub.groupStream)
	assert.Equal(t, "channel-routing-rollup", stub.groupName)
	assert.Equal(t, "0", stub.groupStart)
}

func testRoutingTelemetryRollup() model.RoutingMetricRollup {
	return model.RoutingMetricRollup{
		MemberID: 1, CredentialID: 0, ModelName: "gpt-test", BucketTs: 60,
		ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1, SuccessCount: 1,
	}
}

func countRoutingTelemetryReceipts(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.RoutingTelemetryReceipt{}).Count(&count).Error)
	return count
}

func withRoutingTelemetryRedis(t *testing.T, client routingTelemetryRedis) {
	t.Helper()
	previous := loadRoutingTelemetryRedis
	loadRoutingTelemetryRedis = func() routingTelemetryRedis { return client }
	t.Cleanup(func() { loadRoutingTelemetryRedis = previous })
}

type routingTelemetryRedisStub struct {
	xAddErr             error
	addStream           string
	groupStream         string
	groupName           string
	groupStart          string
	readMessages        []redis.XMessage
	readCalls           int
	pending             []redis.XPendingExt
	groupLag            int64
	lastDeliveredID     string
	undeliveredMessages []redis.XMessage
	ackErrors           []error
	ackCalls            int
}

func (stub *routingTelemetryRedisStub) Do(ctx context.Context, _ ...interface{}) *redis.Cmd {
	lastDeliveredID := stub.lastDeliveredID
	if lastDeliveredID == "" {
		lastDeliveredID = "0-0"
	}
	return redis.NewCmdResult([]interface{}{[]interface{}{
		"name", routingTelemetryConsumerGroup,
		"last-delivered-id", lastDeliveredID,
		"lag", stub.groupLag,
	}}, nil)
}

func (stub *routingTelemetryRedisStub) XAdd(_ context.Context, args *redis.XAddArgs) *redis.StringCmd {
	stub.addStream = args.Stream
	return redis.NewStringResult("1-0", stub.xAddErr)
}

func (stub *routingTelemetryRedisStub) XGroupCreateMkStream(
	_ context.Context,
	stream string,
	group string,
	start string,
) *redis.StatusCmd {
	stub.groupStream = stream
	stub.groupName = group
	stub.groupStart = start
	return redis.NewStatusResult("OK", nil)
}

func (stub *routingTelemetryRedisStub) XReadGroup(context.Context, *redis.XReadGroupArgs) *redis.XStreamSliceCmd {
	stub.readCalls++
	return redis.NewXStreamSliceCmdResult([]redis.XStream{{Stream: routingTelemetryStream, Messages: stub.readMessages}}, nil)
}

func (stub *routingTelemetryRedisStub) XPending(ctx context.Context, _, _ string) *redis.XPendingCmd {
	command := redis.NewXPendingCmd(ctx)
	summary := &redis.XPending{Count: int64(len(stub.pending))}
	if len(stub.pending) > 0 {
		summary.Lower = stub.pending[0].ID
		summary.Higher = stub.pending[len(stub.pending)-1].ID
	}
	command.SetVal(summary)
	return command
}

func (stub *routingTelemetryRedisStub) XPendingExt(ctx context.Context, _ *redis.XPendingExtArgs) *redis.XPendingExtCmd {
	command := redis.NewXPendingExtCmd(ctx)
	command.SetVal(stub.pending)
	return command
}

func (stub *routingTelemetryRedisStub) XRangeN(context.Context, string, string, string, int64) *redis.XMessageSliceCmd {
	return redis.NewXMessageSliceCmdResult(stub.undeliveredMessages, nil)
}

func (stub *routingTelemetryRedisStub) XClaim(context.Context, *redis.XClaimArgs) *redis.XMessageSliceCmd {
	return redis.NewXMessageSliceCmdResult(nil, nil)
}

func (stub *routingTelemetryRedisStub) XAck(context.Context, string, string, ...string) *redis.IntCmd {
	stub.ackCalls++
	var err error
	if stub.ackCalls <= len(stub.ackErrors) {
		err = stub.ackErrors[stub.ackCalls-1]
	}
	return redis.NewIntResult(1, err)
}
