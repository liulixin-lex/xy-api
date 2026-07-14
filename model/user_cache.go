package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"

	"github.com/bytedance/gopkg/util/gopool"
)

var runAsyncCacheBackfill = func(task func()) {
	gopool.Go(task)
}

func identityCacheTTL() time.Duration {
	ttl := time.Duration(common.RedisKeyCacheSeconds()) * time.Second
	if ttl <= 0 || ttl > time.Minute {
		return time.Minute
	}
	return ttl
}

// UserBase struct remains the same as it represents the cached data structure
type UserBase struct {
	Id       int    `json:"id"`
	Group    string `json:"group"`
	Email    string `json:"email"`
	Quota    int    `json:"quota"`
	Status   int    `json:"status"`
	Username string `json:"username"`
	Setting  string `json:"setting"`
}

func (user *UserBase) WriteContext(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyUserGroup, user.Group)
	common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
	common.SetContextKey(c, constant.ContextKeyUserStatus, user.Status)
	common.SetContextKey(c, constant.ContextKeyUserEmail, user.Email)
	common.SetContextKey(c, constant.ContextKeyUserName, user.Username)
	common.SetContextKey(c, constant.ContextKeyUserSetting, user.GetSetting())
}

func (user *UserBase) GetSetting() dto.UserSetting {
	setting := dto.UserSetting{}
	if user.Setting != "" {
		err := common.Unmarshal([]byte(user.Setting), &setting)
		if err != nil {
			common.SysLog("failed to unmarshal setting: " + err.Error())
		}
	}
	return setting
}

// getUserCacheKey returns the key for user cache
func getUserCacheKey(userId int) string {
	return fmt.Sprintf("user:%d", userId)
}

func getUserCacheEpochKey(userId int) string {
	return fmt.Sprintf("cache_epoch:user:%d", userId)
}

// invalidateUserCache clears user cache
func invalidateUserCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	return common.RedisBumpCacheEpochAndDelete(getUserCacheEpochKey(userId), getUserCacheKey(userId))
}

// InvalidateUserCache is the exported version of invalidateUserCache.
// 供 controller 等上层包在用户状态变更（如禁用、删除、角色变更）后主动清理缓存。
func InvalidateUserCache(userId int) error {
	return invalidateUserCache(userId)
}

func populateUserCacheIfEpoch(user User, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := common.RedisHSetObjIfCacheEpoch(
		getUserCacheEpochKey(user.Id), epoch, getUserCacheKey(user.Id), user.ToBaseUser(),
		identityCacheTTL(),
	)
	return err
}

func updateUserCache(user User) error {
	return invalidateUserCache(user.Id)
}

// GetUserCache gets complete user cache from hash
func GetUserCache(userId int) (userCache *UserBase, err error) {
	var user *User
	var fromDB bool
	var cacheEpoch int64
	cacheEpochReady := false
	defer func() {
		// Update Redis cache asynchronously on successful DB read
		if shouldUpdateRedis(fromDB, err) && user != nil && cacheEpochReady {
			runAsyncCacheBackfill(func() {
				if err := populateUserCacheIfEpoch(*user, cacheEpoch); err != nil {
					common.SysLog("failed to update user status cache: " + err.Error())
				}
			})
		}
	}()

	// Try getting from Redis first
	userCache, err = cacheGetUserBase(userId)
	if err == nil {
		return userCache, nil
	}
	if common.RedisEnabled {
		cacheEpoch, err = common.RedisReadCacheEpoch(getUserCacheEpochKey(userId))
		if err == nil {
			cacheEpochReady = true
		}
	}

	// If Redis fails, get from DB
	fromDB = true
	user, err = GetUserById(userId, false)
	if err != nil {
		return nil, err // Return nil and error if DB lookup fails
	}

	// Create cache object from user data
	userCache = &UserBase{
		Id:       user.Id,
		Group:    user.Group,
		Quota:    user.Quota,
		Status:   user.Status,
		Username: user.Username,
		Setting:  user.Setting,
		Email:    user.Email,
	}

	return userCache, nil
}

func cacheGetUserBase(userId int) (*UserBase, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var userCache UserBase
	// Try getting from Redis first
	err := common.RedisHGetObj(getUserCacheKey(userId), &userCache)
	if err != nil {
		return nil, err
	}
	return &userCache, nil
}

// Add atomic quota operations using hash fields
func cacheIncrUserQuota(userId int, delta int64, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	advanced, err := common.RedisHIncrByAndAdvanceCacheEpoch(
		getUserCacheEpochKey(userId), epoch, getUserCacheKey(userId), "Quota", delta,
	)
	if err == nil && advanced {
		return nil
	}
	if err == nil {
		err = errors.New("user quota cache epoch changed concurrently")
	}
	if invalidateErr := invalidateUserCache(userId); invalidateErr != nil {
		return errors.Join(err, fmt.Errorf("invalidate user quota cache: %w", invalidateErr))
	}
	common.SysError("user quota cache epoch conflict; invalidated cache: " + err.Error())
	return nil
}

func cacheDecrUserQuota(userId int, delta int64, epoch int64) error {
	return cacheIncrUserQuota(userId, -delta, epoch)
}

// Helper functions to get individual fields if needed
func getUserGroupCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Group, nil
}

func getUserQuotaCache(userId int) (int, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return 0, err
	}
	return cache.Quota, nil
}

func getUserStatusCache(userId int) (int, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return 0, err
	}
	return cache.Status, nil
}

func getUserNameCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Username, nil
}

func getUserSettingCache(userId int) (dto.UserSetting, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return dto.UserSetting{}, err
	}
	return cache.GetSetting(), nil
}

func updateUserQuotaCacheIfEpoch(userId int, quota int, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := common.RedisHSetFieldIfCacheEpoch(
		getUserCacheEpochKey(userId), epoch, getUserCacheKey(userId), "Quota", fmt.Sprintf("%d", quota),
	)
	return err
}

func updateUserGroupCacheIfEpoch(userId int, group string, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := common.RedisHSetFieldIfCacheEpoch(
		getUserCacheEpochKey(userId), epoch, getUserCacheKey(userId), "Group", group,
	)
	return err
}

func UpdateUserGroupCache(userId int, group string) error {
	if err := invalidateUserCache(userId); err != nil {
		common.SysError("user group committed but cache fence failed: " + err.Error())
	}
	return nil
}

func updateUserNameCacheIfEpoch(userId int, username string, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := common.RedisHSetFieldIfCacheEpoch(
		getUserCacheEpochKey(userId), epoch, getUserCacheKey(userId), "Username", username,
	)
	return err
}

func updateUserSettingCacheIfEpoch(userId int, setting string, epoch int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := common.RedisHSetFieldIfCacheEpoch(
		getUserCacheEpochKey(userId), epoch, getUserCacheKey(userId), "Setting", setting,
	)
	return err
}

// GetUserLanguage returns the user's language preference from cache
// Uses the existing GetUserCache mechanism for efficiency
func GetUserLanguage(userId int) string {
	userCache, err := GetUserCache(userId)
	if err != nil {
		return ""
	}
	return userCache.GetSetting().Language
}
