package channelrouting

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRefreshSnapshotPublishesImmutableObserveIdentity(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})

	priority := int64(11)
	weight := uint(23)
	baseURL := "https://user:secret@example.com/v1?token=hidden"
	channel := model.Channel{
		Id: 301, Name: "primary", Type: 1, Status: common.ChannelStatusEnabled,
		Key: "credential-a", Group: "vip", Models: "gpt-b,gpt-a,gpt-a",
		Priority: &priority, Weight: &weight, BaseURL: &baseURL,
		Balance: 12.5, BalanceUpdatedTime: 100,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 301, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://cost.example", UpstreamGroup: "vip", Enabled: true,
		SyncFailureCount: 2, SyncBackoffUntil: 999,
	}).Error)
	require.NoError(t, db.Model(&model.RoutingChannelConfiguration{}).
		Where("channel_id = ?", 301).
		Update("traffic_class", model.RoutingChannelTrafficClassClaudeCodeOnly).Error)

	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 301, ChannelGeneration: channel.RoutingGeneration,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-a", Group: "vip",
	}, routinghotcache.MetricSnapshot{
		RequestCount: 10, SuccessCount: 9, ReliabilityRequestCount: 9, ReliabilityFailureCount: 1,
		P95LatencyMs: 1200, P95TTFTMs: 300, OutputTokens: 900, GenerationMs: 3000, TPS: 300, UpdatedUnix: 123,
	})
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), view.Revision)
	require.Len(t, view.Pools, 1)
	assert.Equal(t, model.RoutingDeploymentStageObserve, view.Pools[0].DeploymentStage)
	assert.Equal(t, model.RoutingPolicyProfileBalanced, view.Pools[0].PolicyProfile)
	assert.InDelta(t, 0.45, view.Pools[0].SelectorPolicy.WeightAvailability, 0.000001)
	require.Len(t, view.Pools[0].Members, 1)
	member := view.Pools[0].Members[0]
	assert.Equal(t, int64(11), member.LegacyPriority)
	assert.Equal(t, int64(23), member.LegacyWeight)
	assert.InDelta(t, 1.0, member.NormalizedWeight, 1e-12)
	assert.False(t, member.AutomaticTrafficPaused)
	assert.True(t, member.TelemetryKnown)
	require.Len(t, member.Models, 2)
	assert.Equal(t, "gpt-a", member.Models[0].ModelName)
	assert.True(t, member.Models[0].MetricKnown)
	assert.Equal(t, float64(300), member.Models[0].OutputTokensPerSecond)
	assert.Equal(t, "legacy_compat", member.Models[0].MetricSource)
	assert.False(t, member.Models[0].CostKnown, "request cost is resolved from system pricing when a request profile is available")

	require.Len(t, view.Channels, 1)
	assert.Equal(t, "https://example.com", view.Channels[0].Endpoint)
	assert.NotContains(t, view.Channels[0].Endpoint, "secret")
	assert.True(t, view.Channels[0].BalanceKnown)
	assert.Equal(t, 12.5, view.Channels[0].Balance)
	assert.Equal(t, int64(100), view.Channels[0].BalanceUpdatedAt)
	assert.Equal(t, model.RoutingChannelTrafficClassClaudeCodeOnly, view.Channels[0].TrafficClass)
	assert.Equal(t, 1.0, view.Channels[0].UpstreamCostMultiplier)
	assert.Equal(t, float64(1), view.Stats.TelemetryCoverage)
	assert.Equal(t, float64(1), view.Stats.CredentialCoverage)

	identity, ok := ResolveIdentity("vip", 301, "credential-a")
	require.True(t, ok)
	assert.Equal(t, view.Revision, identity.SnapshotRevision)
	assert.Equal(t, channel.RoutingGeneration, identity.ChannelGeneration)
	assert.NotZero(t, identity.PoolID)
	assert.NotZero(t, identity.MemberID)
	assert.NotZero(t, identity.CredentialID)
	observation, observeIdentity, ok := ResolveObserveModelSnapshot("vip", 301, "gpt-a")
	require.True(t, ok)
	assert.Equal(t, "legacy_compat", observation.MetricSource)
	assert.Equal(t, identity.PoolID, observeIdentity.PoolID)
	assert.Equal(t, identity.MemberID, observeIdentity.MemberID)
	assert.Equal(t, view.Revision, observeIdentity.SnapshotRevision)

	view.Pools[0].Members[0].CredentialIDs[0] = 999999
	view.Pools[0].Members[0].Models[0].ModelName = "mutated"
	view.Channels[0].CredentialIDs[0] = 999999
	current, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.NotEqual(t, 999999, current.Pools[0].Members[0].CredentialIDs[0])
	assert.Equal(t, "gpt-a", current.Pools[0].Members[0].Models[0].ModelName)
	assert.NotEqual(t, 999999, current.Channels[0].CredentialIDs[0])

	refreshed, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, view.Revision, refreshed.Revision)
	assert.Equal(t, view.PolicyHash, refreshed.PolicyHash)
	assert.Greater(t, refreshed.RuntimeGeneration, view.RuntimeGeneration)
}

func TestSnapshotUsesChannelBalanceWithoutConnectorOverrides(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})

	channels := []model.Channel{
		{Id: 311, Name: "subscription", Key: "key-311", Group: "vip", Models: "gpt-test", Balance: 9, BalanceUpdatedTime: 100},
		{Id: 312, Name: "unknown-standard", Key: "key-312", Group: "vip", Models: "gpt-test", Balance: 8, BalanceUpdatedTime: 100},
		{Id: 313, Name: "connector-balance", Key: "key-313", Group: "vip", Models: "gpt-test", Balance: 7, BalanceUpdatedTime: 100},
		{Id: 314, Name: "disabled-connector", Key: "key-314", Group: "vip", Models: "gpt-test", Balance: 6, BalanceUpdatedTime: 100},
		{Id: 315, Name: "legacy", Key: "key-315", Group: "vip", Models: "gpt-test", Balance: 5, BalanceUpdatedTime: 100},
	}
	for index := range channels {
		require.NoError(t, db.Create(&channels[index]).Error)
	}
	bindings := []model.RoutingChannelBinding{
		{ChannelID: 311, UpstreamType: model.RoutingUpstreamTypeSub2API, BaseURL: "https://sub2api.example", UpstreamGroup: "subscription", Enabled: true},
		{ChannelID: 312, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: "https://newapi.example", UpstreamGroup: "standard", Enabled: true},
		{ChannelID: 313, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: "https://newapi.example", UpstreamGroup: "standard", Enabled: true},
		{ChannelID: 314, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: "https://newapi.example", UpstreamGroup: "standard", Enabled: false},
	}
	for index := range bindings {
		require.NoError(t, db.Create(&bindings[index]).Error)
	}
	routinghotcache.LoadHealthSnapshots([]model.RoutingChannelHealthState{{
		ChannelID: 313, UpdatedTime: 200,
	}}, 200)

	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)

	byID := make(map[int]ChannelSnapshot, len(view.Channels))
	for _, channel := range view.Channels {
		byID[channel.ID] = channel
	}
	for _, channel := range channels {
		assert.True(t, byID[channel.Id].BalanceKnown)
		assert.Equal(t, channel.Balance, byID[channel.Id].Balance)
		assert.Equal(t, channel.BalanceUpdatedTime, byID[channel.Id].BalanceUpdatedAt)
	}
	assert.Equal(t, 7.0, byID[313].Balance, "retired connector cache must not override Channel.Balance")
	assert.Equal(t, int64(100), byID[313].BalanceUpdatedAt)
}

