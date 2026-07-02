package controller

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type oauthInitialQuotaProvider struct{}

func (oauthInitialQuotaProvider) GetName() string {
	return "InitialQuotaOAuth"
}

func (oauthInitialQuotaProvider) IsEnabled() bool {
	return true
}

func (oauthInitialQuotaProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	return nil, nil
}

func (oauthInitialQuotaProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return nil, nil
}

func (oauthInitialQuotaProvider) IsUserIDTaken(providerUserID string) bool {
	var count int64
	model.DB.Model(&model.User{}).Where("oidc_id = ?", providerUserID).Count(&count)
	return count > 0
}

func (oauthInitialQuotaProvider) FillUserByProviderID(user *model.User, providerUserID string) error {
	err := model.DB.Where("oidc_id = ?", providerUserID).First(user).Error
	if err != nil && err == gorm.ErrRecordNotFound {
		return nil
	}
	return err
}

func (oauthInitialQuotaProvider) SetProviderUserID(user *model.User, providerUserID string) {
	user.OidcId = providerUserID
}

func (oauthInitialQuotaProvider) GetProviderPrefix() string {
	return "oauth_"
}

type mapSession struct {
	values map[interface{}]interface{}
}

func (s *mapSession) ID() string {
	return "test-session"
}

func (s *mapSession) Get(key interface{}) interface{} {
	return s.values[key]
}

func (s *mapSession) Set(key interface{}, val interface{}) {
	s.values[key] = val
}

func (s *mapSession) Delete(key interface{}) {
	delete(s.values, key)
}

func (s *mapSession) Clear() {
	s.values = map[interface{}]interface{}{}
}

func (s *mapSession) AddFlash(interface{}, ...string) {}

func (s *mapSession) Flashes(...string) []interface{} {
	return nil
}

func (s *mapSession) Options(sessions.Options) {}

func (s *mapSession) Save() error {
	return nil
}

func TestOAuthRegistrationAppliesInviteInitialQuotaOnce(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	now := common.GetTimestamp()

	originalRegisterEnabled := common.RegisterEnabled
	originalQuotaForNewUser := common.QuotaForNewUser
	common.RegisterEnabled = true
	common.QuotaForNewUser = 100
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
		common.QuotaForNewUser = originalQuotaForNewUser
	})

	require.NoError(t, db.Create(&model.User{
		Id:       201,
		Username: "oauth-inviter",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff201",
	}).Error)
	require.NoError(t, db.Create(&model.InviteLinkBatch{
		Id:       202,
		Name:     "OAuth initial quota",
		Code:     "oauth-initial-quota",
		BaseLink: "https://example.com/sign-up?invite_batch=oauth-initial-quota",
		ActivityRules: model.InviteRewardActivities{
			{ActivityDetail: "OAuth signup quota", Type: model.InviteRewardRuleInitialQuota, Quota: 700},
			{ActivityDetail: "OAuth first top-up", Type: model.InviteRewardRuleFirstTopUp, Percent: 30},
		},
		StartTime: now - 60,
		EndTime:   now + 3600,
		IsActive:  true,
	}).Error)

	ctx, _ := gin.CreateTestContext(nil)
	session := &mapSession{values: map[interface{}]interface{}{
		"aff":          "aff201",
		"invite_batch": "oauth-initial-quota",
	}}
	provider := oauthInitialQuotaProvider{}
	oauthUser := &oauth.OAuthUser{
		ProviderUserID: "initial-quota-user",
		Username:       "oauthinitial",
		DisplayName:    "OAuth Initial",
		Email:          "oauthinitial@example.com",
	}

	created, err := findOrCreateOAuthUser(ctx, provider, oauthUser, session)
	require.NoError(t, err)
	require.NotNil(t, created)
	retried, err := findOrCreateOAuthUser(ctx, provider, oauthUser, session)
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, created.Id, retried.Id)

	var registered model.User
	require.NoError(t, db.Where("id = ?", created.Id).First(&registered).Error)
	assert.Equal(t, 800, registered.Quota)

	var records []model.InviteInitialQuotaRecord
	require.NoError(t, db.Find(&records).Error)
	require.Len(t, records, 1)
	assert.Equal(t, 201, records[0].InviterId)
	assert.Equal(t, registered.Id, records[0].InviteeId)
	assert.Equal(t, 202, records[0].InviteLinkBatchId)
	assert.Equal(t, 700, records[0].Quota)
}
