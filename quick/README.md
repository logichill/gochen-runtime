# quick：便捷装配门面

`gochen-runtime/quick` 把 Host、REST 路由、CRUD Application、ORM 仓储与事件溯源仓储组合成一条单文件启动路径，面向常规服务装配。复杂定制通过 `host/config` 与各子包直接装配。

## 1. 功能定义

| 入口 | 职责 |
| --- | --- |
| `quick.New(opts...)` | 创建应用声明，默认监听 `:8080`、BasePath `/api/v1` |
| `Application.Use(mw...)` | 追加基础 API 中间件 |
| `Application.Group(prefix)` | 创建继承父组中间件的作用域 |
| `Application.Modules(ctors...)` | 注册模块，默认挂载到 `/<moduleID>` |
| `Application.Mount(prefix, ctor)` | 显式指定模块挂载前缀 |
| `Application.Build()` | 固定声明并创建 `engine.Engine` |
| `Application.Run(ctx)` | `Build` 后阻塞运行 |
| `quick.Module(name, registrar, opts...)` | 创建只包含路由注册的轻量模块 |
| `quick.RESTRegistrar(app, opts...)` | 生成 `func(httpx.IRouteGroup) error` |
| `quick.RESTRegister(group, app, opts...)` | 直接向路由组挂载 REST |
| `quick.App(repo, options)` | 创建 CRUD Application |
| `quick.Repo` / `quick.IsolatedRepo` / `quick.SoftDeleteRepo` | 创建默认、租户隔离、软删除过滤仓储 |
| `quick.EventSourced` / `quick.AtomicEventSourced` | 创建事件溯源仓储 |

## 2. 启动链路

```text
quick.New
  └─ Use / Group / Modules / Mount
       └─ Build
            ├─ config.Build(options...)
            ├─ root 中间件写入 RouteMiddlewares
            ├─ 每个 mount 包装为 module.ModuleCtor
            └─ engine.NewEngineConfig(runtime.NewHostConfig(cfg), cfg)
                 └─ Engine.Start → Host.LoadConfig → Host.Prepare
                      └─ Prepare 执行包装后的 ModuleCtor
                           ├─ 调用原始 ctor 得到模块
                           ├─ 计算模块 HTTP 前缀
                           └─ 写入 cfg.ModuleHTTP
```

关键时序：

- `New`、`Use`、`Group`、`Modules`、`Mount` 只记录声明。
- `Build` 计算根中间件与静态配置，创建 Host 与 Engine；模块工厂在 Host `Prepare` 阶段执行。
- 包装后的模块构造函数在 Host `Prepare` 中读取模块 ID，把 `Group` 前缀、显式 `Mount` 前缀、组中间件写入 `cfg.ModuleHTTP`。
- `Engine.Start` 之后的链路与 `host.Run` 完全一致：`LoadConfig` → `Prepare` → `StartBackground` → `Run` → `Shutdown`。

挂载规则：

- `Modules(...)` 在当前组前缀后追加模块 ID。
- `Group(prefix)` 追加路径前缀并继承中间件。
- `Mount(prefix, ...)` 使用显式前缀；`"/"` 表示当前组根路径。
- 模块 HTTP 配置由 `quick` 的挂载声明生成，在 Host `Prepare` 执行包装构造函数时写入；直接使用 `host.Run` 时由调用方通过 `host/config` 管理。

## 3. 依赖关系

- 上游：业务 `main` 包。
- Runtime：`host/config`、`host/engine`、`host/module`、`host/module/runtime`、`api/rest`、`db/orm/repo`。
- Core：`app/crud`、`app/eventsourced`、`domain`、`httpx`。
- `quick` 负责组合与默认值；HTTP 物理实现、DI 容器和生命周期由 Host 子包提供。

## 4. 示例

```go
app := quick.New()
app.Modules(quick.Module("hello", func(g httpx.IRouteGroup) error {
    g.GET("/ping", func(c httpx.IContext) error { return c.String(200, "pong") })
    return nil
}))
if err := app.Run(context.Background()); err != nil {
    log.Fatal(err)
}
```

完整示例：

- 无配置模块服务：`examples/quick/modules/main.go`
- CRUD + 隔离仓储 + REST：`examples/quick/main.go`
- 端到端 HTTP 往返：`quick/quick_test.go`
