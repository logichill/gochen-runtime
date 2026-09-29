# host：应用启动与模块装配入口

`gochen-runtime/host` 是下游项目的标准入口，负责收敛模块声明、模块化 Host、生命周期 Engine 与运行期装配。常规项目只需要理解两件事：

- `host.Module(...)`：声明一个模块有哪些 provider、路由、事件处理器、投影、后台组件与权限目录。
- `host.Run(...)` / `host.New(...)`：启动一组模块，并统一接入 HTTP、DI、事件、投影、消息与生命周期治理。

低层实现仍按职责分包维护；除非在写框架扩展或自定义 Host，否则不要从业务代码直接依赖这些包：

- `host/engine`：生命周期编排实现。
- `host/module`：模块作者可见的基础契约、模块目录与 `ModuleInitOptions`。
- `host/module/runtime`：默认模块化 Host 实现，主要由根 `host` 包封装调用。
- `host/module/assembly`：`host.Module(...)` builder 背后的模块装配实现。
- `host/module/runtimecap`：模块初始化阶段的 HTTP、authz、event、projection 等运行时能力 accessor，仅用于高级模块集成、adapter 或框架扩展。
- `host/module/initcap`：`runtimecap` 等高级包使用的 typed key/value 存储，不作为业务模块入口。

## 推荐写法

模块声明：

```go
func NewModule() (host.IModule, error) {
	return host.Module("iam").
		Name("IAM").
		Provide(
			NewUserRepository,
			NewUserService,
		).
		RouteRegistrar(NewUserRoutes).
		PermissionDefinitions(PermissionDefinitions()...).
		Build()
}
```

应用启动：

```go
func main() {
	if err := host.Run(context.Background(),
		hostconfig.WithName("erp"),
		hostconfig.WithModules(
			iam.NewModule,
			inventory.NewModule,
			order.NewModule,
		),
		hostconfig.WithHTTPServer(httpServer),
		hostconfig.WithBasePath("/api/v1"),
		hostconfig.WithFailFastOnRouteConflicts(true),
	); err != nil {
		os.Exit(1)
	}
}
```

模块未配置 `WithModuleHTTP` 时也会按模块 ID 自动挂载到 `/<moduleID>`；只有需要中间件、禁用路由、根挂载或自定义前缀时才配置它。`Prefix: "/"` 表示显式根挂载。

跨模块的初始化能力和 HTTP 默认中间件应在 Host 组合根统一注入：

```go
hostconfig.WithModuleCapabilities(provider.ModuleCapability(config)),
hostconfig.WithModuleHTTPDefaults(hostconfig.ModuleHTTPConfig{
	Middlewares: moduleMiddlewares,
}),
```

模块只读取自己声明的 capability；Host 会把通用配置传给每个模块，并为未覆盖的模块沿用 ID 前缀。

## 生命周期

`host.Run(ctx, ...)` 的固定顺序：

1. 加载 Host 配置。
2. 准备 DI、HTTP、EventBus、Transport、ProjectionRuntime 等运行时能力。
3. 构建模块并按依赖拓扑排序。
4. 执行模块 `Init`。
5. 注册模块路由并启动后台组件。
6. 阻塞运行主 HTTP 服务。
7. 收到退出信号后逆序停止模块并关闭外围资源。

模块投影注册后，Host 会先调用 `StartProjection`，再在 manager 提供该能力时调用 `ResumeFromCheckpoint` 主动追赶持久化 backlog；启动失败会按既有清理顺序停止已注册投影。

Host 仅停止由自身启动取得的 Transport；可观测到启动前已运行的实例仍由原所有者负责停止。模块或 Transport 清理失败时，Host 保留待清理资源供 `Shutdown` 重试，清理完成前拒绝再次启动。

## 约束

- 组合根仍是依赖唯一真相源；`host.Run` / `host.New` 接收 `gochen-runtime/host/config` 的 `Option`，HTTP server、transport、event bus、projection runtime 等基础设施通过 `hostconfig.WithXxx(...)` 注入。
- 模块内部不要把 DI 容器当 Service Locator 使用；provider 由 `host.Module(...).Provide(...)` 声明。
- 普通业务模块不要直接构造 descriptor、registration、role；确需读取运行时能力时，通过 `host/module/runtimecap` 的 typed accessor 读取。
- 自定义生命周期应用可使用 `host.NewEngine(app, ...)`；这是高级入口，模块化应用优先使用 `host.Run(...)`。
