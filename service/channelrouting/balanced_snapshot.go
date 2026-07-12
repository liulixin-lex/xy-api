package channelrouting

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

type balancedPoolModelKey struct {
	poolID     int
	model      string
	preferTTFT bool
}

func (snapshot *runtimeSnapshot) compileBalancedPools() error {
	if snapshot == nil {
		return ErrRoutingSessionInvalid
	}
	if snapshot.preparedBalancedPools == nil {
		snapshot.preparedBalancedPools = make(map[balancedPoolModelKey]*routingselector.PreparedBalancedPool)
	}
	now := time.Unix(snapshot.view.BuiltAtUnix, 0)
	if snapshot.view.BuiltAtUnix <= 0 {
		now = time.Now()
	}
	for key, memberIndexes := range snapshot.memberIndexesByPoolModel {
		poolIndex, exists := snapshot.poolIndexByID[key.poolID]
		if !exists || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) {
			return ErrRoutingSessionInvalid
		}
		pool := snapshot.view.Pools[poolIndex]
		policy := pool.BalancedPolicy
		if policy == (BalancedPoolPolicy{}) {
			profile := pool.PolicyProfile
			if profile == "" {
				profile = model.RoutingPolicyProfileBalanced
			}
			var err error
			policy, err = normalizeBalancedPoolPolicy(defaultBalancedPoolPolicy(profile))
			if err != nil {
				return err
			}
			snapshot.view.Pools[poolIndex].BalancedPolicy = policy
		}
		for _, preferTTFT := range []bool{false, true} {
			profile, err := NewRequestProfile("", pool.GroupName, key.model, preferTTFT, 0, 0, 0)
			if err != nil {
				return err
			}
			settings := policy.settings(now, 1, 0, preferTTFT)
			candidates := make([]routingselector.BalancedCandidate, 0, len(memberIndexes))
			for _, memberIndex := range memberIndexes {
				if memberIndex < 0 || memberIndex >= len(pool.Members) {
					return ErrRoutingSessionInvalid
				}
				member := pool.Members[memberIndex]
				observation, ok := snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: key.model}]
				if !ok {
					return ErrRoutingSessionInvalid
				}
				channel, ok := snapshot.channelByID[member.ChannelID]
				if !ok {
					return ErrRoutingSessionInvalid
				}
				candidate, err := balancedCandidateFromSnapshot(pool, member, observation, channel, profile, settings)
				if err != nil {
					return fmt.Errorf("compile balanced pool %d model %q: %w", pool.ID, key.model, err)
				}
				candidates = append(candidates, candidate)
			}
			prepared, err := routingselector.PrepareBalanced(candidates, settings)
			if err != nil {
				return fmt.Errorf("prepare balanced pool %d model %q: %w", pool.ID, key.model, err)
			}
			snapshot.preparedBalancedPools[balancedPoolModelKey{
				poolID: pool.ID, model: key.model, preferTTFT: preferTTFT,
			}] = prepared
		}
	}
	return nil
}

