package host

import (
	"testing"

	"gochen-runtime/host/module"
	"gochen/app"
	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

type testModuleBinding struct {
	contribution module.Binding
	err          error
}

func (b *testModuleBinding) ModuleBinding() (module.Binding, error) { return b.contribution, b.err }

type testBindingApplication struct{}

func (*testBindingApplication) Descriptor() auth.Descriptor {
	return auth.Descriptor{Resource: "orders", Actions: []auth.Action{{Name: "approve", Kind: auth.KindUpdate}}}
}

func TestModuleBindingExpandsExplicitRolesWithoutRunningThem(t *testing.T) {
	application := &testBindingApplication{}
	calls := 0
	registrar := module.RouteRegistrarFunc(func(httpx.IRouteGroup) error { calls++; return nil })
	binding := &testModuleBinding{contribution: module.Binding{Applications: []app.IDescriptorProvider{application}, RouteRegistrars: []module.IRouteRegistrar{registrar}}}
	builder := Module("orders").Binding(binding)
	first, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build()
	if err != nil || calls != 0 {
		t.Fatalf("Build executed routes or mutated builder: calls=%d err=%v", calls, err)
	}
	binding.contribution.Applications[0] = nil
	binding.contribution.RouteRegistrars[0] = nil
	for _, built := range []IModule{first, second} {
		applications := built.(module.IApplicationProvider).Applications()
		if len(applications) != 1 || applications[0] != application {
			t.Fatalf("binding lost exact application identity: %+v", applications)
		}
	}
}

func TestModuleBindingRejectsNilAndConflictingContributions(t *testing.T) {
	var nilBinding *testModuleBinding
	route := &testRouteRegistrar{}
	cases := map[string]*Builder{
		"nil binding":       Module("orders").Binding(nil),
		"typed nil binding": Module("orders").Binding(nilBinding),
		"binding error":     Module("orders").Binding(&testModuleBinding{err: errors.NewCode(errors.InvalidInput, "invalid binding")}),
		"nil application":   Module("orders").Binding(&testModuleBinding{contribution: module.Binding{Applications: []app.IDescriptorProvider{nil}}}),
		"nil registrar":     Module("orders").Binding(&testModuleBinding{contribution: module.Binding{RouteRegistrars: []module.IRouteRegistrar{nil}}}),
		"duplicate role":    Module("orders").RouteRegistrar(route).Binding(&testModuleBinding{contribution: module.Binding{RouteRegistrars: []module.IRouteRegistrar{route}}}),
	}
	for name, builder := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := builder.Build(); err == nil {
				t.Fatal("invalid contribution must fail Build")
			}
		})
	}
}
