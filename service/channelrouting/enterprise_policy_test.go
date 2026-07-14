package channelrouting

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnterprisePolicyRequiresStrictCapacityOnlyForEnterpriseProfile(t *testing.T) {
	policy, err := resolveEnterprisePoolPolicy(model.RoutingPolicyProfileEnterpriseSLO, json.RawMessage(`{
		"enterprise":{"capacity":{"scope":"account","rpm":10,"input_tpm":100,"output_tpm":50,
		"total_tpm":150,"inflight":2,"cost_nano_usd":1000,"lease_ttl_seconds":30,
		"guaranteed_basis_points":2500,"maximum_basis_points":7500}}
	}`))
	require.NoError(t, err)
	assert.Equal(t, CapacityModeRedisBlock, policy.Capacity.Mode)
	assert.Equal(t, EnterpriseCapacityScopeAccount, policy.Capacity.Scope)
	assert.Equal(t, int64(1_000), policy.Capacity.Limit.CostNanoUSD)
	assert.Equal(t, 30*time.Second, policy.Capacity.LeaseTTL)
	assert.Equal(t, 2_500, policy.Capacity.GuaranteedBasisPoints)
	assert.Equal(t, 7_500, policy.Capacity.MaximumBasisPoints)

	_, err = resolveEnterprisePoolPolicy(model.RoutingPolicyProfileEnterpriseSLO, json.RawMessage(`{
		"enterprise":{"capacity":{"mode":"local_soft"}}
	}`))
	assert.ErrorIs(t, err, ErrEnterprisePolicyInvalid)

	_, err = resolveEnterprisePoolPolicy(model.RoutingPolicyProfileBalanced, json.RawMessage(`{
		"enterprise":{"capacity":{"mode":"redis_strict"}}
	}`))
	assert.ErrorIs(t, err, ErrEnterprisePolicyInvalid)
}

