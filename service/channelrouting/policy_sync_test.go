package channelrouting

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncLegacyRoutingPolicyPublishesOnlyChangedTopology(t *testing.T) {
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
	_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	changed, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), changed.CurrentRevision)
	document, revision, err := model.LoadRoutingPolicyRevisionContext(context.Background(), changed.CurrentRevision)
	require.NoError(t, err)
	assert.Equal(t, changed.CurrentHash, revision.ContentHash)
	require.Len(t, document.Pools, 1)
	require.Len(t, document.Pools[0].Members, 1)
	assert.Equal(t, int64(weight), document.Pools[0].Members[0].Weight)
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
	require.Len(t, initialDocument.Pools[0].Members, 1)
	initialCredentialIDs := append([]int(nil), initialDocument.Pools[0].Members[0].CredentialIDs...)

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
	manualDocument.Pools[0].Members[0].Enabled = false
	manualDocument.Pools[0].Members[0].Priority = 88
	manualDocument.Pools[0].Members[0].Weight = 77
	manualDocument.Pools[0].Members[0].Overrides = json.RawMessage(`{"region":"primary"}`)
	manualDocument.Pools[0].Members[0].ExtensionFields = map[string]json.RawMessage{
		"member_extension": json.RawMessage(`{"zone":"a"}`),
	}
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

	for _, topologyChange := range []struct {
		expectedRevision int64
		key              string
	}{
		{expectedRevision: 3, key: "key-b"},
		{expectedRevision: 4, key: "key-c"},
	} {
		expectedRevision := topologyChange.expectedRevision
		key := topologyChange.key
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 902).Update("key", key).Error)
		_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
		require.NoError(t, err)
		head, syncErr := SyncLegacyRoutingPolicyContext(context.Background())
		require.NoError(t, syncErr)
		assert.Equal(t, expectedRevision, head.CurrentRevision)

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
		require.Len(t, member.CredentialIDs, 1)
		assert.NotEqual(t, initialCredentialIDs, member.CredentialIDs)
		assert.Equal(t, legacyRoutingPolicyPreserveSyncReason, revision.Reason)

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
	assert.Len(t, document.Pools[0].Members, MaxDecisionCandidates+1)
}
