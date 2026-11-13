package rest

import (
	"context"
	"net/http"
	"strings"

	"gochen-runtime/host/module"
	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/middleware"
	"gochen/app"
	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

// IEndpoint 是类型化业务入口的最小传输契约。
// Execute 必须复用受保护的 CRUD/ES 应用；AuthorizationBinding 返回该方法同源的固定动作。
// 业务层可保留 Approve 等具名方法，再由同一对象实现此接口，不需要继承框架用例类型。
type IEndpoint[Input, Output any] interface {
	Execute(context.Context, Input) (Output, error)
	AuthorizationBinding() (auth.EntryBinding, error)
}

// EndpointOptions 配置具名业务入口的 HTTP 传输，不改变业务动作或授权计划。
// SuccessStatus 为零时使用 200；MaxBodySize 为零时使用标准请求体上限。
// HTTP.CORS 同标准资源入口生成预检；共享路径时应在上层统一配置 CORS。
type EndpointOptions struct {
	HTTP          HTTPOptions
	Response      ResponseOptions
	Audit         AuditOptions
	MaxBodySize   int64
	SuccessStatus int
}

// EndpointBinding 将同一受保护业务对象的调用能力和冻结授权声明连接到 HTTP。
// 注册阶段与其他路由一样由组合根串行执行；注册后可并发处理请求。
type EndpointBinding[Input, Output any] struct {
	method        string
	path          string
	endpoint      IEndpoint[Input, Output]
	decode        func(httpx.IContext) (Input, error)
	options       EndpointOptions
	authorization auth.EntryBinding
	err           error
}

// Bind 声明具名业务入口；解码器只提供输入，处理器固定调用 endpoint.Execute。
// 动作、权限与资源声明来自同一对象，不接受独立动作名或任意业务 handler。
// options 可省略或提供一个值；配置错误在 Build 或 RegisterRoutes 返回。
func Bind[Input, Output any](method, path string, endpoint IEndpoint[Input, Output], decode func(httpx.IContext) (Input, error), options ...EndpointOptions) *EndpointBinding[Input, Output] {
	b := &EndpointBinding[Input, Output]{method: strings.ToUpper(strings.TrimSpace(method)), path: strings.TrimSpace(path), endpoint: endpoint, decode: decode}
	if len(options) > 1 {
		b.err = errors.NewCode(errors.InvalidInput, "business route accepts at most one options value")
		return b
	}
	if len(options) == 1 {
		b.options = options[0]
	}
	b.options.HTTP.Middlewares = append([]httpx.Middleware(nil), b.options.HTTP.Middlewares...)
	if cfg := b.options.HTTP.CORS; cfg != nil {
		copied := *cfg
		copied.AllowOrigins = nonEmptyStrings(cfg.AllowOrigins)
		copied.AllowMethods = nonEmptyStrings(cfg.AllowMethods)
		copied.AllowHeaders = nonEmptyStrings(cfg.AllowHeaders)
		copied.ExposeHeaders = nonEmptyStrings(cfg.ExposeHeaders)
		b.options.HTTP.CORS = &copied
	}
	if b.options.MaxBodySize == 0 {
		b.options.MaxBodySize = defaultMaxBodySize
	}
	if b.options.SuccessStatus == 0 {
		b.options.SuccessStatus = http.StatusOK
	}
	if b.options.Response.ResponseWrapper == nil {
		b.options.Response.ResponseWrapper = DefaultResponseWrapper
	}
	if isNilService(endpoint) {
		b.err = errors.NewCode(errors.InvalidInput, "business endpoint cannot be nil")
		return b
	}
	b.authorization, b.err = endpoint.AuthorizationBinding()
	if b.err == nil {
		b.authorization = b.authorization.Copy()
		binding := b.authorization.Copy()
		binding.Entry = auth.Entry{Kind: "business", Key: "endpoint"}
		_, b.err = auth.ValidateEntries([]auth.Entry{binding.Entry}, []auth.EntryBinding{binding})
	}
	return b
}

func (b *EndpointBinding[Input, Output]) validate() error {
	if b == nil {
		return errors.NewCode(errors.InvalidInput, "business binding cannot be nil")
	}
	if b.err != nil {
		return b.err
	}
	if b.decode == nil || b.path == "" {
		return errors.NewCode(errors.InvalidInput, "business route requires a path and decoder")
	}
	switch b.method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions:
	default:
		return errors.NewCode(errors.InvalidInput, "unsupported route method: "+b.method)
	}
	if b.options.SuccessStatus < 200 || b.options.SuccessStatus > 299 || b.options.SuccessStatus == http.StatusNoContent || b.options.SuccessStatus == http.StatusResetContent {
		return errors.NewCode(errors.InvalidInput, "business JSON response requires a success status with a response body")
	}
	if b.options.MaxBodySize < 0 {
		return errors.NewCode(errors.InvalidInput, "business request body limit cannot be negative")
	}
	for _, mw := range b.options.HTTP.Middlewares {
		if mw == nil {
			return errors.NewCode(errors.InvalidInput, "business middleware cannot be nil")
		}
	}
	if b.method == http.MethodOptions && newCORSMiddleware(b.options.HTTP.CORS) != nil {
		return errors.NewCode(errors.InvalidInput, "business OPTIONS route conflicts with its CORS preflight")
	}
	return middleware.ValidateCORSConfig(b.options.HTTP.CORS)
}

