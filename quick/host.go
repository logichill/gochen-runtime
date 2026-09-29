package quick

import (
	"context"
	"strings"

	"gochen-runtime/host/module"
	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
)

// ModuleOption 声明 quick.Module 的构造选项。
type ModuleOption func(*quickModule)

// WithMiddlewares 为 Module 挂载模块级 HTTP 中间件。
func WithMiddlewares(middlewares ...httpx.Middleware) ModuleOption {
	return func(m *quickModule) {
		m.middlewares = append(m.middlewares, middlewares...)
	}
}

// WithDependencies 声明轻量模块的前置依赖模块 ID。
func WithDependencies(deps ...string) ModuleOption {
	return func(m *quickModule) {
		m.deps = append(m.deps, deps...)
	}
}

// WithExtensions 声明轻量模块注入的扩展资产。
func WithExtensions(exts ...any) ModuleOption {
	return func(m *quickModule) {
		m.extensions = append(m.extensions, exts...)
	}
}

type quickModule struct {
	id          string
	name        string
	registrar   func(httpx.IRouteGroup) error
	middlewares []httpx.Middleware
	deps        []string
	extensions  []any
	opts        module.ModuleInitOptions
}

func (m *quickModule) ID() string   { return m.id }
func (m *quickModule) Name() string { return m.name }

func (m *quickModule) Init(opts module.ModuleInitOptions) error {
	m.opts = opts
	return nil
}

func (m *quickModule) Start(_ context.Context) (module.ModuleStopFunc, error) {
	return nil, nil
}

func (m *quickModule) DependsOn() []string {
	return append([]string(nil), m.deps...)
}

func (m *quickModule) ModuleExtensions() []any {
	return append([]any(nil), m.extensions...)
}

func (m *quickModule) RegisterRoutes(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if m.registrar == nil {
		return nil
	}

	httpOptions, ok := module.CapabilityFrom(m.opts, module.HTTPKey)
	if !ok || httpOptions == nil {
		logger := logging.ComponentLogger("quick.module")
		logger.Debug(ctx, "route registration skipped because HTTP capability is not enabled on host",
			logging.Field{Key: "module", Value: m.id})
		return nil
	}
	group := httpOptions.MountGroup()
	if group == nil {
		logger := logging.ComponentLogger("quick.module")
		logger.Debug(ctx, "route registration skipped because HTTP mount group is nil",
			logging.Field{Key: "module", Value: m.id})
		return nil
	}

	if len(m.middlewares) > 0 {
		group.Use(m.middlewares...)
	}

	return m.registrar(group)
}

var (
	_ module.IModule                   = (*quickModule)(nil)
	_ module.IRouteModule              = (*quickModule)(nil)
	_ module.IModuleDependencyProvider = (*quickModule)(nil)
	_ module.IModuleExtensionProvider  = (*quickModule)(nil)
)

// Module 快速声明仅包含路由注册的轻量模块构造器。
//
// registrar 接受路由注册函数；对象通过 RegisterRoutes 方法值传入，工厂在调用方显式求值。
func Module(name string, registrar func(httpx.IRouteGroup) error, opts ...ModuleOption) module.ModuleCtor {
	return func() (module.IModule, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.NewCode(errors.InvalidInput, "module name cannot be empty")
		}
		m := &quickModule{
			id:        name,
			name:      name,
			registrar: registrar,
		}
		for _, opt := range opts {
			if opt != nil {
				opt(m)
			}
		}
		return m, nil
	}
}
