package channelrouting

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	defaultHedgeAuditBufferEntries = 2_048
	defaultHedgeAuditBufferBytes   = int64(32 << 20)
	hedgeAuditCompletionBytes      = int64(1 << 10)
	hedgeAuditFlushBatch           = 64
	hedgeAuditFlushMaxBatches      = 32
	hedgeAuditPersistTimeout       = 5 * time.Second
)

var (
	ErrHedgeAuditBufferFull = errors.New("channel routing hedge audit buffer is full")
	ErrHedgeAuditTransition = errors.New("invalid channel routing hedge audit transition")

	defaultHedgeAuditBuffer = newHedgeAttemptAuditBuffer(
		defaultHedgeAuditBufferEntries,
		defaultHedgeAuditBufferBytes,
	)
	hedgeAuditStartPersistence    = model.StartRoutingHedgeAttemptAuditContext
	hedgeAuditCompletePersistence = model.CompleteRoutingHedgeAttemptAuditContext
)

type HedgeAttemptAuditStats struct {
	Entries                    int   `json:"entries"`
	Capacity                   int   `json:"capacity"`
	Bytes                      int64 `json:"buffered_bytes"`
	ByteCapacity               int64 `json:"byte_capacity"`
	InProgress                 int   `json:"in_progress"`
	Completed                  int   `json:"completed"`
	Reserved                   int64 `json:"reserved"`
	Rejected                   int64 `json:"rejected"`
	LastRejectedMs             int64 `json:"last_rejected_ms"`
	Persisted                  int64 `json:"persisted"`
	PersistFailures            int64 `json:"persist_failures"`
	ConsecutivePersistFailures int64 `json:"consecutive_persist_failures"`
	OldestStartedMs            int64 `json:"oldest_started_ms"`
}

type HedgeAttemptAuditReservation struct {
	buffer     *hedgeAttemptAuditBuffer
	attemptKey string
}

type hedgeAttemptAuditEntry struct {
	start      model.RoutingHedgeAttemptStartSpec
	completion *model.RoutingHedgeAttemptCompleteSpec
	auditID    int64
	bytes      int64
	element    *list.Element
}

type hedgeAttemptAuditSnapshot struct {
	attemptKey string
	start      model.RoutingHedgeAttemptStartSpec
	completion *model.RoutingHedgeAttemptCompleteSpec
	auditID    int64
}

type hedgeAttemptAuditBuffer struct {
	mu      sync.Mutex
	flushMu sync.Mutex

	entries             map[string]*hedgeAttemptAuditEntry
	order               list.List
	capacity            int
	byteCapacity        int64
	bytes               int64
	reserved            int64
	rejected            int64
	lastRejectedMs      int64
	persisted           int64
	failures            int64
	consecutiveFailures int64
}

func ReserveHedgeAttemptAudit(
	spec model.RoutingHedgeAttemptStartSpec,
) (*HedgeAttemptAuditReservation, error) {
	return ReserveUpstreamAttemptAudit(spec)
}

func ReserveUpstreamAttemptAudit(
	spec model.RoutingHedgeAttemptStartSpec,
) (*HedgeAttemptAuditReservation, error) {
	return defaultHedgeAuditBuffer.reserve(spec)
}

func (reservation *HedgeAttemptAuditReservation) Complete(
	spec model.RoutingHedgeAttemptCompleteSpec,
) error {
	if reservation == nil || reservation.buffer == nil || reservation.attemptKey == "" {
		return ErrHedgeAuditTransition
	}
	if err := model.ValidateRoutingHedgeAttemptCompleteSpec(spec); err != nil {
		return err
	}
	return reservation.buffer.complete(reservation.attemptKey, spec)
}

// Discard removes an attempt reservation that was proven to be a local replay
// before any upstream send. It deliberately creates no routing observation.
func (reservation *HedgeAttemptAuditReservation) Discard() error {
	if reservation == nil || reservation.buffer == nil || reservation.attemptKey == "" {
		return ErrHedgeAuditTransition
	}
	return reservation.buffer.discard(reservation.attemptKey)
}

func FlushHedgeAttemptAuditsContext(ctx context.Context) (int, error) {
	flushedTotal := 0
	for batchIndex := 0; batchIndex < hedgeAuditFlushMaxBatches; batchIndex++ {
		flushed, err := defaultHedgeAuditBuffer.flush(ctx, hedgeAuditFlushBatch)
		flushedTotal += flushed
		if err != nil {
			return flushedTotal, err
		}
		if flushed < hedgeAuditFlushBatch {
			return flushedTotal, nil
		}
	}
	return flushedTotal, nil
}

