package rest

import (
	"context"
	"gochen-runtime/host/module"
	"net/http"
	"strings"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/middleware"
	"gochen/app"
	appcrud "gochen/app/crud"
	"gochen/auth"

	"gochen/domain"
	"gochen/errors"
	"gochen/httpx"
)

// Option 用于配置标准 REST 路由。
type Option[T domain.IEntity[ID], ID comparable] func(*RouteBuilder[T, ID])

// 标准动作名；与 Application 的 Descriptor 保持同一份命名。
const (
	actionRead        = "read"
	actionList        = "list"
	actionCreate      = "create"
	actionUpdate      = "update"
	actionDelete      = "delete"
	actionRestore     = "restore"
	actionPurge       = "purge"
	actionAuditRead   = "audit_read"
	actionListDeleted = "list_deleted"
)

// routeDef 是一条待注册的路由：纯数据 spec + 已绑定具体类型的 handler。
//
// spec 与 handler 一起产出，避免"路由表"与"实际注册"漂移。
type routeDef struct {
	spec    RouteSpec
	handler httpx.Handler
}

// mountedRoutes 记录已注册的 path→methods，用于生成 CORS 预检路由。
type mountedRoutes map[string]map[string]struct{}

func (m mountedRoutes) add(method, path string) {
	if m[path] == nil {
		m[path] = make(map[string]struct{})
	}
	m[path][method] = struct{}{}
}

// RouteBuilder 按 Descriptor 把 Application 的标准能力组装成 REST 路由。
type RouteBuilder[T domain.IEntity[ID], ID comparable] struct {
	config          *RouteConfig[ID]
	service         ReadApplication[T, ID]
	write           appcrud.IWriteApplication[T, ID]
	execute         appcrud.IActionExecutor[T, ID]
	audit           AuditApplication[ID]
	deletion        DeletionApplication[T, ID]
	auditSet        bool
	deletionSet     bool
	configError     error
	providers       map[string]app.IDescriptorProvider
	maxBulkSize     int
	middlewares     []httpx.Middleware
	fastDenyEnabled bool
	registry        module.IApplicationRegistry
	entryRegistry   IEntryRegistry
	entryModule     string
	cors            httpx.Middleware
	descriptor      auth.Descriptor
}

// NewRouteBuilder 创建路由构建器。
func NewRouteBuilder[T domain.IEntity[ID], ID comparable](
	app ReadApplication[T, ID],
	options ...Option[T, ID],
) (*RouteBuilder[T, ID], error) {
	if isNilService(app) {
		return nil, errors.NewCode(errors.InvalidInput, "application cannot be nil")
	}
	b := newRouteBuilderWithoutApp[T, ID](options...)
	b.service = app
	b.write, _ = app.(appcrud.IWriteApplication[T, ID])
	b.execute, _ = app.(appcrud.IActionExecutor[T, ID])
	if limits, ok := app.(interface{ MaxBulkSize() int }); ok {
		b.maxBulkSize = limits.MaxBulkSize()
	}
	if err := b.prepare(); err != nil {
		return nil, err
	}
	return b, nil
}

// newRouteBuilderWithoutApp 只按 Option 构建路由配置，不绑定具体应用。
//
// 供窄接口挂载（如 MountEventSourced）与 Describe 复用同一套配置与校验。
func newRouteBuilderWithoutApp[T domain.IEntity[ID], ID comparable](options ...Option[T, ID]) *RouteBuilder[T, ID] {
	b := &RouteBuilder[T, ID]{config: DefaultRouteConfig[ID]()}
	for _, opt := range options {
		if opt != nil {
			opt(b)
		}
	}
	if b.config == nil {
		b.config = DefaultRouteConfig[ID]()
	}
	return b
}

// Mount 按 Descriptor 挂载标准 REST 路由，并在配置了启动清单时自动登记应用。
func Mount[T domain.IEntity[ID], ID comparable](
	group httpx.IRouteGroup,
	app ReadApplication[T, ID],
	options ...Option[T, ID],
) error {
	builder, err := NewRouteBuilder[T, ID](app, options...)
	if err != nil {
		return err
	}
	return builder.Register(group)
}

// Describe 是包级入口：推导 Descriptor 与路由表，但不注册任何路由。
//
// 它让装配方在声明期就能拿到一条资源的路由表，而无需启动 Server。
// 返回的 RouteSpec 不含 handler，也不含泛型参数。
func Describe[T domain.IEntity[ID], ID comparable](
	app ReadApplication[T, ID],
	options ...Option[T, ID],
) (auth.Descriptor, []RouteSpec, error) {
	builder, err := NewRouteBuilder[T, ID](app, options...)
	if err != nil {
		return auth.Descriptor{}, nil, err
	}
	return builder.Describe()
}

// Describe 返回 Descriptor 与由它推导出的有序路由表，不注册任何路由。
func (rb *RouteBuilder[T, ID]) Describe() (auth.Descriptor, []RouteSpec, error) {
	if err := rb.prepare(); err != nil {
		return auth.Descriptor{}, nil, err
	}
	return rb.descriptor, Specs(rb.descriptor, rb.config.Routing.BasePath), nil
}