func TestSnapshotPublishesVersionedPoolSelectorPolicyWithoutEnvironmentOverrides(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	smart_routing_setting.ResetForTest()
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeEnterpriseSLO)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, db.Create(&model.Channel{
		Id: 399, Name: "policy-source", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	head, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	document, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, document.Pools, 1)
	document.Pools[0].DeploymentStage = model.RoutingDeploymentStageShadow
	document.Pools[0].PolicyProfile = model.RoutingPolicyProfileCustom
	document.Pools[0].Policy = json.RawMessage(`{
		"weight_availability": 0.8,
		"weight_latency": 0.1,
		"weight_throughput": 0.05,
		"weight_cost": 0.05,
		"availability_floor": 0.99,
		"min_volume": 7,
		"top_k": 1,
		"snapshot_stale_sec": 90,
		"canary": {
			"capacity": {"rpm": 321, "inflight": 7, "future_limit": 1},
			"slow_start": {"minimum_factor": 0.2, "ramp_seconds": 600},
			"evaluation": {"window_seconds": 600, "evaluation_interval_seconds": 60},
			"future_mode": "compatible"
		}
	}`)
	_, err = model.PublishRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision, document, model.RoutingPolicyActivationSpec{
		Stage: model.RoutingDeploymentStageShadow, ActorID: 7, Reason: "shadow_test",
	})
	require.NoError(t, err)

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Pools, 1)
	pool := view.Pools[0]
	assert.Equal(t, model.RoutingDeploymentStageShadow, pool.DeploymentStage)
	assert.Equal(t, model.RoutingPolicyProfileCustom, pool.PolicyProfile)
	assert.InDelta(t, 0.8, pool.SelectorPolicy.WeightAvailability, 0.000001)
	assert.InDelta(t, 0.1, pool.SelectorPolicy.WeightLatency, 0.000001)
	assert.Equal(t, 7, pool.SelectorPolicy.MinVolume)
	assert.Equal(t, 1, pool.SelectorPolicy.TopK)
	assert.Equal(t, 90, pool.SelectorPolicy.SnapshotStaleSec)
	assert.Equal(t, int64(321), pool.CanaryPolicy.Capacity.RPM)
	assert.Equal(t, int64(7), pool.CanaryPolicy.Capacity.Inflight)
	assert.InDelta(t, 0.2, pool.CanaryPolicy.SlowStart.MinimumFactor, 1e-9)
	assert.Equal(t, 600, pool.CanaryPolicy.SlowStart.RampSeconds)
	assert.Equal(t, 600, pool.CanaryPolicy.Evaluation.WindowSeconds)
	assert.Equal(t, 60, pool.CanaryPolicy.Evaluation.EvaluationIntervalSeconds)
}

func TestSnapshotBindsCurrentActivationMetadata(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)

	published, err := model.PublishRoutingPolicyRevisionDBContext(
		context.Background(),
		db,
		0,
		snapshotPolicyDocumentForStages(model.RoutingDeploymentStageCanary),
		model.RoutingPolicyActivationSpec{
			Stage:              model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: 250,
			ActorID:            7,
			Reason:             "activation_snapshot",
		},
	)
	require.NoError(t, err)

	snapshot, err := buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
	require.NoError(t, err)
	view := snapshot.view
	assert.Equal(t, published.Activation.ID, view.ActivationID)
	assert.Equal(t, model.RoutingDeploymentStageCanary, view.ActivationStage)
	assert.Equal(t, 250, view.TrafficBasisPoints)
	require.Len(t, view.Pools, 1)
	assert.Equal(t, model.RoutingDeploymentStageCanary, view.Pools[0].DeploymentStage)

	metadata := snapshotMetadata(view)
	assert.Equal(t, view.ActivationID, metadata.ActivationID)
	assert.Equal(t, view.ActivationStage, metadata.ActivationStage)
	assert.Equal(t, view.TrafficBasisPoints, metadata.TrafficBasisPoints)
}

func TestSnapshotFailsClosedForCorruptCurrentActivation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, db *gorm.DB, published model.RoutingPolicyPublishResult)
	}{
		{
			name: "head has no activation",
			mutate: func(t *testing.T, db *gorm.DB, _ model.RoutingPolicyPublishResult) {
				require.NoError(t, db.Exec("UPDATE routing_policy_heads SET current_activation_id = 0 WHERE id = 1").Error)
			},
		},
		{
			name: "activation belongs to another revision",
			mutate: func(t *testing.T, db *gorm.DB, published model.RoutingPolicyPublishResult) {
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_activations SET revision = ? WHERE id = ?",
					published.Revision.Revision+1,
					published.Activation.ID,
				).Error)
			},
		},
		{
			name: "activation stage differs from head",
			mutate: func(t *testing.T, db *gorm.DB, published model.RoutingPolicyPublishResult) {
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_activations SET stage = ? WHERE id = ?",
					model.RoutingDeploymentStageShadow,
					published.Activation.ID,
				).Error)
			},
		},
		{
			name: "activation traffic violates canary bounds",
			mutate: func(t *testing.T, db *gorm.DB, published model.RoutingPolicyPublishResult) {
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_activations SET traffic_basis_points = 501 WHERE id = ?",
					published.Activation.ID,
				).Error)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openSnapshotTestDB(t)
			withSnapshotTestDB(t, db)
			withSnapshotSecret(t)
			published, err := model.PublishRoutingPolicyRevisionDBContext(
				context.Background(),
				db,
				0,
				snapshotPolicyDocumentForStages(model.RoutingDeploymentStageCanary),
				model.RoutingPolicyActivationSpec{
					Stage:              model.RoutingDeploymentStageCanary,
					TrafficBasisPoints: 250,
					ActorID:            7,
				},
			)
			require.NoError(t, err)
			test.mutate(t, db, published)

			_, err = buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
			assert.ErrorIs(t, err, ErrSnapshotActivation)
		})
	}
}

func TestSnapshotFailsClosedForPoolActivationStageConflict(t *testing.T) {
	tests := []struct {
		name          string
		activation    string
		basis         int
		pools         []string
		publishedPool string
		valid         bool
	}{
		{name: "observe pool under observe activation", activation: model.RoutingDeploymentStageObserve, pools: []string{model.RoutingDeploymentStageObserve}, valid: true},
		{name: "observe and shadow pools under shadow activation", activation: model.RoutingDeploymentStageShadow, pools: []string{model.RoutingDeploymentStageObserve, model.RoutingDeploymentStageShadow}, valid: true},
		{name: "shadow and canary pools under canary activation", activation: model.RoutingDeploymentStageCanary, basis: 250, pools: []string{model.RoutingDeploymentStageShadow, model.RoutingDeploymentStageCanary}, valid: true},
		{name: "shadow activation cannot contain canary pool", activation: model.RoutingDeploymentStageShadow, pools: []string{model.RoutingDeploymentStageCanary}, publishedPool: model.RoutingDeploymentStageShadow},
		{name: "canary activation cannot contain active pool", activation: model.RoutingDeploymentStageCanary, basis: 250, pools: []string{model.RoutingDeploymentStageActive}, publishedPool: model.RoutingDeploymentStageCanary},
		{name: "active activation cannot retain canary pool without canary traffic", activation: model.RoutingDeploymentStageActive, pools: []string{model.RoutingDeploymentStageCanary}, publishedPool: model.RoutingDeploymentStageActive},
		{name: "active activation can contain lower non-canary stages", activation: model.RoutingDeploymentStageActive, pools: []string{model.RoutingDeploymentStageObserve, model.RoutingDeploymentStageShadow, model.RoutingDeploymentStageActive}, valid: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openSnapshotTestDB(t)
			withSnapshotTestDB(t, db)
			withSnapshotSecret(t)
			document := snapshotPolicyDocumentForStages(test.pools...)
			if !test.valid {
				document.Pools[0].DeploymentStage = test.publishedPool
			}
			published, err := model.PublishRoutingPolicyRevisionDBContext(
				context.Background(),
				db,
				0,
				document,
				model.RoutingPolicyActivationSpec{Stage: test.activation, TrafficBasisPoints: test.basis, ActorID: 7},
			)
			require.NoError(t, err)
			if !test.valid {
				document.Pools[0].DeploymentStage = test.pools[0]
				_, contentHash, normalizeErr := model.NormalizeRoutingPolicyDocument(document)
				require.NoError(t, normalizeErr)
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_pool_revisions SET deployment_stage = ? WHERE revision = ? AND pool_id = ?",
					test.pools[0],
					published.Revision.Revision,
					document.Pools[0].PoolID,
				).Error)
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_revisions SET content_hash = ? WHERE revision = ?",
					contentHash,
					published.Revision.Revision,
				).Error)
				require.NoError(t, db.Exec(
					"UPDATE routing_policy_heads SET current_hash = ? WHERE id = 1",
					contentHash,
				).Error)
			}

			_, err = buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
			if test.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrSnapshotActivation)
		})
	}
}

