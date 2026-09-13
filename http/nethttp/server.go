package nethttp

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"

	runtimehttp "gochen-runtime/http"
	"gochen/httpx"
)

// Server 承载一组模块的轻量服务实现。
type Server struct {
	mux          *http.ServeMux
	handler      *atomicHandler
	config       *runtimehttp.WebConfig
	server       *http.Server
	routes       map[string]*route
	routeCounts  map[serverRouteKey]int
	groups       map[string]*RouteGroup
	middlewares  []httpx.Middleware
	staticRoutes []staticRoute
	muxDirty     bool
	muxExposed   bool
	mu           sync.RWMutex
	lifecycleMu  sync.Mutex
	lifecycle    serverLifecycleState

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

func (s *Server) recordRouteLocked(method, path string) {
	method = strings.TrimSpace(strings.ToUpper(method))
	path = s.routeConflictPattern(path)
	if method == "" || path == "" {
		return
	}
	s.routeCounts[serverRouteKey{method: method, path: path}]++
}

func (s *Server) recordMuxRouteLocked(method, path string) {
	method = strings.TrimSpace(strings.ToUpper(method))
	path = s.routeConflictPattern(path)
	if method == "" || path == "" {
		return
	}
	s.routeCounts[serverRouteKey{method: method, path: path}]++
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
