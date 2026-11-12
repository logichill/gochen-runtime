package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/assembly"
	"gochen-runtime/host/internal/bootstrap"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/initcap"
	"gochen-runtime/host/module/runtimecap"
	"gochen/domain/eventsourced"
	gerrors "gochen/errors"
	"gochen/httpx"
	"gochen/messaging"
)

type testModule struct {
	id       string
	name     string
	initFn   func(module.ModuleInitOptions) error
	startFn  func(context.Context) (module.ModuleStopFunc, error)
	routesFn func(context.Context) error
}

type dependencyTestModule struct {
	*testModule
	deps []string
}

func (m *dependencyTestModule) DependsOn() []string { return append([]string(nil), m.deps...) }

// valueTypeTestModule 以值接收者实现 IModule，且含 slice 字段（动态类型不可比较），
// 用于回归 R2a-F02：模块实例不得被用作 map 键。
type valueTypeTestModule struct {
	id      string
	name    string
	deps    []string
	initLog *[]string
}

func (m valueTypeTestModule) ID() string   { return m.id }
func (m valueTypeTestModule) Name() string { return m.name }
func (m valueTypeTestModule) DependsOn() []string {
	return append([]string(nil), m.deps...)
}
func (m valueTypeTestModule) Init(module.ModuleInitOptions) error {
	if m.initLog != nil {
		*m.initLog = append(*m.initLog, m.id)
	}
	return nil
}
func (m valueTypeTestModule) Start(context.Context) (module.ModuleStopFunc, error) { return nil, nil }

func (m *testModule) ID() string   { return m.id }
func (m *testModule) Name() string { return m.name }
func (m *testModule) Init(opts module.ModuleInitOptions) error {
	if m.initFn != nil {
		return m.initFn(opts)
	}
	return nil
}
func (m *testModule) Start(ctx context.Context) (module.ModuleStopFunc, error) {
	if m.startFn != nil {
		return m.startFn(ctx)
	}
	return nil, nil
}
func (m *testModule) RegisterRoutes(ctx context.Context) error {
	if m.routesFn != nil {
		return m.routesFn(ctx)
	}
	return nil
}

type testTransport struct {
	startErr              error
	runningOnStartError   bool
	stopErr               error
	startCalls            int
	stopCalls             int
	stopWithSnapshotCalls int
	running               bool
}

func (t *testTransport) Publish(context.Context, messaging.IMessage) error { return nil }
func (t *testTransport) PublishAll(context.Context, []messaging.IMessage) error {
	return nil
}
func (t *testTransport) Subscribe(context.Context, string, messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error { return nil }, nil
}
func (t *testTransport) Start(context.Context) error {
	t.startCalls++
	if t.startErr == nil || t.runningOnStartError {
		t.running = true
	}
	return t.startErr
}
func (t *testTransport) Stop(context.Context) error {
	t.stopCalls++
	if t.stopErr == nil {
		t.running = false
	}
	return t.stopErr
}
func (t *testTransport) StopWithSnapshot(context.Context) ([]messaging.IMessage, error) {
	t.stopWithSnapshotCalls++
	if t.stopErr == nil {
		t.running = false
	}
	return nil, t.stopErr
}
func (t *testTransport) Stats() messaging.TransportStats {
	return messaging.TransportStats{Running: t.running}
}

type conflictServer struct {
	httpx.IServer
	conflicts []httpx.RouteConflict
}

func (s *conflictServer) RouteConflicts() []httpx.RouteConflict {
	return append([]httpx.RouteConflict(nil), s.conflicts...)
}

type lifecycleServer struct {
	httpx.IServer
	stopErr   error
	stopCalls int
}

type assemblyLifecycleComponent struct {
	startErr   error
	stopErr    error
	startCount int
	stopCount  int
}

func (c *assemblyLifecycleComponent) Start(context.Context) error {
	c.startCount++
	return c.startErr
}

func (c *assemblyLifecycleComponent) Stop(context.Context) error {
	c.stopCount++
	return c.stopErr
}

func (s *lifecycleServer) Stop(context.Context) error {
	s.stopCalls++
	return s.stopErr
}

type stopOnlyTransport struct {
	stopErr   error
	stopCalls int
}

