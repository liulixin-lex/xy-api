package model

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingEndpointEvidenceAndSharedStateContract(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingEndpointStateContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingEndpointStateExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingEndpointStateContract(t, db, test.dbType)
		})
	}
}

func TestRoutingEndpointCountValidationRejectsOverflowingPartitions(t *testing.T) {
	evidence := RoutingEndpointEvidence{
		NodeID: "node-a", NodeEpochID: "epoch-a", EndpointHost: "api.endpoint.test",
		EndpointAuthority: "https://api.endpoint.test:443", Region: "default",
		BucketTs: 1, RequestCount: math.MaxInt64, ReachableCount: math.MaxInt64,
		NetworkFailureCount: math.MaxInt64,
	}
	assert.False(t, validRoutingEndpointEvidence(evidence, 1_000))
	evidence.NetworkFailureCount = 0
	assert.True(t, validRoutingEndpointEvidence(evidence, 1_000))

	aggregate := RoutingEndpointEvidenceAggregate{
		NodeID: "node-a", NodeKey: strings.Repeat("a", 64), EndpointHost: "api.endpoint.test",
		EndpointAuthority: "https://api.endpoint.test:443", EndpointAuthorityKey: strings.Repeat("b", 64),
		Region: "default", RegionKey: strings.Repeat("c", 64), RequestCount: math.MaxInt64,
		ReachableCount: math.MaxInt64, NetworkFailureCount: math.MaxInt64, EvidenceThroughMs: 1,
	}
	assert.False(t, validRoutingEndpointAggregate(aggregate))
	aggregate.NetworkFailureCount = 0
	assert.True(t, validRoutingEndpointAggregate(aggregate))
}

func runRoutingEndpointStateContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingBreakerResetFence{}, &RoutingEndpointEvidence{}, &RoutingEndpointSharedState{}))
	region := "endpoint-contract"
	authority := "https://api.endpoint-contract.test:443"
	require.NoError(t, db.Where("region = ?", region).Delete(&RoutingEndpointEvidence{}).Error)
	require.NoError(t, db.Where("region = ?", region).Delete(&RoutingEndpointSharedState{}).Error)
	nowMs, err := RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	bucket := nowMs / 1000 / 60 * 60

	rows := []RoutingEndpointEvidence{
		endpointEvidenceForTest("node-a", "epoch-a-1", authority, region, bucket, 4, 1),
		endpointEvidenceForTest("node-a", "epoch-a-2", authority, region, bucket, 3, 1),
		endpointEvidenceForTest("node-b", "epoch-b-1", authority, region, bucket, 5, 2),
	}
	_, err = UpsertRoutingEndpointEvidenceContext(context.Background(), rows)
	require.NoError(t, err)

	updated := endpointEvidenceForTest("node-a", "epoch-a-1", authority, region, bucket, 6, 2)
	_, err = UpsertRoutingEndpointEvidenceContext(context.Background(), []RoutingEndpointEvidence{updated})
	require.NoError(t, err)
	aggregates, evaluatedAtMs, err := AggregateRoutingEndpointEvidenceContext(
		context.Background(), region, bucket-60, nowMs-60_000,
	)
	require.NoError(t, err)
	require.Len(t, aggregates, 2, "process epochs must collapse into one stable node voter")
	byNode := map[string]RoutingEndpointEvidenceAggregate{}
	for _, aggregate := range aggregates {
		byNode[aggregate.NodeID] = aggregate
	}
	assert.Equal(t, int64(9), byNode["node-a"].RequestCount)
	assert.Equal(t, int64(3), byNode["node-a"].NetworkFailureCount)
	assert.Equal(t, int64(5), byNode["node-b"].RequestCount)

	through := max(aggregates[0].EvidenceThroughMs, aggregates[1].EvidenceThroughMs)
	state := RoutingEndpointSharedState{
		EndpointHost: "api.endpoint-contract.test", EndpointAuthority: authority, Region: region,
		State: RoutingBreakerStateOpen, Reason: "regional_network_quorum",
		EvidenceCount: 14, NetworkFailureCount: 5, NodeCount: 2, FailureNodeCount: 2,
		CooldownUntilMs: evaluatedAtMs + 30_000, EvidenceFromMs: evaluatedAtMs - 60_000,
		EvidenceThroughMs: through, EvaluatedAtMs: evaluatedAtMs, ExpiresAtMs: evaluatedAtMs + 120_000,
		CreatedTimeMs: evaluatedAtMs, UpdatedTimeMs: evaluatedAtMs,
	}
	require.NoError(t, UpsertRoutingEndpointSharedStatesContext(context.Background(), []RoutingEndpointSharedState{state}))

	stale := state
	stale.State = RoutingBreakerStateHealthy
	stale.Reason = ""
	stale.EvidenceThroughMs--
	stale.EvaluatedAtMs++
	stale.UpdatedTimeMs++
	require.NoError(t, UpsertRoutingEndpointSharedStatesContext(context.Background(), []RoutingEndpointSharedState{stale}))
	states, _, err := ListFreshRoutingEndpointSharedStatesContext(context.Background(), region)
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, RoutingBreakerStateOpen, states[0].State, "older evidence must not resurrect a stale shared state")
	assert.Equal(t, 2, states[0].NodeCount)

	deleted, err := DeleteRoutingEndpointHistoryBeforeContext(context.Background(), nowMs+10_000)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
}

func endpointEvidenceForTest(
	nodeID string,
	epochID string,
	authority string,
	region string,
	bucket int64,
	requests int64,
	failures int64,
) RoutingEndpointEvidence {
	return RoutingEndpointEvidence{
		NodeID: nodeID, NodeEpochID: epochID, QuorumEligible: true,
		EndpointHost: "api.endpoint-contract.test", EndpointAuthority: authority, Region: region, BucketTs: bucket,
		RequestCount: requests, ReachableCount: requests - failures, NetworkFailureCount: failures,
		TotalLatencyMs: requests * 10, TtftSumMs: requests * 5, TtftCount: requests,
	}
}
