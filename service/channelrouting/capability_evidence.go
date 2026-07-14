package channelrouting

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	routingcapability "github.com/QuantumNous/new-api/pkg/routing_capability"
)

var ErrRoutingCapabilityOverrideInvalid = errors.New("routing capability override is invalid")

type routingCapabilityEvidence struct {
	revision              uint64
	requestKindsKnown     RequestKindMask
	requestKindsSupported RequestKindMask
	capabilitiesKnown     RequestCapabilityMask
	capabilitiesSupported RequestCapabilityMask
}

type routingCapabilityOverrides struct {
	revision uint64
	default_ *routingCapabilityEvidence
	models   map[string]routingCapabilityEvidence
}

func (overrides routingCapabilityOverrides) forModel(modelName string) (routingCapabilityEvidence, bool) {
	modelName = strings.TrimSpace(modelName)
	if evidence, ok := overrides.models[modelName]; ok {
		return evidence, true
	}
	if overrides.default_ == nil {
		return routingCapabilityEvidence{}, false
	}
	return *overrides.default_, true
}

func resolveCapabilityRoutingPolicy(policyJSON []byte) (bool, error) {
	enabled, err := routingcapability.ParsePoolPolicy(policyJSON)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrSnapshotPolicyReference, err)
	}
	return enabled, nil
}

func parseRoutingCapabilityOverrides(overridesJSON []byte) (routingCapabilityOverrides, error) {
	document, err := routingcapability.ParseMemberOverrides(overridesJSON)
	if err != nil {
		return routingCapabilityOverrides{}, fmt.Errorf("%w: %v", ErrRoutingCapabilityOverrideInvalid, err)
	}
	if !document.Present {
		return routingCapabilityOverrides{}, nil
	}
	result := routingCapabilityOverrides{revision: document.Revision}
	if document.Default != nil {
		evidence, evidenceErr := routingCapabilityEvidenceFromDocument(document.Revision, *document.Default)
		if evidenceErr != nil {
			return routingCapabilityOverrides{}, evidenceErr
		}
		result.default_ = &evidence
	}
	result.models = make(map[string]routingCapabilityEvidence, len(document.Models))
	for modelName, rawEvidence := range document.Models {
		evidence, evidenceErr := routingCapabilityEvidenceFromDocument(document.Revision, rawEvidence)
		if evidenceErr != nil {
			return routingCapabilityOverrides{}, evidenceErr
		}
		result.models[modelName] = evidence
	}
	return result, nil
}

func routingCapabilityEvidenceFromDocument(
	revision uint64,
	document routingcapability.Evidence,
) (routingCapabilityEvidence, error) {
	var evidence routingCapabilityEvidence
	evidence.revision = revision
	for _, name := range document.RequestKindsKnown {
		mask, ok := RequestKind(name).Mask()
		if !ok {
			return routingCapabilityEvidence{}, ErrRoutingCapabilityOverrideInvalid
		}
		evidence.requestKindsKnown |= mask
	}
	for _, name := range document.RequestKindsSupported {
		mask, ok := RequestKind(name).Mask()
		if !ok {
			return routingCapabilityEvidence{}, ErrRoutingCapabilityOverrideInvalid
		}
		evidence.requestKindsSupported |= mask
	}
	capabilities := map[string]RequestCapabilityMask{
		"tools": RequestCapabilityTools, "json_schema": RequestCapabilityJSONSchema,
		"vision": RequestCapabilityVision, "audio_input": RequestCapabilityAudioInput,
		"audio_output": RequestCapabilityAudioOutput, "image_output": RequestCapabilityImageOutput,
		"file_input": RequestCapabilityFileInput, "video_input": RequestCapabilityVideoInput,
		"video_output": RequestCapabilityVideoOutput, "realtime": RequestCapabilityRealtime,
		"stateful": RequestCapabilityStateful,
	}
	for _, name := range document.CapabilitiesKnown {
		evidence.capabilitiesKnown |= capabilities[name]
	}
	for _, name := range document.CapabilitiesSupported {
		evidence.capabilitiesSupported |= capabilities[name]
	}
	return evidence, nil
}

func resolveMemberCapabilityEvidence(overridesJSON []byte) (routingCapabilityOverrides, error) {
	overrides, err := parseRoutingCapabilityOverrides(overridesJSON)
	if err != nil {
		return routingCapabilityOverrides{}, fmt.Errorf("%w: %v", ErrSnapshotPolicyReference, err)
	}
	return overrides, nil
}

func resolveRoutingCapabilityEvidence(
	_ int,
	modelName string,
	overrides routingCapabilityOverrides,
) routingCapabilityEvidence {
	if evidence, ok := overrides.forModel(modelName); ok {
		return evidence
	}
	return routingCapabilityEvidence{}
}

func ValidateRoutingCapabilityOverrides(document model.RoutingPolicyDocument) error {
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		if _, err := routingcapability.ParsePoolPolicy(pool.Policy); err != nil {
			return fmt.Errorf("%w: pool %d: %v", ErrRoutingCapabilityOverrideInvalid, pool.PoolID, err)
		}
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			if _, err := routingcapability.ParseMemberOverrides(member.Overrides); err != nil {
				return fmt.Errorf("%w: member %d: %v", ErrRoutingCapabilityOverrideInvalid, member.MemberID, err)
			}
		}
	}
	return nil
}
