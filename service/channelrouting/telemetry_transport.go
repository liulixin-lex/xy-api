package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-redis/redis/v8"
)

const (
	routingTelemetryStream                 = "routing:v2:telemetry"
	routingTelemetryConsumerGroup          = "routing-v2-rollup"
	routingTelemetryStreamMaxLen     int64 = 100_000
	routingTelemetryEnvelopeMaxBytes       = 512 << 10
	routingTelemetryPendingMaxItems        = 64
	routingTelemetryPendingMaxBytes        = 8 << 20
	routingTelemetryPendingTTL             = time.Hour
	routingTelemetryConsumerBatch    int64 = 32
	routingTelemetryConsumerBlock          = 2 * time.Second
	routingTelemetryClaimIdle              = 30 * time.Second
)

var (
	ErrRoutingTelemetryPendingFull = errors.New("routing telemetry pending queue is full")
	ErrRoutingTelemetryUnavailable = errors.New("routing telemetry redis stream is unavailable")

	routingTelemetrySequence  atomic.Int64
	routingTelemetryTransport = newTelemetryTransportState()
	loadRoutingTelemetryRedis = func() routingTelemetryRedis {
		if !common.RedisEnabled || common.RDB == nil {
			return nil
		}
		return common.RDB
	}
)

type routingTelemetryRedis interface {
	Do(context.Context, ...interface{}) *redis.Cmd
	XAdd(context.Context, *redis.XAddArgs) *redis.StringCmd
	XGroupCreateMkStream(context.Context, string, string, string) *redis.StatusCmd
	XReadGroup(context.Context, *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XPending(context.Context, string, string) *redis.XPendingCmd
	XPendingExt(context.Context, *redis.XPendingExtArgs) *redis.XPendingExtCmd
	XRangeN(context.Context, string, string, string, int64) *redis.XMessageSliceCmd
	XClaim(context.Context, *redis.XClaimArgs) *redis.XMessageSliceCmd
	XAck(context.Context, string, string, ...string) *redis.IntCmd
}

type routingTelemetryEnvelope struct {
	batch    model.RoutingTelemetryBatch
	payload  []byte
	queuedAt time.Time
}

type RoutingTelemetryTransportStats struct {
	PendingEnvelopes           int    `json:"pending_envelopes"`
	PendingItems               int    `json:"pending_items"`
	PendingBytes               int    `json:"pending_bytes"`
	Published                  int64  `json:"published"`
	FallbackApplied            int64  `json:"fallback_applied"`
	Consumed                   int64  `json:"consumed"`
	DuplicateConsumed          int64  `json:"duplicate_consumed"`
	Rejected                   int64  `json:"rejected"`
	PublishFailures            int64  `json:"publish_failures"`
	ConsumeFailures            int64  `json:"consume_failures"`
	AckFailures                int64  `json:"ack_failures"`
	ExpiredEnvelopes           int64  `json:"expired_envelopes"`
	ExpiredItems               int64  `json:"expired_items"`
	ExpiredBytes               int64  `json:"expired_bytes"`
	LastPublishError           string `json:"last_publish_error,omitempty"`
	LastConsumerError          string `json:"last_consumer_error,omitempty"`
	LastPublishedAtMs          int64  `json:"last_published_at_ms"`
	LastConsumedAtMs           int64  `json:"last_consumed_at_ms"`
	LastFallbackAtMs           int64  `json:"last_fallback_at_ms"`
	LastRejectedAtMs           int64  `json:"last_rejected_at_ms"`
	PipelineAvailable          bool   `json:"pipeline_available"`
	PipelineLagAvailable       bool   `json:"pipeline_lag_available"`
	PipelinePending            int64  `json:"pipeline_pending"`
	PipelineUndelivered        int64  `json:"pipeline_undelivered"`
	PipelineBacklog            int64  `json:"pipeline_backlog"`
	PipelineOldestIdleMs       int64  `json:"pipeline_oldest_idle_ms"`
	PipelineOldestMessageAgeMs int64  `json:"pipeline_oldest_message_age_ms"`
	PipelineLastDeliveredID    string `json:"pipeline_last_delivered_id,omitempty"`
	PipelineCheckedAtMs        int64  `json:"pipeline_checked_at_ms"`
	PipelineLastError          string `json:"pipeline_last_error,omitempty"`
}

type telemetryTransportState struct {
	mu      sync.Mutex
	pending []routingTelemetryEnvelope
	bytes   int
	stats   RoutingTelemetryTransportStats
}

func newTelemetryTransportState() *telemetryTransportState {
	return &telemetryTransportState{pending: make([]routingTelemetryEnvelope, 0, routingTelemetryPendingMaxItems)}
}

func RoutingTelemetryTransportRuntimeStats() RoutingTelemetryTransportStats {
	return routingTelemetryTransport.snapshot(time.Now())
}

func newRoutingTelemetryEnvelope(rollups []model.RoutingMetricRollup, now time.Time) (routingTelemetryEnvelope, error) {
	if len(rollups) == 0 || len(rollups) > model.RoutingTelemetryMaxItems {
		return routingTelemetryEnvelope{}, model.ErrRoutingTelemetryInvalid
	}
	sequence := routingTelemetrySequence.Add(1)
	if sequence <= 0 {
		return routingTelemetryEnvelope{}, errors.New("routing telemetry sequence overflow")
	}
	batch := model.RoutingTelemetryBatch{
		NodeID:       NodeEpochID(),
		Sequence:     sequence,
		ProducedAtMs: now.UnixMilli(),
		Items:        rollups,
	}
	payloadHash, err := model.ComputeRoutingTelemetryPayloadHash(batch)
	if err != nil {
		return routingTelemetryEnvelope{}, err
	}
	batch.PayloadHash = payloadHash
	payload, err := common.Marshal(batch)
	if err != nil {
		return routingTelemetryEnvelope{}, fmt.Errorf("marshal routing telemetry envelope: %w", err)
	}
	if len(payload) > routingTelemetryEnvelopeMaxBytes {
		return routingTelemetryEnvelope{}, fmt.Errorf("routing telemetry envelope exceeds %d bytes", routingTelemetryEnvelopeMaxBytes)
	}
	return routingTelemetryEnvelope{batch: batch, payload: payload, queuedAt: now}, nil
}

func deliverRoutingTelemetryEnvelopeContext(ctx context.Context, envelope routingTelemetryEnvelope) error {
	client := loadRoutingTelemetryRedis()
	if client != nil {
		_, err := client.XAdd(ctx, &redis.XAddArgs{
			Stream: routingTelemetryStream,
			MaxLen: routingTelemetryStreamMaxLen,
			Approx: true,
			Values: []interface{}{"payload", string(envelope.payload)},
		}).Result()
		if err == nil {
			routingTelemetryTransport.markPublished()
			return nil
		}
		routingTelemetryTransport.markPublishFailure(err)
	}

	result, err := model.ApplyRoutingTelemetryBatchContext(ctx, envelope.batch)
	if err != nil {
		return err
	}
	routingTelemetryTransport.markFallback(result.Duplicate)
	return nil
}

func ConsumeRoutingTelemetryOnceContext(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := loadRoutingTelemetryRedis()
	if client == nil {
		routingTelemetryTransport.markPipelineUnavailable(ErrRoutingTelemetryUnavailable)
		return 0, ErrRoutingTelemetryUnavailable
	}
	if err := ensureRoutingTelemetryConsumerGroup(ctx, client); err != nil {
		routingTelemetryTransport.markConsumeFailure(err)
		routingTelemetryTransport.markPipelineUnavailable(err)
		return 0, err
	}
	refreshRoutingTelemetryPipelineStatsContext(ctx, client)

	messages, err := claimStaleRoutingTelemetryMessages(ctx, client)
	if err != nil {
		routingTelemetryTransport.markConsumeFailure(err)
		return 0, err
	}
	if len(messages) == 0 {
		streams, readErr := client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    routingTelemetryConsumerGroup,
			Consumer: routingTelemetryConsumerName(),
			Streams:  []string{routingTelemetryStream, ">"},
			Count:    routingTelemetryConsumerBatch,
			Block:    routingTelemetryConsumerBlock,
		}).Result()
		if errors.Is(readErr, redis.Nil) {
			return 0, nil
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			routingTelemetryTransport.markConsumeFailure(readErr)
			return 0, readErr
		}
		for _, stream := range streams {
			messages = append(messages, stream.Messages...)
		}
	}

	processed := 0
	for _, message := range messages {
		applied, permanent, processErr := applyRoutingTelemetryMessageContext(ctx, message)
		if processErr != nil && !permanent {
			routingTelemetryTransport.markConsumeFailure(processErr)
			return processed, processErr
		}
		if processErr != nil {
			routingTelemetryTransport.markRejected(processErr)
		}
		if _, ackErr := client.XAck(ctx, routingTelemetryStream, routingTelemetryConsumerGroup, message.ID).Result(); ackErr != nil {
			routingTelemetryTransport.markAckFailure(ackErr)
			return processed, ackErr
		}
		if applied {
			processed++
		}
	}
	return processed, nil
}

