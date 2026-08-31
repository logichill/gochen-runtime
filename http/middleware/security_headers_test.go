package middleware

import (
	"net/http"
	"testing"
)

func TestSecureDefaults_SetsDefaultSecurityHeaders(t *testing.T) {
	ctx, rec := newNetHTTPContext(t, http.MethodGet, "")

	called := false
	if err := SecureDefaults()(ctx, func() error {
		called = true
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if !called {
		t.Fatalf("expected next to be called")
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options=nosniff, got %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options=DENY, got %q", got)
	}
	if got := rec.Header().Get("X-XSS-Protection"); got != "0" {
		t.Fatalf("expected X-XSS-Protection=0, got %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("expected Referrer-Policy=strict-origin-when-cross-origin, got %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("expected no CSP by default, got %q", got)
	}
}
