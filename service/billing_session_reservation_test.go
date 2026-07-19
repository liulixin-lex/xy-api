package service

import (
	"context"
	"errors"
	"math"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareBillingSessionReservationTest(t *testing.T, userId, tokenId int, tokenKey string, quota int) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.BillingReservation{}, &model.QuotaLedgerEntry{}, &model.PaymentDebt{}))
	require.NoError(t, model.DB.Exec("DELETE FROM payment_debts").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM quota_ledger_entries").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM billing_reservations").Error)
	require.NoError(t, model.DB.Create(&model.User{
		Id:       userId,
		Username: "billing-session-" + tokenKey,
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		Id:          tokenId,
		UserId:      userId,
		Key:         tokenKey,
		Name:        "billing-session-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: quota,
	}).Error)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM payment_debts")
		model.DB.Exec("DELETE FROM quota_ledger_entries")
		model.DB.Exec("DELETE FROM billing_reservations")
		model.DB.Unscoped().Delete(&model.Token{}, tokenId)
		model.DB.Unscoped().Delete(&model.User{}, userId)
	})
}

func newBillingSessionTestContext() *gin.Context {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	return context
}

func newWalletRelayInfo(requestId string, userId, tokenId int, tokenKey string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestId:       requestId,
		UserId:          userId,
		TokenId:         tokenId,
		TokenKey:        tokenKey,
		OriginModelName: "billing-test-model",
		UserSetting: dto.UserSetting{
			BillingPreference: "wallet_only",
		},
	}
}

func loadBillingSessionBalances(t *testing.T, userId, tokenId int) (int, int, int) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.Select("quota").Where("id = ?", userId).First(&user).Error)
	require.NoError(t, model.DB.Select("remain_quota", "used_quota").Where("id = ?", tokenId).First(&token).Error)
	return user.Quota, token.RemainQuota, token.UsedQuota
}

func TestBillingSessionRefundCompletesBeforeReturning(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950001, 960001, "billing-session-refund", 100)
	relayInfo := newWalletRelayInfo("billing-session-refund", 950001, 960001, "billing-session-refund")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	assert.True(t, session.NeedsRefund())

	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950001, 960001)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)

	session.Refund(newBillingSessionTestContext())
	userQuota, tokenRemain, tokenUsed = loadBillingSessionBalances(t, 950001, 960001)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
	assert.False(t, session.NeedsRefund())

	session.Refund(newBillingSessionTestContext())
	userQuota, tokenRemain, tokenUsed = loadBillingSessionBalances(t, 950001, 960001)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestBillingSessionSettlementIsAtomicAndValidatesRepeatedActualQuota(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950002, 960002, "billing-session-settle", 100)
	relayInfo := newWalletRelayInfo("billing-session-settle", 950002, 960002, "billing-session-settle")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(60))

	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950002, 960002)
	assert.Equal(t, 40, userQuota)
	assert.Equal(t, 40, tokenRemain)
	assert.Equal(t, 60, tokenUsed)
	require.NoError(t, session.Settle(60))
	assert.ErrorIs(t, session.Settle(61), model.ErrBillingReservationConflict)

	userQuota, tokenRemain, tokenUsed = loadBillingSessionBalances(t, 950002, 960002)
	assert.Equal(t, 40, userQuota)
	assert.Equal(t, 40, tokenRemain)
	assert.Equal(t, 60, tokenUsed)
}

