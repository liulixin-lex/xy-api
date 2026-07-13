package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const (
	channelRoutingEventHeartbeatInterval = 15 * time.Second
	channelRoutingEventRetryMs           = 3_000
)

var channelRoutingEventResetResources = []string{
	"overview", "nodes", "groups", "channels", "costs", "probes", "decisions", "policy_drafts", "policies", "operations",
}

type channelRoutingEventEnvelope struct {
	ID            string          `json:"id"`
	Sequence      uint64          `json:"sequence"`
	NodeEpochID   string          `json:"node_epoch_id"`
	Type          string          `json:"type"`
	Revision      uint64          `json:"revision,omitempty"`
	CreatedTimeMs int64           `json:"created_time_ms"`
	Payload       json.RawMessage `json:"payload"`
}

type channelRoutingEventResetPayload struct {
	Reason           string   `json:"reason"`
	RequestedID      string   `json:"requested_id"`
	EarliestID       string   `json:"earliest_id"`
	LatestID         string   `json:"latest_id"`
	RefreshAll       bool     `json:"refresh_all"`
	RefreshResources []string `json:"refresh_resources"`
}

type channelRoutingEventReadyPayload struct {
	LatestID            string `json:"latest_id"`
	HeartbeatIntervalMs int64  `json:"heartbeat_interval_ms"`
	RetryMs             int64  `json:"retry_ms"`
}

type channelRoutingEventCursor struct {
	NodeEpochID string
	Sequence    uint64
	Provided    bool
}

func GetChannelRoutingEvents(c *gin.Context) {
	cursor, err := parseChannelRoutingLastEventID(c.GetHeader("Last-Event-ID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "invalid_last_event_id",
			"message": "Last-Event-ID must use the <node-epoch>:<sequence> format",
		})
		return
	}

	nodeEpochID := channelrouting.NodeEpochID()
	epochChanged := cursor.Provided && cursor.NodeEpochID != nodeEpochID
	var replay channelrouting.RoutingEventReplay
	var stream <-chan channelrouting.RoutingEvent
	var cancel func()
	if !cursor.Provided || epochChanged {
		replay, stream, cancel, err = channelrouting.SubscribeCurrentRoutingEvents()
	} else {
		replay, stream, cancel, err = channelrouting.SubscribeRoutingEvents(cursor.Sequence)
	}
	if err != nil {
		if errors.Is(err, channelrouting.ErrRoutingEventSubscribersFull) {
			c.Header("Retry-After", strconv.Itoa(channelRoutingEventRetryMs/1_000))
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"success": false, "code": "event_subscriber_limit", "message": "channel routing event stream is at capacity",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false, "code": "event_subscription_failed", "message": "failed to subscribe to channel routing events",
		})
		return
	}
	defer cancel()

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	if epochChanged || replay.Gap {
		reason := "cursor_gap"
		if epochChanged {
			reason = "node_epoch_changed"
		}
		payload := channelRoutingEventResetPayload{
			Reason: reason, RequestedID: formatChannelRoutingEventCursor(cursor.NodeEpochID, cursor.Sequence),
			EarliestID: formatChannelRoutingEventCursor(nodeEpochID, replay.EarliestID),
			LatestID:   formatChannelRoutingEventCursor(nodeEpochID, replay.LatestID), RefreshAll: true,
			RefreshResources: append([]string(nil), channelRoutingEventResetResources...),
		}
		encoded, encodeErr := common.Marshal(payload)
		if encodeErr != nil {
			return
		}
		reset := channelrouting.RoutingEvent{
			ID: replay.LatestID, Type: channelrouting.RoutingEventTypeReset,
			CreatedTimeMs: time.Now().UnixMilli(), PayloadJSON: encoded,
		}
		if writeChannelRoutingSSEEvent(c.Writer, reset, nodeEpochID, true) != nil {
			return
		}
	} else if len(replay.Events) > 0 {
		for _, event := range replay.Events {
			if writeChannelRoutingSSEEvent(c.Writer, event, nodeEpochID, true) != nil {
				return
			}
		}
	} else {
		payload := channelRoutingEventReadyPayload{
			LatestID:            formatChannelRoutingEventCursor(nodeEpochID, replay.LatestID),
			HeartbeatIntervalMs: channelRoutingEventHeartbeatInterval.Milliseconds(),
			RetryMs:             channelRoutingEventRetryMs,
		}
		encoded, encodeErr := common.Marshal(payload)
		if encodeErr != nil {
			return
		}
		ready := channelrouting.RoutingEvent{
			Type: channelrouting.RoutingEventTypeReady, CreatedTimeMs: time.Now().UnixMilli(), PayloadJSON: encoded,
		}
		if writeChannelRoutingSSEEvent(c.Writer, ready, nodeEpochID, false) != nil {
			return
		}
	}
	c.Writer.Flush()

	heartbeat := time.NewTicker(channelRoutingEventHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-stream:
			if !ok {
				return
			}
			if writeChannelRoutingSSEEvent(c.Writer, event, nodeEpochID, true) != nil {
				return
			}
			c.Writer.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func parseChannelRoutingLastEventID(raw string) (channelRoutingEventCursor, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return channelRoutingEventCursor{}, nil
	}
	separator := strings.LastIndexByte(value, ':')
	if separator != 32 || separator == len(value)-1 {
		return channelRoutingEventCursor{}, channelrouting.ErrRoutingEventInvalid
	}
	epoch := value[:separator]
	for index := range epoch {
		char := epoch[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return channelRoutingEventCursor{}, channelrouting.ErrRoutingEventInvalid
		}
	}
	sequence, err := strconv.ParseUint(value[separator+1:], 10, 64)
	if err != nil {
		return channelRoutingEventCursor{}, err
	}
	return channelRoutingEventCursor{NodeEpochID: epoch, Sequence: sequence, Provided: true}, nil
}

func formatChannelRoutingEventCursor(nodeEpochID string, sequence uint64) string {
	return nodeEpochID + ":" + strconv.FormatUint(sequence, 10)
}

func writeChannelRoutingSSEEvent(writer io.Writer, event channelrouting.RoutingEvent, nodeEpochID string, includeCursor bool) error {
	payload := json.RawMessage(event.PayloadJSON)
	var payloadObject map[string]json.RawMessage
	if len(nodeEpochID) != 32 || event.Type == "" || strings.ContainsAny(event.Type, "\r\n") || event.CreatedTimeMs <= 0 ||
		common.GetJsonType(payload) != "object" || common.Unmarshal(payload, &payloadObject) != nil || payloadObject == nil {
		return channelrouting.ErrRoutingEventInvalid
	}
	wireID := formatChannelRoutingEventCursor(nodeEpochID, event.ID)
	encoded, err := common.Marshal(channelRoutingEventEnvelope{
		ID: wireID, Sequence: event.ID, NodeEpochID: nodeEpochID, Type: event.Type, Revision: event.Revision,
		CreatedTimeMs: event.CreatedTimeMs, Payload: append(json.RawMessage(nil), payload...),
	})
	if err != nil {
		return err
	}
	if includeCursor {
		if _, err := fmt.Fprintf(writer, "id: %s\n", wireID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "event: %s\n", event.Type); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, "data: "); err != nil {
		return err
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	_, err = io.WriteString(writer, "\n\n")
	return err
}

func publishChannelRoutingControlEvent(eventType string, revision int64, payload any) {
	if revision < 0 {
		return
	}
	if _, err := channelrouting.PublishRoutingEvent(eventType, uint64(revision), payload); err != nil {
		common.SysError("publish channel routing control event: " + common.SanitizeErrorMessage(err.Error()))
	}
}
