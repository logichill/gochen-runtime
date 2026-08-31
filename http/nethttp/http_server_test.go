package nethttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	runtimehttp "gochen-runtime/http"
	"gochen/httpx"
)

func TestHTTPServer_MiddlewareOrderAndGroupInheritance(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	order := make([]string, 0, 5)
	srv.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "global-before")
		err := next()
		order = append(order, "global-after")
		return err
	})
	group := srv.Group("/api")
	group.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "group-before")
		err := next()
		order = append(order, "group-after")
		return err
	})
	group.GET("/test", func(ctx httpx.IContext) error {
		order = append(order, "handler")
		return ctx.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("unexpected response: status=%d body=%q", rec.Code, rec.Body.String())
	}
	want := []string{"global-before", "group-before", "handler", "group-after", "global-after"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
}

func TestHTTPServer_AutoOptionsUsesMiddlewareAndAllowHeader(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	group := srv.Group("/api")
	group.Use(func(ctx httpx.IContext, next func() error) error {
		ctx.SetHeader("X-Middleware", "called")
		return next()
	})
	group.GET("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "get") })
	group.POST("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "post") })

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/shared", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("X-Middleware"); got != "called" {
		t.Fatalf("X-Middleware = %q", got)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST, OPTIONS" {
		t.Fatalf("Allow = %q", got)
	}
}

func TestHTTPServer_AutoOptionsMergesMiddlewaresForSamePath(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	order := make([]string, 0, 2)
	groupA := srv.Group("/api")
	groupA.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "group-a")
		ctx.SetHeader("X-Group-A", "1")
		return next()
	})
	groupB := srv.Group("/api")
	groupB.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "group-b")
		ctx.SetHeader("X-Group-B", "1")
		return next()
	})
	groupA.GET("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "get") })
	groupB.POST("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "post") })

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/shared", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if want := []string{"group-a", "group-b"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
	if rec.Header().Get("X-Group-A") != "1" || rec.Header().Get("X-Group-B") != "1" {
		t.Fatalf("merged middleware headers missing: %#v", rec.Header())
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST, OPTIONS" {
		t.Fatalf("Allow = %q", got)
	}
}

func TestHTTPServer_AutoOptionsDeduplicatesInheritedMiddlewarePrefix(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	order := make([]string, 0, 3)
	commonCalls := 0
	base := srv.Group("/api")
	base.Use(func(ctx httpx.IContext, next func() error) error {
		commonCalls++
		order = append(order, "common")
		return next()
	})
	getGroup := base.Group("/v1")
	getGroup.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "get")
		return next()
	})
	postGroup := base.Group("/v1")
	postGroup.Use(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "post")
		return next()
	})
	getGroup.GET("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "get") })
	postGroup.POST("/shared", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "post") })

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/v1/shared", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if want := []string{"common", "get", "post"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
	if commonCalls != 1 {
		t.Fatalf("common middleware calls = %d, want 1", commonCalls)
	}
}

func TestHTTPServer_ExplicitOptionsDoesNotConflictWithAutoOptions(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.GET("/api/test", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "get") })
	srv.OPTIONS("/api/test", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "explicit") })

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/test", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "explicit" {
		t.Fatalf("unexpected explicit OPTIONS response: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHTTPServer_HandlerReflectsRoutesAddedAfterExposure(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	handler := srv.Handler()
	srv.GET("/after", func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "after") })

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/after", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "after" {
		t.Fatalf("unexpected response: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHTTPServer_ServeMuxNativeRouteIsServedByHandler(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.ServeMux().HandleFunc("/native", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("native"))
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/native", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "native" {
		t.Fatalf("unexpected native response: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHTTPServer_PathParams(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	var gotID string
	srv.GET("/users/:id", func(ctx httpx.IContext) error {
		gotID = ctx.Param("id")
		return ctx.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/42", nil))
	if rec.Code != http.StatusOK || gotID != "42" {
		t.Fatalf("status=%d id=%q", rec.Code, gotID)
	}
}

func TestHTTPServer_ApplyTimeoutDefaultsIncludesHeaderGuards(t *testing.T) {
	cfg := &runtimehttp.WebConfig{}
	srv := NewServer(cfg)
	srv.applyTimeoutDefaults()
	if cfg.ReadHeaderTimeout != runtimehttp.DefaultReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v", cfg.ReadHeaderTimeout)
	}
	if cfg.MaxHeaderBytes != runtimehttp.DefaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %v", cfg.MaxHeaderBytes)
	}
}

func TestHTTPServer_TLSMinVersionDefaultsTo12AndRejectsLowerConfig(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{TLSEnabled: true})
	if err := srv.applySecurityDefaults(); err != nil {
		t.Fatalf("applySecurityDefaults: %v", err)
	}
	httpSrv, err := srv.newHTTPServer(":0")
	if err != nil {
		t.Fatalf("newHTTPServer: %v", err)
	}
	if httpSrv.TLSConfig == nil || httpSrv.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected TLS config: %#v", httpSrv.TLSConfig)
	}

	tooLow := NewServer(&runtimehttp.WebConfig{TLSEnabled: true, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS11}})
	if err := tooLow.applySecurityDefaults(); err == nil {
		t.Fatal("expected TLS 1.1 configuration to be rejected")
	}
}

