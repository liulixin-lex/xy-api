package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

func reserveRoutingAdaptiveConcurrency(
	policyRevision uint64,
	policy model.RoutingCanaryPolicy,
	identity channelrouting.Identity,
	channelID int,
	modelName string,
	upstreamModelName string,
	strictRequest *channelrouting.StrictCapacityRequest,
) (*channelrouting.AdaptiveConcurrencyLeaseSet, error) {
	return channelRoutingCanaryRuntime.tryAcquireAdaptiveConcurrency(
		policyRevision,
		policy,
		identity,
		channelID,
		modelName,
		upstreamModelName,
		strictRequest,
	)
}

func ObserveRoutingAdaptiveConcurrency(
	c *gin.Context,
	signal channelrouting.AdaptiveConcurrencySignal,
) {
	channelRoutingCanaryRuntime.observeAdaptiveConcurrency(
		RoutingAdaptiveConcurrencyTargets(c),
		signal,
	)
}

func RoutingAdaptiveConcurrencyStats() channelrouting.AdaptiveConcurrencyStats {
	if channelRoutingCanaryRuntime == nil || channelRoutingCanaryRuntime.adaptive == nil {
		return channelrouting.AdaptiveConcurrencyStats{}
	}
	return channelRoutingCanaryRuntime.adaptive.Stats()
}
