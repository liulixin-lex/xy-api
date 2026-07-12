package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingPolicyRollbackApprovalRequiresTwoIndependentActors(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, routingPolicyDocumentForTest(200),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "change"},
	)
	require.NoError(t, err)
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), second.Revision.Revision, routingPolicyDocumentForTest(300),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 3, Reason: "second change"},
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	activation := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageActive, ActorID: 20, Reason: "restore active policy",
	}

	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), head.CurrentRevision, first.Revision.Revision, activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	assertRoutingPolicyRowCounts(t, 3, 3, 3, 3, 3)
	assertRoutingPolicyOperationCountForTest(t, 0)

	firstApproval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	retry, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 10,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstApproval.ID, retry.ID)
	_, created, err = CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 11,
	)
	require.NoError(t, err)
	assert.True(t, created)
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), head.CurrentRevision, second.Revision.Revision, activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "approvals must bind the target revision")

	differentReason := activation
	differentReason.Reason = "different rollback intent"
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), head.CurrentRevision, first.Revision.Revision, differentReason,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "approvals must bind the exact rollback reason")

	executorApproved := activation
	executorApproved.ActorID = 10
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), head.CurrentRevision, first.Revision.Revision, executorApproved,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "the executor's own approval cannot count")
	assertRoutingPolicyRowCounts(t, 3, 3, 3, 3, 3)
	assertRoutingPolicyOperationCountForTest(t, 0)

	rolledBack, operation, err := RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), head.CurrentRevision, first.Revision.Revision, activation,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(4), rolledBack.Revision.Revision)
	assert.Equal(t, first.Revision.Revision, rolledBack.Revision.RollbackOfRevision)
	assert.Equal(t, RoutingOperationTypePolicyRollback, operation.OperationType)
	assert.Equal(t, rolledBack.Revision.Revision, operation.ResultRevision)
	assertRoutingPolicyRowCounts(t, 4, 4, 4, 4, 4)
	assertRoutingPolicyOperationCountForTest(t, 1)
	newHead, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), newHead.CurrentRevision, first.Revision.Revision, activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "approvals must bind the exact policy head")
	assertRoutingPolicyRowCounts(t, 4, 4, 4, 4, 4)
	assertRoutingPolicyOperationCountForTest(t, 1)
}

func TestRoutingPolicyEnterpriseRollbackRequiresApprovalOutsideActiveStage(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	enterprise := routingPolicyDocumentForTest(100)
	enterprise.Pools[0].PolicyProfile = RoutingPolicyProfileEnterpriseSLO
	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, enterprise,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "enterprise base"},
	)
	require.NoError(t, err)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, routingPolicyDocumentForTest(200),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "balanced change"},
	)
	require.NoError(t, err)

	activation := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageShadow, ActorID: 20, Reason: "restore enterprise",
	}
	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), second.Revision.Revision, first.Revision.Revision,
		activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	assertRoutingPolicyRowCounts(t, 2, 2, 2, 2, 2)
	assertRoutingPolicyOperationCountForTest(t, 0)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	firstApproval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 10,
	)
	require.NoError(t, err)
	require.True(t, created)
	retry, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 10,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstApproval.ID, retry.ID)
	differentReason := activation
	differentReason.Reason = "restore enterprise with corrected reason"
	differentReasonApproval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, differentReason, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, firstApproval.ID, differentReasonApproval.ID)
	canaryIntent := activation
	canaryIntent.Stage = RoutingDeploymentStageCanary
	canaryIntent.TrafficBasisPoints = RoutingPolicyCanaryMinBasisPoints
	canaryApproval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, canaryIntent, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, firstApproval.ID, canaryApproval.ID)
	items, err := ListRoutingPolicyRollbackApprovalsContext(
		context.Background(), head, first.Revision.Revision,
	)
	require.NoError(t, err)
	assert.Len(t, items, 3)
}

func TestRoutingPolicyRollbackApprovalIsImmutableAndRetainedUntilHeadChanges(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, routingPolicyDocumentForTest(200),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "change"},
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	activation := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageActive, ActorID: 20, Reason: "restore active policy",
	}
	approval, created, err := CreateRoutingPolicyRollbackApprovalContext(
		context.Background(), head, first.Revision.Revision, activation, 10,
	)
	require.NoError(t, err)
	require.True(t, created)

	assert.ErrorIs(t, db.Model(&RoutingPolicyRollbackApproval{}).Where("id = ?", approval.ID).
		Update("actor_id", 12).Error, ErrRoutingPolicyHistoryImmutable)
	assert.ErrorIs(t, db.Where("id = ?", approval.ID).
		Delete(&RoutingPolicyRollbackApproval{}).Error, ErrRoutingPolicyHistoryImmutable)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingPolicyRollbackApproval{}).
		Where("id = ?", approval.ID).Update("target_content_hash", strings.Repeat("f", 64)).Error)
	_, err = ListRoutingPolicyRollbackApprovalsContext(
		context.Background(), head, first.Revision.Revision,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalInvalid)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingPolicyRollbackApproval{}).
		Where("id = ?", approval.ID).Update("target_content_hash", first.Revision.ContentHash).Error)

	cutoff := time.Now().Add(time.Hour).UnixMilli()
	deleted, err := DeleteStaleRoutingPolicyRollbackApprovalsContext(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted, "an approval for the current head must not expire before the head changes")

	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), second.Revision.Revision, routingPolicyDocumentForTest(300),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 3, Reason: "new head"},
	)
	require.NoError(t, err)
	deleted, err = DeleteStaleRoutingPolicyRollbackApprovalsContext(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var count int64
	require.NoError(t, db.Model(&RoutingPolicyRollbackApproval{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRoutingPolicyRollbackConcurrentExecutorsAdvanceHeadOnce(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, routingPolicyDocumentForTest(200),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "change"},
	)
	require.NoError(t, err)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	approvalIntent := RoutingPolicyActivationSpec{
		Stage: RoutingDeploymentStageActive, Reason: "restore active policy",
	}
	for _, actorID := range []int{10, 11} {
		_, _, err = CreateRoutingPolicyRollbackApprovalContext(
			context.Background(), head, first.Revision.Revision, approvalIntent, actorID,
		)
		require.NoError(t, err)
	}

	errs := make([]error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	for index, actorID := range []int{20, 21} {
		go func(index int, actorID int) {
			defer wait.Done()
			<-start
			_, _, errs[index] = RollbackRoutingPolicyRevisionWithOperationContext(
				context.Background(), second.Revision.Revision, first.Revision.Revision,
				RoutingPolicyActivationSpec{
					Stage: RoutingDeploymentStageActive, ActorID: actorID, Reason: "restore active policy",
				},
			)
		}(index, actorID)
	}
	close(start)
	wait.Wait()
	succeeded := 0
	conflicted := 0
	for _, executeErr := range errs {
		if executeErr == nil {
			succeeded++
			continue
		}
		if errors.Is(executeErr, ErrRoutingPolicyRevisionConflict) {
			conflicted++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, conflicted)
	assertRoutingPolicyRowCounts(t, 3, 3, 3, 3, 3)
	assertRoutingPolicyOperationCountForTest(t, 1)
}

func assertRoutingPolicyOperationCountForTest(t *testing.T, expected int64) {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&RoutingOperation{}).Count(&count).Error)
	assert.Equal(t, expected, count)
}
