package channelrouting

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"

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

func TestDecisionAuditPersistsVersionedCostEstimatesWithoutRequestPayload(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	actual := &ShadowCostInput{
		Known: true, Cost: 0.003, WorstCaseKnown: true, WorstCaseCost: 0.012,
		EffectiveKnown: true, EffectiveCost: 0.004, Currency: "USD", Unit: "mixed",
		PricingBasis: "token", PricingHash: strings.Repeat("a", 64), PricingVersion: "upstream-v3",
		ObservedTime: 1_700_000_000, EffectiveTime: 1_699_999_900, ExpiresTime: 1_700_003_600,
		VersionConfidence: model.RoutingCostConfidenceExact, Freshness: model.RoutingCostFreshnessFresh,
		SourceSyncStatus:  model.RoutingUpstreamSyncStatusSuccess,
		AccountSourceType: model.RoutingUpstreamTypeNewAPI, AccountKeyHash: strings.Repeat("b", 64),
		ConfidenceScore: 0.95, FreshnessScore: 0.9,
		ExpectedBreakdown:    model.RoutingCostBreakdown{Input: 0.001, Output: 0.002, Total: 0.003},
		WorstSingleBreakdown: model.RoutingCostBreakdown{Input: 0.002, Output: 0.004, Total: 0.006},
		UpdatedUnix:          1_700_000_000,
	}
	observed := *actual
	observed.Cost = 0.0025
	observed.EffectiveCost = 0.0035
	observed.PricingHash = strings.Repeat("c", 64)
	observed.ExpectedBreakdown = model.RoutingCostBreakdown{Input: 0.001, Output: 0.0015, Total: 0.0025}

	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID: "cost-audit", PoolID: 7, GroupName: "vip", ModelName: "gpt-cost",
		SnapshotRevision: 19, AlgorithmVersion: DecisionAlgorithmObserveV1,
		ActualChannelID: 101, ObservedChannelID: 102,
		ActualCostKnown: true, ActualExpectedCost: actual.Cost,
		ObservedCostKnown: true, ObservedExpectedCost: observed.Cost,
		ActualCostEstimate: actual, ObservedCostEstimate: &observed,
	})
	require.NoError(t, err)
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)

	var stored model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", decisionID).First(&stored).Error)
	assert.Empty(t, stored.RequestProfileJSON)
	assert.NotContains(t, stored.ActualCostEstimateJSON, "authorization")
	assert.NotContains(t, stored.ActualCostEstimateJSON, "private-request-body")
	var decoded ShadowCostInput
	require.NoError(t, common.UnmarshalJsonStr(stored.ActualCostEstimateJSON, &decoded))
	assert.Equal(t, actual.PricingHash, decoded.PricingHash)
	assert.Equal(t, actual.AccountKeyHash, decoded.AccountKeyHash)
	assert.Equal(t, actual.ExpectedBreakdown, decoded.ExpectedBreakdown)
	assert.Equal(t, observed.Cost-actual.Cost, stored.ExpectedCostDelta)
}

func TestDecisionAuditStatsExposeOldestBufferedAge(t *testing.T) {
	buffer := newAuditBuffer(2)
	createdTime := common.GetTimestamp() - 10
	buffer.enqueue(model.RoutingDecisionAudit{DecisionID: "oldest", CreatedTime: createdTime})
	buffer.enqueue(model.RoutingDecisionAudit{DecisionID: "newest", CreatedTime: createdTime + 5})

	stats := buffer.stats()
	assert.Equal(t, createdTime, stats.OldestCreatedTime)
	assert.GreaterOrEqual(t, stats.OldestAgeSec, int64(10))
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

func TestDecisionAuditBatchFlushHonorsInvocationBudget(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(model.RoutingDecisionAuditMaxBatch + 1)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	for index := 0; index < model.RoutingDecisionAuditMaxBatch+1; index++ {
		enqueueDecisionForTest(t, index+1, index+1)
	}

	flushed, err := flushDecisionAuditBatchesContext(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingDecisionAuditMaxBatch, flushed)
	assert.Equal(t, 1, DecisionAuditsStats().Entries)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingDecisionAuditMaxBatch), persisted)
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

