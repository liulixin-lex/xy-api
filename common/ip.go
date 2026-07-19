package common

import "net"

func IsIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil
}

func ParseIP(s string) net.IP {
	return net.ParseIP(s)
}

func IsPrivateIP(ip net.IP) bool {
	// Keep every public caller on the same IANA special-purpose classification
	// used by the SSRF dialer. Despite the historical name, this intentionally
	// includes loopback, link-local, documentation, carrier-NAT, multicast,
	// unspecified, and other non-public address space.
	return isPrivateIP(ip)
}

func IsIpInCIDRList(ip net.IP, cidrList []string) bool {
	for _, cidr := range cidrList {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			// 尝试作为单个IP处理
			if whitelistIP := net.ParseIP(cidr); whitelistIP != nil {
				if ip.Equal(whitelistIP) {
					return true
				}
			}
			continue
		}

		if network.Contains(ip) {
			return true
		}
	}
	return false
}
