package module_test

import (
	"context"
	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/config"
	. "gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen-runtime/host/module/runtimecap"
	"testing"

	"gochen/errors"
	"gochen/httpx"
	"gochen/messaging"
)

type recordingHTTPServer struct {
	groupPrefix   string
	group         *recordingRouteGroup
	registeredGET []string
}

func (s *recordingHTTPServer) GET(path string, _ httpx.Handler) httpx.IServer {
	s.registeredGET = append(s.registeredGET, "GET "+path)
	return s
}
func (s *recordingHTTPServer) POST(_ string, _ httpx.Handler) httpx.IServer    { return s }
func (s *recordingHTTPServer) PUT(_ string, _ httpx.Handler) httpx.IServer     { return s }
func (s *recordingHTTPServer) DELETE(_ string, _ httpx.Handler) httpx.IServer  { return s }
func (s *recordingHTTPServer) PATCH(_ string, _ httpx.Handler) httpx.IServer   { return s }
func (s *recordingHTTPServer) HEAD(_ string, _ httpx.Handler) httpx.IServer    { return s }
func (s *recordingHTTPServer) OPTIONS(_ string, _ httpx.Handler) httpx.IServer { return s }

func (s *recordingHTTPServer) Group(prefix string) httpx.IRouteGroup {
	s.groupPrefix = prefix
	s.group = &recordingRouteGroup{}
	return s.group
}

func (s *recordingHTTPServer) Use(_ ...httpx.Middleware) httpx.IServer { return s }
func (s *recordingHTTPServer) Static(_, _ string) httpx.IServer        { return s }
func (s *recordingHTTPServer) ServeStatic(_, _ string)                 {}
func (s *recordingHTTPServer) Start(_ string) error                    { return nil }
func (s *recordingHTTPServer) Stop(context.Context) error              { return nil }

type recordingRouteGroup struct {
	useCalls      int
	useArgCounts  []int
	registeredGET []string
}

func (g *recordingRouteGroup) GET(path string, _ httpx.Handler) httpx.IRouteGroup {
	g.registeredGET = append(g.registeredGET, "GET "+path)
	return g
}

func (g *recordingRouteGroup) POST(_ string, _ httpx.Handler) httpx.IRouteGroup   { return g }
func (g *recordingRouteGroup) PUT(_ string, _ httpx.Handler) httpx.IRouteGroup    { return g }
func (g *recordingRouteGroup) DELETE(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }
func (g *recordingRouteGroup) PATCH(_ string, _ httpx.Handler) httpx.IRouteGroup  { return g }
func (g *recordingRouteGroup) HEAD(_ string, _ httpx.Handler) httpx.IRouteGroup   { return g }
func (g *recordingRouteGroup) OPTIONS(_ string, _ httpx.Handler) httpx.IRouteGroup {
	return g
}

func (g *recordingRouteGroup) Group(_ string) httpx.IRouteGroup { return g }

func (g *recordingRouteGroup) Use(middlewares ...httpx.Middleware) httpx.IRouteGroup {
	g.useCalls++
	g.useArgCounts = append(g.useArgCounts, len(middlewares))
	return g
}

type testModule struct {
	id   string
	name string

	initCalled  bool
	startCalled bool

	opts ModuleInitOptions
}

func (m *testModule) ID() string { return m.id }

func (m *testModule) Name() string { return m.name }

func (m *testModule) Init(opts ModuleInitOptions) error {
	m.initCalled = true
	m.opts = opts
	return nil
}

func (m *testModule) Start(_ context.Context) (ModuleStopFunc, error) {
	m.startCalled = true
	httpOptions := runtimecap.HTTPFrom(m.opts)
	if httpOptions == nil {
		return nil, nil
	}
	g := httpOptions.MountGroup()
	if g != nil {
		g.GET("/module", func(ctx httpx.IContext) error {
			return ctx.JSON(200, httpx.JSONValue(map[string]any{"ok": true}))
		})
	}
	return nil, nil
}

