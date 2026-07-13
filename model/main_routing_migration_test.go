package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateRoutingV2DedicatedSchemasRequiresExplicitAlphaDrain(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&routingMetricRollupLegacyIndex{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
	))
	t.Setenv(routingV2AlphaDrainedEnv, "false")

	err := migrateRoutingV2DedicatedSchemas(db)
	assert.ErrorIs(t, err, ErrRoutingMetricRollupAlphaDrainRequired)
	assert.Contains(t, err.Error(), "routing:v2:telemetry")
	assert.Contains(t, err.Error(), routingV2AlphaDrainedEnv)
	assert.False(t, db.Migrator().HasTable(&RoutingErrorBudgetState{}))

	t.Setenv(routingV2AlphaDrainedEnv, "true")
	require.NoError(t, migrateRoutingV2DedicatedSchemas(db))
	rollupReady, err := RoutingMetricRollupRevisionKeySchemaReady(db)
	require.NoError(t, err)
	assert.True(t, rollupReady)
	errorBudgetReady, err := RoutingErrorBudgetSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, errorBudgetReady)
}

func TestWaitRoutingV2SchemaReadyFailsClosedBeforeMasterMigration(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	t.Setenv(routingSchemaReadyWaitSecondsEnv, "0")

	err := waitRoutingV2SchemaReady(db)
	assert.ErrorIs(t, err, ErrRoutingV2SchemaNotReady)
}

func TestRoutingV2SchemaVersionGateRequiresExactMarkerAndPhysicalSchema(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, prepareRoutingCanaryEvaluationWindowUniqueIndex(db))
	require.NoError(t, db.AutoMigrate(routingV2RequiredSchemaModels()...))
	require.NoError(t, migrateRoutingV2DedicatedSchemas(db))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	physicalReady, err := routingV2PhysicalSchemaReady(db)
	require.NoError(t, err)
	require.True(t, physicalReady)
	ready, err := RoutingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, db.Create(&RoutingSchemaVersion{
		Component: routingV2SchemaComponent, Version: "channel-routing-v2-stale", UpdatedTimeMs: 1,
	}).Error)
	ready, err = RoutingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, publishRoutingV2SchemaVersion(db))
	ready, err = RoutingV2SchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)
	t.Setenv(routingSchemaReadyWaitSecondsEnv, "0")
	require.NoError(t, waitRoutingV2SchemaReady(db))
	require.NoError(t, invalidateRoutingV2SchemaVersion(db))
	ready, err = RoutingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, publishRoutingV2SchemaVersion(db))

	require.NoError(t, db.Migrator().DropIndex(&RoutingOperation{}, routingOperationRequestKeyUniqueIndex))
	ready, err = RoutingV2SchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.ErrorIs(t, waitRoutingV2SchemaReady(db), ErrRoutingV2SchemaNotReady)
}
