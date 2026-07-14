package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func getTokenCacheKey(key string) string {
	return fmt.Sprintf("token:%s", common.GenerateHMAC(key))
}

func getTokenCacheEpochKey(key string) string {
	return fmt.Sprintf("cache_epoch:token:%s", common.GenerateHMAC(key))
}

func cacheSetTokenIfEpoch(token Token, epoch int64) error {
	cacheKey := getTokenCacheKey(token.Key)
	epochKey := getTokenCacheEpochKey(token.Key)
	token.Clean()
	_, err := common.RedisHSetObjIfCacheEpoch(
		epochKey, epoch, cacheKey, &token, identityCacheTTL(),
	)
	return err
}

func cacheDeleteToken(key string) error {
	return common.RedisBumpCacheEpochAndDelete(getTokenCacheEpochKey(key), getTokenCacheKey(key))
}

func cacheIncrTokenQuota(key string, increment int64, epoch int64) error {
	advanced, err := common.RedisHIncrByAndAdvanceCacheEpoch(
		getTokenCacheEpochKey(key), epoch, getTokenCacheKey(key), constant.TokenFiledRemainQuota, increment,
	)
	if err == nil && advanced {
		return nil
	}
	if err == nil {
		err = errors.New("token quota cache epoch changed concurrently")
	}
	if invalidateErr := cacheDeleteToken(key); invalidateErr != nil {
		return errors.Join(err, fmt.Errorf("invalidate token quota cache: %w", invalidateErr))
	}
	common.SysError("token quota cache epoch conflict; invalidated cache: " + err.Error())
	return nil
}

func cacheDecrTokenQuota(key string, decrement int64, epoch int64) error {
	return cacheIncrTokenQuota(key, -decrement, epoch)
}

// CacheGetTokenByKey 从缓存中获取 token，如果缓存中不存在，则从数据库中获取
func cacheGetTokenByKey(key string) (*Token, error) {
	hmacKey := common.GenerateHMAC(key)
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var token Token
	err := common.RedisHGetObj(fmt.Sprintf("token:%s", hmacKey), &token)
	if err != nil {
		return nil, err
	}
	token.Key = key
	return &token, nil
}
