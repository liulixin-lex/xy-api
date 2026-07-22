package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestWeChatAuthorizationCallbackRejectsAmbiguousQueryAndScrubsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	const path = "/api/payment/wechat/authorize/callback"
	const openID = "oSensitiveOpenID_0123456789"
	context.Request = httptest.NewRequest(http.MethodGet,
		path+"?state=authorization_state_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ&state=replayed&openid="+openID, nil)

	CompleteWeChatPaymentAuthorization(context)

	assert.Equal(t, http.StatusSeeOther, recorder.Code)
	assert.Equal(t, "/wallet?payment_error=payment_authorization_failed", recorder.Header().Get("Location"))
	assert.Empty(t, context.Request.URL.RawQuery)
	assert.Equal(t, path, context.Request.RequestURI)
	assert.NotContains(t, recorder.Body.String(), openID)
	assert.NotContains(t, recorder.Header().Get("Location"), openID)
}

func TestWeChatAuthorizationCallbackRejectsOversizedQueryBeforeParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	const path = "/api/payment/wechat/authorize/callback"
	context.Request = httptest.NewRequest(http.MethodGet,
		path+"?state="+strings.Repeat("a", 1100)+"&openid=oSensitiveOpenID_0123456789", nil)

	CompleteWeChatPaymentAuthorization(context)

	assert.Equal(t, http.StatusSeeOther, recorder.Code)
	assert.Empty(t, context.Request.URL.RawQuery)
	assert.Equal(t, path, context.Request.RequestURI)
}
