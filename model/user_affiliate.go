package model

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InviteRewardRuleContinuous   = "continuous"
	InviteRewardRuleFirstTopUp   = "first_topup"
	InviteRewardRuleInitialQuota = "initial_quota"

	AffiliateRewardStatusPending     = "pending"
	AffiliateRewardStatusAvailable   = "available"
	AffiliateRewardStatusTransferred = "transferred"
	AffiliateRewardStatusCanceled    = "canceled"

	AffiliateRewardWaitSeconds = 7 * 24 * 60 * 60

	legacyInviteRewardRuleContinuous = "continuous_5"
	legacyInviteRewardRuleFirstTopUp = "first_topup_10"

	legacyContinuousRewardPercent = 5
	legacyFirstTopUpRewardPercent = 10
)

type AffiliateRewardRecord struct {
	Id                  int    `json:"id"`
	InviterId           int    `json:"inviter_id" gorm:"index"`
	InviteeId           int    `json:"invitee_id" gorm:"index"`
	TopUpId             int    `json:"topup_id" gorm:"index"`
	InviteLinkBatchId   int    `json:"invite_link_batch_id" gorm:"type:int;column:invite_link_batch_id;index"`
	ActivityDetail      string `json:"activity_detail" gorm:"type:varchar(255);column:activity_detail"`
	InviteRewardRule    string `json:"invite_reward_rule" gorm:"type:varchar(32);column:invite_reward_rule"`
	InviteRewardPercent int    `json:"invite_reward_percent" gorm:"type:int;column:invite_reward_percent"`
	TopUpQuota          int    `json:"topup_quota" gorm:"type:int;column:topup_quota"`
	RewardQuota         int    `json:"reward_quota" gorm:"type:int;column:reward_quota"`
	Status              string `json:"status" gorm:"type:varchar(16);column:status;index"`
	AvailableAt         int64  `json:"available_at" gorm:"column:available_at;index"`
	TransferredQuota    int    `json:"transferred_quota" gorm:"type:int;column:transferred_quota"`
	TransferredAt       int64  `json:"transferred_at" gorm:"column:transferred_at;index"`
	CanceledAt          int64  `json:"canceled_at" gorm:"column:canceled_at;index"`
	CreatedAt           int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
}

