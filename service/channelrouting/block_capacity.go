package channelrouting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	strictCapacityBlockBasisPoints         = 1_000
	strictCapacityBlockLowWaterBasisPoints = 2_500
	strictCapacityBlockMaximumEntries      = 4_096
	strictCapacityBlockMaximumPerEntry     = 4
	strictCapacityBlockEntryIdleTTL        = 30 * time.Minute
)

type strictCapacityBlockManager struct {
	coordinator *StrictCapacityCoordinator
	now         func() time.Time

	mu           sync.Mutex
	entries      map[strictCapacityBlockEntryKey]*strictCapacityBlockEntry
	activeEpochs map[StrictCapacityKey]strictCapacityBlockEpoch
	entryCounts  map[StrictCapacityKey]int
}

type strictCapacityBlockEpoch struct {
	revision   uint64
	configHash string
}

type strictCapacityBlockEntryKey struct {
	resource   StrictCapacityKey
	poolID     int
	revision   uint64
	configHash string
}

type strictCapacityBlockEntry struct {
	manager *strictCapacityBlockManager
	key     strictCapacityBlockEntryKey
	request StrictCapacityRequest
	fenced  atomic.Bool

	mu           sync.Mutex
	blocks       []*strictCapacityBlock
	refillNeeded bool
	lastUsed     time.Time
}

type strictCapacityBlock struct {
	global            *StrictCapacityReservation
	capacity          StrictCapacityDemand
	remaining         StrictCapacityDemand
	inflightAvailable int64
	active            int64
	committed         bool
	retired           bool
	expiresMs         int64
}

type strictCapacityBlockReservation struct {
	entry     *strictCapacityBlockEntry
	block     *strictCapacityBlock
	demand    StrictCapacityDemand
	admission StrictCapacityAdmission

	mu    sync.Mutex
	state strictCapacityReservationState
}

func newStrictCapacityBlockManager(coordinator *StrictCapacityCoordinator) *strictCapacityBlockManager {
	return &strictCapacityBlockManager{
		coordinator:  coordinator,
		now:          time.Now,
		entries:      make(map[strictCapacityBlockEntryKey]*strictCapacityBlockEntry),
		activeEpochs: make(map[StrictCapacityKey]strictCapacityBlockEpoch),
		entryCounts:  make(map[StrictCapacityKey]int),
	}
}

func (manager *strictCapacityBlockManager) tryReserve(
	ctx context.Context,
	request StrictCapacityRequest,
) (*StrictCapacityReservation, error) {
	if manager == nil || manager.coordinator == nil || manager.coordinator.client == nil {
		return nil, ErrStrictCapacityUnavailable
	}
	entry, err := manager.entry(request)
	if err != nil {
		return nil, err
	}
	now := manager.now()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.lastUsed = now
	if entry.fenced.Load() {
		manager.coordinator.stats.blockFenced.Add(1)
		return nil, ErrStrictCapacityConflict
	}
	entry.pruneExpiredLocked(now)
	block := entry.usableBlockLocked(request.Demand, now)
	if entry.refillNeeded {
		refill, refillErr := entry.allocateBlockLocked(ctx, request.Demand)
		if refillErr == nil {
			entry.blocks = append(entry.blocks, refill)
			entry.refillNeeded = false
			block = refill
			manager.coordinator.stats.blockRefills.Add(1)
		} else {
			if errors.Is(refillErr, ErrStrictCapacityConflict) {
				entry.fenced.Store(true)
				manager.coordinator.stats.blockFenced.Add(1)
				return nil, refillErr
			}
			if block == nil {
				return nil, refillErr
			}
			manager.coordinator.stats.blockFallback.Add(1)
		}
	}
	if block == nil {
		block, err = entry.allocateBlockLocked(ctx, request.Demand)
		if err != nil {
			if errors.Is(err, ErrStrictCapacityConflict) {
				entry.fenced.Store(true)
				manager.coordinator.stats.blockFenced.Add(1)
			}
			return nil, err
		}
		entry.blocks = append(entry.blocks, block)
	}
	if !consumeStrictCapacityBlock(block, request.Demand) {
		return nil, ErrStrictCapacityExhausted
	}
	entry.refillNeeded = strictCapacityBlockLowWater(block)
	globalAdmission := block.global.Admission()
	admission := StrictCapacityAdmission{
		Mode: CapacityModeRedisBlock, Key: request.Key, PoolID: request.PoolID,
		PolicyRevision: request.PolicyRevision, CapacityEpoch: globalAdmission.CapacityEpoch,
		Demand: request.Demand, Limit: request.Limit,
		PoolShares:     append([]StrictCapacityPoolShare(nil), request.PoolShares...),
		LeaseTTLMillis: request.LeaseTTL.Milliseconds(), LeaseExpiresMs: block.expiresMs,
		NodeEpochID: NodeEpochID(), BlockLease: true,
	}
	local := &strictCapacityBlockReservation{
		entry: entry, block: block, demand: request.Demand, admission: admission,
		state: strictCapacityReservationPending,
	}
	manager.coordinator.stats.blockAllowed.Add(1)
	return &StrictCapacityReservation{
		coordinator: manager.coordinator,
		admission:   admission,
		block:       local,
		state:       strictCapacityReservationPending,
	}, nil
}

