package model

import (
	"context"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingPolicyRetiredApprovalsDoNotGateSQLite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingPolicyRetiredApprovalContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingPolicyRetiredApprovalsDoNotGateExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			runRoutingPolicyRetiredApprovalContract(t, db, test.dbType)
		})
	}
}

func runRoutingPolicyRetiredApprovalContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	base, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	draft, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)
	activation := RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 20, Reason: "deploy"}
	_, _, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, 10,
	)
	require.NoError(t, err)
	_, _, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, 11,
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 10).Update("status", common.UserStatusDisabled).Error)
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 11).Update("role", common.RoleCommonUser).Error)
	require.NoError(t, db.Where(
		"ptype = ? AND v0 IN ? AND v1 = ? AND v2 = ?",
		"p", []string{"user:10", "user:11"}, "channel_routing", "deploy",
	).Delete(&CasbinRule{}).Error)

	_, published, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
	)
	require.NoError(t, err, "disabled or downgraded historical approvers must not gate deployment")
	assert.Equal(t, RoutingDeploymentStageActive, published.Activation.Stage)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	rollback := RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 30, Reason: "rollback"}
	_, _, err = CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, rollback, 12,
	)
	require.NoError(t, err)
	_, _, err = CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, rollback, 13,
	)
	require.NoError(t, err)
	require.NoError(t, db.Unscoped().Where("id = ?", 12).Delete(&routingPolicyApprovalUserForTest{}).Error)
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 13).Update("role", common.RoleCommonUser).Error)

	rolledBack, _, err := RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), published.Revision.Revision, base.Revision.Revision, rollback,
	)
	require.NoError(t, err, "missing or downgraded historical approvers must not gate rollback")
	assert.Equal(t, published.Revision.Revision+1, rolledBack.Revision.Revision)

	var approvalCount int64
	require.NoError(t, db.Model(&RoutingPolicyApproval{}).Count(&approvalCount).Error)
	assert.Equal(t, int64(2), approvalCount)
	var rollbackApprovalCount int64
	require.NoError(t, db.Model(&RoutingPolicyRollbackApproval{}).Count(&rollbackApprovalCount).Error)
	assert.Equal(t, int64(2), rollbackApprovalCount)
}
