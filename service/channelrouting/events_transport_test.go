package channelrouting

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingRuntimeSettingsEventRefreshesRemoteNodeBeforeFanout(t *testing.T) {
	db := breakerResetServiceTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Option{}, &model.RoutingRuntimeSettingsState{}, &model.RoutingControlAudit{},
	))
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		refreshRoutingRuntimeSettingsOptions = model.RefreshOptionsFromDatabaseChecked
	})

	initial := smart_routing_setting.GetStoredSetting()
	initialDocument, err := common.Marshal(initial)
	require.NoError(t, err)
	initialState, err := model.GetOrReconcileRoutingRuntimeSettingsStateContext(
		context.Background(), string(initialDocument), model.RoutingRuntimeSettingsDocumentHash(initialDocument),
	)
	require.NoError(t, err)

	updated := initial
	updated.Enabled = true
	updated.Mode = smart_routing_setting.ModeBalanced
	updated.TopK = 7
	updated = smart_routing_setting.Normalize(updated)
	updatedDocument, err := common.Marshal(updated)
	require.NoError(t, err)
	values, err := config.ConfigToMap(updated)
	require.NoError(t, err)
	persisted := make(map[string]string, len(values))
	for key, value := range values {
		persisted["smart_routing_setting."+key] = value
	}
	updatedState, err := model.UpdateRoutingRuntimeSettingsContext(
		context.Background(), initialState.Revision, initialState.DocumentHash,
		string(updatedDocument), model.RoutingRuntimeSettingsDocumentHash(updatedDocument), persisted, 71,
	)
	require.NoError(t, err)
	assert.False(t, smart_routing_setting.GetStoredSetting().Enabled)

	payload, err := common.Marshal(map[string]any{
		"revision": updatedState.Revision, "document_hash": updatedState.DocumentHash, "updated_by": 71,
	})
	require.NoError(t, err)
	client := &routingEventRedisMemory{}
	state := newRoutingEventTransportState()
	hub := newRoutingEventHub(8)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), state, client))
	require.NoError(t, broadcastRoutingEventContext(
		context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), RoutingEvent{
			ID: 1, Type: RoutingEventTypeRuntimeSettingsChanged,
			Revision: uint64(updatedState.Revision), CreatedTimeMs: time.Now().UnixMilli(), PayloadJSON: payload,
		},
	))

	refreshFailure := errors.New("temporary option read failure")
	refreshRoutingRuntimeSettingsOptions = func() error { return refreshFailure }
	processed, err := consumeRoutingEventsOnceContext(
		context.Background(), hub, state, client, strings.Repeat("b", 32),
	)
	require.ErrorIs(t, err, refreshFailure)
	assert.Zero(t, processed)
	assert.Equal(t, "0-0", state.snapshot().Cursor, "retryable refresh failure must not advance the Redis cursor")
	assert.Zero(t, hub.stats().Buffered, "stale settings must not be fanned out to local SSE clients")

	refreshRoutingRuntimeSettingsOptions = model.RefreshOptionsFromDatabaseChecked
	processed, err = consumeRoutingEventsOnceContext(
		context.Background(), hub, state, client, strings.Repeat("b", 32),
	)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Equal(t, "1-0", state.snapshot().Cursor)
	assert.Equal(t, updated, smart_routing_setting.GetStoredSetting())
	replay, _, cancel, err := hub.subscribe(0, 1, true)
	require.NoError(t, err)
	cancel()
	require.Len(t, replay.Events, 1)
	assert.Equal(t, RoutingEventTypeRuntimeSettingsChanged, replay.Events[0].Type)
	assert.Equal(t, uint64(updatedState.Revision), replay.Events[0].Revision)
}

func TestRoutingEventTransportBroadcastsAcrossIndependentNodeHubs(t *testing.T) {
	assert.Equal(t, "channel-routing:events", routingEventRedisStream)

	client := &routingEventRedisMemory{}
	nodeA := strings.Repeat("a", 32)
	nodeB := strings.Repeat("b", 32)
	stateA := newRoutingEventTransportState()
	stateB := newRoutingEventTransportState()
	hubA := newRoutingEventHub(8)
	hubB := newRoutingEventHub(8)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), stateA, client))
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), stateB, client))

	event, err := hubA.publish(
		RoutingEventTypePolicyPublished, 9, []byte(`{"operation_id":71,"stage":"active"}`), time.Unix(1_700_000_000, 0),
	)
	require.NoError(t, err)
	require.NoError(t, broadcastRoutingEventContext(context.Background(), stateA, client, nodeA, event))

	processed, err := consumeRoutingEventsOnceContext(context.Background(), hubB, stateB, client, nodeB)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	replayB, _, cancelB, err := hubB.subscribe(0, 1, true)
	require.NoError(t, err)
	cancelB()
	require.Len(t, replayB.Events, 1)
	assert.Equal(t, RoutingEventTypePolicyPublished, replayB.Events[0].Type)
	assert.Equal(t, uint64(9), replayB.Events[0].Revision)
	assert.JSONEq(t, `{"operation_id":71,"stage":"active"}`, string(replayB.Events[0].PayloadJSON))
	assert.Equal(t, uint64(1), stateB.snapshot().Consumed)

	processed, err = consumeRoutingEventsOnceContext(context.Background(), hubA, stateA, client, nodeA)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Equal(t, 1, hubA.stats().Buffered, "the origin must not fan out its Redis echo twice")
	assert.Equal(t, uint64(1), stateA.snapshot().IgnoredOwn)
}

