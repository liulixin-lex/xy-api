package hailuo

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToRequestPayloadRejectsMetadataModelOverride(t *testing.T) {
	adaptor := &TaskAdaptor{}
	_, err := adaptor.convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt: "animate", Duration: 6,
		Metadata: map[string]any{"model": "another-priced-model"},
	}, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "MiniMax-Hailuo-2.3"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot override model")
}
