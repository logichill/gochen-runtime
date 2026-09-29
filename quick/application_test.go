package quick_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	"gochen/errors"
	"gochen/httpx"
	"gochen/messaging/transport/direct"
	"gochen/testkit"
)

func TestApplicationFailedStartPreservesRunningTransport(t *testing.T) {
	ctx := context.Background()
	transport := direct.NewSyncTransport()
	if err := transport.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if transport.Stats().Running {
			if err := transport.Stop(ctx); err != nil {
				t.Error(err)
			}
		}
	})
	app := quick.New(config.WithTransport(transport), config.WithHTTPServer(testkit.NewRecordingServer()))
	if err := app.Run(ctx); !errors.Is(err, errors.Conflict) {
		t.Fatalf("Run error = %v, want Conflict", err)
	}
	if !transport.Stats().Running {
		t.Fatal("failed application startup stopped another owner's transport")
	}
}

func TestApplicationRejectsRouteConflictsWithoutModules(t *testing.T) {
	server := httpx.WithRouteRegistry(testkit.NewRecordingServer())
	handler := func(httpx.IContext) error { return nil }
	server.GET("/duplicate", handler)
	server.GET("/duplicate", handler)
	app := quick.New(config.WithHTTPServer(server), config.WithFailFastOnRouteConflicts(true))
	if err := app.Run(context.Background()); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("Run error = %v, want InvalidInput for duplicate routes", err)
	}
}

func TestApplicationRejectsAmbiguousMounts(t *testing.T) {
	for _, opt := range []config.Option{
		config.WithModules(quick.Module("other", nil)),
		config.WithModuleHTTP("orders", config.ModuleHTTPConfig{Prefix: "/custom"}),
		config.WithModuleHTTPDefaults(config.ModuleHTTPConfig{Middlewares: []httpx.Middleware{
			func(_ httpx.IContext, next func() error) error { return next() },
		}}),
	} {
		if _, err := quick.New(opt).Build(); !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected ambiguous configuration error, got %v", err)
		}
	}
	for _, app := range []*quick.Application{
		quick.New().Modules(nil),
		quick.New().Mount("", quick.Module("orders", nil)),
	} {
		if _, err := app.Build(); !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected invalid mount, got %v", err)
		}
	}
	app := quick.New()
	if _, err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Build(); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("expected single build, got %v", err)
	}
}

func TestApplicationConstructorFailures(t *testing.T) {
	cause := fmt.Errorf("constructor failed")
	for _, tc := range []struct {
		name  string
		ctors []module.ModuleCtor
		check func(error) bool
	}{
		{"duplicate", []module.ModuleCtor{quick.Module("orders", nil), quick.Module("orders", nil)},
			func(err error) bool { return errors.Is(err, errors.Conflict) }},
		{"nil", []module.ModuleCtor{func() (module.IModule, error) { return nil, nil }},
			func(err error) bool { return errors.Is(err, errors.InvalidInput) }},
		{"typed nil", []module.ModuleCtor{func() (module.IModule, error) { return (*module.BaseModule)(nil), nil }},
			func(err error) bool { return errors.Is(err, errors.InvalidInput) }},
		{"cause", []module.ModuleCtor{func() (module.IModule, error) { return nil, cause }},
			func(err error) bool { return errors.Is(err, cause) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := quick.New(config.WithHTTPServer(testkit.NewRecordingServer())).Modules(tc.ctors...)
			if err := app.Run(context.Background()); err == nil || !tc.check(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestApplicationPreservesModuleDependencies(t *testing.T) {
	var order []string
	route := func(name string) func(httpx.IRouteGroup) error {
		return func(httpx.IRouteGroup) error { order = append(order, name); return nil }
	}
	calls := 0
	child := quick.Module("child", route("child"), quick.WithDependencies("parent"))
	app := quick.New(config.WithHTTPServer(testkit.NewRecordingServer()))
	app.Group("/private").Modules(func() (module.IModule, error) { calls++; return child() })
	app.Modules(quick.Module("parent", route("parent")))
	runner, err := app.Build()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("constructor ran during declaration")
	}
	if err = runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(order, []string{"parent", "child"}) {
		t.Fatalf("calls=%d order=%v", calls, order)
	}
}
