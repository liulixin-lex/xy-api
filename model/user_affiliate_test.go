package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetInvitedUsersReturnsOnlySafeUsersForInviter(t *testing.T) {
	truncateTables(t)

	users := []User{
		{Id: 1, Username: "inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff1"},
		{Id: 2, Username: "other-inviter", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff2"},
		{Id: 3, Username: "alice", DisplayName: "Alice", Password: "secret", AffCode: "aff3", InviterId: 1, InviteRewardRule: InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, CreatedAt: 300},
		{Id: 4, Username: "bob", DisplayName: "Bob", Password: "secret", AffCode: "aff4", InviterId: 1, InviteRewardRule: "continuous_5", CreatedAt: 400},
		{Id: 5, Username: "mallory", DisplayName: "Mallory", Password: "secret", AffCode: "aff5", InviterId: 2, CreatedAt: 500},
	}
	require.NoError(t, DB.Create(&users).Error)
	require.NoError(t, DB.Create(&[]AffiliateRewardRecord{
		{InviterId: 1, InviteeId: 3, TopUpId: 10, InviteRewardRule: InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, TopUpQuota: 1000, RewardQuota: 300, CreatedAt: 600},
		{InviterId: 1, InviteeId: 3, TopUpId: 11, InviteRewardRule: InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, TopUpQuota: 2000, RewardQuota: 600, CreatedAt: 700},
		{InviterId: 1, InviteeId: 4, TopUpId: 12, InviteRewardRule: InviteRewardRuleContinuous, InviteRewardPercent: 5, TopUpQuota: 1000, RewardQuota: 50, CreatedAt: 800},
	}).Error)

	invited, err := GetInvitedUsers(1, AffiliateRelationQuery{})

	require.NoError(t, err)
	require.Len(t, invited, 2)
	assert.Equal(t, 4, invited[0].Id)
	assert.Equal(t, "bob", invited[0].Username)
	assert.Equal(t, "Bob", invited[0].DisplayName)
	assert.Equal(t, int64(400), invited[0].CreatedAt)
	assert.Equal(t, InviteRewardRuleContinuous, invited[0].InviteRewardRule)
	assert.Equal(t, 5, invited[0].InviteRewardPercent)
	assert.Equal(t, 50, invited[0].ContributionQuota)
	assert.Equal(t, 3, invited[1].Id)
	assert.Equal(t, "alice", invited[1].Username)
	assert.Equal(t, InviteRewardRuleFirstTopUp, invited[1].InviteRewardRule)
	assert.Equal(t, 30, invited[1].InviteRewardPercent)
	assert.Equal(t, 900, invited[1].ContributionQuota)
}

func TestNormalizeInviteRewardRuleUsesTypeWithoutPercent(t *testing.T) {
	assert.Equal(t, InviteRewardRuleContinuous, NormalizeInviteRewardRule(""))
	assert.Equal(t, InviteRewardRuleContinuous, NormalizeInviteRewardRule("continuous"))
	assert.Equal(t, InviteRewardRuleContinuous, NormalizeInviteRewardRule("continuous_5"))
	assert.Equal(t, InviteRewardRuleFirstTopUp, NormalizeInviteRewardRule("first_topup"))
	assert.Equal(t, InviteRewardRuleFirstTopUp, NormalizeInviteRewardRule("first_topup_10"))
}

func TestInsertSnapshotsCurrentInviteRewardPercent(t *testing.T) {
	truncateTables(t)
	withAffiliateRewardPercents(t, 3, 30)

	require.NoError(t, DB.Create(&User{
		Id:       20,
		Username: "inviter",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff20",
	}).Error)

	user := &User{
		Username:         "invitee",
		Password:         "password",
		Status:           common.UserStatusEnabled,
		AffCode:          "aff21",
		InviterId:        20,
		InviteRewardRule: InviteRewardRuleFirstTopUp,
	}

	require.NoError(t, user.Insert(20))

	var inserted User
	require.NoError(t, DB.Where("username = ?", "invitee").First(&inserted).Error)
	assert.Equal(t, InviteRewardRuleFirstTopUp, inserted.InviteRewardRule)
	assert.Equal(t, 30, inserted.InviteRewardPercent)
}

func TestGetAffiliateRewardSummaryUsesBindingsAndRewardRecords(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&[]User{
		{Id: 30, Username: "inviter-a", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff30"},
		{Id: 31, Username: "inviter-b", Password: "secret", Status: common.UserStatusEnabled, AffCode: "aff31"},
		{Id: 32, Username: "alice", Password: "secret", DisplayName: "Alice", Status: common.UserStatusEnabled, AffCode: "aff32", InviterId: 30, InviteRewardRule: InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, CreatedAt: 100},
		{Id: 33, Username: "bob", Password: "secret", DisplayName: "Bob", Status: common.UserStatusEnabled, AffCode: "aff33", InviterId: 31, InviteRewardRule: InviteRewardRuleContinuous, InviteRewardPercent: 5, CreatedAt: 200},
	}).Error)
	require.NoError(t, DB.Create(&[]AffiliateRewardRecord{
		{InviterId: 30, InviteeId: 32, TopUpId: 21, InviteRewardRule: InviteRewardRuleFirstTopUp, InviteRewardPercent: 30, TopUpQuota: 1000, RewardQuota: 300, CreatedAt: 300},
		{InviterId: 31, InviteeId: 33, TopUpId: 22, InviteRewardRule: InviteRewardRuleContinuous, InviteRewardPercent: 5, TopUpQuota: 1000, RewardQuota: 50, CreatedAt: 400},
	}).Error)

	summary, err := GetAffiliateRewardSummary(AffiliateRelationQuery{
		SearchField: "invitee_username",
		Search:      "ali",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(2), summary.InviterCount)
	assert.Equal(t, int64(2), summary.InviteeCount)
	assert.Equal(t, int64(350), summary.TotalRewardQuota)
	require.Len(t, summary.Relations, 1)
	assert.Equal(t, "inviter-a", summary.Relations[0].InviterUsername)
	assert.Equal(t, "alice", summary.Relations[0].InviteeUsername)
	assert.Equal(t, InviteRewardRuleFirstTopUp, summary.Relations[0].InviteRewardRule)
	assert.Equal(t, 30, summary.Relations[0].InviteRewardPercent)
	assert.Equal(t, 300, summary.Relations[0].RewardQuota)
	assert.Equal(t, int64(100), summary.Relations[0].RegisteredAt)
}
