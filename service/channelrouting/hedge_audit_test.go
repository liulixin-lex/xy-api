package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHedgeAttemptAuditBufferDoesNotTouchDatabaseUntilBoundedFlush(t *testing.T) {
	buffer := newHedgeAttemptAuditBuffer(2, 1<<20)
	var startCalls int
	var completeCalls int
	previousStart := hedgeAuditStartPersistence
	previousComplete := hedgeAuditCompletePersistence
	hedgeAuditStartPersistence = func(ctx context.Context, spec model.RoutingHedgeAttemptStartSpec) (model.RoutingHedgeAttemptAudit, error) {
		startCalls++
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), hedgeAuditPersistTimeout)
		return model.RoutingHedgeAttemptAudit{ID: 17, AttemptKey: spec.AttemptKey}, nil
	}
	hedgeAuditCompletePersistence = func(
		ctx context.Context,
		id int64,
		spec model.RoutingHedgeAttemptCompleteSpec,
	) (model.RoutingHedgeAttemptAudit, error) {
		completeCalls++
		_, ok := ctx.Deadline()
		require.True(t, ok)
		assert.Equal(t, int64(17), id)
		assert.Equal(t, model.RoutingHedgeAttemptResultSuccess, spec.Result)
		return model.RoutingHedgeAttemptAudit{ID: id}, nil
	}
	t.Cleanup(func() {
		hedgeAuditStartPersistence = previousStart
		hedgeAuditCompletePersistence = previousComplete
	})

	reservation, err := buffer.reserve(hedgeAttemptAuditSpecForTest("deferred"))
	require.NoError(t, err)
	require.NoError(t, reservation.Complete(model.RoutingHedgeAttemptCompleteSpec{
		Result: model.RoutingHedgeAttemptResultSuccess, Winner: true, HTTPStatus: 200, UpstreamSent: true,
		CompletedTimeMs: 1_050,
	}))
	assert.Zero(t, startCalls)
	assert.Zero(t, completeCalls)
	assert.Equal(t, 1, buffer.stats().Entries)

	flushed, err := buffer.flush(context.Background(), hedgeAuditFlushBatch)
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	assert.Equal(t, 1, startCalls)
	assert.Equal(t, 1, completeCalls)
	assert.Zero(t, buffer.stats().Entries)
}

func TestHedgeAttemptAuditBufferRetainsCompletedAttemptAcrossPersistenceFailure(t *testing.T) {
	buffer := newHedgeAttemptAuditBuffer(1, 1<<20)
	previousStart := hedgeAuditStartPersistence
	previousComplete := hedgeAuditCompletePersistence
	persistErr := errors.New("audit database unavailable")
	hedgeAuditStartPersistence = func(context.Context, model.RoutingHedgeAttemptStartSpec) (model.RoutingHedgeAttemptAudit, error) {
		return model.RoutingHedgeAttemptAudit{}, persistErr
	}
	t.Cleanup(func() {
		hedgeAuditStartPersistence = previousStart
		hedgeAuditCompletePersistence = previousComplete
	})

	reservation, err := buffer.reserve(hedgeAttemptAuditSpecForTest("retry"))
	require.NoError(t, err)
	require.NoError(t, reservation.Complete(model.RoutingHedgeAttemptCompleteSpec{
		Result: model.RoutingHedgeAttemptResultUpstreamError, HTTPStatus: 502, CompletedTimeMs: 1_050,
	}))
	_, err = buffer.reserve(hedgeAttemptAuditSpecForTest("rejected"))
	assert.ErrorIs(t, err, ErrHedgeAuditBufferFull)

	flushed, err := buffer.flush(context.Background(), hedgeAuditFlushBatch)
	assert.Zero(t, flushed)
	assert.ErrorIs(t, err, persistErr)
	stats := buffer.stats()
	assert.Equal(t, 1, stats.Entries)
	assert.Equal(t, 1, stats.Completed)
	assert.Equal(t, int64(1), stats.Rejected)
	assert.Positive(t, stats.LastRejectedMs)
	assert.Equal(t, int64(1), stats.PersistFailures)
	assert.Equal(t, int64(1), stats.ConsecutivePersistFailures)

	hedgeAuditStartPersistence = func(_ context.Context, spec model.RoutingHedgeAttemptStartSpec) (model.RoutingHedgeAttemptAudit, error) {
		return model.RoutingHedgeAttemptAudit{ID: 23, AttemptKey: spec.AttemptKey}, nil
	}
	hedgeAuditCompletePersistence = func(_ context.Context, id int64, _ model.RoutingHedgeAttemptCompleteSpec) (model.RoutingHedgeAttemptAudit, error) {
		return model.RoutingHedgeAttemptAudit{ID: id}, nil
	}
	flushed, err = buffer.flush(context.Background(), hedgeAuditFlushBatch)
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	stats = buffer.stats()
	assert.Zero(t, stats.Entries)
	assert.Equal(t, int64(1), stats.PersistFailures)
	assert.Zero(t, stats.ConsecutivePersistFailures)
}

