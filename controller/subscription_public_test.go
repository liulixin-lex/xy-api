package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
	publicRoutes := []service.PublicPaymentRoute{
		{
			RouteID: "card_checkout", Provider: model.PaymentProviderStripe,
			PaymentMethod: model.PaymentMethodStripe,
		},
		{
			RouteID: "online_product", Provider: model.PaymentProviderCreem,
			PaymentMethod: model.PaymentMethodCreem,
		},
		{
			RouteID: "premium_checkout", Provider: model.PaymentProviderWaffoPancake,
			PaymentMethod: model.PaymentMethodWaffoPancake,
		},
	}

	payload, err := common.Marshal(PublicSubscriptionPlanDTO{Plan: publicSubscriptionPlan(plan, publicRoutes)})
	require.NoError(t, err)
	serialized := string(payload)
	assert.Contains(t, serialized, `"title":"Thirty days"`)
	assert.Contains(t, serialized, `"includes_expanded_access":true`)
	assert.Contains(t, serialized, `"external_payment_route_ids":["card_checkout","online_product","premium_checkout"]`)
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

func TestPublicSubscriptionPlanProjectsOnlyEligibleRoutesAcrossCheckoutKinds(t *testing.T) {
	eligibleRoutes := []service.PublicPaymentRoute{
		{
			RouteID: "redirect_checkout", Provider: model.PaymentProviderEpay,
			PaymentMethod: "alipay",
		},
		{
			RouteID: "online_product", Provider: model.PaymentProviderCreem,
			PaymentMethod: model.PaymentMethodCreem,
		},
		{
			RouteID: "premium_checkout", Provider: model.PaymentProviderWaffoPancake,
			PaymentMethod: model.PaymentMethodWaffoPancake,
		},
	}

	withoutEligibleRoutes := publicSubscriptionPlan(model.SubscriptionPlan{Id: 1}, nil)
	assert.Empty(t, withoutEligibleRoutes.ExternalPaymentRouteIDs)

	projected := publicSubscriptionPlan(model.SubscriptionPlan{Id: 2}, eligibleRoutes)
	assert.Equal(t, []string{"redirect_checkout", "online_product", "premium_checkout"}, projected.ExternalPaymentRouteIDs)
}

