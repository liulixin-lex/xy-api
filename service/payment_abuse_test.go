package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

type idempotencyCountingProvider struct {
	name    string
	creates atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func preparePaymentWorkerClusterTest(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.SystemInstance{}))
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())
	t.Cleanup(func() { _ = model.DB.Exec("DELETE FROM system_instances").Error })
}

func (provider *idempotencyCountingProvider) Name() string { return provider.name }

func (*idempotencyCountingProvider) ValidateMethod(string) error { return nil }

func (provider *idempotencyCountingProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	provider.creates.Add(1)
	select {
	case provider.entered <- struct{}{}:
	default:
	}
	select {
	case <-provider.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &PaymentStart{
		Flow: PaymentFlowHostedRedirect, URL: "https://pay.example.test/" + order.TradeNo,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ProviderOrderKey: provider.name + ":" + order.TradeNo,
	}, nil
}

func (*idempotencyCountingProvider) VerifyWebhook(*http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("not implemented")
}

func (*idempotencyCountingProvider) Query(context.Context, *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	return nil, nil
}

func TestConcurrentCompatibleStartsCreateUpstreamPaymentOnce(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	preparePaymentWorkerClusterTest(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.User{}, &model.PaymentQuote{}, &model.PaymentUserGuard{}, &model.PaymentOrder{}, &model.PaymentTask{},
		&model.PaymentLimitPolicy{}, &model.PaymentLimitBucket{}, &model.PaymentLimitReservation{}, &model.TopUp{},
	))
	require.NoError(t, model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Option{Key: model.PaymentConfigurationVersionOptionKey, Value: "1"}).Error)

	provider := &idempotencyCountingProvider{
		name: "idempotency-counting-provider", entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	RegisterPaymentProvider(provider)
	const userID = 998821
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: "payment-worker-idempotency", AffCode: "payment-worker-idempotency",
		Status: common.UserStatusEnabled, Group: "default",
	}).Error)
	now := time.Now().Unix()
	quotes := []*model.PaymentQuote{
		{QuoteID: "Q_IDEMPOTENCY_UPSTREAM_A", UserID: userID, OrderKind: model.PaymentOrderKindTopUp, Provider: provider.name, PaymentMethod: "hosted", RequestedAmount: 10, CreditQuota: 5000, ExpectedAmountMinor: 1000, Currency: "USD", PricingSnapshot: `{"amount":10}`, ExpiresAt: now + 3600},
		{QuoteID: "Q_IDEMPOTENCY_UPSTREAM_B", UserID: userID, OrderKind: model.PaymentOrderKindTopUp, Provider: provider.name, PaymentMethod: "hosted", RequestedAmount: 10, CreditQuota: 5000, ExpectedAmountMinor: 1000, Currency: "USD", PricingSnapshot: `{"amount":10}`, ExpiresAt: now + 3601},
	}
	for _, quote := range quotes {
		require.NoError(t, model.CreatePaymentQuote(quote))
	}
	t.Cleanup(func() {
		model.DB.Where("user_id = ?", userID).Delete(&model.TopUp{})
		model.DB.Where("payment_order_id IN (?)", model.DB.Model(&model.PaymentOrder{}).Select("id").Where("user_id = ?", userID)).Delete(&model.PaymentTask{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentOrder{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentQuote{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentUserGuard{})
		model.DB.Delete(&model.User{}, userID)
	})

	first, err := StartPayment(t.Context(), userID, PaymentStartRequest{
		QuoteID: quotes[0].QuoteID, RequestID: "same-client-request",
	})
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, PaymentFlowPending, first.Flow)
	assert.EqualValues(t, 0, provider.creates.Load())
	second, secondErr := StartPayment(t.Context(), userID, PaymentStartRequest{
		QuoteID: quotes[1].QuoteID, RequestID: "same-client-request",
	})
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, PaymentFlowPending, second.Flow)
	assert.Equal(t, first.TradeNo, second.TradeNo)

	tasks, err := model.ClaimDuePaymentTasks(t.Context(), "runner-a", time.Now().Unix(), paymentTaskLeaseDuration, 4)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	runResult := make(chan error, 1)
	go func() {
		runResult <- runPaymentTask(t.Context(), tasks[0], "runner-a")
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider create was not reached")
	}
	otherTasks, err := model.ClaimDuePaymentTasks(t.Context(), "runner-b", time.Now().Unix(), paymentTaskLeaseDuration, 4)
	require.NoError(t, err)
	assert.Empty(t, otherTasks)
	close(provider.release)
	require.NoError(t, <-runResult)
	assert.EqualValues(t, 1, provider.creates.Load())

	retry, err := StartPayment(t.Context(), userID, PaymentStartRequest{
		QuoteID: quotes[1].QuoteID, RequestID: "same-client-request",
	})
	require.NoError(t, err)
	assert.Equal(t, first.TradeNo, retry.TradeNo)
	assert.Equal(t, PaymentFlowPending, retry.Flow)
	assert.EqualValues(t, 1, provider.creates.Load())
	stored, err := model.GetPaymentOrderByTradeNo(first.TradeNo)
	require.NoError(t, err)
	checkout, err := PublicPaymentCheckout(stored)
	require.NoError(t, err)
	assert.Equal(t, PaymentFlowHostedRedirect, checkout.Flow)
	assert.NotEmpty(t, checkout.ContinueURL)
}

