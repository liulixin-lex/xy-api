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
		context.Background(), "legacy-reconcile", "node-a", 30_000, 300_000, false,
	)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, int64(1), first.FencingToken)
	assert.Len(t, first.LeaseToken, 32)

	busy, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 30_000, 300_000, true,
	)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, first.LeaseToken, busy.LeaseToken)

	renewed, err := RenewRoutingControlLeaseContext(context.Background(), first, 120_000)
	require.NoError(t, err)
	assert.Greater(t, renewed.LeaseUntilMs, first.LeaseUntilMs)
	assert.Equal(t, first.FencingToken, renewed.FencingToken)
	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), renewed))
	throttled, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 30_000, 300_000, false,
	)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Positive(t, throttled.LastCompletedMs)

	forced, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 30_000, 300_000, true,
	)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, int64(2), forced.FencingToken)
	require.NoError(t, ReleaseRoutingControlLeaseContext(context.Background(), forced))
}

func TestRoutingControlLeaseRejectsStaleCompletion(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}))

	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, db.Model(&RoutingControlLease{}).
		Where("lease_name = ?", lease.LeaseName).Update("lease_until_ms", 0).Error)
	newer, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "legacy-reconcile", "node-b", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)

	_, err = RenewRoutingControlLeaseContext(context.Background(), lease, 60_000)
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost)
	assert.ErrorIs(t, CompleteRoutingControlLeaseContext(context.Background(), lease), ErrRoutingControlLeaseLost)
	require.NoError(t, CompleteRoutingControlLeaseContext(context.Background(), newer))
}
