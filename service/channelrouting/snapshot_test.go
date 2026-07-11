package channelrouting

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
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
	require.NoError(t, db.Create(&model.Channel{
		Id: 301, Name: "primary", Type: 1, Status: common.ChannelStatusEnabled,
		Key: "credential-a", Group: "vip", Models: "gpt-b,gpt-a,gpt-a",
		Priority: &priority, Weight: &weight, BaseURL: &baseURL,
		Balance: 12.5, BalanceUpdatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 301, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost.example", UpstreamGroup: "vip", Enabled: true,
		SyncFailureCount: 2, SyncBackoffUntil: 999,
	}).Error)

	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-a", Group: "vip",
	}, routinghotcache.MetricSnapshot{
		RequestCount: 10, SuccessCount: 9, ReliabilityRequestCount: 9, ReliabilityFailureCount: 1,
		P95LatencyMs: 1200, P95TTFTMs: 300, OutputTokens: 900, GenerationMs: 3000, TPS: 300, UpdatedUnix: 123,
	})
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: 301, Model: "gpt-a"}, routinghotcache.CostSnapshot{
		Known: true, Cost: 0.0012, Confidence: model.RoutingCostConfidenceFull, UpdatedUnix: 124,
	})

	view, err := RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), view.Revision)
	require.Len(t, view.Pools, 1)
	require.Len(t, view.Pools[0].Members, 1)
	member := view.Pools[0].Members[0]
	assert.Equal(t, int64(11), member.LegacyPriority)
	assert.Equal(t, int64(23), member.LegacyWeight)
	assert.True(t, member.TelemetryKnown)
	require.Len(t, member.Models, 2)
	assert.Equal(t, "gpt-a", member.Models[0].ModelName)
	assert.True(t, member.Models[0].MetricKnown)
	assert.Equal(t, float64(300), member.Models[0].OutputTokensPerSecond)
	assert.Equal(t, "legacy_compat", member.Models[0].MetricSource)
	assert.True(t, member.Models[0].CostKnown)

	require.Len(t, view.Channels, 1)
	assert.Equal(t, "https://example.com", view.Channels[0].Endpoint)
	assert.NotContains(t, view.Channels[0].Endpoint, "secret")
	assert.True(t, view.Channels[0].CostConnectorEnabled)
	assert.Equal(t, 2, view.Channels[0].CostSyncFailures)
	assert.Equal(t, float64(1), view.Stats.TelemetryCoverage)
	assert.Equal(t, float64(1), view.Stats.CredentialCoverage)

	identity, ok := ResolveIdentity("vip", 301, "credential-a")
	require.True(t, ok)
	assert.Equal(t, view.Revision, identity.SnapshotRevision)
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
	}}))
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: member.PoolID, PoolMemberID: member.ID, CredentialID: member.CredentialIDs[1], ChannelID: 305,
		Model: "gpt-test", BucketTs: now, LastSnapshotRevision: first.Revision,
		RequestCount: 2, FailureCount: 2, UnknownClassificationCount: 1,
		TotalLatencyMs: 1100, TtftSumMs: 400, TtftCount: 2,
		OutputTokens: 20, GenerationMs: 200, Err429: 1, Err529: 1,
		RetryAfterCount: 2, RetryAfterTotalMs: 3000,
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
	assert.Zero(t, observation.P95LatencyMs)
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
		Model: "gpt-test", BucketTs: currentBucket, LastSnapshotRevision: first.Revision,
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
		Model: "gpt-test", BucketTs: now, LastSnapshotRevision: first.Revision,
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
		Model: "gpt-test", BucketTs: now, LastSnapshotRevision: second.Revision,
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
		&model.RoutingChannelBinding{},
		&model.RoutingMetricRollup{},
	))
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
	fingerprintA, err := model.RoutingCredentialFingerprint(501, "key-a")
	require.NoError(t, err)
	fingerprintB, err := model.RoutingCredentialFingerprint(501, "key-b")
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
		Model: "Model-X", BucketTs: now, LastSnapshotRevision: 1, RequestCount: 3, SuccessCount: 3,
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

	snapshot, err := buildSnapshotContext(context.Background(), db, SnapshotLimits{
		MaxPools: 1, MaxMembers: 1, MaxCredentials: 2, MaxChannels: 1,
		MaxModelsPerChannel: 1, MaxTotalModelSnapshots: 1, MaxModelBytesPerChannel: 1024,
		MaxTotalChannelBytes: 4096, MaxTotalBindingBytes: 4096, MaxMetricAggregates: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), snapshot.view.Pools[0].Members[0].Models[0].RequestCount)
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

func TestSnapshotSanitizesNonFiniteTelemetryAndCostValues(t *testing.T) {
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
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	key := routinghotcache.Key{
		ChannelID: 304, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}
	routinghotcache.SetMetricForTest(key, routinghotcache.MetricSnapshot{
		RequestCount: 1, SuccessCount: 1, P95LatencyMs: math.NaN(), P95TTFTMs: math.Inf(1), TPS: math.Inf(-1),
	})
	routinghotcache.SetCostForTest(key.CostKey(), routinghotcache.CostSnapshot{
		Known: true, Cost: math.NaN(), Confidence: model.RoutingCostConfidenceFull,
	})
	routinghotcache.SetBalanceForTest(304, routinghotcache.BalanceSnapshot{
		Known: true, Balance: math.Inf(1), UpdatedUnix: common.GetTimestamp(),
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
	assert.GreaterOrEqual(t, view.Stats.InvalidNumericValues, 5)
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
		&model.RoutingChannelBinding{},
		&model.RoutingMetricRollup{},
	))
	return db
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
		&model.RoutingCredentialRef{},
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
