package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingTelemetrySQLiteContract(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingTelemetryContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingTelemetryExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			runRoutingTelemetryContract(t, db, test.dbType)
		})
	}
}

func runRoutingTelemetryContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, DB.AutoMigrate(&RoutingMetricRollup{}, &RoutingTelemetryReceipt{}))
	require.NoError(t, DB.AutoMigrate(&RoutingMetricRollup{}, &RoutingTelemetryReceipt{}))
	assert.True(t, DB.Migrator().HasTable(&RoutingTelemetryReceipt{}))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "NodeKey"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "PayloadHash"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "ApplyToken"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "CompactedThrough"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "TombstoneRanges"))
	assert.True(t, DB.Migrator().HasColumn(&RoutingTelemetryReceipt{}, "DeletedAt"))

	batch := telemetryBatch("contract-node", 1, "contract-payload", telemetryRollup(t, 1, 100, 100))
	first, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 1}, first)

	duplicate, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{Duplicate: true}, duplicate)

	var rollup RoutingMetricRollup
	require.NoError(t, DB.First(&rollup).Error)
	assert.Equal(t, int64(1), rollup.RequestCount)
	assert.Equal(t, int64(1), rollup.LatencySampleCount)
	var receipt RoutingTelemetryReceipt
	require.NoError(t, DB.First(&receipt).Error)
	assert.Equal(t, batch.NodeID, receipt.NodeID)
	assert.Equal(t, routingTelemetryNodeKey(batch.NodeID), receipt.NodeKey)
	assert.Equal(t, batch.Sequence, receipt.Sequence)
	assert.Equal(t, batch.PayloadHash, receipt.PayloadHash)
	assert.Len(t, receipt.ApplyToken, 32)
	assert.Equal(t, len(batch.Items), receipt.ItemCount)
	assert.Equal(t, batch.ProducedAtMs, receipt.ProducedAtMs)
	assert.Positive(t, receipt.AppliedAt)
	var state RoutingTelemetryReceipt
	require.NoError(t, DB.Unscoped().Where("node_key = ? AND sequence = ?", receipt.NodeKey, routingTelemetryStateSequence).First(&state).Error)
	assert.True(t, state.DeletedAt.Valid)
	assert.Zero(t, state.CompactedThrough)
}

func TestRoutingTelemetryDetectsSequenceCollisionAndScopesSequenceByNode(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	base := telemetryBatch("node-a", 7, "payload-a", telemetryRollup(t, 1, 100, 100))
	_, err := ApplyRoutingTelemetryBatchContext(context.Background(), base)
	require.NoError(t, err)

	collision := base
	collision.Items = append([]RoutingMetricRollup(nil), base.Items...)
	collision.Items[0].TotalLatencyMs++
	collision.PayloadHash = mustRoutingTelemetryPayloadHash(collision)
	_, err = ApplyRoutingTelemetryBatchContext(context.Background(), collision)
	assert.ErrorIs(t, err, ErrRoutingTelemetrySequenceCollision)

	otherNode := base
	otherNode.NodeID = "node-b"
	otherNode.PayloadHash = mustRoutingTelemetryPayloadHash(otherNode)
	result, err := ApplyRoutingTelemetryBatchContext(context.Background(), otherNode)
	require.NoError(t, err)
	assert.Equal(t, 1, result.AppliedItems)

	var rollup RoutingMetricRollup
	require.NoError(t, DB.First(&rollup).Error)
	assert.Equal(t, int64(2), rollup.RequestCount)
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Equal(t, int64(2), receipts)
}

func TestRoutingTelemetryMergesDuplicateItemsAndDistributionsBeforeApply(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	first := telemetryRollup(t, 1, 100, 100)
	second := telemetryRollup(t, 1, 100, 1_000)
	batch := telemetryBatch("node-a", 1, "duplicate-items", first, second)

	result, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 1}, result)

	var saved RoutingMetricRollup
	require.NoError(t, DB.First(&saved).Error)
	assert.Equal(t, int64(2), saved.RequestCount)
	assert.Equal(t, int64(2), saved.LatencySampleCount)
	assert.Equal(t, int64(1_100), saved.TotalLatencyMs)
	assert.Equal(t, int64(1), saved.LastSnapshotRevision)
	sketch, err := routingdistribution.DecodeDurationSketch(saved.LatencySketch, saved.SketchCodecVersion)
	require.NoError(t, err)
	assert.Equal(t, int64(2), sketch.Count())
	quantile, err := sketch.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, quantile.Known)
	assert.InDelta(t, telemetryDurationQuantile(t, 0.95, 100, 1_000), quantile.ValueMilliseconds, 0.000001)

	var receipt RoutingTelemetryReceipt
	require.NoError(t, DB.First(&receipt).Error)
	assert.Equal(t, 2, receipt.ItemCount, "receipt records the original envelope item count")
}

