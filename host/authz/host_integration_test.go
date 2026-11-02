package authz_test

import (
	"context"
	"testing"

	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/assembly"
	"gochen-runtime/host/module/runtime"
	"gochen/testkit/require"
)

type registryTestAuthzTarget struct{}

// newHostWithCatalog 装配一个启用了 authz 目录扩展的 Host。
func newHostWithCatalog(registry authz.IAuthzRegistry, desc assembly.ModuleDescriptor) *runtime.Host {
	return runtime.NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return assembly.NewModule(desc), nil
		},
	}, config.WithCatalogRegistrar(authz.NewCatalogRegistrar(registry)))
}

func TestHostRegistersModuleAuthzFromDescriptor(t *testing.T) {
	registry := authz.NewRegistry()
	srv := newHostWithCatalog(registry, assembly.ModuleDescriptor{
		ID:   "demo",
		Name: "Demo",
		Extensions: []any{authz.Catalog{
			PermissionDefinitions: []authz.PermissionDefinition{{
				Code:        "demo:api:read",
				Name:        "Read Demo",
				Description: "allow read demo data",
				Scopes:      []string{"tenant"},
			}},
		}},
	})

	require.NoError(t, srv.Prepare(context.Background()))

	modules := registry.Modules()
	require.Equal(t, 1, len(modules))
	require.Equal(t, "demo", modules[0].ModuleID)

	got := definitionFor(modules[0].PermissionDefinitions, "demo:api:read")
	require.Equal(t, "Read Demo", got.Name)
	require.Equal(t, "allow read demo data", got.Description)
	require.Equal(t, 1, len(got.Scopes))
	require.Equal(t, "tenant", got.Scopes[0])

	permissions := registry.Permissions()
	require.Equal(t, 1, len(permissions))
	require.Equal(t, "demo:api:read", permissions[0])

}

func TestHostSyncsModuleAuthzCatalogAfterRegistration(t *testing.T) {
	registry := authz.NewRegistry()
	var synced authz.ModuleRegistration
	require.NoError(t, registry.InstallModuleCatalogSync(func(reg authz.ModuleRegistration) error {
		synced = reg
		return nil
	}))

	srv := newHostWithCatalog(registry, assembly.ModuleDescriptor{
		ID:   "demo",
		Name: "Demo",
		Extensions: []any{authz.Catalog{
			PermissionDefinitions: []authz.PermissionDefinition{
				{Code: "demo:api:read", Name: "Read Demo", Description: "allow read demo data"},
				{Code: "demo:api:write", Name: "Write Demo", Description: "allow write demo data", Scopes: []string{"tenant"}},
			},
		}},
	})

	require.NoError(t, srv.Prepare(context.Background()))
	require.Equal(t, "demo", synced.ModuleID)
	require.Equal(t, "Demo", synced.ModuleName)

	read := definitionFor(synced.PermissionDefinitions, "demo:api:read")
	require.Equal(t, "Read Demo", read.Name)
	require.Equal(t, "allow read demo data", read.Description)

	write := definitionFor(synced.PermissionDefinitions, "demo:api:write")
	require.Equal(t, "Write Demo", write.Name)
	require.Equal(t, "allow write demo data", write.Description)
	require.Equal(t, 1, len(write.Scopes))
	require.Equal(t, "tenant", write.Scopes[0])
}

func TestHostSyncsNormalizedModuleAuthzCatalog(t *testing.T) {
	registry := authz.NewRegistry()
	var synced authz.ModuleRegistration
	require.NoError(t, registry.InstallModuleCatalogSync(func(reg authz.ModuleRegistration) error {
		synced = reg
		return nil
	}))

	srv := newHostWithCatalog(registry, assembly.ModuleDescriptor{
		ID:         "/Demo",
		Name:       "Demo",
		Extensions: []any{authz.Catalog{Permissions: []string{"demo:api:read"}}},
	})

	require.NoError(t, srv.Prepare(context.Background()))

	modules := registry.Modules()
	require.Equal(t, 1, len(modules))
	require.Equal(t, "demo", modules[0].ModuleID, "模块 ID 必须已规范化")
	require.Equal(t, "demo", synced.ModuleID)
	require.Equal(t, "Demo", synced.ModuleName)
	require.Equal(t, 1, len(synced.PermissionDefinitions))
	require.Equal(t, "demo:api:read", synced.PermissionDefinitions[0].Code)
}

// L0 回归：未注册 CatalogRegistrar 时 Host 不做任何目录登记，且能正常装配。
func TestHostWithoutCatalogRegistrarSkipsAuthz(t *testing.T) {
	registry := authz.NewRegistry()
	srv := runtime.NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return assembly.NewModule(assembly.ModuleDescriptor{
				ID:         "demo",
				Name:       "Demo",
				Extensions: []any{authz.Catalog{Permissions: []string{"demo:api:read"}}},
			}), nil
		},
	})

	require.NoError(t, srv.Prepare(context.Background()))
	require.Equal(t, 0, len(registry.Modules()))
}
