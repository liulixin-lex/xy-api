package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListActiveRoutingRuntimeCheckpointsUsesBoundedStableCursor(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingRuntimeCheckpoint{}))

	for index, spec := range []struct {
		nodeID       string
		observedTime int64
		expiresTime  int64
	}{
		{nodeID: "node-newest", observedTime: 300, expiresTime: 1_000},
		{nodeID: "node-middle", observedTime: 200, expiresTime: 1_000},
		{nodeID: "node-oldest", observedTime: 100, expiresTime: 1_000},
		{nodeID: "node-expired", observedTime: 400, expiresTime: 499},
	} {
		checkpoint, err := NewRoutingRuntimeCheckpoint(
			spec.nodeID,
			"config_stream",
			"routing:v2:config",
			int64(index),
			1,
			map[string]any{"cursor": spec.nodeID},
			spec.observedTime,
			spec.expiresTime,
		)
		require.NoError(t, err)
		_, err = UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, err)
	}

	first, hasMore, err := ListActiveRoutingRuntimeCheckpointsContext(
		context.Background(), "config_stream", "routing:v2:config", 500, 0, 0, 2,
	)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.True(t, hasMore)
	assert.Equal(t, []string{"node-newest", "node-middle"}, []string{first[0].NodeID, first[1].NodeID})

	second, hasMore, err := ListActiveRoutingRuntimeCheckpointsContext(
		context.Background(), "config_stream", "routing:v2:config", 500,
		first[1].ObservedTime, first[1].ID, 2,
	)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.False(t, hasMore)
	assert.Equal(t, "node-oldest", second[0].NodeID)
}

func TestListActiveRoutingRuntimeCheckpointsRejectsUnboundedLimit(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingRuntimeCheckpoint{}))

	_, _, err := ListActiveRoutingRuntimeCheckpointsContext(
		context.Background(), "config_stream", "routing:v2:config", 500, 0, 0,
		RoutingRuntimeCheckpointMaxPageSize+1,
	)
	assert.ErrorIs(t, err, ErrRoutingRuntimeCheckpointInvalid)
}