type InviteInitialQuotaRecord struct {
	Id                int    `json:"id"`
	InviterId         int    `json:"inviter_id" gorm:"index"`
	InviteeId         int    `json:"invitee_id" gorm:"uniqueIndex:idx_invite_initial_quota_once"`
	InviteLinkBatchId int    `json:"invite_link_batch_id" gorm:"type:int;column:invite_link_batch_id;uniqueIndex:idx_invite_initial_quota_once;index"`
	ActivityDetail    string `json:"activity_detail" gorm:"type:varchar(255);column:activity_detail"`
	Quota             int    `json:"quota" gorm:"type:int;column:quota"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
}

type InvitedUser struct {
	Id                      int                    `json:"id"`
	Username                string                 `json:"username"`
	DisplayName             string                 `json:"display_name"`
	CreatedAt               int64                  `json:"created_at"`
	InviteRewardRule        string                 `json:"invite_reward_rule"`
	InviteRewardPercent     int                    `json:"invite_reward_percent"`
	FirstTopupRewardPercent int                    `json:"first_topup_reward_percent"`
	ContinuousRewardPercent int                    `json:"continuous_reward_percent"`
	ActivityRules           InviteRewardActivities `json:"activity_rules"`
	ContributionQuota       int                    `json:"contribution_quota"`
	PendingRewardQuota      int                    `json:"pending_reward_quota"`
	AvailableRewardQuota    int                    `json:"available_reward_quota"`
	TransferredRewardQuota  int                    `json:"transferred_reward_quota"`
	CanceledRewardQuota     int                    `json:"canceled_reward_quota"`
	InitialQuota            int                    `json:"initial_quota"`
}

type AffiliateRelation struct {
	InviterId               int                    `json:"inviter_id"`
	InviterUsername         string                 `json:"inviter_username"`
	InviteeId               int                    `json:"invitee_id"`
	InviteeUsername         string                 `json:"invitee_username"`
	InviteeDisplayName      string                 `json:"invitee_display_name"`
	InviteRewardRule        string                 `json:"invite_reward_rule"`
	InviteRewardPercent     int                    `json:"invite_reward_percent"`
	FirstTopupRewardPercent int                    `json:"first_topup_reward_percent"`
	ContinuousRewardPercent int                    `json:"continuous_reward_percent"`
	ActivityRules           InviteRewardActivities `json:"activity_rules"`
	RewardQuota             int                    `json:"reward_quota"`
	PendingRewardQuota      int                    `json:"pending_reward_quota"`
	AvailableRewardQuota    int                    `json:"available_reward_quota"`
	TransferredRewardQuota  int                    `json:"transferred_reward_quota"`
	CanceledRewardQuota     int                    `json:"canceled_reward_quota"`
	InitialQuota            int                    `json:"initial_quota"`
	RegisteredAt            int64                  `json:"registered_at"`
}

type AffiliateRewardSummary struct {
	InviterCount           int64               `json:"inviter_count"`
	InviteeCount           int64               `json:"invitee_count"`
	TotalRewardQuota       int64               `json:"total_reward_quota"`
	TotalInitialQuota      int64               `json:"total_initial_quota"`
	PendingRewardQuota     int64               `json:"pending_reward_quota"`
	AvailableRewardQuota   int64               `json:"available_reward_quota"`
	TransferredRewardQuota int64               `json:"transferred_reward_quota"`
	CanceledRewardQuota    int64               `json:"canceled_reward_quota"`
	Relations              []AffiliateRelation `json:"relations"`
}

type ReferralRewardDashboard struct {
	ActiveBatch            *InviteLinkBatch `json:"active_batch"`
	InviteLink             string           `json:"invite_link"`
	PendingRewardQuota     int64            `json:"pending_reward_quota"`
	AvailableRewardQuota   int64            `json:"available_reward_quota"`
	TransferredRewardQuota int64            `json:"transferred_reward_quota"`
	CanceledRewardQuota    int64            `json:"canceled_reward_quota"`
	InvitedUserCount       int64            `json:"invited_user_count"`
	InvitedUsers           []InvitedUser    `json:"invited_users"`
}

type AffiliateRelationQuery struct {
	SearchField     string
	Search          string
	InviteType      string
	RegisteredStart int64
	RegisteredEnd   int64
	RewardPercent   *int
}

type affiliateRelationRow struct {
	InviterId               int
	InviterUsername         string
	InviteeId               int
	InviteeUsername         string
	InviteeDisplayName      string
	InviteRewardRule        string
	InviteRewardPercent     int
	FirstTopupRewardPercent int
	ContinuousRewardPercent int
	ActivityRules           InviteRewardActivities `gorm:"column:activity_rules"`
	RewardQuota             int
	InitialQuota            int
	RegisteredAt            int64
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

func IssueInviteInitialQuota(tx *gorm.DB, user *User) error {
	if tx == nil {
		tx = DB
	}
	if user == nil || user.Id == 0 || user.InviterId == 0 || user.InviteLinkBatchId == 0 {
		return nil
	}

	activities := NormalizeInviteRewardActivities(user.InviteRewardRulesSnapshot)
	quota := CalculateInviteInitialQuota(activities)
	if quota <= 0 {
		return nil
	}

	now := common.GetTimestamp()
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "invitee_id"},
			{Name: "invite_link_batch_id"},
		},
		DoNothing: true,
	}).Create(&InviteInitialQuotaRecord{
		InviterId:         user.InviterId,
		InviteeId:         user.Id,
		InviteLinkBatchId: user.InviteLinkBatchId,
		ActivityDetail:    inviteInitialQuotaActivityDetail(activities),
		Quota:             quota,
		CreatedAt:         now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}

	if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", gorm.Expr("quota + ?", quota)).Error; err != nil {
		return err
	}
	user.Quota += quota
	return nil
}

func inviteInitialQuotaActivityDetail(activities InviteRewardActivities) string {
	parts := make([]string, 0)
	for _, activity := range NormalizeInviteRewardActivities(activities) {
		if activity.Type != InviteRewardRuleInitialQuota || activity.Quota <= 0 {
			continue
		}
		if activity.ActivityDetail != "" {
			parts = append(parts, activity.ActivityDetail)
		}
	}
	if len(parts) == 0 {
		return "Initial Quota"
	}
	detail := strings.Join(parts, ", ")
	runes := []rune(detail)
	if len(runes) > InviteRewardActivityDetailMaxLength {
		return string(runes[:InviteRewardActivityDetailMaxLength])
	}
	return detail
}

func GetInvitedUsers(inviterId int, query AffiliateRelationQuery) ([]InvitedUser, error) {
	rows, err := listAffiliateRelations(&inviterId, query)
	if err != nil {
		return nil, err
	}
	users := make([]InvitedUser, 0, len(rows))
	for _, row := range rows {
		rule := NormalizeInviteRewardRule(row.InviteRewardRule)
		firstTopupPercent, continuousPercent := resolveInviteRewardPercents(
			row.InviteRewardRule,
			row.InviteRewardPercent,
			row.FirstTopupRewardPercent,
			row.ContinuousRewardPercent,
		)
		activityRules := resolveAffiliateRelationActivityRules(row, firstTopupPercent, continuousPercent)
		users = append(users, InvitedUser{
			Id:                      row.InviteeId,
			Username:                row.InviteeUsername,
			DisplayName:             row.InviteeDisplayName,
			CreatedAt:               row.RegisteredAt,
			InviteRewardRule:        rule,
			InviteRewardPercent:     ResolveInviteRewardPercent(row.InviteRewardRule, row.InviteRewardPercent),
			FirstTopupRewardPercent: firstTopupPercent,
			ContinuousRewardPercent: continuousPercent,
			ActivityRules:           activityRules,
			ContributionQuota:       row.RewardQuota,
			InitialQuota:            row.InitialQuota,
		})
	}
	if err := fillInvitedUserRewardBreakdowns(inviterId, users); err != nil {
		return nil, err
	}
	return users, nil
}

func GetReferralRewardDashboard(inviterId int, now int64) (*ReferralRewardDashboard, error) {
	if _, err := SettleAvailableAffiliateRewards(inviterId, now); err != nil {
		return nil, err
	}

	var inviter User
	if err := DB.Select("id", "aff_code").First(&inviter, inviterId).Error; err != nil {
		return nil, err
	}

	dashboard := &ReferralRewardDashboard{}
	activeBatch, err := GetActiveInviteLinkBatchAt(now)
	if err == nil {
		dashboard.ActiveBatch = activeBatch
		dashboard.InviteLink = BuildInviteLinkForUser(activeBatch.BaseLink, inviter.AffCode)
	} else if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	if err := DB.Model(&User{}).Where("inviter_id = ?", inviterId).Count(&dashboard.InvitedUserCount).Error; err != nil {
		return nil, err
	}

	var records []AffiliateRewardRecord
	if err := DB.Where("inviter_id = ?", inviterId).Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		pending, available, transferred, canceled := splitAffiliateRewardQuota(record)
		dashboard.PendingRewardQuota += int64(pending)
		dashboard.AvailableRewardQuota += int64(available)
		dashboard.TransferredRewardQuota += int64(transferred)
		dashboard.CanceledRewardQuota += int64(canceled)
	}

	return dashboard, nil
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
	if err := DB.Model(&InviteInitialQuotaRecord{}).Select("COALESCE(SUM(quota), 0)").Scan(&total).Error; err != nil {
		return nil, err
	}
	if total.Valid {
		summary.TotalInitialQuota = total.Int64
	}
	if err := fillAffiliateRewardSummaryBreakdown(summary); err != nil {
		return nil, err
	}

	rows, err := listAffiliateRelations(nil, query)
	if err != nil {
		return nil, err
	}
	summary.Relations = make([]AffiliateRelation, 0, len(rows))
	for _, row := range rows {
		rule := NormalizeInviteRewardRule(row.InviteRewardRule)
		firstTopupPercent, continuousPercent := resolveInviteRewardPercents(
			row.InviteRewardRule,
			row.InviteRewardPercent,
			row.FirstTopupRewardPercent,
			row.ContinuousRewardPercent,
		)
		activityRules := resolveAffiliateRelationActivityRules(row, firstTopupPercent, continuousPercent)
		relation := AffiliateRelation{
			InviterId:               row.InviterId,
			InviterUsername:         row.InviterUsername,
			InviteeId:               row.InviteeId,
			InviteeUsername:         row.InviteeUsername,
			InviteeDisplayName:      row.InviteeDisplayName,
			InviteRewardRule:        rule,
			InviteRewardPercent:     ResolveInviteRewardPercent(row.InviteRewardRule, row.InviteRewardPercent),
			FirstTopupRewardPercent: firstTopupPercent,
			ContinuousRewardPercent: continuousPercent,
			ActivityRules:           activityRules,
			RewardQuota:             row.RewardQuota,
			InitialQuota:            row.InitialQuota,
			RegisteredAt:            row.RegisteredAt,
		}
		if err := fillAffiliateRelationRewardBreakdown(&relation); err != nil {
			return nil, err
		}
		summary.Relations = append(summary.Relations, relation)
	}

	return summary, nil
}

func fillAffiliateRewardSummaryBreakdown(summary *AffiliateRewardSummary) error {
	var records []AffiliateRewardRecord
	if err := DB.Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		pending, available, transferred, canceled := splitAffiliateRewardQuota(record)
		summary.PendingRewardQuota += int64(pending)
		summary.AvailableRewardQuota += int64(available)
		summary.TransferredRewardQuota += int64(transferred)
		summary.CanceledRewardQuota += int64(canceled)
	}
	return nil
}

func fillAffiliateRelationRewardBreakdown(relation *AffiliateRelation) error {
	var records []AffiliateRewardRecord
	if err := DB.Where("inviter_id = ? AND invitee_id = ?", relation.InviterId, relation.InviteeId).Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		pending, available, transferred, canceled := splitAffiliateRewardQuota(record)
		relation.PendingRewardQuota += pending
		relation.AvailableRewardQuota += available
		relation.TransferredRewardQuota += transferred
		relation.CanceledRewardQuota += canceled
	}
	return nil
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

func resolveInviteRewardPercents(rule string, percent int, firstTopupPercent int, continuousPercent int) (int, int) {
	if firstTopupPercent > 0 || continuousPercent > 0 {
		return normalizeRewardPercent(firstTopupPercent), normalizeRewardPercent(continuousPercent)
	}
	resolvedPercent := ResolveInviteRewardPercent(rule, percent)
	if NormalizeInviteRewardRule(rule) == InviteRewardRuleFirstTopUp {
		return resolvedPercent, 0
	}
	return 0, resolvedPercent
}

func resolveAffiliateRelationActivityRules(row affiliateRelationRow, firstTopupPercent int, continuousPercent int) InviteRewardActivities {
	if len(row.ActivityRules) > 0 {
		return row.ActivityRules
	}
	return inviteRewardActivitiesFromSnapshotPercents(firstTopupPercent, continuousPercent)
}

func filterAffiliateRelationRowsByRewardPercent(rows []affiliateRelationRow, rewardPercent *int) []affiliateRelationRow {
	if rewardPercent == nil {
		return rows
	}
	filtered := make([]affiliateRelationRow, 0, len(rows))
	for _, row := range rows {
		firstTopupPercent, continuousPercent := resolveInviteRewardPercents(
			row.InviteRewardRule,
			row.InviteRewardPercent,
			row.FirstTopupRewardPercent,
			row.ContinuousRewardPercent,
		)
		for _, activity := range resolveAffiliateRelationActivityRules(row, firstTopupPercent, continuousPercent) {
			if activity.Type != InviteRewardRuleFirstTopUp && activity.Type != InviteRewardRuleContinuous {
				continue
			}
			if activity.Percent == *rewardPercent {
				filtered = append(filtered, row)
				break
			}
		}
	}
	return filtered
}

func listAffiliateRelations(inviterId *int, query AffiliateRelationQuery) ([]affiliateRelationRow, error) {
	rewardTotals := DB.Model(&AffiliateRewardRecord{}).
		Select("inviter_id, invitee_id, SUM(reward_quota) AS reward_quota").
		Group("inviter_id, invitee_id")
	initialQuotaTotals := DB.Model(&InviteInitialQuotaRecord{}).
		Select("inviter_id, invitee_id, SUM(quota) AS initial_quota").
		Group("inviter_id, invitee_id")

	db := DB.Table("users AS invitees").
		Select(`inviters.id AS inviter_id,
			inviters.username AS inviter_username,
			invitees.id AS invitee_id,
			invitees.username AS invitee_username,
				invitees.display_name AS invitee_display_name,
				invitees.invite_reward_rule AS invite_reward_rule,
				invitees.invite_reward_percent AS invite_reward_percent,
				invitees.invite_first_topup_reward_percent AS first_topup_reward_percent,
				invitees.invite_continuous_reward_percent AS continuous_reward_percent,
				invitees.invite_reward_rules_snapshot AS activity_rules,
				invitees.created_at AS registered_at,
				COALESCE(reward_totals.reward_quota, 0) AS reward_quota,
				COALESCE(initial_quota_totals.initial_quota, 0) AS initial_quota`).
		Joins("LEFT JOIN users AS inviters ON inviters.id = invitees.inviter_id").
		Joins("LEFT JOIN (?) AS reward_totals ON reward_totals.inviter_id = invitees.inviter_id AND reward_totals.invitee_id = invitees.id", rewardTotals).
		Joins("LEFT JOIN (?) AS initial_quota_totals ON initial_quota_totals.inviter_id = invitees.inviter_id AND initial_quota_totals.invitee_id = invitees.id", initialQuotaTotals).
		Where("invitees.inviter_id <> ?", 0)

	if inviterId != nil {
		db = db.Where("invitees.inviter_id = ?", *inviterId)
	}
	if query.RegisteredStart > 0 {
		db = db.Where("invitees.created_at >= ?", query.RegisteredStart)
	}
	if query.RegisteredEnd > 0 {
		db = db.Where("invitees.created_at <= ?", query.RegisteredEnd)
	}
	db = applyAffiliateRelationFilters(db, query)

	rows := make([]affiliateRelationRow, 0)
	err := db.Order("invitees.id desc").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return filterAffiliateRelationRowsByRewardPercent(rows, query.RewardPercent), nil
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

func fillInvitedUserRewardBreakdowns(inviterId int, users []InvitedUser) error {
	for index := range users {
		var records []AffiliateRewardRecord
		if err := DB.Where("inviter_id = ? AND invitee_id = ?", inviterId, users[index].Id).Find(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			pending, available, transferred, canceled := splitAffiliateRewardQuota(record)
			users[index].PendingRewardQuota += pending
			users[index].AvailableRewardQuota += available
			users[index].TransferredRewardQuota += transferred
			users[index].CanceledRewardQuota += canceled
		}
	}
	return nil
}

func splitAffiliateRewardQuota(record AffiliateRewardRecord) (pending int, available int, transferred int, canceled int) {
	switch record.Status {
	case AffiliateRewardStatusPending:
		pending = record.RewardQuota
	case AffiliateRewardStatusTransferred:
		transferred = record.TransferredQuota
		if transferred <= 0 {
			transferred = record.RewardQuota
		}
	case AffiliateRewardStatusCanceled:
		transferred = record.TransferredQuota
		if transferred < 0 {
			transferred = 0
		}
		if transferred > record.RewardQuota {
			transferred = record.RewardQuota
		}
		canceled = record.RewardQuota - transferred
	case AffiliateRewardStatusAvailable, "":
		available = record.RewardQuota - record.TransferredQuota
		if available < 0 {
			available = 0
		}
		transferred = record.TransferredQuota
	}
	return pending, available, transferred, canceled
}

func CancelAffiliateRewardRecord(recordId int, now int64) error {
	if recordId == 0 {
		return errors.New("affiliate reward record id is required")
	}
	if now <= 0 {
		now = GetDBTimestamp()
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		var record AffiliateRewardRecord
		if err := lockForUpdate(tx).First(&record, recordId).Error; err != nil {
			return err
		}
		if record.Status == AffiliateRewardStatusCanceled {
			if record.CanceledAt != 0 {
				return nil
			}
			return tx.Model(&AffiliateRewardRecord{}).Where("id = ?", record.Id).Update("canceled_at", now).Error
		}

		remainingQuota := record.RewardQuota - record.TransferredQuota
		if remainingQuota < 0 {
			remainingQuota = 0
		}
		if record.Status == AffiliateRewardStatusAvailable || record.Status == "" {
			if remainingQuota > 0 {
				if err := tx.Model(&User{}).Where("id = ?", record.InviterId).Updates(map[string]interface{}{
					"aff_quota":   gorm.Expr("CASE WHEN aff_quota >= ? THEN aff_quota - ? ELSE 0 END", remainingQuota, remainingQuota),
					"aff_history": gorm.Expr("CASE WHEN aff_history >= ? THEN aff_history - ? ELSE 0 END", remainingQuota, remainingQuota),
				}).Error; err != nil {
					return err
				}
			}
		}

		return tx.Model(&AffiliateRewardRecord{}).Where("id = ?", record.Id).Updates(map[string]interface{}{
			"status":      AffiliateRewardStatusCanceled,
			"canceled_at": now,
		}).Error
	})
}

func SettleAvailableAffiliateRewards(inviterId int, now int64) (int, error) {
	if inviterId == 0 {
		return 0, nil
	}
	var settled int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		settled, err = settleAvailableAffiliateRewardsTx(tx, inviterId, now)
		return err
	})
	return settled, err
}

func settleAvailableAffiliateRewardsTx(tx *gorm.DB, inviterId int, now int64) (int, error) {
	var records []AffiliateRewardRecord
	if err := lockForUpdate(tx).
		Where("inviter_id = ? AND status = ? AND available_at <= ?", inviterId, AffiliateRewardStatusPending, now).
		Order("available_at asc, id asc").
		Find(&records).Error; err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}

	total := 0
	for _, record := range records {
		result := tx.Model(&AffiliateRewardRecord{}).
			Where("id = ? AND status = ?", record.Id, AffiliateRewardStatusPending).
			Update("status", AffiliateRewardStatusAvailable)
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 1 {
			total += record.RewardQuota
		}
	}
	if total <= 0 {
		return 0, nil
	}
	if err := tx.Model(&User{}).Where("id = ?", inviterId).Updates(map[string]interface{}{
		"aff_quota":   gorm.Expr("aff_quota + ?", total),
		"aff_history": gorm.Expr("aff_history + ?", total),
	}).Error; err != nil {
		return 0, err
	}

	return total, nil
}

func markTransferredAffiliateRewardsTx(tx *gorm.DB, inviterId int, quota int, now int64) error {
	if inviterId == 0 || quota <= 0 {
		return nil
	}

	var records []AffiliateRewardRecord
	if err := lockForUpdate(tx).
		Where("inviter_id = ? AND status IN ?", inviterId, []string{AffiliateRewardStatusAvailable, ""}).
		Order("available_at asc, id asc").
		Find(&records).Error; err != nil {
		return err
	}

	remaining := quota
	for _, record := range records {
		if remaining <= 0 {
			break
		}
		transferable := record.RewardQuota - record.TransferredQuota
		if transferable <= 0 {
			continue
		}
		moveQuota := transferable
		if remaining < transferable {
			moveQuota = remaining
		}
		nextTransferredQuota := record.TransferredQuota + moveQuota
		nextStatus := AffiliateRewardStatusAvailable
		if nextTransferredQuota >= record.RewardQuota {
			nextStatus = AffiliateRewardStatusTransferred
		}
		if err := tx.Model(&AffiliateRewardRecord{}).Where("id = ?", record.Id).Updates(map[string]interface{}{
			"transferred_quota": nextTransferredQuota,
			"transferred_at":    now,
			"status":            nextStatus,
		}).Error; err != nil {
			return err
		}
		remaining -= moveQuota
	}

	return nil
}
