package nethttp

import (
	"net/http"
	"reflect"
	"sort"
	"strings"

	"gochen-runtime/internal/httpheader"
	"gochen/httpx"
)

func (s *Server) registerRoutes() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureMuxLocked()
}

func (s *Server) rebuildMuxLocked() {
	mux := http.NewServeMux()

	for _, static := range s.staticRoutes {
		if static.single {
			s.registerStaticFile(mux, static.prefix, static.root)
			continue
		}
		s.registerStaticRoute(mux, static.prefix, static.root)
	}

	type optionsMeta struct {
		rawPattern        string
		middlewares       []httpx.Middleware
		allowMethods      map[string]struct{}
		hasExplicitOption bool
	}

	metaByPattern := make(map[string]*optionsMeta)
	routeKeys := make([]string, 0, len(s.routes))
	for key := range s.routes {
		routeKeys = append(routeKeys, key)
	}
	sort.Strings(routeKeys)

	for _, key := range routeKeys {
		r := s.routes[key]
		if r == nil {
			continue
		}
		pattern := s.convertPathPattern(r.pattern)
		meta := metaByPattern[pattern]
		if meta == nil {
			meta = &optionsMeta{
				rawPattern:   r.pattern,
				middlewares:  append([]httpx.Middleware{}, r.middlewares...),
				allowMethods: make(map[string]struct{}),
			}
			metaByPattern[pattern] = meta
		} else if len(r.middlewares) > 0 {
			// 同一路径的不同方法可能来自不同 group；合并为稳定链路，避免 map 遍历导致预检语义随机。
			meta.middlewares = mergeMiddlewares(meta.middlewares, r.middlewares)
		}
		meta.allowMethods[r.method] = struct{}{}
		if r.method == http.MethodOptions {
			meta.hasExplicitOption = true
		}
	}

	for _, key := range routeKeys {
		r := s.routes[key]
		if r == nil {
			continue
		}
		route := r // 捕获当前循环变量副本，避免闭包引用同一变量
		pattern := s.convertPathPattern(route.pattern)
		handler := s.createHandler(route, append([]httpx.Middleware{}, s.middlewares...))

		// Go 1.22+ net/http ServeMux 支持 "METHOD /path" 模式。
		// 为避免同一路径在不同 HTTP 方法下重复注册导致冲突，这里仅注册 method-specific pattern。
		methodPattern := route.method + " " + pattern

		mux.HandleFunc(methodPattern, func(w http.ResponseWriter, req *http.Request) {
			// Go 1.22+ ServeMux 使用 "METHOD /path" 模式注册后，只有匹配的方法才会路由到此 handler，
			// 无需在 handler 内部再次检查方法。
			handler(w, req)
		})
	}

	// 为每个路径补充一次 OPTIONS pattern，用于 CORS 预检：
	// - 必须走标准 handler/middleware 链，避免绕过 CORS/鉴权/观测等能力；
	// - 若调用方显式注册了 OPTIONS，则不做自动注册，避免冲突。
	for pattern, meta := range metaByPattern {
		if meta == nil || meta.hasExplicitOption {
			continue
		}
		allowHeader := httpheader.BuildAllowHeader(meta.allowMethods)
		optionsRoute := &route{
			method:      http.MethodOptions,
			pattern:     meta.rawPattern,
			middlewares: append([]httpx.Middleware{}, meta.middlewares...),
			handler: func(ctx httpx.IContext) error {
				if allowHeader != "" {
					ctx.SetHeader("Allow", allowHeader)
				}
				return ctx.String(http.StatusNoContent, "")
			},
		}
		optionsHandler := s.createHandler(optionsRoute, append([]httpx.Middleware{}, s.middlewares...))
		optionsPattern := http.MethodOptions + " " + pattern
		mux.HandleFunc(optionsPattern, func(w http.ResponseWriter, req *http.Request) {
			optionsHandler(w, req)
		})
	}

	s.muxDirty = false
	if s.handler != nil {
		s.handler.Store(mux)
	}
}

func (s *Server) markMuxDirtyLocked() {
	s.muxDirty = true
	if s.serverActive() || s.muxExposed {
		s.rebuildMuxLocked()
	}
}

func (s *Server) ensureMuxLocked() {
	if !s.muxDirty {
		return
	}
	s.rebuildMuxLocked()
}

func mergeMiddlewares(base []httpx.Middleware, extra []httpx.Middleware) []httpx.Middleware {
	if len(extra) == 0 {
		return append([]httpx.Middleware{}, base...)
	}
	out := append([]httpx.Middleware{}, base...)
	prefix := commonMiddlewarePrefix(out, extra)
	for _, mw := range extra[prefix:] {
		if mw == nil {
			continue
		}
		out = append(out, mw)
	}
	return out
}

func commonMiddlewarePrefix(base []httpx.Middleware, extra []httpx.Middleware) int {
	limit := len(base)
	if len(extra) < limit {
		limit = len(extra)
	}
	for i := 0; i < limit; i++ {
		if !sameMiddleware(base[i], extra[i]) {
			return i
		}
	}
	return limit
}

func sameMiddleware(left httpx.Middleware, right httpx.Middleware) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}

// convertPathPattern 转换PathPattern。
func (s *Server) convertPathPattern(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "{" + p[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

// createHandler 创建处理器。
func (s *Server) createHandler(r *route, globalMiddlewares []httpx.Middleware) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, err := NewBaseContext(w, req)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		ctx.trustedProxyChecker = s.isTrustedProxy
		s.parsePathParams(ctx, r.pattern, req)
		// 组装中间件链：全局 -> 路由级
		middlewares := append([]httpx.Middleware{}, globalMiddlewares...)
		if len(r.middlewares) > 0 {
			middlewares = append(middlewares, r.middlewares...)
		}
		// 执行
		if err := s.executeMiddlewareChain(ctx, middlewares, r.handler); err != nil {
			_ = WriteErrorResponse(ctx, err)
		}
		ctx.flushStatus()
	}
}

// parsePathParams 解析PathParams。
func (s *Server) parsePathParams(ctx *Context, pattern string, req *http.Request) {
	parts := strings.Split(strings.Trim(pattern, "/"), "/")
	params := make(map[string]string)
	for _, part := range parts {
		if strings.HasPrefix(part, ":") {
			name := part[1:]
			if v := req.PathValue(name); v != "" {
				params[name] = v
			}
		}
	}
	ctx.SetParams(params)
}

func (s *Server) executeMiddlewareChain(ctx httpx.IContext, middlewares []httpx.Middleware, handler httpx.Handler) error {
	if ctx.IsAborted() {
		return nil
	}
	if len(middlewares) == 0 {
		return handler(ctx)
	}
	return middlewares[0](ctx, func() error { return s.executeMiddlewareChain(ctx, middlewares[1:], handler) })
}