func TestSnapshotPrecomputesDeterministicPoolModelMemberIndexes(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 410, Name: "shared-a", Group: "alpha,beta", Models: "common,alpha-only"},
		{Id: 411, Name: "alpha-b", Group: "alpha", Models: "common,alpha-only"},
		{Id: 412, Name: "beta-b", Group: "beta", Models: "common,beta-only"},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)

	snapshot, err := buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
	require.NoError(t, err)
	for _, pool := range snapshot.view.Pools {
		indexes := snapshot.memberIndexesByPoolModel[poolModelKey{poolID: pool.ID, model: "common"}]
		require.Len(t, indexes, 2)
		assert.Less(t, indexes[0], indexes[1])
		for _, memberIndex := range indexes {
			require.Less(t, memberIndex, len(pool.Members))
			member := pool.Members[memberIndex]
			assert.Equal(t, pool.ID, member.PoolID)
			assert.Contains(t, []int{410, 411, 412}, member.ChannelID)
		}
	}

	indexedMembers := 0
	for _, indexes := range snapshot.memberIndexesByPoolModel {
		indexedMembers += len(indexes)
	}
	assert.Equal(t, snapshot.view.Stats.ModelSnapshotCount, indexedMembers)
	assert.LessOrEqual(t, indexedMembers, DefaultSnapshotLimits.MaxTotalModelSnapshots)
}

func TestSnapshotStableRollupMergesCredentialsAndLiveDeltasWithoutLegacyFallback(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.SnapshotStaleSec = 300
	setting.MetricBucketSec = 60
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	require.NoError(t, db.Create(&model.Channel{
		Id: 305, Name: "multi", Key: "key-a\nkey-b", Group: "vip", Models: "gpt-test",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	first, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, first.Pools, 1)
	require.Len(t, first.Pools[0].Members, 1)
	member := first.Pools[0].Members[0]
	require.Len(t, member.CredentialIDs, 2)
	now := time.Now().Unix()
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{{
		MemberID: member.ID, CredentialID: member.CredentialIDs[0], ModelName: "gpt-test", BucketTs: now - 60,
		ChannelID: 305, PoolID: member.PoolID, LastSnapshotRevision: int64(first.Revision),
		RequestCount: 3, SuccessCount: 2, FailureCount: 1, ReliabilityRequestCount: 3,
		ReliabilityFailureCount: 1, TotalLatencyMs: 900, TtftSumMs: 200, TtftCount: 2,
		OutputTokens: 30, GenerationMs: 300, Err5xx: 1,
		SketchCodecVersion: routingdistribution.SketchCodecVersion,
		LatencySampleCount: 3, LatencySketch: snapshotTestSketch(t, 100, 200, 600),
		TtftSampleCount: 2, TtftSketch: snapshotTestSketch(t, 80, 120),
	}}))
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: member.PoolID, PoolMemberID: member.ID, CredentialID: member.CredentialIDs[1], ChannelID: 305,
		ChannelGeneration: member.ChannelGeneration,
		Model:             "gpt-test", BucketTs: now, LastSnapshotRevision: first.Revision,
		RequestCount: 2, FailureCount: 2, UnknownClassificationCount: 1,
		TotalLatencyMs: 1100, TtftSumMs: 400, TtftCount: 2,
		OutputTokens: 20, GenerationMs: 200, Err429: 1, Err529: 1,
		RetryAfterCount: 2, RetryAfterTotalMs: 3000,
		SketchCodecVersion: routingdistribution.SketchCodecVersion,
		LatencySampleCount: 2, LatencySketch: snapshotTestSketch(t, 400, 700),
		TtftSampleCount: 2, TtftSketch: snapshotTestSketch(t, 150, 250),
	}})
	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 305, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip",
	}, routinghotcache.MetricSnapshot{RequestCount: 999, SuccessCount: 999, P95LatencyMs: 999, TPS: 999})

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	observation := view.Pools[0].Members[0].Models[0]
	assert.True(t, observation.MetricKnown)
	assert.Equal(t, "stable_rollup", observation.MetricSource)
	assert.Equal(t, int64(5), observation.RequestCount)
	assert.Equal(t, int64(2), observation.SuccessCount)
	assert.Equal(t, int64(3), observation.FailureCount)
	assert.Equal(t, int64(1), observation.UnknownClassificationCount)
	assert.Equal(t, float64(400), observation.AverageLatencyMs)
	assert.Equal(t, float64(150), observation.AverageTTFTMs)
	assert.Equal(t, float64(100), observation.OutputTokensPerSecond)
	assert.Equal(t, float64(1500), observation.AverageRetryAfterMs)
	assert.True(t, observation.LatencyDistributionKnown)
	assert.True(t, observation.TTFTDistributionKnown)
	assert.True(t, observation.P95LatencyKnown)
	assert.True(t, observation.P95TTFTKnown)
	assert.Equal(t, float64(1), observation.LatencyDistributionCoverage)
	assert.Equal(t, float64(1), observation.TTFTDistributionCoverage)
	assert.InDelta(t, snapshotTestQuantile(t, 0.50, 100, 200, 600, 400, 700), observation.P50LatencyMs, 0.000001)
	assert.InDelta(t, snapshotTestQuantile(t, 0.95, 100, 200, 600, 400, 700), observation.P95LatencyMs, 0.000001)
	assert.InDelta(t, snapshotTestQuantile(t, 0.99, 100, 200, 600, 400, 700), observation.P99LatencyMs, 0.000001)
	assert.InDelta(t, snapshotTestQuantile(t, 0.50, 80, 120, 150, 250), observation.P50TTFTMs, 0.000001)
	assert.InDelta(t, snapshotTestQuantile(t, 0.95, 80, 120, 150, 250), observation.P95TTFTMs, 0.000001)
	assert.InDelta(t, snapshotTestQuantile(t, 0.99, 80, 120, 150, 250), observation.P99TTFTMs, 0.000001)
	assert.Equal(t, int64(1), observation.Err5xx)
	assert.Equal(t, int64(1), observation.Err429)
	assert.Equal(t, int64(1), observation.Err529)
	require.NotNil(t, view.Stats.UnknownClassificationRate)
	assert.InDelta(t, float64(1)/3, *view.Stats.UnknownClassificationRate, 0.000001)
}

func TestSnapshotStableTelemetryIsolatedByPoolMemberAndCurrentBucketWindow(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	setting.SnapshotStaleSec = 30
	setting.MetricBucketSec = 600
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	require.NoError(t, db.Create(&model.Channel{
		Id: 306, Name: "shared", Key: "key-a\nkey-b", Group: "VIP,vip", Models: "gpt-test",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	first, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, first.Pools, 2)
	memberByGroup := map[string]PoolMemberSnapshot{}
	for _, pool := range first.Pools {
		memberByGroup[pool.GroupName] = pool.Members[0]
	}
	upper := memberByGroup["VIP"]
	require.NotZero(t, upper.ID)
	currentBucket := time.Now().Unix()
	currentBucket -= currentBucket % int64(setting.MetricBucketSec)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: upper.PoolID, PoolMemberID: upper.ID, CredentialID: upper.CredentialIDs[0], ChannelID: 306,
		ChannelGeneration: upper.ChannelGeneration,
		Model:             "gpt-test", BucketTs: currentBucket, LastSnapshotRevision: first.Revision,
		RequestCount: 1, SuccessCount: 1,
	}})

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	for _, pool := range view.Pools {
		observation := pool.Members[0].Models[0]
		if pool.GroupName == "VIP" {
			assert.True(t, observation.MetricKnown)
			assert.Equal(t, "stable_rollup", observation.MetricSource)
		} else {
			assert.False(t, observation.MetricKnown)
			assert.Empty(t, observation.MetricSource)
		}
	}
	assert.Nil(t, view.Stats.UnknownClassificationRate)
}

