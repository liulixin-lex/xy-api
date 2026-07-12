package model

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingPolicyDraftLifecycleUsesCASAndPublishesAtomically(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingPolicyDraftLifecycleContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingPolicyDraftExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingPolicyDraftLifecycleContract(t, db, test.dbType)
		})
	}
}

func TestRoutingPolicyDraftPublishRejectsChangedHeadWithoutMutatingDraft(t *testing.T) {
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
	draft, err = ValidateRoutingPolicyDraftContext(context.Background(), draft.ID, draft.Version, draft.ETag, 2)
	require.NoError(t, err)

	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(150),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 3, Reason: "concurrent publish"},
	)
	require.NoError(t, err)

	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "stale draft"},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyRevisionConflict)
	stored, err := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Equal(t, draft.Version, stored.Version)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, stored.Status)
	assert.Zero(t, stored.PublishedRevision)
}

func TestRoutingPolicyDraftPublishFailureRollsBackRevisionAndDraft(t *testing.T) {
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
	draft, err = ValidateRoutingPolicyDraftContext(context.Background(), draft.ID, draft.Version, draft.ETag, 2)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_routing_draft_outbox
		BEFORE INSERT ON routing_config_outbox
		BEGIN
			SELECT RAISE(FAIL, 'forced draft publish outbox failure');
		END
	`).Error)

	_, _, err = PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "publish"},
	)
	require.Error(t, err)
	stored, err := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Equal(t, draft.Version, stored.Version)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, stored.Status)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, base.Revision.Revision, head.CurrentRevision)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
}

func TestRoutingPolicyDraftPublishPersistsOperationAtomically(t *testing.T) {
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
	draft, err = ValidateRoutingPolicyDraftContext(context.Background(), draft.ID, draft.Version, draft.ETag, 2)
	require.NoError(t, err)
	activation := RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 2, Reason: "publish"}
	_, _, err = CreateRoutingPolicyApprovalContext(context.Background(), draft.ID, draft.Version, draft.ETag, activation, 10)
	require.NoError(t, err)
	_, _, err = CreateRoutingPolicyApprovalContext(context.Background(), draft.ID, draft.Version, draft.ETag, activation, 11)
	require.NoError(t, err)

	publishedDraft, published, operation, err := PublishRoutingPolicyDraftWithOperationContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		activation,
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusPublished, publishedDraft.Status)
	assert.Equal(t, RoutingOperationTypePolicyPublish, operation.OperationType)
	assert.Equal(t, RoutingOperationSubjectPolicyDraft, operation.SubjectType)
	assert.Equal(t, draft.ID, operation.SubjectID)
	assert.Equal(t, RoutingOperationStatusSucceeded, operation.Status)
	assert.Equal(t, published.Revision.Revision, operation.ResultRevision)
	assert.Equal(t, published.Activation.ID, operation.ResultActivationID)
	assert.Equal(t, published.Outbox.ID, operation.ResultOutboxID)
	payload, err := operation.ResultPayload()
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"draft_id":`)
}

func TestRoutingPolicyDraftDetectsStoredDocumentTampering(t *testing.T) {
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
	require.NoError(t, db.Model(&RoutingPolicyDraft{}).Where("id = ?", draft.ID).
		Update("document_json", []byte(`{"schema_version":1,"pools":[]}`)).Error)

	_, err = GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftInvalid)
}

func runRoutingPolicyDraftLifecycleContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
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
	assert.Equal(t, RoutingPolicyDraftStatusEditing, draft.Status)
	assert.Equal(t, int64(1), draft.Version)
	assert.Equal(t, base.Revision.ContentHash, draft.BaseHash)
	require.Len(t, draft.ETag, 64)
	document, err := draft.Document()
	require.NoError(t, err)
	assert.Equal(t, int64(200), document.Pools[0].Members[0].Weight)
	persisted, err := GetRoutingPolicyDraftContext(context.Background(), draft.ID)
	require.NoError(t, err)
	draft = persisted

	_, err = UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, strings.Repeat("f", 64),
		routingPolicyDocumentForTest(250), 2,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftConflict)
	var conflict *RoutingPolicyDraftConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, draft.Version, conflict.ActualVersion)
	assert.Equal(t, draft.ETag, conflict.ActualETag)

	draft, err = UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		routingPolicyDocumentForTest(300), 2,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), draft.Version)
	assert.Equal(t, RoutingPolicyDraftStatusEditing, draft.Status)

	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 3,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(3), draft.Version)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, draft.Status)
	assert.Equal(t, base.Revision.Revision, draft.ValidatedHeadRevision)
	assert.Equal(t, base.Revision.ContentHash, draft.ValidatedHeadHash)

	draft, published, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 4, Reason: "publish draft"},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(4), draft.Version)
	assert.Equal(t, RoutingPolicyDraftStatusPublished, draft.Status)
	assert.Equal(t, published.Revision.Revision, draft.PublishedRevision)
	assert.Equal(t, base.Revision.Revision+1, published.Revision.Revision)
	assert.Equal(t, draft.DocumentHash, published.Revision.ContentHash)

	_, err = UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		routingPolicyDocumentForTest(400), 4,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftImmutable)

	drafts, hasMore, err := ListRoutingPolicyDraftsContext(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, drafts, 1)
	assert.Equal(t, draft.ID, drafts[0].ID)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, published.Revision.Revision, head.CurrentRevision)
}
