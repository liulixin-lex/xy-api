package channelrouting

import (
	"time"

	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
)

func ChannelBalanceRuntimeBlocked(channelID int, now time.Time) (string, bool) {
	if channelID <= 0 {
		return "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	snapshot, ok := routinghotcache.GetChannelBalanceUnavailable(channelID)
	if !ok || snapshot.CooldownUntilUnixMilli <= now.UnixMilli() {
		return "", false
	}
	reason := snapshot.Reason
	if reason == "" {
		reason = ExclusionReasonChannelBalance
	}
	return reason, true
}
