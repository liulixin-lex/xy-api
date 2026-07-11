package routing

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRankCandidatesHandlesCostNaNFreeAndUnknown(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		WeightCost:         1,
		MinVolume:          10,
	}
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, &CostSnapshot{Known: true, Cost: 0}, nil),
		testCandidate(2, 1, 100, 10, &CostSnapshot{Known: true, Cost: 2}, nil),
		testCandidate(3, 1, 100, 10, &CostSnapshot{Known: true, Cost: 4}, nil),
		testCandidate(4, 1, 100, 10, &CostSnapshot{Known: true, Cost: math.NaN()}, nil),
		testCandidate(5, 1, 100, 10, nil, nil),
	}

	decision := RankCandidates(candidates, settings)

	require.Len(t, decision.Ranked, len(candidates))
	assert.Equal(t, 1, decision.Ranked[0].Channel.Id)
	assert.Greater(t, rankedByID(t, decision, 1).Score, rankedByID(t, decision, 3).Score)
	assert.Greater(t, rankedByID(t, decision, 2).Score, rankedByID(t, decision, 3).Score)
	assert.False(t, rankedByID(t, decision, 4).CostKnown)
	assert.False(t, rankedByID(t, decision, 5).CostKnown)
	for _, ranked := range decision.Ranked {
		assert.False(t, math.IsNaN(ranked.Score))
		assert.False(t, math.IsInf(ranked.Score, 0))
	}
}

func TestRankCandidatesDropsCostWeightWhenAllCostsUnknownAndKeepsHealthyAhead(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		WeightCost:         9,
		MinVolume:          10,
	}
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateDegraded}),
		testCandidate(2, 0.1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy}),
	}

	decision := RankCandidates(candidates, settings)

	require.Len(t, decision.Ranked, len(candidates))
	assert.InDelta(t, 1.0, decision.Weights.Availability, 0.000001)
	assert.Zero(t, decision.Weights.Cost)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
	assert.False(t, decision.Ranked[0].Degraded)
	assert.True(t, decision.Ranked[1].Degraded)
	assert.Greater(t, decision.Ranked[1].Score, decision.Ranked[0].Score)
}

func TestRankCandidatesNormalizesWeights(t *testing.T) {
	settings := Settings{
		WeightAvailability: 2,
		WeightLatency:      3,
		WeightThroughput:   0,
		WeightCost:         5,
		MinVolume:          10,
	}

	decision := RankCandidates([]Candidate{
		testCandidate(1, 1, 100, 10, &CostSnapshot{Known: true, Cost: 1}, nil),
	}, settings)

	assert.InDelta(t, 1.0, decision.Weights.Availability+decision.Weights.Latency+decision.Weights.Throughput+decision.Weights.Cost, 0.000001)
	assert.InDelta(t, 0.2, decision.Weights.Availability, 0.000001)
	assert.InDelta(t, 0.3, decision.Weights.Latency, 0.000001)
	assert.Zero(t, decision.Weights.Throughput)
	assert.InDelta(t, 0.5, decision.Weights.Cost, 0.000001)
}

func TestSelectRankedFromCandidatesUsesTTFTOnlyWhenPreferred(t *testing.T) {
	candidates := []Candidate{
		testCandidate(1, 1, 900, 10, nil, nil),
		testCandidate(2, 1, 200, 10, nil, nil),
	}
	candidates[0].Metric.P95TTFTMs = 100
	candidates[1].Metric.P95TTFTMs = 500

	streaming := SelectRankedFromCandidates(candidates, Settings{
		WeightLatency: 1,
		TopK:          1,
		PreferTTFT:    true,
	})
	nonStreaming := SelectRankedFromCandidates(candidates, Settings{
		WeightLatency: 1,
		TopK:          1,
	})

	require.NotNil(t, streaming.Selected)
	require.NotNil(t, nonStreaming.Selected)
	assert.Equal(t, 1, streaming.Selected.Channel.Id)
	assert.Equal(t, 2, nonStreaming.Selected.Channel.Id)
}

