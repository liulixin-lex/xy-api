package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/stretchr/testify/assert"
)

func TestChannelRoutingV2RoutesAreReadOnlyAndPermissionProtected(t *testing.T) {
	expected := map[string]string{
		"/overview":               http.MethodGet,
		"/nodes":                  http.MethodGet,
		"/groups":                 http.MethodGet,
		"/groups/:id":             http.MethodGet,
		"/channels":               http.MethodGet,
		"/costs":                  http.MethodGet,
		"/decisions":              http.MethodGet,
		"/decisions/:id":          http.MethodGet,
		"/decisions/:id/replay":   http.MethodPost,
		"/groups/:id/simulations": http.MethodPost,
	}
	assert.Len(t, channelRoutingReadRoutes, len(expected))
	paths := make(map[string]struct{}, len(channelRoutingReadRoutes))
	for _, route := range channelRoutingReadRoutes {
		assert.Equal(t, expected[route.path], route.method)
		assert.Equal(t, authz.ChannelRead, route.permission)
		_, duplicate := paths[route.path]
		assert.False(t, duplicate)
		paths[route.path] = struct{}{}
	}
}
