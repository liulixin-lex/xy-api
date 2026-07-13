package controller

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRoutingSub2APIMaxTokenBytes = 16 << 10
	testRoutingSub2APIMaxCacheBytes = 16 << 20
)

type routingSub2APILimitsDoerFunc func(*http.Request) (*http.Response, error)

func (do routingSub2APILimitsDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

func setupRoutingSub2APILimitsCacheTest(t *testing.T) {
	t.Helper()
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-limits-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
}

func setRoutingSub2APILimitsLoginResponse(t *testing.T, responseBody string) {
	t.Helper()
	previous := routingCostHTTPDoer
	routingCostHTTPDoer = routingSub2APILimitsDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(strings.NewReader(responseBody)),
			ContentLength: int64(len(responseBody)),
		}, nil
	})
	t.Cleanup(func() { routingCostHTTPDoer = previous })
}

func newRoutingSub2APILimitsRedisGetClient(t *testing.T, value string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: "pipe",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go func() {
				defer serverConn.Close()
				reader := bufio.NewReader(serverConn)
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				argumentCount, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
				if err != nil {
					return
				}
				for range argumentCount {
					lengthLine, readErr := reader.ReadString('\n')
					if readErr != nil {
						return
					}
					length, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
					if parseErr != nil || length < 0 {
						return
					}
					argument := make([]byte, length+2)
					if _, readErr = io.ReadFull(reader, argument); readErr != nil {
						return
					}
				}
				_, _ = fmt.Fprintf(serverConn, "$%d\r\n%s\r\n", len(value), value)
			}()
			return clientConn, nil
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRoutingSub2APIJWTRejectsOversizedConfiguredToken(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	token, err := routingSub2APIJWT(context.Background(), model.RoutingChannelBinding{ChannelID: 1}, model.RoutingCredentials{
		Sub2APIToken: strings.Repeat("x", testRoutingSub2APIMaxTokenBytes+1),
	})

	require.Error(t, err)
	assert.Empty(t, token)
}

func TestLoginRoutingSub2APIRejectsOversizedToken(t *testing.T) {
	oversizedToken := strings.Repeat("x", testRoutingSub2APIMaxTokenBytes+1)
	responseBody := fmt.Sprintf(`{"code":0,"data":{"token":%q,"expires_in":3600}}`, oversizedToken)
	setRoutingSub2APILimitsLoginResponse(t, responseBody)

	token, ttl, err := loginRoutingSub2API(context.Background(), model.RoutingChannelBinding{
		ChannelID: 2,
		BaseURL:   "https://routing.example.com",
	}, model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	})

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Zero(t, ttl)
}

func TestLoginRoutingSub2APICalculatesBoundedCacheTTLWithoutOverflow(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn string
		wantTTL   time.Duration
	}{
		{name: "missing expiry keeps compatibility fallback", wantTTL: time.Hour},
		{name: "short expiry is not cached", expiresIn: "30", wantTTL: 0},
		{name: "buffer stays below real expiry", expiresIn: "90", wantTTL: 30 * time.Second},
		{name: "huge expiry is capped before duration conversion", expiresIn: "9223372036854775807", wantTTL: 24*time.Hour - time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expiresField := ""
			if tt.expiresIn != "" {
				expiresField = `,"expires_in":` + tt.expiresIn
			}
			setRoutingSub2APILimitsLoginResponse(t, `{"code":0,"data":{"token":"token"`+expiresField+`}}`)

			token, ttl, err := loginRoutingSub2API(context.Background(), model.RoutingChannelBinding{
				ChannelID: 3,
				BaseURL:   "https://routing.example.com",
			}, model.RoutingCredentials{
				Sub2APIEmail:    "admin@example.com",
				Sub2APIPassword: "password",
			})

			require.NoError(t, err)
			assert.Equal(t, "token", token)
			assert.Equal(t, tt.wantTTL, ttl)
		})
	}
}

func TestRoutingSub2APIJWTDoesNotCacheTokenPastShortDeclaredExpiry(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	setRoutingSub2APILimitsLoginResponse(t, `{"code":0,"data":{"token":"short-token","expires_in":30}}`)

	token, err := routingSub2APIJWT(context.Background(), model.RoutingChannelBinding{
		ChannelID: 4,
		BaseURL:   "https://routing.example.com",
	}, model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	})

	require.NoError(t, err)
	assert.Equal(t, "short-token", token)
	assert.Empty(t, routingSub2APICachedJWTForTest(4))
}

func TestRoutingSub2APIJWTCacheEnforcesCiphertextByteBudget(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	token := strings.Repeat("x", testRoutingSub2APIMaxTokenBytes)
	for channelID := 1; channelID <= 800; channelID++ {
		setRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(channelID), token, time.Hour+time.Duration(channelID)*time.Second)
	}

	routingSub2APIJWTCache.Lock()
	cacheEntries := len(routingSub2APIJWTCache.values)
	cacheBytes := 0
	for _, entry := range routingSub2APIJWTCache.values {
		cacheBytes += len(entry.Ciphertext)
	}
	routingSub2APIJWTCache.Unlock()
	stats := RoutingSub2APIJWTCacheRuntimeStats()

	assert.Less(t, cacheEntries, 800)
	assert.LessOrEqual(t, cacheBytes, testRoutingSub2APIMaxCacheBytes)
	assert.Equal(t, cacheEntries, stats.Entries)
	assert.Equal(t, cacheBytes, stats.Bytes)
	assert.Positive(t, stats.Evictions)
}

