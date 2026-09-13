package nethttp

import "gochen/httpx"

// RouteGroup 定义带 prefix 与中间件快照语义的路由分组。
//
// 说明：
// - 子路由 path 会拼接在 group prefix 之后；
// - 路由注册时捕获当前 group 的中间件快照，后续 Use 不会回追到已注册路由。
type RouteGroup struct {
	prefix      string
	server      *Server
	middlewares []httpx.Middleware
}

// GET 在当前 group 上注册 GET 路由。
func (g *RouteGroup) GET(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("GET", path, h)
}

// POST 在当前 group 上注册 POST 路由。
func (g *RouteGroup) POST(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("POST", path, h)
}

// PUT 在当前 group 上注册 PUT 路由。
func (g *RouteGroup) PUT(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("PUT", path, h)
}

// DELETE 在当前 group 上注册 DELETE 路由。
func (g *RouteGroup) DELETE(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("DELETE", path, h)
}

// PATCH 在当前 group 上注册 PATCH 路由。
func (g *RouteGroup) PATCH(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("PATCH", path, h)
}

// HEAD 在当前 group 上注册 HEAD 路由。
func (g *RouteGroup) HEAD(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("HEAD", path, h)
}

// OPTIONS 在当前 group 上注册 OPTIONS 路由。
func (g *RouteGroup) OPTIONS(path string, h httpx.Handler) httpx.IRouteGroup {
	return g.add("OPTIONS", path, h)
}

// Group 创建子 group：拼接 prefix 并继承父 group 的中间件快照（子 group 的 Use 不会回写到父 group）。
func (g *RouteGroup) Group(prefix string) httpx.IRouteGroup {
	child := g.server.Group(g.prefix + prefix)
	if rg, ok := child.(*RouteGroup); ok && rg != nil {
		rg.middlewares = append([]httpx.Middleware{}, g.middlewares...)
	}
	return child
}

// Use 追加中间件，仅作用于此后在本 group 上注册的路由（已注册路由保留其注册时的快照）。
func (g *RouteGroup) Use(mw ...httpx.Middleware) httpx.IRouteGroup {
	g.middlewares = append(g.middlewares, mw...)
	return g
}

// add 在 server.routes 上以 "METHOD pattern" 为 key 注册路由，并附上 group 中间件快照。
//
// 说明：
// - 中间件以快照形式落到 route.middlewares，避免通过 handler wrapper 隐式传播；
// - 这样 Server 在自动注册 OPTIONS 等场景下可以复用相同的 middleware 语义。
func (g *RouteGroup) add(method, path string, h httpx.Handler) httpx.IRouteGroup {
	full := g.prefix + path
	snapshot := append([]httpx.Middleware{}, g.middlewares...)
	g.server.mu.Lock()
	defer g.server.mu.Unlock()
	g.server.recordRouteLocked(method, full)
	g.server.routes[method+" "+full] = &route{method: method, pattern: full, handler: h, middlewares: snapshot}
	g.server.markMuxDirtyLocked()
	return g
}

// wrapWithMiddlewares 将 handler 包成执行给定中间件链后再调用 h 的形态。
func (g *RouteGroup) wrapWithMiddlewares(h httpx.Handler, middlewares []httpx.Middleware) httpx.Handler {
	return func(ctx httpx.IContext) error {
		return g.server.executeMiddlewareChain(ctx, middlewares, h)
	}
}
