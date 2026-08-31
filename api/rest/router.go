// Package rest 提供 RESTful API 路由构建功能。
package rest

import (
	"fmt"
	"net/http"
	"strings"

	"gochen-runtime/http/middleware"
	"gochen-runtime/internal/httpheader"
	appcrud "gochen/app/crud"
	"gochen/domain"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"
	"gochen/httpx"
)

// IRouteBuilder 路由构建器接口。
type IRouteBuilder[T domain.IEntity[ID], ID comparable] interface {
	// 配置路由行为
	WithConfig(config *RouteConfig[ID]) IRouteBuilder[T, ID]

	// 注册中间件
	Use(middlewares ...httpx.Middleware) IRouteBuilder[T, ID]

	// 注册到路由组
	Register(group httpx.IRouteGroup) error
}

// RouteBuilder 路由构建器实现。
type RouteBuilder[T domain.IEntity[ID], ID comparable] struct {
	config           *RouteConfig[ID]
	middlewares      []httpx.Middleware
	service          appcrud.IApplication[T, ID]
	cors             httpx.Middleware
	registeredRoutes map[string]map[string]struct{}

	// auditedEnabled 表示当前实体类型是否为 audited（由 T 的方法集决定）。
	// 注意：audited 能力闭环仍需通过 service 能力校验（见 Register）。
	auditedEnabled bool
	auditedService IAuditedService[T, ID]
}

// NewRouteBuilder 创建路由构建器。
func NewRouteBuilder[T domain.IEntity[ID], ID comparable](svc appcrud.IApplication[T, ID]) IRouteBuilder[T, ID] {
	rb := &RouteBuilder[T, ID]{
		config:         DefaultRouteConfig[ID](),
		service:        svc,
		auditedEnabled: audited.IsEntityType[T](),
	}
	if rb.auditedEnabled && !isNilService(svc) {
		if as, ok := svc.(IAuditedService[T, ID]); ok {
			rb.auditedService = as
		}
	}
	return rb
}

// WithConfig 配置路由行为。
func (rb *RouteBuilder[T, ID]) WithConfig(config *RouteConfig[ID]) IRouteBuilder[T, ID] {
	if config != nil {
		rb.config = config
		rb.cors = nil
	}
	return rb
}

// Use 注册中间件。
func (rb *RouteBuilder[T, ID]) Use(middlewares ...httpx.Middleware) IRouteBuilder[T, ID] {
	rb.middlewares = append(rb.middlewares, middlewares...)
	return rb
}

// Register 注册能力。
func (rb *RouteBuilder[T, ID]) Register(group httpx.IRouteGroup) error {
	if isNilService(rb.service) {
		return errors.NewCode(errors.InvalidInput, "service cannot be nil")
	}
	if isNilService(group) {
		return errors.NewCode(errors.InvalidInput, "route group cannot be nil")
	}
	if rb.config == nil || rb.config.Routing.IDCodec == nil {
		return errors.NewCode(errors.InvalidInput, "RouteConfig.Routing.IDCodec is required")
	}
	if err := ensureQuerySchemaForType[T](rb.config); err != nil {
		return err
	}
	if err := rb.validateCORSConfig(); err != nil {
		return err
	}
	if err := rb.validateServiceCapabilities(); err != nil {
		return err
	}
	if err := rb.validateAuditedCapabilities(); err != nil {
		return err
	}

	rb.registeredRoutes = make(map[string]map[string]struct{})

	// 注册路由
	if rb.config.Routing.EnableList || rb.auditedEnabled {
		rb.registerListRoutes(group)
	}
	rb.registerEntityRoutes(group)

	if rb.hasEnabledBatchRoutes() {
		rb.registerBatchRoutes(group)
	}

	if rb.auditedEnabled {
		rb.registerAuditedRoutes(group)
	}
	rb.registerCORSPreflightRoutes(group)

	return nil
}

func (rb *RouteBuilder[T, ID]) validateCORSConfig() error {
	if rb == nil || rb.config == nil || rb.config.HTTP.CORS == nil {
		return nil
	}
	cfg := rb.config.HTTP.CORS
	return middleware.ValidateCORSConfig(&middleware.CORSConfig{
		AllowOrigins:     nonEmptyStrings(cfg.AllowOrigins),
		AllowCredentials: cfg.AllowCredentials,
	})
}

func (rb *RouteBuilder[T, ID]) validateServiceCapabilities() error {
	if rb.hasEnabledBatchRoutes() {
		if _, ok := rb.batchWriter(); !ok {
			return errors.NewCode(errors.InvalidInput, "batch routes require a built-in batch writer or service-specific batch writer")
		}
	}
	return nil
}

func (rb *RouteBuilder[T, ID]) validateAuditedCapabilities() error {
	if !rb.auditedEnabled {
		return nil
	}
	if rb.config == nil || rb.config.Audit.OperatorExtractor == nil {
		return errors.NewCode(errors.InvalidInput, "audited entity requires RouteConfig.Audit.OperatorExtractor")
	}
	if rb.auditedService == nil {
		return errors.NewCode(errors.InvalidInput, "audited entity requires service to implement audited operations")
	}
	if rb.auditedService.AuditStore() == nil {
		return errors.NewCode(errors.InvalidInput, "audited entity requires non-nil auditStore")
	}
	return nil
}

