package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingErrorBudgetCursorIsLeaseFencedAndMonotonic(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingControlLease{}, &RoutingErrorBudgetCursor{}))

	publisherLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-publisher", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	cursor, err := SetRoutingErrorBudgetCursorContext(context.Background(), publisherLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: 10,
		LeaseName: publisherLease.LeaseName, LeaseFencingToken: publisherLease.FencingToken,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(10), cursor.PositionID)

	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), publisherLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: 9,
		LeaseName: publisherLease.LeaseName, LeaseFencingToken: publisherLease.FencingToken,
	})
	assert.ErrorIs(t, err, ErrRoutingErrorBudgetCursorInvalid)

	require.NoError(t, db.Model(&RoutingControlLease{}).
		Where("lease_name = ?", publisherLease.LeaseName).Update("lease_until_ms", 0).Error)
	newPublisherLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-publisher", "node-b", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), publisherLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: 11,
		LeaseName: publisherLease.LeaseName, LeaseFencingToken: publisherLease.FencingToken,
	})
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost)
	cursor, err = SetRoutingErrorBudgetCursorContext(context.Background(), newPublisherLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: 11,
		LeaseName: newPublisherLease.LeaseName, LeaseFencingToken: newPublisherLease.FencingToken,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(11), cursor.PositionID)

	evaluatorLease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-evaluator", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	cursor, err = SetRoutingErrorBudgetCursorContext(context.Background(), evaluatorLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetEvaluatorCursor, PolicyRevision: 7, PositionID: 20,
		LeaseName: evaluatorLease.LeaseName, LeaseFencingToken: evaluatorLease.FencingToken,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7), cursor.PolicyRevision)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), evaluatorLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetEvaluatorCursor, PolicyRevision: 7, PositionID: 19,
		LeaseName: evaluatorLease.LeaseName, LeaseFencingToken: evaluatorLease.FencingToken,
	})
	assert.ErrorIs(t, err, ErrRoutingErrorBudgetCursorInvalid)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), evaluatorLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetEvaluatorCursor, PolicyRevision: 0, PositionID: 0,
		LeaseName: evaluatorLease.LeaseName, LeaseFencingToken: evaluatorLease.FencingToken,
	})
	require.NoError(t, err)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), evaluatorLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetEvaluatorCursor, PolicyRevision: 8, PositionID: 1,
		LeaseName: evaluatorLease.LeaseName, LeaseFencingToken: evaluatorLease.FencingToken,
	})
	require.NoError(t, err)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), evaluatorLease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetEvaluatorCursor, PolicyRevision: 7, PositionID: 2,
		LeaseName: evaluatorLease.LeaseName, LeaseFencingToken: evaluatorLease.FencingToken,
	})
	assert.ErrorIs(t, err, ErrRoutingErrorBudgetCursorInvalid)
}

func TestRoutingErrorBudgetRetentionNeverDeletesUnpublishedHistory(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingControlLease{}, &RoutingErrorBudgetCursor{}, &RoutingErrorBudgetHistory{},
	))
	nowMs, err := RoutingDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	rows := []RoutingErrorBudgetHistory{
		{PoolID: 1, PolicyRevision: 1, Status: "healthy", Reason: "within_budget", AvailabilityTarget: 0.999,
			EvaluationJSON: `{}`, LeaseFencingToken: 1, FirstObservedAtMs: nowMs - 10_000,
			EvaluatedAtMs: nowMs - 10_000, CreatedTime: nowMs / 1_000},
		{PoolID: 2, PolicyRevision: 1, Status: "critical", Reason: "fast_multi_window_burn", AvailabilityTarget: 0.999,
			EvaluationJSON: `{}`, LeaseFencingToken: 1, FirstObservedAtMs: nowMs - 9_000,
			EvaluatedAtMs: nowMs - 9_000, CreatedTime: nowMs / 1_000},
	}
	require.NoError(t, db.Create(&rows).Error)

	deleted, err := DeleteRoutingErrorBudgetHistoryBeforeContext(context.Background(), nowMs)
	require.NoError(t, err)
	assert.Zero(t, deleted)

	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-error-budget-publisher", "node-a", 60_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = SetRoutingErrorBudgetCursorContext(context.Background(), lease, RoutingErrorBudgetCursor{
		CursorName: RoutingErrorBudgetPublisherCursor, PositionID: rows[0].ID,
		LeaseName: lease.LeaseName, LeaseFencingToken: lease.FencingToken,
	})
	require.NoError(t, err)

	deleted, err = DeleteRoutingErrorBudgetHistoryBeforeContext(context.Background(), nowMs)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var retained []RoutingErrorBudgetHistory
	require.NoError(t, db.Order("id ASC").Find(&retained).Error)
	require.Len(t, retained, 1)
	assert.Equal(t, rows[1].ID, retained[0].ID)
}