type startModule struct {
	*BaseModule
	startFn  func(ctx context.Context) (ModuleStopFunc, error)
	routesFn func(ctx context.Context) error
}

func (m *startModule) RegisterRoutes(ctx context.Context) error {
	if m == nil || m.routesFn == nil {
		return nil
	}
	return m.routesFn(ctx)
}

func (m *startModule) Start(ctx context.Context) (ModuleStopFunc, error) {
	if m == nil || m.startFn == nil {
		return nil, nil
	}
	return m.startFn(ctx)
}

type dupRoutesModule struct {
	*BaseModule
	opts ModuleInitOptions
}

func (m *dupRoutesModule) Init(opts ModuleInitOptions) error {
	m.opts = opts
	return nil
}

func (m *dupRoutesModule) RegisterRoutes(context.Context) error {
	if m == nil {
		return nil
	}
	httpOptions := runtimecap.HTTPFrom(m.opts)
	if httpOptions == nil {
		return nil
	}
	g := httpOptions.MountGroup()
	if g == nil {
		return nil
	}
	g.GET("/dup", func(httpx.IContext) error { return nil })
	g.GET("/dup", func(httpx.IContext) error { return nil })
	return nil
}

func (m *dupRoutesModule) Start(context.Context) (ModuleStopFunc, error) { return nil, nil }

func TestHost_ModuleInitAndStart_HTTPOptionsAndMiddlewares(t *testing.T) {
	container := basic.New()
	httpServer := &recordingHTTPServer{}
	mod := &testModule{id: "iam", name: "IAM"}

	mwGlobal1 := func(ctx httpx.IContext, next func() error) error { return next() }
	mwGlobal2 := func(ctx httpx.IContext, next func() error) error { return next() }
	mwModule := func(ctx httpx.IContext, next func() error) error { return next() }

	srv := runtime.NewHost(
		[]ModuleCtor{
			func() (IModule, error) { return mod, nil },
		},
		config.WithContainer(container),
		config.WithHTTPServer(httpServer),
		config.WithRouteMiddlewares(mwGlobal1, mwGlobal2),
		config.WithModuleHTTP("iam", config.ModuleHTTPConfig{Middlewares: []httpx.Middleware{mwModule}}),
	)

	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if !mod.initCalled || mod.opts.Registry == nil || mod.opts.Resolver == nil {
		t.Fatalf("expected module Init to receive public registry/resolver capabilities")
	}
	if _, ok := mod.opts.Registry.(di.IContainer); ok {
		t.Fatalf("registry capability must not expose full container")
	}
	if _, ok := mod.opts.Resolver.(di.IContainer); ok {
		t.Fatalf("resolver capability must not expose full container")
	}

	if httpServer.groupPrefix != "/api/v1" {
		t.Fatalf("expected group prefix %q, got %q", "/api/v1", httpServer.groupPrefix)
	}
	if httpServer.group == nil {
		t.Fatalf("expected http server Group() to be called")
	}
	if httpServer.group.useCalls != 1 || len(httpServer.group.useArgCounts) != 1 || httpServer.group.useArgCounts[0] != 3 {
		t.Fatalf("expected base group.Use to be called once with 3 middlewares, got useCalls=%d args=%v", httpServer.group.useCalls, httpServer.group.useArgCounts)
	}

	if err := srv.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
	if !mod.startCalled {
		t.Fatalf("expected module Start to be called")
	}
	// 模块 MountGroup 会再次调用 Use（模块级 middlewares）
	if httpServer.group.useCalls != 2 || len(httpServer.group.useArgCounts) != 2 || httpServer.group.useArgCounts[1] != 1 {
		t.Fatalf("expected module group.Use to be called with 1 middleware, got useCalls=%d args=%v", httpServer.group.useCalls, httpServer.group.useArgCounts)
	}

	foundHealthz := false
	foundReadyz := false
	foundMetrics := false
	foundSnapshot := false
	for _, r := range httpServer.group.registeredGET {
		switch r {
		case "GET /healthz":
			foundHealthz = true
		case "GET /readyz":
			foundReadyz = true
		case "GET /metrics":
			foundMetrics = true
		case "GET /snapshot":
			foundSnapshot = true
		}
	}
	if !foundHealthz || !foundReadyz {
		t.Fatalf("expected health routes to be registered on base group, got=%v", httpServer.group.registeredGET)
	}
	if foundMetrics || foundSnapshot {
		t.Fatalf("expected metrics/snapshot to require explicit opt-in, got=%v", httpServer.group.registeredGET)
	}

	foundModule := false
	for _, r := range httpServer.group.registeredGET {
		if r == "GET /module" {
			foundModule = true
		}
	}
	if !foundModule {
		t.Fatalf("expected module route to be registered")
	}
}