func (rb *RouteBuilder[T, ID]) hasEnabledBatchRoutes() bool {
	return rb.config != nil && rb.config.Routing.EnableBatch &&
		(rb.config.Routing.EnableCreate || rb.config.Routing.EnableUpdate || rb.config.Routing.EnableDelete)
}

func (rb *RouteBuilder[T, ID]) trackRoute(method string, path string) {
	if rb.registeredRoutes == nil {
		rb.registeredRoutes = make(map[string]map[string]struct{})
	}
	if rb.registeredRoutes[path] == nil {
		rb.registeredRoutes[path] = make(map[string]struct{})
	}
	rb.registeredRoutes[path][method] = struct{}{}
}

// registerListRoutes 注册列表相关路由。
func (rb *RouteBuilder[T, ID]) registerListRoutes(group httpx.IRouteGroup) {
	basePath := ""
	if rb.config != nil {
		basePath = rb.config.Routing.BasePath
	}

	if rb.config.Routing.EnableList {
		// GET /resource - 获取列表
		group.GET(basePath, rb.wrapHandler(rb.handleList))
		rb.trackRoute(http.MethodGet, basePath)
	}

	// audited 扩展：GET /resource/deleted - 获取已删除列表
	if rb.auditedEnabled {
		group.GET(fmt.Sprintf("%s/deleted", basePath), rb.wrapHandler(rb.handleListDeleted))
		rb.trackRoute(http.MethodGet, fmt.Sprintf("%s/deleted", basePath))
	}
}

// registerEntityRoutes 注册实体相关路由。
func (rb *RouteBuilder[T, ID]) registerEntityRoutes(group httpx.IRouteGroup) {
	basePath := ""
	if rb.config != nil {
		basePath = rb.config.Routing.BasePath
	}

	if rb.config.Routing.EnableGet {
		group.GET(fmt.Sprintf("%s/:id", basePath), rb.wrapHandler(rb.handleGet))
		rb.trackRoute(http.MethodGet, fmt.Sprintf("%s/:id", basePath))
	}

	if rb.config.Routing.EnableCreate {
		group.POST(basePath, rb.wrapHandler(rb.handleCreate))
		rb.trackRoute(http.MethodPost, basePath)
	}

	if rb.config.Routing.EnableUpdate {
		group.PUT(fmt.Sprintf("%s/:id", basePath), rb.wrapHandler(rb.handleUpdate))
		rb.trackRoute(http.MethodPut, fmt.Sprintf("%s/:id", basePath))
	}

	if rb.config.Routing.EnableDelete {
		group.DELETE(fmt.Sprintf("%s/:id", basePath), rb.wrapHandler(rb.handleDelete))
		rb.trackRoute(http.MethodDelete, fmt.Sprintf("%s/:id", basePath))
	}
}

// registerAuditedRoutes 注册Audited路由集合。
func (rb *RouteBuilder[T, ID]) registerAuditedRoutes(group httpx.IRouteGroup) {
	basePath := ""
	if rb.config != nil {
		basePath = rb.config.Routing.BasePath
	}
	group.GET(fmt.Sprintf("%s/:id/audit", basePath), rb.wrapHandler(rb.handleAuditTrail))
	rb.trackRoute(http.MethodGet, fmt.Sprintf("%s/:id/audit", basePath))
	group.POST(fmt.Sprintf("%s/:id/restore", basePath), rb.wrapHandler(rb.handleRestore))
	rb.trackRoute(http.MethodPost, fmt.Sprintf("%s/:id/restore", basePath))

	// 物理删除（purge）为"危险操作"，仅在仓储或自定义 service 明确支持时才注册端点。
	if rb.shouldRegisterPurge() {
		group.DELETE(fmt.Sprintf("%s/:id/purge", basePath), rb.wrapHandler(rb.handlePurge))
		rb.trackRoute(http.MethodDelete, fmt.Sprintf("%s/:id/purge", basePath))
	}
}

func (rb *RouteBuilder[T, ID]) shouldRegisterPurge() bool {
	if rb == nil || rb.auditedService == nil {
		return false
	}
	_, ok := rb.service.Repository().(crud.IPurgeRepository[T, ID])
	return ok
}

// registerBatchRoutes 注册批量操作路由。
func (rb *RouteBuilder[T, ID]) registerBatchRoutes(group httpx.IRouteGroup) {
	basePath := ""
	if rb.config != nil {
		basePath = rb.config.Routing.BasePath
	}

	if rb.config.Routing.EnableCreate {
		group.POST(fmt.Sprintf("%s/batch", basePath), rb.wrapHandler(rb.handleCreateAll))
		rb.trackRoute(http.MethodPost, fmt.Sprintf("%s/batch", basePath))
	}

	if rb.config.Routing.EnableUpdate {
		group.PUT(fmt.Sprintf("%s/batch", basePath), rb.wrapHandler(rb.handleUpdateBatch))
		rb.trackRoute(http.MethodPut, fmt.Sprintf("%s/batch", basePath))
	}

	if rb.config.Routing.EnableDelete {
		group.DELETE(fmt.Sprintf("%s/batch", basePath), rb.wrapHandler(rb.handleDeleteBatch))
		rb.trackRoute(http.MethodDelete, fmt.Sprintf("%s/batch", basePath))
	}
}