func TestRoutingTelemetryKeepsSnapshotRevisionsSeparateWithinBatch(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	first := telemetryRollup(t, 1, 100, 100)
	second := telemetryRollup(t, 1, 100, 1_000)
	second.LastSnapshotRevision = 2
	batch := telemetryBatch("node-a", 1, "revision-isolated-items", second, first)

	result, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 2}, result)

	var saved []RoutingMetricRollup
	require.NoError(t, DB.Order("last_snapshot_revision asc").Find(&saved).Error)
	require.Len(t, saved, 2)
	assert.Equal(t, int64(1), saved[0].LastSnapshotRevision)
	assert.Equal(t, int64(100), saved[0].TotalLatencyMs)
	assert.Equal(t, int64(2), saved[1].LastSnapshotRevision)
	assert.Equal(t, int64(1_000), saved[1].TotalLatencyMs)
	assert.Equal(t, int64(1), saved[0].RequestCount)
	assert.Equal(t, int64(1), saved[1].RequestCount)
}

func TestRoutingTelemetryRollsBackReceiptAndAllRollupsOnApplyFailure(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	overflow := telemetryRollup(t, 2, 200, 1)
	overflow.SketchCodecVersion = 0
	overflow.LatencySampleCount = 0
	overflow.LatencySketch = nil
	overflow.TotalLatencyMs = 0
	normalized, err := normalizeRoutingMetricRollups([]RoutingMetricRollup{overflow})
	require.NoError(t, err)
	normalized[0].RequestCount = math.MaxInt64
	require.NoError(t, DB.Create(&normalized[0]).Error)

	batch := telemetryBatch(
		"node-a",
		1,
		"overflow",
		telemetryRollup(t, 1, 100, 100),
		telemetryRollup(t, 2, 200, 200),
	)
	_, err = ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	assert.ErrorIs(t, err, ErrRoutingMetricRollupOverflow)

	var firstCount int64
	require.NoError(t, DB.Model(&RoutingMetricRollup{}).Where("member_id = ?", 1).Count(&firstCount).Error)
	assert.Zero(t, firstCount, "the first item update must roll back when a later item fails")
	var retained RoutingMetricRollup
	require.NoError(t, DB.Where("member_id = ?", 2).First(&retained).Error)
	assert.Equal(t, int64(math.MaxInt64), retained.RequestCount)
	var receiptCount int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receiptCount).Error)
	assert.Zero(t, receiptCount)
}

func TestRoutingTelemetryRejectsInvalidSketchWithoutReceipt(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	rollup := telemetryRollup(t, 1, 100, 100)
	rollup.LatencySketch = []byte("not-a-sketch")
	batch := telemetryBatch("node-a", 1, "bad-sketch", rollup)

	_, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	assert.ErrorIs(t, err, ErrRoutingMetricRollupInvalid)
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Zero(t, receipts)
	var rollups int64
	require.NoError(t, DB.Model(&RoutingMetricRollup{}).Count(&rollups).Error)
	assert.Zero(t, rollups)
}

func TestRoutingTelemetryConcurrentNodesApplyToSameEmptyRollup(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	batches := []RoutingTelemetryBatch{
		telemetryBatch("node-a", 1, "concurrent-node-a", telemetryRollup(t, 1, 100, 100)),
		telemetryBatch("node-b", 1, "concurrent-node-b", telemetryRollup(t, 1, 100, 101)),
	}
	var wait sync.WaitGroup
	for index := range batches {
		wait.Add(1)
		go func(batch RoutingTelemetryBatch) {
			defer wait.Done()
			<-start
			_, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
			errorsChannel <- err
		}(batches[index])
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}

	var saved RoutingMetricRollup
	require.NoError(t, DB.First(&saved).Error)
	assert.Equal(t, int64(2), saved.RequestCount)
	assert.Equal(t, int64(2), saved.LatencySampleCount)
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Equal(t, int64(2), receipts)
}

