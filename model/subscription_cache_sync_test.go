package model

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failRedisAfterFirstCommandHook struct {
	commandCount atomic.Int64
}

func (h *failRedisAfterFirstCommandHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	if h.commandCount.Add(1) > 1 {
		return ctx, errors.New("redis disconnected after cache epoch read")
	}
	return ctx, nil
}

func (*failRedisAfterFirstCommandHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (h *failRedisAfterFirstCommandHook) BeforeProcessPipeline(ctx context.Context, commands []redis.Cmder) (context.Context, error) {
	if h.commandCount.Add(int64(len(commands))) > 1 {
		return ctx, errors.New("redis disconnected after cache epoch read")
	}
	return ctx, nil
}

func (*failRedisAfterFirstCommandHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func useRedisDisconnectAfterEpochRead(t *testing.T) *failRedisAfterFirstCommandHook {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&IdentityCacheSync{}))
	require.NoError(t, DB.Exec("DELETE FROM identity_cache_syncs").Error)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	hook := &failRedisAfterFirstCommandHook{}
	client.AddHook(hook)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
		require.NoError(t, DB.Exec("DELETE FROM identity_cache_syncs").Error)
	})
	return hook
}

func TestPurchaseSubscriptionWithBalanceKeepsPendingSyncAfterPostCommitRedisFailure(t *testing.T) {
	truncateTables(t)
	hook := useRedisDisconnectAfterEpochRead(t)
	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1000
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	user := &User{
		Id: 99102, Username: "subscription-balance-cache", Group: "default",
		Status: common.UserStatusEnabled, Quota: 5000, AffCode: "subscription-balance-cache",
	}
	plan := &SubscriptionPlan{
		Id: 99102, Title: "Balance cache sync", PriceAmount: 2, Currency: "USD",
		DurationUnit: SubscriptionDurationDay, DurationValue: 1, Enabled: true,
		AllowBalancePay: common.GetPointer(true),
	}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)

	require.NoError(t, PurchaseSubscriptionWithBalance(user.Id, plan.Id))
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, 3000, user.Quota)
	assert.GreaterOrEqual(t, hook.commandCount.Load(), int64(3))

	var pending IdentityCacheSync
	require.NoError(t, DB.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
	assert.Equal(t, int64(1), pending.Version)
	assert.Positive(t, pending.Attempts)
	assert.NotEmpty(t, pending.LastError)

	var subscriptionCount int64
	var orderCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptionCount).Error)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&orderCount).Error)
	assert.Equal(t, int64(1), subscriptionCount)
	assert.Equal(t, int64(1), orderCount)
}

func TestCalcSubscriptionBalanceQuotaRejectsOutOfRangeProducts(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	tests := []struct {
		name         string
		price        float64
		quotaPerUnit float64
		want         int
		wantError    bool
	}{
		{name: "zero price", price: 0, quotaPerUnit: 1000, want: 0},
		{name: "fraction rounds up", price: 0.0011, quotaPerUnit: 1000, want: 2},
		{name: "exact maximum", price: 1, quotaPerUnit: common.MaxQuota, want: common.MaxQuota},
		{name: "maximum plus one", price: 1, quotaPerUnit: common.MaxQuota + 1, wantError: true},
		{name: "non finite configuration", price: 1, quotaPerUnit: math.Inf(1), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			common.QuotaPerUnit = test.quotaPerUnit
			quota, err := calcSubscriptionBalanceQuota(test.price)
			if test.wantError {
				require.Error(t, err)
				assert.Zero(t, quota)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, quota)
		})
	}
}

func TestSubscriptionGroupCreateAndExpiryQueueDurableCacheSync(t *testing.T) {
	truncateTables(t)
	useUnavailableRedisForMutationTest(t)

	user := &User{
		Id: 99103, Username: "subscription-group-cache", Group: "default",
		Status: common.UserStatusEnabled, AffCode: "subscription-group-cache",
	}
	plan := &SubscriptionPlan{
		Id: 99103, Title: "Group cache sync", Currency: "USD",
		DurationUnit: SubscriptionDurationDay, DurationValue: 1, Enabled: true,
		UpgradeGroup: "vip",
	}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)

	_, err := AdminBindSubscription(user.Id, plan.Id, "cache sync test")
	require.NoError(t, err)
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, "vip", user.Group)

	var pending IdentityCacheSync
	require.NoError(t, DB.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
	createVersion := pending.Version
	assert.Positive(t, createVersion)

	var subscription UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&subscription).Error)
	require.NoError(t, DB.Model(&subscription).Update("end_time", time.Now().Add(-time.Minute).Unix()).Error)

	expired, err := ExpireDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 1, expired)
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, "default", user.Group)
	require.NoError(t, DB.First(&subscription, subscription.Id).Error)
	assert.Equal(t, "expired", subscription.Status)
	require.NoError(t, DB.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
	assert.Greater(t, pending.Version, createVersion)
}

func TestSubscriptionPostConsumeAndRefundUseTransactionLocalDatabaseTime(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&SubscriptionBillingPeriod{}, &SubscriptionPreConsumeRecord{}))
	require.NoError(t, DB.Exec("DELETE FROM subscription_billing_periods").Error)
	require.NoError(t, DB.Exec("DELETE FROM subscription_pre_consume_records").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Exec("DELETE FROM subscription_pre_consume_records").Error)
		require.NoError(t, DB.Exec("DELETE FROM subscription_billing_periods").Error)
		require.NoError(t, DB.Exec("DELETE FROM user_subscriptions").Error)
	})

	subscription := &UserSubscription{
		Id: 99104, UserId: 99104, PlanId: 99104, AmountTotal: 100, AmountUsed: 0,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", BillingPeriodSequence: 0,
	}
	require.NoError(t, DB.Create(subscription).Error)
	require.NoError(t, PostConsumeUserSubscriptionDelta(subscription.Id, 10))

	var period SubscriptionBillingPeriod
	require.NoError(t, DB.Where("subscription_id = ?", subscription.Id).First(&period).Error)
	assert.Equal(t, int64(10), period.AmountUsed)
	require.NoError(t, DB.First(subscription, subscription.Id).Error)
	assert.Equal(t, int64(10), subscription.AmountUsed)

	record := &SubscriptionPreConsumeRecord{
		RequestId: "subscription-tx-local-time", UserId: subscription.UserId,
		UserSubscriptionId: subscription.Id, BillingPeriodId: period.ID,
		BillingPeriodSeq: period.PeriodSequence, PreConsumed: 10, Status: "consumed",
	}
	require.NoError(t, DB.Create(record).Error)
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))

	require.NoError(t, DB.First(&period, period.ID).Error)
	assert.Zero(t, period.AmountUsed)
	require.NoError(t, DB.First(subscription, subscription.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	require.NoError(t, DB.First(record, record.Id).Error)
	assert.Equal(t, "refunded", record.Status)
}
