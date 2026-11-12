package runtime

import (
	"context"
	"strings"
	"testing"

	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen/app"
	"gochen/auth"
)

type applicationModule struct {
	*module.BaseModule
	applications []app.IDescriptorProvider
	registry     module.IApplicationRegistry
}

func (m *applicationModule) Applications() []app.IDescriptorProvider { return m.applications }
func (m *applicationModule) Init(opts module.ModuleInitOptions) error {
	m.registry = module.ApplicationRegistryFrom(opts)
	return nil
}

type declaredApplication struct{ descriptor auth.Descriptor }

func (a *declaredApplication) Descriptor() auth.Descriptor { return a.descriptor }
func newDeclaredApplication(permission string) *declaredApplication {
	return &declaredApplication{auth.Descriptor{Resource: "job", Actions: []auth.Action{{Name: "run", Kind: auth.KindCreate, Permission: permission}}}}
}
func applicationCtor(m *applicationModule) module.ModuleCtor {
	return func() (module.IModule, error) { return m, nil }
}

func TestPrepareRegistersNonHTTPApplications(t *testing.T) {
	application := newDeclaredApplication("job:run")
	m := &applicationModule{BaseModule: module.NewBaseModule("worker", "worker"), applications: []app.IDescriptorProvider{application}}
	h := NewHost([]module.ModuleCtor{applicationCtor(m)}, config.WithModuleHTTPDefaults(config.ModuleHTTPConfig{Disabled: true}))
	if err := h.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.registry == nil {
		t.Fatal("application registry missing from module initialization")
	}
	actions := h.applicationRegistry.Actions("job")
	if len(actions) != 1 || actions["run"].Permission != "job:run" {
		t.Fatalf("unexpected application catalog: %v", actions)
	}
	other := NewHost(nil)
	if err := other.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(other.applicationRegistry.Resources()) != 0 {
		t.Fatal("application catalog leaked across hosts")
	}
}

func TestPrepareRejectsCrossModuleApplicationConflict(t *testing.T) {
	first := &applicationModule{BaseModule: module.NewBaseModule("first", "first"), applications: []app.IDescriptorProvider{newDeclaredApplication("job:one")}}
	second := &applicationModule{BaseModule: module.NewBaseModule("second", "second"), applications: []app.IDescriptorProvider{newDeclaredApplication("job:two")}}
	h := NewHost([]module.ModuleCtor{applicationCtor(first), applicationCtor(second)})
	err := h.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "module applications") {
		t.Fatalf("expected contextual application conflict, got %v", err)
	}
	if h.backgroundStarted.Load() {
		t.Fatal("background activated before application validation")
	}
}
