package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
)

func TestRoutingAgentHandlerRequiresAgentEnabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	handler := routingAgentHandler{}

	assert.Equal(t, model.SystemTaskTypeRoutingAgent, handler.Type())
	assert.False(t, handler.Enabled())

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:      true,
		Mode:         smart_routing_setting.ModeEnterpriseSLO,
		AgentEnabled: true,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	assert.True(t, handler.Enabled())
	assert.Equal(t, time.Hour, handler.Interval())
}
