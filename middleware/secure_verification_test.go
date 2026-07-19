package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecureVerificationRequiredAllowsSessionBoundToCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("secure-verification-valid-user-binding"))))
	router.GET("/verify", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set(SecureVerificationSessionKey, time.Now().Unix())
		session.Set(secureVerificationMethodSessionKey, "2fa")
		session.Set(secureVerificationUserIDSessionKey, 10)
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	router.GET(
		"/protected",
		func(c *gin.Context) {
			c.Set("id", 10)
		},
		SecureVerificationRequired(),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)

	verifyRecorder := httptest.NewRecorder()
	router.ServeHTTP(verifyRecorder, httptest.NewRequest(http.MethodGet, "/verify", nil))
	require.Equal(t, http.StatusNoContent, verifyRecorder.Code)
	require.NotEmpty(t, verifyRecorder.Result().Cookies())

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	for _, sessionCookie := range verifyRecorder.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code, "unexpected response: %s", recorder.Body.String())
}

func TestSecureVerificationRequiredRejectsAnotherUsersSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("secure-verification-user-binding"))))

	handlerCalled := false
	verificationStateCleared := false
	router.GET(
		"/protected",
		func(c *gin.Context) {
			session := sessions.Default(c)
			session.Set(SecureVerificationSessionKey, time.Now().Unix())
			session.Set(secureVerificationMethodSessionKey, "2fa")
			session.Set(secureVerificationUserIDSessionKey, 11)
			c.Set("id", 22)
			c.Next()
			verificationStateCleared = session.Get(SecureVerificationSessionKey) == nil &&
				session.Get(secureVerificationMethodSessionKey) == nil &&
				session.Get(secureVerificationUserIDSessionKey) == nil
		},
		SecureVerificationRequired(),
		func(c *gin.Context) {
			handlerCalled = true
			c.Status(http.StatusNoContent)
		},
	)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.False(t, handlerCalled)
	assert.True(t, verificationStateCleared)
	assert.Contains(t, recorder.Body.String(), `"code":"VERIFICATION_INVALID"`)
}

func TestOptionalSecureVerificationClearsAnotherUsersSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("optional-secure-verification-user-binding"))))

	verified := true
	verificationStateCleared := false
	router.GET(
		"/protected",
		func(c *gin.Context) {
			session := sessions.Default(c)
			session.Set(SecureVerificationSessionKey, time.Now().Unix())
			session.Set(secureVerificationMethodSessionKey, "passkey")
			session.Set(secureVerificationUserIDSessionKey, 31)
			c.Set("id", 32)
			c.Next()
			verificationStateCleared = session.Get(SecureVerificationSessionKey) == nil &&
				session.Get(secureVerificationMethodSessionKey) == nil &&
				session.Get(secureVerificationUserIDSessionKey) == nil
		},
		OptionalSecureVerification(),
		func(c *gin.Context) {
			verified = c.GetBool("secure_verified")
			c.Status(http.StatusNoContent)
		},
	)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.False(t, verified)
	assert.True(t, verificationStateCleared)
}

func TestSecureVerificationRejectsFutureTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name     string
		optional bool
	}{
		{name: "required"},
		{name: "optional", optional: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := gin.New()
			router.Use(sessions.Sessions("session", cookie.NewStore([]byte("future-secure-verification-state"))))
			handlerCalled := false
			verified := true
			middlewares := []gin.HandlerFunc{func(c *gin.Context) {
				session := sessions.Default(c)
				session.Set(SecureVerificationSessionKey, time.Now().Add(time.Minute).Unix())
				session.Set(secureVerificationMethodSessionKey, "passkey")
				session.Set(secureVerificationUserIDSessionKey, 71)
				c.Set("id", 71)
				c.Next()
			}}
			if testCase.optional {
				middlewares = append(middlewares, OptionalSecureVerification())
			} else {
				middlewares = append(middlewares, SecureVerificationRequired())
			}
			middlewares = append(middlewares, func(c *gin.Context) {
				handlerCalled = true
				verified = c.GetBool("secure_verified")
				c.Status(http.StatusNoContent)
			})
			router.GET("/protected", middlewares...)

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))
			if testCase.optional {
				assert.Equal(t, http.StatusNoContent, recorder.Code)
				assert.True(t, handlerCalled)
				assert.False(t, verified)
			} else {
				assert.Equal(t, http.StatusForbidden, recorder.Code)
				assert.False(t, handlerCalled)
			}
		})
	}
}
