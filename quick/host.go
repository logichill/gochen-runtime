package quick

import (
	"strings"

	"gochen-runtime/host"
	"gochen-runtime/host/module"
	"gochen/errors"
	"gochen/httpx"
)

// ModuleOption 配置正式模块的便捷声明，不创建第二套生命周期。
type ModuleOption func(*host.Builder)

// WithMiddlewares 为 Module 挂载模块级 HTTP 中间件。
func WithMiddlewares(middlewares ...httpx.Middleware) ModuleOption {
	values := append([]httpx.Middleware(nil), middlewares...)
	return func(builder *host.Builder) { builder.Middleware(values...) }
}

// WithDependencies 声明模块的前置依赖模块 ID。
func WithDependencies(deps ...string) ModuleOption {
	values := append([]string(nil), deps...)
	return func(builder *host.Builder) { builder.DependsOn(values...) }
}

// WithExtensions 声明模块注入的扩展资产。
func WithExtensions(exts ...any) ModuleOption {
	values := append([]any(nil), exts...)
	return func(builder *host.Builder) { builder.Extension(values...) }
}

// Module 快速声明仅包含路由注册的正式模块构造器。
// registrar 接受路由函数；对象通过 RegisterRoutes 方法值传入。
func Module(name string, registrar func(httpx.IRouteGroup) error, opts ...ModuleOption) module.ModuleCtor {
	name = strings.TrimSpace(name)
	options := append([]ModuleOption(nil), opts...)
	return func() (module.IModule, error) {
		if name == "" {
			return nil, errors.NewCode(errors.InvalidInput, "module name cannot be empty")
		}
		builder := host.Module(name).Name(name)
		if registrar != nil {
			builder.RouteRegistrar(module.RouteRegistrarFunc(registrar))
		}
		for _, option := range options {
			if option != nil {
				option(builder)
			}
		}
		return builder.Build()
	}
}
