package quick_test

import (
	"context"
	"testing"

	"gochen-runtime/api/rest"
	"gochen-runtime/host"
	hostauthz "gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	hostruntime "gochen-runtime/host/module/runtime"
	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/nethttp"
	"gochen-runtime/quick"
	appcrud "gochen/app/crud"
	"gochen/auth"
	domcrud "gochen/domain/crud"
	"gochen/errors"
	"gochen/httpx"
	"gochen/testkit"
)

type entryEntity struct {
	domcrud.Entity[int64]
	Name string
}

func TestDeclaredRESTEntriesIncludeGeneratedRoutesAndFreezeRegistration(t *testing.T) {
	repo, err := testkit.NewMemoryRepository[*entryEntity, int64](testkit.NewInt64Sequence(1))
	if err != nil {
		t.Fatal(err)
	}
	app, err := quick.DeclaredApp(repo, appcrud.Options[*entryEntity, int64]{Resource: "entry"}, nil, auth.CompileOptions{Checker: auth.ActionCheckerFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	server := nethttp.NewServer(nil)
	registry := hostauthz.NewEntryRegistry()
	group, err := runtimehttp.WithEntryRegistry(server.Group("/api"), registry, "entries")
	if err != nil {
		t.Fatal(err)
	}
	if err := rest.Mount(group.Group("/entries"), app); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateEntries(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	report := registry.Report()
	if len(report) != len(server.Routes()) || len(report) < 10 {
		t.Fatalf("report=%d actual=%d", len(report), len(server.Routes()))
	}
	head, options := false, false
	for _, entry := range report {
		if entry.Class == auth.EntryInvalid {
			t.Fatalf("invalid entry=%+v", entry)
		}
		if entry.Entry.Key == "HEAD /api/entries/{id}" && entry.Class == auth.EntryDeclared {
			head = true
		}
		if entry.Entry.Key == "OPTIONS /api/entries/{id}" && entry.Class == auth.EntryAllowed {
			options = true
		}
	}
	if !head || !options {
		t.Fatalf("missing generated HEAD/OPTIONS in report: %+v", report)
	}
	server.POST("/unbound", func(httpx.IContext) error { return nil })
	if !errors.Is(server.RegistrationError(), errors.FailedPrecondition) {
		t.Fatalf("late registration=%v", server.RegistrationError())
	}
	for _, route := range server.Routes() {
		if route.Path == "/unbound" {
			t.Fatal("new route enabled after validation")
		}
	}
}

type inheritedEntryRegistrar struct {
	app *appcrud.Application[*entryEntity, int64]
}

func (r *inheritedEntryRegistrar) RegisterRoutes(group httpx.IRouteGroup) error {
	return rest.Mount(group.Group("/items"), r.app)
}

func TestHostEntryRegistryReachesModuleRESTRegistrars(t *testing.T) {
	repo, err := testkit.NewMemoryRepository[*entryEntity, int64](testkit.NewInt64Sequence(1))
	if err != nil {
		t.Fatal(err)
	}
	app, err := quick.DeclaredApp(repo, appcrud.Options[*entryEntity, int64]{Resource: "entry"}, nil, auth.CompileOptions{Checker: auth.ActionCheckerFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	server := nethttp.NewServer(nil)
	registry := hostauthz.NewEntryRegistry()
	h := hostruntime.NewHost([]module.ModuleCtor{func() (module.IModule, error) {
		return host.Module("entry").Application(app).RouteRegistrar(&inheritedEntryRegistrar{app: app}).Build()
	}}, config.WithHTTPServer(server), config.WithDisableHealthRoute(true), config.WithEntryValidator(registry))
	if err := h.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.StartBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(registry.Report()) != len(server.Routes()) || len(registry.Report()) == 0 {
		t.Fatalf("report=%d routes=%d", len(registry.Report()), len(server.Routes()))
	}
	for _, entry := range registry.Report() {
		if entry.Class == auth.EntryInvalid || entry.Class == auth.EntryDeclared && entry.Module != "entry" {
			t.Fatalf("module source or execution binding missing: %+v", entry)
		}
	}
}

type entryModule struct{ started *bool }

func (*entryModule) ID() string                          { return "entry-test" }
func (*entryModule) Name() string                        { return "entry-test" }
func (*entryModule) Init(module.ModuleInitOptions) error { return nil }
func (m *entryModule) Start(context.Context) (module.ModuleStopFunc, error) {
	*m.started = true
	return nil, nil
}

func TestEntryGateBlocksPreRegisteredMissingRouteBeforeModuleStart(t *testing.T) {
	server := nethttp.NewServer(nil)
	server.GET("/pre-registered", func(httpx.IContext) error { return nil })
	registry := hostauthz.NewEntryRegistry()
	started := false
	host := hostruntime.NewHost([]module.ModuleCtor{func() (module.IModule, error) { return &entryModule{started: &started}, nil }}, config.WithHTTPServer(server), config.WithDisableHealthRoute(true), config.WithEntryValidator(registry))
	if err := host.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(t.Context()); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("gate=%v", err)
	}
	if started {
		t.Fatal("module side effects started before entry validation")
	}
	missing := 0
	for _, entry := range registry.Report() {
		if entry.Class == auth.EntryInvalid {
			missing++
		}
	}
	if missing != 2 {
		t.Fatalf("missing=%d report=%+v", missing, registry.Report())
	}
	if err := registry.BindHTTP("GET", "/pre-registered", auth.EntryBinding{ResourceAccess: auth.ResourceAccessNone, Source: "public status handler", AllowReason: "liveness"}); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("validated module did not start")
	}
}
