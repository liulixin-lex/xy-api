package controller

import (
	"net/http"
	"net/http/httptest"
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

func setupBillingReservationAdminControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.BillingReservation{},
		&model.QuotaLedgerEntry{},
		&model.BillingReservationAdminResolution{},
		&model.SubscriptionPreConsumeRecord{},
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

func TestResolveBillingReservationForAdminRequiresDashboardSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("use_access_token", true)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))

	ResolveBillingReservationForAdmin(context)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "dashboard session authentication")
}

func TestResolveBillingReservationForAdminSettlesWithVersionedRequest(t *testing.T) {
	db := setupBillingReservationAdminControllerDB(t)
	user := &model.User{Id: 914001, Username: "billing-admin-controller", Quota: 100, Status: common.UserStatusEnabled}
	token := &model.Token{
		Id:          924001,
		UserId:      user.Id,
		Key:         "billing-admin-controller",
		Name:        "billing-admin-controller",
		Status:      common.TokenStatusEnabled,
		RemainQuota: 100,
	}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(token).Error)
	created, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     "billing-admin-controller",
		UserId:        user.Id,
		TokenId:       token.Id,
		FundingSource: model.BillingFundingWallet,
		Quota:         40,
	}, token.Key)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.BillingReservation{}).
		Where("request_id = ?", created.Reservation.RequestId).
		Updates(map[string]interface{}{
			"last_reconciled_at": common.GetTimestamp(),
			"reconcile_note":     "manual review required",
		}).Error)

	body := `{"request_id":"billing-admin-controller","expected_version":1,"resolution":"settle","actual_quota":25,"reason":"verified provider final usage"}`
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "request_id", Value: created.Reservation.RequestId}}
	context.Set("id", 99)
	context.Set("username", "billing-admin")
	context.Set("role", common.RoleAdminUser)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	ResolveBillingReservationForAdmin(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"applied":true`)
	var currentUser model.User
	require.NoError(t, db.Select("quota").Where("id = ?", user.Id).First(&currentUser).Error)
	assert.Equal(t, 75, currentUser.Quota)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Params = context.Params
	secondContext.Set("id", 99)
	secondContext.Set("username", "billing-admin")
	secondContext.Set("role", common.RoleAdminUser)
	secondContext.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	ResolveBillingReservationForAdmin(secondContext)
	assert.Equal(t, http.StatusOK, secondRecorder.Code)
	assert.Contains(t, secondRecorder.Body.String(), `"applied":false`)
	require.NoError(t, db.Select("quota").Where("id = ?", user.Id).First(&currentUser).Error)
	assert.Equal(t, 75, currentUser.Quota)
}

func TestListBillingReservationsForAdminRejectsInvalidUserFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/?user_id=not-a-number", nil)

	ListBillingReservationsForAdmin(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
