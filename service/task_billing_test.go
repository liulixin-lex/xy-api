package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)

	model.DB = db
	model.LOG_DB = db

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true

	if err := db.AutoMigrate(
		&model.Task{},
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Channel{},
		&model.TopUp{},
		&model.UserSubscription{},
		&model.BillingReservation{},
		&model.QuotaLedgerEntry{},
		&model.SubscriptionPreConsumeRecord{},
		&model.SystemTask{},
		&model.SystemTaskLock{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Seed helpers
// ---------------------------------------------------------------------------

func truncate(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM tasks")
		model.DB.Exec("DELETE FROM users")
		model.DB.Exec("DELETE FROM tokens")
		model.DB.Exec("DELETE FROM logs")
		model.DB.Exec("DELETE FROM channels")
		model.DB.Exec("DELETE FROM top_ups")
		model.DB.Exec("DELETE FROM user_subscriptions")
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM quota_ledger_entries")
		model.DB.Exec("DELETE FROM billing_reservations")
		model.DB.Exec("DELETE FROM system_task_locks")
		model.DB.Exec("DELETE FROM system_tasks")
	})
}

func seedUser(t *testing.T, id int, quota int) {
	t.Helper()
	user := &model.User{Id: id, Username: "test_user", Quota: quota, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
}

func seedToken(t *testing.T, id int, userId int, key string, remainQuota int) {
	t.Helper()
	token := &model.Token{
		Id:          id,
		UserId:      userId,
		Key:         key,
		Name:        "test_token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: remainQuota,
		UsedQuota:   0,
	}
	require.NoError(t, model.DB.Create(token).Error)
}

func seedSubscription(t *testing.T, id int, userId int, amountTotal int64, amountUsed int64) {
	t.Helper()
	sub := &model.UserSubscription{
		Id:          id,
		UserId:      userId,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(sub).Error)
}

func seedChannel(t *testing.T, id int) {
	t.Helper()
	ch := &model.Channel{Id: id, Name: "test_channel", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(ch).Error)
}

func makeTask(userId, channelId, quota, tokenId int, billingSource string, subscriptionId int) *model.Task {
	return &model.Task{
		TaskID:    "task_" + time.Now().Format("150405.000"),
		UserId:    userId,
		ChannelId: channelId,
		Quota:     quota,
		Status:    model.TaskStatus(model.TaskStatusInProgress),
		Group:     "default",
		Data:      json.RawMessage(`{}`),
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Properties: model.Properties{
			OriginModelName: "test-model",
		},
		PrivateData: model.TaskPrivateData{
			BillingSource:  billingSource,
			SubscriptionId: subscriptionId,
			TokenId:        tokenId,
			BillingContext: &model.TaskBillingContext{
				ModelPrice:      0.02,
				GroupRatio:      1.0,
				OriginModelName: "test-model",
			},
		},
	}
}

func persistTaskForBillingTest(t *testing.T, task *model.Task, status model.TaskStatus) {
	t.Helper()
	task.Status = status
	require.NoError(t, model.DB.Create(task).Error)
	t.Cleanup(func() {
		model.DB.Delete(&model.Task{}, task.ID)
	})
}

func TestPriceDataOtherRatiosFilterAndSnapshot(t *testing.T) {
	priceData := types.PriceData{}

	priceData.AddOtherRatio("zero", 0)
	priceData.AddOtherRatio("negative", -0.5)
	priceData.AddOtherRatio("nan", math.NaN())
	priceData.AddOtherRatio("inf", math.Inf(1))
	priceData.AddOtherRatio("one", 1)
	priceData.AddOtherRatio("positive", 2.5)

	ratios := priceData.OtherRatios()
	require.Len(t, ratios, 2)
	assert.Equal(t, 1.0, ratios["one"])
	assert.Equal(t, 2.5, ratios["positive"])
	assert.True(t, priceData.HasOtherRatio("one"))
	assert.False(t, priceData.HasOtherRatio("zero"))

	ratios["positive"] = 99
	ratios["new"] = 3
	nextSnapshot := priceData.OtherRatios()
	assert.Equal(t, 2.5, nextSnapshot["positive"])
	assert.NotContains(t, nextSnapshot, "new")
}

func TestPriceDataReplaceAndApplyOtherRatios(t *testing.T) {
	priceData := types.PriceData{}

	replaced := priceData.ReplaceOtherRatios(map[string]float64{
		"zero":     0,
		"negative": -3,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
		"one":      1,
		"duration": 2,
		"size":     1.5,
	})

	require.True(t, replaced)
	assert.Equal(t, 3.0, priceData.OtherRatioMultiplier())
	assert.Equal(t, 30.0, priceData.ApplyOtherRatiosToFloat(10))
	assert.Equal(t, 10.0, priceData.RemoveOtherRatiosFromFloat(30))
	assert.True(t, decimal.NewFromInt(30).Equal(priceData.ApplyOtherRatiosToDecimal(decimal.NewFromInt(10))))

	replaced = priceData.ReplaceOtherRatios(map[string]float64{
		"zero": 0,
		"nan":  math.NaN(),
	})

	require.False(t, replaced)
	assert.Nil(t, priceData.OtherRatios())
	assert.Equal(t, 1.0, priceData.OtherRatioMultiplier())
}

func TestTaskBillingOtherFiltersHistoricalOtherRatios(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.OtherRatios = map[string]float64{
		"seconds":  2,
		"identity": 1,
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
	}

	other := taskBillingOther(task)

	assert.Equal(t, 2.0, other["seconds"])
	assert.Equal(t, 1.0, other["identity"])
	assert.NotContains(t, other, "zero")
	assert.NotContains(t, other, "negative")
	assert.NotContains(t, other, "nan")
	assert.NotContains(t, other, "inf")
}

func TestTaskBillingContextPriceDataFiltersMultiplier(t *testing.T) {
	priceData := taskBillingContextPriceData(&model.TaskBillingContext{
		OtherRatios: map[string]float64{
			"seconds":  2,
			"size":     3,
			"identity": 1,
			"zero":     0,
			"negative": -1,
			"nan":      math.NaN(),
			"inf":      math.Inf(1),
		},
	})

	require.NotNil(t, priceData)
	assert.Equal(t, 6.0, priceData.OtherRatioMultiplier())
	assert.Equal(t, map[string]float64{
		"seconds":  2,
		"size":     3,
		"identity": 1,
	}, priceData.OtherRatios())
}

func TestTaskTokenQuotaUsesImmutableSubmissionSnapshot(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.ModelRatio = 2
	task.PrivateData.BillingContext.GroupRatio = 3
	task.PrivateData.BillingContext.OtherRatios = map[string]float64{"duration": 4}

	quota, clamp, ok := taskTokenQuotaFromSnapshot(task, 10)
	require.True(t, ok)
	assert.Nil(t, clamp)
	assert.Equal(t, 240, quota)

	task.PrivateData.BillingContext.ModelRatio = math.NaN()
	_, _, ok = taskTokenQuotaFromSnapshot(task, 10)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// Read-back helpers
// ---------------------------------------------------------------------------

func getUserQuota(t *testing.T, id int) int {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&user).Error)
	return user.Quota
}

