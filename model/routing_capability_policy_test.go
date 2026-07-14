package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRoutingPolicyDocumentValidatesCapabilityConfiguration(t *testing.T) {
	base := RoutingPolicyDocument{SchemaVersion: RoutingPolicySchemaVersion, Pools: []RoutingPolicyPoolContent{{
		PoolID: 1, GroupName: "default", DisplayName: "Default",
		DeploymentStage: RoutingDeploymentStageShadow, PolicyProfile: RoutingPolicyProfileBalanced,
		Policy: json.RawMessage(`{"capability_routing":{"enabled":true,"schema_version":1}}`),
		Members: []RoutingPolicyMemberContent{{
			MemberID: 11, ChannelID: 101, Enabled: true, Weight: 100,
			Overrides: json.RawMessage(`{"capabilities":{"revision":3,"default":{
				"request_kinds_known":["chat_completions"],
				"request_kinds_supported":["chat_completions"],
				"capabilities_known":["tools"],
				"capabilities_supported":["tools"]
			}}}`),
		}},
	}}}

	_, _, err := NormalizeRoutingPolicyDocument(base)
	require.NoError(t, err)

	invalidPolicy := base
	invalidPolicy.Pools = append([]RoutingPolicyPoolContent(nil), base.Pools...)
	invalidPolicy.Pools[0].Policy = json.RawMessage(`{"capability_routing":{"enabled":true}}`)
	_, _, err = NormalizeRoutingPolicyDocument(invalidPolicy)
	assert.ErrorIs(t, err, ErrRoutingPolicyInvalid)

	invalidOverride := base
	invalidOverride.Pools = append([]RoutingPolicyPoolContent(nil), base.Pools...)
	invalidOverride.Pools[0].Members = append([]RoutingPolicyMemberContent(nil), base.Pools[0].Members...)
	invalidOverride.Pools[0].Members[0].Overrides = json.RawMessage(`{"capabilities":{"revision":3,"default":{
		"request_kinds_known":["chat_completions"],
		"request_kinds_supported":["responses"]
	}}}`)
	_, _, err = NormalizeRoutingPolicyDocument(invalidOverride)
	assert.ErrorIs(t, err, ErrRoutingPolicyInvalid)
}