// Register 注册 Descriptor 声明的标准路由。
//
// 流程固定为：校验 → 推导路由表 → 逐条挂载 → CORS 预检 → 启动清单登记。
func (rb *RouteBuilder[T, ID]) Register(group httpx.IRouteGroup) error {
	if isNilService(group) {
		return errors.NewCode(errors.InvalidInput, "route group cannot be nil")
	}
	if rb.entryRegistry == nil {
		var err error
		rb.entryRegistry, rb.entryModule, err = runtimehttp.EntryRegistryFrom(group)
		if err != nil {
			return err
		}
	}
	if err := rb.prepare(); err != nil {
		return err
	}

	mounted := make(mountedRoutes)
	for _, route := range rb.standardRoutes() {
		if err := rb.mount(group, route); err != nil {
			return err
		}
		mounted.add(route.spec.Method, route.spec.Path)
	}
	if err := rb.registerPreflightRoutes(group, mounted); err != nil {
		return err
	}
	return rb.registerStartup()
}

// prepare 做注册前的装配期校验，并把 Descriptor 与配额上限收敛到 builder 上。
//
// 它不依赖路由组，因此 Describe 也能复用同一套校验。
func (rb *RouteBuilder[T, ID]) prepare() error {
	if isNilService(rb.service) {
		return errors.NewCode(errors.InvalidInput, "application cannot be nil")
	}
	if rb.config == nil {
		return errors.NewCode(errors.InvalidInput, "route config cannot be nil")
	}
	if rb.config.Routing.IDCodec == nil {
		return errors.NewCode(errors.InvalidInput, "RouteConfig.Routing.IDCodec is required")
	}
	descriptor, err := rb.composeCapabilities()
	if err != nil {
		return err
	}
	if err := ensureQuerySchemaForType[T](rb.config); err != nil {
		return err
	}
	if err := rb.validateCORSConfig(); err != nil {
		return err
	}
	if err := rb.validateCapabilities(descriptor); err != nil {
		return err
	}
	if max := rb.service.MaxPageSize(); max > 0 && (rb.config.Query.MaxPageSize <= 0 || rb.config.Query.MaxPageSize > max) {
		rb.config.Query.MaxPageSize = max
	}
	rb.descriptor = descriptor
	return nil
}

// standardRoutes 把 Descriptor 声明的动作展开为有序路由表，再按 Op 绑定 handler。
//
// 路由集合本身是 Specs 的纯函数；这里只负责把每条 spec 映射到本包的 handler。
func (rb *RouteBuilder[T, ID]) standardRoutes() []routeDef {
	specs := Specs(rb.descriptor, rb.config.Routing.BasePath)
	routes := make([]routeDef, 0, len(specs))
	for _, spec := range specs {
		routes = append(routes, routeDef{spec: spec, handler: rb.handlerFor(spec.Op)})
	}
	return routes
}

// handlerFor 把 Op 映射到标准 handler。
func (rb *RouteBuilder[T, ID]) handlerFor(op Op) httpx.Handler {
	switch op {
	case OpList:
		return rb.handleList
	case OpListDeleted:
		return rb.handleListDeleted
	case OpCreate:
		return rb.handleCreate
	case OpCreateBulk:
		return rb.handleCreateBulk
	case OpUpdate:
		return rb.handleUpdate
	case OpUpdateBulk:
		return rb.handleUpdateBulk
	case OpDelete:
		return rb.handleDelete
	case OpDeleteBulk:
		return rb.handleDeleteBulk
	case OpRead:
		return rb.handleGet
	case OpAuditRead:
		return rb.handleAuditTrail
	case OpRestore:
		return rb.handleRestore
	case OpPurge:
		return rb.handlePurge
	}
	return nil
}

// mount 给单条路由套上中间件、错误映射与可选快速拒绝，然后注册。
//
// 标准路由与专用归属迁移路由共用这一条注册路径。
func (rb *RouteBuilder[T, ID]) mount(group httpx.IRouteGroup, route routeDef) error {
	if route.handler == nil {
		return errors.NewCode(errors.InvalidInput, "route handler cannot be nil").
			WithContext("op", string(route.spec.Op))
	}
	handler := route.handler
	if rb.entryRegistry != nil {
		provider, ok := any(rb.providers[route.spec.Action]).(interface {
			AuthorizationBinding(string) (auth.EntryBinding, error)
		})
		if !ok {
			return errors.NewCode(errors.InvalidInput, "entry registration requires a declared application execution")
		}
		binding, err := provider.AuthorizationBinding(route.spec.Action)
		if err != nil {
			return err
		}
		binding.Module = rb.entryModule
		location, ok := group.(httpx.IRouteLocation)
		if !ok {
			return errors.NewCode(errors.InvalidInput, "route group must expose actual path resolution")
		}
		if err := rb.entryRegistry.BindHTTP(route.spec.Method, location.RoutePath(route.spec.Path), binding); err != nil {
			return err
		}
	}
	if rb.fastDenyEnabled {
		handler = rb.fastDeny(route.spec.Action, handler)
	}
	return runtimehttp.RegisterRoute(group, route.spec.Method, route.spec.Path, rb.wrapHandler(handler))
}

