package model

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingErrorBudgetStateUsesLeaseFencingAndMonotonicEvaluation(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingErrorBudgetStateContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingErrorBudgetStateExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingErrorBudgetStateContract(t, db, test.dbType)
		})
	}
}

func TestAggregateRoutingMetricReliabilityByRevisionFailsClosedForAnyLegacyRow(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingMetricRollup{}))
	require.NoError(t, db.Create(&[]RoutingMetricRollup{
		{
			MemberID: 1, CredentialID: 1, ModelName: "gpt-current", ModelKey: "current",
			BucketTs: 100, ChannelID: 1, PoolID: 7,
			SchemaVersion:        RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
			LastSnapshotRevision: 1, RequestCount: 100,
			ReliabilityRequestCount: 100,
		},
		{
			MemberID: 2, CredentialID: 2, ModelName: "gpt-legacy", ModelKey: "legacy",
			BucketTs: 100, ChannelID: 2, PoolID: 7,
			SchemaVersion: 2, LastSnapshotRevision: 2, RequestCount: 400,
			ReliabilityRequestCount: 400, ReliabilityFailureCount: 8,
		},
	}).Error)

	aggregate, err := AggregateRoutingMetricReliabilityByRevisionContext(
		context.Background(), 7, 1, 0, 200,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(100), aggregate.RequestCount)
	assert.Zero(t, aggregate.FailureCount)
	assert.Equal(t, int64(400), aggregate.UnisolatedRequestCount)
	assert.Equal(t, int64(8), aggregate.UnisolatedFailureCount)
	assert.False(t, aggregate.RevisionIsolated)
}

func runRoutingErrorBudgetStateContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}))
	require.NoError(t, MigrateRoutingErrorBudgetModels(db))
	require.NoError(t, MigrateRoutingErrorBudgetModels(db), "the error budget migration must be idempotent")
	require.NoError(t, db.Where("pool_id = ?", 7).Delete(&RoutingErrorBudgetState{}).Error)
	require.NoError(t, db.Where("pool_id IN ?", []int{7, 99}).Delete(&RoutingErrorBudgetHistory{}).Error)
	require.NoError(t, db.Where("cursor_name IN ?", []string{RoutingErrorBudgetEvaluatorCursor, RoutingErrorBudgetPublisherCursor}).Delete(&RoutingErrorBudgetCursor{}).Error)
	require.NoError(t, db.Where("lease_name = ?", "routing-error-budget-evaluator").Delete(&RoutingControlLease{}).Error)
	databaseNowMs, err := routingErrorBudgetDatabaseNowMs(db)
	require.NoError(t, err)

	firstLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-evaluator", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	first := routingErrorBudgetStateForTest(firstLease, 1, "healthy", "within_budget", databaseNowMs)
	transition, err := UpsertRoutingErrorBudgetStateWithTransitionContext(context.Background(), firstLease, &first)
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.Positive(t, transition.HistoryID)
	assert.Equal(t, int64(1), transition.PolicyRevision)
	assert.Equal(t, databaseNowMs, first.FirstObservedAtMs)

	sameState := routingErrorBudgetStateForTest(firstLease, 1, "healthy", "within_budget", databaseNowMs+1)
	transition, err = UpsertRoutingErrorBudgetStateWithTransitionContext(context.Background(), firstLease, &sameState)
	require.NoError(t, err)
	assert.Nil(t, transition)
	assert.Equal(t, databaseNowMs, sameState.FirstObservedAtMs)
	assert.Equal(t, databaseNowMs, sameState.LastChangedAtMs)

	changedState := routingErrorBudgetStateForTest(firstLease, 1, "critical", "fast_multi_window_burn", databaseNowMs+2)
	transition, err = UpsertRoutingErrorBudgetStateWithTransitionContext(context.Background(), firstLease, &changedState)
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.Equal(t, "healthy", transition.PreviousStatus)
	assert.Equal(t, "critical", transition.Status)
	assert.Equal(t, databaseNowMs+2, changedState.FirstObservedAtMs)

	newRevision := routingErrorBudgetStateForTest(firstLease, 2, "warning", "slow_multi_window_burn", databaseNowMs+3)
	transition, err = UpsertRoutingErrorBudgetStateWithTransitionContext(context.Background(), firstLease, &newRevision)
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.Empty(t, transition.PreviousStatus)
	transitionStream, err := ListRoutingErrorBudgetTransitionsAfterContext(
		context.Background(), transition.HistoryID-1, 1,
	)
	require.NoError(t, err)
	require.Len(t, transitionStream, 1)
	assert.Equal(t, transition.HistoryID, transitionStream[0].HistoryID)
	assert.Equal(t, int64(2), transitionStream[0].PolicyRevision)

	revisionOne, err := GetRoutingErrorBudgetStateForRevisionContext(context.Background(), 7, 1)
	require.NoError(t, err)
	assert.Equal(t, "critical", revisionOne.Status)
	revisionTwo, err := GetRoutingErrorBudgetStateForRevisionContext(context.Background(), 7, 2)
	require.NoError(t, err)
	assert.Equal(t, "warning", revisionTwo.Status)
	latest, err := GetRoutingErrorBudgetStateContext(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, int64(2), latest.PolicyRevision)

	history, err := ListRoutingErrorBudgetHistoryContext(context.Background(), 7, 1, 0, 10)
	require.NoError(t, err)
	require.Len(t, history, 2, "unchanged evaluations must not create transition history")
	assert.Equal(t, "critical", history[0].Status)
	assert.Equal(t, "healthy", history[1].Status)

	staleTime := routingErrorBudgetStateForTest(firstLease, 2, "critical", "fast_multi_window_burn", databaseNowMs+3)
	_, err = UpsertRoutingErrorBudgetStateContext(context.Background(), firstLease, &staleTime)
	assert.ErrorIs(t, err, ErrRoutingErrorBudgetStateStale)

	require.NoError(t, db.Model(&RoutingControlLease{}).
		Where("lease_name = ?", firstLease.LeaseName).
		Update("lease_until_ms", databaseNowMs-1).Error)
	expiredLeaseWrite := routingErrorBudgetStateForTest(
		firstLease, 3, "healthy", "within_budget", databaseNowMs-2,
	)
	transition, err = UpsertRoutingErrorBudgetStateWithTransitionContext(
		context.Background(), firstLease, &expiredLeaseWrite,
	)
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost,
		"database time must reject an expired lease even when the evaluation timestamp predates expiry")
	assert.Nil(t, transition)

	newLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-evaluator", "node-b", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	newOwnerState := routingErrorBudgetStateForTest(newLease, 2, "healthy", "within_budget", databaseNowMs+4)
	changed, err := UpsertRoutingErrorBudgetStateContext(context.Background(), newLease, &newOwnerState)
	require.NoError(t, err)
	assert.True(t, changed)

	staleOwner := routingErrorBudgetStateForTest(firstLease, 1, "warning", "slow_multi_window_burn", databaseNowMs+5)
	_, err = UpsertRoutingErrorBudgetStateContext(context.Background(), firstLease, &staleOwner)
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost)

	oldHistory := RoutingErrorBudgetHistory{
		PoolID: 99, PolicyRevision: 1, Status: "healthy", Reason: "within_budget",
		AvailabilityTarget: 0.999, EvaluationJSON: `{}`, LeaseFencingToken: 1,
		FirstObservedAtMs: databaseNowMs - 10_000, EvaluatedAtMs: databaseNowMs - 10_000,
		CreatedTime: databaseNowMs / 1_000,
	}
	newHistory := oldHistory
	newHistory.EvaluatedAtMs = databaseNowMs
	newHistory.FirstObservedAtMs = databaseNowMs
	historyRows := []RoutingErrorBudgetHistory{oldHistory, newHistory}
	require.NoError(t, db.Create(&historyRows).Error)
	publisherLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-publisher", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), publisherLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: historyRows[1].ID,
		LeaseName: publisherLease.LeaseName, LeaseFencingToken: publisherLease.FencingToken,
	})
	require.NoError(t, err)
	deleted, err := DeleteRoutingErrorBudgetHistoryBeforeContext(context.Background(), databaseNowMs-5_000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var retained int64
	require.NoError(t, db.Model(&RoutingErrorBudgetHistory{}).Where("pool_id = ?", 99).Count(&retained).Error)
	assert.Equal(t, int64(1), retained)
}

