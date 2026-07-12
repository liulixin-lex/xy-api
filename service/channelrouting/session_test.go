package channelrouting

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestRoutingSessionPinsOneSnapshotAndConcretePool(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	_, err := NewRequestRoutingSession("request", "default")
	assert.ErrorIs(t, err, ErrRoutingSessionUnavailable)

	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))
	for _, groupName := range []string{"", " ", "auto", "AUTO"} {
		_, err = NewRequestRoutingSession("request", groupName)
		assert.ErrorIs(t, err, ErrRoutingSessionGroupRequired)
	}
	_, err = NewRequestRoutingSession("", "default")
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)

	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	assert.Equal(t, uint64(11), session.SnapshotRevision())
	assert.Equal(t, uint64(3), session.RuntimeGeneration())
	assert.Equal(t, 29, session.PoolID())

	SetSnapshotForTest(canarySessionSnapshotForTest(12, 4, 402, 29, 201))
	plan, active, err := session.Plan(RequestRoutingPlanInput{ModelName: "gpt-test"})
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, plan.Gate.InCanary)
	assert.Equal(t, uint64(11), plan.Replay.PolicyRevision)
	assert.Equal(t, uint64(3), plan.Replay.RuntimeGeneration)
	assert.Equal(t, 101, plan.Result.SelectedChannelID)
	assert.Equal(t, Identity{SnapshotRevision: 11, PoolID: 29, MemberID: 11, CredentialID: 1_001}, plan.SelectedIdentity)

	newSession, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	assert.Equal(t, uint64(12), newSession.SnapshotRevision())
	newPlan, active, err := newSession.Plan(RequestRoutingPlanInput{ModelName: "gpt-test"})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 201, newPlan.Result.SelectedChannelID)
}

func TestRequestRoutingSessionSetPinsOneSnapshotAcrossConcreteGroups(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(21, 4, 501, 41, 301)
	view.Pools = append(view.Pools, PoolSnapshot{
		ID: 42, GroupName: "secondary", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		Members: []PoolMemberSnapshot{{
			ID: 22, PoolID: 42, ChannelID: 302, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{2_002},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		}},
	})
	view.Channels = append(view.Channels, ChannelSnapshot{ID: 302, Status: common.ChannelStatusEnabled})
	SetSnapshotForTest(view)

	sessions, err := NewRequestRoutingSessionSet("cohort-0005")
	require.NoError(t, err)
	primary, err := sessions.Session("default")
	require.NoError(t, err)
	assert.Equal(t, uint64(21), primary.SnapshotRevision())

	SetSnapshotForTest(canarySessionSnapshotForTest(22, 5, 502, 41, 401))
	secondary, err := sessions.Session("secondary")
	require.NoError(t, err)
	assert.Equal(t, uint64(21), secondary.SnapshotRevision())
	assert.Equal(t, 42, secondary.PoolID())
	_, err = sessions.Session("auto")
	assert.ErrorIs(t, err, ErrRoutingSessionGroupRequired)
}

func TestRequestRoutingSessionReturnsCanaryGateForControlCohort(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))

	session, err := NewRequestRoutingSession("cohort-0027", "default")
	require.NoError(t, err)
	called := false
	plan, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test",
		SlowStartFactor: func(SlowStartKey) (float64, error) {
			called = true
			return 0.5, nil
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.False(t, plan.Gate.InCanary)
	assert.Equal(t, 221, plan.Gate.Bucket)
	assert.False(t, called, "control traffic must preserve the legacy path without building canary candidates")
	assert.Zero(t, plan.Replay)
	assert.Zero(t, plan.Result)
	assert.Zero(t, plan.SelectedIdentity)
}