func TestGetSubscriptionPlansUsesCanonicalCustomRouteID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))
	confirmPaymentComplianceForTest(t)
	t.Setenv("PAYMENT_SECRET_KEY", "subscription-route-contract-key-0123456789")

	originalMethods := operation_setting.PayMethods
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalStoreID := setting.WaffoPancakeStoreID
	originalProductID := setting.WaffoPancakeProductID
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeStoreID = originalStoreID
		setting.WaffoPancakeProductID = originalProductID
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		operation_setting.CustomCallbackAddress = originalCallbackAddress
	})

	operation_setting.PayMethods = []map[string]string{{
		"name": "Hosted payment", "type": model.PaymentMethodWaffoPancake,
		"provider": model.PaymentProviderWaffoPancake, "route_id": "premium_plan_checkout",
		"public_method": "online_payment", "channel_alias": "alternative_checkout",
	}}
	setting.WaffoPancakeMerchantID = "subscription-merchant"
	setting.WaffoPancakePrivateKey = "subscription-private-key"
	setting.WaffoPancakeStoreID = "subscription-store"
	setting.WaffoPancakeProductID = ""
	setting.WaffoPancakeUnitPrice = 1
	operation_setting.CustomCallbackAddress = "https://api.example.com"

	plan := model.SubscriptionPlan{
		Title: "Canonical route plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 1000, WaffoPancakeProductId: "provider_private_product",
		Enabled: true,
	}
	require.NoError(t, db.Create(&plan).Error)

	planRecorder := httptest.NewRecorder()
	planContext, _ := gin.CreateTestContext(planRecorder)
	planContext.Set("id", 982005)
	planContext.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/plans", nil)
	GetSubscriptionPlans(planContext)

	require.Equal(t, http.StatusOK, planRecorder.Code)
	var planResponse struct {
		Success bool                        `json:"success"`
		Data    []PublicSubscriptionPlanDTO `json:"data"`
	}
	require.NoError(t, common.Unmarshal(planRecorder.Body.Bytes(), &planResponse))
	require.True(t, planResponse.Success)
	require.Len(t, planResponse.Data, 1)
	assert.Equal(t, []string{"premium_plan_checkout"}, planResponse.Data[0].Plan.ExternalPaymentRouteIDs)
	assert.NotContains(t, planRecorder.Body.String(), "provider_private_product")
	assert.NotContains(t, strings.ToLower(planRecorder.Body.String()), model.PaymentProviderWaffoPancake)

	resolved, err := service.ResolvePublicPaymentRoute("premium_plan_checkout")
	require.NoError(t, err)
	assert.Equal(t, model.PaymentProviderWaffoPancake, resolved.Provider)
	assert.Equal(t, model.PaymentMethodWaffoPancake, resolved.PaymentMethod)

	topUpRecorder := httptest.NewRecorder()
	topUpContext, _ := gin.CreateTestContext(topUpRecorder)
	topUpContext.Set("id", 982005)
	topUpContext.Request = httptest.NewRequest(http.MethodGet, "/api/user/topup", nil)
	GetTopUpInfo(topUpContext)

	require.Equal(t, http.StatusOK, topUpRecorder.Code)
	var topUpResponse struct {
		Success bool `json:"success"`
		Data    struct {
			PaymentRoutes             []publicTopUpRouteView `json:"payment_routes"`
			SubscriptionPaymentRoutes []publicTopUpRouteView `json:"subscription_payment_routes"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(topUpRecorder.Body.Bytes(), &topUpResponse))
	require.True(t, topUpResponse.Success)
	assert.Empty(t, topUpResponse.Data.PaymentRoutes)
	require.Len(t, topUpResponse.Data.SubscriptionPaymentRoutes, 1)
	assert.Equal(t, "premium_plan_checkout", topUpResponse.Data.SubscriptionPaymentRoutes[0].RouteID)

	setting.WaffoPancakeUnitPrice = 0.0001
	notReadyRecorder := httptest.NewRecorder()
	notReadyContext, _ := gin.CreateTestContext(notReadyRecorder)
	notReadyContext.Set("id", 982005)
	notReadyContext.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/plans", nil)
	GetSubscriptionPlans(notReadyContext)

	require.Equal(t, http.StatusOK, notReadyRecorder.Code)
	var notReadyResponse struct {
		Success bool                        `json:"success"`
		Data    []PublicSubscriptionPlanDTO `json:"data"`
	}
	require.NoError(t, common.Unmarshal(notReadyRecorder.Body.Bytes(), &notReadyResponse))
	require.True(t, notReadyResponse.Success)
	require.Len(t, notReadyResponse.Data, 1)
	assert.Empty(t, notReadyResponse.Data[0].Plan.ExternalPaymentRouteIDs)

	setting.WaffoPancakeUnitPrice = 1
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Update(
		"waffo_pancake_product_id", "",
	).Error)
	missingProductRecorder := httptest.NewRecorder()
	missingProductContext, _ := gin.CreateTestContext(missingProductRecorder)
	missingProductContext.Set("id", 982005)
	missingProductContext.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/plans", nil)
	GetSubscriptionPlans(missingProductContext)

	require.Equal(t, http.StatusOK, missingProductRecorder.Code)
	var missingProductResponse struct {
		Success bool                        `json:"success"`
		Data    []PublicSubscriptionPlanDTO `json:"data"`
	}
	require.NoError(t, common.Unmarshal(missingProductRecorder.Body.Bytes(), &missingProductResponse))
	require.True(t, missingProductResponse.Success)
	require.Len(t, missingProductResponse.Data, 1)
	assert.Empty(t, missingProductResponse.Data[0].Plan.ExternalPaymentRouteIDs)
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
