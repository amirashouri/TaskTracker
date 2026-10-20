package install

import (
	"net"
	"strings"
)

// LANAddress returns the IPv4 address a customer should use on their local
// network. It prefers RFC 1918 addresses and skips loopback, link-local, VPN,
// and container-bridge interfaces, which are up but not reachable from a phone.
func LANAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	fallback := ""
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ip := firstLANIPv4(addrs)
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed != nil && parsed.IsPrivate() {
			return ip
		}
		if fallback == "" {
			fallback = ip
		}
	}
	return fallback
}

func firstLANIPv4(addrs []net.Addr) string {
	fallback := ""
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		if fallback == "" {
			fallback = ip.String()
		}
	}
	return fallback
}

func isVirtualInterface(name string) bool {
	prefixes := []string{
		"docker", "br-", "bridge", "veth", "virbr", "vmnet", "utun", "tun", "tap", "tailscale",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
