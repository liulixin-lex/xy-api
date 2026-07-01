package model

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const (
	InviteRewardRuleContinuous = "continuous"
	InviteRewardRuleFirstTopUp = "first_topup"

	legacyInviteRewardRuleContinuous = "continuous_5"
	legacyInviteRewardRuleFirstTopUp = "first_topup_10"

	legacyContinuousRewardPercent = 5
	legacyFirstTopUpRewardPercent = 10
)

type AffiliateRewardRecord struct {
	Id                  int    `json:"id"`
	InviterId           int    `json:"inviter_id" gorm:"index"`
	InviteeId           int    `json:"invitee_id" gorm:"index"`
	TopUpId             int    `json:"topup_id" gorm:"uniqueIndex"`
	InviteRewardRule    string `json:"invite_reward_rule" gorm:"type:varchar(32);column:invite_reward_rule"`
	InviteRewardPercent int    `json:"invite_reward_percent" gorm:"type:int;column:invite_reward_percent"`
	TopUpQuota          int    `json:"topup_quota" gorm:"type:int;column:topup_quota"`
	RewardQuota         int    `json:"reward_quota" gorm:"type:int;column:reward_quota"`
	CreatedAt           int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
}

type InvitedUser struct {
	Id                  int    `json:"id"`
	Username            string `json:"username"`
	DisplayName         string `json:"display_name"`
	CreatedAt           int64  `json:"created_at"`
	InviteRewardRule    string `json:"invite_reward_rule"`
	InviteRewardPercent int    `json:"invite_reward_percent"`
	ContributionQuota   int    `json:"contribution_quota"`
}

type AffiliateRelation struct {
	InviterId           int    `json:"inviter_id"`
	InviterUsername     string `json:"inviter_username"`
	InviteeId           int    `json:"invitee_id"`
	InviteeUsername     string `json:"invitee_username"`
	InviteeDisplayName  string `json:"invitee_display_name"`
	InviteRewardRule    string `json:"invite_reward_rule"`
	InviteRewardPercent int    `json:"invite_reward_percent"`
	RewardQuota         int    `json:"reward_quota"`
	RegisteredAt        int64  `json:"registered_at"`
}

type AffiliateRewardSummary struct {
	InviterCount     int64               `json:"inviter_count"`
	InviteeCount     int64               `json:"invitee_count"`
	TotalRewardQuota int64               `json:"total_reward_quota"`
	Relations        []AffiliateRelation `json:"relations"`
}

type AffiliateRelationQuery struct {
	SearchField string
	Search      string
	InviteType  string
}

type affiliateRelationRow struct {
	InviterId           int
	InviterUsername     string
	InviteeId           int
	InviteeUsername     string
	InviteeDisplayName  string
	InviteRewardRule    string
	InviteRewardPercent int
	RewardQuota         int
	RegisteredAt        int64
}

func NormalizeInviteRewardRule(rule string) string {
	switch strings.TrimSpace(rule) {
	case InviteRewardRuleFirstTopUp, legacyInviteRewardRuleFirstTopUp:
		return InviteRewardRuleFirstTopUp
	case InviteRewardRuleContinuous, legacyInviteRewardRuleContinuous:
		return InviteRewardRuleContinuous
	default:
		return InviteRewardRuleContinuous
	}
}

func (user *User) ApplyInviteRewardBinding(inviterId int) {
	user.InviteRewardRule = NormalizeInviteRewardRule(user.InviteRewardRule)
	if inviterId == 0 {
		user.InviteRewardPercent = 0
		return
	}
	if user.InviteRewardPercent > 0 {
		user.InviteRewardPercent = normalizeRewardPercent(user.InviteRewardPercent)
		return
	}
	user.InviteRewardPercent = currentInviteRewardPercent(user.InviteRewardRule)
}

func ResolveInviteRewardPercent(rule string, percent int) int {
	if percent > 0 {
		return normalizeRewardPercent(percent)
	}
	switch strings.TrimSpace(rule) {
	case legacyInviteRewardRuleContinuous:
		return legacyContinuousRewardPercent
	case legacyInviteRewardRuleFirstTopUp:
		return legacyFirstTopUpRewardPercent
	}
	switch NormalizeInviteRewardRule(rule) {
	case InviteRewardRuleFirstTopUp:
		return operation_setting.GetPaymentSetting().AffiliateFirstTopupPercent
	default:
		return operation_setting.GetPaymentSetting().AffiliateContinuousPercent
	}
}

func GetInvitedUsers(inviterId int, query AffiliateRelationQuery) ([]InvitedUser, error) {
	rows, err := listAffiliateRelations(&inviterId, query)
	if err != nil {
		return nil, err
	}
	users := make([]InvitedUser, 0, len(rows))
	for _, row := range rows {
		rule := NormalizeInviteRewardRule(row.InviteRewardRule)
		users = append(users, InvitedUser{
			Id:                  row.InviteeId,
			Username:            row.InviteeUsername,
			DisplayName:         row.InviteeDisplayName,
			CreatedAt:           row.RegisteredAt,
			InviteRewardRule:    rule,
			InviteRewardPercent: ResolveInviteRewardPercent(row.InviteRewardRule, row.InviteRewardPercent),
			ContributionQuota:   row.RewardQuota,
		})
	}
	return users, err
}

