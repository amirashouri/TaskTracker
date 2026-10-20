package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func ipRequest(remoteAddr, xff, forwarded string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if forwarded != "" {
		req.Header.Set("Forwarded", forwarded)
	}
	return req
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8:1::/48"),
	}
	// End on a valid address, not a trailing comma: a malformed header would
	// be rejected for that reason too, and the test would not prove the cap.
	oversized := strings.TrimSuffix(
		strings.Repeat("203.0.113.9,", maxForwardedHeaderBytes/len("203.0.113.9,")+1),
		",",
	)

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		forwarded  string
		trusted    []netip.Prefix
		want       string
	}{
		{
			name:       "no proxy configured uses the peer",
			remoteAddr: "192.0.2.10:1234",
			xff:        "203.0.113.9",
			want:       "192.0.2.10",
		},
		{
			name:       "untrusted peer ignores x-forwarded-for",
			remoteAddr: "192.0.2.10:1234",
			xff:        "203.0.113.9",
			trusted:    trusted,
			want:       "192.0.2.10",
		},
		{
			name:       "untrusted peer ignores forwarded",
			remoteAddr: "192.0.2.10:1234",
			forwarded:  "for=203.0.113.9",
			trusted:    trusted,
			want:       "192.0.2.10",
		},
		{
			name:       "trusted peer with no header is itself the client",
			remoteAddr: "10.0.0.5:1234",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "single forwarded hop",
			remoteAddr: "10.0.0.5:1234",
			xff:        "203.0.113.9",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "rightmost trusted hop is skipped",
			remoteAddr: "10.0.0.5:1234",
			xff:        "203.0.113.9, 10.0.0.6",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "the first untrusted hop from the right wins over a forged leftmost",
			remoteAddr: "10.0.0.5:1234",
			xff:        "198.51.100.7, 203.0.113.9, 10.0.0.6",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "an all-trusted chain returns the leftmost hop",
			remoteAddr: "10.0.0.5:1234",
			xff:        "10.0.0.6, 10.0.0.7",
			trusted:    trusted,
			want:       "10.0.0.6",
		},
		{
			name:       "forwarded is used when x-forwarded-for is absent",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  "for=203.0.113.9;proto=https, for=10.0.0.6",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "quoted bracketed IPv6 with a port",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  `for="[2001:db8:2::1]:443"`,
			trusted:    trusted,
			want:       "2001:db8:2::1",
		},
		{
			name:       "quoted bracketed IPv6 without a port",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  `for="[2001:db8:2::2]"`,
			trusted:    trusted,
			want:       "2001:db8:2::2",
		},
		{
			name:       "x-forwarded-for wins over forwarded",
			remoteAddr: "10.0.0.5:1234",
			xff:        "198.51.100.1",
			forwarded:  "for=203.0.113.9",
			trusted:    trusted,
			want:       "198.51.100.1",
		},
		{
			name:       "IPv6 trusted peer",
			remoteAddr: "[2001:db8:1::5]:1234",
			xff:        "2001:db8:2::9",
			trusted:    trusted,
			want:       "2001:db8:2::9",
		},
		{
			name:       "IPv4-mapped peer is unmapped before the trust check",
			remoteAddr: "[::ffff:10.0.0.5]:1234",
			xff:        "203.0.113.9",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "IPv4-mapped forwarded address is unmapped",
			remoteAddr: "10.0.0.5:1234",
			xff:        "::ffff:203.0.113.9",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "forwarded address with a port",
			remoteAddr: "10.0.0.5:1234",
			xff:        "203.0.113.9:443",
			trusted:    trusted,
			want:       "203.0.113.9",
		},
		{
			name:       "peer without a port",
			remoteAddr: "10.0.0.5",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "unparsable x-forwarded-for falls back to the peer",
			remoteAddr: "10.0.0.5:1234",
			xff:        "not-an-ip",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "one bad x-forwarded-for hop rejects the whole header",
			remoteAddr: "10.0.0.5:1234",
			xff:        "203.0.113.9, not-an-ip",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "oversized x-forwarded-for falls back to the peer",
			remoteAddr: "10.0.0.5:1234",
			xff:        oversized,
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "forwarded unknown falls back to the peer",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  "for=unknown",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "forwarded obfuscated identifier falls back to the peer",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  "for=_hidden",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "forwarded without a for parameter falls back to the peer",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  "proto=https;by=10.0.0.6",
			trusted:    trusted,
			want:       "10.0.0.5",
		},
		{
			name:       "unparsable remote address is returned verbatim",
			remoteAddr: "not-an-address",
			trusted:    trusted,
			want:       "not-an-address",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientIP(ipRequest(tc.remoteAddr, tc.xff, tc.forwarded), tc.trusted)
			if got != tc.want {
				t.Errorf("clientIP(%q, xff=%q, forwarded=%q) = %q, want %q",
					tc.remoteAddr, tc.xff, tc.forwarded, got, tc.want)
			}
		})
	}
}

// A header can arrive as more than one line; Go exposes each line separately.
// The resolver must walk the combined chain, not only the first line.
func TestClientIPCombinesXForwardedForLines(t *testing.T) {
	req := ipRequest("10.0.0.5:1234", "", "")
	req.Header.Add("X-Forwarded-For", "203.0.113.9")
	req.Header.Add("X-Forwarded-For", "10.0.0.6")

	got := clientIP(req, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got != "203.0.113.9" {
		t.Errorf("clientIP with two header lines = %q, want 203.0.113.9", got)
	}
}

// The size cap must apply to Forwarded too, not just X-Forwarded-For.
func TestClientIPRejectsAnOversizedForwardedHeader(t *testing.T) {
	oversized := "for=203.0.113.9;" + strings.Repeat("x=y;", maxForwardedHeaderBytes)
	got := clientIP(ipRequest("10.0.0.5:1234", "", oversized),
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got != "10.0.0.5" {
		t.Errorf("clientIP with an oversized Forwarded header = %q, want the peer", got)
	}
}

// A zone identifies an interface on the local host, not the client. It must not
// leak into the limiter key.
func TestClientIPDropsTheZoneFromThePeer(t *testing.T) {
	got := clientIP(ipRequest("[fe80::1%eth0]:1234", "", ""), nil)
	if got != "fe80::1" {
		t.Errorf("clientIP with a zoned peer = %q, want fe80::1", got)
	}
}
