package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSecureVerificationSessionBindsCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("secure-verification-session-test"))))

	var saveErr error
	var verifiedUserID int
	var readyAtCleared bool
	var readyUserIDCleared bool
	router.GET("/verify", func(c *gin.Context) {
		c.Set("id", 41)
		session := sessions.Default(c)
		session.Set(PasskeyReadySessionKey, time.Now().Unix())
		session.Set(passkeyReadyUserIDSessionKey, 41)

		_, saveErr = setSecureVerificationSession(c, secureVerificationMethodPasskey)
		verifiedUserID, _ = session.Get(secureVerificationUserIDSessionKey).(int)
		readyAtCleared = session.Get(PasskeyReadySessionKey) == nil
		readyUserIDCleared = session.Get(passkeyReadyUserIDSessionKey) == nil
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/verify", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NoError(t, saveErr)
	assert.Equal(t, 41, verifiedUserID)
	assert.True(t, readyAtCleared)
	assert.True(t, readyUserIDCleared)
}

func TestConsumePasskeyReadyRejectsAnotherUsersMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("passkey-ready-user-binding-test"))))

	verified := true
	var consumeErr error
	readyStateCleared := false
	router.GET("/verify", func(c *gin.Context) {
		c.Set("id", 52)
		session := sessions.Default(c)
		session.Set(PasskeyReadySessionKey, time.Now().Unix())
		session.Set(passkeyReadyUserIDSessionKey, 51)

		verified, consumeErr = consumePasskeyReady(c)
		readyStateCleared = session.Get(PasskeyReadySessionKey) == nil &&
			session.Get(passkeyReadyUserIDSessionKey) == nil
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/verify", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.ErrorIs(t, consumeErr, errPasskeyReadyStateInvalid)
	assert.False(t, verified)
	assert.True(t, readyStateCleared)
}

func TestConsumePasskeyReadyRejectsFutureMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("future-passkey-ready-test"))))

	verified := true
	var consumeErr error
	router.GET("/verify", func(c *gin.Context) {
		c.Set("id", 53)
		session := sessions.Default(c)
		session.Set(PasskeyReadySessionKey, time.Now().Add(time.Minute).Unix())
		session.Set(passkeyReadyUserIDSessionKey, 53)
		verified, consumeErr = consumePasskeyReady(c)
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/verify", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.ErrorIs(t, consumeErr, errPasskeyReadyStateInvalid)
	assert.False(t, verified)
}

func TestConsumePasskeyReadyAcceptsSerializedNumericSessionValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("serialized-passkey-ready-test"))))

	verified := false
	var consumeErr error
	router.GET("/verify", func(c *gin.Context) {
		c.Set("id", 57)
		session := sessions.Default(c)
		session.Set(PasskeyReadySessionKey, strconv.FormatInt(time.Now().Unix(), 10))
		session.Set(passkeyReadyUserIDSessionKey, "57")
		verified, consumeErr = consumePasskeyReady(c)
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/verify", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NoError(t, consumeErr)
	assert.True(t, verified)
}

func TestUniversalVerifyRejectsOversizedInputBeforeUserLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "oversized body", body: `{"method":"2fa","code":"` + strings.Repeat("1", secureVerificationRequestBodyLimit) + `"}`},
		{name: "oversized code", body: `{"method":"2fa","code":"` + strings.Repeat("1", secureVerificationCodeMaxLength+1) + `"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/verify", func(c *gin.Context) {
				c.Set("id", 54)
			}, UniversalVerify)

			request := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), `"code":"secure_verification_request_invalid"`)
		})
	}
}

func TestSetupLoginClearsSecureVerificationMarkers(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}))

	user := &model.User{
		Username: "secure-session-switch-user",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(user).Error)

	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("setup-login-secure-state-test"))))
	markersCleared := false
	loggedInUserID := 0
	router.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set(SecureVerificationSessionKey, time.Now().Unix())
		session.Set(secureVerificationMethodSessionKey, secureVerificationMethod2FA)
		session.Set(secureVerificationUserIDSessionKey, user.Id+1)
		session.Set(PasskeyReadySessionKey, time.Now().Unix())
		session.Set(passkeyReadyUserIDSessionKey, user.Id+1)

		setupLogin(user, c)
		markersCleared = session.Get(SecureVerificationSessionKey) == nil &&
			session.Get(secureVerificationMethodSessionKey) == nil &&
			session.Get(secureVerificationUserIDSessionKey) == nil &&
			session.Get(PasskeyReadySessionKey) == nil &&
			session.Get(passkeyReadyUserIDSessionKey) == nil
		loggedInUserID, _ = session.Get("id").(int)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, markersCleared)
	assert.Equal(t, user.Id, loggedInUserID)
}

func TestGetSessionUserAcceptsSerializedNumericUserID(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := &model.User{
		Username: "serialized-passkey-session-user",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(user).Error)

	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("serialized-passkey-user-test"))))
	var loadedUser *model.User
	var loadErr error
	router.GET("/user", func(c *gin.Context) {
		sessions.Default(c).Set("id", strconv.Itoa(user.Id))
		loadedUser, loadErr = getSessionUser(c)
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/user", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NoError(t, loadErr)
	require.NotNil(t, loadedUser)
	assert.Equal(t, user.Id, loadedUser.Id)
}