func (rb *RouteBuilder[T, ID]) fastDeny(action string, next httpx.Handler) httpx.Handler {
	provider, ok := any(rb.providers[action]).(interface{ AuthorizationPlan() (auth.Plan, bool) })
	if !ok {
		return next
	}
	plan, bound := provider.AuthorizationPlan()
	if !bound {
		return next
	}
	if _, safe := plan.FastDenyPermissions[action]; !safe {
		return next
	}
	executor, ok := any(rb.providers[action]).(interface {
		FastDeny(context.Context, string) error
	})
	if !ok {
		return next
	}
	return func(c httpx.IContext) error {
		if err := executor.FastDeny(c.RequestContext(), action); err != nil {
			return err
		}
		return next(c)
	}
}

func (rb *RouteBuilder[T, ID]) validateCORSConfig() error {
	if rb.config.HTTP.CORS == nil {
		return nil
	}
	cfg := rb.config.HTTP.CORS
	return middleware.ValidateCORSConfig(&middleware.CORSConfig{
		AllowOrigins:     nonEmptyStrings(cfg.AllowOrigins),
		AllowCredentials: cfg.AllowCredentials,
	})
}

func (rb *RouteBuilder[T, ID]) validateCapabilities(descriptor auth.Descriptor) error {
	actions := descriptorActions(descriptor)
	for _, name := range []string{actionCreate, actionUpdate, actionDelete} {
		if _, declared := actions[name]; !declared {
			continue
		}
		if isNilService(rb.write) && (name != actionDelete || isNilService(rb.deletion)) {
			return errors.NewCode(errors.InvalidInput, "descriptor declares writes but the application does not implement them").
				WithContext("resource", descriptor.Resource).WithContext("action", name)
		}
		if name != actionDelete && isNilService(rb.execute) {
			return errors.NewCode(errors.InvalidInput, "standard create and update routes require bulk action execution").
				WithContext("resource", descriptor.Resource).WithContext("action", name)
		}
	}
	if _, declared := actions[actionAuditRead]; declared && isNilService(rb.audit) {
		return errors.NewCode(errors.InvalidInput, "descriptor declares audit_read but the application does not implement IAuditReader").
			WithContext("resource", descriptor.Resource)
	}
	for _, name := range []string{actionRestore, actionPurge, actionListDeleted} {
		if _, declared := actions[name]; declared && isNilService(rb.deletion) {
			return errors.NewCode(errors.InvalidInput, "lifecycle action requires an explicit deletion capability").WithContext("action", name)
		}
	}
	return nil
}

func (rb *RouteBuilder[T, ID]) lifecycle() (DeletionApplication[T, ID], error) {
	if isNilService(rb.deletion) {
		return nil, errors.NewCode(errors.InvalidInput, "deletion capability is not configured")
	}
	return rb.deletion, nil
}

func descriptorActions(descriptor auth.Descriptor) map[string]auth.Action {
	out := make(map[string]auth.Action, len(descriptor.Actions))
	for _, action := range descriptor.Actions {
		out[action.Name] = action
	}
	return out
}

func (rb *RouteBuilder[T, ID]) registerPreflightRoutes(group httpx.IRouteGroup, mounted mountedRoutes) error {
	return registerPreflightRoutes(group, mounted, rb.corsMiddleware(), rb.entryRegistry, rb.entryModule)
}

func (rb *RouteBuilder[T, ID]) registerStartup() error {
	if rb.registry == nil {
		return nil
	}
	for _, capability := range rb.applications() {
		if err := rb.registry.Register(capability); err != nil {
			return err
		}
	}
	return nil
}

func isCORSPreflightRequest(ctx httpx.IContext) bool {
	return ctx != nil &&
		ctx.Method() == http.MethodOptions &&
		ctx.Header("Origin") != "" &&
		ctx.Header("Access-Control-Request-Method") != ""
}

// wrapHandler 应用中间件、错误处理与 AfterCommitError 的降级映射。
func (rb *RouteBuilder[T, ID]) wrapHandler(handler httpx.Handler) httpx.Handler {
	middlewares := make([]httpx.Middleware, 0, len(rb.middlewares)+len(rb.config.HTTP.Middlewares)+1)
	if cors := rb.corsMiddleware(); cors != nil {
		middlewares = append(middlewares, cors)
	}
	middlewares = append(middlewares, rb.middlewares...)
	middlewares = append(middlewares, rb.config.HTTP.Middlewares...)

	executor := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		mw := middlewares[i]
		next := executor
		executor = func(ctx httpx.IContext) error {
			return mw(ctx, func() error { return next(ctx) })
		}
	}

	return wrapResponse(executor, rb.config.Response, rb.config.Body.MaxBodySize)
}

func (rb *RouteBuilder[T, ID]) corsMiddleware() httpx.Middleware {
	if rb.cors == nil {
		rb.cors = newCORSMiddleware(rb.config.HTTP.CORS)
	}
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