func TestSelectRankedFromCandidatesFallsBackToTotalLatencyForInvalidTTFT(t *testing.T) {
	for _, ttft := range []struct {
		name  string
		value float64
	}{
		{name: "missing"},
		{name: "nan", value: math.NaN()},
	} {
		t.Run(ttft.name, func(t *testing.T) {
			candidates := []Candidate{
				testCandidate(1, 1, 900, 10, nil, nil),
				testCandidate(2, 1, 200, 10, nil, nil),
			}
			candidates[0].Metric.P95TTFTMs = ttft.value
			candidates[1].Metric.P95TTFTMs = ttft.value

			decision := SelectRankedFromCandidates(candidates, Settings{
				WeightLatency: 1,
				TopK:          1,
				PreferTTFT:    true,
			})

			require.NotNil(t, decision.Selected)
			assert.Equal(t, 2, decision.Selected.Channel.Id)
		})
	}
}

func TestRankCandidatesTreatsStaleCostsAsUnknown(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		WeightCost:         9,
		MinVolume:          10,
		NowUnix:            2000,
		SnapshotStaleSec:   60,
	}

	decision := RankCandidates([]Candidate{
		testCandidate(1, 1, 100, 10, &CostSnapshot{Known: true, Cost: 0.01, UpdatedUnix: 1000}, nil),
		testCandidate(2, 1, 100, 10, &CostSnapshot{Known: true, Cost: 100, UpdatedUnix: 1000}, nil),
	}, settings)

	require.Len(t, decision.Ranked, 2)
	assert.False(t, decision.Ranked[0].CostKnown)
	assert.False(t, decision.Ranked[1].CostKnown)
	assert.Zero(t, decision.Weights.Cost)
}

func TestSelectRankedFromCandidatesTopKDeterministic(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		TopK:               3,
		RandomSeed:         99,
	}
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, nil, nil),
		testCandidate(2, 0.9, 100, 10, nil, nil),
		testCandidate(3, 0.8, 100, 10, nil, nil),
		testCandidate(4, 0.7, 100, 10, nil, nil),
		testCandidate(5, 0.6, 100, 10, nil, nil),
	}

	first := SelectRankedFromCandidates(candidates, settings)
	second := SelectRankedFromCandidates(candidates, settings)
	expectedIndex := weightedTopKIndex(first.Ranked[:settings.TopK], settings.RandomSeed)

	require.NotNil(t, first.Selected)
	require.NotNil(t, second.Selected)
	assert.Equal(t, first.Selected.Channel.Id, second.Selected.Channel.Id)
	assert.Equal(t, first.Ranked[expectedIndex].Channel.Id, first.Selected.Channel.Id)
	assert.LessOrEqual(t, expectedIndex, 2)

	settings.TopK = 1
	topOnly := SelectRankedFromCandidates(candidates, settings)
	require.NotNil(t, topOnly.Selected)
	assert.Equal(t, 1, topOnly.Selected.Channel.Id)
}

func TestSelectRankedFromCandidatesWeightsTopKByScore(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		TopK:               3,
		RandomSeed:         99,
	}
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, nil, nil),
		testCandidate(2, 0, 100, 10, nil, nil),
		testCandidate(3, 0, 100, 10, nil, nil),
	}

	decision := SelectRankedFromCandidates(candidates, settings)

	require.NotNil(t, decision.Selected)
	assert.Equal(t, 1, decision.Selected.Channel.Id)
}

func TestSelectRankedFromCandidatesSamplesTopKWithinHighestPriorityPool(t *testing.T) {
	highPriority := int64(100)
	lowPriority := int64(1)
	candidates := []Candidate{
		testCandidate(1, 0.1, 100, 10, nil, nil),
		testCandidate(2, 0.1, 100, 10, nil, nil),
		testCandidate(3, 1, 100, 10, nil, nil),
	}
	candidates[0].Channel.Priority = &highPriority
	candidates[1].Channel.Priority = &highPriority
	candidates[2].Channel.Priority = &lowPriority

	for seed := int64(0); seed < 20; seed++ {
		decision := SelectRankedFromCandidates(candidates, Settings{
			WeightAvailability: 1,
			MinVolume:          10,
			TopK:               3,
			RandomSeed:         seed,
		})

		require.NotNil(t, decision.Selected)
		assert.NotEqual(t, 3, decision.Selected.Channel.Id, "seed %d selected a lower-priority channel", seed)
	}
}

