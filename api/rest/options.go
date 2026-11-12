package rest

import (
	"gochen-runtime/http/middleware"
	"gochen/app/query"

	"gochen/domain"
	"gochen/httpx"
)

// WithBasePath 设置资源基础路径。
func WithBasePath[T domain.IEntity[ID], ID comparable](path string) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.config.Routing.BasePath = path
	}
}

// WithMiddleware 追加路由中间件。
func WithMiddleware[T domain.IEntity[ID], ID comparable](middlewares ...httpx.Middleware) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.middlewares = append(b.middlewares, middlewares...)
	}
}

// WithAuth 为 REST 路由追加认证/鉴权中间件。
func WithAuth[T domain.IEntity[ID], ID comparable](middleware httpx.Middleware) Option[T, ID] {
	return WithMiddleware[T, ID](middleware)
}

// WithLogging 为 REST 路由追加日志中间件。
func WithLogging[T domain.IEntity[ID], ID comparable](middleware httpx.Middleware) Option[T, ID] {
	return WithMiddleware[T, ID](middleware)
}

// WithPagination 配置分页默认值与上限。
func WithPagination[T domain.IEntity[ID], ID comparable](defaultSize, maxSize int) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if defaultSize > 0 {
			b.config.Query.DefaultPageSize = defaultSize
		}
		if maxSize > 0 {
			b.config.Query.MaxPageSize = maxSize
		}
	}
}

// WithPaginationEnabled 控制列表路由是否返回分页结果。
func WithPaginationEnabled[T domain.IEntity[ID], ID comparable](enabled bool) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.config.Query.EnablePagination = enabled
	}
}

// WithQuerySchema 配置动态查询 schema。
func WithQuerySchema[T domain.IEntity[ID], ID comparable](schema *query.QuerySchema) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.config.Query.QuerySchema = schema
		applyQuerySchemaDefaults(b.config)
	}
}

// WithMaxBulkSize 限制 HTTP 批量元素数。
func WithMaxBulkSize[T domain.IEntity[ID], ID comparable](size int) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if size > 0 {
			b.config.Body.MaxBulkSize = size
		}
	}
}

// WithMaxBodySize 设置请求体大小上限。
func WithMaxBodySize[T domain.IEntity[ID], ID comparable](size int64) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if size > 0 {
			b.config.Body.MaxBodySize = size
		}
	}
}

// WithValidator 设置协议层请求体校验函数。
func WithValidator[T domain.IEntity[ID], ID comparable](validator func(any) error) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.config.Body.Validator = validator
	}
}

// WithOperatorExtractor 配置操作人提取函数。
func WithOperatorExtractor[T domain.IEntity[ID], ID comparable](extractor func(httpx.IContext) (string, bool)) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		b.config.Audit.OperatorExtractor = extractor
	}
}

// WithCORS 配置允许的来源列表。
func WithCORS[T domain.IEntity[ID], ID comparable](origins []string) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if b.config.HTTP.CORS == nil {
			b.config.HTTP.CORS = &middleware.CORSConfig{
				AllowMethods: middleware.DefaultCORSAllowMethods(),
				AllowHeaders: middleware.DefaultCORSAllowHeaders(),
				MaxAge:       middleware.DefaultCORSMaxAge,
			}
		}
		b.config.HTTP.CORS.AllowOrigins = origins
		b.cors = nil
	}
}

// WithErrorHandler 设置错误处理器。
func WithErrorHandler[T domain.IEntity[ID], ID comparable](handler func(httpx.IContext, error) httpx.IResponse) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if handler != nil {
			b.config.Response.ErrorHandler = handler
		}
	}
}

// WithResponseWrapper 设置成功响应包装器。
func WithResponseWrapper[T domain.IEntity[ID], ID comparable](wrapper func(any) any) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if wrapper != nil {
			b.config.Response.ResponseWrapper = wrapper
		}
	}
}

// WithHTTP201ForCreate 创建资源返回 201 Created。
func WithHTTP201ForCreate[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) { b.config.Response.UseHTTP201ForCreate = true }
}

// WithFastDeny 启用编译计划提供的安全提前拒绝，检查器与正式授权共用。
func WithFastDeny[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) { b.fastDenyEnabled = true }
}

// WithStartupRegistry 让 Mount 同时把应用登记进 Host 启动清单（D17）。
func WithStartupRegistry[T domain.IEntity[ID], ID comparable](registry IStartupRegistry) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) { b.registry = registry }
}
