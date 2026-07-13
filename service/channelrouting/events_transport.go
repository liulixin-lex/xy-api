package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
)

const (
	routingEventRedisStream               = "routing:v2:events"
	routingEventRedisStreamMaxLen         = 10_000
	routingEventRedisReadCount      int64 = 64
	routingEventRedisReadBlock            = 2 * time.Second
	routingEventRedisPublishTimeout       = time.Second
)

type routingEventRedis interface {
	XAdd(context.Context, *redis.XAddArgs) *redis.StringCmd
	XRead(context.Context, *redis.XReadArgs) *redis.XStreamSliceCmd
	XRevRangeN(context.Context, string, string, string, int64) *redis.XMessageSliceCmd
}

type RoutingEventTransportStats struct {
	Available       bool   `json:"available"`
	Cursor          string `json:"cursor"`
	Published       uint64 `json:"published"`
	PublishFailures uint64 `json:"publish_failures"`
	Consumed        uint64 `json:"consumed"`
	IgnoredOwn      uint64 `json:"ignored_own"`
	Rejected        uint64 `json:"rejected"`
	LastPublishedMs int64  `json:"last_published_ms"`
	LastConsumedMs  int64  `json:"last_consumed_ms"`
	LastError       string `json:"last_error,omitempty"`
}

type routingEventTransportState struct {
	initMu sync.Mutex
	mu     sync.Mutex

	initialized bool
	cursor      string
	stats       RoutingEventTransportStats
}

type routingEventTransportMessage struct {
	sourceNodeEpochID string
	sourceSequence    uint64
	eventType         string
	revision          uint64
	createdTimeMs     int64
	payload           []byte
}

var (
	defaultRoutingEventTransport = newRoutingEventTransportState()
	loadRoutingEventRedis        = func() routingEventRedis {
		if !common.RedisEnabled || common.RDB == nil {
			return nil
		}
		return common.RDB
	}
)

func newRoutingEventTransportState() *routingEventTransportState {
	return &routingEventTransportState{cursor: "0-0"}
}

func RoutingEventTransportRuntimeStats() RoutingEventTransportStats {
	return defaultRoutingEventTransport.snapshot()
}

func broadcastRoutingEvent(event RoutingEvent) {
	client := loadRoutingEventRedis()
	if client == nil {
		defaultRoutingEventTransport.markUnavailable()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), routingEventRedisPublishTimeout)
	defer cancel()
	if err := broadcastRoutingEventContext(ctx, defaultRoutingEventTransport, client, NodeEpochID(), event); err != nil {
		defaultRoutingEventTransport.markPublishFailure(err)
	}
}

func broadcastRoutingEventContext(
	ctx context.Context,
	state *routingEventTransportState,
	client routingEventRedis,
	nodeEpochID string,
	event RoutingEvent,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil || client == nil || !validRoutingNodeEpochID(nodeEpochID) || event.ID == 0 ||
		!validRoutingEventType(event.Type) || event.CreatedTimeMs <= 0 || len(event.PayloadJSON) == 0 ||
		len(event.PayloadJSON) > routingEventPayloadMaximum {
		return ErrRoutingEventInvalid
	}
	payloadHash := sha256.Sum256(event.PayloadJSON)
	_, err := client.XAdd(ctx, &redis.XAddArgs{
		Stream: routingEventRedisStream,
		MaxLen: routingEventRedisStreamMaxLen,
		Approx: true,
		Values: []interface{}{
			"source_node_epoch_id", nodeEpochID,
			"source_sequence", strconv.FormatUint(event.ID, 10),
			"event_type", event.Type,
			"revision", strconv.FormatUint(event.Revision, 10),
			"created_time_ms", strconv.FormatInt(event.CreatedTimeMs, 10),
			"payload_hash", hex.EncodeToString(payloadHash[:]),
			"payload", string(event.PayloadJSON),
		},
	}).Result()
	if err != nil {
		return err
	}
	state.markPublished()
	return nil
}

func ConsumeRoutingEventsOnceContext(ctx context.Context) (int, error) {
	return consumeRoutingEventsOnceContext(
		ctx, defaultRoutingEventHubSnapshot(), defaultRoutingEventTransport, loadRoutingEventRedis(), NodeEpochID(),
	)
}

func consumeRoutingEventsOnceContext(
	ctx context.Context,
	hub *routingEventHub,
	state *routingEventTransportState,
	client routingEventRedis,
	nodeEpochID string,
) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if hub == nil || state == nil || client == nil || !validRoutingNodeEpochID(nodeEpochID) {
		if state != nil {
			state.markUnavailable()
		}
		return 0, ErrRoutingEventInvalid
	}
	if err := initializeRoutingEventTransportContext(ctx, state, client); err != nil {
		state.markError(err)
		return 0, err
	}
	cursor := state.currentCursor()
	streams, err := client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{routingEventRedisStream, cursor},
		Count:   routingEventRedisReadCount,
		Block:   routingEventRedisReadBlock,
	}).Result()
	if errors.Is(err, redis.Nil) {
		state.markAvailable()
		return 0, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		state.markError(err)
		return 0, err
	}

	processed := 0
	for _, stream := range streams {
		for _, raw := range stream.Messages {
			message, decodeErr := decodeRoutingEventTransportMessage(raw)
			if decodeErr != nil {
				state.markRejected(raw.ID, decodeErr)
				processed++
				continue
			}
			if message.sourceNodeEpochID == nodeEpochID {
				state.markIgnoredOwn(raw.ID)
				processed++
				continue
			}
			if message.eventType == RoutingEventTypeBreakerReset {
				applied, applyErr := applyDurableRoutingBreakerResetEventPayloadContext(ctx, message.payload)
				if applyErr != nil {
					state.markRejected(raw.ID, applyErr)
					processed++
					continue
				}
				if !applied {
					state.markConsumed(raw.ID)
					processed++
					continue
				}
			}
			if _, publishErr := hub.publish(
				message.eventType, message.revision, message.payload, time.UnixMilli(message.createdTimeMs),
			); publishErr != nil {
				state.markRejected(raw.ID, publishErr)
				processed++
				continue
			}
			state.markConsumed(raw.ID)
			processed++
		}
	}
	return processed, nil
}