// ModuleBinding 返回同一业务入口派生的资源动作声明及其实际路由注册器。
// 多资源业务的每个资源都进入应用清单，完整执行计划仍由入口门禁核对。
func (b *EndpointBinding[Input, Output]) ModuleBinding() (module.Binding, error) {
	if err := b.validate(); err != nil {
		return module.Binding{}, err
	}
	var applications []app.IDescriptorProvider
	if b.authorization.Plan != nil {
		applications = append(applications, endpointApplication(b.authorization.Plan, b.authorization.Action))
	}
	for _, resource := range b.authorization.Resources {
		applications = append(applications, endpointApplication(resource.Plan, resource.Action))
	}
	return module.Binding{Applications: applications, RouteRegistrars: []module.IRouteRegistrar{b}}, nil
}

type endpointDescriptor struct{ descriptor auth.Descriptor }

func (d endpointDescriptor) Descriptor() auth.Descriptor { return d.descriptor.Copy() }

func endpointApplication(plan *auth.Plan, action string) app.IDescriptorProvider {
	declared, _ := plan.Descriptor.Action(action)
	return endpointDescriptor{descriptor: auth.Descriptor{Resource: plan.Descriptor.Resource, Actions: []auth.Action{declared}}}
}

// RegisterRoutes 从业务对象登记完整授权绑定，再挂载只能执行该对象的处理器。
// 不在 Host 内使用时，业务对象仍须通过受保护 Application 执行授权。
// 同标准 Mount 一样，登记错误可能留下部分路由；失败后应重建路由组或服务器，不原地重试。
func (b *EndpointBinding[Input, Output]) RegisterRoutes(group httpx.IRouteGroup) error {
	if err := b.validate(); err != nil {
		return err
	}
	if isNilService(group) {
		return errors.NewCode(errors.InvalidInput, "business route requires a route group")
	}
	registry, moduleID, err := runtimehttp.EntryRegistryFrom(group)
	if err != nil {
		return err
	}
	if registry != nil {
		if err := runtimehttp.BindEntry(group, b.method, b.path, b.authorization.Copy()); err != nil {
			return err
		}
	}
	handler := httpx.Handler(func(c httpx.IContext) error {
		ctx, err := requestContext(c, b.options.Audit.OperatorExtractor)
		if err != nil {
			return err
		}
		input, err := b.decode(c)
		if err != nil {
			return err
		}
		output, err := b.endpoint.Execute(ctx, input)
		if err != nil {
			return err
		}
		return c.JSON(b.options.SuccessStatus, httpx.JSONValue(b.options.Response.ResponseWrapper(output)))
	})
	cors := newCORSMiddleware(b.options.HTTP.CORS)
	middlewares := append([]httpx.Middleware(nil), b.options.HTTP.Middlewares...)
	if cors != nil {
		middlewares = append([]httpx.Middleware{cors}, middlewares...)
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		mw, next := middlewares[i], handler
		handler = func(c httpx.IContext) error { return mw(c, func() error { return next(c) }) }
	}
	if err := runtimehttp.RegisterRoute(group, b.method, b.path, wrapResponse(handler, b.options.Response, b.options.MaxBodySize)); err != nil {
		return err
	}
	mounted := make(mountedRoutes)
	mounted.add(b.method, b.path)
	return registerPreflightRoutes(group, mounted, cors, registry, moduleID)
}
