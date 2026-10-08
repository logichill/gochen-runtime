# AGENTS 准则（Gochen Runtime）

本仓库为 Gochen Runtime（module `gochen-runtime`），是 Core 契约的物理驱动层；局部约定见本文件与 `api/rest/.agents/CONTEXT.md`。

- `../gochen`（Core，module `gochen`）：领域、应用、认证、事件、消息、策略、流程与 HTTP 抽象
- 开发规范 `SPEC.md`、文档门户 `docs/` 与架构设计记录统一位于 `gochen` 仓库，**本仓库同样受其约束**

## 通用准则

- Go 项目：遵守 Go 编码规范。
- 重构不保留兼容层代码，保持项目的干净清爽。
- 执行代码创建/修改后要执行 go fmt
- 默认验证：本仓库内执行 `go build ./... && go vet ./... && go test -count=1 ./...`（sqlite 为纯内存测试驱动）；全量入口见 `Makefile`（`make build && make vet && make test && make examples-smoke`）。禁止退化为 workspace 级一把梭，否则会掩盖 Core 的零依赖不变量。

## 命名铁律

- **repo 名 = module path = 目录名**，三者必须一致（当前：`gochen` / `gochen-runtime`）。
- module path **禁止使用 Go 标准库顶级包名**；新增 module 前核对 `go list std | grep -x '<候选名>'` 为空。本模块曾名为 `runtime`，`runtime/{debug,metrics,pprof,trace,cgo,race,coverage}` 7 个子包名因此永久不可用（`ambiguous import` 硬编译失败），`runtime/di` 还会被工具与人眼误判为标准库。
- 本仓库内**不建议新建**与标准库 `runtime` 子包同名的顶级目录（`debug`/`metrics`/`pprof`/`trace` 等）：改名后已无编译冲突，但语义模糊，应取更具体的命名。

## 模块边界与依赖

- 依赖硬规则：只允许 `gochen-runtime → gochen` 单向依赖，禁止任何反向引用。Core 任何文件（含测试与示例）禁止 import `gochen-runtime`；若发现 Core 需要本仓库能力，应回到 Core 定义接口，由本仓库提供实现。
- **落在 Runtime 的职责**：SQL 物理持久化、配置加载、net/http server、Host/DI 容器、REST 路由注册、SQL 治理存储与权限目录。
- **不得落在 Runtime 的职责**：领域建模、用例编排契约、安全语义定义、错误模型、上下文语义——这些属于 Core。
- 第三方依赖仅限物理驱动所需（yaml/sqlite）。**当前生产代码第三方依赖只有 `gopkg.in/yaml.v3`（1 个文件）；sqlite 为纯测试依赖**。新增生产第三方依赖必须先说明为何标准库不可行。
- 开发态由 workspace 级 `go.work`（位于两仓库父目录）组合，并以 `replace gochen => ../gochen` 兜底；**发布态必须改为真实 Core 版本，不依赖本地 replace**。

## 顶级目录

`api auth config db di eventing host http internal process quick security examples`

新增顶级目录必须先说明架构理由。`examples` 仅存放示例 `main` 包，生产代码禁止 import。

## 架构约定

1. **Host 模块契约**：canonical API 为 `host/module`；`host/module/runtime` 只提供 Host 实现，不重复转出模块类型和构造函数。
2. **Quick 门面**：`quick` 是唯一的便捷装配门面（Host 启动、REST 注册、Service、Repo 工厂），底层子包保留复杂场景的显式定制通道。装配与列名约定见 `gochen` 仓库 `docs/guides/quick-assembly.md`。
3. **安全能力落点**：Application 层用装饰器（`security`）；Repository 层一律用**构造期显式选项**（`db/orm/repo.WithIsolation` / `WithResourceKind` / `WithScope`）表达安全约束，禁止包装 `IRepository`——否则可选能力接口会丢失或被假冒。
4. **HTTP middleware**：路径跳过规则与响应状态解析由包内共享 primitive 统一，相关 capability interface 不作为公共 API 暴露。
5. **SQL migration**：分句与人工 review guard 复用本仓库 `db/sqlscan` 词法原语；本仓库仅保留 migration 特有的 `#` 行注释规则。

## 下游项目

- ../gochen-contrib（第三方生态适配器：gin/gorm/redis/otel/prometheus；接入指南见 `gochen` 仓库 `docs/guides/contrib-guide.md`）
- ../gochen-iam、../gochen-llm、../gochen-workflow
- ../alife、../ems、../erp
