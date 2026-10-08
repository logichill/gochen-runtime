package bootstrap

import (
	"context"
	"reflect"
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	"gochen-runtime/internal/testutil"
	"gochen/eventing/bus"
	"gochen/eventing/projection"
	"gochen/messaging"
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

type mockTransport = testutil.MockTransport

type lifecycleSubscriberTransport struct{}

func (t *lifecycleSubscriberTransport) Subscribe(context.Context, string, messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error { return nil }, nil
}

func (t *lifecycleSubscriberTransport) Start(context.Context) error { return nil }

func (t *lifecycleSubscriberTransport) Stop(context.Context) error { return nil }

func TestAssemblyHelpers_UseTypedDIProtocol(t *testing.T) {
	container := newTypedProtocolTestContainer()
	transport := &mockTransport{}
	eventBus := bus.NewEventBus(messaging.NewMessageBus(transport))
	pm := &projection.ProjectionManager[int64]{}

	if err := container.RegisterInstance(reflect.TypeOf((*messaging.ITransport)(nil)).Elem(), di.NewInstance(transport)); err != nil {
		t.Fatalf("register transport failed: %v", err)
	}
	if err := container.RegisterInstance(reflect.TypeOf((*bus.IEventBus)(nil)).Elem(), di.NewInstance(eventBus)); err != nil {
		t.Fatalf("register event bus failed: %v", err)
	}
	if err := container.RegisterInstance(reflect.TypeOf((*projection.IProjectionRegistrar)(nil)).Elem(), di.NewInstance(pm)); err != nil {
		t.Fatalf("register projection registrar failed: %v", err)
	}

	gotTransport, ok := tryResolveTransport(container)
	if !ok || gotTransport != transport {
		t.Fatalf("unexpected transport: %#v", gotTransport)
	}

	gotEventBus, ok := tryResolveEventBus(container)
	if !ok || gotEventBus != eventBus {
		t.Fatalf("unexpected event bus: %#v", gotEventBus)
	}

	gotProjectionRuntime, err := resolveProjectionRuntime(nil, container)
	if err != nil {
		t.Fatalf("resolve projection runtime failed: %v", err)
	}
	if gotProjectionRuntime != pm {
		t.Fatalf("unexpected projection runtime: %#v", gotProjectionRuntime)
	}

	other := newTypedProtocolTestContainer()
	otherTransport := &mockTransport{}
	if err := registerInstanceByType(other, (*messaging.ITransport)(nil), otherTransport); err != nil {
		t.Fatalf("registerInstanceByType failed: %v", err)
	}
	if !other.IsRegistered(reflect.TypeOf((*messaging.ITransport)(nil)).Elem()) {
		t.Fatal("expected transport to be registered by type")
	}

	if len(container.forbiddenCalls) > 0 {
		t.Fatalf("expected typed DI protocol only, got forbidden calls: %v", container.forbiddenCalls)
	}
	if len(other.forbiddenCalls) > 0 {
		t.Fatalf("expected typed DI protocol only, got forbidden calls in helper path: %v", other.forbiddenCalls)
	}
}

func TestPrepare_RegistersInjectedRuntimeCapabilities(t *testing.T) {
	container := basic.New()
	transport := &mockTransport{}
	eventBus := bus.NewEventBus(messaging.NewMessageBus(transport))
	pm := &projection.ProjectionManager[int64]{}

	rt, err := Prepare(Config{
		Container:          container,
		DisableHealthRoute: true,
		Transport:          transport,
		EventBus:           eventBus,
		ProjectionRuntime:  pm,
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if rt.Transport != transport || rt.EventBus != eventBus || rt.ProjectionRuntime != pm {
		t.Fatalf("runtime capabilities not assigned from injected values: %#v", rt)
	}
	assertResolvedSame(t, container, (*messaging.ITransport)(nil), transport)
	assertResolvedSame(t, container, (*capability.ITransport)(nil), transport)
	assertResolvedSame(t, container, (*bus.IEventBus)(nil), eventBus)
	assertResolvedSame(t, container, (*capability.IEventSubscriber)(nil), eventBus)
	assertResolvedSame(t, container, (*projection.IProjectionRegistrar)(nil), pm)
	assertResolvedSame(t, container, (*capability.IProjectionRuntime)(nil), pm)
}

func TestPrepare_AllowsInjectedLifecycleSubscriberTransport(t *testing.T) {
	container := basic.New()
	transport := &lifecycleSubscriberTransport{}

	rt, err := Prepare(Config{
		Container:          container,
		DisableHealthRoute: true,
		Transport:          transport,
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if rt.Transport != transport {
		t.Fatalf("unexpected transport: %#v", rt.Transport)
	}
	assertResolvedSame(t, container, (*capability.ITransport)(nil), transport)
	if _, err := container.Resolve(reflect.TypeOf((*messaging.ITransport)(nil)).Elem()); err == nil {
		t.Fatal("did not expect lifecycle-only transport registered as messaging.ITransport")
	}
}

func TestPrepare_RegistersResolvedRuntimeCapabilities(t *testing.T) {
	container := basic.New()
	transport := &mockTransport{}
	eventBus := bus.NewEventBus(messaging.NewMessageBus(transport))
	pm := &projection.ProjectionManager[int64]{}

	if err := container.RegisterInstance(reflect.TypeOf((*messaging.ITransport)(nil)).Elem(), di.NewInstance(transport)); err != nil {
		t.Fatalf("register transport failed: %v", err)
	}
	if err := container.RegisterInstance(reflect.TypeOf((*bus.IEventBus)(nil)).Elem(), di.NewInstance(eventBus)); err != nil {
		t.Fatalf("register event bus failed: %v", err)
	}
	if err := container.RegisterInstance(reflect.TypeOf((*projection.IProjectionRegistrar)(nil)).Elem(), di.NewInstance(pm)); err != nil {
		t.Fatalf("register projection registrar failed: %v", err)
	}

	rt, err := Prepare(Config{
		Container:          container,
		DisableHealthRoute: true,
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if rt.Transport != transport || rt.EventBus != eventBus || rt.ProjectionRuntime != pm {
		t.Fatalf("runtime capabilities not resolved from container: %#v", rt)
	}
	assertResolvedSame(t, container, (*capability.ITransport)(nil), transport)
	assertResolvedSame(t, container, (*capability.IEventSubscriber)(nil), eventBus)
	assertResolvedSame(t, container, (*capability.IProjectionRuntime)(nil), pm)
}

func assertResolvedSame[T any](t *testing.T, container di.IContainer, ifacePtr *T, want any) {
	t.Helper()
	iface := reflect.TypeOf(ifacePtr).Elem()
	got, err := container.Resolve(iface)
	if err != nil {
		t.Fatalf("resolve %s failed: %v", iface, err)
	}
	if got != want {
		t.Fatalf("resolve %s got %#v, want %#v", iface, got, want)
	}
}

// 回归 R2a-F03：注入的 typed-nil Transport/EventBus 必须按“未提供”归一化，
// 不得进入 Runtime 能力字段或在容器中登记 typed-nil 实例
// （否则 Prepare 报错，或后续模块启动在 nil 接收者上 panic）。
func TestPrepare_NormalizesTypedNilInjectedCapabilities(t *testing.T) {
	container := basic.New()
	var typedNilTransport *lifecycleSubscriberTransport
	var typedNilEventBus *lifecycleSubscriberTransport

	rt, err := Prepare(Config{
		Container:          container,
		DisableHealthRoute: true,
		Transport:          typedNilTransport,
		EventBus:           typedNilEventBus,
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if rt.Transport != nil {
		t.Fatalf("typed-nil transport must be treated as absent, got %#v", rt.Transport)
	}
	if rt.EventBus != nil {
		t.Fatalf("typed-nil event bus must be treated as absent, got %#v", rt.EventBus)
	}
	if container.IsRegistered(reflect.TypeOf((*capability.ITransport)(nil)).Elem()) {
		t.Fatal("typed-nil transport was registered in the container")
	}
	if container.IsRegistered(reflect.TypeOf((*capability.IEventSubscriber)(nil)).Elem()) {
		t.Fatal("typed-nil event bus was registered in the container")
	}
}
