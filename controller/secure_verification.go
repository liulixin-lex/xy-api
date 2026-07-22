package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	// SecureVerificationSessionKey means the user has fully passed secure verification.
	SecureVerificationSessionKey       = "secure_verified_at"
	secureVerificationMethodSessionKey = "secure_verified_method"
	secureVerificationUserIDSessionKey = "secure_verified_user_id"
	secureVerificationMethod2FA        = "2fa"
	secureVerificationMethodPasskey    = "passkey"
	// PasskeyReadySessionKey means WebAuthn finished and /api/verify can finalize step-up verification.
	PasskeyReadySessionKey       = "secure_passkey_ready_at"
	passkeyReadyUserIDSessionKey = "secure_passkey_ready_user_id"
	// SecureVerificationTimeout 验证有效期（秒）
	SecureVerificationTimeout = 300 // 5分钟
	// PasskeyReadyTimeout passkey ready 标记有效期（秒）
	PasskeyReadyTimeout                = 60
	secureVerificationRequestBodyLimit = 4 << 10
	secureVerificationCodeMaxLength    = 128
)

var errPasskeyReadyStateInvalid = errors.New("invalid Passkey verification state")

type UniversalVerifyRequest struct {
	Method string `json:"method"` // "2fa" 或 "passkey"
	Code   string `json:"code,omitempty"`
}

