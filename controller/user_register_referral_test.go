package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterRequiresInviteBatchAndAffCodeForReferralBinding(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	now := common.GetTimestamp()

	originalRegisterEnabled := common.RegisterEnabled
	originalPasswordRegisterEnabled := common.PasswordRegisterEnabled
	originalEmailVerificationEnabled := common.EmailVerificationEnabled
	originalGenerateDefaultToken := constant.GenerateDefaultToken
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	constant.GenerateDefaultToken = false
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
		common.PasswordRegisterEnabled = originalPasswordRegisterEnabled
		common.EmailVerificationEnabled = originalEmailVerificationEnabled
		constant.GenerateDefaultToken = originalGenerateDefaultToken
	})

	require.NoError(t, db.Create(&model.User{
		Id:       1,
		Username: "inviter",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff1",
	}).Error)
	require.NoError(t, db.Create(&model.InviteLinkBatch{
		Id:                      2,
		Name:                    "Spring",
		Code:                    "spring",
		BaseLink:                "https://example.com/sign-up?invite_batch=spring",
		FirstTopupRewardPercent: 35,
		ContinuousRewardPercent: 7,
		StartTime:               now - 60,
		EndTime:                 now + 3600,
		IsActive:                true,
	}).Error)

	registerUser(t, gin.H{
		"username": "onlyaff",
		"password": "password123",
		"aff_code": "aff1",
	})
	var onlyAff model.User
	require.NoError(t, db.Where("username = ?", "onlyaff").First(&onlyAff).Error)
	assert.Equal(t, 0, onlyAff.InviterId)
	assert.Equal(t, 0, onlyAff.InviteLinkBatchId)

	registerUser(t, gin.H{
		"username":     "withbatch",
		"password":     "password123",
		"aff_code":     "aff1",
		"invite_batch": "spring",
	})
	var withBatch model.User
	require.NoError(t, db.Where("username = ?", "withbatch").First(&withBatch).Error)
	assert.Equal(t, 1, withBatch.InviterId)
	assert.Equal(t, 2, withBatch.InviteLinkBatchId)
	assert.Equal(t, 35, withBatch.InviteFirstTopupRewardPercent)
	assert.Equal(t, 7, withBatch.InviteContinuousRewardPercent)
	assert.NotZero(t, withBatch.InviteBoundAt)
}

func TestRegisterAppliesInviteInitialQuotaFromBatch(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	now := common.GetTimestamp()

	originalRegisterEnabled := common.RegisterEnabled
	originalPasswordRegisterEnabled := common.PasswordRegisterEnabled
	originalEmailVerificationEnabled := common.EmailVerificationEnabled
	originalGenerateDefaultToken := constant.GenerateDefaultToken
	originalQuotaForNewUser := common.QuotaForNewUser
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	common.QuotaForNewUser = 100
	constant.GenerateDefaultToken = false
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
		common.PasswordRegisterEnabled = originalPasswordRegisterEnabled
		common.EmailVerificationEnabled = originalEmailVerificationEnabled
		common.QuotaForNewUser = originalQuotaForNewUser
		constant.GenerateDefaultToken = originalGenerateDefaultToken
	})

	require.NoError(t, db.Create(&model.User{
		Id:       101,
		Username: "inviter-initial-quota",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff101",
	}).Error)
	require.NoError(t, db.Create(&model.InviteLinkBatch{
		Id:       102,
		Name:     "Initial quota",
		Code:     "initial-quota",
		BaseLink: "https://example.com/sign-up?invite_batch=initial-quota",
		ActivityRules: model.InviteRewardActivities{
			{ActivityDetail: "Signup quota", Type: model.InviteRewardRuleInitialQuota, Quota: 500},
			{ActivityDetail: "Launch first top-up", Type: model.InviteRewardRuleFirstTopUp, Percent: 30},
			{ActivityDetail: "Ongoing", Type: model.InviteRewardRuleContinuous, Percent: 5},
		},
		StartTime: now - 60,
		EndTime:   now + 3600,
		IsActive:  true,
	}).Error)

	registerUser(t, gin.H{
		"username":     "withinitialquota",
		"password":     "password123",
		"aff_code":     "aff101",
		"invite_batch": "initial-quota",
	})

	var registered model.User
	require.NoError(t, db.Where("username = ?", "withinitialquota").First(&registered).Error)
	assert.Equal(t, 600, registered.Quota)

	var initialRecords []model.InviteInitialQuotaRecord
	require.NoError(t, db.Find(&initialRecords).Error)
	require.Len(t, initialRecords, 1)
	assert.Equal(t, 101, initialRecords[0].InviterId)
	assert.Equal(t, registered.Id, initialRecords[0].InviteeId)
	assert.Equal(t, 102, initialRecords[0].InviteLinkBatchId)
	assert.Equal(t, 500, initialRecords[0].Quota)

	var affiliateRewardCount int64
	require.NoError(t, db.Model(&model.AffiliateRewardRecord{}).Count(&affiliateRewardCount).Error)
	assert.Equal(t, int64(0), affiliateRewardCount)
}

func registerUser(t *testing.T, payload gin.H) {
	t.Helper()
	body, err := common.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/register", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	Register(ctx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"success":true`)
}
