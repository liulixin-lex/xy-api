package channelrouting

const ExclusionReasonClaudeCodeOnly = "claude_code_only"

func requestTrafficExclusionReason(profile RequestProfile, channel ChannelSnapshot) string {
	if channel.ServesClaudeCode && profile.TrafficClass != RequestTrafficClassClaudeCode {
		return ExclusionReasonClaudeCodeOnly
	}
	return ""
}