func TestEnterpriseStrictCapacityCompilesSharedAccountFairShares(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	policy := defaultBalancedPoolPolicy(model.RoutingPolicyProfileEnterpriseSLO)
	clientModel := "gpt-test" + ratio_setting.CompactModelSuffix
	view := SnapshotView{
		Revision: 7, RuntimeGeneration: 1,
		PolicyHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ActivationID: 9, ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []PoolSnapshot{
			{
				ID: 1, GroupName: "plus", DeploymentStage: model.RoutingDeploymentStageActive,
				PolicyProfile: model.RoutingPolicyProfileEnterpriseSLO, BalancedPolicy: policy,
				CanaryPolicy: model.DefaultRoutingCanaryPolicy(),
				Members: []PoolMemberSnapshot{{
					ID: 11, PoolID: 1, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
					LegacyWeight: 100, CredentialIDs: []int{1_001},
					Models: []ModelSnapshot{{ModelName: clientModel, upstreamAccountID: 77}},
				}},
			},
			{
				ID: 2, GroupName: "pro", DeploymentStage: model.RoutingDeploymentStageActive,
				PolicyProfile: model.RoutingPolicyProfileEnterpriseSLO, BalancedPolicy: policy,
				CanaryPolicy: model.DefaultRoutingCanaryPolicy(),
				Members: []PoolMemberSnapshot{{
					ID: 22, PoolID: 2, ChannelID: 202, PhysicalStatus: common.ChannelStatusEnabled,
					LegacyWeight: 100, CredentialIDs: []int{2_002},
					Models: []ModelSnapshot{{ModelName: clientModel, upstreamAccountID: 77}},
				}},
			},
		},
		Channels: []ChannelSnapshot{
			{ID: 101, Status: common.ChannelStatusEnabled, ModelMapping: `{"gpt-test":"alias","alias":"upstream-gpt"}`},
			{ID: 202, Status: common.ChannelStatusEnabled, ModelMapping: `{"gpt-test":"alias","alias":"upstream-gpt"}`},
		},
	}
	SetSnapshotForTest(view)

	plus, err := NewRequestRoutingSession("enterprise-plus", "plus")
	require.NoError(t, err)
	request, strict, err := plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 100},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 50},
		0, false,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Equal(t, StrictCapacityKey{AccountID: 77, Model: "upstream-gpt"}, request.Key)
	assert.Equal(t, StrictCapacityDemand{RPM: 1, InputTPM: 100, OutputTPM: 50, TotalTPM: 150, Inflight: 1}, request.Demand)
	require.Len(t, request.PoolShares, 2)
	assert.Equal(t, StrictCapacityPoolShare{PoolID: 1, GuaranteedBasisPoints: 5_000, MaximumBasisPoints: 10_000}, request.PoolShares[0])
	assert.Equal(t, StrictCapacityPoolShare{PoolID: 2, GuaranteedBasisPoints: 5_000, MaximumBasisPoints: 10_000}, request.PoolShares[1])

	pro, err := NewRequestRoutingSession("enterprise-pro", "pro")
	require.NoError(t, err)
	proRequest, strict, err := pro.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 2, MemberID: 22, CredentialID: 2_002},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 100},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 50},
		0, false,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Equal(t, request.Key, proRequest.Key)
	assert.Equal(t, request.PoolShares, proRequest.PoolShares)

	unknownRequest, strict, err := plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionApplicableUnknown},
		CapacityDimensionEstimate{State: CapacityDimensionApplicableUnknown},
		0, false,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Equal(t, unknownRequest.Limit.InputTPM, unknownRequest.Demand.InputTPM)
	assert.Equal(t, unknownRequest.Limit.OutputTPM, unknownRequest.Demand.OutputTPM)
	assert.Equal(t, unknownRequest.Limit.TotalTPM, unknownRequest.Demand.TotalTPM)

	zeroRequest, strict, err := plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionNotApplicable},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown},
		0, false,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Zero(t, zeroRequest.Demand.InputTPM)
	assert.Zero(t, zeroRequest.Demand.OutputTPM)
	assert.Zero(t, zeroRequest.Demand.TotalTPM)

	_, strict, err = plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "stale-upstream",
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 100},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 50},
		0, false,
	)
	require.True(t, strict)
	assert.ErrorIs(t, err, ErrStrictCapacityConflict)

	runtime := currentSnapshot.Load()
	require.NotNil(t, runtime)
	planKey := strictCapacityPlanKey{memberID: 11, credentialID: 1_001, model: clientModel}
	plan := runtime.strictCapacityPlans[planKey]
	plan.Limit.CostNanoUSD = 1_000
	runtime.strictCapacityPlans[planKey] = plan
	costed, strict, err := plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionApplicableUnknown},
		CapacityDimensionEstimate{State: CapacityDimensionApplicableUnknown},
		0.0000005, true,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Equal(t, int64(500), costed.Demand.CostNanoUSD)

	_, strict, err = plus.StrictCapacityRequest(
		Identity{SnapshotRevision: 7, PoolID: 1, MemberID: 11, CredentialID: 1_001},
		clientModel, "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionNotApplicable},
		CapacityDimensionEstimate{State: CapacityDimensionNotApplicable},
		0, false,
	)
	require.True(t, strict)
	assert.ErrorIs(t, err, ErrEnterpriseCapacityCostUnknown)
}

func TestCapacityAdmissionFromStrictPreservesAuditableDimensions(t *testing.T) {
	clientModel := "gpt-test" + ratio_setting.CompactModelSuffix
	strict := StrictCapacityAdmission{
		Mode:   CapacityModeRedisStrict,
		Key:    StrictCapacityKey{AccountID: 7, Model: "upstream-gpt"},
		PoolID: 2, PolicyRevision: 9,
		Demand:         StrictCapacityDemand{RPM: 1, InputTPM: 100, OutputTPM: 50, TotalTPM: 150, Inflight: 1, CostNanoUSD: 25},
		Limit:          StrictCapacityLimit{RPM: 10, InputTPM: 1_000, OutputTPM: 500, TotalTPM: 1_500, Inflight: 3, CostNanoUSD: 100},
		PoolShares:     []StrictCapacityPoolShare{{PoolID: 2, GuaranteedBasisPoints: 10_000, MaximumBasisPoints: 10_000}},
		LeaseTTLMillis: 30_000, LeaseExpiresMs: time.Now().Add(30 * time.Second).UnixMilli(),
	}
	admission, err := CapacityAdmissionFromStrict(
		Identity{SnapshotRevision: 9, PoolID: 2, MemberID: 21}, clientModel, strict,
	)
	require.NoError(t, err)
	assert.Equal(t, CapacityModeRedisStrict, admission.Mode)
	assert.Equal(t, clientModel, admission.Key.Model)
	assert.Equal(t, "upstream-gpt", admission.Strict.Key.Model)
	assert.Equal(t, Demand{RPM: 1, InputTPM: 100, OutputTPM: 50, Inflight: 1}, admission.Demand)
	require.NotNil(t, admission.Strict)
	assert.Equal(t, strict.Demand, admission.Strict.Demand)
	assert.Equal(t, strict.PoolShares, admission.Strict.PoolShares)
}

