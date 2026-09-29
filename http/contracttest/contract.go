// Package contracttest 验证 HTTP 适配器共同的中间件、安全配置和静态资源行为。
package contracttest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/middleware"
	"gochen/errors"
	"gochen/httpx"
)

// IServer 暴露可直接测试的 HTTP handler 和 Core 服务契约。
type IServer interface {
	httpx.IServer
	Handler() http.Handler
}

// Factory 根据共享配置创建待测适配器。
type Factory func(*runtimehttp.WebConfig) (IServer, error)

// Run 在具体适配器上执行共同的行为契约。
func Run(t *testing.T, factory Factory) {
	t.Helper()
	newServer := func(t *testing.T) IServer {
		t.Helper()
		logging := false
		srv, err := factory(&runtimehttp.WebConfig{Mode: "test", EnableRequestLog: &logging})
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	t.Run("Context", func(t *testing.T) { runContext(t, newServer) })
	t.Run("ModuleMounts", func(t *testing.T) { runModules(t, newServer) })
	for _, scope := range []string{"global", "group"} {
		t.Run("MiddlewareShortCircuit/"+scope, func(t *testing.T) {
			srv := newServer(t)
			deny := func(ctx httpx.IContext, next func() error) error {
				return ctx.String(http.StatusUnauthorized, "denied")
			}
			called := false
			handler := func(ctx httpx.IContext) error { called = true; return ctx.String(http.StatusOK, "unexpected") }
			if scope == "global" {
				srv.Use(deny)
				srv.GET("/route", handler)
			} else {
				srv.Group("/route").Use(deny).GET("", handler)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
			if called || rec.Code != http.StatusUnauthorized || rec.Body.String() != "denied" {
				t.Fatalf("short circuit failed: called=%v status=%d body=%q", called, rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("CORSPreflight", func(t *testing.T) {
		srv := newServer(t)
		const origin = "https://example.com"
		srv.Use(middleware.CORS(&middleware.CORSConfig{
			AllowOrigins: []string{origin}, AllowMethods: []string{http.MethodGet},
		}))
		called := false
		srv.GET("/route", func(ctx httpx.IContext) error {
			called = true
			return ctx.String(http.StatusOK, "unexpected")
		})
		req := httptest.NewRequest(http.MethodOptions, "/route", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if called || rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("preflight failed: called=%v status=%d headers=%v", called, rec.Code, rec.Header())
		}
	})
	t.Run("AbortStopsNext", func(t *testing.T) {
		srv := newServer(t)
		called := false
		srv.Use(func(ctx httpx.IContext, next func() error) error {
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return next()
		})
		srv.GET("/route", func(ctx httpx.IContext) error {
			called = true
			return ctx.String(http.StatusOK, "unexpected")
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
		if called || rec.Code != http.StatusUnauthorized {
			t.Fatalf("Abort did not stop next: called=%v status=%d", called, rec.Code)
		}
	})
	t.Run("AbortResponseNotOverwrittenByError", func(t *testing.T) {
		srv := newServer(t)
		srv.GET("/route", func(ctx httpx.IContext) error {
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return errors.NewCode(errors.Internal, "error after response")
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
		if rec.Code != http.StatusUnauthorized || rec.Body.Len() != 0 {
			t.Fatalf("committed abort response changed: status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
	t.Run("MiddlewareErrorAndOrder", func(t *testing.T) {
		srv := newServer(t)
		failure := errors.NewCode(errors.Internal, "handler failed")
		var order []string
		trace := func(name string) httpx.Middleware {
			return func(ctx httpx.IContext, next func() error) error {
				order = append(order, name+" before")
				err := next()
				order = append(order, name+" after")
				if !errors.Is(err, failure) {
					t.Errorf("%s did not receive handler error: %v", name, err)
				}
				if name == "global" {
					return ctx.String(http.StatusTeapot, "handled")
				}
				return err
			}
		}
		srv.Use(trace("global"))
		group := srv.Group("/api").Use(trace("group"))
		group.Group("/nested").Use(trace("nested")).GET("/route", func(httpx.IContext) error {
			order = append(order, "handler")
			return failure
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nested/route", nil))
		want := []string{"global before", "group before", "nested before", "handler", "nested after", "group after", "global after"}
		if !reflect.DeepEqual(order, want) || rec.Code != http.StatusTeapot || rec.Body.String() != "handled" {
			t.Fatalf("middleware chain: order=%v status=%d body=%q", order, rec.Code, rec.Body.String())
		}
	})
	t.Run("StaticSafety", func(t *testing.T) {
		srv := newServer(t)
		root, outside := t.TempDir(), t.TempDir()
		for _, name := range []string{"public.txt", ".env"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(root, "escape.txt")); err != nil {
			t.Fatal(err)
		}
		srv.Static("/files", root)
		for path, status := range map[string]int{"/files/public.txt": http.StatusOK, "/files/.env": http.StatusNotFound, "/files/escape.txt": http.StatusNotFound, "/files/": http.StatusNotFound} {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != status {
				t.Errorf("%s: status=%d want=%d", path, rec.Code, status)
			}
		}
	})
	t.Run("StaticMiddleware", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "public.txt")
		if err := os.WriteFile(file, []byte("private fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, prefix := range []string{"/files", "/files/"} {
			srv := newServer(t)
			srv.Use(func(ctx httpx.IContext, next func() error) error {
				return ctx.String(http.StatusForbidden, "denied")
			})
			srv.Static(prefix, root)
			srv.ServeStatic("/file", file)
			for _, path := range []string{"/files", "/files/public.txt", "/file"} {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != http.StatusForbidden || rec.Body.String() != "denied" {
					t.Errorf("static route bypassed middleware: prefix=%s path=%s status=%d body=%q", prefix, path, rec.Code, rec.Body.String())
				}
			}
		}
	})
	t.Run("StaticDirectoryRedirect", func(t *testing.T) {
		srv := newServer(t)
		root := t.TempDir()
		dir := filepath.Join(root, "nested", "directory")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, indexDir := range []string{root, dir} {
			if err := os.WriteFile(filepath.Join(indexDir, "index.html"), []byte("index"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		srv.Static("/files", root)
		for _, path := range []string{"/files", "/files/nested/directory"} {
			req := httptest.NewRequest(http.MethodGet, path+"?lang=zh", nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			location, err := rec.Result().Location()
			if err != nil || rec.Code != http.StatusMovedPermanently {
				t.Fatalf("directory redirect: status=%d location=%v err=%v", rec.Code, location, err)
			}
			target := req.URL.ResolveReference(location).String()
			if target != path+"/?lang=zh" {
				t.Fatalf("redirect lost mount prefix or query: %s", target)
			}
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Code != http.StatusOK || rec.Body.String() != "index" {
				t.Fatalf("redirected index unavailable: status=%d body=%q", rec.Code, rec.Body.String())
			}
		}
	})
	t.Run("StaticRejectsOtherMethods", func(t *testing.T) {
		srv := newServer(t)
		root := t.TempDir()
		file := filepath.Join(root, "public.txt")
		if err := os.WriteFile(file, []byte("static fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		srv.Static("/files", root)
		srv.ServeStatic("/file", file)
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			for _, path := range []string{"/file", "/files", "/files/public.txt"} {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
				if rec.Code < 400 || rec.Body.String() == "static fixture" {
					t.Errorf("static route accepted %s %s: status=%d body=%q", method, path, rec.Code, rec.Body.String())
				}
			}
		}
	})
	t.Run("StopBeforeStart", func(t *testing.T) {
		srv := newServer(t)
		if err := srv.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := srv.Start("127.0.0.1:0"); !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start after Stop = %v, want ErrServerClosed", err)
		}
	})
	t.Run("TLS", func(t *testing.T) { runTLS(t, factory) })
}
