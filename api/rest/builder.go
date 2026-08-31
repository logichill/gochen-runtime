// Package rest 提供 RESTful CRUD 路由的构建与注册入口。
package rest

import (
	"gochen-runtime/http/middleware"
	"gochen/app/crud"
	"gochen/app/query"
	"gochen/domain"
	"gochen/errors"
	"gochen/httpx"
	"gochen/validate"
)

// Option 用于按需修改 ApiBuilder 的路由和服务配置。
type Option[T domain.IEntity[ID], ID comparable] func(*ApiBuilder[T, ID])

// cloneServiceConfig 复制服务配置。
func cloneServiceConfig(cfg *crud.ServiceConfig) *crud.ServiceConfig {
	if cfg == nil {
		def := crud.DefaultServiceConfig()
		copyCfg := *def
		return &copyCfg
	}
	clone := *cfg
	return &clone
}

// IApiBuilder 定义 CRUD API 构建器暴露的核心能力。
type IApiBuilder[T domain.IEntity[ID], ID comparable] interface {
	// Route 配置 CRUD 路由的注册行为（如分页/批量/鉴权等）。
	Route(config func(*RouteConfig[ID])) IApiBuilder[T, ID]

	// Service 配置 Application 的服务行为（如批量上限/分页上限等）。
	Service(config func(*crud.ServiceConfig)) IApiBuilder[T, ID]

	// Hooks 配置 Application 的写入生命周期钩子。
	Hooks(config func(*crud.Hooks[T, ID])) IApiBuilder[T, ID]

	// Middleware 为构建出来的 CRUD 路由追加中间件（按注册顺序执行）。
	Middleware(middlewares ...httpx.Middleware) IApiBuilder[T, ID]

	// Build 生成并把 CRUD 路由注册到指定路由组。
	Build(group httpx.IRouteGroup) error
}

// ApiBuilder 负责把 Application 能力组装成一组标准 CRUD 路由。
type ApiBuilder[T domain.IEntity[ID], ID comparable] struct {
	routeConfig       *RouteConfig[ID]
	serviceConfig     *crud.ServiceConfig
	serviceConfigured bool
	middlewares       []httpx.Middleware
	service           crud.IApplication[T, ID]
	validator         validate.IValidator
	hooks             *crud.Hooks[T, ID]
}

// NewApiBuilder 创建一个可继续配置的 CRUD API 构建器。
func NewApiBuilder[T domain.IEntity[ID], ID comparable](
	svc crud.IApplication[T, ID],
	options ...Option[T, ID],
) (*ApiBuilder[T, ID], error) {
	if isNilService(svc) {
		return nil, errors.NewCode(errors.InvalidInput, "service is nil")
	}
	routeCfg := DefaultRouteConfig[ID]()

	svcConfig := cloneServiceConfig(svc.Config())

	builder := &ApiBuilder[T, ID]{
		routeConfig:   routeCfg,
		serviceConfig: svcConfig,
		service:       svc,
	}

	for _, opt := range options {
		if opt != nil {
			opt(builder)
		}
	}
	return builder, nil
}

// Route 配置 CRUD 路由的注册行为（如分页、批量、校验器与操作人提取规则等）。
func (rb *ApiBuilder[T, ID]) Route(config func(*RouteConfig[ID])) IApiBuilder[T, ID] {
	if config == nil {
		return rb
	}
	config(rb.routeConfig)
	return rb
}

// Service 调整 Application 层的分页、批量上限等服务配置。
func (rb *ApiBuilder[T, ID]) Service(config func(*crud.ServiceConfig)) IApiBuilder[T, ID] {
	if config == nil {
		return rb
	}
	if rb.serviceConfig == nil {
		rb.serviceConfig = cloneServiceConfig(crud.DefaultServiceConfig())
	}
	config(rb.serviceConfig)
	rb.serviceConfigured = true
	return rb
}

// Middleware 为 CRUD 路由追加中间件。
func (rb *ApiBuilder[T, ID]) Middleware(middlewares ...httpx.Middleware) IApiBuilder[T, ID] {
	rb.middlewares = append(rb.middlewares, middlewares...)
	return rb
}

// Hooks 为 Application 注入 Create/Update/Delete 等显式生命周期钩子。
func (rb *ApiBuilder[T, ID]) Hooks(config func(*crud.Hooks[T, ID])) IApiBuilder[T, ID] {
	if config == nil {
		return rb
	}
	if rb.hooks == nil {
		rb.hooks = &crud.Hooks[T, ID]{}
	}
	config(rb.hooks)
	return rb
}