func TestDegradedCandidateDoesNotOutrankHealthyCandidate(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
	}
	candidates := []Candidate{
		testCandidate(10, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateDegraded}),
		testCandidate(20, 0.1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy}),
	}

	decision := RankCandidates(candidates, settings)

	require.Len(t, decision.Ranked, len(candidates))
	assert.Equal(t, 20, decision.Ranked[0].Channel.Id)
	assert.False(t, decision.Ranked[0].Degraded)
	assert.True(t, decision.Ranked[1].Degraded)
}

func TestRankCandidatesAppliesAvailabilityFloorWithEnoughVolume(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		WeightCost:         9,
		MinVolume:          10,
		AvailabilityFloor:  0.95,
	}
	candidates := []Candidate{
		testCandidate(1, 0.90, 100, 10, &CostSnapshot{Known: true, Cost: 0.01}, nil),
		testCandidate(2, 0.96, 200, 5, &CostSnapshot{Known: true, Cost: 100}, nil),
	}

	decision := RankCandidates(candidates, settings)

	require.Len(t, decision.Ranked, 1)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
}

func TestRankCandidatesKeepsLowVolumeCandidatesDespiteAvailabilityFloor(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          50,
		AvailabilityFloor:  0.95,
	}
	candidate := testCandidate(1, 0.1, 100, 10, nil, nil)
	candidate.Metric.RequestCount = 3
	candidate.Metric.SuccessCount = 0
	candidate.Metric.ReliabilityRequestCount = 3
	candidate.Metric.ReliabilityFailureCount = 3

	decision := RankCandidates([]Candidate{candidate}, settings)

	require.Len(t, decision.Ranked, 1)
	assert.Equal(t, 1, decision.Ranked[0].Channel.Id)
}

func TestAvailabilityUsesReliabilitySamplesAndLegacyRowsStayNeutral(t *testing.T) {
	settings := Settings{WeightAvailability: 1, MinVolume: 1}
	reliable := testCandidate(1, 1, 100, 1, nil, nil)
	reliable.Metric.ReliabilityRequestCount = 10
	reliable.Metric.ReliabilityFailureCount = 2
	reliable.Metric.RequestCount = 100
	reliable.Metric.SuccessCount = 1

	legacy := testCandidate(2, 0, 100, 1, nil, nil)
	legacy.Metric.ReliabilityRequestCount = 0
	legacy.Metric.ReliabilityFailureCount = 0
	legacy.Metric.RequestCount = 100
	legacy.Metric.SuccessCount = 0

	decision := RankCandidates([]Candidate{reliable, legacy}, settings)

	assert.InDelta(t, 0.8, rankedByID(t, decision, 1).Availability, 0.000001)
	assert.InDelta(t, availabilityNeutralPrior, rankedByID(t, decision, 2).Availability, 0.000001)
}

func TestAvailabilityFloorUsesReliabilityVolumeOnly(t *testing.T) {
	candidate := testCandidate(1, 0, 100, 1, nil, nil)
	candidate.Metric.RequestCount = 1000
	candidate.Metric.SuccessCount = 0
	candidate.Metric.ReliabilityRequestCount = 3
	candidate.Metric.ReliabilityFailureCount = 3

	decision := RankCandidates([]Candidate{candidate}, Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		AvailabilityFloor:  0.99,
	})

	require.Len(t, decision.Ranked, 1)
}

func TestRankCandidatesCapacityCooldownIsHardFilterWithoutBreakerBypass(t *testing.T) {
	cooling := testCandidate(1, 1, 100, 1, nil, &BreakerSnapshot{State: BreakerStateHealthy})
	cooling.Capacity = &CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000}
	open := testCandidate(2, 1, 100, 1, nil, &BreakerSnapshot{State: BreakerStateOpen, CooldownUntilUnix: 300, UpdatedUnix: 100})

	decision := RankCandidates([]Candidate{cooling, open}, Settings{
		WeightAvailability: 1,
		MaxEjectedPct:      0,
		NowUnix:            150,
		NowUnixMilli:       150_000,
	})

	assert.Equal(t, 1, decision.FilteredCapacity)
	assert.True(t, decision.BreakerBypassed)
	require.Len(t, decision.Ranked, 1)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
}

