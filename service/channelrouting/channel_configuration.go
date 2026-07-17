package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const routingChannelConfigurationPublishLease = 30 * time.Second

var ErrRoutingChannelConfigurationETagInvalid = errors.New("invalid routing channel configuration ETag")

var routingChannelConfigurationEventEpoch atomic.Uint64

type routingChannelConfigurationEventState struct {
	configuration  model.RoutingChannelConfiguration
	channelDeleted bool
	staleLifecycle bool
}

type RoutingChannelConfigurationETag struct {
	ChannelID         int
	RoutingIdentity   string
	RoutingGeneration string
	Revision          int64
	StateHash         string
}

func ChannelConfigurationETag(configuration model.RoutingChannelConfiguration) (string, error) {
	stateHash, err := model.RoutingChannelConfigurationStateHash(configuration)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"\"rcc2.%s.%s.%d.%d.%s\"",
		configuration.RoutingIdentity,
		configuration.RoutingGeneration,
		configuration.ChannelID,
		configuration.Revision,
		stateHash,
	), nil
}

func ParseChannelConfigurationETag(value string) (RoutingChannelConfigurationETag, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 256 || value[0] != '"' || value[len(value)-1] != '"' {
		return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
	}
	parts := strings.Split(value[1:len(value)-1], ".")
	if len(parts) == 4 && parts[0] == "rcc" {
		channelID, err := strconv.Atoi(parts[1])
		if err != nil || channelID <= 0 {
			return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
		}
		revision, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || revision <= 0 || !validChannelConfigurationETagHash(parts[3]) {
			return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
		}
		return RoutingChannelConfigurationETag{ChannelID: channelID, Revision: revision, StateHash: parts[3]}, nil
	}
	if len(parts) != 6 || parts[0] != "rcc2" || !validChannelConfigurationETagIdentity(parts[1]) ||
		!validChannelConfigurationETagIdentity(parts[2]) || !validChannelConfigurationETagHash(parts[5]) {
		return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
	}
	channelID, err := strconv.Atoi(parts[3])
	if err != nil || channelID <= 0 {
		return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
	}
	revision, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || revision <= 0 {
		return RoutingChannelConfigurationETag{}, ErrRoutingChannelConfigurationETagInvalid
	}
	return RoutingChannelConfigurationETag{
		ChannelID: channelID, RoutingIdentity: parts[1], RoutingGeneration: parts[2],
		Revision: revision, StateHash: parts[5],
	}, nil
}

