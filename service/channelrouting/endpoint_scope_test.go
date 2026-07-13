package channelrouting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEndpointHostAndRoutingRegionAreCanonical(t *testing.T) {
	t.Setenv("ROUTING_REGION", " US-EAST_1 ")
	assert.Equal(t, "us-east_1", RoutingRegion())
	assert.Equal(t, "api.example.test", EndpointHost("https://API.Example.Test.:8443/v1", 7))
	assert.Equal(t, "https://api.example.test:8443", EndpointAuthority("https://API.Example.Test.:8443/v1", 7))
	assert.Equal(t, "https://api.example.test:443", EndpointAuthority("https://api.example.test/other", 7))
	assert.Equal(t, "http://api.example.test:80", EndpointAuthority("http://api.example.test/v1", 7))
	assert.Equal(t, "2001:db8::1", EndpointHost("https://[2001:db8::1]/v1", 7))
	assert.Equal(t, "https://[2001:db8::1]:443", EndpointAuthority("https://[2001:db8::1]/v1", 7))
	assert.Equal(t, "channel-7", EndpointHost("not a url", 7))
	assert.Equal(t, "channel://channel-7", EndpointAuthority("not a url", 7))
	assert.Equal(t, "unknown", EndpointHost("", 0))

	t.Setenv("ROUTING_REGION", "region/unsafe")
	assert.Equal(t, defaultRoutingRegion, RoutingRegion())
	t.Setenv("ROUTING_REGION", strings.Repeat("a", 65))
	assert.Equal(t, defaultRoutingRegion, RoutingRegion())
}