type VerificationStatusResponse struct {
	Verified  bool  `json:"verified"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

func secureVerificationAPIError(c *gin.Context, status int, code, message string, diagnostic error) {
	if diagnostic != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"secure verification rejected user_id=%d code=%s error=%q",
			c.GetInt("id"), code, diagnostic.Error(),
		))
	}
	c.JSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": message,
	})
}

// UniversalVerify 通用验证接口
// 支持 2FA 和 Passkey 验证，验证成功后在 session 中记录时间戳
func UniversalVerify(c *gin.Context) {
	userId := c.GetInt("id")
	if userId == 0 {
		secureVerificationAPIError(c, http.StatusUnauthorized, "secure_verification_auth_required", "请先登录", nil)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, secureVerificationRequestBodyLimit)
	var req UniversalVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		secureVerificationAPIError(c, http.StatusBadRequest, "secure_verification_request_invalid", "安全验证请求无效", err)
		return
	}
	req.Method = strings.ToLower(strings.TrimSpace(req.Method))
	req.Code = strings.TrimSpace(req.Code)
	if len(req.Method) > 16 || len(req.Code) > secureVerificationCodeMaxLength {
		secureVerificationAPIError(c, http.StatusBadRequest, "secure_verification_request_invalid", "安全验证请求无效", nil)
		return
	}

	// 获取用户信息
	user := &model.User{Id: userId}
	if err := user.FillUserById(); err != nil {
		secureVerificationAPIError(c, http.StatusServiceUnavailable, "secure_verification_user_unavailable", "暂时无法完成安全验证", err)
		return
	}

	if user.Status != common.UserStatusEnabled {
		secureVerificationAPIError(c, http.StatusForbidden, "secure_verification_account_disabled", "当前账号无法完成安全验证", nil)
		return
	}

	// 检查用户的验证方式
	twoFA, twoFAErr := model.GetTwoFAByUserId(userId)
	if twoFAErr != nil {
		secureVerificationAPIError(c, http.StatusServiceUnavailable, "secure_verification_method_unavailable", "暂时无法读取安全验证方式", twoFAErr)
		return
	}
	has2FA := twoFA != nil && twoFA.IsEnabled

	passkey, passkeyErr := model.GetPasskeyByUserID(userId)
	if passkeyErr != nil && !errors.Is(passkeyErr, model.ErrPasskeyNotFound) {
		secureVerificationAPIError(c, http.StatusServiceUnavailable, "secure_verification_method_unavailable", "暂时无法读取安全验证方式", passkeyErr)
		return
	}
	hasPasskey := passkeyErr == nil && passkey != nil

	if !has2FA && !hasPasskey {
		secureVerificationAPIError(c, http.StatusConflict, "secure_verification_not_configured", "请先启用 2FA 或 Passkey", nil)
		return
	}

	// 根据验证方式进行验证
	var verified bool
	var verifyMethod string
	var err error

	switch req.Method {
	case "2fa":
		if !has2FA {
			secureVerificationAPIError(c, http.StatusConflict, "secure_verification_method_unavailable", "当前账号未启用 2FA", nil)
			return
		}
		if req.Code == "" {
			secureVerificationAPIError(c, http.StatusBadRequest, "secure_verification_code_required", "请输入验证码", nil)
			return
		}
		verified = validateTwoFactorAuth(twoFA, req.Code)
		verifyMethod = "2FA"

	case "passkey":
		if !hasPasskey {
			secureVerificationAPIError(c, http.StatusConflict, "secure_verification_method_unavailable", "当前账号未启用 Passkey", nil)
			return
		}
		// Passkey branch only trusts the short-lived marker written by PasskeyVerifyFinish.
		verified, err = consumePasskeyReady(c)
		if err != nil {
			if errors.Is(err, errPasskeyReadyStateInvalid) {
				secureVerificationAPIError(c, http.StatusForbidden, "secure_verification_passkey_state_invalid", "Passkey 验证状态无效，请重新验证", err)
			} else {
				secureVerificationAPIError(c, http.StatusServiceUnavailable, "secure_verification_session_unavailable", "暂时无法保存安全验证状态", err)
			}
			return
		}
		if !verified {
			secureVerificationAPIError(c, http.StatusForbidden, "secure_verification_passkey_required", "请先完成 Passkey 验证", nil)
			return
		}
		verifyMethod = "Passkey"

	default:
		secureVerificationAPIError(c, http.StatusBadRequest, "secure_verification_method_invalid", "不支持此安全验证方式", nil)
		return
	}

	if !verified {
		secureVerificationAPIError(c, http.StatusForbidden, "secure_verification_failed", "验证失败，请检查验证码", nil)
		return
	}

	// 验证成功，在 session 中记录时间戳
	now, err := setSecureVerificationSession(c, req.Method)
	if err != nil {
		secureVerificationAPIError(c, http.StatusServiceUnavailable, "secure_verification_session_unavailable", "暂时无法保存安全验证状态", err)
		return
	}

	// 记录日志
	model.RecordLog(userId, model.LogTypeSystem, fmt.Sprintf("通用安全验证成功 (验证方式: %s)", verifyMethod))

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "验证成功",
		"data": gin.H{
			"verified":   true,
			"expires_at": now + SecureVerificationTimeout,
		},
	})
}

func setSecureVerificationSession(c *gin.Context, method string) (int64, error) {
	session := sessions.Default(c)
	session.Delete(PasskeyReadySessionKey)
	session.Delete(passkeyReadyUserIDSessionKey)
	now := time.Now().Unix()
	session.Set(SecureVerificationSessionKey, now)
	session.Set(secureVerificationMethodSessionKey, method)
	session.Set(secureVerificationUserIDSessionKey, c.GetInt("id"))
	if err := session.Save(); err != nil {
		return 0, err
	}
	return now, nil
}

func consumePasskeyReady(c *gin.Context) (bool, error) {
	session := sessions.Default(c)
	readyAtRaw := session.Get(PasskeyReadySessionKey)
	if readyAtRaw == nil {
		return false, nil
	}

	readyAt, ok := common.SessionValueInt64(readyAtRaw)
	if !ok || readyAt <= 0 {
		session.Delete(PasskeyReadySessionKey)
		session.Delete(passkeyReadyUserIDSessionKey)
		if err := session.Save(); err != nil {
			return false, err
		}
		return false, errPasskeyReadyStateInvalid
	}
	readyUserID, ok := common.SessionValueInt(session.Get(passkeyReadyUserIDSessionKey))
	if !ok || readyUserID != c.GetInt("id") {
		session.Delete(PasskeyReadySessionKey)
		session.Delete(passkeyReadyUserIDSessionKey)
		if err := session.Save(); err != nil {
			return false, err
		}
		return false, errPasskeyReadyStateInvalid
	}
	session.Delete(PasskeyReadySessionKey)
	session.Delete(passkeyReadyUserIDSessionKey)
	if err := session.Save(); err != nil {
		return false, err
	}
	// Expired ready markers cannot be reused.
	elapsed := time.Now().Unix() - readyAt
	if elapsed < 0 {
		return false, errPasskeyReadyStateInvalid
	}
	if elapsed >= PasskeyReadyTimeout {
		return false, nil
	}
	return true, nil
}
