package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-redis/redis/v8"
)

const (
	routingConfigStream                = "channel-routing:config"
	RoutingConfigCheckpointKind        = "config_stream"
	RoutingConfigCheckpointScope       = routingConfigStream
	routingConfigStreamMaxLen    int64 = 10_000
	routingConfigReadCount       int64 = 32
	routingConfigReadBlock             = 2 * time.Second
	routingConfigClaimLease            = 30 * time.Second
	routingConfigPayloadMaxBytes       = 64 << 10
	routingConfigCheckpointTTL         = 8 * 24 * time.Hour
)

var (
	ErrRoutingConfigStreamUnavailable = errors.New("routing config redis stream is unavailable")
	ErrRoutingConfigRevisionUnknown   = errors.New("routing config revision is not committed")
	routingConfigState                = newRoutingConfigStreamState()
	refreshRoutingConfigSnapshot      = RefreshSnapshotContext
	loadRoutingConfigSnapshotMetadata = CurrentSnapshotMetadata
	loadRoutingConfigPolicyHead       = model.GetRoutingPolicyHeadContext
	loadRoutingConfigRedis            = func() routingConfigRedis {
		if !common.RedisEnabled || common.RDB == nil {
			return nil
		}
		return common.RDB
	}
)

type routingConfigRedis interface {
	XAdd(context.Context, *redis.XAddArgs) *redis.StringCmd
	XRead(context.Context, *redis.XReadArgs) *redis.XStreamSliceCmd
	XRevRangeN(context.Context, string, string, string, int64) *redis.XMessageSliceCmd
}

type RoutingConfigStreamStats struct {
	Cursor              string `json:"cursor"`
	Published           int64  `json:"published"`
	PublishFailures     int64  `json:"publish_failures"`
	Consumed            int64  `json:"consumed"`
	IgnoredOldRevision  int64  `json:"ignored_old_revision"`
	Rejected            int64  `json:"rejected"`
	RefreshFailures     int64  `json:"refresh_failures"`
	CheckpointFailures  int64  `json:"checkpoint_failures"`
	LastEventRevision   int64  `json:"last_event_revision"`
	LastAppliedRevision int64  `json:"last_applied_revision"`
	LastPublishedAt     int64  `json:"last_published_at"`
	LastConsumedAt      int64  `json:"last_consumed_at"`
	LastError           string `json:"last_error,omitempty"`
}

type routingConfigStreamState struct {
	initMu      sync.Mutex
	mu          sync.Mutex
	initialized bool
	cursor      string
	sequence    int64
	stats       RoutingConfigStreamStats
}

func newRoutingConfigStreamState() *routingConfigStreamState {
	return &routingConfigStreamState{cursor: "0-0"}
}

func RoutingConfigStreamRuntimeStats() RoutingConfigStreamStats {
	routingConfigState.mu.Lock()
	defer routingConfigState.mu.Unlock()
	stats := routingConfigState.stats
	stats.Cursor = routingConfigState.cursor
	return stats
}

func PublishRoutingConfigOutboxOnceContext(ctx context.Context) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := common.GetTimestamp()
	outbox, err := model.ClaimRoutingConfigOutboxContext(ctx, now, int64(routingConfigClaimLease/time.Second))
	if err != nil || outbox == nil {
		return false, err
	}
	client := loadRoutingConfigRedis()
	if client == nil {
		err := ErrRoutingConfigStreamUnavailable
		releaseErr := model.ReleaseRoutingConfigOutboxClaimContext(ctx, outbox.ID, outbox.ClaimToken, now+routingConfigBackoffSeconds(outbox.Attempts), err)
		routingConfigState.markPublishFailure(err)
		if releaseErr != nil {
			return false, fmt.Errorf("config stream unavailable: %v; release claim: %w", err, releaseErr)
		}
		return false, err
	}
	_, err = client.XAdd(ctx, &redis.XAddArgs{
		Stream: routingConfigStream,
		MaxLen: routingConfigStreamMaxLen,
		Approx: true,
		Values: []interface{}{
			"event_id", outbox.EventID,
			"revision", strconv.FormatInt(outbox.Revision, 10),
			"payload_hash", outbox.PayloadHash,
			"payload", outbox.PayloadJSON,
		},
	}).Result()
	if err != nil {
		releaseErr := model.ReleaseRoutingConfigOutboxClaimContext(
			ctx, outbox.ID, outbox.ClaimToken, now+routingConfigBackoffSeconds(outbox.Attempts), err,
		)
		routingConfigState.markPublishFailure(err)
		if releaseErr != nil {
			return false, fmt.Errorf("publish config event: %v; release claim: %w", err, releaseErr)
		}
		return false, err
	}
	if err := model.MarkRoutingConfigOutboxPublishedContext(ctx, outbox.ID, outbox.ClaimToken, common.GetTimestamp()); err != nil {
		routingConfigState.markPublishFailure(err)
		return false, err
	}
	routingConfigState.markPublished(outbox.Revision)
	return true, nil
}

