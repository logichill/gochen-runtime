package rest

import (
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

// CommandOptions 配置类型化命令的 HTTP 传输，不改变业务动作或授权计划。
// SuccessStatus 为零时使用 200；MaxBodySize 为零时使用标准请求体上限。
// HTTP.CORS 同标准资源入口生成预检；共享路径时应在上层统一配置 CORS。
type CommandOptions struct {
	HTTP          HTTPOptions
	Response      ResponseOptions
	Audit         AuditOptions
	MaxBodySize   int64
	SuccessStatus int
}

// CommandBinding 将同一 Command 的调用能力和冻结授权声明连接到 HTTP。
// 注册阶段与其他路由一样由组合根串行执行；注册后可并发处理请求。
type CommandBinding[Input, Access, Output any] struct {
	method        string
	path          string
	command       *app.Command[Input, Access, Output]
	decode        func(httpx.IContext) (Input, error)
	options       CommandOptions
	authorization auth.EntryBinding
	err           error
}

// Bind 声明命令入口；解码器只提供输入，处理器固定调用 command.Execute。
// 动作、权限与全部资源声明来自同一 command，不接受独立动作名或业务 handler。
// options 可省略或提供一个值；配置错误在 Build 或 RegisterRoutes 返回。
func Bind[Input, Access, Output any](method, path string, command *app.Command[Input, Access, Output], decode func(httpx.IContext) (Input, error), options ...CommandOptions) *CommandBinding[Input, Access, Output] {
	b := &CommandBinding[Input, Access, Output]{method: strings.ToUpper(strings.TrimSpace(method)), path: strings.TrimSpace(path), command: command, decode: decode}
	if len(options) > 1 {
		b.err = errors.NewCode(errors.InvalidInput, "command route accepts at most one options value")
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
	b.authorization, b.err = command.AuthorizationBinding()
	return b
}

func (b *CommandBinding[Input, Access, Output]) validate() error {
	if b == nil {
		return errors.NewCode(errors.InvalidInput, "command binding cannot be nil")
	}
	if b.err != nil {
		return b.err
	}
	if b.decode == nil || b.path == "" {
		return errors.NewCode(errors.InvalidInput, "command route requires a path and decoder")
	}
	switch b.method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions:
	default:
		return errors.NewCode(errors.InvalidInput, "unsupported route method: "+b.method)
	}
	if b.options.SuccessStatus < 200 || b.options.SuccessStatus > 299 || b.options.SuccessStatus == http.StatusNoContent || b.options.SuccessStatus == http.StatusResetContent {
		return errors.NewCode(errors.InvalidInput, "command JSON response requires a success status with a response body")
	}
	if b.options.MaxBodySize < 0 {
		return errors.NewCode(errors.InvalidInput, "command request body limit cannot be negative")
	}
	for _, mw := range b.options.HTTP.Middlewares {
		if mw == nil {
			return errors.NewCode(errors.InvalidInput, "command middleware cannot be nil")
		}
	}
	if b.method == http.MethodOptions && newCORSMiddleware(b.options.HTTP.CORS) != nil {
		return errors.NewCode(errors.InvalidInput, "command OPTIONS route conflicts with its CORS preflight")
	}
	return middleware.ValidateCORSConfig(b.options.HTTP.CORS)
}

// ModuleBinding 返回同一命令派生的资源动作声明及其实际路由注册器。
// 多资源命令的每个资源都进入应用清单，完整执行计划仍由入口门禁核对。
func (b *CommandBinding[Input, Access, Output]) ModuleBinding() (module.Binding, error) {
	if err := b.validate(); err != nil {
		return module.Binding{}, err
	}
	applications := []app.IDescriptorProvider{commandApplication(b.authorization.Plan, b.authorization.Action)}
	for _, resource := range b.authorization.Resources {
		applications = append(applications, commandApplication(resource.Plan, resource.Action))
	}
	return module.Binding{Applications: applications, RouteRegistrars: []module.IRouteRegistrar{b}}, nil
}

type commandDescriptor struct{ descriptor auth.Descriptor }

func (d commandDescriptor) Descriptor() auth.Descriptor { return d.descriptor.Copy() }

func commandApplication(plan *auth.Plan, action string) app.IDescriptorProvider {
	declared, _ := plan.Descriptor.Action(action)
	return commandDescriptor{descriptor: auth.Descriptor{Resource: plan.Descriptor.Resource, Actions: []auth.Action{declared}}}
}

// RegisterRoutes 从命令本身登记完整授权绑定，再挂载只能执行该命令的处理器。
// 不在 Host 内使用时仍由 Command 执行完整授权，不要求另建入口注册表。
// 同标准 Mount 一样，登记错误可能留下部分路由；失败后应重建路由组或服务器，不原地重试。
func (b *CommandBinding[Input, Access, Output]) RegisterRoutes(group httpx.IRouteGroup) error {
	if err := b.validate(); err != nil {
		return err
	}
	if isNilService(group) {
		return errors.NewCode(errors.InvalidInput, "command route requires a route group")
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
		output, err := b.command.Execute(ctx, input)
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
