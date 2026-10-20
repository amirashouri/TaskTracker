package install

import (
	"net"
	"testing"
)

func ipNet(address string) *net.IPNet {
	ip := net.ParseIP(address)
	if ip == nil {
		panic("bad test address " + address)
	}
	if ip.To4() != nil {
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(24, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(64, 128)}
}

func TestFirstLANIPv4PrefersAPrivateAddress(t *testing.T) {
	addrs := []net.Addr{
		ipNet("127.0.0.1"),
		ipNet("169.254.10.20"),
		ipNet("8.8.8.8"),
		ipNet("192.168.1.10"),
	}

	if got := firstLANIPv4(addrs); got != "192.168.1.10" {
		t.Errorf("firstLANIPv4 = %q, want the private address", got)
	}
}

func TestFirstLANIPv4FallsBackToAPublicAddress(t *testing.T) {
	addrs := []net.Addr{
		ipNet("127.0.0.1"),
		ipNet("fe80::1"),
		ipNet("8.8.8.8"),
	}

	if got := firstLANIPv4(addrs); got != "8.8.8.8" {
		t.Errorf("firstLANIPv4 = %q, want the public address fallback", got)
	}
}

func TestFirstLANIPv4SkipsLoopbackLinkLocalIPv6AndNonIPNet(t *testing.T) {
	addrs := []net.Addr{
		ipNet("127.0.0.1"),
		ipNet("169.254.10.20"),
		ipNet("fe80::1"),
		ipNet("2001:db8::1"),
		&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 8080},
	}

	if got := firstLANIPv4(addrs); got != "" {
		t.Errorf("firstLANIPv4 = %q, want no IPv4 address", got)
	}
}

func TestIsVirtualInterface(t *testing.T) {
	for _, name := range []string{"docker0", "br-1234", "bridge0", "veth42", "virbr0", "vmnet1", "utun3", "tun0", "tap0", "tailscale0"} {
		if !isVirtualInterface(name) {
			t.Errorf("isVirtualInterface(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"en0", "en1", "eth0", "wlan0", "ens5", "bond0"} {
		if isVirtualInterface(name) {
			t.Errorf("isVirtualInterface(%q) = true, want false", name)
		}
	}
}
