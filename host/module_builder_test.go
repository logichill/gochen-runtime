package host

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/internal/assembly"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtimecap"
	"gochen-runtime/internal/testutil"
	"gochen/domain"
	"gochen/domain/eventsourced"
	"gochen/errors"
	"gochen/eventing/projection"
	"gochen/httpx"
	"gochen/messaging"
)

type mockRouteGroup = testutil.MockRouteGroup

func newMockRouteGroup() *mockRouteGroup {
	return testutil.NewMockRouteGroup()
}

type mockTransport = testutil.MockTransport

func newMockTransport() *mockTransport {
	return testutil.NewMockTransport()
}

// testService 测试用服务
type testService struct {
	name string
}

func NewTestService() *testService {
	return &testService{name: "test"}
}

func (s *testService) Name() string {
	return s.name
}

// testRouteRegistrar 测试用路由注册器
type testRouteRegistrar struct {
	service *testService
}

func NewTestRouteRegistrar(svc *testService) *testRouteRegistrar {
	return &testRouteRegistrar{service: svc}
}

func (r *testRouteRegistrar) RegisterRoutes(group httpx.IRouteGroup) error {
	group.GET("/test", nil)
	group.POST("/test", nil)
	return nil
}

// testEventHandler 测试用事件处理器
type testEventHandler struct {
	service *testService
	handled int
	msgType string
}

func NewTestEventHandler(svc *testService) *testEventHandler {
	return &testEventHandler{service: svc, msgType: "test.event"}
}

func (h *testEventHandler) Type() string {
	return h.msgType
}

func (h *testEventHandler) Handle(ctx context.Context, msg messaging.IMessage) error {
	h.handled++
	return nil
}

type multiTypeEventHandler struct {
	msgTypes []string
}

func NewMultiTypeEventHandler() *multiTypeEventHandler {
	return &multiTypeEventHandler{msgTypes: []string{"a.event", "b.event"}}
}

func (h *multiTypeEventHandler) Type() string { return "multiTypeEventHandler" }

func (h *multiTypeEventHandler) Handle(context.Context, messaging.IMessage) error { return nil }

func (h *multiTypeEventHandler) EventTypes() []string { return h.msgTypes }

type emptyTypeEventHandler struct{}

func NewEmptyTypeEventHandler() *emptyTypeEventHandler { return &emptyTypeEventHandler{} }

func (h *emptyTypeEventHandler) Type() string { return "" }

func (h *emptyTypeEventHandler) Handle(context.Context, messaging.IMessage) error { return nil }

type testProjectionBase = testutil.TestProjectionBase

type testProjection1 struct{ testProjectionBase }
type testProjection2 struct{ testProjectionBase }

func NewTestProjection1() *testProjection1 {
	return &testProjection1{testProjectionBase: testutil.NewTestProjectionBase("p1")}
}
func NewTestProjection2() *testProjection2 {
	return &testProjection2{testProjectionBase: testutil.NewTestProjectionBase("p2")}
}

type testRuntimeComponent struct {
	service     *testService
	startedWith string
	startCount  int
	stopCount   int
}

func NewTestRuntimeComponent(svc *testService) *testRuntimeComponent {
	return &testRuntimeComponent{service: svc}
}

func (c *testRuntimeComponent) Start(context.Context) error {
	c.startCount++
	if c.service != nil {
		c.startedWith = c.service.Name()
	}
	return nil
}

func (c *testRuntimeComponent) Stop(context.Context) error {
	c.stopCount++
	return nil
}

type bootAggregateEvent struct{}

func (e *bootAggregateEvent) EventType() string { return "BootAggregateEvent" }

type bootAggregate struct {
	*eventsourced.EventSourcedAggregate[int64]
}

func (a *bootAggregate) ApplyBootAggregateEvent(*bootAggregateEvent) {}

type bootTaggedAggregate struct {
	*eventsourced.EventSourcedAggregate[int64] `aggregate:"boot_tagged"`
}

func (a *bootTaggedAggregate) ApplyBootAggregateEvent(*bootAggregateEvent) {}

type bootBadAggregate struct {
	*eventsourced.EventSourcedAggregate[int64]
}

func (a *bootBadAggregate) ApplyFirst(*bootAggregateEvent)  {}
func (a *bootBadAggregate) ApplySecond(*bootAggregateEvent) {}

type bootInitAggregateEvent struct{}

func (e *bootInitAggregateEvent) EventType() string { return "BootInitAggregateEvent" }

