package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingTrafficAdmissibleFailsClosedForClaudeCodeOnlyChannels(t *testing.T) {
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	routinghotcache.ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 11, TrafficClass: model.RoutingChannelTrafficClassClaudeCodeOnly},
	}, time.Now().Unix())
	gin.SetMode(gin.TestMode)

	for _, test := range []struct {
		name      string
		class     channelrouting.RequestTrafficClass
		channelID int
		want      bool
	}{
		{name: "legacy unknown restricted", class: channelrouting.RequestTrafficClassLegacy, channelID: 11},
		{name: "explicit unknown restricted", class: channelrouting.RequestTrafficClassUnknown, channelID: 11},
		{name: "standard restricted", class: channelrouting.RequestTrafficClassStandard, channelID: 11},
		{name: "Claude Code admitted", class: channelrouting.RequestTrafficClassClaudeCode, channelID: 11, want: true},
		{name: "ordinary channel admitted", class: channelrouting.RequestTrafficClassStandard, channelID: 12, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			common.SetContextKey(ctx, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileInput{
				TrafficClass: test.class,
			})
			admissible, err := ChannelRoutingTrafficAdmissible(ctx, test.channelID)
			require.NoError(t, err)
			assert.Equal(t, test.want, admissible)
		})
	}
}

func TestFilterRoutingTrafficAdmissibleChannelsUsesLivePolicy(t *testing.T) {
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	routinghotcache.ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 21, TrafficClass: model.RoutingChannelTrafficClassClaudeCodeOnly},
	}, time.Now().Unix())
	channels := []*model.Channel{{Id: 21}, {Id: 22}}

	standard, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(standard, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileInput{
		TrafficClass: channelrouting.RequestTrafficClassStandard,
	})
	filtered, err := filterRoutingTrafficAdmissibleChannels(standard, channels)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, 22, filtered[0].Id)

	claudeCode, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(claudeCode, constant.ContextKeyRoutingRequestProfile, channelrouting.RequestProfileInput{
		TrafficClass: channelrouting.RequestTrafficClassClaudeCode,
	})
	filtered, err = filterRoutingTrafficAdmissibleChannels(claudeCode, channels)
	require.NoError(t, err)
	assert.Equal(t, channels, filtered)
}
