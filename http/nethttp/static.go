package nethttp

import (
	"net/http"
	"net/url"
	"strings"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/internal/httpheader"
	"gochen/httpx"
)

func (s *Server) Static(prefix, root string) httpx.IServer {
	return s.static(prefix, root)
}

func (s *Server) static(prefix, root string) httpx.IServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRoutesMutableLocked(); err != nil {
		return s
	}

	s.recordStaticRouteLocked(prefix)
	s.staticRoutes = append(s.staticRoutes, staticRoute{prefix: prefix, root: root})
	s.markMuxDirtyLocked()
	return s
}

func (s *Server) registerStaticRoute(mux *http.ServeMux, prefix, root string) error {
	if prefix == "" {
		prefix = "/"
	}
	pattern := prefix
	if !strings.HasSuffix(pattern, "/") {
		pattern += "/"
	}
	// 显式挂载跳转，保证挂载点的无斜线请求也执行全局中间件。
	if base := strings.TrimSuffix(pattern, "/"); base != "" {
		if err := registerMuxHandler(mux, base, s.wrapStaticHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			location := url.URL{Path: pattern, RawQuery: r.URL.RawQuery}
			http.Redirect(w, r, location.String(), http.StatusMovedPermanently)
		}))); err != nil {
			return err
		}
	}

	h := runtimehttp.NewStaticHandler(root)
	return registerMuxHandler(mux, pattern, s.wrapStaticHandler(http.StripPrefix(pattern, h)))
}

func (s *Server) ServeStatic(path, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRoutesMutableLocked(); err != nil {
		return
	}

	s.recordMuxRouteLocked(http.MethodGet, path)
	s.recordMuxRouteLocked(http.MethodHead, path)
	s.staticRoutes = append(s.staticRoutes, staticRoute{prefix: path, root: root, single: true})
	s.markMuxDirtyLocked()
}

func (s *Server) recordStaticRouteLocked(prefix string) {
	p := normalizeRoutePath(prefix)
	if p == "" {
		p = "/"
	}
	pattern := p
	if !strings.HasSuffix(pattern, "/") {
		pattern += "/"
	}
	s.recordMuxRouteLocked(http.MethodGet, pattern)
	s.recordMuxRouteLocked(http.MethodHead, pattern)
	if base := strings.TrimSuffix(pattern, "/"); base != "" {
		s.recordMuxRouteLocked(http.MethodGet, base)
		s.recordMuxRouteLocked(http.MethodHead, base)
	}
}

func (s *Server) registerStaticFile(mux *http.ServeMux, path, root string) error {
	return registerMuxHandler(mux, path, s.wrapStaticHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("X-Content-Type-Options") == "" {
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		http.ServeFile(w, r, root)
	})))
}

func (s *Server) wrapStaticHandler(handler http.Handler) http.Handler {
	return s.createHandler(&route{handler: func(ctx httpx.IContext) error {
		if ctx.Method() != http.MethodGet && ctx.Method() != http.MethodHead {
			ctx.SetHeader("Allow", httpheader.BuildAllowHeader(map[string]struct{}{
				http.MethodGet:  {},
				http.MethodHead: {},
			}))
			return ctx.String(http.StatusMethodNotAllowed, http.StatusText(http.StatusMethodNotAllowed))
		}
		current := ctx.(*Context)
		handler.ServeHTTP(current.ResponseWriter(), current.Request())
		return nil
	}}, append([]httpx.Middleware(nil), s.middlewares...))
}