func ConsumeRoutingConfigOnceContext(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := loadRoutingConfigRedis()
	if client == nil {
		return 0, ErrRoutingConfigStreamUnavailable
	}
	if err := initializeRoutingConfigCursorContext(ctx, client); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		routingConfigState.markError(err)
		return 0, err
	}
	cursor := routingConfigState.currentCursor()
	streams, err := client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{routingConfigStream, cursor},
		Count:   routingConfigReadCount,
		Block:   routingConfigReadBlock,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		routingConfigState.markError(err)
		return 0, err
	}

	processed := 0
	for _, stream := range streams {
		for _, message := range stream.Messages {
			event, permanent, decodeErr := decodeRoutingConfigMessage(message)
			if decodeErr != nil {
				if !permanent {
					return processed, decodeErr
				}
				routingConfigState.markRejected(message.ID, decodeErr)
				currentRevision := int64(0)
				if metadata, ok := loadRoutingConfigSnapshotMetadata(); ok {
					currentRevision = int64(metadata.Revision)
				}
				if err := persistRoutingConfigCheckpointContext(ctx, message.ID, currentRevision); err != nil {
					routingConfigState.markCheckpointFailure(err)
				}
				processed++
				continue
			}
			if err := applyRoutingConfigEventContext(ctx, event); err != nil {
				if errors.Is(err, ErrRoutingConfigRevisionUnknown) {
					routingConfigState.markRejected(message.ID, err)
					currentRevision := int64(0)
					if metadata, ok := loadRoutingConfigSnapshotMetadata(); ok {
						currentRevision = int64(metadata.Revision)
					}
					if checkpointErr := persistRoutingConfigCheckpointContext(ctx, message.ID, currentRevision); checkpointErr != nil {
						routingConfigState.markCheckpointFailure(checkpointErr)
					}
					processed++
					continue
				}
				routingConfigState.markRefreshFailure(err)
				return processed, err
			}
			routingConfigState.markConsumed(message.ID, event.Revision)
			checkpointRevision := event.Revision
			configurationEpoch := uint64(0)
			if metadata, ok := loadRoutingConfigSnapshotMetadata(); ok {
				checkpointRevision = int64(metadata.Revision)
				configurationEpoch = metadata.ConfigurationEpoch
			}
			_, _ = publishLocalRoutingEvent(RoutingEventTypePolicyApplied, uint64(checkpointRevision), map[string]any{
				"event_revision": event.Revision, "local_revision": checkpointRevision,
				"configuration_epoch": configurationEpoch,
			})
			if err := persistRoutingConfigCheckpointContext(ctx, message.ID, checkpointRevision); err != nil {
				routingConfigState.markCheckpointFailure(err)
			}
			processed++
		}
	}
	return processed, nil
}

