package channelrouting

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotQueriesReturnOnlyRequestedPageAndDeepCopies(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	unknownRate := 0.25
	SetSnapshotForTest(SnapshotView{
		Revision:    9,
		BuiltAtUnix: 123,
		Stats: SnapshotStats{
			PoolCount:                 3,
			UnknownClassificationRate: &unknownRate,
		},
		Pools: []PoolSnapshot{
			{ID: 1, GroupName: "a", Members: []PoolMemberSnapshot{{ID: 11, CredentialIDs: []int{101}, Models: []ModelSnapshot{{ModelName: "gpt-a"}}}}},
			{ID: 2, GroupName: "b", Members: []PoolMemberSnapshot{{ID: 22, CredentialIDs: []int{202}, Models: []ModelSnapshot{{ModelName: "gpt-b"}}}}},
			{ID: 3, GroupName: "c", Members: []PoolMemberSnapshot{{ID: 33, CredentialIDs: []int{303}, Models: []ModelSnapshot{{ModelName: "gpt-c"}}}}},
		},
		Channels: []ChannelSnapshot{
			{ID: 1, Name: "a", CredentialIDs: []int{101}},
			{ID: 2, Name: "b", CredentialIDs: []int{202}},
			{ID: 3, Name: "c", CredentialIDs: []int{303}},
		},
	})

	pools, total, metadata, ok := ListPoolSnapshots("", 1, 1)
	require.True(t, ok)
	require.Len(t, pools, 1)
	assert.Equal(t, 3, total)
	assert.Equal(t, 2, pools[0].ID)
	assert.Equal(t, uint64(9), metadata.Revision)
	assert.Len(t, metadata.NodeEpochID, 32)
	require.NotNil(t, metadata.Stats.UnknownClassificationRate)

	pools[0].Members[0].CredentialIDs[0] = 999
	pools[0].Members[0].Models[0].ModelName = "mutated"
	*metadata.Stats.UnknownClassificationRate = 1
	channels, channelTotal, _, ok := ListChannelSnapshots("", nil, nil, 1, 1)
	require.True(t, ok)
	require.Len(t, channels, 1)
	assert.Equal(t, 3, channelTotal)
	channels[0].CredentialIDs[0] = 999

	current, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, 202, current.Pools[1].Members[0].CredentialIDs[0])
	assert.Equal(t, "gpt-b", current.Pools[1].Members[0].Models[0].ModelName)
	assert.Equal(t, 202, current.Channels[1].CredentialIDs[0])
	require.NotNil(t, current.Stats.UnknownClassificationRate)
	assert.Equal(t, 0.25, *current.Stats.UnknownClassificationRate)
}

func TestTelemetryAggregateKeepsGlobalP95SeparateFromWorstMemberP95(t *testing.T) {
	view := SnapshotView{Pools: []PoolSnapshot{{
		ID: 1,
		Members: []PoolMemberSnapshot{
			{ID: 11, Models: []ModelSnapshot{{
				ModelName: "large", MetricKnown: true, RequestCount: 10_000,
				P95TTFTKnown: true, P95TTFTMs: 100,
			}}},
			{ID: 12, Models: []ModelSnapshot{{
				ModelName: "small", MetricKnown: true, RequestCount: 1,
				P95TTFTKnown: true, P95TTFTMs: 10_000,
			}}},
		},
	}}}

	aggregate := telemetryAggregate(view)
	assert.False(t, aggregate.P95TTFTKnown)
	assert.True(t, aggregate.MaxMemberP95TTFTKnown)
	assert.Equal(t, float64(10_000), aggregate.MaxMemberP95TTFTMs)

	view.AggregateP95TTFTKnown = true
	view.AggregateP95TTFTMs = 100
	aggregate = telemetryAggregate(view)
	assert.True(t, aggregate.P95TTFTKnown)
	assert.Equal(t, float64(100), aggregate.P95TTFTMs)
	assert.Equal(t, float64(10_000), aggregate.MaxMemberP95TTFTMs)
}

