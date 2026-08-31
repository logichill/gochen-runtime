package module_test

import (
	"context"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/config"
	. "gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"reflect"
	"testing"

	"gochen-runtime/di"
)

type depModule struct {
	*BaseModule
	deps      []string
	initOrder *[]string
}

func (m *depModule) DependsOn() []string { return m.deps }

func (m *depModule) Init(_ ModuleInitOptions) error {
	if m.initOrder != nil {
		*m.initOrder = append(*m.initOrder, m.ID())
	}
	return nil
}

func TestHost_ModuleDependencies_ToposortAffectsInitOrder(t *testing.T) {
	var order []string

	a := &depModule{BaseModule: &BaseModule{ModuleID: "a", ModuleName: "A"}, initOrder: &order}
	b := &depModule{BaseModule: &BaseModule{ModuleID: "b", ModuleName: "B"}, deps: []string{"a"}, initOrder: &order}
	c := &depModule{BaseModule: &BaseModule{ModuleID: "c", ModuleName: "C"}, deps: []string{"b"}, initOrder: &order}

	srv := runtime.NewHost([]ModuleCtor{
		func() (IModule, error) { return c, nil },
		func() (IModule, error) { return b, nil },
		func() (IModule, error) { return a, nil },
	})

	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	want := []string{"a", "b", "c"}
	if len(order) != len(want) {
		t.Fatalf("unexpected init order length: want=%v got=%v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("unexpected init order at %d: want=%v got=%v", i, want, order)
		}
	}
}

func TestHost_ModuleDependencies_UnknownDependencyFailsFast(t *testing.T) {
	a := &depModule{BaseModule: &BaseModule{ModuleID: "a", ModuleName: "A"}, deps: []string{"missing"}}

	srv := runtime.NewHost([]ModuleCtor{
		func() (IModule, error) { return a, nil },
	})

	if err := srv.Prepare(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestHost_ModuleDependencies_CycleFailsFast(t *testing.T) {
	a := &depModule{BaseModule: &BaseModule{ModuleID: "a", ModuleName: "A"}, deps: []string{"b"}}
	b := &depModule{BaseModule: &BaseModule{ModuleID: "b", ModuleName: "B"}, deps: []string{"a"}}

	srv := runtime.NewHost([]ModuleCtor{
		func() (IModule, error) { return a, nil },
		func() (IModule, error) { return b, nil },
	})

	if err := srv.Prepare(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestHost_StartBackground_FailsFastOnContainerCircularDependencies(t *testing.T) {
	container := basic.New()
	type hostCycleA struct{}
	type hostCycleB struct{}

	if err := container.RegisterConstructor(di.NewConstructor(func(b *hostCycleB) *hostCycleA {
		return &hostCycleA{}
	})); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := container.RegisterConstructor(di.NewConstructor(func(a *hostCycleA) *hostCycleB {
		return &hostCycleB{}
	})); err != nil {
		t.Fatalf("register B: %v", err)
	}
	if _, err := container.Resolve(reflect.TypeOf((*hostCycleA)(nil))); err == nil {
		// ensure Diagnose has a concrete cycle independent from runtime start order
		t.Fatalf("expected direct resolve to fail on circular dependency")
	}

	srv := runtime.NewHost(nil, config.WithContainer(container))
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := srv.StartBackground(context.Background()); err == nil {
		t.Fatalf("expected StartBackground to fail on circular dependencies")
	}
}
