package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingPolicyDraftValidateRejectsInvalidLiveReferencesWithoutAdvancing(t *testing.T) {
	for _, test := range routingPolicyLiveReferenceMutationsForTest() {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			require.NoError(t, migrateRoutingPolicyModelsForTest(db))
			require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
			draft, err := CreateRoutingPolicyDraftContext(
				context.Background(), 0, routingPolicyDocumentForTest(100), 2,
			)
			require.NoError(t, err)
			require.NoError(t, test.mutate(db))

			_, err = ValidateRoutingPolicyDraftContext(
				context.Background(), draft.ID, draft.Version, draft.ETag, 2,
			)
			assert.ErrorIs(t, err, ErrRoutingPolicyReferenceInvalid)
			stored, loadErr := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
			require.NoError(t, loadErr)
			assert.Equal(t, draft.Version, stored.Version)
			assert.Equal(t, RoutingPolicyDraftStatusEditing, stored.Status)
			assert.Zero(t, stored.ValidatedTimeMs)
			assertRoutingPolicyRowCounts(t, 0, 0, 0, 0, 0)
			assertRoutingPolicyOperationCountForTest(t, 0)
		})
	}
}

func TestRoutingPolicyDraftPublishRejectsLiveReferenceDriftAtomically(t *testing.T) {
	for _, test := range routingPolicyLiveReferenceMutationsForTest() {
		t.Run(test.name, func(t *testing.T) {
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
			require.NoError(t, test.mutate(db))

			_, _, _, err = PublishRoutingPolicyDraftWithOperationContext(
				context.Background(), draft.ID, draft.Version, draft.ETag,
				RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "publish"},
			)
			assert.ErrorIs(t, err, ErrRoutingPolicyReferenceInvalid)
			stored, loadErr := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
			require.NoError(t, loadErr)
			assert.Equal(t, draft.Version, stored.Version)
			assert.Equal(t, RoutingPolicyDraftStatusValidated, stored.Status)
			assert.Zero(t, stored.PublishedRevision)
			head, loadErr := GetRoutingPolicyHeadContext(context.Background())
			require.NoError(t, loadErr)
			assert.Equal(t, base.Revision.Revision, head.CurrentRevision)
			assert.Equal(t, base.Activation.ID, head.CurrentActivationID)
			assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
			assertRoutingPolicyOperationCountForTest(t, 0)
		})
	}
}

func TestRoutingPolicyRollbackRejectsLiveReferenceDriftAtomically(t *testing.T) {
	for _, test := range routingPolicyLiveReferenceMutationsForTest() {
		t.Run(test.name, func(t *testing.T) {
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
			_, _, err = CreateRoutingPolicyRollbackApprovalContext(
				context.Background(), head, first.Revision.Revision, activation, 10,
			)
			require.NoError(t, err)
			_, _, err = CreateRoutingPolicyRollbackApprovalContext(
				context.Background(), head, first.Revision.Revision, activation, 11,
			)
			require.NoError(t, err)
			require.NoError(t, test.mutate(db))

			_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
				context.Background(), second.Revision.Revision, first.Revision.Revision, activation,
			)
			assert.ErrorIs(t, err, ErrRoutingPolicyReferenceInvalid)
			actualHead, loadErr := GetRoutingPolicyHeadContext(context.Background())
			require.NoError(t, loadErr)
			assert.Equal(t, second.Revision.Revision, actualHead.CurrentRevision)
			assert.Equal(t, second.Activation.ID, actualHead.CurrentActivationID)
			assertRoutingPolicyRowCounts(t, 2, 2, 2, 2, 2)
			assertRoutingPolicyOperationCountForTest(t, 0)
		})
	}
}

func routingPolicyLiveReferenceMutationsForTest() []struct {
	name   string
	mutate func(*gorm.DB) error
} {
	return []struct {
		name   string
		mutate func(*gorm.DB) error
	}{
		{
			name: "channel deleted",
			mutate: func(db *gorm.DB) error {
				return db.Where("id = ?", 1001).Delete(&Channel{}).Error
			},
		},
		{
			name: "credential inactive",
			mutate: func(db *gorm.DB) error {
				return db.Model(&RoutingCredentialRef{}).Where("id = ?", 201).Update("active", false).Error
			},
		},
		{
			name: "credential moved to another channel",
			mutate: func(db *gorm.DB) error {
				return db.Model(&RoutingCredentialRef{}).Where("id = ?", 201).Update("channel_id", 9999).Error
			},
		},
		{
			name: "model mapping cycle",
			mutate: func(db *gorm.DB) error {
				return db.Model(&Channel{}).Where("id = ?", 1001).
					Update("model_mapping", `{"gpt-test":"alias","alias":"gpt-test"}`).Error
			},
		},
		{
			name: "empty model mapping target",
			mutate: func(db *gorm.DB) error {
				return db.Model(&Channel{}).Where("id = ?", 1001).
					Update("model_mapping", `{"gpt-test":""}`).Error
			},
		},
	}
}