func initializeRoutingConfigCursorContext(ctx context.Context, client routingConfigRedis) error {
	state := routingConfigState
	state.initMu.Lock()
	defer state.initMu.Unlock()
	if state.isInitialized() {
		return nil
	}

	cursor := "0-0"
	messages, err := client.XRevRangeN(ctx, routingConfigStream, "+", "-", 1).Result()
	if err != nil {
		return err
	}
	if len(messages) > 0 {
		if messages[0].ID == "" {
			return errors.New("routing config stream tail id is invalid")
		}
		cursor = messages[0].ID
	}

	head, err := loadRoutingConfigPolicyHead(ctx)
	if err != nil {
		return err
	}
	view := SnapshotView{}
	metadata, available := loadRoutingConfigSnapshotMetadata()
	if available && int64(metadata.Revision) == head.CurrentRevision && metadata.PolicyHash == head.CurrentHash &&
		metadata.ActivationID == head.CurrentActivationID && metadata.ActivationStage == head.CurrentStage {
		view.Revision = metadata.Revision
		view.ConfigurationEpoch = metadata.ConfigurationEpoch
		view.ConfigurationHash = metadata.ConfigurationHash
		view.PolicyHash = metadata.PolicyHash
		view.ActivationID = metadata.ActivationID
		view.ActivationStage = metadata.ActivationStage
		view.TrafficBasisPoints = metadata.TrafficBasisPoints
	} else if head.CurrentRevision > 0 || head.CurrentHash != "" {
		view, err = refreshRoutingConfigSnapshot(ctx)
		if err != nil {
			return err
		}
		if int64(view.Revision) < head.CurrentRevision ||
			(int64(view.Revision) == head.CurrentRevision && view.PolicyHash != head.CurrentHash) {
			return errors.New("routing config startup snapshot did not converge to policy head")
		}
	}

	state.markInitialized(cursor, int64(view.Revision))
	if err := persistRoutingConfigCheckpointContext(ctx, cursor, int64(view.Revision)); err != nil {
		state.markCheckpointFailure(err)
	}
	return nil
}

func ReconcileRoutingConfigContext(ctx context.Context) error {
	view, err := refreshRoutingConfigSnapshot(ctx)
	if err != nil {
		return err
	}
	return persistRoutingConfigCheckpointContext(ctx, routingConfigState.currentCursor(), int64(view.Revision))
}

func decodeRoutingConfigMessage(message redis.XMessage) (model.RoutingConfigEvent, bool, error) {
	eventID, ok := routingConfigStringValue(message.Values["event_id"])
	if !ok || eventID == "" || len(eventID) > 64 {
		return model.RoutingConfigEvent{}, true, errors.New("routing config event id is invalid")
	}
	revisionText, ok := routingConfigStringValue(message.Values["revision"])
	if !ok {
		return model.RoutingConfigEvent{}, true, errors.New("routing config revision is invalid")
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision <= 0 {
		return model.RoutingConfigEvent{}, true, errors.New("routing config revision is invalid")
	}
	payloadHash, ok := routingConfigStringValue(message.Values["payload_hash"])
	if !ok || len(payloadHash) != sha256.Size*2 {
		return model.RoutingConfigEvent{}, true, errors.New("routing config payload hash is invalid")
	}
	payload, ok := routingConfigStringValue(message.Values["payload"])
	if !ok || len(payload) == 0 || len(payload) > routingConfigPayloadMaxBytes {
		return model.RoutingConfigEvent{}, true, errors.New("routing config payload is invalid")
	}
	sum := sha256.Sum256([]byte(payload))
	if hex.EncodeToString(sum[:]) != payloadHash {
		return model.RoutingConfigEvent{}, true, errors.New("routing config payload hash mismatch")
	}
	var event model.RoutingConfigEvent
	if err := common.UnmarshalJsonStr(payload, &event); err != nil {
		return model.RoutingConfigEvent{}, true, fmt.Errorf("decode routing config event: %w", err)
	}
	if event.SchemaVersion != model.RoutingPolicySchemaVersion || event.EventID != eventID || event.Revision != revision ||
		event.Revision <= event.PreviousRevision || event.ContentHash == "" || len(event.ContentHash) != sha256.Size*2 {
		return model.RoutingConfigEvent{}, true, errors.New("routing config event identity is inconsistent")
	}
	return event, false, nil
}

func applyRoutingConfigEventContext(ctx context.Context, event model.RoutingConfigEvent) error {
	if metadata, ok := loadRoutingConfigSnapshotMetadata(); ok && int64(metadata.Revision) >= event.Revision {
		routingConfigState.markIgnored(event.Revision, int64(metadata.Revision))
		return nil
	}
	view, err := refreshRoutingConfigSnapshot(ctx)
	if err != nil {
		return err
	}
	if int64(view.Revision) < event.Revision {
		head, headErr := loadRoutingConfigPolicyHead(ctx)
		if headErr != nil {
			return headErr
		}
		if head.CurrentRevision < event.Revision {
			return ErrRoutingConfigRevisionUnknown
		}
		return errors.New("routing config snapshot did not converge to event revision")
	}
	if int64(view.Revision) > event.Revision {
		routingConfigState.markIgnored(event.Revision, int64(view.Revision))
		return nil
	}
	if view.PolicyHash != event.ContentHash {
		return errors.New("routing config snapshot did not converge to event revision")
	}
	return nil
}

func persistRoutingConfigCheckpointContext(ctx context.Context, cursor string, revision int64) error {
	sequence := routingConfigState.nextSequence()
	now := common.GetTimestamp()
	policyHash := ""
	activationID := int64(0)
	activationStage := ""
	trafficBasisPoints := 0
	configurationEpoch := uint64(0)
	configurationHash := ""
	if metadata, ok := loadRoutingConfigSnapshotMetadata(); ok && int64(metadata.Revision) == revision {
		policyHash = metadata.PolicyHash
		activationID = metadata.ActivationID
		activationStage = metadata.ActivationStage
		trafficBasisPoints = metadata.TrafficBasisPoints
		configurationEpoch = metadata.ConfigurationEpoch
		configurationHash = metadata.ConfigurationHash
	}
	checkpoint, err := model.NewRoutingRuntimeCheckpoint(
		NodeEpochID(),
		RoutingConfigCheckpointKind,
		RoutingConfigCheckpointScope,
		revision,
		sequence,
		map[string]any{
			"cursor":               cursor,
			"policy_hash":          policyHash,
			"configuration_epoch":  configurationEpoch,
			"configuration_hash":   configurationHash,
			"activation_id":        activationID,
			"activation_stage":     activationStage,
			"traffic_basis_points": trafficBasisPoints,
		},
		now,
		now+int64(routingConfigCheckpointTTL/time.Second),
	)
	if err != nil {
		return err
	}
	_, err = model.UpsertRoutingRuntimeCheckpointContext(ctx, checkpoint)
	return err
}

func routingConfigBackoffSeconds(attempt int) int64 {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return int64(1 << (attempt - 1))
}

func routingConfigStringValue(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case int:
		return strconv.Itoa(typed), true
	default:
		return "", false
	}
}

