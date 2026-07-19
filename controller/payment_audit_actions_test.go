package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaymentAuditControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.PaymentOrder{}, &model.PaymentEvent{},
		&model.PaymentCustomerBinding{}, &model.PaymentCustomerBindingRetirement{}, &model.PaymentOperationsAudit{},
	))

	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
		common.RedisEnabled = originalRedisEnabled
	})
	return db
}

func TestPaymentAuditMutationHandlersRejectAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
		param   gin.Param
	}{
		{name: "fulfill", handler: ResolveManualPaymentOrder, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "reject", handler: RejectManualPaymentOrder, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "void", handler: VoidManualPaymentOrder, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "external refund", handler: ConfirmExternalPaymentRefund, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "acknowledge credential incident", handler: AcknowledgePaymentCredentialIncident, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "resolve credential incident", handler: ResolvePaymentCredentialIncident, param: gin.Param{Key: "trade_no", Value: "PO_TEST"}},
		{name: "dismiss", handler: DismissUnmatchedPaymentEvent, param: gin.Param{Key: "id", Value: "1"}},
		{name: "link", handler: LinkUnmatchedPaymentEvent, param: gin.Param{Key: "id", Value: "1"}},
		{name: "retry legacy", handler: RetryLegacyUnmatchedPaymentEvent, param: gin.Param{Key: "id", Value: "1"}},
		{name: "resolve legacy top-up", handler: ResolveLegacyTopUpPaymentEvent, param: gin.Param{Key: "id", Value: "1"}},
		{name: "resolve legacy subscription", handler: ResolveLegacySubscriptionPaymentEvent, param: gin.Param{Key: "id", Value: "1"}},
		{name: "retire Stripe customer binding", handler: RetireStripeCustomerBinding, param: gin.Param{Key: "id", Value: "1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{test.param}
			context.Set("use_access_token", true)
			context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))

			test.handler(context)

			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "dashboard session authentication")
		})
	}
}

func TestRejectManualPaymentOrderMapsVersionConflictToHTTP409(t *testing.T) {
	db := setupPaymentAuditControllerDB(t)
	order := &model.PaymentOrder{
		TradeNo: "PO_CONTROLLER_CONFLICT", UserID: 73001, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", ExpectedAmountMinor: 100,
		Currency: "USD", Status: model.PaymentOrderStatusManualReview, Version: 2,
	}
	require.NoError(t, db.Create(order).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "trade_no", Value: order.TradeNo}}
	context.Set("id", 99)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"trade_no":"PO_CONTROLLER_CONFLICT","expected_version":1,"reason":"verified provider state changed during review"}`,
	))

	RejectManualPaymentOrder(context)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "version changed")
	var stored model.PaymentOrder
	require.NoError(t, db.First(&stored, order.ID).Error)
	assert.Equal(t, model.PaymentOrderStatusManualReview, stored.Status)
	assert.EqualValues(t, 2, stored.Version)
}

func TestPaymentAuditOrderActionMapsInvalidAndMissingInputs(t *testing.T) {
	setupPaymentAuditControllerDB(t)
	gin.SetMode(gin.TestMode)

	invalidRecorder := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidRecorder)
	invalidContext.Params = gin.Params{{Key: "trade_no", Value: "PO_INVALID_ACTION"}}
	invalidContext.Set("id", 99)
	invalidContext.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"trade_no":"PO_INVALID_ACTION","expected_version":0,"reason":"reason is long enough"}`,
	))
	RejectManualPaymentOrder(invalidContext)
	assert.Equal(t, http.StatusBadRequest, invalidRecorder.Code)

	missingRecorder := httptest.NewRecorder()
	missingContext, _ := gin.CreateTestContext(missingRecorder)
	missingContext.Params = gin.Params{{Key: "trade_no", Value: "PO_MISSING_ACTION"}}
	missingContext.Set("id", 99)
	missingContext.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"trade_no":"PO_MISSING_ACTION","expected_version":1,"reason":"verified provider transaction is absent"}`,
	))
	RejectManualPaymentOrder(missingContext)
	assert.Equal(t, http.StatusNotFound, missingRecorder.Code)
}

func TestListPaymentAuditPaginatesAllUnmatchedEvents(t *testing.T) {
	db := setupPaymentAuditControllerDB(t)
	for index := 1; index <= 3; index++ {
		require.NoError(t, db.Create(&model.PaymentEvent{
			Provider: model.PaymentProviderStripe, EventKey: "controller-unmatched-" + strconv.Itoa(index),
			EventType: "checkout.session.completed", Status: model.PaymentEventStatusManualReview,
			NormalizedPayload: `{"secret":"must-not-leak"}`,
			CreatedAt:         int64(index), UpdatedAt: int64(index),
		}).Error)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/?unmatched_page=2&unmatched_page_size=1", nil)
	ListPaymentAudit(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "normalized_payload")
	assert.NotContains(t, recorder.Body.String(), "must-not-leak")
	var response struct {
		Data struct {
			UnmatchedEvents   []model.PaymentEvent `json:"unmatched_events"`
			UnmatchedTotal    int64                `json:"unmatched_total"`
			UnmatchedPage     int                  `json:"unmatched_page"`
			UnmatchedPageSize int                  `json:"unmatched_page_size"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.EqualValues(t, 3, response.Data.UnmatchedTotal)
	assert.Equal(t, 2, response.Data.UnmatchedPage)
	assert.Equal(t, 1, response.Data.UnmatchedPageSize)
	require.Len(t, response.Data.UnmatchedEvents, 1)
	assert.Equal(t, "controller-unmatched-2", response.Data.UnmatchedEvents[0].EventKey)
}