func TestSnapshotExcludesCredentialZeroAfterKeylessChannelBecomesKeyed(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	require.NoError(t, db.Create(&model.Channel{
		Id: 308, Name: "keyless-to-keyed", Group: "default", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	first, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	member := first.Pools[0].Members[0]
	now := time.Now().Unix()
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{{
		MemberID: member.ID, CredentialID: 0, ModelName: "gpt-test", BucketTs: now,
		ChannelID: 308, PoolID: member.PoolID, LastSnapshotRevision: int64(first.Revision),
		RequestCount: 4, SuccessCount: 4,
	}}))
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: member.PoolID, PoolMemberID: member.ID, CredentialID: 0, ChannelID: 308,
		ChannelGeneration: member.ChannelGeneration,
		Model:             "gpt-test", BucketTs: now, LastSnapshotRevision: first.Revision,
		RequestCount: 1, SuccessCount: 1,
	}})

	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 308).Update("key", "new-key").Error)
	_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	second, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	identity, ok := ResolveIdentity("default", 308, "new-key")
	require.True(t, ok)
	assert.Positive(t, identity.CredentialID)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: identity.PoolID, PoolMemberID: identity.MemberID, CredentialID: identity.CredentialID, ChannelID: 308,
		ChannelGeneration: identity.ChannelGeneration,
		Model:             "gpt-test", BucketTs: now, LastSnapshotRevision: second.Revision,
		RequestCount: 2, SuccessCount: 2,
	}})

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	observation := view.Pools[0].Members[0].Models[0]
	assert.Equal(t, "stable_rollup", observation.MetricSource)
	assert.Equal(t, int64(2), observation.RequestCount)
	assert.Equal(t, int64(2), observation.SuccessCount)
}

func TestSnapshotExternalDatabaseCompatibility(t *testing.T) {
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
			db := openSnapshotExternalTestDB(t, test.dbType, dsn)
			runSnapshotExternalContract(t, db, test.dbType)
		})
	}
}

func runSnapshotExternalContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withSnapshotTestDBType(t, db, dbType)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.RoutingTopologyMetadata{},
		&model.RoutingPool{},
		&model.RoutingPoolMember{},
		&model.RoutingCredentialRef{},
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
		&model.RoutingDecisionAudit{},
		&model.RoutingDecisionReplayChunk{},
		&model.RoutingChannelBinding{},
		&model.RoutingChannelConfiguration{},
		&model.RoutingConfigurationEpoch{},
		&model.RoutingMetricRollup{},
	))
	require.NoError(t, model.EnsureRoutingConfigurationEpoch(db))
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 501, Name: "shared", Key: "key-a\nkey-b", Group: "VIP,vip", Models: "Model-X,model-x", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 502, Name: "keyless", Group: "local", Models: "keyless-model"},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	var pools []model.RoutingPool
	require.NoError(t, db.Order("id asc").Find(&pools).Error)
	poolByGroup := make(map[string]model.RoutingPool, len(pools))
	for _, pool := range pools {
		poolByGroup[pool.GroupName] = pool
	}
	var members []model.RoutingPoolMember
	require.NoError(t, db.Order("id asc").Find(&members).Error)
	memberByPoolChannel := make(map[poolChannelKey]model.RoutingPoolMember, len(members))
	for _, member := range members {
		memberByPoolChannel[poolChannelKey{PoolID: member.PoolID, ChannelID: member.ChannelID}] = member
	}
	upperMember := memberByPoolChannel[poolChannelKey{PoolID: poolByGroup["VIP"].ID, ChannelID: 501}]
	keylessMember := memberByPoolChannel[poolChannelKey{PoolID: poolByGroup["local"].ID, ChannelID: 502}]
	require.Positive(t, upperMember.ID)
	require.Positive(t, keylessMember.ID)
	var credentials []model.RoutingCredentialRef
	require.NoError(t, db.Where("channel_id = ?", 501).Order("id asc").Find(&credentials).Error)
	require.Len(t, credentials, 2)
	var channel501 model.Channel
	require.NoError(t, db.Select("id", "routing_generation").Where("id = ?", 501).First(&channel501).Error)
	fingerprintA, err := model.RoutingCredentialFingerprint(501, channel501.RoutingGeneration, "key-a")
	require.NoError(t, err)
	fingerprintB, err := model.RoutingCredentialFingerprint(501, channel501.RoutingGeneration, "key-b")
	require.NoError(t, err)
	credentialByFingerprint := make(map[string]model.RoutingCredentialRef, len(credentials))
	for _, credential := range credentials {
		credentialByFingerprint[credential.Fingerprint] = credential
	}
	credentialA := credentialByFingerprint[fingerprintA]
	credentialB := credentialByFingerprint[fingerprintB]
	require.Positive(t, credentialA.ID)
	require.Positive(t, credentialB.ID)
	now := time.Now().Unix()
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{
		{MemberID: upperMember.ID, CredentialID: credentialA.ID, ModelName: "Model-X", BucketTs: now, ChannelID: 501, PoolID: upperMember.PoolID, LastSnapshotRevision: 1, RequestCount: 1, SuccessCount: 1},
		{MemberID: upperMember.ID, CredentialID: credentialB.ID, ModelName: "Model-X", BucketTs: now, ChannelID: 501, PoolID: upperMember.PoolID, LastSnapshotRevision: 1, RequestCount: 2, SuccessCount: 2},
		{MemberID: upperMember.ID, CredentialID: credentialA.ID, ModelName: "model-x", BucketTs: now, ChannelID: 501, PoolID: upperMember.PoolID, LastSnapshotRevision: 1, RequestCount: 4, SuccessCount: 4},
		{MemberID: keylessMember.ID, CredentialID: 0, ModelName: "keyless-model", BucketTs: now, ChannelID: 502, PoolID: keylessMember.PoolID, LastSnapshotRevision: 1, RequestCount: 5, SuccessCount: 5},
	}))
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 501).Update("key", "key-a").Error)
	_, err = model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: upperMember.PoolID, PoolMemberID: upperMember.ID, CredentialID: credentialA.ID, ChannelID: 501,
		ChannelGeneration: upperMember.ChannelGeneration,
		Model:             "Model-X", BucketTs: now, LastSnapshotRevision: 1, RequestCount: 3, SuccessCount: 3,
	}})

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	viewsByGroup := make(map[string]PoolSnapshot, len(view.Pools))
	for _, pool := range view.Pools {
		viewsByGroup[pool.GroupName] = pool
	}
	upperModels := viewsByGroup["VIP"].Members[0].Models
	require.Len(t, upperModels, 2)
	assert.Equal(t, "Model-X", upperModels[0].ModelName)
	assert.Equal(t, int64(4), upperModels[0].RequestCount)
	assert.Equal(t, "model-x", upperModels[1].ModelName)
	assert.Equal(t, int64(4), upperModels[1].RequestCount)
	assert.False(t, viewsByGroup["vip"].Members[0].TelemetryKnown)
	assert.Equal(t, int64(5), viewsByGroup["local"].Members[0].Models[0].RequestCount)
}

