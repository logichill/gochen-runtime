package module_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	. "gochen-runtime/host/module"
	"gochen-runtime/host/module/initcap"
	"gochen-runtime/host/module/runtimecap"
	"gochen-runtime/http/nethttp"
)

func TestRuntimeManifestModuleSnapshot(t *testing.T) {
	manifestModule := NewRuntimeManifestModule("test-service")
	registry := NewModuleRegistry()
	if err := registry.Register(ModuleInfo{
		ID:   "iam",
		Name: "Identity and Access",
		Features: []PublicFeature{
			{Key: "auth.mode", Value: "jwt"},
			{Key: "auth.csrf", Value: "enabled"},
		},
	}); err != nil {
		t.Fatalf("register module: %v", err)
	}

	container := basic.New()
	recorder := httptest.NewRecorder()
	server := nethttp.NewServer(nil)
	group := server.Group("/api")

	opts := NewModuleInitOptions(
		container,
		container,
		container,
		initcap.Set(HTTPKey, IHTTPOptions(runtimecap.NewModuleHTTPOptions(group, "/runtime"))),
		runtimecap.ModuleRegistry(capability.IModuleRegistry(registry)),
	)

	if err := manifestModule.Init(opts); err != nil {
		t.Fatalf("init manifest module: %v", err)
	}

	if err := manifestModule.RegisterRoutes(context.Background()); err != nil {
		t.Fatalf("register routes: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/modules", nil)
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200, body: %s", recorder.Code, recorder.Body.String())
	}
}