func refreshRoutingTelemetryPipelineStatsContext(ctx context.Context, client routingTelemetryRedis) {
	summary, err := client.XPending(ctx, routingTelemetryStream, routingTelemetryConsumerGroup).Result()
	if err != nil {
		routingTelemetryTransport.markPipelineUnavailable(err)
		return
	}
	pendingCount := int64(0)
	oldestIdle := time.Duration(0)
	oldestMessageAgeMs := int64(0)
	if summary != nil {
		pendingCount = summary.Count
		oldestMessageAgeMs = routingTelemetryMessageAgeMs(summary.Lower, time.Now().UnixMilli())
	}
	if pendingCount > 0 {
		pending, pendingErr := client.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: routingTelemetryStream,
			Group:  routingTelemetryConsumerGroup,
			Start:  "-",
			End:    "+",
			Count:  1,
		}).Result()
		if pendingErr != nil && !errors.Is(pendingErr, redis.Nil) {
			routingTelemetryTransport.markPipelineUnavailable(pendingErr)
			return
		}
		if len(pending) > 0 {
			oldestIdle = pending[0].Idle
		}
	}
	undelivered, lastDeliveredID, err := routingTelemetryUndeliveredLagContext(ctx, client)
	if err != nil {
		routingTelemetryTransport.markPipelineUnavailable(err)
		return
	}
	if undelivered > 0 {
		start := "(0-0"
		if lastDeliveredID != "" {
			start = "(" + lastDeliveredID
		}
		messages, rangeErr := client.XRangeN(ctx, routingTelemetryStream, start, "+", 1).Result()
		if rangeErr != nil && !errors.Is(rangeErr, redis.Nil) {
			routingTelemetryTransport.markPipelineUnavailable(rangeErr)
			return
		}
		if len(messages) > 0 {
			oldestMessageAgeMs = max(oldestMessageAgeMs, routingTelemetryMessageAgeMs(messages[0].ID, time.Now().UnixMilli()))
		}
	}
	routingTelemetryTransport.markPipelineAvailable(
		pendingCount,
		undelivered,
		oldestIdle,
		oldestMessageAgeMs,
		lastDeliveredID,
	)
}

