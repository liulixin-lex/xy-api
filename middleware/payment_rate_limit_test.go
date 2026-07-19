package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentRateLimitsArePerUserAndOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	originalQuoteEnabled := common.PaymentQuoteRateLimitEnable
	originalQuoteLimit := common.PaymentQuoteRateLimitNum
	originalQuoteDuration := common.PaymentQuoteRateLimitDuration
	originalStartEnabled := common.PaymentStartRateLimitEnable
	originalStartLimit := common.PaymentStartRateLimitNum
	originalStartDuration := common.PaymentStartRateLimitDuration
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.PaymentQuoteRateLimitEnable = originalQuoteEnabled
		common.PaymentQuoteRateLimitNum = originalQuoteLimit
		common.PaymentQuoteRateLimitDuration = originalQuoteDuration
		common.PaymentStartRateLimitEnable = originalStartEnabled
		common.PaymentStartRateLimitNum = originalStartLimit
		common.PaymentStartRateLimitDuration = originalStartDuration
	})
	common.RedisEnabled = false
	common.PaymentQuoteRateLimitEnable = true
	common.PaymentQuoteRateLimitNum = 2
	common.PaymentQuoteRateLimitDuration = 60
	common.PaymentStartRateLimitEnable = true
	common.PaymentStartRateLimitNum = 1
	common.PaymentStartRateLimitDuration = 60

	engine := gin.New()
	authenticate := func(c *gin.Context) {
		c.Set("id", c.GetInt("test_user_id"))
	}
	setUserFromHeader := func(c *gin.Context) {
		var userID int
		_, _ = fmt.Sscanf(c.GetHeader("X-Test-User"), "%d", &userID)
		c.Set("test_user_id", userID)
	}
	engine.POST("/quote", setUserFromHeader, authenticate, PaymentQuoteRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	engine.POST("/start", setUserFromHeader, authenticate, PaymentStartRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(path string, userID int) int {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if userID > 0 {
			req.Header.Set("X-Test-User", fmt.Sprintf("%d", userID))
		}
		engine.ServeHTTP(recorder, req)
		return recorder.Code
	}

	const firstUser = 1987651
	require.Equal(t, http.StatusNoContent, request("/quote", firstUser))
	require.Equal(t, http.StatusNoContent, request("/quote", firstUser))
	assert.Equal(t, http.StatusTooManyRequests, request("/quote", firstUser))
	assert.Equal(t, http.StatusNoContent, request("/quote", firstUser+1))

	// The start bucket is independent from the exhausted quote bucket.
	assert.Equal(t, http.StatusNoContent, request("/start", firstUser))
	assert.Equal(t, http.StatusTooManyRequests, request("/start", firstUser))
	assert.Equal(t, http.StatusNoContent, request("/start", firstUser+1))
	assert.Equal(t, http.StatusUnauthorized, request("/start", 0))
}