func TestHTTPServer_StaticSafeDefaults(t *testing.T) {
	root := t.TempDir()
	writeServerTestFile(t, filepath.Join(root, "index.html"), "<html>ok</html>")
	writeServerTestFile(t, filepath.Join(root, "app.js"), "console.log('ok')")
	writeServerTestFile(t, filepath.Join(root, ".env"), "secret")
	writeServerTestFile(t, filepath.Join(root, "dir", "file.txt"), "content")
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.Static("/static/", root)

	tests := []struct {
		path        string
		wantStatus  int
		wantCache   string
		wantNoSniff bool
	}{
		{path: "/static/", wantStatus: http.StatusOK, wantCache: "no-cache", wantNoSniff: true},
		{path: "/static/app.js", wantStatus: http.StatusOK, wantCache: "public, max-age=3600", wantNoSniff: true},
		{path: "/static/.env", wantStatus: http.StatusNotFound},
		{path: "/static/dir/", wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantStatus {
			t.Fatalf("GET %s status=%d, want %d", tc.path, rec.Code, tc.wantStatus)
		}
		if tc.wantCache != "" && rec.Header().Get("Cache-Control") != tc.wantCache {
			t.Fatalf("GET %s Cache-Control=%q", tc.path, rec.Header().Get("Cache-Control"))
		}
		if tc.wantNoSniff && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s X-Content-Type-Options=%q", tc.path, rec.Header().Get("X-Content-Type-Options"))
		}
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/static/", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("unexpected method response: status=%d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestHTTPServer_ServeStaticSetsNoSniff(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.js")
	writeServerTestFile(t, file, "console.log('ok')")
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.ServeStatic("/app.js", file)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestHTTPServer_StaticBlocksSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeServerTestFile(t, outside, "top-secret")
	if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.Static("/static/", root)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/leak.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestNewServer_NilConfigAppliesDefaults(t *testing.T) {
	srv := NewServer(nil)
	if srv.config.Port != runtimehttp.DefaultWebPort || srv.config.Host != runtimehttp.DefaultWebHost || srv.config.Mode != runtimehttp.DefaultWebMode {
		t.Fatalf("unexpected default config: %#v", srv.config)
	}
}

func TestHTTPServer_RouteConflictsUseCanonicalMuxPattern(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	handler := func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "ok") }
	srv.GET("/users/:id", handler)
	srv.GET("/users/{id}", handler)

	if !hasServerRouteConflict(srv.RouteConflicts(), "GET", "/users/{id}", 2) {
		t.Fatalf("canonical conflict missing: %#v", srv.RouteConflicts())
	}
}

func TestHTTPServer_RouteConflictsIncludeStaticRoutes(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	srv.Static("/assets", t.TempDir())
	srv.Static("/assets", t.TempDir())

	conflicts := srv.RouteConflicts()
	if !hasServerRouteConflict(conflicts, "GET", "/assets/", 2) || !hasServerRouteConflict(conflicts, "HEAD", "/assets/", 2) {
		t.Fatalf("static conflicts missing: %#v", conflicts)
	}
}

func TestHTTPServer_StartDoesNotOwnRouteConflictFailFast(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{TLSEnabled: true})
	handler := func(ctx httpx.IContext) error { return ctx.String(http.StatusOK, "ok") }
	srv.GET("/dup", handler)
	srv.GET("/dup", handler)

	err := srv.Start("127.0.0.1:0")
	if err == nil || strings.Contains(err.Error(), "route conflicts detected") {
		t.Fatalf("Start should return TLS configuration error, got %v", err)
	}
}

func hasServerRouteConflict(conflicts []httpx.RouteConflict, method, path string, count int) bool {
	for _, conflict := range conflicts {
		if conflict.Method == method && conflict.Path == path && conflict.Count == count {
			return true
		}
	}
	return false
}

func writeServerTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