func TestHedgeAttemptAuditBufferFailureDoesNotStarveLaterCompletedAttempts(t *testing.T) {
	buffer := newHedgeAttemptAuditBuffer(2, 1<<20)
	persistErr := errors.New("poison audit row")
	previousStart := hedgeAuditStartPersistence
	previousComplete := hedgeAuditCompletePersistence
	hedgeAuditStartPersistence = func(_ context.Context, spec model.RoutingHedgeAttemptStartSpec) (model.RoutingHedgeAttemptAudit, error) {
		if spec.RequestID == "poison" {
			return model.RoutingHedgeAttemptAudit{}, persistErr
		}
		return model.RoutingHedgeAttemptAudit{ID: 41, AttemptKey: spec.AttemptKey}, nil
	}
	hedgeAuditCompletePersistence = func(_ context.Context, id int64, _ model.RoutingHedgeAttemptCompleteSpec) (model.RoutingHedgeAttemptAudit, error) {
		return model.RoutingHedgeAttemptAudit{ID: id}, nil
	}
	t.Cleanup(func() {
		hedgeAuditStartPersistence = previousStart
		hedgeAuditCompletePersistence = previousComplete
	})

	poison, err := buffer.reserve(hedgeAttemptAuditSpecForTest("poison"))
	require.NoError(t, err)
	healthy, err := buffer.reserve(hedgeAttemptAuditSpecForTest("healthy"))
	require.NoError(t, err)
	completion := model.RoutingHedgeAttemptCompleteSpec{
		Result: model.RoutingHedgeAttemptResultUpstreamError, HTTPStatus: 502, CompletedTimeMs: 1_050,
	}
	require.NoError(t, poison.Complete(completion))
	require.NoError(t, healthy.Complete(completion))

	flushed, err := buffer.flush(context.Background(), 2)
	assert.ErrorIs(t, err, persistErr)
	assert.Equal(t, 1, flushed)
	stats := buffer.stats()
	assert.Equal(t, 1, stats.Entries)
	assert.Equal(t, 1, stats.Completed)
	assert.Equal(t, int64(1), stats.Persisted)
	assert.Equal(t, int64(1), stats.PersistFailures)
	assert.Zero(t, stats.ConsecutivePersistFailures,
		"a later successful persistence must recover current pipeline health")
}

func TestFlushHedgeAttemptAuditsDrainsHighRateBurstInBoundedBatches(t *testing.T) {
	const attempts = hedgeAuditFlushBatch*4 + 7
	ResetHedgeAttemptAuditsForTest(attempts)
	t.Cleanup(func() { ResetHedgeAttemptAuditsForTest() })
	previousStart := hedgeAuditStartPersistence
	previousComplete := hedgeAuditCompletePersistence
	var nextID int64
	hedgeAuditStartPersistence = func(_ context.Context, spec model.RoutingHedgeAttemptStartSpec) (model.RoutingHedgeAttemptAudit, error) {
		nextID++
		return model.RoutingHedgeAttemptAudit{ID: nextID, AttemptKey: spec.AttemptKey}, nil
	}
	hedgeAuditCompletePersistence = func(_ context.Context, id int64, _ model.RoutingHedgeAttemptCompleteSpec) (model.RoutingHedgeAttemptAudit, error) {
		return model.RoutingHedgeAttemptAudit{ID: id}, nil
	}
	t.Cleanup(func() {
		hedgeAuditStartPersistence = previousStart
		hedgeAuditCompletePersistence = previousComplete
	})

	for index := 0; index < attempts; index++ {
		spec := hedgeAttemptAuditSpecForTest(fmt.Sprintf("burst-%d", index))
		reservation, err := ReserveUpstreamAttemptAudit(spec)
		require.NoError(t, err)
		require.NoError(t, reservation.Complete(model.RoutingHedgeAttemptCompleteSpec{
			Result: model.RoutingHedgeAttemptResultUpstreamError, HTTPStatus: 502,
			UpstreamSent: true, FinalAttempt: true, CompletedTimeMs: 1_050,
		}))
	}

	flushed, err := FlushHedgeAttemptAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, attempts, flushed)
	stats := HedgeAttemptAuditsStats()
	assert.Zero(t, stats.Entries)
	assert.Equal(t, int64(attempts), stats.Persisted)
	assert.Zero(t, stats.Rejected)
	assert.Zero(t, stats.PersistFailures)
}

func hedgeAttemptAuditSpecForTest(requestID string) model.RoutingHedgeAttemptStartSpec {
	return model.RoutingHedgeAttemptStartSpec{
		RequestID: requestID, NodeEpochID: strings.Repeat("a", 32),
		PolicyRevision: 7, AlgorithmVersion: DecisionAlgorithmBalancedV1, PoolID: 3, MemberID: 11,
		ChannelID: 101, CredentialID: 121, ModelName: "gpt-test",
		ExecutionMode: model.RoutingAttemptExecutionHedge,
		Role:          model.RoutingHedgeAttemptRolePrimary, EndpointAuthority: "https://api.example.test:443",
		Region: "default", StartedTimeMs: 1_000,
		Cost: model.RoutingHedgeAttemptCostSpec{
			Known:        true,
			ExpectedCost: 0.001, WorstCaseCost: 0.002, EffectiveCost: 0.0012,
			Currency: "USD", Unit: "request", PricingBasis: SystemRoutingPricingBasis,
			PricingHash: fmt.Sprintf("%064x", 1), PricingVersion: "pricing-v1",
			PricingIdentity:       "billing:" + fmt.Sprintf("%064x", 1) + ":channel-config:7",
			ConfigurationRevision: 7, UpstreamCostMultiplier: 1.5,
			BaselineExpectedKnown: true, BaselineExpectedCost: 0.0005,
			BaselineWorstCaseKnown: true, BaselineWorstCaseCost: 0.001,
			ConfidenceScore: 1, FreshnessScore: 1,
			ExpectedBreakdown:    model.RoutingCostBreakdown{Input: 0.0004, Output: 0.0006, Total: 0.001},
			WorstSingleBreakdown: model.RoutingCostBreakdown{Input: 0.0008, Output: 0.0012, Total: 0.002},
			ObservedTime:         900, EffectiveTime: 900, ExpiresTime: 2_000,
		},
	}
}
