package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPublishRoutingPolicyRevisionConcurrentCASHasSingleWinner(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	const publishers = 2
	start := make(chan struct{})
	results := make([]RoutingPolicyPublishResult, publishers)
	errs := make([]error, publishers)
	var wait sync.WaitGroup
	wait.Add(publishers)
	for index := 0; index < publishers; index++ {
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = PublishRoutingPolicyRevisionContext(
				context.Background(),
				0,
				routingPolicyDocumentForTest(int64(index+10)),
				RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100 + index},
			)
		}(index)
	}
	close(start)
	wait.Wait()

	winners := 0
	conflicts := 0
	for index := range errs {
		if errs[index] == nil {
			winners++
			assert.Equal(t, int64(1), results[index].Revision.Revision)
			continue
		}
		if errors.Is(errs[index], ErrRoutingPolicyRevisionConflict) {
			conflicts++
		}
	}
	assert.Equal(t, 1, winners)
	assert.Equal(t, 1, conflicts)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), head.CurrentRevision)
	assert.NotZero(t, head.CurrentActivationID)
	assert.Len(t, head.CurrentHash, 64)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
}

func TestPublishRoutingPolicyRevisionRollsBackEveryWriteWhenOutboxFails(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	require.NoError(t, DB.Exec(`
		CREATE TRIGGER fail_routing_config_outbox_insert
		BEFORE INSERT ON routing_config_outbox
		BEGIN
			SELECT RAISE(FAIL, 'forced outbox failure');
		END
	`).Error)

	_, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		routingPolicyDocumentForTest(10),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.Error(t, err)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Zero(t, head.CurrentRevision)
	assert.Zero(t, head.CurrentActivationID)
	assert.Empty(t, head.CurrentHash)
	assertRoutingPolicyRowCounts(t, 0, 0, 0, 0, 0)
}

func TestRoutingPolicyRollbackCreatesHigherImmutableRevision(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		routingPolicyDocumentForTest(10),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		1,
		routingPolicyDocumentForTest(20),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageCanary, TrafficBasisPoints: 100, ActorID: 101},
	)
	require.NoError(t, err)

	err = DB.Model(&RoutingPolicyRevision{}).
		Where("revision = ?", first.Revision.Revision).
		Update("content_hash", "tampered").Error
	require.ErrorIs(t, err, ErrRoutingPolicyHistoryImmutable)
	err = DB.Model(&RoutingPolicyMemberRevision{}).
		Where("revision = ?", first.Revision.Revision).
		Update("weight", 999).Error
	require.ErrorIs(t, err, ErrRoutingPolicyHistoryImmutable)

	rollback, err := RollbackRoutingPolicyRevisionContext(
		context.Background(),
		second.Revision.Revision,
		first.Revision.Revision,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 102, Reason: "regression"},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(3), rollback.Revision.Revision)
	assert.Equal(t, first.Revision.Revision, rollback.Revision.RollbackOfRevision)
	assert.Equal(t, first.Revision.ContentHash, rollback.Revision.ContentHash)

	firstDocument, _, err := LoadRoutingPolicyRevisionContext(context.Background(), first.Revision.Revision)
	require.NoError(t, err)
	rollbackDocument, _, err := LoadRoutingPolicyRevisionContext(context.Background(), rollback.Revision.Revision)
	require.NoError(t, err)
	assert.Equal(t, firstDocument, rollbackDocument)

	secondDocument, loadedSecond, err := LoadRoutingPolicyRevisionContext(context.Background(), second.Revision.Revision)
	require.NoError(t, err)
	assert.Equal(t, int64(20), secondDocument.Pools[0].Members[0].Weight)
	assert.Equal(t, second.Revision.ContentHash, loadedSecond.ContentHash)
	assertRoutingPolicyRowCounts(t, 3, 3, 3, 3, 3)
}

func TestLoadRoutingPolicyRevisionRejectsCorruptContent(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	invalid := routingPolicyDocumentForTest(10)
	invalid.Pools[0].Members = append(invalid.Pools[0].Members, invalid.Pools[0].Members[0])
	_, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		invalid,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.ErrorIs(t, err, ErrRoutingPolicyInvalid)
	assertRoutingPolicyRowCounts(t, 0, 0, 0, 0, 0)

	result, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		routingPolicyDocumentForTest(10),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)
	require.NoError(t, DB.Exec(
		"UPDATE routing_policy_member_revisions SET weight = ? WHERE revision = ?",
		999,
		result.Revision.Revision,
	).Error)

	_, _, err = LoadRoutingPolicyRevisionContext(context.Background(), result.Revision.Revision)
	require.ErrorIs(t, err, ErrRoutingPolicyContentCorrupt)
}