func TestRankCandidatesRestoresCapacityCandidateAtDeadline(t *testing.T) {
	candidate := testCandidate(1, 1, 100, 1, nil, nil)
	candidate.Capacity = &CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000}

	decision := RankCandidates([]Candidate{candidate}, Settings{WeightAvailability: 1, NowUnix: 200, NowUnixMilli: 200_000})

	assert.Zero(t, decision.FilteredCapacity)
	require.Len(t, decision.Ranked, 1)
}

func TestRankCandidatesOrdersAdminPriorityBeforeScore(t *testing.T) {
	highPriority := int64(100)
	lowPriority := int64(1)
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, nil, nil),
		testCandidate(2, 0.1, 100, 10, nil, nil),
	}
	candidates[0].Channel.Priority = &lowPriority
	candidates[1].Channel.Priority = &highPriority

	decision := RankCandidates(candidates, Settings{
		WeightAvailability: 1,
		MinVolume:          10,
	})

	require.Len(t, decision.Ranked, 2)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
	assert.Greater(t, decision.Ranked[1].Score, decision.Ranked[0].Score)
}

func TestRankCandidatesPrefersLowerInflightWhenScoreTies(t *testing.T) {
	candidates := []Candidate{
		testCandidate(1, 1, 100, 10, nil, nil),
		testCandidate(2, 1, 100, 10, nil, nil),
	}
	candidates[0].Metric.Inflight = 3

	decision := RankCandidates(candidates, Settings{
		WeightAvailability: 1,
		MinVolume:          10,
	})

	require.Len(t, decision.Ranked, 2)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
	assert.Equal(t, int64(0), decision.Ranked[0].Inflight)
	assert.Equal(t, int64(3), decision.Ranked[1].Inflight)
}

func TestOpenBreakerFilteredUnlessMaxEjectedPctExceeded(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		MaxEjectedPct:      50,
		NowUnix:            1000,
		SnapshotStaleSec:   60,
	}

	t.Run("filters fresh open breaker", func(t *testing.T) {
		decision := RankCandidates([]Candidate{
			testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, UpdatedUnix: 1000}),
			testCandidate(2, 0.5, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy, UpdatedUnix: 1000}),
		}, settings)

		require.Len(t, decision.Ranked, 1)
		assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
		assert.False(t, decision.BreakerBypassed)
		assert.Equal(t, 1, decision.FilteredOpen)
	})

	t.Run("bypasses open filter when ejected percent is too high", func(t *testing.T) {
		decision := RankCandidates([]Candidate{
			testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, UpdatedUnix: 1000}),
			testCandidate(2, 0.9, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, UpdatedUnix: 1000}),
			testCandidate(3, 0.1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy, UpdatedUnix: 1000}),
		}, settings)

		require.Len(t, decision.Ranked, 1)
		assert.True(t, decision.BreakerBypassed)
		assert.Equal(t, 2, decision.FilteredOpen)
		assert.Equal(t, 3, decision.Ranked[0].Channel.Id)
	})

	t.Run("uses bypassed open breakers only when every soft candidate is open", func(t *testing.T) {
		decision := RankCandidates([]Candidate{
			testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, UpdatedUnix: 1000}),
			testCandidate(2, 0.9, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, UpdatedUnix: 1000}),
		}, settings)

		require.Len(t, decision.Ranked, 2)
		assert.True(t, decision.BreakerBypassed)
		assert.Zero(t, decision.FilteredOpen)
		assert.True(t, decision.Ranked[0].Degraded)
		assert.True(t, decision.Ranked[0].Open)
	})

	t.Run("stale open breaker fails open", func(t *testing.T) {
		decision := RankCandidates([]Candidate{
			testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, Reason: BreakerReasonAuthFail, UpdatedUnix: 900}),
			testCandidate(2, 0.5, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy, UpdatedUnix: 1000}),
		}, settings)

		require.Len(t, decision.Ranked, 2)
		assert.False(t, decision.BreakerBypassed)
		assert.Zero(t, decision.FilteredOpen)
	})
}

