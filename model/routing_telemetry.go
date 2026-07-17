package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingTelemetryMaxItems       = 32
	RoutingTelemetryNodeIDMaxRunes = 128

	routingTelemetryNodeIDMaxBytes        = 512
	routingTelemetryReceiptRetentionBatch = 500
	routingTelemetryStateSequence         = int64(0)
	routingTelemetryMaxTombstoneRanges    = 4_096
	routingTelemetryTombstoneMaxBytes     = 60 << 10
	routingTelemetryTombstonePrefix       = "v1:"
)

var (
	ErrRoutingTelemetryInvalid           = errors.New("invalid routing telemetry batch")
	ErrRoutingTelemetrySequenceCollision = errors.New("routing telemetry sequence collision")
	errRoutingTelemetryTombstoneLimit    = errors.New("routing telemetry tombstone encoding exceeds limit")
)

// RoutingTelemetryReceipt makes an immutable node sequence safe to redeliver.
type RoutingTelemetryReceipt struct {
	ID           int64  `json:"id" gorm:"primaryKey"`
	NodeID       string `json:"node_id" gorm:"type:varchar(128);not null"`
	NodeKey      string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_routing_telemetry_event,priority:1"`
	Sequence     int64  `json:"sequence" gorm:"bigint;not null;uniqueIndex:idx_routing_telemetry_event,priority:2"`
	PayloadHash  string `json:"payload_hash" gorm:"type:char(64);not null"`
	ApplyToken   string `json:"-" gorm:"type:char(32);not null"`
	ItemCount    int    `json:"item_count" gorm:"not null"`
	ProducedAtMs int64  `json:"produced_at_ms" gorm:"bigint;not null"`
	AppliedAt    int64  `json:"applied_at" gorm:"bigint;not null;index"`

	// Sequence zero is a soft-deleted internal state row. Default GORM queries
	// continue to expose only immutable delivery receipts.
	CompactedThrough int64          `json:"-" gorm:"bigint"`
	TombstoneRanges  string         `json:"-" gorm:"type:text"`
	DeletedAt        gorm.DeletedAt `json:"-" gorm:"index"`
}

func (RoutingTelemetryReceipt) TableName() string {
	return "routing_telemetry_receipts"
}

type RoutingTelemetryBatch struct {
	NodeID       string                `json:"node_id"`
	Sequence     int64                 `json:"sequence"`
	PayloadHash  string                `json:"payload_hash"`
	ProducedAtMs int64                 `json:"produced_at_ms"`
	Items        []RoutingMetricRollup `json:"items"`
}

type RoutingTelemetryApplyResult struct {
	Duplicate    bool `json:"duplicate"`
	AppliedItems int  `json:"applied_items"`
}

type routingTelemetrySequenceRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ComputeRoutingTelemetryPayloadHash binds an immutable node sequence to the
// exact envelope contents that were produced. PayloadHash itself is excluded.
func ComputeRoutingTelemetryPayloadHash(batch RoutingTelemetryBatch) (string, error) {
	payload, err := common.Marshal(struct {
		NodeID       string                `json:"node_id"`
		Sequence     int64                 `json:"sequence"`
		ProducedAtMs int64                 `json:"produced_at_ms"`
		Items        []RoutingMetricRollup `json:"items"`
	}{
		NodeID:       batch.NodeID,
		Sequence:     batch.Sequence,
		ProducedAtMs: batch.ProducedAtMs,
		Items:        batch.Items,
	})
	if err != nil {
		return "", fmt.Errorf("marshal routing telemetry payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func ApplyRoutingTelemetryBatchContext(ctx context.Context, batch RoutingTelemetryBatch) (RoutingTelemetryApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RoutingTelemetryApplyResult{}, err
	}
	normalized, err := normalizeRoutingTelemetryBatch(batch)
	if err != nil {
		return RoutingTelemetryApplyResult{}, err
	}
	nodeKey := routingTelemetryNodeKey(batch.NodeID)
	result := RoutingTelemetryApplyResult{}
	err = runRoutingMetricTransactionWithRetry(ctx, func(tx *gorm.DB) error {
		result = RoutingTelemetryApplyResult{}
		state, err := loadRoutingTelemetryStateForUpdate(ctx, tx, batch.NodeID, nodeKey)
		if err != nil {
			return err
		}
		compacted, err := routingTelemetryStateContainsSequence(state, batch.Sequence)
		if err != nil {
			return err
		}
		if compacted {
			result.Duplicate = true
			return nil
		}

		var applyToken [16]byte
		if _, err := rand.Read(applyToken[:]); err != nil {
			return fmt.Errorf("generate routing telemetry apply token: %w", err)
		}
		receipt := RoutingTelemetryReceipt{
			NodeID:       batch.NodeID,
			NodeKey:      nodeKey,
			Sequence:     batch.Sequence,
			PayloadHash:  batch.PayloadHash,
			ApplyToken:   hex.EncodeToString(applyToken[:]),
			ItemCount:    len(batch.Items),
			ProducedAtMs: batch.ProducedAtMs,
			AppliedAt:    time.Now().UnixMilli(),
		}
		created := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_key"}, {Name: "sequence"}},
			DoNothing: true,
		}).Create(&receipt)
		if created.Error != nil {
			return created.Error
		}
		var existing RoutingTelemetryReceipt
		if err := lockForUpdate(tx.WithContext(ctx)).Where(
			"node_key = ? AND sequence = ?", receipt.NodeKey, receipt.Sequence,
		).First(&existing).Error; err != nil {
			return err
		}
		if existing.ApplyToken != receipt.ApplyToken {
			if existing.NodeID != receipt.NodeID || existing.PayloadHash != receipt.PayloadHash {
				return ErrRoutingTelemetrySequenceCollision
			}
			result.Duplicate = true
			return nil
		}
		appliedItems, err := applyRoutingMetricRollupsTx(ctx, tx, normalized)
		if err != nil {
			return err
		}
		result.AppliedItems = appliedItems
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return RoutingTelemetryApplyResult{}, ctx.Err()
	}
	if err != nil {
		return RoutingTelemetryApplyResult{}, err
	}
	return result, nil
}

func loadRoutingTelemetryStateForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	nodeID string,
	nodeKey string,
) (*RoutingTelemetryReceipt, error) {
	state := RoutingTelemetryReceipt{
		NodeID:          nodeID,
		NodeKey:         nodeKey,
		Sequence:        routingTelemetryStateSequence,
		PayloadHash:     strings.Repeat("0", sha256.Size*2),
		ApplyToken:      strings.Repeat("0", 32),
		TombstoneRanges: routingTelemetryTombstonePrefix,
		AppliedAt:       time.Now().UnixMilli(),
		DeletedAt:       gorm.DeletedAt{Time: time.Now(), Valid: true},
	}
	columns := `"node_id", "node_key", "sequence", "payload_hash", "apply_token", "item_count", "produced_at_ms", "applied_at", "compacted_through", "tombstone_ranges", "deleted_at"`
	placeholders := "?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?"
	insertState := "INSERT INTO routing_telemetry_receipts (" + columns + ") VALUES (" + placeholders + ") " +
		`ON CONFLICT ("node_key", "sequence") DO NOTHING`
	if tx.Dialector.Name() == "mysql" {
		columns = "`node_id`, `node_key`, `sequence`, `payload_hash`, `apply_token`, `item_count`, `produced_at_ms`, `applied_at`, `compacted_through`, `tombstone_ranges`, `deleted_at`"
		insertState = "INSERT IGNORE INTO routing_telemetry_receipts (" + columns + ") VALUES (" + placeholders + ")"
	}
	var existing RoutingTelemetryReceipt
	for attempt := 0; attempt < 2; attempt++ {
		if err := tx.WithContext(ctx).Exec(
			insertState,
			state.NodeID,
			state.NodeKey,
			state.Sequence,
			state.PayloadHash,
			state.ApplyToken,
			state.ItemCount,
			state.ProducedAtMs,
			state.AppliedAt,
			state.CompactedThrough,
			state.TombstoneRanges,
			state.DeletedAt.Time,
		).Error; err != nil {
			return nil, err
		}
		err := lockForUpdate(tx.WithContext(ctx).Unscoped()).Where(
			"node_key = ? AND sequence = ?", nodeKey, routingTelemetryStateSequence,
		).First(&existing).Error
		if err == nil {
			break
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) || attempt == 1 {
			return nil, err
		}
	}
	if existing.NodeID != nodeID || existing.NodeKey != nodeKey || existing.Sequence != routingTelemetryStateSequence || !existing.DeletedAt.Valid {
		return nil, ErrRoutingTelemetrySequenceCollision
	}
	if _, err := routingTelemetryStateRanges(&existing); err != nil {
		return nil, err
	}
	return &existing, nil
}

