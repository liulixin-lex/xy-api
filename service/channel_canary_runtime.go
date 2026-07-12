package service

import (
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
)

const (
	channelRoutingCanaryMaxEntries           = 100_000
	channelRoutingCanaryShards               = 64
	channelRoutingCanaryMaxSlowStartRuntimes = 4_096
	channelRoutingCanaryCapacityIdleTTL      = 15 * time.Minute
)

var errChannelRoutingCanaryRuntimeFull = errors.New("channel routing canary runtime capacity reached")

type channelRoutingCanaryRuntimeKey struct {
	PolicyRevision uint64
	PoolID         int
}

type channelRoutingCanarySlowStartRuntime struct {
	policy   model.RoutingCanarySlowStartPolicy
	tracker  *channelrouting.SlowStartTracker
	seen     map[channelrouting.SlowStartKey]struct{}
	lastUsed time.Time
}

type channelRoutingCanaryRuntimeManager struct {
	capacity *channelrouting.CapacityTracker
	clock    channelrouting.Clock

	slowStartMu      sync.Mutex
	slowStarts       map[channelRoutingCanaryRuntimeKey]*channelRoutingCanarySlowStartRuntime
	slowStartEntries int
}

var channelRoutingCanaryRuntime = mustNewChannelRoutingCanaryRuntimeManager(nil)

func mustNewChannelRoutingCanaryRuntimeManager(clock channelrouting.Clock) *channelRoutingCanaryRuntimeManager {
	manager, err := newChannelRoutingCanaryRuntimeManager(clock)
	if err != nil {
		panic(err)
	}
	return manager
}

func newChannelRoutingCanaryRuntimeManager(clock channelrouting.Clock) (*channelRoutingCanaryRuntimeManager, error) {
	capacity, err := channelrouting.NewCapacityTracker(channelrouting.CapacityConfig{
		MaxEntries: channelRoutingCanaryMaxEntries,
		IdleTTL:    channelRoutingCanaryCapacityIdleTTL,
		Shards:     channelRoutingCanaryShards,
		Clock:      clock,
	})
	if err != nil {
		return nil, err
	}
	return &channelRoutingCanaryRuntimeManager{
		capacity:   capacity,
		clock:      clock,
		slowStarts: make(map[channelRoutingCanaryRuntimeKey]*channelRoutingCanarySlowStartRuntime),
	}, nil
}

func (manager *channelRoutingCanaryRuntimeManager) tryReserve(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	key channelrouting.CapacityKey,
	demand channelrouting.Demand,
) (*channelrouting.Reservation, error) {
	if manager == nil || manager.capacity == nil || policyRevision == 0 {
		return nil, channelrouting.ErrCapacityInvalidInput
	}
	normalized, err := model.NormalizeRoutingCanaryPolicy(policy)
	if err != nil {
		return nil, channelrouting.ErrCapacityInvalidInput
	}
	key.PolicyRevision = policyRevision
	return manager.capacity.TryReserve(key, demand, channelrouting.Limit{
		RPM:       normalized.Capacity.RPM,
		InputTPM:  normalized.Capacity.InputTPM,
		OutputTPM: normalized.Capacity.OutputTPM,
		Inflight:  normalized.Capacity.Inflight,
	})
}

func (manager *channelRoutingCanaryRuntimeManager) slowStartFactor(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	key channelrouting.SlowStartKey,
) (float64, error) {
	if manager == nil || policyRevision == 0 || key.PoolID <= 0 {
		return 0, channelrouting.ErrSlowStartInvalidKey
	}
	normalized, err := model.NormalizeRoutingCanaryPolicy(policy)
	if err != nil {
		return 0, channelrouting.ErrSlowStartInvalidPolicy
	}
	now := manager.now()
	manager.slowStartMu.Lock()
	defer manager.slowStartMu.Unlock()
	manager.pruneSlowStartsLocked(now)

	runtimeKey := channelRoutingCanaryRuntimeKey{PolicyRevision: policyRevision, PoolID: key.PoolID}
	runtime := manager.slowStarts[runtimeKey]
	if runtime == nil {
		if len(manager.slowStarts) >= channelRoutingCanaryMaxSlowStartRuntimes {
			manager.evictOldestSlowStartLocked(runtimeKey)
		}
		tracker, trackerErr := channelrouting.NewSlowStartTracker(channelrouting.SlowStartPolicy{
			MinimumFactor: normalized.SlowStart.MinimumFactor,
			RampDuration:  time.Duration(normalized.SlowStart.RampSeconds) * time.Second,
			StateTTL:      time.Duration(normalized.SlowStart.StateTTLSeconds) * time.Second,
			MaxEntries:    channelRoutingCanaryMaxEntries,
		}, manager.clock)
		if trackerErr != nil {
			return 0, trackerErr
		}
		runtime = &channelRoutingCanarySlowStartRuntime{
			policy:   normalized.SlowStart,
			tracker:  tracker,
			seen:     make(map[channelrouting.SlowStartKey]struct{}),
			lastUsed: now,
		}
		manager.slowStarts[runtimeKey] = runtime
	} else if runtime.policy != normalized.SlowStart {
		return 0, channelrouting.ErrSlowStartInvalidPolicy
	}

	runtime.lastUsed = now
	if _, exists := runtime.seen[key]; !exists {
		if manager.slowStartEntries >= channelRoutingCanaryMaxEntries {
			manager.evictOldestSlowStartLocked(runtimeKey)
		}
		if manager.slowStartEntries >= channelRoutingCanaryMaxEntries {
			return 0, errChannelRoutingCanaryRuntimeFull
		}
		runtime.seen[key] = struct{}{}
		manager.slowStartEntries++
		state, startErr := runtime.tracker.StartNew(key)
		return state.Factor, startErr
	}
	return runtime.tracker.Factor(key)
}

func (manager *channelRoutingCanaryRuntimeManager) now() time.Time {
	if manager.clock != nil {
		return manager.clock.Now()
	}
	return time.Now()
}

func (manager *channelRoutingCanaryRuntimeManager) pruneSlowStartsLocked(now time.Time) {
	for key, runtime := range manager.slowStarts {
		idleTTL := time.Duration(runtime.policy.StateTTLSeconds) * time.Second
		if idleTTL <= 0 || now.Before(runtime.lastUsed.Add(idleTTL)) {
			continue
		}
		delete(manager.slowStarts, key)
		manager.slowStartEntries -= len(runtime.seen)
	}
}

func (manager *channelRoutingCanaryRuntimeManager) evictOldestSlowStartLocked(excluded channelRoutingCanaryRuntimeKey) {
	var victim channelRoutingCanaryRuntimeKey
	var victimRuntime *channelRoutingCanarySlowStartRuntime
	for key, runtime := range manager.slowStarts {
		if key == excluded {
			continue
		}
		if victimRuntime == nil || runtime.lastUsed.Before(victimRuntime.lastUsed) ||
			(runtime.lastUsed.Equal(victimRuntime.lastUsed) && lessChannelRoutingCanaryRuntimeKey(key, victim)) {
			victim = key
			victimRuntime = runtime
		}
	}
	if victimRuntime == nil {
		return
	}
	delete(manager.slowStarts, victim)
	manager.slowStartEntries -= len(victimRuntime.seen)
}

func lessChannelRoutingCanaryRuntimeKey(left channelRoutingCanaryRuntimeKey, right channelRoutingCanaryRuntimeKey) bool {
	if left.PolicyRevision != right.PolicyRevision {
		return left.PolicyRevision < right.PolicyRevision
	}
	return left.PoolID < right.PoolID
}