func initializeRoutingEventTransportContext(
	ctx context.Context,
	state *routingEventTransportState,
	client routingEventRedis,
) error {
	state.initMu.Lock()
	defer state.initMu.Unlock()
	if state.isInitialized() {
		return nil
	}
	cursor := "0-0"
	messages, err := client.XRevRangeN(ctx, routingEventRedisStream, "+", "-", 1).Result()
	if err != nil {
		return err
	}
	if len(messages) > 0 {
		if messages[0].ID == "" {
			return ErrRoutingEventInvalid
		}
		cursor = messages[0].ID
	}
	state.markInitialized(cursor)
	return nil
}

func decodeRoutingEventTransportMessage(raw redis.XMessage) (routingEventTransportMessage, error) {
	sourceNodeEpochID, ok := routingEventStringValue(raw.Values["source_node_epoch_id"])
	if !ok || !validRoutingNodeEpochID(sourceNodeEpochID) {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	sourceSequenceText, ok := routingEventStringValue(raw.Values["source_sequence"])
	if !ok {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	sourceSequence, err := strconv.ParseUint(sourceSequenceText, 10, 64)
	if err != nil || sourceSequence == 0 {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	eventType, ok := routingEventStringValue(raw.Values["event_type"])
	if !ok || !validRoutingEventType(eventType) {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	revisionText, ok := routingEventStringValue(raw.Values["revision"])
	if !ok {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	revision, err := strconv.ParseUint(revisionText, 10, 64)
	if err != nil {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	createdTimeText, ok := routingEventStringValue(raw.Values["created_time_ms"])
	if !ok {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	createdTimeMs, err := strconv.ParseInt(createdTimeText, 10, 64)
	if err != nil || createdTimeMs <= 0 {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	payloadHash, ok := routingEventStringValue(raw.Values["payload_hash"])
	if !ok || len(payloadHash) != sha256.Size*2 {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	payloadText, ok := routingEventStringValue(raw.Values["payload"])
	payload := []byte(payloadText)
	if !ok || len(payload) == 0 || len(payload) > routingEventPayloadMaximum {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != payloadHash {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	var payloadObject map[string]any
	if common.Unmarshal(payload, &payloadObject) != nil || payloadObject == nil {
		return routingEventTransportMessage{}, ErrRoutingEventInvalid
	}
	return routingEventTransportMessage{
		sourceNodeEpochID: sourceNodeEpochID, sourceSequence: sourceSequence, eventType: eventType,
		revision: revision, createdTimeMs: createdTimeMs, payload: payload,
	}, nil
}

func defaultRoutingEventHubSnapshot() *routingEventHub {
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	return hub
}

func validRoutingNodeEpochID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for index := range value {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func routingEventStringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	default:
		return "", false
	}
}

func (state *routingEventTransportState) snapshot() RoutingEventTransportStats {
	if state == nil {
		return RoutingEventTransportStats{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	stats := state.stats
	stats.Cursor = state.cursor
	return stats
}

func (state *routingEventTransportState) currentCursor() string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.cursor
}

func (state *routingEventTransportState) isInitialized() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.initialized
}

func (state *routingEventTransportState) markInitialized(cursor string) {
	state.mu.Lock()
	state.initialized = true
	state.cursor = cursor
	state.stats.Available = true
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingEventTransportState) markAvailable() {
	state.mu.Lock()
	state.stats.Available = true
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingEventTransportState) markUnavailable() {
	state.mu.Lock()
	state.stats.Available = false
	state.mu.Unlock()
}

func (state *routingEventTransportState) markPublished() {
	state.mu.Lock()
	state.stats.Available = true
	state.stats.Published++
	state.stats.LastPublishedMs = time.Now().UnixMilli()
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingEventTransportState) markPublishFailure(err error) {
	state.mu.Lock()
	state.stats.Available = false
	state.stats.PublishFailures++
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingEventTransportState) markConsumed(cursor string) {
	state.mu.Lock()
	state.cursor = cursor
	state.stats.Available = true
	state.stats.Consumed++
	state.stats.LastConsumedMs = time.Now().UnixMilli()
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingEventTransportState) markIgnoredOwn(cursor string) {
	state.mu.Lock()
	state.cursor = cursor
	state.stats.Available = true
	state.stats.IgnoredOwn++
	state.stats.LastConsumedMs = time.Now().UnixMilli()
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingEventTransportState) markRejected(cursor string, err error) {
	state.mu.Lock()
	state.cursor = cursor
	state.stats.Available = true
	state.stats.Rejected++
	state.stats.LastConsumedMs = time.Now().UnixMilli()
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingEventTransportState) markError(err error) {
	state.mu.Lock()
	state.stats.Available = false
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func ResetRoutingEventTransportForTest() {
	defaultRoutingEventTransport = newRoutingEventTransportState()
	loadRoutingEventRedis = func() routingEventRedis {
		if !common.RedisEnabled || common.RDB == nil {
			return nil
		}
		return common.RDB
	}
}
