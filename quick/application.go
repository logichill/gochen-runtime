package quick

import (
	"context"
	"path"
	"strings"

	"gochen-runtime/host/config"
	"gochen-runtime/host/engine"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen/errors"
	"gochen/httpx"
)

// Application 声明模块挂载并通过 Host 执行生命周期。
// 声明仅用于启动前的单线程装配；Build 后不可再次构建。
type Application struct {
	root    *Group
	options []config.Option
	mounts  []mount
	built   bool
}

// Group 声明 HTTP 前缀和中间件作用域，不创建额外生命周期模块。
// Use 在 Build 前追加的中间件作用于该组及子组的全部模块。
type Group struct {
	app         *Application
	parent      *Group
	prefix      string
	middlewares []httpx.Middleware
}

type mount struct {
	group    *Group
	prefix   string
	explicit bool
	ctor     module.ModuleCtor
}

// New 创建默认监听 :8080、BasePath 为 /api/v1 的应用声明。
// 高级配置复用 host/config；模块通过 Modules/Mount 注册。
func New(opts ...config.Option) *Application {
	app := &Application{options: append([]config.Option(nil), opts...)}
	app.root = &Group{app: app}
	return app
}

// Use 追加基础 API 中间件，包括健康检查路由。
func (a *Application) Use(middlewares ...httpx.Middleware) *Application {
	a.root.Use(middlewares...)
	return a
}

// Group 创建继承父作用域中间件的子组；空前缀仅增加策略作用域。
func (a *Application) Group(prefix string) *Group {
	return a.root.Group(prefix)
}

// Modules 注册使用默认模块 ID 前缀的模块。
func (a *Application) Modules(ctors ...module.ModuleCtor) *Application {
	a.root.Modules(ctors...)
	return a
}

// Mount 在基础 API 路径下显式指定模块前缀。
func (a *Application) Mount(prefix string, ctor module.ModuleCtor) *Application {
	a.root.Mount(prefix, ctor)
	return a
}

// Use 追加当前组的中间件，不影响父组和兄弟组。
func (g *Group) Use(middlewares ...httpx.Middleware) *Group {
	g.middlewares = append(g.middlewares, middlewares...)
	return g
}

// Group 创建继承当前组策略的子组。
func (g *Group) Group(prefix string) *Group {
	return &Group{app: g.app, parent: g, prefix: prefix}
}

// Modules 注册模块，默认在当前组前缀后追加模块 ID。
func (g *Group) Modules(ctors ...module.ModuleCtor) *Group {
	for _, ctor := range ctors {
		g.app.mounts = append(g.app.mounts, mount{group: g, ctor: ctor})
	}
	return g
}

// Mount 显式覆盖模块前缀；"/" 表示当前组根路径，不改变中间件继承。
func (g *Group) Mount(prefix string, ctor module.ModuleCtor) *Group {
	g.app.mounts = append(g.app.mounts, mount{group: g, prefix: prefix, explicit: true, ctor: ctor})
	return g
}

func (g *Group) scope() (string, []httpx.Middleware) {
	if g.parent == nil {
		return "", nil // 根中间件由 Host 基础路由统一应用。
	}
	prefix, middlewares := g.parent.scope()
	return path.Join("/", prefix, g.prefix), append(middlewares, g.middlewares...)
}

// Build 固定挂载声明并创建生命周期引擎；模块工厂仍在 Host Prepare 阶段执行。
// 不能混用按 ID 的 HTTP 配置与声明式挂载，避免隐藏的覆盖和中间件丢失。
func (a *Application) Build() (*engine.Engine, error) {
	if a.built {
		return nil, errors.NewCode(errors.FailedPrecondition, "application already built")
	}
	cfg := config.Build(a.options...)
	if cfg.Port > 65535 {
		return nil, errors.NewCode(errors.InvalidInput, "server port must be in range 1-65535")
	}
	if len(cfg.Modules) != 0 || len(cfg.ModuleHTTP) != 0 ||
		cfg.ModuleHTTPDefaults.Disabled || cfg.ModuleHTTPDefaults.Prefix != "" ||
		len(cfg.ModuleHTTPDefaults.Middlewares) != 0 || cfg.ModuleHTTPDefaults.DisableDefaultMiddlewares {
		return nil, errors.NewCode(errors.InvalidInput, "use Modules, Mount and Group instead of host module HTTP options")
	}
	cfg.RouteMiddlewares = append(cfg.RouteMiddlewares, a.root.middlewares...)
	for _, entry := range a.mounts {
		if entry.ctor == nil {
			return nil, errors.NewCode(errors.InvalidInput, "module constructor is nil")
		}
		if entry.explicit && strings.TrimSpace(entry.prefix) == "" {
			return nil, errors.NewCode(errors.InvalidInput, "mount prefix is empty; use / for group root")
		}
		prefix, middlewares := entry.group.scope()
		middlewares = append([]httpx.Middleware(nil), middlewares...)
		cfg.Modules = append(cfg.Modules, func() (module.IModule, error) {
			m, err := entry.ctor()
			if err != nil {
				return nil, err
			}
			if m == nil || isNil(m) {
				return nil, errors.NewCode(errors.InvalidInput, "module constructor returned nil")
			}
			id, err := module.NormalizeModuleID(m.ID())
			if err != nil {
				return nil, err
			}
			if _, exists := cfg.ModuleHTTP[id]; exists {
				return nil, errors.NewCode(errors.Conflict, "duplicate module mount").WithContext("module", id)
			}
			suffix := id
			if entry.explicit {
				suffix = entry.prefix
			}
			cfg.ModuleHTTP[id] = config.ModuleHTTPConfig{
				Prefix:      path.Join("/", prefix, suffix),
				Middlewares: middlewares,
			}
			return m, nil
		})
	}
	a.built = true
	return engine.NewEngineConfig(runtime.NewHostConfig(cfg), cfg)
}

// Run 构建应用并阻塞运行，直到取消、退出信号或运行失败。
func (a *Application) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	runner, err := a.Build()
	if err != nil {
		return err
	}
	return runner.Start(ctx)
}
