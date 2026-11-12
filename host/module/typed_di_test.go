package module_test

import (
	"context"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/internal/assembly"
	. "gochen-runtime/host/module"
	"gochen-runtime/host/module/runtimecap"
	"reflect"
	"testing"

	"gochen-runtime/di"
)

type typedProtocolTestContainer struct {
	inner          di.IContainer
	forbiddenCalls []string
}

func newTypedProtocolTestContainer() *typedProtocolTestContainer {
	return &typedProtocolTestContainer{inner: basic.New()}
}

func (c *typedProtocolTestContainer) recordForbiddenCall(name string) {
	c.forbiddenCalls = append(c.forbiddenCalls, name)
}

func (c *typedProtocolTestContainer) RegisterSingleton(serviceType reflect.Type, factory di.Factory) error {
	return c.inner.RegisterSingleton(serviceType, factory)
}

func (c *typedProtocolTestContainer) RegisterTransient(serviceType reflect.Type, factory di.Factory) error {
	return c.inner.RegisterTransient(serviceType, factory)
}

func (c *typedProtocolTestContainer) RegisterInstance(serviceType reflect.Type, instance di.Instance) error {
	return c.inner.RegisterInstance(serviceType, instance)
}

func (c *typedProtocolTestContainer) RegisterConstructor(constructor di.Constructor) error {
	c.recordForbiddenCall("RegisterConstructor")
	return c.inner.RegisterConstructor(constructor)
}

func (c *typedProtocolTestContainer) Resolve(serviceType reflect.Type) (any, error) {
	return c.inner.Resolve(serviceType)
}

func (c *typedProtocolTestContainer) IsRegistered(serviceType reflect.Type) bool {
	return c.inner.IsRegistered(serviceType)
}

func (c *typedProtocolTestContainer) ListRegisteredTypes() map[string]reflect.Type {
	return c.inner.ListRegisteredTypes()
}

func (c *typedProtocolTestContainer) Invoke(invocation di.Invocation) error {
	return c.inner.Invoke(invocation)
}

func (c *typedProtocolTestContainer) Clear() {
	c.inner.Clear()
}

func TestExplicitModule_UsesTypedDIProtocolAcrossInitAndRoutes(t *testing.T) {
	container := newTypedProtocolTestContainer()
	routeGroup := newMockRouteGroup()

	module := assembly.NewModule(assembly.ModuleDescriptor{
		ID:   "typed-di",
		Name: "Typed DI",
		Registrations: []assembly.Registration{
			{
				Lifetime:    SingletonLifetime,
				ServiceType: reflect.TypeOf((*testService)(nil)),
				Factory:     di.NewFactory(NewTestService),
			},
			{
				Lifetime:    SingletonLifetime,
				ServiceType: reflect.TypeOf((*IRouteRegistrar)(nil)).Elem(),
				Factory:     di.NewFactory(NewTestRouteRegistrar),
				Roles:       []assembly.Role{assembly.RoleRouteRegistrar},
			},
		},
	})

	opts := runtimecap.WithHTTP(
		NewModuleInitOptions(container, container, container),
		runtimecap.NewModuleHTTPOptions(routeGroup, ""),
	)

	if err := module.Init(opts); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	routeModule, ok := module.(IRouteModule)
	if !ok {
		t.Fatal("module does not implement IRouteModule")
	}
	if err := routeModule.RegisterRoutes(context.Background()); err != nil {
		t.Fatalf("RegisterRoutes failed: %v", err)
	}

	stop, err := module.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if stop == nil {
		t.Fatal("expected stop function")
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if len(container.forbiddenCalls) > 0 {
		t.Fatalf("expected typed DI protocol only, got forbidden calls: %v", container.forbiddenCalls)
	}
	if len(routeGroup.SubGroups) == 0 {
		t.Fatal("expected route subgroup to be created")
	}
}