func routingTelemetryUndeliveredLagContext(ctx context.Context, client routingTelemetryRedis) (int64, string, error) {
	result, err := client.Do(ctx, "XINFO", "GROUPS", routingTelemetryStream).Result()
	if err != nil {
		return 0, "", err
	}
	groups, ok := result.([]interface{})
	if !ok {
		return 0, "", errors.New("routing telemetry group lag response is invalid")
	}
	for _, rawGroup := range groups {
		fields, ok := rawGroup.([]interface{})
		if !ok || len(fields)%2 != 0 {
			continue
		}
		name := ""
		lastDeliveredID := ""
		lag := int64(-1)
		for index := 0; index < len(fields); index += 2 {
			key, keyOK := routingTelemetryRedisString(fields[index])
			if !keyOK {
				continue
			}
			switch key {
			case "name":
				name, _ = routingTelemetryRedisString(fields[index+1])
			case "last-delivered-id":
				lastDeliveredID, _ = routingTelemetryRedisString(fields[index+1])
			case "lag":
				lag, _ = routingTelemetryRedisInt64(fields[index+1])
			}
		}
		if name != routingTelemetryConsumerGroup {
			continue
		}
		if lag < 0 {
			return 0, lastDeliveredID, errors.New("routing telemetry consumer group does not expose undelivered lag")
		}
		return lag, lastDeliveredID, nil
	}
	return 0, "", errors.New("routing telemetry consumer group is missing")
}

