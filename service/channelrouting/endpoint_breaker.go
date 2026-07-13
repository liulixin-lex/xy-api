package channelrouting

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

const BreakerScopeEndpoint = "endpoint"

func endpointBreakerForChannel(
	channel ChannelSnapshot,
	now time.Time,
	staleSeconds int,
) (*routingselector.BreakerSnapshot, string, string) {
	authority := EndpointAuthority(channel.Endpoint, channel.ID)
	region := RoutingRegion()
	key := routingbreaker.NewEndpointKey(authority, region)
	var local *routingselector.BreakerSnapshot
	if snapshot, ok := routinghotcache.GetBreaker(key.HotcacheKey()); ok {
		state := strings.ToLower(strings.TrimSpace(snapshot.State))
		fresh := snapshot.UpdatedUnix <= 0 || now.IsZero() || staleSeconds <= 0 ||
			now.Unix()-snapshot.UpdatedUnix <= int64(staleSeconds)
		if fresh && state != "" && state != model.RoutingBreakerStateHealthy {
			local = &routingselector.BreakerSnapshot{
				State: state, Reason: snapshot.Reason, CooldownUntilUnix: snapshot.CooldownUntilUnix,
				HalfOpenInflight: snapshot.HalfOpenInflight, UpdatedUnix: snapshot.UpdatedUnix,
			}
		}
	}
	var shared *routingselector.BreakerSnapshot
	if snapshot, ok := routinghotcache.GetSharedEndpointBreaker(key.HotcacheKey()); ok {
		state := strings.ToLower(strings.TrimSpace(snapshot.State))
		fresh := (now.IsZero() || snapshot.ExpiresUnix <= 0 || now.Unix() < snapshot.ExpiresUnix) &&
			(snapshot.UpdatedUnix <= 0 || now.IsZero() || staleSeconds <= 0 ||
				now.Unix()-snapshot.UpdatedUnix <= int64(staleSeconds))
		if fresh && state != "" && state != model.RoutingBreakerStateHealthy {
			shared = &routingselector.BreakerSnapshot{
				State: state, Reason: snapshot.Reason, CooldownUntilUnix: snapshot.CooldownUntilUnix,
				UpdatedUnix: snapshot.UpdatedUnix,
			}
		}
	}
	merged, _ := mergeRoutingBreaker(local, shared)
	return merged, authority, region
}

func mergeRoutingBreaker(
	base *routingselector.BreakerSnapshot,
	endpoint *routingselector.BreakerSnapshot,
) (*routingselector.BreakerSnapshot, bool) {
	if endpoint == nil {
		return cloneRoutingBreaker(base), false
	}
	if base == nil {
		return cloneRoutingBreaker(endpoint), true
	}
	baseRank := routingBreakerRank(base)
	endpointRank := routingBreakerRank(endpoint)
	if endpointRank > baseRank || (endpointRank == baseRank && endpoint.UpdatedUnix > base.UpdatedUnix) {
		return cloneRoutingBreaker(endpoint), true
	}
	return cloneRoutingBreaker(base), false
}

func mergeShadowBreaker(
	base *ShadowBreakerInput,
	endpoint *routingselector.BreakerSnapshot,
) (*ShadowBreakerInput, bool) {
	var routingBase *routingselector.BreakerSnapshot
	if base != nil {
		routingBase = &routingselector.BreakerSnapshot{
			State: base.State, Reason: base.Reason, CooldownUntilUnix: base.CooldownUntilUnix,
			HalfOpenInflight: base.HalfOpenInflight, UpdatedUnix: base.UpdatedUnix,
		}
	}
	merged, endpointSelected := mergeRoutingBreaker(routingBase, endpoint)
	if merged == nil {
		return nil, endpointSelected
	}
	return &ShadowBreakerInput{
		State: merged.State, Reason: merged.Reason, CooldownUntilUnix: merged.CooldownUntilUnix,
		HalfOpenInflight: merged.HalfOpenInflight, UpdatedUnix: merged.UpdatedUnix,
	}, endpointSelected
}

func routingBreakerRank(breaker *routingselector.BreakerSnapshot) int {
	if breaker == nil {
		return 0
	}
	reason := strings.ToLower(strings.TrimSpace(breaker.Reason))
	if reason == routingselector.BreakerReasonAuthFail || reason == routingselector.BreakerReasonBalance {
		return 100
	}
	switch strings.ToLower(strings.TrimSpace(breaker.State)) {
	case routingselector.BreakerStateOpen:
		return 4
	case routingselector.BreakerStateHalfOpen:
		return 3
	case routingselector.BreakerStateDegraded:
		return 2
	case routingselector.BreakerStateHealthy:
		return 1
	default:
		return 0
	}
}

func cloneRoutingBreaker(breaker *routingselector.BreakerSnapshot) *routingselector.BreakerSnapshot {
	if breaker == nil {
		return nil
	}
	clone := *breaker
	return &clone
}
