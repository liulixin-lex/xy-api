package model

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedQuotaPreConsumeSubject(t *testing.T, id int, userQuota int, tokenQuota int, unlimited bool) (*User, *Token) {
	t.Helper()
	user := &User{
		Id: id, Username: "quota-preconsume-user-" + common.GetRandomString(8),
		AffCode: "quota-preconsume-aff-" + common.GetRandomString(8),
		Status:  common.UserStatusEnabled, Quota: userQuota,
	}
	require.NoError(t, DB.Create(user).Error)
	token := &Token{
		Id: id, UserId: id, Key: "sk-quota-preconsume-" + common.GetRandomString(12),
		Name: "quota-preconsume-token", Status: common.TokenStatusEnabled,
		RemainQuota: tokenQuota, UnlimitedQuota: unlimited, ExpiredTime: -1,
	}
	require.NoError(t, DB.Create(token).Error)
	return user, token
}

func TestPreConsumeUserAndTokenQuotaConcurrentOnlyOneSucceeds(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	user, token := seedQuotaPreConsumeSubject(t, 99701, 100, 100, false)

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- PreConsumeUserAndTokenQuota(user.Id, token.Id, token.Key, 80, true)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		failures++
		assert.True(t, errors.Is(err, ErrUserQuotaInsufficient) || errors.Is(err, ErrTokenQuotaInsufficient))
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, failures)

	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, 20, token.RemainQuota)
	assert.Equal(t, 80, token.UsedQuota)
}

func TestPreConsumeUserAndTokenQuotaRollsBackPartialMutation(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	tests := []struct {
		name          string
		id            int
		userQuota     int
		tokenQuota    int
		expectedError error
	}{
		{name: "wallet insufficient", id: 99702, userQuota: 50, tokenQuota: 100, expectedError: ErrUserQuotaInsufficient},
		{name: "token insufficient", id: 99703, userQuota: 100, tokenQuota: 50, expectedError: ErrTokenQuotaInsufficient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			user, token := seedQuotaPreConsumeSubject(t, test.id, test.userQuota, test.tokenQuota, false)
			err := PreConsumeUserAndTokenQuota(user.Id, token.Id, token.Key, 80, true)
			require.ErrorIs(t, err, test.expectedError)

			require.NoError(t, DB.First(user, user.Id).Error)
			require.NoError(t, DB.First(token, token.Id).Error)
			assert.Equal(t, test.userQuota, user.Quota)
			assert.Equal(t, test.tokenQuota, token.RemainQuota)
			assert.Zero(t, token.UsedQuota)
		})
	}
}

func TestPreConsumeUserAndTokenQuotaPreservesUnlimitedTokenBehavior(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	user, token := seedQuotaPreConsumeSubject(t, 99704, 100, 0, true)

	require.NoError(t, PreConsumeUserAndTokenQuota(user.Id, token.Id, token.Key, 80, true))
	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, -80, token.RemainQuota)
	assert.Equal(t, 80, token.UsedQuota)
}

func TestPreConsumeUserAndTokenQuotaRejectsMismatchedTokenOwner(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	walletOwner, _ := seedQuotaPreConsumeSubject(t, 99709, 100, 100, false)
	tokenOwner, foreignToken := seedQuotaPreConsumeSubject(t, 99710, 100, 100, false)

	err := PreConsumeUserAndTokenQuota(walletOwner.Id, foreignToken.Id, foreignToken.Key, 80, true)
	require.ErrorIs(t, err, ErrTokenQuotaInsufficient)

	require.NoError(t, DB.First(walletOwner, walletOwner.Id).Error)
	require.NoError(t, DB.First(tokenOwner, tokenOwner.Id).Error)
	require.NoError(t, DB.First(foreignToken, foreignToken.Id).Error)
	assert.Equal(t, 100, walletOwner.Quota)
	assert.Equal(t, 100, tokenOwner.Quota)
	assert.Equal(t, 100, foreignToken.RemainQuota)
	assert.Zero(t, foreignToken.UsedQuota)
}

func TestPreConsumeUserAndTokenQuotaKeepsDurableSyncWhenRedisUnavailable(t *testing.T) {
	truncateTables(t)
	useUnavailableRedisForMutationTest(t)
	user, token := seedQuotaPreConsumeSubject(t, 99705, 100, 100, false)

	require.NoError(t, PreConsumeUserAndTokenQuota(user.Id, token.Id, token.Key, 80, true))
	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	assert.Equal(t, 20, user.Quota)
	assert.Equal(t, 20, token.RemainQuota)
	assert.Equal(t, 80, token.UsedQuota)

	var subjects []string
	require.NoError(t, DB.Model(&IdentityCacheSync{}).Order("subject_key asc").Pluck("subject_key", &subjects).Error)
	assert.ElementsMatch(t, []string{getUserCacheKey(user.Id), getTokenCacheKey(token.Key)}, subjects)
}

