package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestBuildInviteLinkBatchDefaultsToReferralRoute(t *testing.T) {
	baseLink := BuildInviteLinkBatchBaseLink("", "spring")

	assert.Equal(t, "/r/spring", baseLink)
	assert.Equal(t, "/r/spring?aff=aff123", BuildInviteLinkForUser(baseLink, "aff123"))
}

func TestCreateInviteLinkBatchDefaultsBaseLinkToReferralRoute(t *testing.T) {
	truncateTables(t)

	require.NoError(t, CreateInviteLinkBatch(&InviteLinkBatch{
		Name:                    "Default referral route",
		Code:                    "default-route",
		FirstTopupRewardPercent: 30,
		ContinuousRewardPercent: 5,
		StartTime:               1_800_000_000,
		EndTime:                 1_800_086_400,
		IsActive:                true,
	}))

	var batch InviteLinkBatch
	require.NoError(t, DB.Where("code = ?", "default-route").First(&batch).Error)
	assert.Equal(t, "/r/default-route", batch.BaseLink)
}

func TestCreateInviteLinkBatchValidatesCodeForReferralRoute(t *testing.T) {
	validCodes := []string{"spring", "spring-2026", "partner_abc", "ABC123"}
	for _, code := range validCodes {
		t.Run("valid "+code, func(t *testing.T) {
			truncateTables(t)

			err := CreateInviteLinkBatch(&InviteLinkBatch{
				Name:                    "Valid code",
				Code:                    code,
				FirstTopupRewardPercent: 30,
				ContinuousRewardPercent: 5,
				StartTime:               1_800_000_000,
				EndTime:                 1_800_086_400,
				IsActive:                true,
			})

			require.NoError(t, err)
		})
	}

	invalidCodes := []string{"spring/2026", "spring 2026", "春季", "spring?x=1", "spring#top"}
	for _, code := range invalidCodes {
		t.Run("invalid "+code, func(t *testing.T) {
			truncateTables(t)

			err := CreateInviteLinkBatch(&InviteLinkBatch{
				Name:                    "Invalid code",
				Code:                    code,
				FirstTopupRewardPercent: 30,
				ContinuousRewardPercent: 5,
				StartTime:               1_800_000_000,
				EndTime:                 1_800_086_400,
				IsActive:                true,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invite link batch code")
		})
	}
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
			{ActivityDetail: "New user quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
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
		{ActivityDetail: "New user quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
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
		{ActivityDetail: "New user quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
	}, inserted.InviteRewardRulesSnapshot)
	assert.Equal(t, 30, inserted.InviteFirstTopupRewardPercent)
	assert.Equal(t, 5, inserted.InviteContinuousRewardPercent)
}

func TestCalculateInviteInitialQuotaSumsQuotaActivitiesOnly(t *testing.T) {
	activities := InviteRewardActivities{
		{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 30, Quota: 9000},
		{ActivityDetail: "Ongoing", Type: InviteRewardRuleContinuous, Percent: 5, Quota: 8000},
		{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
		{ActivityDetail: "Partner quota", Type: InviteRewardRuleInitialQuota, Quota: 250},
	}

	assert.Equal(t, 750, CalculateInviteInitialQuota(activities))
}

func TestCreateInviteLinkBatchRejectsNegativeInitialQuota(t *testing.T) {
	truncateTables(t)

	err := CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Negative initial quota",
		Code:     "negative-initial-quota",
		BaseLink: "/sign-up?invite_batch=negative-initial-quota",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: -1},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
		IsActive:  true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-negative")

	var count int64
	require.NoError(t, DB.Model(&InviteLinkBatch{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestCreateInviteLinkBatchAllowsInitialQuotaAboveOneHundred(t *testing.T) {
	truncateTables(t)

	require.NoError(t, CreateInviteLinkBatch(&InviteLinkBatch{
		Name:     "Large initial quota",
		Code:     "large-initial-quota",
		BaseLink: "/sign-up?invite_batch=large-initial-quota",
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: 101},
		},
		StartTime: 1_800_000_000,
		EndTime:   1_800_086_400,
		IsActive:  true,
	}))

	var batch InviteLinkBatch
	require.NoError(t, DB.Where("code = ?", "large-initial-quota").First(&batch).Error)
	require.Len(t, batch.ActivityRules, 1)
	assert.Equal(t, 101, batch.ActivityRules[0].Quota)
}

func TestInviteInitialQuotaIssuedOnceAndExcludedFromAffiliateRewards(t *testing.T) {
	truncateTables(t)

	commonQuotaForNewUser := common.QuotaForNewUser
	common.QuotaForNewUser = 100
	t.Cleanup(func() {
		common.QuotaForNewUser = commonQuotaForNewUser
	})

	require.NoError(t, DB.Create(&User{
		Id:       91,
		Username: "inviter-initial-quota",
		Password: "secret",
		AffCode:  "aff91",
	}).Error)

	user := &User{
		Username: "invitee-initial-quota",
		Password: "password",
		Status:   1,
	}
	user.ApplyInviteLinkBinding(&InviteLinkBinding{
		InviterId:         91,
		InviteLinkBatchId: 92,
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
			{ActivityDetail: "Launch first top-up", Type: InviteRewardRuleFirstTopUp, Percent: 30},
			{ActivityDetail: "Ongoing", Type: InviteRewardRuleContinuous, Percent: 5},
		},
		BoundAt: 1_800_000_001,
	})

	require.NoError(t, user.Insert(91))
	require.NoError(t, IssueInviteInitialQuota(DB, user))
	require.NoError(t, IssueInviteInitialQuota(DB, user))

	var inserted User
	require.NoError(t, DB.Where("username = ?", "invitee-initial-quota").First(&inserted).Error)
	assert.Equal(t, 600, inserted.Quota)

	var initialRecords []InviteInitialQuotaRecord
	require.NoError(t, DB.Find(&initialRecords).Error)
	require.Len(t, initialRecords, 1)
	assert.Equal(t, 91, initialRecords[0].InviterId)
	assert.Equal(t, inserted.Id, initialRecords[0].InviteeId)
	assert.Equal(t, 92, initialRecords[0].InviteLinkBatchId)
	assert.Equal(t, "Signup quota", initialRecords[0].ActivityDetail)
	assert.Equal(t, 500, initialRecords[0].Quota)

	var affiliateRewardCount int64
	require.NoError(t, DB.Model(&AffiliateRewardRecord{}).Count(&affiliateRewardCount).Error)
	assert.Equal(t, int64(0), affiliateRewardCount)
}

func TestIssueInviteInitialQuotaTreatsConcurrentDuplicateAsAlreadyIssued(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&User{
		Id:       93,
		Username: "inviter-initial-quota-race",
		Password: "secret",
		AffCode:  "aff93",
	}).Error)
	require.NoError(t, DB.Create(&User{
		Id:       94,
		Username: "invitee-initial-quota-race",
		Password: "secret",
		Status:   1,
		Quota:    100,
	}).Error)

	user := &User{
		Id:                94,
		InviterId:         93,
		InviteLinkBatchId: 95,
		Quota:             100,
		InviteRewardRulesSnapshot: InviteRewardActivities{
			{ActivityDetail: "Signup quota", Type: InviteRewardRuleInitialQuota, Quota: 500},
		},
	}

	callbackName := "test:insert_initial_quota_duplicate"
	triggered := false
	require.NoError(t, DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		record, ok := tx.Statement.Dest.(*InviteInitialQuotaRecord)
		if !ok || triggered {
			return
		}
		triggered = true
		tx.Exec(
			"INSERT INTO invite_initial_quota_records (inviter_id, invitee_id, invite_link_batch_id, activity_detail, quota, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			record.InviterId,
			record.InviteeId,
			record.InviteLinkBatchId,
			record.ActivityDetail,
			record.Quota,
			record.CreatedAt,
		)
	}))
	t.Cleanup(func() {
		require.NoError(t, DB.Callback().Create().Remove(callbackName))
	})

	require.NoError(t, IssueInviteInitialQuota(DB, user))
	assert.True(t, triggered)

	var inserted User
	require.NoError(t, DB.First(&inserted, 94).Error)
	assert.Equal(t, 100, inserted.Quota)

	var count int64
	require.NoError(t, DB.Model(&InviteInitialQuotaRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestIssueInviteInitialQuotaTruncatesCombinedActivityDetail(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&User{
		Id:       96,
		Username: "inviter-initial-quota-long",
		Password: "secret",
		AffCode:  "aff96",
	}).Error)

	user := &User{
		Username: "invitee-initial-quota-long",
		Password: "password",
		Status:   1,
	}
	user.ApplyInviteLinkBinding(&InviteLinkBinding{
		InviterId:         96,
		InviteLinkBatchId: 97,
		ActivityRules: InviteRewardActivities{
			{ActivityDetail: strings.Repeat("一", 200), Type: InviteRewardRuleInitialQuota, Quota: 500},
			{ActivityDetail: strings.Repeat("二", 200), Type: InviteRewardRuleInitialQuota, Quota: 250},
		},
		BoundAt: 1_800_000_001,
	})

	require.NoError(t, user.Insert(96))

	var record InviteInitialQuotaRecord
	require.NoError(t, DB.First(&record).Error)
	assert.LessOrEqual(t, len([]rune(record.ActivityDetail)), InviteRewardActivityDetailMaxLength)
	assert.Equal(t, 750, record.Quota)
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
