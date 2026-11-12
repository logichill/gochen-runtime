package runtime

import (
	"context"
	"reflect"
	"testing"
	"time"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/assembly"
	"gochen-runtime/host/module"
	gerrors "gochen/errors"
	"gochen/eventing"
	"gochen/eventing/bus"
	"gochen/eventing/projection"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/messaging"
)

// ownershipTestProjection 是最小的可注册投影实现，用于端到端所有权断言。
type ownershipTestProjection struct {
	name string
}

func (p *ownershipTestProjection) Name() string { return p.name }

func (p *ownershipTestProjection) Handle(context.Context, eventing.IEvent) error { return nil }

func (p *ownershipTestProjection) SupportedEventTypes() []string {
	return []string{"OwnedProjectionEvent"}
}

func (p *ownershipTestProjection) Rebuild(context.Context, []eventing.Event[int64]) error {
	return nil
}

func (p *ownershipTestProjection) Status() projection.ProjectionStatus {
	return projection.ProjectionStatus{Name: p.name}
}

// 两个包装类型给每个注册项提供互不相同的 DI service type，避免同容器重复注册。
type moduleProjectionFirst struct{ *ownershipTestProjection }

type moduleProjectionSecond struct{ *ownershipTestProjection }

func newOwnershipTestHost(t *testing.T, ctors []module.ModuleCtor) (*Host, *projection.ProjectionManager[int64]) {
	t.Helper()
	container := basic.New()
	eventBus := bus.NewEventBus(messaging.NewMessageBus(&testTransport{}))
	manager, err := projection.NewProjectionManager[int64](
		store.NewMemoryEventStore[int64](), eventBus, registry.NewRegistry(), upcast.NewUpgraderRegistry())
	if err != nil {
		t.Fatalf("NewProjectionManager failed: %v", err)
	}
	// 模拟应用侧 DI 接线：投影管理器以单例注册在所有权注册接口下，
	// Host 必须自行从容器解析它，而不是依赖 config.WithProjectionRuntime。
	if err := container.RegisterInstance(
		reflect.TypeOf((*projection.IProjectionRegistrar)(nil)).Elem(),
		di.NewInstance(manager),
	); err != nil {
		t.Fatalf("register projection manager failed: %v", err)
	}

	host := NewHost(ctors,
		config.WithContainer(container),
		config.WithEventBus(eventBus),
		config.WithShutdownTimeout(time.Second),
	)
	return host, manager
}

func projectionModuleCtor(id string, registrations ...assembly.Registration) module.ModuleCtor {
	return func() (module.IModule, error) {
		return assembly.NewModule(assembly.ModuleDescriptor{
			ID:            id,
			Name:          id,
			Registrations: registrations,
		}), nil
	}
}

// TestHostProjectionOwnershipLifecycleThroughDIContainer 覆盖真实 Host → assembly → DI 单例链路：
// 模块声明的投影必须经容器解析出的所有权运行期完成注册，并在 Shutdown 时通过释放函数清理。
func TestHostProjectionOwnershipLifecycleThroughDIContainer(t *testing.T) {
	ctx := context.Background()
	owned := &ownershipTestProjection{name: "assembly-owned"}
	host, manager := newOwnershipTestHost(t, []module.ModuleCtor{
		projectionModuleCtor("owners", assembly.Registration{
			Lifetime:    module.SingletonLifetime,
			ServiceType: reflect.TypeOf(moduleProjectionFirst{}),
			Instance:    moduleProjectionFirst{owned},
			Roles:       []assembly.Role{assembly.RoleProjection},
		}),
	})

	if err := host.Prepare(ctx); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := host.StartBackground(ctx); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}
	if _, err := manager.ProjectionStatus("assembly-owned"); err != nil {
		t.Fatalf("module projection was not registered through the DI-resolved runtime: %v", err)
	}

	if err := host.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if _, err := manager.ProjectionStatus("assembly-owned"); !gerrors.Is(err, gerrors.NotFound) {
		t.Fatalf("shutdown must release the module projection through its release function, got %v", err)
	}
}

// TestHostProjectionRollbackKeepsOtherOwnerRegistration 覆盖同名冲突下的跨所有者安全：
// 模块注册失败的清理只能释放本次注册对应的资源，不得删除其他所有者的同名注册。
func TestHostProjectionRollbackKeepsOtherOwnerRegistration(t *testing.T) {
	ctx := context.Background()
	sharedOwner := &ownershipTestProjection{name: "shared"}
	host, manager := newOwnershipTestHost(t, []module.ModuleCtor{
		projectionModuleCtor("thief",
			assembly.Registration{
				Lifetime:    module.SingletonLifetime,
				ServiceType: reflect.TypeOf(moduleProjectionFirst{}),
				Instance:    moduleProjectionFirst{&ownershipTestProjection{name: "thief-owned"}},
				Roles:       []assembly.Role{assembly.RoleProjection},
			},
			assembly.Registration{
				Lifetime:    module.SingletonLifetime,
				ServiceType: reflect.TypeOf(moduleProjectionSecond{}),
				Instance:    moduleProjectionSecond{sharedOwner},
				Roles:       []assembly.Role{assembly.RoleProjection},
			},
		),
	})

	// 外部所有者先持有 "shared"，模块第二个投影的注册必然与之冲突。
	releaseOwner, err := manager.RegisterProjectionAny(ctx, sharedOwner)
	if err != nil {
		t.Fatalf("owner registration failed: %v", err)
	}

	if err := host.Prepare(ctx); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	err = host.StartBackground(ctx)
	if !gerrors.Is(err, gerrors.Conflict) {
		t.Fatalf("StartBackground error = %v, want projection name conflict", err)
	}

	if _, err := manager.ProjectionStatus("thief-owned"); !gerrors.Is(err, gerrors.NotFound) {
		t.Fatalf("module rollback left its own projection registered: %v", err)
	}
	if _, err := manager.ProjectionStatus("shared"); err != nil {
		t.Fatalf("module rollback deleted another owner's projection: %v", err)
	}
	if err := releaseOwner(ctx); err != nil {
		t.Fatalf("owner release failed: %v", err)
	}
	if _, err := manager.ProjectionStatus("shared"); !gerrors.Is(err, gerrors.NotFound) {
		t.Fatalf("owner release must remove its own projection, got %v", err)
	}
}
