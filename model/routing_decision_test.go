package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteRoutingDecisionAuditsBeforeContextUsesRecoverableBatches(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingDecisionAudit{}))

	old := make([]RoutingDecisionAudit, routingDecisionRetentionBatchSize+1)
	for index := range old {
		old[index] = RoutingDecisionAudit{
			DecisionID: fmt.Sprintf("old-%d", index), GroupName: "default", ModelName: "gpt-test", CreatedTime: 10,
		}
	}
	require.NoError(t, DB.CreateInBatches(old, RoutingDecisionAuditMaxBatch).Error)
	require.NoError(t, DB.Create(&RoutingDecisionAudit{
		DecisionID: "current", GroupName: "default", ModelName: "gpt-test", CreatedTime: 20,
	}).Error)

	deleted, err := DeleteRoutingDecisionAuditsBeforeContext(context.Background(), 15)
	require.NoError(t, err)
	assert.Equal(t, int64(routingDecisionRetentionBatchSize+1), deleted)
	var remaining []RoutingDecisionAudit
	require.NoError(t, DB.Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "current", remaining[0].DecisionID)
}

func TestCreateRoutingDecisionAuditsUsesExactCrossDatabaseFilterKeys(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingDecisionAudit{}))
	audits := []RoutingDecisionAudit{
		{DecisionID: "upper", RequestID: "Request-X", PoolID: 1, GroupName: "VIP", ModelName: "Model-X", SnapshotRevision: 1, CreatedTime: 10},
		{DecisionID: "lower", RequestID: "request-x", PoolID: 2, GroupName: "vip", ModelName: "model-x", SnapshotRevision: 1, CreatedTime: 10},
	}
	require.NoError(t, CreateRoutingDecisionAuditsContext(context.Background(), audits))

	var upper RoutingDecisionAudit
	require.NoError(t, DB.Where("group_key = ? AND model_key = ? AND request_key = ?",
		RoutingDecisionGroupKey("VIP"), RoutingDecisionModelKey("Model-X"), RoutingDecisionRequestKey("Request-X"),
	).First(&upper).Error)
	assert.Equal(t, "upper", upper.DecisionID)
	assert.NotEqual(t, RoutingDecisionGroupKey("VIP"), RoutingDecisionGroupKey("vip"))
	assert.NotEqual(t, RoutingDecisionModelKey("Model-X"), RoutingDecisionModelKey("model-x"))
	assert.NotEqual(t, RoutingDecisionRequestKey("Request-X"), RoutingDecisionRequestKey("request-x"))

	invalid := audits[0]
	invalid.DecisionID = "invalid"
	invalid.ModelName = string([]byte{0xff})
	require.ErrorIs(t, CreateRoutingDecisionAuditsContext(context.Background(), []RoutingDecisionAudit{invalid}), ErrRoutingDecisionAuditInvalid)
	var count int64
	require.NoError(t, DB.Model(&RoutingDecisionAudit{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}
