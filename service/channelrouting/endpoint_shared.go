package channelrouting

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

const endpointSharedMinimumNodes = 2

var endpointSharedMaintenance sync.Mutex

type EndpointBreakerSourceView struct {
	Known               bool   `json:"known"`
	State               string `json:"state"`
	Reason              string `json:"reason"`
	CooldownUntilUnix   int64  `json:"cooldown_until"`
	UpdatedUnix         int64  `json:"updated_at"`
	ExpiresUnix         int64  `json:"expires_at,omitempty"`
	EvidenceCount       int64  `json:"evidence_count,omitempty"`
	NetworkFailureCount int64  `json:"network_failure_count,omitempty"`
	NodeCount           int    `json:"node_count,omitempty"`
	FailureNodeCount    int    `json:"failure_node_count,omitempty"`
}

type EndpointBreakerView struct {
	EndpointAuthority string                    `json:"endpoint_authority"`
	Region            string                    `json:"region"`
	Local             EndpointBreakerSourceView `json:"local"`
	Shared            EndpointBreakerSourceView `json:"shared"`
	Effective         EndpointBreakerSourceView `json:"effective"`
}

func FlushAndRefreshSharedEndpointBreakersContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpointSharedMaintenance.Lock()
	defer endpointSharedMaintenance.Unlock()
	if !model.RoutingEndpointSchemaReady() {
		return nil
	}

	if err := flushRoutingEndpointEvidenceContext(ctx); err != nil {
		return err
	}
	if err := evaluateRoutingEndpointSharedStateContext(ctx, setting); err != nil {
		return err
	}
	return refreshRoutingEndpointSharedCacheContext(ctx)
}

func RefreshSharedEndpointBreakersContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpointSharedMaintenance.Lock()
	defer endpointSharedMaintenance.Unlock()
	if !model.RoutingEndpointSchemaReady() {
		return nil
	}
	return refreshRoutingEndpointSharedCacheContext(ctx)
}

func flushRoutingEndpointEvidenceContext(ctx context.Context) error {
	snapshots := routingmetrics.EndpointSnapshots()
	if len(snapshots) == 0 {
		return nil
	}
	stableNodeID, quorumEligible := StableNodeID()
	nodeID := stableNodeID
	if nodeID == "" {
		nodeID = NodeEpochID()
	}
	rows := make([]model.RoutingEndpointEvidence, 0, min(len(snapshots), model.RoutingEndpointEvidenceMaxBatch))
	for start := 0; start < len(snapshots); start += model.RoutingEndpointEvidenceMaxBatch {
		end := min(start+model.RoutingEndpointEvidenceMaxBatch, len(snapshots))
		rows = rows[:0]
		for _, snapshot := range snapshots[start:end] {
			if snapshot.RequestCount <= 0 {
				continue
			}
			rows = append(rows, model.RoutingEndpointEvidence{
				NodeID: nodeID, NodeEpochID: NodeEpochID(), QuorumEligible: quorumEligible,
				EndpointHost: snapshot.EndpointHost, EndpointAuthority: snapshot.EndpointAuthority,
				Region: snapshot.Region, ResetGeneration: snapshot.ResetGeneration,
				BucketTs:     snapshot.BucketTs,
				RequestCount: snapshot.RequestCount, ReachableCount: snapshot.ReachableCount,
				NetworkFailureCount: snapshot.NetworkFailureCount, TotalLatencyMs: snapshot.TotalLatencyMs,
				TtftSumMs: snapshot.TtftSumMs, TtftCount: snapshot.TtftCount,
			})
		}
		if _, err := model.UpsertRoutingEndpointEvidenceContext(ctx, rows); err != nil {
			return err
		}
	}
	return nil
}

func evaluateRoutingEndpointSharedStateContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
) error {
	region := RoutingRegion()
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return err
	}
	windowSeconds := endpointSharedWindowSeconds(setting)
	cutoffSeconds := nowMs/1000 - windowSeconds
	rows, evaluatedAtMs, err := model.AggregateRoutingEndpointEvidenceContext(
		ctx, region, cutoffSeconds, nowMs-windowSeconds*1000,
	)
	if err != nil {
		return err
	}
	type endpointAggregate struct {
		host            string
		authority       string
		region          string
		evidence        int64
		failures        int64
		nodes           int
		failureNodes    int
		evidenceThrough int64
		resetGeneration int64
	}
	aggregates := make(map[string]*endpointAggregate)
	for _, row := range rows {
		key := row.EndpointAuthorityKey + "\x00" + row.RegionKey
		aggregate := aggregates[key]
		if aggregate == nil {
			aggregate = &endpointAggregate{
				host: row.EndpointHost, authority: row.EndpointAuthority, region: row.Region,
				resetGeneration: row.ResetGeneration,
			}
			aggregates[key] = aggregate
		}
		if aggregate.resetGeneration != row.ResetGeneration {
			return model.ErrRoutingEndpointEvidenceInvalid
		}
		evaluated := row.ReachableCount + row.NetworkFailureCount
		if evaluated < 0 || row.NetworkFailureCount > evaluated || math.MaxInt64-aggregate.evidence < evaluated ||
			math.MaxInt64-aggregate.failures < row.NetworkFailureCount {
			return model.ErrRoutingEndpointEvidenceInvalid
		}
		aggregate.evidence += evaluated
		aggregate.failures += row.NetworkFailureCount
		aggregate.nodes++
		if row.NetworkFailureCount > 0 {
			aggregate.failureNodes++
		}
		aggregate.evidenceThrough = max(aggregate.evidenceThrough, row.EvidenceThroughMs)
	}
	keys := make([]string, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	states := make([]model.RoutingEndpointSharedState, 0, len(keys))
	minimumVolume := max(int64(setting.MinVolume), int64(10))
	openRate := float64(setting.FailureRatePct) / 100
	if openRate <= 0 || openRate > 1 {
		openRate = 0.5
	}
	degradedRate := math.Max(0.1, openRate/2)
	for _, key := range keys {
		aggregate := aggregates[key]
		state := model.RoutingBreakerStateHealthy
		reason := ""
		cooldownUntilMs := int64(0)
		failureRate := float64(0)
		if aggregate.evidence > 0 {
			failureRate = float64(aggregate.failures) / float64(aggregate.evidence)
		}
		quorum := aggregate.nodes >= endpointSharedMinimumNodes && aggregate.failureNodes >= endpointSharedMinimumNodes
		if quorum && aggregate.evidence >= minimumVolume && failureRate >= openRate {
			state = model.RoutingBreakerStateOpen
			reason = "regional_network_quorum"
			cooldownUntilMs = evaluatedAtMs + int64(max(setting.BaseCooldownSec, 1))*1000
		} else if quorum && aggregate.evidence >= max(minimumVolume/2, int64(10)) && failureRate >= degradedRate {
			state = model.RoutingBreakerStateDegraded
			reason = "regional_network_degraded"
		}
		states = append(states, model.RoutingEndpointSharedState{
			EndpointHost: aggregate.host, EndpointAuthority: aggregate.authority, Region: aggregate.region,
			ResetGeneration: aggregate.resetGeneration,
			State:           state, Reason: reason, EvidenceCount: aggregate.evidence,
			NetworkFailureCount: aggregate.failures, NodeCount: aggregate.nodes, FailureNodeCount: aggregate.failureNodes,
			CooldownUntilMs: cooldownUntilMs, EvidenceFromMs: cutoffSeconds * 1000,
			EvidenceThroughMs: aggregate.evidenceThrough, EvaluatedAtMs: evaluatedAtMs,
			ExpiresAtMs: evaluatedAtMs + windowSeconds*1000, CreatedTimeMs: evaluatedAtMs, UpdatedTimeMs: evaluatedAtMs,
		})
	}
	return model.UpsertRoutingEndpointSharedStatesContext(ctx, states)
}

func refreshRoutingEndpointSharedCacheContext(ctx context.Context) error {
	states, nowMs, err := model.ListFreshRoutingEndpointSharedStatesContext(ctx, RoutingRegion())
	if err != nil {
		return err
	}
	entries := make([]routinghotcache.SharedEndpointBreakerEntry, 0, len(states))
	for _, state := range states {
		if state.ExpiresAtMs <= nowMs {
			continue
		}
		entries = append(entries, routinghotcache.SharedEndpointBreakerEntry{
			Key: routingbreaker.NewEndpointKey(state.EndpointAuthority, state.Region).HotcacheKey(),
			Snapshot: routinghotcache.SharedEndpointBreakerSnapshot{
				State: state.State, Reason: state.Reason, CooldownUntilUnix: state.CooldownUntilMs / 1000,
				UpdatedUnix: state.UpdatedTimeMs / 1000, ExpiresUnix: state.ExpiresAtMs / 1000,
				EvidenceCount: state.EvidenceCount, NetworkFailureCount: state.NetworkFailureCount,
				NodeCount: state.NodeCount, FailureNodeCount: state.FailureNodeCount,
			},
		})
	}
	routinghotcache.ReplaceSharedEndpointBreakers(entries)
	return nil
}

