package quick

import (
	"context"
	"reflect"
	"strings"

	"gochen-runtime/host/config"
	"gochen-runtime/host/engine"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen-runtime/http/nethttp"
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
	registrar   any
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

	switch r := m.registrar.(type) {
	case func(httpx.IRouteGroup) error:
		return r(group)
	case interface {
		RegisterRoutes(httpx.IRouteGroup) error
	}:
		return r.RegisterRoutes(group)
	case func() (interface {
		RegisterRoutes(httpx.IRouteGroup) error
	}, error):
		inst, err := r()
		if err != nil {
			return err
		}
		if inst != nil {
			return inst.RegisterRoutes(group)
		}
		return nil
	case func() (func(httpx.IRouteGroup) error, error):
		fn, err := r()
		if err != nil {
			return err
		}
		if fn != nil {
			return fn(group)
		}
		return nil
	default:
		// 兼容自定义命名函数类型（例如 type RouteRegistrarFunc func(httpx.IRouteGroup) error）。
		//
		// 形参与返回值都要求类型完全相同，不能放宽成 AssignableTo：
		//   - 形参放宽会放进 func(*concreteGroup) error，而实际传入的是宿主给的
		//     另一个具体类型，Call 时 panic；方向也正好反了（该问的是
		//     IRouteGroup 能否传进形参，而非形参能否赋给 IRouteGroup）；
		//   - 返回值放宽会放进 func(httpx.IRouteGroup) MyErr（值类型实现 error），
		//     此时 res[0].IsNil() 对非 nilable 类型 panic。
		// 放宽换来的那点灵活性不值得用启动期 panic 去换，落到 default 报
		// InvalidInput 才是这里该有的失败模式。
		if fnVal := reflect.ValueOf(m.registrar); fnVal.IsValid() && fnVal.Kind() == reflect.Func {
			fnType := fnVal.Type()
			routeGroupType := reflect.TypeOf((*httpx.IRouteGroup)(nil)).Elem()
			errType := reflect.TypeOf((*error)(nil)).Elem()
			if fnType.NumIn() == 1 && fnType.NumOut() == 1 &&
				fnType.In(0) == routeGroupType &&
				fnType.Out(0) == errType {
				res := fnVal.Call([]reflect.Value{reflect.ValueOf(group)})
				if !res[0].IsNil() {
					return res[0].Interface().(error)
				}
				return nil
			}
		}
		return errors.NewCode(errors.InvalidInput, "unsupported route registrar type in quick module").
			WithContext("module", m.id)
	}
}

var (
	_ module.IModule                   = (*quickModule)(nil)
	_ module.IRouteModule              = (*quickModule)(nil)
	_ module.IModuleDependencyProvider = (*quickModule)(nil)
	_ module.IModuleExtensionProvider  = (*quickModule)(nil)
)

// Module 快速声明仅包含路由注册的轻量模块构造器。
//
// registrar 支持：
//   - func(httpx.IRouteGroup) error (匿名函数或自定义命名函数类型，例如 quick.RESTRegistrar(app))
//   - interface{ RegisterRoutes(httpx.IRouteGroup) error }
//   - func() (interface{ RegisterRoutes(httpx.IRouteGroup) error }, error)
//   - func() (func(httpx.IRouteGroup) error, error)
func Module(name string, registrar any, opts ...ModuleOption) module.ModuleCtor {
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

// Run 极简微服务启动入口：配置基础路由 /api/v1、HTTP 服务、注册模块并阻塞运行主循环直到信号停止。
func Run(
	ctx context.Context,
	name string,
	port int,
	modules ...module.ModuleCtor,
) error {
	srv, err := Server(name, port, modules...)
	if err != nil {
		return err
	}
	return srv.Start(ctx)
}

// Server 快速创建可编排控制的微服务生命周期引擎实例。
func Server(
	name string,
	port int,
	modules ...module.ModuleCtor,
) (*engine.Engine, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.NewCode(errors.InvalidInput, "server name cannot be empty")
	}
	if port <= 0 || port > 65535 {
		return nil, errors.NewCode(errors.InvalidInput, "server port must be in range 1-65535").
			WithContext("port", port)
	}

	cfg := config.Build(
		config.WithName(name),
		config.WithPort(port),
		config.WithAddress("0.0.0.0"),
		config.WithBasePath("/api/v1"),
		config.WithHTTPServer(nethttp.NewServer(nil)),
		config.WithFailFastOnRouteConflicts(true),
		config.WithModules(modules...),
	)

	app := runtime.NewHostConfig(cfg)
	return engine.NewEngineConfig(app, cfg)
}
