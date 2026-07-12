package model

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingPolicyApprovalQuorumIsVersionBoundImmutableAndIdempotent(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
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
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, draft.CreatedBy,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalInvalid, "the draft author cannot approve their own deployment")

	first, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	retry, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, 10,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)
	differentReason := activation
	differentReason.Reason = "deploy with corrected reason"
	differentReasonApproval, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, differentReason, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.ID, differentReasonApproval.ID)
	canaryIntent := activation
	canaryIntent.Stage = RoutingDeploymentStageCanary
	canaryIntent.TrafficBasisPoints = RoutingPolicyCanaryMinBasisPoints
	canaryApproval, created, err := CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, canaryIntent, 10,
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.ID, canaryApproval.ID)

	approvals, err := RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), draft, activation, RoutingPolicyRequiredApprovals,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	require.Len(t, approvals, 1)

	_, created, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation, 11,
	)
	require.NoError(t, err)
	assert.True(t, created)
	approvals, err = RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), draft, activation, RoutingPolicyRequiredApprovals,
	)
	require.NoError(t, err)
	require.Len(t, approvals, 2)
	assert.NotEqual(t, approvals[0].ActorID, approvals[1].ActorID)
	deleted, err := DeleteStaleRoutingPolicyApprovalsContext(
		context.Background(), time.Now().Add(time.Hour).UnixMilli(), 100,
	)
	require.NoError(t, err)
	assert.Zero(t, deleted, "approvals for an unchanged validated draft must be retained")

	assert.ErrorIs(t, db.Model(&RoutingPolicyApproval{}).Where("id = ?", first.ID).
		Update("actor_id", 12).Error, ErrRoutingPolicyHistoryImmutable)
	assert.ErrorIs(t, db.Where("id = ?", first.ID).Delete(&RoutingPolicyApproval{}).Error, ErrRoutingPolicyHistoryImmutable)

	updated, err := UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, routingPolicyDocumentForTest(300), 2,
	)
	require.NoError(t, err)
	updated, err = ValidateRoutingPolicyDraftContext(
		context.Background(), updated.ID, updated.Version, updated.ETag, 2,
	)
	require.NoError(t, err)
	approvals, err = RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), updated, activation, RoutingPolicyRequiredApprovals,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	assert.Empty(t, approvals, "approvals for an earlier draft version must not authorize a changed document")
	deleted, err = DeleteStaleRoutingPolicyApprovalsContext(
		context.Background(), time.Now().Add(time.Hour).UnixMilli(), 100,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(4), deleted)
}

func TestRoutingPolicyApprovalRejectsStaleValidatedHead(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
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
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(150),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 3, Reason: "concurrent"},
	)
	require.NoError(t, err)

	_, _, err = CreateRoutingPolicyApprovalContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 20, Reason: "deploy"}, 10,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyRevisionConflict)
	var count int64
	require.NoError(t, db.Model(&RoutingPolicyApproval{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRoutingPolicyActivePublishRequiresTwoDistinctApprovals(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
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

	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	_, _, err = CreateRoutingPolicyApprovalContext(context.Background(), draft.ID, draft.Version, draft.ETag, activation, 10)
	require.NoError(t, err)
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired)
	_, _, err = CreateRoutingPolicyApprovalContext(context.Background(), draft.ID, draft.Version, draft.ETag, activation, 11)
	require.NoError(t, err)

	differentIntent := activation
	differentIntent.Reason = "different deploy intent"
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, differentIntent,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "approvals must bind the exact activation intent")

	approverAsPublisher := activation
	approverAsPublisher.ActorID = 10
	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, approverAsPublisher,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyApprovalRequired, "the publisher's own approval cannot count toward quorum")

	publishedDraft, published, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, activation,
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusPublished, publishedDraft.Status)
	assert.Equal(t, RoutingDeploymentStageActive, published.Activation.Stage)
}

func TestRoutingPolicyApprovalConcurrentRetriesProduceOneVotePerActor(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
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
	actors := []int{10, 10, 11, 11}
	errs := make([]error, len(actors))
	created := make([]bool, len(actors))
	approvals := make([]RoutingPolicyApproval, len(actors))
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(len(actors))
	for index := range actors {
		go func(index int) {
			defer wait.Done()
			<-start
			approvals[index], created[index], errs[index] = CreateRoutingPolicyApprovalContext(
				context.Background(), draft.ID, draft.Version, draft.ETag, activation, actors[index],
			)
		}(index)
	}
	close(start)
	wait.Wait()
	createdCount := 0
	idsByActor := make(map[int]int64)
	for index := range actors {
		require.NoError(t, errs[index])
		if created[index] {
			createdCount++
		}
		if id, exists := idsByActor[actors[index]]; exists {
			assert.Equal(t, id, approvals[index].ID)
		} else {
			idsByActor[actors[index]] = approvals[index].ID
		}
	}
	assert.Equal(t, 2, createdCount)
	quorum, err := RequireRoutingPolicyApprovalQuorumContext(
		context.Background(), draft, activation, RoutingPolicyRequiredApprovals,
	)
	require.NoError(t, err)
	assert.Len(t, quorum, 2)
}