func TestRoutingTelemetryValidatesEnvelopeIdentity(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	valid := telemetryBatch("node-a", 1, "valid", telemetryRollup(t, 1, 100, 100))
	tests := []struct {
		name   string
		mutate func(*RoutingTelemetryBatch)
	}{
		{name: "empty node", mutate: func(batch *RoutingTelemetryBatch) { batch.NodeID = "" }},
		{name: "invalid utf8 node", mutate: func(batch *RoutingTelemetryBatch) { batch.NodeID = string([]byte{0xff}) }},
		{name: "long node", mutate: func(batch *RoutingTelemetryBatch) {
			batch.NodeID = strings.Repeat("n", RoutingTelemetryNodeIDMaxRunes+1)
		}},
		{name: "zero sequence", mutate: func(batch *RoutingTelemetryBatch) { batch.Sequence = 0 }},
		{name: "short hash", mutate: func(batch *RoutingTelemetryBatch) { batch.PayloadHash = "abc" }},
		{name: "uppercase hash", mutate: func(batch *RoutingTelemetryBatch) { batch.PayloadHash = strings.Repeat("A", 64) }},
		{name: "non hex hash", mutate: func(batch *RoutingTelemetryBatch) { batch.PayloadHash = strings.Repeat("z", 64) }},
		{name: "zero produced time", mutate: func(batch *RoutingTelemetryBatch) { batch.ProducedAtMs = 0 }},
		{name: "empty items", mutate: func(batch *RoutingTelemetryBatch) { batch.Items = nil }},
		{name: "too many items", mutate: func(batch *RoutingTelemetryBatch) {
			batch.Items = make([]RoutingMetricRollup, RoutingTelemetryMaxItems+1)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := valid
			test.mutate(&batch)
			_, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
			assert.ErrorIs(t, err, ErrRoutingTelemetryInvalid)
		})
	}
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Zero(t, receipts)
}

func TestRoutingTelemetryRejectsPayloadTamperingBeforeCreatingReceipt(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	batch := telemetryBatch("node-a", 1, "valid", telemetryRollup(t, 1, 100, 100))
	batch.Items[0].TotalLatencyMs++

	_, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	assert.ErrorIs(t, err, ErrRoutingTelemetryInvalid)
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Zero(t, receipts)
}

func TestDeleteRoutingTelemetryReceiptsBeforeContext(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	receipts := []RoutingTelemetryReceipt{
		{NodeID: "node-a", NodeKey: routingTelemetryNodeKey("node-a"), Sequence: 1, PayloadHash: telemetryPayloadHash("a"), ItemCount: 1, ProducedAtMs: 1, AppliedAt: 10},
		{NodeID: "node-a", NodeKey: routingTelemetryNodeKey("node-a"), Sequence: 2, PayloadHash: telemetryPayloadHash("b"), ItemCount: 1, ProducedAtMs: 2, AppliedAt: 20},
		{NodeID: "node-a", NodeKey: routingTelemetryNodeKey("node-a"), Sequence: 3, PayloadHash: telemetryPayloadHash("c"), ItemCount: 1, ProducedAtMs: 3, AppliedAt: 30},
	}
	require.NoError(t, DB.Create(&receipts).Error)

	deleted, err := DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 25)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	var retained []RoutingTelemetryReceipt
	require.NoError(t, DB.Order("sequence asc").Find(&retained).Error)
	require.Len(t, retained, 1)
	assert.Equal(t, int64(3), retained[0].Sequence)
}

func TestRoutingTelemetryCompactedReceiptCannotReplayAfterCleanup(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	batch := telemetryBatch("node-compact", 1, "compact", telemetryRollup(t, 1, 100, 100))
	result, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 1}, result)
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Where("sequence = ?", batch.Sequence).Update("applied_at", 10).Error)

	deleted, err := DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var receipts int64
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Count(&receipts).Error)
	assert.Zero(t, receipts)

	duplicate, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{Duplicate: true}, duplicate)
	var rollup RoutingMetricRollup
	require.NoError(t, DB.First(&rollup).Error)
	assert.Equal(t, int64(1), rollup.RequestCount)

	var state RoutingTelemetryReceipt
	require.NoError(t, DB.Unscoped().Where(
		"node_key = ? AND sequence = ?", routingTelemetryNodeKey(batch.NodeID), routingTelemetryStateSequence,
	).First(&state).Error)
	assert.Equal(t, int64(1), state.CompactedThrough)
	ranges, err := routingTelemetryStateRanges(&state)
	require.NoError(t, err)
	assert.Empty(t, ranges)
}

