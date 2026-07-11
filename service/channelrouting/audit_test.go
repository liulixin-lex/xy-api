package channelrouting

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDecisionAuditBufferIsBoundedAndFlushesLatestRetainedRecords(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	firstID := enqueueDecisionForTest(t, 1, 11)
	secondID := enqueueDecisionForTest(t, 2, 12)
	thirdID := enqueueDecisionForTest(t, 3, 13)

	stats := DecisionAuditsStats()
	assert.Equal(t, 2, stats.Entries)
	assert.Equal(t, int64(3), stats.Enqueued)
	assert.Equal(t, int64(1), stats.Dropped)

	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, flushed)
	stats = DecisionAuditsStats()
	assert.Zero(t, stats.Entries)
	assert.Equal(t, int64(2), stats.Flushed)

	var audits []model.RoutingDecisionAudit
	require.NoError(t, db.Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 2)
	assert.NotEqual(t, firstID, audits[0].DecisionID)
	assert.ElementsMatch(t, []string{secondID, thirdID}, []string{audits[0].DecisionID, audits[1].DecisionID})
	assert.NotContains(t, audits[0].CandidatesJSON, "api-key")
}

func TestDecisionAuditFlushFailureRequeuesWithoutLosingOlderRecords(t *testing.T) {
	db := openDecisionAuditTestDB(t, false)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(3)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	firstID := enqueueDecisionForTest(t, 1, 11)
	secondID := enqueueDecisionForTest(t, 2, 12)

	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.Error(t, err)
	assert.Zero(t, flushed)
	assert.Equal(t, 2, DecisionAuditsStats().Entries)

	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	flushed, err = FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, flushed)

	var audits []model.RoutingDecisionAudit
	require.NoError(t, db.Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 2)
	assert.Equal(t, firstID, audits[0].DecisionID)
	assert.Equal(t, secondID, audits[1].DecisionID)
}

func TestEnqueueDecisionSanitizesNonFiniteScoresAndCapsCandidates(t *testing.T) {
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	candidates := make([]DecisionCandidate, MaxDecisionCandidates+1)
	for index := range candidates {
		candidates[index] = DecisionCandidate{
			PoolMemberID: index + 1,
			ChannelID:    index + 1,
			Eligible:     true,
			Score:        math.NaN(),
		}
	}
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default",
		ModelName: "gpt-test", Candidates: candidates,
	})
	require.NoError(t, err)

	batch := decisionBuffer.drain(1)
	require.Len(t, batch, 1)
	assert.Equal(t, MaxDecisionCandidates+1, batch[0].CandidateCount)
	assert.Equal(t, MaxDecisionCandidates+1, batch[0].EligibleCount)
	var payload struct {
		Truncated  bool                `json:"truncated"`
		Candidates []DecisionCandidate `json:"candidates"`
	}
	require.NoError(t, common.UnmarshalJsonStr(batch[0].CandidatesJSON, &payload))
	assert.True(t, payload.Truncated)
	require.Len(t, payload.Candidates, MaxDecisionCandidates)
	assert.Zero(t, payload.Candidates[0].Score)
}

func TestDecisionAuditPayloadFitsCrossDatabaseTextColumn(t *testing.T) {
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	candidates := make([]DecisionCandidate, MaxDecisionCandidates)
	for index := range candidates {
		candidates[index] = DecisionCandidate{
			PoolMemberID:    index + 1,
			ChannelID:       index + 100,
			Eligible:        false,
			ExclusionReason: strings.Repeat("x", MaxDecisionReasonRunes*4),
			Score:           0.25,
		}
	}
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default",
		ModelName: "gpt-test", Candidates: candidates,
	})
	require.NoError(t, err)

	batch := decisionBuffer.drain(1)
	require.Len(t, batch, 1)
	assert.LessOrEqual(t, len(batch[0].CandidatesJSON), MaxDecisionCandidatesJSON)
	var payload struct {
		Candidates []DecisionCandidate `json:"candidates"`
	}
	require.NoError(t, common.UnmarshalJsonStr(batch[0].CandidatesJSON, &payload))
	require.Len(t, payload.Candidates, MaxDecisionCandidates)
	assert.Len(t, []rune(payload.Candidates[0].ExclusionReason), MaxDecisionReasonRunes)
}

