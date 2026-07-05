package controller

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedReferralUserAndBatch(t *testing.T, inviterId int, affCode string, batchId int, batchCode string, endOffset int64) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, model.DB.Create(&model.User{
		Id:       inviterId,
		Username: "inviter-" + affCode,
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  affCode,
	}).Error)
	require.NoError(t, model.DB.Create(&model.InviteLinkBatch{
		Id:                      batchId,
		Name:                    "Batch " + batchCode,
		Code:                    batchCode,
		BaseLink:                "/sign-up?invite_batch=" + batchCode,
		FirstTopupRewardPercent: 35,
		ContinuousRewardPercent: 7,
		StartTime:               now - 60,
		EndTime:                 now + endOffset,
		IsActive:                true,
	}).Error)
}

func captureReferral(t *testing.T, inviteBatch string, aff string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Params = gin.Params{{Key: "invite_batch", Value: inviteBatch}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/r/"+inviteBatch+"?aff="+aff, nil)
	for _, cookie := range cookies {
		ctx.Request.AddCookie(cookie)
	}

	CaptureReferralLink(ctx)

	require.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
	return w
}

func referralCookieFromRecorder(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == model.ReferralCookieName {
			return cookie
		}
	}
	t.Fatalf("expected %s cookie", model.ReferralCookieName)
	return nil
}

func TestCaptureReferralLinkCreatesCaptureCookieAndCapsExpiryAtBatchEnd(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 1, "aff1", 2, "short", 3600)

	w := captureReferral(t, "short", "aff1")

	cookie := referralCookieFromRecorder(t, w)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.InDelta(t, 3600, cookie.MaxAge, 3)

	var captures []model.ReferralCapture
	require.NoError(t, db.Find(&captures).Error)
	require.Len(t, captures, 1)
	assert.NotEmpty(t, captures[0].TokenHash)
	assert.NotEqual(t, cookie.Value, captures[0].TokenHash)
	assert.Equal(t, 1, captures[0].InviterId)
	assert.Equal(t, "aff1", captures[0].AffCode)
	assert.Equal(t, 2, captures[0].InviteLinkBatchId)
	assert.Equal(t, "short", captures[0].InviteBatchCode)
	assert.Equal(t, model.ReferralCaptureSourceLink, captures[0].Source)
	assert.InDelta(t, common.GetTimestamp()+3600, captures[0].ExpiresAt, 3)
}

func TestCaptureReferralLinkThroughGinRoute(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 1, "aff1", 2, "short", 3600)

	router := gin.New()
	router.GET("/r/:invite_batch", CaptureReferralLink)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/r/short?aff=aff1", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusFound, recorder.Code)
	assert.Equal(t, "/", recorder.Header().Get("Location"))
	cookie := referralCookieFromRecorder(t, recorder)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)

	var capture model.ReferralCapture
	require.NoError(t, db.Where("aff_code = ? AND invite_batch_code = ?", "aff1", "short").First(&capture).Error)
	assert.Equal(t, 1, capture.InviterId)
	assert.Equal(t, 2, capture.InviteLinkBatchId)
}

func TestCaptureReferralLinkRedirectsHomeWithoutCookieForInvalidLink(t *testing.T) {
	setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 1, "aff1", 2, "short", 3600)

	for _, tc := range []struct {
		name        string
		inviteBatch string
		aff         string
	}{
		{name: "missing batch", inviteBatch: "missing", aff: "aff1"},
		{name: "missing affiliate", inviteBatch: "short", aff: "missing-aff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := captureReferral(t, tc.inviteBatch, tc.aff)

			for _, cookie := range w.Result().Cookies() {
				if cookie.Name != model.ReferralCookieName {
					continue
				}
				assert.Empty(t, cookie.Value)
				assert.Equal(t, -1, cookie.MaxAge)
			}
		})
	}
}

func TestCaptureReferralLinkClearsExistingCookieForInvalidLink(t *testing.T) {
	setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 1, "aff1", 2, "short", 3600)

	w := captureReferral(t, "short", "missing-aff", &http.Cookie{Name: model.ReferralCookieName, Value: "old-token"})

	cookie := referralCookieFromRecorder(t, w)
	assert.Equal(t, "", cookie.Value)
	assert.Equal(t, -1, cookie.MaxAge)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
}

func TestCaptureReferralLinkLaterValidClickSupersedesPreviousCookie(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 1, "aff1", 2, "first", 86400)
	require.NoError(t, db.Model(&model.InviteLinkBatch{}).Where("id = ?", 2).Update("is_active", false).Error)
	seedReferralUserAndBatch(t, 3, "aff2", 4, "second", 86400)
	require.NoError(t, db.Model(&model.InviteLinkBatch{}).Where("id = ?", 2).Update("is_active", true).Error)

	first := captureReferral(t, "first", "aff1")
	oldCookie := referralCookieFromRecorder(t, first)
	second := captureReferral(t, "second", "aff2", oldCookie)
	newCookie := referralCookieFromRecorder(t, second)

	assert.NotEqual(t, oldCookie.Value, newCookie.Value)
	var oldCapture model.ReferralCapture
	require.NoError(t, db.Where("aff_code = ?", "aff1").First(&oldCapture).Error)
	assert.NotZero(t, oldCapture.SupersededAt)

	current, err := model.GetValidReferralCaptureByToken(newCookie.Value, common.GetTimestamp())
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, "aff2", current.AffCode)
}

