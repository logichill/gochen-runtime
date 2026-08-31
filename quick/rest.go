package quick

import (
	"gochen-runtime/api/rest"
	"gochen/app/crud"
	"gochen/domain"
	"gochen/httpx"
)

// RESTRegistrar 生成匹配 module 路由注册器签名的闭包。
//
// 一行代码将 Application 的标准 RESTful CRUD 路由绑定到模块中。
func RESTRegistrar[T domain.IEntity[ID], ID comparable](
	app crud.IApplication[T, ID],
	opts ...rest.Option[T, ID],
) func(httpx.IRouteGroup) error {
	return func(group httpx.IRouteGroup) error {
		return rest.Register[T, ID](group, app, opts...)
	}
}

// RESTRegister 直接将 Application 的 RESTful CRUD 路由挂载到指定 IRouteGroup。
func RESTRegister[T domain.IEntity[ID], ID comparable](
	group httpx.IRouteGroup,
	app crud.IApplication[T, ID],
	opts ...rest.Option[T, ID],
) error {
	return rest.Register[T, ID](group, app, opts...)
}
