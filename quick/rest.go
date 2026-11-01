package quick

import (
	"gochen-runtime/api/rest"
	"gochen/domain"
	"gochen/httpx"
)

// RESTRegistrar 生成匹配 module 路由注册器签名的闭包。
func RESTRegistrar[T domain.IEntity[ID], ID comparable](
	app rest.Application[T, ID],
	opts ...rest.Option[T, ID],
) func(httpx.IRouteGroup) error {
	return func(group httpx.IRouteGroup) error {
		return rest.Mount[T, ID](group, app, opts...)
	}
}

// RESTRegister 将 Application 的标准 REST 路由挂载到指定 IRouteGroup。
func RESTRegister[T domain.IEntity[ID], ID comparable](
	group httpx.IRouteGroup,
	app rest.Application[T, ID],
	opts ...rest.Option[T, ID],
) error {
	return rest.Mount[T, ID](group, app, opts...)
}
