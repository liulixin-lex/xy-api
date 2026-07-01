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

type invitedUsersResponse struct {
	Success bool                `json:"success"`
	Data    []model.InvitedUser `json:"data"`
}

func TestGetAffInvitedUsersReturnsCurrentUsersInvites(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&[]model.User{
		{Id: 1, Username: "inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff1"},
		{Id: 2, Username: "other", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff2"},
		{Id: 3, Username: "alice", Password: "secret", DisplayName: "Alice", AffCode: "aff3", InviterId: 1, CreatedAt: 300},
		{Id: 4, Username: "bob", Password: "secret", DisplayName: "Bob", AffCode: "aff4", InviterId: 2, CreatedAt: 400},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/aff/invited", nil)

	GetAffInvitedUsers(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response invitedUsersResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "alice", response.Data[0].Username)
	assert.Equal(t, "Alice", response.Data[0].DisplayName)
}
