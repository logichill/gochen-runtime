# gochen-runtime/api/rest：REST CRUD 路由注册（面向组合根/DI）

`gochen-runtime/api/rest` 的目标很明确：**把一套标准 CRUD（可选 audited 扩展）路由注册到 `httpx.IRouteGroup`**，并尽量在“装配阶段”把缺失能力暴露出来（fail-fast），从而减少业务里的样板代码与运行期错误。

本文档只讲顶层概念与装配入口；相关设计背景与迁移说明见 `gochen` 仓库：

- `docs/architecture/framework-design.md`
- `docs/guides/db-schema-migration-guide.md`

## 1. 顶层概念（读完这一节就够用了）

### 1.1 两阶段模型：装配（Build）→ 注册（Register）

```
组合根/DI
  ├─ Build：把“配置/依赖”装配到一起（可多处叠加）
  └─ Register：把路由挂到 httpx.IRouteGroup（并做 fail-fast 校验）
```

### 1.2 你真正需要记住的入口

| 入口                                           | 用途                                  | 什么时候用                            |
| ---------------------------------------------- | ------------------------------------- | ------------------------------------- |
| `rest.Register(group, app, opts...)`        | 一次性注册 CRUD(+audited) 路由        | 默认推荐（大多数场景）                |
| `builder, err := rest.NewApiBuilder[*User, int64](app, opts...); if err != nil { return err }; return builder.Build(group)` | 分阶段叠加配置后再注册                | 模块化/DI/按环境覆盖配置              |

> `rest.Register(...)` 只是 `NewApiBuilder[T, ID](...).Build(...)` 的便捷封装；两者最终都会走到 `RouteBuilder.Register(...)`。

### 1.3 `ApiBuilder` vs `RouteBuilder`：职责对照

| 名称           | 文件                  | 关注点                                                                                                                  | 产出                                          |
| -------------- | --------------------- | ----------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| `ApiBuilder`   | `api/rest/builder.go` | 装配：`RouteConfig` + `ServiceConfig` + validator + hooks + middleware                                                  | 组装完毕后调用 `RouteBuilder.Register(group)` |
| `RouteBuilder` | `api/rest/router.go`  | 注册：创建 handler 并挂路由；audited 能力闭环 fail-fast；应用 `RouteConfig`（分页/白名单/MaxBodySize/错误与响应包装等） | 路由已注册到 `httpx.IRouteGroup`              |

**middleware 顺序（从外到内）**：
`RouteConfig.HTTP.CORS` → `api/rest` 内置 `AsAPI()` → `ApiBuilder.Middleware(...)` → `RouteConfig.HTTP.Middlewares` → handler。

### 1.4 audited 为什么要“多一些约束”

只要实体实现了 `audited.IAuditedEntity[ID]`，就会启用 audited 扩展端点。为避免“路由挂上了但运行期才发现缺能力”，框架会在装配阶段直接 fail-fast（`RouteBuilder.Register`）：

- audited service 必须具备 audited 能力面（否则 `Build/Register` 返回错误）
- `auditStore` 必须非 nil（否则返回错误）
- `RouteConfig.Audit.OperatorExtractor` 必须配置（写操作必须能拿到 operator）

另外，audited 写操作在运行期会强制要求 operator：若 `OperatorExtractor` 返回空值，会返回 `ValidationError("missing operator")`（见 `RouteBuilder.mustAuditedContext`）。

## 2. 最小示例（推荐写法）

### 2.1 普通 CRUD

```go
import (
	rest "gochen-runtime/api/rest"
	appcrud "gochen/app/crud"
	"gochen/domain/crud"
	"gochen/httpx"
	"gochen/validate"
)

// 说明：
// - group/userRepo/validator 通常由组合根/DI 创建；
// - group 可以来自 `httpx/nethttp.NewServer(nil).Group("/api")` 或业务侧自定义路由实现。

var (
	group     httpx.IRouteGroup
	userRepo  crud.IRepository[*User, int64]
	validator validate.IValidator
)

type User struct {
	ID      int64  `json:"id"`
	Version uint64 `json:"version"`
	Name    string `json:"name"`
}
func (u *User) GetID() int64       { return u.ID }
func (u *User) GetVersion() uint64 { return u.Version }

userApp, err := appcrud.NewApplication(userRepo, &appcrud.Options[*User, int64]{Validator: validator})
if err != nil {
  return err
}

if err := rest.Register[*User, int64](
	group,
	userApp,
	func(b *rest.ApiBuilder[*User, int64]) {
		b.Route(func(cfg *rest.RouteConfig[int64]) {
			cfg.Routing.BasePath = "/users"
			cfg.Body.MaxBodySize = 10 << 20
		})
	},
); err != nil {
  return err
}
```

