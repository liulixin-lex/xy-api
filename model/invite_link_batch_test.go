package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInviteLinkBatchValidityUsesShanghaiWindow(t *testing.T) {
	batch := InviteLinkBatch{
		Code:      "batch-a",
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
	}

	assert.False(t, batch.IsValidAt(1_799_999_999))
	assert.True(t, batch.IsValidAt(1_800_000_000))
	assert.True(t, batch.IsValidAt(1_800_086_400))
	assert.False(t, batch.IsValidAt(1_800_086_401))
}

func TestSetActiveInviteLinkBatchKeepsSingleActiveBatch(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&[]InviteLinkBatch{
		{
			Id:                      1,
			Name:                    "Spring",
			Code:                    "spring",
			BaseLink:                "https://example.com/sign-up?invite_batch=spring",
			FirstTopupRewardPercent: 30,
			ContinuousRewardPercent: 5,
			StartTime:               1_800_000_000,
			EndTime:                 1_800_086_400,
			IsActive:                true,
		},
		{
			Id:                      2,
			Name:                    "Summer",
			Code:                    "summer",
			BaseLink:                "https://example.com/sign-up?invite_batch=summer",
			FirstTopupRewardPercent: 40,
			ContinuousRewardPercent: 8,
			StartTime:               1_800_000_000,
			EndTime:                 1_800_086_400,
			IsActive:                false,
		},
	}).Error)

	require.NoError(t, SetActiveInviteLinkBatch(2))

	active, err := GetActiveInviteLinkBatchAt(1_800_000_001)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, 2, active.Id)

	var batches []InviteLinkBatch
	require.NoError(t, DB.Order("id asc").Find(&batches).Error)
	require.Len(t, batches, 2)
	assert.False(t, batches[0].IsActive)
	assert.True(t, batches[1].IsActive)
}

func TestResolveInviteLinkBindingRequiresBatchAndAffiliateCode(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&User{
		Id:       10,
		Username: "inviter",
		Password: "secret",
		AffCode:  "aff10",
	}).Error)
	require.NoError(t, DB.Create(&InviteLinkBatch{
		Id:                      20,
		Name:                    "Spring",
		Code:                    "spring",
		BaseLink:                "https://example.com/sign-up?invite_batch=spring",
		FirstTopupRewardPercent: 35,
		ContinuousRewardPercent: 7,
		StartTime:               1_800_000_000,
		EndTime:                 1_800_086_400,
		IsActive:                true,
	}).Error)

	missingBatch, err := ResolveInviteLinkBinding("", "aff10", now)
	require.NoError(t, err)
	assert.Nil(t, missingBatch)

	missingAff, err := ResolveInviteLinkBinding("spring", "", now)
	require.NoError(t, err)
	assert.Nil(t, missingAff)

	unknownAff, err := ResolveInviteLinkBinding("spring", "missing", now)
	require.NoError(t, err)
	assert.Nil(t, unknownAff)

	expired, err := ResolveInviteLinkBinding("spring", "aff10", 1_800_086_401)
	require.NoError(t, err)
	assert.Nil(t, expired)

	binding, err := ResolveInviteLinkBinding("spring", "aff10", now)
	require.NoError(t, err)
	require.NotNil(t, binding)
	assert.Equal(t, 10, binding.InviterId)
	assert.Equal(t, 20, binding.InviteLinkBatchId)
	assert.Equal(t, 35, binding.FirstTopupRewardPercent)
	assert.Equal(t, 7, binding.ContinuousRewardPercent)
	assert.Equal(t, now, binding.BoundAt)
}

