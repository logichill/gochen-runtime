package authz

import (
	"gochen/errors"
	"strings"
	"testing"
)

func TestSyncModuleCatalog_NotifiesAllInstalledHooks(t *testing.T) {
	registry := NewRegistry()
	var firstCalls int
	var secondCalls int

	if err := registry.InstallModuleCatalogSync(func(reg ModuleRegistration) error {
		if reg.ModuleID != "demo" {
			t.Fatalf("unexpected module id: %q", reg.ModuleID)
		}
		firstCalls++
		return nil
	}); err != nil {
		t.Fatalf("InstallModuleCatalogSync first hook failed: %v", err)
	}
	if err := registry.InstallModuleCatalogSync(func(reg ModuleRegistration) error {
		if reg.ModuleName != "Demo" {
			t.Fatalf("unexpected module name: %q", reg.ModuleName)
		}
		secondCalls++
		return nil
	}); err != nil {
		t.Fatalf("InstallModuleCatalogSync second hook failed: %v", err)
	}

	if err := registry.SyncModuleCatalog(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
	}); err != nil {
		t.Fatalf("SyncModuleCatalog failed: %v", err)
	}

	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("expected both hooks to be called once, got first=%d second=%d", firstCalls, secondCalls)
	}
}

func TestInstallModuleCatalogSync_ReplaysExistingCatalog(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SyncModuleCatalog(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
		PermissionDefinitions: []PermissionDefinition{
			{
				Code:   "demo.read",
				Scopes: []string{"tenant"},
			},
		},
	}); err != nil {
		t.Fatalf("SyncModuleCatalog failed: %v", err)
	}

	var replayed ModuleRegistration
	if err := registry.InstallModuleCatalogSync(func(reg ModuleRegistration) error {
		replayed = reg
		return nil
	}); err != nil {
		t.Fatalf("InstallModuleCatalogSync replay hook failed: %v", err)
	}

	if replayed.ModuleID != "demo" || replayed.ModuleName != "Demo" {
		t.Fatalf("unexpected replayed module: %#v", replayed)
	}
	if len(replayed.PermissionDefinitions) != 1 || replayed.PermissionDefinitions[0].Code != "demo.read" {
		t.Fatalf("unexpected replayed definitions: %#v", replayed.PermissionDefinitions)
	}
	if len(replayed.PermissionDefinitions[0].Scopes) != 1 || replayed.PermissionDefinitions[0].Scopes[0] != "tenant" {
		t.Fatalf("unexpected replayed scopes: %#v", replayed.PermissionDefinitions)
	}
}

func TestInstallModuleCatalogSync_ReportsReplayErrors(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SyncModuleCatalog(ModuleRegistration{ModuleID: "demo", ModuleName: "Demo"}); err != nil {
		t.Fatalf("SyncModuleCatalog failed: %v", err)
	}

	expected := errors.New("replay failed")
	err := registry.InstallModuleCatalogSync(func(ModuleRegistration) error {
		return expected
	})
	if err == nil {
		t.Fatal("expected replay error")
	}
	if !strings.Contains(err.Error(), expected.Error()) {
		t.Fatalf("unexpected replay error: %v", err)
	}
}

func TestInstallModuleCatalogSync_AllowsMultipleClosureHooksFromSameFactory(t *testing.T) {
	registry := NewRegistry()
	received := map[string]int{}
	newHook := func(name string) ModuleCatalogSyncFunc {
		return func(reg ModuleRegistration) error {
			if reg.ModuleID != "demo" {
				t.Fatalf("unexpected module id: %q", reg.ModuleID)
			}
			received[name]++
			return nil
		}
	}

	if err := registry.InstallModuleCatalogSync(newHook("first")); err != nil {
		t.Fatalf("InstallModuleCatalogSync first hook failed: %v", err)
	}
	if err := registry.InstallModuleCatalogSync(newHook("second")); err != nil {
		t.Fatalf("InstallModuleCatalogSync second hook failed: %v", err)
	}

	if err := registry.SyncModuleCatalog(ModuleRegistration{
		ModuleID:   "demo",
		ModuleName: "Demo",
	}); err != nil {
		t.Fatalf("SyncModuleCatalog failed: %v", err)
	}

	if received["first"] != 1 || received["second"] != 1 {
		t.Fatalf("expected both closure hooks to be called once, got %#v", received)
	}
}

func TestInstallModuleCatalogSync_SkipsStaleReplayAfterConcurrentSync(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SyncModuleCatalog(ModuleRegistration{
		ModuleID:   "alpha",
		ModuleName: "Alpha",
		PermissionDefinitions: []PermissionDefinition{
			{Code: "alpha.read"},
		},
	}); err != nil {
		t.Fatalf("SyncModuleCatalog alpha failed: %v", err)
	}
	if err := registry.SyncModuleCatalog(ModuleRegistration{
		ModuleID:   "beta",
		ModuleName: "Beta",
		PermissionDefinitions: []PermissionDefinition{
			{Code: "beta.read"},
		},
	}); err != nil {
		t.Fatalf("SyncModuleCatalog beta failed: %v", err)
	}

	var received []string
	triggered := false
	if err := registry.InstallModuleCatalogSync(func(reg ModuleRegistration) error {
		if len(reg.PermissionDefinitions) == 0 {
			t.Fatalf("unexpected empty definitions: %#v", reg)
		}
		received = append(received, reg.PermissionDefinitions[0].Code)
		if reg.ModuleID == "alpha" && !triggered {
			triggered = true
			return registry.SyncModuleCatalog(ModuleRegistration{
				ModuleID:   "beta",
				ModuleName: "Beta",
				PermissionDefinitions: []PermissionDefinition{
					{Code: "beta.write"},
				},
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("InstallModuleCatalogSync failed: %v", err)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 deliveries, got %d: %#v", len(received), received)
	}
	if received[0] != "alpha.read" {
		t.Fatalf("unexpected first delivery: %#v", received)
	}
	if received[1] != "beta.write" {
		t.Fatalf("stale replay was not skipped: %#v", received)
	}
}