func routingTelemetryRedisString(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func routingTelemetryRedisInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func routingTelemetryMessageAgeMs(messageID string, nowMs int64) int64 {
	separator := strings.IndexByte(messageID, '-')
	if separator <= 0 {
		return 0
	}
	producedMs, err := strconv.ParseInt(messageID[:separator], 10, 64)
	if err != nil || producedMs <= 0 || producedMs >= nowMs {
		return 0
	}
	return nowMs - producedMs
}

func ensureRoutingTelemetryConsumerGroup(ctx context.Context, client routingTelemetryRedis) error {
	err := client.XGroupCreateMkStream(ctx, routingTelemetryStream, routingTelemetryConsumerGroup, "0").Err()
	if err == nil || strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

func claimStaleRoutingTelemetryMessages(ctx context.Context, client routingTelemetryRedis) ([]redis.XMessage, error) {
	pending, err := client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: routingTelemetryStream,
		Group:  routingTelemetryConsumerGroup,
		Start:  "-",
		End:    "+",
		Count:  routingTelemetryConsumerBatch,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	ids := make([]string, 0, len(pending))
	for _, entry := range pending {
		if entry.Idle >= routingTelemetryClaimIdle {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return client.XClaim(ctx, &redis.XClaimArgs{
		Stream:   routingTelemetryStream,
		Group:    routingTelemetryConsumerGroup,
		Consumer: routingTelemetryConsumerName(),
		MinIdle:  routingTelemetryClaimIdle,
		Messages: ids,
	}).Result()
}

func applyRoutingTelemetryMessageContext(ctx context.Context, message redis.XMessage) (bool, bool, error) {
	value, exists := message.Values["payload"]
	if !exists {
		return false, true, errors.New("routing telemetry stream message has no payload")
	}
	payload, ok := routingTelemetryPayloadBytes(value)
	if !ok || len(payload) == 0 || len(payload) > routingTelemetryEnvelopeMaxBytes {
		return false, true, errors.New("routing telemetry stream payload has invalid size or type")
	}
	var batch model.RoutingTelemetryBatch
	if err := common.Unmarshal(payload, &batch); err != nil {
		return false, true, fmt.Errorf("decode routing telemetry stream payload: %w", err)
	}
	result, err := model.ApplyRoutingTelemetryBatchContext(ctx, batch)
	if err != nil {
		permanent := errors.Is(err, model.ErrRoutingTelemetryInvalid) ||
			errors.Is(err, model.ErrRoutingTelemetrySequenceCollision) ||
			errors.Is(err, model.ErrRoutingMetricRollupInvalid) ||
			errors.Is(err, model.ErrRoutingMetricRollupOverflow)
		return false, permanent, err
	}
	routingTelemetryTransport.markConsumed(result.Duplicate)
	return true, false, nil
}

func routingTelemetryPayloadBytes(value interface{}) ([]byte, bool) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return append([]byte(nil), typed...), true
	default:
		return nil, false
	}
}

func routingTelemetryConsumerName() string {
	return "node-" + NodeEpochID()
}

func (state *telemetryTransportState) enqueue(envelope routingTelemetryEnvelope, now time.Time) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.expireLocked(now)
	if len(state.pending) >= routingTelemetryPendingMaxItems || state.bytes+len(envelope.payload) > routingTelemetryPendingMaxBytes {
		return ErrRoutingTelemetryPendingFull
	}
	state.pending = append(state.pending, envelope)
	state.bytes += len(envelope.payload)
	return nil
}

func (state *telemetryTransportState) peek(now time.Time) (routingTelemetryEnvelope, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.expireLocked(now)
	if len(state.pending) == 0 {
		return routingTelemetryEnvelope{}, false
	}
	return state.pending[0], true
}

func (state *telemetryTransportState) remove(sequence int64) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pending) == 0 || state.pending[0].batch.Sequence != sequence {
		return false
	}
	state.bytes -= len(state.pending[0].payload)
	copy(state.pending, state.pending[1:])
	state.pending[len(state.pending)-1] = routingTelemetryEnvelope{}
	state.pending = state.pending[:len(state.pending)-1]
	return true
}

func (state *telemetryTransportState) hasCapacity(now time.Time) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.expireLocked(now)
	return len(state.pending) < routingTelemetryPendingMaxItems && state.bytes < routingTelemetryPendingMaxBytes
}

func (state *telemetryTransportState) snapshot(now time.Time) RoutingTelemetryTransportStats {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.expireLocked(now)
	stats := state.stats
	stats.PendingEnvelopes = len(state.pending)
	stats.PendingBytes = state.bytes
	for _, envelope := range state.pending {
		stats.PendingItems += len(envelope.batch.Items)
	}
	return stats
}

