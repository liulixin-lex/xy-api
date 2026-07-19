package common

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSRFProtectionRejectsLiteralPrivateAndReservedIPs(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:   false,
		DomainFilterMode: false,
		IpFilterMode:     false,
	}

	tests := []string{
		"0.0.0.0",
		"0.1.2.3",
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"169.254.169.254",
		"192.88.99.2",
		"198.18.0.1",
		"::",
		"::1",
		"::127.0.0.1",
		"fe80::1",
		"fc00::1",
		"::ffff:0.0.0.0",
		"::ffff:127.0.0.1",
		"64:ff9b::7f00:1",
		"64:ff9b:1::1",
		"100:0:0:1::1",
		"2002:7f00:1::",
		"3fff::1",
		"5f00::1",
	}
	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			require.Error(t, protection.ValidateNetworkTarget(host, 80))
		})
	}
}

func TestSSRFProtectionAllowsOrdinaryPublicIPs(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:   false,
		DomainFilterMode: false,
		IpFilterMode:     false,
	}

	for _, host := range []string{
		"1.1.1.1",
		"8.8.8.8",
		"2001:4860:4860::8888",
		"2606:4700:4700::1111",
	} {
		t.Run(host, func(t *testing.T) {
			require.NoError(t, protection.ValidateNetworkTarget(host, 443))
		})
	}
}

func TestSSRFProtectionAllowsPrivateIPWhenExplicitlyEnabled(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:   true,
		DomainFilterMode: false,
		IpFilterMode:     false,
	}

	require.NoError(t, protection.ValidateNetworkTarget("10.0.0.1", 80))
}

func TestSSRFProtectionRejectsResolvedPrivateAndTransitionIPs(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:         false,
		DomainFilterMode:       false,
		IpFilterMode:           false,
		ApplyIPFilterForDomain: true,
	}

	require.NoError(t, protection.ValidateNetworkTarget("example.com", 80))
	for _, rawIP := range []string{
		"169.254.169.254",
		"192.88.99.2",
		"64:ff9b:1::1",
		"2002:7f00:1::",
		"5f00::1",
	} {
		t.Run(rawIP, func(t *testing.T) {
			require.Error(t, protection.ValidateResolvedIP("example.com", net.ParseIP(rawIP)))
		})
	}
}

func TestNewSSRFProtectionFromFetchSettingParsesPortRanges(t *testing.T) {
	protection, err := NewSSRFProtectionFromFetchSetting(false, false, false, nil, nil, []string{"80", "8000-8001"}, true)
	require.NoError(t, err)

	require.NoError(t, protection.ValidateNetworkTarget("example.com", 8001))
	require.Error(t, protection.ValidateNetworkTarget("example.com", 9000))
}
