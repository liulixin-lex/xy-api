package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyRoutingChannelMetricWithoutGeneration struct {
	ID           int    `gorm:"primaryKey"`
	ChannelID    int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:1"`
	APIKeyIndex  int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:2"`
	ModelName    string `gorm:"uniqueIndex:idx_routing_metric_key,priority:3"`
	Group        string `gorm:"column:group;uniqueIndex:idx_routing_metric_key,priority:4"`
	BucketTs     int64  `gorm:"uniqueIndex:idx_routing_metric_key,priority:5"`
	RequestCount int64
}

func (legacyRoutingChannelMetricWithoutGeneration) TableName() string {
	return (RoutingChannelMetric{}).TableName()
}

type legacyRoutingBreakerStateWithoutGeneration struct {
	ID          int    `gorm:"primaryKey"`
	ChannelID   int    `gorm:"uniqueIndex:idx_routing_breaker_key,priority:1"`
	APIKeyIndex int    `gorm:"uniqueIndex:idx_routing_breaker_key,priority:2"`
	ModelName   string `gorm:"uniqueIndex:idx_routing_breaker_key,priority:3"`
	Group       string `gorm:"column:group;uniqueIndex:idx_routing_breaker_key,priority:4"`
	State       string
}

func (legacyRoutingBreakerStateWithoutGeneration) TableName() string {
	return (RoutingBreakerState{}).TableName()
}

type legacyRoutingChannelHealthStateWithoutGeneration struct {
	ID          int `gorm:"primaryKey"`
	ChannelID   int `gorm:"uniqueIndex"`
	AuthFailure bool
}

func (legacyRoutingChannelHealthStateWithoutGeneration) TableName() string {
	return (RoutingChannelHealthState{}).TableName()
}

type legacyRoutingCredentialHealthStateWithoutGeneration struct {
	CredentialID  int `gorm:"primaryKey"`
	ChannelID     int
	UpdatedTimeMs int64
}

func (legacyRoutingCredentialHealthStateWithoutGeneration) TableName() string {
	return (RoutingCredentialHealthState{}).TableName()
}