func (manager *strictCapacityBlockManager) entry(
	request StrictCapacityRequest,
) (*strictCapacityBlockEntry, error) {
	configHash := strictCapacityConfigurationHash(request)
	if configHash == "" {
		return nil, ErrStrictCapacityInvalid
	}
	epoch := strictCapacityBlockEpoch{revision: request.PolicyRevision, configHash: configHash}
	key := strictCapacityBlockEntryKey{
		resource: request.Key, poolID: request.PoolID,
		revision: request.PolicyRevision, configHash: configHash,
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, activeExists := manager.activeEpochs[request.Key]
	if active, exists := manager.activeEpochs[request.Key]; exists {
		switch {
		case request.PolicyRevision < active.revision:
			manager.coordinator.stats.blockFenced.Add(1)
			return nil, ErrStrictCapacityConflict
		case request.PolicyRevision == active.revision && active.configHash != configHash:
			manager.coordinator.stats.blockFenced.Add(1)
			return nil, ErrStrictCapacityConflict
		case request.PolicyRevision > active.revision:
			manager.activeEpochs[request.Key] = epoch
			for entryKey, entry := range manager.entries {
				if entryKey.resource == request.Key && entryKey.revision < request.PolicyRevision {
					entry.fenced.Store(true)
				}
			}
		}
	} else {
		manager.activeEpochs[request.Key] = epoch
	}
	if entry := manager.entries[key]; entry != nil {
		return entry, nil
	}
	manager.pruneEntriesLocked(manager.now())
	if len(manager.entries) >= strictCapacityBlockMaximumEntries {
		if !activeExists && manager.entryCounts[request.Key] == 0 {
			delete(manager.activeEpochs, request.Key)
		}
		return nil, ErrStrictCapacityStateLimit
	}
	entry := &strictCapacityBlockEntry{
		manager: manager, key: key, request: request, lastUsed: manager.now(),
	}
	manager.entries[key] = entry
	manager.entryCounts[request.Key]++
	return entry, nil
}

func (manager *strictCapacityBlockManager) pruneEntriesLocked(now time.Time) {
	for key, entry := range manager.entries {
		entry.mu.Lock()
		entry.pruneExpiredLocked(now)
		removable := len(entry.blocks) == 0 &&
			(entry.fenced.Load() || !now.Before(entry.lastUsed.Add(strictCapacityBlockEntryIdleTTL)))
		entry.mu.Unlock()
		if removable {
			delete(manager.entries, key)
			manager.entryCounts[key.resource]--
			if manager.entryCounts[key.resource] <= 0 {
				delete(manager.entryCounts, key.resource)
				delete(manager.activeEpochs, key.resource)
			}
		}
	}
}

func (entry *strictCapacityBlockEntry) allocateBlockLocked(
	ctx context.Context,
	minimum StrictCapacityDemand,
) (*strictCapacityBlock, error) {
	entry.pruneRetiredLocked()
	if len(entry.blocks) >= strictCapacityBlockMaximumPerEntry {
		return nil, ErrStrictCapacityStateLimit
	}
	blockRequest := entry.request
	blockRequest.Demand = strictCapacityBlockDemand(entry.request, minimum)
	if !validStrictCapacityDemand(blockRequest.Demand) ||
		!validStrictCapacityLimit(blockRequest.Limit, blockRequest.Demand) {
		return nil, ErrStrictCapacityInvalid
	}
	global, err := entry.manager.coordinator.tryReserveAtomic(ctx, blockRequest)
	if err != nil {
		return nil, err
	}
	admission := global.Admission()
	return &strictCapacityBlock{
		global: global, capacity: blockRequest.Demand, remaining: blockRequest.Demand,
		inflightAvailable: blockRequest.Demand.Inflight,
		expiresMs:         admission.LeaseExpiresMs,
	}, nil
}

func (entry *strictCapacityBlockEntry) usableBlockLocked(
	demand StrictCapacityDemand,
	now time.Time,
) *strictCapacityBlock {
	safety := max(entry.request.LeaseTTL/10, 100*time.Millisecond)
	minimumExpires := now.Add(safety).UnixMilli()
	for _, block := range entry.blocks {
		if block == nil || block.retired || block.expiresMs <= minimumExpires {
			continue
		}
		if strictCapacityBlockCovers(block, demand) {
			return block
		}
	}
	return nil
}

func (entry *strictCapacityBlockEntry) pruneExpiredLocked(now time.Time) {
	nowMs := now.UnixMilli()
	for _, block := range entry.blocks {
		if block == nil || block.retired || block.expiresMs > nowMs {
			continue
		}
		block.retired = true
		entry.manager.coordinator.stats.blockExpired.Add(1)
	}
	entry.pruneRetiredLocked()
}

func (entry *strictCapacityBlockEntry) pruneRetiredLocked() {
	kept := entry.blocks[:0]
	for _, block := range entry.blocks {
		if block != nil && !block.retired {
			kept = append(kept, block)
		}
	}
	entry.blocks = kept
}

func strictCapacityBlockDemand(
	request StrictCapacityRequest,
	minimum StrictCapacityDemand,
) StrictCapacityDemand {
	maximumBasisPoints := 10_000
	for _, share := range request.PoolShares {
		if share.PoolID == request.PoolID {
			maximumBasisPoints = share.MaximumBasisPoints
			break
		}
	}
	dimension := func(limit int64, demand int64) int64 {
		if limit <= 0 || demand <= 0 {
			return demand
		}
		target := (limit*strictCapacityBlockBasisPoints + 9_999) / 10_000
		poolMaximum := limit * int64(maximumBasisPoints) / 10_000
		if poolMaximum >= demand {
			target = min(target, poolMaximum)
		}
		return max(target, demand)
	}
	result := StrictCapacityDemand{
		RPM:         dimension(request.Limit.RPM, minimum.RPM),
		InputTPM:    dimension(request.Limit.InputTPM, minimum.InputTPM),
		OutputTPM:   dimension(request.Limit.OutputTPM, minimum.OutputTPM),
		Inflight:    dimension(request.Limit.Inflight, minimum.Inflight),
		CostNanoUSD: dimension(request.Limit.CostNanoUSD, minimum.CostNanoUSD),
	}
	result.TotalTPM = dimension(request.Limit.TotalTPM, minimum.TotalTPM)
	if result.InputTPM > 0 && result.OutputTPM > 0 &&
		result.InputTPM <= strictCapacityMaximumValue-result.OutputTPM {
		combined := result.InputTPM + result.OutputTPM
		poolTotalMaximum := request.Limit.TotalTPM * int64(maximumBasisPoints) / 10_000
		if combined <= request.Limit.TotalTPM && (poolTotalMaximum < minimum.TotalTPM || combined <= poolTotalMaximum) {
			result.TotalTPM = max(result.TotalTPM, combined)
		} else {
			result.InputTPM = minimum.InputTPM
			result.OutputTPM = minimum.OutputTPM
			result.TotalTPM = minimum.TotalTPM
		}
	}
	return result
}

func strictCapacityBlockCovers(block *strictCapacityBlock, demand StrictCapacityDemand) bool {
	return block != nil && !block.retired &&
		block.remaining.RPM >= demand.RPM &&
		block.remaining.InputTPM >= demand.InputTPM &&
		block.remaining.OutputTPM >= demand.OutputTPM &&
		block.remaining.TotalTPM >= demand.TotalTPM &&
		block.remaining.CostNanoUSD >= demand.CostNanoUSD &&
		block.inflightAvailable >= demand.Inflight
}

func consumeStrictCapacityBlock(block *strictCapacityBlock, demand StrictCapacityDemand) bool {
	if !strictCapacityBlockCovers(block, demand) {
		return false
	}
	block.remaining.RPM -= demand.RPM
	block.remaining.InputTPM -= demand.InputTPM
	block.remaining.OutputTPM -= demand.OutputTPM
	block.remaining.TotalTPM -= demand.TotalTPM
	block.remaining.CostNanoUSD -= demand.CostNanoUSD
	block.inflightAvailable -= demand.Inflight
	block.active++
	return true
}

func strictCapacityBlockLowWater(block *strictCapacityBlock) bool {
	if block == nil || block.retired {
		return true
	}
	dimensions := [][2]int64{
		{block.capacity.RPM, block.remaining.RPM},
		{block.capacity.InputTPM, block.remaining.InputTPM},
		{block.capacity.OutputTPM, block.remaining.OutputTPM},
		{block.capacity.TotalTPM, block.remaining.TotalTPM},
		{block.capacity.CostNanoUSD, block.remaining.CostNanoUSD},
		{block.capacity.Inflight, block.inflightAvailable},
	}
	for _, values := range dimensions {
		if values[0] > 0 && values[1]*10_000 <= values[0]*strictCapacityBlockLowWaterBasisPoints {
			return true
		}
	}
	return false
}

func (reservation *strictCapacityBlockReservation) admissionSnapshot() StrictCapacityAdmission {
	if reservation == nil || reservation.entry == nil || reservation.block == nil {
		return StrictCapacityAdmission{}
	}
	reservation.entry.mu.Lock()
	defer reservation.entry.mu.Unlock()
	admission := reservation.admission
	admission.LeaseExpiresMs = reservation.block.expiresMs
	admission.PoolShares = append([]StrictCapacityPoolShare(nil), reservation.admission.PoolShares...)
	return admission
}

func (reservation *strictCapacityBlockReservation) commit(ctx context.Context) error {
	if reservation == nil || reservation.entry == nil || reservation.block == nil {
		return ErrStrictCapacityTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == strictCapacityReservationCommitted {
		return nil
	}
	if reservation.state != strictCapacityReservationPending {
		return ErrStrictCapacityTransition
	}
	reservation.entry.mu.Lock()
	defer reservation.entry.mu.Unlock()
	if reservation.block.retired || reservation.block.expiresMs <= reservation.entry.manager.now().UnixMilli() {
		return ErrStrictCapacityLost
	}
	if !reservation.block.committed {
		if err := reservation.block.global.Commit(ctx); err != nil {
			return err
		}
		reservation.block.committed = true
	}
	reservation.state = strictCapacityReservationCommitted
	reservation.entry.manager.coordinator.stats.committed.Add(1)
	return nil
}

func (reservation *strictCapacityBlockReservation) cancel(ctx context.Context) error {
	if reservation == nil || reservation.entry == nil || reservation.block == nil {
		return ErrStrictCapacityTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == strictCapacityReservationCanceled {
		return nil
	}
	if reservation.state != strictCapacityReservationPending {
		return ErrStrictCapacityTransition
	}
	reservation.entry.mu.Lock()
	defer reservation.entry.mu.Unlock()
	block := reservation.block
	if block.retired || block.active <= 0 {
		return ErrStrictCapacityLost
	}
	pristineAfterCancel := !block.committed && block.active == 1 &&
		strictCapacityDemandEqualAfterAdd(block.remaining, reservation.demand, block.capacity) &&
		block.inflightAvailable+reservation.demand.Inflight == block.capacity.Inflight
	if pristineAfterCancel {
		if err := block.global.Cancel(ctx); err != nil {
			return err
		}
		block.active = 0
		block.retired = true
	} else {
		restoreStrictCapacityBlock(block, reservation.demand, true)
	}
	reservation.entry.refillNeeded = false
	reservation.entry.pruneRetiredLocked()
	reservation.state = strictCapacityReservationCanceled
	reservation.entry.manager.coordinator.stats.canceled.Add(1)
	return nil
}

func (reservation *strictCapacityBlockReservation) release(ctx context.Context) error {
	if reservation == nil || reservation.entry == nil || reservation.block == nil {
		return ErrStrictCapacityTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == strictCapacityReservationReleased {
		return nil
	}
	if reservation.state != strictCapacityReservationCommitted {
		return ErrStrictCapacityTransition
	}
	reservation.entry.mu.Lock()
	defer reservation.entry.mu.Unlock()
	block := reservation.block
	if block.retired || block.active <= 0 {
		return ErrStrictCapacityLost
	}
	rateExhausted := block.remaining.RPM == 0 && block.remaining.InputTPM == 0 &&
		block.remaining.OutputTPM == 0 && block.remaining.TotalTPM == 0 && block.remaining.CostNanoUSD == 0
	retireAfterRelease := block.active == 1 && rateExhausted
	if retireAfterRelease {
		if err := block.global.Release(ctx); err != nil {
			return err
		}
		block.active = 0
		block.inflightAvailable += reservation.demand.Inflight
		block.retired = true
	} else {
		restoreStrictCapacityBlock(block, reservation.demand, false)
	}
	reservation.entry.refillNeeded = strictCapacityBlockLowWater(block)
	reservation.entry.pruneRetiredLocked()
	reservation.state = strictCapacityReservationReleased
	reservation.entry.manager.coordinator.stats.released.Add(1)
	return nil
}

func (reservation *strictCapacityBlockReservation) renew(ctx context.Context, leaseTTL time.Duration) error {
	if reservation == nil || reservation.entry == nil || reservation.block == nil ||
		leaseTTL.Milliseconds() != reservation.admission.LeaseTTLMillis {
		return ErrStrictCapacityTransition
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state != strictCapacityReservationPending && reservation.state != strictCapacityReservationCommitted {
		return ErrStrictCapacityTransition
	}
	reservation.entry.mu.Lock()
	defer reservation.entry.mu.Unlock()
	block := reservation.block
	now := reservation.entry.manager.now()
	if block.retired || block.expiresMs <= now.UnixMilli() {
		return ErrStrictCapacityLost
	}
	if time.Duration(block.expiresMs-now.UnixMilli())*time.Millisecond > leaseTTL/2 {
		return nil
	}
	err := block.global.Renew(ctx, leaseTTL)
	if err != nil {
		if block.expiresMs > now.UnixMilli() {
			return nil
		}
		return err
	}
	block.expiresMs = block.global.Admission().LeaseExpiresMs
	reservation.admission.LeaseExpiresMs = block.expiresMs
	return nil
}

func restoreStrictCapacityBlock(
	block *strictCapacityBlock,
	demand StrictCapacityDemand,
	restoreRate bool,
) {
	if restoreRate {
		block.remaining.RPM += demand.RPM
		block.remaining.InputTPM += demand.InputTPM
		block.remaining.OutputTPM += demand.OutputTPM
		block.remaining.TotalTPM += demand.TotalTPM
		block.remaining.CostNanoUSD += demand.CostNanoUSD
	}
	block.inflightAvailable += demand.Inflight
	block.active--
}

func strictCapacityDemandEqualAfterAdd(
	remaining StrictCapacityDemand,
	demand StrictCapacityDemand,
	capacity StrictCapacityDemand,
) bool {
	return remaining.RPM+demand.RPM == capacity.RPM &&
		remaining.InputTPM+demand.InputTPM == capacity.InputTPM &&
		remaining.OutputTPM+demand.OutputTPM == capacity.OutputTPM &&
		remaining.TotalTPM+demand.TotalTPM == capacity.TotalTPM &&
		remaining.CostNanoUSD+demand.CostNanoUSD == capacity.CostNanoUSD
}
