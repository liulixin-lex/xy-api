package controller

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

var routingClaudeCodeUAPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)
var routingClaudeCodeLegacyUserIDPattern = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([a-fA-F0-9-]*)_session_([a-fA-F0-9-]{36})$`)

var routingClaudeCodeSystemPrompts = []string{
	"You are Claude Code, Anthropic's official CLI for Claude.",
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
	"You are a file search specialist for Claude Code, Anthropic's official CLI for Claude.",
	"You are a helpful AI assistant tasked with summarizing conversations.",
	"You are an interactive CLI tool that helps users",
}

func classifyRoutingRequestTraffic(
	c *gin.Context,
	relayFormat types.RelayFormat,
	request *dto.ClaudeRequest,
) channelrouting.RequestTrafficClass {
	if relayFormat != types.RelayFormatClaude || c == nil || c.Request == nil || request == nil {
		return channelrouting.RequestTrafficClassStandard
	}
	if !routingClaudeCodeUAPattern.MatchString(c.GetHeader("User-Agent")) {
		return channelrouting.RequestTrafficClassStandard
	}

	path := c.Request.URL.Path
	if strings.HasSuffix(path, "/messages/count_tokens") || !strings.Contains(path, "messages") {
		return channelrouting.RequestTrafficClassClaudeCode
	}
	if request.MaxTokens != nil && *request.MaxTokens == 1 && strings.Contains(strings.ToLower(request.Model), "haiku") {
		return channelrouting.RequestTrafficClassClaudeCode
	}
	if !routingClaudeCodeSystemPrompt(request) ||
		strings.TrimSpace(c.GetHeader("X-App")) == "" ||
		strings.TrimSpace(c.GetHeader("anthropic-beta")) == "" ||
		strings.TrimSpace(c.GetHeader("anthropic-version")) == "" ||
		!routingClaudeCodeMetadataUserID(request.Metadata) {
		return channelrouting.RequestTrafficClassUnknown
	}
	return channelrouting.RequestTrafficClassClaudeCode
}

func routingClaudeCodeSystemPrompt(request *dto.ClaudeRequest) bool {
	if request == nil || strings.TrimSpace(request.Model) == "" || request.IsStringSystem() {
		return false
	}
	for _, entry := range request.ParseSystem() {
		text := entry.GetText()
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "x-anthropic-billing-header") && strings.Contains(text, "cc_entrypoint=") {
			return true
		}
		for _, template := range routingClaudeCodeSystemPrompts {
			if routingClaudeCodeDiceCoefficient(text, template) >= 0.5 {
				return true
			}
		}
	}
	return false
}

func routingClaudeCodeMetadataUserID(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var metadata dto.ClaudeMetadata
	if common.Unmarshal(raw, &metadata) != nil {
		return false
	}
	userID := strings.TrimSpace(metadata.UserId)
	if userID == "" {
		return false
	}
	if strings.HasPrefix(userID, "{") {
		var value struct {
			DeviceID  string `json:"device_id"`
			SessionID string `json:"session_id"`
		}
		return common.UnmarshalJsonStr(userID, &value) == nil && value.DeviceID != "" && value.SessionID != ""
	}
	return routingClaudeCodeLegacyUserIDPattern.MatchString(userID)
}

func routingClaudeCodeDiceCoefficient(left string, right string) float64 {
	leftBigrams := routingClaudeCodeBigrams(strings.Join(strings.Fields(left), " "))
	rightBigrams := routingClaudeCodeBigrams(strings.Join(strings.Fields(right), " "))
	if len(leftBigrams) == 0 || len(rightBigrams) == 0 {
		return 0
	}
	intersection := 0
	leftTotal := 0
	rightTotal := 0
	for bigram, leftCount := range leftBigrams {
		leftTotal += leftCount
		if rightCount := rightBigrams[bigram]; rightCount > 0 {
			intersection += min(leftCount, rightCount)
		}
	}
	for _, count := range rightBigrams {
		rightTotal += count
	}
	return float64(2*intersection) / float64(leftTotal+rightTotal)
}

func routingClaudeCodeBigrams(value string) map[string]int {
	runes := []rune(strings.ToLower(value))
	bigrams := make(map[string]int, max(len(runes)-1, 0))
	for index := 0; index+1 < len(runes); index++ {
		bigrams[string(runes[index:index+2])]++
	}
	return bigrams
}