func TestQuotaRefundGuardsPreventOverflowAndUnderflow(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	user, token := seedQuotaPreConsumeSubject(t, 99706, common.MaxQuota, common.MaxQuota, false)
	token.UsedQuota = 1
	require.NoError(t, DB.Model(token).Update("used_quota", token.UsedQuota).Error)

	require.ErrorIs(t, RefundUserQuota(user.Id, 1), ErrQuotaRefundOutOfRange)
	require.ErrorIs(t, RefundTokenQuota(token.Id, token.Key, 1), ErrQuotaRefundOutOfRange)
	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	assert.Equal(t, common.MaxQuota, user.Quota)
	assert.Equal(t, common.MaxQuota, token.RemainQuota)
	assert.Equal(t, 1, token.UsedQuota)

	exactUser, exactToken := seedQuotaPreConsumeSubject(t, 99707, common.MaxQuota-1, common.MaxQuota-1, false)
	exactToken.UsedQuota = 1
	require.NoError(t, DB.Model(exactToken).Update("used_quota", exactToken.UsedQuota).Error)
	require.NoError(t, RefundUserQuota(exactUser.Id, 1))
	require.NoError(t, RefundTokenQuota(exactToken.Id, exactToken.Key, 1))
	require.NoError(t, DB.First(exactUser, exactUser.Id).Error)
	require.NoError(t, DB.First(exactToken, exactToken.Id).Error)
	assert.Equal(t, common.MaxQuota, exactUser.Quota)
	assert.Equal(t, common.MaxQuota, exactToken.RemainQuota)
	assert.Zero(t, exactToken.UsedQuota)
}

func TestPostSettleQuotaDeltasMayRemainNegative(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	user, token := seedQuotaPreConsumeSubject(t, 99708, 10, 10, false)

	require.NoError(t, DecreaseUserQuota(user.Id, 20, false))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 20))
	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	assert.Equal(t, -10, user.Quota)
	assert.Equal(t, -10, token.RemainQuota)
	assert.Equal(t, 20, token.UsedQuota)
}

func TestPreConsumeUserAndTokenQuotaExternalDatabaseCompatibility(t *testing.T) {
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
			if db.Migrator().HasTable(&User{}) || db.Migrator().HasTable(&Token{}) {
				t.Skip("refusing to run against external database because quota tables already exist")
			}
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&Token{})
				_ = db.Migrator().DropTable(&User{})
			})

			previousRedisEnabled := common.RedisEnabled
			common.RedisEnabled = false
			t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
			walletOwner, walletToken := seedQuotaPreConsumeSubject(t, 99721, 100, 100, false)
			tokenOwner, foreignUnlimitedToken := seedQuotaPreConsumeSubject(t, 99722, 100, 0, true)
			concurrentUser, concurrentToken := seedQuotaPreConsumeSubject(t, 99723, 100, 100, false)

			err := PreConsumeUserAndTokenQuota(walletOwner.Id, foreignUnlimitedToken.Id, foreignUnlimitedToken.Key, 80, true)
			require.ErrorIs(t, err, ErrTokenQuotaInsufficient)
			require.NoError(t, PreConsumeUserAndTokenQuota(walletOwner.Id, walletToken.Id, walletToken.Key, 80, true))
			require.NoError(t, PreConsumeUserAndTokenQuota(tokenOwner.Id, foreignUnlimitedToken.Id, foreignUnlimitedToken.Key, 80, true))
			start := make(chan struct{})
			results := make(chan error, 2)
			var workers sync.WaitGroup
			for range 2 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					results <- PreConsumeUserAndTokenQuota(
						concurrentUser.Id, concurrentToken.Id, concurrentToken.Key, 80, true,
					)
				}()
			}
			close(start)
			workers.Wait()
			close(results)
			successes := 0
			failures := 0
			for resultErr := range results {
				if resultErr == nil {
					successes++
					continue
				}
				failures++
				assert.True(t, errors.Is(resultErr, ErrUserQuotaInsufficient) || errors.Is(resultErr, ErrTokenQuotaInsufficient))
			}
			assert.Equal(t, 1, successes)
			assert.Equal(t, 1, failures)

			require.NoError(t, db.First(walletOwner, walletOwner.Id).Error)
			require.NoError(t, db.First(walletToken, walletToken.Id).Error)
			require.NoError(t, db.First(tokenOwner, tokenOwner.Id).Error)
			require.NoError(t, db.First(foreignUnlimitedToken, foreignUnlimitedToken.Id).Error)
			require.NoError(t, db.First(concurrentUser, concurrentUser.Id).Error)
			require.NoError(t, db.First(concurrentToken, concurrentToken.Id).Error)
			assert.Equal(t, 20, walletOwner.Quota)
			assert.Equal(t, 20, walletToken.RemainQuota)
			assert.Equal(t, 80, walletToken.UsedQuota)
			assert.Equal(t, 20, tokenOwner.Quota)
			assert.Equal(t, -80, foreignUnlimitedToken.RemainQuota)
			assert.Equal(t, 80, foreignUnlimitedToken.UsedQuota)
			assert.Equal(t, 20, concurrentUser.Quota)
			assert.Equal(t, 20, concurrentToken.RemainQuota)
			assert.Equal(t, 80, concurrentToken.UsedQuota)
		})
	}
}
