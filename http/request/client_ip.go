package request

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ResolveClientIP 在可信代理场景下解析客户端 IP。
func ResolveClientIP(r *http.Request, trustedProxyChecker func(netip.Addr) bool) string {
	if r == nil {
		return ""
	}
	remoteIP, remoteOK := ParseRemoteIP(r.RemoteAddr)
	if remoteOK && trustedProxyChecker != nil && trustedProxyChecker(remoteIP) {
		if xff := strings.TrimSpace(strings.Join(r.Header.Values("X-Forwarded-For"), ",")); xff != "" {
			if ip, ok := resolveForwardedChain(remoteIP, xff, trustedProxyChecker); ok {
				return ip.String()
			}
			// A malformed chain cannot establish a trustworthy client boundary.
			return remoteIP.String()
		}
		if ip, ok := ParseForwardedIP(r.Header.Get("X-Real-IP")); ok {
			return ip.String()
		}
	}
	if remoteOK {
		return remoteIP.String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func resolveForwardedChain(remoteIP netip.Addr, xff string, trustedProxyChecker func(netip.Addr) bool) (netip.Addr, bool) {
	current := remoteIP
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if !trustedProxyChecker(current) {
			return current, true
		}
		next, ok := ParseForwardedIP(parts[i])
		if !ok {
			return netip.Addr{}, false
		}
		current = next
	}
	return current, current.IsValid()
}

// ParseRemoteIP 解析RemoteIP。
func ParseRemoteIP(remoteAddr string) (netip.Addr, bool) {
	if remoteAddr == "" {
		return netip.Addr{}, false
	}
	host := strings.TrimSpace(remoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip, true
}

// ParseForwardedFor 解析Forwarded。
func ParseForwardedFor(xff string) (netip.Addr, bool) {
	if xff == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(xff, ",")
	for _, p := range parts {
		if ip, ok := ParseForwardedIP(p); ok {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

// ParseForwardedIP 解析ForwardedIP。
func ParseForwardedIP(v string) (netip.Addr, bool) {
	s := strings.TrimSpace(v)
	if s == "" {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip, true
}
