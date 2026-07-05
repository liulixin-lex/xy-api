package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const oauthReferralTokenHashSessionKey = "referral_token_hash"

type referralCaptureRequest struct {
	AffCode     string `json:"aff_code"`
	Aff         string `json:"aff"`
	InviteBatch string `json:"invite_batch"`
}

func CaptureReferralLink(c *gin.Context) {
	now := common.GetTimestamp()
	capture, token, err := model.CreateReferralCapture(
		c.Param("invite_batch"),
		c.Query("aff"),
		model.ReferralCaptureSourceLink,
		now,
	)
	if err != nil {
		common.SysError("failed to capture referral link: " + err.Error())
		clearReferralCookie(c)
		c.Redirect(http.StatusFound, "/")
		return
	}
	if capture == nil {
		clearReferralCookie(c)
		c.Redirect(http.StatusFound, "/")
		return
	}
	if oldToken := referralTokenFromCookie(c); oldToken != "" {
		_ = model.SupersedeReferralCaptureByToken(oldToken, now)
	}
	setReferralCookie(c, token, int(capture.ExpiresAt-now))
	c.Redirect(http.StatusFound, "/")
}

func CaptureLegacyReferral(c *gin.Context) {
	var req referralCaptureRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	affCode := strings.TrimSpace(req.AffCode)
	if affCode == "" {
		affCode = strings.TrimSpace(req.Aff)
	}
	now := common.GetTimestamp()
	capture, token, err := model.CreateReferralCapture(
		req.InviteBatch,
		affCode,
		model.ReferralCaptureSourceLegacy,
		now,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if capture == nil {
		clearReferralCookie(c)
		common.ApiSuccess(c, model.ReferralCaptureCurrent{})
		return
	}
	if oldToken := referralTokenFromCookie(c); oldToken != "" {
		_ = model.SupersedeReferralCaptureByToken(oldToken, now)
	}
	setReferralCookie(c, token, int(capture.ExpiresAt-now))
	common.ApiSuccess(c, capture.Current())
}

func CaptureManualReferral(c *gin.Context) {
	var req referralCaptureRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	now := common.GetTimestamp()
	current, err := currentReferralCaptureFromRequest(c, now)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if current != nil && current.Current().Locked {
		common.ApiSuccess(c, current.Current())
		return
	}

	affCode := strings.TrimSpace(req.AffCode)
	if affCode == "" {
		affCode = strings.TrimSpace(req.Aff)
	}
	capture, token, err := model.CreateManualReferralCapture(affCode, now)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if capture == nil {
		clearReferralCookie(c)
		common.ApiErrorMsg(c, "Invalid referral code")
		return
	}
	if current != nil {
		_ = model.ClearReferralCaptureByTokenHash(current.TokenHash, now)
	}
	setReferralCookie(c, token, int(capture.ExpiresAt-now))
	common.ApiSuccess(c, capture.Current())
}

func GetCurrentReferral(c *gin.Context) {
	now := common.GetTimestamp()
	capture, err := currentReferralCaptureFromRequest(c, now)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if capture == nil {
		clearReferralCookie(c)
		common.ApiSuccess(c, model.ReferralCaptureCurrent{})
		return
	}
	common.ApiSuccess(c, capture.Current())
}

func ClearCurrentReferral(c *gin.Context) {
	now := common.GetTimestamp()
	capture, err := currentReferralCaptureFromRequest(c, now)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if capture == nil {
		clearReferralCookie(c)
		common.ApiSuccess(c, model.ReferralCaptureCurrent{})
		return
	}
	if capture.Current().Locked {
		common.ApiErrorMsg(c, "Referral from invite link cannot be cleared")
		return
	}
	if err := model.ClearReferralCaptureByTokenHash(capture.TokenHash, now); err != nil {
		common.ApiError(c, err)
		return
	}
	clearReferralCookie(c)
	common.ApiSuccess(c, model.ReferralCaptureCurrent{})
}

func currentReferralCaptureFromRequest(c *gin.Context, now int64) (*model.ReferralCapture, error) {
	token := referralTokenFromCookie(c)
	if token == "" {
		return nil, nil
	}
	return model.GetValidReferralCaptureByToken(token, now)
}

func referralTokenFromCookie(c *gin.Context) string {
	cookie, err := c.Cookie(model.ReferralCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie)
}

func setReferralCookie(c *gin.Context, token string, maxAge int) {
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     model.ReferralCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  time.Now().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearReferralCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     model.ReferralCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func isSecureRequest(c *gin.Context) bool {
	if c.Request != nil && c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

func syncReferralToOAuthSession(c *gin.Context, session sessions.Session, now int64) error {
	session.Delete("aff")
	session.Delete("invite_batch")
	session.Delete("aff_rule")
	session.Delete(oauthReferralTokenHashSessionKey)

	affCode := strings.TrimSpace(c.Query("aff"))
	inviteBatch := strings.TrimSpace(c.Query("invite_batch"))
	if affCode != "" && inviteBatch != "" {
		capture, token, err := model.CreateReferralCapture(inviteBatch, affCode, model.ReferralCaptureSourceLegacy, now)
		if err != nil {
			return err
		}
		if capture != nil {
			if oldToken := referralTokenFromCookie(c); oldToken != "" {
				_ = model.SupersedeReferralCaptureByToken(oldToken, now)
			}
			setReferralCookie(c, token, int(capture.ExpiresAt-now))
			session.Set(oauthReferralTokenHashSessionKey, capture.TokenHash)
		}
		return nil
	}

	capture, err := currentReferralCaptureFromRequest(c, now)
	if err != nil {
		return err
	}
	if capture == nil {
		clearReferralCookie(c)
		return nil
	}
	session.Set(oauthReferralTokenHashSessionKey, capture.TokenHash)
	return nil
}

func clearOAuthReferralState(c *gin.Context, session sessions.Session) {
	session.Delete("aff")
	session.Delete("invite_batch")
	session.Delete("aff_rule")
	session.Delete(oauthReferralTokenHashSessionKey)
	clearReferralCookie(c)
}