func TestRoutingPolicyRejectsHistoricalMemberIdentityRebinding(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	firstDocument := routingPolicyDocumentForTest(10)
	_, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, firstDocument,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)

	withoutMember := routingPolicyDocumentForTest(10)
	withoutMember.Pools[0].Members = nil
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 1, withoutMember,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 101},
	)
	require.NoError(t, err)

	rebound := routingPolicyDocumentForTest(10)
	rebound.Pools[0].Members[0].ChannelID = 1002
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 2, rebound,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 102},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyMemberIdentity)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), head.CurrentRevision)

	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 2, firstDocument,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 103},
	)
	require.NoError(t, err)
	assertRoutingPolicyRowCounts(t, 3, 3, 2, 3, 3)
}

func TestRoutingPolicyRejectsHistoricalPoolIdentityRebinding(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	firstDocument := routingPolicyDocumentForTest(10)
	_, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, firstDocument,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)

	displayChanged := routingPolicyDocumentForTest(10)
	displayChanged.Pools[0].DisplayName = "VIP production"
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 1, displayChanged,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 101},
	)
	require.NoError(t, err)

	empty := RoutingPolicyDocument{SchemaVersion: RoutingPolicySchemaVersion}
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 2, empty,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 102},
	)
	require.NoError(t, err)

	renamed := routingPolicyDocumentForTest(10)
	renamed.Pools[0].GroupName = "PRO"
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 3, renamed,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 103},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyPoolIdentity)

	reclaimed := RoutingPolicyDocument{
		SchemaVersion: RoutingPolicySchemaVersion,
		Pools: []RoutingPolicyPoolContent{{
			PoolID:          12,
			GroupName:       "VIP",
			DisplayName:     "Replacement",
			DeploymentStage: RoutingDeploymentStageShadow,
			PolicyProfile:   RoutingPolicyProfileBalanced,
		}},
	}
	_, err = PublishRoutingPolicyRevisionContext(
		context.Background(), 3, reclaimed,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 104},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyPoolIdentity)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), head.CurrentRevision)
}

func TestRoutingPolicySupportsEmptyRevisionAndRejectsNonPositiveCredentialIDs(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	empty, hash, err := NormalizeRoutingPolicyDocument(RoutingPolicyDocument{})
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicySchemaVersion, empty.SchemaVersion)
	assert.Empty(t, empty.Pools)
	assert.Len(t, hash, 64)
	published, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, empty,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageObserve, ActorID: 0, Reason: "empty_install"},
	)
	require.NoError(t, err)
	assert.Zero(t, published.Revision.PoolCount)
	assert.Zero(t, published.Revision.MemberCount)
	loaded, _, err := LoadRoutingPolicyRevisionContext(context.Background(), published.Revision.Revision)
	require.NoError(t, err)
	assert.Empty(t, loaded.Pools)
	assertRoutingPolicyRowCounts(t, 1, 0, 0, 1, 1)

	for _, credentialID := range []int{0, -1} {
		document := routingPolicyDocumentForTest(10)
		document.Pools[0].Members[0].CredentialIDs = []int{credentialID}
		_, _, normalizeErr := NormalizeRoutingPolicyDocument(document)
		assert.ErrorIs(t, normalizeErr, ErrRoutingPolicyInvalid)
	}
}

func TestRoutingPolicyAllowsLargePoolAndRejectsTopologyLimit(t *testing.T) {
	large := routingPolicyDocumentForTest(10)
	large.Pools[0].Members = make([]RoutingPolicyMemberContent, 65)
	for index := range large.Pools[0].Members {
		large.Pools[0].Members[index] = RoutingPolicyMemberContent{
			MemberID: index + 1, ChannelID: index + 1, Enabled: true, Weight: 1,
		}
	}
	_, _, err := NormalizeRoutingPolicyDocument(large)
	require.NoError(t, err)

	document := routingPolicyDocumentForTest(10)
	document.Pools[0].Members = make([]RoutingPolicyMemberContent, RoutingPolicyMaxMembersPerPool+1)
	for index := range document.Pools[0].Members {
		document.Pools[0].Members[index] = RoutingPolicyMemberContent{
			MemberID: index + 1, ChannelID: index + 1, Enabled: true, Weight: 1,
		}
	}

	_, _, err = NormalizeRoutingPolicyDocument(document)
	assert.ErrorIs(t, err, ErrRoutingPolicyInvalid)
	var limitErr *RoutingPolicyPoolLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, 11, limitErr.PoolID)
	assert.Equal(t, "VIP", limitErr.GroupName)
	assert.Equal(t, RoutingPolicyMaxMembersPerPool+1, limitErr.MemberCount)
	assert.Equal(t, RoutingPolicyMaxMembersPerPool, limitErr.Limit)
}

