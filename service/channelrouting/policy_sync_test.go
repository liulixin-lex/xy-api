package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRefreshSnapshotMigratesLegacyPolicyExactlyOnce(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 903, Name: "legacy-policy", Key: "key-a", Group: "VIP", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var pool model.RoutingPool
	require.NoError(t, db.Where("group_name = ? AND active = ?", "VIP", true).First(&pool).Error)
	require.NoError(t, model.EnsureRoutingPolicyHeadDBContext(context.Background(), db))
	legacy := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicyLegacySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: pool.ID, GroupName: pool.GroupName, DisplayName: "Legacy VIP",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileCustom,
			Policy:          json.RawMessage(`{"hedge_enabled":false}`),
			Members:         []model.RoutingPolicyMemberContent{},
		}},
	}
	legacy, contentHash, err := model.NormalizeRoutingPolicyDocument(legacy)
	require.NoError(t, err)
	groupHash := sha256.Sum256([]byte(pool.GroupName))
	now := time.Now().Unix()
	revision := model.RoutingPolicyRevision{
		Revision: 1, SchemaVersion: model.RoutingPolicyLegacySchemaVersion, ContentHash: contentHash,
		PoolCount: 1, ActorID: 42, Reason: "legacy manual policy", CreatedTime: now,
	}
	poolRow := model.RoutingPolicyPoolRevision{
		Revision: 1, PoolID: pool.ID, GroupKey: fmt.Sprintf("%x", groupHash),
		GroupName: pool.GroupName, DisplayName: legacy.Pools[0].DisplayName,
		DeploymentStage: legacy.Pools[0].DeploymentStage, PolicyProfile: legacy.Pools[0].PolicyProfile,
		PolicyJSON: string(legacy.Pools[0].Policy),
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if err := tx.Create(&poolRow).Error; err != nil {
			return err
		}
		activation := model.RoutingPolicyActivation{
			Revision: 1, Stage: model.RoutingDeploymentStageShadow,
			ActorID: 42, Reason: "legacy manual policy", CreatedTime: now,
		}
		if err := tx.Create(&activation).Error; err != nil {
			return err
		}
		return tx.Model(&model.RoutingPolicyHead{}).Where("id = ?", 1).Updates(map[string]any{
			"current_revision": int64(1), "current_activation_id": activation.ID,
			"current_hash": contentHash, "current_stage": activation.Stage, "updated_time": now,
		}).Error
	}))

	first, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(2), first.PolicyRevision)
	second, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first.PolicyRevision, second.PolicyRevision)
	var revisionCount int64
	require.NoError(t, db.Model(&model.RoutingPolicyRevision{}).Count(&revisionCount).Error)
	assert.Equal(t, int64(2), revisionCount)

	legacyHistory, legacyRevision, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, 1)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingPolicyLegacySchemaVersion, legacyRevision.SchemaVersion)
	assert.Equal(t, "Legacy VIP", legacyHistory.Pools[0].DisplayName)
	current, currentRevision, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, 2)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingPolicySchemaVersion, currentRevision.SchemaVersion)
	require.Len(t, current.Pools, 1)
	assert.Equal(t, "Legacy VIP", current.Pools[0].DisplayName)
	assert.Empty(t, current.Pools[0].Members)
}

