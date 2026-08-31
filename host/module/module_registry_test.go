package module_test

import (
	"context"
	"slices"
	"testing"

	"gochen-runtime/host/config"
	. "gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
)

type registryTestModule struct {
	*BaseModule
	initFn   func(ModuleInitOptions) error
	features []PublicFeature
}

func (m *registryTestModule) Init(opts ModuleInitOptions) error {
	if m.initFn != nil {
		return m.initFn(opts)
	}
	return nil
}

func (m *registryTestModule) PublicFeatures() []PublicFeature {
	return append([]PublicFeature(nil), m.features...)
}

func TestModuleRegistry_Register_NormalizesAndLists(t *testing.T) {
	registry := NewModuleRegistry()
	if err := registry.Register(ModuleInfo{ID: " demo ", Name: "Demo"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := registry.Register(ModuleInfo{ID: "demo", Name: "Demo"}); err != nil {
		t.Fatalf("duplicate Register should be idempotent: %v", err)
	}

	modules := registry.Modules()
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if modules[0].ID != "demo" || modules[0].Name != "Demo" {
		t.Fatalf("unexpected module snapshot: %#v", modules[0])
	}
}

func TestModuleRegistry_Register_RejectsConflictingName(t *testing.T) {
	registry := NewModuleRegistry()
	if err := registry.Register(ModuleInfo{ID: "demo", Name: "Demo"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := registry.Register(ModuleInfo{ID: "demo", Name: "Other"}); err == nil {
		t.Fatal("expected conflicting name to fail")
	}
}

func TestModuleRegistry_Register_NormalizesAndCopiesFeatures(t *testing.T) {
	registry := NewModuleRegistry()
	features := []PublicFeature{
		{Key: "variant", Value: " standard "},
		{Key: "tenant_mode", Value: " single "},
	}
	if err := registry.Register(ModuleInfo{ID: "iam", Name: "IAM", Features: features}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	features[0].Value = "changed"

	modules := registry.Modules()
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	want := []PublicFeature{{Key: "tenant_mode", Value: "single"}, {Key: "variant", Value: "standard"}}
	if !slices.Equal(modules[0].Features, want) {
		t.Fatalf("unexpected normalized features: %#v", modules[0].Features)
	}

	modules[0].Features[0].Value = "mutated"
	if got := registry.Modules()[0].Features[0].Value; got != "single" {
		t.Fatalf("registry snapshot was mutated through caller: %q", got)
	}
}

func TestModuleRegistry_Register_RejectsInvalidOrConflictingFeatures(t *testing.T) {
	registry := NewModuleRegistry()
	if err := registry.Register(ModuleInfo{ID: "iam", Name: "IAM", Features: []PublicFeature{{Key: "TenantMode", Value: "single"}}}); err == nil {
		t.Fatal("expected invalid feature key to fail")
	}
	if err := registry.Register(ModuleInfo{ID: "iam", Name: "IAM", Features: []PublicFeature{{Key: "tenant_mode", Value: "single"}}}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := registry.Register(ModuleInfo{ID: "iam", Name: "IAM", Features: []PublicFeature{{Key: "tenant_mode", Value: "tenant"}}}); err == nil {
		t.Fatal("expected conflicting feature value to fail")
	}
}

func TestServerModuleRegistry_OnlyContainsExplicitRegistrations(t *testing.T) {
	t.Run("registers module identity from descriptor", func(t *testing.T) {
		registry := NewModuleRegistry()
		srv := runtime.NewHost([]ModuleCtor{
			func() (IModule, error) {
				return &registryTestModule{
					BaseModule: NewBaseModule("demo", "Demo"),
					features:   []PublicFeature{{Key: "mode", Value: "single"}},
				}, nil
			},
		}, config.WithModuleRegistry(registry))

		if err := srv.Prepare(context.Background()); err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}
		modules := registry.Modules()
		if len(modules) != 1 || modules[0].ID != "demo" || modules[0].Name != "Demo" || len(modules[0].Features) != 1 {
			t.Fatalf("unexpected registry snapshot: %#v", modules)
		}
	})
}

// 模块 authz 目录的注册与同步用例已迁移至 runtime/host/authz（可选扩展包）。
