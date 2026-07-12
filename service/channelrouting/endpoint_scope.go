package channelrouting

import (
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const defaultRoutingRegion = "default"

// RoutingRegion is the stable failure domain for outbound network health.
func RoutingRegion() string {
	return normalizeRoutingRegion(os.Getenv("ROUTING_REGION"))
}

func EndpointHost(endpoint string, channelID int) string {
	host, _ := endpointScopeIdentity(endpoint, channelID)
	return host
}

func EndpointAuthority(endpoint string, channelID int) string {
	_, authority := endpointScopeIdentity(endpoint, channelID)
	return authority
}

func endpointScopeIdentity(endpoint string, channelID int) (string, string) {
	endpoint = strings.TrimSpace(endpoint)
	if parsed, err := url.Parse(endpoint); err == nil {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
		if host != "" {
			scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
			port := parsed.Port()
			if port == "" {
				switch scheme {
				case "http":
					port = "80"
				case "https":
					port = "443"
				}
			}
			authority := host
			if port != "" {
				authority = net.JoinHostPort(host, port)
			}
			if scheme != "" {
				authority = scheme + "://" + authority
			}
			return truncateActiveProbeText(host, 255), truncateActiveProbeText(authority, 320)
		}
	}
	if channelID > 0 {
		fallback := "channel-" + strconv.Itoa(channelID)
		return fallback, "channel://" + fallback
	}
	return "unknown", "channel://unknown"
}

func normalizeRoutingRegion(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" || len(region) > 64 {
		return defaultRoutingRegion
	}
	for _, char := range region {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '-' || char == '_' || char == '.' {
			continue
		}
		return defaultRoutingRegion
	}
	return region
}