func GetAffiliateRewardSummary(query AffiliateRelationQuery) (*AffiliateRewardSummary, error) {
	summary := &AffiliateRewardSummary{}

	if err := DB.Model(&User{}).Where("inviter_id <> ?", 0).Distinct("inviter_id").Count(&summary.InviterCount).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&User{}).Where("inviter_id <> ?", 0).Count(&summary.InviteeCount).Error; err != nil {
		return nil, err
	}

	var total sql.NullInt64
	if err := DB.Model(&AffiliateRewardRecord{}).Select("COALESCE(SUM(reward_quota), 0)").Scan(&total).Error; err != nil {
		return nil, err
	}
	if total.Valid {
		summary.TotalRewardQuota = total.Int64
	}

	rows, err := listAffiliateRelations(nil, query)
	if err != nil {
		return nil, err
	}
	summary.Relations = make([]AffiliateRelation, 0, len(rows))
	for _, row := range rows {
		rule := NormalizeInviteRewardRule(row.InviteRewardRule)
		summary.Relations = append(summary.Relations, AffiliateRelation{
			InviterId:           row.InviterId,
			InviterUsername:     row.InviterUsername,
			InviteeId:           row.InviteeId,
			InviteeUsername:     row.InviteeUsername,
			InviteeDisplayName:  row.InviteeDisplayName,
			InviteRewardRule:    rule,
			InviteRewardPercent: ResolveInviteRewardPercent(row.InviteRewardRule, row.InviteRewardPercent),
			RewardQuota:         row.RewardQuota,
			RegisteredAt:        row.RegisteredAt,
		})
	}

	return summary, nil
}

func normalizeRewardPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func currentInviteRewardPercent(rule string) int {
	setting := operation_setting.GetPaymentSetting()
	if NormalizeInviteRewardRule(rule) == InviteRewardRuleFirstTopUp {
		return normalizeRewardPercent(setting.AffiliateFirstTopupPercent)
	}
	return normalizeRewardPercent(setting.AffiliateContinuousPercent)
}

func listAffiliateRelations(inviterId *int, query AffiliateRelationQuery) ([]affiliateRelationRow, error) {
	rewardTotals := DB.Model(&AffiliateRewardRecord{}).
		Select("inviter_id, invitee_id, SUM(reward_quota) AS reward_quota").
		Group("inviter_id, invitee_id")

	db := DB.Table("users AS invitees").
		Select(`inviters.id AS inviter_id,
			inviters.username AS inviter_username,
			invitees.id AS invitee_id,
			invitees.username AS invitee_username,
			invitees.display_name AS invitee_display_name,
			invitees.invite_reward_rule AS invite_reward_rule,
			invitees.invite_reward_percent AS invite_reward_percent,
			invitees.created_at AS registered_at,
			COALESCE(reward_totals.reward_quota, 0) AS reward_quota`).
		Joins("LEFT JOIN users AS inviters ON inviters.id = invitees.inviter_id").
		Joins("LEFT JOIN (?) AS reward_totals ON reward_totals.inviter_id = invitees.inviter_id AND reward_totals.invitee_id = invitees.id", rewardTotals).
		Where("invitees.inviter_id <> ?", 0)

	if inviterId != nil {
		db = db.Where("invitees.inviter_id = ?", *inviterId)
	}
	db = applyAffiliateRelationFilters(db, query)

	rows := make([]affiliateRelationRow, 0)
	err := db.Order("invitees.id desc").Scan(&rows).Error
	return rows, err
}

func applyAffiliateRelationFilters(db *gorm.DB, query AffiliateRelationQuery) *gorm.DB {
	inviteType := NormalizeInviteRewardRule(query.InviteType)
	if strings.TrimSpace(query.InviteType) != "" {
		db = db.Where("invitees.invite_reward_rule IN ?", legacyValuesForInviteType(inviteType))
	}

	search := strings.TrimSpace(query.Search)
	if search == "" {
		return db
	}
	likeValue := "%" + search + "%"
	switch strings.TrimSpace(query.SearchField) {
	case "inviter_username":
		return db.Where("inviters.username LIKE ?", likeValue)
	case "invitee_username", "username":
		return db.Where("invitees.username LIKE ?", likeValue)
	case "invitee_display_name", "display_name":
		return db.Where("invitees.display_name LIKE ?", likeValue)
	case "invite_type":
		return db.Where("invitees.invite_reward_rule IN ?", legacyValuesForInviteType(NormalizeInviteRewardRule(search)))
	case "invite_percent":
		percent, err := strconv.Atoi(search)
		if err != nil {
			return db
		}
		legacyRules := make([]string, 0)
		if percent == legacyContinuousRewardPercent {
			legacyRules = append(legacyRules, legacyInviteRewardRuleContinuous)
		}
		if percent == legacyFirstTopUpRewardPercent {
			legacyRules = append(legacyRules, legacyInviteRewardRuleFirstTopUp)
		}
		if len(legacyRules) == 0 {
			return db.Where("invitees.invite_reward_percent = ?", percent)
		}
		return db.Where("invitees.invite_reward_percent = ? OR invitees.invite_reward_rule IN ?", percent, legacyRules)
	case "reward_quota", "contribution_quota":
		quota, err := strconv.Atoi(search)
		if err != nil {
			return db
		}
		return db.Where("COALESCE(reward_totals.reward_quota, 0) = ?", quota)
	default:
		return db.Where("invitees.username LIKE ? OR invitees.display_name LIKE ? OR inviters.username LIKE ?", likeValue, likeValue, likeValue)
	}
}

func legacyValuesForInviteType(rule string) []string {
	if NormalizeInviteRewardRule(rule) == InviteRewardRuleFirstTopUp {
		return []string{InviteRewardRuleFirstTopUp, legacyInviteRewardRuleFirstTopUp}
	}
	return []string{InviteRewardRuleContinuous, legacyInviteRewardRuleContinuous, ""}
}