func TestSyncLegacyRoutingPolicyDoesNotPublishTopologyOnlyChanges(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 901, Name: "policy-sync", Key: "key-a", Group: "VIP", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	first, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.CurrentRevision)
	assert.Len(t, first.CurrentHash, 64)

	unchanged, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first.CurrentRevision, unchanged.CurrentRevision)
	var outboxCount int64
	require.NoError(t, db.Model(&model.RoutingConfigOutbox{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)

	weight := uint(25)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 901).Update("weight", weight).Error)
	topology, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	changed, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first.CurrentRevision, changed.CurrentRevision)
	document, revision, err := model.LoadRoutingPolicyRevisionContext(context.Background(), changed.CurrentRevision)
	require.NoError(t, err)
	assert.Equal(t, changed.CurrentHash, revision.ContentHash)
	require.Len(t, document.Pools, 1)
	assert.Empty(t, document.Pools[0].Members)
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(first.CurrentRevision), view.PolicyRevision)
	assert.Equal(t, uint64(topology.TopologyEpoch), view.TopologyEpoch)
	require.Len(t, view.Pools, 1)
	require.Len(t, view.Pools[0].Members, 1)
	assert.Equal(t, int64(weight), view.Pools[0].Members[0].LegacyWeight)
	require.NoError(t, db.Model(&model.RoutingConfigOutbox{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)
}

func TestSyncLegacyRoutingPolicyPreservesManualPolicyAcrossTopologyChanges(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 902, Name: "policy-preserve", Key: "key-a", Group: "VIP", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	initialHead, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	initialDocument, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, initialHead.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, initialDocument.Pools, 1)
	require.Empty(t, initialDocument.Pools[0].Members)
	var topologyMember model.RoutingPoolMember
	require.NoError(t, db.Where("channel_id = ? AND active = ?", 902, true).First(&topologyMember).Error)

	manualDocument := initialDocument
	manualDocument.ExtensionFields = map[string]json.RawMessage{
		"root_extension": json.RawMessage(`{"owner":"routing-platform"}`),
	}
	manualDocument.Pools[0].DeploymentStage = model.RoutingDeploymentStageShadow
	manualDocument.Pools[0].PolicyProfile = model.RoutingPolicyProfileCustom
	manualDocument.Pools[0].Policy = json.RawMessage(`{"max_error_rate":0.05}`)
	manualDocument.Pools[0].ExtensionFields = map[string]json.RawMessage{
		"pool_extension": json.RawMessage(`{"tier":"critical"}`),
	}
	enabled := false
	priority := int64(88)
	weight := int64(77)
	manualDocument.Pools[0].Members = []model.RoutingPolicyMemberContent{{
		MemberID: topologyMember.ID, ChannelID: topologyMember.ChannelID,
		RoutingGeneration: topologyMember.ChannelGeneration,
		Enabled:           false, Priority: priority, Weight: weight,
		EnabledOverride: &enabled, PriorityOverride: &priority, WeightOverride: &weight,
		Overrides: json.RawMessage(`{"region":"primary"}`),
		ExtensionFields: map[string]json.RawMessage{
			"member_extension": json.RawMessage(`{"zone":"a"}`),
		},
	}}
	manual, err := model.PublishRoutingPolicyRevisionDBContext(
		context.Background(),
		db,
		initialHead.CurrentRevision,
		manualDocument,
		model.RoutingPolicyActivationSpec{
			Stage:              model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: 250,
			ActorID:            42,
			Reason:             "admin_policy_update",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), manual.Revision.Revision)

	for _, key := range []string{"key-b", "key-c"} {
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 902).Update("key", key).Error)
		_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
		require.NoError(t, err)
		head, syncErr := SyncLegacyRoutingPolicyContext(context.Background())
		require.NoError(t, syncErr)
		assert.Equal(t, manual.Revision.Revision, head.CurrentRevision)

		document, revision, loadErr := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision)
		require.NoError(t, loadErr)
		require.Len(t, document.Pools, 1)
		require.Len(t, document.Pools[0].Members, 1)
		pool := document.Pools[0]
		member := pool.Members[0]
		assert.Equal(t, model.RoutingDeploymentStageShadow, pool.DeploymentStage)
		assert.Equal(t, model.RoutingPolicyProfileCustom, pool.PolicyProfile)
		assert.JSONEq(t, `{"max_error_rate":0.05}`, string(pool.Policy))
		assert.JSONEq(t, `{"owner":"routing-platform"}`, string(document.ExtensionFields["root_extension"]))
		assert.JSONEq(t, `{"tier":"critical"}`, string(pool.ExtensionFields["pool_extension"]))
		assert.False(t, member.Enabled)
		assert.Equal(t, int64(88), member.Priority)
		assert.Equal(t, int64(77), member.Weight)
		assert.JSONEq(t, `{"region":"primary"}`, string(member.Overrides))
		assert.JSONEq(t, `{"zone":"a"}`, string(member.ExtensionFields["member_extension"]))
		assert.Empty(t, member.CredentialIDs)
		assert.Equal(t, "admin_policy_update", revision.Reason)

		var activation model.RoutingPolicyActivation
		require.NoError(t, db.Where("id = ?", head.CurrentActivationID).First(&activation).Error)
		assert.Equal(t, model.RoutingDeploymentStageCanary, activation.Stage)
		assert.Equal(t, 250, activation.TrafficBasisPoints)
	}
}

func TestSyncLegacyRoutingPolicyNormalizesHistoricalNonCanaryTraffic(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 904, Name: "normalize-traffic", Group: "default", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	initial, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		"UPDATE routing_policy_activations SET traffic_basis_points = 250 WHERE id = ?",
		initial.CurrentActivationID,
	).Error)

	healed, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, initial.CurrentRevision+1, healed.CurrentRevision)
	var activation model.RoutingPolicyActivation
	require.NoError(t, db.Where("id = ?", healed.CurrentActivationID).First(&activation).Error)
	assert.Equal(t, model.RoutingDeploymentStageObserve, activation.Stage)
	assert.Zero(t, activation.TrafficBasisPoints)
	assert.Equal(t, legacyRoutingPolicyNormalizeActivationReason, activation.Reason)
}

