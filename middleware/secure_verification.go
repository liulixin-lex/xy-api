package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	// SecureVerificationSessionKey 安全验证的 session key（与 controller 保持一致）
	SecureVerificationSessionKey       = "secure_verified_at"
	secureVerificationMethodSessionKey = "secure_verified_method"
	secureVerificationUserIDSessionKey = "secure_verified_user_id"
	// SecureVerificationTimeout 验证有效期（秒）
	SecureVerificationTimeout = 300 // 5分钟
)

const (
	secureVerificationRequiredCode           = "VERIFICATION_REQUIRED"
	secureVerificationInvalidCode            = "VERIFICATION_INVALID"
	secureVerificationExpiredCode            = "VERIFICATION_EXPIRED"
	secureVerificationSessionUnavailableCode = "VERIFICATION_SESSION_UNAVAILABLE"
)

// SecureVerificationRequired 安全验证中间件
// 检查用户是否在有效时间内通过了安全验证
// 如果未验证或验证已过期，返回稳定的 403 业务错误。
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := c.GetInt("id")
		if userId == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "未登录",
				"code":    "AUTH_SESSION_REQUIRED",
			})
			c.Abort()
			return
		}

		session := sessions.Default(c)
		_, code, err := readSecureVerificationSession(session, userId, time.Now().Unix())
		if err != nil {
			common.SysLog("failed to clear invalid secure verification session: " + err.Error())
			writeSecureVerificationError(c, http.StatusServiceUnavailable, secureVerificationSessionUnavailableCode)
			c.Abort()
			return
		}
		if code != "" {
			writeSecureVerificationError(c, http.StatusForbidden, code)
			c.Abort()
			return
		}

		c.Next()
	}
}

func clearSecureVerificationSession(session sessions.Session) error {
	session.Delete(SecureVerificationSessionKey)
	session.Delete(secureVerificationMethodSessionKey)
	session.Delete(secureVerificationUserIDSessionKey)
	return session.Save()
}

func readSecureVerificationSession(session sessions.Session, userID int, now int64) (int64, string, error) {
	verifiedAtRaw := session.Get(SecureVerificationSessionKey)
	if verifiedAtRaw == nil {
		return 0, secureVerificationRequiredCode, nil
	}
	verifiedAt, timestampValid := common.SessionValueInt64(verifiedAtRaw)
	verifiedUserID, userValid := common.SessionValueInt(session.Get(secureVerificationUserIDSessionKey))
	verifiedMethod, methodValid := session.Get(secureVerificationMethodSessionKey).(string)
	if !timestampValid || verifiedAt <= 0 || !userValid || verifiedUserID <= 0 || verifiedUserID != userID ||
		!methodValid || strings.TrimSpace(verifiedMethod) == "" {
		return 0, secureVerificationInvalidCode, clearSecureVerificationSession(session)
	}
	elapsed := now - verifiedAt
	if elapsed < 0 {
		return 0, secureVerificationInvalidCode, clearSecureVerificationSession(session)
	}
	if elapsed >= SecureVerificationTimeout {
		return 0, secureVerificationExpiredCode, clearSecureVerificationSession(session)
	}
	return verifiedAt, "", nil
}

func writeSecureVerificationError(c *gin.Context, status int, code string) {
	message := "验证状态异常，请重新验证"
	switch code {
	case secureVerificationRequiredCode:
		message = "需要安全验证"
	case secureVerificationExpiredCode:
		message = "验证已过期，请重新验证"
	case secureVerificationSessionUnavailableCode:
		message = "暂时无法保存验证状态，请稍后重试"
	}
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
}

// OptionalSecureVerification 可选的安全验证中间件
// 如果用户已验证，则在 context 中设置标记，但不阻止请求继续
// 用于某些需要区分是否已验证的场景
func OptionalSecureVerification() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := c.GetInt("id")
		if userId == 0 {
			c.Set("secure_verified", false)
			c.Next()
			return
		}

		session := sessions.Default(c)
		verifiedAt, code, err := readSecureVerificationSession(session, userId, time.Now().Unix())
		if err != nil {
			common.SysLog("failed to clear invalid optional secure verification session: " + err.Error())
		}
		if code != "" || err != nil {
			c.Set("secure_verified", false)
			c.Next()
			return
		}

		c.Set("secure_verified", true)
		c.Set("secure_verified_at", verifiedAt)
		c.Next()
	}
}

// ClearSecureVerification 清除安全验证状态
// 用于用户登出或需要强制重新验证的场景
func ClearSecureVerification(c *gin.Context) {
	session := sessions.Default(c)
	if err := clearSecureVerificationSession(session); err != nil {
		common.SysLog("failed to clear secure verification session: " + err.Error())
	}
}
