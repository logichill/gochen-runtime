package request

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestResolveClientIP_TrustsForwardedHeadersOnlyFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "bad, 203.0.113.10")

	untrusted := ResolveClientIP(req, func(netip.Addr) bool { return false })
	if untrusted != "10.0.0.1" {
		t.Fatalf("untrusted ResolveClientIP = %q, want remote IP", untrusted)
	}

	trusted := ResolveClientIP(req, func(ip netip.Addr) bool {
		return ip == netip.MustParseAddr("10.0.0.1")
	})
	if trusted != "203.0.113.10" {
		t.Fatalf("trusted ResolveClientIP = %q, want forwarded IP", trusted)
	}
}

func TestResolveClientIP_StripsTrustedProxyChainFromRight(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")

	trusted := func(ip netip.Addr) bool {
		return ip == netip.MustParseAddr("10.0.0.1") || ip == netip.MustParseAddr("10.0.0.2")
	}
	if got := ResolveClientIP(req, trusted); got != "203.0.113.10" {
		t.Fatalf("ResolveClientIP = %q, want first untrusted hop", got)
	}
}

func TestResolveClientIP_DoesNotTrustSpoofedLeftmostForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	// A boundary proxy appended the actual peer to a client-supplied XFF value.
	req.Header.Set("X-Forwarded-For", "198.51.100.66, 203.0.113.10")

	trusted := func(ip netip.Addr) bool { return ip == netip.MustParseAddr("10.0.0.1") }
	if got := ResolveClientIP(req, trusted); got != "203.0.113.10" {
		t.Fatalf("ResolveClientIP = %q, want rightmost untrusted hop", got)
	}
}

func TestResolveClientIP_DoesNotTrustSpoofedFirstForwardedForHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Add("X-Forwarded-For", "198.51.100.66")
	req.Header.Add("X-Forwarded-For", "203.0.113.10")

	trusted := func(ip netip.Addr) bool { return ip == netip.MustParseAddr("10.0.0.1") }
	if got := ResolveClientIP(req, trusted); got != "203.0.113.10" {
		t.Fatalf("ResolveClientIP = %q, want proxy-appended client hop", got)
	}
}

func TestResolveClientIP_MalformedForwardedChainFallsBackToRemote(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, malformed")

	trusted := func(ip netip.Addr) bool { return ip == netip.MustParseAddr("10.0.0.1") }
	if got := ResolveClientIP(req, trusted); got != "10.0.0.1" {
		t.Fatalf("ResolveClientIP = %q, want trusted remote fallback", got)
	}
}

func TestParseRemoteIP_Boundaries(t *testing.T) {
	if _, ok := ParseRemoteIP(""); ok {
		t.Fatalf("ParseRemoteIP empty ok=true, want false")
	}
	if ip, ok := ParseRemoteIP("[2001:db8::1]:443"); !ok || ip.String() != "2001:db8::1" {
		t.Fatalf("ParseRemoteIP IPv6 hostport = %v, %v", ip, ok)
	}
}
