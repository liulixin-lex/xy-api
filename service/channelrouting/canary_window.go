package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
)

const (
	CanaryCohortWindowCheckpointKind = "canary_cohort_window"
	canaryCohortWindowSchemaVersion  = 1
	defaultCanaryWindowMaxEntries    = 8_192
	defaultCanaryWindowShards        = 32
	defaultCanaryWindowTTL           = 2 * time.Hour
	defaultCanaryWindowFlushLimit    = 100
	canaryWindowScopeMaxBytes        = 512
)

var (
	ErrCanaryWindowInvalid     = errors.New("invalid channel routing canary outcome window")
	ErrCanaryWindowClosed      = errors.New("channel routing canary outcome window is closed")
	ErrCanaryWindowEntriesFull = errors.New("channel routing canary outcome window entry limit reached")
	ErrCanaryWindowOverflow    = errors.New("channel routing canary outcome window counter overflow")
)

type CanaryWindowIdentity struct {
	PoolID             int        `json:"pool_id"`
	ActivationID       int64      `json:"activation_id"`
	PolicyRevision     uint64     `json:"policy_revision"`
	TrafficBasisPoints int        `json:"traffic_basis_points"`
	RolloutKey         RolloutKey `json:"rollout_key"`
	WindowSeconds      int        `json:"window_seconds"`
	LatenessSeconds    int        `json:"lateness_seconds"`
}

type CanaryLogicalOutcome struct {
	Identity                    CanaryWindowIdentity `json:"identity"`
	Cohort                      string               `json:"cohort"`
	CompletedAt                 time.Time            `json:"completed_at"`
	Success                     bool                 `json:"success"`
	RoutingFailure              bool                 `json:"routing_failure"`
	Attempts                    int64                `json:"attempts"`
	CostKnown                   bool                 `json:"cost_known"`
	ExpectedPlatformCostNanoUSD int64                `json:"expected_platform_cost_nano_usd"`
	ClientTTFTMilliseconds      int64                `json:"client_ttft_ms"`
}

type CanaryCohortWindowStats struct {
	LogicalRequests             int64  `json:"logical_requests"`
	Successes                   int64  `json:"successes"`
	Failures                    int64  `json:"failures"`
	RoutingFailures             int64  `json:"routing_failures"`
	Attempts                    int64  `json:"attempts"`
	CostKnownRequests           int64  `json:"cost_known_requests"`
	ExpectedPlatformCostNanoUSD int64  `json:"expected_platform_cost_nano_usd"`
	TTFTSampleCount             int64  `json:"ttft_sample_count"`
	TTFTSketchCodecVersion      int    `json:"ttft_sketch_codec_version"`
	TTFTSketch                  []byte `json:"ttft_sketch,omitempty"`
}

type CanaryCohortWindowCheckpoint struct {
	SchemaVersion      int                     `json:"schema_version"`
	PoolID             int                     `json:"pool_id"`
	ActivationID       int64                   `json:"activation_id"`
	PolicyRevision     uint64                  `json:"policy_revision"`
	TrafficBasisPoints int                     `json:"traffic_basis_points"`
	RolloutKey         RolloutKey              `json:"rollout_key"`
	WindowSeconds      int                     `json:"window_seconds"`
	WindowStartUnixMs  int64                   `json:"window_start_unix_ms"`
	WindowEndUnixMs    int64                   `json:"window_end_unix_ms"`
	Control            CanaryCohortWindowStats `json:"control"`
	Canary             CanaryCohortWindowStats `json:"canary"`
}

type CanaryWindowStats struct {
	Entries       int   `json:"entries"`
	Recorded      int64 `json:"recorded"`
	Ignored       int64 `json:"ignored"`
	ClosedDrops   int64 `json:"closed_drops"`
	OverflowDrops int64 `json:"overflow_drops"`
	EntryDrops    int64 `json:"entry_drops"`
	ClampedTTFT   int64 `json:"clamped_ttft"`
	Flushed       int64 `json:"flushed"`
	FlushFailures int64 `json:"flush_failures"`
}

