package testutil

import "gochen/httpx"

// MockRouteGroup 用于测试的路由分组 mock。
type MockRouteGroup struct {
	Handlers    map[string]httpx.Handler
	Routes      []string
	Middlewares []httpx.Middleware
	SubGroups   []*MockRouteGroup
}

// NewMockRouteGroup 创建一个用于测试注册行为的空路由分组。
func NewMockRouteGroup() *MockRouteGroup {
	return &MockRouteGroup{
		Handlers: make(map[string]httpx.Handler),
	}
}

func (m *MockRouteGroup) record(method, path string, handler httpx.Handler) {
	if m.Handlers == nil {
		m.Handlers = make(map[string]httpx.Handler)
	}
	m.Handlers[method+" "+path] = handler
	m.Routes = append(m.Routes, method+":"+path)
}

// GET 记录一条 GET 路由。
func (m *MockRouteGroup) GET(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("GET", path, handler)
	return m
}

// POST 记录一条 POST 路由。
func (m *MockRouteGroup) POST(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("POST", path, handler)
	return m
}

// PUT 记录一条 PUT 路由。
func (m *MockRouteGroup) PUT(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("PUT", path, handler)
	return m
}

// DELETE 记录一条 DELETE 路由。
func (m *MockRouteGroup) DELETE(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("DELETE", path, handler)
	return m
}

// PATCH 记录一条 PATCH 路由。
func (m *MockRouteGroup) PATCH(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("PATCH", path, handler)
	return m
}

// HEAD 记录一条 HEAD 路由。
func (m *MockRouteGroup) HEAD(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("HEAD", path, handler)
	return m
}

// OPTIONS 记录一条 OPTIONS 路由。
func (m *MockRouteGroup) OPTIONS(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record("OPTIONS", path, handler)
	return m
}

// Group 创建或嵌套子路由组。
func (m *MockRouteGroup) Group(prefix string) httpx.IRouteGroup {
	_ = prefix
	sub := NewMockRouteGroup()
	m.SubGroups = append(m.SubGroups, sub)
	return sub
}

// Use 记录中间件。
func (m *MockRouteGroup) Use(middleware ...httpx.Middleware) httpx.IRouteGroup {
	m.Middlewares = append(m.Middlewares, middleware...)
	return m
}

var _ httpx.IRouteGroup = (*MockRouteGroup)(nil)
