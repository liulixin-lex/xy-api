package channelrouting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotQueriesReturnOnlyRequestedPageAndDeepCopies(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	unknownRate := 0.25
	currentSnapshot.Store(&runtimeSnapshot{
		view: SnapshotView{
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
		},
	})

	pools, total, metadata, ok := ListPoolSnapshots("", 1, 1)
	require.True(t, ok)
	require.Len(t, pools, 1)
	assert.Equal(t, 3, total)
	assert.Equal(t, 2, pools[0].ID)
	assert.Equal(t, uint64(9), metadata.Revision)
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
