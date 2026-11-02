# gochen-runtime

`gochen-runtime` 是 Gochen 框架的**官方默认物理驱动与 Host 标准运行时**。

无需配置文件即可启动模块 HTTP 服务：

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

导入 `gochen-runtime/quick`、`gochen/httpx` 及标准库 `context`、`log`。默认路径为 `/api/v1/hello/ping`，端口 8080。使用 `app.Group(prefix).Use(middlewares...)` 为模块共享前缀和中间件，只有特殊路径才用 `Mount(prefix, ctor)`。完整可运行示例：`go run ./examples/quick/modules`。

基于 Go 标准库打造，零重型第三方依赖（Zero Bloat），为企业级应用提供一等公民的生产级运行时闭环。

---

## 架构体系与技术选型

在新一代 Gochen 体系中，三层架构定位如下：

```
┌─────────────────────────────────────────────────────────────┐
│ 1. gochen (Core 契约)                                       │
│    - 领域建模、用例编排、事件溯源、总线与端口抽象 (零外部依赖)       │
└──────────────────────────────┬──────────────────────────────┘
                               │
         ┌─────────────────────┴─────────────────────┐
         ▼                                           ▼
┌──────────────────────────────┐            ┌──────────────────────────────┐
│ 2. gochen-runtime (标准运行时)│            │ 3. gochen-contrib (生态外设)  │
│ - 官方默认、一等公民生产运行时  │            │ - 满足非标准库技术栈选型需求 │
│ - 纯 Go 标准库打造 (Zero Bloat)│            │ - 按需引入第三方外设驱动     │
│ - host, nethttp, sql, migrate│            │ - gin, gorm, redis, otel     │
└──────────────────────────────┘            └──────────────────────────────┘
```

* **官方默认生产栈（推荐）**：使用 `gochen` + `gochen-runtime`。由 `host` 提供生命周期与类型安全 DI，`http/nethttp` 提供 HTTP 服务，`db/orm/lite` + `db/orm/repo` 提供持久化与 SQL 迁移，享受纯标准库的轻量与稳定性。
* **第三方生态扩展栈（按需）**：若业务必须采用特定开源外设（Gin、GORM、Redis 分布式锁、OpenTelemetry 导出、Prometheus Client），可按需从 `gochen-contrib` 仓库引入对应驱动。

---

## 模块结构

- `host/`：Host 模块容器、生命周期引擎与模块化装配
- `di/`：类型安全的依赖注入（DI）容器
- `api/rest/`：基于契约的自动化 REST CRUD 路由生成器
- `http/`：基于 Go 标准库 `net/http` 的 HTTP 服务实现、中间件与请求上下文
- `db/`：SQL Builder、Lite ORM、Repo 仓储、Schema 迁移与差异比对
- `auth/`、`host/authz/`：SQL 权限存储、权限目录、动作检查与参数化范围解析
- `config/`：多源配置加载与校验
- `process/`：Workflow SQL 存储与分布式锁实现
- `eventing/`：SQL Event Store、Outbox 与 Projection 引擎
- `quick/`：极简快速装配门面（Quick Host / Rest / Repo）
- `examples/`：端到端与驱动层可运行示例

资源仓储通过 `db/orm/repo.WithResourceContract` 配置维度能力，默认保护全部维度；普通更新落实版本、保护值与授权条件。Transfer 在同源事务中执行条件更新和实际后像回读。`AuditResource` 将资源契约绑定到完整 JSON 快照、类型化维度列和隔离列，AuditStore 构造校验真实表列。外部迁移使用 `api/rest.Transfer`，要求幂等键及调用方版本，见 [REST 接入](api/rest/README.md)。
