package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func isCacheableStaticAsset(path string) bool {
	if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/v1") {
		return false
	}
	if strings.HasPrefix(path, "/assets/") {
		return true
	}

	lowerPath := strings.ToLower(path)
	for _, ext := range []string{
		".css",
		".gif",
		".ico",
		".jpeg",
		".jpg",
		".js",
		".json",
		".map",
		".otf",
		".png",
		".svg",
		".ttf",
		".webmanifest",
		".webp",
		".woff",
		".woff2",
	} {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	return false
}

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		if isCacheableStaticAsset(c.Request.URL.Path) {
			c.Header("Cache-Control", "max-age=604800") // one week
		} else {
			c.Header("Cache-Control", "no-cache")
		}
		c.Header("Cache-Version", "b688f2fb5be447c25e5aa3bd063087a83db32a288bf6a4f35f2d8db310e40b14")
		c.Next()
	}
}
