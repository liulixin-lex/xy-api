package model

import (
	"context"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingControlLeaseSerializesAndThrottlesClusterWork(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingControlLeaseContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingControlLeaseExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingControlLeaseContract(t, db, test.dbType)
		})
	}
}

func runRoutingControlLeaseContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}))
	require.NoError(t, db.Where("lease_name = ?", "legacy-reconcile").Delete(&RoutingControlLease{}).Error)

	first, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-a", 1_000, 30_000, 300_000, false,
	)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, int64(1), first.FencingToken)
	assert.Len(t, first.LeaseToken, 32)

	busy, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 1_001, 30_000, 300_000, true,
	)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, first.LeaseToken, busy.LeaseToken)

	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), first, 2_000))
	throttled, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 2_001, 30_000, 300_000, false,
	)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, int64(2_000), throttled.LastCompletedMs)

	forced, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 2_002, 30_000, 300_000, true,
	)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, int64(2), forced.FencingToken)
	require.NoError(t, ReleaseRoutingControlLeaseContext(context.Background(), forced, 2_003))
}

func TestRoutingControlLeaseRejectsStaleCompletion(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}))

	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-a", 1_000, 10, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	newer, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 1_011, 10, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)

	assert.ErrorIs(t, CompleteRoutingControlLeaseContext(context.Background(), lease, 1_012), ErrRoutingControlLeaseLost)
	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), newer, 1_013))
}