func routingTelemetryStateContainsSequence(state *RoutingTelemetryReceipt, sequence int64) (bool, error) {
	if sequence <= state.CompactedThrough {
		return true, nil
	}
	ranges, err := routingTelemetryStateRanges(state)
	if err != nil {
		return false, err
	}
	index := sort.Search(len(ranges), func(index int) bool {
		return ranges[index].End >= sequence
	})
	return index < len(ranges) && ranges[index].Start <= sequence, nil
}

func routingTelemetryStateRanges(state *RoutingTelemetryReceipt) ([]routingTelemetrySequenceRange, error) {
	if state.CompactedThrough < 0 {
		return nil, fmt.Errorf("%w: telemetry high-water is negative", ErrRoutingTelemetryInvalid)
	}
	if state.TombstoneRanges == "" {
		return nil, nil
	}
	var ranges []routingTelemetrySequenceRange
	if strings.HasPrefix(state.TombstoneRanges, routingTelemetryTombstonePrefix) {
		if len(state.TombstoneRanges) > routingTelemetryTombstoneMaxBytes {
			return nil, fmt.Errorf("%w: telemetry tombstone encoding exceeds limit", ErrRoutingTelemetryInvalid)
		}
		decoded, err := decodeRoutingTelemetryStateRanges(
			state.CompactedThrough,
			strings.TrimPrefix(state.TombstoneRanges, routingTelemetryTombstonePrefix),
		)
		if err != nil {
			return nil, err
		}
		ranges = decoded
	} else if err := common.UnmarshalJsonStr(state.TombstoneRanges, &ranges); err != nil {
		return nil, fmt.Errorf("%w: decode telemetry tombstones: %v", ErrRoutingTelemetryInvalid, err)
	}
	if len(ranges) > routingTelemetryMaxTombstoneRanges {
		return nil, fmt.Errorf("%w: too many telemetry tombstone ranges", ErrRoutingTelemetryInvalid)
	}
	previousEnd := state.CompactedThrough
	for index := range ranges {
		current := ranges[index]
		if current.Start <= state.CompactedThrough || current.End < current.Start || current.Start <= 0 ||
			(previousEnd == math.MaxInt64 || current.Start <= previousEnd+1) {
			return nil, fmt.Errorf("%w: malformed telemetry tombstone ranges", ErrRoutingTelemetryInvalid)
		}
		previousEnd = current.End
	}
	return ranges, nil
}