type bootInitAggregate struct {
	*eventsourced.EventSourcedAggregate[int64]
}

func (a *bootInitAggregate) ApplyBootInitAggregateEvent(*bootInitAggregateEvent) {}

var _ domain.IDomainEvent = (*bootAggregateEvent)(nil)
var _ domain.IDomainEvent = (*bootInitAggregateEvent)(nil)

type mockProjectionManager struct {
	registered []string
	started    []string
	stopped    []string
}

func (m *mockProjectionManager) RegisterProjectionAny(ctx context.Context, p any) error {
	projection, ok := p.(projection.IProjection[int64])
	if !ok {
		return errors.NewCode(errors.InvalidInput, "projection type mismatch")
	}
	return m.RegisterProjectionWithContext(ctx, projection)
}

func (m *mockProjectionManager) RegisterProjectionWithContext(ctx context.Context, p projection.IProjection[int64]) error {
	m.registered = append(m.registered, p.Name())
	return nil
}

func (m *mockProjectionManager) StartProjection(name string) error {
	m.started = append(m.started, name)
	return nil
}

func (m *mockProjectionManager) StopProjection(name string) error {
	m.stopped = append(m.stopped, name)
	return nil
}

func mustBuildHostModule(t *testing.T, builder *Builder) IModule {
	t.Helper()
	module, err := builder.Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	return module
}

func newModuleDescriptorForTest(t *testing.T, builder *Builder) (assembly.ModuleDescriptor, error) {
	t.Helper()
	return builder.descriptor()
}

func TestModuleBuilder_IDAndName(t *testing.T) {
	mod := mustBuildHostModule(t, Module("test-module").Name("Test Module"))

	if mod.ID() != "test-module" {
		t.Errorf("expected ID 'test-module', got '%s'", mod.ID())
	}

	if mod.Name() != "Test Module" {
		t.Errorf("expected Name 'Test Module', got '%s'", mod.Name())
	}
}

func TestModuleBuilder_Init(t *testing.T) {
	container := basic.New()

	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			Provide(NewTestService),
	)

	opts := module.NewModuleInitOptions(container, container, container)

	err := mod.Init(opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// 验证 provider 已注册
	var svc *testService
	err = container.Invoke(di.NewInvocation(func(s *testService) {
		svc = s
	}))
	if err != nil {
		t.Fatalf("Failed to resolve testService: %v", err)
	}
	if svc == nil {
		t.Error("testService is nil")
	}
}

func TestModuleBuilder_RegisterRoutes(t *testing.T) {
	container := basic.New()
	routeGroup := newMockRouteGroup()

	testMiddleware := func(ctx httpx.IContext, next func() error) error {
		return next()
	}

	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			Provide(NewTestService).
			RouteRegistrar(NewTestRouteRegistrar(NewTestService())).
			Middleware(testMiddleware),
	)

	opts := runtimecap.WithHTTP(
		module.NewModuleInitOptions(container, container, container),
		runtimecap.NewModuleHTTPOptions(routeGroup, ""),
	)

	err := mod.Init(opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	routeModule, ok := mod.(module.IRouteModule)
	if !ok {
		t.Fatal("module does not implement module.IRouteModule")
	}

	err = routeModule.RegisterRoutes(context.Background())
	if err != nil {
		t.Fatalf("RegisterRoutes failed: %v", err)
	}

	// 验证中间件已应用（应用到子组上）
	// MountGroup() 创建子组并应用中间件
	if len(routeGroup.SubGroups) == 0 {
		t.Fatal("expected subgroup to be created")
	}
	subGroup := routeGroup.SubGroups[0]
	if len(subGroup.Middlewares) == 0 {
		t.Error("middleware was not applied to subgroup")
	}
}

func TestNewModule_RegisterRoutes_ResolveRoleByExplicitServiceType(t *testing.T) {
	container := basic.New()
	routeGroup := newMockRouteGroup()

	mod := assembly.NewModule(assembly.ModuleDescriptor{
		ID:   "test",
		Name: "Test",
		Registrations: []assembly.Registration{
			{
				Lifetime:    module.SingletonLifetime,
				ServiceType: reflect.TypeOf((*testService)(nil)),
				Factory:     di.NewFactory(NewTestService),
			},
			{
				Lifetime:    module.SingletonLifetime,
				ServiceType: reflect.TypeOf((*module.IRouteRegistrar)(nil)).Elem(),
				Factory:     di.NewFactory(NewTestRouteRegistrar),
				Roles:       []assembly.Role{assembly.RoleRouteRegistrar},
			},
		},
	})

	opts := runtimecap.WithHTTP(
		module.NewModuleInitOptions(container, container, container),
		runtimecap.NewModuleHTTPOptions(routeGroup, ""),
	)

	if err := mod.Init(opts); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	routeModule, ok := mod.(module.IRouteModule)
	if !ok {
		t.Fatal("module does not implement module.IRouteModule")
	}

	if err := routeModule.RegisterRoutes(context.Background()); err != nil {
		t.Fatalf("RegisterRoutes failed: %v", err)
	}
}

