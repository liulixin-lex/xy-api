package controller

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runManageUserRequest(t *testing.T, body string, actorID, actorRole int, secureVerified, accessToken bool) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/user/manage", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	context.Set("id", actorID)
	context.Set("role", actorRole)
	context.Set("username", "quota-operator")
	context.Set("secure_verified", secureVerified)
	context.Set("use_access_token", accessToken)

	ManageUser(context)
	return recorder
}

func runManageUserQuotaRequest(t *testing.T, userID int, mode string, value int) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, userID, mode, value)
	return runManageUserRequest(t, body, 1, common.RoleRootUser, true, false)
}

func TestManageUserQuotaOverrideRejectsOutOfRangeValues(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710001, Username: "quota-override-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	for _, value := range []int{-1, common.MaxQuota + 1} {
		recorder := runManageUserQuotaRequest(t, user.Id, "override", value)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"success":false`)

		var persisted model.User
		require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
		assert.Equal(t, 100, persisted.Quota)
	}
}

func TestManageUserQuotaAddSubtractRejectExtremeValuesWithoutMutation(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710002, Username: "quota-boundary-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	for _, mode := range []string{"add", "subtract"} {
		recorder := runManageUserQuotaRequest(t, user.Id, mode, math.MaxInt)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"success":false`)

		var persisted model.User
		require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
		assert.Equal(t, 100, persisted.Quota)
	}
}

func TestManageUserQuotaRequiresPaymentOperationsPermission(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710003, Username: "quota-permission-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":10}`, user.Id)
	recorder := runManageUserRequest(t, body, 710099, common.RoleAdminUser, true, false)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var persisted model.User
	require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
	assert.Equal(t, 100, persisted.Quota)
}

func TestManageUserQuotaRequiresDashboardStepUp(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710004, Username: "quota-step-up-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)
	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":10}`, user.Id)

	missingStepUp := runManageUserRequest(t, body, 1, common.RoleRootUser, false, false)
	assert.Equal(t, http.StatusForbidden, missingStepUp.Code)
	assert.Contains(t, missingStepUp.Body.String(), `"code":"VERIFICATION_REQUIRED"`)

	accessToken := runManageUserRequest(t, body, 1, common.RoleRootUser, true, true)
	assert.Equal(t, http.StatusForbidden, accessToken.Code)
	assert.NotContains(t, accessToken.Body.String(), "VERIFICATION_REQUIRED")

	var persisted model.User
	require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
	assert.Equal(t, 100, persisted.Quota)
}

func TestManageUserQuotaVerifiedSessionSucceeds(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710005, Username: "quota-verified-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	recorder := runManageUserQuotaRequest(t, user.Id, "override", 125)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var persisted model.User
	require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
	assert.Equal(t, 125, persisted.Quota)
}

func TestManageUserNonQuotaActionDoesNotRequirePaymentStepUp(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	user := model.User{Id: 710006, Username: "non-quota-manage-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100}
	require.NoError(t, db.Create(&user).Error)

	body := fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id)
	recorder := runManageUserRequest(t, body, 710099, common.RoleAdminUser, false, false)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var persisted model.User
	require.NoError(t, db.Select("status", "quota").First(&persisted, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, persisted.Status)
	assert.Equal(t, 100, persisted.Quota)
}

func TestManageUserQuotaAdjustmentsInvalidateStaleRedisQuota(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		value    int
		expected int
	}{
		{name: "add", mode: "add", value: 10, expected: 110},
		{name: "subtract", mode: "subtract", value: 10, expected: 90},
		{name: "override", mode: "override", value: 125, expected: 125},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			user := model.User{
				Id: 710100 + index, Username: "quota-redis-" + test.name,
				Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100,
			}
			require.NoError(t, db.Create(&user).Error)

			previousRDB := common.RDB
			previousRedisEnabled := common.RedisEnabled
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			common.RDB = client
			common.RedisEnabled = true
			t.Cleanup(func() {
				_ = client.Close()
				common.RDB = previousRDB
				common.RedisEnabled = previousRedisEnabled
			})

			stale := user.ToBaseUser()
			stale.Quota = 777
			cacheKey := fmt.Sprintf("user:%d", user.Id)
			require.NoError(t, common.RedisHSetObj(cacheKey, stale, time.Hour))

			recorder := runManageUserQuotaRequest(t, user.Id, test.mode, test.value)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":true`)
			assert.False(t, server.Exists(cacheKey))

			quota, err := model.GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, test.expected, quota)
			var persisted model.User
			require.NoError(t, db.Select("quota").First(&persisted, user.Id).Error)
			assert.Equal(t, test.expected, persisted.Quota)
		})
	}
}