func TestSnapshotLimitFailurePreservesLastKnownGood(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	require.NoError(t, db.Create(&model.Channel{
		Id: 302, Name: "primary", Key: "credential", Group: "default", Models: "gpt-a,gpt-b",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	first, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)

	_, err = buildSnapshotContext(context.Background(), db, SnapshotLimits{
		MaxPools: 1, MaxMembers: 1, MaxCredentials: 1, MaxChannels: 1,
		MaxModelsPerChannel: 1, MaxTotalModelSnapshots: 1, MaxModelBytesPerChannel: 1024,
		MaxTotalChannelBytes: 4096, MaxTotalBindingBytes: 4096, MaxMetricAggregates: 1,
	})
	require.ErrorIs(t, err, ErrSnapshotLimitExceeded)

	current, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, first.Revision, current.Revision)
	assert.Equal(t, "gpt-a", current.Pools[0].Members[0].Models[0].ModelName)
}

func TestSnapshotMetricLimitAppliesAfterDatabaseAggregation(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 307, Name: "aggregate", Key: "key-a\nkey-b", Group: "default", Models: "gpt-test",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var member model.RoutingPoolMember
	require.NoError(t, db.First(&member).Error)
	var credentials []model.RoutingCredentialRef
	require.NoError(t, db.Order("id asc").Find(&credentials).Error)
	require.Len(t, credentials, 2)
	now := time.Now().Unix()
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{
		{MemberID: member.ID, CredentialID: credentials[0].ID, ModelName: "gpt-test", BucketTs: now - 60, ChannelID: 307, PoolID: member.PoolID, LastSnapshotRevision: 1, RequestCount: 1},
		{MemberID: member.ID, CredentialID: credentials[1].ID, ModelName: "gpt-test", BucketTs: now, ChannelID: 307, PoolID: member.PoolID, LastSnapshotRevision: 1, RequestCount: 2},
	}))
	_, err = SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)

	snapshot, err := buildSnapshotContext(context.Background(), db, SnapshotLimits{
		MaxPools: 1, MaxMembers: 1, MaxCredentials: 2, MaxChannels: 1,
		MaxModelsPerChannel: 1, MaxTotalModelSnapshots: 1, MaxModelBytesPerChannel: 1024,
		MaxTotalChannelBytes: 4096, MaxTotalBindingBytes: 4096, MaxMetricAggregates: 1,
	})
	require.NoError(t, err)
	observation := snapshot.view.Pools[0].Members[0].Models[0]
	assert.Equal(t, int64(3), observation.RequestCount)
	assert.False(t, observation.LatencyDistributionKnown)
	assert.False(t, observation.P95LatencyKnown)
	assert.Zero(t, observation.LatencyDistributionCoverage)
	assert.Zero(t, observation.P95LatencyMs)
}

func TestSnapshotMetricPaginationStopsBeforePayloadLoadAtLimits(t *testing.T) {
	tests := []struct {
		name      string
		rollups   func(t *testing.T, member model.RoutingPoolMember) []model.RoutingMetricRollup
		configure func(t *testing.T, limits *SnapshotLimits)
		reason    string
	}{
		{
			name: "row limit",
			rollups: func(t *testing.T, member model.RoutingPoolMember) []model.RoutingMetricRollup {
				now := time.Now().Unix()
				return []model.RoutingMetricRollup{
					snapshotMetricRollup(member, model.RoutingMetricRollupModelKey("gpt-test"), "gpt-test", now-1, nil),
					snapshotMetricRollup(member, model.RoutingMetricRollupModelKey("gpt-test"), "gpt-test", now, nil),
				}
			},
			configure: func(t *testing.T, limits *SnapshotLimits) {
				limits.MaxMetricRollupRows = 1
			},
			reason: snapshotTelemetryReasonRollupRows,
		},
		{
			name: "scan row limit",
			rollups: func(t *testing.T, member model.RoutingPoolMember) []model.RoutingMetricRollup {
				now := time.Now().Unix()
				return []model.RoutingMetricRollup{
					snapshotMetricRollup(member, model.RoutingMetricRollupModelKey("gpt-test"), "gpt-test", now-1, nil),
					snapshotMetricRollup(member, model.RoutingMetricRollupModelKey("gpt-test"), "gpt-test", now, nil),
				}
			},
			configure: func(t *testing.T, limits *SnapshotLimits) {
				limits.MaxMetricRollupScanRows = 1
			},
			reason: snapshotTelemetryReasonScanRows,
		},
		{
			name: "total sketch bytes",
			rollups: func(t *testing.T, member model.RoutingPoolMember) []model.RoutingMetricRollup {
				return []model.RoutingMetricRollup{
					snapshotMetricRollup(member, model.RoutingMetricRollupModelKey("gpt-test"), "gpt-test", time.Now().Unix(), snapshotTestSketch(t, 25)),
				}
			},
			configure: func(t *testing.T, limits *SnapshotLimits) {
				encoded := snapshotTestSketch(t, 25)
				require.Greater(t, len(encoded), 1)
				limits.MaxMetricSketchBytes = len(encoded) - 1
			},
			reason: snapshotTelemetryReasonSketchBytes,
		},
		{
			name: "codec blob bytes",
			rollups: func(t *testing.T, member model.RoutingPoolMember) []model.RoutingMetricRollup {
				return []model.RoutingMetricRollup{
					snapshotMetricRollup(
						member,
						model.RoutingMetricRollupModelKey("gpt-test"),
						"gpt-test",
						time.Now().Unix(),
						make([]byte, routingdistribution.MaxEncodedBytes+1),
					),
				}
			},
			configure: func(t *testing.T, limits *SnapshotLimits) {},
			reason:    snapshotTelemetryReasonSketchBlob,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, member := setupSnapshotMetricPaginationTest(t, 610)
			rollups := test.rollups(t, member)
			require.NoError(t, db.Create(&rollups).Error)
			queries := observeSnapshotMetricQueries(t, db)
			limits := DefaultSnapshotLimits
			test.configure(t, &limits)

			snapshot, err := buildSnapshotContext(context.Background(), db, limits)
			require.NoError(t, err)
			assert.Equal(t, snapshotTelemetryStatusUnavailable, snapshot.view.Stats.MetricTelemetryStatus)
			assert.Equal(t, test.reason, snapshot.view.Stats.MetricTelemetryReason)
			require.Len(t, snapshot.view.Pools, 1)
			require.Len(t, snapshot.view.Pools[0].Members, 1)
			require.Len(t, snapshot.view.Pools[0].Members[0].Models, 1)
			observation := snapshot.view.Pools[0].Members[0].Models[0]
			assert.False(t, observation.MetricKnown)
			assert.False(t, observation.P95TTFTKnown)
			assert.False(t, snapshot.view.AggregateP95TTFTKnown)
			assert.Equal(t, 1, queries.total)
			assert.Zero(t, queries.payload)
		})
	}
}

func TestSnapshotMetricPaginationMergesAcrossStableCompositeOrder(t *testing.T) {
	db, member := setupSnapshotMetricPaginationTest(t, 611)
	now := time.Now().Unix()
	rollups := make([]model.RoutingMetricRollup, 0, snapshotMetricRollupPageSize+1)
	for index := 0; index <= snapshotMetricRollupPageSize; index++ {
		var sketch []byte
		switch index {
		case 0:
			sketch = snapshotTestSketch(t, 1_000)
		case snapshotMetricRollupPageSize:
			sketch = snapshotTestSketch(t, 10)
		}
		rollups = append(rollups, snapshotMetricRollup(
			member,
			model.RoutingMetricRollupModelKey("gpt-test"),
			"gpt-test",
			now-int64(snapshotMetricRollupPageSize-index),
			sketch,
		))
	}
	require.NoError(t, db.CreateInBatches(&rollups, 20).Error)

	snapshot, err := buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
	require.NoError(t, err)
	require.Len(t, snapshot.view.Pools, 1)
	require.Len(t, snapshot.view.Pools[0].Members, 1)
	require.Len(t, snapshot.view.Pools[0].Members[0].Models, 1)
	observation := snapshot.view.Pools[0].Members[0].Models[0]
	assert.Equal(t, int64(2), observation.RequestCount)
	assert.Equal(t, int64(2), observation.SuccessCount)
	assert.True(t, observation.LatencyDistributionKnown)
	assert.True(t, observation.P95LatencyKnown)
	assert.Equal(t, float64(1), observation.LatencyDistributionCoverage)
	assert.InDelta(t, snapshotTestQuantile(t, 0.95, 10, 1_000), observation.P95LatencyMs, 0.000001)
}

