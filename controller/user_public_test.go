package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSelfOmitsStripeCustomerIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	user := &model.User{
		Id: 982101, Username: "public_payment_user", DisplayName: "Public Payment User",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		StripeCustomer: "cus_internal_identity_must_not_leave_server",
	}
	require.NoError(t, db.Create(user).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", user.Id)
	context.Set("role", user.Role)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/user/self", nil)

	GetSelf(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"username":"public_payment_user"`)
	assert.NotContains(t, body, "stripe_customer")
	assert.NotContains(t, body, "cus_internal_identity_must_not_leave_server")
}
