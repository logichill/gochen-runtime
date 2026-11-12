package http_test

import (
	"context"
	"testing"

	hostauthz "gochen-runtime/host/authz"
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

type entryEntity struct{ domcrud.Entity[int64] }

func TestDeclaredHTTPRoutesBindConcreteExecutionAndRejectMissingActions(t *testing.T) {
	repo, err := testkit.NewMemoryRepository[*entryEntity, int64](testkit.NewInt64Sequence(1))
	if err != nil {
		t.Fatal(err)
	}
	application, err := quick.DeclaredApp(repo, appcrud.Options[*entryEntity, int64]{Resource: "entry"}, nil, auth.CompileOptions{Checker: auth.ActionCheckerFunc(func(context.Context, string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	server := nethttp.NewServer(nil)
	registry := hostauthz.NewEntryRegistry()
	group, err := runtimehttp.WithEntryRegistry(server.Group("/api"), registry, "entries")
	if err != nil {
		t.Fatal(err)
	}
	group = group.Group("/items").Use(func(_ httpx.IContext, next func() error) error { return next() })
	handler := func(c httpx.IContext) error {
		_, err := application.Get(c.RequestContext(), 1)
		return err
	}
	if err := runtimehttp.RegisterDeclaredRoute(group, "GET", "/:id", handler, application, "missing"); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("missing action=%v", err)
	}
	if len(server.Routes()) != 0 {
		t.Fatal("invalid action was registered")
	}
	if err := runtimehttp.RegisterDeclaredRoute(group, "GET", "/:id", handler, application, "read"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateEntries(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	for _, entry := range registry.Report() {
		if entry.Class == auth.EntryDeclared && (entry.Module != "entries" || entry.Action != "read" || entry.Plan.Descriptor.Resource != "entry") {
			t.Fatalf("execution binding=%+v", entry)
		}
	}
}

func TestAuthorizationHTTPAssemblyRejectsTypedNilDependencies(t *testing.T) {
	server := nethttp.NewServer(nil)
	var registry *hostauthz.EntryRegistry
	if _, err := runtimehttp.WithEntryRegistry(server.Group("/"), registry, "entries"); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("typed nil registry=%v", err)
	}
	var application *appcrud.Application[*entryEntity, int64]
	if err := runtimehttp.RegisterDeclaredRoute(server.Group("/"), "GET", "/", func(httpx.IContext) error { return nil }, application, "read"); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("typed nil execution=%v", err)
	}
}
