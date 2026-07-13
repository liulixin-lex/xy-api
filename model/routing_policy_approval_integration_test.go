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

func TestRoutingPolicyApprovalIntentBehaviorSQLite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingPolicyApprovalIntentBehaviorContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingPolicyApprovalIntentBehaviorExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingPolicyApprovalIntentBehaviorContract(t, db, test.dbType)
		})
	}
}

func runRoutingPolicyApprovalIntentBehaviorContract(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
) {
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
	publishIntent := RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 20, Reason: "deploy"}
	firstApproval, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent, 10,
	)
	require.NoError(t, err)
	require.True(t, created)
	retry, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent, 10,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstApproval.ID, retry.ID)
	differentPublishIntent := publishIntent
	differentPublishIntent.Reason = "deploy corrected"
	_, created, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, differentPublishIntent, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	_, created, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent, 11,
	)
	require.NoError(t, err)
	assert.True(t, created)
	quorum, err := RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), draft, publishIntent, RoutingPolicyRequiredApprovals,
	)
	require.NoError(t, err)
	assert.Len(t, quorum, 2)
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 11).Update("status", common.UserStatusDisabled).Error)
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "a disabled approver must no longer count")
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 11).Update("status", common.UserStatusEnabled).Error)
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 11).Update("role", common.RoleCommonUser).Error)
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "a downgraded approver must no longer count")
	require.NoError(t, db.Model(&routingPolicyApprovalUserForTest{}).
		Where("id = ?", 11).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
		"p", "user:11", "channel_routing", "deploy",
	).Delete(&CasbinRule{}).Error)
	require.NoError(t, db.Create(&CasbinRule{
		Ptype: "p", V0: "role:admin", V1: "channel_routing", V2: "deploy", V3: "allow",
	}).Error)
	quorum, err = RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), draft, publishIntent, RoutingPolicyRequiredApprovals,
	)
	require.NoError(t, err)
	assert.Len(t, quorum, 2, "the current admin role baseline must count when no user override exists")
	require.NoError(t, db.Create(&CasbinRule{
		Ptype: "p", V0: "user:11", V1: "channel_routing", V2: "deploy", V3: "deny",
	}).Error)
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "a user deny must override an allowed admin baseline")
	require.NoError(t, db.Where(
		"ptype = ? AND v0 IN ? AND v1 = ? AND v2 = ?",
		"p", []string{"user:11", "role:admin"}, "channel_routing", "deploy",
	).Delete(&CasbinRule{}).Error)
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "a revoked deploy grant must invalidate historical approval")
	require.NoError(t, db.Create(&CasbinRule{
		Ptype: "p", V0: "user:11", V1: "channel_routing", V2: "deploy", V3: "allow",
	}).Error)
	_, published, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, publishIntent,
	)
	require.NoError(t, err)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	rollbackIntent := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageActive, ActorID: 30, Reason: "rollback",
	}
	firstRollbackApproval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, rollbackIntent, 12,
	)
	require.NoError(t, err)
	require.True(t, created)
	retryRollback, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, rollbackIntent, 12,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstRollbackApproval.ID, retryRollback.ID)
	differentRollbackIntent := rollbackIntent
	differentRollbackIntent.Reason = "rollback corrected"
	_, created, err = CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, differentRollbackIntent, 12,
	)
	require.NoError(t, err)
	assert.True(t, created)
	_, created, err = CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, base.Revision.Revision, rollbackIntent, 13,
	)
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, db.Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?",
		"p", "user:13", "channel_routing", "deploy",
	).Delete(&CasbinRule{}).Error)
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), published.Revision.Revision, base.Revision.Revision, rollbackIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "rollback must revalidate current deploy grants")
	require.NoError(t, db.Create(&CasbinRule{
		Ptype: "p", V0: "user:13", V1: "channel_routing", V2: "deploy", V3: "allow",
	}).Error)
	rolledBack, _, err := RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), published.Revision.Revision, base.Revision.Revision, rollbackIntent,
	)
	require.NoError(t, err)
	assert.Equal(t, published.Revision.Revision+1, rolledBack.Revision.Revision)
}
