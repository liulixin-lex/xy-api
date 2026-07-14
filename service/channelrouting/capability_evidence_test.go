package channelrouting

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCapabilityRoutingPolicyRequiresExplicitVersionedEnablement(t *testing.T) {
	enabled, err := resolveCapabilityRoutingPolicy(nil)
	require.NoError(t, err)
	assert.False(t, enabled)

	enabled, err = resolveCapabilityRoutingPolicy([]byte(`{"future_option":true}`))
	require.NoError(t, err)
	assert.False(t, enabled)

	enabled, err = resolveCapabilityRoutingPolicy([]byte(`{
		"capability_routing":{"enabled":true,"schema_version":1}
	}`))
	require.NoError(t, err)
	assert.True(t, enabled)

	invalid := []string{
		`{"capability_routing":true}`,
		`{"capability_routing":{"enabled":true}}`,
		`{"capability_routing":{"enabled":true,"schema_version":2}}`,
		`{"capability_routing":{"enabled":true,"schema_version":1,"unknown":1}}`,
	}
	for _, raw := range invalid {
		_, err := resolveCapabilityRoutingPolicy([]byte(raw))
		assert.ErrorIs(t, err, ErrSnapshotPolicyReference)
	}
}

func TestResolveMemberCapabilityEvidenceValidatesNamesAndKnownSubsets(t *testing.T) {
	evidence, err := resolveMemberCapabilityEvidence([]byte(`{
		"region":"primary",
		"capabilities":{
			"revision":7,
			"default":{
				"request_kinds_known":["chat_completions","responses"],
				"request_kinds_supported":["chat_completions","responses"],
				"capabilities_known":["tools","json_schema","vision"],
				"capabilities_supported":["tools","vision"]
			},
			"models":{
				"gpt-json":{
					"request_kinds_known":["responses"],
					"request_kinds_supported":["responses"],
					"capabilities_known":["tools","json_schema"],
					"capabilities_supported":["tools","json_schema"]
				}
			}
		}
	}`))
	require.NoError(t, err)

	defaultEvidence, ok := evidence.forModel("gpt-text")
	require.True(t, ok)
	assert.Equal(t, uint64(7), defaultEvidence.revision)
	assert.Equal(t, RequestKindMaskChatCompletions|RequestKindMaskResponses, defaultEvidence.requestKindsKnown)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityJSONSchema|RequestCapabilityVision, defaultEvidence.capabilitiesKnown)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityVision, defaultEvidence.capabilitiesSupported)

	modelEvidence, ok := evidence.forModel("gpt-json")
	require.True(t, ok)
	assert.Equal(t, RequestKindMaskResponses, modelEvidence.requestKindsSupported)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityJSONSchema, modelEvidence.capabilitiesSupported)

	invalid := []string{
		`{"capabilities":{"revision":0,"default":{"request_kinds_known":[],"request_kinds_supported":[],"capabilities_known":[],"capabilities_supported":[]}}}`,
		`{"capabilities":{"revision":1,"default":{"request_kinds_known":["unknown"],"request_kinds_supported":[],"capabilities_known":[],"capabilities_supported":[]}}}`,
		`{"capabilities":{"revision":1,"default":{"request_kinds_known":["responses"],"request_kinds_supported":["chat_completions"],"capabilities_known":[],"capabilities_supported":[]}}}`,
		`{"capabilities":{"revision":1,"default":{"request_kinds_known":[],"request_kinds_supported":[],"capabilities_known":["tools"],"capabilities_supported":["json_schema"]}}}`,
		`{"capabilities":{"revision":1,"default":{"request_kinds_known":[],"request_kinds_supported":[],"capabilities_known":["tools","tools"],"capabilities_supported":[]}}}`,
		`{"capabilities":{"revision":1,"default":{"request_kinds_known":[],"request_kinds_supported":[],"capabilities_known":[],"capabilities_supported":[],"unknown":true}}}`,
	}
	for _, raw := range invalid {
		_, err := resolveMemberCapabilityEvidence([]byte(raw))
		assert.ErrorIs(t, err, ErrSnapshotPolicyReference)
	}
}

