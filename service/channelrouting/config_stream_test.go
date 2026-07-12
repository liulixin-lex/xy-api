package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingConfigOutboxPublishesStreamEventAndMarksRow(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingPolicyHead{}, &model.RoutingPolicyRevision{}, &model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{}, &model.RoutingPolicyActivation{}, &model.RoutingConfigOutbox{},
	))
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	published, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		testRoutingConfigPolicyDocument(),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1},
	)
	require.NoError(t, err)

	stub := &routingConfigRedisStub{}
	withRoutingConfigRedis(t, stub)
	ok, err := PublishRoutingConfigOutboxOnceContext(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	require.NotNil(t, stub.addArgs)
	assert.Equal(t, routingConfigStream, stub.addArgs.Stream)
	assert.Equal(t, routingConfigStreamMaxLen, stub.addArgs.MaxLen)

	var stored model.RoutingConfigOutbox
	require.NoError(t, db.First(&stored, published.Outbox.ID).Error)
	assert.Positive(t, stored.PublishedTime)
	assert.Empty(t, stored.ClaimToken)
	assert.Equal(t, 1, stored.Attempts)
}

func TestRoutingConfigConsumersUseIndependentXReadCursorAndConvergeIdempotently(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	event, message := testRoutingConfigMessage(t, 2, 1)
	refreshCalls := 0
	refreshRoutingConfigSnapshot = func(context.Context) (SnapshotView, error) {
		refreshCalls++
		return SnapshotView{Revision: uint64(event.Revision), PolicyHash: event.ContentHash}, nil
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) { return SnapshotMetadata{}, false }
	t.Cleanup(ResetRoutingConfigStreamForTest)

	for node := 0; node < 2; node++ {
		routingConfigState = newRoutingConfigStreamState()
		stub := &routingConfigRedisStub{readMessages: []redis.XMessage{message}}
		loadRoutingConfigRedis = func() routingConfigRedis { return stub }
		processed, err := ConsumeRoutingConfigOnceContext(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, processed)
		require.NotNil(t, stub.readArgs)
		assert.Equal(t, []string{routingConfigStream, "0-0"}, stub.readArgs.Streams)
		assert.Equal(t, message.ID, RoutingConfigStreamRuntimeStats().Cursor)
	}
	assert.Equal(t, 2, refreshCalls, "each node independently observes the config event")
}

func TestRoutingConfigStartupBaselinesAtStreamTailBeforeConsumingNewEvents(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	historyEvent, historyMessage := testRoutingConfigMessage(t, 3, 2)
	nextEvent, nextMessage := testRoutingConfigMessage(t, 4, 3)
	metadata := SnapshotMetadata{}
	metadataAvailable := false
	refreshCalls := 0
	refreshRoutingConfigSnapshot = func(context.Context) (SnapshotView, error) {
		refreshCalls++
		view := SnapshotView{Revision: uint64(historyEvent.Revision), PolicyHash: historyEvent.ContentHash}
		if refreshCalls > 1 {
			view = SnapshotView{Revision: uint64(nextEvent.Revision), PolicyHash: nextEvent.ContentHash}
		}
		metadata = SnapshotMetadata{Revision: view.Revision, PolicyHash: view.PolicyHash}
		metadataAvailable = true
		return view, nil
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) {
		return metadata, metadataAvailable
	}
	loadRoutingConfigPolicyHead = func(context.Context) (model.RoutingPolicyHead, error) {
		return model.RoutingPolicyHead{CurrentRevision: historyEvent.Revision, CurrentHash: historyEvent.ContentHash}, nil
	}
	stub := &routingConfigRedisStub{
		reverseMessages: []redis.XMessage{historyMessage},
		readMessages:    []redis.XMessage{nextMessage},
	}
	withRoutingConfigRedis(t, stub)

	processed, err := ConsumeRoutingConfigOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	require.NotNil(t, stub.readArgs)
	assert.Equal(t, []string{routingConfigStream, historyMessage.ID}, stub.readArgs.Streams)
	assert.Equal(t, nextMessage.ID, RoutingConfigStreamRuntimeStats().Cursor)
	assert.Equal(t, 2, refreshCalls)

	checkpoint, err := model.GetRoutingRuntimeCheckpointContext(
		context.Background(), NodeEpochID(), RoutingConfigCheckpointKind, RoutingConfigCheckpointScope,
	)
	require.NoError(t, err)
	assert.Equal(t, nextEvent.Revision, checkpoint.PolicyRevision)
}

