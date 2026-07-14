package model

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCacheEpochRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	previousFrequency := common.SyncFrequency
	common.RDB = client
	common.RedisEnabled = true
	common.SyncFrequency = 60
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
		common.SyncFrequency = previousFrequency
	})
	return server
}

type blockingIdentityCacheEvalHook struct {
	started chan struct{}
	once    sync.Once
}

func (hook *blockingIdentityCacheEvalHook) BeforeProcess(
	ctx context.Context,
	command redis.Cmder,
) (context.Context, error) {
	if command.Name() != "eval" && command.Name() != "evalsha" {
		return ctx, nil
	}
	hook.once.Do(func() { close(hook.started) })
	<-ctx.Done()
	return ctx, ctx.Err()
}

func (*blockingIdentityCacheEvalHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (*blockingIdentityCacheEvalHook) BeforeProcessPipeline(
	ctx context.Context,
	_ []redis.Cmder,
) (context.Context, error) {
	return ctx, nil
}

func (*blockingIdentityCacheEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestCacheEpochRejectsDelayedUserQuotaAndTokenBackfills(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	previousScheduler := runAsyncCacheBackfill
	queued := make([]func(), 0, 4)
	runAsyncCacheBackfill = func(task func()) {
		queued = append(queued, task)
	}
	t.Cleanup(func() { runAsyncCacheBackfill = previousScheduler })

	fullUser := User{
		Id: 88501, Username: "epoch-full-user", AffCode: "epoch-full",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&fullUser).Error)
	cached, err := GetUserCache(fullUser.Id)
	require.NoError(t, err)
	assert.Equal(t, 1000, cached.Quota)
	require.Len(t, queued, 1)
	require.NoError(t, db.Model(&User{}).Where("id = ?", fullUser.Id).Update("quota", 900).Error)
	require.NoError(t, InvalidateUserCache(fullUser.Id))
	queued[0]()
	queued = queued[:0]
	exists, err := common.RDB.Exists(context.Background(), getUserCacheKey(fullUser.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)

	quotaUser := User{
		Id: 88502, Username: "epoch-quota-user", AffCode: "epoch-quota",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&quotaUser).Error)
	require.NoError(t, common.RedisHSetObj(
		getUserCacheKey(quotaUser.Id), quotaUser.ToBaseUser(), time.Minute,
	))
	quota, err := GetUserQuota(quotaUser.Id, true)
	require.NoError(t, err)
	assert.Equal(t, 1000, quota)
	require.Len(t, queued, 1)
	require.NoError(t, db.Model(&User{}).Where("id = ?", quotaUser.Id).Update("quota", 900).Error)
	require.NoError(t, InvalidateUserCache(quotaUser.Id))
	queued[0]()
	queued = queued[:0]
	exists, err = common.RDB.Exists(context.Background(), getUserCacheKey(quotaUser.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)

	tokenUser := User{
		Id: 88503, Username: "epoch-token-user", AffCode: "epoch-token",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&tokenUser).Error)
	token := Token{
		Id: tokenUser.Id, UserId: tokenUser.Id, Key: "sk-epoch-token", Name: "epoch-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&token).Error)
	cachedToken, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 1000, cachedToken.RemainQuota)
	require.Len(t, queued, 1)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{
		"remain_quota": 900, "used_quota": 100,
	}).Error)
	require.NoError(t, InvalidateUserTokensCache(token.UserId))
	queued[0]()
	exists, err = common.RDB.Exists(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
}

func TestCacheEpochMutationRejectsCacheMissOldReaders(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	previousScheduler := runAsyncCacheBackfill
	queued := make([]func(), 0, 2)
	runAsyncCacheBackfill = func(task func()) {
		queued = append(queued, task)
	}
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		runAsyncCacheBackfill = previousScheduler
	})

	user := User{
		Id: 88507, Username: "epoch-legacy-user", AffCode: "epoch-legacy-user",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	quota, err := GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, 1000, quota)
	require.Len(t, queued, 1)
	require.NoError(t, IncreaseUserQuota(user.Id, 25, true))
	queued[0]()
	queued = queued[:0]
	userCacheExists, err := common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, userCacheExists)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 1025, user.Quota)

	token := Token{
		Id: 88508, UserId: user.Id, Key: "sk-epoch-legacy-token", Name: "epoch-legacy-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, UsedQuota: 100, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&token).Error)
	cachedToken, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 1000, cachedToken.RemainQuota)
	require.Len(t, queued, 1)
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 25))
	queued[0]()
	tokenCacheExists, err := common.RDB.Exists(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Zero(t, tokenCacheExists)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 1025, token.RemainQuota)
	assert.Equal(t, 75, token.UsedQuota)
}

