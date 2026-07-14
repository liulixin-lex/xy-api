package controller

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBillingLogAuditPayloadAdvancesOnlyAfterSuccessfulRun(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-log-audit.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	succeeded, err := model.CreateSystemTask(
		model.SystemTaskTypeBillingLogAudit,
		billingLogAuditPayload{InsertedAfter: 1_700_000_000},
		nil,
	)
	require.NoError(t, err)
	succeededAt := int64(1_700_001_000)
	succeededResult, err := common.Marshal(billingLogAuditResult{
		NextStatsAfterID:    41,
		NextLogsAfterID:     52,
		NextAdminOpsAfterID: 63,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.SystemTask{}).Where("id = ?", succeeded.ID).Updates(map[string]any{
		"status":     model.SystemTaskStatusSucceeded,
		"updated_at": succeededAt,
		"active_key": nil,
		"result":     string(succeededResult),
	}).Error)

	payload, ok := (billingLogAuditHandler{}).NewPayload().(billingLogAuditPayload)
	require.True(t, ok)
	assert.Equal(t, succeededAt-int64(billingLogConflictAuditOverlap/time.Second), payload.InsertedAfter)
	assert.Equal(t, int64(41), payload.StatsAfterID)
	assert.Equal(t, int64(52), payload.LogsAfterID)
	assert.Equal(t, int64(63), payload.AdminOpsAfterID)

	continuedResult, err := common.Marshal(billingLogAuditResult{
		InsertedAfter: 1_700_000_100, AuditThrough: 1_700_002_000,
		ConflictBudgetExhausted: true, NextConflictAfterKey: "task:cursor:terminal:v1",
		NextStatsAfterID: 43, NextLogsAfterID: 54, NextAdminOpsAfterID: 65,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.SystemTask{}).Where("id = ?", succeeded.ID).
		Update("result", string(continuedResult)).Error)
	payload, ok = (billingLogAuditHandler{}).NewPayload().(billingLogAuditPayload)
	require.True(t, ok)
	assert.Equal(t, int64(1_700_000_100), payload.InsertedAfter)
	assert.Equal(t, int64(1_700_002_000), payload.AuditThrough)
	assert.Equal(t, "task:cursor:terminal:v1", payload.ConflictAfterKey)
	assert.Equal(t, int64(43), payload.StatsAfterID)
	assert.Equal(t, int64(54), payload.LogsAfterID)
	assert.Equal(t, int64(65), payload.AdminOpsAfterID)

	failed, err := model.CreateSystemTask(
		model.SystemTaskTypeBillingLogAudit,
		billingLogAuditPayload{
			InsertedAfter: 1_699_999_000, StatsAfterID: 61, LogsAfterID: 72, AdminOpsAfterID: 83,
		},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.SystemTask{}).Where("id = ?", failed.ID).Updates(map[string]any{
		"status":     model.SystemTaskStatusFailed,
		"updated_at": succeededAt + 100,
		"active_key": nil,
	}).Error)

	payload, ok = (billingLogAuditHandler{}).NewPayload().(billingLogAuditPayload)
	require.True(t, ok)
	assert.Equal(t, int64(1_699_999_000), payload.InsertedAfter)
	assert.Equal(t, int64(61), payload.StatsAfterID)
	assert.Equal(t, int64(72), payload.LogsAfterID)
	assert.Equal(t, int64(83), payload.AdminOpsAfterID)
}

func TestDrainExpiredBillingProjectionPagesCrossesPagesAndResetsAtTail(t *testing.T) {
	startedAt := time.Unix(1_800_900_000, 0)
	statsCalls := 0
	logsCalls := 0
	cleanup, err := drainExpiredBillingProjectionPages(
		context.Background(), startedAt, 11, 22,
		billingProjectionCleanupDeps{
			now: func() time.Time { return startedAt },
			statsPage: func(_ context.Context, now time.Time, afterID int64, limit int) (int64, int64, bool, error) {
				assert.Equal(t, startedAt, now)
				assert.Equal(t, billingProjectionCleanupPageSize, limit)
				statsCalls++
				if statsCalls == 1 {
					assert.Equal(t, int64(11), afterID)
					return 400, 511, true, nil
				}
				assert.Equal(t, int64(511), afterID)
				return 25, 0, false, nil
			},
			logsPage: func(_ context.Context, now time.Time, afterID int64, limit int) (int64, int64, bool, error) {
				assert.Equal(t, startedAt, now)
				assert.Equal(t, billingProjectionCleanupPageSize, limit)
				logsCalls++
				if logsCalls == 1 {
					assert.Equal(t, int64(22), afterID)
					return 390, 522, true, nil
				}
				assert.Equal(t, int64(522), afterID)
				return 35, 0, false, nil
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(425), cleanup.StatsDeleted)
	assert.Equal(t, int64(425), cleanup.LogsDeleted)
	assert.Equal(t, 2, cleanup.StatsPages)
	assert.Equal(t, 2, cleanup.LogsPages)
	assert.Zero(t, cleanup.NextStatsAfterID)
	assert.Zero(t, cleanup.NextLogsAfterID)
	assert.False(t, cleanup.BudgetExhausted)

	worstCaseCycle := billingLogAuditInterval + billingProjectionCleanupTimeBudget
	perTypeCandidatesPerSecond := billingProjectionCleanupPageSize *
		billingProjectionCleanupMaxPagesPerRun / int(worstCaseCycle/time.Second)
	assert.GreaterOrEqual(t, perTypeCandidatesPerSecond, 800)
}

func TestDrainExpiredBillingProjectionPagesPersistsCursorWhenBudgetExpires(t *testing.T) {
	startedAt := time.Unix(1_800_900_100, 0)
	nowCalls := 0
	logsCalled := false
	cleanup, err := drainExpiredBillingProjectionPages(
		context.Background(), startedAt, 31, 42,
		billingProjectionCleanupDeps{
			now: func() time.Time {
				nowCalls++
				if nowCalls == 1 {
					return startedAt
				}
				return startedAt.Add(billingProjectionCleanupTimeBudget)
			},
			statsPage: func(_ context.Context, _ time.Time, afterID int64, _ int) (int64, int64, bool, error) {
				assert.Equal(t, int64(31), afterID)
				return 10, 531, true, nil
			},
			logsPage: func(_ context.Context, _ time.Time, _ int64, _ int) (int64, int64, bool, error) {
				logsCalled = true
				return 0, 0, false, nil
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(10), cleanup.StatsDeleted)
	assert.Equal(t, 1, cleanup.StatsPages)
	assert.Zero(t, cleanup.LogsPages)
	assert.Equal(t, int64(531), cleanup.NextStatsAfterID)
	assert.Equal(t, int64(42), cleanup.NextLogsAfterID)
	assert.True(t, cleanup.BudgetExhausted)
	assert.False(t, logsCalled)
}
