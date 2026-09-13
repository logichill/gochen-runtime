package nethttp

import (
	"context"
	"net/http"
	"net/netip"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/request"
	"gochen/errors"
)

// Start 启动 HTTP 服务，配置构建与校验由 Runtime 的共享入口完成。
func (s *Server) Start(addr string) error {
	if err := s.beginStart(); err != nil {
		return err
	}
	checker, err := request.NewTrustedProxyChecker(s.config.TrustedProxies)
	if err != nil {
		return s.failStart(err)
	}
	s.mu.Lock()
	s.trustedProxyChecker = checker
	s.mu.Unlock()

	s.registerRoutes()
	srv, err := runtimehttp.NewHTTPServer(s.config, addr, s.mux)
	if err != nil {
		return s.failStart(err)
	}
	if err := s.publishServer(srv); err != nil {
		return err
	}
	if s.config.TLSEnabled {
		return s.finishServe(srv, srv.ListenAndServeTLS(s.config.CertFile, s.config.KeyFile))
	}
	return s.finishServe(srv, srv.ListenAndServe())
}

func (s *Server) beginStart() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	switch s.lifecycle {
	case serverLifecycleStopped:
		return http.ErrServerClosed
	case serverLifecycleStarting, serverLifecycleServing:
		return errors.NewCode(errors.Conflict, "http server is already starting or running")
	default:
		s.lifecycle = serverLifecycleStarting
		return nil
	}
}

func (s *Server) failStart(startErr error) error {
	s.lifecycleMu.Lock()
	stopped := s.lifecycle == serverLifecycleStopped
	if !stopped {
		s.lifecycle = serverLifecycleIdle
	}
	s.lifecycleMu.Unlock()
	if stopped {
		return http.ErrServerClosed
	}
	return startErr
}

func (s *Server) publishServer(srv *http.Server) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycle == serverLifecycleStopped {
		return http.ErrServerClosed
	}
	if s.lifecycle != serverLifecycleStarting {
		return errors.NewCode(errors.Conflict, "http server start state changed unexpectedly")
	}
	s.server = srv
	s.lifecycle = serverLifecycleServing
	return nil
}

func (s *Server) finishServe(srv *http.Server, serveErr error) error {
	s.lifecycleMu.Lock()
	if s.server == srv {
		s.server = nil
	}
	if s.lifecycle != serverLifecycleStopped {
		s.lifecycle = serverLifecycleIdle
	}
	s.lifecycleMu.Unlock()
	return serveErr
}

func (s *Server) serverActive() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.lifecycle == serverLifecycleStarting || s.lifecycle == serverLifecycleServing
}

func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	s.mu.RLock()
	checker := s.trustedProxyChecker
	s.mu.RUnlock()
	return checker != nil && checker(addr)
}

// Stop 在给定上下文约束下优雅关闭底层 HTTP 服务。
func (s *Server) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	s.lifecycleMu.Lock()
	s.lifecycle = serverLifecycleStopped
	srv := s.server
	s.lifecycleMu.Unlock()
	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

// Handler 返回底层 http.Handler。
func (s *Server) Handler() http.Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.muxExposed = true
	s.ensureMuxLocked()
	return s.mux
}

// ServeMux 返回底层 *http.ServeMux。
func (s *Server) ServeMux() *http.ServeMux {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.muxExposed = true
	s.ensureMuxLocked()
	return s.mux
}