func TestCacheEpochConcurrentQuotaDeltasPreserveAuthoritativeTotal(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	user := User{
		Id: 88510, Username: "epoch-concurrent-user", AffCode: "epoch-concurrent-user",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	epoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	written, err := common.RedisHSetObjIfCacheEpoch(
		getUserCacheEpochKey(user.Id), epoch, getUserCacheKey(user.Id), user.ToBaseUser(), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, increaseUserQuota(user.Id, 25))
	require.NoError(t, increaseUserQuota(user.Id, 25))

	start := make(chan struct{})
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			errs <- cacheIncrUserQuota(user.Id, 25, epoch)
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for cacheErr := range errs {
		require.NoError(t, cacheErr)
	}

	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 1050, user.Quota)
	cacheExists, err := common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, cacheExists)
}

func TestLegacyQuotaMutationsCommitAndQueueWhenRedisIsUnavailable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	user := User{
		Id: 88509, Username: "epoch-unavailable-user", AffCode: "epoch-unavailable-user",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	token := Token{
		Id: 88509, UserId: user.Id, Key: "sk-epoch-unavailable-token", Name: "epoch-unavailable-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, UsedQuota: 100, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)

	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
	})

	require.NoError(t, IncreaseUserQuota(user.Id, 25, true))
	require.NoError(t, DecreaseUserQuota(user.Id, 25, true))
	require.NoError(t, SetUserQuota(user.Id, 777))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 25))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 25))

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 777, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
	var pending int64
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&pending).Error)
	assert.Equal(t, int64(2), pending)
}

func TestQuotaMutationsStaySynchronousWhileBatchUpdatesAreActive(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchUpdateEnabled })
	user := User{
		Id: 88511, Username: "epoch-batch-user", AffCode: "epoch-batch-user",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	token := Token{
		Id: 88511, UserId: user.Id, Key: "sk-epoch-batch-token", Name: "epoch-batch-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, UsedQuota: 100, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)
	userEpoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	written, err := common.RedisHSetObjIfCacheEpoch(
		getUserCacheEpochKey(user.Id), userEpoch, getUserCacheKey(user.Id), user.ToBaseUser(), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, written)
	tokenEpoch, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(token, tokenEpoch))

	require.NoError(t, IncreaseUserQuota(user.Id, 25, false))
	require.NoError(t, DecreaseUserQuota(user.Id, 10, false))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 25))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 10))

	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 1015, user.Quota)
	assert.Equal(t, 1015, token.RemainQuota)
	assert.Equal(t, 85, token.UsedQuota)
	cachedUserQuota, err := common.RDB.HGet(context.Background(), getUserCacheKey(user.Id), "Quota").Int()
	require.NoError(t, err)
	assert.Equal(t, 1015, cachedUserQuota)
	cachedTokenQuota, err := common.RDB.HGet(context.Background(), getTokenCacheKey(token.Key), "RemainQuota").Int()
	require.NoError(t, err)
	assert.Equal(t, 1015, cachedTokenQuota)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	_, userQueued := batchUpdateStores[BatchUpdateTypeUserQuota][user.Id]
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	batchUpdateLocks[BatchUpdateTypeTokenQuota].Lock()
	_, tokenQueued := batchUpdateStores[BatchUpdateTypeTokenQuota][token.Id]
	batchUpdateLocks[BatchUpdateTypeTokenQuota].Unlock()
	assert.False(t, userQueued)
	assert.False(t, tokenQueued)
}

func TestTokenDisableAdvancesEpochBeforeOldBackfillRuns(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	previousScheduler := runAsyncCacheBackfill
	queued := make([]func(), 0, 1)
	runAsyncCacheBackfill = func(task func()) {
		queued = append(queued, task)
	}
	t.Cleanup(func() { runAsyncCacheBackfill = previousScheduler })
	token := Token{
		Id: 88512, UserId: 88512, Key: "sk-epoch-disable-token", Name: "epoch-disable-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&token).Error)
	stale, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	stale.Status = common.TokenStatusDisabled
	require.NoError(t, stale.SelectUpdate())
	queued[0]()

	cacheExists, err := common.RDB.Exists(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Zero(t, cacheExists)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, token.Status)
}

func TestUserGroupAndSettingMutationsRejectOldBackfills(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	previousScheduler := runAsyncCacheBackfill
	queued := make([]func(), 0, 2)
	runAsyncCacheBackfill = func(task func()) {
		queued = append(queued, task)
	}
	t.Cleanup(func() { runAsyncCacheBackfill = previousScheduler })
	user := User{
		Id: 88513, Username: "epoch-profile-user", AffCode: "epoch-profile-user",
		Status: common.UserStatusEnabled, Group: "old-group", Setting: `{"language":"en"}`, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	epoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	written, err := common.RedisHSetObjIfCacheEpoch(
		getUserCacheEpochKey(user.Id), epoch, getUserCacheKey(user.Id), user.ToBaseUser(), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, written)
	group, err := GetUserGroup(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "old-group", group)
	setting, err := GetUserSetting(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "en", setting.Language)
	require.Len(t, queued, 2)

	user.Group = "new-group"
	require.NoError(t, user.Update(false))
	require.NoError(t, UpdateUserSetting(user.Id, dto.UserSetting{Language: "zh"}))
	for _, backfill := range queued {
		backfill()
	}
	cacheExists, err := common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, cacheExists)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, "new-group", user.Group)
	assert.Equal(t, "zh", user.GetSetting().Language)
}

