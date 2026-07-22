package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentTaskFenceRejectsExpiredWorkerResult(t *testing.T) {
	truncateTables(t)
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_PAYMENT_TASK_FENCE", UserID: 979001, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayNative,
		RequestID: "payment-task-fence", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: PaymentOrderStatusPending,
		ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	_, err := EnsurePaymentTask(order.ID, PaymentTaskOperationCreate, now)
	require.NoError(t, err)

	first, err := ClaimDuePaymentTasks(t.Context(), "runner-a", now, 15*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.NoError(t, DB.Model(&PaymentTask{}).Where("id = ?", first[0].ID).Update("lease_until", now-1).Error)

	second, err := ClaimDuePaymentTasks(t.Context(), "runner-b", now, 15*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Greater(t, second[0].FenceToken, first[0].FenceToken)

	payload := `{"flow":"qr","trade_no":"PO_PAYMENT_TASK_FENCE","qr_content":"weixin://wxpay/fenced","expires_at":9999999999}`
	err = SavePaymentOrderStartWithProviderIdentityFenced(order.TradeNo, "qr", payload, now+1800,
		"xorpay:first", "", first[0].FenceToken)
	assert.ErrorIs(t, err, ErrPaymentTaskLeaseLost)
	require.NoError(t, SavePaymentOrderStartWithProviderIdentityFenced(order.TradeNo, "qr", payload, now+1800,
		"xorpay:second", "", second[0].FenceToken))

	stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	require.NotNil(t, stored.ProviderOrderKey)
	assert.Equal(t, "xorpay:second", *stored.ProviderOrderKey)
}

func TestPaymentTaskClaimAllowsOnlyOneCurrentLease(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_PAYMENT_TASK_SINGLE_CLAIM", UserID: 979002, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "payment-task-single-claim",
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 500,
		Status: PaymentOrderStatusPending, ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	_, err := EnsurePaymentTask(order.ID, PaymentTaskOperationCreate, now)
	require.NoError(t, err)

	first, err := ClaimDuePaymentTasks(t.Context(), "runner-a", now, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	second, err := ClaimDuePaymentTasks(t.Context(), "runner-b", now, time.Minute, 1)
	require.NoError(t, err)
	assert.Empty(t, second)
}

func TestCreateTaskManualReviewRequiresMatchingCreationFence(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_CREATE_MANUAL_REVIEW_FENCE", UserID: 979004,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
		PaymentMethod: PaymentMethodStripe, RequestID: "create-manual-review-fence",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 500,
		Status: PaymentOrderStatusPending, ExpiresAt: now + 3600,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	_, err := EnsurePaymentTask(order.ID, PaymentTaskOperationCreate, now)
	require.NoError(t, err)
	claimed, err := ClaimDuePaymentTasks(t.Context(), "create-runner", now, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("creation_fence_token", claimed[0].FenceToken+1).Error)
	err = MarkPaymentOrderManualReviewForTask(order.TradeNo, "stale create result", claimed[0], "create-runner")
	assert.ErrorIs(t, err, ErrPaymentTaskLeaseLost)

	stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusPending, stored.Status)
}

func TestReconcileTaskFencePreventsStaleManualReviewAndSettlement(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979003, 0)
	order := createTopUpPaymentOrder(t, 979003, PaymentProviderWaffo, PaymentMethodWaffo, 1000, 5000)
	now := common.GetTimestamp()
	_, err := EnsurePaymentTask(order.ID, PaymentTaskOperationReconcile, now)
	require.NoError(t, err)

	first, err := ClaimDuePaymentTasks(t.Context(), "reconcile-runner-a", now, 15*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.NoError(t, DB.Model(&PaymentTask{}).Where("id = ?", first[0].ID).Update("lease_until", now-1).Error)
	second, err := ClaimDuePaymentTasks(t.Context(), "reconcile-runner-b", now, 15*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Greater(t, second[0].FenceToken, first[0].FenceToken)

	err = MarkPaymentOrderManualReviewForTask(order.TradeNo, "stale provider result", first[0], "reconcile-runner-a")
	assert.ErrorIs(t, err, ErrPaymentTaskLeaseLost)
	_, err = ProcessPaymentEventForTask(paidPaymentEvent(order, "waffo-stale-reconcile-paid"), first[0], "reconcile-runner-a")
	assert.ErrorIs(t, err, ErrPaymentTaskLeaseLost)

	stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusPending, stored.Status)

	settlement, err := ProcessPaymentEventForTask(
		paidPaymentEvent(order, "waffo-current-reconcile-paid"), second[0], "reconcile-runner-b",
	)
	require.NoError(t, err)
	require.NotNil(t, settlement)
	stored, err = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusFulfilled, stored.Status)
	var user User
	require.NoError(t, DB.First(&user, order.UserID).Error)
	assert.Equal(t, 5000, user.Quota)
}
