package nethttp

import "gochen/httpx"

// GET 注册 GET 路由。
func (s *Server) GET(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("GET", path, handler)
}

// POST 注册 POST 路由。
func (s *Server) POST(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("POST", path, handler)
}

// PUT 注册 PUT 路由。
func (s *Server) PUT(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("PUT", path, handler)
}

// DELETE 注册 DELETE 路由。
func (s *Server) DELETE(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("DELETE", path, handler)
}

// PATCH 注册 PATCH 路由。
func (s *Server) PATCH(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("PATCH", path, handler)
}

// HEAD 注册 HEAD 路由。
func (s *Server) HEAD(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("HEAD", path, handler)
}

// OPTIONS 注册 OPTIONS 路由。
func (s *Server) OPTIONS(path string, handler httpx.Handler) httpx.IServer {
	return s.addRoute("OPTIONS", path, handler)
}

// addRoute 把一条路由定义写入内部注册表。
func (s *Server) addRoute(method, path string, handler httpx.Handler) httpx.IServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := method + " " + path
	if !s.recordRouteLocked(method, path) {
		return s
	}
	s.routes[key] = &route{
		method:      method,
		pattern:     path,
		handler:     handler,
		middlewares: nil,
	}
	s.markMuxDirtyLocked()
	return s
}

// Group 创建一个带前缀的路由分组。
//
// prefix 统一规范化（前缀斜杠补齐 + 尾部斜杠剥离），避免 "api/" 之类前缀
// 在后续路由拼接中产生 "//users" 双斜杠 pattern；规范化后的前缀同时用作
// groups 表的 key，保证同一逻辑前缀复用同一 RouteGroup。
func (s *Server) Group(prefix string) httpx.IRouteGroup {
	prefix = httpx.NormalizeRoutePrefix(prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &RouteGroup{prefix: prefix, server: s, middlewares: make([]httpx.Middleware, 0)}
	s.groups[prefix] = g
	return g
}

// Use 追加全局中间件。
func (s *Server) Use(middleware ...httpx.Middleware) httpx.IServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRoutesMutableLocked(); err != nil {
		return s
	}
	s.middlewares = append(s.middlewares, middleware...)
	s.markMuxDirtyLocked()
	return s
}