func TestTelemetryAggregateUsesTTFTCountForZeroMillisecondCoverage(t *testing.T) {
	view := SnapshotView{Pools: []PoolSnapshot{{
		ID: 1,
		Members: []PoolMemberSnapshot{{
			ID: 11,
			Models: []ModelSnapshot{{
				ModelName: "zero", MetricKnown: true, MetricSource: "stable_rollup",
				P95TTFTKnown: true, P95TTFTMs: 0, ttftCount: 1,
			}},
		}},
	}}}

	aggregate := telemetryAggregate(view)
	require.True(t, aggregate.P95TTFTKnown)
	assert.Zero(t, aggregate.P95TTFTMs)

	view.Pools[0].Members[0].Models = append(view.Pools[0].Members[0].Models, ModelSnapshot{
		ModelName: "missing-distribution", MetricKnown: true, MetricSource: "stable_rollup",
		AverageTTFTMs: 0, ttftCount: 1,
	})
	aggregate = telemetryAggregate(view)
	assert.False(t, aggregate.P95TTFTKnown)
}

func TestCurrentSnapshotSummaryUsesPrecomputedTelemetry(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	currentSnapshot.Store(&runtimeSnapshot{
		view: SnapshotView{Revision: 1, Pools: []PoolSnapshot{{
			ID: 1, Members: []PoolMemberSnapshot{{
				ID: 1, Models: []ModelSnapshot{{MetricKnown: true, RequestCount: 999}},
			}},
		}}},
		telemetrySummary: TelemetryAggregate{ObservedRequests: 7},
	})

	_, aggregate, ok := CurrentSnapshotSummary()
	require.True(t, ok)
	assert.Equal(t, int64(7), aggregate.ObservedRequests)
}

func TestPoolSummaryAndDetailQueriesStayBounded(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(SnapshotView{
		Revision: 2,
		Pools: []PoolSnapshot{{
			ID: 10, GroupName: "default", DisplayName: "Default", Source: "policy_revision",
			Members: []PoolMemberSnapshot{
				{ID: 1, PoolID: 10, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled, TelemetryKnown: true,
					CredentialIDs: []int{1, 2, 3}, Models: []ModelSnapshot{
						{ModelName: "a", CostKnown: true},
						{ModelName: "b", BreakerState: model.RoutingBreakerStateOpen},
						{ModelName: "c", BreakerState: model.RoutingBreakerStateDegraded},
					}},
				{ID: 2, PoolID: 10, ChannelID: 102},
				{ID: 3, PoolID: 10, ChannelID: 103},
			},
		}},
	})

	summaries, total, _, ok := ListPoolSnapshotSummaries("def", 0, 10)
	require.True(t, ok)
	require.Len(t, summaries, 1)
	assert.Equal(t, 1, total)
	assert.Equal(t, 3, summaries[0].MemberCount)
	assert.Equal(t, 1, summaries[0].EnabledChannels)
	assert.Equal(t, 1, summaries[0].OpenModels)
	assert.Equal(t, 1, summaries[0].DegradedModels)
	assert.Equal(t, 1, summaries[0].KnownCostModels)

	page, _, found := GetPoolSnapshotPage(10, 1, 1, 1, 1)
	require.True(t, found)
	assert.Equal(t, 3, page.MemberCount)
	assert.True(t, page.MembersTruncated)
	require.Len(t, page.Members, 1)
	assert.Equal(t, 2, page.Members[0].ID)

	first, _, found := GetPoolSnapshotPage(10, 0, 1, 1, 1)
	require.True(t, found)
	require.Len(t, first.Members, 1)
	member := first.Members[0]
	assert.Equal(t, 3, member.CredentialCount)
	assert.True(t, member.CredentialsTruncated)
	assert.Len(t, member.CredentialIDs, 1)
	assert.Equal(t, 3, member.ModelCount)
	assert.True(t, member.ModelsTruncated)
	assert.Len(t, member.Models, 1)
}
