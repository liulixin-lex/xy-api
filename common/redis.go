package common

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

var RDB *redis.Client
var RedisEnabled = true

func RedisKeyCacheSeconds() int {
	return SyncFrequency
}

// InitRedisClient This function is called after init()
func InitRedisClient() (err error) {
	if os.Getenv("REDIS_CONN_STRING") == "" {
		RedisEnabled = false
		SysLog("REDIS_CONN_STRING not set, Redis is not enabled")
		return nil
	}
	if os.Getenv("SYNC_FREQUENCY") == "" {
		SysLog("SYNC_FREQUENCY not set, use default value 60")
		SyncFrequency = 60
	}
	SysLog("Redis is enabled")
	opt, err := redis.ParseURL(os.Getenv("REDIS_CONN_STRING"))
	if err != nil {
		FatalLog("failed to parse Redis connection string: " + err.Error())
	}
	opt.PoolSize = GetEnvOrDefault("REDIS_POOL_SIZE", 10)
	RDB = redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = RDB.Ping(ctx).Result()
	if err != nil {
		FatalLog("Redis ping test failed: " + err.Error())
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis connected to %s", opt.Addr))
		SysLog(fmt.Sprintf("Redis database: %d", opt.DB))
	}
	return err
}

func ParseRedisOption() *redis.Options {
	opt, err := redis.ParseURL(os.Getenv("REDIS_CONN_STRING"))
	if err != nil {
		FatalLog("failed to parse Redis connection string: " + err.Error())
	}
	return opt
}

func RedisSet(key string, value string, expiration time.Duration) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis SET: key=%s, value=%s, expiration=%v", key, value, expiration))
	}
	ctx := context.Background()
	return RDB.Set(ctx, key, value, expiration).Err()
}

func RedisGet(key string) (string, error) {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis GET: key=%s", key))
	}
	ctx := context.Background()
	val, err := RDB.Get(ctx, key).Result()
	return val, err
}

//func RedisExpire(key string, expiration time.Duration) error {
//	ctx := context.Background()
//	return RDB.Expire(ctx, key, expiration).Err()
//}
//
//func RedisGetEx(key string, expiration time.Duration) (string, error) {
//	ctx := context.Background()
//	return RDB.GetSet(ctx, key, expiration).Result()
//}

func RedisDel(key string) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis DEL: key=%s", key))
	}
	ctx := context.Background()
	return RDB.Del(ctx, key).Err()
}

func RedisDelKey(key string) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis DEL Key: key=%s", key))
	}
	ctx := context.Background()
	return RDB.Del(ctx, key).Err()
}

func RedisHSetObj(key string, obj interface{}, expiration time.Duration) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HSET: key=%s, obj=%+v, expiration=%v", key, obj, expiration))
	}
	ctx := context.Background()

	data, err := redisHashObjectFields(obj)
	if err != nil {
		return err
	}

	txn := RDB.TxPipeline()
	txn.HSet(ctx, key, data)

	// 只有在 expiration 大于 0 时才设置过期时间
	if expiration > 0 {
		txn.Expire(ctx, key, expiration)
	}

	_, err = txn.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to execute transaction: %w", err)
	}
	return nil
}

func redisHashObjectFields(obj interface{}) (map[string]interface{}, error) {
	value := reflect.ValueOf(obj)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil, fmt.Errorf("obj must be a non-nil pointer to a struct, got %T", obj)
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("obj must be a pointer to a struct, got %T", obj)
	}
	valueType := value.Type()
	data := make(map[string]interface{}, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := valueType.Field(index)
		fieldValue := value.Field(index)
		if field.Type.String() == "gorm.DeletedAt" {
			continue
		}
		if fieldValue.Kind() == reflect.Ptr {
			if fieldValue.IsNil() {
				data[field.Name] = ""
				continue
			}
			fieldValue = fieldValue.Elem()
		}
		if fieldValue.Kind() == reflect.Bool {
			data[field.Name] = strconv.FormatBool(fieldValue.Bool())
			continue
		}
		data[field.Name] = fmt.Sprintf("%v", fieldValue.Interface())
	}
	return data, nil
}