func TestMigrateRoutingErrorBudgetModelsReplacesLegacyPoolOnlyUniqueIndex(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&routingErrorBudgetLegacyState{}))
	require.NoError(t, db.Create(&routingErrorBudgetLegacyState{PoolID: 7, PolicyRevision: 1}).Error)

	err := MigrateRoutingErrorBudgetModels(db)
	assert.ErrorIs(t, err, ErrRoutingErrorBudgetAlphaDrainRequired)
	assert.True(t, db.Migrator().HasIndex(&RoutingErrorBudgetState{}, routingErrorBudgetLegacyPoolIndex))
	assert.False(t, db.Migrator().HasTable(&RoutingErrorBudgetHistory{}), "a closed drain gate must not expand or contract the legacy schema")

	require.NoError(t, MigrateRoutingErrorBudgetModelsWithOptions(db, RoutingErrorBudgetMigrationOptions{
		AlphaV2Drained: true,
	}))
	ready, err := RoutingErrorBudgetSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)
	require.NoError(t, WaitRoutingErrorBudgetSchemaReady(context.Background(), db, 0))
	require.NoError(t, db.Create(&RoutingErrorBudgetState{
		PoolID: 7, PolicyRevision: 2, AvailabilityTarget: 0.999,
		Status: "healthy", Reason: "within_budget", EvaluationJSON: `{}`,
		LeaseFencingToken: 1, FirstObservedAtMs: 1, LastEvaluatedAtMs: 1,
		LastChangedAtMs: 1, CreatedTime: 1, UpdatedTime: 1,
	}).Error)
}

func TestMigrateRoutingErrorBudgetModelsConcurrentSQLite(t *testing.T) {
	dsn := t.TempDir() + "/routing-error-budget.db"
	first, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	second, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	for _, db := range []*gorm.DB{first, second} {
		sqlDB, openErr := db.DB()
		require.NoError(t, openErr)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.Exec("PRAGMA busy_timeout = 5000").Error)
	}

	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for _, db := range []*gorm.DB{first, second} {
		workers.Add(1)
		go func(candidate *gorm.DB) {
			defer workers.Done()
			<-start
			errors <- MigrateRoutingErrorBudgetModels(candidate)
		}(db)
	}
	close(start)
	workers.Wait()
	close(errors)
	for migrationErr := range errors {
		require.NoError(t, migrationErr)
	}
	ready, err := RoutingErrorBudgetSchemaReady(first)
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestRoutingErrorBudgetStateRollsBackWhenHistoryCannotPersist(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}, &RoutingErrorBudgetState{}))
	databaseNowMs, err := routingErrorBudgetDatabaseNowMs(db)
	require.NoError(t, err)
	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-evaluator", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	state := routingErrorBudgetStateForTest(lease, 1, "healthy", "within_budget", databaseNowMs)

	transition, err := UpsertRoutingErrorBudgetStateWithTransitionContext(context.Background(), lease, &state)
	require.Error(t, err)
	assert.Nil(t, transition)
	var states int64
	require.NoError(t, db.Model(&RoutingErrorBudgetState{}).Where("pool_id = ?", 7).Count(&states).Error)
	assert.Zero(t, states, "latest state and immutable history must commit atomically")
}

type routingErrorBudgetLegacyState struct {
	ID                 int64   `gorm:"primaryKey"`
	PoolID             int     `gorm:"uniqueIndex;not null"`
	PolicyRevision     int64   `gorm:"bigint;index;not null"`
	AvailabilityTarget float64 `gorm:"not null"`
	Status             string  `gorm:"type:varchar(32);index;not null"`
	Reason             string  `gorm:"type:varchar(64);index;not null"`
	EvaluationJSON     string  `gorm:"type:text;not null"`
	LeaseFencingToken  int64   `gorm:"bigint;not null"`
	FirstObservedAtMs  int64   `gorm:"bigint;not null"`
	LastEvaluatedAtMs  int64   `gorm:"bigint;index;not null"`
	LastChangedAtMs    int64   `gorm:"bigint;index;not null"`
	CreatedTime        int64   `gorm:"bigint;not null"`
	UpdatedTime        int64   `gorm:"bigint;index;not null"`
}

func (routingErrorBudgetLegacyState) TableName() string {
	return "routing_error_budget_states"
}

func routingErrorBudgetStateForTest(
	lease RoutingControlLease,
	revision int64,
	status string,
	reason string,
	evaluatedAtMs int64,
) RoutingErrorBudgetState {
	return RoutingErrorBudgetState{
		PoolID: 7, PolicyRevision: revision, AvailabilityTarget: 0.999,
		Status: status, Reason: reason, EvaluationJSON: `{"pool_id":7}`,
		LeaseFencingToken: lease.FencingToken,
		FirstObservedAtMs: evaluatedAtMs, LastEvaluatedAtMs: evaluatedAtMs,
		LastChangedAtMs: evaluatedAtMs, CreatedTime: 1, UpdatedTime: 1,
	}
}
