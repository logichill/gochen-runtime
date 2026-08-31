package host_test

import (
	"context"
	"gochen-runtime/di/basic"
	"sync"
	"testing"
	"time"

	"gochen-runtime/host"
	"gochen-runtime/host/config"
	"gochen-runtime/host/engine"
	"gochen/httpx"
)

// minimalHTTPServer 为并发测试提供的 IServer 实现，只记录 Start/Stop 调用。
type minimalHTTPServer struct {
	mu         sync.Mutex
	started    bool
	stopped    bool
	startCalls int
	stopCalls  int
}

// GET result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) GET(_ string, _ httpx.Handler) httpx.IServer { return s }

// POST result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) POST(_ string, _ httpx.Handler) httpx.IServer { return s }

// PUT result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) PUT(_ string, _ httpx.Handler) httpx.IServer { return s }

// DELETE result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) DELETE(_ string, _ httpx.Handler) httpx.IServer { return s }

// PATCH result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) PATCH(_ string, _ httpx.Handler) httpx.IServer { return s }

// HEAD result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) HEAD(_ string, _ httpx.Handler) httpx.IServer { return s }

// OPTIONS result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) OPTIONS(_ string, _ httpx.Handler) httpx.IServer {
	return s
}

// Group result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) Group(_ string) httpx.IRouteGroup { return &noopRouteGroup{} }

// Use 追加中间件。
//
// 参数：
//
// 返回：
// - result：测试返回值（类型：gochenhttp.IServer）
func (s *minimalHTTPServer) Use(_ ...httpx.Middleware) httpx.IServer { return s }

// Static result：测试返回值（类型：gochenhttp.IServer）。
//
// 参数：
//
// 返回：
func (s *minimalHTTPServer) Static(_, _ string) httpx.IServer { return s }

// ServeStatic 提供服务生命周期能力。
//
// 参数：
func (s *minimalHTTPServer) ServeStatic(_, _ string) {}

// Start 启动数据。
//
// 参数：
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *minimalHTTPServer) Start(_ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	s.started = true
	return nil
}

// Stop 在 ctx 约束下停止后台任务并释放资源。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *minimalHTTPServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopCalls++
	s.stopped = true
	return nil
}

type noopRouteGroup struct{}

// GET result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) GET(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// POST result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) POST(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// PUT result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) PUT(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// DELETE result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) DELETE(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// PATCH result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) PATCH(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// HEAD result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) HEAD(_ string, _ httpx.Handler) httpx.IRouteGroup { return g }

// OPTIONS result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) OPTIONS(_ string, _ httpx.Handler) httpx.IRouteGroup {
	return g
}

// Group result：测试返回值（类型：gochenhttp.IRouteGroup）。
//
// 参数：
//
// 返回：
func (g *noopRouteGroup) Group(_ string) httpx.IRouteGroup { return g }

// Use 追加中间件。
//
// 参数：
//
// 返回：
// - result：测试返回值（类型：gochenhttp.IRouteGroup）
func (g *noopRouteGroup) Use(_ ...httpx.Middleware) httpx.IRouteGroup { return g }

// TestHost_WithEngine_ConcurrentSafe 验证 Host 与 Engine 的并发交互安全。
func TestHost_WithEngine_ConcurrentSafe(t *testing.T) {
	httpServer := &minimalHTTPServer{}
	container := basic.New()

	runner, err := host.New(
		config.WithModules(),
		config.WithContainer(container),
		config.WithHTTPServer(httpServer),
		config.WithShutdownTimeout(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("host.New returned error: %v", err)
	}

	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Engine.Start with Host returned error: %v", err)
	}

	if runner.State() != engine.StateStopped {
		t.Fatalf("expected engine state %v, got %v", engine.StateStopped, runner.State())
	}

	httpServer.mu.Lock()
	defer httpServer.mu.Unlock()
	if httpServer.startCalls == 0 || httpServer.stopCalls == 0 {
		t.Fatalf("expected HTTP server Start/Stop to be called at least once, got startCalls=%d stopCalls=%d",
			httpServer.startCalls, httpServer.stopCalls)
	}
	if !httpServer.started || !httpServer.stopped {
		t.Fatalf("expected HTTP server started/stopped flags to be true, got started=%v stopped=%v", httpServer.started, httpServer.stopped)
	}
}