### 2.1.1 注册 CRUD Hooks

标准 CRUD 扩展点通过 `crud.Options.Hooks` 在组合根构造应用时配置：

```go
userApp, err := appcrud.NewApplication(userRepo, &appcrud.Options[*User, int64]{
	Hooks: &appcrud.Hooks[*User, int64]{
		Create: appcrud.Stage[*User]{
			Before: func(ctx context.Context, user *User) error {
				if strings.TrimSpace(user.Name) == "" {
					return errors.NewCode(errors.Validation, "user name is required")
				}
				return nil
			},
		},
	},
})
if err != nil {
	return err
}
if err := rest.Register[*User, int64](group, userApp); err != nil {
	return err
}
```

`Before`、`After`、`PostCommit` 的调用阶段由 `app/crud` 的 writeflow 固定编排，按 `Create`、`Update`、`Delete` 动作分组；`api/rest` 不修改应用配置。应用构造后再装配安全装饰器，同一应用的 HTTP、Worker、CLI 入口共用这些行为。

### 2.1.2 给 CRUD 列表加 QuerySchema（支持自动推导）

如果你本来就依赖 `filter/sorts/fields` 这套统一结构，但又不想停留在“纯字符串协议”，现在有两种接法：

- 默认接法：CRUD builder 会在 `QuerySchema == nil` 且未手工设置 `AllowedFilterFields / AllowedSortFields / AllowedFields` 时，按实体 struct + `query` tag 自动推导 schema；
- 显式接法：你也可以继续手写 `QuerySchema`，手写结果优先，不会被自动推导覆盖。

先看默认更省样板的接法：

```go
type User struct {
	ID        int64     `json:"id" query:"nofilter,select"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	Age       int       `json:"age" query:"ops=eq|gte,sort,select"`
	CreatedAt time.Time `json:"created_at" query:"ops=gte|lte,sort,select"`
	Version   uint64    `json:"version" query:"-"`
}

if err := rest.Register[*User, int64](
	group,
	userApp,
	func(b *rest.ApiBuilder[*User, int64]) {
		b.Route(func(cfg *rest.RouteConfig[int64]) {
			cfg.Routing.BasePath = "/users"
		})
	},
); err != nil {
	return err
}
```

`query` tag 支持的常用规则：

- `field=` / `name=`：覆盖字段名；
- `ops=`：覆盖默认操作符集合（用 `|` 分隔，如 `ops=eq|gte`）；
- `type=`：覆盖字段类型（如 `type=enum`）；
- `sort` / `nosort`、`select` / `noselect`、`nofilter`：覆盖能力面；
- `layout=` / `time_layout=`：覆盖时间格式；
- 不写字段名时默认用 `snake_case`；如需特殊命名，可设置 `RouteConfig.Query.QuerySchemaInferOptions.FieldNameMapper`。

如果你更想完全手写 schema，也可以继续这样配：

```go
userQuerySchema := dataquery.NewQuerySchema(
	dataquery.StringField("name", dataquery.WithFilterOps(dataquery.FilterOpLike), dataquery.AllowSelect()),
	dataquery.BoolField("active", dataquery.AllowSelect()),
	dataquery.IntField("age", dataquery.WithFilterOps(dataquery.FilterOpEq, dataquery.FilterOpGte), dataquery.AllowSort(), dataquery.AllowSelect()),
	dataquery.TimeField("created_at", dataquery.WithFilterOps(dataquery.FilterOpGte, dataquery.FilterOpLte), dataquery.AllowSort(), dataquery.AllowSelect()),
)

