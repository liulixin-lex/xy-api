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

func TestMigrateDBPathsRegisterDurableTaskBillingOperations(t *testing.T) {
	tests := []struct {
		name    string
		migrate func() error
	}{
		{name: "serial", migrate: migrateDB},
		{name: "fast", migrate: migrateDBFast},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			t.Setenv(routingV2AlphaDrainedEnv, "true")
			require.NoError(t, db.AutoMigrate(&legacyLogV0110{}))
			require.NoError(t, db.Create(&legacyLogV0110{
				UserId: 19, CreatedAt: 1_720_000_000, Type: LogTypeManage,
				Content: "v0.1.10 main migration log", RequestId: "legacy-main-log",
			}).Error)
			require.NoError(t, db.AutoMigrate(&AsyncBillingManualResolution{}))
			require.NoError(t, db.Migrator().DropIndex(
				&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
			))
			require.NoError(t, db.Migrator().CreateIndex(
				&legacyAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
			))

			require.NoError(t, test.migrate())
			require.NoError(t, test.migrate(), "migration must remain idempotent across restarts")

			assert.True(t, db.Migrator().HasTable(&TaskBillingOperation{}))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_task"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_key"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_pending"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_lease"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_log_pending"))

			assert.True(t, db.Migrator().HasTable(&MidjourneyBillingOperation{}))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_task"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_key"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_pending"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_lease"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_log_pending"))
			assert.True(t, db.Migrator().HasTable(&IdentityCacheSync{}))
			assert.True(t, db.Migrator().HasIndex(&IdentityCacheSync{}, "idx_identity_cache_sync_pending"))
			assert.True(t, db.Migrator().HasIndex(&IdentityCacheSync{}, "idx_identity_cache_sync_live_pending"))
			var retainedLog Log
			require.NoError(t, db.Where("request_id = ?", "legacy-main-log").First(&retainedLog).Error)
			assert.Equal(t, "v0.1.10 main migration log", retainedLog.Content)
			assert.Nil(t, retainedLog.BillingOperationKey)
			assert.True(t, db.Migrator().HasIndex(&Log{}, billingLogOperationKeyIndex))

			ready, err := RoutingV2SchemaReady(db)
			require.NoError(t, err)
			assert.True(t, ready)
			asyncBillingReady, err := AsyncBillingV2SchemaReady(db)
			require.NoError(t, err)
			assert.True(t, asyncBillingReady)
			manualIndexReady, err := asyncBillingUniqueIndexReady(
				db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
				[]string{"reservation_id", "expected_version"},
			)
			require.NoError(t, err)
			assert.True(t, manualIndexReady)
		})
	}
}