func (state *routingConfigStreamState) currentCursor() string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.cursor
}

func (state *routingConfigStreamState) isInitialized() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.initialized
}

func (state *routingConfigStreamState) markInitialized(cursor string, revision int64) {
	state.mu.Lock()
	state.initialized = true
	state.cursor = cursor
	if revision > state.stats.LastAppliedRevision {
		state.stats.LastAppliedRevision = revision
	}
	state.mu.Unlock()
}

func (state *routingConfigStreamState) nextSequence() int64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.sequence++
	return state.sequence
}

func (state *routingConfigStreamState) markPublished(revision int64) {
	state.mu.Lock()
	state.stats.Published++
	state.stats.LastEventRevision = revision
	state.stats.LastPublishedAt = common.GetTimestamp()
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markPublishFailure(err error) {
	state.mu.Lock()
	state.stats.PublishFailures++
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markConsumed(cursor string, revision int64) {
	state.mu.Lock()
	state.cursor = cursor
	state.stats.Consumed++
	state.stats.LastEventRevision = revision
	if revision > state.stats.LastAppliedRevision {
		state.stats.LastAppliedRevision = revision
	}
	state.stats.LastConsumedAt = common.GetTimestamp()
	state.stats.LastError = ""
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markIgnored(eventRevision int64, currentRevision int64) {
	state.mu.Lock()
	state.stats.IgnoredOldRevision++
	state.stats.LastEventRevision = eventRevision
	state.stats.LastAppliedRevision = currentRevision
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markRejected(cursor string, err error) {
	state.mu.Lock()
	state.cursor = cursor
	state.stats.Rejected++
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markRefreshFailure(err error) {
	state.mu.Lock()
	state.stats.RefreshFailures++
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markCheckpointFailure(err error) {
	state.mu.Lock()
	state.stats.CheckpointFailures++
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func (state *routingConfigStreamState) markError(err error) {
	state.mu.Lock()
	state.stats.LastError = common.SanitizeErrorMessage(err.Error())
	state.mu.Unlock()
}

func ResetRoutingConfigStreamForTest() {
	routingConfigState = newRoutingConfigStreamState()
	refreshRoutingConfigSnapshot = RefreshSnapshotContext
	loadRoutingConfigSnapshotMetadata = CurrentSnapshotMetadata
	loadRoutingConfigPolicyHead = model.GetRoutingPolicyHeadContext
	loadRoutingConfigRedis = func() routingConfigRedis {
		if !common.RedisEnabled || common.RDB == nil {
			return nil
		}
		return common.RDB
	}
}
