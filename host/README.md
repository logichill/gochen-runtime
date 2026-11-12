# host：模块容器与生命周期入口

`gochen-runtime/host` 是 Gochen 应用的组合根。它把模块声明、DI 容器、HTTP 服务、事件总线、消息传输、投影运行时和模块目录收敛到一条固定的启动/停止链路中。

常规业务代码使用三个入口：

- `host.Module(...)`：声明模块的 provider、路由、事件处理器、投影、后台组件和目录扩展。
- `host.Run(...)`：创建标准模块化 Host 并执行完整生命周期。
- `host.New(...)`：只创建生命周期引擎，由调用方决定何时 `Start`。

## 1. 功能定义

| 入口 | 职责 |
| --- | --- |
| `host.Module(id)` | 创建模块构造器；链式声明后调用 `Build()` 得到 `IModule` |
| `host.Run(ctx, opts...)` | 装配模块化 Host，执行 `Engine.Start` |
| `host.New(opts...)` | 创建标准模块化 Host 的生命周期引擎 |
| `host.NewEngine(app, opts...)` | 为非模块化应用提供自定义 `engine.IApp` 的生命周期引擎 |
| `host.NewModuleRegistry()` | 创建模块目录注册表 |
| `host.NewMetadataRegistry()` | 创建事件溯源聚合 metadata 注册表 |
| `module.IApplicationRegistry` | Host 实例内的启动期应用登记窄端口，运行前冻结 |

`host.Module(...)` 的声明项：

| 声明 | 作用 |
| --- | --- |
| `.Application(instances...)` | 登记 Core `app.IDescriptorProvider`，包含非 HTTP 应用 |
| `.DependsOn(ids...)` | 声明模块启动依赖，不隐式注入对象 |
| `.PublicFeatures(values...)` | 声明可公开的部署特性，不得包含秘密 |
| `.Provide(fns...)` | 注册单例 provider；返回值类型即 DI 服务类型 |
| `.RouteRegistrar(instances...)` | 注册实现 `RegisterRoutes(httpx.IRouteGroup) error` 的路由注册器 |
| `.EventHandler(instances...)` | 注册 `messaging.IMessageHandler`，在模块 `Start` 时订阅 |
| `.Projection(module.ProjectionOf[ID](instance))` | 注册投影，在模块 `Start` 时注册、启动并追赶 checkpoint |
| `.RuntimeComponent(instances...)` | 注册实现 `capability.IRuntimeComponent` 的后台组件 |
| `.Aggregate(...)` | 声明事件溯源聚合；装配期校验，`Init` 阶段写入 metadata registry |
| `.Extension(...)` | 携带目录扩展载荷，交给 `IModuleCatalogRegistrar` 解释 |
| `.Middleware(...)` | 为模块 HTTP 路由挂载中间件 |
| `.OnStart(...)` / `.OnStop(...)` | 模块启动/停止钩子 |

## 2. 目录与依赖关系

| 子包 | 职责 |
| --- | --- |
| `host` | 对外门面：Builder、Run/New、启动期清单 |
| `host/engine` | 生命周期状态机与 `IApp` 五阶段编排 |
| `host/config` | Host、模块运行时与 Engine 共享的配置模型和 Option |
| `host/module` | 模块作者契约：`IModule`、`ModuleInitOptions`、模块目录 |
| `host/internal/assembly` | `host.Module(...)` 背后的显式模块装配与角色分类 |
| `host/module/runtime` | 默认模块化 Host 实现，由根 `host` 包创建 |
| `host/module/runtimecap` | 模块初始化阶段的 HTTP、事件、投影、传输能力 accessor |
| `host/module/initcap` | `runtimecap` 等高级包使用的 typed key/value 存储 |
| `host/capability` | 模块运行期需要的订阅、传输、投影、目录与组件接口 |
| `host/internal/bootstrap` | HTTP、消息、投影等外围运行时的默认装配 |
| `host/authz` | 可选权限目录扩展：Catalog、Registry、权限校验 |

依赖方向：

```text
业务 main / quick
        │
        ▼
      host
        │
        ├── host/engine ──> host/config ──> di/basic、http、gochen 核心
        ├── host/module/runtime ──> host/internal/bootstrap
        │                              ├── http/nethttp、http/middleware
        │                              └── eventing/monitoring
        ├── host/internal/assembly ──> host/capability
        ├── host/module/runtimecap ──> host/module/initcap
        └── host/authz（可选）
```