func TestBillingSessionSettlementShortfallFreezesAndRetriesIdempotently(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950008, 960008, "billing-session-shortfall", 50)
	relayInfo := newWalletRelayInfo("billing-session-shortfall", 950008, 960008, "billing-session-shortfall")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)

	const targetQuota = 60
	const concurrency = 8
	errorsChannel := make(chan error, concurrency)
	var waitGroup sync.WaitGroup
	for range concurrency {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsChannel <- session.Settle(targetQuota)
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		assert.ErrorIs(t, err, model.ErrInsufficientUserQuota)
	}
	assert.False(t, session.NeedsRefund())

	reservation, err := model.GetBillingReservation(relayInfo.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusReserved, reservation.Status)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, targetQuota, reservation.SettlementTarget)
	assert.Equal(t, 40, reservation.ReservedQuota)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)
	assert.Equal(t, model.BillingSettlementFailureUserQuota, reservation.SettlementFailureCode)
	assert.True(t, reservation.ShortfallFreezeApplied)

	var shortfallLedgerCount int64
	require.NoError(t, model.DB.Model(&model.QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", relayInfo.RequestId, model.QuotaLedgerPhaseShortfall).
		Count(&shortfallLedgerCount).Error)
	assert.EqualValues(t, 1, shortfallLedgerCount)

	var user model.User
	require.NoError(t, model.DB.First(&user, 950008).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950008, 960008)
	assert.Equal(t, 10, userQuota)
	assert.Equal(t, 10, tokenRemain)
	assert.Equal(t, 40, tokenUsed)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", 950008).Update("quota", 30).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 960008).Update("remain_quota", 30).Error)
	require.NoError(t, session.Settle(targetQuota))

	reservation, err = model.GetBillingReservation(relayInfo.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusSettled, reservation.Status)
	assert.Positive(t, reservation.ShortfallResolvedAt)
	require.NoError(t, model.DB.First(&user, 950008).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	open, err := model.HasOpenUserPaymentFreeze(950008)
	require.NoError(t, err)
	assert.False(t, open)
}

func TestBillingSessionTokenShortfallRollsBackWalletAndRefundReleasesFreeze(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950009, 960009, "billing-session-token-shortfall", 100)
	relayInfo := newWalletRelayInfo("billing-session-token-shortfall", 950009, 960009, "billing-session-token-shortfall")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 960009).Update("remain_quota", 10).Error)

	err := session.Settle(60)
	assert.ErrorIs(t, err, model.ErrInsufficientTokenQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950009, 960009)
	assert.Equal(t, 60, userQuota, "wallet debit must roll back when token settlement fails")
	assert.Equal(t, 10, tokenRemain)
	assert.Equal(t, 40, tokenUsed)

	reservation, err := model.GetBillingReservation(relayInfo.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.BillingSettlementFailureTokenQuota, reservation.SettlementFailureCode)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)

	_, err = model.RefundBillingReservation(relayInfo.RequestId, relayInfo.TokenKey)
	require.NoError(t, err)
	var user model.User
	require.NoError(t, model.DB.First(&user, 950009).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
}

func TestBillingSessionRealtimeReserveShortfallPersistsLiabilityAndBlocksRefund(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950012, 960012, "billing-session-realtime-shortfall", 50)
	relayInfo := newWalletRelayInfo("billing-session-realtime-shortfall", 950012, 960012, "billing-session-realtime-shortfall")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)

	err := session.Reserve(60)
	assert.ErrorIs(t, err, model.ErrInsufficientUserQuota)
	assert.False(t, session.NeedsRefund())
	session.Refund(newBillingSessionTestContext())

	reservation, err := model.GetBillingReservation(relayInfo.RequestId)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusReserved, reservation.Status)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, 60, reservation.SettlementTarget)
	assert.Equal(t, 20, reservation.SettlementShortfallQuota)
	var user model.User
	require.NoError(t, model.DB.First(&user, 950012).Error)
	assert.True(t, user.PaymentFrozen)
}

func TestPostWssConsumeQuotaLogsChargedAmountAndShortfallState(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950010, 960010, "billing-session-wss-log", 50)
	relayInfo := newWalletRelayInfo("billing-session-wss-log", 950010, 960010, "billing-session-wss-log")
	relayInfo.StartTime = time.Now()
	relayInfo.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 123456}
	relayInfo.UsingGroup = "default"
	relayInfo.PriceData.ModelRatio = 1
	relayInfo.PriceData.GroupRatioInfo.GroupRatio = 1
	require.Nil(t, PreConsumeBilling(newBillingSessionTestContext(), 40, relayInfo))
	t.Cleanup(func() {
		model.LOG_DB.Where("user_id = ?", 950010).Delete(&model.Log{})
	})

	usage := &dto.RealtimeUsage{TotalTokens: 60, InputTokens: 60}
	usage.InputTokenDetails.TextTokens = 60
	err := PostWssConsumeQuota(newBillingSessionTestContext(), relayInfo, relayInfo.OriginModelName, usage, "")
	assert.ErrorIs(t, err, model.ErrInsufficientUserQuota)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", 950010, model.LogTypeConsume).Order("id desc").First(&log).Error)
	assert.Equal(t, 40, log.Quota)
	var other map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	assert.Equal(t, billingSettlementStatusShortfall, other["billing_settlement_status"])
	assert.EqualValues(t, 60, other["billing_target_quota"])
	assert.EqualValues(t, 40, other["billing_charged_quota"])
	assert.EqualValues(t, 20, other["billing_outstanding_quota"])
}