const redisNextCacheEpochLua = `
local function next_cache_epoch(sequence_key)
  local redis_time = redis.call('TIME')
  local wall_epoch = tonumber(redis_time[1]) * 1000000 + tonumber(redis_time[2])
  local previous_epoch = tonumber(redis.call('GET', sequence_key) or '') or 0
  local next_epoch = wall_epoch
  if previous_epoch >= next_epoch then next_epoch = previous_epoch + 1 end
  local encoded_epoch = string.format('%.0f', next_epoch)
  redis.call('SET', sequence_key, encoded_epoch)
  return encoded_epoch
end
`

func RedisReadCacheEpoch(key string) (int64, error) {
	if RDB == nil || key == "" {
		return 0, errors.New("redis cache epoch is unavailable")
	}
	const script = redisNextCacheEpochLua + `
local epoch = redis.call('GET', KEYS[1])
if epoch then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  return epoch
end
epoch = next_cache_epoch(KEYS[2])
redis.call('SET', KEYS[1], epoch, 'PX', ARGV[1])
return epoch
`
	epoch, err := RDB.Eval(context.Background(), script,
		[]string{key, "cache_epoch:sequence"}, cacheEpochTTL().Milliseconds()).Int64()
	if err != nil {
		return 0, err
	}
	if epoch <= 0 {
		return 0, errors.New("redis cache epoch is invalid")
	}
	return epoch, nil
}

func RedisBumpCacheEpochAndDelete(epochKey, cacheKey string) error {
	return RedisBumpCacheEpochAndDeleteContext(context.Background(), epochKey, cacheKey)
}