func TestRegisterUsesReferralCookieBeforeRequestBodyAndFallsBackWithoutCookie(t *testing.T) {
	db := setupModelListControllerTestDB(t)
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

	seedReferralUserAndBatch(t, 10, "aff10", 20, "cookie-batch", 86400)
	require.NoError(t, db.Model(&model.InviteLinkBatch{}).Where("id = ?", 20).Update("is_active", false).Error)
	seedReferralUserAndBatch(t, 11, "aff11", 21, "body-batch", 86400)
	require.NoError(t, db.Model(&model.InviteLinkBatch{}).Where("id = ?", 20).Update("is_active", true).Error)
	capture, token, err := model.CreateReferralCapture("cookie-batch", "aff10", model.ReferralCaptureSourceLink, common.GetTimestamp())
	require.NoError(t, err)
	require.NotNil(t, capture)

	registerUserWithCookie(t, gin.H{
		"username":     "cookiewins",
		"password":     "password123",
		"aff_code":     "aff11",
		"invite_batch": "body-batch",
	}, &http.Cookie{Name: model.ReferralCookieName, Value: token})

	var cookieWins model.User
	require.NoError(t, db.Where("username = ?", "cookiewins").First(&cookieWins).Error)
	assert.Equal(t, 10, cookieWins.InviterId)
	assert.Equal(t, 20, cookieWins.InviteLinkBatchId)

	registerUserWithCookie(t, gin.H{
		"username":     "fallback",
		"password":     "password123",
		"aff_code":     "aff11",
		"invite_batch": "body-batch",
	})

	var fallback model.User
	require.NoError(t, db.Where("username = ?", "fallback").First(&fallback).Error)
	assert.Equal(t, 11, fallback.InviterId)
	assert.Equal(t, 21, fallback.InviteLinkBatchId)
}

func TestManualReferralUsesActiveBatchAndCannotOverrideLinkCapture(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 30, "aff30", 40, "active", 86400)
	require.NoError(t, db.Create(&model.User{
		Id:       31,
		Username: "inviter-aff31",
		Password: "secret",
		Status:   common.UserStatusEnabled,
		AffCode:  "aff31",
	}).Error)

	manualRecorder := httptest.NewRecorder()
	manualCtx, _ := gin.CreateTestContext(manualRecorder)
	body, err := common.Marshal(gin.H{"aff_code": "aff30"})
	require.NoError(t, err)
	manualCtx.Request = httptest.NewRequest(http.MethodPost, "/api/referral/manual", bytes.NewReader(body))
	manualCtx.Request.Header.Set("Content-Type", "application/json")

	CaptureManualReferral(manualCtx)

	require.Equal(t, http.StatusOK, manualRecorder.Code)
	var manualResponse struct {
		Success bool                         `json:"success"`
		Data    model.ReferralCaptureCurrent `json:"data"`
	}
	require.NoError(t, common.Unmarshal(manualRecorder.Body.Bytes(), &manualResponse))
	require.True(t, manualResponse.Success)
	assert.Equal(t, "aff30", manualResponse.Data.AffCode)
	assert.Equal(t, "active", manualResponse.Data.InviteBatchCode)
	assert.False(t, manualResponse.Data.Locked)

	linkCapture, linkToken, err := model.CreateReferralCapture("active", "aff31", model.ReferralCaptureSourceLink, common.GetTimestamp())
	require.NoError(t, err)
	require.NotNil(t, linkCapture)
	lockedRecorder := httptest.NewRecorder()
	lockedCtx, _ := gin.CreateTestContext(lockedRecorder)
	lockedCtx.Request = httptest.NewRequest(http.MethodPost, "/api/referral/manual", bytes.NewReader(body))
	lockedCtx.Request.Header.Set("Content-Type", "application/json")
	lockedCtx.Request.AddCookie(&http.Cookie{Name: model.ReferralCookieName, Value: linkToken})

	CaptureManualReferral(lockedCtx)

	require.Equal(t, http.StatusOK, lockedRecorder.Code)
	var lockedResponse struct {
		Success bool                         `json:"success"`
		Data    model.ReferralCaptureCurrent `json:"data"`
	}
	require.NoError(t, common.Unmarshal(lockedRecorder.Body.Bytes(), &lockedResponse))
	require.True(t, lockedResponse.Success)
	assert.Equal(t, "aff31", lockedResponse.Data.AffCode)
	assert.Equal(t, model.ReferralCaptureSourceLink, lockedResponse.Data.Source)

	var linkAfter model.ReferralCapture
	require.NoError(t, db.First(&linkAfter, linkCapture.Id).Error)
	assert.Zero(t, linkAfter.ClearedAt)
	assert.Zero(t, linkAfter.SupersededAt)
}

