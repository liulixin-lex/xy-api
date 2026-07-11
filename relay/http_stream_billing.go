package relay

import (
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func finalizeHTTPStreamError(c *gin.Context, info *relaycommon.RelayInfo, usage any, preferAudio bool) {
	if !info.HTTPStreamClientCommitted(c) {
		return
	}
	usageDto, _ := usage.(*dto.Usage)
	if !service.ValidUsage(usageDto) {
		usageDto = nil
	}
	postHTTPUsage(c, info, usageDto, preferAudio)
}

func finalizeCommittedHTTPStreamFailure(c *gin.Context, info *relaycommon.RelayInfo, usage any, preferAudio bool) bool {
	if info == nil || !info.HTTPStreamClientCommitted(c) || !info.HTTPStreamHasFailure() {
		return false
	}
	finalizeHTTPStreamError(c, info, usage, preferAudio)
	return true
}

func postHTTPUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, preferAudio bool) {
	containsAudioRatios := ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)
	if usage != nil && (preferAudio || hasHTTPAudioTokens(usage) && containsAudioRatios) {
		service.PostAudioConsumeQuota(c, info, usage, "")
		return
	}
	service.PostTextConsumeQuota(c, info, usage, nil)
}

func hasHTTPAudioTokens(usage *dto.Usage) bool {
	return usage != nil && (usage.CompletionTokenDetails.AudioTokens > 0 || usage.PromptTokensDetails.AudioTokens > 0)
}