func decodeRoutingTelemetryStateRanges(compactedThrough int64, encoded string) ([]routingTelemetrySequenceRange, error) {
	if encoded == "" {
		return nil, nil
	}
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: decode telemetry tombstones: %v", ErrRoutingTelemetryInvalid, err)
	}
	ranges := make([]routingTelemetrySequenceRange, 0)
	previousEnd := compactedThrough
	for len(payload) > 0 {
		gap, gapBytes := binary.Uvarint(payload)
		if gapBytes <= 0 {
			return nil, fmt.Errorf("%w: malformed telemetry tombstone gap", ErrRoutingTelemetryInvalid)
		}
		payload = payload[gapBytes:]
		length, lengthBytes := binary.Uvarint(payload)
		if lengthBytes <= 0 {
			return nil, fmt.Errorf("%w: malformed telemetry tombstone length", ErrRoutingTelemetryInvalid)
		}
		payload = payload[lengthBytes:]
		if previousEnd == math.MaxInt64 {
			return nil, fmt.Errorf("%w: telemetry tombstone follows maximum sequence", ErrRoutingTelemetryInvalid)
		}
		base := uint64(previousEnd) + 1
		if base > math.MaxInt64 || gap > uint64(math.MaxInt64)-base {
			return nil, fmt.Errorf("%w: telemetry tombstone start overflows", ErrRoutingTelemetryInvalid)
		}
		start := base + gap
		if length > math.MaxInt64-start {
			return nil, fmt.Errorf("%w: telemetry tombstone end overflows", ErrRoutingTelemetryInvalid)
		}
		end := start + length
		ranges = append(ranges, routingTelemetrySequenceRange{Start: int64(start), End: int64(end)})
		if len(ranges) > routingTelemetryMaxTombstoneRanges {
			return nil, fmt.Errorf("%w: too many telemetry tombstone ranges", ErrRoutingTelemetryInvalid)
		}
		previousEnd = int64(end)
	}
	return ranges, nil
}

func encodeRoutingTelemetryStateRanges(compactedThrough int64, ranges []routingTelemetrySequenceRange) (string, error) {
	payload := make([]byte, 0, len(ranges)*2)
	previousEnd := compactedThrough
	var scratch [binary.MaxVarintLen64]byte
	for index := range ranges {
		current := ranges[index]
		gap := uint64(current.Start - previousEnd - 1)
		written := binary.PutUvarint(scratch[:], gap)
		payload = append(payload, scratch[:written]...)
		written = binary.PutUvarint(scratch[:], uint64(current.End-current.Start))
		payload = append(payload, scratch[:written]...)
		previousEnd = current.End
	}
	encoded := routingTelemetryTombstonePrefix + base64.RawStdEncoding.EncodeToString(payload)
	if len(encoded) > routingTelemetryTombstoneMaxBytes {
		return "", errRoutingTelemetryTombstoneLimit
	}
	return encoded, nil
}

func addRoutingTelemetryStateSequences(state *RoutingTelemetryReceipt, sequences []int64) error {
	ranges, err := routingTelemetryStateRanges(state)
	if err != nil {
		return err
	}
	for _, sequence := range sequences {
		if sequence <= 0 {
			return fmt.Errorf("%w: telemetry tombstone sequence must be positive", ErrRoutingTelemetryInvalid)
		}
		if sequence > state.CompactedThrough {
			ranges = append(ranges, routingTelemetrySequenceRange{Start: sequence, End: sequence})
		}
	}
	sort.Slice(ranges, func(left int, right int) bool {
		if ranges[left].Start != ranges[right].Start {
			return ranges[left].Start < ranges[right].Start
		}
		return ranges[left].End < ranges[right].End
	})

	highWater := state.CompactedThrough
	compacted := make([]routingTelemetrySequenceRange, 0, len(ranges))
	for _, current := range ranges {
		if current.End <= highWater {
			continue
		}
		if current.Start <= highWater || (highWater != math.MaxInt64 && current.Start == highWater+1) {
			highWater = max(highWater, current.End)
			continue
		}
		if len(compacted) > 0 {
			last := &compacted[len(compacted)-1]
			if current.Start <= last.End || (last.End != math.MaxInt64 && current.Start == last.End+1) {
				last.End = max(last.End, current.End)
				continue
			}
		}
		compacted = append(compacted, current)
	}
	state.CompactedThrough = highWater
	if len(compacted) > routingTelemetryMaxTombstoneRanges {
		expireRoutingTelemetryStateRanges(state, compacted)
		return nil
	}
	encoded, err := encodeRoutingTelemetryStateRanges(highWater, compacted)
	if errors.Is(err, errRoutingTelemetryTombstoneLimit) {
		expireRoutingTelemetryStateRanges(state, compacted)
		return nil
	}
	if err != nil {
		return err
	}
	state.TombstoneRanges = encoded
	return nil
}