func TestDecisionAuditBufferEnforcesTTLAndByteBudget(t *testing.T) {
	now := common.GetTimestamp()
	old := model.RoutingDecisionAudit{
		DecisionID: "old", CandidatesJSON: `{"candidates":[]}`,
		CreatedTime: now - int64(decisionBufferTTL/time.Second) - 1,
	}
	current := model.RoutingDecisionAudit{
		DecisionID: "current", CandidatesJSON: `{"candidates":[]}`, CreatedTime: now,
	}
	ttlBuffer := newAuditBufferWithLimits(3, 1<<20)
	ttlBuffer.enqueue(old)
	ttlBuffer.enqueue(current)
	stats := ttlBuffer.stats()
	assert.Equal(t, 1, stats.Entries)
	assert.Equal(t, int64(1), stats.Expired)
	assert.Positive(t, stats.Bytes)

	byteBuffer := newAuditBufferWithLimits(3, decisionAuditSize(current)+1)
	byteBuffer.enqueue(current)
	newer := current
	newer.DecisionID = "newer"
	byteBuffer.enqueue(newer)
	stats = byteBuffer.stats()
	assert.Equal(t, 1, stats.Entries)
	assert.Equal(t, int64(1), stats.Dropped)
	assert.Equal(t, int64(1), stats.ByteDrops)
	assert.LessOrEqual(t, stats.Bytes, stats.ByteCapacity)
}

func TestDecisionAuditRequeueExpiresRemainingOldHeadBeforePreservingBatch(t *testing.T) {
	now := common.GetTimestamp()
	buffer := newAuditBufferWithLimits(3, 1<<20)
	failed := model.RoutingDecisionAudit{DecisionID: "failed", CreatedTime: now}
	oldRemaining := model.RoutingDecisionAudit{
		DecisionID: "expired", CreatedTime: now - int64(decisionBufferTTL/time.Second) - 1,
	}
	buffer.enqueue(failed)
	buffer.enqueue(oldRemaining)
	batch := buffer.drain(1)
	require.Len(t, batch, 1)

	buffer.requeueFront(batch)

	stats := buffer.stats()
	assert.Equal(t, 1, stats.Entries)
	assert.Equal(t, int64(1), stats.Expired)
	requeued := buffer.drain(1)
	require.Len(t, requeued, 1)
	assert.Equal(t, "failed", requeued[0].DecisionID)
}

func TestEnqueueDecisionTruncatesAlgorithmVersionToDatabaseColumn(t *testing.T) {
	ResetDecisionAuditsForTest(1)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test", AlgorithmVersion: strings.Repeat("v", 256),
	})
	require.NoError(t, err)
	batch := decisionBuffer.drain(1)
	require.Len(t, batch, 1)
	assert.Len(t, []rune(batch[0].AlgorithmVersion), 64)
}

func TestEnqueueDecisionRejectsInvalidUTF8BeforeBuffering(t *testing.T) {
	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	invalid := string([]byte{0xff})
	inputs := []DecisionInput{
		{PoolID: 1, SnapshotRevision: 1, GroupName: invalid, ModelName: "gpt-test"},
		{PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: invalid},
		{PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test", RequestID: invalid},
		{PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test", AlgorithmVersion: invalid},
	}
	for _, input := range inputs {
		_, err := EnqueueDecision(input)
		require.Error(t, err)
	}
	assert.Zero(t, DecisionAuditsStats().Entries)
}

func TestEnqueueDecisionDropsUnknownStableIdentity(t *testing.T) {
	ResetDecisionAuditsForTest(1)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err := EnqueueDecision(DecisionInput{GroupName: "default", ModelName: "gpt-test"})
	require.ErrorIs(t, err, ErrDecisionIdentityUnknown)
	stats := DecisionAuditsStats()
	assert.Zero(t, stats.Entries)
	assert.Equal(t, int64(1), stats.Dropped)
	assert.Equal(t, int64(1), stats.UnknownIdentityDrops)
}

func enqueueDecisionForTest(t *testing.T, actualChannelID int, observedChannelID int) string {
	t.Helper()
	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID:         "request-id",
		PoolID:            7,
		GroupName:         "default",
		ModelName:         "gpt-test",
		SnapshotRevision:  3,
		AlgorithmVersion:  DecisionAlgorithmObserveV1,
		ActualChannelID:   actualChannelID,
		ObservedChannelID: observedChannelID,
		Candidates: []DecisionCandidate{{
			PoolMemberID: 17,
			ChannelID:    observedChannelID,
			Eligible:     true,
			Score:        0.9,
		}},
	})
	require.NoError(t, err)
	return decisionID
}

func openDecisionAuditTestDB(t *testing.T, migrate bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	if migrate {
		require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	}
	return db
}
