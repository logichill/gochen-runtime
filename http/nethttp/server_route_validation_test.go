package nethttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen/errors"
	"gochen/httpx"
)

func registerAmbiguousOptionsRoutes(server *Server, calls *int) {
	handler := func(c httpx.IContext) error {
		*calls++
		return c.String(http.StatusOK, "executed")
	}
	server.GET("/api/v1/notes/browse/:id", handler)
	server.POST("/api/v1/notes/:id/rename", handler)
}

func requireRouteConfigurationError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errors.InvalidInput) || !strings.Contains(err.Error(), "HTTP route pattern") {
		t.Fatalf("route configuration error = %v, want InvalidInput pattern error", err)
	}
}

func assertAmbiguousRoutesClosed(t *testing.T, handler http.Handler, calls *int) {
	t.Helper()
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notes/browse/1"},
		{http.MethodPost, "/api/v1/notes/1/rename"},
		{http.MethodOptions, "/api/v1/notes/browse/rename"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503", request.method, request.path, response.Code)
		}
	}
	if *calls != 0 {
		t.Fatalf("invalid configuration executed %d business handlers", *calls)
	}
}

func TestHTTPServer_SealRejectsAmbiguousAutomaticOptions(t *testing.T) {
	server := NewServer(nil)
	calls := 0
	registerAmbiguousOptionsRoutes(server, &calls)
	if conflicts := server.RouteConflicts(); len(conflicts) != 0 {
		t.Fatalf("fixture must conflict by pattern, not exact method/path: %+v", conflicts)
	}
	err := server.SealRoutes()
	requireRouteConfigurationError(t, err)
	if server.RegistrationError() != err || server.SealRoutes() != err {
		t.Fatal("route configuration failure was not retained")
	}
	assertAmbiguousRoutesClosed(t, server.Handler(), &calls)
}

func TestHTTPServer_DirectDispatchRejectsInvalidConfiguration(t *testing.T) {
	for _, accessor := range []struct {
		name    string
		handler func(*Server) http.Handler
	}{
		{"Handler", func(server *Server) http.Handler { return server.Handler() }},
		{"ServeMux", func(server *Server) http.Handler { return server.ServeMux() }},
	} {
		t.Run(accessor.name, func(t *testing.T) {
			server := NewServer(nil)
			calls := 0
			registerAmbiguousOptionsRoutes(server, &calls)
			handler := accessor.handler(server)
			requireRouteConfigurationError(t, server.RegistrationError())
			assertAmbiguousRoutesClosed(t, handler, &calls)
		})
	}
}

func TestHTTPServer_InvalidRebuildReplacesPreviouslyExposedDispatch(t *testing.T) {
	server := NewServer(nil)
	calls := 0
	server.GET("/api/v1/notes/browse/:id", func(c httpx.IContext) error {
		calls++
		return c.String(http.StatusOK, "previous")
	})
	handler := server.Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/notes/browse/1", nil))
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("valid initial dispatch = %d, calls=%d", response.Code, calls)
	}
	calls = 0
	server.POST("/api/v1/notes/:id/rename", func(c httpx.IContext) error {
		calls++
		return c.String(http.StatusOK, "new")
	})
	err := server.RegistrationError()
	requireRouteConfigurationError(t, err)
	assertAmbiguousRoutesClosed(t, handler, &calls)
	server.GET("/later", func(c httpx.IContext) error { calls++; return c.String(http.StatusOK, "later") })
	if server.RegistrationError() != err {
		t.Fatal("later registration replaced the original configuration error")
	}
	assertAmbiguousRoutesClosed(t, server.Handler(), &calls)
}

func TestHTTPServer_StartRejectsAmbiguousRoutesBeforeListening(t *testing.T) {
	server := NewServer(nil)
	calls := 0
	registerAmbiguousOptionsRoutes(server, &calls)
	for attempt := 0; attempt < 2; attempt++ {
		// 非法地址保证回归时不会阻塞监听；预期在使用地址前就拒绝路由配置。
		err := server.Start("not-a-listen-address")
		requireRouteConfigurationError(t, err)
		if server.serverActive() {
			t.Fatal("invalid route configuration activated server lifecycle")
		}
	}
	assertAmbiguousRoutesClosed(t, server.Handler(), &calls)
}

func TestHTTPServer_SealRejectsInvalidPatternAndStaticConflict(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		register func(*Server)
	}{
		{"invalid wildcard", func(s *Server) { s.GET("/items/{broken", func(httpx.IContext) error { return nil }) }},
		{"duplicate static", func(s *Server) { s.Static("/assets", t.TempDir()); s.Static("/assets", t.TempDir()) }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := NewServer(nil)
			fixture.register(server)
			requireRouteConfigurationError(t, server.SealRoutes())
		})
	}
}
