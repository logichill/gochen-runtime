package nethttp_test

import (
	"context"
	"strings"
	"testing"

	"gochen-runtime/host"
	hostauthz "gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	hostruntime "gochen-runtime/host/module/runtime"
	"gochen-runtime/http/nethttp"
	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

func TestHostRejectsAmbiguousOptionsBeforeStartingModules(t *testing.T) {
	server := nethttp.NewServer(nil)
	server.GET("/api/v1/notes/browse/:id", func(httpx.IContext) error { t.Fatal("invalid routes were executed"); return nil })
	server.POST("/api/v1/notes/:id/rename", func(httpx.IContext) error { t.Fatal("invalid routes were executed"); return nil })
	entries := hostauthz.NewEntryRegistry()
	for _, route := range server.Routes() {
		if route.Method != "GET" && route.Method != "POST" {
			continue
		}
		if err := entries.BindHTTP(route.Method, route.Path, auth.EntryBinding{ResourceAccess: auth.ResourceAccessNone, AllowReason: "public test fixture", Source: "route validation test"}); err != nil {
			t.Fatal(err)
		}
	}
	started := false
	runtime := hostruntime.NewHost([]module.ModuleCtor{func() (module.IModule, error) {
		return host.Module("probe").OnStart(func(context.Context) error { started = true; return nil }).Build()
	}}, config.WithHTTPServer(server), config.WithDisableHealthRoute(true), config.WithEntryValidator(entries))
	if err := runtime.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := runtime.StartBackground(t.Context())
	if !errors.Is(err, errors.InvalidInput) || !strings.Contains(err.Error(), "HTTP route pattern") {
		t.Fatalf("StartBackground error = %v, want invalid route configuration", err)
	}
	if started {
		t.Fatal("module started before route configuration validation")
	}
}
