package sora

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemixRequestURLUsesEscapedPersistedUpstreamIdentity(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://provider.example/"}
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{
		Action:               constant.TaskActionRemix,
		OriginTaskID:         "task-public",
		OriginUpstreamTaskID: "provider/task?version=1#clip",
	}}

	requestURL, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	assert.Equal(
		t,
		"https://provider.example/v1/videos/provider%2Ftask%3Fversion=1%23clip/remix",
		requestURL,
	)
	assert.NotContains(t, requestURL, "task-public")

	info.OriginUpstreamTaskID = ""
	_, err = adaptor.BuildRequestURL(info)
	require.Error(t, err)
}
