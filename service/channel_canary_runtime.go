package service

import (
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const (
	channelRoutingCanaryMaxEntries           = 100_000
	channelRoutingCanaryShards               = 64
	channelRoutingCanaryMaxSlowStartRuntimes = 4_096
	channelRoutingCanaryCapacityIdleTTL      = 15 * time.Minute
	channelRoutingAdaptiveIdleTTL            = 30 * time.Minute
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

type channelRoutingSlowStartProbe struct {
	ChannelID      int
	PolicyRevision uint64
	Policy         model.RoutingCanaryPolicy
	Key            channelrouting.SlowStartKey
}

type channelRoutingCanaryRuntimeManager struct {
	capacity *channelrouting.CapacityTracker
	adaptive *channelrouting.AdaptiveConcurrencyController
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
	adaptive, err := channelrouting.NewAdaptiveConcurrencyController(channelrouting.AdaptiveConcurrencyConfig{
		MaxEntries: channelRoutingCanaryMaxEntries,
		IdleTTL:    channelRoutingAdaptiveIdleTTL,
		Shards:     channelRoutingCanaryShards,
		Clock:      clock,
	})
	if err != nil {
		return nil, err
	}
	return &channelRoutingCanaryRuntimeManager{
		capacity:   capacity,
		adaptive:   adaptive,
		clock:      clock,
		slowStarts: make(map[channelRoutingCanaryRuntimeKey]*channelRoutingCanarySlowStartRuntime),
	}, nil
}

func (manager *channelRoutingCanaryRuntimeManager) tryAcquireAdaptiveConcurrency(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	identity channelrouting.Identity,
	channelID int,
	modelName string,
	upstreamModelName string,
	strictRequest *channelrouting.StrictCapacityRequest,
) (*channelrouting.AdaptiveConcurrencyLeaseSet, error) {
	if manager == nil || manager.adaptive == nil || policyRevision == 0 ||
		identity.SnapshotRevision != policyRevision || identity.PoolID <= 0 || identity.MemberID <= 0 ||
		channelID <= 0 || modelName == "" {
		return nil, channelrouting.ErrAdaptiveConcurrencyInvalid
	}
	normalized, err := model.NormalizeRoutingCanaryPolicy(policy)
	if err != nil || normalized.Capacity.Inflight <= 0 {
		return nil, channelrouting.ErrAdaptiveConcurrencyInvalid
	}
	targets := []channelrouting.AdaptiveConcurrencyTarget{{
		Key: channelrouting.AdaptiveConcurrencyKey{
			PolicyRevision: policyRevision,
			Scope:          channelrouting.AdaptiveConcurrencyScopeMemberModel,
			PoolID:         identity.PoolID,
			MemberID:       identity.MemberID,
			Model:          modelName,
		},
		Policy: channelrouting.DefaultAdaptiveConcurrencyPolicy(normalized.Capacity.Inflight),
	}}
	sharedMaximum := normalized.Capacity.Inflight
	sharedKey := channelrouting.AdaptiveConcurrencyKey{
		PolicyRevision: policyRevision,
		Scope:          channelrouting.AdaptiveConcurrencyScopeSharedResource,
		ChannelID:      channelID,
		CredentialID:   identity.CredentialID,
		Model:          upstreamModelName,
	}
	if strictRequest != nil {
		sharedMaximum = strictRequest.Limit.Inflight
		sharedKey.ChannelID = 0
		sharedKey.AccountID = strictRequest.Key.AccountID
		sharedKey.CredentialID = strictRequest.Key.CredentialID
		sharedKey.Model = strictRequest.Key.Model
	}
	if sharedKey.Model == "" {
		sharedKey.Model = modelName
	}
	if sharedMaximum <= 0 {
		return nil, channelrouting.ErrAdaptiveConcurrencyInvalid
	}
	targets = append(targets, channelrouting.AdaptiveConcurrencyTarget{
		Key: sharedKey, Policy: channelrouting.DefaultAdaptiveConcurrencyPolicy(sharedMaximum),
	})
	return manager.adaptive.TryAcquire(targets)
}

func (manager *channelRoutingCanaryRuntimeManager) observeAdaptiveConcurrency(
	targets []channelrouting.AdaptiveConcurrencyTarget,
	signal channelrouting.AdaptiveConcurrencySignal,
) {
	if manager == nil || manager.adaptive == nil || len(targets) == 0 {
		return
	}
	manager.adaptive.Observe(targets, signal)
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
	runtimeKey, runtime, err := manager.slowStartRuntimeLocked(policyRevision, normalized, key.PoolID, now)
	if err != nil {
		return 0, err
	}
	newKey, err := manager.registerSlowStartKeyLocked(runtimeKey, runtime, key)
	if err != nil {
		return 0, err
	}
	if newKey {
		state, startErr := runtime.tracker.StartNew(key)
		return state.Factor, startErr
	}
	return runtime.tracker.Factor(key)
}

func (manager *channelRoutingCanaryRuntimeManager) startRecovery(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	key channelrouting.SlowStartKey,
) error {
	if manager == nil || policyRevision == 0 || key.PoolID <= 0 {
		return channelrouting.ErrSlowStartInvalidKey
	}
	normalized, err := model.NormalizeRoutingCanaryPolicy(policy)
	if err != nil {
		return channelrouting.ErrSlowStartInvalidPolicy
	}
	now := manager.now()
	manager.slowStartMu.Lock()
	defer manager.slowStartMu.Unlock()
	manager.pruneSlowStartsLocked(now)
	runtimeKey, runtime, err := manager.slowStartRuntimeLocked(policyRevision, normalized, key.PoolID, now)
	if err != nil {
		return err
	}
	if _, err := manager.registerSlowStartKeyLocked(runtimeKey, runtime, key); err != nil {
		return err
	}
	_, err = runtime.tracker.StartRecovery(key)
	return err
}

func (manager *channelRoutingCanaryRuntimeManager) observeSlowStartProbe(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	key channelrouting.SlowStartKey,
	healthy bool,
	hardFailure bool,
) error {
	if manager == nil || policyRevision == 0 || key.PoolID <= 0 || (!healthy && !hardFailure) {
		if !healthy && !hardFailure {
			return nil
		}
		return channelrouting.ErrSlowStartInvalidKey
	}
	normalized, err := model.NormalizeRoutingCanaryPolicy(policy)
	if err != nil {
		return channelrouting.ErrSlowStartInvalidPolicy
	}
	now := manager.now()
	manager.slowStartMu.Lock()
	defer manager.slowStartMu.Unlock()
	manager.pruneSlowStartsLocked(now)
	runtimeKey, runtime, err := manager.slowStartRuntimeLocked(policyRevision, normalized, key.PoolID, now)
	if err != nil {
		return err
	}
	if _, err := manager.registerSlowStartKeyLocked(runtimeKey, runtime, key); err != nil {
		return err
	}
	if hardFailure {
		_, err = runtime.tracker.MarkHardFailure(key)
		return err
	}
	if _, exists := runtime.tracker.State(key); !exists {
		_, err = runtime.tracker.StartRecovery(key)
		return err
	}
	_, err = runtime.tracker.MarkHealthy(key)
	return err
}

func prepareRoutingSlowStartProbe(
	c *gin.Context,
	channelID int,
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	key channelrouting.SlowStartKey,
) error {
	if c == nil || channelID <= 0 || policyRevision == 0 || key.PoolID <= 0 || key.MemberID <= 0 || key.Model == "" {
		return channelrouting.ErrSlowStartInvalidKey
	}
	if _, err := model.NormalizeRoutingCanaryPolicy(policy); err != nil {
		return channelrouting.ErrSlowStartInvalidPolicy
	}
	common.SetContextKey(c, constant.ContextKeyRoutingSlowStartProbe, channelRoutingSlowStartProbe{
		ChannelID: channelID, PolicyRevision: policyRevision, Policy: policy, Key: key,
	})
	return nil
}

func ObserveRoutingSlowStartProbe(c *gin.Context, healthy bool, hardFailure bool) error {
	if c == nil {
		return nil
	}
	probe, ok := common.GetContextKeyType[channelRoutingSlowStartProbe](c, constant.ContextKeyRoutingSlowStartProbe)
	if !ok {
		return nil
	}
	common.SetContextKey(c, constant.ContextKeyRoutingSlowStartProbe, nil)
	return channelRoutingCanaryRuntime.observeSlowStartProbe(
		probe.PolicyRevision, probe.Policy, probe.Key, healthy, hardFailure,
	)
}

func clearRoutingSlowStartProbe(c *gin.Context, channelID int) {
	if c == nil {
		return
	}
	probe, ok := common.GetContextKeyType[channelRoutingSlowStartProbe](c, constant.ContextKeyRoutingSlowStartProbe)
	if ok && (channelID <= 0 || probe.ChannelID == channelID) {
		common.SetContextKey(c, constant.ContextKeyRoutingSlowStartProbe, nil)
	}
}

func (manager *channelRoutingCanaryRuntimeManager) slowStartRuntimeLocked(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	poolID int,
	now time.Time,
) (channelRoutingCanaryRuntimeKey, *channelRoutingCanarySlowStartRuntime, error) {
	runtimeKey := channelRoutingCanaryRuntimeKey{PolicyRevision: policyRevision, PoolID: poolID}
	runtime := manager.slowStarts[runtimeKey]
	if runtime != nil {
		if runtime.policy != policy.SlowStart {
			return runtimeKey, nil, channelrouting.ErrSlowStartInvalidPolicy
		}
		runtime.lastUsed = now
		return runtimeKey, runtime, nil
	}
	if len(manager.slowStarts) >= channelRoutingCanaryMaxSlowStartRuntimes {
		manager.evictOldestSlowStartLocked(runtimeKey)
	}
	if len(manager.slowStarts) >= channelRoutingCanaryMaxSlowStartRuntimes {
		return runtimeKey, nil, errChannelRoutingCanaryRuntimeFull
	}
	tracker, err := channelrouting.NewSlowStartTracker(channelrouting.SlowStartPolicy{
		MinimumFactor: policy.SlowStart.MinimumFactor,
		RampDuration:  time.Duration(policy.SlowStart.RampSeconds) * time.Second,
		StateTTL:      time.Duration(policy.SlowStart.StateTTLSeconds) * time.Second,
		MaxEntries:    channelRoutingCanaryMaxEntries,
	}, manager.clock)
	if err != nil {
		return runtimeKey, nil, err
	}
	runtime = &channelRoutingCanarySlowStartRuntime{
		policy: policy.SlowStart, tracker: tracker,
		seen: make(map[channelrouting.SlowStartKey]struct{}), lastUsed: now,
	}
	manager.slowStarts[runtimeKey] = runtime
	return runtimeKey, runtime, nil
}

func (manager *channelRoutingCanaryRuntimeManager) registerSlowStartKeyLocked(
	runtimeKey channelRoutingCanaryRuntimeKey,
	runtime *channelRoutingCanarySlowStartRuntime,
	key channelrouting.SlowStartKey,
) (bool, error) {
	if runtime == nil || runtime.tracker == nil {
		return false, channelrouting.ErrSlowStartInvalidPolicy
	}
	if _, exists := runtime.seen[key]; exists {
		return false, nil
	}
	if manager.slowStartEntries >= channelRoutingCanaryMaxEntries {
		manager.evictOldestSlowStartLocked(runtimeKey)
	}
	if manager.slowStartEntries >= channelRoutingCanaryMaxEntries {
		return false, errChannelRoutingCanaryRuntimeFull
	}
	runtime.seen[key] = struct{}{}
	manager.slowStartEntries++
	return true, nil
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