func TestCredentialIncidentControllerActionWritesDurableAudit(t *testing.T) {
	db := setupPaymentAuditControllerDB(t)
	order := &model.PaymentOrder{
		TradeNo: "PO_CONTROLLER_INCIDENT", UserID: 73101, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderStripe, PaymentMethod: model.PaymentMethodStripe,
		Status: model.PaymentOrderStatusFulfilled, StatusReason: "settled",
		CredentialIncident: true, CredentialIncidentState: model.PaymentCredentialIncidentOpen,
		CredentialIncidentGeneration: 4, CredentialIncidentReason: "revoked webhook credential",
		Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "trade_no", Value: order.TradeNo}}
	context.Set("id", 99)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"trade_no":"PO_CONTROLLER_INCIDENT","expected_version":1,"reason":"reviewed revoked Stripe webhook evidence"}`,
	))
	AcknowledgePaymentCredentialIncident(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var stored model.PaymentOrder
	require.NoError(t, db.First(&stored, order.ID).Error)
	assert.Equal(t, model.PaymentOrderStatusFulfilled, stored.Status)
	assert.True(t, stored.CredentialIncident)
	assert.Equal(t, model.PaymentCredentialIncidentAcknowledged, stored.CredentialIncidentState)
	var eventCount, auditCount int64
	require.NoError(t, db.Model(&model.PaymentEvent{}).Where("provider = ? AND payment_order_id = ?", "admin", order.ID).Count(&eventCount).Error)
	require.NoError(t, db.Model(&model.PaymentOperationsAudit{}).Where("payment_order_id = ?", order.ID).Count(&auditCount).Error)
	assert.EqualValues(t, 1, eventCount)
	assert.EqualValues(t, 1, auditCount)
}

func TestStripeCustomerBindingRetirementControllerRequiresMatchingSubject(t *testing.T) {
	db := setupPaymentAuditControllerDB(t)
	user := &model.User{Id: 73102, Username: "binding-retire-controller", AffCode: "binding-retire-aff", StripeCustomer: "cus_controller_retire"}
	require.NoError(t, db.Create(user).Error)
	binding := &model.PaymentCustomerBinding{Provider: model.PaymentProviderStripe, CustomerKey: user.StripeCustomer, UserID: user.Id}
	require.NoError(t, db.Create(binding).Error)

	conflictRecorder := httptest.NewRecorder()
	conflictContext, _ := gin.CreateTestContext(conflictRecorder)
	conflictContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(binding.ID, 10)}}
	conflictContext.Set("id", 99)
	conflictContext.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"binding_id":1,"user_id":73102,"expected_version":2,"reason":"stale binding version must not be retired"}`,
	))
	RetireStripeCustomerBinding(conflictContext)
	assert.Equal(t, http.StatusConflict, conflictRecorder.Code)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(binding.ID, 10)}}
	context.Set("id", 99)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"binding_id":1,"user_id":73102,"expected_version":1,"reason":"retire compromised Stripe customer binding"}`,
	))
	RetireStripeCustomerBinding(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var activeCount, retiredCount int64
	require.NoError(t, db.Model(&model.PaymentCustomerBinding{}).Where("id = ?", binding.ID).Count(&activeCount).Error)
	require.NoError(t, db.Model(&model.PaymentCustomerBindingRetirement{}).Where("original_binding_id = ?", binding.ID).Count(&retiredCount).Error)
	assert.Zero(t, activeCount)
	assert.EqualValues(t, 1, retiredCount)
}
