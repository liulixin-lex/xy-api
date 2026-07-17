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

func TestRoutingPolicyRollbackNeedsNoApprovalQuorum(t *testing.T) {
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
}

func TestRoutingPolicyEnterpriseRollbackNeedsNoApprovalQuorum(t *testing.T) {
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
	rolledBack, operation, err := RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), second.Revision.Revision, first.Revision.Revision,
		activation,
	)
	require.NoError(t, err)
	assert.Equal(t, first.Revision.Revision, rolledBack.Revision.RollbackOfRevision)
	assert.Equal(t, RoutingOperationStatusSucceeded, operation.Status)
}

func TestRoutingPolicyRollbackApprovalIsImmutableAndPermanentlyRetained(t *testing.T) {
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
	assert.Zero(t, deleted)

	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), second.Revision.Revision, routingPolicyDocumentForTest(300),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 3, Reason: "new head"},
	)
	require.NoError(t, err)
	deleted, err = DeleteStaleRoutingPolicyRollbackApprovalsContext(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted, "historical approval facts must survive head changes")
	var count int64
	require.NoError(t, db.Model(&RoutingPolicyRollbackApproval{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
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
