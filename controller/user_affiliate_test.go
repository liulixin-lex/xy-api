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

type affiliateRewardSummaryResponse struct {
	Success bool                          `json:"success"`
	Data    *model.AffiliateRewardSummary `json:"data"`
}

func TestGetAffInvitedUsersReturnsCurrentUsersInvites(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&[]model.User{
		{Id: 1, Username: "inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff1"},
		{Id: 2, Username: "other", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff2"},
		{Id: 3, Username: "alice", Password: "secret", DisplayName: "Alice", AffCode: "aff3", InviterId: 1, InviteRewardRule: model.InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, CreatedAt: 300},
		{Id: 4, Username: "bob", Password: "secret", DisplayName: "Bob", AffCode: "aff4", InviterId: 2, CreatedAt: 400},
	}).Error)
	require.NoError(t, db.Create(&model.AffiliateRewardRecord{
		InviterId:           1,
		InviteeId:           3,
		TopUpId:             1,
		InviteRewardRule:    model.InviteRewardRuleFirstTopUp,
		InviteRewardPercent: 30,
		TopUpQuota:          1000,
		RewardQuota:         300,
		CreatedAt:           500,
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
	assert.Equal(t, model.InviteRewardRuleFirstTopUp, response.Data[0].InviteRewardRule)
	assert.Equal(t, 30, response.Data[0].InviteRewardPercent)
	assert.Equal(t, 300, response.Data[0].ContributionQuota)
}

func TestGetAdminAffiliateRewardsReturnsSummaryAndFilteredRelations(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&[]model.User{
		{Id: 10, Username: "inviter-a", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff10"},
		{Id: 11, Username: "inviter-b", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff11"},
		{Id: 12, Username: "alice", Password: "secret", DisplayName: "Alice", AffCode: "aff12", InviterId: 10, InviteRewardRule: model.InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, CreatedAt: 300},
		{Id: 13, Username: "bob", Password: "secret", DisplayName: "Bob", AffCode: "aff13", InviterId: 11, InviteRewardRule: model.InviteRewardRuleContinuous, InviteRewardPercent: 5, CreatedAt: 400},
	}).Error)
	require.NoError(t, db.Create(&[]model.AffiliateRewardRecord{
		{InviterId: 10, InviteeId: 12, TopUpId: 10, InviteRewardRule: model.InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, TopUpQuota: 1000, RewardQuota: 300, CreatedAt: 500},
		{InviterId: 11, InviteeId: 13, TopUpId: 11, InviteRewardRule: model.InviteRewardRuleContinuous, InviteRewardPercent: 5, TopUpQuota: 1000, RewardQuota: 50, CreatedAt: 600},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/option/affiliate_rewards?search_field=invitee_username&search=ali", nil)

	GetAdminAffiliateRewards(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response affiliateRewardSummaryResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotNil(t, response.Data)
	assert.Equal(t, int64(2), response.Data.InviterCount)
	assert.Equal(t, int64(2), response.Data.InviteeCount)
	assert.Equal(t, int64(350), response.Data.TotalRewardQuota)
	require.Len(t, response.Data.Relations, 1)
	assert.Equal(t, "inviter-a", response.Data.Relations[0].InviterUsername)
	assert.Equal(t, "alice", response.Data.Relations[0].InviteeUsername)
	assert.Equal(t, 300, response.Data.Relations[0].RewardQuota)
}
