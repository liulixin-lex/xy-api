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

func TestRoutingPolicyDraftPreservesUnknownExtensionsAcrossCreateReadUpdateAndPublish(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	var document RoutingPolicyDocument
	require.NoError(t, common.Unmarshal([]byte(`{
		"schema_version":2,
		"pools":[{
			"pool_id":11,
			"group_name":"VIP",
			"display_name":"VIP",
			"deployment_stage":"shadow",
			"policy_profile":"balanced",
			"default_enabled":true,
			"default_priority":0,
			"default_weight":100,
			"policy":{"future_policy":{"mode":"adaptive","revision":9007199254740993}},
			"members":[{
				"member_id":101,
				"channel_id":1001,
				"routing_generation":"000000000000000000000000000003e9",
				"enabled":true,
				"priority":1,
				"weight":100,
				"enabled_override":true,
				"priority_override":1,
				"weight_override":100,
				"credential_ids":[201,202],
				"overrides":{"future_override":{"enabled":true,"label":"blue","revision":9007199254740993}},
				"member_extension":{"failure_domain":"zone-a","ordinal":9007199254740993}
			}],
			"pool_extension":{"owner":"operations","labels":["primary","llm"]}
		}],
		"root_extension":{"format":"vendor-next","revision":3}
	}`), &document))
	assertRoutingPolicyExtensionFixture(t, document, 100)

	draft, err := CreateRoutingPolicyDraftContext(context.Background(), 0, document, 10)
	require.NoError(t, err)
	created, err := draft.Document()
	require.NoError(t, err)
	assertRoutingPolicyExtensionFixture(t, created, 100)

	weight := int64(250)
	created.Pools[0].Members[0].WeightOverride = &weight
	draft, err = UpdateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, created, 10,
	)
	require.NoError(t, err)
	updated, err := draft.Document()
	require.NoError(t, err)
	assertRoutingPolicyExtensionFixture(t, updated, 250)

	draft, err = ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 10,
	)
	require.NoError(t, err)
	draft, published, err := PublishRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 10, Reason: "extension round trip"},
	)
	require.NoError(t, err)
	require.NotNil(t, published.Revision.ExtensionsJSON)
	assert.Contains(t, *published.Revision.ExtensionsJSON, `"root_extension"`)

	publishedDocument, revision, err := LoadRoutingPolicyRevisionContext(
		context.Background(), published.Revision.Revision,
	)
	require.NoError(t, err)
	assert.Equal(t, draft.DocumentHash, revision.ContentHash)
	assertRoutingPolicyExtensionFixture(t, publishedDocument, 250)
}

func assertRoutingPolicyExtensionFixture(t *testing.T, document RoutingPolicyDocument, weight int64) {
	t.Helper()
	require.Len(t, document.Pools, 1)
	require.Len(t, document.Pools[0].Members, 1)
	assert.Equal(t, weight, document.Pools[0].Members[0].Weight)
	assert.JSONEq(t, `{"format":"vendor-next","revision":3}`, string(document.ExtensionFields["root_extension"]))
	assert.JSONEq(t, `{"owner":"operations","labels":["primary","llm"]}`, string(document.Pools[0].ExtensionFields["pool_extension"]))
	assert.JSONEq(t, `{"failure_domain":"zone-a","ordinal":9007199254740993}`, string(document.Pools[0].Members[0].ExtensionFields["member_extension"]))
	assert.Contains(t, string(document.Pools[0].Members[0].ExtensionFields["member_extension"]), "9007199254740993")
	assert.JSONEq(t, `{"future_policy":{"mode":"adaptive","revision":9007199254740993}}`, string(document.Pools[0].Policy))
	assert.JSONEq(t, `{"future_override":{"enabled":true,"label":"blue","revision":9007199254740993}}`, string(document.Pools[0].Members[0].Overrides))
	assert.Contains(t, string(document.Pools[0].Policy), "9007199254740993")
	assert.Contains(t, string(document.Pools[0].Members[0].Overrides), "9007199254740993")
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
	assert.Empty(t, drafts)
	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, published.Revision.Revision, head.CurrentRevision)
}

func TestRoutingPolicyDraftWorkspaceDerivesStaleStateAndBatchDeleteIsAtomic(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, routingPolicyDocumentForTest(100),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	first, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	second, err := CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(300), 3,
	)
	require.NoError(t, err)
	first, err = ValidateRoutingPolicyDraftContext(
		context.Background(), first.ID, first.Version, first.ETag, 2,
	)
	require.NoError(t, err)

	advanced, err := PublishRoutingPolicyRevisionContext(
		context.Background(), base.Revision.Revision, routingPolicyDocumentForTest(400),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 4, Reason: "advance"},
	)
	require.NoError(t, err)
	assert.Equal(t, base.Revision.Revision+1, advanced.Revision.Revision)

	drafts, hasMore, err := ListRoutingPolicyDraftsContext(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, drafts, 2)
	for _, draft := range drafts {
		assert.Equal(t, RoutingPolicyDraftWorkspaceStale, draft.WorkspaceState)
		assert.True(t, draft.Stale)
		assert.True(t, draft.CanDelete)
		assert.False(t, draft.CanValidate)
		assert.False(t, draft.CanPublish)
		assert.Equal(t, "base_policy_changed", draft.BlockingReason)
	}

	_, err = ValidateRoutingPolicyDraftContext(
		context.Background(), second.ID, second.Version, second.ETag, 3,
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyRevisionConflict)
	unchanged, err := GetRoutingPolicyDraftContext(context.Background(), second.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusEditing, unchanged.Status)
	assert.Equal(t, second.Version, unchanged.Version)

	err = DeleteRoutingPolicyDraftsContext(context.Background(), []RoutingPolicyDraftDeleteSpec{
		{ID: first.ID, ExpectedVersion: first.Version, ExpectedETag: first.ETag},
		{ID: second.ID, ExpectedVersion: second.Version, ExpectedETag: strings.Repeat("f", 64)},
	})
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftConflict)
	_, err = GetRoutingPolicyDraftContext(context.Background(), first.ID)
	require.NoError(t, err)
	_, err = GetRoutingPolicyDraftContext(context.Background(), second.ID)
	require.NoError(t, err)

	require.NoError(t, DeleteRoutingPolicyDraftsContext(context.Background(), []RoutingPolicyDraftDeleteSpec{
		{ID: first.ID, ExpectedVersion: first.Version, ExpectedETag: first.ETag},
		{ID: second.ID, ExpectedVersion: second.Version, ExpectedETag: second.ETag},
	}))
	_, err = GetRoutingPolicyDraftContext(context.Background(), first.ID)
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftNotFound)
	_, err = GetRoutingPolicyDraftContext(context.Background(), second.ID)
	assert.ErrorIs(t, err, ErrRoutingPolicyDraftNotFound)
}