func TestExpiredQueuedPaymentNeverCreatesProviderOrder(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	preparePaymentWorkerClusterTest(t)
	provider := &idempotencyCountingProvider{
		name: "expired-queue-provider", entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	close(provider.release)
	RegisterPaymentProvider(provider)

	order := &model.PaymentOrder{
		TradeNo: "PAY_EXPIRED_BEFORE_WORKER", UserID: 998822,
		OrderKind: model.PaymentOrderKindTopUp, Provider: provider.name, PaymentMethod: "hosted",
		ConfigurationVersion: 1, ExpectedAmountMinor: 100, Currency: "USD",
		RequestedAmount: 1, CreditQuota: 500, Status: model.PaymentOrderStatusPending,
		ExpiresAt: time.Now().Add(-time.Minute).Unix(), CreatedAt: time.Now().Add(-time.Hour).Unix(),
		UpdatedAt: time.Now().Add(-time.Hour).Unix(), Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	_, err := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, time.Now().Unix())
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB.Where("payment_order_id = ?", order.ID).Delete(&model.PaymentTask{})
		model.DB.Delete(&model.PaymentOrder{}, order.ID)
	})

	tasks, err := model.ClaimDuePaymentTasks(t.Context(), "expiry-runner", time.Now().Unix(), paymentTaskLeaseDuration, 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, runPaymentTask(t.Context(), tasks[0], "expiry-runner"))
	assert.Zero(t, provider.creates.Load())

	stored, err := model.GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusExpired, stored.Status)
}

func TestQueuedPaymentRechecksCurrentGatewayEnablementBeforeProviderCreate(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	preparePaymentWorkerClusterTest(t)
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	originalMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
		operation_setting.PayMethods = originalMethods
	})
	paymentSetting.ComplianceConfirmed = false
	paymentSetting.ComplianceTermsVersion = ""
	operation_setting.PayMethods = []map[string]string{{
		"name": "Alipay", "type": "alipay", "provider": model.PaymentProviderEpay,
	}}

	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	require.NoError(t, err)
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PAY_DISABLED_AFTER_QUOTE", UserID: 998824,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderEpay, PaymentMethod: "alipay",
		ConfigurationVersion: configurationVersion, ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 500, Status: model.PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	_, err = model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB.Where("payment_order_id = ?", order.ID).Delete(&model.PaymentTask{})
		model.DB.Delete(&model.PaymentOrder{}, order.ID)
	})

	tasks, err := model.ClaimDuePaymentTasks(t.Context(), "disabled-gateway-runner", now, paymentTaskLeaseDuration, 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, runPaymentTask(t.Context(), tasks[0], "disabled-gateway-runner"))

	stored, err := model.GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusFailed, stored.Status)
	assert.Empty(t, stored.StartPayload)
	task, err := model.GetPaymentTaskForOrder(order.ID, model.PaymentTaskOperationCreate)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, model.PaymentTaskStatusFailed, task.Status)
	assert.Equal(t, "provider_configuration_invalid", task.LastErrorCode)
}

func TestJSAPICreateTaskWaitsForBrowserAuthorization(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	preparePaymentWorkerClusterTest(t)
	originalProvider, err := GetPaymentProvider(model.PaymentProviderXorPay)
	require.NoError(t, err)
	provider := &idempotencyCountingProvider{
		name: model.PaymentProviderXorPay, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	close(provider.release)
	RegisterPaymentProvider(provider)
	t.Cleanup(func() { RegisterPaymentProvider(originalProvider) })

	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	require.NoError(t, err)
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PAY_JSAPI_WAITING_AUTHORIZATION", UserID: 998823,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderXorPay,
		PaymentMethod: model.PaymentMethodXorPayJSAPI, ConfigurationVersion: configurationVersion,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 500,
		Status: model.PaymentOrderStatusPending, ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	_, err = model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB.Where("payment_order_id = ?", order.ID).Delete(&model.PaymentTask{})
		model.DB.Delete(&model.PaymentOrder{}, order.ID)
	})

	tasks, err := model.ClaimDuePaymentTasks(t.Context(), "jsapi-auth-runner", now, paymentTaskLeaseDuration, 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NoError(t, runPaymentTask(t.Context(), tasks[0], "jsapi-auth-runner"))
	assert.Zero(t, provider.creates.Load())

	task, err := model.GetPaymentTaskForOrder(order.ID, model.PaymentTaskOperationCreate)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, model.PaymentTaskStatusRetryWait, task.Status)
	assert.Equal(t, "browser_authorization_required", task.LastErrorCode)
	stored, err := model.GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusPending, stored.Status)
}

func TestPaymentOrderExpirySweepIsRegisteredAsScheduledWork(t *testing.T) {
	handler := paymentOrderExpiryHandler{}
	assert.Equal(t, model.SystemTaskTypePaymentExpiry, handler.Type())
	assert.True(t, handler.Enabled())
	assert.Equal(t, time.Minute, handler.Interval())
}
