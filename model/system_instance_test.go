package model

import (
	"testing"
	"time"

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

func TestUpsertSystemInstancePreservesActiveConflictingIncarnation(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	v2Info := func(version, incarnationID string) map[string]any {
		return map[string]any{
			"version":      version,
			"capabilities": map[string]any{"async_billing_incarnation_id": incarnationID},
		}
	}
	require.NoError(t, UpsertSystemInstance("shared-node", "incarnation-a", v2Info("v2", "incarnation-a"), 100, now))
	require.NoError(t, UpsertSystemInstance(
		"shared-node", "incarnation-a", v2Info("v2-new-heartbeat", "incarnation-a"), 100, now+1,
	))

	err := UpsertSystemInstance("shared-node", "incarnation-b", v2Info("other", "incarnation-b"), 100, now+2)
	assert.ErrorIs(t, err, ErrSystemInstanceIncarnationConflict)
	var persisted SystemInstance
	require.NoError(t, DB.First(&persisted, "node_name = ?", "shared-node").Error)
	assert.Equal(t, int64(100), persisted.StartedAt)
	assert.Contains(t, persisted.Info, "v2-new-heartbeat")

	// Simulate a legacy writer, which still uses an unconditional ON CONFLICT
	// update. The next v2 heartbeat must expose the legacy incarnation instead
	// of immediately hiding it from the fleet gate.
	require.NoError(t, DB.Model(&SystemInstance{}).Where("node_name = ?", "shared-node").Updates(map[string]any{
		"info": `{"schema_version":0}`, "started_at": int64(300),
		"last_seen_at": now + 3, "updated_at": now + 3,
	}).Error)
	err = UpsertSystemInstance("shared-node", "incarnation-a", v2Info("v2", "incarnation-a"), 100, now+4)
	assert.ErrorIs(t, err, ErrSystemInstanceIncarnationConflict)
	require.NoError(t, DB.First(&persisted, "node_name = ?", "shared-node").Error)
	assert.Equal(t, int64(300), persisted.StartedAt)
	assert.Contains(t, persisted.Info, "schema_version")

	require.NoError(t, DB.Model(&SystemInstance{}).Where("node_name = ?", "shared-node").
		Update("last_seen_at", now-SystemInstanceStaleAfterSeconds-1).Error)
	require.NoError(t, UpsertSystemInstance(
		"shared-node", "incarnation-c", v2Info("replacement", "incarnation-c"), 100, now+5,
	))
	require.NoError(t, DB.First(&persisted, "node_name = ?", "shared-node").Error)
	assert.Equal(t, int64(100), persisted.StartedAt)
	assert.Contains(t, persisted.Info, "replacement")
}