type CanaryWindowAggregatorConfig struct {
	MaxEntries int
	Shards     int
	TTL        time.Duration
	Clock      Clock
}

type CanaryWindowAggregator struct {
	config CanaryWindowAggregatorConfig
	shards []canaryWindowShard

	admissionMu   sync.Mutex
	flushMu       sync.Mutex
	entries       atomic.Int64
	recorded      atomic.Int64
	ignored       atomic.Int64
	closedDrops   atomic.Int64
	overflowDrops atomic.Int64
	entryDrops    atomic.Int64
	clampedTTFT   atomic.Int64
	flushed       atomic.Int64
	flushFailures atomic.Int64
}

type canaryWindowShard struct {
	mu      sync.Mutex
	entries map[canaryWindowKey]*canaryWindowEntry
}

type canaryWindowKey struct {
	PoolID         int
	ActivationID   int64
	PolicyRevision uint64
	RolloutKey     RolloutKey
	WindowStartMs  int64
}

type canaryWindowEntry struct {
	identity      CanaryWindowIdentity
	windowStartMs int64
	windowEndMs   int64
	control       canaryCohortAccumulator
	canary        canaryCohortAccumulator
	frozen        *CanaryCohortWindowCheckpoint
	updatedAt     time.Time
}

type canaryCohortAccumulator struct {
	logicalRequests             int64
	successes                   int64
	failures                    int64
	routingFailures             int64
	attempts                    int64
	costKnownRequests           int64
	expectedPlatformCostNanoUSD int64
	ttft                        *routingdistribution.DurationSketch
}

var defaultCanaryWindowAggregator = func() *CanaryWindowAggregator {
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: defaultCanaryWindowMaxEntries,
		Shards:     defaultCanaryWindowShards,
		TTL:        defaultCanaryWindowTTL,
	})
	if err != nil {
		panic(err)
	}
	return aggregator
}()

func NewCanaryWindowAggregator(config CanaryWindowAggregatorConfig) (*CanaryWindowAggregator, error) {
	if config.MaxEntries <= 0 || config.Shards <= 0 || config.Shards > capacityMaxShards || config.TTL <= 0 {
		return nil, ErrCanaryWindowInvalid
	}
	if config.Clock == nil {
		config.Clock = wallClock{}
	}
	aggregator := &CanaryWindowAggregator{config: config, shards: make([]canaryWindowShard, config.Shards)}
	for index := range aggregator.shards {
		aggregator.shards[index].entries = make(map[canaryWindowKey]*canaryWindowEntry)
	}
	return aggregator, nil
}

func RecordCanaryLogicalOutcome(outcome CanaryLogicalOutcome) error {
	return defaultCanaryWindowAggregator.Record(outcome)
}

