package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withAuthDatabase(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	model.DB = db
	common.RedisEnabled = false

	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func withStaleAuthRedis(t *testing.T, user *model.User, token *model.Token) {
	t.Helper()

	previousRDB := common.RDB
	previousRedisEnabled := common.RedisEnabled
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RDB = client
	common.RedisEnabled = true

	staleUser := user.ToBaseUser()
	staleUser.Status = common.UserStatusEnabled
	staleUser.PaymentFrozen = false
	require.NoError(t, common.RedisHSetObj(
		fmt.Sprintf("user:%d", user.Id),
		staleUser,
		time.Hour,
	))

	staleToken := *token
	staleToken.Clean()
	require.NoError(t, common.RedisHSetObj(
		fmt.Sprintf("token:%s", common.GenerateHMAC(token.Key)),
		&staleToken,
		time.Hour,
	))

	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousRDB
		common.RedisEnabled = previousRedisEnabled
	})
}

func createAuthToken(t *testing.T, db *gorm.DB, userID int) *model.Token {
	t.Helper()

	token := &model.Token{
		UserId:         userID,
		Key:            "paymentfreezetoken",
		Status:         common.TokenStatusEnabled,
		Name:           "payment-freeze-test",
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}
	require.NoError(t, db.Create(token).Error)
	return token
}

func performAPITokenAuthRequest(
	t *testing.T,
	tokenKey string,
	auth gin.HandlerFunc,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlerCalled := false
	router.GET("/protected", auth, func(c *gin.Context) {
		handlerCalled = true
		c.JSON(http.StatusOK, gin.H{
			"id":            c.GetInt("id"),
			"role":          c.GetInt("role"),
			"group":         c.GetString("group"),
			"context_group": common.GetContextKeyString(c, constant.ContextKeyUserGroup),
		})
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer sk-"+tokenKey)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, handlerCalled
}

func performAccessTokenAuthRequest(
	t *testing.T,
	user *model.User,
	accessToken string,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("payment-freeze-access-token-test"))))
	handlerCalled := false
	router.GET("/protected", UserAuth(), func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("New-Api-User", fmt.Sprintf("%d", user.Id))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, handlerCalled
}

func performSessionAuthRequest(
	t *testing.T,
	user *model.User,
	sessionValues map[string]any,
	auth gin.HandlerFunc,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("payment-freeze-auth-test"))))
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		for key, value := range sessionValues {
			session.Set(key, value)
		}
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	handlerCalled := false
	router.GET("/protected", auth, func(c *gin.Context) {
		handlerCalled = true
		c.JSON(http.StatusOK, gin.H{
			"id":    c.GetInt("id"),
			"role":  c.GetInt("role"),
			"group": c.GetString("group"),
		})
	})

	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	require.Equal(t, http.StatusNoContent, loginRecorder.Code)

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("New-Api-User", fmt.Sprintf("%d", user.Id))
	for _, sessionCookie := range loginRecorder.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, handlerCalled
}

func staleSessionValues(userID int) map[string]any {
	return map[string]any{
		"id":       userID,
		"username": "stale-name",
		"role":     common.RoleRootUser,
		"status":   common.UserStatusEnabled,
		"group":    "stale-group",
	}
}

