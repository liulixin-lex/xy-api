package router

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

const trustedProxyCIDRsEnv = "TRUSTED_PROXY_CIDRS"

// ConfigureTrustedProxies keeps forwarded client-IP headers disabled unless
// the deployment explicitly identifies the reverse-proxy networks that may
// supply them. Invalid or globally trusted CIDRs fail closed during startup.
func ConfigureTrustedProxies(engine *gin.Engine) error {
	if engine == nil {
		return errors.New("Gin engine is required")
	}

	engine.ForwardedByClientIP = false
	engine.RemoteIPHeaders = nil
	if err := engine.SetTrustedProxies(nil); err != nil {
		return fmt.Errorf("disable trusted proxies: %w", err)
	}

	rawCIDRs := strings.TrimSpace(os.Getenv(trustedProxyCIDRsEnv))
	if rawCIDRs == "" {
		return nil
	}

	trustedProxies := make([]string, 0)
	seen := make(map[string]struct{})
	for _, rawCIDR := range strings.Split(rawCIDRs, ",") {
		rawCIDR = strings.TrimSpace(rawCIDR)
		if rawCIDR == "" {
			return fmt.Errorf("%s contains an empty CIDR", trustedProxyCIDRsEnv)
		}
		_, network, err := net.ParseCIDR(rawCIDR)
		if err != nil {
			return fmt.Errorf("%s contains invalid CIDR %q: %w", trustedProxyCIDRsEnv, rawCIDR, err)
		}
		prefixLength, _ := network.Mask.Size()
		if prefixLength == 0 {
			return fmt.Errorf("%s must not trust every address with %q", trustedProxyCIDRsEnv, rawCIDR)
		}
		canonicalCIDR := network.String()
		if _, exists := seen[canonicalCIDR]; exists {
			continue
		}
		seen[canonicalCIDR] = struct{}{}
		trustedProxies = append(trustedProxies, canonicalCIDR)
	}

	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("configure %s: %w", trustedProxyCIDRsEnv, err)
	}
	engine.ForwardedByClientIP = true
	engine.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	return nil
}