func TestEnqueueDecisionSummarizesExclusionsBeforeCandidateTruncation(t *testing.T) {
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	candidates := make([]DecisionCandidate, MaxDecisionCandidates+3)
	for index := range candidates {
		candidates[index] = DecisionCandidate{PoolMemberID: index + 1, ChannelID: index + 1, Eligible: true}
	}
	candidates[MaxDecisionCandidates].Eligible = false
	candidates[MaxDecisionCandidates].ExclusionReason = ExclusionReasonRequestFailed
	candidates[MaxDecisionCandidates+1].Eligible = false
	candidates[MaxDecisionCandidates+1].ExclusionReason = ExclusionReasonRequestFailed
	candidates[MaxDecisionCandidates+2].Eligible = false
	candidates[MaxDecisionCandidates+2].ExclusionReason = ExclusionReasonLocalCapacity

	_, err := EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test", Candidates: candidates,
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)
	assert.Equal(t, len(candidates), audits[0].CandidateCount)
	assert.Equal(t, MaxDecisionCandidates, audits[0].EligibleCount)
	var summary model.RoutingDecisionExclusionSummary
	require.NoError(t, common.UnmarshalJsonStr(audits[0].ExclusionSummaryJSON, &summary))
	assert.Equal(t, model.RoutingDecisionExclusionSummary{
		ExcludedCount: 3,
		Reasons: []model.RoutingDecisionExclusionCount{
			{Reason: ExclusionReasonLocalCapacity, Count: 1},
			{Reason: ExclusionReasonRequestFailed, Count: 2},
		},
	}, summary)
	assert.NotContains(t, audits[0].CandidatesJSON, ExclusionReasonRequestFailed)
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

func TestMaximumReplayPayloadProducesPersistedReplayableAudit(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	requestID := "max-pool-replay"
	profile, err := NewRequestProfile(
		"/v1/chat/completions", "default", "gpt-test", true, 0, 1_000, 200,
	)
	require.NoError(t, err)
	seed, err := DeriveShadowSeed(requestID, 7, 0)
	require.NoError(t, err)
	candidates := make([]ShadowCandidateInput, MaxDecisionCandidates)
	for index := range candidates {
		candidates[index] = ShadowCandidateInput{
			PoolMemberID: index + 1,
			ChannelID:    index + 1,
			Priority:     math.MaxInt64,
			Weight:       math.MaxUint,
			Metric: &ShadowMetricInput{
				RequestCount: math.MaxInt64, SuccessCount: math.MaxInt64,
				ReliabilityRequestCount: math.MaxInt64, ReliabilityFailureCount: math.MaxInt64,
				P95LatencyMs: math.MaxFloat64, P95TTFTMs: math.MaxFloat64,
				OutputTokensPerSecond: math.MaxFloat64, Inflight: math.MaxInt64,
			},
			Cost: &ShadowReplayCostInput{Known: true, Cost: math.MaxFloat64, UpdatedUnix: math.MaxInt64},
			Breaker: &ShadowBreakerInput{
				State: strings.Repeat("s", 32), Reason: strings.Repeat("r", 64),
				CooldownUntilUnix: math.MaxInt64, HalfOpenInflight: math.MaxInt64, UpdatedUnix: math.MaxInt64,
			},
			Capacity: &ShadowCapacityInput{
				SourceStatusCode: 599, CooldownUntilUnixMilli: math.MaxInt64, UpdatedUnixMilli: math.MaxInt64,
			},
		}
	}
	input, err := BuildShadowReplayInput(1, 7, 3, strings.Repeat("a", 64), profile, routingselector.Settings{
		WeightAvailability: 1, AvailabilityFloor: 0, MinVolume: 1, TopK: 1,
		MaxEjectedPct: 50, HalfOpenProbes: 1, SnapshotStaleSec: 1_800,
		NowUnix: 1_000, NowUnixMilli: 1_000_000, RandomSeed: seed, PreferTTFT: true,
	}, candidates)
	require.NoError(t, err)
	replayJSON, err := common.Marshal(input)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(replayJSON), MaxDecisionReplayJSON)

	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID: requestID, PoolID: 1, GroupName: "default", ModelName: "gpt-test",
		SnapshotRevision: 7, AlgorithmVersion: DecisionAlgorithmShadowV1, IsStream: true,
		ObservedChannelID: replay.SelectedChannelID, FilteredOpen: replay.FilteredOpen,
		FilteredCapacity: replay.FilteredCapacity, BreakerBypassed: replay.BreakerBypassed,
		Candidates: replay.Candidates, ReplayInput: &input,
		DifferenceType: ClassifyShadowDifference(0, replay),
	})
	require.NoError(t, err)
	_, err = FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	var audit model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", decisionID).First(&audit).Error)
	assert.True(t, audit.Replayable)
	assert.Equal(t, MaxDecisionCandidates, audit.CandidateCount)
	_, err = ReplayDecisionAudit(audit)
	require.NoError(t, err)
}