func TestRequestRoutingSessionPlanUsesPoolModelIndexAndFailsClosed(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members = []PoolMemberSnapshot{
		{
			ID: 11, PoolID: 29, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_001},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 12, PoolID: 29, ChannelID: 102, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_002},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 13, PoolID: 29, ChannelID: 103, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_003},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 14, PoolID: 29, ChannelID: 104, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_004, 1_005},
			Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		},
		{
			ID: 15, PoolID: 29, ChannelID: 105, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 100, LegacyWeight: 100, CredentialIDs: []int{1_006},
			Models: []ModelSnapshot{{ModelName: "other-model"}},
		},
	}
	view.Pools = append(view.Pools, PoolSnapshot{
		ID: 30, GroupName: "other", DeploymentStage: model.RoutingDeploymentStageCanary,
		SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
		Members: []PoolMemberSnapshot{{
			ID: 99, PoolID: 30, ChannelID: 999, PhysicalStatus: common.ChannelStatusEnabled,
			LegacyPriority: 1_000, LegacyWeight: 1_000, Models: []ModelSnapshot{{ModelName: "gpt-test"}},
		}},
	})
	view.Channels = []ChannelSnapshot{
		{ID: 101, Status: common.ChannelStatusEnabled},
		{ID: 102, Status: common.ChannelStatusEnabled},
		{ID: 103, Status: common.ChannelStatusEnabled},
		{ID: 104, Status: common.ChannelStatusEnabled},
		{ID: 105, Status: common.ChannelStatusEnabled},
		{ID: 999, Status: common.ChannelStatusEnabled},
	}
	SetSnapshotForTest(view)

	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)
	var slowStartKeys []SlowStartKey
	plan, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName:                  "gpt-test",
		AllowedChannelIDs:          []int{101, 102, 103, 104, 105},
		CapacityExcludedChannelIDs: []int{103},
		ExcludedChannelIDs: []int{
			102,
		},
		SlowStartFactor: func(key SlowStartKey) (float64, error) {
			slowStartKeys = append(slowStartKeys, key)
			return 0.25, nil
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, plan.Gate.InCanary)
	assert.Equal(t, DecisionAlgorithmCanaryV1, plan.Replay.AlgorithmVersion)
	require.Len(t, plan.Replay.Candidates, 4, "only the target pool/model index may feed the plan")
	assert.Equal(t, []SlowStartKey{{PoolID: 29, MemberID: 11, Model: "gpt-test"}}, slowStartKeys)
	assert.Equal(t, 0.25, plan.Replay.Candidates[0].SlowStartFactor)
	assert.Equal(t, ExclusionReasonRequestFailed, plan.Replay.Candidates[1].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonLocalCapacity, plan.Replay.Candidates[2].RequestExclusionReason)
	assert.Equal(t, ExclusionReasonMultiKeyUnsupported, plan.Replay.Candidates[3].RequestExclusionReason)

	assert.Equal(t, 101, plan.Result.SelectedChannelID)
	assert.Equal(t, Identity{SnapshotRevision: 11, PoolID: 29, MemberID: 11, CredentialID: 1_001}, plan.SelectedIdentity)
	candidates := make(map[int]DecisionCandidate, len(plan.Result.Candidates))
	for _, candidate := range plan.Result.Candidates {
		candidates[candidate.ChannelID] = candidate
	}
	assert.True(t, candidates[101].Eligible)
	assert.False(t, candidates[102].Eligible, "an explicitly failed request target must never remain eligible")
	assert.Equal(t, ExclusionReasonRequestFailed, candidates[102].ExclusionReason)
	assert.False(t, candidates[103].Eligible)
	assert.Equal(t, ExclusionReasonLocalCapacity, candidates[103].ExclusionReason)
	assert.False(t, candidates[104].Eligible)
	_, crossedPool := candidates[999]
	assert.False(t, crossedPool)
	_, wrongModel := candidates[105]
	assert.False(t, wrongModel)
}

func TestRequestRoutingSessionPlanRejectsInvalidBoundedInputs(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(canarySessionSnapshotForTest(11, 3, 401, 29, 101))
	session, err := NewRequestRoutingSession("cohort-0005", "default")
	require.NoError(t, err)

	_, active, err := session.Plan(RequestRoutingPlanInput{ModelName: "gpt-test", AllowedChannelIDs: []int{0}})
	assert.True(t, active)
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)

	_, active, err = session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test",
		SlowStartFactor: func(SlowStartKey) (float64, error) {
			return 1.01, nil
		},
	})
	assert.True(t, active)
	assert.ErrorIs(t, err, ErrRoutingSessionInvalid)
}

func TestRequestRoutingSessionReplanPinsTimeAndSlowStartFactors(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := canarySessionSnapshotForTest(11, 3, 401, 29, 101)
	view.Pools[0].Members = append(view.Pools[0].Members, PoolMemberSnapshot{
		ID: 12, PoolID: 29, ChannelID: 102, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_002},
		Models: []ModelSnapshot{{ModelName: "gpt-test"}},
	})
	view.Channels = append(view.Channels, ChannelSnapshot{ID: 102, Status: common.ChannelStatusEnabled})
	SetSnapshotForTest(view)

	sessions, err := NewRequestRoutingSessionSet("cohort-0005")
	require.NoError(t, err)
	session, err := sessions.Session("default")
	require.NoError(t, err)
	calls := 0
	factor := func(SlowStartKey) (float64, error) {
		calls++
		return float64(calls) / 10, nil
	}
	first, active, err := session.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test", AllowedChannelIDs: []int{101, 102}, SlowStartFactor: factor,
	})
	require.NoError(t, err)
	require.True(t, active)
	secondSession, err := sessions.Session("default")
	require.NoError(t, err)
	second, active, err := secondSession.Plan(RequestRoutingPlanInput{
		ModelName: "gpt-test", AllowedChannelIDs: []int{101, 102},
		CapacityExcludedChannelIDs: []int{first.Result.SelectedChannelID}, SlowStartFactor: factor,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Same(t, session, secondSession)
	assert.Equal(t, 2, calls, "each member slow-start factor must be captured once per logical request")
	assert.Equal(t, first.Replay.Settings.NowUnix, second.Replay.Settings.NowUnix)
	assert.Equal(t, first.Replay.Settings.NowUnixMilli, second.Replay.Settings.NowUnixMilli)
	assert.Equal(t, first.Replay.Candidates[1].SlowStartFactor, second.Replay.Candidates[1].SlowStartFactor)
}

func canarySessionSnapshotForTest(revision uint64, generation uint64, activationID int64, poolID int, channelID int) SnapshotView {
	return SnapshotView{
		Revision:           revision,
		RuntimeGeneration:  generation,
		PolicyHash:         strings.Repeat("a", 64),
		ActivationID:       activationID,
		ActivationStage:    model.RoutingDeploymentStageCanary,
		TrafficBasisPoints: 100,
		Pools: []PoolSnapshot{{
			ID: poolID, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			SelectorPolicy: defaultPoolSelectorPolicy(model.RoutingPolicyProfileBalanced),
			Members: []PoolMemberSnapshot{{
				ID: 11, PoolID: poolID, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
				LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_001},
				Models: []ModelSnapshot{{ModelName: "gpt-test"}},
			}},
		}},
		Channels: []ChannelSnapshot{{ID: channelID, Status: common.ChannelStatusEnabled}},
	}
}