func getTokenRemainQuota(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("remain_quota").Where("id = ?", id).First(&token).Error)
	return token.RemainQuota
}

func getTokenUsedQuota(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&token).Error)
	return token.UsedQuota
}

func getSubscriptionUsed(t *testing.T, id int) int64 {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.Select("amount_used").Where("id = ?", id).First(&sub).Error)
	return sub.AmountUsed
}

func getLastLog(t *testing.T) *model.Log {
	t.Helper()
	var log model.Log
	err := model.LOG_DB.Order("id desc").First(&log).Error
	if err != nil {
		return nil
	}
	return &log
}

func countLogs(t *testing.T) int64 {
	t.Helper()
	var count int64
	model.LOG_DB.Model(&model.Log{}).Count(&count)
	return count
}

// ===========================================================================
// RefundTaskQuota tests
// ===========================================================================

func TestRefundTaskQuota_Wallet(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 1, 1, 1
	const initQuota, preConsumed = 10000, 3000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-test-key", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	persistTaskForBillingTest(t, task, model.TaskStatusFailure)

	RefundTaskQuota(ctx, task, "task failed: upstream error")
	RefundTaskQuota(ctx, task, "duplicate task failure")

	// User quota should increase by preConsumed
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))

	// Token remain_quota should increase, used_quota should decrease
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, -preConsumed, getTokenUsedQuota(t, tokenID))

	// A refund log should be created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, preConsumed, log.Quota)
	assert.Equal(t, "test-model", log.ModelName)
	assert.EqualValues(t, 1, countLogs(t))
}

