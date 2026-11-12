# gochen-runtime/api/rest：按 Descriptor 挂载 REST 路由

`api/rest` 把 `Application.Descriptor()` 声明的动作映射为 HTTP 路由。调用方提供 `rest.ReadApplication`（读接口、Descriptor 与分页上限），写入、批量、审计与生命周期能力按声明显式检查。声明了动作却缺少所需能力时，在注册路由前失败。

授权概念见 `gochen` 仓库 `docs/architecture/layered-authz.md`，字段保护与归属迁移见该仓库 `docs/architecture/resource-contracts.md`。

## 1. 入口

| 入口 | 用途 |
| --- | --- |
| `rest.Mount(group, app, opts...)` | 一次性挂载标准路由；推荐 |
| `builder, err := rest.NewRouteBuilder(app, opts...)` | 需要先构造再 `Register(group)` 时使用 |
| `rest.Resource(app, opts...)` | 将同一应用和标准路由作为一个 Module binding 提交 |
| `rest.Bind(method, path, command, decode, options...)` | 将同一类型化 Command 的执行与全部授权元数据绑定为 HTTP 入口 |
| `rest.Transfer(group, app, runner, path, action, opts...)` | 显式注册带调用方版本与幂等键的 POST 迁移动作 |
| `rest.Describe(app, opts...)` | 只推导 Descriptor 与路由表，不注册任何路由 |
| `quick.RESTRegistrar(app, opts...)` | 生成 `func(httpx.IRouteGroup) error`，供显式 `module.RouteRegistrarFunc` 使用 |

`Mount` 只做这几件事：校验 Descriptor、推导路由、包装中间件与错误映射、可选地登记启动清单。handler 本身只解码请求并调用 Application。

```go
app, err := quick.DeclaredApp[*User, int64](repo,
	crud.Options[*User, int64]{Resource: "user"},
	[]auth.Rule{{Scope: &auth.ScopeRule{Resolver: resolver}}},
	auth.CompileOptions{Checker: checker},
)
if err != nil {
	return err
}

if err := rest.Mount[*User, int64](router, app,
	rest.WithBasePath[*User, int64]("/users"),
	rest.WithEntryRegistry[*User, int64](entries, "users"),
); err != nil {
	return err
}
```

`entries` 由 `host/authz.NewEntryRegistry()` 创建，并通过 `config.WithEntryValidator(entries)` 接入 Host。注册器登记 Application 的实际动作执行计划；Host 从 HTTP 驱动独立枚举路由，补齐隐式 HEAD 和协议 OPTIONS，缺失或无效绑定会阻断业务模块和传输启动。其他手写路由同样需要登记资源执行绑定或明确的公共放行理由。

## 2. 标准路由

Descriptor 中声明了对应动作时才注册：

| 动作 | 方法 | 路径 | 说明 |
| --- | --- | --- | --- |
| `list` | GET | `{BasePath}` | 分页/过滤/排序/字段选择 |
| `list_deleted` | GET | `{BasePath}/deleted` | 需要 `LifecycleApplication` |
| `create` | POST | `{BasePath}` | 创建单个实体 |
| `create` | POST | `{BasePath}/bulk` | 批量创建（`ExecuteNewBulk`） |
| `update` | PUT | `{BasePath}/:id` | 更新单个实体 |
| `update` | PUT | `{BasePath}/bulk` | 批量更新（`ExecuteBulk`） |
| `delete` | DELETE | `{BasePath}/:id` | 删除单个实体 |
| `delete` | DELETE | `{BasePath}/bulk` | 批量删除（`DeleteBulk`） |
| `audit_read` | GET | `{BasePath}/:id/audit` | 需要 `crud.IAuditReader` |
| `restore` | POST | `{BasePath}/:id/restore` | 需要 `LifecycleApplication` |
| `purge` | DELETE | `{BasePath}/:id/purge` | 需要 `LifecycleApplication` |
| `read` | GET | `{BasePath}/:id` | 读取单个实体 |

`BasePath` 为空时直接注册到路由组根路径。批量路由共用单个动作的权限码；一批请求只做一次授权、一个事务，提交前任一元素失败整批回滚。

单条和批量 PUT 必须携带调用方读取时的 `version`，版本 0 合法。缺失、null、负数或溢出返回 HTTP 400（InvalidInput），过期版本返回 HTTP 409（Concurrency）；批量任一冲突整批回滚。仓储显式关闭版本控制时，标准 HTTP 入口仍要求该字段。

单条更新请求示例：

```json
{"name":"新名称","version":0}
```

批量更新中每项同时提供 `id`、业务字段和 `version`。