func TestSyncLegacyRoutingPolicySafelyDowngradesHistoricalInvalidCanary(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 905, Name: "downgrade-canary", Group: "default", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	initial, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	document, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, initial.CurrentRevision)
	require.NoError(t, err)
	document.Pools[0].DeploymentStage = model.RoutingDeploymentStageCanary
	canary, err := model.PublishRoutingPolicyRevisionDBContext(
		context.Background(),
		db,
		initial.CurrentRevision,
		document,
		model.RoutingPolicyActivationSpec{
			Stage:              model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: 100,
			ActorID:            7,
			Reason:             "historical_canary",
		},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		"UPDATE routing_policy_activations SET traffic_basis_points = 0 WHERE id = ?",
		canary.Activation.ID,
	).Error)

	healed, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, canary.Revision.Revision+1, healed.CurrentRevision)
	var activation model.RoutingPolicyActivation
	require.NoError(t, db.Where("id = ?", healed.CurrentActivationID).First(&activation).Error)
	assert.Equal(t, model.RoutingDeploymentStageShadow, activation.Stage)
	assert.Zero(t, activation.TrafficBasisPoints)
	assert.Equal(t, legacyRoutingPolicyCanarySafetyDowngradeReason, activation.Reason)
	healedDocument, healedRevision, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, healed.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, healedDocument.Pools, 1)
	assert.Equal(t, model.RoutingDeploymentStageShadow, healedDocument.Pools[0].DeploymentStage)
	assert.Equal(t, legacyRoutingPolicyCanarySafetyDowngradeReason, healedRevision.Reason)
}

func TestPolicySyncAndSnapshotUseProvidedDatabase(t *testing.T) {
	target := openSnapshotTestDB(t)
	withSnapshotTestDB(t, target)
	withSnapshotSecret(t)
	require.NoError(t, target.Create(&model.Channel{
		Id: 903, Name: "policy-database", Key: "key-a", Group: "VIP", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	other := openSnapshotTestDB(t)
	model.DB = other
	head, err := syncLegacyRoutingPolicyDBContext(context.Background(), target)
	require.NoError(t, err)
	assert.Equal(t, int64(1), head.CurrentRevision)

	var targetRevisionCount int64
	require.NoError(t, target.Model(&model.RoutingPolicyRevision{}).Count(&targetRevisionCount).Error)
	assert.Equal(t, int64(1), targetRevisionCount)
	var otherRevisionCount int64
	require.NoError(t, other.Model(&model.RoutingPolicyRevision{}).Count(&otherRevisionCount).Error)
	assert.Zero(t, otherRevisionCount)

	snapshot, err := buildSnapshotContext(context.Background(), target, DefaultSnapshotLimits)
	require.NoError(t, err)
	assert.Equal(t, uint64(head.CurrentRevision), snapshot.view.Revision)
	require.Len(t, snapshot.view.Pools, 1)
	assert.Equal(t, "VIP", snapshot.view.Pools[0].GroupName)
}

func TestEmptyInstallPublishesStableRevisionAndQueryableSnapshot(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	first, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.CurrentRevision)
	assert.Len(t, first.CurrentHash, 64)
	unchanged, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first.CurrentRevision, unchanged.CurrentRevision)

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(first.CurrentRevision), view.Revision)
	assert.Empty(t, view.Pools)
	assert.Empty(t, view.Channels)
	assert.Equal(t, snapshotTelemetryStatusComplete, view.Stats.MetricTelemetryStatus)

	pools, total, metadata, ok := ListPoolSnapshots("", 0, 20)
	require.True(t, ok)
	assert.Empty(t, pools)
	assert.Zero(t, total)
	assert.Equal(t, view.Revision, metadata.Revision)
	channels, total, _, ok := ListChannelSnapshots("", nil, nil, 0, 20)
	require.True(t, ok)
	assert.Empty(t, channels)
	assert.Zero(t, total)

	var revisionCount int64
	require.NoError(t, db.Model(&model.RoutingPolicyRevision{}).Count(&revisionCount).Error)
	assert.Equal(t, int64(1), revisionCount)
}

func TestLegacyPolicySyncSupportsGroupLargerThanAuditPayload(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)

	channels := make([]model.Channel, MaxDecisionCandidates+1)
	for index := range channels {
		channels[index] = model.Channel{
			Id: index + 1, Name: "large-group", Group: "default", Models: "gpt-test",
		}
	}
	require.NoError(t, db.CreateInBatches(&channels, 20).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	head, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	document, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, document.Pools, 1)
	assert.Empty(t, document.Pools[0].Members)
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Pools, 1)
	assert.Len(t, view.Pools[0].Members, MaxDecisionCandidates+1)
}
