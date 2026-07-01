package model

const (
	InviteRewardRuleContinuous = "continuous_5"
	InviteRewardRuleFirstTopUp = "first_topup_10"
)

type InvitedUser struct {
	Id          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	CreatedAt   int64  `json:"created_at"`
}

func NormalizeInviteRewardRule(rule string) string {
	switch rule {
	case InviteRewardRuleFirstTopUp, "first_topup":
		return InviteRewardRuleFirstTopUp
	default:
		return InviteRewardRuleContinuous
	}
}

func GetInvitedUsers(inviterId int) ([]InvitedUser, error) {
	users := make([]InvitedUser, 0)
	err := DB.Model(&User{}).
		Select("id", "username", "display_name", "created_at").
		Where("inviter_id = ?", inviterId).
		Order("id desc").
		Find(&users).Error
	return users, err
}