func (t *stopOnlyTransport) Publish(context.Context, messaging.IMessage) error { return nil }
func (t *stopOnlyTransport) PublishAll(context.Context, []messaging.IMessage) error {
	return nil
}
func (t *stopOnlyTransport) Subscribe(context.Context, string, messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error { return nil }, nil
}
func (t *stopOnlyTransport) Start(context.Context) error { return nil }
func (t *stopOnlyTransport) Stop(context.Context) error {
	t.stopCalls++
	return t.stopErr
}
func (t *stopOnlyTransport) Stats() messaging.TransportStats { return messaging.TransportStats{} }

type testHTTPServer struct{}

func (s *testHTTPServer) GET(string, httpx.Handler) httpx.IServer     { return s }
func (s *testHTTPServer) POST(string, httpx.Handler) httpx.IServer    { return s }
func (s *testHTTPServer) PUT(string, httpx.Handler) httpx.IServer     { return s }
func (s *testHTTPServer) DELETE(string, httpx.Handler) httpx.IServer  { return s }
func (s *testHTTPServer) PATCH(string, httpx.Handler) httpx.IServer   { return s }
func (s *testHTTPServer) HEAD(string, httpx.Handler) httpx.IServer    { return s }
func (s *testHTTPServer) OPTIONS(string, httpx.Handler) httpx.IServer { return s }
func (s *testHTTPServer) Group(string) httpx.IRouteGroup              { return &testRouteGroup{} }
func (s *testHTTPServer) Use(...httpx.Middleware) httpx.IServer       { return s }
func (s *testHTTPServer) Static(string, string) httpx.IServer         { return s }
func (s *testHTTPServer) ServeStatic(string, string)                  {}
func (s *testHTTPServer) Start(string) error                          { return nil }
func (s *testHTTPServer) Stop(context.Context) error                  { return nil }

type testRouteGroup struct{}

func (g *testRouteGroup) GET(string, httpx.Handler) httpx.IRouteGroup     { return g }
func (g *testRouteGroup) POST(string, httpx.Handler) httpx.IRouteGroup    { return g }
func (g *testRouteGroup) PUT(string, httpx.Handler) httpx.IRouteGroup     { return g }
func (g *testRouteGroup) DELETE(string, httpx.Handler) httpx.IRouteGroup  { return g }
func (g *testRouteGroup) PATCH(string, httpx.Handler) httpx.IRouteGroup   { return g }
func (g *testRouteGroup) HEAD(string, httpx.Handler) httpx.IRouteGroup    { return g }
func (g *testRouteGroup) OPTIONS(string, httpx.Handler) httpx.IRouteGroup { return g }
func (g *testRouteGroup) Group(string) httpx.IRouteGroup                  { return g }
func (g *testRouteGroup) Use(...httpx.Middleware) httpx.IRouteGroup       { return g }

func TestPrepareNormalizesModuleHTTPConfigKeys(t *testing.T) {
	t.Parallel()

	var gotPrefix string
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return &testModule{
				id:   "Users",
				name: "Users",
				initFn: func(opts module.ModuleInitOptions) error {
					httpOpt := runtimecap.HTTPFrom(opts)
					if httpOpt == nil {
						t.Fatal("expected module HTTP options")
					}
					gotPrefix = httpOpt.Prefix()
					return nil
				},
			}, nil
		},
	}, config.WithHTTPServer(&testHTTPServer{}), config.WithBasePath("/api"), func(cfg *config.Config) {
		cfg.ModuleHTTP["users"] = config.ModuleHTTPConfig{Prefix: "/custom"}
	})

	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if gotPrefix != "/custom" {
		t.Fatalf("unexpected module HTTP prefix: %q", gotPrefix)
	}
}

func TestPrepareDefaultsModuleHTTPPrefixToModuleID(t *testing.T) {
	t.Parallel()

	var gotPrefix string
	middleware := func(httpx.IContext, func() error) error { return nil }
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return &testModule{
				id:   "Users",
				name: "Users",
				initFn: func(opts module.ModuleInitOptions) error {
					httpOpt := runtimecap.HTTPFrom(opts)
					if httpOpt == nil {
						t.Fatal("expected module HTTP options")
					}
					gotPrefix = httpOpt.Prefix()
					return nil
				},
			}, nil
		},
	}, config.WithHTTPServer(&testHTTPServer{}), config.WithBasePath("/api"), config.WithModuleHTTP(
		"users",
		config.ModuleHTTPConfig{Middlewares: []httpx.Middleware{middleware}},
	))

	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if gotPrefix != "/users" {
		t.Fatalf("expected default module prefix %q, got %q", "/users", gotPrefix)
	}
}