func expireRoutingTelemetryStateRanges(state *RoutingTelemetryReceipt, ranges []routingTelemetrySequenceRange) {
	// This path is reached only while deleting receipts beyond the configured
	// idempotency window. Advancing across gaps rejects very late delivery
	// instead of letting an oversized tombstone block retention forever.
	highWater := state.CompactedThrough
	for index := range ranges {
		highWater = max(highWater, ranges[index].End)
	}
	state.CompactedThrough = highWater
	state.TombstoneRanges = routingTelemetryTombstonePrefix
}

func DeleteRoutingTelemetryReceiptsBeforeContext(ctx context.Context, cutoff int64) (int64, error) {
	if cutoff <= 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var candidates []RoutingTelemetryReceipt
		if err := DB.WithContext(ctx).
			Where("applied_at < ?", cutoff).
			Order("applied_at asc").
			Order("id asc").
			Limit(routingTelemetryReceiptRetentionBatch).
			Find(&candidates).Error; err != nil {
			return total, err
		}
		if len(candidates) == 0 {
			break
		}

		candidateIDs := make([]int64, 0, len(candidates))
		nodes := make(map[string]string)
		for index := range candidates {
			candidateIDs = append(candidateIDs, candidates[index].ID)
			nodes[candidates[index].NodeKey] = candidates[index].NodeID
		}
		nodeKeys := make([]string, 0, len(nodes))
		for nodeKey := range nodes {
			nodeKeys = append(nodeKeys, nodeKey)
		}
		sort.Strings(nodeKeys)

		var deleted int64
		err := runRoutingMetricTransactionWithRetry(ctx, func(tx *gorm.DB) error {
			deleted = 0
			states := make(map[string]*RoutingTelemetryReceipt, len(nodeKeys))
			for _, nodeKey := range nodeKeys {
				state, err := loadRoutingTelemetryStateForUpdate(ctx, tx, nodes[nodeKey], nodeKey)
				if err != nil {
					return err
				}
				states[nodeKey] = state
			}

			var receipts []RoutingTelemetryReceipt
			if err := lockForUpdate(tx.WithContext(ctx)).
				Where("id IN ? AND applied_at < ?", candidateIDs, cutoff).
				Order("node_key asc").
				Order("sequence asc").
				Order("id asc").
				Find(&receipts).Error; err != nil {
				return err
			}
			if len(receipts) == 0 {
				return nil
			}

			sequencesByNode := make(map[string][]int64, len(states))
			ids := make([]int64, 0, len(receipts))
			for index := range receipts {
				receipt := receipts[index]
				state, exists := states[receipt.NodeKey]
				if !exists || state.NodeID != receipt.NodeID {
					return ErrRoutingTelemetrySequenceCollision
				}
				sequencesByNode[receipt.NodeKey] = append(sequencesByNode[receipt.NodeKey], receipt.Sequence)
				ids = append(ids, receipt.ID)
			}
			for _, nodeKey := range nodeKeys {
				sequences := sequencesByNode[nodeKey]
				if len(sequences) == 0 {
					continue
				}
				state := states[nodeKey]
				if err := addRoutingTelemetryStateSequences(state, sequences); err != nil {
					return err
				}
				if err := tx.WithContext(ctx).Unscoped().Model(&RoutingTelemetryReceipt{}).
					Where("id = ?", state.ID).
					Updates(map[string]any{
						"compacted_through": state.CompactedThrough,
						"tombstone_ranges":  state.TombstoneRanges,
						"applied_at":        time.Now().UnixMilli(),
					}).Error; err != nil {
					return err
				}
			}

			deleteResult := tx.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&RoutingTelemetryReceipt{})
			if deleteResult.Error != nil {
				return deleteResult.Error
			}
			deleted = deleteResult.RowsAffected
			return nil
		})
		if err != nil {
			return total, err
		}
		total += deleted
		if len(candidates) < routingTelemetryReceiptRetentionBatch {
			break
		}
	}
	statesDeleted, err := deleteExpiredRoutingTelemetryStatesContext(ctx, cutoff)
	return total + statesDeleted, err
}