func TestRoutingEventTransportAdvancesPastPoisonMessage(t *testing.T) {
	client := &routingEventRedisMemory{}
	state := newRoutingEventTransportState()
	hub := newRoutingEventHub(4)
	require.NoError(t, initializeRoutingEventTransportContext(context.Background(), state, client))

	event, err := hub.publish("routing.changed", 1, []byte(`{"resource":"overview"}`), time.Unix(1_700_000_000, 0))
	require.NoError(t, err)
	require.NoError(t, broadcastRoutingEventContext(
		context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), event,
	))
	client.mu.Lock()
	client.messages[0].Values["payload_hash"] = strings.Repeat("0", 64)
	client.mu.Unlock()

	processed, err := consumeRoutingEventsOnceContext(
		context.Background(), hub, state, client, strings.Repeat("b", 32),
	)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	stats := state.snapshot()
	assert.Equal(t, uint64(1), stats.Rejected)
	assert.Equal(t, "1-0", stats.Cursor)
	assert.Equal(t, 1, hub.stats().Buffered, "a poison remote event must not alter the local ring")

	processed, err = consumeRoutingEventsOnceContext(
		context.Background(), hub, state, client, strings.Repeat("b", 32),
	)
	require.NoError(t, err)
	assert.Zero(t, processed)
}