func TestPrepareInjectsSharedModuleCapabilitiesAndHTTPDefaults(t *testing.T) {
	capabilityKey := initcap.NewKey[string]("test.shared")
	defaultMiddleware := func(httpx.IContext, func() error) error { return nil }
	moduleMiddleware := func(httpx.IContext, func() error) error { return nil }
	var gotCapability string
	var gotPrefix string
	var gotMiddlewares []httpx.Middleware

	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return &testModule{
				id:   "users",
				name: "Users",
				initFn: func(opts module.ModuleInitOptions) error {
					gotCapability, _ = module.CapabilityFrom(opts, capabilityKey)
					httpOpt := runtimecap.HTTPFrom(opts)
					if httpOpt == nil {
						t.Fatal("expected module HTTP options")
					}
					gotPrefix = httpOpt.Prefix()
					gotMiddlewares = httpOpt.Middlewares()
					return nil
				},
			}, nil
		},
	},
		config.WithHTTPServer(&testHTTPServer{}),
		config.WithModuleCapabilities(initcap.Set(capabilityKey, "shared")),
		config.WithModuleHTTPDefaults(config.ModuleHTTPConfig{Middlewares: []httpx.Middleware{defaultMiddleware}}),
		config.WithModuleHTTP("users", config.ModuleHTTPConfig{Middlewares: []httpx.Middleware{moduleMiddleware}}),
	)

	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if gotCapability != "shared" {
		t.Fatalf("expected shared module capability, got %q", gotCapability)
	}
	if gotPrefix != "/users" {
		t.Fatalf("expected module ID prefix %q, got %q", "/users", gotPrefix)
	}
	if len(gotMiddlewares) != 2 {
		t.Fatalf("expected default and module middlewares, got %d", len(gotMiddlewares))
	}
}

func TestPrepareUsesRootModuleHTTPPrefixWhenExplicitlyConfigured(t *testing.T) {
	t.Parallel()

	var gotPrefix string
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return &testModule{
				id:   "users",
				name: "Users",
				initFn: func(opts module.ModuleInitOptions) error {
					httpOpt := runtimecap.HTTPFrom(opts)
					if httpOpt == nil {
						t.Fatal("expected module HTTP options")
					}
					gotPrefix = httpOpt.Prefix()
					return nil
				},
			}, nil
		},
	}, config.WithHTTPServer(&testHTTPServer{}), config.WithBasePath("/api"), func(cfg *config.Config) {
		cfg.ModuleHTTP["users"] = config.ModuleHTTPConfig{Prefix: "/"}
	})

	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if gotPrefix != "/" {
		t.Fatalf("expected explicit root module prefix %q, got %q", "/", gotPrefix)
	}
}

func TestPrepareRejectsDuplicateNormalizedModuleHTTPKeys(t *testing.T) {
	t.Parallel()

	host := NewHost(nil, func(cfg *config.Config) {
		cfg.ModuleHTTP["Users"] = config.ModuleHTTPConfig{}
		cfg.ModuleHTTP["users"] = config.ModuleHTTPConfig{}
	})

	err := host.normalizeModuleHTTPConfigKeys()
	if err == nil {
		t.Fatal("expected duplicate normalized key error")
	}
}

func TestEnsureFrameworkRegistriesRegistersCapabilityInterfaces(t *testing.T) {
	t.Parallel()

	container := basic.New()
	moduleRegistry := module.NewModuleRegistry()
	metadataRegistry := eventsourced.NewMetadataRegistry()
	host := &Host{
		config: &config.Config{
			Container:        container,
			ModuleRegistry:   moduleRegistry,
			MetadataRegistry: metadataRegistry,
		},
		container: container,
	}

	if err := host.ensureFrameworkRegistries(); err != nil {
		t.Fatalf("ensureFrameworkRegistries failed: %v", err)
	}

	assertResolvedInstance := func(serviceType reflect.Type, want any) {
		t.Helper()
		got, err := container.Resolve(serviceType)
		if err != nil {
			t.Fatalf("Resolve(%s) failed: %v", serviceType, err)
		}
		if got != want {
			t.Fatalf("Resolve(%s) got %#v, want %#v", serviceType, got, want)
		}
	}
	assertResolvedInstance(reflect.TypeOf((*capability.IModuleRegistry)(nil)).Elem(), moduleRegistry)
	assertResolvedInstance(reflect.TypeOf((*capability.IMetadataRegistry)(nil)).Elem(), metadataRegistry)
}