func deleteExpiredRoutingTelemetryStatesContext(ctx context.Context, cutoff int64) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var candidateIDs []int64
		if err := DB.WithContext(ctx).Unscoped().Table("routing_telemetry_receipts AS state").
			Where("state.sequence = ? AND state.deleted_at IS NOT NULL AND state.applied_at < ?", routingTelemetryStateSequence, cutoff).
			Where(`NOT EXISTS (
				SELECT 1 FROM routing_telemetry_receipts AS active_receipt
				WHERE active_receipt.node_key = state.node_key
					AND active_receipt.sequence > 0
					AND active_receipt.deleted_at IS NULL
			)`).
			Order("state.applied_at asc").
			Order("state.id asc").
			Limit(routingTelemetryReceiptRetentionBatch).
			Pluck("state.id", &candidateIDs).Error; err != nil {
			return total, err
		}
		if len(candidateIDs) == 0 {
			return total, nil
		}

		var deleted int64
		err := runRoutingMetricTransactionWithRetry(ctx, func(tx *gorm.DB) error {
			var states []RoutingTelemetryReceipt
			if err := lockForUpdate(tx.WithContext(ctx).Unscoped()).
				Where("id IN ? AND sequence = ? AND deleted_at IS NOT NULL AND applied_at < ?", candidateIDs, routingTelemetryStateSequence, cutoff).
				Order("id asc").
				Find(&states).Error; err != nil {
				return err
			}
			if len(states) == 0 {
				return nil
			}
			nodeKeys := make([]string, 0, len(states))
			for index := range states {
				nodeKeys = append(nodeKeys, states[index].NodeKey)
			}
			var activeNodeKeys []string
			if err := tx.WithContext(ctx).Model(&RoutingTelemetryReceipt{}).
				Where("node_key IN ? AND sequence > ?", nodeKeys, routingTelemetryStateSequence).
				Distinct("node_key").
				Pluck("node_key", &activeNodeKeys).Error; err != nil {
				return err
			}
			active := make(map[string]struct{}, len(activeNodeKeys))
			for _, nodeKey := range activeNodeKeys {
				active[nodeKey] = struct{}{}
			}
			ids := make([]int64, 0, len(states))
			for index := range states {
				if _, exists := active[states[index].NodeKey]; !exists {
					ids = append(ids, states[index].ID)
				}
			}
			if len(ids) == 0 {
				return nil
			}
			result := tx.WithContext(ctx).Unscoped().Where("id IN ?", ids).Delete(&RoutingTelemetryReceipt{})
			if result.Error != nil {
				return result.Error
			}
			deleted = result.RowsAffected
			return nil
		})
		if err != nil {
			return total, err
		}
		total += deleted
		if len(candidateIDs) < routingTelemetryReceiptRetentionBatch {
			return total, nil
		}
	}
}

func normalizeRoutingTelemetryBatch(batch RoutingTelemetryBatch) ([]RoutingMetricRollup, error) {
	return normalizeRoutingTelemetryBatchAt(batch, time.Now())
}

