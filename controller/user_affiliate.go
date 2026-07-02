package controller

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetAffInvitedUsers(c *gin.Context) {
	users, err := model.GetInvitedUsers(c.GetInt("id"), affiliateRelationQueryFromRequest(c))
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
	summary, err := model.GetAffiliateRewardSummary(affiliateRelationQueryFromRequest(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}

func affiliateRelationQueryFromRequest(c *gin.Context) model.AffiliateRelationQuery {
	query := model.AffiliateRelationQuery{
		SearchField: c.Query("search_field"),
		Search:      c.Query("search"),
		InviteType:  c.Query("invite_type"),
	}
	query.RegisteredStart = int64Query(c, "registered_start", "start_time")
	query.RegisteredEnd = int64Query(c, "registered_end", "end_time")
	if percent, ok := intQuery(c, "reward_percent"); ok {
		query.RewardPercent = &percent
	}
	return query
}

func int64Query(c *gin.Context, names ...string) int64 {
	for _, name := range names {
		value := strings.TrimSpace(c.Query(name))
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func intQuery(c *gin.Context, name string) (int, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