func TestUserAuthRejectsPaymentFrozenActiveSession(t *testing.T) {
	db := withAuthDatabase(t)
	user := &model.User{
		Username:      "frozen-user",
		Password:      "test-password",
		Role:          common.RoleCommonUser,
		Status:        common.UserStatusEnabled,
		PaymentFrozen: true,
		Group:         "default",
	}
	require.NoError(t, db.Create(user).Error)

	recorder, handlerCalled := performSessionAuthRequest(t, user, staleSessionValues(user.Id), UserAuth())

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.False(t, handlerCalled)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestUserAuthUsesCurrentDatabaseIdentityInsteadOfSessionSnapshot(t *testing.T) {
	db := withAuthDatabase(t)
	user := &model.User{
		Username: "current-user",
		Password: "test-password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "current-group",
	}
	require.NoError(t, db.Create(user).Error)
	sessionValues := staleSessionValues(user.Id)
	sessionValues["status"] = common.UserStatusDisabled

	recorder, handlerCalled := performSessionAuthRequest(t, user, sessionValues, UserAuth())

	require.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"id":%d,"role":%d,"group":"current-group"}`, user.Id, common.RoleCommonUser), recorder.Body.String())
}

func TestAdminAuthRejectsStaleElevatedSessionRole(t *testing.T) {
	db := withAuthDatabase(t)
	user := &model.User{
		Username: "demoted-user",
		Password: "test-password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(user).Error)

	recorder, handlerCalled := performSessionAuthRequest(t, user, staleSessionValues(user.Id), AdminAuth())

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.False(t, handlerCalled)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestAccessTokenAuthRejectsPaymentFrozenUser(t *testing.T) {
	db := withAuthDatabase(t)
	accessToken := "current-access-token"
	user := &model.User{
		Username:      "frozen-access-token-user",
		Password:      "test-password",
		AccessToken:   &accessToken,
		Role:          common.RoleCommonUser,
		Status:        common.UserStatusEnabled,
		PaymentFrozen: true,
		Group:         "default",
	}
	require.NoError(t, db.Create(user).Error)

	recorder, handlerCalled := performAccessTokenAuthRequest(t, user, accessToken)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.False(t, handlerCalled)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestAPITokenAuthRejectsAuthoritativeUserRestrictionsWithStaleRedis(t *testing.T) {
	authMiddlewares := []struct {
		name string
		auth func() func(*gin.Context)
	}{
		{name: "relay", auth: TokenAuth},
		{name: "read-only", auth: TokenAuthReadOnly},
	}
	restrictions := []struct {
		name    string
		updates map[string]any
	}{
		{name: "payment-frozen", updates: map[string]any{"payment_frozen": true}},
		{name: "disabled", updates: map[string]any{"status": common.UserStatusDisabled}},
	}

	for _, authMiddleware := range authMiddlewares {
		for _, restriction := range restrictions {
			t.Run(authMiddleware.name+"/"+restriction.name, func(t *testing.T) {
				db := withAuthDatabase(t)
				user := &model.User{
					Username: "api-token-user",
					Password: "test-password",
					Role:     common.RoleCommonUser,
					Status:   common.UserStatusEnabled,
					Group:    "default",
				}
				require.NoError(t, db.Create(user).Error)
				token := createAuthToken(t, db, user.Id)
				withStaleAuthRedis(t, user, token)

				// Simulate a committed payment freeze/admin status change whose Redis
				// invalidation failed. The stale hashes remain deliberately intact.
				require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Updates(restriction.updates).Error)

				recorder, handlerCalled := performAPITokenAuthRequest(t, token.Key, authMiddleware.auth())

				assert.Equal(t, http.StatusForbidden, recorder.Code)
				assert.False(t, handlerCalled)
				assert.NotContains(t, recorder.Body.String(), "payment_frozen")
			})
		}
	}
}

func TestAPITokenAuthUsesAuthoritativeRoleAndGroupWithStaleRedis(t *testing.T) {
	authMiddlewares := []struct {
		name string
		auth func() func(*gin.Context)
	}{
		{name: "relay", auth: TokenAuth},
		{name: "read-only", auth: TokenAuthReadOnly},
	}

	for _, authMiddleware := range authMiddlewares {
		t.Run(authMiddleware.name, func(t *testing.T) {
			db := withAuthDatabase(t)
			user := &model.User{
				Username: "authoritative-api-token-user",
				Password: "test-password",
				Role:     common.RoleAdminUser,
				Status:   common.UserStatusEnabled,
				Group:    "current-group",
			}
			require.NoError(t, db.Create(user).Error)
			token := createAuthToken(t, db, user.Id)
			staleUser := *user
			staleUser.Group = "stale-group"
			withStaleAuthRedis(t, &staleUser, token)

			recorder, handlerCalled := performAPITokenAuthRequest(t, token.Key, authMiddleware.auth())

			require.True(t, handlerCalled)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.JSONEq(t, fmt.Sprintf(
				`{"id":%d,"role":%d,"group":"current-group","context_group":"current-group"}`,
				user.Id,
				common.RoleAdminUser,
			), recorder.Body.String())
		})
	}
}

func TestAPITokenAuthUsesAuthoritativeTokenStateWithStaleRedis(t *testing.T) {
	states := []struct {
		name               string
		updates            map[string]any
		readOnlyMayInspect bool
	}{
		{
			name:    "disabled",
			updates: map[string]any{"status": common.TokenStatusDisabled},
		},
		{
			name:               "expired",
			updates:            map[string]any{"status": common.TokenStatusExpired},
			readOnlyMayInspect: true,
		},
		{
			name:               "exhausted",
			updates:            map[string]any{"status": common.TokenStatusExhausted},
			readOnlyMayInspect: true,
		},
	}

	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			db := withAuthDatabase(t)
			user := &model.User{
				Username: "token-state-api-user",
				Password: "test-password",
				Role:     common.RoleCommonUser,
				Status:   common.UserStatusEnabled,
				Group:    "default",
			}
			require.NoError(t, db.Create(user).Error)
			token := createAuthToken(t, db, user.Id)
			withStaleAuthRedis(t, user, token)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(state.updates).Error)

			relayRecorder, relayHandlerCalled := performAPITokenAuthRequest(t, token.Key, TokenAuth())
			assert.Equal(t, http.StatusUnauthorized, relayRecorder.Code)
			assert.False(t, relayHandlerCalled)

			readOnlyRecorder, readOnlyHandlerCalled := performAPITokenAuthRequest(t, token.Key, TokenAuthReadOnly())
			if state.readOnlyMayInspect {
				assert.Equal(t, http.StatusOK, readOnlyRecorder.Code)
				assert.True(t, readOnlyHandlerCalled)
			} else {
				assert.Equal(t, http.StatusUnauthorized, readOnlyRecorder.Code)
				assert.False(t, readOnlyHandlerCalled)
			}
		})
	}
}

func TestTokenAuthReadOnlyTreatsEmptyTokenAsInvalid(t *testing.T) {
	withAuthDatabase(t)

	recorder, handlerCalled := performAPITokenAuthRequest(t, "", TokenAuthReadOnly())

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.False(t, handlerCalled)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestAPITokenAuthFailsClosedWhenAuthorityDatabaseIsUnavailable(t *testing.T) {
	authMiddlewares := []struct {
		name string
		auth func() func(*gin.Context)
	}{
		{name: "relay", auth: TokenAuth},
		{name: "read-only", auth: TokenAuthReadOnly},
	}

	for _, authMiddleware := range authMiddlewares {
		t.Run(authMiddleware.name, func(t *testing.T) {
			db := withAuthDatabase(t)
			user := &model.User{
				Username: "database-failure-api-token-user",
				Password: "test-password",
				Role:     common.RoleCommonUser,
				Status:   common.UserStatusEnabled,
				Group:    "default",
			}
			require.NoError(t, db.Create(user).Error)
			token := createAuthToken(t, db, user.Id)
			withStaleAuthRedis(t, user, token)

			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			recorder, handlerCalled := performAPITokenAuthRequest(t, token.Key, authMiddleware.auth())

			assert.Equal(t, http.StatusInternalServerError, recorder.Code)
			assert.False(t, handlerCalled)
			assert.NotContains(t, recorder.Body.String(), "database is closed")
			assert.NotContains(t, recorder.Body.String(), "sql:")
		})
	}
}