func TestShadowReplaySupportsPoolBeyondAuditCandidateLimit(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	requestID := "large-pool-replay"
	profile, err := NewRequestProfile("/v1/chat/completions", "default", "gpt-test", false, 0, 100, 20)
	require.NoError(t, err)
	seed, err := DeriveShadowSeed(requestID, 8, 0)
	require.NoError(t, err)
	candidates := make([]ShadowCandidateInput, MaxDecisionCandidates+1)
	for index := range candidates {
		candidates[index] = ShadowCandidateInput{
			PoolMemberID: index + 1,
			ChannelID:    index + 1,
			Weight:       1,
		}
	}
	input, err := BuildShadowReplayInput(1, 8, 4, strings.Repeat("b", 64), profile, routingselector.Settings{
		WeightAvailability: 1, TopK: 1, MaxEjectedPct: 50, HalfOpenProbes: 1,
		SnapshotStaleSec: 1_800, NowUnix: 2_000, NowUnixMilli: 2_000_000, RandomSeed: seed,
	}, candidates)
	require.NoError(t, err)
	replayJSON, err := common.Marshal(input)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(replayJSON), MaxDecisionReplayJSON)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	require.Len(t, replay.Candidates, MaxDecisionCandidates+1)

	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID: requestID, PoolID: 1, GroupName: "default", ModelName: "gpt-test",
		SnapshotRevision: 8, AlgorithmVersion: DecisionAlgorithmShadowV1,
		ObservedChannelID: replay.SelectedChannelID, FilteredOpen: replay.FilteredOpen,
		FilteredCapacity: replay.FilteredCapacity, BreakerBypassed: replay.BreakerBypassed,
		Candidates: replay.Candidates, ReplayInput: &input,
		DifferenceType: ClassifyShadowDifference(0, replay),
	})
	require.NoError(t, err)
	_, err = FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	var audit model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", decisionID).First(&audit).Error)
	assert.True(t, audit.Replayable)
	assert.Equal(t, MaxDecisionCandidates+1, audit.CandidateCount)
	var payload struct {
		Truncated  bool                `json:"truncated"`
		Candidates []DecisionCandidate `json:"candidates"`
	}
	require.NoError(t, common.UnmarshalJsonStr(audit.CandidatesJSON, &payload))
	assert.True(t, payload.Truncated)
	require.Len(t, payload.Candidates, MaxDecisionCandidates)
	_, err = ReplayDecisionAudit(audit)
	require.NoError(t, err)
}