func RedisBumpCacheEpochAndDeleteContext(ctx context.Context, epochKey, cacheKey string) error {
	if RDB == nil || epochKey == "" || cacheKey == "" {
		return errors.New("redis cache fence is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const script = redisNextCacheEpochLua + `
local epoch = next_cache_epoch(KEYS[3])
redis.call('SET', KEYS[1], epoch, 'PX', ARGV[1])
redis.call('DEL', KEYS[2])
return epoch
`
	_, err := RDB.Eval(ctx, script,
		[]string{epochKey, cacheKey, "cache_epoch:sequence"}, cacheEpochTTL().Milliseconds()).Result()
	return err
}

func cacheEpochTTL() time.Duration {
	ttl := 2 * time.Duration(RedisKeyCacheSeconds()) * time.Second
	if ttl < 24*time.Hour {
		return 24 * time.Hour
	}
	return ttl
}

func RedisHSetObjIfCacheEpoch(
	epochKey string,
	expectedEpoch int64,
	cacheKey string,
	obj interface{},
	expiration time.Duration,
) (bool, error) {
	if RDB == nil || epochKey == "" || cacheKey == "" || expectedEpoch <= 0 {
		return false, errors.New("redis cache fence is unavailable")
	}
	data, err := redisHashObjectFields(obj)
	if err != nil {
		return false, err
	}
	arguments := make([]interface{}, 0, 2+len(data)*2)
	arguments = append(arguments, strconv.FormatInt(expectedEpoch, 10), expiration.Milliseconds())
	for field, value := range data {
		arguments = append(arguments, field, value)
	}
	const script = `
local epoch = redis.call('GET', KEYS[1])
if not epoch then return 0 end
if epoch ~= ARGV[1] then return 0 end
for index = 3, #ARGV, 2 do
  redis.call('HSET', KEYS[2], ARGV[index], ARGV[index + 1])
end
local ttl = tonumber(ARGV[2])
if ttl and ttl > 0 then redis.call('PEXPIRE', KEYS[2], ttl) end
return 1
`
	written, err := RDB.Eval(context.Background(), script, []string{epochKey, cacheKey}, arguments...).Int64()
	if err != nil {
		return false, err
	}
	return written == 1, nil
}

func RedisHSetFieldIfCacheEpoch(
	epochKey string,
	expectedEpoch int64,
	cacheKey string,
	field string,
	value interface{},
) (bool, error) {
	if RDB == nil || epochKey == "" || cacheKey == "" || field == "" || expectedEpoch <= 0 {
		return false, errors.New("redis cache fence is unavailable")
	}
	const script = `
local epoch = redis.call('GET', KEYS[1])
if not epoch then return 0 end
if epoch ~= ARGV[1] then return 0 end
local ttl = redis.call('PTTL', KEYS[2])
if ttl <= 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[2], ARGV[3])
return 1
`
	written, err := RDB.Eval(context.Background(), script, []string{epochKey, cacheKey},
		strconv.FormatInt(expectedEpoch, 10), field, value).Int64()
	if err != nil {
		return false, err
	}
	return written == 1, nil
}

func RedisHIncrByAndAdvanceCacheEpoch(
	epochKey string,
	expectedEpoch int64,
	cacheKey string,
	field string,
	delta int64,
) (bool, error) {
	if RDB == nil || epochKey == "" || cacheKey == "" || field == "" || expectedEpoch <= 0 {
		return false, errors.New("redis cache fence is unavailable")
	}
	const script = redisNextCacheEpochLua + `
local epoch = redis.call('GET', KEYS[1])
if not epoch then return 0 end
if epoch ~= ARGV[1] then return 0 end
local next_epoch = next_cache_epoch(KEYS[3])
redis.call('SET', KEYS[1], next_epoch, 'PX', ARGV[4])
local ttl = redis.call('PTTL', KEYS[2])
if ttl > 0 then
  local incremented = redis.pcall('HINCRBY', KEYS[2], ARGV[2], ARGV[3])
  if type(incremented) == 'table' and incremented.err then
    redis.call('DEL', KEYS[2])
  end
else
  redis.call('DEL', KEYS[2])
end
return 1
`
	written, err := RDB.Eval(context.Background(), script,
		[]string{epochKey, cacheKey, "cache_epoch:sequence"},
		strconv.FormatInt(expectedEpoch, 10), field, delta, cacheEpochTTL().Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return written == 1, nil
}

func RedisHGetObj(key string, obj interface{}) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HGETALL: key=%s", key))
	}
	ctx := context.Background()

	result, err := RDB.HGetAll(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to load hash from Redis: %w", err)
	}

	if len(result) == 0 {
		return fmt.Errorf("key %s not found in Redis", key)
	}

	// Handle both pointer and non-pointer values
	val := reflect.ValueOf(obj)
	if val.Kind() != reflect.Ptr {
		return fmt.Errorf("obj must be a pointer to a struct, got %T", obj)
	}

	v := val.Elem()
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("obj must be a pointer to a struct, got pointer to %T", v.Interface())
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldName := field.Name
		if value, ok := result[fieldName]; ok {
			fieldValue := v.Field(i)

			// Handle pointer types
			if fieldValue.Kind() == reflect.Ptr {
				if value == "" {
					continue
				}
				if fieldValue.IsNil() {
					fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
				}
				fieldValue = fieldValue.Elem()
			}

			// Enhanced type handling for Token struct
			switch fieldValue.Kind() {
			case reflect.String:
				fieldValue.SetString(value)
			case reflect.Int, reflect.Int64:
				intValue, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return fmt.Errorf("failed to parse int field %s: %w", fieldName, err)
				}
				fieldValue.SetInt(intValue)
			case reflect.Bool:
				boolValue, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("failed to parse bool field %s: %w", fieldName, err)
				}
				fieldValue.SetBool(boolValue)
			case reflect.Struct:
				// Special handling for gorm.DeletedAt
				if fieldValue.Type().String() == "gorm.DeletedAt" {
					if value != "" {
						timeValue, err := time.Parse(time.RFC3339, value)
						if err != nil {
							return fmt.Errorf("failed to parse DeletedAt field %s: %w", fieldName, err)
						}
						fieldValue.Set(reflect.ValueOf(gorm.DeletedAt{Time: timeValue, Valid: true}))
					}
				}
			default:
				return fmt.Errorf("unsupported field type: %s for field %s", fieldValue.Kind(), fieldName)
			}
		}
	}

	return nil
}

// RedisIncr Add this function to handle atomic increments
func RedisIncr(key string, delta int64) error {
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis INCR: key=%s, delta=%d", key, delta))
	}
	// 检查键的剩余生存时间
	ttlCmd := RDB.TTL(context.Background(), key)
	ttl, err := ttlCmd.Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get TTL: %w", err)
	}

	// 只有在 key 存在且有 TTL 时才需要特殊处理
	if ttl > 0 {
		ctx := context.Background()
		// 开始一个Redis事务
		txn := RDB.TxPipeline()

		// 减少余额
		decrCmd := txn.IncrBy(ctx, key, delta)
		if err := decrCmd.Err(); err != nil {
			return err // 如果减少失败，则直接返回错误
		}

		// 重新设置过期时间，使用原来的过期时间
		txn.Expire(ctx, key, ttl)

		// 执行事务
		_, err = txn.Exec(ctx)
		return err
	}
	return nil
}
