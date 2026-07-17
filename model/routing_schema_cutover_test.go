package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRoutingSchemaFleetForCutoverRejectsActiveLegacyInstances(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	now := int64(1_720_000_000)
	instanceInfo := func(appVersion string, schemaVersion string) string {
		t.Helper()
		payload, err := common.Marshal(map[string]any{
			"runtime": map[string]any{"version": appVersion},
			"capabilities": map[string]any{
				"channel_routing_schema": schemaVersion,
			},
		})
		require.NoError(t, err)
		return string(payload)
	}
	require.NoError(t, db.Create(&[]SystemInstance{
		{
			NodeName: "current-node", Info: instanceInfo("v0.1.14", RoutingSchemaCurrentVersion),
			LastSeenAt: now,
		},
		{
			NodeName: "legacy-node", Info: instanceInfo("v0.1.13", ""),
			LastSeenAt: now - SystemInstanceStaleAfterSeconds,
		},
		{
			NodeName: "stopped-legacy-node", Info: instanceInfo("v0.1.13", ""),
			LastSeenAt: now - SystemInstanceStaleAfterSeconds - 1,
		},
	}).Error)

	err := validateRoutingSchemaFleetForCutover(db, now)

	assert.ErrorIs(t, err, ErrRoutingSchemaFleetIncompatible)
	assert.Contains(t, err.Error(), "legacy-node")
	assert.Contains(t, err.Error(), "v0.1.13")
	assert.Contains(t, err.Error(), "routing_schema=\"unreported\"")
	assert.NotContains(t, err.Error(), "stopped-legacy-node")
	assert.Contains(t, err.Error(), "wait at least 90 seconds")

	require.NoError(t, db.Model(&SystemInstance{}).Where("node_name = ?", "legacy-node").
		Update("info", instanceInfo("v0.1.14", RoutingSchemaCurrentVersion)).Error)
	require.NoError(t, validateRoutingSchemaFleetForCutover(db, now))
}

func TestValidateRoutingSchemaFleetForCutoverFailsClosedOnMalformedPayload(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	now := int64(1_720_000_000)
	require.NoError(t, db.Create(&SystemInstance{
		NodeName: "malformed-node", Info: "{", LastSeenAt: now,
	}).Error)

	err := validateRoutingSchemaFleetForCutover(db, now)

	assert.ErrorIs(t, err, ErrRoutingSchemaFleetIncompatible)
	assert.Contains(t, err.Error(), "malformed-node")
	assert.Contains(t, err.Error(), "app=\"unreported\"")
}

func TestPreflightRoutingSchemaCutoverAllowsSingleProcessSQLiteUpgrade(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	require.NoError(t, db.AutoMigrate(&SystemInstance{}))
	require.NoError(t, db.Create(&SystemInstance{
		NodeName: "previous-process", Info: `{}`, LastSeenAt: 1_720_000_000,
	}).Error)

	require.NoError(t, preflightRoutingSchemaCutover(db))
}
