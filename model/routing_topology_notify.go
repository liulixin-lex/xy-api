package model

var routingTopologyChangeSignal = make(chan struct{}, 1)

func NotifyRoutingTopologyChanged() {
	select {
	case routingTopologyChangeSignal <- struct{}{}:
	default:
	}
}

func RoutingTopologyChanges() <-chan struct{} {
	return routingTopologyChangeSignal
}

func ResetRoutingTopologyChangesForTest() {
	for {
		select {
		case <-routingTopologyChangeSignal:
		default:
			return
		}
	}
}