func TestBillingSessionZeroQuotaFailureStillFinalizesReservation(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950003, 960003, "billing-session-zero", 100)
	relayInfo := newWalletRelayInfo("billing-session-zero", 950003, 960003, "billing-session-zero")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 0)
	require.Nil(t, apiErr)
	assert.True(t, session.NeedsRefund())

	session.Refund(newBillingSessionTestContext())
	reservation, err := model.GetBillingReservation("billing-session-zero")
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusRefunded, reservation.Status)
	assert.False(t, session.NeedsRefund())
}

func TestBillingSessionSettlementIntentFailureCannotTriggerDeliveredUsageRefund(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950011, 960011, "billing-session-intent-failure", 100)
	relayInfo := newWalletRelayInfo("billing-session-intent-failure", 950011, 960011, "billing-session-intent-failure")
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)

	require.Error(t, session.Settle(-1))
	assert.False(t, session.NeedsRefund())
	session.Refund(newBillingSessionTestContext())
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950011, 960011)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
}

func TestRealtimeUsageExtendsOneDurableReservationWithoutDoubleCharging(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950006, 960006, "billing-realtime", 100_000)
	ctx := newBillingSessionTestContext()
	relayInfo := newWalletRelayInfo("billing-realtime", 950006, 960006, "billing-realtime")
	relayInfo.OriginModelName = "gpt-4o"
	relayInfo.UsingGroup = "default"
	relayInfo.UserGroup = "default"
	require.Nil(t, PreConsumeBilling(ctx, 100, relayInfo))

	usage := &dto.RealtimeUsage{}
	usage.InputTokenDetails.TextTokens = 1_000
	usage.InputTokens = 1_000
	usage.TotalTokens = 1_000
	groupRatio := ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
	modelRatio, _, _ := ratio_setting.GetModelRatio(relayInfo.OriginModelName)
	expectedQuota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails: TokenDetails{TextTokens: usage.InputTokenDetails.TextTokens},
		ModelName:    relayInfo.OriginModelName, ModelRatio: modelRatio, GroupRatio: groupRatio,
	})
	require.Nil(t, clamp)
	require.Positive(t, expectedQuota)
	require.NoError(t, PreWssConsumeQuota(ctx, relayInfo, usage))

	reservation, err := model.GetBillingReservation(relayInfo.RequestId)
	require.NoError(t, err)
	assert.Equal(t, 100+expectedQuota, reservation.ReservedQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950006, 960006)
	assert.Equal(t, 100_000-reservation.ReservedQuota, userQuota)
	assert.Equal(t, 100_000-reservation.ReservedQuota, tokenRemain)
	assert.Equal(t, reservation.ReservedQuota, tokenUsed)

	require.NoError(t, SettleBilling(ctx, relayInfo, expectedQuota))
	userQuota, tokenRemain, tokenUsed = loadBillingSessionBalances(t, 950006, 960006)
	assert.Equal(t, 100_000-expectedQuota, userQuota)
	assert.Equal(t, 100_000-expectedQuota, tokenRemain)
	assert.Equal(t, expectedQuota, tokenUsed)
}

