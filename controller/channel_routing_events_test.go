package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingEventsSendsReadyEventBeforeHeartbeat(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	_, err := channelrouting.PublishRoutingEvent("routing.changed", 1, map[string]any{"resource": "overview"})
	require.NoError(t, err)
	recorder := performCanceledChannelRoutingEventRequest(t, "")

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "text/event-stream; charset=utf-8", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache, no-transform", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	assert.Contains(t, recorder.Body.String(), "event: routing.ready\n")
	assert.NotContains(t, recorder.Body.String(), ": heartbeat")
	assert.NotContains(t, recorder.Body.String(), "\nid: ")
	assert.Contains(t, recorder.Body.String(), `"latest_id":"`+channelrouting.NodeEpochID()+`:1"`)
	assert.NotContains(t, recorder.Body.String(), "event: routing.changed")
}

func TestChannelRoutingEventsReplaysAfterEpochCursor(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	first, err := channelrouting.PublishRoutingEvent("routing.changed", 10, map[string]any{"resource": "overview"})
	require.NoError(t, err)
	second, err := channelrouting.PublishRoutingEvent("routing.changed", 11, map[string]any{"resource": "groups"})
	require.NoError(t, err)

	cursor := formatChannelRoutingEventCursor(channelrouting.NodeEpochID(), first.ID)
	recorder := performCanceledChannelRoutingEventRequest(t, cursor)
	body := recorder.Body.String()
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, body, "id: "+formatChannelRoutingEventCursor(channelrouting.NodeEpochID(), second.ID)+"\n")
	assert.Contains(t, body, `"sequence":2`)
	assert.Contains(t, body, `"revision":11`)
	assert.Contains(t, body, `"resource":"groups"`)
	assert.NotContains(t, body, `"sequence":1`)
}

func TestChannelRoutingEventsResetsOnNodeEpochChange(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	_, err := channelrouting.PublishRoutingEvent("routing.changed", 1, map[string]any{"resource": "overview"})
	require.NoError(t, err)

	oldEpoch := strings.Repeat("a", 32)
	if oldEpoch == channelrouting.NodeEpochID() {
		oldEpoch = strings.Repeat("b", 32)
	}
	recorder := performCanceledChannelRoutingEventRequest(t, oldEpoch+":42")
	body := recorder.Body.String()
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, body, "event: routing.reset\n")
	assert.Contains(t, body, `"reason":"node_epoch_changed"`)
	assert.Contains(t, body, `"refresh_all":true`)
	assert.Contains(t, body, "id: "+channelrouting.NodeEpochID()+":1\n")
	assert.NotContains(t, body, "event: routing.changed")
}

func TestChannelRoutingEventsResetsWhenCursorWasEvicted(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	for index := 0; index < 2_050; index++ {
		_, err := channelrouting.PublishRoutingEvent("routing.changed", uint64(index+1), map[string]any{"resource": "overview"})
		require.NoError(t, err)
	}

	recorder := performCanceledChannelRoutingEventRequest(t, channelrouting.NodeEpochID()+":1")
	body := recorder.Body.String()
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, body, "event: routing.reset\n")
	assert.Contains(t, body, `"reason":"cursor_gap"`)
	assert.Contains(t, body, `"earliest_id":"`+channelrouting.NodeEpochID()+`:3"`)
	assert.Contains(t, body, `"latest_id":"`+channelrouting.NodeEpochID()+`:2050"`)
}

func TestChannelRoutingEventsRejectsMalformedCursor(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	recorder := performCanceledChannelRoutingEventRequest(t, "12")

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"invalid_last_event_id"`)
}

func TestChannelRoutingEventsRejectsConnectionPastGlobalBound(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	cancels := make([]func(), 0, 64)
	for index := 0; index < 64; index++ {
		_, _, cancel, err := channelrouting.SubscribeRoutingEvents(0)
		require.NoError(t, err)
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	recorder := performCanceledChannelRoutingEventRequest(t, "")
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "3", recorder.Header().Get("Retry-After"))
	assert.Contains(t, recorder.Body.String(), `"code":"event_subscriber_limit"`)
}

func TestChannelRoutingSSEEventPreservesLargePayloadIntegers(t *testing.T) {
	var output strings.Builder
	err := writeChannelRoutingSSEEvent(&output, channelrouting.RoutingEvent{
		ID:            1,
		Type:          "routing.changed",
		CreatedTimeMs: 1,
		PayloadJSON:   []byte(`{"operation_id":9007199254740993}`),
	}, strings.Repeat("a", 32), true)
	require.NoError(t, err)

	assert.Contains(t, output.String(), `"operation_id":9007199254740993`)
	assert.NotContains(t, output.String(), `"operation_id":9007199254740992`)
}

func performCanceledChannelRoutingEventRequest(t *testing.T, lastEventID string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/channel-routing/events", nil).WithContext(requestContext)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	c.Request = request
	GetChannelRoutingEvents(c)
	return recorder
}