func TestNewModuleDescriptor_AssignsCapabilityRoles(t *testing.T) {
	desc, err := newModuleDescriptorForTest(t,
		Module("test").
			Name("Test").
			EventHandler(NewTestEventHandler(NewTestService())).
			Projection(module.ProjectionOf[int64](NewTestProjection1())).
			RuntimeComponent(NewTestRuntimeComponent(NewTestService())),
	)
	if err != nil {
		t.Fatalf("NewModuleDescriptor failed: %v", err)
	}

	got := map[assembly.Role]int{}
	for _, reg := range desc.Registrations {
		for _, role := range reg.Roles {
			got[role]++
		}
	}

	if got[assembly.RoleEventHandler] != 1 {
		t.Fatalf("expected 1 event handler role, got %d", got[assembly.RoleEventHandler])
	}
	if got[assembly.RoleProjection] != 1 {
		t.Fatalf("expected 1 projection role, got %d", got[assembly.RoleProjection])
	}
	if got[assembly.RoleRuntimeComponent] != 1 {
		t.Fatalf("expected 1 runtime component role, got %d", got[assembly.RoleRuntimeComponent])
	}
}

func TestModuleBuilder_NilProviders(t *testing.T) {
	container := basic.New()

	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			Provide(
				nil, // 应该被忽略
				NewTestService,
				nil, // 应该被忽略
			),
	)

	opts := module.NewModuleInitOptions(container, container, container)

	err := mod.Init(opts)
	if err != nil {
		t.Fatalf("Init failed with nil providers: %v", err)
	}
}

