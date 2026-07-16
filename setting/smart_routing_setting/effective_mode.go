package smart_routing_setting

// EffectiveMode is the single global capability ceiling for channel routing.
// Published policy stages may narrow behavior further, but can never enable a
// data-plane capability that this mode does not allow.
type EffectiveMode string

const (
	EffectiveModeLegacy        EffectiveMode = "legacy"
	EffectiveModeObserve       EffectiveMode = "observe"
	EffectiveModeShadow        EffectiveMode = "shadow"
	EffectiveModeBalanced      EffectiveMode = "balanced"
	EffectiveModeEnterpriseSLO EffectiveMode = "enterprise_slo"
)

func ResolveEffectiveMode(setting SmartRoutingSetting) EffectiveMode {
	if !setting.Enabled {
		return EffectiveModeLegacy
	}
	switch setting.Mode {
	case ModeObserve:
		return EffectiveModeObserve
	case ModeShadow:
		return EffectiveModeShadow
	case ModeBalanced:
		return EffectiveModeBalanced
	case ModeEnterpriseSLO:
		return EffectiveModeEnterpriseSLO
	default:
		return EffectiveModeLegacy
	}
}

func CurrentEffectiveMode() EffectiveMode {
	return ResolveEffectiveMode(GetSetting())
}

func (mode EffectiveMode) UsesLegacyDataPlane() bool {
	return mode == EffectiveModeLegacy || mode == EffectiveModeObserve || mode == EffectiveModeShadow
}

func (mode EffectiveMode) RecordsObserveDecision() bool {
	return mode == EffectiveModeObserve
}

func (mode EffectiveMode) RecordsShadowDecision() bool {
	return mode == EffectiveModeShadow
}

func (mode EffectiveMode) AllowsBalancedDataPlane() bool {
	return mode == EffectiveModeBalanced || mode == EffectiveModeEnterpriseSLO
}

func (mode EffectiveMode) AllowsActiveProbe() bool {
	return mode.AllowsBalancedDataPlane()
}

func (mode EffectiveMode) AllowsAttemptControl() bool {
	return mode.AllowsBalancedDataPlane()
}

func (mode EffectiveMode) AllowsAffinityRouting() bool {
	return mode.AllowsBalancedDataPlane()
}

func (mode EffectiveMode) AllowsEnterpriseFeatures() bool {
	return mode == EffectiveModeEnterpriseSLO
}
