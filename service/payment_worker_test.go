package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type manualReviewQueryPaymentProvider struct {
	sawConfigurationReadLockContext bool
}

type xorPayMigrationRecoveryProvider struct {
	createCalls int
	queryCalls  int
}

func (*xorPayMigrationRecoveryProvider) Name() string {
	return model.PaymentProviderXorPay
}

func (*xorPayMigrationRecoveryProvider) ValidateMethod(string) error { return nil }

func (provider *xorPayMigrationRecoveryProvider) Create(context.Context, *model.PaymentOrder) (*PaymentStart, error) {
	provider.createCalls++
	return nil, errors.New("unexpected create call")
}

func (*xorPayMigrationRecoveryProvider) VerifyWebhook(*http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("unexpected webhook call")
}

func (provider *xorPayMigrationRecoveryProvider) Query(_ context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	provider.queryCalls++
	return &NormalizedPaymentEvent{
		Provider:      model.PaymentProviderXorPay,
		EventKey:      "query2:" + order.TradeNo + ":new",
		EventType:     "query2:new",
		TradeNo:       order.TradeNo,
		Currency:      order.Currency,
		PaymentMethod: order.PaymentMethod,
	}, nil
}

func (*manualReviewQueryPaymentProvider) Name() string {
	return model.PaymentProviderWaffoPancake
}

func (*manualReviewQueryPaymentProvider) ValidateMethod(string) error { return nil }

func (*manualReviewQueryPaymentProvider) Create(context.Context, *model.PaymentOrder) (*PaymentStart, error) {
	return nil, errors.New("unexpected create call")
}

func (*manualReviewQueryPaymentProvider) VerifyWebhook(*http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("unexpected webhook call")
}

func (provider *manualReviewQueryPaymentProvider) Query(ctx context.Context, _ *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	provider.sawConfigurationReadLockContext = paymentConfigurationReadLockHeld(ctx)
	return nil, model.ErrPaymentManualReview
}

func TestRepairPaymentTasksDoesNotStarveOrdersBeyondBatch(t *testing.T) {
	require.NoError(t, model.DB.Exec("DELETE FROM payment_tasks").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_orders").Error)
	now := common.GetTimestamp()
	orders := make([]model.PaymentOrder, paymentMaintenanceBatchSize+1)
	for i := range orders {
		orders[i] = model.PaymentOrder{
			TradeNo: fmt.Sprintf("PO_TASK_REPAIR_%03d", i), UserID: 988001,
			OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
			PaymentMethod: model.PaymentMethodStripe, ConfigurationVersion: 1,
			RequestID: fmt.Sprintf("task-repair-%03d", i), ExpectedAmountMinor: 100,
			Currency: "USD", Status: model.PaymentOrderStatusPending,
			ExpiresAt: now + int64(time.Hour/time.Second), CreatedAt: now, UpdatedAt: now, Version: 1,
		}
	}
	require.NoError(t, model.DB.CreateInBatches(&orders, 25).Error)
	for i := 0; i < paymentMaintenanceBatchSize; i++ {
		_, err := model.EnsurePaymentTask(orders[i].ID, model.PaymentTaskOperationCreate, now)
		require.NoError(t, err)
	}

	require.NoError(t, repairPaymentTasks())

	task, err := model.GetPaymentTaskForOrder(orders[len(orders)-1].ID, model.PaymentTaskOperationCreate)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, model.PaymentTaskStatusPending, task.Status)
	var count int64
	require.NoError(t, model.DB.Model(&model.PaymentTask{}).
		Where("operation = ?", model.PaymentTaskOperationCreate).Count(&count).Error)
	assert.EqualValues(t, len(orders), count)
}

func TestPaymentTaskClaimPassDoesNotLeaseWorkWhenExpectedClusterMemberIsMissing(t *testing.T) {
	enablePaymentMultiNodeModeForTest(t, "payment-local", "payment-peer")
	t.Setenv("PAYMENT_SECRET_KEY", "payment-worker-membership-test-key-000001")
	usePaymentClusterRedisForTest(t, common.DatabaseTypePostgreSQL)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_tasks").Error)

	require.NoError(t, ReportCurrentSystemInstance())
	task, err := model.EnsurePaymentMaintenanceTask()
	require.NoError(t, err)
	require.Equal(t, model.PaymentTaskStatusPending, task.Status)

	runPaymentTaskClaimPass("membership-blocked-runner")

	var stored model.PaymentTask
	require.NoError(t, model.DB.Where("id = ?", task.ID).First(&stored).Error)
	assert.Equal(t, model.PaymentTaskStatusPending, stored.Status)
	assert.Empty(t, stored.LeaseOwner)
	assert.Zero(t, stored.Attempts)
	assert.Zero(t, stored.FenceToken)
}