func TestUserSettingCommitsAndQueuesWhenRedisIsUnavailable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	user := User{
		Id: 88514, Username: "epoch-setting-failure", AffCode: "epoch-setting-failure",
		Status: common.UserStatusEnabled, Setting: `{"language":"en"}`,
	}
	require.NoError(t, db.Create(&user).Error)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})

	require.NoError(t, UpdateUserSetting(user.Id, dto.UserSetting{Language: "zh"}))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, "zh", user.GetSetting().Language)
	var pending IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
}

func TestQuotaMutationDoesNotReturnRetryableErrorAfterDatabaseCommit(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	server := setupCacheEpochRedis(t)
	user := User{
		Id: 88516, Username: "epoch-post-commit", AffCode: "epoch-post-commit",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	epoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	written, err := common.RedisHSetObjIfCacheEpoch(
		getUserCacheEpochKey(user.Id), epoch, getUserCacheKey(user.Id), user.ToBaseUser(), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, written)
	const callbackName = "test:close_redis_after_quota_commit"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(*gorm.DB) {
		server.Close()
	}))
	t.Cleanup(func() { db.Callback().Update().Remove(callbackName) })

	require.NoError(t, IncreaseUserQuota(user.Id, 25, true))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 1025, user.Quota)
}

func TestTokenUpdateDoesNotReturnRetryableErrorAfterDatabaseCommit(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	server := setupCacheEpochRedis(t)
	token := Token{
		Id: 88517, UserId: 88517, Key: "sk-epoch-post-commit", Name: "epoch-post-commit",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&token).Error)
	_, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
	require.NoError(t, err)
	const callbackName = "test:close_redis_after_token_commit"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(*gorm.DB) {
		server.Close()
	}))
	t.Cleanup(func() { db.Callback().Update().Remove(callbackName) })

	token.Status = common.TokenStatusDisabled
	require.NoError(t, token.SelectUpdate())
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, token.Status)
}

func TestIdentityCacheSyncRecoversOldRedisKeysAfterClientPartition(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	goodClient := common.RDB
	badClient := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = badClient.Close() })
	previousScheduler := runAsyncCacheBackfill
	runAsyncCacheBackfill = func(task func()) { task() }
	t.Cleanup(func() { runAsyncCacheBackfill = previousScheduler })

	user := User{
		Id: 88520, Username: "cache-sync-partition-user", AffCode: "cache-sync-partition-user",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	token := Token{
		Id: 88520, UserId: user.Id, Key: "sk-cache-sync-partition-token", Name: "cache-sync-partition-token",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)
	userEpoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(user, userEpoch))
	tokenEpoch, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(token, tokenEpoch))

	const callbackName = "test:partition_identity_cache_client_after_commit"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(*gorm.DB) {
		common.RDB = badClient
	}))
	t.Cleanup(func() { db.Callback().Update().Remove(callbackName) })

	require.NoError(t, IncreaseUserQuota(user.Id, 25, true))
	common.RDB = goodClient
	staleQuota, err := goodClient.HGet(context.Background(), getUserCacheKey(user.Id), "Quota").Int()
	require.NoError(t, err)
	assert.Equal(t, 1000, staleQuota, "the Redis service must retain the old key while only the client is partitioned")
	var pendingUserSync IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pendingUserSync).Error)
	assert.Equal(t, int64(1), pendingUserSync.Version)

	recoveryAt := time.Now().Add(2 * time.Second)
	subjects, err := FindPendingIdentityCacheSyncSubjects(recoveryAt, 10)
	require.NoError(t, err)
	require.Contains(t, subjects, getUserCacheKey(user.Id))
	require.NoError(t, SyncIdentityCacheSubject(context.Background(), getUserCacheKey(user.Id), recoveryAt))
	exists, err := goodClient.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
	quota, err := GetUserQuota(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 1025, quota)
	refilledQuota, err := goodClient.HGet(context.Background(), getUserCacheKey(user.Id), "Quota").Int()
	require.NoError(t, err)
	assert.Equal(t, 1025, refilledQuota)

	common.RDB = goodClient
	require.NoError(t, SetUserQuota(user.Id, 640))
	common.RDB = goodClient
	staleQuota, err = goodClient.HGet(context.Background(), getUserCacheKey(user.Id), "Quota").Int()
	require.NoError(t, err)
	assert.Equal(t, 1025, staleQuota)
	require.NoError(t, SyncIdentityCacheSubject(
		context.Background(), getUserCacheKey(user.Id), time.Now().Add(2*time.Second),
	))
	quota, err = GetUserQuota(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 640, quota)

	common.RDB = goodClient
	token.Status = common.TokenStatusDisabled
	require.NoError(t, token.SelectUpdate())
	common.RDB = goodClient
	staleStatus, err := goodClient.HGet(context.Background(), getTokenCacheKey(token.Key), "Status").Int()
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusEnabled, staleStatus)
	var pendingTokenSync IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getTokenCacheKey(token.Key)).First(&pendingTokenSync).Error)
	assert.NotContains(t, pendingTokenSync.SubjectKey, token.Key)
	assert.NotContains(t, pendingTokenSync.CacheKey, token.Key)
	assert.NotContains(t, pendingTokenSync.EpochKey, token.Key)
	recoveryAt = time.Now().Add(2 * time.Second)
	require.NoError(t, SyncIdentityCacheSubject(context.Background(), getTokenCacheKey(token.Key), recoveryAt))
	exists, err = goodClient.Exists(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
	refilledToken, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusDisabled, refilledToken.Status)
	refilledStatus, err := goodClient.HGet(context.Background(), getTokenCacheKey(token.Key), "Status").Int()
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusDisabled, refilledStatus)

	var pending int64
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&pending).Error)
	assert.Zero(t, pending)
}