func (rb *RouteBuilder[T, ID]) registerCORSPreflightRoutes(group httpx.IRouteGroup) {
	cors := rb.corsMiddleware()
	if cors == nil {
		return
	}
	for path, methods := range rb.corsPreflightMethodsByPath() {
		allowHeader := httpheader.BuildAllowHeader(methods)
		group.OPTIONS(path, func(c httpx.IContext) error {
			if allowHeader != "" {
				c.SetHeader("Allow", allowHeader)
			}
			if isCORSPreflightRequest(c) {
				return cors(c, func() error {
					return c.String(http.StatusNoContent, "")
				})
			}
			return c.String(http.StatusNoContent, "")
		})
	}
}

func (rb *RouteBuilder[T, ID]) corsPreflightMethodsByPath() map[string]map[string]struct{} {
	paths := make(map[string]map[string]struct{})
	if rb == nil || rb.config == nil {
		return paths
	}
	for path, methods := range rb.registeredRoutes {
		cloned := make(map[string]struct{}, len(methods))
		for method := range methods {
			cloned[method] = struct{}{}
		}
		paths[path] = cloned
	}
	return paths
}

func isCORSPreflightRequest(ctx httpx.IContext) bool {
	return ctx != nil &&
		ctx.Method() == http.MethodOptions &&
		ctx.Header("Origin") != "" &&
		ctx.Header("Access-Control-Request-Method") != ""
}

// wrapHandler 包装处理器，应用中间件和错误处理。
//
// 说明：
// - 中间件链在路由注册时预构建（而非每次请求重建），闭包在此刻确定。
func (rb *RouteBuilder[T, ID]) wrapHandler(handler func(httpx.IContext) error) func(httpx.IContext) error {
	middlewares := make([]httpx.Middleware, 0, len(rb.middlewares)+len(rb.config.HTTP.Middlewares))
	if cors := rb.corsMiddleware(); cors != nil {
		middlewares = append(middlewares, cors)
	}
	middlewares = append(middlewares, rb.middlewares...)
	if rb.config != nil && len(rb.config.HTTP.Middlewares) > 0 {
		middlewares = append(middlewares, rb.config.HTTP.Middlewares...)
	}

	// 构建执行链：从后向前包装，确保执行顺序正确
	executor := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		mw := middlewares[i] // 捕获当前迭代的中间件
		next := executor     // 捕获当前的下一个执行器
		executor = func(ctx httpx.IContext) error {
			return mw(ctx, func() error {
				return next(ctx)
			})
		}
	}

	// 预获取错误处理器
	errorHandler := DefaultErrorHandler
	if rb.config != nil && rb.config.Response.ErrorHandler != nil {
		errorHandler = rb.config.Response.ErrorHandler
	}

	// 预获取 MaxBodySize
	// MaxBodySize > 0: 使用该限制；<= 0: 未配置或无效值，由 httpx 使用默认上限。
	maxBodySize := int64(0)
	if rb.config != nil {
		maxBodySize = rb.config.Body.MaxBodySize
	}

	return func(c httpx.IContext) error {
		if maxBodySize > 0 {
			c.Set(httpx.MaxBodySizeKey, httpx.ValueOf(maxBodySize))
		}

		if err := executor(c); err != nil {
			response := errorHandler(c, err)
			if response == nil {
				response = DefaultErrorHandler(c, err)
			}
			return response.Send(c)
		}

		return nil
	}
}

func (rb *RouteBuilder[T, ID]) corsMiddleware() httpx.Middleware {
	if rb == nil || rb.config == nil || rb.config.HTTP.CORS == nil {
		return nil
	}
	if rb.cors != nil {
		return rb.cors
	}
	cfg := rb.config.HTTP.CORS
	origins := nonEmptyStrings(cfg.AllowOrigins)
	if len(origins) == 0 {
		return nil
	}
	methods := nonEmptyStrings(cfg.AllowMethods)
	if len(methods) == 0 {
		methods = middleware.DefaultCORSAllowMethods()
	}
	headers := nonEmptyStrings(cfg.AllowHeaders)
	if len(headers) == 0 {
		headers = middleware.DefaultCORSAllowHeaders()
	}
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = middleware.DefaultCORSMaxAge
	}
	corsCfg := &middleware.CORSConfig{
		AllowOrigins:     origins,
		AllowMethods:     methods,
		AllowHeaders:     headers,
		ExposeHeaders:    nonEmptyStrings(cfg.ExposeHeaders),
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           maxAge,
	}
	rb.cors = middleware.CORS(corsCfg)
	return rb.cors
}

func nonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
