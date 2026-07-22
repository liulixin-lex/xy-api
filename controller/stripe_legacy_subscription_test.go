package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runStripeLegacyInventoryListRequest(path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, path, nil)
	AdminListStripeLegacySubscriptionInventory(context)
	return recorder
}

func TestAdminStripeLegacyInventoryDistinguishesMissingSchema(t *testing.T) {
	setupModelListControllerTestDB(t)

	recorder := runStripeLegacyInventoryListRequest("/api/subscription/admin/stripe/inventory")

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"stripe_inventory_schema_not_ready"}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "stripe_legacy_subscriptions")
}

func TestAdminStripeLegacyInventoryReturnsStableEmptyPage(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.StripeLegacySubscription{}))

	recorder := runStripeLegacyInventoryListRequest("/api/subscription/admin/stripe/inventory?p=1&page_size=10")

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{
		"success":true,
		"message":"",
		"data":{"page":1,"page_size":10,"total":0,"items":[]}
	}`, recorder.Body.String())
}

func TestAdminStripeLegacyInventoryRejectsInvalidFilters(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.StripeLegacySubscription{}))

	for _, path := range []string{
		"/api/subscription/admin/stripe/inventory?user_id=-1",
		"/api/subscription/admin/stripe/inventory?customer_id=attacker",
		"/api/subscription/admin/stripe/inventory?subscription_id=attacker",
	} {
		recorder := runStripeLegacyInventoryListRequest(path)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.JSONEq(t, `{"success":false,"code":"stripe_inventory_filter_invalid"}`, recorder.Body.String())
	}
}

func TestAdminStripeLegacyInventoryNormalizesNegativePagination(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.StripeLegacySubscription{}))
	require.NoError(t, db.Create(&model.StripeLegacySubscription{
		StripeSubscriptionID: "sub_pagination",
		StripeCustomerID:     "cus_pagination",
		MappingStatus:        model.StripeLegacyMappingUnmapped,
		Status:               "active",
	}).Error)

	recorder := runStripeLegacyInventoryListRequest("/api/subscription/admin/stripe/inventory?p=-2&page_size=-1")

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"page":1`)
	assert.Contains(t, recorder.Body.String(), `"page_size":10`)
	assert.Contains(t, recorder.Body.String(), `"total":1`)
}

func TestStripeLegacySubscriptionViewExposesCancellationVersion(t *testing.T) {
	view := newStripeLegacySubscriptionView(&model.StripeLegacySubscription{
		ID:                   41,
		StripeSubscriptionID: "sub_viewversion",
		StripeCustomerID:     "cus_viewversion",
		MappingStatus:        model.StripeLegacyMappingMapped,
		Status:               "active",
		CancelAtPeriodEnd:    true,
		CurrentPeriodEnd:     1_800_000_000,
		StateObservedAt:      1_700_000_000,
		UpdatedAt:            1_700_000_123,
	})

	assert.Equal(t, int64(1_700_000_123), view.ExpectedUpdatedAt)
}

func TestAdminCancelStripeLegacySubscriptionRejectsMismatchedInventoryID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "42"}}
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/subscription/admin/stripe/inventory/42/cancel-at-period-end",
		strings.NewReader(`{"inventory_id":41,"expected_updated_at":100,"reason":"stop recurring renewal"}`),
	)

	AdminCancelStripeLegacySubscriptionAtPeriodEnd(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"stripe_inventory_cancel_invalid"}`, recorder.Body.String())
}

func TestAdminCancelStripeLegacySubscriptionRejectsAccessTokenBeforeMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("use_access_token", true)
	context.Params = gin.Params{{Key: "id", Value: "42"}}
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/subscription/admin/stripe/inventory/42/cancel-at-period-end",
		strings.NewReader(`{"inventory_id":42,"expected_updated_at":100,"reason":"stop recurring renewal"}`),
	)

	AdminCancelStripeLegacySubscriptionAtPeriodEnd(context)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_operations_auth_required"}`, recorder.Body.String())
}
