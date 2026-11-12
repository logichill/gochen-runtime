package nethttp

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"

	runtimehttp "gochen-runtime/http"
	"gochen/errors"
	"gochen/httpx"
)

// Server 承载一组模块的轻量服务实现。
type Server struct {
	mux             *http.ServeMux
	handler         *atomicHandler
	config          *runtimehttp.WebConfig
	server          *http.Server
	routes          map[string]*route
	routeCounts     map[serverRouteKey]int
	groups          map[string]*RouteGroup
	middlewares     []httpx.Middleware
	staticRoutes    []staticRoute
	muxDirty        bool
	muxExposed      bool
	mu              sync.RWMutex
	routesSealed    bool
	registrationErr error
	lifecycleMu     sync.Mutex
	lifecycle       serverLifecycleState

	trustedProxyChecker func(netip.Addr) bool
}

type serverLifecycleState uint8

const (
	serverLifecycleIdle serverLifecycleState = iota
	serverLifecycleStarting
	serverLifecycleServing
	serverLifecycleStopped
)

type route struct {
	method      string
	pattern     string
	handler     httpx.Handler
	middlewares []httpx.Middleware
}

type staticRoute struct {
	prefix string
	root   string
	single bool
}

type serverRouteKey struct {
	method string
	path   string
}

// NewServer 创建 net/http 适配层服务器。
func NewServer(config *runtimehttp.WebConfig) *Server {
	if config == nil {
		config = runtimehttp.DefaultWebConfig()
	}
	current := http.NewServeMux()
	handler := newAtomicHandler(current)
	wrapper := http.NewServeMux()
	srv := &Server{
		mux:          wrapper,
		handler:      handler,
		config:       config,
		routes:       make(map[string]*route),
		routeCounts:  make(map[serverRouteKey]int),
		groups:       make(map[string]*RouteGroup),
		middlewares:  make([]httpx.Middleware, 0),
		staticRoutes: make([]staticRoute, 0),
	}
	wrapper.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.mu.RLock()
		ready := srv.muxExposed && !srv.muxDirty
		srv.mu.RUnlock()
		if !ready {
			srv.mu.Lock()
			srv.muxExposed = true
			srv.ensureMuxLocked()
			srv.mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	return srv
}

// RouteConflicts 返回重复注册的 method+path 路由。
func (s *Server) RouteConflicts() []httpx.RouteConflict {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]httpx.RouteConflict, 0)
	for k, count := range s.routeCounts {
		if count > 1 {
			out = append(out, httpx.RouteConflict{Method: k.method, Path: k.path, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func (s *Server) recordRouteLocked(method, path string) bool {
	if err := s.requireRoutesMutableLocked(); err != nil {
		return false
	}
	method = strings.TrimSpace(strings.ToUpper(method))
	path = s.routeConflictPattern(path)
	if method == "" || path == "" {
		return false
	}
	s.routeCounts[serverRouteKey{method: method, path: path}]++
	return true
}

func (s *Server) recordMuxRouteLocked(method, path string) {
	method = strings.TrimSpace(strings.ToUpper(method))
	path = s.routeConflictPattern(path)
	if method == "" || path == "" {
		return
	}
	s.routeCounts[serverRouteKey{method: method, path: path}]++
}

func (s *Server) requireRoutesMutableLocked() error {
	if s.routesSealed {
		s.registrationErr = errors.NewCode(errors.FailedPrecondition, "route registration is sealed; assemble and validate a new server")
		return s.registrationErr
	}
	return nil
}

// Routes 从实际注册源枚举普通、静态及重定向路由，使用驱动规范路径。
func (s *Server) Routes() []httpx.RegisteredRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]httpx.RegisteredRoute, 0, len(s.routeCounts))
	seen := make(map[serverRouteKey]bool, len(s.routeCounts))
	for key := range s.routeCounts {
		out = append(out, httpx.RegisteredRoute{Method: key.method, Path: key.path})
		seen[key] = true
	}
	for key := range s.routeCounts {
		if key.method == http.MethodGet && !seen[serverRouteKey{method: http.MethodHead, path: key.path}] {
			out = append(out, httpx.RegisteredRoute{Method: http.MethodHead, Path: key.path, AliasMethod: http.MethodGet})
		}
	}
	for _, route := range s.routes {
		key := serverRouteKey{method: http.MethodOptions, path: s.routeConflictPattern(route.pattern)}
		if !seen[key] {
			out = append(out, httpx.RegisteredRoute{Method: key.method, Path: key.path, AllowReason: "HTTP protocol negotiation"})
			seen[key] = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// SealRoutes 封存实际路由；封存后注册失败通过返回错误报告。
func (s *Server) SealRoutes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routesSealed = true
	return s.registrationErr
}

// RegistrationError 返回链式注册 API 拒绝最近一次修改的错误。
func (s *Server) RegistrationError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.registrationErr
}

// RoutePath 返回当前组在驱动中的规范注册路径。
func (g *RouteGroup) RoutePath(relative string) string {
	return g.server.routeConflictPattern(g.prefix + httpx.NormalizeRoutePath(relative))
}

func (s *Server) routeConflictPattern(path string) string {
	path = normalizeRoutePath(path)
	if path == "" {
		return ""
	}
	return s.convertPathPattern(path)
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