func HedgeAttemptAuditsStats() HedgeAttemptAuditStats {
	return defaultHedgeAuditBuffer.stats()
}

func ResetHedgeAttemptAuditsForTest(capacity ...int) {
	entryCapacity := defaultHedgeAuditBufferEntries
	if len(capacity) > 0 && capacity[0] > 0 {
		entryCapacity = capacity[0]
	}
	defaultHedgeAuditBuffer = newHedgeAttemptAuditBuffer(entryCapacity, defaultHedgeAuditBufferBytes)
}

func DeleteExpiredRoutingHedgeAttemptAuditsContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxRetentionDays := int((time.Duration(1<<63 - 1)) / (24 * time.Hour))
	if retentionDays > maxRetentionDays {
		retentionDays = maxRetentionDays
	}
	databaseNowMs, err := model.RoutingDatabaseNowMsContext(ctx)
	if err != nil {
		return 0, err
	}
	cutoffMs := time.UnixMilli(databaseNowMs).Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	return model.DeleteRoutingHedgeAttemptAuditsBeforeContext(ctx, cutoffMs)
}

func newHedgeAttemptAuditBuffer(capacity int, byteCapacity int64) *hedgeAttemptAuditBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if byteCapacity < 1 {
		byteCapacity = 1
	}
	return &hedgeAttemptAuditBuffer{
		entries:  make(map[string]*hedgeAttemptAuditEntry, capacity),
		capacity: capacity, byteCapacity: byteCapacity,
	}
}

func (buffer *hedgeAttemptAuditBuffer) reserve(
	spec model.RoutingHedgeAttemptStartSpec,
) (*HedgeAttemptAuditReservation, error) {
	if buffer == nil {
		return nil, ErrHedgeAuditBufferFull
	}
	if spec.AttemptKey == "" {
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			return nil, err
		}
		spec.AttemptKey = hex.EncodeToString(token)
	}
	if err := model.ValidateRoutingHedgeAttemptStartSpec(spec); err != nil {
		return nil, err
	}
	encoded, err := common.Marshal(spec)
	if err != nil {
		return nil, err
	}
	entryBytes := int64(len(encoded)) + hedgeAuditCompletionBytes

	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.entries) >= buffer.capacity || entryBytes > buffer.byteCapacity || buffer.bytes+entryBytes > buffer.byteCapacity {
		buffer.rejected++
		buffer.lastRejectedMs = time.Now().UnixMilli()
		return nil, ErrHedgeAuditBufferFull
	}
	if _, exists := buffer.entries[spec.AttemptKey]; exists {
		return nil, ErrHedgeAuditTransition
	}
	entry := &hedgeAttemptAuditEntry{start: spec, bytes: entryBytes}
	entry.element = buffer.order.PushBack(spec.AttemptKey)
	buffer.entries[spec.AttemptKey] = entry
	buffer.bytes += entryBytes
	buffer.reserved++
	return &HedgeAttemptAuditReservation{buffer: buffer, attemptKey: spec.AttemptKey}, nil
}

func (buffer *hedgeAttemptAuditBuffer) complete(
	attemptKey string,
	spec model.RoutingHedgeAttemptCompleteSpec,
) error {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	entry, exists := buffer.entries[attemptKey]
	if !exists {
		return ErrHedgeAuditTransition
	}
	if entry.completion != nil {
		if *entry.completion == spec {
			return nil
		}
		return ErrHedgeAuditTransition
	}
	copy := spec
	entry.completion = &copy
	return nil
}

func (buffer *hedgeAttemptAuditBuffer) discard(attemptKey string) error {
	buffer.flushMu.Lock()
	defer buffer.flushMu.Unlock()
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	entry, exists := buffer.entries[attemptKey]
	if !exists || entry.auditID != 0 || entry.completion != nil {
		return ErrHedgeAuditTransition
	}
	delete(buffer.entries, attemptKey)
	buffer.order.Remove(entry.element)
	buffer.bytes -= entry.bytes
	return nil
}

