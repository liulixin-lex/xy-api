package channelrouting

const (
	ExclusionReasonRequestKindUnknown     = "request_kind_unknown"
	ExclusionReasonRequestKindUnsupported = "request_kind_unsupported"
	ExclusionReasonCapabilityUnknown      = "required_capability_unknown"
	ExclusionReasonCapabilityUnsupported  = "required_capability_unsupported"
)

func (snapshot ModelSnapshot) ValidCapabilityState() bool {
	return snapshot.RequestKindsKnown&^requestKindMaskAll == 0 &&
		snapshot.RequestKindsSupported&^requestKindMaskAll == 0 &&
		snapshot.RequestKindsSupported&^snapshot.RequestKindsKnown == 0 &&
		snapshot.CapabilitiesKnown&^requestCapabilityMaskAll == 0 &&
		snapshot.CapabilitiesSupported&^requestCapabilityMaskAll == 0 &&
		snapshot.CapabilitiesSupported&^snapshot.CapabilitiesKnown == 0
}

func requestCapabilityExclusionReason(profile RequestProfile, snapshot ModelSnapshot) string {
	if profile.SchemaVersion != RequestProfileSchemaV2 {
		return ""
	}
	kindMask, ok := profile.RequestKind.Mask()
	if !ok || snapshot.RequestKindsKnown&^requestKindMaskAll != 0 ||
		snapshot.RequestKindsSupported&^requestKindMaskAll != 0 ||
		snapshot.RequestKindsSupported&^snapshot.RequestKindsKnown != 0 ||
		snapshot.RequestKindsKnown&kindMask != kindMask {
		return ExclusionReasonRequestKindUnknown
	}
	if snapshot.RequestKindsSupported&kindMask != kindMask {
		return ExclusionReasonRequestKindUnsupported
	}
	required := profile.RequiredCapabilities
	if snapshot.CapabilitiesKnown&^requestCapabilityMaskAll != 0 ||
		snapshot.CapabilitiesSupported&^requestCapabilityMaskAll != 0 ||
		snapshot.CapabilitiesSupported&^snapshot.CapabilitiesKnown != 0 ||
		snapshot.CapabilitiesKnown&required != required {
		return ExclusionReasonCapabilityUnknown
	}
	if snapshot.CapabilitiesSupported&required != required {
		return ExclusionReasonCapabilityUnsupported
	}
	return ""
}

func isRequestCapabilityExclusion(reason string) bool {
	switch reason {
	case ExclusionReasonRequestKindUnknown,
		ExclusionReasonRequestKindUnsupported,
		ExclusionReasonCapabilityUnknown,
		ExclusionReasonCapabilityUnsupported:
		return true
	default:
		return false
	}
}
