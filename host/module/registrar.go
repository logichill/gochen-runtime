package module

import "gochen/httpx"

// IRouteRegistrar 路由注册器接口。
type IRouteRegistrar interface {
	RegisterRoutes(group httpx.IRouteGroup) error
}

// RouteRegistrarFunc 将路由函数转换为显式注册器。
type RouteRegistrarFunc func(httpx.IRouteGroup) error

// RegisterRoutes 调用路由声明函数。
func (f RouteRegistrarFunc) RegisterRoutes(group httpx.IRouteGroup) error { return f(group) }
