package model

import (
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