func TestRefreshSnapshotPublishesVersionedCapabilityEvidenceBehindPoolGate(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, db.Create(&model.Channel{
		Id: 741, Name: "capability-source", Type: 1, Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-json,gpt-text", Priority: &priority, Weight: &weight,
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	head, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	document, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, document.Pools, 1)
	require.Len(t, document.Pools[0].Members, 1)
	document.Pools[0].DeploymentStage = model.RoutingDeploymentStageShadow
	document.Pools[0].Policy = json.RawMessage(`{
		"capability_routing":{"enabled":true,"schema_version":1}
	}`)
	document.Pools[0].Members[0].Overrides = json.RawMessage(`{
		"capabilities":{
			"revision":9,
			"default":{
				"request_kinds_known":["chat_completions"],
				"request_kinds_supported":["chat_completions"],
				"capabilities_known":["tools","json_schema"],
				"capabilities_supported":["tools"]
			},
			"models":{
				"gpt-json":{
					"request_kinds_known":["responses"],
					"request_kinds_supported":["responses"],
					"capabilities_known":["tools","json_schema"],
					"capabilities_supported":["tools","json_schema"]
				}
			}
		}
	}`)
	_, err = model.PublishRoutingPolicyRevisionDBContext(
		context.Background(),
		db,
		head.CurrentRevision,
		document,
		model.RoutingPolicyActivationSpec{
			Stage: model.RoutingDeploymentStageShadow, ActorID: 7, Reason: "capability_evidence_test",
		},
	)
	require.NoError(t, err)

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Pools, 1)
	pool := view.Pools[0]
	assert.True(t, pool.CapabilityRoutingEnabled)
	require.Len(t, pool.Members, 1)
	require.Len(t, pool.Members[0].Models, 2)

	models := make(map[string]ModelSnapshot, len(pool.Members[0].Models))
	for _, snapshot := range pool.Members[0].Models {
		models[snapshot.ModelName] = snapshot
	}
	assert.Equal(t, uint64(9), models["gpt-json"].CapabilityRevision)
	assert.Equal(t, RequestKindMaskResponses, models["gpt-json"].RequestKindsSupported)
	assert.Equal(t, RequestCapabilityTools|RequestCapabilityJSONSchema, models["gpt-json"].CapabilitiesSupported)
	assert.Equal(t, RequestKindMaskChatCompletions, models["gpt-text"].RequestKindsSupported)
	assert.Equal(t, RequestCapabilityTools, models["gpt-text"].CapabilitiesSupported)
}

func TestCapabilityEvidenceDoesNotChangeRoutingUntilPoolGateIsEnabled(t *testing.T) {
	balancedPolicy, err := normalizeBalancedPoolPolicy(defaultBalancedPoolPolicy(model.RoutingPolicyProfileBalanced))
	require.NoError(t, err)
	pool := PoolSnapshot{
		ID: 1, GroupName: "default", SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		BalancedPolicy: balancedPolicy,
	}
	member := PoolMemberSnapshot{
		ID: 11, PoolID: 1, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 10, LegacyWeight: 10,
	}
	channel := ChannelSnapshot{ID: 101, Status: common.ChannelStatusEnabled}
	profile := requestProfileV2ForCapabilityTest(t, RequestKindResponses, RequestCapabilityJSONSchema)
	kindMask, ok := RequestKindResponses.Mask()
	require.True(t, ok)
	observation := ModelSnapshot{
		ModelName: "gpt-test", RequestKindsKnown: kindMask, RequestKindsSupported: kindMask,
		CapabilitiesKnown: RequestCapabilityJSONSchema,
	}

	shadowCandidate, _, err := shadowCandidateFromSnapshot(
		pool, member, observation, channel, profile,
		pool.SelectorPolicy.selectorSettings(1_000, 1_000_000, 7, false),
	)
	require.NoError(t, err)
	assert.Empty(t, shadowCandidate.RequestExclusionReason)
	balancedCandidate, err := balancedCandidateFromSnapshot(
		pool, member, observation, channel, profile,
		pool.BalancedPolicy.settings(time.Unix(1_000, 0), 7, 101, false),
	)
	require.NoError(t, err)
	assert.Empty(t, balancedCandidate.HardExclusionReason)

	pool.CapabilityRoutingEnabled = true
	shadowCandidate, _, err = shadowCandidateFromSnapshot(
		pool, member, observation, channel, profile,
		pool.SelectorPolicy.selectorSettings(1_000, 1_000_000, 7, false),
	)
	require.NoError(t, err)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, shadowCandidate.RequestExclusionReason)
	balancedCandidate, err = balancedCandidateFromSnapshot(
		pool, member, observation, channel, profile,
		pool.BalancedPolicy.settings(time.Unix(1_000, 0), 7, 101, false),
	)
	require.NoError(t, err)
	assert.Equal(t, ExclusionReasonCapabilityUnsupported, balancedCandidate.HardExclusionReason)
}