func TestRoutingTelemetryCompactionPreservesOutOfOrderGaps(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	first := telemetryBatch("node-gap", 1, "gap-1", telemetryRollup(t, 1, 100, 100))
	third := telemetryBatch("node-gap", 3, "gap-3", telemetryRollup(t, 1, 100, 300))
	_, err := ApplyRoutingTelemetryBatchContext(context.Background(), first)
	require.NoError(t, err)
	_, err = ApplyRoutingTelemetryBatchContext(context.Background(), third)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Where("sequence IN ?", []int64{1, 3}).Update("applied_at", 10).Error)

	deleted, err := DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	var state RoutingTelemetryReceipt
	require.NoError(t, DB.Unscoped().Where(
		"node_key = ? AND sequence = ?", routingTelemetryNodeKey(first.NodeID), routingTelemetryStateSequence,
	).First(&state).Error)
	assert.Equal(t, int64(1), state.CompactedThrough)
	ranges, err := routingTelemetryStateRanges(&state)
	require.NoError(t, err)
	assert.Equal(t, []routingTelemetrySequenceRange{{Start: 3, End: 3}}, ranges)

	second := telemetryBatch("node-gap", 2, "gap-2", telemetryRollup(t, 1, 100, 200))
	secondResult, err := ApplyRoutingTelemetryBatchContext(context.Background(), second)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 1}, secondResult)
	thirdDuplicate, err := ApplyRoutingTelemetryBatchContext(context.Background(), third)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{Duplicate: true}, thirdDuplicate)

	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Where("sequence = ?", 2).Update("applied_at", 10).Error)
	deleted, err = DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	require.NoError(t, DB.Unscoped().Where("id = ?", state.ID).First(&state).Error)
	assert.Equal(t, int64(3), state.CompactedThrough)
	ranges, err = routingTelemetryStateRanges(&state)
	require.NoError(t, err)
	assert.Empty(t, ranges)

	var rollup RoutingMetricRollup
	require.NoError(t, DB.First(&rollup).Error)
	assert.Equal(t, int64(3), rollup.RequestCount)
}

func TestRoutingTelemetryTombstonesUseCompactBoundedEncoding(t *testing.T) {
	state := RoutingTelemetryReceipt{TombstoneRanges: routingTelemetryTombstonePrefix}
	sequences := make([]int64, routingTelemetryMaxTombstoneRanges)
	for index := range sequences {
		sequences[index] = int64((index + 1) * 2)
	}

	require.NoError(t, addRoutingTelemetryStateSequences(&state, sequences))
	assert.LessOrEqual(t, len(state.TombstoneRanges), routingTelemetryTombstoneMaxBytes)
	assert.True(t, strings.HasPrefix(state.TombstoneRanges, routingTelemetryTombstonePrefix))
	ranges, err := routingTelemetryStateRanges(&state)
	require.NoError(t, err)
	require.Len(t, ranges, routingTelemetryMaxTombstoneRanges)
	assert.Equal(t, routingTelemetrySequenceRange{Start: 2, End: 2}, ranges[0])
	assert.Equal(t, routingTelemetrySequenceRange{Start: int64(routingTelemetryMaxTombstoneRanges * 2), End: int64(routingTelemetryMaxTombstoneRanges * 2)}, ranges[len(ranges)-1])
}

func TestRoutingTelemetryTombstoneOverflowExpiresRetentionGaps(t *testing.T) {
	const step = int64(1_000_000_000_000_000)
	ranges := make([]routingTelemetrySequenceRange, routingTelemetryMaxTombstoneRanges)
	end := int64(0)
	for index := range ranges {
		start := end + step
		end = start + step - 2
		ranges[index] = routingTelemetrySequenceRange{Start: start, End: end}
	}
	legacyJSON, err := common.Marshal(ranges)
	require.NoError(t, err)
	state := RoutingTelemetryReceipt{TombstoneRanges: string(legacyJSON)}

	require.NoError(t, addRoutingTelemetryStateSequences(&state, nil))
	assert.Equal(t, end, state.CompactedThrough)
	assert.Equal(t, routingTelemetryTombstonePrefix, state.TombstoneRanges)
	decoded, err := routingTelemetryStateRanges(&state)
	require.NoError(t, err)
	assert.Empty(t, decoded)
}

func TestRoutingTelemetryCompactionStateExpiresAfterIdempotencyWindow(t *testing.T) {
	setupRoutingTelemetrySQLite(t)
	batch := telemetryBatch("node-expired-state", 1, "expired-state", telemetryRollup(t, 1, 100, 100))
	_, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&RoutingTelemetryReceipt{}).Where("sequence = ?", batch.Sequence).Update("applied_at", 10).Error)

	deleted, err := DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	nodeKey := routingTelemetryNodeKey(batch.NodeID)
	require.NoError(t, DB.Unscoped().Model(&RoutingTelemetryReceipt{}).
		Where("node_key = ? AND sequence = ?", nodeKey, routingTelemetryStateSequence).
		Update("applied_at", 10).Error)

	deleted, err = DeleteRoutingTelemetryReceiptsBeforeContext(context.Background(), 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var states int64
	require.NoError(t, DB.Unscoped().Model(&RoutingTelemetryReceipt{}).
		Where("node_key = ? AND sequence = ?", nodeKey, routingTelemetryStateSequence).
		Count(&states).Error)
	assert.Zero(t, states)

	replayed, err := ApplyRoutingTelemetryBatchContext(context.Background(), batch)
	require.NoError(t, err)
	assert.Equal(t, RoutingTelemetryApplyResult{AppliedItems: 1}, replayed)
	var rollup RoutingMetricRollup
	require.NoError(t, DB.First(&rollup).Error)
	assert.Equal(t, int64(2), rollup.RequestCount, "idempotency is guaranteed only through the retained state window")
}