func TestExpiredAndConsumedReferralCapturesDoNotBindRegistration(t *testing.T) {
	db := setupModelListControllerTestDB(t)
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
	seedReferralUserAndBatch(t, 50, "aff50", 60, "expiring", 86400)

	expiredCapture, expiredToken, err := model.CreateReferralCapture("expiring", "aff50", model.ReferralCaptureSourceLink, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, db.Model(expiredCapture).Update("expires_at", common.GetTimestamp()-1).Error)
	registerUserWithCookie(t, gin.H{"username": "expireduser", "password": "password123"}, &http.Cookie{Name: model.ReferralCookieName, Value: expiredToken})
	var expiredUser model.User
	require.NoError(t, db.Where("username = ?", "expireduser").First(&expiredUser).Error)
	assert.Zero(t, expiredUser.InviterId)

	consumedCapture, consumedToken, err := model.CreateReferralCapture("expiring", "aff50", model.ReferralCaptureSourceLink, common.GetTimestamp())
	require.NoError(t, err)
	consumed, err := model.ConsumeReferralCaptureTx(db, consumedCapture.TokenHash, 999, common.GetTimestamp())
	require.NoError(t, err)
	require.True(t, consumed)
	registerUserWithCookie(t, gin.H{"username": "consumeduser", "password": "password123"}, &http.Cookie{Name: model.ReferralCookieName, Value: consumedToken})
	var consumedUser model.User
	require.NoError(t, db.Where("username = ?", "consumeduser").First(&consumedUser).Error)
	assert.Zero(t, consumedUser.InviterId)
}

func TestEmailDomainWhitelistUsesTranslatableMainstreamProviderMessage(t *testing.T) {
	setupModelListControllerTestDB(t)
	originalEnabled := common.EmailDomainRestrictionEnabled
	originalWhitelist := append([]string(nil), common.EmailDomainWhitelist...)
	common.EmailDomainRestrictionEnabled = true
	common.EmailDomainWhitelist = []string{"gmail.com"}
	t.Cleanup(func() {
		common.EmailDomainRestrictionEnabled = originalEnabled
		common.EmailDomainWhitelist = originalWhitelist
	})

	blocked := httptest.NewRecorder()
	blockedCtx, _ := gin.CreateTestContext(blocked)
	blockedCtx.Request = httptest.NewRequest(http.MethodGet, "/api/verification?email=user@example.com", nil)
	SendEmailVerification(blockedCtx)

	require.Equal(t, http.StatusOK, blocked.Code)
	assert.Contains(t, blocked.Body.String(), model.EmailDomainWhitelistMainstreamMessage)

	require.NoError(t, model.DB.Create(&model.User{Username: "taken", Password: "secret", Email: "used@gmail.com", AffCode: "used1"}).Error)
	allowed := httptest.NewRecorder()
	allowedCtx, _ := gin.CreateTestContext(allowed)
	allowedCtx.Request = httptest.NewRequest(http.MethodGet, "/api/verification?email=used@gmail.com", nil)
	SendEmailVerification(allowedCtx)

	require.Equal(t, http.StatusOK, allowed.Code)
	assert.NotContains(t, allowed.Body.String(), model.EmailDomainWhitelistMainstreamMessage)
}

func registerUserWithCookie(t *testing.T, payload gin.H, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/register", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		ctx.Request.AddCookie(cookie)
	}

	Register(ctx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"success":true`)
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == model.ReferralCookieName && cookie.MaxAge < 0 {
			return w
		}
	}
	if len(cookies) > 0 {
		t.Fatalf("expected %s cleanup cookie", model.ReferralCookieName)
	}
	return w
}

func TestReferralCookieIsSecureForHTTPSRequests(t *testing.T) {
	setupModelListControllerTestDB(t)
	seedReferralUserAndBatch(t, 70, "aff70", 80, "secure", 86400)

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Params = gin.Params{{Key: "invite_batch", Value: "secure"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/r/secure?aff=aff70", nil)
	ctx.Request.Header.Set("X-Forwarded-Proto", "https")
	ctx.Request.TLS = &tlsConnectionState

	CaptureReferralLink(ctx)

	cookie := referralCookieFromRecorder(t, w)
	assert.True(t, cookie.Secure)
}

func TestPasswordLoginClearsReferralCookie(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	originalPasswordLoginEnabled := common.PasswordLoginEnabled
	common.PasswordLoginEnabled = true
	t.Cleanup(func() {
		common.PasswordLoginEnabled = originalPasswordLoginEnabled
	})

	hashedPassword, err := common.Password2Hash("password123")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{
		Id:       90,
		Username: "login-clear-referral",
		Password: hashedPassword,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		AffCode:  "own90",
	}).Error)

	payload, err := common.Marshal(gin.H{
		"username": "login-clear-referral",
		"password": "password123",
	})
	require.NoError(t, err)

	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("login-clear-referral"))))
	router.POST("/api/user/login", Login)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: model.ReferralCookieName, Value: "stale-token"})
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == model.ReferralCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared)
}

var tlsConnectionState = tlsState()

func tlsState() tls.ConnectionState {
	return tls.ConnectionState{HandshakeComplete: true}
}
