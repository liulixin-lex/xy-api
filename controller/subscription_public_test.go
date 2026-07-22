package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionBalancePayHidesModelDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
	))
	confirmPaymentComplianceForTest(t)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 982001)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/subscription/balance/pay",
		strings.NewReader(`{"plan_id":1,"request_id":"balance_public_contract"}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")

	SubscriptionRequestBalancePay(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"code":"payment_temporarily_unavailable"`)
	assert.NotContains(t, strings.ToLower(body), "record not found")
	assert.NotContains(t, strings.ToLower(body), "sql")
}

func TestPublicSubscriptionPlanOmitsProviderInventory(t *testing.T) {
	plan := model.SubscriptionPlan{
		Id: 42, Title: "Thirty days", PriceAmount: 12, Currency: "USD",
		DurationUnit: model.SubscriptionDurationDay, DurationValue: 30,
		StripePriceId: "price_recurring_private", CreemProductId: "creem_private",
		WaffoPancakeProductId: "waffo_private", Enabled: true, SortOrder: 99,
		UpgradeGroup: "internal-premium", DowngradeGroup: "internal-default",
	}
	plan.NormalizeDefaults()

	payload, err := common.Marshal(PublicSubscriptionPlanDTO{Plan: publicSubscriptionPlan(plan)})
	require.NoError(t, err)
	serialized := string(payload)
	assert.Contains(t, serialized, `"title":"Thirty days"`)
	assert.Contains(t, serialized, `"includes_expanded_access":true`)
	assert.Contains(t, serialized, `"external_payment_route_ids":["pay_`)
	assert.NotContains(t, serialized, `"external_payment_channels"`)
	assert.NotContains(t, serialized, `"creem"`)
	assert.NotContains(t, serialized, `"waffo_pancake"`)
	assert.NotContains(t, serialized, `"enabled"`)
	assert.NotContains(t, serialized, `"sort_order"`)
	assert.NotContains(t, serialized, `"allow_wallet_overflow"`)
	assert.NotContains(t, serialized, `"upgrade_group"`)
	assert.NotContains(t, serialized, `"downgrade_group"`)
	assert.NotContains(t, serialized, "internal-premium")
	assert.NotContains(t, serialized, "internal-default")
	assert.NotContains(t, serialized, "stripe_price_id")
	assert.NotContains(t, serialized, "price_recurring_private")
	assert.NotContains(t, serialized, "creem_product_id")
	assert.NotContains(t, serialized, "waffo_pancake_product_id")
}

func TestPublicSubscriptionSelfSerializationOmitsInternalModelFields(t *testing.T) {
	paymentOrderID := int64(99123)
	internal := model.UserSubscription{
		Id: 61, UserId: 90210, PlanId: 42, PaymentOrderId: &paymentOrderID,
		AmountTotal: 10000, AmountUsed: 2500, AmountUsedTotal: 9000,
		UsageAccountingVersion: 1, StartTime: 100, EndTime: 200,
		Status: "active", Source: "order", LastResetTime: 110,
		NextResetTime: 150, QuotaResetVersion: 7,
		QuotaResetPeriod:        model.SubscriptionResetDaily,
		QuotaResetCustomSeconds: 123, UpgradeGroup: "internal-premium",
		PrevUserGroup: "internal-previous", DowngradeGroup: "internal-default",
		AllowWalletOverflow: true, CreatedAt: 90, UpdatedAt: 120,
	}

	data := PublicSubscriptionSelf{
		BillingPreference: "subscription_first",
		Subscriptions: publicSubscriptionSummaries(
			[]model.SubscriptionSummary{{Subscription: &internal}},
			map[int]string{42: "Thirty days"},
		),
		AllSubscriptions: publicSubscriptionSummaries(
			[]model.SubscriptionSummary{{Subscription: &internal}},
			map[int]string{42: "Thirty days"},
		),
	}
	payload, err := common.Marshal(data)
	require.NoError(t, err)
	serialized := string(payload)

	assert.Contains(t, serialized, `"plan_title":"Thirty days"`)
	assert.Contains(t, serialized, `"amount_used":2500`)
	for _, forbidden := range []string{
		`"user_id"`, `"payment_order_id"`, `"source"`,
		`"upgrade_group"`, `"downgrade_group"`, `"prev_user_group"`,
		`"amount_used_total"`, `"usage_accounting_version"`,
		`"quota_reset_version"`, `"last_reset_time"`,
		`"quota_reset_period"`, `"quota_reset_custom_seconds"`,
		`"allow_wallet_overflow"`, `"created_at"`, `"updated_at"`,
	} {
		assert.NotContains(t, serialized, forbidden)
	}
	assert.NotContains(t, serialized, "internal-premium")
	assert.NotContains(t, serialized, "internal-previous")
	assert.NotContains(t, serialized, "internal-default")
}

func TestGetSubscriptionSelfUsesPublicProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
	))
	plan := model.SubscriptionPlan{
		Id: 77, Title: "Public entitlement", Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		UpgradeGroup: "internal-premium", DowngradeGroup: "internal-default",
	}
	require.NoError(t, db.Create(&plan).Error)
	paymentOrderID := int64(123456)
	subscription := model.UserSubscription{
		Id: 88, UserId: 982002, PlanId: plan.Id, PaymentOrderId: &paymentOrderID,
		AmountTotal: 5000, AmountUsed: 1000, AmountUsedTotal: 1500,
		StartTime: 100, EndTime: common.GetTimestamp() + 3600, Status: "active",
		Source: "order", NextResetTime: common.GetTimestamp() + 1800,
		UpgradeGroup: "internal-premium", PrevUserGroup: "internal-previous",
		DowngradeGroup: "internal-default", AllowWalletOverflow: true,
	}
	require.NoError(t, db.Create(&subscription).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", subscription.UserId)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/self", nil)

	GetSubscriptionSelf(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	serialized := recorder.Body.String()
	assert.Contains(t, serialized, `"plan_title":"Public entitlement"`)
	for _, forbidden := range []string{
		`"user_id"`, `"payment_order_id"`, `"source"`,
		`"upgrade_group"`, `"downgrade_group"`, `"prev_user_group"`,
		`"amount_used_total"`, `"created_at"`, `"updated_at"`,
	} {
		assert.NotContains(t, serialized, forbidden)
	}
}

func TestGetSubscriptionSelfDoesNotHideDatabaseFailureAsEmptyState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 982004)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/self", nil)

	GetSubscriptionSelf(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"payment_temporarily_unavailable"`)
	assert.NotContains(t, recorder.Body.String(), `"subscriptions":[]`)
}

func TestAdminSubscriptionPlanKeepsLegacyStripeMappingExplicit(t *testing.T) {
	dto := SubscriptionPlanDTO{
		Plan:                 model.SubscriptionPlan{StripePriceId: "price_legacy_recurring"},
		StripePriceIDPurpose: model.SubscriptionPlanStripePriceIDPurposeLegacyRecurring,
	}
	payload, err := common.Marshal(dto)
	require.NoError(t, err)
	serialized := string(payload)
	assert.Contains(t, serialized, `"stripe_price_id":"price_legacy_recurring"`)
	assert.Contains(t, serialized, `"stripe_price_id_purpose":"legacy_recurring_mapping_only"`)
}
