package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
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
	return &oauth.OAuthToken{AccessToken: "test-token"}, nil
}

func (oauthInitialQuotaProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return &oauth.OAuthUser{
		ProviderUserID: "callback-user",
		Username:       "callbackuser",
		DisplayName:    "Callback User",
		Email:          "callback@example.com",
	}, nil
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

	created, isNewUser, err := findOrCreateOAuthUser(ctx, provider, oauthUser, session)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.True(t, isNewUser)
	retried, isNewUser, err := findOrCreateOAuthUser(ctx, provider, oauthUser, session)
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.False(t, isNewUser)
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

func TestOAuthRegistrationConsumesReferralTokenFromSession(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	now := common.GetTimestamp()

	originalRegisterEnabled := common.RegisterEnabled
	common.RegisterEnabled = true
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
	})

	require.NoError(t, db.Create(&model.User{
		Id:       301,
		Username: "oauth-capture-inviter",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff301",
	}).Error)
	require.NoError(t, db.Create(&model.InviteLinkBatch{
		Id:                      302,
		Name:                    "OAuth capture",
		Code:                    "oauth-capture",
		BaseLink:                "/sign-up?invite_batch=oauth-capture",
		FirstTopupRewardPercent: 25,
		ContinuousRewardPercent: 6,
		StartTime:               now - 60,
		EndTime:                 now + 3600,
		IsActive:                true,
	}).Error)
	capture, _, err := model.CreateReferralCapture("oauth-capture", "aff301", model.ReferralCaptureSourceLink, now)
	require.NoError(t, err)
	require.NotNil(t, capture)

	ctx, _ := gin.CreateTestContext(nil)
	session := &mapSession{values: map[interface{}]interface{}{
		oauthReferralTokenHashSessionKey: capture.TokenHash,
	}}
	created, isNewUser, err := findOrCreateOAuthUser(ctx, oauthInitialQuotaProvider{}, &oauth.OAuthUser{
		ProviderUserID: "session-token-user",
		Username:       "sessiontokenuser",
		DisplayName:    "Session Token User",
		Email:          "sessiontoken@example.com",
	}, session)
	require.NoError(t, err)
	require.True(t, isNewUser)
	require.NotNil(t, created)

	var registered model.User
	require.NoError(t, db.Where("id = ?", created.Id).First(&registered).Error)
	assert.Equal(t, 301, registered.InviterId)
	assert.Equal(t, 302, registered.InviteLinkBatchId)
	assert.Equal(t, 25, registered.InviteFirstTopupRewardPercent)
	assert.Equal(t, 6, registered.InviteContinuousRewardPercent)

	var consumed model.ReferralCapture
	require.NoError(t, db.First(&consumed, capture.Id).Error)
	assert.Equal(t, registered.Id, consumed.ConsumedByUserId)
	assert.NotZero(t, consumed.ConsumedAt)
}

func TestOAuthExistingUserDoesNotBindReferralAndClearsCookie(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	now := common.GetTimestamp()
	originalRegisterEnabled := common.RegisterEnabled
	common.RegisterEnabled = true
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
		oauth.Unregister("referraltest")
	})

	require.NoError(t, db.Create(&model.User{
		Id:       401,
		Username: "existing-oauth",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "own401",
		OidcId:   "callback-user",
	}).Error)
	require.NoError(t, db.Create(&model.User{
		Id:       402,
		Username: "oauth-existing-inviter",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff402",
	}).Error)
	require.NoError(t, db.Create(&model.InviteLinkBatch{
		Id:        403,
		Name:      "OAuth existing",
		Code:      "oauth-existing",
		BaseLink:  "/sign-up?invite_batch=oauth-existing",
		StartTime: now - 60,
		EndTime:   now + 3600,
		IsActive:  true,
	}).Error)
	capture, token, err := model.CreateReferralCapture("oauth-existing", "aff402", model.ReferralCaptureSourceLink, now)
	require.NoError(t, err)
	require.NotNil(t, capture)

	oauth.Register("referraltest", oauthInitialQuotaProvider{})
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("referral-oauth-test"))))
	router.GET("/api/oauth/:provider", HandleOAuth)
	router.GET("/seed", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("oauth_state", "state-ok")
		session.Set(oauthReferralTokenHashSessionKey, capture.TokenHash)
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})

	clientCookies := make([]*http.Cookie, 0)
	seedRecorder := httptest.NewRecorder()
	seedRequest := httptest.NewRequest(http.MethodGet, "/seed", nil)
	router.ServeHTTP(seedRecorder, seedRequest)
	clientCookies = append(clientCookies, seedRecorder.Result().Cookies()...)
	clientCookies = append(clientCookies, &http.Cookie{Name: model.ReferralCookieName, Value: token})

	callbackRecorder := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, "/api/oauth/referraltest?state=state-ok&code=ok", nil)
	for _, cookie := range clientCookies {
		callbackRequest.AddCookie(cookie)
	}
	router.ServeHTTP(callbackRecorder, callbackRequest)

	require.Equal(t, http.StatusOK, callbackRecorder.Code)
	var existing model.User
	require.NoError(t, db.First(&existing, 401).Error)
	assert.Zero(t, existing.InviterId)
	assert.Zero(t, existing.InviteLinkBatchId)

	var notConsumed model.ReferralCapture
	require.NoError(t, db.First(&notConsumed, capture.Id).Error)
	assert.Zero(t, notConsumed.ConsumedAt)
	assert.Zero(t, notConsumed.ConsumedByUserId)

	cleared := false
	for _, cookie := range callbackRecorder.Result().Cookies() {
		if cookie.Name == model.ReferralCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared)
}