func endpointSharedWindowSeconds(setting smart_routing_setting.SmartRoutingSetting) int64 {
	window := int64(max(setting.FlushIntervalMin, 1) * 120)
	if window < 120 {
		window = 120
	}
	if setting.SnapshotStaleSec > 0 && window > int64(setting.SnapshotStaleSec) {
		window = int64(setting.SnapshotStaleSec)
	}
	if window > 600 {
		window = 600
	}
	return max(window, int64(60))
}

func ListEndpointBreakerViews() []EndpointBreakerView {
	type keyedView struct {
		view EndpointBreakerView
	}
	views := make(map[string]*keyedView)
	for _, entry := range routinghotcache.ListLocalEndpointBreakers() {
		key := entry.Key.EndpointAuthority + "\x00" + entry.Key.Region
		item := views[key]
		if item == nil {
			item = &keyedView{view: EndpointBreakerView{EndpointAuthority: entry.Key.EndpointAuthority, Region: entry.Key.Region}}
			views[key] = item
		}
		item.view.Local = endpointSourceView(entry.Snapshot, entry.Snapshot.UpdatedUnix > 0)
	}
	for _, entry := range routinghotcache.ListSharedEndpointBreakers() {
		key := entry.Key.EndpointAuthority + "\x00" + entry.Key.Region
		item := views[key]
		if item == nil {
			item = &keyedView{view: EndpointBreakerView{EndpointAuthority: entry.Key.EndpointAuthority, Region: entry.Key.Region}}
			views[key] = item
		}
		item.view.Shared = endpointSourceView(entry.Snapshot, true)
	}
	keys := make([]string, 0, len(views))
	for key := range views {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EndpointBreakerView, 0, len(keys))
	for _, key := range keys {
		view := views[key].view
		view.Effective = mergeEndpointSourceViews(view.Local, view.Shared)
		result = append(result, view)
	}
	return result
}

func endpointSourceView(snapshot routinghotcache.SharedEndpointBreakerSnapshot, known bool) EndpointBreakerSourceView {
	return EndpointBreakerSourceView{
		Known: known, State: snapshot.State, Reason: snapshot.Reason,
		CooldownUntilUnix: snapshot.CooldownUntilUnix, UpdatedUnix: snapshot.UpdatedUnix, ExpiresUnix: snapshot.ExpiresUnix,
		EvidenceCount: snapshot.EvidenceCount, NetworkFailureCount: snapshot.NetworkFailureCount,
		NodeCount: snapshot.NodeCount, FailureNodeCount: snapshot.FailureNodeCount,
	}
}

func mergeEndpointSourceViews(local EndpointBreakerSourceView, shared EndpointBreakerSourceView) EndpointBreakerSourceView {
	if !local.Known {
		return shared
	}
	if !shared.Known {
		return local
	}
	localSnapshot := &routingBreakerSnapshotView{State: local.State, Reason: local.Reason, UpdatedUnix: local.UpdatedUnix}
	sharedSnapshot := &routingBreakerSnapshotView{State: shared.State, Reason: shared.Reason, UpdatedUnix: shared.UpdatedUnix}
	if endpointSourceRank(sharedSnapshot) > endpointSourceRank(localSnapshot) ||
		(endpointSourceRank(sharedSnapshot) == endpointSourceRank(localSnapshot) && shared.UpdatedUnix > local.UpdatedUnix) {
		return shared
	}
	return local
}

type routingBreakerSnapshotView struct {
	State       string
	Reason      string
	UpdatedUnix int64
}

func endpointSourceRank(snapshot *routingBreakerSnapshotView) int {
	if snapshot == nil {
		return 0
	}
	switch snapshot.State {
	case model.RoutingBreakerStateOpen:
		return 4
	case model.RoutingBreakerStateHalfOpen:
		return 3
	case model.RoutingBreakerStateDegraded:
		return 2
	case model.RoutingBreakerStateHealthy:
		return 1
	default:
		return 0
	}
}

func DeleteExpiredRoutingEndpointHistoryContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		return 0, nil
	}
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(ctx)
	if err != nil {
		return 0, err
	}
	maximumDays := int64(math.MaxInt64 / int64(24*time.Hour/time.Millisecond))
	days := int64(retentionDays)
	if days > maximumDays {
		days = maximumDays
	}
	return model.DeleteRoutingEndpointHistoryBeforeContext(ctx, nowMs-days*int64(24*time.Hour/time.Millisecond))
}
