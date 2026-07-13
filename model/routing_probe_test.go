package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingProbeResultFencingIdempotencyAndRetention(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingProbeResultContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingProbeResultExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingProbeResultContract(t, db, test.dbType)
		})
	}
}

func runRoutingProbeResultContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}, &RoutingProbeResult{}))
	require.NoError(t, db.Where("lease_name LIKE ?", "routing-probe-test-%").Delete(&RoutingControlLease{}).Error)
	require.NoError(t, db.Where("probe_id IN ?", []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}).Delete(&RoutingProbeResult{}).Error)

	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-probe-test-a", "node-a", 1_000, 0, false,
	)
	require.NoError(t, err)
	require.True(t, acquired)

	probe := RoutingProbeResult{
		ProbeID:           strings.Repeat("a", 64),
		TargetKey:         strings.Repeat("1", 64),
		ProbeType:         RoutingProbeTypeServing,
		SnapshotRevision:  9,
		PoolID:            10,
		MemberID:          11,
		ChannelID:         12,
		CredentialID:      0,
		GroupName:         "default",
		ModelName:         "gpt-test",
		EndpointHost:      "api.example.test",
		EndpointAuthority: "https://api.example.test:443",
		Region:            "us-east-1",
		BreakerScope:      "member",
		EvidenceCount:     1,
		NodeCount:         1,
		BreakerState:      RoutingBreakerStateHealthy,
		Outcome:           RoutingProbeOutcomeSuccess,
		PromptTokens:      1,
		CompletionTokens:  1,
		CostNanoUSD:       10,
		LatencyMs:         100,
		StartedTimeMs:     1_000,
		FinishedTimeMs:    1_100,
		LeaseFencingToken: lease.FencingToken,
		NodeEpochID:       lease.HolderID,
		CreatedTime:       1_100,
	}
	saved, created, err := CreateRoutingProbeResultContext(context.Background(), lease, probe)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Positive(t, saved.ID)

	duplicate, created, err := CreateRoutingProbeResultContext(context.Background(), lease, probe)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, saved.ID, duplicate.ID)

	conflict := probe
	conflict.TargetKey = strings.Repeat("2", 64)
	_, _, err = CreateRoutingProbeResultContext(context.Background(), lease, conflict)
	assert.ErrorIs(t, err, ErrRoutingProbeResultConflict)

	results, err := ListRoutingProbeResultsContext(context.Background(), RoutingProbeResultFilter{PoolID: 10, Limit: 10})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, probe.ProbeID, results[0].ProbeID)
	_, err = ListRoutingProbeResultsContext(context.Background(), RoutingProbeResultFilter{PoolID: -1})
	assert.ErrorIs(t, err, ErrRoutingProbeResultInvalid)
	_, err = ListRoutingProbeResultsContext(context.Background(), RoutingProbeResultFilter{Outcome: "unknown"})
	assert.ErrorIs(t, err, ErrRoutingProbeResultInvalid)

	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), lease))
	newLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-probe-test-a", "node-b", 1_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	stale := probe
	stale.ProbeID = strings.Repeat("b", 64)
	_, _, err = CreateRoutingProbeResultContext(context.Background(), lease, stale)
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost)
	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), newLease))
	require.NoError(t, db.Create(&RoutingControlLease{
		LeaseName: "routing-probe-test-old", HolderID: "", LeaseToken: "",
		LeaseUntilMs: 0, FencingToken: 1, UpdatedTimeMs: 1_000,
	}).Error)
	require.NoError(t, db.Create(&RoutingControlLease{
		LeaseName: "routing-probe-test-fresh", HolderID: "node-fresh", LeaseToken: strings.Repeat("f", 32),
		LeaseUntilMs: 1_000, FencingToken: 1, UpdatedTimeMs: 2_000,
	}).Error)

	deleted, err := DeleteRoutingProbeResultsBeforeContext(context.Background(), 1_100)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
	deleted, err = DeleteRoutingProbeResultsBeforeContext(context.Background(), 1_101)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	deleted, err = DeleteRoutingControlLeasesByPrefixBeforeContext(
		context.Background(), "routing-probe-test-", 1_104, 1_104, 100,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = DeleteRoutingControlLeasesByPrefixBeforeContext(context.Background(), "routing-probe-%", 1_104, 1_104, 100)
	assert.ErrorIs(t, err, ErrRoutingControlLeaseInvalid)
	var freshCount int64
	require.NoError(t, db.Model(&RoutingControlLease{}).Where("lease_name = ?", "routing-probe-test-fresh").Count(&freshCount).Error)
	assert.Equal(t, int64(1), freshCount)
}