func TestRoutingPolicyLargeJSONUsesByteBoundedInsertBatches(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	largeObject := json.RawMessage(`{"payload":"` + strings.Repeat("x", 30<<10) + `"}`)
	document := RoutingPolicyDocument{
		SchemaVersion: RoutingPolicySchemaVersion,
		Pools:         make([]RoutingPolicyPoolContent, 40),
	}
	for index := range document.Pools {
		document.Pools[index] = RoutingPolicyPoolContent{
			PoolID:          index + 1,
			GroupName:       fmt.Sprintf("group-%03d", index),
			DisplayName:     fmt.Sprintf("Group %03d", index),
			DeploymentStage: RoutingDeploymentStageShadow,
			PolicyProfile:   RoutingPolicyProfileCustom,
			Policy:          append(json.RawMessage(nil), largeObject...),
			Members: []RoutingPolicyMemberContent{{
				MemberID:  index + 1,
				ChannelID: index + 1,
				Enabled:   true,
				Weight:    1,
				Overrides: append(json.RawMessage(nil), largeObject...),
			}},
		}
	}

	poolBatches := 0
	memberBatches := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:routing_policy_batch_bytes", func(tx *gorm.DB) {
		switch rows := tx.Statement.Dest.(type) {
		case *[]RoutingPolicyPoolRevision:
			poolBatches++
			batchBytes := 0
			for index := range *rows {
				batchBytes += routingPolicyPoolRowEncodedSize((*rows)[index])
			}
			assert.LessOrEqual(t, batchBytes, routingPolicyInsertBatchMaxBytes)
		case *[]RoutingPolicyMemberRevision:
			memberBatches++
			batchBytes := 0
			for index := range *rows {
				batchBytes += routingPolicyMemberRowEncodedSize((*rows)[index])
			}
			assert.LessOrEqual(t, batchBytes, routingPolicyInsertBatchMaxBytes)
		}
	}))

	published, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, document,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100, Reason: "large_policy"},
	)
	require.NoError(t, err)
	assert.Equal(t, 40, published.Revision.PoolCount)
	assert.Equal(t, 40, published.Revision.MemberCount)
	assert.Greater(t, poolBatches, 1)
	assert.Greater(t, memberBatches, 1)
}

func TestRoutingPolicyMigrationIsIdempotentAndRevisionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing-policy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	first, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		routingPolicyDocumentForTest(10),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Revision.Revision)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	reopened, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	DB = reopened
	LOG_DB = reopened
	reopenedSQL, err := reopened.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopenedSQL.Close() })
	require.NoError(t, migrateRoutingPolicyModelsForTest(reopened))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), head.CurrentRevision)
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		head.CurrentRevision,
		routingPolicyDocumentForTest(20),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageCanary, TrafficBasisPoints: 100, ActorID: 101},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.Revision.Revision)

	var headCount int64
	require.NoError(t, DB.Model(&RoutingPolicyHead{}).Count(&headCount).Error)
	assert.Equal(t, int64(1), headCount)
	assertRoutingPolicyRowCounts(t, 2, 2, 2, 2, 2)
}

func TestRoutingConfigOutboxClaimReleaseAndPublishLifecycle(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	_, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		0,
		routingPolicyDocumentForTest(10),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 100},
	)
	require.NoError(t, err)

	claimed, err := ClaimRoutingConfigOutboxContext(context.Background(), 100, 30)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Len(t, claimed.ClaimToken, 32)
	assert.Equal(t, 1, claimed.Attempts)
	assert.Equal(t, int64(130), claimed.ClaimedUntil)

	none, err := ClaimRoutingConfigOutboxContext(context.Background(), 101, 30)
	require.NoError(t, err)
	assert.Nil(t, none)
	require.ErrorIs(t, MarkRoutingConfigOutboxPublishedContext(context.Background(), claimed.ID, strings.Repeat("0", 32), 102), ErrRoutingConfigOutboxClaimLost)
	require.NoError(t, ReleaseRoutingConfigOutboxClaimContext(context.Background(), claimed.ID, claimed.ClaimToken, 110, errors.New("redis timeout token-secret")))

	none, err = ClaimRoutingConfigOutboxContext(context.Background(), 109, 30)
	require.NoError(t, err)
	assert.Nil(t, none)
	retried, err := ClaimRoutingConfigOutboxContext(context.Background(), 110, 30)
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, 2, retried.Attempts)
	assert.NotEqual(t, claimed.ClaimToken, retried.ClaimToken)
	require.NoError(t, MarkRoutingConfigOutboxPublishedContext(context.Background(), retried.ID, retried.ClaimToken, 111))

	none, err = ClaimRoutingConfigOutboxContext(context.Background(), 200, 30)
	require.NoError(t, err)
	assert.Nil(t, none)
	var stored RoutingConfigOutbox
	require.NoError(t, DB.First(&stored, retried.ID).Error)
	assert.Equal(t, int64(111), stored.PublishedTime)
	assert.Empty(t, stored.ClaimToken)
	assert.Empty(t, stored.LastError)
}