func TestHost_EnableMonitoringRoutes_RegistersMetricsAndSnapshot(t *testing.T) {
	httpServer := &recordingHTTPServer{}
	mod := &testModule{id: "iam", name: "IAM"}

	srv := runtime.NewHost(
		[]ModuleCtor{
			func() (IModule, error) { return mod, nil },
		},
		config.WithHTTPServer(httpServer),
		config.WithEnableMonitoringRoutes(true),
	)

	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	foundMetrics := false
	foundSnapshot := false
	for _, r := range httpServer.group.registeredGET {
		switch r {
		case "GET /metrics":
			foundMetrics = true
		case "GET /snapshot":
			foundSnapshot = true
		}
	}
	if !foundMetrics || !foundSnapshot {
		t.Fatalf("expected monitoring routes to be registered with opt-in, got=%v", httpServer.group.registeredGET)
	}
}

func TestHost_EnableMonitoringRoutesWorksWhenHealthRouteDisabled(t *testing.T) {
	httpServer := &recordingHTTPServer{}
	mod := &testModule{id: "iam", name: "IAM"}

	srv := runtime.NewHost(
		[]ModuleCtor{
			func() (IModule, error) { return mod, nil },
		},
		config.WithHTTPServer(httpServer),
		config.WithDisableHealthRoute(true),
		config.WithEnableMonitoringRoutes(true),
	)

	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	foundHealthz := false
	foundMetrics := false
	foundSnapshot := false
	for _, r := range httpServer.group.registeredGET {
		switch r {
		case "GET /healthz":
			foundHealthz = true
		case "GET /metrics":
			foundMetrics = true
		case "GET /snapshot":
			foundSnapshot = true
		}
	}
	if foundHealthz {
		t.Fatalf("expected health route disabled, got=%v", httpServer.group.registeredGET)
	}
	if !foundMetrics || !foundSnapshot {
		t.Fatalf("expected monitoring routes despite disabled health route, got=%v", httpServer.group.registeredGET)
	}
}

func TestHost_StartBackground_ModuleStartFail_RollbackStops(t *testing.T) {
	container := basic.New()

	stopped := false
	mod1 := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return func(context.Context) error { stopped = true; return nil }, nil
		},
	}
	mod2 := &startModule{
		BaseModule: &BaseModule{ModuleID: "m2", ModuleName: "M2"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return nil, context.Canceled
		},
	}

	srv := runtime.NewHost(
		[]ModuleCtor{
			func() (IModule, error) { return mod1, nil },
			func() (IModule, error) { return mod2, nil },
		},
		config.WithContainer(container),
	)

	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	err := srv.StartBackground(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !stopped {
		t.Fatalf("expected rollback stop to be called")
	}
}

type failingStartTransport struct {
	startErr   error
	closeCalls int
}

func (t *failingStartTransport) Publish(context.Context, messaging.IMessage) error      { return nil }
func (t *failingStartTransport) PublishAll(context.Context, []messaging.IMessage) error { return nil }
func (t *failingStartTransport) Subscribe(context.Context, string, messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error { return nil }, nil
}
func (t *failingStartTransport) Start(context.Context) error { return t.startErr }
func (t *failingStartTransport) Stop(context.Context) error {
	t.closeCalls++
	return nil
}
func (t *failingStartTransport) Stats() messaging.TransportStats { return messaging.TransportStats{} }