func TestOversizedShadowReplayPersistsChunksAndReplaysExactly(t *testing.T) {
	db := openDecisionAuditTestDB(t, true)
	withSnapshotTestDB(t, db)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	chunkBatches := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:replay_chunk_batches", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.RoutingDecisionReplayChunk); ok {
			chunkBatches++
		}
	}))

	requestID := "oversized-pool-replay"
	profile, err := NewRequestProfile("/v1/chat/completions", "default", "gpt-test", false, 0, 100, 20)
	require.NoError(t, err)
	seed, err := DeriveShadowSeed(requestID, 9, 0)
	require.NoError(t, err)
	candidates := make([]ShadowCandidateInput, model.RoutingPolicyMaxMembersPerPool)
	for index := range candidates {
		candidates[index] = ShadowCandidateInput{
			PoolMemberID: index + 1,
			ChannelID:    index + 1,
			Weight:       1,
			Metric: &ShadowMetricInput{
				RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100,
				ReliabilityFailureCount: 1, P95LatencyMs: 50, P95TTFTMs: 10,
				OutputTokensPerSecond: 100,
			},
		}
	}
	input, err := BuildShadowReplayInput(1, 9, 5, strings.Repeat("c", 64), profile, routingselector.Settings{
		WeightAvailability: 1, TopK: 1, MaxEjectedPct: 50, HalfOpenProbes: 1,
		SnapshotStaleSec: 1_800, NowUnix: 3_000, NowUnixMilli: 3_000_000, RandomSeed: seed,
	}, candidates)
	require.NoError(t, err)
	replayJSON, err := common.Marshal(input)
	require.NoError(t, err)
	assert.Greater(t, len(replayJSON), MaxDecisionReplayJSON)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	require.Len(t, replay.Candidates, len(candidates))

	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID: requestID, PoolID: 1, GroupName: "default", ModelName: "gpt-test",
		SnapshotRevision: 9, AlgorithmVersion: DecisionAlgorithmShadowV1,
		ObservedChannelID: replay.SelectedChannelID, FilteredOpen: replay.FilteredOpen,
		FilteredCapacity: replay.FilteredCapacity, BreakerBypassed: replay.BreakerBypassed,
		Candidates: replay.Candidates, ReplayInput: &input,
		DifferenceType: ClassifyShadowDifference(0, replay),
	})
	require.NoError(t, err)
	_, err = FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	var audit model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", decisionID).First(&audit).Error)
	assert.True(t, audit.Replayable)
	assert.Empty(t, audit.ReplayInputJSON)
	assert.Positive(t, audit.ReplayChunkCount)
	assert.Equal(t, len(replayJSON), audit.ReplayInputBytes)
	assert.Equal(t, len(candidates), audit.CandidateCount)
	var chunks []model.RoutingDecisionReplayChunk
	require.NoError(t, db.Where("decision_id = ?", decisionID).Order("chunk_index asc").Find(&chunks).Error)
	require.Len(t, chunks, audit.ReplayChunkCount)
	assert.Greater(t, chunkBatches, 1, "maximum legal pool replay must use byte-bounded database batches")
	for index := range chunks {
		assert.LessOrEqual(t, chunks[index].PayloadBytes, model.RoutingDecisionReplayChunkMaxBytes)
	}
	reassembled, err := model.LoadRoutingDecisionReplayInputContext(context.Background(), audit)
	require.NoError(t, err)
	assert.JSONEq(t, string(replayJSON), reassembled)
	_, err = ReplayDecisionAudit(audit)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoutingDecisionReplayChunk{}).
		Where("decision_id = ? AND chunk_index = ?", decisionID, 0).
		Update("payload", chunks[0].Payload+"x").Error)
	_, err = model.LoadRoutingDecisionReplayInputContext(context.Background(), audit)
	assert.ErrorIs(t, err, model.ErrRoutingDecisionAuditInvalid)
}

func TestDecisionReplayChunksExternalDatabaseCompatibility(t *testing.T) {
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
			db := openSnapshotExternalTestDB(t, test.dbType, dsn)
			withSnapshotTestDBType(t, db, test.dbType)
			require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}, &model.RoutingDecisionReplayChunk{}))

			decisionID := "external-chunk-replay"
			snapshotHash := strings.Repeat("a", 64)
			policyHash := strings.Repeat("b", 64)
			payload := []byte(`{"snapshot_hash":"` + snapshotHash + `","policy_hash":"` + policyHash + `","payload":"` +
				strings.Repeat("x", model.RoutingDecisionReplayChunkMaxBytes+1) + `"}`)
			totalHash, chunks, err := model.NewRoutingDecisionReplayChunks(decisionID, payload, 10)
			require.NoError(t, err)
			require.Greater(t, len(chunks), 1)
			audit := model.RoutingDecisionAudit{
				DecisionID: decisionID, Replayable: true, SnapshotHash: snapshotHash, PolicyHash: policyHash,
				ReplayInputHash: totalHash, ReplayInputBytes: len(payload), ReplayChunkCount: len(chunks), CreatedTime: 10,
			}
			require.NoError(t, db.Create(&audit).Error)
			require.NoError(t, db.Create(&chunks).Error)

			reassembled, err := model.LoadRoutingDecisionReplayInputContext(context.Background(), audit)
			require.NoError(t, err)
			assert.Equal(t, string(payload), reassembled)
			deleted, err := model.DeleteRoutingDecisionAuditsBeforeContext(context.Background(), 11)
			require.NoError(t, err)
			assert.Equal(t, int64(1), deleted)
			var chunkCount int64
			require.NoError(t, db.Model(&model.RoutingDecisionReplayChunk{}).Count(&chunkCount).Error)
			assert.Zero(t, chunkCount)
		})
	}
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
		require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}, &model.RoutingDecisionReplayChunk{}))
	}
	return db
}