func TestRefundTaskQuota_Subscription(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 2, 2, 2, 1
	const preConsumed = 2000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-key", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
	persistTaskForBillingTest(t, task, model.TaskStatusFailure)

	RefundTaskQuota(ctx, task, "subscription task failed")

	// Subscription used should decrease by preConsumed
	assert.Equal(t, subUsed-int64(preConsumed), getSubscriptionUsed(t, subID))

	// Token should also be refunded
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

func TestRefundTaskQuota_ZeroQuota(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 3
	seedUser(t, userID, 5000)

	task := makeTask(userID, 0, 0, 0, BillingSourceWallet, 0)

	RefundTaskQuota(ctx, task, "zero quota task")

	// No change to user quota
	assert.Equal(t, 5000, getUserQuota(t, userID))

	// No log created
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRefundTaskQuota_NoToken(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 4, 4
	const initQuota, preConsumed = 10000, 1500

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0) // TokenId=0
	persistTaskForBillingTest(t, task, model.TaskStatusFailure)

	RefundTaskQuota(ctx, task, "no token task failed")

	// User quota refunded
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))

	// Log created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

// ===========================================================================
// RecalculateTaskQuota tests
// ===========================================================================

func TestRecalculate_PositiveDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 10, 10, 10
	const initQuota, preConsumed = 10000, 2000
	const actualQuota = 3000 // under-charged by 1000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-pos", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")
	RecalculateTaskQuota(ctx, task, actualQuota, "duplicate adaptor adjustment")

	// User quota should decrease by the delta (1000 additional charge)
	assert.Equal(t, initQuota-(actualQuota-preConsumed), getUserQuota(t, userID))

	// Token should also be charged the delta
	assert.Equal(t, tokenRemain-(actualQuota-preConsumed), getTokenRemainQuota(t, tokenID))

	// task.Quota should be updated to actualQuota
	assert.Equal(t, actualQuota, task.Quota)

	// Log type should be Consume (additional charge)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeConsume, log.Type)
	assert.Equal(t, actualQuota-preConsumed, log.Quota)
	assert.EqualValues(t, 1, countLogs(t))
}

func TestRecalculate_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 11, 11, 11
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged by 2000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-neg", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")

	// User quota should increase by abs(delta) = 2000 (refund overpayment)
	assert.Equal(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))

	// Token should be refunded the difference
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	// task.Quota updated
	assert.Equal(t, actualQuota, task.Quota)

	// Log type should be Refund
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, preConsumed-actualQuota, log.Quota)
}

func TestRecalculate_ZeroDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 12
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	RecalculateTaskQuota(ctx, task, preConsumed, "exact match")

	// No change to user quota
	assert.Equal(t, initQuota, getUserQuota(t, userID))

	// No log created (delta is zero)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRecalculate_ActualQuotaZero(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 13
	const initQuota = 10000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, 5000, 0, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, 0, "zero actual")

	// No change (early return)
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRecalculate_Subscription_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 14, 14, 14, 2
	const preConsumed = 5000
	const actualQuota = 2000 // over-charged by 3000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-recalc", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	RecalculateTaskQuota(ctx, task, actualQuota, "subscription over-charge")

	// Subscription used should decrease by delta (refund 3000)
	assert.Equal(t, subUsed-int64(preConsumed-actualQuota), getSubscriptionUsed(t, subID))

	// Token refunded
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	assert.Equal(t, actualQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

// ===========================================================================
// CAS + Billing integration tests
// Simulates the flow in updateVideoSingleTask (service/task_polling.go)
// ===========================================================================

// simulatePollBilling reproduces the CAS + billing logic from updateVideoSingleTask.
// It takes a persisted task (already in DB), applies the new status, and performs
// the conditional update + billing exactly as the polling loop does.
func simulatePollBilling(ctx context.Context, task *model.Task, newStatus model.TaskStatus, actualQuota int) {
	snap := task.Snapshot()

	shouldRefund := false
	shouldSettle := false
	quota := task.Quota

	task.Status = newStatus
	switch string(newStatus) {
	case model.TaskStatusSuccess:
		task.Progress = "100%"
		task.FinishTime = 9999
		shouldSettle = true
	case model.TaskStatusFailure:
		task.Progress = "100%"
		task.FinishTime = 9999
		task.FailReason = "upstream error"
		if quota != 0 {
			shouldRefund = true
		}
	default:
		task.Progress = "50%"
	}

	isDone := task.Status == model.TaskStatus(model.TaskStatusSuccess) || task.Status == model.TaskStatus(model.TaskStatusFailure)
	if isDone && snap.Status != task.Status {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			shouldRefund = false
			shouldSettle = false
		} else if !won {
			shouldRefund = false
			shouldSettle = false
		}
	} else if !snap.Equal(task.Snapshot()) {
		_, _ = task.UpdateWithStatus(snap.Status)
	}

	if shouldSettle && actualQuota > 0 {
		RecalculateTaskQuota(ctx, task, actualQuota, "test settle")
	}
	if shouldRefund {
		RefundTaskQuota(ctx, task, task.FailReason)
	}
}

