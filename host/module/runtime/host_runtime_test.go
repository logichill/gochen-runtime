package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/bootstrap"
	"gochen-runtime/host/module"
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
	if t.startErr == nil {
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
