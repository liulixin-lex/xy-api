package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentOperationsOverviewReportsActionableBacklog(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	order := &PaymentOrder{
		TradeNo: "PAY_OVERVIEW_PENDING", UserID: 991100,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderXorPay,
		PaymentMethod: PaymentMethodXorPayAlipay, ExpectedAmountMinor: 100,
		Currency: "CNY", Status: PaymentOrderStatusPending, CreatedAt: now - 90,
		UpdatedAt: now - 90, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	require.NoError(t, DB.Create(&PaymentTask{
		TaskID: "payment:create:overview", PaymentOrderID: order.ID,
		Operation: PaymentTaskOperationCreate, Status: PaymentTaskStatusRetryWait,
		Phase: PaymentTaskPhaseProviderCallStarted, AvailableAt: now + 10,
		CreatedAt: now - 80, UpdatedAt: now - 5,
	}).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderXorPay, EventKey: "overview-event", EventType: "paid",
		TradeNo: "UNMATCHED_OVERVIEW", Status: PaymentEventStatusManualReview,
		CreatedAt: now - 70, UpdatedAt: now - 60,
	}).Error)
	require.NoError(t, DB.Create(&PaymentLimitReservation{
		PaymentOrderID: order.ID, Provider: order.Provider, PaymentMethod: order.PaymentMethod,
		Currency: order.Currency, DayKey: "2026-07-21", AmountMinor: 100,
		Status: PaymentLimitReservationActive, ExpiresAt: now - 1, CreatedAt: now - 90, UpdatedAt: now - 90,
	}).Error)

	overview, err := GetPaymentOperationsOverview(now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), overview.PreparingOrders)
	assert.Equal(t, int64(1), overview.CreateTaskBacklog)
	assert.Equal(t, int64(1), overview.RetryWaitingTasks)
	assert.Equal(t, int64(80), overview.OldestCreateTaskAgeSeconds)
	assert.Equal(t, int64(1), overview.UnmatchedPaymentEvents)
	assert.Equal(t, int64(1), overview.ActiveLimitReservations)
	assert.Equal(t, int64(1), overview.ExpiredActiveLimitReservations)
	assert.Equal(t, int64(1), overview.PaymentConfigurationVersion)
}