func TestStartBackgroundFailsFastOnRouteConflictsBeforeModuleStart(t *testing.T) {
	t.Parallel()

	started := false
	host := &Host{
		config: &config.Config{FailFastOnRouteConflicts: true},
		modules: []module.IModule{
			&testModule{
				id:       "users",
				name:     "users",
				routesFn: func(context.Context) error { return nil },
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					started = true
					return nil, nil
				},
			},
		},
		runtime: &bootstrap.Runtime{
			HTTPServer: &conflictServer{
				IServer:   httpx.WithRouteRegistry(&testHTTPServer{}),
				conflicts: []httpx.RouteConflict{{Method: "GET", Path: "/users", Count: 2}},
			},
		},
	}

	err := host.StartBackground(context.Background())
	if err == nil {
		t.Fatal("expected route conflict error")
	}
	if started {
		t.Fatal("module Start should not run after route conflict fail-fast")
	}
}

func TestDefaultHostConfigEnablesRouteConflictFailFast(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	if !cfg.FailFastOnRouteConflicts {
		t.Fatal("expected route conflict fail-fast to be enabled by default")
	}
}

func TestStartBackgroundRollsBackStartedModulesOnTransportFailure(t *testing.T) {
	t.Parallel()

	rolledBack := false
	host := &Host{
		config: &config.Config{},
		modules: []module.IModule{
			&testModule{
				id:   "users",
				name: "users",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					return func(context.Context) error {
						rolledBack = true
						return nil
					}, nil
				},
			},
		},
		runtime: &bootstrap.Runtime{
			Transport: &testTransport{startErr: errors.New("boom")},
		},
	}

	err := host.StartBackground(context.Background())
	if err == nil {
		t.Fatal("expected transport start error")
	}
	if !rolledBack {
		t.Fatal("expected module rollback stop to be called")
	}
	if len(host.moduleStops) != 0 {
		t.Fatalf("expected no retained module stops after rollback, got %d", len(host.moduleStops))
	}
}

func TestStartBackgroundCleansPartiallyStartedTransportOnFailure(t *testing.T) {
	startErr := errors.New("transport start failed after initialization")
	transport := &testTransport{startErr: startErr, runningOnStartError: true}
	host := &Host{
		config:  &config.Config{},
		runtime: &bootstrap.Runtime{Transport: transport},
	}

	if err := host.StartBackground(context.Background()); !gerrors.Is(err, startErr) {
		t.Fatalf("StartBackground error = %v, want %v", err, startErr)
	}
	if transport.stopWithSnapshotCalls != 1 {
		t.Fatalf("transport rollback snapshot calls = %d, want 1", transport.stopWithSnapshotCalls)
	}
	if transport.running {
		t.Fatal("transport remained running after failed start rollback")
	}
}

func TestStartBackgroundRetainsFailedRollbackForShutdownRetry(t *testing.T) {
	startErr := errors.New("second module start failed")
	stopErr := errors.New("first module rollback failed")
	stopCalls := 0
	host := &Host{
		config: &config.Config{},
		modules: []module.IModule{
			&testModule{
				id:   "first",
				name: "first",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					return func(context.Context) error {
						stopCalls++
						if stopCalls == 1 {
							return stopErr
						}
						return nil
					}, nil
				},
			},
			&testModule{
				id:   "second",
				name: "second",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					return nil, startErr
				},
			},
		},
	}

	err := host.StartBackground(context.Background())
	if !gerrors.Is(err, startErr) || !gerrors.Is(err, stopErr) {
		t.Fatalf("expected start and rollback failures, got %v", err)
	}
	if !host.backgroundStarted.Load() || len(host.moduleStops) != 1 {
		t.Fatalf("failed rollback must remain active for retry, started=%v stops=%d", host.backgroundStarted.Load(), len(host.moduleStops))
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected retained stop retry to succeed, got %v", err)
	}
	if host.backgroundStarted.Load() || len(host.moduleStops) != 0 || stopCalls != 2 {
		t.Fatalf("unexpected retry state: started=%v stops=%d calls=%d", host.backgroundStarted.Load(), len(host.moduleStops), stopCalls)
	}
}