func TestIdentityCacheSyncCancellationStopsRedisAndKeepsDurableRetry(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	hook := &blockingIdentityCacheEvalHook{started: make(chan struct{})}
	client.AddHook(hook)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})

	now := time.Now()
	record := IdentityCacheSync{
		SubjectKey: "user:88524", EpochKey: "cache_epoch:user:88524", CacheKey: "user:88524",
		Version: 1, NextRetryMs: 0, CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, db.Create(&record).Error)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- SyncIdentityCacheSubject(ctx, record.SubjectKey, now)
	}()

	select {
	case <-hook.started:
	case <-time.After(time.Second):
		t.Fatal("identity cache sync did not reach Redis Eval")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("identity cache sync did not stop after cancellation")
	}

	var pending IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", record.SubjectKey).First(&pending).Error)
	assert.Equal(t, 1, pending.Attempts)
	assert.Greater(t, pending.NextRetryMs, now.UnixMilli())
	assert.Contains(t, pending.LastError, context.Canceled.Error())
}

func TestIdentityCacheSyncCoalescesBySubjectAndAcknowledgesWithVersionCAS(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	var firstVersion int64
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		firstVersion, err = queueUserCacheSyncTx(tx, 88521, now)
		return err
	}))
	var secondVersion int64
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		secondVersion, err = queueUserCacheSyncTx(tx, 88521, now.Add(time.Millisecond))
		return err
	}))
	assert.Equal(t, firstVersion+1, secondVersion)
	var count int64
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	require.NoError(t, acknowledgeIdentityCacheSyncVersion(context.Background(), getUserCacheKey(88521), firstVersion))
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "an older completion must not erase a newer pending mutation")
	require.NoError(t, acknowledgeIdentityCacheSyncVersion(context.Background(), getUserCacheKey(88521), secondVersion))
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Unscoped().Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var thirdVersion int64
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		thirdVersion, err = queueUserCacheSyncTx(tx, 88521, now.Add(2*time.Millisecond))
		return err
	}))
	assert.Equal(t, secondVersion+1, thirdVersion)
	require.NoError(t, acknowledgeIdentityCacheSyncVersion(context.Background(), getUserCacheKey(88521), secondVersion))
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "a delayed acknowledgement must not erase a resurrected mutation")
	require.NoError(t, acknowledgeIdentityCacheSyncVersion(context.Background(), getUserCacheKey(88521), thirdVersion))
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestIdentityCacheSyncPersistsProfileAndTokenUpdateCompensation(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	goodClient := common.RDB
	badClient := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = badClient.Close() })
	user := User{
		Id: 88522, Username: "cache-sync-profile", AffCode: "cache-sync-profile",
		Status: common.UserStatusEnabled, Group: "old", Setting: `{"language":"en"}`, Quota: 1000,
	}
	token := Token{
		Id: 88522, UserId: user.Id, Key: "sk-cache-sync-profile", Name: "old-token-name",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)

	const callbackName = "test:partition_profile_cache_client_after_commit"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(*gorm.DB) {
		common.RDB = badClient
	}))
	t.Cleanup(func() { db.Callback().Update().Remove(callbackName) })

	refillUser := func() {
		common.RDB = goodClient
		epoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
		require.NoError(t, err)
		require.NoError(t, populateUserCacheIfEpoch(user, epoch))
	}
	recoverUser := func() {
		common.RDB = goodClient
		var pending IdentityCacheSync
		require.NoError(t, db.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
		require.NoError(t, SyncIdentityCacheSubject(
			context.Background(), getUserCacheKey(user.Id), time.Now().Add(2*time.Second),
		))
	}

	refillUser()
	user.Group = "new"
	common.RDB = badClient
	require.NoError(t, user.Update(false))
	recoverUser()
	refillUser()
	user.Username = "cache-sync-edited"
	common.RDB = badClient
	require.NoError(t, user.Edit(false))
	recoverUser()
	refillUser()
	require.NoError(t, UpdateUserSetting(user.Id, dto.UserSetting{Language: "zh"}))
	recoverUser()

	common.RDB = goodClient
	tokenEpoch, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(token, tokenEpoch))
	token.Name = "new-token-name"
	common.RDB = badClient
	require.NoError(t, token.Update())
	common.RDB = goodClient
	var pendingToken IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getTokenCacheKey(token.Key)).First(&pendingToken).Error)
	require.NoError(t, SyncIdentityCacheSubject(
		context.Background(), getTokenCacheKey(token.Key), time.Now().Add(2*time.Second),
	))

	var pending int64
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&pending).Error)
	assert.Zero(t, pending)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, "cache-sync-edited", user.Username)
	assert.Equal(t, "new", user.Group)
	assert.Equal(t, "zh", user.GetSetting().Language)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, "new-token-name", token.Name)
}

