package channelrouting

import "github.com/QuantumNous/new-api/model"

const ExclusionReasonClaudeCodeOnly = "claude_code_only"

func requestTrafficExclusionReason(profile RequestProfile, channel ChannelSnapshot) string {
	if channel.TrafficClass == model.RoutingChannelTrafficClassClaudeCodeOnly && profile.TrafficClass != RequestTrafficClassClaudeCode {
		return ExclusionReasonClaudeCodeOnly
	}
	return ""
}
