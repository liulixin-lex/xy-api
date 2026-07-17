package model

import "sync"

var routingPricingPublisher struct {
	sync.RWMutex
	hook func(RoutingPricingVersion)
}

// RegisterRoutingPricingChangePublisher installs the control-plane transport
// hook used after a committed pricing update. The returned function restores
// the previous hook and is primarily useful to isolate tests.
func RegisterRoutingPricingChangePublisher(hook func(RoutingPricingVersion)) func() {
	routingPricingPublisher.Lock()
	previous := routingPricingPublisher.hook
	routingPricingPublisher.hook = hook
	routingPricingPublisher.Unlock()
	return func() {
		routingPricingPublisher.Lock()
		routingPricingPublisher.hook = previous
		routingPricingPublisher.Unlock()
	}
}

func notifyRoutingPricingChanged(version RoutingPricingVersion) {
	if version.ID != routingPricingVersionID || version.Epoch <= 0 ||
		!validRoutingChannelHash(version.StateHash) || version.UpdatedTime <= 0 {
		return
	}
	routingPricingPublisher.RLock()
	hook := routingPricingPublisher.hook
	routingPricingPublisher.RUnlock()
	if hook != nil {
		hook(version)
	}
}
