package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
)

func TestHandleWaffoPaymentUsesSDKOrderStatusSemantics(t *testing.T) {
	testCases := []struct {
		name                string
		orderStatus         string
		expectedDisposition waffoOrderStatusDisposition
		expectedTopUpStatus string
		expectedEventStatus string
		expectedPaid        bool
		expectedFailed      bool
		expectedReview      bool
		expectedQuota       int
	}{
		{
			name:                "payment succeeds only on PAY_SUCCESS",
			orderStatus:         core.OrderStatusPaySuccess,
			expectedDisposition: waffoOrderStatusSucceeded,
			expectedTopUpStatus: common.TopUpStatusSuccess,
			expectedEventStatus: model.PaymentEventStatusProcessed,
			expectedPaid:        true,
			expectedQuota:       2,
		},
		{
			name:                "closed order is terminal failure",
			orderStatus:         core.OrderStatusOrderClose,
			expectedDisposition: waffoOrderStatusFailed,
			expectedTopUpStatus: common.TopUpStatusFailed,
			expectedEventStatus: model.PaymentEventStatusProcessed,
			expectedFailed:      true,
		},
		{
			name:                "payment in progress remains pending",
			orderStatus:         core.OrderStatusPayInProgress,
			expectedDisposition: waffoOrderStatusPending,
			expectedTopUpStatus: common.TopUpStatusPending,
			expectedEventStatus: model.PaymentEventStatusProcessed,
		},
		{
			name:                "authorization required remains pending",
			orderStatus:         core.OrderStatusAuthorizationRequired,
			expectedDisposition: waffoOrderStatusPending,
			expectedTopUpStatus: common.TopUpStatusPending,
			expectedEventStatus: model.PaymentEventStatusProcessed,
		},
		{
			name:                "authorized waiting capture remains pending",
			orderStatus:         core.OrderStatusAuthedWaitingCapture,
			expectedDisposition: waffoOrderStatusPending,
			expectedTopUpStatus: common.TopUpStatusPending,
			expectedEventStatus: model.PaymentEventStatusProcessed,
		},
		{
			name:                "unknown status preserves order and requires review",
			orderStatus:         "UNRECOGNIZED_PROVIDER_STATE",
			expectedDisposition: waffoOrderStatusManualReview,
			expectedTopUpStatus: common.TopUpStatusPending,
			expectedEventStatus: model.PaymentEventStatusManualReview,
			expectedReview:      true,
		},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupMidjourneyControllerBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.TopUp{}, &model.PaymentEvent{}))

			userID := 99100 + index
			tradeNo := fmt.Sprintf("WAFFO-STATUS-%d", index)
			require.NoError(t, db.Create(&model.User{
				Id:       userID,
				Username: fmt.Sprintf("waffo_status_%d", index),
				Status:   common.UserStatusEnabled,
			}).Error)
			require.NoError(t, db.Create(&model.TopUp{
				UserId:              userID,
				Amount:              2,
				Money:               1,
				TradeNo:             tradeNo,
				PaymentMethod:       model.PaymentMethodWaffo,
				PaymentProvider:     model.PaymentProviderWaffo,
				Currency:            "USD",
				ExpectedAmountMinor: 100,
				CreditQuotaSnapshot: 2,
				CreateTime:          time.Now().Unix(),
				Status:              common.TopUpStatusPending,
			}).Error)

			result := &core.PaymentNotificationResult{
				MerchantOrderID:  tradeNo,
				AcquiringOrderID: fmt.Sprintf("waffo-provider-order-%d", index),
				OrderStatus:      testCase.orderStatus,
				OrderCurrency:    "USD",
				OrderAmount:      "1.00",
			}
			normalizedEvent, err := normalizedWaffoWebhookEvent(core.EventPayment, result)
			require.NoError(t, err)
			assert.Equal(t, testCase.expectedDisposition, classifyWaffoOrderStatus(testCase.orderStatus))
			assert.Equal(t, testCase.expectedPaid, normalizedEvent.Paid)
			assert.Equal(t, testCase.expectedFailed, normalizedEvent.Failed)
			assert.Equal(t, testCase.expectedFailed, normalizedEvent.PermanentFailure)
			assert.Equal(t, testCase.expectedReview, normalizedEvent.ManualReview)
			require.NoError(t, service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent))

			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", nil)
			webhookHandler := core.NewWebhookHandler(&config.WaffoConfig{})
			handleWaffoPayment(context, webhookHandler, result, normalizedEvent)

			assert.Equal(t, http.StatusOK, recorder.Code)
			var storedTopUp model.TopUp
			require.NoError(t, db.Where("trade_no = ?", tradeNo).First(&storedTopUp).Error)
			assert.Equal(t, testCase.expectedTopUpStatus, storedTopUp.Status)

			var storedEvent model.PaymentEvent
			require.NoError(t, db.Where("provider = ? AND event_key = ?", model.PaymentProviderWaffo, normalizedEvent.EventKey).First(&storedEvent).Error)
			assert.Equal(t, testCase.expectedEventStatus, storedEvent.Status)
			if testCase.expectedReview {
				assert.Equal(t, "waffo_order_status_unknown", storedEvent.LastError)
			} else {
				assert.Empty(t, storedEvent.LastError)
			}

			var storedUser model.User
			require.NoError(t, db.First(&storedUser, userID).Error)
			assert.Equal(t, testCase.expectedQuota, storedUser.Quota)
		})
	}
}
