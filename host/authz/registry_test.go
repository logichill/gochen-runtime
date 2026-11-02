package authz

import (
	"testing"

	"gochen/errors"
)

type registryTestFoo struct {
	ID int
}

type registryTestBar struct {
	ID int
}

func TestRegistryRegisterModule_CollectsCatalog(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterModule(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
		Permissions: []string{
			"demo:api:read",
		},
		PermissionDefinitions: []PermissionDefinition{
			{
				Code:        "demo:api:write",
				Name:        "Write Demo",
				Description: "allow write demo data",
				Scopes:      []string{"tenant"},
				BuiltinOnly: true,
				RiskLevel:   "high",
			},
		},
	})
	if err != nil {
		t.Fatalf("RegisterModule failed: %v", err)
	}

	if got := registry.Permissions(); len(got) != 2 || got[0] != "demo:api:read" || got[1] != "demo:api:write" {
		t.Fatalf("unexpected permissions: %#v", got)
	}

	modules := registry.Modules()
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if modules[0].ModuleID != "demo" || modules[0].ModuleName != "Demo" {
		t.Fatalf("unexpected module snapshot: %#v", modules[0])
	}
	if got := findPermissionDefinition(modules[0].PermissionDefinitions, "demo:api:read"); got.Code != "demo:api:read" {
		t.Fatalf("expected bare definition for demo:api:read, got %#v", got)
	}
	if got := findPermissionDefinition(modules[0].PermissionDefinitions, "demo:api:write"); got.Name != "Write Demo" || got.Description != "allow write demo data" || len(got.Scopes) != 1 || got.Scopes[0] != "tenant" || !got.BuiltinOnly || got.RiskLevel != "high" {
		t.Fatalf("unexpected merged definition for demo:api:write: %#v", got)
	}

}

func TestRegistryRegisterModule_RejectsCrossModuleConflicts(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterModule(ModuleRegistration{
		ModuleID:   "demo-a",
		ModuleName: "Demo A",
		Permissions: []string{
			"demo:api:read",
		},
	}); err != nil {
		t.Fatalf("seed RegisterModule failed: %v", err)
	}

	if err := registry.RegisterModule(ModuleRegistration{
		ModuleID:   "demo-b",
		ModuleName: "Demo B",
		Permissions: []string{
			"demo:api:read",
		},
	}); err == nil {
		t.Fatal("expected duplicate permission to fail")
	}

}

func TestRegistryRegisterModule_RejectsInvalidPermissionCode(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterModule(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
		Permissions: []string{
			"demo.read",
		},
	})
	if err == nil {
		t.Fatal("expected invalid permission code to fail")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestRegistryModule_ReturnsClonedSnapshot(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterModule(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
		PermissionDefinitions: []PermissionDefinition{
			{
				Code:   "demo:api:read",
				Scopes: []string{"tenant"},
			},
		},
	}); err != nil {
		t.Fatalf("RegisterModule failed: %v", err)
	}

	module, ok := registry.Module("demo")
	if !ok {
		t.Fatal("expected module snapshot")
	}
	if len(module.PermissionDefinitions) != 1 || module.PermissionDefinitions[0].Code != "demo:api:read" {
		t.Fatalf("unexpected module snapshot: %#v", module)
	}

	module.PermissionDefinitions[0].Code = "mutated"
	module.PermissionDefinitions[0].Scopes[0] = "mutated"
	module.Permissions[0] = "mutated"

	again, ok := registry.Module("demo")
	if !ok {
		t.Fatal("expected module snapshot on second read")
	}
	if again.PermissionDefinitions[0].Code != "demo:api:read" || again.Permissions[0] != "demo:api:read" {
		t.Fatalf("expected registry snapshot to be cloned, got %#v", again)
	}
	if len(again.PermissionDefinitions[0].Scopes) != 1 || again.PermissionDefinitions[0].Scopes[0] != "tenant" {
		t.Fatalf("expected nested scopes to be cloned, got %#v", again)
	}
}

func findPermissionDefinition(definitions []PermissionDefinition, code string) PermissionDefinition {
	for _, definition := range definitions {
		if definition.Code == code {
			return definition
		}
	}
	return PermissionDefinition{}
}
