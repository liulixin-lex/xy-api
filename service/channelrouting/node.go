package channelrouting

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var routingNodeEpochID = strings.ReplaceAll(uuid.NewString(), "-", "")

func NodeEpochID() string {
	return routingNodeEpochID
}

// StableNodeID is the operator-provided identity used for regional quorum.
// Process epochs are intentionally never promoted to stable voters.
func StableNodeID() (string, bool) {
	value := strings.TrimSpace(os.Getenv("ROUTING_NODE_ID"))
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return "", false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return "", false
	}
	return value, true
}
