package channelrouting

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	routingEventBufferCapacity    = 2_048
	routingEventSubscriberBuffer  = 64
	routingEventSubscriberMaximum = 64
	routingEventPayloadMaximum    = 16 << 10
	routingEventTypeMaximumBytes  = 64

	RoutingEventTypeReady                       = "routing.ready"
	RoutingEventTypeReset                       = "routing.reset"
	RoutingEventTypePolicyDraftChanged          = "routing.policy_draft.changed"
	RoutingEventTypePolicySimulation            = "routing.policy_simulation.completed"
	RoutingEventTypePolicyPublished             = "routing.policy.published"
	RoutingEventTypePolicyRolledBack            = "routing.policy.rolled_back"
	RoutingEventTypePolicyApplied               = "routing.policy.applied"
	RoutingEventTypeOperationChanged            = "routing.operation.changed"
	RoutingEventTypeChannelConfigurationChanged = "routing.channel_configuration.changed"
	RoutingEventTypeRuntimeSettingsChanged      = "routing.runtime_settings.changed"
	RoutingEventTypePricingChanged              = "routing.pricing.changed"
	RoutingEventTypeProbeCompleted              = "routing.probe.completed"
	RoutingEventTypeAuditExportReady            = "routing.audit_export.ready"
	RoutingEventTypeErrorBudgetChanged          = "routing.error_budget.changed"
	RoutingEventTypeBreakerReset                = "routing.breaker.reset"
	RoutingEventTypeBreakerOpened               = "routing.breaker.opened"
	RoutingEventTypeBreakerRecovered            = "routing.breaker.recovered"
)

var (
	ErrRoutingEventInvalid              = errors.New("invalid channel routing event")
	ErrRoutingEventSubscribersFull      = errors.New("channel routing event subscriber limit reached")
	ErrRoutingEventTransportUnavailable = errors.New("channel routing event transport is unavailable")
)

type RoutingEvent struct {
	ID            uint64 `json:"id"`
	Type          string `json:"type"`
	Revision      uint64 `json:"revision,omitempty"`
	CreatedTimeMs int64  `json:"created_time_ms"`
	PayloadJSON   []byte `json:"-"`
}

type RoutingEventReplay struct {
	Events     []RoutingEvent
	Requested  uint64
	EarliestID uint64
	LatestID   uint64
	Gap        bool
}

type RoutingEventStats struct {
	Buffered         int                        `json:"buffered"`
	Capacity         int                        `json:"capacity"`
	Subscribers      int                        `json:"subscribers"`
	SubscriberLimit  int                        `json:"subscriber_limit"`
	LatestID         uint64                     `json:"latest_id"`
	Evicted          uint64                     `json:"evicted"`
	SlowDisconnected uint64                     `json:"slow_disconnected"`
	Rejected         uint64                     `json:"rejected"`
	Transport        RoutingEventTransportStats `json:"transport"`
}

type routingEventHub struct {
	mu sync.Mutex

	nextID           uint64
	events           []RoutingEvent
	eventStart       int
	eventCount       int
	subscribers      map[uint64]chan RoutingEvent
	subscriberLimit  int
	nextSubscriber   uint64
	evicted          uint64
	slowDisconnected uint64
	rejected         uint64
}

var (
	defaultRoutingEventHubMu sync.RWMutex
	defaultRoutingEventHub   = newRoutingEventHub(routingEventBufferCapacity)
)

func newRoutingEventHub(capacity int) *routingEventHub {
	if capacity < 1 {
		capacity = 1
	}
	return &routingEventHub{
		events:          make([]RoutingEvent, capacity),
		subscribers:     make(map[uint64]chan RoutingEvent),
		subscriberLimit: routingEventSubscriberMaximum,
	}
}

func PublishRoutingEvent(eventType string, revision uint64, payload any) (RoutingEvent, error) {
	event, err := publishLocalRoutingEvent(eventType, revision, payload)
	if err != nil {
		return RoutingEvent{}, err
	}
	broadcastRoutingEvent(event)
	return event, nil
}

func publishLocalRoutingEvent(eventType string, revision uint64, payload any) (RoutingEvent, error) {
	encoded, err := common.Marshal(payload)
	if err != nil {
		return RoutingEvent{}, err
	}
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	return hub.publish(eventType, revision, encoded, time.Now())
}

func SubscribeRoutingEvents(afterID uint64) (RoutingEventReplay, <-chan RoutingEvent, func(), error) {
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	return hub.subscribe(afterID, routingEventSubscriberBuffer, true)
}

func SubscribeCurrentRoutingEvents() (RoutingEventReplay, <-chan RoutingEvent, func(), error) {
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	return hub.subscribe(0, routingEventSubscriberBuffer, false)
}

func CurrentRoutingEventStats() RoutingEventStats {
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	stats := hub.stats()
	stats.Transport = RoutingEventTransportRuntimeStats()
	return stats
}

func RecentRoutingEvents(limit int, eventTypes ...string) []RoutingEvent {
	if limit <= 0 {
		return []RoutingEvent{}
	}
	allowed := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		eventType = strings.TrimSpace(eventType)
		if validRoutingEventType(eventType) {
			allowed[eventType] = struct{}{}
		}
	}
	defaultRoutingEventHubMu.RLock()
	hub := defaultRoutingEventHub
	defaultRoutingEventHubMu.RUnlock()
	return hub.recent(limit, allowed)
}