func TestCASGuardedRefund_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 20, 20, 20
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-win", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS wins: task in DB should now be FAILURE
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)

	// Refund should have happened
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

func TestCASGuardedRefund_Lose(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 21, 21, 21
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-lose", tokenRemain)
	seedChannel(t, channelID)

	// Create task with IN_PROGRESS in DB
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate another process already transitioning to FAILURE
	model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusFailure)

	// Our process still has the old in-memory state (IN_PROGRESS) and tries to transition
	// task.Status is still IN_PROGRESS in the snapshot
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS lost: user quota should NOT change (no double refund)
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))

	// No billing log should be created
	assert.Equal(t, int64(0), countLogs(t))
}

func TestCASGuardedSettle_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 22, 22, 22
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged, should get partial refund
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-settle-win", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusSuccess), actualQuota)

	// CAS wins: task should be SUCCESS
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)

	// Settlement should refund the over-charge (5000 - 3000 = 2000 back to user)
	assert.Equal(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	// task.Quota should be updated to actualQuota
	assert.Equal(t, actualQuota, task.Quota)
}

func TestNonTerminalUpdate_NoBilling(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 23, 23
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	task.Progress = "20%"
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate a non-terminal poll update (still IN_PROGRESS, progress changed)
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusInProgress), 0)

	// User quota should NOT change
	assert.Equal(t, initQuota, getUserQuota(t, userID))

	// No billing log
	assert.Equal(t, int64(0), countLogs(t))

	// Task progress should be updated in DB
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, "50%", reloaded.Progress)
}

// ===========================================================================
// Mock adaptor for settleTaskBillingOnComplete tests
// ===========================================================================

type mockAdaptor struct {
	adjustReturn int
}

func (m *mockAdaptor) Init(_ *relaycommon.RelayInfo) {}
func (m *mockAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	return nil, nil
}
func (m *mockAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) { return nil, nil }
func (m *mockAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return m.adjustReturn
}

func TestPrepareTaskBillingOnCompletePersistsExplicitZeroAndRejectsRetarget(t *testing.T) {
	task := &model.Task{
		TaskID: "task_explicit_zero_intent",
		Quota:  100,
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ModelRatio: 0.1,
				GroupRatio: 0.1,
			},
		},
	}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 1}
	decision, err := PrepareTaskBillingOnComplete(&mockAdaptor{}, task, taskResult)
	require.NoError(t, err)
	assert.Zero(t, decision.ActualQuota)
	targetQuota, hasIntent := task.PrivateData.BillingTargetQuotaIntent()
	assert.True(t, hasIntent)
	assert.Zero(t, targetQuota)

	_, err = PrepareTaskBillingOnComplete(&mockAdaptor{}, task, taskResult)
	require.NoError(t, err)
	_, err = PrepareTaskBillingOnComplete(&mockAdaptor{adjustReturn: 1}, task, taskResult)
	assert.ErrorIs(t, err, model.ErrBillingReservationConflict)
}

