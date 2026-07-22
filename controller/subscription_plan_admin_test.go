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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func adminSubscriptionPlanRequest(
	t *testing.T,
	method string,
	path string,
	plan model.SubscriptionPlan,
	params gin.Params,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := common.Marshal(AdminUpsertSubscriptionPlanRequest{Plan: plan})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = params
	context.Request = httptest.NewRequest(method, path, strings.NewReader(string(payload)))
	context.Request.Header.Set("Content-Type", "application/json")
	handler(context)
	return recorder
}

func validStripeMappingPlan(title string, stripePriceID string) model.SubscriptionPlan {
	return model.SubscriptionPlan{
		Title:            title,
		PriceAmount:      12,
		Currency:         "USD",
		DurationUnit:     model.SubscriptionDurationMonth,
		DurationValue:    1,
		QuotaResetPeriod: model.SubscriptionResetNever,
		Enabled:          true,
		TotalAmount:      1000,
		StripePriceId:    stripePriceID,
	}
}

func TestAdminSubscriptionPlanStripePriceIDCreateUpdateContractSQLite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))
	confirmPaymentComplianceForTest(t)

	createRecorder := adminSubscriptionPlanRequest(
		t,
		http.MethodPost,
		"/api/subscription/admin/plans",
		validStripeMappingPlan("Legacy mapping", "  price_legacy_create  "),
		nil,
		AdminCreateSubscriptionPlan,
	)
	require.Equal(t, http.StatusOK, createRecorder.Code)
	assert.Contains(t, createRecorder.Body.String(), `"success":true`)

	var stored model.SubscriptionPlan
	require.NoError(t, db.Where("title = ?", "Legacy mapping").First(&stored).Error)
	assert.Equal(t, "price_legacy_create", stored.StripePriceId)

	invalidCreateRecorder := adminSubscriptionPlanRequest(
		t,
		http.MethodPost,
		"/api/subscription/admin/plans",
		validStripeMappingPlan("Invalid mapping", "product_not_a_stripe_price"),
		nil,
		AdminCreateSubscriptionPlan,
	)
	require.Equal(t, http.StatusBadRequest, invalidCreateRecorder.Code)
	assert.Contains(t, invalidCreateRecorder.Body.String(), `"success":false`)
	assert.Contains(t, invalidCreateRecorder.Body.String(), `"code":"subscription_plan_invalid"`)
	assert.NotContains(t, invalidCreateRecorder.Body.String(), "product_not_a_stripe_price")
	var planCount int64
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Count(&planCount).Error)
	assert.EqualValues(t, 1, planCount)

	updatePath := "/api/subscription/admin/plans/" + strconv.Itoa(stored.Id)
	updateParams := gin.Params{{Key: "id", Value: strconv.Itoa(stored.Id)}}
	updateRecorder := adminSubscriptionPlanRequest(
		t,
		http.MethodPut,
		updatePath,
		validStripeMappingPlan("Legacy mapping", "  price_legacy_updated  "),
		updateParams,
		AdminUpdateSubscriptionPlan,
	)
	require.Equal(t, http.StatusOK, updateRecorder.Code)
	assert.Contains(t, updateRecorder.Body.String(), `"success":true`)
	require.NoError(t, db.First(&stored, stored.Id).Error)
	assert.Equal(t, "price_legacy_updated", stored.StripePriceId)

	oversizedPriceID := "price_" + strings.Repeat("a", 123)
	invalidUpdateRecorder := adminSubscriptionPlanRequest(
		t,
		http.MethodPut,
		updatePath,
		validStripeMappingPlan("Legacy mapping", oversizedPriceID),
		updateParams,
		AdminUpdateSubscriptionPlan,
	)
	require.Equal(t, http.StatusBadRequest, invalidUpdateRecorder.Code)
	assert.Contains(t, invalidUpdateRecorder.Body.String(), `"success":false`)
	assert.Contains(t, invalidUpdateRecorder.Body.String(), `"code":"subscription_plan_invalid"`)
	assert.NotContains(t, invalidUpdateRecorder.Body.String(), oversizedPriceID)
	require.NoError(t, db.First(&stored, stored.Id).Error)
	assert.Equal(t, "price_legacy_updated", stored.StripePriceId)
}