if err := rest.Register[*User, int64](
	group,
	userApp,
	rest.WithQuerySchema[*User, int64](userQuerySchema),
	func(b *rest.ApiBuilder[*User, int64]) {
		b.Route(func(cfg *rest.RouteConfig[int64]) {
			cfg.Routing.BasePath = "/users"
		})
	},
); err != nil {
	return err
}
```

这样 API 层会前置获得这些能力：

- `filter=active:eq:TRUE` 会规范化成布尔值语义；
- `filter=active:like:TRUE` 会直接返回 400，因为 `bool` 字段不支持 `like`；
- `sorts=created_at:desc` 只有在字段声明了 `AllowSort()` 时才允许；
- `fields=name,age` 只有在字段声明了 `AllowSelect()` 时才允许。
- 解析出的过滤条件会在 adapter 层立即收口成 `QueryFilters`；默认 `db/orm/repo` 会优先把它们作为 `bool/int64/time.Time/...` 真实类型传给底层 SQL builder，而不是退回字符串比较。
- 自动推导或 `rest.WithQuerySchema(...)` 都会在 `AllowedFilterFields / AllowedSortFields / AllowedFields` 为空时自动派生默认白名单；如需进一步收窄，仍可在 `RouteConfig` 中手工覆盖。
- 如果你已经手工设置了 `Allowed*`，但仍想启用自动推导，可显式设置 `RouteConfig.Query.QuerySchemaInferOptions`。

完整可运行示例见：`examples/domain/query/inferred/main.go`；业务自定义 filter 绑定示例见：`examples/domain/query/filter/main.go`

### 2.1.3 手写路由也能复用同一套 DSL

如果某个接口不是直接走 CRUD builder，而是手写 handler，也可以复用同一套解析协议：

```go
cfg := rest.DefaultRouteConfig[int64]()
cfg.Query.DefaultPageSize = 20
cfg.Query.MaxPageSize = 100
cfg.Query.QuerySchema = userQuerySchema

opts, err := rest.ParsePaginationOptions(ctx, cfg)
if err != nil {
	return err
}
```

这样手写分页路由也能和 CRUD builder 共享同一套 `page/size/filter/sorts/fields` 查询协议，解析结果统一放入 `PaginationOptions`。只需要非分页查询时，使用 `ParseQueryParams` 获取 `QueryRequest`。

### 2.2 Audited CRUD（实体实现 `audited.IAuditedEntity[int64]`）

audited 的关键点是：**写操作必须能拿到 operator**，并在装配阶段 fail-fast。

```go
import (
	rest "gochen-runtime/api/rest"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/httpx"
	"gochen/validate"
)

var (
	group     httpx.IRouteGroup
	repo      crud.IRepository[*User, int64]
	validator validate.IValidator
	auditStore audited.IAuditStore
)

// auditedApp 要求 repo 支持事务运行器（app/crud.ITransactional / WithinTx），audited 应用服务会在同一事务边界内编排业务写与审计写；
// 具体“审计落库是否与业务写同库同事务”取决于 auditStore 的实现。
auditedApp, err := appaudited.NewApplication(repo, auditStore, &appcrud.Options[*User, int64]{Validator: validator})
if err != nil {
  return err
}