func TestAsyncTaskWalletShortfallFreezesIdempotentlyAndReleasesAfterSettlement(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentDebt{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM payment_debts") })
	const userID, tokenID, channelID = 41, 41, 41
	seedUser(t, userID, 50)
	seedToken(t, tokenID, userID, "sk-task-wallet-shortfall", 50)
	seedChannel(t, channelID)
	_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "task-wallet-shortfall",
		UserId:        userID,
		TokenId:       tokenID,
		FundingSource: model.BillingFundingWallet,
		Quota:         40,
	}, "sk-task-wallet-shortfall")
	require.NoError(t, err)
	task := makeTask(userID, channelID, 40, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_wallet_shortfall"
	require.NoError(t, model.CreateTaskWithBillingReservation(task, "task-wallet-shortfall", 40, "sk-task-wallet-shortfall"))
	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(60))
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)

	RecalculateTaskQuota(context.Background(), task, 60, "wallet shortfall test")
	RecalculateTaskQuota(context.Background(), task, 60, "wallet shortfall retry")
	reservation, err := model.GetBillingReservation("task-wallet-shortfall")
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusReserved, reservation.Status)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, 60, reservation.SettlementTarget)
	assert.Equal(t, model.BillingSettlementFailureUserQuota, reservation.SettlementFailureCode)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)
	var shortfallLedgers int64
	require.NoError(t, model.DB.Model(&model.QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", reservation.RequestId, model.QuotaLedgerPhaseShortfall).
		Count(&shortfallLedgers).Error)
	assert.EqualValues(t, 1, shortfallLedgers)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("quota", 30).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("remain_quota", 30).Error)
	RecalculateTaskQuota(context.Background(), task, 60, "wallet shortfall recovered")
	reservation, err = model.GetBillingReservation("task-wallet-shortfall")
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusSettled, reservation.Status)
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.Equal(t, 10, getUserQuota(t, userID))
	assert.Equal(t, 10, getTokenRemainQuota(t, tokenID))
}

func TestAsyncTaskTokenShortfallRollsBackWalletAndFreezes(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentDebt{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM payment_debts") })
	const userID, tokenID, channelID = 42, 42, 42
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "sk-task-token-shortfall", 50)
	seedChannel(t, channelID)
	_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "task-token-shortfall",
		UserId:        userID,
		TokenId:       tokenID,
		FundingSource: model.BillingFundingWallet,
		Quota:         40,
	}, "sk-task-token-shortfall")
	require.NoError(t, err)
	task := makeTask(userID, channelID, 40, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_token_shortfall"
	require.NoError(t, model.CreateTaskWithBillingReservation(task, "task-token-shortfall", 40, "sk-task-token-shortfall"))
	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(60))
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)

	RecalculateTaskQuota(context.Background(), task, 60, "token shortfall test")
	reservation, err := model.GetBillingReservation("task-token-shortfall")
	require.NoError(t, err)
	assert.Equal(t, model.BillingSettlementFailureTokenQuota, reservation.SettlementFailureCode)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)
	assert.Equal(t, 60, getUserQuota(t, userID), "wallet debit must roll back when token settlement fails")
	assert.Equal(t, 10, getTokenRemainQuota(t, tokenID))
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)
}