func TestSnapshotMetricBudgetUsesOnlyCurrentPolicyModelsAndCredentials(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	restoreSystemRoutingRatioSettings(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"current":1.25}`))
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
	})

	require.NoError(t, db.Create(&model.Channel{
		Id: 612, Name: "budget", Key: "key-a\nkey-b", Group: "default", Models: "current",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	head, err := SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	document, _, err := model.LoadRoutingPolicyRevisionDBContext(context.Background(), db, head.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, document.Pools, 1)
	require.Empty(t, document.Pools[0].Members)
	var topologyMember model.RoutingPoolMember
	require.NoError(t, db.Where("channel_id = ? AND active = ?", 612, true).First(&topologyMember).Error)
	var credentials []model.RoutingCredentialRef
	require.NoError(t, db.Where(
		"channel_id = ? AND channel_generation = ? AND active = ?",
		612, topologyMember.ChannelGeneration, true,
	).Order("id asc").Find(&credentials).Error)
	require.Len(t, credentials, 2)
	selectedCredential := credentials[0].ID
	unselectedCredential := credentials[1].ID
	member := model.RoutingPolicyMemberContent{
		MemberID: topologyMember.ID, ChannelID: topologyMember.ChannelID,
		RoutingGeneration: topologyMember.ChannelGeneration,
		CredentialIDs:     []int{selectedCredential}, Overrides: json.RawMessage(`{}`),
	}
	document.Pools[0].Members = []model.RoutingPolicyMemberContent{member}
	published, err := model.PublishRoutingPolicyRevisionDBContext(
		context.Background(), db, head.CurrentRevision, document,
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 7, Reason: "select_one_credential"},
	)
	require.NoError(t, err)

	now := time.Now().Unix()
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{
		snapshotTTFTRollup(t, member.MemberID, document.Pools[0].PoolID, 612, selectedCredential, "current", now, 10, published.Revision.Revision),
		snapshotTTFTRollup(t, member.MemberID, document.Pools[0].PoolID, 612, unselectedCredential, "current", now, 9_000, published.Revision.Revision),
		snapshotTTFTRollup(t, member.MemberID, document.Pools[0].PoolID, 612, unselectedCredential, "current", now-1, 8_000, published.Revision.Revision),
		snapshotTTFTRollup(t, member.MemberID, document.Pools[0].PoolID, 612, selectedCredential, "removed", now, 10_000, published.Revision.Revision),
		snapshotTTFTRollup(t, 99_999, 99_999, 99_999, 0, "current", now, 10_000, published.Revision.Revision),
	}))

	limits := DefaultSnapshotLimits
	limits.MaxMetricRollupRows = 1
	first, err := buildSnapshotContext(context.Background(), db, limits)
	require.NoError(t, err)
	firstView, err := publishRuntimeSnapshot(first)
	require.NoError(t, err)
	assert.Equal(t, uint64(published.Revision.Revision), firstView.Revision)
	assert.Equal(t, snapshotTelemetryStatusComplete, firstView.Stats.MetricTelemetryStatus)
	assert.Equal(t, 1, firstView.Stats.MetricRollupRows)
	assert.GreaterOrEqual(t, firstView.Stats.MetricRollupScannedRows, 3)
	require.Len(t, firstView.Pools, 1)
	require.Len(t, firstView.Pools[0].Members, 1)
	require.Len(t, firstView.Pools[0].Members[0].Models, 1)
	observation := firstView.Pools[0].Members[0].Models[0]
	require.True(t, observation.MetricKnown)
	assert.Equal(t, int64(1), observation.RequestCount)
	assert.InDelta(t, 10, observation.P95TTFTMs, 1)
	assert.InDelta(t, 10, firstView.AggregateP95TTFTMs, 1)

	weight := int64(101)
	document.Pools[0].Members[0].Weight = weight
	document.Pools[0].Members[0].WeightOverride = &weight
	next, err := model.PublishRoutingPolicyRevisionDBContext(
		context.Background(), db, published.Revision.Revision, document,
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 8, Reason: "overflow_revision"},
	)
	require.NoError(t, err)
	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{
		snapshotTTFTRollup(t, member.MemberID, document.Pools[0].PoolID, 612, selectedCredential, "current", now-1, 20, next.Revision.Revision),
	}))
	second, err := buildSnapshotContext(context.Background(), db, limits)
	require.NoError(t, err)
	secondView, err := publishRuntimeSnapshot(second)
	require.NoError(t, err)
	assert.Equal(t, uint64(next.Revision.Revision), secondView.Revision)
	assert.Equal(t, snapshotTelemetryStatusUnavailable, secondView.Stats.MetricTelemetryStatus)
	assert.Equal(t, snapshotTelemetryReasonRollupRows, secondView.Stats.MetricTelemetryReason)
	assert.Equal(t, 2, secondView.Stats.MetricRollupRows)
	observation = secondView.Pools[0].Members[0].Models[0]
	assert.False(t, observation.MetricKnown)
	assert.False(t, observation.P95TTFTKnown)
	assert.False(t, secondView.AggregateP95TTFTKnown)
	assert.False(t, observation.CostKnown, "request cost is resolved only after a request profile is available")
	require.NotNil(t, observation.CostPricing, "system pricing remains independent from telemetry availability")
	require.NotNil(t, observation.CostPricing.PerRequestCost)
	assert.Equal(t, 1.25, *observation.CostPricing.PerRequestCost)
	assert.Empty(t, observation.CostUnknownReason)
	current, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, secondView.Revision, current.Revision)
}

func TestResolveIdentityKeepsPoolMemberForKeylessChannel(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	require.NoError(t, db.Create(&model.Channel{
		Id: 303, Name: "keyless", Group: "local", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)

	identity, ok := ResolveIdentity("local", 303, "")
	require.True(t, ok)
	assert.Equal(t, view.Revision, identity.SnapshotRevision)
	assert.Positive(t, identity.PoolID)
	assert.Positive(t, identity.MemberID)
	assert.Zero(t, identity.CredentialID)
}

func TestSnapshotSanitizesNonFiniteTelemetryAndBalanceValues(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	ResetSnapshotForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		ResetSnapshotForTest()
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&model.Channel{
		Id: 304, Name: "non-finite", Key: "key", Group: "default", Models: "gpt-test",
		Balance: math.Inf(1), BalanceUpdatedTime: common.GetTimestamp(),
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var currentChannel model.Channel
	require.NoError(t, db.Select("id", "routing_generation").Where("id = ?", 304).First(&currentChannel).Error)
	key := routinghotcache.Key{
		ChannelID: 304, ChannelGeneration: currentChannel.RoutingGeneration,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}
	routinghotcache.SetMetricForTest(key, routinghotcache.MetricSnapshot{
		RequestCount: 1, SuccessCount: 1, P95LatencyMs: math.NaN(), P95TTFTMs: math.Inf(1), TPS: math.Inf(-1),
	})
	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Pools, 1)
	observation := view.Pools[0].Members[0].Models[0]
	assert.Zero(t, observation.P95LatencyMs)
	assert.Zero(t, observation.P95TTFTMs)
	assert.Zero(t, observation.OutputTokensPerSecond)
	assert.False(t, observation.CostKnown)
	assert.Zero(t, observation.Cost)
	assert.False(t, view.Channels[0].BalanceKnown)
	assert.GreaterOrEqual(t, view.Stats.InvalidNumericValues, 4)
	_, err = common.Marshal(view)
	require.NoError(t, err)
}

func TestBuildSnapshotHonorsCanceledContext(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := buildSnapshotContext(ctx, db, DefaultSnapshotLimits)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSnapshotFailsClosedForInvalidPolicyReferences(t *testing.T) {
	tests := []struct {
		name       string
		channels   []model.Channel
		configure  func(t *testing.T, db *gorm.DB, document *model.RoutingPolicyDocument)
		invalidate func(t *testing.T, db *gorm.DB, document model.RoutingPolicyDocument)
	}{
		{
			name:     "missing channel",
			channels: []model.Channel{{Id: 701, Name: "primary", Key: "key-a", Group: "default", Models: "gpt-test"}},
			configure: func(t *testing.T, db *gorm.DB, document *model.RoutingPolicyDocument) {
				document.Pools[0].Members[0].ChannelID = 701
			},
			invalidate: func(t *testing.T, db *gorm.DB, document model.RoutingPolicyDocument) {
				require.NoError(t, db.Where("id = ?", document.Pools[0].Members[0].ChannelID).
					Delete(&model.Channel{}).Error)
			},
		},
		{
			name:     "missing credential",
			channels: []model.Channel{{Id: 701, Name: "primary", Key: "key-a", Group: "default", Models: "gpt-test"}},
			configure: func(t *testing.T, db *gorm.DB, document *model.RoutingPolicyDocument) {
				var credential model.RoutingCredentialRef
				require.NoError(t, db.Where("channel_id = ? AND active = ?", 701, true).First(&credential).Error)
				document.Pools[0].Members[0].ChannelID = 701
				document.Pools[0].Members[0].CredentialIDs = []int{credential.ID}
			},
			invalidate: func(t *testing.T, db *gorm.DB, document model.RoutingPolicyDocument) {
				require.NoError(t, db.Where("id = ?", document.Pools[0].Members[0].CredentialIDs[0]).
					Delete(&model.RoutingCredentialRef{}).Error)
			},
		},
		{
			name:     "active member generation does not match current channel",
			channels: []model.Channel{{Id: 704, Name: "primary", Group: "default", Models: "gpt-test"}},
			configure: func(t *testing.T, db *gorm.DB, document *model.RoutingPolicyDocument) {
				document.Pools[0].Members[0].ChannelID = 704
			},
			invalidate: func(t *testing.T, db *gorm.DB, document model.RoutingPolicyDocument) {
				require.NoError(t, db.Model(&model.RoutingPoolMember{}).
					Where("id = ?", document.Pools[0].Members[0].MemberID).
					Update("channel_generation", strings.Repeat("f", 32)).Error)
			},
		},
		{
			name: "credential belongs to another channel",
			channels: []model.Channel{
				{Id: 702, Name: "primary", Key: "key-a", Group: "default", Models: "gpt-test"},
				{Id: 703, Name: "secondary", Key: "key-b", Group: "other", Models: "gpt-test"},
			},
			configure: func(t *testing.T, db *gorm.DB, document *model.RoutingPolicyDocument) {
				var credential model.RoutingCredentialRef
				require.NoError(t, db.Where("channel_id = ? AND active = ?", 702, true).First(&credential).Error)
				document.Pools[0].Members[0].ChannelID = 702
				document.Pools[0].Members[0].CredentialIDs = []int{credential.ID}
			},
			invalidate: func(t *testing.T, db *gorm.DB, document model.RoutingPolicyDocument) {
				require.NoError(t, db.Model(&model.RoutingCredentialRef{}).
					Where("id = ?", document.Pools[0].Members[0].CredentialIDs[0]).
					Update("channel_id", 703).Error)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openSnapshotTestDB(t)
			withSnapshotTestDB(t, db)
			withSnapshotSecret(t)
			require.NoError(t, db.Create(&test.channels).Error)
			_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
			require.NoError(t, err)
			document := model.RoutingPolicyDocument{
				SchemaVersion: model.RoutingPolicySchemaVersion,
				Pools: []model.RoutingPolicyPoolContent{{
					PoolID:          8_001,
					GroupName:       "manual",
					DisplayName:     "Manual",
					DeploymentStage: model.RoutingDeploymentStageShadow,
					PolicyProfile:   model.RoutingPolicyProfileBalanced,
					Members: []model.RoutingPolicyMemberContent{{
						MemberID: 8_101, ChannelID: 1, Enabled: true, Weight: 1,
					}},
				}},
			}
			test.configure(t, db, &document)
			policyMember := &document.Pools[0].Members[0]
			var topologyMember model.RoutingPoolMember
			require.NoError(t, db.Where(
				"channel_id = ? AND active = ?", policyMember.ChannelID, true,
			).First(&topologyMember).Error)
			var topologyPool model.RoutingPool
			require.NoError(t, db.Where("id = ?", topologyMember.PoolID).First(&topologyPool).Error)
			document.Pools[0].PoolID = topologyPool.ID
			document.Pools[0].GroupName = topologyPool.GroupName
			document.Pools[0].DisplayName = topologyPool.DisplayName
			policyMember.MemberID = topologyMember.ID
			policyMember.RoutingGeneration = topologyMember.ChannelGeneration
			_, err = model.PublishRoutingPolicyRevisionDBContext(
				context.Background(), db, 0, document,
				model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 7, Reason: "reference_test"},
			)
			require.NoError(t, err)
			test.invalidate(t, db, document)

			_, err = buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
			assert.ErrorIs(t, err, ErrSnapshotPolicyReference)
		})
	}
}

func TestSlowSnapshotBuildReleasesTelemetryMaintenanceAfterDatabaseSnapshot(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	require.NoError(t, db.Create(&model.Channel{
		Id: 704, Name: "slow-build", Group: "default", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	var releaseOnce sync.Once
	releaseBuild := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseBuild()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:slow_snapshot_revision", func(tx *gorm.DB) {
		if tx.Statement.Table != "routing_policy_revisions" {
			return
		}
		blockOnce.Do(func() {
			close(blocked)
			<-release
		})
	}))

	done := make(chan error, 1)
	go func() {
		_, buildErr := buildSnapshotContext(context.Background(), db, DefaultSnapshotLimits)
		done <- buildErr
	}()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("snapshot build did not reach the delayed revision query")
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lockRoutingTelemetry(lockCtx), "slow database work must not starve telemetry flush")
	unlockRoutingTelemetry()
	releaseBuild()
	require.NoError(t, <-done)
}

func openSnapshotTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.RoutingTopologyMetadata{},
		&model.RoutingPool{},
		&model.RoutingPoolMember{},
		&model.RoutingCredentialRef{},
		&model.RoutingDecisionAudit{},
		&model.RoutingDecisionReplayChunk{},
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
		&model.RoutingChannelBinding{},
		&model.RoutingConfigurationEpoch{},
		&model.RoutingChannelConfiguration{},
		&model.RoutingMetricRollup{},
	))
	require.NoError(t, model.EnsureRoutingConfigurationEpoch(db))
	return db
}

func snapshotPolicyDocumentForStages(stages ...string) model.RoutingPolicyDocument {
	document := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools:         make([]model.RoutingPolicyPoolContent, len(stages)),
	}
	for index, stage := range stages {
		document.Pools[index] = model.RoutingPolicyPoolContent{
			PoolID:          8_000 + index,
			GroupName:       "activation-" + stage + "-" + string(rune('a'+index)),
			DisplayName:     "Activation " + stage,
			DeploymentStage: stage,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
		}
	}
	return document
}

func openSnapshotExternalTestDB(t *testing.T, dbType common.DatabaseType, dsn string) *gorm.DB {
	t.Helper()
	var (
		db  *gorm.DB
		err error
	)
	switch dbType {
	case common.DatabaseTypeMySQL:
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	case common.DatabaseTypePostgreSQL:
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	default:
		t.Fatalf("unsupported snapshot database type %q", dbType)
	}
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	t.Cleanup(func() { _ = sqlDB.Close() })

	models := []any{
		&model.RoutingMetricRollup{},
		&model.RoutingChannelBinding{},
		&model.RoutingChannelConfiguration{},
		&model.RoutingConfigurationEpoch{},
		&model.RoutingCredentialRef{},
		&model.RoutingDecisionReplayChunk{},
		&model.RoutingDecisionAudit{},
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
		&model.RoutingPoolMember{},
		&model.RoutingPool{},
		&model.RoutingTopologyMetadata{},
		&model.Channel{},
	}
	for _, table := range models {
		if db.Migrator().HasTable(table) {
			t.Skip("refusing to run snapshot contract against a non-empty external database")
		}
	}
	t.Cleanup(func() {
		for _, table := range models {
			_ = db.Migrator().DropTable(table)
		}
	})
	return db
}

func withSnapshotTestDB(t *testing.T, db *gorm.DB) {
	withSnapshotTestDBType(t, db, common.DatabaseTypeSQLite)
}

func withSnapshotTestDBType(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	model.DB = db
	common.SetDatabaseTypes(dbType, dbType)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousType, previousLogType)
	})
}

func withSnapshotSecret(t *testing.T) {
	t.Helper()
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-channel-routing-snapshot-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
}

type snapshotMetricQueryCounts struct {
	total   int
	payload int
}

func setupSnapshotMetricPaginationTest(t *testing.T, channelID int) (*gorm.DB, model.RoutingPoolMember) {
	t.Helper()
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	withSnapshotSecret(t)
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	require.NoError(t, db.Create(&model.Channel{
		Id: channelID, Name: "pagination", Group: "default", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = SyncLegacyRoutingPolicyContext(context.Background())
	require.NoError(t, err)
	var member model.RoutingPoolMember
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&member).Error)
	return db, member
}

func snapshotMetricRollup(
	member model.RoutingPoolMember,
	modelKey string,
	modelName string,
	bucketTs int64,
	latencySketch []byte,
) model.RoutingMetricRollup {
	requestCount := int64(0)
	codecVersion := 0
	if len(latencySketch) > 0 {
		requestCount = 1
		codecVersion = routingdistribution.SketchCodecVersion
	}
	return model.RoutingMetricRollup{
		MemberID:             member.ID,
		CredentialID:         0,
		ModelName:            modelName,
		ModelKey:             modelKey,
		BucketTs:             bucketTs,
		ChannelID:            member.ChannelID,
		ChannelGeneration:    member.ChannelGeneration,
		PoolID:               member.PoolID,
		SchemaVersion:        model.RoutingMetricRollupSchemaVersion,
		LastSnapshotRevision: 1,
		SketchCodecVersion:   codecVersion,
		LatencySampleCount:   requestCount,
		LatencySketch:        latencySketch,
		RequestCount:         requestCount,
		SuccessCount:         requestCount,
		TotalLatencyMs:       requestCount,
	}
}

func snapshotTTFTRollup(
	t *testing.T,
	memberID int,
	poolID int,
	channelID int,
	credentialID int,
	modelName string,
	bucketTs int64,
	ttftMs int64,
	revision int64,
) model.RoutingMetricRollup {
	t.Helper()
	sketch := snapshotTestSketch(t, ttftMs)
	return model.RoutingMetricRollup{
		MemberID:                memberID,
		CredentialID:            credentialID,
		ModelName:               modelName,
		BucketTs:                bucketTs,
		ChannelID:               channelID,
		PoolID:                  poolID,
		SchemaVersion:           model.RoutingMetricRollupSchemaVersion,
		LastSnapshotRevision:    revision,
		SketchCodecVersion:      routingdistribution.SketchCodecVersion,
		LatencySampleCount:      1,
		LatencySketch:           append([]byte(nil), sketch...),
		TtftSampleCount:         1,
		TtftSketch:              append([]byte(nil), sketch...),
		RequestCount:            1,
		SuccessCount:            1,
		ReliabilityRequestCount: 1,
		TotalLatencyMs:          ttftMs,
		TtftSumMs:               ttftMs,
		TtftCount:               1,
	}
}

func observeSnapshotMetricQueries(t *testing.T, db *gorm.DB) *snapshotMetricQueryCounts {
	t.Helper()
	counts := &snapshotMetricQueryCounts{}
	const callbackName = "channelrouting:snapshot_metric_query_counts"
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "metric_rollups" {
			return
		}
		counts.total++
		for _, selected := range tx.Statement.Selects {
			if selected == "metric_rollups.*" {
				counts.payload++
				break
			}
		}
	}))
	return counts
}

func snapshotTestSketch(t *testing.T, values ...int64) []byte {
	t.Helper()
	sketch := routingdistribution.NewDurationSketch()
	for _, value := range values {
		_, err := sketch.AddMillis(value)
		require.NoError(t, err)
	}
	data, err := sketch.MarshalBinary()
	require.NoError(t, err)
	return data
}

func TestAggregateRoutingMetricTTFTP95MergesUnderlyingSamples(t *testing.T) {
	lowVolume := routingdistribution.NewDurationSketch()
	for sample := 0; sample < 99; sample++ {
		_, err := lowVolume.AddMillis(100)
		require.NoError(t, err)
	}
	highOutlier := routingdistribution.NewDurationSketch()
	_, err := highOutlier.AddMillis(10_000)
	require.NoError(t, err)

	metrics := map[stableMetricKey]stableMetricAggregate{
		{memberID: 1, model: "gpt-test"}: {ttftCount: 99, ttftSketch: lowVolume},
		{memberID: 2, model: "gpt-test"}: {ttftCount: 1, ttftSketch: highOutlier},
	}
	p95, known, err := aggregateRoutingMetricTTFTP95(metrics, map[memberModelKey]ModelSnapshot{
		{memberID: 1, model: "gpt-test"}: {MetricSource: "stable_rollup"},
		{memberID: 2, model: "gpt-test"}: {MetricSource: "stable_rollup"},
	})
	require.NoError(t, err)
	require.True(t, known)
	assert.InDelta(t, 100, p95, 3)
}

func TestAggregateRoutingMetricTTFTP95ExcludesModelsOutsideCurrentSnapshot(t *testing.T) {
	current := routingdistribution.NewDurationSketch()
	_, err := current.AddMillis(25)
	require.NoError(t, err)
	removed := routingdistribution.NewDurationSketch()
	_, err = removed.AddMillis(10_000)
	require.NoError(t, err)

	p95, known, err := aggregateRoutingMetricTTFTP95(
		map[stableMetricKey]stableMetricAggregate{
			{memberID: 1, model: "current"}: {ttftCount: 1, ttftSketch: current},
			{memberID: 1, model: "removed"}: {ttftCount: 1, ttftSketch: removed},
		},
		map[memberModelKey]ModelSnapshot{
			{memberID: 1, model: "current"}: {MetricSource: "stable_rollup"},
		},
	)
	require.NoError(t, err)
	require.True(t, known)
	assert.InDelta(t, 25, p95, 1)
}

func TestPublishRuntimeSnapshotIsRevisionMonotonicAndHashBound(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	first, err := publishRuntimeSnapshot(&runtimeSnapshot{view: SnapshotView{
		Revision: 5, PolicyHash: strings.Repeat("a", 64),
	}})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first.RuntimeGeneration)

	_, err = publishRuntimeSnapshot(&runtimeSnapshot{view: SnapshotView{
		Revision: 3, PolicyHash: strings.Repeat("a", 64),
	}})
	assert.ErrorIs(t, err, ErrSnapshotRevisionRollback)
	_, err = publishRuntimeSnapshot(&runtimeSnapshot{view: SnapshotView{
		Revision: 5, PolicyHash: strings.Repeat("b", 64),
	}})
	assert.ErrorIs(t, err, ErrSnapshotRevisionConflict)

	current, ok := CurrentSnapshot()
	require.True(t, ok)
	assert.Equal(t, uint64(5), current.Revision)
	assert.Equal(t, strings.Repeat("a", 64), current.PolicyHash)

	refreshed, err := publishRuntimeSnapshot(&runtimeSnapshot{view: SnapshotView{
		Revision: 5, PolicyHash: strings.Repeat("a", 64),
	}})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), refreshed.RuntimeGeneration)
}

func snapshotTestQuantile(t *testing.T, quantile float64, values ...int64) float64 {
	t.Helper()
	sketch, err := routingdistribution.DecodeDurationSketch(snapshotTestSketch(t, values...), routingdistribution.SketchCodecVersion)
	require.NoError(t, err)
	result, err := sketch.Quantile(quantile)
	require.NoError(t, err)
	require.True(t, result.Known)
	return result.ValueMilliseconds
}
