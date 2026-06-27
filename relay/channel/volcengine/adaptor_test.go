package volcengine

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRequestURLUsesDoubaoCodingPlanOpenAIBaseURL(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: "doubao-coding-plan",
		},
	}

	requestURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)

	assert.Equal(t, "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions", requestURL)
}