// Build 把当前配置应用到 service，并将 CRUD 路由注册到指定路由组。
func (rb *ApiBuilder[T, ID]) Build(group httpx.IRouteGroup) error {
	// 重新创建服务（如果配置有变化）
	if rb.service == nil {
		return errors.NewCode(errors.InvalidInput, "service cannot be nil")
	}

	if rb.serviceConfigured {
		svc, ok := any(rb.service).(crud.IServiceConfigUpdatable)
		if !ok {
			return errors.NewCode(errors.FailedPrecondition, "service configuration was provided but service does not support updates")
		}
		svc.UpdateConfig(cloneServiceConfig(rb.serviceConfig))
	}

	if rb.validator != nil {
		svc, ok := any(rb.service).(crud.IValidatorAware)
		if !ok {
			return errors.NewCode(errors.FailedPrecondition, "validator was provided but service does not support validator injection")
		}
		svc.SetValidator(rb.validator)
	}

	if rb.hooks != nil {
		svc, ok := any(rb.service).(crud.IHooksAware[T, ID])
		if !ok {
			return errors.NewCode(errors.FailedPrecondition, "hooks were provided but service does not support hook injection")
		}
		svc.SetHooks(rb.hooks)
	}

	// 创建路由构建器
	routeBuilder := NewRouteBuilder[T, ID](rb.service).
		WithConfig(rb.routeConfig)

	// 安全分层：api/rest 默认属于 API 链路，避免误用 Web Session 语义。
	routeBuilder.Use(middleware.AsAPI())

	// 添加中间件
	if len(rb.middlewares) > 0 {
		routeBuilder.Use(rb.middlewares...)
	}

	// 注册到路由组
	return routeBuilder.Register(group)
}

// Register 是一层便捷封装：创建 builder 后立刻完成路由注册。
func Register[T domain.IEntity[ID], ID comparable](
	group httpx.IRouteGroup,
	svc crud.IApplication[T, ID],
	options ...Option[T, ID],
) error {
	builder, err := NewApiBuilder[T, ID](svc, options...)
	if err != nil {
		return err
	}
	return builder.Build(group)
}

// WithBatchOperations 启用批量路由，并同步限制 application 层的最大批量大小。
func WithBatchOperations[T domain.IEntity[ID], ID comparable](maxSize int) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(config *RouteConfig[ID]) {
			config.Routing.EnableBatch = true
		})
		rb.Service(func(cfg *crud.ServiceConfig) {
			if maxSize > 0 {
				cfg.MaxBatchSize = maxSize
			}
		})
	}
}

// WithPagination 启用分页列表路由，并配置默认/最大 page size。
//
// 参数：
// - defaultSize：未显式传 size 时使用的默认值。
// - maxSize：单页允许的最大数量（用于限流与保护下游仓储）。
func WithPagination[T domain.IEntity[ID], ID comparable](defaultSize, maxSize int) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(config *RouteConfig[ID]) {
			config.Query.EnablePagination = true
			config.Query.DefaultPageSize = defaultSize
			config.Query.MaxPageSize = maxSize
		})
	}
}

// WithQuerySchema 配置 CRUD 列表接口的动态查询 schema。
func WithQuerySchema[T domain.IEntity[ID], ID comparable](schema *query.QuerySchema) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(config *RouteConfig[ID]) {
			config.Query.QuerySchema = schema
			applyQuerySchemaDefaults(config)
		})
	}
}

// WithCustomValidator 配置 API 协议层的请求体验证函数。
//
// 参数：
// - validator：对 Create/Update 请求体执行的校验函数，返回非 nil 表示拒绝请求。
func WithCustomValidator[T domain.IEntity[ID], ID comparable](validator func(any) error) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(config *RouteConfig[ID]) {
			config.Body.Validator = validator
		})
	}
}

// WithValidator 配置 application 层的策略校验器。
//
// 参数：
// - v：会注入 Application；若需 HTTP payload 专属校验，请使用 WithCustomValidator。
func WithValidator[T domain.IEntity[ID], ID comparable](v validate.IValidator) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		if v == nil {
			return
		}
		rb.validator = v
	}
}

// WithCORS 配置 CORS 允许的来源列表；若重复调用，会以最后一次传入的 origins 覆盖 AllowOrigins。
//
// 参数：
// - origins：允许跨域访问的来源列表。
func WithCORS[T domain.IEntity[ID], ID comparable](origins []string) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(config *RouteConfig[ID]) {
			if config.HTTP.CORS == nil {
				config.HTTP.CORS = &middleware.CORSConfig{
					AllowMethods: middleware.DefaultCORSAllowMethods(),
					AllowHeaders: middleware.DefaultCORSAllowHeaders(),
					MaxAge:       middleware.DefaultCORSMaxAge,
				}
			}
			config.HTTP.CORS.AllowOrigins = origins
		})
	}
}

// WithAuth 为 CRUD 路由追加认证/鉴权中间件。
//
// 参数：
// - middleware：鉴权中间件（通常会注入租户/用户/权限上下文）。
func WithAuth[T domain.IEntity[ID], ID comparable](middleware httpx.Middleware) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Middleware(middleware)
	}
}

// WithLogging 为 CRUD 路由追加日志中间件。
//
// 参数：
// - middleware：日志中间件（通常用于请求日志、耗时与错误采样等）。
func WithLogging[T domain.IEntity[ID], ID comparable](middleware httpx.Middleware) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Middleware(middleware)
	}
}

// WithOperatorExtractor 配置操作人提取函数（用于 audited 写入时注入 operator）。
//
// 参数：
// - extractor：从 HTTP 上下文提取 operator 的函数；返回 ok=false 表示缺失。
func WithOperatorExtractor[T domain.IEntity[ID], ID comparable](extractor func(httpx.IContext) (string, bool)) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Route(func(cfg *RouteConfig[ID]) {
			cfg.Audit.OperatorExtractor = extractor
		})
	}
}

// WithHooks 为 builder 配置一组 application 生命周期钩子。
func WithHooks[T domain.IEntity[ID], ID comparable](config func(*crud.Hooks[T, ID])) Option[T, ID] {
	return func(rb *ApiBuilder[T, ID]) {
		rb.Hooks(config)
	}
}
