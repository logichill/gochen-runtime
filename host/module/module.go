package module

import (
	"context"

	"gochen-runtime/di"
	"gochen-runtime/host/module/initcap"
	"gochen/errors"
)

// IModule 定义 Host 可管理的基础模块生命周期。
type IModule interface {
	// ID 返回模块的稳定标识（建议使用小写、短横线分隔，例如 "iam"/"llm"/"process"）。
	//
	// 约定：
	// - 必须稳定且在同一进程内唯一；
	// - 必须是 URL-safe 的 path segment（推荐：`[a-z0-9][a-z0-9_-]*`）。
	ID() string

	// Name 返回面向运维或调试展示的模块名称。
	Name() string

	// Init 在 Host 的 Prepare 阶段被调用。
	//
	// 约定：
	// - Init 应尽量避免实例化副作用（避免 Resolve/启动 goroutine 等）；
	// - 推荐在 Init 内完成 providers/constructors 注册，并保存 options 供 Start 使用。
	Init(options ModuleInitOptions) error

	// Start 在 Host 的 StartBackground 阶段被调用。
	//
	// 约定：
	// - Start 负责模块运行期动作（挂载路由、事件订阅、投影注册、后台任务启动等）；
	// - Start 必须是“非阻塞”的（若需循环任务，应自行启动 goroutine）；
	// - 返回的 stop 用于回滚与优雅关闭（可为 nil）。
	Start(ctx context.Context) (ModuleStopFunc, error)
}

// IModuleDependencyProvider 表达模块依赖关系（可选能力）。
//
// 说明：
// - 用于让 Host 在启动期基于依赖关系进行拓扑排序，避免依赖 ctor 顺序导致的隐式脆弱约束；
// - 返回值为模块 ID 列表（与 IModule.ID() 同一命名空间）；建议仅声明“必须先启动”的硬依赖。
type IModuleDependencyProvider interface {
	DependsOn() []string
}

// IModuleExtensionProvider 暴露模块声明的可选能力扩展载荷（可选能力）。
//
// 设计意图：Host 基础容器不认识任何具体扩展的语义，只负责把载荷
// 原样交给已注册的 capability.IModuleCatalogRegistrar。具体扩展（如
// runtime/host/authz 的权限目录）自行断言自己关心的载荷类型。
// 这样 L0 场景不会把安全包编译进依赖图。
type IModuleExtensionProvider interface {
	ModuleExtensions() []any
}

// IRouteModule 表示除了基础生命周期外，还会向 HTTP 层注册路由的模块。
type IRouteModule interface {
	IModule
	RegisterRoutes(ctx context.Context) error
}

// ModuleStopFunc 定义模块启动后的停止函数（用于回滚与优雅关闭）。
type ModuleStopFunc func(ctx context.Context) error

// ModuleInitOptions 定义模块初始化所需的运行环境与可选能力。
//
// 约定：
// - Init 主要用于注册显式 provider/实例并保存运行环境引用（避免实例化副作用）；
// - Start 用于执行运行期动作（挂载路由、订阅事件、注册投影、启动后台任务等），并返回 stop 用于回滚/关闭。
type ModuleInitOptions struct {
	// Registry 仅用于 Init 阶段注册显式 provider（禁止作为 Service Locator 向下渗透）。
	Registry di.IRegistry
	// Resolver 用于显式暴露模块运行期所需按类型解析能力，避免模块内对具体容器类型做断言。
	Resolver     di.IResolver
	container    IModuleContainer
	capabilities initcap.Store
}

// NewModuleInitOptions 创建模块初始化选项。
//
// container 承载框架装配层需要的完整 DI 能力，普通模块只能通过公开字段看到
// Registry/Resolver，避免把构造器注册、函数注入调用与内省能力扩散到业务模块。
func NewModuleInitOptions(registry di.IRegistry, resolver di.IResolver, container IModuleContainer, capabilities ...initcap.Setter) ModuleInitOptions {
	return ModuleInitOptions{
		Registry:     di.RegistryOnly(registry),
		Resolver:     di.ResolverOnly(resolver),
		container:    container,
		capabilities: initcap.NewStore(capabilities...),
	}
}

// WithCapability 写入模块初始化阶段的类型化 capability。
//
// 该函数服务高级扩展包；普通业务模块应优先使用 host/module/runtimecap 的
// HTTPFrom、EventBusFrom 等按能力命名的 accessor。
func WithCapability[T any](opts ModuleInitOptions, key initcap.Key[T], value T) ModuleInitOptions {
	opts.capabilities = initcap.With(opts.capabilities, key, value)
	return opts
}

// CapabilityFrom 读取模块初始化阶段的类型化 capability。
//
// 该函数服务高级扩展包；普通业务模块应优先使用 host/module/runtimecap 的
// HTTPFrom、EventBusFrom 等按能力命名的 accessor。
func CapabilityFrom[T any](opts ModuleInitOptions, key initcap.Key[T]) (T, bool) {
	return initcap.Get(opts.capabilities, key)
}

// ModuleCtor 负责创建模块实例（不承担装配副作用）。
//
// 约定：
// - Providers 注册应在 module.Init 阶段完成；
// - ctor 可用于读取模块配置并创建 module（例如注入静态配置、feature flags 等）。
type ModuleCtor func() (IModule, error)

// BuildModules 依次执行模块构造函数，并保留成功创建的模块实例。
//
// 构造阶段只负责拿到模块对象；真正的 provider 注册、路由挂载和后台启动仍分别
// 由后续的 Init/Start 阶段处理。
func BuildModules(ctors ...ModuleCtor) ([]IModule, error) {
	if len(ctors) == 0 {
		return nil, nil
	}

	modules := make([]IModule, 0, len(ctors))
	for i, ctor := range ctors {
		if ctor == nil {
			continue
		}
		m, err := ctor()
		if err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "failed to build module").WithContext("index", i)
		}
		if m != nil {
			modules = append(modules, m)
		}
	}
	return modules, nil
}

// BaseModule 提供 IModule 的最小默认实现，便于具体模块按需覆写。
type BaseModule struct {
	ModuleID   string
	ModuleName string
}

// NewBaseModule 创建仅携带 ID/Name 的基础模块骨架。
func NewBaseModule(id string, name string) *BaseModule {
	return &BaseModule{ModuleID: id, ModuleName: name}
}

func (m *BaseModule) ID() string {
	if m == nil {
		return ""
	}
	return m.ModuleID
}

func (m *BaseModule) Name() string {
	if m == nil {
		return ""
	}
	return m.ModuleName
}

// Init 提供默认空实现，允许简单模块只覆写自己关心的阶段。
func (m *BaseModule) Init(_ ModuleInitOptions) error { return nil }

// Start 提供默认空实现，表示该模块没有额外运行期动作。
func (m *BaseModule) Start(_ context.Context) (ModuleStopFunc, error) { return nil, nil }
