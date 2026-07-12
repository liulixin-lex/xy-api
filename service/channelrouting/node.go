package channelrouting

import (
	"strings"

	"github.com/google/uuid"
)

var routingNodeEpochID = strings.ReplaceAll(uuid.NewString(), "-", "")

func NodeEpochID() string {
	return routingNodeEpochID
}
