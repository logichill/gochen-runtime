package authz_test

import (
	"net/http/httptest"
	"testing"

	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module/runtime"
	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/nethttp"
	"gochen/auth"
	"gochen/httpx"
)

func TestHostMonitoringRoutesParticipateInEntryValidation(t *testing.T) {
	for _, unbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "declared_diagnostics", true: "unbound_business_route"}[unbound], func(t *testing.T) {
			server := nethttp.NewServer(&runtimehttp.WebConfig{})
			if unbound {
				server.GET("/custom/business", func(ctx httpx.IContext) error { return ctx.JSON(200, httpx.JSONValue("unexpected business handler")) })
			}
			registry := authz.NewEntryRegistry()
			host := runtime.NewHost(nil, config.WithHTTPServer(server), config.WithBasePath("/custom"), config.WithEnableMonitoringRoutes(true), config.WithEntryValidator(registry))
			if err := host.Prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			err := host.StartBackground(t.Context())
			if unbound {
				if err == nil {
					t.Fatal("an unbound business route passed the startup gate")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Shutdown(t.Context()) })
			for _, path := range []string{"/healthz", "/readyz", "/metrics", "/snapshot"} {
				found := false
				for _, status := range registry.Report() {
					if status.Entry.Key == "GET /custom"+path {
						found = status.Class == auth.EntryAllowed && status.Module == "host" && status.Source != ""
					}
				}
				if !found {
					t.Fatalf("missing diagnostic binding for %s", path)
				}
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/custom"+path, nil))
				if recorder.Code != 200 {
					t.Fatalf("%s returned %d: %s", path, recorder.Code, recorder.Body.String())
				}
			}
		})
	}
}