func balancedCandidateFromSnapshot(
	pool PoolSnapshot,
	member PoolMemberSnapshot,
	observation ModelSnapshot,
	channel ChannelSnapshot,
	profile RequestProfile,
	settings routingselector.BalancedSettings,
) (routingselector.BalancedCandidate, error) {
	if pool.ID <= 0 || member.ID <= 0 || member.PoolID != pool.ID || member.ChannelID <= 0 ||
		channel.ID != member.ChannelID || member.LegacyWeight < 0 {
		return routingselector.BalancedCandidate{}, routingselector.ErrBalancedCandidateInvalid
	}
	candidate := routingselector.BalancedCandidate{
		Candidate: routingselector.Candidate{
			Channel: &model.Channel{Id: channel.ID, Name: channel.Name, Type: channel.Type, Status: channel.Status},
		},
		BusinessTier:        member.LegacyPriority,
		TargetWeight:        float64(member.LegacyWeight),
		Confidence:          balancedMetricConfidence(observation, pool.BalancedPolicy.MinVolume),
		Freshness:           balancedMetricFreshness(observation.MetricUpdatedUnix, settings.NowUnix, settings.SnapshotStaleSec),
		SlowStartFactor:     1,
		MetricUpdatedUnix:   observation.MetricUpdatedUnix,
		ExplorationEligible: !observation.MetricKnown || observation.ReliabilityRequestCount < int64(pool.BalancedPolicy.MinVolume),
	}
	if member.PhysicalStatus != common.ChannelStatusEnabled || channel.Status != common.ChannelStatusEnabled {
		candidate.HardExclusionReason = routingselector.BalancedExclusionRuntimeBlocked
	}
	if member.MultiKey || channel.MultiKey || member.CredentialsTruncated || len(member.CredentialIDs) > 1 {
		candidate.HardExclusionReason = routingselector.BalancedExclusionCredentialUnavailable
	}
	if observation.MetricKnown || observation.Inflight > 0 {
		candidate.Candidate.Metric = &routingselector.MetricSnapshot{
			RequestCount:            observation.RequestCount,
			SuccessCount:            observation.SuccessCount,
			ReliabilityRequestCount: observation.ReliabilityRequestCount,
			ReliabilityFailureCount: observation.ReliabilityFailureCount,
			TPS:                     observation.OutputTokensPerSecond,
			Inflight:                observation.Inflight,
		}
		if observation.P95LatencyKnown {
			candidate.Candidate.Metric.P95LatencyMs = observation.P95LatencyMs
		}
		if observation.P95TTFTKnown {
			candidate.Candidate.Metric.P95TTFTMs = observation.P95TTFTMs
		}
	}
	cost, err := shadowExpectedCost(observation, profile)
	if err != nil {
		return routingselector.BalancedCandidate{}, err
	}
	if cost != nil {
		candidate.Candidate.Cost = &routingselector.CostSnapshot{
			Known: cost.Known, Cost: cost.Cost, UpdatedUnix: cost.UpdatedUnix,
		}
	}
	if observation.BreakerKnown {
		candidate.Candidate.Breaker = &routingselector.BreakerSnapshot{
			State: observation.BreakerState, Reason: observation.BreakerReason,
			CooldownUntilUnix: observation.BreakerCooldownUntil,
			HalfOpenInflight:  observation.BreakerHalfOpenInflight,
			UpdatedUnix:       observation.BreakerUpdatedUnix,
		}
	}
	if channel.AuthFailure && shadowMarkerFresh(channel.AuthFailureUpdatedAt, settings.NowUnix, settings.SnapshotStaleSec) {
		candidate.Candidate.Breaker = &routingselector.BreakerSnapshot{
			State: routingselector.BreakerStateOpen, Reason: routingselector.BreakerReasonAuthFail,
			UpdatedUnix: channel.AuthFailureUpdatedAt,
		}
	} else if channel.BalanceKnown && channel.Balance < pool.BalancedPolicy.BalanceMarginUSD &&
		shadowMarkerFresh(channel.BalanceUpdatedAt, settings.NowUnix, settings.SnapshotStaleSec) {
		candidate.Candidate.Breaker = &routingselector.BreakerSnapshot{
			State: routingselector.BreakerStateOpen, Reason: routingselector.BreakerReasonBalance,
			UpdatedUnix: channel.BalanceUpdatedAt,
		}
	}
	if observation.CapacityCooldownUntilMs > 0 || observation.CapacityStatusCode > 0 || observation.CapacityUpdatedUnixMilli > 0 {
		candidate.Candidate.Capacity = &routingselector.CapacityCooldownSnapshot{
			SourceStatusCode:       observation.CapacityStatusCode,
			CooldownUntilUnixMilli: observation.CapacityCooldownUntilMs,
			UpdatedUnixMilli:       observation.CapacityUpdatedUnixMilli,
		}
	}
	return candidate, nil
}

func balancedMetricConfidence(observation ModelSnapshot, minimumVolume int) float64 {
	if !observation.MetricKnown {
		return 0.50
	}
	if observation.ReliabilityRequestCount < int64(max(minimumVolume, 0)) {
		return 0.75
	}
	return 1
}

func balancedMetricFreshness(updatedUnix int64, nowUnix int64, staleSeconds int) float64 {
	if updatedUnix <= 0 || nowUnix <= 0 || updatedUnix > nowUnix || staleSeconds <= 0 {
		return 0.50
	}
	age := nowUnix - updatedUnix
	if age >= int64(staleSeconds) {
		return 0.50
	}
	return 1 - 0.5*float64(age)/float64(staleSeconds)
}