func TestViolationFeeUsesCheckedIdempotentBillingReservation(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950007, 960007, "billing-violation", 100_000)
	settings := model_setting.GetGrokSettings()
	original := *settings
	t.Cleanup(func() { *settings = original })
	settings.ViolationDeductionEnabled = true
	settings.ViolationDeductionAmount = 0.001

	relayInfo := newWalletRelayInfo("billing-violation", 950007, 960007, "billing-violation")
	relayInfo.BillingSource = BillingSourceWallet
	relayInfo.PriceData.GroupRatioInfo.GroupRatio = 1
	apiErr := types.NewError(errors.New(CSAMViolationMarker), types.ErrorCodeViolationFeeGrokCSAM)
	ctx := newBillingSessionTestContext()

	expectedQuota, clamp := calcViolationFeeQuota(settings.ViolationDeductionAmount, 1)
	require.Nil(t, clamp)
	require.Positive(t, expectedQuota)
	assert.True(t, ChargeViolationFeeIfNeeded(ctx, relayInfo, apiErr))
	assert.True(t, ChargeViolationFeeIfNeeded(ctx, relayInfo, apiErr))

	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950007, 960007)
	assert.Equal(t, 100_000-expectedQuota, userQuota)
	assert.Equal(t, 100_000-expectedQuota, tokenRemain)
	assert.Equal(t, expectedQuota, tokenUsed)
	requestID := "violation_" + common.Sha1([]byte(relayInfo.RequestId+":"+string(types.ErrorCodeViolationFeeGrokCSAM)))
	reservation, err := model.GetBillingReservation(requestID)
	require.NoError(t, err)
	assert.Equal(t, model.BillingReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.SettledQuota)

	clampedQuota, saturation := calcViolationFeeQuota(math.Inf(1), 1)
	assert.Equal(t, math.MaxInt32, clampedQuota)
	require.NotNil(t, saturation)
}

func TestRecalculateTaskQuotaUsesDurableReservationExactlyOnce(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950004, 960004, "billing-task-settle", 100)
	relayInfo := newWalletRelayInfo("billing-task-settle", 950004, 960004, "billing-task-settle")
	_, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)
	task := &model.Task{
		TaskID:    "task_service_billing_settle",
		UserId:    950004,
		ChannelId: 1,
		Group:     "default",
		Status:    model.TaskStatusInProgress,
		Properties: model.Properties{
			OriginModelName: "billing-test-model",
		},
		PrivateData: model.TaskPrivateData{
			TokenId:          960004,
			BillingRequestId: "billing-task-settle",
			BillingSource:    BillingSourceWallet,
		},
	}
	require.NoError(t, model.CreateTaskWithBillingReservation(task, "billing-task-settle", 40, "billing-task-settle"))
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusSuccess).Error)
	task.Status = model.TaskStatusSuccess
	t.Cleanup(func() {
		model.DB.Delete(&model.Task{}, task.ID)
		model.LOG_DB.Where("user_id = ?", 950004).Delete(&model.Log{})
	})

	RecalculateTaskQuota(context.Background(), task, 60, "durable async settle")
	RecalculateTaskQuota(context.Background(), task, 60, "duplicate durable async settle")
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950004, 960004)
	assert.Equal(t, 40, userQuota)
	assert.Equal(t, 40, tokenRemain)
	assert.Equal(t, 60, tokenUsed)
	var storedTask model.Task
	require.NoError(t, model.DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 60, storedTask.Quota)
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", 950004, model.LogTypeConsume).Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}

func TestRefundTaskQuotaUsesDurableReservationExactlyOnce(t *testing.T) {
	prepareBillingSessionReservationTest(t, 950005, 960005, "billing-task-refund", 100)
	relayInfo := newWalletRelayInfo("billing-task-refund", 950005, 960005, "billing-task-refund")
	_, apiErr := NewBillingSession(newBillingSessionTestContext(), relayInfo, 40)
	require.Nil(t, apiErr)
	task := &model.Task{
		TaskID:    "task_service_billing_refund",
		UserId:    950005,
		ChannelId: 1,
		Group:     "default",
		Status:    model.TaskStatusInProgress,
		Properties: model.Properties{
			OriginModelName: "billing-test-model",
		},
		PrivateData: model.TaskPrivateData{
			TokenId:          960005,
			BillingRequestId: "billing-task-refund",
			BillingSource:    BillingSourceWallet,
		},
	}
	require.NoError(t, model.CreateTaskWithBillingReservation(task, "billing-task-refund", 40, "billing-task-refund"))
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusFailure).Error)
	task.Status = model.TaskStatusFailure
	t.Cleanup(func() {
		model.DB.Delete(&model.Task{}, task.ID)
		model.LOG_DB.Where("user_id = ?", 950005).Delete(&model.Log{})
	})

	RefundTaskQuota(context.Background(), task, "upstream failed")
	RefundTaskQuota(context.Background(), task, "duplicate upstream failure")
	userQuota, tokenRemain, tokenUsed := loadBillingSessionBalances(t, 950005, 960005)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", 950005, model.LogTypeRefund).Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}
