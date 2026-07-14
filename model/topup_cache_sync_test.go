package model

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useUnavailableRedisForMutationTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&IdentityCacheSync{}))
	require.NoError(t, DB.Exec("DELETE FROM identity_cache_syncs").Error)

	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "unavailable:0",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("redis unavailable for mutation test")
		},
		MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
		require.NoError(t, DB.Exec("DELETE FROM identity_cache_syncs").Error)
	})
}

func TestRechargeEpayCommitsAndKeepsDurableCacheSyncWhenRedisIsUnavailable(t *testing.T) {
	truncateTables(t)
	useUnavailableRedisForMutationTest(t)
	withQuotaPerUnitForAffiliateTest(t, 1000)

	user := &User{
		Id: 99101, Username: "epay-cache-sync", Status: common.UserStatusEnabled, AffCode: "epay-cache-sync",
	}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(&TopUp{
		UserId: user.Id, Amount: 2, Money: 2, TradeNo: "epay-cache-sync",
		PaymentMethod: PaymentProviderEpay, PaymentProvider: PaymentProviderEpay,
		Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
	}).Error)

	require.NoError(t, RechargeEpay("epay-cache-sync", "alipay", "127.0.0.1"))
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.Equal(t, 2000, user.Quota)

	var pending IdentityCacheSync
	require.NoError(t, DB.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
	assert.Positive(t, pending.Version)
	assert.Positive(t, pending.Attempts)

	version := pending.Version
	attempts := pending.Attempts
	nextRetryMs := pending.NextRetryMs
	require.NoError(t, RechargeEpay("epay-cache-sync", "alipay", "127.0.0.1"))
	require.NoError(t, DB.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
	assert.Equal(t, version, pending.Version)
	assert.Equal(t, attempts, pending.Attempts)
	assert.Equal(t, nextRetryMs, pending.NextRetryMs)
}
