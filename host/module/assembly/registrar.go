package assembly

import "gochen/httpx"

// IRouteRegistrar 路由注册器接口。
type IRouteRegistrar interface {
	RegisterRoutes(group httpx.IRouteGroup) error
}