func TestRoutingSub2APIJWTCacheStatsTrackCiphertextBytes(t *testing.T) {
	t.Run("overwrite", func(t *testing.T) {
		setupRoutingSub2APILimitsCacheTest(t)
		authKey := routingSub2APITestAuthKey(8)
		setRoutingSub2APICachedJWT(context.Background(), authKey, "short-token", time.Hour)
		setRoutingSub2APICachedJWT(context.Background(), authKey, strings.Repeat("x", 1_024), time.Hour)

		routingSub2APIJWTCache.Lock()
		wantBytes := len(routingSub2APIJWTCache.values[authKey].Ciphertext)
		routingSub2APIJWTCache.Unlock()
		stats := RoutingSub2APIJWTCacheRuntimeStats()

		assert.Equal(t, RoutingSub2APIJWTCacheStats{
			Entries: 1,
			Bytes:   wantBytes,
		}, stats)
	})

	t.Run("expiration", func(t *testing.T) {
		setupRoutingSub2APILimitsCacheTest(t)
		authKey := routingSub2APITestAuthKey(9)
		setRoutingSub2APICachedJWT(context.Background(), authKey, "expiring-token", time.Hour)
		routingSub2APIJWTCache.Lock()
		entry := routingSub2APIJWTCache.values[authKey]
		entry.ExpiresAt = common.GetTimestamp()
		routingSub2APIJWTCache.values[authKey] = entry
		routingSub2APIJWTCache.Unlock()

		_, found := getRoutingSub2APICachedJWT(context.Background(), authKey)
		stats := RoutingSub2APIJWTCacheRuntimeStats()

		assert.False(t, found)
		assert.Equal(t, RoutingSub2APIJWTCacheStats{Expirations: 1}, stats)
	})

	t.Run("eviction", func(t *testing.T) {
		setupRoutingSub2APILimitsCacheTest(t)
		routingSub2APIJWTCache.Lock()
		routingSub2APIMaxJWTEntries = 1
		routingSub2APIJWTCache.Unlock()
		setRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(10), "old-token", time.Hour)
		setRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(11), "new-token", 2*time.Hour)

		routingSub2APIJWTCache.Lock()
		wantBytes := 0
		for _, entry := range routingSub2APIJWTCache.values {
			wantBytes += len(entry.Ciphertext)
		}
		routingSub2APIJWTCache.Unlock()
		stats := RoutingSub2APIJWTCacheRuntimeStats()

		assert.Equal(t, RoutingSub2APIJWTCacheStats{
			Entries:   1,
			Bytes:     wantBytes,
			Evictions: 1,
		}, stats)
	})
}

func TestSetRoutingSub2APICachedJWTSkipsNonPositiveTTLWithoutCounterSideEffects(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)

	setRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(5), "token", 0)

	assert.Empty(t, routingSub2APICachedJWTForTest(5))
	assert.Equal(t, RoutingSub2APIJWTCacheStats{}, RoutingSub2APIJWTCacheRuntimeStats())
}

func TestRoutingSub2APIRedisReadRejectsOversizedCachedToken(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	oversizedToken := strings.Repeat("x", testRoutingSub2APIMaxTokenBytes+1)
	encrypted, err := common.EncryptAESGCMString(oversizedToken)
	require.NoError(t, err)
	redisClient := newRoutingSub2APILimitsRedisGetClient(t, encrypted)
	common.RedisEnabled = true
	common.RDB = redisClient

	token, ok := getRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(6))

	assert.False(t, ok)
	assert.Empty(t, token)
}

func TestRoutingSub2APIRedisWriteRejectsOversizedTokenBeforeNetworkAccess(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	var dialCalls atomic.Int32
	redisClient := redis.NewClient(&redis.Options{
		Addr: "unused:0",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, fmt.Errorf("unexpected Redis network access")
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	common.RedisEnabled = true
	common.RDB = redisClient

	setRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(7), strings.Repeat("x", testRoutingSub2APIMaxTokenBytes+1), time.Hour)

	assert.Zero(t, dialCalls.Load())
	assert.Empty(t, routingSub2APICachedJWTForTest(7))
}

func TestRoutingSub2APIRedisReadFailureDoesNotFallbackToLocalJWT(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	authKey := routingSub2APITestAuthKey(12)
	setRoutingSub2APICachedJWT(context.Background(), authKey, "local-jwt", time.Hour)
	redisClient := redis.NewClient(&redis.Options{
		Addr: "unavailable:0",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("redis unavailable")
		},
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	common.RedisEnabled = true
	common.RDB = redisClient

	token, ok := getRoutingSub2APICachedJWT(context.Background(), authKey)

	assert.False(t, ok)
	assert.Empty(t, token)
}

func TestRoutingSub2APILocalRetiredAndGenerationStateStayBounded(t *testing.T) {
	setupRoutingSub2APILimitsCacheTest(t)
	routingSub2APILoginCoordinator.Lock()
	routingSub2APIMaxRetired = 2
	routingSub2APILoginCoordinator.Unlock()
	for channelID := 20; channelID < 24; channelID++ {
		ciphertext := fmt.Sprintf("ciphertext-%d", channelID)
		invalidateRoutingSub2APIJWT(context.Background(), model.RoutingChannelBinding{
			ID: channelID, ChannelID: channelID, UpstreamType: model.RoutingUpstreamTypeSub2API,
			BaseURL: "https://routing.example.com", EncCredentials: &ciphertext,
			KeyVersion: model.RoutingCredentialKeyVersion,
		})
	}

	routingSub2APILoginCoordinator.RLock()
	retiredEntries := len(routingSub2APILoginCoordinator.retired)
	generationEntries := len(routingSub2APILoginCoordinator.generations)
	routingSub2APILoginCoordinator.RUnlock()
	assert.LessOrEqual(t, retiredEntries, 2)
	assert.LessOrEqual(t, generationEntries, 2)
}