边界：

- 业务模块依赖 `host`、`gochen/httpx`，需要权限目录时再依赖 `host/authz`。
- `host` 依赖 Runtime 的 `di`、`http`、`eventing/monitoring` 与 Core 的 `httpx`、`messaging`、`projection`、`eventsourced`。
- `host` 向模块注入能力；`api/rest` 通过 `RouteRegistrar` 接入，`quick` 通过 `host/config` 与 `host/module/runtime` 创建 Host。
- `host/config` 是 Host 启动配置模型；顶层 `gochen-runtime/config` 是多源配置加载组件，两者职责独立。

## 3. 启动链路

```text
main
 ├─ host.Run(ctx, opts...)
 │    ├─ host.New → config.Build → runtime.NewHostConfig → engine.NewEngineConfig
 │    └─ engine.Start
 └─ quick.New(...).Modules(...).Run(ctx)
      └─ quick.Build → engine.NewEngineConfig(runtime.NewHostConfig(cfg), cfg)
           └─ engine.Start
```

`Engine.Start` 的固定阶段：

| 阶段 | Engine 动作 | Host 动作 |
| --- | --- | --- |
| Init | `OnBeforeInit` → `LoadConfig` → `OnAfterInit` | 规范化配置并检查重复装配 |
| Prepare | 在 `StartupTimeout` 内调用 `Prepare` | 装配外围运行时、构建模块、DI 注册、目录登记 |
| StartBackground | `OnBeforeStart` → 启动后台 → 启动 Run goroutine → `OnAfterStart` | 挂载路由、启动模块、启动 Transport |
| Run | 等待 `Run` 返回或退出信号 | 启动主 HTTP 服务并阻塞 |
| Shutdown | 在 `ShutdownTimeout` 内执行停止钩子与 `Shutdown` | 停 HTTP、逆序停模块、停自有 Transport |

### 3.1 Prepare：装配与初始化

`Host.Prepare` 的顺序固定：

1. `ensureFrameworkRegistries` 把 `IModuleRegistry`、`IMetadataRegistry` 注入 DI 容器。
2. `bootstrap.Prepare` 装配外围运行时。
3. `module.BuildModules` 执行模块构造函数。
4. 校验模块 ID、规范化模块 HTTP 配置键、按 `DependsOn` 拓扑排序。
5. 逐模块调用 `Init`。
6. 写入模块及应用目录（包括非 HTTP 应用），检查资源动作冲突，并回调已注入的目录登记器。

`bootstrap.Prepare` 的内部顺序：

1. 解析或注入 `EventBus`、`Transport`，登记消息能力。
2. 使用注入的 `httpx.IServer`，或创建 `nethttp.Server`；启用冲突检查时包装 `httpx.WithRouteRegistry`。
3. 创建 `BasePath` 路由组，挂载安全分层、Host 级中间件与健康/监控路由。
4. 解析或注入投影运行时，登记 `IProjectionRuntime` 与 `projection.IProjectionRegistrar`。

模块 `Init` 的作用：

- 仅将 `.Provide` 的高级服务注册写入 DI 容器；角色实例直接保存在模块声明中，同类型不同实例合法。
- 按角色分类，保存给后续 `RegisterRoutes` 与 `Start` 使用。
- 执行 `.Aggregate(...)` 的 metadata 注册钩子。
- 通过 `ModuleInitOptions` 读取 Host 注入的 HTTP、事件、投影、传输能力。

### 3.2 StartBackground：路由、模块与消息

`Host.StartBackground` 的顺序固定：

1. 使用 `di.Diagnose` 检查 DI 循环依赖，固定并核对后台组件的唯一生命周期所有权。
2. `registerModuleRoutes` 挂载所有 `IRouteModule` 的路由；启用冲突检查时在挂载后验证路由表。
3. 冻结应用目录；收集实际 HTTP、消息及后台入口，执行全部 ValidateEntries，然后执行全部 ActivateEntries。
4. 按拓扑顺序逐模块 `Start`。
5. 启动 Transport。

模块 `Start` 的内部顺序：

1. 解析并订阅事件处理器。
2. 注册投影 → 启动投影 → 从 checkpoint 追赶 backlog。
3. 启动 `IRuntimeComponent`。
4. 调用 `.OnStart(...)`。

任一模块启动失败时，Host 逆序停止已启动模块，并保留停止失败的资源供后续重试。Transport 在本阶段启动；Host 只接管本次启动取得所有权的实例。

