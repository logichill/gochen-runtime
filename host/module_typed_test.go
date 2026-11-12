package host

import (
	"context"
	"testing"

	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen-runtime/host/module/runtimecap"
	"gochen/httpx"
)

func TestTypedModuleRejectsNilAndDuplicateRoles(t *testing.T) {
	var route *testRouteRegistrar
	var handler *testEventHandler
	var component *testRuntimeComponent
	var fn module.RouteRegistrarFunc
	tests := map[string]*Builder{
		"route":          Module("test").RouteRegistrar(route),
		"function":       Module("test").RouteRegistrar(fn),
		"handler":        Module("test").EventHandler(handler),
		"projection":     Module("test").Projection(module.ProjectionRegistration{}),
		"nil projection": Module("test").Projection(module.ProjectionOf[int64]((*testProjection1)(nil))),
		"component":      Module("test").RuntimeComponent(component),
	}
	instance := NewTestRuntimeComponent(nil)
	tests["duplicate"] = Module("test").RuntimeComponent(instance, instance)
	for name, builder := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := builder.Build(); err == nil {
				t.Fatal("expected invalid declaration")
			}
		})
	}
}

func TestTypedModuleFreezesDeclarationCollections(t *testing.T) {
	deps := []string{"catalog"}
	features := []module.PublicFeature{{Key: "export", Value: "enabled"}}
	builder := Module("orders").DependsOn(deps...).PublicFeatures(features...)
	mod := mustBuildHostModule(t, builder)
	deps[0] = "changed"
	features[0].Value = "changed"
	builder.DependsOn("later").PublicFeatures(module.PublicFeature{Key: "later", Value: "enabled"})
	actualDeps := mod.(module.IModuleDependencyProvider).DependsOn()
	actualFeatures := mod.(module.IPublicFeatureProvider).PublicFeatures()
	if len(actualDeps) != 1 || actualDeps[0] != "catalog" || len(actualFeatures) != 1 || actualFeatures[0].Value != "enabled" {
		t.Fatalf("declarations changed: %v %v", actualDeps, actualFeatures)
	}
	actualDeps[0] = "mutated"
	actualFeatures[0].Value = "mutated"
	if mod.(module.IModuleDependencyProvider).DependsOn()[0] != "catalog" || mod.(module.IPublicFeatureProvider).PublicFeatures()[0].Value != "enabled" {
		t.Fatal("returned declaration aliases internal slice")
	}
}

func TestTypedModuleSameTypeDistinctInstances(t *testing.T) {
	first, second := NewTestRuntimeComponent(nil), NewTestRuntimeComponent(nil)
	mod := mustBuildHostModule(t, Module("workers").RuntimeComponent(first, second))
	container := basic.New()
	if err := mod.Init(module.NewModuleInitOptions(container, container, container)); err != nil {
		t.Fatal(err)
	}
	components, err := mod.(capability.IRuntimeComponentProvider).RuntimeComponents()
	if err != nil || len(components) != 2 || components[0] != first || components[1] != second {
		t.Fatalf("wrong instances: %v %v", components, err)
	}
	stop, err := mod.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.startCount != 1 || second.startCount != 1 {
		t.Fatal("each instance must start exactly once")
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first.stopCount != 1 || second.stopCount != 1 {
		t.Fatal("each instance must stop exactly once")
	}
}

func TestTypedModuleRejectsCrossModuleLifecycleOwnerBeforeStart(t *testing.T) {
	worker := NewTestRuntimeComponent(nil)
	ctor := func(id string) module.ModuleCtor {
		return func() (module.IModule, error) { return Module(id).RuntimeComponent(worker).Build() }
	}
	h := runtime.NewHost([]module.ModuleCtor{ctor("one"), ctor("two")}, config.WithDisableHealthRoute(true))
	if err := h.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.StartBackground(context.Background()); err == nil {
		t.Fatal("duplicate ownership must fail")
	}
	if worker.startCount != 0 {
		t.Fatal("worker started before ownership validation")
	}
}

func TestTypedModuleRegistrarMiddlewareIsolation(t *testing.T) {
	group := newMockRouteGroup()
	container := basic.New()
	mod := mustBuildHostModule(t, Module("routes").RouteRegistrar(
		module.RouteRegistrarFunc(func(g httpx.IRouteGroup) error {
			g.Use(func(ctx httpx.IContext, next func() error) error { return next() })
			return nil
		}),
		module.RouteRegistrarFunc(func(g httpx.IRouteGroup) error { g.GET("/second", nil); return nil }),
	))
	if err := mod.Init(runtimecap.WithHTTP(module.NewModuleInitOptions(container, container, container), runtimecap.NewModuleHTTPOptions(group, ""))); err != nil {
		t.Fatal(err)
	}
	if err := mod.(module.IRouteModule).RegisterRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	mount := group.SubGroups[0]
	if len(mount.SubGroups) != 2 {
		t.Fatalf("expected independent registrar groups: %+v", mount)
	}
	if len(mount.SubGroups[0].Middlewares) != 1 || len(mount.SubGroups[1].Middlewares) != 0 {
		t.Fatal("registrar middleware leaked")
	}
}

// 含接口字段的 struct 类型可比较，不代表其动态值可比较。
type typedValueWorker struct{ value any }

func (typedValueWorker) Start(context.Context) error { return nil }

type typedValueRegistrar struct{ value any }

func (typedValueRegistrar) RegisterRoutes(httpx.IRouteGroup) error { return nil }

func TestTypedModuleRequiresPointerLifecycleIdentity(t *testing.T) {
	for _, worker := range []typedValueWorker{{value: []int{1}}, {value: 1}} {
		if _, err := Module("worker").RuntimeComponent(worker).Build(); err == nil {
			t.Fatal("value lifecycle must require explicit pointer identity")
		}
	}
}

func TestTypedModuleAllowsEqualValueRegistrarsWithoutIdentity(t *testing.T) {
	for _, value := range []any{[]int{1}, 1} {
		// 路由值对象没有 Start/Stop 所有权，不按值相等判断实例重复。
		registrar := typedValueRegistrar{value: value}
		if _, err := Module("routes").RouteRegistrar(registrar, registrar).Build(); err != nil {
			t.Fatal(err)
		}
	}
}
