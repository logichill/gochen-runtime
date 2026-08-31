package nethttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen/contextx"
	"gochen/httpx"
)

func TestTrustedTenantHeaderResolverReadsHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set(httpx.HeaderTenantID, "t1")

	tenantID, err := TrustedTenantHeaderResolver(req)
	if err != nil {
		t.Fatalf("TrustedTenantHeaderResolver returned error: %v", err)
	}
	if tenantID != "t1" {
		t.Fatalf("expected tenant id t1, got %q", tenantID)
	}
}

func TestTenantMiddlewareInjectsContextAndSetsHeader(t *testing.T) {
	h := TenantMiddleware(func(*http.Request) (string, error) {
		return "t1", nil
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := contextx.TenantID(r.Context()); got != "t1" {
			t.Fatalf("expected tenant id in context, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get(httpx.HeaderTenantID); got != "t1" {
		t.Fatalf("expected response tenant header, got %q", got)
	}
}

func TestTenantMiddlewareDoesNotReadHeaderWithoutExplicitResolver(t *testing.T) {
	h := TenantMiddleware(func(*http.Request) (string, error) {
		return "", nil
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := contextx.TenantID(r.Context()); got != "" {
			t.Fatalf("expected empty tenant id in context, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set(httpx.HeaderTenantID, "t1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get(httpx.HeaderTenantID); got != "" {
		t.Fatalf("expected no response tenant header, got %q", got)
	}
}

func TestTenantMiddlewareRejectsInvalidHeader(t *testing.T) {
	called := false
	h := TenantMiddleware(TrustedTenantHeaderResolver)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set(httpx.HeaderTenantID, "bad tenant")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if called {
		t.Fatalf("next handler should not be called")
	}
}

func TestTenantMiddlewareNilResolverFailsClosed(t *testing.T) {
	called := false
	h := TenantMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if called {
		t.Fatalf("next handler should not be called")
	}
}