func TestPrepareRoutingRuntimeGenerationSchemaColdStartsLegacyStateExactlyOnce(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&legacyRoutingChannelMetricWithoutGeneration{},
		&legacyRoutingBreakerStateWithoutGeneration{},
		&legacyRoutingChannelHealthStateWithoutGeneration{},
		&legacyRoutingCredentialHealthStateWithoutGeneration{},
	))
	require.NoError(t, db.Create(&legacyRoutingChannelMetricWithoutGeneration{
		ChannelID: 1, APIKeyIndex: -1, ModelName: "gpt-test", Group: "default", BucketTs: 60, RequestCount: 1,
	}).Error)
	require.NoError(t, db.Create(&legacyRoutingBreakerStateWithoutGeneration{
		ChannelID: 1, APIKeyIndex: -1, ModelName: "gpt-test", Group: "default", State: RoutingBreakerStateOpen,
	}).Error)
	require.NoError(t, db.Create(&legacyRoutingChannelHealthStateWithoutGeneration{ChannelID: 1, AuthFailure: true}).Error)
	require.NoError(t, db.Create(&legacyRoutingCredentialHealthStateWithoutGeneration{
		CredentialID: 7, ChannelID: 1, UpdatedTimeMs: 1,
	}).Error)

	require.NoError(t, prepareRoutingRuntimeGenerationSchema(db))
	for _, state := range []any{
		&RoutingChannelMetric{}, &RoutingBreakerState{}, &RoutingChannelHealthState{}, &RoutingCredentialHealthState{},
	} {
		assert.True(t, db.Migrator().HasColumn(state, "channel_generation"))
		var count int64
		require.NoError(t, db.Model(state).Count(&count).Error)
		assert.Zero(t, count)
	}
	assert.False(t, db.Migrator().HasIndex(&RoutingChannelMetric{}, routingChannelMetricLegacyUniqueIndex))
	assert.False(t, db.Migrator().HasIndex(&RoutingBreakerState{}, routingBreakerLegacyUniqueIndex))
	assert.False(t, db.Migrator().HasIndex(&RoutingChannelHealthState{}, routingChannelHealthLegacyUniqueIndex))

	require.NoError(t, db.AutoMigrate(
		&RoutingChannelMetric{}, &RoutingBreakerState{}, &RoutingChannelHealthState{}, &RoutingCredentialHealthState{},
	))
	generation := "11111111111111111111111111111111"
	require.NoError(t, db.Create(&RoutingChannelHealthState{
		ChannelID: 1, ChannelGeneration: generation, UpdatedTime: 10,
	}).Error)
	require.NoError(t, db.Create(&RoutingCredentialHealthState{
		CredentialID: 8, ChannelID: 1, ChannelGeneration: generation, UpdatedTimeMs: 10,
	}).Error)
	require.NoError(t, db.Create(&RoutingCredentialHealthState{
		CredentialID: 9, ChannelID: 1, UpdatedTimeMs: 11,
	}).Error)
	require.NoError(t, prepareRoutingRuntimeGenerationSchema(db))
	var count int64
	require.NoError(t, db.Model(&RoutingChannelHealthState{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	require.NoError(t, db.Model(&RoutingCredentialHealthState{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var credentialHealth RoutingCredentialHealthState
	require.NoError(t, db.First(&credentialHealth).Error)
	assert.Equal(t, 8, credentialHealth.CredentialID)
	assert.Equal(t, generation, credentialHealth.ChannelGeneration)
}

func TestMigrateRoutingRuntimeGenerationSchemaBackfillsOnlyProvableHistory(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingPoolMember{}, &RoutingMetricRollup{}, &RoutingProbeResult{}, &RoutingDecisionAudit{},
	))
	generation := "22222222222222222222222222222222"
	require.NoError(t, db.Create(&RoutingPoolMember{
		ID: 11, PoolID: 3, ChannelID: 7, ChannelGeneration: generation, Source: "test", Active: false,
	}).Error)
	require.NoError(t, db.Create(&RoutingMetricRollup{
		MemberID: 11, CredentialID: 0, ModelName: "gpt-test", ModelKey: RoutingMetricRollupModelKey("gpt-test"),
		BucketTs: 60, ChannelID: 7, PoolID: 3, SchemaVersion: 3, LastSnapshotRevision: 9,
		RequestCount: 1, SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&RoutingMetricRollup{
		MemberID: 99, CredentialID: 0, ModelName: "unknown", ModelKey: RoutingMetricRollupModelKey("unknown"),
		BucketTs: 60, ChannelID: 99, PoolID: 9, SchemaVersion: 3, LastSnapshotRevision: 9,
		RequestCount: 1, SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&RoutingProbeResult{
		ProbeID:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TargetKey: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MemberID:  11, ChannelID: 7,
	}).Error)
	require.NoError(t, db.Create(&RoutingDecisionAudit{
		DecisionID: "decision-history", ActualChannelID: 7, ObservedChannelID: 7, SelectedMemberID: 11,
	}).Error)

	require.NoError(t, MigrateRoutingRuntimeGenerationSchema(db))
	var rollups []RoutingMetricRollup
	require.NoError(t, db.Order("id asc").Find(&rollups).Error)
	require.Len(t, rollups, 2)
	assert.Equal(t, generation, rollups[0].ChannelGeneration)
	assert.Equal(t, RoutingMetricRollupSchemaVersion, rollups[0].SchemaVersion)
	assert.Empty(t, rollups[1].ChannelGeneration)
	assert.Equal(t, 3, rollups[1].SchemaVersion)

	var probe RoutingProbeResult
	require.NoError(t, db.First(&probe).Error)
	assert.Equal(t, generation, probe.ChannelGeneration)
	var decision RoutingDecisionAudit
	require.NoError(t, db.First(&decision).Error)
	assert.Equal(t, generation, decision.ActualChannelGeneration)
	assert.Equal(t, generation, decision.ObservedChannelGeneration)
	assert.Equal(t, generation, decision.SelectedChannelGeneration)

	require.NoError(t, MigrateRoutingRuntimeGenerationSchema(db))
}
