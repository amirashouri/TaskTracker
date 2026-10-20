package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// maxForwardedHeaderBytes bounds the forwarding header the resolver will read.
// A trusted proxy that sends a larger one is more likely to be compromised or
// misconfigured than to be describing a real hop list, so the peer address is
// used instead of walking an unbounded value.
const maxForwardedHeaderBytes = 8 << 10

// clientIP returns the address the sign-in limiter keys on. The peer address is
// authoritative unless it falls inside a configured proxy prefix; only then is
// a forwarding header read, and only to find the first hop that is not another
// trusted proxy.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, ok := parsePeer(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if !isTrusted(peer, trusted) {
		return peer.String()
	}

	hops, ok := forwardedHops(r)
	if !ok || len(hops) == 0 {
		return peer.String()
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !isTrusted(hops[i], trusted) {
			return hops[i].String()
		}
	}
	// Every hop is trusted. The leftmost is the originating address; returning
	// it is more useful than collapsing the whole chain back to the peer.
	return hops[0].String()
}

func isTrusted(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// parsePeer reads RemoteAddr's host. The optional port is not part of the
// address; a value without one is accepted so a test or a non-standard listener
// still yields a usable key.
func parsePeer(remoteAddr string) (netip.Addr, bool) {
	host := strings.TrimSpace(remoteAddr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return normalizeAddr(addr), true
}

func normalizeAddr(addr netip.Addr) netip.Addr {
	if addr.Zone() != "" {
		addr = addr.WithZone("")
	}
	return addr.Unmap()
}

// forwardedHops returns the address list from X-Forwarded-For, or from
// Forwarded when the former is absent. The headers are alternatives, not a
// combined chain, so only one is read. A header that cannot be read in full is
// rejected as a whole.
func forwardedHops(r *http.Request) ([]netip.Addr, bool) {
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		return parseXForwardedFor(values)
	}
	if values := r.Header.Values("Forwarded"); len(values) > 0 {
		return parseForwarded(values)
	}
	return nil, false
}

func parseXForwardedFor(values []string) ([]netip.Addr, bool) {
	raw := strings.Join(values, ",")
	if len(raw) > maxForwardedHeaderBytes {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	hops := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		addr, ok := parseForwardedAddr(strings.TrimSpace(part))
		if !ok {
			return nil, false
		}
		hops = append(hops, addr)
	}
	return hops, len(hops) > 0
}

// parseForwarded reads RFC 7239 Forwarded elements. A missing or obfuscated
// `for` parameter makes the whole header unusable: the resolver cannot know
// which hop it describes, and guessing would be worse than the peer address.
func parseForwarded(values []string) ([]netip.Addr, bool) {
	raw := strings.Join(values, ",")
	if len(raw) > maxForwardedHeaderBytes {
		return nil, false
	}
	elements, ok := splitForwarded(raw)
	if !ok {
		return nil, false
	}
	hops := make([]netip.Addr, 0, len(elements))
	for _, element := range elements {
		value, ok := forwardedFor(element)
		if !ok {
			return nil, false
		}
		addr, ok := parseForwardedAddr(value)
		if !ok {
			return nil, false
		}
		hops = append(hops, addr)
	}
	return hops, len(hops) > 0
}

// splitForwarded separates the comma-delimited elements without splitting a
// quoted parameter value, which may legally contain a comma.
func splitForwarded(raw string) ([]string, bool) {
	var elements []string
	var current strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case inQuotes && r == '\\':
			current.WriteRune(r)
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			elements = append(elements, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if inQuotes || escaped {
		return nil, false
	}
	elements = append(elements, strings.TrimSpace(current.String()))
	return elements, true
}

func forwardedFor(element string) (string, bool) {
	for _, param := range strings.Split(element, ";") {
		key, value, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "for") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return "", false
			}
			value = unquoted
		}
		if value == "" || strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
			return "", false
		}
		return value, true
	}
	return "", false
}

// parseForwardedAddr accepts the shapes both headers use: a bare address, an
// address with a port, and a bracketed IPv6 address with an optional port.
func parseForwardedAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return normalizeAddr(addr), true
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return normalizeAddr(addrPort.Addr()), true
	}
	return netip.Addr{}, false
}