func (buffer *hedgeAttemptAuditBuffer) flush(ctx context.Context, limit int) (int, error) {
	if buffer == nil || limit < 1 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	flushCtx, cancel := context.WithTimeout(ctx, hedgeAuditPersistTimeout)
	defer cancel()

	buffer.flushMu.Lock()
	defer buffer.flushMu.Unlock()
	batch := buffer.snapshot(limit)
	persisted := 0
	var persistErr error
	for _, item := range batch {
		if err := flushCtx.Err(); err != nil {
			buffer.markFailure()
			return persisted, errors.Join(persistErr, err)
		}
		auditID := item.auditID
		if auditID == 0 {
			audit, err := hedgeAuditStartPersistence(flushCtx, item.start)
			if err != nil {
				buffer.markFailure()
				buffer.moveToBack(item.attemptKey)
				persistErr = errors.Join(persistErr, err)
				continue
			}
			auditID = audit.ID
			if auditID <= 0 || !buffer.markStarted(item.attemptKey, auditID) {
				buffer.markFailure()
				buffer.moveToBack(item.attemptKey)
				persistErr = errors.Join(persistErr, ErrHedgeAuditTransition)
				continue
			}
		}
		if item.completion == nil {
			continue
		}
		if _, err := hedgeAuditCompletePersistence(flushCtx, auditID, *item.completion); err != nil {
			buffer.markFailure()
			buffer.moveToBack(item.attemptKey)
			persistErr = errors.Join(persistErr, err)
			continue
		}
		if !buffer.removePersisted(item.attemptKey, *item.completion) {
			buffer.markFailure()
			persistErr = errors.Join(persistErr, ErrHedgeAuditTransition)
			continue
		}
		persisted++
	}
	return persisted, persistErr
}

func (buffer *hedgeAttemptAuditBuffer) snapshot(limit int) []hedgeAttemptAuditSnapshot {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if limit > len(buffer.entries) {
		limit = len(buffer.entries)
	}
	batch := make([]hedgeAttemptAuditSnapshot, 0, limit)
	for element := buffer.order.Front(); element != nil && len(batch) < limit; element = element.Next() {
		attemptKey, _ := element.Value.(string)
		entry := buffer.entries[attemptKey]
		if entry == nil {
			continue
		}
		if entry.auditID > 0 && entry.completion == nil {
			continue
		}
		item := hedgeAttemptAuditSnapshot{attemptKey: attemptKey, start: entry.start, auditID: entry.auditID}
		if entry.completion != nil {
			copy := *entry.completion
			item.completion = &copy
		}
		batch = append(batch, item)
	}
	return batch
}

func (buffer *hedgeAttemptAuditBuffer) markStarted(attemptKey string, auditID int64) bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	entry := buffer.entries[attemptKey]
	if entry == nil {
		return false
	}
	if entry.auditID != 0 && entry.auditID != auditID {
		return false
	}
	entry.auditID = auditID
	return true
}

func (buffer *hedgeAttemptAuditBuffer) removePersisted(
	attemptKey string,
	completion model.RoutingHedgeAttemptCompleteSpec,
) bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	entry := buffer.entries[attemptKey]
	if entry == nil || entry.completion == nil || *entry.completion != completion {
		return false
	}
	delete(buffer.entries, attemptKey)
	buffer.order.Remove(entry.element)
	buffer.bytes -= entry.bytes
	buffer.persisted++
	buffer.consecutiveFailures = 0
	return true
}

func (buffer *hedgeAttemptAuditBuffer) markFailure() {
	buffer.mu.Lock()
	buffer.failures++
	buffer.consecutiveFailures++
	buffer.mu.Unlock()
}

func (buffer *hedgeAttemptAuditBuffer) moveToBack(attemptKey string) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	entry := buffer.entries[attemptKey]
	if entry != nil && entry.element != nil {
		buffer.order.MoveToBack(entry.element)
	}
}

func (buffer *hedgeAttemptAuditBuffer) stats() HedgeAttemptAuditStats {
	if buffer == nil {
		return HedgeAttemptAuditStats{}
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	stats := HedgeAttemptAuditStats{
		Entries: len(buffer.entries), Capacity: buffer.capacity,
		Bytes: buffer.bytes, ByteCapacity: buffer.byteCapacity,
		Reserved: buffer.reserved, Rejected: buffer.rejected, LastRejectedMs: buffer.lastRejectedMs,
		Persisted: buffer.persisted, PersistFailures: buffer.failures,
		ConsecutivePersistFailures: buffer.consecutiveFailures,
	}
	for element := buffer.order.Front(); element != nil; element = element.Next() {
		attemptKey, _ := element.Value.(string)
		entry := buffer.entries[attemptKey]
		if entry == nil {
			continue
		}
		if stats.OldestStartedMs == 0 || entry.start.StartedTimeMs < stats.OldestStartedMs {
			stats.OldestStartedMs = entry.start.StartedTimeMs
		}
		if entry.completion == nil {
			stats.InProgress++
		} else {
			stats.Completed++
		}
	}
	return stats
}