func TestHost_StartBackground_TransportStartFail_DoesNotRetainModuleStops(t *testing.T) {
	container := basic.New()
	stopCalls := 0
	transport := &failingStartTransport{startErr: errors.New("transport failed")}
	mod := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return func(context.Context) error {
				stopCalls++
				return nil
			}, nil
		},
	}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
		config.WithTransport(transport),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	err := srv.StartBackground(context.Background())
	if err == nil {
		t.Fatal("expected transport start error, got nil")
	}
	if stopCalls != 1 {
		t.Fatalf("expected rollback stop to be called once, got %d", stopCalls)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("expected shutdown not to rerun module stop after rollback, got %d", stopCalls)
	}
}

func TestHost_Shutdown_ClearsModuleStopsAfterSuccess(t *testing.T) {
	container := basic.New()
	stopCalls := 0
	mod := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return func(context.Context) error {
				stopCalls++
				return nil
			}, nil
		},
	}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := srv.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown returned error: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("expected module stop to run once across repeated shutdown, got %d", stopCalls)
	}
}

func TestHost_Shutdown_RetainsFailedModuleStopsForRetry(t *testing.T) {
	container := basic.New()
	stopCalls := 0
	mod := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return func(context.Context) error {
				stopCalls++
				if stopCalls == 1 {
					return errors.New("transient stop failure")
				}
				return nil
			}, nil
		},
	}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := srv.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}

	if err := srv.Shutdown(context.Background()); err == nil {
		t.Fatal("expected first Shutdown to fail")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected second Shutdown to succeed, got %v", err)
	}
	if stopCalls != 2 {
		t.Fatalf("expected failed stop to be retried once, got %d calls", stopCalls)
	}
}

func TestHost_Shutdown_SkipsTransportCloseUntilModuleStopRetrySucceeds(t *testing.T) {
	container := basic.New()
	transport := &failingStartTransport{}
	stopCalls := 0
	mod := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		startFn: func(_ context.Context) (ModuleStopFunc, error) {
			return func(context.Context) error {
				stopCalls++
				if stopCalls == 1 {
					return errors.New("transient stop failure")
				}
				return nil
			}, nil
		},
	}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
		config.WithTransport(transport),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := srv.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}

	if err := srv.Shutdown(context.Background()); err == nil {
		t.Fatal("expected first Shutdown to fail")
	}
	if transport.closeCalls != 0 {
		t.Fatalf("expected transport close to be deferred until module stops succeed, got %d", transport.closeCalls)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected second Shutdown to succeed, got %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("expected transport to close after successful module stop retry, got %d", transport.closeCalls)
	}
}

func TestHost_StartBackground_RegisterRoutesBeforeStart(t *testing.T) {
	container := basic.New()

	seq := []string{}
	mod := &startModule{
		BaseModule: &BaseModule{ModuleID: "m1", ModuleName: "M1"},
		routesFn:   func(context.Context) error { seq = append(seq, "routes"); return nil },
		startFn:    func(context.Context) (ModuleStopFunc, error) { seq = append(seq, "start"); return nil, nil },
	}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := srv.StartBackground(context.Background()); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
	if len(seq) != 2 || seq[0] != "routes" || seq[1] != "start" {
		t.Fatalf("expected call order [routes start], got %v", seq)
	}
}

func TestHost_StartBackground_FailFastOnRouteConflicts(t *testing.T) {
	container := basic.New()
	httpServer := &recordingHTTPServer{}
	mod := &dupRoutesModule{BaseModule: &BaseModule{ModuleID: "iam", ModuleName: "IAM"}}

	srv := runtime.NewHost(
		[]ModuleCtor{func() (IModule, error) { return mod, nil }},
		config.WithContainer(container),
		config.WithHTTPServer(httpServer),
		config.WithFailFastOnRouteConflicts(true),
	)
	if err := srv.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	err := srv.StartBackground(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got: %#v", err)
	}
}