func normalizeRoutingTelemetryBatchAt(batch RoutingTelemetryBatch, now time.Time) ([]RoutingMetricRollup, error) {
	if !validRoutingTelemetryNodeID(batch.NodeID) || batch.Sequence <= 0 ||
		!validRoutingTelemetryPayloadHash(batch.PayloadHash) || batch.ProducedAtMs <= 0 ||
		len(batch.Items) == 0 || len(batch.Items) > RoutingTelemetryMaxItems {
		return nil, ErrRoutingTelemetryInvalid
	}
	nowMs := now.UnixMilli()
	futureSkewMs := int64(routingMetricRollupFutureSkew / time.Millisecond)
	maxProducedAtMs := int64(math.MaxInt64)
	if nowMs <= math.MaxInt64-futureSkewMs {
		maxProducedAtMs = nowMs + futureSkewMs
	}
	if batch.ProducedAtMs > maxProducedAtMs {
		return nil, fmt.Errorf("%w: produced time is too far in the future", ErrRoutingTelemetryInvalid)
	}
	expectedHash, err := ComputeRoutingTelemetryPayloadHash(batch)
	if err != nil || subtle.ConstantTimeCompare([]byte(expectedHash), []byte(batch.PayloadHash)) != 1 {
		return nil, ErrRoutingTelemetryInvalid
	}
	producedAtSeconds := batch.ProducedAtMs / int64(time.Second/time.Millisecond)
	futureSkewSeconds := int64(routingMetricRollupFutureSkew / time.Second)
	maxProducedBucketTs := int64(math.MaxInt64)
	if producedAtSeconds <= math.MaxInt64-futureSkewSeconds {
		maxProducedBucketTs = producedAtSeconds + futureSkewSeconds
	}
	type metricKey struct {
		memberID         int
		credentialID     int
		modelKey         string
		bucketTs         int64
		snapshotRevision int64
	}
	merged := make(map[metricKey]RoutingMetricRollup, len(batch.Items))
	for index := range batch.Items {
		normalized, err := normalizeRoutingMetricRollupsAt([]RoutingMetricRollup{batch.Items[index]}, now)
		if err != nil {
			return nil, err
		}
		incoming := normalized[0]
		if routingGenerationFencingAvailable(DB) && !validRoutingIdentity(incoming.ChannelGeneration) {
			return nil, fmt.Errorf("%w: channel generation is missing or invalid at item %d", ErrRoutingTelemetryInvalid, index)
		}
		if incoming.BucketTs > maxProducedBucketTs {
			return nil, fmt.Errorf("%w: bucket timestamp is not plausible for produced time at item %d", ErrRoutingTelemetryInvalid, index)
		}
		key := metricKey{
			memberID:         incoming.MemberID,
			credentialID:     incoming.CredentialID,
			modelKey:         incoming.ModelKey,
			bucketTs:         incoming.BucketTs,
			snapshotRevision: incoming.LastSnapshotRevision,
		}
		current, exists := merged[key]
		if !exists {
			merged[key] = incoming
			continue
		}
		if current.ModelName != incoming.ModelName || current.ChannelID != incoming.ChannelID ||
			current.ChannelGeneration != incoming.ChannelGeneration || current.PoolID != incoming.PoolID {
			return nil, fmt.Errorf("%w: conflicting metric identity at item %d", ErrRoutingTelemetryInvalid, index)
		}
		if err := mergeRoutingMetricRollup(&current, &incoming); err != nil {
			return nil, err
		}
		if err := validateRoutingMetricRollup(&current); err != nil {
			return nil, fmt.Errorf("%w: merged item %d: %v", ErrRoutingMetricRollupInvalid, index, err)
		}
		merged[key] = current
	}

	rollups := make([]RoutingMetricRollup, 0, len(merged))
	for _, rollup := range merged {
		rollups = append(rollups, rollup)
	}
	sort.Slice(rollups, func(i, j int) bool {
		left := rollups[i]
		right := rollups[j]
		if left.MemberID != right.MemberID {
			return left.MemberID < right.MemberID
		}
		if left.CredentialID != right.CredentialID {
			return left.CredentialID < right.CredentialID
		}
		if left.ModelKey != right.ModelKey {
			return left.ModelKey < right.ModelKey
		}
		if left.BucketTs != right.BucketTs {
			return left.BucketTs < right.BucketTs
		}
		return left.LastSnapshotRevision < right.LastSnapshotRevision
	})
	return rollups, nil
}

func validRoutingTelemetryNodeID(nodeID string) bool {
	return nodeID != "" && nodeID == strings.TrimSpace(nodeID) && utf8.ValidString(nodeID) &&
		utf8.RuneCountInString(nodeID) <= RoutingTelemetryNodeIDMaxRunes && len(nodeID) <= routingTelemetryNodeIDMaxBytes
}

func validRoutingTelemetryPayloadHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func routingTelemetryNodeKey(nodeID string) string {
	sum := sha256.Sum256([]byte(nodeID))
	return hex.EncodeToString(sum[:])
}