func TestResolveInviteLinkBindingSnapshotsActivityRules(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&User{
		Id:       12,
		Username: "inviter",
		Password: "secret",
		AffCode:  "aff12",
	}).Error)
	require.NoError(t, DB.Create(&InviteLinkBatch{
		Id:       22,
		Name:     "Stacked",
		Code:     "stacked",
		BaseLink: "https://example.com/sign-up?invite_batch=stacked",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 20},
			{ActivityDetail: "VIP first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 10},
			{ActivityDetail: "Ongoing partner", Type: InviteRewardRuleContinuous, Percent: 5},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
		IsActive:  true,
	}).Error)

	binding, err := ResolveInviteLinkBinding("stacked", "aff12", now)
	require.NoError(t, err)
	require.NotNil(t, binding)
	assert.Equal(t, 30, binding.FirstTopupRewardPercent)
	assert.Equal(t, 5, binding.ContinuousRewardPercent)
	assert.Equal(t, InviteRewardActivities{
		{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 20},
		{ActivityDetail: "VIP first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 10},
		{ActivityDetail: "Ongoing partner", Type: InviteRewardRuleContinuous, Percent: 5},
	}, binding.ActivityRules)

	user := &User{
		Username: "activity-snapshot",
		Password: "password",
		Status:   1,
	}
	user.ApplyInviteLinkBinding(binding)
	require.NoError(t, user.Insert(binding.InviterId))

	require.NoError(t, UpdateInviteLinkBatch(&InviteLinkBatch{
		Id:       22,
		Name:     "Stacked edited",
		Code:     "stacked",
		BaseLink: "https://example.com/sign-up?invite_batch=stacked",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Edited continuous", Type: InviteRewardRuleContinuous, Percent: 50},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
		IsActive:  true,
	}))

	var inserted User
	require.NoError(t, DB.Where("username = ?", "activity-snapshot").First(&inserted).Error)
	assert.Equal(t, InviteRewardActivities{
		{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 20},
		{ActivityDetail: "VIP first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 10},
		{ActivityDetail: "Ongoing partner", Type: InviteRewardRuleContinuous, Percent: 5},
	}, inserted.InviteRewardRulesSnapshot)
	assert.Equal(t, 30, inserted.InviteFirstTopupRewardPercent)
	assert.Equal(t, 5, inserted.InviteContinuousRewardPercent)
}

func TestResolveInviteLinkBindingKeepsZeroPercentFirstTopupActivity(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&User{
		Id:       13,
		Username: "inviter-zero-first",
		Password: "secret",
		AffCode:  "aff13",
	}).Error)
	require.NoError(t, DB.Create(&InviteLinkBatch{
		Id:       23,
		Name:     "Zero first top-up",
		Code:     "zero-first",
		BaseLink: "https://example.com/sign-up?invite_batch=zero-first",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "No first top-up bonus", Type: InviteRewardRuleFirstTopUp, Percent: 0},
			{ActivityDetail: "Ongoing partner", Type: InviteRewardRuleContinuous, Percent: 5},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
		IsActive:  true,
	}).Error)

	binding, err := ResolveInviteLinkBinding("zero-first", "aff13", now)

	require.NoError(t, err)
	require.NotNil(t, binding)
	assert.Equal(t, 0, binding.FirstTopupRewardPercent)
	assert.Equal(t, 5, binding.ContinuousRewardPercent)
}

func TestResolveInviteLinkBindingRequiresActiveBatch(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&User{
		Id:       11,
		Username: "inviter",
		Password: "secret",
		AffCode:  "aff11",
	}).Error)
	require.NoError(t, DB.Create(&InviteLinkBatch{
		Id:                      21,
		Name:                    "Inactive",
		Code:                    "inactive",
		BaseLink:                "https://example.com/sign-up?invite_batch=inactive",
		FirstTopupRewardPercent: 35,
		ContinuousRewardPercent: 7,
		StartTime:               now - 60,
		EndTime:                 now + 3600,
		IsActive:                false,
	}).Error)

	binding, err := ResolveInviteLinkBinding("inactive", "aff11", now)

	require.NoError(t, err)
	assert.Nil(t, binding)
}

func TestInsertPersistsInviteLinkBatchSnapshot(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&User{
		Id:       30,
		Username: "inviter",
		Password: "secret",
		AffCode:  "aff30",
	}).Error)
	require.NoError(t, DB.Create(&InviteLinkBatch{
		Id:                      40,
		Name:                    "Spring",
		Code:                    "spring",
		BaseLink:                "https://example.com/sign-up?invite_batch=spring",
		FirstTopupRewardPercent: 32,
		ContinuousRewardPercent: 6,
		StartTime:               1_800_000_000,
		EndTime:                 1_800_086_400,
		IsActive:                true,
	}).Error)

	binding, err := ResolveInviteLinkBinding("spring", "aff30", now)
	require.NoError(t, err)
	require.NotNil(t, binding)

	user := &User{
		Username: "invitee",
		Password: "password",
		Status:   1,
	}
	user.ApplyInviteLinkBinding(binding)
	require.NoError(t, user.Insert(binding.InviterId))

	var inserted User
	require.NoError(t, DB.Where("username = ?", "invitee").First(&inserted).Error)
	assert.Equal(t, 30, inserted.InviterId)
	assert.Equal(t, 40, inserted.InviteLinkBatchId)
	assert.Equal(t, 32, inserted.InviteFirstTopupRewardPercent)
	assert.Equal(t, 6, inserted.InviteContinuousRewardPercent)
	assert.Equal(t, now, inserted.InviteBoundAt)
}