func TestCreateTaskRecoversProviderCallAfterConfigurationMigration(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-create-migration-recovery-key-0001")
	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.PaymentOrder{}, &model.PaymentTask{}, &model.SystemInstance{},
	))
	model.InitOptionMap()
	require.NoError(t, model.DB.Exec("DELETE FROM payment_tasks").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_orders").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())

	originalProvider, err := GetPaymentProvider(model.PaymentProviderXorPay)
	require.NoError(t, err)
	provider := &xorPayMigrationRecoveryProvider{}
	RegisterPaymentProvider(provider)
	t.Cleanup(func() { RegisterPaymentProvider(originalProvider) })

	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	require.NoError(t, err)
	require.Greater(t, configurationVersion, int64(0))
	var originalVersionOption model.Option
	versionLookup := model.DB.Where("key = ?", model.PaymentConfigurationVersionOptionKey).
		Limit(1).Find(&originalVersionOption)
	require.NoError(t, versionLookup.Error)
	versionOptionExisted := versionLookup.RowsAffected > 0
	t.Cleanup(func() {
		if versionOptionExisted {
			_ = model.DB.Model(&model.Option{}).
				Where("key = ?", model.PaymentConfigurationVersionOptionKey).
				Update("value", originalVersionOption.Value).Error
		} else {
			_ = model.DB.Where("key = ?", model.PaymentConfigurationVersionOptionKey).
				Delete(&model.Option{}).Error
		}
		model.InitOptionMap()
	})
	if versionOptionExisted {
		require.NoError(t, model.DB.Model(&model.Option{}).
			Where("key = ?", model.PaymentConfigurationVersionOptionKey).
			Update("value", strconv.FormatInt(configurationVersion, 10)).Error)
	} else {
		require.NoError(t, model.DB.Create(&model.Option{
			Key:   model.PaymentConfigurationVersionOptionKey,
			Value: strconv.FormatInt(configurationVersion, 10),
		}).Error)
	}
	now := common.GetTimestamp()
	order := &model.PaymentOrder{
		TradeNo: "PO_XORPAY_MIGRATION_RECOVERY", UserID: 988003,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderXorPay,
		PaymentMethod: model.PaymentMethodXorPayNative, ConfigurationVersion: configurationVersion,
		ProviderCredentialGeneration: 1, RequestID: "xorpay-migration-recovery",
		ExpectedAmountMinor: 100, Currency: "CNY", Status: model.PaymentOrderStatusPending,
		ExpiresAt: now + int64(time.Hour/time.Second), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	task, err := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, now)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(task).Updates(map[string]any{
		"phase": model.PaymentTaskPhaseProviderCallStarted,
	}).Error)

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", model.PaymentConfigurationVersionOptionKey).
		Update("value", strconv.FormatInt(configurationVersion+1, 10)).Error)

	claimed, err := model.ClaimDuePaymentTasks(t.Context(), "migration-recovery-runner", now, paymentTaskLeaseDuration, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, runPaymentCreateTask(t.Context(), claimed[0], "migration-recovery-runner"))

	assert.Zero(t, provider.createCalls)
	assert.Equal(t, 1, provider.queryCalls)
	storedOrder, err := model.GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusPending, storedOrder.Status)
	storedTask, err := model.GetPaymentTaskForOrder(order.ID, model.PaymentTaskOperationCreate)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentTaskStatusRetryWait, storedTask.Status)
	assert.Equal(t, model.PaymentTaskPhaseProviderCallStarted, storedTask.Phase)
	assert.Equal(t, "provider_create_uncertain", storedTask.LastErrorCode)
}

func TestReconcileManualReviewErrorStopsRetriesUnderConfigurationSnapshot(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-reconcile-manual-review-key-0001")
	require.NoError(t, model.DB.AutoMigrate(
		&model.PaymentOrder{}, &model.PaymentTask{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.SystemInstance{},
	))
	require.NoError(t, model.DB.Exec("DELETE FROM payment_tasks").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_orders").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM system_instances").Error)
	require.NoError(t, ReportCurrentSystemInstance())

	originalProvider, err := GetPaymentProvider(model.PaymentProviderWaffoPancake)
	require.NoError(t, err)
	provider := &manualReviewQueryPaymentProvider{}
	RegisterPaymentProvider(provider)
	t.Cleanup(func() { RegisterPaymentProvider(originalProvider) })

	now := common.GetTimestamp()
	order := &model.PaymentOrder{
		TradeNo: "PO_RECONCILE_MANUAL_REVIEW", UserID: 988002,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderWaffoPancake,
		PaymentMethod: model.PaymentMethodWaffoPancake, ConfigurationVersion: 1,
		RequestID: "reconcile-manual-review", ExpectedAmountMinor: 100,
		Currency: "USD", Status: model.PaymentOrderStatusPending, StartPayload: `{}`,
		ExpiresAt: now + int64(time.Hour/time.Second), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	_, err = model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationReconcile, now)
	require.NoError(t, err)
	claimed, err := model.ClaimDuePaymentTasks(t.Context(), "manual-review-runner", now, paymentTaskLeaseDuration, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, runPaymentReconcileTask(t.Context(), claimed[0], "manual-review-runner"))
	assert.True(t, provider.sawConfigurationReadLockContext)

	storedOrder, err := model.GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusManualReview, storedOrder.Status)
	assert.Equal(t, "payment provider identity requires administrator review", storedOrder.StatusReason)
	storedTask, err := model.GetPaymentTaskForOrder(order.ID, model.PaymentTaskOperationReconcile)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentTaskStatusManualReview, storedTask.Status)
	assert.Equal(t, "provider_identity_review", storedTask.LastErrorCode)
}