func TestIdentityCacheSyncPersistsDeletionAndBindingCompensation(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	require.NoError(t, db.AutoMigrate(&UserOAuthBinding{}))
	setupCacheEpochRedis(t)
	goodClient := common.RDB
	badClient := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = badClient.Close() })
	const updateCallback = "test:partition_identity_delete_update"
	const deleteCallback = "test:partition_identity_delete_delete"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(updateCallback, func(*gorm.DB) {
		common.RDB = badClient
	}))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(deleteCallback, func(*gorm.DB) {
		common.RDB = badClient
	}))
	t.Cleanup(func() {
		db.Callback().Update().Remove(updateCallback)
		db.Callback().Delete().Remove(deleteCallback)
	})
	recoverSubject := func(subject string) {
		common.RDB = goodClient
		var pending IdentityCacheSync
		require.NoError(t, db.Where("subject_key = ?", subject).First(&pending).Error)
		require.NoError(t, SyncIdentityCacheSubject(context.Background(), subject, time.Now().Add(2*time.Second)))
	}

	bindingUser := User{
		Id: 88524, Username: "cache-sync-clear-binding", AffCode: "cache-sync-clear-binding",
		Email: "old@example.com", Status: common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(&bindingUser).Error)
	userEpoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(bindingUser.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(bindingUser, userEpoch))
	require.NoError(t, bindingUser.ClearBinding("email"))
	common.RDB = goodClient
	staleEmail, err := goodClient.HGet(context.Background(), getUserCacheKey(bindingUser.Id), "Email").Result()
	require.NoError(t, err)
	assert.Equal(t, "old@example.com", staleEmail)
	recoverSubject(getUserCacheKey(bindingUser.Id))
	require.NoError(t, db.First(&bindingUser, bindingUser.Id).Error)
	assert.Empty(t, bindingUser.Email)

	deleteUser := User{
		Id: 88525, Username: "cache-sync-soft-delete", AffCode: "cache-sync-soft-delete",
		Status: common.UserStatusEnabled,
	}
	deleteToken := Token{
		Id: 88525, UserId: deleteUser.Id, Key: "sk-cache-sync-soft-delete", Name: "cache-sync-soft-delete",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&deleteUser).Error)
	require.NoError(t, db.Create(&deleteToken).Error)
	common.RDB = goodClient
	tokenEpoch, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(deleteToken.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(deleteToken, tokenEpoch))
	require.NoError(t, deleteToken.Delete())
	common.RDB = goodClient
	exists, err := goodClient.Exists(context.Background(), getTokenCacheKey(deleteToken.Key)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
	recoverSubject(getTokenCacheKey(deleteToken.Key))
	var persistedToken Token
	require.NoError(t, db.Unscoped().First(&persistedToken, deleteToken.Id).Error)
	assert.True(t, persistedToken.DeletedAt.Valid)

	common.RDB = goodClient
	userEpoch, err = common.RedisReadCacheEpoch(getUserCacheEpochKey(deleteUser.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(deleteUser, userEpoch))
	require.NoError(t, deleteUser.Delete())
	common.RDB = goodClient
	exists, err = goodClient.Exists(context.Background(), getUserCacheKey(deleteUser.Id)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
	recoverSubject(getUserCacheKey(deleteUser.Id))

	hardUser := User{
		Id: 88526, Username: "cache-sync-hard-delete", AffCode: "cache-sync-hard-delete",
		Status: common.UserStatusEnabled,
	}
	hardToken := Token{
		Id: 88526, UserId: hardUser.Id, Key: "sk-cache-sync-hard-delete", Name: "cache-sync-hard-delete",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&hardUser).Error)
	require.NoError(t, db.Create(&hardToken).Error)
	common.RDB = goodClient
	userEpoch, err = common.RedisReadCacheEpoch(getUserCacheEpochKey(hardUser.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(hardUser, userEpoch))
	tokenEpoch, err = common.RedisReadCacheEpoch(getTokenCacheEpochKey(hardToken.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(hardToken, tokenEpoch))
	require.NoError(t, HardDeleteUserById(hardUser.Id))
	common.RDB = goodClient
	recoverSubject(getUserCacheKey(hardUser.Id))
	recoverSubject(getTokenCacheKey(hardToken.Key))
	assert.ErrorIs(t, db.Unscoped().First(&User{}, hardUser.Id).Error, gorm.ErrRecordNotFound)

	batchUser := User{
		Id: 88527, Username: "cache-sync-batch-delete", AffCode: "cache-sync-batch-delete",
		Status: common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(&batchUser).Error)
	batchKeys := []string{"sk-cache-sync-batch-a", "sk-cache-sync-batch-b"}
	batchIDs := []int{885271, 885272}
	for index := range batchIDs {
		batchToken := Token{
			Id: batchIDs[index], UserId: batchUser.Id, Key: batchKeys[index], Name: batchKeys[index],
			Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
		}
		require.NoError(t, db.Create(&batchToken).Error)
		common.RDB = goodClient
		epoch, epochErr := common.RedisReadCacheEpoch(getTokenCacheEpochKey(batchToken.Key))
		require.NoError(t, epochErr)
		require.NoError(t, cacheSetTokenIfEpoch(batchToken, epoch))
	}
	deleted, err := BatchDeleteTokens(batchIDs, batchUser.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	for _, key := range batchKeys {
		recoverSubject(getTokenCacheKey(key))
	}

	var pending int64
	require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&pending).Error)
	assert.Zero(t, pending)
}

func TestCheckinQuotaCommitsAndQueuesWhenRedisIsUnavailable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	require.NoError(t, db.AutoMigrate(&Checkin{}))
	user := User{
		Id: 88528, Username: "cache-sync-checkin", AffCode: "cache-sync-checkin",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})

	checkin := &Checkin{
		UserId: user.Id, CheckinDate: "2099-01-01", QuotaAwarded: 25, CreatedAt: time.Now().Unix(),
	}
	result, err := userCheckinWithTransaction(checkin, user.Id, checkin.QuotaAwarded)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 1025, user.Quota)
	var pending IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
}