### 3.3 Run 与 Shutdown

`Host.Run` 构造监听地址并调用 `HTTP server.Start`；`http.ErrServerClosed` 视为正常退出。

`Engine` 收到退出信号或运行错误后进入 `Shutdown`：

1. 停止 HTTP 服务。
2. 按模块注册的逆序调用停止函数。
3. 所有模块停止成功后，停止本 Host 取得的 Transport。
4. 清理失败的资源保留在 Host 中，`Shutdown` 可重试。

模块停止时的内部顺序为：取消事件订阅 → `.OnStop(...)` → 停止运行期组件 → 释放投影注册。

## 4. 模块初始化能力

`ModuleInitOptions` 向模块暴露受限视图：

- `Registry`：仅注册 provider。
- `Resolver`：按类型解析依赖。
- `container`：框架装配层使用的完整容器能力，通过 `module.ResolveModuleContainer` 获取。

常用能力读取：

```go
httpOptions := runtimecap.HTTPFrom(opts)
eventBus := runtimecap.EventBusFrom(opts)
projectionRuntime := runtimecap.ProjectionRuntimeFrom(opts)
transport := runtimecap.TransportFrom(opts)
metadata := runtimecap.MetadataRegistryFrom(opts)
modules := runtimecap.ModuleRegistryFrom(opts)
applications := module.ApplicationRegistryFrom(opts)
```

模块 HTTP 默认挂载到 `/<moduleID>`。`WithModuleHTTP` 可覆盖前缀、追加中间件或关闭模块 HTTP；`WithModuleHTTPDefaults` 设置所有模块的默认前缀与中间件。`Prefix: "/"` 表示当前 BasePath 路由组的根路径，路由不再追加模块 ID 前缀。

## 5. 常用配置

```go
host.Run(ctx,
    hostconfig.WithName("erp"),
    hostconfig.WithModules(iam.NewModule, order.NewModule),
    hostconfig.WithHTTPServer(server),
    hostconfig.WithEventBus(eventBus),
    hostconfig.WithTransport(transport),
    hostconfig.WithProjectionRuntime(projectionRuntime),
    hostconfig.WithCatalogRegistrar(authz.NewCatalogRegistrar(registry)),
    hostconfig.WithBasePath("/api/v1"),
    hostconfig.WithModuleHTTPDefaults(hostconfig.ModuleHTTPConfig{
        Middlewares: moduleMiddlewares,
    }),
    hostconfig.WithFailFastOnRouteConflicts(true),
)
```

默认值：监听 `0.0.0.0:8080`，BasePath 为 `/api/v1`，启动超时 30s，停止超时 10s。

## 6. 示例

- 模块化 Host：`examples/host/module_boot/main.go`
- 无配置模块 HTTP 服务：`examples/quick/modules/main.go`
- 极简装配门面：`quick/README.md`

## 7. 类型化声明与资源归属

Repository、Application、Routes 和 Worker 在 ModuleCtor 中显式创建一次，然后把同一实例提交到对应角色。函数路由使用 `module.RouteRegistrarFunc`。普通角色不接受构造器，也不会按对象类型去重。高级 DI 与自定义 `module.IModule` 仍可显式使用。

```go
return host.Module("notes").
    Application(application).
    RouteRegistrar(module.RouteRegistrarFunc(quick.RESTRegistrar[*Note, string](application))).
    Build()
```

Builder 仅用于单线程声明；Build 复制集合，之后修改 Builder 或原始 slice 不改变已构建模块。Extension 的内容遵守自身不可变或复制契约。应用描述应在应用构造时冻结。完整可运行示例见 `examples/typed-module`。

每个 registrar 使用独立子 group，HTTP 策略按 Host 全局 → quick Group → 模块 Middleware → registrar 局部执行。Mount 只覆盖前缀，不移除继承的策略。

后台组件必须使用非空指针身份；值对象请包装为指针。同一组件在可枚举的模块角色中只能有一个生命周期所有者，跨模块重复会在入口激活前失败。同一对象兼任事件处理器和 Worker 合法，订阅 token 与 Start/Stop 分别释放。投影 ID 与管理器的匹配仍在 Start 注册投影时检查，失败会回滚已取得的资源。

组合根注入的数据库、客户端不被自动关闭。模块仅清理自己启动的 Worker 和取得的注册 token。失败清理保留责任，Shutdown 可重试；存在残留资源时不能再次启动。部分路由注册失败必须重建 Host。
