package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/nethttp"
)

func TestCORS_PreservesCacheVariationWithoutOrigin(t *testing.T) {
	for _, origin := range []string{"", "https://a.example", "https://b.example"} {
		t.Run(origin, func(t *testing.T) {
			ctx, rec := newNetHTTPContext(t, http.MethodGet, origin)
			rec.Header().Add("Vary", "Accept-Encoding")
			rec.Header().Add("Vary", "Accept-Language")
			mw := CORS(&CORSConfig{AllowOrigins: []string{"https://a.example"}})
			if err := mw(ctx, func() error { return ctx.String(http.StatusOK, "ok") }); err != nil {
				t.Fatal(err)
			}
			vary := strings.Join(rec.Header().Values("Vary"), ",")
			for _, want := range []string{"Accept-Encoding", "Accept-Language", "Origin"} {
				if !strings.Contains(vary, want) {
					t.Errorf("Vary = %q, missing %s", vary, want)
				}
			}
		})
	}
}

func newNetHTTPContext(t *testing.T, method string, origin string) (*nethttp.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(method, "http://example.com/", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return newNetHTTPContextFromRequest(t, req)
}

func newNetHTTPContextFromRequest(t *testing.T, req *http.Request) (*nethttp.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	return ctx, rec
}

func TestCORSFromWebConfig_SafeDefaults(t *testing.T) {
	origin := "https://a.example"

	cases := []struct {
		name string
		cfg  *runtimehttp.WebConfig
	}{
		{name: "nil-config", cfg: nil},
		{name: "disabled", cfg: &runtimehttp.WebConfig{CORSEnabled: false}},
		{name: "enabled-but-empty-allowlist", cfg: &runtimehttp.WebConfig{CORSEnabled: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newNetHTTPContext(t, http.MethodGet, origin)
			mw := CORSFromWebConfig(tc.cfg)

			called := false
			if err := mw(ctx, func() error {
				called = true
				return ctx.String(http.StatusOK, "ok")
			}); err != nil {
				t.Fatalf("middleware returned error: %v", err)
			}

			if !called {
				t.Fatalf("expected next to be called")
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("expected no CORS header, got %q", got)
			}
		})
	}
}

func TestCORSFromWebConfig_Allowlist(t *testing.T) {
	origin := "https://a.example"
	cfg := &runtimehttp.WebConfig{
		CORSEnabled:      true,
		CORSAllowOrigins: []string{origin},
		CORSAllowMethods: []string{"GET"},
		CORSAllowHeaders: []string{"Content-Type"},
	}

	ctx, rec := newNetHTTPContext(t, http.MethodGet, origin)
	mw := CORSFromWebConfig(cfg)

	if err := mw(ctx, func() error {
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("expected allow origin %q, got %q", origin, got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary=Origin, got %q", got)
	}
}

func TestCORSFromWebConfig_DefaultsAllowCSRFHeader(t *testing.T) {
	origin := "https://a.example"
	req := httptest.NewRequest(http.MethodOptions, "http://example.com/", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "X-CSRF-Token")
	ctx, rec := newNetHTTPContextFromRequest(t, req)

	mw := CORSFromWebConfig(&runtimehttp.WebConfig{
		CORSEnabled:      true,
		CORSAllowOrigins: []string{origin},
	})

	if err := mw(ctx, func() error {
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, Origin, X-CSRF-Token" {
		t.Fatalf("expected default allow headers to include CSRF header, got %q", got)
	}
}

func TestCORS_AllowlistNonMatchingOriginStillVaries(t *testing.T) {
	ctx, rec := newNetHTTPContext(t, http.MethodGet, "https://b.example")
	mw := CORS(&CORSConfig{
		AllowOrigins: []string{"https://a.example"},
		AllowMethods: []string{"GET"},
	})

	if err := mw(ctx, func() error {
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected non-matching origin to be rejected, got %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary=Origin for non-matching allowlist origin, got %q", got)
	}
}

func TestCORS_NilIsNoop(t *testing.T) {
	origin := "https://a.example"
	ctx, rec := newNetHTTPContext(t, http.MethodGet, origin)

	called := false
	if err := CORS(nil)(ctx, func() error {
		called = true
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if !called {
		t.Fatalf("expected next to be called")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS header, got %q", got)
	}
}

func TestCORS_AllowAllOriginsByStar(t *testing.T) {
	origin := "https://evil.example"
	ctx, rec := newNetHTTPContext(t, http.MethodGet, origin)

	mw := CORS(&CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET"},
		AllowHeaders:     []string{"Content-Type"},
		AllowCredentials: false,
		MaxAge:           60,
	})

	if err := mw(ctx, func() error {
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected allow origin '*', got %q", got)
	}
}

func TestCORS_AllowAllOriginsRequiresOriginHeader(t *testing.T) {
	ctx, rec := newNetHTTPContext(t, http.MethodGet, "")

	mw := CORS(&CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET"},
	})

	if err := mw(ctx, func() error {
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow origin without Origin request header, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("expected no allow methods without Origin request header, got %q", got)
	}
}

func TestCORS_PreflightNonMatchingOriginSkipsAllowHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "http://example.com/", nil)
	req.Header.Set("Origin", "https://b.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	ctx, rec := newNetHTTPContextFromRequest(t, req)

	mw := CORS(&CORSConfig{
		AllowOrigins: []string{"https://a.example"},
		AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"Content-Type"},
		MaxAge:       60,
	})

	if err := mw(ctx, func() error {
		t.Fatal("expected preflight to skip next")
		return nil
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow origin for non-matching preflight, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("expected no allow methods for non-matching preflight, got %q", got)
	}
}

func TestCORS_Preflight_WritesNoContentAndSkipsNext(t *testing.T) {
	origin := "https://a.example"
	req := httptest.NewRequest(http.MethodOptions, "http://example.com/", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	ctx, rec := newNetHTTPContextFromRequest(t, req)

	mw := CORS(&CORSConfig{
		AllowOrigins: []string{origin},
		AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"Content-Type"},
		MaxAge:       60,
	})

	called := false
	if err := mw(ctx, func() error {
		called = true
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if called {
		t.Fatalf("expected next not to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("expected allow origin %q, got %q", origin, got)
	}
}

func TestCORS_OptionsWithoutPreflight_DoesNotShortCircuit(t *testing.T) {
	origin := "https://a.example"
	ctx, rec := newNetHTTPContext(t, http.MethodOptions, origin)

	mw := CORS(&CORSConfig{
		AllowOrigins: []string{origin},
		AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"Content-Type"},
		MaxAge:       60,
	})

	called := false
	if err := mw(ctx, func() error {
		called = true
		return ctx.String(http.StatusOK, "ok")
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if !called {
		t.Fatalf("expected next to be called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestCORSFromWebConfig_AllowCredentialsWithExplicitOrigin(t *testing.T) {
	origin := "http://localhost:5173"
	mw := CORSFromWebConfig(&runtimehttp.WebConfig{
		CORSEnabled:          true,
		CORSAllowOrigins:     []string{origin},
		CORSAllowCredentials: true,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	r.Header.Set("Origin", origin)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("credentials header = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("origin = %q", got)
	}
}