func TestRoutingRuntimeCheckpointIsMonotonicAndCollisionSafe(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))

	first, err := NewRoutingRuntimeCheckpoint("node-a", "config_stream", "routing:v2:config", 1, 10, map[string]any{"cursor": "1-0"}, 100, 200)
	require.NoError(t, err)
	stored, err := UpsertRoutingRuntimeCheckpointContext(context.Background(), first)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.PolicyRevision)
	assert.Equal(t, int64(10), stored.Sequence)

	older, err := NewRoutingRuntimeCheckpoint("node-a", "config_stream", "routing:v2:config", 0, 99, map[string]any{"cursor": "0-9"}, 101, 201)
	require.NoError(t, err)
	stored, err = UpsertRoutingRuntimeCheckpointContext(context.Background(), older)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.PolicyRevision)
	assert.Equal(t, int64(10), stored.Sequence)

	collision, err := NewRoutingRuntimeCheckpoint("node-a", "config_stream", "routing:v2:config", 1, 10, map[string]any{"cursor": "different"}, 102, 202)
	require.NoError(t, err)
	_, err = UpsertRoutingRuntimeCheckpointContext(context.Background(), collision)
	assert.ErrorIs(t, err, ErrRoutingRuntimeCheckpointInvalid)

	newer, err := NewRoutingRuntimeCheckpoint("node-a", "config_stream", "routing:v2:config", 2, 11, map[string]any{"cursor": "2-0"}, 103, 203)
	require.NoError(t, err)
	stored, err = UpsertRoutingRuntimeCheckpointContext(context.Background(), newer)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stored.PolicyRevision)
	assert.Equal(t, int64(11), stored.Sequence)

	loaded, err := GetRoutingRuntimeCheckpointContext(context.Background(), "node-a", "config_stream", "routing:v2:config")
	require.NoError(t, err)
	assert.Equal(t, stored.PayloadHash, loaded.PayloadHash)
}

func routingPolicyDocumentForTest(weight int64) RoutingPolicyDocument {
	return RoutingPolicyDocument{
		SchemaVersion: RoutingPolicySchemaVersion,
		Pools: []RoutingPolicyPoolContent{{
			PoolID:          11,
			GroupName:       "VIP",
			DisplayName:     "VIP",
			DeploymentStage: RoutingDeploymentStageShadow,
			PolicyProfile:   RoutingPolicyProfileBalanced,
			Members: []RoutingPolicyMemberContent{{
				MemberID:      101,
				ChannelID:     1001,
				Enabled:       true,
				Priority:      1,
				Weight:        weight,
				CredentialIDs: []int{201, 202},
			}},
		}},
	}
}

func migrateRoutingPolicyModelsForTest(db *gorm.DB) error {
	return db.AutoMigrate(
		&RoutingPolicyHead{},
		&RoutingPolicyRevision{},
		&RoutingPolicyPoolRevision{},
		&RoutingPolicyMemberRevision{},
		&RoutingPolicyActivation{},
		&RoutingConfigOutbox{},
		&RoutingRuntimeCheckpoint{},
	)
}

func assertRoutingPolicyRowCounts(t *testing.T, revisions, pools, members, activations, outbox int64) {
	t.Helper()
	for _, item := range []struct {
		model any
		want  int64
	}{
		{model: &RoutingPolicyRevision{}, want: revisions},
		{model: &RoutingPolicyPoolRevision{}, want: pools},
		{model: &RoutingPolicyMemberRevision{}, want: members},
		{model: &RoutingPolicyActivation{}, want: activations},
		{model: &RoutingConfigOutbox{}, want: outbox},
	} {
		var count int64
		require.NoError(t, DB.Model(item.model).Count(&count).Error)
		assert.Equal(t, item.want, count)
	}
}