批量更新保留未提交的 `dim` 字段。显式提交的维度由 Core 有效资源契约判定，允许可修改维度；受保护维度变化使整批回滚。

audited 应用的写入口要求能提取 operator；`Mount` 在装配期检查 `WithOperatorExtractor`，运行期缺少 operator 的请求会被拒绝。审计/软删字段由框架管理。过滤分页通过 `QueryCount` 使用相同条件统计总数。

### 2.1 路由表自省

路由集合是 `(auth.Descriptor, BasePath)` 的纯函数，可以脱离注册单独推导：

```go
desc, specs, err := rest.Describe[*User, int64](app,
	rest.WithBasePath[*User, int64]("/users"),
)
specs = rest.Specs(desc, "/users") // 纯投影，等价于上面的 specs
```

`RouteSpec` 包含 `Op / Method / Path / Action / Permission`，可用于 OpenAPI、冲突检测和路由清单。`Describe` 复用注册校验，`Mount` 根据同一份 `Specs` 挂载实际路由。

注册顺序为：校验应用与配置 → 推导路由 → 挂载中间件与 handler → CORS 预检 → 可选启动清单登记。HTTP 方法由 `http.RegisterRoute` 统一分派，非标准方法返回 `InvalidInput`。

## 3. 业务动作

业务用例使用自己的输入、输出与命名接口；装配端通过 `app.Command` 连接其授权访问能力。HTTP 不再接收另一个动作名或可以任意选择应用动作的 handler：

```go
// approve 已通过 app.NewCommand 绑定资源授权、受约束 Access 与业务函数。
// decodeApprove 只做 HTTP 输入解码：func(httpx.IContext) (ApproveOrder, error)。
httpBinding := rest.Bind("POST", "/:id/approve", approve, decodeApprove)
orderModule, err := host.Module("orders").Binding(httpBinding).Build()

// 后台直接调用同一对象，业务层可以用 OrderApprover.Approve 包装它。
result, err := approve.Execute(ctx, input)
```

完整示例见 [business-actions](../../examples/business-actions/)。`Command` 的全部资源必须先通过授权，再构造访问能力与执行用例；HTTP 与后台都走这条路径。注册元数据来自同一对象的 `AuthorizationBinding`，主资源与附加资源不会分开手工声明。`ModuleBinding` 同时把命令实际使用的资源动作投影进应用清单；不把计划中未调用的动作登记为已提供的用例。

`Bind` 返回类型化 `CommandBinding`，同时实现 `module.IBinding` 与 `module.IRouteRegistrar`。也可以直接调用 `RegisterRoutes(group)`。配置错误由 `Build` 或 `RegisterRoutes` 返回，不执行命令。路由登记与标准 `Mount` 一样并非事务性操作；若登记过程中失败，重试应重建路由组或服务器，避免重复部分已登记的入口。原 `rest.Action` 与 `ActionHandler` 已移除。

可选的一个 `CommandOptions` 值复用 `HTTPOptions`、`ResponseOptions` 和 `AuditOptions`，另有 `MaxBodySize`、`SuccessStatus`。默认以 200 和 `DefaultResponseWrapper(output)` 返回结果，保留标准请求体上限、错误映射与提交后降级标识。只接受带响应体的 2xx JSON 状态。操作人提取派生原请求 context，不替换主体、租户与取消信号；业务授权、版本和审计要求仍在用例内部执行。CORS 配置按标准资源方式生成预检，同一路径的多个入口应由上层统一配置 CORS。

普通 CRUD 资源可以一次绑定，无需重复登记应用与路由：

```go
notes, err := host.Module("notes").
    Binding(rest.Resource(application)).
    Build()
```

绑定只描述显式参与角色，不创建数据库、不运行用例或启动 worker。直接注册到 `IRouteGroup` 的手写路由仍须满足 Host 的独立实际入口检查。

## 4. 归属迁移

`rest.Transfer` 要求应用声明 KindTransfer、ReceiveAction 和 Dimensions，并实现 ITransferApplication。Runner 使用 tracked 模式，存储支持幂等结果和并发预占。目标存在性及接收权限由应用授权器判定。

POST 请求提供 `Idempotency-Key`，body 形如 `{"expected_version":0,"targets":[{"dimension":"department","value":7}]}`。版本 0 合法，缺失或 null 返回 InvalidInput。幂等键绑定主体、租户、资源、动作和实体；相同有效键返回首次 Result.Operation.ID 与结果。过期键产生新操作，继续验证原调用方版本。提交后失败和已存储 degraded 结果均返回 HTTP 200，设置 `X-Execution-Status: degraded`。