func TestListInviteLinkBatchesWithStatsPutsActiveBatchFirst(t *testing.T) {
	truncateTables(t)
	now := int64(1_800_000_001)

	require.NoError(t, DB.Create(&[]InviteLinkBatch{
		{
			Id:                      1,
			Name:                    "Expired",
			Code:                    "expired",
			BaseLink:                "https://example.com/sign-up?invite_batch=expired",
			FirstTopupRewardPercent: 20,
			ContinuousRewardPercent: 3,
			StartTime:               now - 3600,
			EndTime:                 now - 60,
		},
		{
			Id:                      2,
			Name:                    "Active",
			Code:                    "active",
			BaseLink:                "https://example.com/sign-up?invite_batch=active",
			FirstTopupRewardPercent: 35,
			ContinuousRewardPercent: 7,
			StartTime:               now - 60,
			EndTime:                 now + 3600,
			IsActive:                true,
		},
	}).Error)
	require.NoError(t, DB.Create(&[]User{
		{Id: 100, Username: "invitee-a", Password: "secret", AffCode: "aff100", InviterId: 1, InviteLinkBatchId: 2},
		{Id: 101, Username: "invitee-b", Password: "secret", AffCode: "aff101", InviterId: 1, InviteLinkBatchId: 2},
	}).Error)

	batches, err := ListInviteLinkBatchesWithStats(now)
	require.NoError(t, err)
	require.Len(t, batches, 2)
	assert.Equal(t, 2, batches[0].Id)
	assert.True(t, batches[0].IsActive)
	assert.True(t, batches[0].IsValid)
	assert.Equal(t, int64(2), batches[0].UsageCount)
	assert.Equal(t, 1, batches[1].Id)
	assert.False(t, batches[1].IsValid)
	assert.Equal(t, int64(0), batches[1].UsageCount)
}

func TestCreateInviteLinkBatchRejectsOversizedActivityDescription(t *testing.T) {
	truncateTables(t)

	err := CreateInviteLinkBatch(&InviteLinkBatch{
		Name:                    "Oversized",
		Code:                    "oversized",
		BaseLink:                "https://example.com/sign-up?invite_batch=oversized",
		FirstTopupRewardPercent: 30,
		ContinuousRewardPercent: 5,
		StartTime:               1_800_000_000,
		EndTime:                 1_800_086_400,
		DescriptionMode:         InviteDescriptionModeCustom,
		CustomDescription:       strings.Repeat("x", 16*1024+1),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity description is too long")
}

func TestCreateInviteLinkBatchValidatesActivityRules(t *testing.T) {
	truncateTables(t)

	err := CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Invalid",
		Code:     "invalid",
		BaseLink: "https://example.com/sign-up?invite_batch=invalid",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "", Type: InviteRewardRuleFirstTopUp, Percent: 30},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity detail is required")

	err = CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Invalid",
		Code:     "invalid",
		BaseLink: "https://example.com/sign-up?invite_batch=invalid",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: strings.Repeat("x", 256), Type: InviteRewardRuleFirstTopUp, Percent: 30},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity detail is too long")

	err = CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Invalid",
		Code:     "invalid",
		BaseLink: "https://example.com/sign-up?invite_batch=invalid",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Launch", Type: "", Percent: 30},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity type is required")

	err = CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Invalid",
		Code:     "invalid",
		BaseLink: "https://example.com/sign-up?invite_batch=invalid",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Launch", Type: InviteRewardRuleFirstTopUp, Percent: 101},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity reward percent must be between 0 and 100")
}