func (hub *routingEventHub) publish(
	eventType string,
	revision uint64,
	payload []byte,
	now time.Time,
) (RoutingEvent, error) {
	eventType = strings.TrimSpace(eventType)
	var payloadObject map[string]any
	if hub == nil || now.IsZero() || !validRoutingEventType(eventType) || len(payload) == 0 ||
		len(payload) > routingEventPayloadMaximum || common.Unmarshal(payload, &payloadObject) != nil || payloadObject == nil {
		return RoutingEvent{}, ErrRoutingEventInvalid
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.nextID == ^uint64(0) {
		return RoutingEvent{}, ErrRoutingEventInvalid
	}
	hub.nextID++
	event := RoutingEvent{
		ID: hub.nextID, Type: eventType, Revision: revision,
		CreatedTimeMs: now.UnixMilli(), PayloadJSON: append([]byte(nil), payload...),
	}
	if hub.eventCount < len(hub.events) {
		index := (hub.eventStart + hub.eventCount) % len(hub.events)
		hub.events[index] = event
		hub.eventCount++
	} else {
		hub.events[hub.eventStart] = event
		hub.eventStart = (hub.eventStart + 1) % len(hub.events)
		hub.evicted++
	}
	for id, subscriber := range hub.subscribers {
		select {
		case subscriber <- cloneRoutingEvent(event):
		default:
			for draining := true; draining; {
				select {
				case <-subscriber:
				default:
					draining = false
				}
			}
			resetPayload, _ := common.Marshal(map[string]any{
				"reason": "slow_subscriber", "earliest_id": hub.events[hub.eventStart].ID,
				"latest_id": hub.nextID, "refresh_all": true,
			})
			subscriber <- RoutingEvent{
				ID: hub.nextID, Type: RoutingEventTypeReset, CreatedTimeMs: now.UnixMilli(), PayloadJSON: resetPayload,
			}
			close(subscriber)
			delete(hub.subscribers, id)
			hub.slowDisconnected++
		}
	}
	return cloneRoutingEvent(event), nil
}

func (hub *routingEventHub) subscribe(afterID uint64, buffer int, replayExisting bool) (RoutingEventReplay, <-chan RoutingEvent, func(), error) {
	if hub == nil {
		closed := make(chan RoutingEvent)
		close(closed)
		return RoutingEventReplay{Requested: afterID, Gap: true}, closed, func() {}, ErrRoutingEventInvalid
	}
	if buffer < 1 {
		buffer = 1
	}
	hub.mu.Lock()
	if len(hub.subscribers) >= hub.subscriberLimit {
		hub.rejected++
		hub.mu.Unlock()
		closed := make(chan RoutingEvent)
		close(closed)
		return RoutingEventReplay{Requested: afterID, Gap: true}, closed, func() {}, ErrRoutingEventSubscribersFull
	}
	if !replayExisting {
		afterID = hub.nextID
	}
	replay := RoutingEventReplay{Requested: afterID, LatestID: hub.nextID}
	if hub.eventCount == 0 {
		replay.Gap = afterID > hub.nextID
	} else {
		replay.EarliestID = hub.events[hub.eventStart].ID
		replay.Gap = replayExisting && (afterID > hub.nextID || (afterID > 0 && afterID < replay.EarliestID-1))
		if !replay.Gap {
			for offset := 0; offset < hub.eventCount; offset++ {
				event := hub.events[(hub.eventStart+offset)%len(hub.events)]
				if event.ID > afterID {
					replay.Events = append(replay.Events, cloneRoutingEvent(event))
				}
			}
		}
	}
	hub.nextSubscriber++
	subscriberID := hub.nextSubscriber
	stream := make(chan RoutingEvent, buffer)
	hub.subscribers[subscriberID] = stream
	hub.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			hub.mu.Lock()
			if current, exists := hub.subscribers[subscriberID]; exists && current == stream {
				delete(hub.subscribers, subscriberID)
				close(stream)
			}
			hub.mu.Unlock()
		})
	}
	return replay, stream, cancel, nil
}

func (hub *routingEventHub) stats() RoutingEventStats {
	if hub == nil {
		return RoutingEventStats{}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return RoutingEventStats{
		Buffered: hub.eventCount, Capacity: len(hub.events), Subscribers: len(hub.subscribers),
		SubscriberLimit: hub.subscriberLimit, LatestID: hub.nextID, Evicted: hub.evicted,
		SlowDisconnected: hub.slowDisconnected, Rejected: hub.rejected,
	}
}

func (hub *routingEventHub) recent(limit int, allowed map[string]struct{}) []RoutingEvent {
	if hub == nil || limit <= 0 {
		return []RoutingEvent{}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if limit > hub.eventCount {
		limit = hub.eventCount
	}
	events := make([]RoutingEvent, 0, limit)
	for offset := hub.eventCount - 1; offset >= 0 && len(events) < limit; offset-- {
		event := hub.events[(hub.eventStart+offset)%len(hub.events)]
		if len(allowed) > 0 {
			if _, ok := allowed[event.Type]; !ok {
				continue
			}
		}
		events = append(events, cloneRoutingEvent(event))
	}
	return events
}

func validRoutingEventType(eventType string) bool {
	if eventType == "" || len(eventType) > routingEventTypeMaximumBytes {
		return false
	}
	for index := range eventType {
		char := eventType[index]
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func cloneRoutingEvent(event RoutingEvent) RoutingEvent {
	event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
	return event
}

func ResetRoutingEventsForTest() {
	defaultRoutingEventHubMu.Lock()
	defaultRoutingEventHub = newRoutingEventHub(routingEventBufferCapacity)
	defaultRoutingEventHubMu.Unlock()
	resetRoutingChannelConfigurationEventEpochForTest()
}