func TestMidjourneySubscriptionShortfallFreezesWithoutDebitingWallet(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentDebt{}, &model.Midjourney{}))
	if !model.DB.Migrator().HasTable(&model.SubscriptionPlan{}) {
		require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}))
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM payment_debts")
		model.DB.Where("user_id = ?", 43).Delete(&model.Midjourney{})
		model.DB.Where("id = ?", 43).Delete(&model.UserSubscription{})
		model.DB.Where("id = ?", 43).Delete(&model.SubscriptionPlan{})
	})
	const userID, tokenID, channelID, planID, subscriptionID = 43, 43, 43, 43, 43
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "sk-mj-sub-shortfall", 100)
	seedChannel(t, channelID)
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id:               planID,
		Title:            "Async shortfall plan",
		PriceAmount:      10,
		Currency:         "USD",
		DurationUnit:     model.SubscriptionDurationMonth,
		DurationValue:    1,
		Enabled:          true,
		TotalAmount:      50,
		QuotaResetPeriod: model.SubscriptionResetNever,
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		Id:          subscriptionID,
		UserId:      userID,
		PlanId:      planID,
		AmountTotal: 50,
		StartTime:   time.Now().Add(-time.Hour).Unix(),
		EndTime:     time.Now().Add(time.Hour).Unix(),
		Status:      "active",
	}).Error)
	preConsumed, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "mj-subscription-shortfall",
		UserId:        userID,
		TokenId:       tokenID,
		FundingSource: model.BillingFundingSubscription,
		Quota:         40,
	}, "sk-mj-sub-shortfall")
	require.NoError(t, err)
	claimed, err := model.ClaimMidjourneyBillingReservation("mj-subscription-shortfall")
	require.NoError(t, err)
	require.True(t, claimed)
	task := &model.Midjourney{
		UserId:     userID,
		MjId:       "mj_subscription_shortfall",
		Action:     "IMAGINE",
		SubmitTime: time.Now().UnixMilli(),
		ChannelId:  channelID,
		Quota:      40,
		Group:      "default",
		Progress:   "0%",
		PrivateData: model.TaskPrivateData{
			BillingSource:    model.BillingFundingSubscription,
			SubscriptionId:   preConsumed.Reservation.SubscriptionId,
			TokenId:          tokenID,
			BillingRequestId: "mj-subscription-shortfall",
		},
	}
	require.NoError(t, model.CreateMidjourneyWithBillingReservation(task, "mj-subscription-shortfall", 40, "sk-mj-sub-shortfall"))
	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(60))
	task.Status = model.MidjourneyStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)

	SettleMidjourneyTaskQuota(context.Background(), task, 60, "subscription shortfall test")
	reservation, err := model.GetBillingReservation("mj-subscription-shortfall")
	require.NoError(t, err)
	assert.Equal(t, model.BillingSettlementFailureSubscriptionQuota, reservation.SettlementFailureCode)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.First(&subscription, subscriptionID).Error)
	assert.EqualValues(t, 40, subscription.AmountUsed)
	assert.Equal(t, 100, getUserQuota(t, userID), "subscription shortfall must not debit wallet")
	assert.Equal(t, 60, getTokenRemainQuota(t, tokenID))
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)
}

func TestAsyncBillingTransientSettlementErrorDoesNotFreeze(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentDebt{}))
	t.Cleanup(func() { model.DB.Exec("DELETE FROM payment_debts") })
	const userID, tokenID = 44, 44
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "sk-task-transient-error", 100)
	_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "task-transient-error",
		UserId:        userID,
		TokenId:       tokenID,
		FundingSource: model.BillingFundingWallet,
		Quota:         40,
	}, "sk-task-transient-error")
	require.NoError(t, err)

	markAsyncBillingSettlementShortfall(
		context.Background(), "task_transient_error", "task-transient-error", 60,
		errors.New("database temporarily unavailable"),
	)
	reservation, err := model.GetBillingReservation("task-transient-error")
	require.NoError(t, err)
	assert.False(t, reservation.ShortfallFreezeApplied)
	assert.Empty(t, reservation.SettlementFailureCode)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
}

// ===========================================================================
// PerCallBilling tests — settleTaskBillingOnComplete
// ===========================================================================

func TestSettle_PerCallBilling_SkipsAdaptorAdjust(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 30, 30, 30
	const initQuota, preConsumed = 10000, 5000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-adaptor", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.PerCallBilling = true
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	adaptor := &mockAdaptor{adjustReturn: 2000}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no adjustment despite adaptor returning 2000
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestSettle_PerCallBilling_SkipsTotalTokens(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 31, 31, 31
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 7000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-tokens", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.PerCallBilling = true
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	adaptor := &mockAdaptor{adjustReturn: 0}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 9999}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no recalculation by tokens
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestSettle_NonPerCallBilling_AppliesAdaptorAdjustment(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 32, 32, 32
	const initQuota, preConsumed = 10000, 5000
	const adaptorQuota = 3000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-nonpercall-adj", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	// PerCallBilling defaults to false
	persistTaskForBillingTest(t, task, model.TaskStatusSuccess)

	adaptor := &mockAdaptor{adjustReturn: adaptorQuota}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Non-per-call: adaptor adjustment applies (refund 2000)
	assert.Equal(t, initQuota+(preConsumed-adaptorQuota), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+(preConsumed-adaptorQuota), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, adaptorQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}