if err := rest.Register[*User, int64](
	group,
	auditedApp,
	func(b *rest.ApiBuilder[*User, int64]) {
		b.Route(func(cfg *rest.RouteConfig[int64]) {
			cfg.Routing.BasePath = "/users"
			cfg.Audit.OperatorExtractor = func(c httpx.IContext) (string, bool) {
				// 由鉴权中间件写入（示例）
				if v, ok := c.Get("operator"); ok {
					if op, ok := v.(string); ok && op != "" {
						return op, true
					}
				}
				return "", false
			}
		})
	},
); err != nil {
  return err
}
```

## 3. 路由形态（生成哪些端点）

### 3.1 基础 CRUD

- `GET    {BasePath}`：列表（支持分页/排序/过滤/字段选择）
- `GET    {BasePath}/:id`：详情
- `POST   {BasePath}`：创建
- `PUT    {BasePath}/:id`：更新
- `DELETE {BasePath}/:id`：删除

基础 CRUD 路由可通过 `RouteConfig.Routing.EnableList/Get/Create/Update/Delete` 分别开关；关闭某个路由后，`RouteBuilder` 不再注册该路由的 handler，但 service 仍须满足 `IApplication` 完整接口。

列表查询参数（与实现保持一致）：

- `page` / `size`：分页（仅 `Query.EnablePagination=true` 时生效）
- 约定：`page/size` 必须为正整数，否则返回 400
- 分页路由同时支持 `size` 和 `page_size` 两种参数名；二者同时出现时以 `size` 为准。非分页路由会忽略这两个参数
- `filter`：可重复，语法 `filter=<field>:<op>:<value>`（一元操作符 `is_null/not_null` 允许省略 value）
- `sorts`：`sorts=field[:asc|desc],field2[:asc|desc]`（同字段多次出现以最后一次为准；`asc/desc` 之外会返回 400）
- `fields`：`fields=f1,f2,f3`（字段去重，保序）

白名单行为（breaking change）：

- 若配置 `AllowedSortFields`，请求中出现不在白名单的 sort field 会返回 400（不再静默忽略）。
- 若配置 `AllowedFields`，请求中出现不在白名单的字段选择会返回 400（不再静默忽略）。

支持的 filter op：

- `eq` / `ne` / `like`
- `gt` / `gte` / `lt` / `lte`（需配置 QuerySchema；无 schema 时返回 400）
- `in` / `not_in`（value 为逗号分隔列表）
- `is_null` / `not_null`

示例：

- `GET /users?filter=age:eq:18&filter=status:eq:active`
- `GET /users?sorts=created_at:desc,name:asc`
- `GET /users?fields=id,name,email`
- `GET /users?page=1&size=20&filter=role:in:admin,user&sorts=created_at:desc`

过滤组合逻辑：

- `api/rest` 只负责解析并把 `Filters` 透传给仓储；最终语义由仓储实现决定；
- 默认 ORM 仓储（`db/orm/repo`）会逐条追加 `WHERE ...`，等价于 **AND**；当前 HTTP 层不提供 OR 表达式语法。

### 3.2 批量（`RouteConfig.Routing.EnableBatch=true`）

- `POST   {BasePath}/batch`
- `PUT    {BasePath}/batch`
- `DELETE {BasePath}/batch`

批量校验语义：

- `POST/PUT /batch` 的单条实体校验错误会保留原错误码，并在错误上下文中追加 `index`；
- audited `PUT /batch` 与单条更新一致，必须携带并匹配当前持久化 `version`；
- `DELETE /batch` 要求 `ids` 非空，空数组或缺失会返回 `InvalidInput`。

### 3.3 Audited 扩展（实体实现 `audited.IAuditedEntity[int64]` 时默认启用）

- `GET    {BasePath}/deleted`
- `GET    {BasePath}/:id/audit`（支持 `page/size`）
- `POST   {BasePath}/:id/restore`
- `DELETE {BasePath}/:id/purge`（仅在应用的 `HasPurgeCapability()` 返回 true 时注册；标准 audited 应用根据底层仓储的 `crud.IPurgeRepository` 能力报告，安全包装保留此结果）

audited 写操作约定：

- `PUT {BasePath}/:id` 必须携带 `version`；且不允许通过该接口更新 `created_* / updated_* / deleted_*` 字段（避免绕过审计与软删语义）

## 4. 配置项说明（只列“确实生效”的关键项）

### 4.1 `RouteConfig`

定义见：`api/rest/config.go`。`RouteConfig` 按关注点分组：

- `Routing.BasePath`：路由前缀（可选；默认空路径，注册到当前 group 根路径）
- `Routing.IDCodec`：用于解析 `:id` 参数并复用统一 Bind/Scan 语义；默认会尝试按 ID 底层类型（`int64/string`）自动装配；否则需显式提供（否则 `Build/Register` fail-fast）
- `Routing.EnableList` / `Routing.EnableGet` / `Routing.EnableCreate` / `Routing.EnableUpdate` / `Routing.EnableDelete`：是否注册对应基础 CRUD 路由（默认：true）；关闭后不注册对应路由
- `Routing.EnableBatch`：是否注册 batch 路由（默认：false；批量路由依赖应用的批量写能力，需通过 `WithBatchOperations(n)` 或显式配置开启）
- `Query.EnablePagination`：是否允许 `page/size` 分页（默认：true）
- `Query.DefaultPageSize` / `Query.MaxPageSize`：默认分页大小与最大分页大小；API 层会裁剪 size，application 层也会用 `ServiceConfig.MaxPageSize` 做保护
- `Query.AllowedFilterFields` / `Query.AllowedSortFields` / `Query.AllowedFields`：query 白名单
- `Query.QuerySchema`：可选的动态查询字段 schema；可声明字段类型、允许的 filter op、是否允许排序/字段投影，并在 API 层前置做值校验与规范化
- `Body.MaxBodySize` / `Body.Validator`：请求体大小限制与 API 层请求体验证。`MaxBodySize > 0` 使用该端点限制；`<= 0` 表示未配置或无效值，并由 `httpx` 默认 10MB 上限兜底。
- `Body.MaxBatchSize`：本组 HTTP 路由的批量元素数上限，默认 1000；无效值使用默认值。`WithBatchOperations(n)` 设置此项，Create/Update/Delete 批量请求均在调用应用前校验。
- `Response.ErrorHandler` / `Response.ResponseWrapper` / `Response.UseHTTP201ForCreate`：统一错误、响应结构与创建状态码
- `HTTP.CORS` / `HTTP.Middlewares`：CORS 配置与路由级 middleware
- `Audit.OperatorExtractor`：audited 写操作 operator 提取（audited 场景 **装配期必填**；写操作时 **运行期必需能提取到非空 operator**）

`api/rest` 不再提供授权器、permission 或 constrained-writer 配置。需要统一的动作/资源授权时，在 Application 组合根使用 `gochen-runtime/security.Profile.Build` 与 `gochen/app/security/*` 装饰器；如需在 HTTP 入口提前拦截明显无权请求，可额外挂载 `gochen-runtime/api/rest/action.FastDeny`。Fast-Deny 只是性能优化，不能替代 Application 层授权。

> middleware 执行顺序：`RouteConfig.HTTP.CORS` → `api/rest` 内置 `AsAPI()` → `ApiBuilder.Middleware(...)` → `RouteConfig.HTTP.Middlewares` → handler。

### 4.2 `crud.ServiceConfig`

定义见 Core `app/crud/config.go`，通过 `crud.Options.Config` 在应用构造时注入。

以下限制作用于应用的所有调用入口：

- `AutoValidate`：是否执行领域不变量校验与注入的 validator
- `MaxBatchSize`：批量上限
- `MaxPageSize`：分页上限（crud 层也会使用）

`ServiceConfig` 已收敛为以上 3 个字段；事务/缓存/软删等策略属于上层组合根或业务自定义能力，框架不会通过“某个开关”隐式帮你实现。

REST 的批量与分页限制不能放宽应用的业务限制。应用可以复用于多组路由，各组分别配置 HTTP 限制。Builder 不提供 Service/Hooks/Validator 注入；HTTP payload 专属校验使用 `WithCustomValidator`。构造 API 的迁移方式见 Core `app/README.md`。

## 5. 关于 CORS（避免误解）

`RouteConfig.HTTP.CORS` 默认是非 nil 空配置；由于 `AllowOrigins` 为空，路由级 CORS 仍是 no-op，不会自动开放跨域。需要路由级 CORS 时，使用 `rest.WithCORS(...)` 或显式设置 `RouteConfig.HTTP.CORS.AllowOrigins`；需要全局 CORS 时，继续优先在 HTTP Server 层注册中间件。

路由级 CORS 的自动 `OPTIONS` preflight 只枚举 `RouteBuilder` 注册的标准 CRUD / batch / audited 模板路由。通过业务代码直接注册的自定义路由（例如额外的 `PATCH`）应自行注册对应 `OPTIONS`，或放到全局 CORS 中间件统一处理。

## 6. 错误处理与 fail-fast

`Build/Register` 可能返回的典型错误（便于在组合根阶段尽早暴露装配问题）：

- `service` 为 nil
- `RouteConfig.Routing.IDCodec` 为 nil 且无法自动装配
- audited 实体但未配置 `RouteConfig.Audit.OperatorExtractor`
- audited 实体但 service 不具备 audited 能力面（不支持 Restore/Purge/AuditTrail 等）
- audited 实体但 `AuditStore()` 返回 nil

## 7. 常见问题（FAQ）

### 7.1 如何禁用某个 CRUD 操作（例如不允许 Delete）？

通过 `RouteConfig.Routing.EnableList/Get/Create/Update/Delete` 可分别关闭模板 CRUD 路由。关闭某个路由后，`RouteBuilder` 不再注册该路由的 handler，但 service 仍须满足 `IApplication` 完整接口。

如需在禁用模板路由后保留定制行为，可手动 `group.GET/POST/...` 注册需要的路由。

### 7.2 audited 写操作拿不到 operator 会怎样？

写操作会返回 400（ValidationError：`missing operator`），同时不会调用下游 service（见 `RouteBuilder.mustAuditedContext`）。

### 7.3 如何自定义单个端点的行为？

模板路由的 handler 是框架内部实现，无法“替换其中某一个 handler”。如果需要对某个端点做差异化行为，建议直接手工注册该端点，或在 application/service 层实现差异化用例并从 handler 调用。

## 8. 示例工程

- 普通 CRUD：`examples/domain/crud/main.go`
- audited CRUD：`examples/domain/audited/main.go`