func TestModuleBuilder_InvalidProvider_PreservesRootCause(t *testing.T) {
	_, err := Module("test").
		Name("Test").
		Provide("not-a-function").
		Build()
	if err == nil {
		t.Fatal("expected Build to fail for invalid provider")
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.Code() != errors.InvalidInput {
		t.Fatalf("expected InvalidInput, got %v", appErr.Code())
	}
	if !strings.Contains(err.Error(), "provider must be a function") {
		t.Fatalf("expected original provider error, got: %v", err)
	}
	if got := appErr.Details()["field"]; got != "providers" {
		t.Fatalf("expected field detail providers, got %#v", got)
	}
	if got := appErr.Details()["index"]; got != 0 {
		t.Fatalf("expected index detail 0, got %#v", got)
	}
	if got := appErr.Details()["module"]; got != "test" {
		t.Fatalf("expected module detail test, got %#v", got)
	}
}

func TestModuleBuilder_NoHTTP(t *testing.T) {
	container := basic.New()

	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			RouteRegistrar(NewTestRouteRegistrar(NewTestService())),
	)

	opts := runtimecap.WithHTTP(module.NewModuleInitOptions(container, container, container), nil) // HTTP 未启用

	// 需要先注册依赖
	container.RegisterConstructor(di.NewConstructor(NewTestService))

	err := mod.Init(opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	routeModule := mod.(module.IRouteModule)
	err = routeModule.RegisterRoutes(context.Background())
	if err != nil {
		t.Fatalf("RegisterRoutes should not fail when HTTP is nil: %v", err)
	}
}

func TestModuleBuilder_Aggregates_RegisterMetadataFailFast(t *testing.T) {
	_, err := Module("test").
		Name("Test").
		Aggregate(Aggregate(&bootBadAggregate{}, "boot_bad")).
		Build()
	if err == nil {
		t.Fatal("expected Build to fail for invalid aggregate metadata")
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.Code() != errors.Conflict {
		t.Fatalf("expected Conflict, got %v", appErr.Code())
	}
	if got := appErr.Details()["field"]; got != "aggregates" {
		t.Fatalf("expected field detail aggregates, got %#v", got)
	}
	if got := appErr.Details()["index"]; got != 0 {
		t.Fatalf("expected index detail 0, got %#v", got)
	}
}

func TestAggregateFromTag_CapturesResolveError(t *testing.T) {
	registration := AggregateFromTag(&bootBadAggregate{})
	if registration.Error == nil {
		t.Fatal("expected AggregateFromTag to capture tag resolve error")
	}
	if registration.AggregateType != "" {
		t.Fatalf("expected empty aggregate type, got %q", registration.AggregateType)
	}
}

func TestAggregateFromTag_ResolvesTaggedAggregate(t *testing.T) {
	registration := AggregateFromTag(&bootTaggedAggregate{})
	if registration.Error != nil {
		t.Fatalf("unexpected error: %v", registration.Error)
	}
	if registration.AggregateType != "boot_tagged" {
		t.Fatalf("expected aggregate type boot_tagged, got %q", registration.AggregateType)
	}
}

func TestModuleBuilder_Aggregates_ValidateForRuntimeInit(t *testing.T) {
	metadataRegistry := eventsourced.NewMetadataRegistry()
	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			Aggregate(Aggregate(&bootAggregate{}, "boot_aggregate")),
	)

	container := basic.New()
	if err := mod.Init(module.NewModuleInitOptions(container, container, container, runtimecap.MetadataRegistry(metadataRegistry))); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	meta1, err := metadataRegistry.Resolve(&bootAggregate{}, "boot_aggregate")
	if err != nil {
		t.Fatalf("MetadataRegistry.Resolve failed: %v", err)
	}
	meta2, err := metadataRegistry.Register(&bootAggregate{}, "boot_aggregate")
	if err != nil {
		t.Fatalf("MetadataRegistry.Register failed: %v", err)
	}
	if meta1 != meta2 {
		t.Fatalf("expected boot aggregate metadata to reuse the registered cache")
	}

	agg := &bootAggregate{}
	esa, err := eventsourced.InitAggregate[int64](metadataRegistry, agg, 1, "boot_aggregate")
	if err != nil {
		t.Fatalf("InitAggregate failed: %v", err)
	}
	agg.EventSourcedAggregate = esa
	if err := agg.ApplyAndRecord(&bootAggregateEvent{}); err != nil {
		t.Fatalf("ApplyAndRecord failed: %v", err)
	}
}

func TestModuleBuilder_Aggregates_DoNotRegisterBeforeInit(t *testing.T) {
	metadataRegistry := eventsourced.NewMetadataRegistry()
	mod := mustBuildHostModule(t,
		Module("test").
			Name("Test").
			Aggregate(Aggregate(&bootInitAggregate{}, "boot_init_aggregate")),
	)

	if _, err := metadataRegistry.Resolve(&bootInitAggregate{}, "boot_init_aggregate"); err == nil {
		t.Fatal("expected BuildModule to avoid registering aggregate metadata before Init")
	}

	container := basic.New()
	if err := mod.Init(module.NewModuleInitOptions(container, container, container, runtimecap.MetadataRegistry(metadataRegistry))); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	meta, err := metadataRegistry.Resolve(&bootInitAggregate{}, "boot_init_aggregate")
	if err != nil {
		t.Fatalf("MetadataRegistry.Resolve after Init failed: %v", err)
	}
	if meta == nil {
		t.Fatal("expected metadata to be registered during Init")
	}
}

func TestNewModuleDescriptor_Aggregates_RegisterAtInit(t *testing.T) {
	metadataRegistry := eventsourced.NewMetadataRegistry()
	desc, err := newModuleDescriptorForTest(t,
		Module("test").
			Name("Test").
			Aggregate(Aggregate(&bootInitAggregate{}, "boot_descriptor_aggregate")),
	)
	if err != nil {
		t.Fatalf("NewModuleDescriptor failed: %v", err)
	}

	if _, err := metadataRegistry.Resolve(&bootInitAggregate{}, "boot_descriptor_aggregate"); err == nil {
		t.Fatal("expected NewModuleDescriptor to avoid registering aggregate metadata before Init")
	}

	mod := assembly.NewModule(desc)
	container := basic.New()
	if err := mod.Init(module.NewModuleInitOptions(container, container, container, runtimecap.MetadataRegistry(metadataRegistry))); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	meta, err := metadataRegistry.Resolve(&bootInitAggregate{}, "boot_descriptor_aggregate")
	if err != nil {
		t.Fatalf("MetadataRegistry.Resolve after descriptor Init failed: %v", err)
	}
	if meta == nil {
		t.Fatal("expected descriptor init to register aggregate metadata")
	}
}
