package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSubscriptionEpayReturnAlwaysDefersToCanonicalOrderStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet,
		"/api/subscription/epay/return?trade_status=TRADE_SUCCESS&money=0.01&out_trade_no=subscription-order-1&sign=forged", nil)

	SubscriptionEpayReturn(context)

	assert.Equal(t, http.StatusFound, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "pay=pending")
	assert.Contains(t, recorder.Header().Get("Location"), "trade_no=subscription-order-1")
}