func TestEnterpriseStrictCapacityCompilesPartialGuaranteesDeterministically(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	balanced := defaultBalancedPoolPolicy(model.RoutingPolicyProfileEnterpriseSLO)
	view := SnapshotView{
		Revision: 12, RuntimeGeneration: 1,
		PolicyHash: strings.Repeat("e", 64), ActivationID: 13,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []PoolSnapshot{
			{
				ID: 1, GroupName: "explicit", DeploymentStage: model.RoutingDeploymentStageActive,
				PolicyProfile: model.RoutingPolicyProfileEnterpriseSLO, BalancedPolicy: balanced,
				CanaryPolicy: model.DefaultRoutingCanaryPolicy(),
				Members: []PoolMemberSnapshot{{
					ID: 11, PoolID: 1, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
					CredentialIDs: []int{1_001}, Models: []ModelSnapshot{{ModelName: "gpt-test", upstreamAccountID: 77}},
				}},
			},
			{
				ID: 2, GroupName: "defaulted", DeploymentStage: model.RoutingDeploymentStageActive,
				PolicyProfile: model.RoutingPolicyProfileEnterpriseSLO, BalancedPolicy: balanced,
				CanaryPolicy: model.DefaultRoutingCanaryPolicy(),
				Members: []PoolMemberSnapshot{{
					ID: 22, PoolID: 2, ChannelID: 202, PhysicalStatus: common.ChannelStatusEnabled,
					CredentialIDs: []int{2_002}, Models: []ModelSnapshot{{ModelName: "gpt-test", upstreamAccountID: 77}},
				}},
			},
		},
		Channels: []ChannelSnapshot{
			{ID: 101, Status: common.ChannelStatusEnabled, ModelMapping: `{"gpt-test":"upstream-gpt"}`},
			{ID: 202, Status: common.ChannelStatusEnabled, ModelMapping: `{"gpt-test":"upstream-gpt"}`},
		},
	}
	SetSnapshotForTest(view)
	snapshot := currentSnapshot.Load()
	require.NotNil(t, snapshot)
	explicit := defaultEnterprisePoolPolicy(model.RoutingPolicyProfileEnterpriseSLO)
	explicit.Capacity.GuaranteedBasisPoints = 6_000
	explicit.Capacity.guaranteeConfigured = true
	defaulted := defaultEnterprisePoolPolicy(model.RoutingPolicyProfileEnterpriseSLO)
	snapshot.view.Pools[0].enterprisePolicy = explicit
	snapshot.view.Pools[1].enterprisePolicy = defaulted
	require.NoError(t, snapshot.compileStrictCapacityPlans())

	session, err := NewRequestRoutingSession("partial-guarantee", "explicit")
	require.NoError(t, err)
	request, strict, err := session.StrictCapacityRequest(
		Identity{SnapshotRevision: 12, PoolID: 1, MemberID: 11, CredentialID: 1_001}, "gpt-test", "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 1},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 1},
		0, false,
	)
	require.NoError(t, err)
	require.True(t, strict)
	assert.Equal(t, []StrictCapacityPoolShare{
		{PoolID: 1, GuaranteedBasisPoints: 6_000, MaximumBasisPoints: 10_000},
		{PoolID: 2, GuaranteedBasisPoints: 4_000, MaximumBasisPoints: 10_000},
	}, request.PoolShares)

	_, strict, err = session.StrictCapacityRequest(
		Identity{SnapshotRevision: 12, PoolID: 1, MemberID: 11}, "gpt-test", "upstream-gpt",
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 1},
		CapacityDimensionEstimate{State: CapacityDimensionBoundedKnown, Tokens: 1},
		0, false,
	)
	require.True(t, strict)
	assert.ErrorIs(t, err, ErrEnterpriseCapacityIdentity)

	overcommitted := defaulted
	overcommitted.Capacity.GuaranteedBasisPoints = 5_000
	overcommitted.Capacity.guaranteeConfigured = true
	snapshot.view.Pools[1].enterprisePolicy = overcommitted
	assert.ErrorIs(t, snapshot.compileStrictCapacityPlans(), ErrEnterprisePolicyInvalid)

	tooSmallMaximum := defaulted
	tooSmallMaximum.Capacity.MaximumBasisPoints = 3_000
	snapshot.view.Pools[1].enterprisePolicy = tooSmallMaximum
	assert.ErrorIs(t, snapshot.compileStrictCapacityPlans(), ErrEnterprisePolicyInvalid)
}
