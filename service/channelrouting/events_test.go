package channelrouting

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingEventHubRejectsUnsafeOrUnboundedEvents(t *testing.T) {
	hub := newRoutingEventHub(3)
	now := time.Unix(1_700_000_000, 0)

	event, err := hub.publish(" routing.policy_published ", 7, []byte(`{"operation_id":11}`), now)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), event.ID)
	assert.Equal(t, "routing.policy_published", event.Type)
	assert.Equal(t, uint64(7), event.Revision)
	assert.Equal(t, now.UnixMilli(), event.CreatedTimeMs)

	tests := []struct {
		name      string
		eventType string
		payload   []byte
	}{
		{name: "empty type", eventType: "", payload: []byte(`{}`)},
		{name: "sse injection", eventType: "routing.ok\nevent: stolen", payload: []byte(`{}`)},
		{name: "uppercase type", eventType: "Routing.ok", payload: []byte(`{}`)},
		{name: "oversized type", eventType: strings.Repeat("a", routingEventTypeMaximumBytes+1), payload: []byte(`{}`)},
		{name: "array payload", eventType: "routing.ok", payload: []byte(`[]`)},
		{name: "null payload", eventType: "routing.ok", payload: []byte(`null`)},
		{name: "malformed payload", eventType: "routing.ok", payload: []byte(`{`)},
		{name: "oversized payload", eventType: "routing.ok", payload: []byte(`{"value":"` + strings.Repeat("a", routingEventPayloadMaximum) + `"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := hub.publish(test.eventType, 0, test.payload, now)
			require.ErrorIs(t, err, ErrRoutingEventInvalid)
		})
	}
}

func TestRoutingEventHubReplaysCursorAndDetectsGaps(t *testing.T) {
	hub := newRoutingEventHub(3)
	for index := 1; index <= 5; index++ {
		_, err := hub.publish("routing.changed", uint64(index), []byte(`{"resource":"overview"}`), time.Unix(int64(index), 0))
		require.NoError(t, err)
	}

	replay, _, cancel, err := hub.subscribe(2, 1, true)
	require.NoError(t, err)
	cancel()
	require.False(t, replay.Gap)
	assert.Equal(t, uint64(3), replay.EarliestID)
	assert.Equal(t, uint64(5), replay.LatestID)
	require.Len(t, replay.Events, 3)
	assert.Equal(t, []uint64{3, 4, 5}, []uint64{replay.Events[0].ID, replay.Events[1].ID, replay.Events[2].ID})

	gap, _, cancelGap, err := hub.subscribe(1, 1, true)
	require.NoError(t, err)
	cancelGap()
	assert.True(t, gap.Gap)
	assert.Empty(t, gap.Events)

	future, _, cancelFuture, err := hub.subscribe(6, 1, true)
	require.NoError(t, err)
	cancelFuture()
	assert.True(t, future.Gap)

	fresh, _, cancelFresh, err := hub.subscribe(0, 1, true)
	require.NoError(t, err)
	cancelFresh()
	assert.False(t, fresh.Gap)
	assert.Len(t, fresh.Events, 3)

	stats := hub.stats()
	assert.Equal(t, 3, stats.Buffered)
	assert.Equal(t, 3, stats.Capacity)
	assert.Equal(t, uint64(5), stats.LatestID)
	assert.Equal(t, uint64(2), stats.Evicted)
	assert.Zero(t, stats.Subscribers)
}

func TestRoutingEventHubDisconnectsSlowSubscriber(t *testing.T) {
	hub := newRoutingEventHub(4)
	_, stream, cancel, err := hub.subscribe(0, 1, true)
	require.NoError(t, err)
	defer cancel()

	_, err = hub.publish("routing.changed", 1, []byte(`{"resource":"overview"}`), time.Unix(1, 0))
	require.NoError(t, err)
	_, err = hub.publish("routing.changed", 2, []byte(`{"resource":"overview"}`), time.Unix(2, 0))
	require.NoError(t, err)

	reset, ok := <-stream
	require.True(t, ok)
	assert.Equal(t, RoutingEventTypeReset, reset.Type)
	assert.JSONEq(t, `{"reason":"slow_subscriber","earliest_id":1,"latest_id":2,"refresh_all":true}`, string(reset.PayloadJSON))
	_, ok = <-stream
	assert.False(t, ok)

	stats := hub.stats()
	assert.Zero(t, stats.Subscribers)
	assert.Equal(t, uint64(1), stats.SlowDisconnected)
}

func TestRoutingEventHubReturnsPayloadCopies(t *testing.T) {
	hub := newRoutingEventHub(2)
	payload := []byte(`{"resource":"overview"}`)
	event, err := hub.publish("routing.changed", 1, payload, time.Unix(1, 0))
	require.NoError(t, err)
	payload[2] = 'X'
	event.PayloadJSON[2] = 'Y'

	replay, _, cancel, err := hub.subscribe(0, 1, true)
	require.NoError(t, err)
	cancel()
	require.Len(t, replay.Events, 1)
	assert.JSONEq(t, `{"resource":"overview"}`, string(replay.Events[0].PayloadJSON))
}

func TestRoutingEventHubRejectsSubscribersAtBound(t *testing.T) {
	hub := newRoutingEventHub(2)
	hub.subscriberLimit = 1
	_, _, cancel, err := hub.subscribe(0, 1, true)
	require.NoError(t, err)
	defer cancel()

	_, stream, rejectedCancel, err := hub.subscribe(0, 1, true)
	require.ErrorIs(t, err, ErrRoutingEventSubscribersFull)
	rejectedCancel()
	_, ok := <-stream
	assert.False(t, ok)
	stats := hub.stats()
	assert.Equal(t, 1, stats.Subscribers)
	assert.Equal(t, 1, stats.SubscriberLimit)
	assert.Equal(t, uint64(1), stats.Rejected)
}

func TestRoutingEventHubFreshSubscriptionStartsAtLatestAtomically(t *testing.T) {
	hub := newRoutingEventHub(4)
	for index := 1; index <= 3; index++ {
		_, err := hub.publish("routing.changed", uint64(index), []byte(`{"resource":"overview"}`), time.Unix(int64(index), 0))
		require.NoError(t, err)
	}

	replay, stream, cancel, err := hub.subscribe(0, 2, false)
	require.NoError(t, err)
	defer cancel()
	assert.False(t, replay.Gap)
	assert.Empty(t, replay.Events)
	assert.Equal(t, uint64(3), replay.Requested)
	assert.Equal(t, uint64(3), replay.LatestID)

	published, err := hub.publish("routing.changed", 4, []byte(`{"resource":"groups"}`), time.Unix(4, 0))
	require.NoError(t, err)
	received := <-stream
	assert.Equal(t, published.ID, received.ID)
}

func TestRecentRoutingEventsFiltersAndReturnsNewestFirst(t *testing.T) {
	ResetRoutingEventsForTest()
	t.Cleanup(ResetRoutingEventsForTest)

	_, err := PublishRoutingEvent(RoutingEventTypePolicyPublished, 10, map[string]any{"policy": "first"})
	require.NoError(t, err)
	_, err = PublishRoutingEvent(RoutingEventTypeProbeCompleted, 10, map[string]any{"probe": "ignored"})
	require.NoError(t, err)
	latest, err := PublishRoutingEvent(RoutingEventTypePolicyRolledBack, 9, map[string]any{"policy": "latest"})
	require.NoError(t, err)

	events := RecentRoutingEvents(2, RoutingEventTypePolicyPublished, RoutingEventTypePolicyRolledBack)
	require.Len(t, events, 2)
	assert.Equal(t, latest.ID, events[0].ID)
	assert.Equal(t, RoutingEventTypePolicyRolledBack, events[0].Type)
	assert.Equal(t, RoutingEventTypePolicyPublished, events[1].Type)

	events[0].PayloadJSON[0] = '['
	again := RecentRoutingEvents(1, RoutingEventTypePolicyRolledBack)
	require.Len(t, again, 1)
	assert.Equal(t, byte('{'), again[0].PayloadJSON[0])
	assert.Empty(t, RecentRoutingEvents(0))
}
