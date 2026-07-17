package model

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConvertRoutingPolicyDocumentToV2PreservesOnlyProvenLifecycleOverrides(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))

	legacy := routingPolicyLegacyDocumentForConversionTest()
	converted, summary, err := ConvertRoutingPolicyDocumentToV2DBContext(
		context.Background(), db, legacy, true,
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyLegacySchemaVersion, summary.SourceSchemaVersion)
	assert.Equal(t, 1, summary.PreservedPools)
	assert.Equal(t, 1, summary.PreservedMemberOverrides)
	assert.Equal(t, 1, summary.ResetMemberOverrides)
	assert.Equal(t, 1, summary.DroppedCredentialRefs)
	require.Len(t, converted.Pools, 1)
	pool := converted.Pools[0]
	require.NotNil(t, pool.DefaultEnabled)
	require.NotNil(t, pool.DefaultPriority)
	require.NotNil(t, pool.DefaultWeight)
	assert.True(t, *pool.DefaultEnabled)
	assert.Zero(t, *pool.DefaultPriority)
	assert.Equal(t, int64(100), *pool.DefaultWeight)
	require.Len(t, pool.Members, 1)
	member := pool.Members[0]
	assert.Equal(t, 101, member.MemberID)
	assert.Equal(t, routingPolicyGenerationForChannelTest(1001), member.RoutingGeneration)
	require.NotNil(t, member.EnabledOverride)
	require.NotNil(t, member.PriorityOverride)
	require.NotNil(t, member.WeightOverride)
	assert.False(t, *member.EnabledOverride)
	assert.Equal(t, int64(7), *member.PriorityOverride)
	assert.Equal(t, int64(42), *member.WeightOverride)
	assert.Equal(t, []int{201}, member.CredentialIDs)
	assert.JSONEq(t, `{"region":"primary"}`, string(member.Overrides))

	reset, resetSummary, err := ConvertRoutingPolicyDocumentToV2DBContext(
		context.Background(), db, legacy, false,
	)
	require.NoError(t, err)
	require.Len(t, reset.Pools, 1)
	assert.Empty(t, reset.Pools[0].Members)
	assert.Equal(t, 2, resetSummary.ResetMemberOverrides)
	assert.Equal(t, 2, resetSummary.DroppedCredentialRefs)
}

func TestConvertRoutingPolicyDocumentToV2DropsUnprovenCredentialReference(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	legacy := routingPolicyLegacyDocumentForConversionTest()
	legacy.Pools[0].Members = legacy.Pools[0].Members[:1]
	legacy.Pools[0].Members[0].CredentialIDs = []int{999}

	converted, summary, err := ConvertRoutingPolicyDocumentToV2DBContext(
		context.Background(), db, legacy, true,
	)
	require.NoError(t, err)
	require.Len(t, converted.Pools, 1)
	require.Len(t, converted.Pools[0].Members, 1)
	assert.Empty(t, converted.Pools[0].Members[0].CredentialIDs)
	assert.Equal(t, 1, summary.DroppedCredentialRefs)
}

func TestLegacyRoutingPolicyHistoryRequiresSafeV2DraftBeforeRollback(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingPolicyModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	legacy := routingPolicyLegacyDocumentForConversionTest()
	legacy.Pools[0].Members = legacy.Pools[0].Members[:1]
	legacyRevision := seedLegacyRoutingPolicyRevisionForTest(t, db, legacy)

	current, err := PublishRoutingPolicyRevisionContext(
		context.Background(), legacyRevision.Revision, routingPolicyDocumentForTest(300),
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageShadow, ActorID: 2, Reason: "current v2"},
	)
	require.NoError(t, err)
	loadedLegacy, loadedRevision, err := LoadRoutingPolicyRevisionContext(
		context.Background(), legacyRevision.Revision,
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyLegacySchemaVersion, loadedRevision.SchemaVersion)
	assert.Equal(t, RoutingPolicyLegacySchemaVersion, loadedLegacy.SchemaVersion)

	_, _, err = RollbackRoutingPolicyRevisionWithOperationContext(
		context.Background(), current.Revision.Revision, legacyRevision.Revision,
		RoutingPolicyActivationSpec{Stage: RoutingDeploymentStageActive, ActorID: 3, Reason: "unsafe direct rollback"},
	)
	assert.ErrorIs(t, err, ErrRoutingPolicyLegacyRollback)

	converted, summary, err := ConvertRoutingPolicyDocumentToV2DBContext(
		context.Background(), db, loadedLegacy, true,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.PreservedMemberOverrides)
	draft, err := CreateRoutingPolicyDraftContext(
		context.Background(), current.Revision.Revision, converted, 3,
	)
	require.NoError(t, err)
	validated, err := ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 3,
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingPolicyDraftStatusValidated, validated.Status)
}

func routingPolicyLegacyDocumentForConversionTest() RoutingPolicyDocument {
	return RoutingPolicyDocument{
		SchemaVersion: RoutingPolicyLegacySchemaVersion,
		Pools: []RoutingPolicyPoolContent{{
			PoolID: 11, GroupName: "VIP", DisplayName: "VIP",
			DeploymentStage: RoutingDeploymentStageShadow,
			PolicyProfile:   RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{}`),
			Members: []RoutingPolicyMemberContent{
				{
					MemberID: 101, ChannelID: 1001, Enabled: false, Priority: 7, Weight: 42,
					CredentialIDs: []int{201}, Overrides: json.RawMessage(`{"region":"primary"}`),
				},
				{
					MemberID: 999, ChannelID: 9999, Enabled: true, Priority: 1, Weight: 10,
					CredentialIDs: []int{999}, Overrides: json.RawMessage(`{"legacy":true}`),
				},
			},
		}},
	}
}

func seedLegacyRoutingPolicyRevisionForTest(
	t *testing.T,
	db *gorm.DB,
	document RoutingPolicyDocument,
) RoutingPolicyRevision {
	t.Helper()
	normalized, contentHash, err := NormalizeRoutingPolicyDocument(document)
	require.NoError(t, err)
	require.Equal(t, RoutingPolicyLegacySchemaVersion, normalized.SchemaVersion)
	now := time.Now().Unix()
	revision := RoutingPolicyRevision{
		Revision: 1, SchemaVersion: RoutingPolicyLegacySchemaVersion, ContentHash: contentHash,
		PoolCount: len(normalized.Pools), MemberCount: routingPolicyDocumentMemberCount(normalized),
		ActorID: 1, Reason: "legacy policy", CreatedTime: now,
	}
	poolRows, memberRows, err := routingPolicyRevisionRows(revision.Revision, normalized)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if len(poolRows) > 0 {
			if err := tx.Create(&poolRows).Error; err != nil {
				return err
			}
		}
		if len(memberRows) > 0 {
			if err := tx.Create(&memberRows).Error; err != nil {
				return err
			}
		}
		activation := RoutingPolicyActivation{
			Revision: revision.Revision, Stage: RoutingDeploymentStageShadow,
			ActorID: 1, Reason: "legacy policy", CreatedTime: now,
		}
		if err := tx.Create(&activation).Error; err != nil {
			return err
		}
		return tx.Model(&RoutingPolicyHead{}).Where("id = ?", routingPolicyHeadID).Updates(map[string]any{
			"current_revision": revision.Revision, "current_activation_id": activation.ID,
			"current_hash": contentHash, "current_stage": activation.Stage, "updated_time": now,
		}).Error
	}))
	return revision
}
