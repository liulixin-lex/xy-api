package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetAffInvitedUsers(c *gin.Context) {
	users, err := model.GetInvitedUsers(c.GetInt("id"), model.AffiliateRelationQuery{
		SearchField: c.Query("search_field"),
		Search:      c.Query("search"),
		InviteType:  c.Query("invite_type"),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, users)
}

func GetReferralRewards(c *gin.Context) {
	dashboard, err := model.GetReferralRewardDashboard(c.GetInt("id"), common.GetTimestamp())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, dashboard)
}

func GetAdminAffiliateRewards(c *gin.Context) {
	summary, err := model.GetAffiliateRewardSummary(model.AffiliateRelationQuery{
		SearchField: c.Query("search_field"),
		Search:      c.Query("search"),
		InviteType:  c.Query("invite_type"),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}
