package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteStaleSystemInstancesDeletesOnlyStaleRows(t *testing.T) {
	truncateTables(t)

	now := int64(1000)
	instances := []*SystemInstance{
		{NodeName: "online", LastSeenAt: now - 10},
		{NodeName: "boundary", LastSeenAt: now - SystemInstanceStaleAfterSeconds},
		{NodeName: "stale-a", LastSeenAt: now - SystemInstanceStaleAfterSeconds - 1},
		{NodeName: "stale-b", LastSeenAt: now - SystemInstanceStaleAfterSeconds - 30},
	}
	require.NoError(t, DB.Create(&instances).Error)

	deletedCount, err := DeleteStaleSystemInstances(now)

	require.NoError(t, err)
	assert.EqualValues(t, 2, deletedCount)

	var remaining []SystemInstance
	require.NoError(t, DB.Order("node_name ASC").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	assert.Equal(t, "boundary", remaining[0].NodeName)
	assert.Equal(t, "online", remaining[1].NodeName)
}

func TestDeleteStaleSystemInstanceDeletesNamedStaleRowOnly(t *testing.T) {
	truncateTables(t)

	now := int64(1000)
	instances := []*SystemInstance{
		{NodeName: "online", LastSeenAt: now - 10},
		{NodeName: "stale", LastSeenAt: now - SystemInstanceStaleAfterSeconds - 1},
	}
	require.NoError(t, DB.Create(&instances).Error)

	deleted, err := DeleteStaleSystemInstance("online", now)
	require.NoError(t, err)
	assert.False(t, deleted)

	deleted, err = DeleteStaleSystemInstance("stale", now)
	require.NoError(t, err)
	assert.True(t, deleted)

	var remaining []SystemInstance
	require.NoError(t, DB.Order("node_name ASC").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "online", remaining[0].NodeName)
}
