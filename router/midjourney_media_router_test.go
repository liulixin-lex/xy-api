package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestMidjourneyMediaRoutesRejectAnonymousRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("midjourney-media-test"))))
	registerMjRouterGroup(router.Group("/mj"))

	for _, path := range []string{
		"/mj/image/task-id",
		"/mj/video/task-id",
		"/mj/video/task-id/0",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, path)
	}
}
