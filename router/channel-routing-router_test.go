package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/stretchr/testify/assert"
)

func TestChannelRoutingV2RoutesAreReadOnlyAndPermissionProtected(t *testing.T) {
	assert.Len(t, channelRoutingReadRoutes, 7)
	paths := make(map[string]struct{}, len(channelRoutingReadRoutes))
	for _, route := range channelRoutingReadRoutes {
		assert.Equal(t, http.MethodGet, route.method)
		assert.Equal(t, authz.ChannelRead, route.permission)
		_, duplicate := paths[route.path]
		assert.False(t, duplicate)
		paths[route.path] = struct{}{}
	}
}