func TestRoutingConfigOldRevisionNeverRollsSnapshotBack(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	_, message := testRoutingConfigMessage(t, 3, 2)
	refreshCalls := 0
	refreshRoutingConfigSnapshot = func(context.Context) (SnapshotView, error) {
		refreshCalls++
		return SnapshotView{}, errors.New("must not refresh an older event")
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) {
		return SnapshotMetadata{Revision: 5}, true
	}
	loadRoutingConfigPolicyHead = func(context.Context) (model.RoutingPolicyHead, error) {
		return model.RoutingPolicyHead{CurrentRevision: 5}, nil
	}
	stub := &routingConfigRedisStub{readMessages: []redis.XMessage{message}}
	withRoutingConfigRedis(t, stub)

	processed, err := ConsumeRoutingConfigOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Zero(t, refreshCalls)
	stats := RoutingConfigStreamRuntimeStats()
	assert.Equal(t, int64(1), stats.IgnoredOldRevision)
	assert.Equal(t, int64(5), stats.LastAppliedRevision)
}

func TestRoutingConfigColdNodeAcceptsDatabaseRevisionAheadOfStreamEvent(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	_, message := testRoutingConfigMessage(t, 3, 2)
	refreshCalls := 0
	metadataAvailable := false
	policyHash := strings.Repeat("b", 64)
	refreshRoutingConfigSnapshot = func(context.Context) (SnapshotView, error) {
		refreshCalls++
		metadataAvailable = true
		return SnapshotView{
			Revision: 5, PolicyHash: policyHash, ActivationID: 71,
			ActivationStage: model.RoutingDeploymentStageCanary, TrafficBasisPoints: 250,
		}, nil
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) {
		return SnapshotMetadata{
			Revision: 5, PolicyHash: policyHash, ActivationID: 71,
			ActivationStage: model.RoutingDeploymentStageCanary, TrafficBasisPoints: 250,
		}, metadataAvailable
	}
	stub := &routingConfigRedisStub{readMessages: []redis.XMessage{message}}
	withRoutingConfigRedis(t, stub)

	processed, err := ConsumeRoutingConfigOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Equal(t, 1, refreshCalls)
	stats := RoutingConfigStreamRuntimeStats()
	assert.Equal(t, int64(1), stats.IgnoredOldRevision)
	assert.Equal(t, int64(5), stats.LastAppliedRevision)
	assert.Equal(t, message.ID, stats.Cursor)
	checkpoint, err := model.GetRoutingRuntimeCheckpointContext(
		context.Background(), NodeEpochID(), RoutingConfigCheckpointKind, RoutingConfigCheckpointScope,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(5), checkpoint.PolicyRevision)
	var payload struct {
		PolicyHash         string `json:"policy_hash"`
		ActivationID       int64  `json:"activation_id"`
		ActivationStage    string `json:"activation_stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
	}
	require.NoError(t, checkpoint.DecodePayload(&payload))
	assert.Equal(t, policyHash, payload.PolicyHash)
	assert.Equal(t, int64(71), payload.ActivationID)
	assert.Equal(t, model.RoutingDeploymentStageCanary, payload.ActivationStage)
	assert.Equal(t, 250, payload.TrafficBasisPoints)
}

func TestRoutingConfigRejectsCorruptEventAndAdvancesPastPoisonMessage(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	_, message := testRoutingConfigMessage(t, 1, 0)
	message.Values["payload_hash"] = strings.Repeat("0", 64)
	stub := &routingConfigRedisStub{readMessages: []redis.XMessage{message}}
	withRoutingConfigRedis(t, stub)

	processed, err := ConsumeRoutingConfigOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	stats := RoutingConfigStreamRuntimeStats()
	assert.Equal(t, int64(1), stats.Rejected)
	assert.Equal(t, message.ID, stats.Cursor)
}

func TestRoutingConfigRejectsSelfConsistentUncommittedFutureRevision(t *testing.T) {
	db := openRoutingConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingRuntimeCheckpoint{}))
	_, message := testRoutingConfigMessage(t, math.MaxInt64, 1)
	refreshRoutingConfigSnapshot = func(context.Context) (SnapshotView, error) {
		return SnapshotView{Revision: 5, PolicyHash: strings.Repeat("b", 64)}, nil
	}
	loadRoutingConfigSnapshotMetadata = func() (SnapshotMetadata, bool) {
		return SnapshotMetadata{Revision: 5}, true
	}
	loadRoutingConfigPolicyHead = func(context.Context) (model.RoutingPolicyHead, error) {
		return model.RoutingPolicyHead{CurrentRevision: 5}, nil
	}
	stub := &routingConfigRedisStub{readMessages: []redis.XMessage{message}}
	withRoutingConfigRedis(t, stub)

	processed, err := ConsumeRoutingConfigOnceContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	stats := RoutingConfigStreamRuntimeStats()
	assert.Equal(t, int64(1), stats.Rejected)
	assert.Equal(t, message.ID, stats.Cursor)
}

func openRoutingConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingPolicyHead{}, &model.RoutingPolicyRevision{}))
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	ResetRoutingConfigStreamForTest()
	t.Cleanup(ResetRoutingConfigStreamForTest)
	return db
}

func testRoutingConfigPolicyDocument() model.RoutingPolicyDocument {
	return model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 1, GroupName: "default", DisplayName: "Default",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Members: []model.RoutingPolicyMemberContent{{
				MemberID: 1, ChannelID: 1, Enabled: true, Priority: 1, Weight: 1,
			}},
		}},
	}
}

func testRoutingConfigMessage(t *testing.T, revision int64, previous int64) (model.RoutingConfigEvent, redis.XMessage) {
	t.Helper()
	event := model.RoutingConfigEvent{
		SchemaVersion:    model.RoutingPolicySchemaVersion,
		EventID:          "event-" + strconv.FormatInt(revision, 10),
		Revision:         revision,
		PreviousRevision: previous,
		ContentHash:      strings.Repeat("a", 64),
		CreatedTime:      100,
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	return event, redis.XMessage{
		ID: strconv.FormatInt(revision, 10) + "-0",
		Values: map[string]interface{}{
			"event_id": event.EventID, "revision": strconv.FormatInt(revision, 10),
			"payload_hash": hex.EncodeToString(sum[:]), "payload": string(payload),
		},
	}
}

func withRoutingConfigRedis(t *testing.T, client routingConfigRedis) {
	t.Helper()
	loadRoutingConfigRedis = func() routingConfigRedis { return client }
	t.Cleanup(ResetRoutingConfigStreamForTest)
}

type routingConfigRedisStub struct {
	addArgs         *redis.XAddArgs
	addErr          error
	readArgs        *redis.XReadArgs
	readMessages    []redis.XMessage
	readErr         error
	reverseMessages []redis.XMessage
	reverseErr      error
}

func (stub *routingConfigRedisStub) XAdd(_ context.Context, args *redis.XAddArgs) *redis.StringCmd {
	stub.addArgs = args
	return redis.NewStringResult("1-0", stub.addErr)
}

func (stub *routingConfigRedisStub) XRead(_ context.Context, args *redis.XReadArgs) *redis.XStreamSliceCmd {
	stub.readArgs = args
	if stub.readErr != nil {
		return redis.NewXStreamSliceCmdResult(nil, stub.readErr)
	}
	return redis.NewXStreamSliceCmdResult([]redis.XStream{{Stream: routingConfigStream, Messages: stub.readMessages}}, nil)
}

func (stub *routingConfigRedisStub) XRevRangeN(_ context.Context, _ string, _ string, _ string, _ int64) *redis.XMessageSliceCmd {
	return redis.NewXMessageSliceCmdResult(stub.reverseMessages, stub.reverseErr)
}
