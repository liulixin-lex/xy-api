package middleware

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const RouteTagKey = "route_tag"

func RouteTag(tag string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(RouteTagKey, tag)
		c.Next()
	}
}

func SetUpLogger(server *gin.Engine) {
	server.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		var requestID string
		if param.Keys != nil {
			requestID, _ = param.Keys[common.RequestIdKey].(string)
		}
		tag, _ := param.Keys[RouteTagKey].(string)
		if tag == "" {
			tag = "web"
		}
		return fmt.Sprintf("[GIN] %s | %s | %s | %3d | %13v | %15s | %7s %s\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			tag,
			requestID,
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			sanitizeAccessLogPath(param.Path),
		)
	}))
}

func sanitizeAccessLogPath(path string) string {
	cleanPath := path
	if queryIndex := strings.IndexByte(cleanPath, '?'); queryIndex >= 0 {
		cleanPath = cleanPath[:queryIndex]
	}
	if paymentCallbackAccessLogPath(cleanPath) {
		return cleanPath
	}
	return path
}

func paymentCallbackAccessLogPath(path string) bool {
	switch path {
	case "/api/payment/wechat/authorize/callback",
		"/api/payment/epay/notify",
		"/api/user/epay/notify",
		"/api/subscription/epay/notify",
		"/api/subscription/epay/return",
		"/api/xorpay/notify",
		"/api/stripe/webhook",
		"/api/creem/webhook",
		"/api/waffo/webhook":
		return true
	default:
		return strings.HasPrefix(path, "/api/waffo-pancake/webhook/")
	}
}