func TestRoutingTelemetryIngressTimeBoundsUseProducedAndServerTime(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	valid := telemetryBatch("node-time", 1, "time", telemetryRollup(t, 1, now.Unix()-3600, 100))
	valid.ProducedAtMs = now.UnixMilli()
	valid.PayloadHash = mustRoutingTelemetryPayloadHash(valid)
	_, err := normalizeRoutingTelemetryBatchAt(valid, now)
	require.NoError(t, err)

	futureProduced := valid
	futureProduced.ProducedAtMs = now.UnixMilli() + int64(routingMetricRollupFutureSkew/time.Millisecond) + 1
	futureProduced.PayloadHash = mustRoutingTelemetryPayloadHash(futureProduced)
	_, err = normalizeRoutingTelemetryBatchAt(futureProduced, now)
	assert.ErrorIs(t, err, ErrRoutingTelemetryInvalid)

	futureBucket := valid
	futureBucket.ProducedAtMs = now.Add(-5 * time.Minute).UnixMilli()
	futureBucket.Items = append([]RoutingMetricRollup(nil), valid.Items...)
	futureBucket.Items[0].BucketTs = futureBucket.ProducedAtMs/int64(time.Second/time.Millisecond) + int64(routingMetricRollupFutureSkew/time.Second) + 1
	futureBucket.PayloadHash = mustRoutingTelemetryPayloadHash(futureBucket)
	_, err = normalizeRoutingTelemetryBatchAt(futureBucket, now)
	assert.ErrorIs(t, err, ErrRoutingTelemetryInvalid)

}

func setupRoutingTelemetrySQLite(t *testing.T) {
	t.Helper()
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingMetricRollup{}, &RoutingTelemetryReceipt{}))
}

func telemetryBatch(nodeID string, sequence int64, payload string, items ...RoutingMetricRollup) RoutingTelemetryBatch {
	batch := RoutingTelemetryBatch{
		NodeID:       nodeID,
		Sequence:     sequence,
		ProducedAtMs: 1_000,
		Items:        items,
	}
	batch.PayloadHash = mustRoutingTelemetryPayloadHash(batch)
	return batch
}

func mustRoutingTelemetryPayloadHash(batch RoutingTelemetryBatch) string {
	hash, err := ComputeRoutingTelemetryPayloadHash(batch)
	if err != nil {
		panic(err)
	}
	return hash
}

func telemetryRollup(t *testing.T, memberID int, bucketTs int64, latencyMs int64) RoutingMetricRollup {
	t.Helper()
	return RoutingMetricRollup{
		MemberID:                memberID,
		CredentialID:            memberID,
		ModelName:               "model-test",
		BucketTs:                bucketTs,
		ChannelID:               memberID,
		PoolID:                  1,
		LastSnapshotRevision:    1,
		SketchCodecVersion:      routingdistribution.SketchCodecVersion,
		LatencySampleCount:      1,
		LatencySketch:           telemetryDurationSketch(t, latencyMs),
		RequestCount:            1,
		SuccessCount:            1,
		ReliabilityRequestCount: 1,
		TotalLatencyMs:          latencyMs,
	}
}

func telemetryDurationSketch(t *testing.T, values ...int64) []byte {
	t.Helper()
	sketch := routingdistribution.NewDurationSketch()
	for _, value := range values {
		_, err := sketch.AddMillis(value)
		require.NoError(t, err)
	}
	data, err := sketch.MarshalBinary()
	require.NoError(t, err)
	return data
}

func telemetryDurationQuantile(t *testing.T, quantile float64, values ...int64) float64 {
	t.Helper()
	sketch, err := routingdistribution.DecodeDurationSketch(telemetryDurationSketch(t, values...), routingdistribution.SketchCodecVersion)
	require.NoError(t, err)
	result, err := sketch.Quantile(quantile)
	require.NoError(t, err)
	require.True(t, result.Known)
	return result.ValueMilliseconds
}

func telemetryPayloadHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