REST 与命令入口共用 `operation.Runner.ExecuteTransfer` 的身份绑定和输入校验。Transfer 从事务外开始，已有活动事务中的调用返回 `FailedPrecondition`。完整执行顺序见 `gochen` 仓库 `docs/architecture/resource-contracts.md`。

## 5. 配置项

`RouteConfig` 按关注点分组，常用 Option：

| Option | 说明 |
| --- | --- |
| `WithBasePath(path)` | 资源基础路径 |
| `WithPagination(defaultSize, maxSize)` | 分页默认值与上限；实际上限不超过 Application 的 `MaxPageSize` |
| `WithQuerySchema(schema)` | 动态查询 schema；未提供时按实体类型自动推导 |
| `WithMaxBulkSize(n)` | HTTP 批量元素上限（不能放宽 Application 的业务上限） |
| `WithMaxBodySize(n)` | 请求体大小上限 |
| `WithValidator(fn)` | 协议层请求体校验 |
| `WithOperatorExtractor(fn)` | 提取操作人并写入 `contextx.WithOperator` |
| `WithCORS(origins)` | 路由级 CORS |
| `WithErrorHandler(fn)` | 自定义错误响应 |
| `WithResponseWrapper(fn)` | 成功响应包装 |
| `WithHTTP201ForCreate()` | 创建成功返回 201 |
| `WithPermissionChecker(checker)` | 按 Descriptor 权限码快速拒绝 |
| `WithMiddleware(mw...)` / `WithAuth(mw)` / `WithLogging(mw)` | 追加路由中间件 |
| `WithStartupRegistry(registry)` | `Mount` 时把应用登记进 Host 启动清单 |

`WithPermissionChecker` 只是性能优化：真正的授权始终在 Application 层，删除它不改变安全语义。

## 6. 列表查询协议

- `page` / `size`（`page_size` 作为 `size` 的别名）：分页参数；非法值返回 400。
- `filter=<field>:<op>:<value>`：可重复，支持 `eq/ne/like/gt/gte/lt/lte/in/not_in/is_null/not_null`。
- `sorts=field[:asc|desc],...`：字段去重，方向非法返回 400。
- `fields=f1,f2,...`：字段投影，去重保序。

多个 `filter` 由仓储组合为 AND。过滤、排序和投影遵循 QuerySchema 及字段白名单；越界字段返回 HTTP 400。

## 7. 响应与错误

- 成功响应默认包装为 `httpx.NewSuccessMessage(data)`；可用 `WithResponseWrapper` 覆盖。
- handler 返回错误时交给 `RouteConfig.Response.ErrorHandler`（默认 `DefaultErrorHandler`）。
- 返回 `contextx.WrapAfterCommitError(err)` 表示“主操作已提交、后续步骤失败”：HTTP 返回 200，响应头 `X-Execution-Status: degraded`。

## 8. 装配期校验

`Mount`/`Register` 在注册任何路由前会检查：

- Application 与路由组非 nil；
- Descriptor 合法（资源名非空、动作名非空且不重复）；
- `:id` 路由有可用的 ID codec；
- Descriptor 声明了 `restore/purge/list_deleted` 时，Application 实现 `LifecycleApplication`；
- audited 应用配置了 `OperatorExtractor`；
- CORS 配置合法。

## 9. 示例

- 普通 CRUD：`examples/domain/crud/main.go`
- audited CRUD：`examples/domain/audited/main.go`
- 分层授权：`examples/security/layering/main.go`
- quick 装配与真实 HTTP 往返：`quick/quick_test.go`

## 10. 启动与请求链路

`api/rest` 位于 Host 的路由挂载阶段：

```text
Host 启动 → 模块路由注册 → rest.Mount → 校验并挂载路由
请求进入 → 路由中间件 → handler → Application → Repository
请求结束 → 响应包装或错误处理
```

`rest.Bind` 与标准路由共用错误映射、提交后降级、操作人派生和 CORS 预检；命令的完整授权始终在 `Command.Execute` 执行。

## 11. 依赖关系

| 方向 | 依赖 |
| --- | --- |
| 上游调用方 | `host.Module(...).RouteRegistrar(...)`、`quick.RESTRegistrar(...)` |
| Core 契约 | `app/crud`、`app/audited`、`auth`、`app/query`、`codec/idcodec`、`contextx`、`httpx` |
| Runtime 物理层 | `gochen-runtime/http`（`RegisterRoute` 与路由方法分派）、`http/middleware`、`http/request` |
| 内部工具 | `gochen-runtime/internal/httpheader` |

`api/rest` 不持有 DI 容器和模块生命周期；应用实例、Repository 和授权器由调用方构造并传入。