func TestStartBackgroundRollbackUsesIndependentCleanupBudget(t *testing.T) {
	type ctxKey struct{}
	var gotValue any
	var gotDeadline bool
	var gotCanceled bool
	host := &Host{
		config: &config.Config{ShutdownTimeout: 50 * time.Millisecond},
		modules: []module.IModule{
			&testModule{
				id: "started", name: "started",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					return func(ctx context.Context) error {
						gotValue = ctx.Value(ctxKey{})
						_, gotDeadline = ctx.Deadline()
						gotCanceled = ctx.Err() != nil
						return nil
					}, nil
				},
			},
			&testModule{id: "failed", name: "failed", startFn: func(context.Context) (module.ModuleStopFunc, error) {
				return nil, errors.New("start failed")
			}},
		},
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := context.WithValue(baseCtx, ctxKey{}, "kept")
	cancel()
	if err := host.StartBackground(ctx); err == nil {
		t.Fatal("expected start failure")
	}
	if gotValue != "kept" || !gotDeadline || gotCanceled {
		t.Fatalf("rollback context lost value/deadline or inherited cancellation: value=%v deadline=%v canceled=%v", gotValue, gotDeadline, gotCanceled)
	}
}

func TestStartBackgroundRepeatedCallFailsFast(t *testing.T) {
	t.Parallel()

	startCalls := 0
	host := &Host{
		config: &config.Config{},
		modules: []module.IModule{
			&testModule{
				id:   "users",
				name: "users",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					startCalls++
					return func(context.Context) error { return nil }, nil
				},
			},
		},
	}

	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("first StartBackground failed: %v", err)
	}
	err := host.StartBackground(context.Background())
	if err == nil {
		t.Fatal("expected repeated StartBackground to fail")
	}
	if !gerrors.Is(err, gerrors.Conflict) {
		t.Fatalf("expected conflict error, got %v", err)
	}
	if startCalls != 1 {
		t.Fatalf("expected module start once, got %d", startCalls)
	}
}

func TestStartBackgroundFailureRollsBackStartedFlagAndAllowsRetry(t *testing.T) {
	startCalls := 0
	failFirst := true
	host := &Host{
		config: &config.Config{},
		modules: []module.IModule{
			&testModule{
				id:   "users",
				name: "users",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					startCalls++
					if failFirst {
						failFirst = false
						return nil, errors.New("start failed")
					}
					return func(context.Context) error { return nil }, nil
				},
			},
		},
	}

	firstErr := host.StartBackground(context.Background())
	if firstErr == nil {
		t.Fatal("expected first StartBackground to fail")
	}
	if host.backgroundStarted.Load() {
		t.Fatal("expected backgroundStarted to roll back after failed start")
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("expected retry StartBackground to succeed, got %v", err)
	}
	if startCalls != 2 {
		t.Fatalf("expected module start twice, got %d", startCalls)
	}
}

func TestStartBackgroundDoesNotReregisterRoutesAfterModuleFailure(t *testing.T) {
	startErr := errors.New("module start failed")
	failFirst := true
	var group httpx.IRouteGroup
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return &testModule{
				id:   "users",
				name: "users",
				initFn: func(opts module.ModuleInitOptions) error {
					httpOptions := runtimecap.HTTPFrom(opts)
					if httpOptions == nil {
						t.Fatal("expected module HTTP options")
					}
					group = httpOptions.MountGroup()
					return nil
				},
				routesFn: func(context.Context) error {
					group.GET("/retry", nil)
					return nil
				},
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					if failFirst {
						failFirst = false
						return nil, startErr
					}
					return nil, nil
				},
			}, nil
		},
	}, config.WithHTTPServer(&testHTTPServer{}))
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := host.StartBackground(context.Background()); !gerrors.Is(err, startErr) {
		t.Fatalf("first StartBackground error = %v, want %v", err, startErr)
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("retry StartBackground failed: %v", err)
	}

	rr, ok := host.runtime.HTTPServer.(httpx.IRouteRegistry)
	if !ok {
		t.Fatal("expected route registry")
	}
	if conflicts := rr.RouteConflicts(); len(conflicts) != 0 {
		t.Fatalf("route conflicts after retry = %#v, want none", conflicts)
	}
}