func CanaryCostNanoUSD(costUSD float64) (int64, bool) {
	if math.IsNaN(costUSD) || math.IsInf(costUSD, 0) || costUSD < 0 ||
		costUSD > float64(math.MaxInt64)/1_000_000_000 {
		return 0, false
	}
	value := math.Round(costUSD * 1_000_000_000)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

func (aggregator *CanaryWindowAggregator) Record(outcome CanaryLogicalOutcome) error {
	if aggregator == nil || !validCanaryLogicalOutcome(outcome) {
		return ErrCanaryWindowInvalid
	}
	windowLifetime := time.Duration(outcome.Identity.WindowSeconds+outcome.Identity.LatenessSeconds) * time.Second
	if windowLifetime <= 0 || windowLifetime > aggregator.config.TTL {
		return ErrCanaryWindowInvalid
	}
	windowMs := int64(outcome.Identity.WindowSeconds) * int64(time.Second/time.Millisecond)
	completedMs := outcome.CompletedAt.UnixMilli()
	if completedMs <= 0 || windowMs <= 0 {
		return ErrCanaryWindowInvalid
	}
	windowStartMs := completedMs / windowMs * windowMs
	if windowStartMs > math.MaxInt64-windowMs {
		return ErrCanaryWindowInvalid
	}
	key := canaryWindowKey{
		PoolID: outcome.Identity.PoolID, ActivationID: outcome.Identity.ActivationID,
		PolicyRevision: outcome.Identity.PolicyRevision, RolloutKey: outcome.Identity.RolloutKey,
		WindowStartMs: windowStartMs,
	}
	now := aggregator.config.Clock.Now()
	shard := aggregator.shardFor(key)
	shard.mu.Lock()
	entry := shard.entries[key]
	if entry != nil {
		err := aggregator.recordLocked(entry, outcome, now)
		shard.mu.Unlock()
		return err
	}
	shard.mu.Unlock()

	aggregator.admissionMu.Lock()
	defer aggregator.admissionMu.Unlock()
	aggregator.pruneExpiredLocked(now)
	shard = aggregator.shardFor(key)
	shard.mu.Lock()
	entry = shard.entries[key]
	if entry != nil {
		err := aggregator.recordLocked(entry, outcome, now)
		shard.mu.Unlock()
		return err
	}
	shard.mu.Unlock()
	if aggregator.entries.Load() >= int64(aggregator.config.MaxEntries) && !aggregator.evictOldestLocked() {
		aggregator.entryDrops.Add(1)
		return ErrCanaryWindowEntriesFull
	}
	entry = &canaryWindowEntry{
		identity: outcome.Identity, windowStartMs: windowStartMs, windowEndMs: windowStartMs + windowMs,
		control:   canaryCohortAccumulator{ttft: routingdistribution.NewDurationSketch()},
		canary:    canaryCohortAccumulator{ttft: routingdistribution.NewDurationSketch()},
		updatedAt: now,
	}
	shard.mu.Lock()
	if existing := shard.entries[key]; existing != nil {
		entry = existing
	} else {
		shard.entries[key] = entry
		aggregator.entries.Add(1)
	}
	err := aggregator.recordLocked(entry, outcome, now)
	shard.mu.Unlock()
	return err
}

func (aggregator *CanaryWindowAggregator) recordLocked(
	entry *canaryWindowEntry,
	outcome CanaryLogicalOutcome,
	now time.Time,
) error {
	if entry == nil || entry.identity != outcome.Identity || entry.frozen != nil ||
		!now.Before(time.UnixMilli(entry.windowEndMs).Add(time.Duration(entry.identity.LatenessSeconds)*time.Second)) {
		aggregator.closedDrops.Add(1)
		return ErrCanaryWindowClosed
	}
	cohort := &entry.control
	if outcome.Cohort == model.RoutingDecisionCohortCanary {
		cohort = &entry.canary
	}
	if err := addCanaryOutcome(cohort, outcome, aggregator); err != nil {
		aggregator.overflowDrops.Add(1)
		return err
	}
	entry.updatedAt = now
	aggregator.recorded.Add(1)
	return nil
}

func addCanaryOutcome(
	cohort *canaryCohortAccumulator,
	outcome CanaryLogicalOutcome,
	aggregator *CanaryWindowAggregator,
) error {
	if cohort == nil || cohort.ttft == nil {
		return ErrCanaryWindowInvalid
	}
	values := []*int64{&cohort.logicalRequests, &cohort.attempts}
	deltas := []int64{1, outcome.Attempts}
	if outcome.Success {
		values = append(values, &cohort.successes)
		deltas = append(deltas, 1)
	} else {
		values = append(values, &cohort.failures)
		deltas = append(deltas, 1)
	}
	if outcome.RoutingFailure {
		values = append(values, &cohort.routingFailures)
		deltas = append(deltas, 1)
	}
	if outcome.CostKnown {
		values = append(values, &cohort.costKnownRequests, &cohort.expectedPlatformCostNanoUSD)
		deltas = append(deltas, 1, outcome.ExpectedPlatformCostNanoUSD)
	}
	for index := range values {
		if deltas[index] < 0 || *values[index] > math.MaxInt64-deltas[index] {
			return ErrCanaryWindowOverflow
		}
	}
	for index := range values {
		*values[index] += deltas[index]
	}
	if outcome.Success && outcome.ClientTTFTMilliseconds > 0 {
		result, err := cohort.ttft.AddMillis(outcome.ClientTTFTMilliseconds)
		if err != nil {
			for index := range values {
				*values[index] -= deltas[index]
			}
			return err
		}
		if result.Clamped {
			aggregator.clampedTTFT.Add(1)
		}
	}
	return nil
}

func FlushCanaryOutcomeCheckpointsContext(ctx context.Context) (int, error) {
	return defaultCanaryWindowAggregator.FlushContext(ctx, defaultCanaryWindowFlushLimit)
}

func (aggregator *CanaryWindowAggregator) FlushContext(ctx context.Context, limit int) (int, error) {
	if aggregator == nil || limit <= 0 || limit > model.RoutingRuntimeCheckpointMaxPageSize {
		return 0, ErrCanaryWindowInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	aggregator.flushMu.Lock()
	defer aggregator.flushMu.Unlock()
	now := aggregator.config.Clock.Now()
	type frozenWindow struct {
		key        canaryWindowKey
		checkpoint CanaryCohortWindowCheckpoint
	}
	windows := make([]frozenWindow, 0, limit)
	for shardIndex := range aggregator.shards {
		shard := &aggregator.shards[shardIndex]
		shard.mu.Lock()
		for key, entry := range shard.entries {
			if len(windows) >= limit {
				break
			}
			freezeAt := time.UnixMilli(entry.windowEndMs).Add(time.Duration(entry.identity.LatenessSeconds) * time.Second)
			if now.Before(freezeAt) {
				continue
			}
			if entry.frozen == nil {
				checkpoint, err := freezeCanaryWindow(entry)
				if err != nil {
					shard.mu.Unlock()
					aggregator.flushFailures.Add(1)
					return 0, err
				}
				entry.frozen = &checkpoint
			}
			windows = append(windows, frozenWindow{key: key, checkpoint: *entry.frozen})
		}
		shard.mu.Unlock()
		if len(windows) >= limit {
			break
		}
	}

	flushed := 0
	for index := range windows {
		window := windows[index]
		scope := CanaryWindowCheckpointScope(window.checkpoint)
		checkpoint, err := model.NewRoutingRuntimeCheckpoint(
			NodeEpochID(), CanaryCohortWindowCheckpointKind, scope,
			int64(window.checkpoint.PolicyRevision), 1, window.checkpoint,
			window.checkpoint.WindowEndUnixMs/1_000,
			now.Add(aggregator.config.TTL).Unix(),
		)
		if err == nil {
			_, err = model.UpsertRoutingRuntimeCheckpointContext(ctx, checkpoint)
		}
		if err != nil {
			aggregator.flushFailures.Add(1)
			return flushed, err
		}
		shard := aggregator.shardFor(window.key)
		shard.mu.Lock()
		if entry := shard.entries[window.key]; entry != nil && entry.frozen != nil {
			delete(shard.entries, window.key)
			aggregator.entries.Add(-1)
		}
		shard.mu.Unlock()
		flushed++
		aggregator.flushed.Add(1)
	}
	return flushed, nil
}

func freezeCanaryWindow(entry *canaryWindowEntry) (CanaryCohortWindowCheckpoint, error) {
	if entry == nil || entry.frozen != nil || entry.windowStartMs <= 0 || entry.windowEndMs <= entry.windowStartMs {
		return CanaryCohortWindowCheckpoint{}, ErrCanaryWindowInvalid
	}
	control, err := freezeCanaryCohort(entry.control)
	if err != nil {
		return CanaryCohortWindowCheckpoint{}, err
	}
	canary, err := freezeCanaryCohort(entry.canary)
	if err != nil {
		return CanaryCohortWindowCheckpoint{}, err
	}
	return CanaryCohortWindowCheckpoint{
		SchemaVersion: canaryCohortWindowSchemaVersion,
		PoolID:        entry.identity.PoolID, ActivationID: entry.identity.ActivationID,
		PolicyRevision: entry.identity.PolicyRevision, TrafficBasisPoints: entry.identity.TrafficBasisPoints,
		RolloutKey: entry.identity.RolloutKey, WindowSeconds: entry.identity.WindowSeconds,
		WindowStartUnixMs: entry.windowStartMs, WindowEndUnixMs: entry.windowEndMs,
		Control: control, Canary: canary,
	}, nil
}

func freezeCanaryCohort(cohort canaryCohortAccumulator) (CanaryCohortWindowStats, error) {
	stats := CanaryCohortWindowStats{
		LogicalRequests: cohort.logicalRequests, Successes: cohort.successes, Failures: cohort.failures,
		RoutingFailures: cohort.routingFailures, Attempts: cohort.attempts,
		CostKnownRequests:           cohort.costKnownRequests,
		ExpectedPlatformCostNanoUSD: cohort.expectedPlatformCostNanoUSD,
	}
	if cohort.ttft != nil && cohort.ttft.Count() > 0 {
		encoded, err := cohort.ttft.MarshalBinary()
		if err != nil {
			return CanaryCohortWindowStats{}, err
		}
		stats.TTFTSampleCount = cohort.ttft.Count()
		stats.TTFTSketchCodecVersion = routingdistribution.SketchCodecVersion
		stats.TTFTSketch = encoded
	}
	return stats, nil
}

func DecodeCanaryCohortWindowCheckpoint(
	checkpoint model.RoutingRuntimeCheckpoint,
) (CanaryCohortWindowCheckpoint, error) {
	if checkpoint.CheckpointKind != CanaryCohortWindowCheckpointKind || checkpoint.PolicyRevision <= 0 {
		return CanaryCohortWindowCheckpoint{}, ErrCanaryWindowInvalid
	}
	var payload CanaryCohortWindowCheckpoint
	if err := checkpoint.DecodePayload(&payload); err != nil || !validCanaryWindowCheckpoint(payload) ||
		checkpoint.PolicyRevision != int64(payload.PolicyRevision) || checkpoint.Scope != CanaryWindowCheckpointScope(payload) {
		return CanaryCohortWindowCheckpoint{}, ErrCanaryWindowInvalid
	}
	return payload, nil
}

func CanaryWindowCheckpointScope(checkpoint CanaryCohortWindowCheckpoint) string {
	return fmt.Sprintf(
		"v1/pool/%d/activation/%d/rollout/%s/window/%d",
		checkpoint.PoolID, checkpoint.ActivationID, checkpoint.RolloutKey, checkpoint.WindowEndUnixMs,
	)
}

func validCanaryWindowCheckpoint(checkpoint CanaryCohortWindowCheckpoint) bool {
	if checkpoint.SchemaVersion != canaryCohortWindowSchemaVersion || checkpoint.PoolID <= 0 ||
		checkpoint.ActivationID <= 0 || checkpoint.PolicyRevision == 0 ||
		checkpoint.TrafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		checkpoint.TrafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints ||
		!validShadowHash(string(checkpoint.RolloutKey)) || checkpoint.WindowSeconds < 1 ||
		checkpoint.WindowStartUnixMs <= 0 || checkpoint.WindowEndUnixMs <= checkpoint.WindowStartUnixMs ||
		checkpoint.WindowEndUnixMs-checkpoint.WindowStartUnixMs != int64(checkpoint.WindowSeconds)*1_000 ||
		len(CanaryWindowCheckpointScope(checkpoint)) > canaryWindowScopeMaxBytes {
		return false
	}
	return validFrozenCanaryCohort(checkpoint.Control) && validFrozenCanaryCohort(checkpoint.Canary)
}

func validFrozenCanaryCohort(stats CanaryCohortWindowStats) bool {
	values := []int64{
		stats.LogicalRequests, stats.Successes, stats.Failures, stats.RoutingFailures, stats.Attempts,
		stats.CostKnownRequests, stats.ExpectedPlatformCostNanoUSD, stats.TTFTSampleCount,
	}
	for _, value := range values {
		if value < 0 {
			return false
		}
	}
	if stats.Successes > stats.LogicalRequests || stats.Failures > stats.LogicalRequests ||
		stats.Successes > math.MaxInt64-stats.Failures || stats.Successes+stats.Failures != stats.LogicalRequests ||
		stats.RoutingFailures > stats.Failures ||
		stats.CostKnownRequests > stats.LogicalRequests || stats.Attempts < stats.Successes ||
		stats.TTFTSampleCount > stats.Successes {
		return false
	}
	if stats.TTFTSampleCount == 0 {
		return stats.TTFTSketchCodecVersion == 0 && len(stats.TTFTSketch) == 0
	}
	if stats.TTFTSketchCodecVersion != routingdistribution.SketchCodecVersion || len(stats.TTFTSketch) == 0 {
		return false
	}
	sketch, err := routingdistribution.DecodeDurationSketch(stats.TTFTSketch, stats.TTFTSketchCodecVersion)
	return err == nil && sketch.Count() == stats.TTFTSampleCount
}

func validCanaryLogicalOutcome(outcome CanaryLogicalOutcome) bool {
	identity := outcome.Identity
	if identity.PoolID <= 0 || identity.ActivationID <= 0 || identity.PolicyRevision == 0 ||
		identity.TrafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		identity.TrafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints ||
		!validShadowHash(string(identity.RolloutKey)) || identity.WindowSeconds < 1 || identity.WindowSeconds > 3_600 ||
		identity.LatenessSeconds < 0 || identity.LatenessSeconds > 3_600 || outcome.CompletedAt.IsZero() ||
		(outcome.Cohort != model.RoutingDecisionCohortControl && outcome.Cohort != model.RoutingDecisionCohortCanary) ||
		outcome.Attempts < 0 || (!outcome.RoutingFailure && outcome.Attempts == 0) ||
		outcome.ExpectedPlatformCostNanoUSD < 0 || (!outcome.CostKnown && outcome.ExpectedPlatformCostNanoUSD != 0) ||
		outcome.ClientTTFTMilliseconds < 0 || (outcome.Success && outcome.RoutingFailure) {
		return false
	}
	return true
}

func (aggregator *CanaryWindowAggregator) Stats() CanaryWindowStats {
	if aggregator == nil {
		return CanaryWindowStats{}
	}
	aggregator.admissionMu.Lock()
	aggregator.pruneExpiredLocked(aggregator.config.Clock.Now())
	aggregator.admissionMu.Unlock()
	return CanaryWindowStats{
		Entries: int(aggregator.entries.Load()), Recorded: aggregator.recorded.Load(), Ignored: aggregator.ignored.Load(),
		ClosedDrops: aggregator.closedDrops.Load(), OverflowDrops: aggregator.overflowDrops.Load(),
		EntryDrops: aggregator.entryDrops.Load(), ClampedTTFT: aggregator.clampedTTFT.Load(),
		Flushed: aggregator.flushed.Load(), FlushFailures: aggregator.flushFailures.Load(),
	}
}

func CurrentCanaryWindowStats() CanaryWindowStats {
	return defaultCanaryWindowAggregator.Stats()
}

func (aggregator *CanaryWindowAggregator) shardFor(key canaryWindowKey) *canaryWindowShard {
	hash := fnv.New64a()
	_, _ = fmt.Fprintf(hash, "%d\x00%d\x00%d\x00%s\x00%d", key.PoolID, key.ActivationID, key.PolicyRevision, key.RolloutKey, key.WindowStartMs)
	return &aggregator.shards[hash.Sum64()%uint64(len(aggregator.shards))]
}

func (aggregator *CanaryWindowAggregator) pruneExpiredLocked(now time.Time) {
	for shardIndex := range aggregator.shards {
		shard := &aggregator.shards[shardIndex]
		shard.mu.Lock()
		for key, entry := range shard.entries {
			freezeAt := time.UnixMilli(entry.windowEndMs).Add(time.Duration(entry.identity.LatenessSeconds) * time.Second)
			if entry.frozen != nil || !now.Before(freezeAt) {
				continue
			}
			if now.Sub(entry.updatedAt) > aggregator.config.TTL {
				delete(shard.entries, key)
				aggregator.entries.Add(-1)
				aggregator.ignored.Add(1)
			}
		}
		shard.mu.Unlock()
	}
}

func (aggregator *CanaryWindowAggregator) evictOldestLocked() bool {
	var victim canaryWindowKey
	var victimShard *canaryWindowShard
	var victimUpdated time.Time
	now := aggregator.config.Clock.Now()
	found := false
	for shardIndex := range aggregator.shards {
		shard := &aggregator.shards[shardIndex]
		shard.mu.Lock()
		for key, entry := range shard.entries {
			freezeAt := time.UnixMilli(entry.windowEndMs).Add(time.Duration(entry.identity.LatenessSeconds) * time.Second)
			if entry.frozen != nil || !now.Before(freezeAt) {
				continue
			}
			if !found || entry.updatedAt.Before(victimUpdated) ||
				(entry.updatedAt.Equal(victimUpdated) && lessCanaryWindowKey(key, victim)) {
				victim = key
				victimShard = shard
				victimUpdated = entry.updatedAt
				found = true
			}
		}
		shard.mu.Unlock()
	}
	if !found || victimShard == nil {
		return false
	}
	victimShard.mu.Lock()
	entry, exists := victimShard.entries[victim]
	if exists {
		freezeAt := time.UnixMilli(entry.windowEndMs).Add(time.Duration(entry.identity.LatenessSeconds) * time.Second)
		if entry.frozen != nil || !now.Before(freezeAt) || !entry.updatedAt.Equal(victimUpdated) {
			victimShard.mu.Unlock()
			return false
		}
		delete(victimShard.entries, victim)
		aggregator.entries.Add(-1)
		aggregator.entryDrops.Add(1)
	}
	victimShard.mu.Unlock()
	return true
}

func lessCanaryWindowKey(left canaryWindowKey, right canaryWindowKey) bool {
	if left.PoolID != right.PoolID {
		return left.PoolID < right.PoolID
	}
	if left.ActivationID != right.ActivationID {
		return left.ActivationID < right.ActivationID
	}
	if left.PolicyRevision != right.PolicyRevision {
		return left.PolicyRevision < right.PolicyRevision
	}
	if left.RolloutKey != right.RolloutKey {
		return strings.Compare(string(left.RolloutKey), string(right.RolloutKey)) < 0
	}
	return left.WindowStartMs < right.WindowStartMs
}

func ResetCanaryWindowAggregatorForTest(aggregator ...*CanaryWindowAggregator) {
	if len(aggregator) > 0 && aggregator[0] != nil {
		defaultCanaryWindowAggregator = aggregator[0]
		return
	}
	replacement, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: defaultCanaryWindowMaxEntries,
		Shards:     defaultCanaryWindowShards,
		TTL:        defaultCanaryWindowTTL,
	})
	if err != nil {
		panic(err)
	}
	defaultCanaryWindowAggregator = replacement
}
