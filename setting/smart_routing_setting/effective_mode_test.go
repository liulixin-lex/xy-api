package smart_routing_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveEffectiveModeAppliesGlobalCapabilityCeiling(t *testing.T) {
	tests := []struct {
		name       string
		setting    SmartRoutingSetting
		want       EffectiveMode
		legacy     bool
		observe    bool
		shadow     bool
		balanced   bool
		probe      bool
		attempt    bool
		affinity   bool
		enterprise bool
	}{
		{name: "disabled", setting: SmartRoutingSetting{Enabled: false, Mode: ModeEnterpriseSLO}, want: EffectiveModeLegacy, legacy: true},
		{name: "observe", setting: SmartRoutingSetting{Enabled: true, Mode: ModeObserve}, want: EffectiveModeObserve, legacy: true, observe: true},
		{name: "shadow", setting: SmartRoutingSetting{Enabled: true, Mode: ModeShadow}, want: EffectiveModeShadow, legacy: true, shadow: true},
		{name: "balanced", setting: SmartRoutingSetting{Enabled: true, Mode: ModeBalanced}, want: EffectiveModeBalanced, balanced: true, probe: true, attempt: true, affinity: true},
		{name: "enterprise", setting: SmartRoutingSetting{Enabled: true, Mode: ModeEnterpriseSLO}, want: EffectiveModeEnterpriseSLO, balanced: true, probe: true, attempt: true, affinity: true, enterprise: true},
		{name: "invalid fails closed", setting: SmartRoutingSetting{Enabled: true, Mode: "future"}, want: EffectiveModeLegacy, legacy: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := ResolveEffectiveMode(tt.setting)
			assert.Equal(t, tt.want, mode)
			assert.Equal(t, tt.legacy, mode.UsesLegacyDataPlane())
			assert.Equal(t, tt.observe, mode.RecordsObserveDecision())
			assert.Equal(t, tt.shadow, mode.RecordsShadowDecision())
			assert.Equal(t, tt.balanced, mode.AllowsBalancedDataPlane())
			assert.Equal(t, tt.probe, mode.AllowsActiveProbe())
			assert.Equal(t, tt.attempt, mode.AllowsAttemptControl())
			assert.Equal(t, tt.affinity, mode.AllowsAffinityRouting())
			assert.Equal(t, tt.enterprise, mode.AllowsEnterpriseFeatures())
		})
	}
}
