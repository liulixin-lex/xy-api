package channelrouting

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSharedEndpointBreakerRequiresDistinctStableNodeQuorumAndRestoresCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerResetFence{}, &model.RoutingEndpointEvidence{}, &model.RoutingEndpointSharedState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	t.Setenv("ROUTING_REGION", "quorum-region")

	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	bucket := nowMs / 1000 / 60 * 60
	authority := "https://api.quorum.test:443"
	rows := []model.RoutingEndpointEvidence{
		endpointSharedEvidenceForTest("node-a", "epoch-a", authority, bucket),
		endpointSharedEvidenceForTest("node-b", "epoch-b", authority, bucket),
	}
	_, err = model.UpsertRoutingEndpointEvidenceContext(context.Background(), rows)
	require.NoError(t, err)
	setting := smart_routing_setting.Normalize(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeEnterpriseSLO,
		FailureRatePct: 50, MinVolume: 10, BaseCooldownSec: 30,
		FlushIntervalMin: 1, SnapshotStaleSec: 600,
	})
	require.NoError(t, evaluateRoutingEndpointSharedStateContext(context.Background(), setting))
	require.NoError(t, refreshRoutingEndpointSharedCacheContext(context.Background()))

	key := routingbreaker.NewEndpointKey(authority, "quorum-region").HotcacheKey()
	shared, ok := routinghotcache.GetSharedEndpointBreaker(key)
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateOpen, shared.State)
	assert.Equal(t, 2, shared.NodeCount)
	assert.Equal(t, 2, shared.FailureNodeCount)

	computedAuthority := EndpointAuthority(authority, 7)
	assert.Equal(t, authority, computedAuthority)
	direct, directOK := routinghotcache.GetSharedEndpointBreaker(
		routingbreaker.NewEndpointKey(computedAuthority, "quorum-region").HotcacheKey(),
	)
	require.True(t, directOK)
	assert.Equal(t, model.RoutingBreakerStateOpen, direct.State)
	effective, gotAuthority, region := endpointBreakerForChannel(
		ChannelSnapshot{ID: 7, Endpoint: authority}, time.Unix(direct.UpdatedUnix, 0), setting.SnapshotStaleSec,
	)
	require.NotNil(t, effective)
	assert.Equal(t, authority, gotAuthority)
	assert.Equal(t, "quorum-region", region)
	assert.Equal(t, model.RoutingBreakerStateOpen, effective.State)
}

func TestSharedEndpointBreakerDoesNotPromoteSingleNodeFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerResetFence{}, &model.RoutingEndpointEvidence{}, &model.RoutingEndpointSharedState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	t.Setenv("ROUTING_REGION", "single-node-region")

	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	bucket := nowMs / 1000 / 60 * 60
	authority := "https://api.single-node.test:443"
	_, err = model.UpsertRoutingEndpointEvidenceContext(context.Background(), []model.RoutingEndpointEvidence{
		endpointSharedEvidenceForTest("node-a", "epoch-a", authority, bucket),
	})
	require.NoError(t, err)
	setting := smart_routing_setting.Normalize(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeEnterpriseSLO,
		FailureRatePct: 50, MinVolume: 10, BaseCooldownSec: 30,
		FlushIntervalMin: 1, SnapshotStaleSec: 600,
	})
	require.NoError(t, evaluateRoutingEndpointSharedStateContext(context.Background(), setting))
	require.NoError(t, refreshRoutingEndpointSharedCacheContext(context.Background()))

	shared, ok := routinghotcache.GetSharedEndpointBreaker(
		routingbreaker.NewEndpointKey(authority, "single-node-region").HotcacheKey(),
	)
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateHealthy, shared.State)
	assert.Equal(t, 1, shared.NodeCount)
	assert.Equal(t, 1, shared.FailureNodeCount)
}

func TestStableNodeIDMustBeExplicitAndValid(t *testing.T) {
	t.Setenv("ROUTING_NODE_ID", "")
	_, ok := StableNodeID()
	assert.False(t, ok)
	t.Setenv("ROUTING_NODE_ID", "gateway-shanghai-01")
	value, ok := StableNodeID()
	assert.True(t, ok)
	assert.Equal(t, "gateway-shanghai-01", value)
	t.Setenv("ROUTING_NODE_ID", "invalid node")
	_, ok = StableNodeID()
	assert.False(t, ok)
}

func endpointSharedEvidenceForTest(
	nodeID string,
	epochID string,
	authority string,
	bucket int64,
) model.RoutingEndpointEvidence {
	return model.RoutingEndpointEvidence{
		NodeID: nodeID, NodeEpochID: epochID, QuorumEligible: true,
		EndpointHost: "api.quorum.test", EndpointAuthority: authority, Region: RoutingRegion(), BucketTs: bucket,
		RequestCount: 10, ReachableCount: 2, NetworkFailureCount: 8,
		TotalLatencyMs: 1_000, TtftSumMs: 500, TtftCount: 10,
	}
}
