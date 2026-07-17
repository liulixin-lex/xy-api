package model

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyRoutingAgentRecommendationForRetirement struct {
	ID           int64  `gorm:"primaryKey"`
	TargetJSON   string `gorm:"type:text"`
	ProposedJSON string `gorm:"type:text"`
}

func (legacyRoutingAgentRecommendationForRetirement) TableName() string {
	return routingAgentRecommendationTable
}

func TestRetireRoutingAgentPlaceholderSchemaIsIdempotent(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Option{}, &SystemTask{}, &SystemTaskLock{}, &RoutingRuntimeSettingsState{},
		&RoutingControlAudit{}, &RoutingSchemaVersion{}, &legacyRoutingAgentRecommendationForRetirement{},
	))
	require.NoError(t, db.Create(&[]Option{
		{Key: retiredRoutingAgentOptionKeys[0], Value: "true"},
		{Key: retiredRoutingAgentOptionKeys[1], Value: "true"},
		{Key: retiredRoutingAgentOptionKeys[2], Value: "claude-placeholder"},
		{Key: "smart_routing_setting.enabled", Value: "true"},
	}).Error)
	activeKey := routingAgentTaskType
	task := SystemTask{
		TaskID: "systask_" + strings.Repeat("a", 32), Type: routingAgentTaskType,
		Status: SystemTaskStatusPending, ActiveKey: &activeKey,
	}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&SystemTaskLock{
		Type: routingAgentTaskType, TaskID: task.TaskID, LockedBy: "legacy-node", LockedUntil: 100, UpdatedAt: 1,
	}).Error)
	require.NoError(t, db.Create(&legacyRoutingAgentRecommendationForRetirement{
		TargetJSON: `{"pool_id":1}`, ProposedJSON: `{"weight":100}`,
	}).Error)
	document := `{"enabled":true,"agent_enabled":true,"agent_auto_apply":true,"agent_model":"claude-placeholder"}`
	require.NoError(t, db.Create(&RoutingRuntimeSettingsState{
		ID: routingRuntimeSettingsStateID, Revision: 1,
		DocumentHash: RoutingRuntimeSettingsDocumentHash([]byte(document)), DocumentJSON: document,
		UpdatedTimeMs: 1,
	}).Error)

	for iteration := 0; iteration < 2; iteration++ {
		require.NoError(t, RetireRoutingAgentPlaceholderSchema(db))
	}

	var retiredOptionCount int64
	require.NoError(t, db.Model(&Option{}).Where("key IN ?", retiredRoutingAgentOptionKeys).Count(&retiredOptionCount).Error)
	assert.Zero(t, retiredOptionCount)
	var retained Option
	require.NoError(t, db.Where("key = ?", "smart_routing_setting.enabled").First(&retained).Error)
	assert.Equal(t, "true", retained.Value)
	var taskCount int64
	require.NoError(t, db.Model(&SystemTask{}).Where("type = ?", routingAgentTaskType).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
	var lockCount int64
	require.NoError(t, db.Model(&SystemTaskLock{}).Where("type = ?", routingAgentTaskType).Count(&lockCount).Error)
	assert.Zero(t, lockCount)
	assert.False(t, db.Migrator().HasTable(routingAgentRecommendationTable))

	state, err := GetRoutingRuntimeSettingsStateDBContext(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, int64(2), state.Revision)
	assert.NotContains(t, state.DocumentJSON, "agent_enabled")
	assert.NotContains(t, state.DocumentJSON, "agent_auto_apply")
	assert.NotContains(t, state.DocumentJSON, "agent_model")
	var audits []RoutingControlAudit
	require.NoError(t, db.Where("event_type = ?", "runtime_settings.routing_agent_retired").Find(&audits).Error)
	require.Len(t, audits, 1)
	assert.Equal(t, RoutingControlAuditSourceMigration, audits[0].Source)

	var marker RoutingSchemaVersion
	require.NoError(t, db.Where("component = ?", routingAgentRetirementComponent).First(&marker).Error)
	assert.Equal(t, routingAgentRetirementComplete, marker.Version)
}
