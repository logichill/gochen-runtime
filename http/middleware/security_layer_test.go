package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/contextx"
	"gochen/httpx"
)

func TestSecurityLayer_AsAPI_DeniesSessionAndSetsLayer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	// 预置 session，确保 API layer 会隐藏该语义而不是依赖“未设置”。
	derived, err := contextx.WithSessionID(ctx.RequestContext(), "sess-1")
	if err != nil {
		t.Fatalf("WithSessionID returned error: %v", err)
	}
	ctx.SetContext(ctx.RequestContext().WithContext(derived))

	if err := AsAPI()(ctx, func() error {
		if got := httpx.RequestSecurityLayer(ctx.RequestContext()); got != httpx.SecurityLayerAPI {
			t.Fatalf("expected security layer %q, got %q", httpx.SecurityLayerAPI, got)
		}
		if got := contextx.SessionID(ctx.RequestContext()); got != "" {
			t.Fatalf("expected session id to be denied, got %q", got)
		}
		if contextx.SessionVisible(ctx.RequestContext()) {
			t.Fatalf("expected session visibility to be denied")
		}
		return nil
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
}

func TestSecurityLayer_AsWeb_AllowsSessionAndSetsLayer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	derived, err := contextx.WithSessionID(ctx.RequestContext(), "sess-1")
	if err != nil {
		t.Fatalf("WithSessionID returned error: %v", err)
	}
	ctx.SetContext(ctx.RequestContext().WithContext(derived))

	if err := AsWeb()(ctx, func() error {
		if got := httpx.RequestSecurityLayer(ctx.RequestContext()); got != httpx.SecurityLayerWeb {
			t.Fatalf("expected security layer %q, got %q", httpx.SecurityLayerWeb, got)
		}
		if got := contextx.SessionID(ctx.RequestContext()); got != "sess-1" {
			t.Fatalf("expected session id %q, got %q", "sess-1", got)
		}
		if !contextx.SessionVisible(ctx.RequestContext()) {
			t.Fatalf("expected session visibility to be allowed")
		}
		return nil
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
}

func TestSecurityLayer_Override_APIDenyToWebAllow(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	derived, err := contextx.WithSessionID(ctx.RequestContext(), "sess-1")
	if err != nil {
		t.Fatalf("WithSessionID returned error: %v", err)
	}
	ctx.SetContext(ctx.RequestContext().WithContext(derived))

	if err := AsAPI()(ctx, func() error {
		// Override: inner layer should win.
		return AsWeb()(ctx, func() error {
			if got := httpx.RequestSecurityLayer(ctx.RequestContext()); got != httpx.SecurityLayerWeb {
				t.Fatalf("expected security layer %q, got %q", httpx.SecurityLayerWeb, got)
			}
			if got := contextx.SessionID(ctx.RequestContext()); got != "sess-1" {
				t.Fatalf("expected session id %q after override, got %q", "sess-1", got)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
}
