package authz_test

import (
	"context"
	"testing"

	"gochen-runtime/host"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/capability"
	"gochen/testkit/require"
)

type catalogTarget struct{}

func buildModule(t *testing.T, id string, exts ...any) host.IModule {
	t.Helper()
	builder := host.Module(id).Name(id)
	if len(exts) > 0 {
		builder = builder.Extension(exts...)
	}
	m, err := builder.Build()
	require.NoError(t, err)
	return m
}

func entriesFor(modules ...host.IModule) []capability.ModuleCatalogEntry {
	entries := make([]capability.ModuleCatalogEntry, 0, len(modules))
	for _, m := range modules {
		entries = append(entries, capability.ModuleCatalogEntry{ID: m.ID(), Module: m})
	}
	return entries
}

func TestCatalogRegistrarRegistersDeclaredPermissions(t *testing.T) {
	registry := authz.NewRegistry()
	registrar := authz.NewCatalogRegistrar(registry)
	require.NotNil(t, registrar)

	m := buildModule(t, "demo", authz.Catalog{
		Permissions: []string{"demo:api:read"},
		PermissionDefinitions: []authz.PermissionDefinition{{
			Code:        "demo:api:write",
			Name:        "Write Demo",
			Description: "allow write demo data",
			Scopes:      []string{"tenant"},
		}},
	})

	require.NoError(t, registrar.RegisterModuleCatalog(context.Background(), entriesFor(m)))

	reg, ok := registry.Module("demo")
	require.True(t, ok)
	require.Equal(t, "demo", reg.ModuleID)

	// 裸权限码与带元数据的定义都必须进入目录。
	require.Equal(t, 2, len(reg.PermissionDefinitions))
	require.Equal(t, "demo:api:read", definitionFor(reg.PermissionDefinitions, "demo:api:read").Code)

	write := definitionFor(reg.PermissionDefinitions, "demo:api:write")
	require.Equal(t, "Write Demo", write.Name)
	require.Equal(t, "allow write demo data", write.Description)
	require.Equal(t, 1, len(write.Scopes))
	require.Equal(t, "tenant", write.Scopes[0])

}

// 未声明目录的模块不得产生空注册项。
func TestCatalogRegistrarSkipsModulesWithoutCatalog(t *testing.T) {
	registry := authz.NewRegistry()
	registrar := authz.NewCatalogRegistrar(registry)

	m := buildModule(t, "plain")
	require.NoError(t, registrar.RegisterModuleCatalog(context.Background(), entriesFor(m)))

	_, ok := registry.Module("plain")
	require.False(t, ok)
	require.Equal(t, 0, len(registry.Modules()))
}

// 同一模块的多个 Catalog 扩展载荷必须合并，而不是相互覆盖。
func TestCatalogRegistrarMergesMultipleCatalogs(t *testing.T) {
	registry := authz.NewRegistry()
	registrar := authz.NewCatalogRegistrar(registry)

	m := buildModule(t, "multi",
		authz.Catalog{Permissions: []string{"multi:api:read"}},
		authz.Catalog{Permissions: []string{"multi:api:write"}},
	)
	require.NoError(t, registrar.RegisterModuleCatalog(context.Background(), entriesFor(m)))

	reg, ok := registry.Module("multi")
	require.True(t, ok)
	require.Equal(t, 2, len(reg.Permissions))
}

// registry 为 nil 时不得返回一个会静默吞掉目录的空壳 registrar。
func TestNewCatalogRegistrarRejectsNilRegistry(t *testing.T) {
	require.Nil(t, authz.NewCatalogRegistrar(nil))
}

func TestCatalogIsEmpty(t *testing.T) {
	require.True(t, authz.Catalog{}.IsEmpty())
	require.False(t, authz.Catalog{Permissions: []string{"demo:api:read"}}.IsEmpty())
}

func definitionFor(definitions []authz.PermissionDefinition, code string) authz.PermissionDefinition {
	for _, definition := range definitions {
		if definition.Code == code {
			return definition
		}
	}
	return authz.PermissionDefinition{}
}
