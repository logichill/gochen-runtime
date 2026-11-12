package rest

import (
	runtimehttp "gochen-runtime/http"
	"gochen/domain"
)

// IEntryRegistry 登记实际路由对应的执行绑定；实际入口由 HTTP 驱动独立枚举。
type IEntryRegistry = runtimehttp.IEntryRegistry

// WithEntryRegistry 将标准路由与声明式 Application 的条件执行契约绑定。
func WithEntryRegistry[T domain.IEntity[ID], ID comparable](registry IEntryRegistry, module string) Option[T, ID] {
	return func(builder *RouteBuilder[T, ID]) { builder.entryRegistry = registry; builder.entryModule = module }
}