func TestAffiliateQuotaTransferCommitsAndQueuesWhenRedisIsUnavailable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	require.NoError(t, db.AutoMigrate(&AffiliateRewardRecord{}))
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })
	user := User{
		Id: 88529, Username: "cache-sync-affiliate", AffCode: "cache-sync-affiliate",
		Status: common.UserStatusEnabled, Quota: 1000, AffQuota: 300, AffHistoryQuota: 300,
	}
	require.NoError(t, db.Create(&user).Error)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})

	require.NoError(t, user.TransferAffQuotaToQuota(200))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 1200, user.Quota)
	assert.Equal(t, 100, user.AffQuota)
	var pending IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getUserCacheKey(user.Id)).First(&pending).Error)
}

func TestIdentityCacheSyncExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql57", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres96", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			withRoutingTestDB(t, db, test.dbType)
			require.False(t, db.Migrator().HasTable(&IdentityCacheSync{}))
			require.NoError(t, db.AutoMigrate(&IdentityCacheSync{}))
			assert.True(t, db.Migrator().HasColumn(&IdentityCacheSync{}, "deleted_at"))
			assert.True(t, db.Migrator().HasIndex(&IdentityCacheSync{}, "idx_identity_cache_sync_live_pending"))
			t.Cleanup(func() { _ = db.Migrator().DropTable(&IdentityCacheSync{}) })

			now := time.Now()
			var firstVersion int64
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				firstVersion, err = queueUserCacheSyncTx(tx, 88523, now)
				return err
			}))
			var secondVersion int64
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				secondVersion, err = queueUserCacheSyncTx(tx, 88523, now.Add(time.Millisecond))
				return err
			}))
			assert.Equal(t, firstVersion+1, secondVersion)
			var count int64
			require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)
			require.NoError(t, acknowledgeIdentityCacheSyncVersion(
				context.Background(), getUserCacheKey(88523), firstVersion,
			))
			require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)
			require.NoError(t, acknowledgeIdentityCacheSyncVersion(
				context.Background(), getUserCacheKey(88523), secondVersion,
			))
			require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Unscoped().Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)

			var thirdVersion int64
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				thirdVersion, err = queueUserCacheSyncTx(tx, 88523, now.Add(2*time.Millisecond))
				return err
			}))
			assert.Equal(t, secondVersion+1, thirdVersion)
			require.NoError(t, acknowledgeIdentityCacheSyncVersion(
				context.Background(), getUserCacheKey(88523), secondVersion,
			))
			require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)
			require.NoError(t, acknowledgeIdentityCacheSyncVersion(
				context.Background(), getUserCacheKey(88523), thirdVersion,
			))
			require.NoError(t, db.Model(&IdentityCacheSync{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestIdentityCacheTTLIsBounded(t *testing.T) {
	setupCacheEpochRedis(t)
	common.SyncFrequency = 3600
	user := User{Id: 88518, Username: "epoch-ttl-user", Status: common.UserStatusEnabled, Quota: 1000}
	userEpoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(user, userEpoch))
	userTTL, err := common.RDB.TTL(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	assert.Positive(t, userTTL)
	assert.LessOrEqual(t, userTTL, time.Minute)

	token := Token{Id: 88518, Key: "sk-epoch-ttl-token", RemainQuota: 1000}
	tokenEpoch, err := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
	require.NoError(t, err)
	require.NoError(t, cacheSetTokenIfEpoch(token, tokenEpoch))
	tokenTTL, err := common.RDB.TTL(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Positive(t, tokenTTL)
	assert.LessOrEqual(t, tokenTTL, time.Minute)
}

func TestAsyncBillingCacheSyncInvalidatesOnlyAffectedToken(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	setupCacheEpochRedis(t)
	const userID = 88519
	user := User{
		Id: userID, Username: "epoch-target-token", AffCode: "epoch-target-token",
		Status: common.UserStatusEnabled, Quota: 1000,
	}
	require.NoError(t, db.Create(&user).Error)
	userEpoch, err := common.RedisReadCacheEpoch(getUserCacheEpochKey(user.Id))
	require.NoError(t, err)
	require.NoError(t, populateUserCacheIfEpoch(user, userEpoch))

	const tokenCount = 64
	targetTokenID := userID*10 + tokenCount/2
	tokenEpochs := make(map[int]int64, tokenCount)
	tokenKeys := make(map[int]string, tokenCount)
	for index := 0; index < tokenCount; index++ {
		tokenID := userID*10 + index
		token := Token{
			Id: tokenID, UserId: userID, Key: fmt.Sprintf("sk-epoch-target-%d", index),
			Name: fmt.Sprintf("epoch-target-%d", index), Status: common.TokenStatusEnabled,
			RemainQuota: 1000, ExpiredTime: -1,
		}
		require.NoError(t, db.Create(&token).Error)
		epoch, epochErr := common.RedisReadCacheEpoch(getTokenCacheEpochKey(token.Key))
		require.NoError(t, epochErr)
		require.NoError(t, cacheSetTokenIfEpoch(token, epoch))
		tokenEpochs[tokenID] = epoch
		tokenKeys[tokenID] = token.Key
	}
	reservation := AsyncBillingReservation{
		ID: 88519, ReservationKey: "epoch-target-token", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindTask, PublicTaskID: "epoch_target_token",
		State: AsyncBillingReservationStateAccepted, UserID: userID, TokenID: targetTokenID,
		FundingSource: TaskBillingSourceWallet, CacheSyncVersion: 1, CacheSyncPending: true,
		CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
	}
	require.NoError(t, db.Create(&reservation).Error)
	require.NoError(t, SyncAsyncBillingReservationCaches(context.Background(), reservation.ID, time.Now()))

	userExists, err := common.RDB.Exists(context.Background(), getUserCacheKey(userID)).Result()
	require.NoError(t, err)
	assert.Zero(t, userExists)
	for tokenID, key := range tokenKeys {
		exists, existsErr := common.RDB.Exists(context.Background(), getTokenCacheKey(key)).Result()
		require.NoError(t, existsErr)
		if tokenID == targetTokenID {
			assert.Zero(t, exists)
			continue
		}
		assert.Equal(t, int64(1), exists)
		epoch, epochErr := common.RedisReadCacheEpoch(getTokenCacheEpochKey(key))
		require.NoError(t, epochErr)
		assert.Equal(t, tokenEpochs[tokenID], epoch)
	}
	require.NoError(t, db.First(&reservation, reservation.ID).Error)
	assert.False(t, reservation.CacheSyncPending)
	assert.Equal(t, int64(1), reservation.CacheSyncedVersion)
}

func TestHardDeleteUserInvalidatesUserAndTokenCachesAfterCommit(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	require.NoError(t, db.AutoMigrate(&UserOAuthBinding{}))
	setupCacheEpochRedis(t)
	user := User{Id: 88504, Username: "hard-delete-cache", Status: common.UserStatusEnabled, Quota: 1000}
	token := Token{
		Id: user.Id, UserId: user.Id, Key: "sk-hard-delete-cache", Name: "hard-delete-cache",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, common.RedisHSetObj(getUserCacheKey(user.Id), user.ToBaseUser(), time.Minute))
	cachedToken := token
	cachedToken.Clean()
	require.NoError(t, common.RedisHSetObj(getTokenCacheKey(token.Key), &cachedToken, time.Minute))

	require.NoError(t, HardDeleteUserById(user.Id))
	require.NoError(t, HardDeleteUserById(user.Id))
	userExists, err := common.RDB.Exists(context.Background(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, err)
	tokenExists, err := common.RDB.Exists(context.Background(), getTokenCacheKey(token.Key)).Result()
	require.NoError(t, err)
	assert.Zero(t, userExists)
	assert.Zero(t, tokenExists)
	assert.ErrorIs(t, db.Unscoped().First(&User{}, user.Id).Error, gorm.ErrRecordNotFound)
}

func TestTokenDisableCommitsAndQueuesWhenRedisIsUnavailable(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	token := Token{
		Id: 88505, UserId: 88505, Key: "sk-cache-epoch-failure", Name: "cache-epoch-failure",
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(&token).Error)
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1,
	})
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})

	token.Status = common.TokenStatusDisabled
	err := token.SelectUpdate()
	require.NoError(t, err)
	var persisted Token
	require.NoError(t, db.First(&persisted, token.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, persisted.Status)
	var pending IdentityCacheSync
	require.NoError(t, db.Where("subject_key = ?", getTokenCacheKey(token.Key)).First(&pending).Error)
}

func TestCacheEpochExpiresWithoutAllowingAnOldGenerationToRefill(t *testing.T) {
	server := setupCacheEpochRedis(t)
	const userID = 88506
	epochKey := getUserCacheEpochKey(userID)
	cacheKey := getUserCacheKey(userID)
	_, err := common.RedisReadCacheEpoch(epochKey)
	require.NoError(t, err)
	require.NoError(t, common.RedisBumpCacheEpochAndDelete(epochKey, cacheKey))
	staleEpoch, err := common.RedisReadCacheEpoch(epochKey)
	require.NoError(t, err)
	ttl, err := common.RDB.TTL(context.Background(), epochKey).Result()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ttl, 24*time.Hour-time.Second)

	server.FastForward(24*time.Hour + time.Second)
	written, err := common.RedisHSetObjIfCacheEpoch(
		epochKey, staleEpoch, cacheKey,
		&UserBase{Id: userID, Quota: 1000, Status: common.UserStatusEnabled}, time.Minute,
	)
	require.NoError(t, err)
	assert.False(t, written)
	newEpoch, err := common.RedisReadCacheEpoch(epochKey)
	require.NoError(t, err)
	assert.NotEqual(t, staleEpoch, newEpoch)
	written, err = common.RedisHSetObjIfCacheEpoch(
		epochKey, newEpoch, cacheKey,
		&UserBase{Id: userID, Quota: 900, Status: common.UserStatusEnabled}, time.Minute,
	)
	require.NoError(t, err)
	assert.True(t, written)
}

func TestCacheEpochUsesRedisTimeHighWaterAfterFlush(t *testing.T) {
	setupCacheEpochRedis(t)
	const userID = 88515
	epochKey := getUserCacheEpochKey(userID)
	firstEpoch, err := common.RedisReadCacheEpoch(epochKey)
	require.NoError(t, err)
	require.NoError(t, common.RDB.FlushAll(context.Background()).Err())
	secondEpoch, err := common.RedisReadCacheEpoch(epochKey)
	require.NoError(t, err)
	assert.Greater(t, secondEpoch, firstEpoch)
}