func TestStartBackgroundAssemblyCleansPartialRuntimeAndRetries(t *testing.T) {
	startErr := errors.New("assembly component start failed")
	component := &assemblyLifecycleComponent{startErr: startErr}
	onStopCalls := 0
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return assembly.NewModule(assembly.ModuleDescriptor{
				ID:   "assembly-test",
				Name: "assembly-test",
				Registrations: []assembly.Registration{{
					Lifetime:    module.SingletonLifetime,
					ServiceType: reflect.TypeOf((*capability.IRuntimeComponent)(nil)).Elem(),
					Instance:    component,
					Roles:       []assembly.Role{assembly.RoleRuntimeComponent},
				}},
				OnStop: func(context.Context) error {
					onStopCalls++
					return nil
				},
			}), nil
		},
	}, config.WithShutdownTimeout(time.Second))
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if err := host.StartBackground(context.Background()); !errors.Is(err, startErr) {
		t.Fatalf("StartBackground error = %v, want component start error", err)
	}
	if component.startCount != 1 || component.stopCount != 1 {
		t.Fatalf("partial assembly component lifecycle = start:%d stop:%d, want 1/1", component.startCount, component.stopCount)
	}
	if onStopCalls != 0 {
		t.Fatalf("OnStop must not run when the hook stage was never reached, got %d calls", onStopCalls)
	}

	component.startErr = nil
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("retry StartBackground failed: %v", err)
	}
	if component.startCount != 2 {
		t.Fatalf("expected component start retry, got %d starts", component.startCount)
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if component.stopCount != 2 {
		t.Fatalf("expected successful run cleanup after retry, got %d stops", component.stopCount)
	}
	// 未配置 OnStart 时，启动走到钩子阶段同样视为已进入，OnStop 必须被调用一次。
	if onStopCalls != 1 {
		t.Fatalf("OnStop-only module must receive OnStop after a successful start, got %d calls", onStopCalls)
	}
}

func TestStartBackgroundAssemblyRetainsCleanupErrorForRetry(t *testing.T) {
	startErr := errors.New("assembly component start failed")
	stopErr := errors.New("assembly component cleanup failed")
	component := &assemblyLifecycleComponent{startErr: startErr, stopErr: stopErr}
	var assembled module.IModule
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			assembled = assembly.NewModule(assembly.ModuleDescriptor{
				ID:   "assembly-retry",
				Name: "assembly-retry",
				Registrations: []assembly.Registration{{
					Lifetime:    module.SingletonLifetime,
					ServiceType: reflect.TypeOf((*capability.IRuntimeComponent)(nil)).Elem(),
					Instance:    component,
					Roles:       []assembly.Role{assembly.RoleRuntimeComponent},
				}},
			})
			return assembled, nil
		},
	}, config.WithShutdownTimeout(time.Second))
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := host.StartBackground(context.Background()); !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("StartBackground error = %v, want start and cleanup errors", err)
	}
	if component.stopCount != 1 {
		t.Fatalf("cleanup attempts = %d, want one failed attempt", component.stopCount)
	}
	component.stopErr = nil
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if component.stopCount != 2 {
		t.Fatalf("cleanup attempts after retry = %d, want 2", component.stopCount)
	}
	component.startErr = nil
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("start after cleanup retry failed: %v", err)
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("final shutdown failed: %v", err)
	}
}

func TestHostStartsModulesByDependencyAndStopsInReverseOrder(t *testing.T) {
	var starts []string
	var stops []string
	newModule := func(id string, deps ...string) module.ModuleCtor {
		return func() (module.IModule, error) {
			return &dependencyTestModule{
				testModule: &testModule{
					id: id, name: id,
					startFn: func(context.Context) (module.ModuleStopFunc, error) {
						starts = append(starts, id)
						return func(context.Context) error {
							stops = append(stops, id)
							return nil
						}, nil
					},
				},
				deps: deps,
			}, nil
		}
	}
	host := NewHost([]module.ModuleCtor{newModule("dependent", "base"), newModule("base")})
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}
	if got, want := strings.Join(starts, ","), "base,dependent"; got != want {
		t.Fatalf("start order = %q, want %q", got, want)
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if got, want := strings.Join(stops, ","), "dependent,base"; got != want {
		t.Fatalf("stop order = %q, want %q", got, want)
	}
}