func (state *telemetryTransportState) expireLocked(now time.Time) {
	for len(state.pending) > 0 && now.Sub(state.pending[0].queuedAt) >= routingTelemetryPendingTTL {
		envelope := state.pending[0]
		state.stats.ExpiredEnvelopes++
		state.stats.ExpiredItems += int64(len(envelope.batch.Items))
		state.stats.ExpiredBytes += int64(len(envelope.payload))
		state.bytes -= len(envelope.payload)
		copy(state.pending, state.pending[1:])
		state.pending[len(state.pending)-1] = routingTelemetryEnvelope{}
		state.pending = state.pending[:len(state.pending)-1]
	}
}

func (state *telemetryTransportState) markPublished() {
	state.mu.Lock()
	state.stats.Published++
	state.stats.LastPublishedAtMs = time.Now().UnixMilli()
	state.stats.LastPublishError = ""
	state.mu.Unlock()
}

func (state *telemetryTransportState) markPublishFailure(err error) {
	state.mu.Lock()
	state.stats.PublishFailures++
	state.stats.LastPublishError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *telemetryTransportState) markFallback(duplicate bool) {
	state.mu.Lock()
	state.stats.FallbackApplied++
	if duplicate {
		state.stats.DuplicateConsumed++
	}
	state.stats.LastFallbackAtMs = time.Now().UnixMilli()
	state.mu.Unlock()
}

func (state *telemetryTransportState) markConsumed(duplicate bool) {
	state.mu.Lock()
	state.stats.Consumed++
	if duplicate {
		state.stats.DuplicateConsumed++
	}
	state.stats.LastConsumedAtMs = time.Now().UnixMilli()
	state.stats.LastConsumerError = ""
	state.mu.Unlock()
}

func (state *telemetryTransportState) markRejected(err error) {
	state.mu.Lock()
	state.stats.Rejected++
	state.stats.LastRejectedAtMs = time.Now().UnixMilli()
	state.stats.LastConsumerError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *telemetryTransportState) markConsumeFailure(err error) {
	state.mu.Lock()
	state.stats.ConsumeFailures++
	state.stats.LastConsumerError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *telemetryTransportState) markAckFailure(err error) {
	state.mu.Lock()
	state.stats.AckFailures++
	state.stats.LastConsumerError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *telemetryTransportState) markPipelineAvailable(
	pending int64,
	undelivered int64,
	oldestIdle time.Duration,
	oldestMessageAgeMs int64,
	lastDeliveredID string,
) {
	state.mu.Lock()
	state.stats.PipelineAvailable = true
	state.stats.PipelineLagAvailable = true
	state.stats.PipelinePending = max(int64(0), pending)
	state.stats.PipelineUndelivered = max(int64(0), undelivered)
	if state.stats.PipelinePending > math.MaxInt64-state.stats.PipelineUndelivered {
		state.stats.PipelineBacklog = math.MaxInt64
	} else {
		state.stats.PipelineBacklog = state.stats.PipelinePending + state.stats.PipelineUndelivered
	}
	state.stats.PipelineOldestIdleMs = max(int64(0), oldestIdle.Milliseconds())
	state.stats.PipelineOldestMessageAgeMs = max(int64(0), oldestMessageAgeMs)
	state.stats.PipelineLastDeliveredID = lastDeliveredID
	state.stats.PipelineCheckedAtMs = time.Now().UnixMilli()
	state.stats.PipelineLastError = ""
	state.mu.Unlock()
}

func (state *telemetryTransportState) markPipelineUnavailable(err error) {
	state.mu.Lock()
	state.stats.PipelineAvailable = false
	state.stats.PipelineLagAvailable = false
	state.stats.PipelinePending = 0
	state.stats.PipelineUndelivered = 0
	state.stats.PipelineBacklog = 0
	state.stats.PipelineOldestIdleMs = 0
	state.stats.PipelineOldestMessageAgeMs = 0
	state.stats.PipelineLastDeliveredID = ""
	state.stats.PipelineCheckedAtMs = time.Now().UnixMilli()
	if err != nil {
		state.stats.PipelineLastError = common.SanitizeErrorMessage(err.Error())
	}
	state.mu.Unlock()
}

func ResetRoutingTelemetryTransportForTest() {
	routingTelemetrySequence.Store(0)
	routingTelemetryTransport = newTelemetryTransportState()
}
