package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func performCacheMiddlewareRequest(target string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Cache())
	router.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCacheMiddlewareDoesNotCacheHtmlOrAPIRoutes(t *testing.T) {
	tests := []string{
		"/",
		"/?theme=default",
		"/pricing",
		"/console/channel",
		"/api/pricing",
		"/v1/models",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			recorder := performCacheMiddlewareRequest(target)

			assert.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
		})
	}
}

func TestCacheMiddlewareCachesStaticAssets(t *testing.T) {
	tests := []string{
		"/assets/index-a1b2c3.js",
		"/assets/style-a1b2c3.css",
		"/favicon.ico",
		"/logo.png",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			recorder := performCacheMiddlewareRequest(target)

			assert.Equal(t, "max-age=604800", recorder.Header().Get("Cache-Control"))
		})
	}
}