func TestOpenBreakerAfterCooldownIsEligibleAsHalfOpen(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		MaxEjectedPct:      50,
		NowUnix:            2000,
		SnapshotStaleSec:   300,
	}

	decision := RankCandidates([]Candidate{
		testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{
			State:             BreakerStateOpen,
			UpdatedUnix:       1990,
			CooldownUntilUnix: 1999,
		}),
		testCandidate(2, 0.5, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy, UpdatedUnix: 2000}),
	}, settings)

	require.Len(t, decision.Ranked, 2)
	assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
	assert.Equal(t, 1, decision.Ranked[1].Channel.Id)
	assert.True(t, decision.Ranked[1].Degraded)
	assert.False(t, decision.Ranked[1].Open)
	assert.False(t, decision.BreakerBypassed)
	assert.Zero(t, decision.FilteredOpen)
}

func TestAuthFailureAndLowBalanceAreHardFilteredEvenWhenBreakerBypassWouldApply(t *testing.T) {
	settings := Settings{
		WeightAvailability: 1,
		MinVolume:          10,
		MaxEjectedPct:      50,
		NowUnix:            1000,
		SnapshotStaleSec:   60,
	}

	decision := RankCandidates([]Candidate{
		testCandidate(1, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, Reason: BreakerReasonAuthFail, UpdatedUnix: 1000}),
		testCandidate(2, 1, 100, 10, nil, &BreakerSnapshot{State: BreakerStateOpen, Reason: BreakerReasonBalance, UpdatedUnix: 1000}),
		testCandidate(3, 0.5, 100, 10, nil, &BreakerSnapshot{State: BreakerStateHealthy, UpdatedUnix: 1000}),
	}, settings)

	require.Len(t, decision.Ranked, 1)
	assert.Equal(t, 3, decision.Ranked[0].Channel.Id)
	assert.False(t, decision.BreakerBypassed)
	assert.Equal(t, 2, decision.FilteredOpen)
}

func TestRankCandidatesDoesNotModifyOriginalChannelPointer(t *testing.T) {
	weight := uint(42)
	priority := int64(7)
	channel := &model.Channel{
		Id:       100,
		Name:     "immutable",
		Weight:   &weight,
		Priority: &priority,
		Balance:  12.5,
	}
	metric := &MetricSnapshot{RequestCount: 100, SuccessCount: 99, P95LatencyMs: 123, TPS: 4}
	cost := &CostSnapshot{Known: true, Cost: 1.25}
	breaker := &BreakerSnapshot{State: BreakerStateDegraded}
	candidate := Candidate{
		Channel: channel,
		Metric:  metric,
		Cost:    cost,
		Breaker: breaker,
	}
	beforeChannel := *channel
	beforeMetric := *metric
	beforeCost := *cost
	beforeBreaker := *breaker

	decision := SelectRankedFromCandidates([]Candidate{candidate}, Settings{
		WeightAvailability: 1,
		WeightLatency:      1,
		WeightThroughput:   1,
		WeightCost:         1,
		MinVolume:          10,
		TopK:               1,
	})

	require.Len(t, decision.Ranked, 1)
	assert.Same(t, channel, decision.Ranked[0].Channel)
	assert.Equal(t, beforeChannel, *channel)
	assert.Equal(t, beforeMetric, *metric)
	assert.Equal(t, beforeCost, *cost)
	assert.Equal(t, beforeBreaker, *breaker)
}

func testCandidate(id int, availability float64, p95LatencyMs float64, tps float64, cost *CostSnapshot, breaker *BreakerSnapshot) Candidate {
	requests := int64(100)
	successes := int64(math.Round(availability * float64(requests)))
	return Candidate{
		Channel: &model.Channel{Id: id},
		Metric: &MetricSnapshot{
			RequestCount:            requests,
			SuccessCount:            successes,
			ReliabilityRequestCount: requests,
			ReliabilityFailureCount: requests - successes,
			P95LatencyMs:            p95LatencyMs,
			TPS:                     tps,
		},
		Cost:    cost,
		Breaker: breaker,
	}
}

func rankedByID(t *testing.T, decision Decision, channelID int) RankedCandidate {
	t.Helper()
	for _, ranked := range decision.Ranked {
		if ranked.Channel != nil && ranked.Channel.Id == channelID {
			return ranked
		}
	}
	require.Failf(t, "ranked candidate not found", "channel_id=%d", channelID)
	return RankedCandidate{}
}