func validChannelConfigurationETagIdentity(value string) bool {
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

func validChannelConfigurationETagHash(value string) bool {
	if len(value) != 64 {
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

func ChannelConfigurationCostBasisSummary(models []string) (int, bool) {
	unique := make(map[string]struct{}, len(models))
	for _, modelName := range models {
		modelName = strings.TrimSpace(modelName)
		if modelName != "" {
			unique[modelName] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return 0, false
	}
	available := true
	for modelName := range unique {
		if billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr {
			expression, exists := billing_setting.GetBillingExpr(modelName)
			if !exists || strings.TrimSpace(expression) == "" {
				available = false
				continue
			}
			if _, err := billingexpr.CompileFromCache(expression); err != nil {
				available = false
			}
			continue
		}
		value, _, exists := ratio_setting.GetModelRatioOrPrice(modelName)
		if !exists || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			available = false
		}
	}
	return len(unique), available
}

func PublishRoutingChannelConfigurationOutboxOnceContext(ctx context.Context) (bool, error) {
	return publishRoutingChannelConfigurationOutboxContext(ctx, 0)
}

func PublishRoutingChannelConfigurationOutboxByIDContext(ctx context.Context, outboxID int64) (bool, error) {
	if outboxID <= 0 {
		return false, model.ErrRoutingChannelConfigurationInvalid
	}
	return publishRoutingChannelConfigurationOutboxContext(ctx, outboxID)
}

func publishRoutingChannelConfigurationOutboxContext(ctx context.Context, outboxID int64) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := common.GetTimestamp()
	outbox, err := model.ClaimRoutingChannelConfigurationOutboxContext(
		ctx, outboxID, now, int64(routingChannelConfigurationPublishLease/time.Second),
	)
	if err != nil || outbox == nil {
		return false, err
	}
	eventPayload, err := outbox.DecodePayload()
	if err != nil {
		return false, releaseRoutingChannelConfigurationOutbox(ctx, *outbox, err)
	}
	state, err := refreshRoutingChannelConfigurationEventContext(ctx, eventPayload)
	if err != nil {
		return false, releaseRoutingChannelConfigurationOutbox(ctx, *outbox, err)
	}
	epochAdvanced := reserveRoutingChannelConfigurationEventEpoch(uint64(eventPayload.ConfigEpoch), false)
	applyRoutingChannelConfigurationEventState(eventPayload, state)
	var event RoutingEvent
	if epochAdvanced {
		event, err = publishLocalRoutingEvent(
			RoutingEventTypeChannelConfigurationChanged,
			uint64(eventPayload.ConfigEpoch),
			eventPayload,
		)
	} else {
		// Global epochs order snapshots, but an older event can belong to a
		// different channel. Keep it off the local SSE ring while still sending
		// it to peer nodes so each node can converge that channel from database
		// truth instead of silently losing a channel-scoped invalidation.
		event = RoutingEvent{
			ID:            uint64(outbox.ID),
			Type:          RoutingEventTypeChannelConfigurationChanged,
			Revision:      uint64(eventPayload.ConfigEpoch),
			CreatedTimeMs: time.Now().UnixMilli(),
			PayloadJSON:   []byte(outbox.PayloadJSON),
		}
	}
	if err != nil {
		return false, releaseRoutingChannelConfigurationOutbox(ctx, *outbox, err)
	}
	client := loadRoutingEventRedis()
	if client != nil {
		if err := broadcastRoutingEventContext(ctx, defaultRoutingEventTransport, client, NodeEpochID(), event); err != nil {
			return false, releaseRoutingChannelConfigurationOutbox(ctx, *outbox, err)
		}
	}
	if err := model.MarkRoutingChannelConfigurationOutboxPublishedContext(
		ctx, outbox.ID, outbox.ClaimToken, common.GetTimestamp(),
	); err != nil {
		return false, err
	}
	return true, nil
}

func consumeRoutingChannelConfigurationEventPayload(ctx context.Context, payload []byte) (bool, error) {
	event, err := model.DecodeRoutingChannelConfigurationEvent(payload)
	if err != nil {
		return false, err
	}
	state, err := refreshRoutingChannelConfigurationEventContext(ctx, event)
	if err != nil {
		return false, err
	}
	epochAdvanced := reserveRoutingChannelConfigurationEventEpoch(uint64(event.ConfigEpoch), false)
	applyRoutingChannelConfigurationEventState(event, state)
	return epochAdvanced, nil
}

func reserveRoutingChannelConfigurationEventEpoch(epoch uint64, allowEqual bool) bool {
	if epoch == 0 {
		return false
	}
	for {
		current := routingChannelConfigurationEventEpoch.Load()
		if epoch < current || epoch == current && !allowEqual {
			return false
		}
		if epoch == current || routingChannelConfigurationEventEpoch.CompareAndSwap(current, epoch) {
			return true
		}
	}
}

func refreshRoutingChannelConfigurationEventContext(
	ctx context.Context,
	event model.RoutingChannelConfigurationEvent,
) (routingChannelConfigurationEventState, error) {
	// Publish committed database truth before advancing the event epoch. A
	// transient refresh failure can then be retried without acknowledging the
	// event, while snapshot CAS prevents an older event from restoring old cost.
	if ctx == nil {
		ctx = context.Background()
	}
	if event.RoutingGeneration == "" || event.RoutingIdentity == "" || event.AggregateID == "" {
		// Pre-v2 events cannot prove which lifecycle they belong to. Acknowledge
		// them without applying channel-scoped state so a reused numeric ID can
		// never reinterpret legacy work as belonging to the current channel.
		return routingChannelConfigurationEventState{staleLifecycle: true}, nil
	}
	view, err := RefreshSnapshotContext(ctx)
	if err != nil {
		return routingChannelConfigurationEventState{}, err
	}
	if view.ConfigurationEpoch < uint64(event.ConfigEpoch) ||
		(view.ConfigurationEpoch == uint64(event.ConfigEpoch) && view.ConfigurationHash != event.ConfigurationHash) {
		return routingChannelConfigurationEventState{}, model.ErrRoutingChannelConfigurationChanged
	}
	var snapshotChannel *ChannelSnapshot
	for index := range view.Channels {
		if view.Channels[index].ID == event.ChannelID {
			snapshotChannel = &view.Channels[index]
			break
		}
	}
	configuration, err := model.GetRoutingChannelConfigurationContext(ctx, event.ChannelID)
	if errors.Is(err, gorm.ErrRecordNotFound) && snapshotChannel == nil {
		// A successful full snapshot contains every channel and configuration.
		// Their joint absence is authoritative deletion, not a transient lookup
		// miss, so the old update event must be consumed instead of poisoning the
		// Redis cursor forever.
		return routingChannelConfigurationEventState{channelDeleted: true}, nil
	}
	if err != nil {
		return routingChannelConfigurationEventState{}, err
	}
	if configuration.RoutingIdentity != event.RoutingIdentity ||
		configuration.RoutingGeneration != event.RoutingGeneration ||
		event.AggregateID != event.RoutingGeneration || event.AggregateRevision != event.Revision {
		return routingChannelConfigurationEventState{staleLifecycle: true}, nil
	}
	if configuration.Revision < event.Revision {
		return routingChannelConfigurationEventState{}, model.ErrRoutingChannelConfigurationChanged
	}
	if snapshotChannel != nil &&
		(configuration.Revision != snapshotChannel.ConfigurationRevision ||
			configuration.UpstreamCostMultiplier != snapshotChannel.UpstreamCostMultiplier ||
			configuration.TrafficClass != snapshotChannel.TrafficClass) {
		return routingChannelConfigurationEventState{}, model.ErrRoutingChannelConfigurationChanged
	}
	if configuration.Revision == event.Revision {
		stateHash, hashErr := model.RoutingChannelConfigurationStateHash(configuration)
		if hashErr != nil {
			return routingChannelConfigurationEventState{}, hashErr
		}
		if stateHash != event.StateHash {
			return routingChannelConfigurationEventState{}, model.ErrRoutingChannelConfigurationChanged
		}
	}
	return routingChannelConfigurationEventState{configuration: configuration}, nil
}

func applyRoutingChannelConfigurationEventState(
	event model.RoutingChannelConfigurationEvent,
	state routingChannelConfigurationEventState,
) {
	if state.staleLifecycle {
		return
	}
	if state.channelDeleted {
		routinghotcache.DeleteChannelTrafficPolicy(event.ChannelID, event.UpdatedTime)
		model.NotifyRoutingTopologyChanged()
		return
	}
	ApplyCommittedRoutingChannelConfiguration(state.configuration)
}

func ApplyCommittedRoutingChannelConfiguration(configuration model.RoutingChannelConfiguration) {
	if configuration.ChannelID <= 0 || configuration.UpdatedTime <= 0 ||
		configuration.TrafficClass != model.RoutingChannelTrafficClassAll &&
			configuration.TrafficClass != model.RoutingChannelTrafficClassClaudeCodeOnly {
		return
	}
	routinghotcache.SetChannelTrafficPolicy(
		configuration.ChannelID,
		configuration.TrafficClass == model.RoutingChannelTrafficClassClaudeCodeOnly,
		configuration.UpdatedTime,
	)
	model.NotifyRoutingTopologyChanged()
}

func resetRoutingChannelConfigurationEventEpochForTest() {
	routingChannelConfigurationEventEpoch.Store(0)
}

func releaseRoutingChannelConfigurationOutbox(
	ctx context.Context,
	outbox model.RoutingChannelConfigurationOutbox,
	publishErr error,
) error {
	nextAttempt := common.GetTimestamp() + routingConfigBackoffSeconds(outbox.Attempts)
	releaseErr := model.ReleaseRoutingChannelConfigurationOutboxClaimContext(
		ctx, outbox.ID, outbox.ClaimToken, nextAttempt, publishErr,
	)
	if releaseErr != nil {
		return fmt.Errorf("publish routing channel configuration: %v; release outbox claim: %w", publishErr, releaseErr)
	}
	return publishErr
}
