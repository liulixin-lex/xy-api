package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFlushStableTelemetryPersistsAllBatchesAndRequeuesOnlyFailedTail(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)

	snapshots := make([]routingmetrics.StableSnapshot, model.RoutingMetricRollupMaxBatch+1)
	for index := range snapshots {
		snapshots[index] = routingmetrics.StableSnapshot{
			PoolID: 1, PoolMemberID: index + 1, CredentialID: index + 1, ChannelID: index + 1,
			Model: fmt.Sprintf("model-%03d", index), BucketTs: 60, LastSnapshotRevision: 1,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		}
	}
	routingmetrics.RequeueStableSnapshots(snapshots)

	var createCalls int
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("routing_rollup_fail_second_batch", func(tx *gorm.DB) {
		createCalls++
		if createCalls == 2 {
			tx.AddError(errors.New("second batch failed"))
		}
	}))

	flushed, err := FlushStableTelemetryContext(context.Background())
	require.ErrorContains(t, err, "second batch failed")
	assert.Equal(t, model.RoutingMetricRollupMaxBatch, flushed)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingMetricRollupMaxBatch), persisted)
	remaining := routingmetrics.StableSnapshots()
	require.Len(t, remaining, 1)
	assert.Equal(t, model.RoutingMetricRollupMaxBatch+1, remaining[0].PoolMemberID)

	require.NoError(t, db.Callback().Create().Remove("routing_rollup_fail_second_batch"))
	flushed, err = FlushStableTelemetryContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	assert.Empty(t, routingmetrics.StableSnapshots())
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingMetricRollupMaxBatch+1), persisted)
}

func TestFlushStableTelemetryDatabaseFailureKeepsLiveDelta(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 0, ChannelID: 1,
		Model: "keyless", BucketTs: 60, LastSnapshotRevision: 1, RequestCount: 1, SuccessCount: 1,
	}})

	flushed, err := FlushStableTelemetryContext(context.Background())
	require.Error(t, err)
	assert.Zero(t, flushed)
	require.Len(t, routingmetrics.StableSnapshots(), 1)
}

func TestDeleteExpiredRoutingHistoryCleansRollupsAndAudits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingDecisionAudit{}))
	withSnapshotTestDB(t, db)

	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{{
		MemberID: 1, CredentialID: 0, ModelName: "old", BucketTs: 1,
		ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1,
	}}))
	require.NoError(t, db.Create(&model.RoutingDecisionAudit{DecisionID: "old", CreatedTime: 1}).Error)

	deleted, err := DeleteExpiredRoutingHistoryContext(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
}

func enableStableTelemetryTest(t *testing.T) {
	t.Helper()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
}