// 回归 R2a-F02：含不可比较字段（slice）的值类型模块经 ModuleCtor 注册后，
// Prepare 不得因以模块实例作 map 键而 panic（hash of unhashable），
// 且依赖拓扑排序仍必须生效。
func TestPrepareAcceptsNonComparableValueTypeModules(t *testing.T) {
	var initOrder []string
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			return valueTypeTestModule{id: "dependent", name: "dependent", deps: []string{"base"}, initLog: &initOrder}, nil
		},
		func() (module.IModule, error) {
			return valueTypeTestModule{id: "base", name: "base", initLog: &initOrder}, nil
		},
	}, config.WithHTTPServer(&testHTTPServer{}))

	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare with non-comparable value-type modules failed: %v", err)
	}
	if got, want := strings.Join(initOrder, ","), "base,dependent"; got != want {
		t.Fatalf("module init order = %q, want %q", got, want)
	}
}

func TestAssemblyModuleOnStopMatchesEnteredOnStartAndIsNotRepeated(t *testing.T) {
	onStartErr := errors.New("on start failed")
	onStopCalls := 0
	var assembled module.IModule
	host := NewHost([]module.ModuleCtor{
		func() (module.IModule, error) {
			assembled = assembly.NewModule(assembly.ModuleDescriptor{
				ID:   "hook-test",
				Name: "hook-test",
				OnStart: func(context.Context) error {
					return onStartErr
				},
				OnStop: func(context.Context) error {
					onStopCalls++
					return nil
				},
			})
			return assembled, nil
		},
	})
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	stop, err := assembled.Start(context.Background())
	if !errors.Is(err, onStartErr) || stop == nil {
		t.Fatalf("Start returned stop=%v err=%v, want hook error and stop closure", stop != nil, err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("first stop failed: %v", err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
	if onStopCalls != 1 {
		t.Fatalf("OnStop calls = %d, want exactly 1", onStopCalls)
	}
}

func TestAssemblyModuleRejectsDirectRestartWhileOnStopIsPending(t *testing.T) {
	onStopErr := errors.New("on stop failed")
	onStopCalls := 0
	assembled := assembly.NewModule(assembly.ModuleDescriptor{
		ID:   "pending-stop",
		Name: "pending-stop",
		OnStart: func(context.Context) error {
			return nil
		},
		OnStop: func(context.Context) error {
			onStopCalls++
			if onStopCalls == 1 {
				return onStopErr
			}
			return nil
		},
	})
	host := NewHost([]module.ModuleCtor{func() (module.IModule, error) {
		return assembled, nil
	}})
	if err := host.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	stop, err := assembled.Start(context.Background())
	if err != nil || stop == nil {
		t.Fatalf("Start returned stop=%v err=%v, want a usable stop closure", stop != nil, err)
	}
	if err := stop(context.Background()); !errors.Is(err, onStopErr) {
		t.Fatalf("first Stop error = %v, want %v", err, onStopErr)
	}
	if _, err := assembled.Start(context.Background()); !errors.Is(err, gerrors.Conflict) {
		t.Fatalf("Start during pending OnStop = %v, want Conflict", err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	finalStop, err := assembled.Start(context.Background())
	if err != nil {
		t.Fatalf("Start after cleanup failed: %v", err)
	}
	if err := finalStop(context.Background()); err != nil {
		t.Fatalf("final cleanup failed: %v", err)
	}
}

func TestShutdownRetainsFailedModuleStopsAndSkipsTransportClose(t *testing.T) {
	t.Parallel()

	stopErr := errors.New("stop failed")
	server := &lifecycleServer{IServer: &testHTTPServer{}}
	transport := &testTransport{}
	host := &Host{
		runtime: &bootstrap.Runtime{
			HTTPServer: server,
			Transport:  transport,
		},
		moduleStops: []module.ModuleStopFunc{
			func(context.Context) error { return stopErr },
		},
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}

	err := host.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected shutdown error")
	}
	if server.stopCalls != 1 {
		t.Fatalf("expected HTTP server stop once, got %d", server.stopCalls)
	}
	if len(host.moduleStops) != 1 {
		t.Fatalf("expected failed module stop to be retained, got %d", len(host.moduleStops))
	}
	if transport.stopCalls != 0 || transport.stopWithSnapshotCalls != 0 {
		t.Fatal("transport should not close while failed module stops remain")
	}
}

func TestShutdownFailureKeepsBackgroundStartedAndRejectsRestart(t *testing.T) {
	t.Parallel()

	stopErr := errors.New("stop failed")
	startCalls := 0
	stopCalls := 0
	host := &Host{
		config: &config.Config{},
		modules: []module.IModule{
			&testModule{
				id:   "users",
				name: "users",
				startFn: func(context.Context) (module.ModuleStopFunc, error) {
					startCalls++
					return func(context.Context) error {
						stopCalls++
						return stopErr
					}, nil
				},
			},
		},
	}

	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}
	if err := host.Shutdown(context.Background()); err == nil {
		t.Fatal("expected Shutdown to fail")
	}
	if !host.backgroundStarted.Load() {
		t.Fatal("expected backgroundStarted to remain true after failed shutdown")
	}
	err := host.StartBackground(context.Background())
	if err == nil {
		t.Fatal("expected StartBackground to reject restart after failed shutdown")
	}
	if !gerrors.Is(err, gerrors.Conflict) {
		t.Fatalf("expected conflict error, got %v", err)
	}
	if startCalls != 1 {
		t.Fatalf("expected module start once, got %d", startCalls)
	}
	if stopCalls != 1 {
		t.Fatalf("expected failed module stop once, got %d", stopCalls)
	}
	if len(host.moduleStops) != 1 {
		t.Fatalf("expected failed module stop to remain for retry, got %d", len(host.moduleStops))
	}
}

func TestShutdownIgnoresAlreadyStoppedTransportConflict(t *testing.T) {
	t.Parallel()

	transport := &testTransport{
		stopErr: messaging.NewTransportAlreadyStoppedError("transport is not running"),
	}
	host := &Host{
		runtime: &bootstrap.Runtime{
			HTTPServer: &lifecycleServer{IServer: &testHTTPServer{}},
			Transport:  transport,
		},
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}

	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if transport.stopWithSnapshotCalls != 1 {
		t.Fatalf("expected StopWithSnapshot once, got %d", transport.stopWithSnapshotCalls)
	}
}

func TestShutdownIgnoresAlreadyStoppedStopOnlyTransport(t *testing.T) {
	t.Parallel()

	transport := &stopOnlyTransport{
		stopErr: messaging.NewTransportAlreadyStoppedError("transport is not running"),
	}
	host := &Host{
		runtime: &bootstrap.Runtime{
			HTTPServer: &lifecycleServer{IServer: &testHTTPServer{}},
			Transport:  transport,
		},
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}

	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if transport.stopCalls != 1 {
		t.Fatalf("expected Stop once, got %d", transport.stopCalls)
	}
}

func TestShutdownReturnsTransportStopError(t *testing.T) {
	t.Parallel()

	stopErr := errors.New("stop failed")
	transport := &stopOnlyTransport{stopErr: stopErr}
	host := &Host{
		runtime: &bootstrap.Runtime{
			HTTPServer: &lifecycleServer{IServer: &testHTTPServer{}},
			Transport:  transport,
		},
	}
	if err := host.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground failed: %v", err)
	}

	err := host.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected Shutdown to return transport stop error")
	}
	if !errors.Is(err, stopErr) {
		t.Fatalf("expected transport stop error to be preserved, got %v", err)
	}
	if transport.stopCalls != 1 {
		t.Fatalf("expected Stop once, got %d", transport.stopCalls)
	}
}

func TestShutdownHonorsCallerContext(t *testing.T) {
	t.Parallel()

	newHost := func(gotErr *error) *Host {
		return &Host{
			moduleStops: []module.ModuleStopFunc{
				func(ctx context.Context) error {
					*gotErr = ctx.Err()
					return nil
				},
			},
		}
	}

	var canceledErr error
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newHost(&canceledErr).Shutdown(canceledCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("canceled ctx error = %v, want context.Canceled", canceledErr)
	}

	var expiredErr error
	expiredCtx, expire := context.WithTimeout(context.Background(), time.Millisecond)
	defer expire()
	<-expiredCtx.Done()
	if err := newHost(&expiredErr).Shutdown(expiredCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if !errors.Is(expiredErr, context.DeadlineExceeded) {
		t.Fatalf("expired ctx error = %v, want context.DeadlineExceeded", expiredErr)
	}
}
