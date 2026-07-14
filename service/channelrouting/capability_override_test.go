package channelrouting

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingCapabilityOverridesResolveDefaultAndModelEvidence(t *testing.T) {
	overrides, err := parseRoutingCapabilityOverrides(mustMarshalRoutingCapabilityTest(t, map[string]any{
		"capabilities": map[string]any{
			"revision": 7,
			"default": map[string]any{
				"request_kinds_known":     []string{"chat_completions", "responses"},
				"request_kinds_supported": []string{"chat_completions"},
				"capabilities_known":      []string{"tools", "vision"},
				"capabilities_supported":  []string{"tools"},
			},
			"models": map[string]any{
				"gpt-vision": map[string]any{
					"request_kinds_known":     []string{"chat_completions", "responses"},
					"request_kinds_supported": []string{"chat_completions", "responses"},
					"capabilities_known":      []string{"tools", "vision", "json_schema"},
					"capabilities_supported":  []string{"tools", "vision", "json_schema"},
				},
			},
		},
	}))
	require.NoError(t, err)

	defaultEvidence := resolveRoutingCapabilityEvidence(constant.ChannelTypeOpenAI, "gpt-text", overrides)
	assert.Equal(t, uint64(7), defaultEvidence.revision)
	assert.Equal(t, RequestKindMaskChatCompletions|RequestKindMaskResponses, defaultEvidence.requestKindsKnown)
	assert.Equal(t, RequestKindMaskChatCompletions, defaultEvidence.requestKindsSupported)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityVision, defaultEvidence.capabilitiesKnown)
	assert.Equal(t, RequestCapabilityTools, defaultEvidence.capabilitiesSupported)

	vision := resolveRoutingCapabilityEvidence(constant.ChannelTypeOpenAI, "gpt-vision", overrides)
	assert.Equal(t, RequestKindMaskChatCompletions|RequestKindMaskResponses, vision.requestKindsSupported)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityVision|RequestCapabilityJSONSchema, vision.capabilitiesSupported)
}

func TestRoutingCapabilityOverridesRejectInvalidEvidence(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
	}{
		{
			name: "missing revision",
			overrides: map[string]any{"capabilities": map[string]any{
				"default": map[string]any{},
			}},
		},
		{
			name: "supported kind is not known",
			overrides: map[string]any{"capabilities": map[string]any{
				"revision": 1,
				"default": map[string]any{
					"request_kinds_known":     []string{"chat_completions"},
					"request_kinds_supported": []string{"responses"},
				},
			}},
		},
		{
			name: "supported capability is not known",
			overrides: map[string]any{"capabilities": map[string]any{
				"revision": 1,
				"default": map[string]any{
					"capabilities_known":     []string{"tools"},
					"capabilities_supported": []string{"vision"},
				},
			}},
		},
		{
			name: "unknown capability",
			overrides: map[string]any{"capabilities": map[string]any{
				"revision": 1,
				"default": map[string]any{
					"capabilities_known":     []string{"telepathy"},
					"capabilities_supported": []string{},
				},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRoutingCapabilityOverrides(mustMarshalRoutingCapabilityTest(t, test.overrides))
			assert.ErrorIs(t, err, ErrRoutingCapabilityOverrideInvalid)
		})
	}
}

func TestValidateRoutingCapabilityOverridesChecksWholePolicy(t *testing.T) {
	document := model.RoutingPolicyDocument{SchemaVersion: model.RoutingPolicySchemaVersion, Pools: []model.RoutingPolicyPoolContent{{
		PoolID: 1, GroupName: "default", DisplayName: "Default",
		DeploymentStage: model.RoutingDeploymentStageObserve,
		PolicyProfile:   model.RoutingPolicyProfileBalanced,
		Policy:          mustMarshalRoutingCapabilityTest(t, map[string]any{}),
		Members: []model.RoutingPolicyMemberContent{{
			MemberID: 11, ChannelID: 101, Enabled: true, Weight: 100,
			Overrides: mustMarshalRoutingCapabilityTest(t, map[string]any{"capabilities": map[string]any{
				"revision": 1,
				"default": map[string]any{
					"request_kinds_known":     []string{"chat_completions"},
					"request_kinds_supported": []string{"chat_completions"},
				},
			}}),
		}},
	}}}
	require.NoError(t, ValidateRoutingCapabilityOverrides(document))

	document.Pools[0].Members[0].Overrides = mustMarshalRoutingCapabilityTest(t, map[string]any{
		"capabilities": map[string]any{"revision": 1, "default": map[string]any{
			"request_kinds_known": []string{"unknown"}, "request_kinds_supported": []string{},
		}},
	})
	assert.ErrorIs(t, ValidateRoutingCapabilityOverrides(document), ErrRoutingCapabilityOverrideInvalid)
}

func TestMissingRoutingCapabilityEvidenceRemainsUnknown(t *testing.T) {
	chat := resolveRoutingCapabilityEvidence(constant.ChannelTypeOpenAI, "gpt-test", routingCapabilityOverrides{})
	assert.Zero(t, chat.requestKindsKnown)
	assert.Zero(t, chat.requestKindsSupported)
	assert.Zero(t, chat.capabilitiesKnown)

	image := resolveRoutingCapabilityEvidence(constant.ChannelTypeOpenAI, "gpt-image-1", routingCapabilityOverrides{})
	assert.Zero(t, image.requestKindsKnown)
	assert.Zero(t, image.requestKindsSupported)
	assert.Zero(t, image.capabilitiesKnown)
}

func mustMarshalRoutingCapabilityTest(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return encoded
}
