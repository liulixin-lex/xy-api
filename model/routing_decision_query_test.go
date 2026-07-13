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

func TestRoutingDecisionSummaryQueryFiltersTimeAndOmitsLargePayloads(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingDecisionAudit{}))

	largePayload := strings.Repeat("candidate-detail", 2_000)
	rows := []RoutingDecisionAudit{
		{
			DecisionID: "decision-1", RequestID: "request-a", RequestKey: RoutingDecisionRequestKey("request-a"),
			PoolID: 1, GroupName: "vip", GroupKey: RoutingDecisionGroupKey("vip"),
			ModelName: "model-a", ModelKey: RoutingDecisionModelKey("model-a"),
			ObservedMatchesActual: true, CandidatesJSON: largePayload, CreatedTime: 100,
		},
		{
			DecisionID: "decision-2", RequestID: "request-b", RequestKey: RoutingDecisionRequestKey("request-b"),
			PoolID: 1, GroupName: "vip", GroupKey: RoutingDecisionGroupKey("vip"),
			ModelName: "model-a", ModelKey: RoutingDecisionModelKey("model-a"),
			ObservedMatchesActual: false, CandidatesJSON: largePayload, CandidateCount: 80,
			EligibleCount: 70, ActualExpectedCost: 1.5, ObservedExpectedCost: 1.25,
			ExpectedCostDelta: -0.25, Replayable: true, CreatedTime: 200,
		},
		{
			DecisionID: "decision-3", RequestID: "request-c", RequestKey: RoutingDecisionRequestKey("request-c"),
			PoolID: 2, GroupName: "other", GroupKey: RoutingDecisionGroupKey("other"),
			ModelName: "model-b", ModelKey: RoutingDecisionModelKey("model-b"),
			ObservedMatchesActual: false, CandidatesJSON: largePayload, CreatedTime: 300,
		},
	}
	require.NoError(t, db.Create(&rows).Error)

	matched := false
	replayable := true
	items, hasMore, err := ListRoutingDecisionAuditSummariesContext(context.Background(), RoutingDecisionAuditSummaryFilter{
		Limit: 10, GroupKey: RoutingDecisionGroupKey("vip"), ModelKey: RoutingDecisionModelKey("model-a"),
		ObservedMatchesActual: &matched, Replayable: &replayable, FromTime: 150, ToTime: 250,
	})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, items, 1)
	assert.Equal(t, "decision-2", items[0].DecisionID)
	assert.Equal(t, 80, items[0].CandidateCount)
	assert.Equal(t, -0.25, items[0].ExpectedCostDelta)

	encoded, err := common.Marshal(items)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "candidate-detail")

	items, hasMore, err = ListRoutingDecisionAuditSummariesContext(context.Background(), RoutingDecisionAuditSummaryFilter{
		Limit: 1,
	})
	require.NoError(t, err)
	assert.True(t, hasMore)
	require.Len(t, items, 1)
	assert.Equal(t, "decision-3", items[0].DecisionID)
}

func TestLatestRoutingDecisionReplayProfilesChooseNewestPerModelAndStream(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "sqlite", dbType: common.DatabaseTypeSQLite},
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *gorm.DB
			if test.dbType == common.DatabaseTypeSQLite {
				db = openRoutingSQLiteTestDB(t)
			} else {
				dsn := os.Getenv(test.envKey)
				if dsn == "" {
					t.Skipf("%s is not set", test.envKey)
				}
				db = openRoutingExternalTestDB(t, test.dbType, dsn)
				if db.Migrator().HasTable(&RoutingDecisionAudit{}) {
					t.Skip("refusing to run against external database because routing decision audits already exists")
				}
				t.Cleanup(func() { _ = db.Migrator().DropTable(&RoutingDecisionAudit{}) })
			}
			withRoutingTestDB(t, db, test.dbType)
			runLatestRoutingDecisionReplayProfilesContract(t, db)
		})
	}
}

func runLatestRoutingDecisionReplayProfilesContract(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&RoutingDecisionAudit{}))
	rows := []RoutingDecisionAudit{
		{DecisionID: "model-a-old", PoolID: 9, ModelName: "Model-A", ModelKey: RoutingDecisionModelKey("Model-A"), Replayable: true, CreatedTime: 100},
		{DecisionID: "model-a-new", PoolID: 9, ModelName: "Model-A", ModelKey: RoutingDecisionModelKey("Model-A"), Replayable: true, CreatedTime: 200},
		{DecisionID: "model-a-stream", PoolID: 9, ModelName: "Model-A", ModelKey: RoutingDecisionModelKey("Model-A"), IsStream: true, Replayable: true, CreatedTime: 300},
		{DecisionID: "model-b-not-replayable", PoolID: 9, ModelName: "Model-B", ModelKey: RoutingDecisionModelKey("Model-B"), CreatedTime: 400},
		{DecisionID: "model-c", PoolID: 9, ModelName: "Model-C", ModelKey: RoutingDecisionModelKey("Model-C"), Replayable: true, CreatedTime: 500},
		{DecisionID: "other-pool", PoolID: 10, ModelName: "Model-D", ModelKey: RoutingDecisionModelKey("Model-D"), Replayable: true, CreatedTime: 600},
	}
	require.NoError(t, db.Create(&rows).Error)

	items, err := ListLatestRoutingDecisionReplayProfilesContext(context.Background(), 9, 10)
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, "model-c", items[0].DecisionID)
	assert.Equal(t, "model-a-stream", items[1].DecisionID)
	assert.Equal(t, "model-a-new", items[2].DecisionID)
}

func TestRoutingDecisionSummaryQueryRejectsInvalidTimeWindow(t *testing.T) {
	_, _, err := ListRoutingDecisionAuditSummariesContext(context.Background(), RoutingDecisionAuditSummaryFilter{
		Limit: 10, FromTime: 200, ToTime: 100,
	})
	assert.ErrorIs(t, err, ErrRoutingDecisionQueryInvalid)
}