func TestRoutingEventTransportDeduplicatesBreakerResetGeneration(t *testing.T) {
	for _, mode := range []string{"ttl", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			db := breakerResetServiceTestDB(t)
			resetRoutingBreakerResetRuntimeForTest()
			t.Cleanup(func() {
				resetRoutingBreakerResetRuntimeForTest()
				routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
			})
			now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
			routingbreaker.ResetDefaultForTest(routingbreaker.Config{
				Consecutive5xxThreshold: 1, FailureRateThreshold: 1, FailureRateMinSamples: 1,
				WindowSize: 4, EntryTTL: time.Hour, MaxEntries: 16,
				ResetGenerationTTL: time.Minute, MaxResetGenerations: 1,
				Now: func() time.Time { return now },
			})
			target := model.RoutingBreakerResetTarget{
				Scope: model.RoutingBreakerResetScopeMember, PoolID: 1, MemberID: 2, ChannelID: 3,
				APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-reset", GroupName: "default",
			}
			seedBreakerResetServicePolicy(t, db, target)
			_, _, err := model.CreateRoutingBreakerResetOperationContext(context.Background(), model.RoutingOperationSpec{
				Type: model.RoutingOperationTypeBreakerReset, EvaluationHash: strings.Repeat("a", 64),
				SubjectType: model.RoutingOperationSubjectMemberBreaker, SubjectID: int64(target.MemberID), PoolID: target.PoolID,
				ExpectedRevision: 1, ExpectedActivationID: 1, Reason: "transport reset",
			}, target)
			require.NoError(t, err)
			nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
			require.NoError(t, err)
			claimed, err := model.ClaimRoutingOperationContext(
				context.Background(), model.RoutingOperationTypeBreakerReset, nowMs, 30_000,
			)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			execution, err := model.ExecuteRoutingBreakerResetOperationContext(context.Background(), *claimed)
			require.NoError(t, err)
			payload := []byte(execution.Outbox.PayloadJSON)

			client := &routingEventRedisMemory{}
			state := newRoutingEventTransportState()
			hub := newRoutingEventHub(8)
			require.NoError(t, initializeRoutingEventTransportContext(context.Background(), state, client))
			for sequence := uint64(1); sequence <= 2; sequence++ {
				require.NoError(t, broadcastRoutingEventContext(
					context.Background(), newRoutingEventTransportState(), client, strings.Repeat("a", 32), RoutingEvent{
						ID: sequence, Type: RoutingEventTypeBreakerReset,
						CreatedTimeMs: execution.Event.ResetAtMs, PayloadJSON: payload,
					},
				))
			}
			key := routingbreaker.Key{
				ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
				Model: target.ModelName, Group: target.GroupName,
			}
			require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(
				key, routingbreaker.FailureProvider5xx,
			).State)
			forged := execution.Event
			forged.Generation++
			forgedPayload, err := common.Marshal(forged)
			require.NoError(t, err)
			_, err = applyDurableRoutingBreakerResetEventPayloadContext(context.Background(), forgedPayload)
			assert.ErrorIs(t, err, model.ErrRoutingBreakerResetInvalid)
			assert.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordAttempt(
				key, false, 429, 0,
			).State)
			processed, err := consumeRoutingEventsOnceContext(
				context.Background(), hub, state, client, strings.Repeat("b", 32),
			)
			require.NoError(t, err)
			assert.Equal(t, 2, processed)
			assert.Equal(t, 1, hub.stats().Buffered)
			assert.Equal(t, uint64(2), state.snapshot().Consumed)

			routingbreaker.ClearDefaultKey(key)
			if mode == "ttl" {
				now = now.Add(2 * time.Minute)
				routingbreaker.RuntimeStats()
			} else {
				now = now.Add(time.Second)
				otherKey := routingbreaker.Key{
					ChannelID: 4, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
					Model: "gpt-other", Group: "default",
				}
				_, applied := routingbreaker.ApplyDefaultResetGeneration(otherKey, 1)
				require.True(t, applied)
				routingbreaker.ClearDefaultKey(otherKey)
			}
			assert.Zero(t, routingbreaker.DefaultResetGeneration(key))
			require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(
				key, routingbreaker.FailureProvider5xx,
			).State)

			require.NoError(t, broadcastRoutingEventContext(
				context.Background(), newRoutingEventTransportState(), client, strings.Repeat("c", 32), RoutingEvent{
					ID: 3, Type: RoutingEventTypeBreakerReset,
					CreatedTimeMs: execution.Event.ResetAtMs, PayloadJSON: payload,
				},
			))
			processed, err = consumeRoutingEventsOnceContext(
				context.Background(), hub, state, client, strings.Repeat("b", 32),
			)
			require.NoError(t, err)
			assert.Equal(t, 1, processed)
			assert.Equal(t, 1, hub.stats().Buffered)
			dirty := routingbreaker.DirtySnapshots()
			require.Len(t, dirty, 1)
			assert.Equal(t, key, dirty[0].Key)
			assert.Equal(t, routingbreaker.StateOpen, dirty[0].State)
		})
	}
}

type routingEventRedisMemory struct {
	mu       sync.Mutex
	messages []redis.XMessage
}

func (memory *routingEventRedisMemory) XAdd(_ context.Context, args *redis.XAddArgs) *redis.StringCmd {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	id := strconv.Itoa(len(memory.messages)+1) + "-0"
	values := make(map[string]any)
	switch fields := args.Values.(type) {
	case []interface{}:
		for index := 0; index+1 < len(fields); index += 2 {
			key, ok := fields[index].(string)
			if ok {
				values[key] = fields[index+1]
			}
		}
	case map[string]interface{}:
		for key, value := range fields {
			values[key] = value
		}
	}
	memory.messages = append(memory.messages, redis.XMessage{ID: id, Values: values})
	return redis.NewStringResult(id, nil)
}

func (memory *routingEventRedisMemory) XRead(_ context.Context, args *redis.XReadArgs) *redis.XStreamSliceCmd {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	cursor := "0-0"
	if len(args.Streams) >= 2 {
		cursor = args.Streams[1]
	}
	start := 0
	if cursor != "0-0" {
		start = len(memory.messages)
		for index := range memory.messages {
			if memory.messages[index].ID == cursor {
				start = index + 1
				break
			}
		}
	}
	if start >= len(memory.messages) {
		return redis.NewXStreamSliceCmdResult(nil, redis.Nil)
	}
	messages := append([]redis.XMessage(nil), memory.messages[start:]...)
	if args.Count > 0 && int64(len(messages)) > args.Count {
		messages = messages[:args.Count]
	}
	return redis.NewXStreamSliceCmdResult([]redis.XStream{{Stream: routingEventRedisStream, Messages: messages}}, nil)
}

func (memory *routingEventRedisMemory) XRevRangeN(_ context.Context, _ string, _ string, _ string, count int64) *redis.XMessageSliceCmd {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if len(memory.messages) == 0 || count <= 0 {
		return redis.NewXMessageSliceCmdResult(nil, nil)
	}
	return redis.NewXMessageSliceCmdResult([]redis.XMessage{memory.messages[len(memory.messages)-1]}, nil)
}
