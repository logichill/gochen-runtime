package quick_test

import (
	"context"
	"testing"

	hostauthz "gochen-runtime/host/authz"
	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/nethttp"
	"gochen-runtime/quick"
	appcrud "gochen/app/crud"
	"gochen/auth"
	"gochen/testkit"
)

func TestReadRESTRegistrarPreservesAuthorizationEntries(t *testing.T) {
	repository, err := testkit.NewMemoryRepository[*entryEntity, int64](nil)
	if err != nil {
		t.Fatal(err)
	}
	options := appcrud.Options[*entryEntity, int64]{Resource: "entry"}
	descriptor, err := appcrud.Describe(options, false)
	if err != nil {
		t.Fatal(err)
	}
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData}, auth.CompileOptions{
		Metadata: repository.ExecutionDescriptor().Metadata(),
		Checker:  auth.ActionCheckerFunc(func(context.Context, string) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := appcrud.NewReadApplication(repository, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, useRegistrar := range []bool{false, true} {
		server := nethttp.NewServer(nil)
		registry := hostauthz.NewEntryRegistry()
		group, err := runtimehttp.WithEntryRegistry(server.Group("/items"), registry, "readonly")
		if err != nil {
			t.Fatal(err)
		}
		if useRegistrar {
			err = quick.RESTRegistrar(application)(group)
		} else {
			err = quick.RESTRegister(group, application)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.ValidateEntries(t.Context(), server); err != nil {
			t.Fatal(err)
		}
		declared := 0
		for _, entry := range registry.Report() {
			if entry.Class == auth.EntryInvalid {
				t.Fatalf("invalid entry: %+v", entry)
			}
			if entry.Class == auth.EntryDeclared {
				declared++
				if entry.Module != "readonly" {
					t.Fatalf("lost module binding: %+v", entry)
				}
			}
		}
		if declared != 4 {
			t.Fatalf("declared GET/HEAD routes = %d", declared)
		}
		for _, route := range server.Routes() {
			if route.Method != "GET" && route.Method != "HEAD" && route.Method != "OPTIONS" {
				t.Fatalf("read application exposed write route: %+v", route)
			}
		}
	}
}
